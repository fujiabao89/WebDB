package execution

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/fujiabao89/webdb/internal/adapter"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/fujiabao89/webdb/internal/sqlpolicy"
	"github.com/google/uuid"
)

// Pipeline 执行管线：Policy → Credential Resolver → Adapter。
// 安全断言：Policy 拒绝时 Credential Resolver 调用 0 次；
//
//	Credential 失败时 Adapter 调用 0 次。
type Pipeline struct {
	store       ConnectionReader
	policyStore ConnectionPolicyReader
	members     WorkspaceMemberReader

	resolver  credentials.CredentialResolver
	adapter   AdapterClient
	mysqlMode sqlpolicy.MySQLLexerMode
}

// ConnectionReader 仅暴露管线所需的工作区绑定连接读取能力。
type ConnectionReader interface {
	ConnectionByID(ctx context.Context, wsID, id uuid.UUID) (*metadata.Connection, error)
}

// WorkspaceMemberReader 仅暴露管线所需的工作区成员资格检查能力。
type WorkspaceMemberReader interface {
	MemberByWorkspaceAndUser(ctx context.Context, wsID, userID uuid.UUID) (*metadata.WorkspaceMember, error)
}

// ConnectionPolicyReader 仅暴露管线所需的连接策略读取能力。
type ConnectionPolicyReader interface {
	PolicyByConnection(ctx context.Context, wsID, connID uuid.UUID) (*metadata.ConnectionPolicy, error)
}

// AdapterHandle 是执行管线使用的最小 Adapter handle 契约。
type AdapterHandle interface {
	Query(ctx context.Context, req adapter.FirstPageRequest) (*adapter.QueryResult, error)
	Release()
}

// AdapterClient 是执行管线使用的最小 Adapter manager 契约。
type AdapterClient interface {
	Get(ctx context.Context, cfg adapter.ConnectConfig) (AdapterHandle, error)
}

type adapterManagerClient struct {
	manager *adapter.AdapterManager
}

func (c adapterManagerClient) Get(ctx context.Context, cfg adapter.ConnectConfig) (AdapterHandle, error) {
	return c.manager.Get(ctx, cfg)
}

// NewAdapterClient 将生产 AdapterManager 收窄为管线依赖。
func NewAdapterClient(manager *adapter.AdapterManager) AdapterClient {
	if manager == nil {
		return nil
	}
	return adapterManagerClient{manager: manager}
}

// PipelineConfig 管线配置。
type PipelineConfig struct {
	Store       ConnectionReader
	PolicyStore ConnectionPolicyReader
	Members     WorkspaceMemberReader
	Resolver    credentials.CredentialResolver
	Adapter     AdapterClient
	MySQLMode   sqlpolicy.MySQLLexerMode
}

// NewPipeline 创建执行管线。
func NewPipeline(cfg PipelineConfig) *Pipeline {
	return &Pipeline{
		store:       cfg.Store,
		policyStore: cfg.PolicyStore,
		members:     cfg.Members,
		resolver:    cfg.Resolver,
		adapter:     cfg.Adapter,
		mysqlMode:   cfg.MySQLMode,
	}
}

// ExecuteRequest 执行请求。
type ExecuteRequest struct {
	Principal    AuthenticatedPrincipal
	ConnectionID uuid.UUID
	SQL          string
	Args         []any
	Engine       Engine
}

// ExecuteResult 执行结果。
type ExecuteResult struct {
	Decision           sqlpolicy.PolicyDecision
	CredentialResolved bool
	AdapterCalled      bool
	Result             *adapter.QueryResult
	ErrorCode          StableErrorCode
}

// Execute 按顺序执行：Connection → Policy → Resolver → Adapter。
// 先从服务端获取连接元数据以确定权威 Engine，再以该 Engine 评估 SQL 策略；
// 如果客户端声称的 Engine 与连接记录不一致则拒绝（防止方言策略绕过）。
func (p *Pipeline) Execute(ctx context.Context, req ExecuteRequest) (*ExecuteResult, error) {
	result := &ExecuteResult{}
	if p == nil || p.store == nil || p.policyStore == nil || p.members == nil || p.resolver == nil || p.adapter == nil {
		result.ErrorCode = ErrInternalError
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	// 阶段 A: 成员资格与工作区权限 — 未激活或非成员拒绝。
	member, err := p.members.MemberByWorkspaceAndUser(ctx, req.Principal.WorkspaceID, req.Principal.UserID)
	if err != nil {
		result.ErrorCode = mapMembershipError(err)
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	if member == nil {
		result.ErrorCode = ErrForbidden
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	if !member.Role.CanRead() {
		result.ErrorCode = ErrForbidden
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	// 阶段 B: 连接元数据（工作区绑定），确定服务端权威 Engine
	conn, err := p.store.ConnectionByID(ctx, req.Principal.WorkspaceID, req.ConnectionID)
	if err != nil {
		result.ErrorCode = mapConnectionError(err)
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	serverEngine := Engine(conn.Engine)
	if req.Engine != "" && req.Engine != serverEngine {
		result.ErrorCode = ErrUnsupportedEngine
		return result, fmt.Errorf("%w", ErrUnsupportedEngine)
	}

	// 阶段 C: SQL Policy（使用服务端权威 Engine）
	decision, code := EvaluateSQL(serverEngine, req.SQL, p.mysqlMode)
	result.Decision = decision
	if !decision.Allowed {
		result.ErrorCode = code
		return result, fmt.Errorf("%w: %s", ErrReadNotAllowed, code)
	}

	// 连接策略必须来自服务端存储；缺失或未明确允许读取时 fail-closed。
	policy, err := p.policyStore.PolicyByConnection(ctx, conn.WorkspaceID, conn.ID)
	if err != nil {
		result.ErrorCode = mapPolicyStoreError(err)
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	if policy == nil {
		result.ErrorCode = ErrPolicyNotConfigured
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	if policy.AllowRead == nil || !*policy.AllowRead {
		result.ErrorCode = ErrReadNotAllowed
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	if policy.StatementTimeoutMs <= 0 || policy.MaxRows <= 0 {
		result.ErrorCode = ErrInternalError
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	// 阶段 C': Credential Resolver
	payload, err := p.resolver.ResolveCredential(ctx, conn.WorkspaceID, conn.SecretRef, conn.SecretVersion)
	if err != nil {
		result.CredentialResolved = false
		result.ErrorCode = mapCredentialError(err)
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	result.CredentialResolved = true

	configRevision, err := connectionConfigRevision(conn)
	if err != nil {
		result.ErrorCode = ErrInternalError
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	// 阶段 D: Adapter
	cfg := adapter.ConnectConfig{
		ConnectionID:   conn.ID.String(),
		SecretVersion:  conn.SecretVersion,
		ConfigRevision: configRevision,
		Engine:         adapter.Engine(conn.Engine),
		Host:           conn.Host,
		Port:           conn.Port,
		User:           payload.User,
		Password:       payload.Password,
		Database:       conn.Database,
		TLS:            adapter.TLSRequire,
	}

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(policy.StatementTimeoutMs)*time.Millisecond)
	defer cancel()

	handle, err := p.adapter.Get(execCtx, cfg)
	if err != nil {
		result.ErrorCode = mapAdapterError(err)
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	defer handle.Release()

	result.AdapterCalled = true

	// ADR-014 尚未完成 VerifiedSortPlan 迁移。没有可信唯一键证明时，仅允许
	// 单页受限执行，不伪造 SortKey.Unique，也不发放 continuation token。
	effectiveMaxRows := policy.MaxRows
	if effectiveMaxRows > 500 {
		effectiveMaxRows = 500
	}
	queryResult, err := handle.Query(execCtx, adapter.FirstPageRequest{
		Scope: adapter.UserWorkspaceScope{
			UserID:      req.Principal.UserID.String(),
			WorkspaceID: req.Principal.WorkspaceID.String(),
		},
		SQL:      req.SQL,
		Args:     req.Args,
		SortKeys: nil,
		PageSize: effectiveMaxRows,
		MaxRows:  effectiveMaxRows,
	})
	if err != nil {
		result.ErrorCode = mapAdapterError(err)
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	result.Result = queryResult
	return result, nil
}

func connectionConfigRevision(conn *metadata.Connection) (int64, error) {
	if conn == nil || conn.UpdatedAt.IsZero() {
		return 0, fmt.Errorf("connection updated_at is required")
	}
	revision := conn.UpdatedAt.UnixMicro()
	if revision <= 0 {
		return 0, fmt.Errorf("connection updated_at must be after unix epoch")
	}
	return revision, nil
}

// mapMembershipError 映射成员资格查询错误到稳定错误码。
func mapMembershipError(err error) StableErrorCode {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForbidden
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrExecutionTimeout
	}
	if errors.Is(err, context.Canceled) {
		return ErrExecutionCancelled
	}
	return ErrInternalError
}

// mapConnectionError 映射连接查询错误到稳定错误码。
func mapConnectionError(err error) StableErrorCode {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConnectionNotFound
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrExecutionTimeout
	}
	if errors.Is(err, context.Canceled) {
		return ErrExecutionCancelled
	}
	return ErrInternalError
}

func mapPolicyStoreError(err error) StableErrorCode {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrExecutionTimeout
	}
	if errors.Is(err, context.Canceled) {
		return ErrExecutionCancelled
	}
	return ErrInternalError
}

// mapCredentialError 映射凭证错误到稳定错误码。
func mapCredentialError(err error) StableErrorCode {
	if credentials.IsErrorCode(err, credentials.ErrCredentialNotFound) {
		return StableErrorCode(credentials.ErrCredentialNotFound)
	}
	if credentials.IsErrorCode(err, credentials.ErrCredentialRetired) {
		return StableErrorCode(credentials.ErrCredentialRetired)
	}
	if credentials.IsErrorCode(err, credentials.ErrDecryptionFailed) {
		return StableErrorCode(credentials.ErrDecryptionFailed)
	}
	if credentials.IsErrorCode(err, credentials.ErrUnknownKEKVersion) {
		return StableErrorCode(credentials.ErrUnknownKEKVersion)
	}
	if credentials.IsErrorCode(err, credentials.ErrWrapQuotaExhausted) {
		return StableErrorCode(credentials.ErrWrapQuotaExhausted)
	}
	return ErrInternalError
}

// mapAdapterError 映射 Adapter 错误到稳定错误码。
func mapAdapterError(err error) StableErrorCode {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrExecutionTimeout
	}
	if errors.Is(err, context.Canceled) {
		return ErrExecutionCancelled
	}

	var adapterErr *adapter.AdapterError
	if errors.As(err, &adapterErr) {
		switch adapterErr.Code {
		case adapter.ErrRateLimited:
			return ErrRateLimited
		case adapter.ErrConnPoolExhausted:
			return ErrConnectionBusy
		case adapter.ErrQueryTimeout:
			return ErrExecutionTimeout
		case adapter.ErrQueryCanceled:
			return ErrExecutionCancelled
		case adapter.ErrUnsupportedQuery:
			return ErrUnsupportedQuery
		case adapter.ErrConfigConflict:
			return ErrConnectionConfigConflict
		}
	}
	return ErrInternalError
}

package execution

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

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
	store metadata.ConnectionStore

	resolver credentials.CredentialResolver
	adapter  *adapter.AdapterManager
}

// PipelineConfig 管线配置。
type PipelineConfig struct {
	Store    metadata.ConnectionStore
	Resolver credentials.CredentialResolver
	Adapter  *adapter.AdapterManager
}

// NewPipeline 创建执行管线。
func NewPipeline(cfg PipelineConfig) *Pipeline {
	return &Pipeline{
		store:    cfg.Store,
		resolver: cfg.Resolver,
		adapter:  cfg.Adapter,
	}
}

// ExecuteRequest 执行请求。
type ExecuteRequest struct {
	Principal    AuthenticatedPrincipal
	ConnectionID uuid.UUID
	SQL          string
	Args         []any
	Engine       Engine
	Mode         sqlpolicy.MySQLLexerMode
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
	decision, code := EvaluateSQL(serverEngine, req.SQL, req.Mode)
	result.Decision = decision
	if !decision.Allowed {
		result.ErrorCode = code
		return result, fmt.Errorf("%w: %s", ErrReadNotAllowed, code)
	}

	// 阶段 C': Credential Resolver
	payload, err := p.resolver.ResolveCredential(ctx, conn.WorkspaceID, conn.SecretRef, conn.SecretVersion)
	if err != nil {
		result.CredentialResolved = false
		result.ErrorCode = mapCredentialError(err)
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	result.CredentialResolved = true

	// 阶段 D: Adapter
	cfg := adapter.ConnectConfig{
		ConnectionID:   conn.ID.String(),
		SecretVersion:  conn.SecretVersion,
		ConfigRevision: 1,
		Engine:         adapter.Engine(conn.Engine),
		Host:           conn.Host,
		Port:           conn.Port,
		User:           payload.User,
		Password:       payload.Password,
		Database:       conn.Database,
		TLS:            adapter.TLSRequire,
	}

	handle, err := p.adapter.Get(ctx, cfg)
	if err != nil {
		result.ErrorCode = mapAdapterError(err)
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	defer handle.Release()

	result.AdapterCalled = true

	queryResult, err := handle.Query(ctx, adapter.FirstPageRequest{
		Scope: adapter.UserWorkspaceScope{
			UserID:      req.Principal.UserID.String(),
			WorkspaceID: req.Principal.WorkspaceID.String(),
		},
		SQL:      req.SQL,
		Args:     req.Args,
		SortKeys: []adapter.SortKey{{Column: "1", Order: adapter.SortAsc, Unique: true}},
		PageSize: 100,
		MaxRows:  500,
	})
	if err != nil {
		result.ErrorCode = mapAdapterError(err)
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	result.Result = queryResult
	return result, nil
}

// mapConnectionError 映射连接查询错误到稳定错误码。
func mapConnectionError(err error) StableErrorCode {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConnectionNotFound
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return StableErrorCode("execution_timeout")
	}
	if errors.Is(err, context.Canceled) {
		return StableErrorCode("execution_cancelled")
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
	return ErrInternalError
}

// mapAdapterError 映射 Adapter 错误到稳定错误码。
func mapAdapterError(err error) StableErrorCode {
	if errors.Is(err, context.DeadlineExceeded) {
		return StableErrorCode("execution_timeout")
	}
	if errors.Is(err, context.Canceled) {
		return StableErrorCode("execution_cancelled")
	}
	return ErrInternalError
}

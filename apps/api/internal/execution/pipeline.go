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
	"github.com/fujiabao89/webdb/internal/pagination"
	"github.com/fujiabao89/webdb/internal/queryplan"
	"github.com/fujiabao89/webdb/internal/sqlpolicy"
	"github.com/google/uuid"
)

// Pipeline 执行管线：Policy → Credential Resolver → Adapter。
// 安全断言：Policy 拒绝时 Credential Resolver 调用 0 次；
//
//	Credential 失败时 Adapter 调用 0 次。
//
// WEB-23：配置 Tx/Audit/Alarm 时接入追加式审计与 execution 生命周期（ADR-017）。
type Pipeline struct {
	store       ConnectionReader
	policyStore ConnectionPolicyReader
	members     WorkspaceMemberReader

	resolver  credentials.CredentialResolver
	adapter   AdapterClient
	mysqlMode sqlpolicy.MySQLLexerMode

	txs               metadata.TxStore
	audit             metadata.AuditEventStore
	alarm             SecurityAlarm
	clock             func() time.Time
	newTrace          func() string
	auditWriteTimeout time.Duration // 可注入的审计持久化超时（VuXZW）

	registry    *pagination.Registry // ADR-015 Service-owned continuation registry
	ownRegistry bool                 // 是否由本管线创建（需 Close 释放）
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
// ADR-015：Adapter 不生成/解析/保存 continuation token；续页输入为
// 不可伪造的 queryplan.VerifiedNextPagePlan。
type AdapterHandle interface {
	Query(ctx context.Context, req adapter.FirstPageRequest) (*adapter.QueryResult, error)
	NextPage(ctx context.Context, scope adapter.UserWorkspaceScope, plan queryplan.VerifiedNextPagePlan) (*adapter.QueryResult, error)
	LoadTableMetadata(ctx context.Context, schema, table string) (*queryplan.TableMetadata, error)
	// ResolveQualifiedTable 返回未限定表名实际解析到的可信 schema
	// （PG 沿完整 search_path 解析 relation，MySQL=连接数据库）；
	// 查询失败或空时调用方 fail-closed。
	ResolveQualifiedTable(ctx context.Context, table string) (string, error)
	PoolGeneration() int64
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

	// WEB-23：审计感知管线。Tx 与 Audit 需同时配置；nil 时保持无审计旧行为。
	Tx                metadata.TxStore
	Audit             metadata.AuditEventStore
	Alarm             SecurityAlarm
	Clock             func() time.Time
	Trace             func() string
	AuditWriteTimeout time.Duration // 可注入的审计持久化超时（VuXZW）

	// Pagination 是 Service-owned continuation registry（ADR-015）。
	// 为 nil 时 NewPipeline 创建默认 registry；管线持有时需调用 Close 释放。
	Pagination *pagination.Registry
}

// defaultAuditWriteTimeout 审计持久化默认超时（与 connections/credentials 一致）。
const defaultAuditWriteTimeout = 5 * time.Second

// NewPipeline 创建执行管线。
func NewPipeline(cfg PipelineConfig) *Pipeline {
	clock := cfg.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	newTrace := cfg.Trace
	if newTrace == nil {
		newTrace = func() string { return uuid.NewString() }
	}
	alarm := cfg.Alarm
	if alarm == nil {
		alarm = NewStderrAlarm()
	}
	auditWriteTimeout := cfg.AuditWriteTimeout
	if auditWriteTimeout <= 0 {
		auditWriteTimeout = defaultAuditWriteTimeout
	}
	reg := cfg.Pagination
	ownRegistry := false
	if reg == nil {
		reg = pagination.New(pagination.DefaultConfig())
		ownRegistry = true
	}
	return &Pipeline{
		store:             cfg.Store,
		policyStore:       cfg.PolicyStore,
		members:           cfg.Members,
		resolver:          cfg.Resolver,
		adapter:           cfg.Adapter,
		mysqlMode:         cfg.MySQLMode,
		txs:               cfg.Tx,
		audit:             cfg.Audit,
		alarm:             alarm,
		clock:             clock,
		newTrace:          newTrace,
		auditWriteTimeout: auditWriteTimeout,
		registry:          reg,
		ownRegistry:       ownRegistry,
	}
}

// Close 释放管线持有的 registry（仅当由管线创建时）。
func (p *Pipeline) Close() {
	if p != nil && p.registry != nil && p.ownRegistry {
		p.registry.Close()
		p.registry = nil
	}
}

// ExecuteRequest 执行请求。
// SortKeys 为客户端排序意图（queryplan 中立类型，不含唯一性声明）；PageSize 0=默认。
type ExecuteRequest struct {
	Principal    AuthenticatedPrincipal
	ConnectionID uuid.UUID
	SQL          string
	Args         []any
	Engine       Engine
	SortKeys     []queryplan.SortKey
	PageSize     int
}

// ExecuteResult 执行结果。
// NextPageToken 仅在需要分页、唯一性证明有效且确有后续页时发放（ADR-014/015）。
type ExecuteResult struct {
	Decision           sqlpolicy.PolicyDecision
	CredentialResolved bool
	AdapterCalled      bool
	Result             *adapter.QueryResult
	ErrorCode          StableErrorCode
	TraceID            string
	ExecutionID        *uuid.UUID
	NextPageToken      *string
}

// Execute 按顺序执行：Connection → Policy → Resolver → Adapter，
// 并在配置了 Tx/Audit 时接入追加式审计与 execution 生命周期（ADR-017）。
// 先从服务端获取连接元数据以确定权威 Engine，再以该 Engine 评估 SQL 策略；
// 如果客户端声称的 Engine 与连接记录不一致则拒绝（防止方言策略绕过）。
func (p *Pipeline) Execute(ctx context.Context, req ExecuteRequest) (*ExecuteResult, error) {
	result := &ExecuteResult{}
	if p == nil || p.store == nil || p.policyStore == nil || p.members == nil || p.resolver == nil || p.adapter == nil {
		result.ErrorCode = ErrInternalError
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	// 生产 Pipeline 必须同时配置 Tx 与 Audit（fail-closed）；任一缺失都拒绝，
	// 避免 pending execution 无法持久化终态或审计写入缺失（finding 1）。
	if p.txs == nil || p.audit == nil {
		result.ErrorCode = ErrInternalError
		return result, fmt.Errorf("%w: Tx 与 Audit 必须同时配置", result.ErrorCode)
	}

	// 阶段 A: 成员资格与工作区权限 — 未激活或非成员拒绝（不写审计，proposal §7.1）。
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

	// 阶段 B: 连接元数据（工作区绑定），确定服务端权威 Engine（不写审计）。
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

	// 阶段 C: SQL Policy（使用服务端权威 Engine）。
	decision, code := EvaluateSQL(serverEngine, req.SQL, p.mysqlMode)
	result.Decision = decision
	statementHash := decision.Classification.StatementHash
	if statementHash == "" {
		statementHash = hashRawSQL(req.SQL)
	}

	traceID := p.newTrace()
	result.TraceID = traceID
	now := p.clock()

	// 阶段 B': 创建 Execution（pending）。Execute 前置校验已保证 Tx/Audit 均配置（finding 1），
	// 因此 exec 恒非 nil（finding 2）。
	mtx, err := p.txs.Begin(ctx)
	if err != nil {
		// 阶段 B 失败 → internal_error + $SECURITY_ALERT（proposal §9.1，VuXZO）。
		metadata.EmitAlarm(p.alarm, ctx, SecurityAlertEvent{
			TraceID: traceID, WorkspaceID: conn.WorkspaceID, Code: string(ErrInternalError), OccurredAt: p.clock(),
		})
		result.ErrorCode = ErrInternalError
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	exec := &metadata.Execution{
		WorkspaceID:   conn.WorkspaceID,
		ConnectionID:  conn.ID,
		ActorID:       req.Principal.UserID,
		StatementHash: statementHash,
		Status:        metadata.ExecStatusPending,
		TraceID:       traceID,
	}
	if err := mtx.CreateExecution(ctx, exec); err != nil {
		// 创建失败时回滚并跳过 Commit（finding 3，VuuxR）。
		mtx.Rollback()
		// 阶段 B 失败 → internal_error + $SECURITY_ALERT（proposal §9.1，VuXZO）。
		metadata.EmitAlarm(p.alarm, ctx, SecurityAlertEvent{
			TraceID: traceID, WorkspaceID: conn.WorkspaceID, Code: string(ErrInternalError), OccurredAt: p.clock(),
		})
		result.ErrorCode = ErrInternalError
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	if err := mtx.Commit(); err != nil {
		// 阶段 B 失败 → internal_error + $SECURITY_ALERT（proposal §9.1，VuXZO）。
		metadata.EmitAlarm(p.alarm, ctx, SecurityAlertEvent{
			TraceID: traceID, WorkspaceID: conn.WorkspaceID, Code: string(ErrInternalError), OccurredAt: p.clock(),
		})
		result.ErrorCode = ErrInternalError
		// finding 5：pending execution 未持久化时清除未提交的 ExecutionID。
		result.ExecutionID = nil
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	// finding 2：pending execution 创建后立即提交并释放事务，
	// 不跨越 PolicyByConnection / ResolveCredential，避免长事务占用连接。
	result.ExecutionID = &exec.ID

	// 阶段 C 拒绝：Execution=failed + Audit(sql.execute, denied)，Adapter 调用 0 次。
	if !decision.Allowed {
		result.ErrorCode = code
		// 传入实际拒绝原因 code（如 statement_not_allowed），而非笼统的 ErrReadNotAllowed（VuXZQ）。
		if r, err := p.failPreExecution(ctx, exec, result, conn, traceID, now,
			metadata.OutcomeDenied, metadata.AuditMetadata{
				StatementHash: &statementHash,
				ReasonCode:    strPtr(string(decision.ReasonCode)),
				Engine:        strPtr(string(serverEngine)),
			}, code); err != nil {
			return r, err
		}
		return result, fmt.Errorf("%w: %s", ErrReadNotAllowed, code)
	}

	// 连接策略必须来自服务端存储；缺失或未明确允许读取时 fail-closed。
	policy, err := p.policyStore.PolicyByConnection(ctx, conn.WorkspaceID, conn.ID)
	if err != nil {
		// outside：策略查询失败时先写 failed + 审计，再保留错误码映射与返回。
		result.ErrorCode = mapPolicyStoreError(err)
		if r, e := p.failPreExecution(ctx, exec, result, conn, traceID, now,
			metadata.OutcomeFailed, metadata.AuditMetadata{
				StatementHash: &statementHash,
				ErrorCode:     strPtr(string(result.ErrorCode)),
				Engine:        strPtr(string(serverEngine)),
			}, result.ErrorCode); e != nil {
			return r, e
		}
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	if policy == nil {
		result.ErrorCode = ErrPolicyNotConfigured
		if r, err := p.failPreExecution(ctx, exec, result, conn, traceID, now,
			metadata.OutcomeDenied, metadata.AuditMetadata{
				StatementHash: &statementHash,
				ReasonCode:    strPtr("policy_not_configured"),
				Engine:        strPtr(string(serverEngine)),
			}, ErrPolicyNotConfigured); err != nil {
			return r, err
		}
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	if policy.AllowRead == nil || !*policy.AllowRead {
		result.ErrorCode = ErrReadNotAllowed
		if r, err := p.failPreExecution(ctx, exec, result, conn, traceID, now,
			metadata.OutcomeDenied, metadata.AuditMetadata{
				StatementHash: &statementHash,
				ReasonCode:    strPtr("read_not_allowed"),
				Engine:        strPtr(string(serverEngine)),
			}, ErrReadNotAllowed); err != nil {
			return r, err
		}
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	if policy.StatementTimeoutMs <= 0 || policy.MaxRows <= 0 {
		// finding 3：策略参数校验失败统一写 failed + 审计，而非直接返回。
		result.ErrorCode = ErrInternalError
		if r, err := p.failPreExecution(ctx, exec, result, conn, traceID, now,
			metadata.OutcomeFailed, metadata.AuditMetadata{
				StatementHash: &statementHash,
				ErrorCode:     strPtr(string(ErrInternalError)),
				Engine:        strPtr(string(serverEngine)),
			}, ErrInternalError); err != nil {
			return r, err
		}
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	// 阶段 C': Credential Resolver。失败时 E14-E16 审计 + $SECURITY_ALERT，Adapter 调用 0 次。
	payload, err := p.resolver.ResolveCredential(ctx, conn.WorkspaceID, conn.SecretRef, conn.SecretVersion)
	if err != nil {
		result.CredentialResolved = false
		result.ErrorCode = mapCredentialError(err)
		credErr := err
		if r, e := p.failExecutionWith(ctx, exec, result, conn, traceID, now,
			func(mtx metadata.MetadataTx) error {
				return p.recordCredentialFailure(ctx, mtx, exec, result, conn, traceID, now, credErr)
			}, result.ErrorCode); e != nil {
			return r, e
		}
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	result.CredentialResolved = true

	configRevision, err := connectionConfigRevision(conn)
	if err != nil {
		// finding 3：配置修订失败统一写 failed + 审计，而非直接返回。
		result.ErrorCode = ErrInternalError
		if r, e := p.failPreExecution(ctx, exec, result, conn, traceID, now,
			metadata.OutcomeFailed, metadata.AuditMetadata{
				StatementHash: &statementHash,
				ErrorCode:     strPtr(string(ErrInternalError)),
				Engine:        strPtr(string(serverEngine)),
			}, ErrInternalError); e != nil {
			return r, e
		}
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	// 阶段 D-0: execution running 更新（短生命周期事务，finding 2）。
	// pending→running 更新或提交失败意味着元数据库故障，按审计失败处理
	// （返回 audit_failed + $SECURITY_ALERT），而非降级为 internal_error（vpvC7 outside）。
	mtx, err = p.txs.Begin(ctx)
	if err != nil {
		return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, ErrInternalError)
	}
	exec.Status = metadata.ExecStatusRunning
	if err := mtx.UpdateExecution(ctx, conn.WorkspaceID, exec); err != nil {
		mtx.Rollback()
		return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, ErrInternalError)
	}
	if err := mtx.Commit(); err != nil {
		return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, ErrInternalError)
	}

	// 阶段 D: Adapter。
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
		if err := p.recordPostExecution(ctx, exec, result, conn, traceID, now, statementHash); err != nil {
			return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, result.ErrorCode)
		}
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	released := false
	defer func() {
		// 兜底：任何路径（含 panic）都归还连接，且避免重复释放（outside finding 4）。
		if !released {
			handle.Release()
		}
	}()

	result.AdapterCalled = true

	// ADR-014：分页前置判定。effectiveMaxRows > effectivePageSize 时必须在
	// 用户查询执行前取得可信唯一性证明；无法证明则在目标库执行前 fail-closed。
	effectiveMaxRows := policy.MaxRows
	if effectiveMaxRows > 500 {
		effectiveMaxRows = 500
	}
	normalizedPageSize := req.PageSize
	if normalizedPageSize <= 0 {
		normalizedPageSize = 100
	}
	if normalizedPageSize > 500 {
		normalizedPageSize = 500
	}
	effectivePageSize := normalizedPageSize
	if effectivePageSize > effectiveMaxRows {
		effectivePageSize = effectiveMaxRows
	}
	requiresPagination := effectiveMaxRows > effectivePageSize

	var sortPlan queryplan.VerifiedSortPlan
	var schemaGen, tableSchema, tableName string
	if requiresPagination {
		sortPlan, schemaGen, tableSchema, tableName, err = p.verifySortPlan(execCtx, req, conn, handle, serverEngine)
		if err != nil {
			handle.Release()
			released = true
			result.ErrorCode = ErrUnsupportedQuery
			if rErr := p.recordPostExecution(ctx, exec, result, conn, traceID, now, statementHash); rErr != nil {
				return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, result.ErrorCode)
			}
			return result, fmt.Errorf("%w", result.ErrorCode)
		}
	}

	queryResult, err := handle.Query(execCtx, adapter.FirstPageRequest{
		Scope: adapter.UserWorkspaceScope{
			UserID:      req.Principal.UserID.String(),
			WorkspaceID: req.Principal.WorkspaceID.String(),
		},
		SQL:      req.SQL,
		Args:     req.Args,
		SortPlan: sortPlan,
		PageSize: effectivePageSize,
		MaxRows:  effectiveMaxRows,
	})
	if err != nil {
		handle.Release()
		released = true
		result.ErrorCode = mapAdapterError(err)
		if err := p.recordPostExecution(ctx, exec, result, conn, traceID, now, statementHash); err != nil {
			return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, result.ErrorCode)
		}
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	// ADR-015：Service 是 token/registry 唯一 Owner；深拷贝 SQL/Args/last values
	// 后创建 continuation，客户端仅持有 opaque handle。
	// 注意：token 创建后先不发布；须待 recordPostExecution（execution 终态 + 审计）
	// 成功后才写入 result.NextPageToken。审计失败时原子撤销 registry token（Codex P1-B），
	// 否则调用方会在第一页审计未成功的情况下继续读取后续页。
	var contToken string
	continuationCreated := false
	if requiresPagination && queryResult.HasMore && queryResult.TotalReturned < effectiveMaxRows {
		token, tokErr := p.createContinuation(req, conn, handle, sortPlan, schemaGen, tableSchema, tableName,
			policy, statementHash, queryResult, effectivePageSize, effectiveMaxRows)
		if tokErr != nil {
			handle.Release()
			released = true
			result.ErrorCode = mapPaginationError(tokErr)
			if err := p.recordPostExecution(ctx, exec, result, conn, traceID, now, statementHash); err != nil {
				return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, result.ErrorCode)
			}
			return result, fmt.Errorf("%w", result.ErrorCode)
		}
		contToken = token
		continuationCreated = true
	}

	// 单页查询完成、结果已完整填充且无活动游标：立即释放 handle，
	// 再进入 recordPostExecution，避免审计持久化期间占用连接池（outside finding 4）。
	handle.Release()
	released = true

	result.Result = queryResult
	if err := p.recordPostExecution(ctx, exec, result, conn, traceID, now, statementHash); err != nil {
		if continuationCreated {
			p.registry.Revoke(contToken)
		}
		return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, result.ErrorCode)
	}
	if continuationCreated {
		result.NextPageToken = &contToken
	}
	return result, nil
}

// ---- ADR-014/015：分页唯一性证明与续页 ----------------------------------------

// policyVersionOf 以 ConnectionPolicy.UpdatedAt 派生 policy version（与
// connectionConfigRevision 一致）；策略变化即旧 token 失效。
func policyVersionOf(policy *metadata.ConnectionPolicy) (int64, error) {
	if policy == nil || policy.UpdatedAt.IsZero() {
		return 0, fmt.Errorf("policy updated_at is required")
	}
	v := policy.UpdatedAt.UnixMicro()
	if v <= 0 {
		return 0, fmt.Errorf("policy updated_at must be after unix epoch")
	}
	return v, nil
}

// verifySortPlan 在用户查询执行前构造可信唯一性证明（ADR-014）。
// 步骤：保守形状提取 → 加载 SchemaSnapshot → VerifySortPlan。
// 任何一步失败都 fail-closed（调用方映射为 unsupported_query，Adapter.Query=0）。
// 返回计划、schema generation 与表 lineage（供续页 schema generation 重新校验）。
func (p *Pipeline) verifySortPlan(
	execCtx context.Context,
	req ExecuteRequest,
	conn *metadata.Connection,
	handle AdapterHandle,
	engine Engine,
) (queryplan.VerifiedSortPlan, string, string, string, error) {
	dialect := sqlpolicy.Dialect(engine)
	shape, err := sqlpolicy.AnalyzeShape(dialect, req.SQL)
	if err != nil {
		return nil, "", "", "", err
	}
	if shape.BaseSchema == "" {
		// 未限定表名：经方言 relation 解析（PG 沿完整 search_path 而非 current_schema()
		// 只返回首项存在的 schema——表可能位于后置 search_path 条目，Codex P1；
		// MySQL 经连接数据库）。查询失败或返回空 → fail-closed。
		s, err := handle.ResolveQualifiedTable(execCtx, shape.BaseTable)
		if err != nil || s == "" {
			return nil, "", "", "", fmt.Errorf("resolve table schema: %w", err)
		}
		shape.BaseSchema = s
	}
	meta, err := handle.LoadTableMetadata(execCtx, shape.BaseSchema, shape.BaseTable)
	if err != nil {
		return nil, "", "", "", err
	}
	snap, err := queryplan.NewSchemaSnapshot(conn.ID.String(), queryplan.Dialect(dialect), handle.PoolGeneration(), meta)
	if err != nil {
		return nil, "", "", "", err
	}
	plan, err := queryplan.VerifySortPlan(snap, shape, req.SortKeys)
	if err != nil {
		return nil, "", "", "", err
	}
	return plan, snap.SchemaGeneration, snap.SchemaName(), snap.TableName(), nil
}

// createContinuation 由 Service 创建 continuation token（ADR-015）。
// SQL/Args/LastSortValues 深拷贝后计字节配额；客户端仅持有 opaque handle。
func (p *Pipeline) createContinuation(
	req ExecuteRequest,
	conn *metadata.Connection,
	handle AdapterHandle,
	plan queryplan.VerifiedSortPlan,
	schemaGen, tableSchema, tableName string,
	policy *metadata.ConnectionPolicy,
	statementHash string,
	queryResult *adapter.QueryResult,
	effectivePageSize, effectiveMaxRows int,
) (string, error) {
	specs := plan.SortSpecs()
	lastVals, err := adapter.ExtractLastValues(queryResult.Rows, queryResult.Columns, specs)
	if err != nil {
		return "", err
	}
	pv, err := policyVersionOf(policy)
	if err != nil {
		return "", err
	}
	state := &pagination.ContinuationState{
		UserID:           req.Principal.UserID.String(),
		WorkspaceID:      req.Principal.WorkspaceID.String(),
		ConnectionID:     conn.ID.String(),
		PoolGeneration:   handle.PoolGeneration(),
		SchemaGeneration: schemaGen,
		TableSchema:      tableSchema,
		TableName:        tableName,
		PolicyVersion:    pv,
		StatementHash:    statementHash,
		SortPlan:         plan,
		SQL:              req.SQL,
		Args:             deepCopyArgs(req.Args),
		LastSortValues:   lastVals,
		CumulativeCount:  queryResult.TotalReturned,
		PageSize:         effectivePageSize,
		MaxRows:          effectiveMaxRows,
		TimeoutMs:        policy.StatementTimeoutMs,
	}
	return p.registry.Create(state)
}

// NextPageRequest 续页请求（客户端仅提交 opaque token，不得重交 SQL/Args/SortKeys）。
type NextPageRequest struct {
	Principal AuthenticatedPrincipal
	Token     string
}

// ExecuteNextPage 执行续页并重新授权（ADR-015 §7 / WEB-34 §9.4）。
//
// 流程：原子 claim → 重新验证成员/连接/策略/generation/statement hash →
// 构造 VerifiedNextPagePlan → Adapter.NextPage → Rotate/Complete/Abort。
// claim 后任何失败不恢复旧 token；撤权、策略变化、generation 变化、取消、
// 超时、DB 错误均使旧 token 永久失效。
//
// 注意：D11 每物理页独立 Execution/Audit 的持久化编排由 WEB-35 承接；
// 本方法提供分页安全后端（重新授权 + 状态机），不写审计。
func (p *Pipeline) ExecuteNextPage(ctx context.Context, req NextPageRequest) (*ExecuteResult, error) {
	result := &ExecuteResult{}
	if p == nil || p.registry == nil || p.store == nil || p.policyStore == nil ||
		p.members == nil || p.resolver == nil || p.adapter == nil {
		result.ErrorCode = ErrInternalError
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	claim, err := p.registry.Claim(req.Token)
	if err != nil {
		result.ErrorCode = mapPaginationError(err)
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	// panic 兜底：claim 后任何路径（成员/策略/凭证/元数据/adapter 调用 panic）都要
	// 归还 in-flight token 并释放配额，避免占用 registry 配额直到 TTL（Greptile P2）。
	// Abort 幂等：正常 Rotate/Complete 或显式 Abort 后，经 claim ownership/version
	// 校验成为安全 no-op，且不会删除已旋转出的新 token。
	defer func() { _ = claim.Abort() }()
	state := claim.State()

	// 重新授权：token 绑定 principal（ADR-015 §4）。同工作区其他用户不得横向
	// 使用他人 token；不匹配即 abort，不泄露其他信息。
	if state.UserID != req.Principal.UserID.String() || state.WorkspaceID != req.Principal.WorkspaceID.String() {
		claim.Abort()
		result.ErrorCode = ErrInvalidPageToken
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	// 重新授权：成员资格。
	member, err := p.members.MemberByWorkspaceAndUser(ctx, req.Principal.WorkspaceID, req.Principal.UserID)
	if err != nil {
		claim.Abort()
		result.ErrorCode = mapMembershipError(err)
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	if member == nil || !member.Role.CanRead() {
		claim.Abort()
		result.ErrorCode = ErrForbidden
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	// 重新授权：连接（工作区绑定）。
	connID, err := uuid.Parse(state.ConnectionID)
	if err != nil {
		claim.Abort()
		result.ErrorCode = ErrInvalidPageToken
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	conn, err := p.store.ConnectionByID(ctx, req.Principal.WorkspaceID, connID)
	if err != nil {
		claim.Abort()
		result.ErrorCode = mapConnectionError(err)
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	if conn.ID.String() != state.ConnectionID {
		claim.Abort()
		result.ErrorCode = ErrInvalidPageToken
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	// 重新授权：策略（AllowRead/MaxRows/timeout/policy version）。
	policy, err := p.policyStore.PolicyByConnection(ctx, conn.WorkspaceID, conn.ID)
	if err != nil {
		claim.Abort()
		result.ErrorCode = mapPolicyStoreError(err)
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	if policy == nil || policy.AllowRead == nil || !*policy.AllowRead ||
		policy.MaxRows <= 0 || policy.StatementTimeoutMs <= 0 {
		claim.Abort()
		result.ErrorCode = ErrInvalidPageToken
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	pv, err := policyVersionOf(policy)
	if err != nil || pv != state.PolicyVersion {
		claim.Abort()
		result.ErrorCode = ErrInvalidPageToken
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	// 不得从 token 恢复旧的更宽松 MaxRows/timeout。
	effectiveMaxRows := policy.MaxRows
	if effectiveMaxRows > state.MaxRows {
		effectiveMaxRows = state.MaxRows
	}
	effectiveTimeout := policy.StatementTimeoutMs
	if effectiveTimeout > state.TimeoutMs {
		effectiveTimeout = state.TimeoutMs
	}

	// 凭证（重新解析，不信任 token 中的任何旧凭据）。
	payload, err := p.resolver.ResolveCredential(ctx, conn.WorkspaceID, conn.SecretRef, conn.SecretVersion)
	if err != nil {
		claim.Abort()
		result.ErrorCode = mapCredentialError(err)
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	configRevision, err := connectionConfigRevision(conn)
	if err != nil {
		claim.Abort()
		result.ErrorCode = ErrInternalError
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
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
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(effectiveTimeout)*time.Millisecond)
	defer cancel()
	handle, err := p.adapter.Get(execCtx, cfg)
	if err != nil {
		claim.Abort()
		result.ErrorCode = mapAdapterError(err)
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	released := false
	defer func() {
		if !released {
			handle.Release()
		}
	}()

	// pool generation 变化 → token 失效。
	if handle.PoolGeneration() != state.PoolGeneration {
		claim.Abort()
		result.ErrorCode = ErrInvalidPageToken
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	// schema generation 重新校验：重新加载可信元数据并与 state 对比。
	meta, err := handle.LoadTableMetadata(execCtx, state.TableSchema, state.TableName)
	if err != nil {
		claim.Abort()
		result.ErrorCode = ErrInvalidPageToken
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	snap, err := queryplan.NewSchemaSnapshot(
		state.ConnectionID,
		queryplan.Dialect(conn.Engine),
		handle.PoolGeneration(),
		meta,
	)
	if err != nil || snap.SchemaGeneration != state.SchemaGeneration {
		claim.Abort()
		result.ErrorCode = ErrInvalidPageToken
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	// 构造不可伪造的 VerifiedNextPagePlan（SQL/Args 来自 state，不来自客户端）。
	nextPlan, err := queryplan.NewVerifiedNextPagePlan(
		state.SortPlan, state.LastSortValues, state.SQL, state.Args,
		state.PageSize, effectiveMaxRows, state.CumulativeCount,
	)
	if err != nil {
		claim.Abort()
		result.ErrorCode = ErrInvalidPageToken
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	result.AdapterCalled = true
	queryResult, err := handle.NextPage(execCtx, adapter.UserWorkspaceScope{
		UserID:      req.Principal.UserID.String(),
		WorkspaceID: req.Principal.WorkspaceID.String(),
	}, nextPlan)
	if err != nil {
		handle.Release()
		released = true
		claim.Abort()
		result.ErrorCode = mapAdapterError(err)
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	handle.Release()
	released = true
	result.Result = queryResult

	// Rotate/Complete：旧 token 永不恢复。
	if queryResult.HasMore && queryResult.TotalReturned < effectiveMaxRows {
		specs := state.SortPlan.SortSpecs()
		newLast, err := adapter.ExtractLastValues(queryResult.Rows, queryResult.Columns, specs)
		if err != nil {
			claim.Abort()
			result.ErrorCode = mapAdapterError(err)
			return result, fmt.Errorf("%w", result.ErrorCode)
		}
		newState := &pagination.ContinuationState{
			UserID:           state.UserID,
			WorkspaceID:      state.WorkspaceID,
			ConnectionID:     state.ConnectionID,
			PoolGeneration:   state.PoolGeneration,
			SchemaGeneration: state.SchemaGeneration,
			TableSchema:      state.TableSchema,
			TableName:        state.TableName,
			PolicyVersion:    state.PolicyVersion,
			StatementHash:    state.StatementHash,
			SortPlan:         state.SortPlan,
			SQL:              state.SQL,
			Args:             state.Args,
			LastSortValues:   newLast,
			CumulativeCount:  queryResult.TotalReturned,
			PageSize:         state.PageSize,
			MaxRows:          effectiveMaxRows,
			TimeoutMs:        effectiveTimeout,
		}
		newToken, err := claim.Rotate(newState)
		if err != nil {
			result.ErrorCode = mapPaginationError(err)
			return result, fmt.Errorf("%w", result.ErrorCode)
		}
		result.NextPageToken = &newToken
	} else {
		if err := claim.Complete(); err != nil {
			result.ErrorCode = mapPaginationError(err)
			return result, fmt.Errorf("%w", result.ErrorCode)
		}
	}
	return result, nil
}

// ---- 审计与 execution 生命周期辅助（ADR-017）----------------------------------

// auditFailed 处理审计写入失败：fail-closed 不返回结果；触发安全告警。
// 告警时间戳在 emit 时用 p.clock() 采样，避免沿用执行前采样导致的时间倒错（Qodo #3）。
func (p *Pipeline) auditFailed(
	ctx context.Context,
	result *ExecuteResult,
	traceID string,
	wsID uuid.UUID,
	originalCode StableErrorCode,
) (*ExecuteResult, error) {
	// 告警通道自身失败不得 panic / 改变 fail-closed 返回（T28）。
	metadata.EmitAlarm(p.alarm, ctx, SecurityAlertEvent{
		TraceID:     traceID,
		WorkspaceID: wsID,
		Code:        string(ErrAuditFailed),
		OccurredAt:  p.clock(),
	})
	result.ErrorCode = ErrAuditFailed
	// ADR-017 §6：审计失败不向调用方返回查询结果；也不得泄露未成功审计的续页 token
	// （Codex P1-B）。registry 中的对应 token 由调用方在审计失败路径撤销。
	result.Result = nil
	result.NextPageToken = nil
	return result, fmt.Errorf("%w (original error: %s)", result.ErrorCode, originalCode)
}

// failExecutionWith 在执行前失败时开启短生命周期事务：按 record 回调更新 execution=failed
// + 追加失败审计并提交（finding 2/3）。任一失败经 auditFailed 返回 audit_failed + $SECURITY_ALERT，
// 避免 deferred Rollback 撤销已记录的 execution 与审计状态。
func (p *Pipeline) failExecutionWith(
	ctx context.Context,
	exec *metadata.Execution,
	result *ExecuteResult,
	conn *metadata.Connection,
	traceID string,
	now time.Time,
	record func(mtx metadata.MetadataTx) error,
	originalCode StableErrorCode,
) (*ExecuteResult, error) {
	if exec == nil {
		// 不变量：Execute 前置校验保证 execution 已创建；缺失视为内部错误，
		// 不静默跳过失败审计（finding 2）。
		result.ErrorCode = ErrInternalError
		return result, fmt.Errorf("%w: execution not initialized", ErrInternalError)
	}
	mtx, err := p.txs.Begin(ctx)
	if err != nil {
		return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, originalCode)
	}
	defer mtx.Rollback()
	if err := record(mtx); err != nil {
		return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, originalCode)
	}
	if _, err := p.commitPreExecution(ctx, mtx, result, traceID, conn.WorkspaceID); err != nil {
		return result, err
	}
	return result, nil
}

// failPreExecution 在执行前失败时开启短生命周期事务：更新 execution=failed + 追加失败审计并提交
// （finding 2/3）。
func (p *Pipeline) failPreExecution(
	ctx context.Context,
	exec *metadata.Execution,
	result *ExecuteResult,
	conn *metadata.Connection,
	traceID string,
	now time.Time,
	outcome metadata.AuditOutcome,
	md metadata.AuditMetadata,
	originalCode StableErrorCode,
) (*ExecuteResult, error) {
	return p.failExecutionWith(ctx, exec, result, conn, traceID, now,
		func(mtx metadata.MetadataTx) error {
			return p.recordPreExecution(ctx, mtx, exec, result, conn, traceID, now, outcome, md)
		}, originalCode)
}

// commitPreExecution 提交执行前的事务（execution failed + audit 原子持久化，ADR-017 §6）。
// 提交失败意味着审计持久化无法保证（DB 连接丢失/提交失败），必须按审计失败处理
// 并触发 $SECURITY_ALERT，而非降级为 internal_error（Codex P1）。
func (p *Pipeline) commitPreExecution(
	ctx context.Context,
	mtx metadata.MetadataTx,
	result *ExecuteResult,
	traceID string,
	wsID uuid.UUID,
) (*ExecuteResult, error) {
	if err := mtx.Commit(); err != nil {
		return p.auditFailed(ctx, result, traceID, wsID, ErrInternalError)
	}
	return result, nil
}

// recordPreExecution 在执行前（阶段 C/C'）原子记录 execution=failed + 审计事件。
// 任一失败即返回错误 → 调用方 fail-closed，Adapter 调用 0 次。
func (p *Pipeline) recordPreExecution(
	ctx context.Context,
	mtx metadata.MetadataTx,
	exec *metadata.Execution,
	result *ExecuteResult,
	conn *metadata.Connection,
	traceID string,
	now time.Time,
	outcome metadata.AuditOutcome,
	md metadata.AuditMetadata,
) error {
	code := string(result.ErrorCode)
	exec.Status = metadata.ExecStatusFailed
	exec.ErrorCode = &code
	finished := now
	exec.FinishedAt = &finished

	if err := mtx.UpdateExecution(ctx, conn.WorkspaceID, exec); err != nil {
		return err
	}

	event, err := newAuditEvent(
		conn.WorkspaceID,
		metadata.ActorTypeUser,
		&exec.ActorID,
		&conn.ID,
		&exec.ID,
		metadata.ActionSQLExecute,
		"execution",
		exec.ID.String(),
		outcome,
		md,
		traceID,
		now,
	)
	if err != nil {
		return err
	}
	return mtx.AppendAudit(ctx, event)
}

// recordCredentialFailure 记录凭证解析失败：E14-E16 + $SECURITY_ALERT，Adapter 调用 0 次。
func (p *Pipeline) recordCredentialFailure(
	ctx context.Context,
	mtx metadata.MetadataTx,
	exec *metadata.Execution,
	result *ExecuteResult,
	conn *metadata.Connection,
	traceID string,
	now time.Time,
	credErr error,
) error {
	code := string(result.ErrorCode)

	// 仅解密类失败触发安全告警（ADR-017 §6 / Qodo #2 / CodeRabbit #8）；
	// credential_not_found/credential_retired 等查找类失败只记录 E14，不升级告警。
	// 告警时间戳在 emit 时采样（Qodo #3）。
	if credentials.IsDecryptFailureCode(credentials.ErrorCode(code)) {
		metadata.EmitAlarm(p.alarm, ctx, SecurityAlertEvent{
			TraceID:     traceID,
			WorkspaceID: conn.WorkspaceID,
			Code:        code,
			OccurredAt:  p.clock(),
		})
	}

	var action string
	var md metadata.AuditMetadata
	switch result.ErrorCode {
	case StableErrorCode(credentials.ErrCredentialNotFound), StableErrorCode(credentials.ErrCredentialRetired):
		// E14: credential.lookup 失败（system actor）
		action = metadata.ActionCredentialLookup
		md = metadata.AuditMetadata{
			SecretRef: strPtr(conn.SecretRef.String()),
			ErrorCode: strPtr(code),
		}
	case StableErrorCode(credentials.ErrDecryptionFailed), StableErrorCode(credentials.ErrUnknownKEKVersion):
		// E15/E16: 仅真正的解密失败 / 未知 KEK 版本记 credential.decrypt（VuXZU）。
		action = metadata.ActionCredentialDecrypt
		md = metadata.AuditMetadata{
			SecretRef:     strPtr(conn.SecretRef.String()),
			SecretVersion: intPtr(conn.SecretVersion),
			ErrorCode:     strPtr(code),
		}
		// E16: unknown_kek_version 需携带 kek_version（Codex P1）。
		var kekErr *credentials.KEKVersionError
		if errors.As(credErr, &kekErr) {
			md.KEKVersion = intPtr(kekErr.Version)
		}
	default:
		// wrap_quota_exhausted / internal_error 等非解密失败：归为 E14 lookup（VuXZU）。
		action = metadata.ActionCredentialLookup
		md = metadata.AuditMetadata{
			SecretRef: strPtr(conn.SecretRef.String()),
			ErrorCode: strPtr(code),
		}
	}

	exec.Status = metadata.ExecStatusFailed
	exec.ErrorCode = &code
	finished := now
	exec.FinishedAt = &finished
	if err := mtx.UpdateExecution(ctx, conn.WorkspaceID, exec); err != nil {
		return err
	}

	// E14-E16 事件矩阵要求 execution_id 为 NULL（proposal §8.1），
	// 凭证失败事件由 system actor 记录，不关联 execution（CodeRabbit #18）。
	event, err := newAuditEvent(
		conn.WorkspaceID,
		metadata.ActorTypeSystem,
		nil,
		&conn.ID,
		nil,
		action,
		"credential",
		conn.SecretRef.String(),
		metadata.OutcomeFailed,
		md,
		traceID,
		now,
	)
	if err != nil {
		return err
	}
	return mtx.AppendAudit(ctx, event)
}

// recordPostExecution 在执行完成后记录 execution 终态 + 审计事件。
// execution 更新在独立事务中先提交；审计后置独立写入。
// 审计写入失败时：不返回结果、返回 audit_failed，execution 已记录为终态（ADR-017 §6）。
func (p *Pipeline) recordPostExecution(
	ctx context.Context,
	exec *metadata.Execution,
	result *ExecuteResult,
	conn *metadata.Connection,
	traceID string,
	now time.Time,
	statementHash string,
) error {
	// 调用方取消 ctx 时，仍需用未取消的 context 持久化 execution 终态与审计（Codex P1），
	// 否则 running execution 永不终结、E13 永不追加；同时设置独立超时，避免元数据库
	// 无响应时无限阻塞请求 goroutine（VuXZW）。
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.auditWriteTimeout)
	defer cancel()

	var status metadata.ExecutionStatus
	var outcome metadata.AuditOutcome
	md := metadata.AuditMetadata{
		StatementHash: &statementHash,
		Engine:        strPtr(string(conn.Engine)),
	}

	switch result.ErrorCode {
	case ErrExecutionCancelled:
		// E13 cancelled：矩阵不含 environment。
		status = metadata.ExecStatusCancelled
		outcome = metadata.OutcomeCancelled
		md.ErrorCode = strPtr("query_cancelled")
	case ErrExecutionTimeout:
		// E12 timeout：矩阵不含 environment。
		status = metadata.ExecStatusFailed
		outcome = metadata.OutcomeFailed
		md.ErrorCode = strPtr("query_timeout")
	case ErrUnsupportedQuery:
		// unsupported_query：需要分页但缺少/无法验证唯一性证明，执行前拒绝
		// （PAGE-01：Execution failed，Audit denied）。
		status = metadata.ExecStatusFailed
		outcome = metadata.OutcomeDenied
		md.ErrorCode = strPtr(string(result.ErrorCode))
		md.Environment = strPtr(string(conn.Environment))
	case "":
		// E10 succeeded：矩阵要求 environment。
		status = metadata.ExecStatusCompleted
		outcome = metadata.OutcomeSucceeded
		md.Environment = strPtr(string(conn.Environment))
		if result.Result != nil {
			rowCount := result.Result.TotalReturned
			md.RowCount = intPtr(rowCount)
			// 同步写入 execution 记录，使 executions.row_count 可交叉核对（CodeRabbit #19）。
			exec.RowCount = &rowCount
		}
	default:
		// E11 failed：矩阵要求 environment。
		status = metadata.ExecStatusFailed
		outcome = metadata.OutcomeFailed
		md.ErrorCode = strPtr(string(result.ErrorCode))
		md.Environment = strPtr(string(conn.Environment))
	}

	exec.Status = status
	// executions.error_code 使用稳定的 result 错误码（成功为 nil），与 recordPreExecution
	// 词汇表一致；md.ErrorCode 保持 metadata 专用码（query_cancelled/query_timeout，VuXZZ）。
	if result.ErrorCode != "" {
		exec.ErrorCode = strPtr(string(result.ErrorCode))
	} else {
		exec.ErrorCode = nil
	}
	// finished_at 在 adapter 工作完成后采样，避免早于 started_at（Codex P1）。
	finished := p.clock()
	exec.FinishedAt = &finished
	if dur := finished.Sub(exec.StartedAt); dur > 0 {
		d := int(dur.Milliseconds())
		md.DurationMs = &d
		exec.DurationMs = &d
	}

	// 阶段 D 后：execution 更新独立事务提交（proposal §9.1）。
	mtx, err := p.txs.Begin(auditCtx)
	if err != nil {
		return err
	}
	defer mtx.Rollback()
	if err := mtx.UpdateExecution(auditCtx, conn.WorkspaceID, exec); err != nil {
		return err
	}
	if err := mtx.Commit(); err != nil {
		return err
	}

	// 审计后置独立写入。occurred_at 使用 post-adapter 的 finished 时间戳，
	// 而非执行前采样值，确保审计时间不早于实际查询（Codex P1）。
	event, err := newAuditEvent(
		conn.WorkspaceID,
		metadata.ActorTypeUser,
		&exec.ActorID,
		&conn.ID,
		&exec.ID,
		metadata.ActionSQLExecute,
		"execution",
		exec.ID.String(),
		outcome,
		md,
		traceID,
		finished,
	)
	if err != nil {
		return err
	}
	return p.audit.AppendAudit(auditCtx, event)
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

// deepCopyArgs 递归深拷贝 []any（Args 敏感，不共享可变引用）。
func deepCopyArgs(args []any) []any {
	if args == nil {
		return nil
	}
	out := make([]any, len(args))
	for i, v := range args {
		out[i] = deepCopyAnyValue(v)
	}
	return out
}

func deepCopyAnyValue(v any) any {
	switch t := v.(type) {
	case []any:
		return deepCopyArgs(t)
	case []byte:
		cp := make([]byte, len(t))
		copy(cp, t)
		return cp
	default:
		return v
	}
}

// mapPaginationError 映射 registry 错误到稳定错误码。
func mapPaginationError(err error) StableErrorCode {
	if pagination.IsRegistryErrorCode(err, pagination.ErrInvalidPageToken) {
		return ErrInvalidPageToken
	}
	if pagination.IsRegistryErrorCode(err, pagination.ErrPaginationCapacity) {
		return ErrPaginationCapacityExhausted
	}
	if pagination.IsRegistryErrorCode(err, pagination.ErrRegistryClosed) {
		return ErrInternalError
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
		case adapter.ErrInvalidPageToken:
			return ErrInvalidPageToken
		}
	}
	return ErrInternalError
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

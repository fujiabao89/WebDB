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
//
// WEB-23：配置 Tx/Audit/Alarm 时接入追加式审计与 execution 生命周期（ADR-017）。
type Pipeline struct {
	store       ConnectionReader
	policyStore ConnectionPolicyReader
	members     WorkspaceMemberReader

	resolver  credentials.CredentialResolver
	adapter   AdapterClient
	mysqlMode sqlpolicy.MySQLLexerMode

	txs      metadata.TxStore
	audit    metadata.AuditEventStore
	alarm    SecurityAlarm
	clock    func() time.Time
	newTrace func() string
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

	// WEB-23：审计感知管线。Tx 与 Audit 需同时配置；nil 时保持无审计旧行为。
	Tx    metadata.TxStore
	Audit metadata.AuditEventStore
	Alarm SecurityAlarm
	Clock func() time.Time
	Trace func() string
}

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
	return &Pipeline{
		store:       cfg.Store,
		policyStore: cfg.PolicyStore,
		members:     cfg.Members,
		resolver:    cfg.Resolver,
		adapter:     cfg.Adapter,
		mysqlMode:   cfg.MySQLMode,
		txs:         cfg.Tx,
		audit:       cfg.Audit,
		alarm:       alarm,
		clock:       clock,
		newTrace:    newTrace,
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
	TraceID            string
	ExecutionID        *uuid.UUID
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
	if (p.txs == nil) != (p.audit == nil) {
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

	// 阶段 B': 创建 Execution（pending）— 仅在配置了元数据库事务时启用。
	var exec *metadata.Execution
	var mtx metadata.MetadataTx
	if p.txs != nil {
		mtx, err = p.txs.Begin(ctx)
		if err != nil {
			result.ErrorCode = ErrInternalError
			return result, fmt.Errorf("%w", result.ErrorCode)
		}
		defer mtx.Rollback()

		exec = &metadata.Execution{
			WorkspaceID:   conn.WorkspaceID,
			ConnectionID:  conn.ID,
			ActorID:       req.Principal.UserID,
			StatementHash: statementHash,
			Status:        metadata.ExecStatusPending,
			TraceID:       traceID,
		}
		if err := mtx.CreateExecution(ctx, exec); err != nil {
			result.ErrorCode = ErrInternalError
			return result, fmt.Errorf("%w", result.ErrorCode)
		}
		result.ExecutionID = &exec.ID
	}

	// 阶段 C 拒绝：Execution=failed + Audit(sql.execute, denied)，Adapter 调用 0 次。
	if !decision.Allowed {
		result.ErrorCode = code
		if mtx != nil {
			if err := p.recordPreExecution(ctx, mtx, exec, result, conn, traceID, now,
				metadata.OutcomeDenied, metadata.AuditMetadata{
					StatementHash: &statementHash,
					ReasonCode:    strPtr(string(decision.ReasonCode)),
					Engine:        strPtr(string(serverEngine)),
				}); err != nil {
				return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, ErrReadNotAllowed)
			}
			// 提交 execution failed + audit denied（同一事务原子持久化，ADR-017 §6）。
			if _, err := p.commitPreExecution(ctx, mtx, result, traceID, conn.WorkspaceID); err != nil {
				return result, err
			}
		}
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
		if mtx != nil {
			if err := p.recordPreExecution(ctx, mtx, exec, result, conn, traceID, now,
				metadata.OutcomeDenied, metadata.AuditMetadata{
					StatementHash: &statementHash,
					ReasonCode:    strPtr("policy_not_configured"),
					Engine:        strPtr(string(serverEngine)),
				}); err != nil {
				return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, ErrPolicyNotConfigured)
			}
			if _, err := p.commitPreExecution(ctx, mtx, result, traceID, conn.WorkspaceID); err != nil {
				return result, err
			}
		}
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	if policy.AllowRead == nil || !*policy.AllowRead {
		result.ErrorCode = ErrReadNotAllowed
		if mtx != nil {
			if err := p.recordPreExecution(ctx, mtx, exec, result, conn, traceID, now,
				metadata.OutcomeDenied, metadata.AuditMetadata{
					StatementHash: &statementHash,
					ReasonCode:    strPtr("read_not_allowed"),
					Engine:        strPtr(string(serverEngine)),
				}); err != nil {
				return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, ErrReadNotAllowed)
			}
			if _, err := p.commitPreExecution(ctx, mtx, result, traceID, conn.WorkspaceID); err != nil {
				return result, err
			}
		}
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	if policy.StatementTimeoutMs <= 0 || policy.MaxRows <= 0 {
		result.ErrorCode = ErrInternalError
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	// 阶段 C': Credential Resolver。失败时 E14-E16 审计 + $SECURITY_ALERT，Adapter 调用 0 次。
	payload, err := p.resolver.ResolveCredential(ctx, conn.WorkspaceID, conn.SecretRef, conn.SecretVersion)
	if err != nil {
		result.CredentialResolved = false
		result.ErrorCode = mapCredentialError(err)
		if mtx != nil {
			if err := p.recordCredentialFailure(ctx, mtx, exec, result, conn, traceID, now, err); err != nil {
				return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, result.ErrorCode)
			}
			if _, err := p.commitPreExecution(ctx, mtx, result, traceID, conn.WorkspaceID); err != nil {
				return result, err
			}
		}
		return result, fmt.Errorf("%w", result.ErrorCode)
	}
	result.CredentialResolved = true

	configRevision, err := connectionConfigRevision(conn)
	if err != nil {
		result.ErrorCode = ErrInternalError
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	// 阶段 D-0: execution running 更新并在执行前提交元数据库事务。
	// pending→running 更新或提交失败意味着元数据库故障，按审计失败处理
	// （返回 audit_failed + $SECURITY_ALERT），而非降级为 internal_error（vpvC7 outside）。
	if mtx != nil {
		exec.Status = metadata.ExecStatusRunning
		if err := mtx.UpdateExecution(ctx, conn.WorkspaceID, exec); err != nil {
			return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, ErrInternalError)
		}
		if err := mtx.Commit(); err != nil {
			return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, ErrInternalError)
		}
		mtx = nil
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
		if exec != nil {
			if err := p.recordPostExecution(ctx, exec, result, conn, traceID, now, statementHash); err != nil {
				return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, result.ErrorCode)
			}
		}
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
		if exec != nil {
			if err := p.recordPostExecution(ctx, exec, result, conn, traceID, now, statementHash); err != nil {
				return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, result.ErrorCode)
			}
		}
		return result, fmt.Errorf("%w", result.ErrorCode)
	}

	result.Result = queryResult
	if exec != nil {
		if err := p.recordPostExecution(ctx, exec, result, conn, traceID, now, statementHash); err != nil {
			return p.auditFailed(ctx, result, traceID, conn.WorkspaceID, result.ErrorCode)
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
	// ADR-017 §6：审计失败不向调用方返回查询结果。
	result.Result = nil
	return result, fmt.Errorf("%w (original error: %s)", result.ErrorCode, originalCode)
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
	default:
		// E15/E16: credential.decrypt 失败（system actor）
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
	// 否则 running execution 永不终结、E13 永不追加。
	if ctx.Err() != nil {
		ctx = context.WithoutCancel(ctx)
	}

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
	exec.ErrorCode = md.ErrorCode
	// finished_at 在 adapter 工作完成后采样，避免早于 started_at（Codex P1）。
	finished := p.clock()
	exec.FinishedAt = &finished
	if dur := finished.Sub(exec.StartedAt); dur > 0 {
		d := int(dur.Milliseconds())
		md.DurationMs = &d
		exec.DurationMs = &d
	}

	// 阶段 D 后：execution 更新独立事务提交（proposal §9.1）。
	mtx, err := p.txs.Begin(ctx)
	if err != nil {
		return err
	}
	defer mtx.Rollback()
	if err := mtx.UpdateExecution(ctx, conn.WorkspaceID, exec); err != nil {
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
	return p.audit.AppendAudit(ctx, event)
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

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

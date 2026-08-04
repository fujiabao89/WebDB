// Package connections 提供连接生命周期的最小 orchestration seam。
// 仅接入追加式审计（E1/E2/E7/E8），不实现公开 HTTP API。
package connections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fujiabao89/webdb/internal/adapter"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// StableErrorCode 连接服务稳定错误码。
type StableErrorCode string

func (e StableErrorCode) Error() string { return string(e) }

const (
	ErrConnectionNotFound StableErrorCode = "connection_not_found"
	ErrForbidden          StableErrorCode = "forbidden"
	ErrInternalError      StableErrorCode = "internal_error"
	ErrAuditFailed        StableErrorCode = "audit_failed"
)

// connectionTestTimeout 连接测试的连接级超时上限（CodeRabbit #10）。
// 即使调用方 ctx 没有 deadline，ping 也必须在有界时间内完成。
const connectionTestTimeout = 5 * time.Second

// auditWriteTimeout 审计持久化的独立超时上限（与调用方取消解耦，vpvC6）。
// 用户取消请求不阻止审计事件写入。
const auditWriteTimeout = 5 * time.Second

// atomicTxTimeout 原子事务的有界超时上限（WebDB 安全边界）：Create/Update 的
// BeginConnection/mutation/AppendAudit/Commit 前置统一绑定带 deadline 的 transaction
// context，防止调用方 ctx 无超时时事务操作无限等待；请求取消时事务回滚。
const atomicTxTimeout = 5 * time.Second

// Principal 由可信上游提供的已验证身份。
type Principal struct {
	UserID      uuid.UUID
	WorkspaceID uuid.UUID
}

// ConnectionTester 抽象连接测试能力（生产实现包装 adapter.PoolHandle.Ping）。
type ConnectionTester interface {
	Ping(ctx context.Context, cfg adapter.ConnectConfig) error
}

// AdapterPingTester 将 AdapterManager 收窄为 ConnectionTester。
type AdapterPingTester struct {
	Manager *adapter.AdapterManager
}

// Ping 获取连接并执行 Ping。
func (t AdapterPingTester) Ping(ctx context.Context, cfg adapter.ConnectConfig) error {
	handle, err := t.Manager.Get(ctx, cfg)
	if err != nil {
		return err
	}
	defer handle.Release()
	return handle.Ping(ctx)
}

// Service 连接生命周期服务（orchestration seam）。
// Create/Update 的元数据库 mutation 与对应 AuditEvent（E1/E2）在同一
// ConnectionAtomicTx 内原子提交（D11，WEB-25）；Test（E7/E8）为外部副作用例外，
// 保持 post-commit 语义。
type Service struct {
	conns                 metadata.ConnectionStore
	txstore               metadata.AtomicTxStore
	members               metadata.WorkspaceMemberStore
	audit                 metadata.AuditEventStore
	alarm                 metadata.SecurityAlarm
	resolver              credentials.CredentialResolver
	tester                ConnectionTester
	clock                 func() time.Time
	newTrace              func() string
	connectionTestTimeout time.Duration // 可注入的连接测试超时（vpvC5）
	auditWriteTimeout     time.Duration // 可注入的审计持久化超时（vtiLM）
	atomicTxTimeout       time.Duration // 可注入的原子事务有界超时（WebDB 安全边界）
	logger                *slog.Logger
}

// logStorageFailure 记录底层存储故障的根因（服务端日志）。
// 写入日志前对 err 消息统一脱敏，禁止原始敏感内容进入日志（vti-OS）。
func (s *Service) logStorageFailure(op string, wsID, connID uuid.UUID, err error) {
	s.logger.Error("connection service failure",
		"op", op, "workspace_id", wsID.String(), "connection_id", connID.String(),
		"error", metadata.RedactSensitive(err.Error()))
}

// auditEventBuildFailed 统一处理审计事件构建失败：返回 audit_failed 并触发
// $SECURITY_ALERT（与 writeAudit 一致，outside f）。
// 构建根因通过 logStorageFailure 保留（含操作上下文，脱敏，vti-OV）；
// 告警使用非取消 context + 独立超时，即使调用方取消也发出 audit_failed（vti-OV）。
func (s *Service) auditEventBuildFailed(ctx context.Context, wsID uuid.UUID, buildErr error) error {
	s.logStorageFailure("audit_event_build_failed", wsID, uuid.Nil, buildErr)
	alarmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.auditWriteTimeout)
	defer cancel()
	metadata.EmitAlarm(s.alarm, alarmCtx, metadata.SecurityAlertEvent{
		TraceID:     s.newTrace(),
		WorkspaceID: wsID,
		Code:        string(ErrAuditFailed),
		OccurredAt:  s.clock(),
	})
	return fmt.Errorf("%w: audit event build failed", ErrAuditFailed)
}

// NewService 创建连接服务。
func NewService(
	conns metadata.ConnectionStore,
	txstore metadata.AtomicTxStore,
	members metadata.WorkspaceMemberStore,
	audit metadata.AuditEventStore,
	alarm metadata.SecurityAlarm,
	resolver credentials.CredentialResolver,
	tester ConnectionTester,
) *Service {
	return &Service{
		conns:                 conns,
		txstore:               txstore,
		members:               members,
		audit:                 audit,
		alarm:                 alarm,
		resolver:              resolver,
		tester:                tester,
		clock:                 func() time.Time { return time.Now().UTC() },
		newTrace:              func() string { return uuid.NewString() },
		connectionTestTimeout: connectionTestTimeout,
		auditWriteTimeout:     auditWriteTimeout,
		atomicTxTimeout:       atomicTxTimeout,
		logger:                slog.Default(),
	}
}

// Create 创建连接并写入 E1 connection.create。mutation（CreateConnection）与 E1 审计
// 在同一 ConnectionAtomicTx 内原子提交；审计失败整体回滚（无连接残留）。
func (s *Service) Create(ctx context.Context, p Principal, conn *metadata.Connection) (*metadata.Connection, error) {
	if err := s.requireManager(ctx, p); err != nil {
		return nil, err
	}
	conn.WorkspaceID = p.WorkspaceID
	conn.CreatedBy = p.UserID
	// 连接 ID 由服务端预生成（原子事务 op 需在 BeginConnection 前持有非零 ID）。
	if conn.ID == uuid.Nil {
		conn.ID = uuid.New()
	}
	traceID := s.newTrace()
	op, err := metadata.NewOperationContext(
		s.newTrace(), p.WorkspaceID, "connection", conn.ID.String(),
		metadata.ActionConnectionCreate, &conn.ID, p.UserID, "user",
		string(metadata.OutcomeSucceeded), traceID,
	)
	if err != nil {
		return nil, s.auditEventBuildFailed(ctx, conn.WorkspaceID, err)
	}
	// 原子事务使用有界 transaction context（WebDB 安全边界）：Begin/mutation/AppendAudit
	// 统一绑定带 deadline 的 ctx，防止调用方 ctx 无超时时事务操作无限等待。
	txCtx, txCancel := context.WithTimeout(ctx, s.atomicTxTimeout)
	defer txCancel()
	atx, err := s.txstore.BeginConnection(txCtx, op)
	if err != nil {
		s.logStorageFailure("create.begin_connection", p.WorkspaceID, conn.ID, err)
		return nil, fmt.Errorf("%w: create connection failed", ErrInternalError)
	}
	defer atx.Rollback()
	if err := atx.CreateConnection(txCtx, conn); err != nil {
		// 根因仅进服务端日志，返回保持脱敏（VuXZG）。
		s.logStorageFailure("create.connection", p.WorkspaceID, conn.ID, err)
		return nil, fmt.Errorf("%w: create connection failed", ErrInternalError)
	}

	event, err := newConnectionAuditEvent(
		p, conn.ID, metadata.ActionConnectionCreate, "connection", conn.ID.String(),
		metadata.OutcomeSucceeded,
		metadata.AuditMetadata{
			Engine:      strPtr(string(conn.Engine)),
			Environment: strPtr(string(conn.Environment)),
		},
		traceID, s.clock(),
	)
	if err != nil {
		return nil, s.auditEventBuildFailed(ctx, conn.WorkspaceID, err)
	}
	if err := atx.AppendAudit(txCtx, op, event); err != nil {
		return nil, s.auditAppendFailed(ctx, conn.WorkspaceID, err)
	}
	if err := atx.Commit(); err != nil {
		s.logStorageFailure("create.commit_connection", p.WorkspaceID, conn.ID, err)
		return nil, fmt.Errorf("%w: create connection failed", ErrInternalError)
	}
	return conn, nil
}

// Update 更新连接并写入 E2 connection.update。mutation（UpdateConnection）与 E2 审计
// 在同一 ConnectionAtomicTx 内原子提交；审计失败整体回滚（更新不残留）。
func (s *Service) Update(ctx context.Context, p Principal, conn *metadata.Connection) error {
	if err := s.requireManager(ctx, p); err != nil {
		return err
	}
	conn.WorkspaceID = p.WorkspaceID
	// 空 ID 是调用方输入错误，直接返回连接类稳定错误；不得映射为 audit_failed 或
	// 触发 $SECURITY_ALERT（CodeRabbit-2：无效输入被误报为审计子系统故障）。
	if conn.ID == uuid.Nil {
		return fmt.Errorf("%w: connection id required", ErrConnectionNotFound)
	}
	traceID := s.newTrace()
	op, err := metadata.NewOperationContext(
		s.newTrace(), p.WorkspaceID, "connection", conn.ID.String(),
		metadata.ActionConnectionUpdate, &conn.ID, p.UserID, "user",
		string(metadata.OutcomeSucceeded), traceID,
	)
	if err != nil {
		return s.auditEventBuildFailed(ctx, conn.WorkspaceID, err)
	}
	// 原子事务使用有界 transaction context（WebDB 安全边界）。
	txCtx, txCancel := context.WithTimeout(ctx, s.atomicTxTimeout)
	defer txCancel()
	atx, err := s.txstore.BeginConnection(txCtx, op)
	if err != nil {
		s.logStorageFailure("update.begin_connection", p.WorkspaceID, conn.ID, err)
		return fmt.Errorf("%w: update connection failed", ErrInternalError)
	}
	defer atx.Rollback()
	if err := atx.UpdateConnection(txCtx, p.WorkspaceID, conn); err != nil {
		// 根因仅进服务端日志，返回保持脱敏（VuXZG）。
		s.logStorageFailure("update.connection", p.WorkspaceID, conn.ID, err)
		return fmt.Errorf("%w: update connection failed", ErrInternalError)
	}

	event, err := newConnectionAuditEvent(
		p, conn.ID, metadata.ActionConnectionUpdate, "connection", conn.ID.String(),
		metadata.OutcomeSucceeded,
		metadata.AuditMetadata{Environment: strPtr(string(conn.Environment))},
		traceID, s.clock(),
	)
	if err != nil {
		return s.auditEventBuildFailed(ctx, conn.WorkspaceID, err)
	}
	if err := atx.AppendAudit(txCtx, op, event); err != nil {
		return s.auditAppendFailed(ctx, conn.WorkspaceID, err)
	}
	if err := atx.Commit(); err != nil {
		s.logStorageFailure("update.commit_connection", p.WorkspaceID, conn.ID, err)
		return fmt.Errorf("%w: update connection failed", ErrInternalError)
	}
	return nil
}

// Test 测试连接连通性。成功写入 E7，失败写入 E8。
// 与 Create/Update 一致，先校验调用者为工作区 owner/admin，
// 避免非成员触发凭证解析与外发数据库 ping（Codex P1）。
func (s *Service) Test(ctx context.Context, p Principal, connID uuid.UUID) error {
	if err := s.requireManager(ctx, p); err != nil {
		return err
	}
	conn, err := s.conns.ConnectionByID(ctx, p.WorkspaceID, connID)
	if err != nil {
		// 仅 sql.ErrNoRows 视为连接不存在；超时/池耗尽等元数据库故障映射为 internal_error，
		// 根因写入服务端结构化日志（不含凭证/明文，vtiLQ），避免掩盖存储故障（vpvC6）。
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: connection not found", ErrConnectionNotFound)
		}
		s.logStorageFailure("test.connection_by_id", p.WorkspaceID, connID, err)
		return fmt.Errorf("%w: connection lookup failed", ErrInternalError)
	}

	payload, err := s.resolver.ResolveCredential(ctx, conn.WorkspaceID, conn.SecretRef, conn.SecretVersion)
	if err != nil {
		code := mapCredentialError(err)
		// 只生成一次 trace，供告警与审计事件共用，确保两条记录可关联（outside e）。
		traceID := s.newTrace()
		// 告警与审计使用非取消 context + 独立超时，避免调用方取消丢失审计（outside e）。
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.auditWriteTimeout)
		defer cancel()
		// 仅解密类失败触发安全告警（Qodo #2 / CodeRabbit #8），与凭证层一致；
		// credential_not_found/credential_retired 等查找类失败仅记录 E8，不升级告警。
		if credentials.IsDecryptFailureCode(credentials.ErrorCode(code)) {
			metadata.EmitAlarm(s.alarm, auditCtx, metadata.SecurityAlertEvent{
				TraceID:     traceID,
				WorkspaceID: conn.WorkspaceID,
				Code:        code,
				OccurredAt:  s.clock(),
			})
		}
		event, buildErr := newConnectionAuditEvent(
			p, conn.ID, metadata.ActionConnectionTest, "connection", conn.ID.String(),
			metadata.OutcomeFailed,
			metadata.AuditMetadata{
				Engine:      strPtr(string(conn.Engine)),
				Environment: strPtr(string(conn.Environment)),
				ErrorCode:   strPtr(code),
			},
			traceID, s.clock(),
		)
		if buildErr != nil {
			return s.auditEventBuildFailed(auditCtx, conn.WorkspaceID, buildErr)
		}
		if writeErr := s.writeAudit(auditCtx, event, conn.WorkspaceID); writeErr != nil {
			return writeErr
		}
		// 返回由映射得到的稳定错误码，脱敏消息，不拼接原始凭证错误（CodeRabbit #9）。
		return fmt.Errorf("%w: credential resolution failed", StableErrorCode(code))
	}

	cfg := adapter.ConnectConfig{
		ConnectionID:  conn.ID.String(),
		SecretVersion: conn.SecretVersion,
		Engine:        adapter.Engine(conn.Engine),
		Host:          conn.Host,
		Port:          conn.Port,
		User:          payload.User,
		Password:      payload.Password,
		Database:      conn.Database,
		TLS:           adapter.TLSRequire,
	}

	// 为 ping 派生带上限的 context，即使调用方 ctx 无 deadline 也避免无限挂起（CodeRabbit #10）。
	// 超时上限可注入（vpvC5），便于测试验证真实 deadline 行为。
	pingCtx, cancel := context.WithTimeout(ctx, s.connectionTestTimeout)
	defer cancel()

	start := s.clock()
	testErr := s.tester.Ping(pingCtx, cfg)
	durationMs := int(s.clock().Sub(start).Milliseconds())
	if durationMs < 0 {
		durationMs = 0
	}

	outcome := metadata.OutcomeSucceeded
	md := metadata.AuditMetadata{
		Engine:      strPtr(string(conn.Engine)),
		Environment: strPtr(string(conn.Environment)),
		DurationMs:  intPtr(durationMs),
	}
	if testErr != nil {
		outcome = metadata.OutcomeFailed
		md.ErrorCode = strPtr(mapConnectionTestError(testErr))
	}

	event, err := newConnectionAuditEvent(
		p, conn.ID, metadata.ActionConnectionTest, "connection", conn.ID.String(),
		outcome, md, s.newTrace(), s.clock(),
	)
	if err != nil {
		return s.auditEventBuildFailed(ctx, conn.WorkspaceID, err)
	}
	if err := s.writeAudit(ctx, event, conn.WorkspaceID); err != nil {
		return err
	}
	if testErr != nil {
		// 返回稳定错误码，脱敏消息，不拼接原始 ping 错误（CodeRabbit #9）。
		return fmt.Errorf("%w", StableErrorCode(mapConnectionTestError(testErr)))
	}
	return nil
}

// auditAppendFailed 处理原子事务内审计追加失败：触发告警并返回 audit_failed
// （事务由调用方 defer Rollback 回滚，mutation 无残留）。
func (s *Service) auditAppendFailed(ctx context.Context, wsID uuid.UUID, err error) error {
	alarmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.auditWriteTimeout)
	defer cancel()
	metadata.EmitAlarm(s.alarm, alarmCtx, metadata.SecurityAlertEvent{
		TraceID:     s.newTrace(),
		WorkspaceID: wsID,
		Code:        string(ErrAuditFailed),
		OccurredAt:  s.clock(),
	})
	return fmt.Errorf("%w: audit write failed", ErrAuditFailed)
}

// writeAudit 写审计；失败触发安全告警并返回 audit_failed。
// 审计持久化与调用方取消解耦（vpvC6）：即使请求 ctx 已取消，审计事件仍须写入，
// 使用 context.WithoutCancel + 独立超时避免无界阻塞；超时上限可注入（vtiLM）。
func (s *Service) writeAudit(ctx context.Context, event *metadata.AuditEvent, wsID uuid.UUID) error {
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.auditWriteTimeout)
	defer cancel()
	if err := s.audit.AppendAudit(auditCtx, event); err != nil {
		metadata.EmitAlarm(s.alarm, auditCtx, metadata.SecurityAlertEvent{
			TraceID:     event.TraceID,
			WorkspaceID: wsID,
			Code:        string(ErrAuditFailed),
			// 统一告警时间戳来源为服务时钟（CodeRabbit #11），与凭证层 audited.go 一致。
			OccurredAt: s.clock(),
		})
		return fmt.Errorf("%w: audit write failed", ErrAuditFailed)
	}
	return nil
}

// requireManager 校验调用者为工作区 owner/admin 成员。
func (s *Service) requireManager(ctx context.Context, p Principal) error {
	if s.members == nil {
		return fmt.Errorf("%w: members store not configured", ErrInternalError)
	}
	member, err := s.members.MemberByWorkspaceAndUser(ctx, p.WorkspaceID, p.UserID)
	if err != nil {
		// 成员存储故障与真正的权限拒绝区分：根因仅进服务端日志（VuXZG），
		// 返回保持脱敏 forbidden。
		s.logStorageFailure("require_manager.member_lookup", p.WorkspaceID, uuid.Nil, err)
		return fmt.Errorf("%w: member lookup failed", ErrForbidden)
	}
	if member == nil {
		return fmt.Errorf("%w", ErrForbidden)
	}
	switch member.Role {
	case metadata.RoleOwner, metadata.RoleAdmin:
		return nil
	default:
		return fmt.Errorf("%w", ErrForbidden)
	}
}

// newConnectionAuditEvent 构建连接审计事件（user actor）。
func newConnectionAuditEvent(
	p Principal,
	connID uuid.UUID,
	action, resourceType, resourceID string,
	outcome metadata.AuditOutcome,
	md metadata.AuditMetadata,
	traceID string,
	occurredAt time.Time,
) (*metadata.AuditEvent, error) {
	raw, err := md.Marshal()
	if err != nil {
		return nil, err
	}
	actorID := p.UserID
	connRef := connID
	return &metadata.AuditEvent{
		WorkspaceID:  p.WorkspaceID,
		ActorType:    metadata.ActorTypeUser,
		ActorID:      &actorID,
		ConnectionID: &connRef,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Outcome:      outcome,
		Metadata:     raw,
		TraceID:      traceID,
		OccurredAt:   occurredAt,
	}, nil
}

func mapCredentialError(err error) string {
	if credentials.IsErrorCode(err, credentials.ErrCredentialNotFound) {
		return string(credentials.ErrCredentialNotFound)
	}
	if credentials.IsErrorCode(err, credentials.ErrCredentialRetired) {
		return string(credentials.ErrCredentialRetired)
	}
	if credentials.IsErrorCode(err, credentials.ErrDecryptionFailed) {
		return string(credentials.ErrDecryptionFailed)
	}
	if credentials.IsErrorCode(err, credentials.ErrUnknownKEKVersion) {
		return string(credentials.ErrUnknownKEKVersion)
	}
	return string(credentials.ErrInternalError)
}

func mapConnectionTestError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "execution_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "query_cancelled"
	}
	var adapterErr *adapter.AdapterError
	if errors.As(err, &adapterErr) {
		return string(adapterErr.Code)
	}
	return "connection_failed"
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

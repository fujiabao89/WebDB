package credentials

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// ErrAuditFailed 凭证生命周期审计写入失败稳定错误码（ADR-017 §6）。
const ErrAuditFailed ErrorCode = "audit_failed"

// auditWriteTimeout 审计持久化的独立超时上限（与调用方取消解耦，用于失败路径审计与告警）。
const auditWriteTimeout = 5 * time.Second

// credentialReader 解析所需的信封读取能力（收窄依赖，仅 EnvelopeByRef）。
type credentialReader interface {
	EnvelopeByRef(ctx context.Context, wsID, secretRef uuid.UUID, version int) (*metadata.CredentialEnvelope, error)
}

// LifecycleManager 管理凭证创建、解析、轮换和退役。
// 它是审计协调器（WEB-25 设计 §3）：credential mutation（Create/Rotate/Retire）与
// 对应 AuditEvent（E3-E6）在同一 CredentialAtomicTx 内原子提交（D11）。成功路径审计
// 通过 atx.AppendAudit 与 mutation 同事务提交；失败路径审计（E5 rotate failed、
// E14-E16 resolve 失败）通过 audit 存储后置写入。
type LifecycleManager struct {
	read              credentialReader // EnvelopeByRef（Resolve 读取）
	txstore           metadata.AtomicTxStore
	audit             metadata.AuditEventStore // 失败路径审计（E5/E14-E16）
	kek               KEKProvider
	alarm             metadata.SecurityAlarm
	clock             func() time.Time
	newTrace          func() string
	auditWriteTimeout time.Duration
	logger            *slog.Logger
}

// NewLifecycleManager 创建 LifecycleManager（审计协调器）。
func NewLifecycleManager(
	read credentialReader,
	txstore metadata.AtomicTxStore,
	audit metadata.AuditEventStore,
	kek KEKProvider,
	alarm metadata.SecurityAlarm,
	logger ...*slog.Logger,
) *LifecycleManager {
	l := slog.Default()
	if len(logger) > 0 && logger[0] != nil {
		l = logger[0]
	}
	return &LifecycleManager{
		read:              read,
		txstore:           txstore,
		audit:             audit,
		kek:               kek,
		alarm:             alarm,
		clock:             func() time.Time { return time.Now().UTC() },
		newTrace:          func() string { return uuid.NewString() },
		auditWriteTimeout: auditWriteTimeout,
		logger:            l,
	}
}

// auditContext 从调用方 ctx 派生审计持久化上下文：剥离取消信号并加上独立超时，
// 确保失败路径审计/告警即使客户端断开仍被写入。
func (m *LifecycleManager) auditContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), m.auditWriteTimeout)
}

// logStorageFailure 记录底层存储/KEK 故障的根因（服务端日志），
// 保持对外返回的稳定错误码脱敏；写入日志前统一脱敏。
func (m *LifecycleManager) logStorageFailure(op string, workspaceID, secretRef uuid.UUID, err error) {
	m.logger.Error("credential lifecycle failure",
		"op", op, "workspace_id", workspaceID.String(), "secret_ref", secretRef.String(),
		"error", metadata.RedactSensitive(err.Error()))
}

// auditEventBuildFailed 统一处理审计事件构建失败：返回 audit_failed 并触发 $SECURITY_ALERT。
func (m *LifecycleManager) auditEventBuildFailed(ctx context.Context, wsID uuid.UUID, buildErr error) error {
	auditCtx, cancel := m.auditContext(ctx)
	defer cancel()
	metadata.EmitAlarm(m.alarm, auditCtx, metadata.SecurityAlertEvent{
		TraceID:     m.newTrace(),
		WorkspaceID: wsID,
		Code:        string(ErrAuditFailed),
		OccurredAt:  m.clock(),
	})
	return fmt.Errorf("%w: audit event build failed", ErrAuditFailed)
}

// auditAppendFailed 处理原子事务内审计追加失败：触发告警并返回 audit_failed
// （事务由调用方 defer Rollback 回滚，mutation 无残留）。
func (m *LifecycleManager) auditAppendFailed(ctx context.Context, wsID uuid.UUID, err error) error {
	alarmCtx, cancel := m.auditContext(ctx)
	defer cancel()
	metadata.EmitAlarm(m.alarm, alarmCtx, metadata.SecurityAlertEvent{
		TraceID:     m.newTrace(),
		WorkspaceID: wsID,
		Code:        string(ErrAuditFailed),
		OccurredAt:  m.clock(),
	})
	return fmt.Errorf("%w: audit write failed", ErrAuditFailed)
}

// writeFailureAudit 失败路径后置审计写入（不参与原子事务）；失败触发告警并返回 audit_failed。
func (m *LifecycleManager) writeFailureAudit(ctx context.Context, event *metadata.AuditEvent) error {
	auditCtx, cancel := m.auditContext(ctx)
	defer cancel()
	if err := m.audit.AppendAudit(auditCtx, event); err != nil {
		alarmCtx, alarmCancel := m.auditContext(ctx)
		defer alarmCancel()
		metadata.EmitAlarm(m.alarm, alarmCtx, metadata.SecurityAlertEvent{
			TraceID:     event.TraceID,
			WorkspaceID: event.WorkspaceID,
			Code:        string(ErrAuditFailed),
			OccurredAt:  m.clock(),
		})
		return fmt.Errorf("%w: audit write failed", ErrAuditFailed)
	}
	return nil
}

// Create 创建新凭证并返回 Envelope。mutation（InsertEnvelope）与 E3 审计在同一
// CredentialAtomicTx 内原子提交；审计失败则整体回滚（无 envelope 残留）。
func (m *LifecycleManager) Create(ctx context.Context, wsID, actorID uuid.UUID, payload CredentialPayload) (*metadata.CredentialEnvelope, error) {
	secretRef := uuid.New()
	traceID := m.newTrace()
	op, err := metadata.NewOperationContext(
		m.newTrace(), wsID, "credential", secretRef.String(),
		metadata.ActionCredentialCreate, nil, actorID, "user",
		string(metadata.OutcomeSucceeded), traceID,
	)
	if err != nil {
		return nil, m.auditEventBuildFailed(ctx, wsID, err)
	}
	atx, err := m.txstore.BeginCredential(ctx, op)
	if err != nil {
		m.logStorageFailure("create.begin", wsID, secretRef, err)
		return nil, fmt.Errorf("%w: internal failure", ErrInternalError)
	}
	defer atx.Rollback()

	ver, kekKey, err := m.kek.ActiveKEK()
	if err != nil {
		m.logStorageFailure("create.active_kek", wsID, secretRef, err)
		return nil, fmt.Errorf("%w: internal failure", ErrInternalError)
	}
	if err := m.kek.ReserveWrap(ver); err != nil {
		return nil, err
	}

	env, err := SealEnvelope(payload, wsID, secretRef, 1, SuiteAES256GCMv1, ver, kekKey, rand.Reader)
	if err != nil {
		return nil, err
	}

	if err := atx.InsertEnvelope(ctx, env); err != nil {
		m.logStorageFailure("create.insert_envelope", wsID, secretRef, err)
		return nil, fmt.Errorf("%w: internal failure", ErrInternalError)
	}

	event, buildErr := newLifecycleAuditEvent(
		wsID, metadata.ActorTypeUser, &actorID, nil, nil,
		metadata.ActionCredentialCreate, "credential", secretRef.String(),
		metadata.OutcomeSucceeded,
		metadata.AuditMetadata{
			SecretRef:     strPtr(secretRef.String()),
			SecretVersion: intPtr(env.Version),
			EnvelopeSuite: strPtr(env.EnvelopeSuite),
			KEKVersion:    intPtr(env.KEKVersion),
		},
		traceID, m.clock(),
	)
	if buildErr != nil {
		return nil, m.auditEventBuildFailed(ctx, wsID, buildErr)
	}
	if err := atx.AppendAudit(ctx, op, event); err != nil {
		return nil, m.auditAppendFailed(ctx, wsID, err)
	}
	if err := atx.Commit(); err != nil {
		m.logStorageFailure("create.commit", wsID, secretRef, err)
		return nil, fmt.Errorf("%w: internal failure", ErrInternalError)
	}
	return env, nil
}

// Resolve 解析指定版本的凭证。普通执行路径：retired_at 非空时返回 credential_retired。
// 任一失败均记录 E14-E16 并触发安全告警（decrypt 类失败）。
func (m *LifecycleManager) Resolve(ctx context.Context, wsID, secretRef uuid.UUID, version int) (CredentialPayload, error) {
	env, err := m.read.EnvelopeByRef(ctx, wsID, secretRef, version)
	if err != nil {
		if errors.Is(err, metadata.ErrEnvelopeNotFound) {
			re := fmt.Errorf("%w: credential not found", ErrCredentialNotFound)
			m.writeResolveFailureAudit(ctx, wsID, secretRef, version, re)
			return CredentialPayload{}, re
		}
		m.logStorageFailure("resolve.envelope_by_ref", wsID, secretRef, err)
		re := fmt.Errorf("%w: internal failure", ErrInternalError)
		m.writeResolveFailureAudit(ctx, wsID, secretRef, version, re)
		return CredentialPayload{}, re
	}

	if env.RetiredAt != nil {
		re := fmt.Errorf("%w: version %d retired", ErrCredentialRetired, version)
		m.writeResolveFailureAudit(ctx, wsID, secretRef, version, re)
		return CredentialPayload{}, re
	}

	kek, err := m.kek.GetKEK(env.KEKVersion)
	if err != nil {
		m.logStorageFailure("resolve.get_kek", wsID, secretRef, err)
		re := &KEKVersionError{
			Version: env.KEKVersion,
			err:     fmt.Errorf("%w: unknown kek version", ErrUnknownKEKVersion),
		}
		m.writeResolveFailureAudit(ctx, wsID, secretRef, version, re)
		return CredentialPayload{}, re
	}

	payload, openErr := OpenEnvelope(env, wsID, secretRef, kek)
	if openErr != nil {
		m.writeResolveFailureAudit(ctx, wsID, secretRef, version, openErr)
		return payload, openErr
	}
	return payload, nil
}

// writeResolveFailureAudit 后置写入 Resolve 失败审计（E14 查找 / E15-E16 解密），
// decrypt 类失败触发安全告警。
func (m *LifecycleManager) writeResolveFailureAudit(ctx context.Context, wsID, secretRef uuid.UUID, version int, err error) {
	code := credentialErrorCode(err)
	var action string
	var md metadata.AuditMetadata
	switch code {
	case ErrCredentialNotFound, ErrCredentialRetired, ErrInternalError:
		action = metadata.ActionCredentialLookup
		md = metadata.AuditMetadata{
			SecretRef: strPtr(secretRef.String()),
			ErrorCode: strPtr(string(code)),
		}
	default:
		action = metadata.ActionCredentialDecrypt
		md = metadata.AuditMetadata{
			SecretRef:     strPtr(secretRef.String()),
			SecretVersion: intPtr(version),
			ErrorCode:     strPtr(string(code)),
		}
		var kekErr *KEKVersionError
		if errors.As(err, &kekErr) {
			md.KEKVersion = intPtr(kekErr.Version)
		}
		if IsDecryptFailureCode(code) {
			alarmCtx, alarmCancel := m.auditContext(ctx)
			defer alarmCancel()
			metadata.EmitAlarm(m.alarm, alarmCtx, metadata.SecurityAlertEvent{
				TraceID:     m.newTrace(),
				WorkspaceID: wsID,
				Code:        string(code),
				OccurredAt:  m.clock(),
			})
		}
	}
	event, buildErr := newLifecycleAuditEvent(
		wsID, metadata.ActorTypeSystem, nil, nil, nil,
		action, "credential", secretRef.String(),
		metadata.OutcomeFailed, md, m.newTrace(), m.clock(),
	)
	if buildErr != nil {
		_ = m.auditEventBuildFailed(ctx, wsID, buildErr)
		return
	}
	_ = m.writeFailureAudit(ctx, event)
}

// Rotate 轮换凭证：在事务中创建新版本、更新连接引用，并与 E4 审计原子提交。
// 成功：E4 与 mutation 同事务提交。失败：事务回滚，E5 后置写入。
func (m *LifecycleManager) Rotate(ctx context.Context, wsID, actorID, secretRef uuid.UUID, expectedVersion int, newPayload CredentialPayload) (*metadata.CredentialEnvelope, error) {
	traceID := m.newTrace()
	op, err := metadata.NewOperationContext(
		m.newTrace(), wsID, "credential", secretRef.String(),
		metadata.ActionCredentialRotate, nil, actorID, "user",
		string(metadata.OutcomeSucceeded), traceID,
	)
	if err != nil {
		return nil, m.auditEventBuildFailed(ctx, wsID, err)
	}
	atx, err := m.txstore.BeginCredential(ctx, op)
	if err != nil {
		m.logStorageFailure("rotate.begin", wsID, secretRef, err)
		return nil, fmt.Errorf("%w: internal failure", ErrInternalError)
	}
	defer atx.Rollback()

	env, err := atx.LockEnvelopeForUpdate(ctx, wsID, secretRef)
	if err != nil {
		if errors.Is(err, metadata.ErrEnvelopeNotFound) {
			m.writeRotateFailedAudit(ctx, wsID, actorID, secretRef, expectedVersion, fmt.Errorf("%w: credential not found", ErrCredentialNotFound))
			return nil, fmt.Errorf("%w: credential not found", ErrCredentialNotFound)
		}
		m.logStorageFailure("rotate.lock_envelope", wsID, secretRef, err)
		return nil, fmt.Errorf("%w: internal failure", ErrInternalError)
	}
	if env.Version != expectedVersion {
		vce := &VersionConflictError{
			Expected: expectedVersion,
			Actual:   env.Version,
			err:      fmt.Errorf("%w", ErrVersionConflict),
		}
		m.writeRotateFailedAudit(ctx, wsID, actorID, secretRef, expectedVersion, vce)
		return nil, vce
	}
	if env.RetiredAt != nil {
		re := fmt.Errorf("%w: version %d retired", ErrCredentialRetired, env.Version)
		m.writeRotateFailedAudit(ctx, wsID, actorID, secretRef, expectedVersion, re)
		return nil, re
	}

	newVersion := env.Version + 1
	ver, kekKey, err := m.kek.ActiveKEK()
	if err != nil {
		m.logStorageFailure("rotate.active_kek", wsID, secretRef, err)
		return nil, fmt.Errorf("%w: internal failure", ErrInternalError)
	}
	if err := m.kek.ReserveWrap(ver); err != nil {
		m.logStorageFailure("rotate.reserve_wrap", wsID, secretRef, err)
		return nil, err
	}

	newEnv, err := SealEnvelope(newPayload, wsID, secretRef, newVersion, SuiteAES256GCMv1, ver, kekKey, rand.Reader)
	if err != nil {
		return nil, err
	}

	if err := atx.InsertEnvelope(ctx, newEnv); err != nil {
		m.logStorageFailure("rotate.insert_envelope", wsID, secretRef, err)
		return nil, fmt.Errorf("%w: internal failure", ErrInternalError)
	}
	if err := atx.UpdateConnectionVersion(ctx, wsID, secretRef, newVersion); err != nil {
		m.logStorageFailure("rotate.update_connection_version", wsID, secretRef, err)
		return nil, fmt.Errorf("%w: internal failure", ErrInternalError)
	}

	event, buildErr := newRotateSucceededEvent(wsID, actorID, secretRef, expectedVersion, newEnv, traceID, m.clock())
	if buildErr != nil {
		return nil, m.auditEventBuildFailed(ctx, wsID, buildErr)
	}
	if err := atx.AppendAudit(ctx, op, event); err != nil {
		return nil, m.auditAppendFailed(ctx, wsID, err)
	}
	if err := atx.Commit(); err != nil {
		m.logStorageFailure("rotate.commit", wsID, secretRef, err)
		return nil, fmt.Errorf("%w: internal failure", ErrInternalError)
	}
	return newEnv, nil
}

// writeRotateFailedAudit 后置写入 E5 credential.rotate failed 审计。
func (m *LifecycleManager) writeRotateFailedAudit(ctx context.Context, wsID, actorID, secretRef uuid.UUID, expectedVersion int, err error) {
	event, buildErr := newRotateFailedEvent(wsID, actorID, secretRef, expectedVersion, err, m.newTrace(), m.clock())
	if buildErr != nil {
		_ = m.auditEventBuildFailed(ctx, wsID, buildErr)
		return
	}
	_ = m.writeFailureAudit(ctx, event)
}

// Retire 退役指定版本。被连接引用时拒绝。mutation（UpdateRetiredAt）与 E6 审计
// 在同一 CredentialAtomicTx 内原子提交。
func (m *LifecycleManager) Retire(ctx context.Context, wsID, actorID, secretRef uuid.UUID, version int) error {
	traceID := m.newTrace()
	op, err := metadata.NewOperationContext(
		m.newTrace(), wsID, "credential", secretRef.String(),
		metadata.ActionCredentialRetire, nil, actorID, "user",
		string(metadata.OutcomeSucceeded), traceID,
	)
	if err != nil {
		return m.auditEventBuildFailed(ctx, wsID, err)
	}
	atx, err := m.txstore.BeginCredential(ctx, op)
	if err != nil {
		m.logStorageFailure("retire.begin", wsID, secretRef, err)
		return fmt.Errorf("%w: internal failure", ErrInternalError)
	}
	defer atx.Rollback()

	env, err := atx.LockEnvelopeVersion(ctx, wsID, secretRef, version)
	if err != nil {
		if errors.Is(err, metadata.ErrEnvelopeNotFound) {
			return fmt.Errorf("%w: credential not found", ErrCredentialNotFound)
		}
		m.logStorageFailure("retire.lock_envelope_version", wsID, secretRef, err)
		return fmt.Errorf("%w: internal failure", ErrInternalError)
	}

	if env.RetiredAt == nil {
		count, err := atx.CountConnectionsByVersion(ctx, wsID, secretRef, version)
		if err != nil {
			m.logStorageFailure("retire.count_connections", wsID, secretRef, err)
			return fmt.Errorf("%w: internal failure", ErrInternalError)
		}
		if count > 0 {
			return fmt.Errorf("%w: %d connections still reference version %d", ErrCredentialInUse, count, version)
		}

		if err := atx.UpdateRetiredAt(ctx, wsID, secretRef, version); err != nil {
			m.logStorageFailure("retire.update_retired_at", wsID, secretRef, err)
			return fmt.Errorf("%w: internal failure", ErrInternalError)
		}
	}
	// 幂等：已退役时无新 mutation，仍追加 E6 并通过审计闸门提交。

	event, buildErr := newRetireSucceededEvent(wsID, actorID, secretRef, version, traceID, m.clock())
	if buildErr != nil {
		return m.auditEventBuildFailed(ctx, wsID, buildErr)
	}
	if err := atx.AppendAudit(ctx, op, event); err != nil {
		return m.auditAppendFailed(ctx, wsID, err)
	}
	if err := atx.Commit(); err != nil {
		m.logStorageFailure("retire.commit", wsID, secretRef, err)
		return fmt.Errorf("%w: internal failure", ErrInternalError)
	}
	return nil
}

// ---- 与 execution/adapter 的集成接口 ------------------------------------------

// CredentialResolver 为执行管线提供凭证解析能力。
type CredentialResolver interface {
	ResolveCredential(ctx context.Context, workspaceID, secretRef uuid.UUID, secretVersion int) (CredentialPayload, error)
}

// ResolveCredential 实现 CredentialResolver 接口。
func (m *LifecycleManager) ResolveCredential(ctx context.Context, workspaceID, secretRef uuid.UUID, secretVersion int) (CredentialPayload, error) {
	return m.Resolve(ctx, workspaceID, secretRef, secretVersion)
}

// 编译时检查
var _ CredentialResolver = (*LifecycleManager)(nil)

// ---- 审计事件构建 -------------------------------------------------------------

// newRotateSucceededEvent 构建 E4 credential.rotate succeeded 事件。
func newRotateSucceededEvent(wsID, actorID, secretRef uuid.UUID, expectedVersion int, env *metadata.CredentialEnvelope, traceID string, occurredAt time.Time) (*metadata.AuditEvent, error) {
	return newLifecycleAuditEvent(
		wsID, metadata.ActorTypeUser, &actorID, nil, nil,
		metadata.ActionCredentialRotate, "credential", secretRef.String(),
		metadata.OutcomeSucceeded,
		metadata.AuditMetadata{
			SecretRef:     strPtr(secretRef.String()),
			OldVersion:    intPtr(expectedVersion),
			NewVersion:    intPtr(env.Version),
			EnvelopeSuite: strPtr(env.EnvelopeSuite),
			KEKVersion:    intPtr(env.KEKVersion),
		},
		traceID, occurredAt,
	)
}

// newRotateFailedEvent 构建 E5 credential.rotate failed 事件。
func newRotateFailedEvent(wsID, actorID, secretRef uuid.UUID, expectedVersion int, err error, traceID string, occurredAt time.Time) (*metadata.AuditEvent, error) {
	code := credentialErrorCode(err)
	md := metadata.AuditMetadata{
		SecretRef: strPtr(secretRef.String()),
		ErrorCode: strPtr(string(code)),
	}
	if code == ErrVersionConflict {
		md.ExpectedVersion = intPtr(expectedVersion)
		var vce *VersionConflictError
		if errors.As(err, &vce) {
			md.ActualVersion = intPtr(vce.Actual)
		}
	}
	return newLifecycleAuditEvent(
		wsID, metadata.ActorTypeUser, &actorID, nil, nil,
		metadata.ActionCredentialRotate, "credential", secretRef.String(),
		metadata.OutcomeFailed, md, traceID, occurredAt,
	)
}

// newRetireSucceededEvent 构建 E6 credential.retire succeeded 事件。
func newRetireSucceededEvent(wsID, actorID, secretRef uuid.UUID, version int, traceID string, occurredAt time.Time) (*metadata.AuditEvent, error) {
	return newLifecycleAuditEvent(
		wsID, metadata.ActorTypeUser, &actorID, nil, nil,
		metadata.ActionCredentialRetire, "credential", secretRef.String(),
		metadata.OutcomeSucceeded,
		metadata.AuditMetadata{
			SecretRef:     strPtr(secretRef.String()),
			SecretVersion: intPtr(version),
		},
		traceID, occurredAt,
	)
}

// newLifecycleAuditEvent 构建凭证生命周期审计事件（user/system actor 判别）。
func newLifecycleAuditEvent(
	wsID uuid.UUID,
	actorType metadata.AuditActorType,
	actorID *uuid.UUID,
	connID, execID *uuid.UUID,
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
	return &metadata.AuditEvent{
		WorkspaceID:  wsID,
		ActorType:    actorType,
		ActorID:      actorID,
		ConnectionID: connID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Outcome:      outcome,
		Metadata:     raw,
		TraceID:      traceID,
		ExecutionID:  execID,
		OccurredAt:   occurredAt,
	}, nil
}

// credentialErrorCode 将凭证生命周期错误映射为稳定错误码。
func credentialErrorCode(err error) ErrorCode {
	switch {
	case IsErrorCode(err, ErrCredentialNotFound):
		return ErrCredentialNotFound
	case IsErrorCode(err, ErrCredentialRetired):
		return ErrCredentialRetired
	case IsErrorCode(err, ErrDecryptionFailed):
		return ErrDecryptionFailed
	case IsErrorCode(err, ErrUnknownKEKVersion):
		return ErrUnknownKEKVersion
	case IsErrorCode(err, ErrVersionConflict):
		return ErrVersionConflict
	case IsErrorCode(err, ErrCredentialInUse):
		return ErrCredentialInUse
	case IsErrorCode(err, ErrWrapQuotaExhausted):
		return ErrWrapQuotaExhausted
	case IsErrorCode(err, ErrInvalidPayload):
		return ErrInvalidPayload
	case IsErrorCode(err, ErrPayloadTooLarge):
		return ErrPayloadTooLarge
	default:
		return ErrInternalError
	}
}

// IsDecryptFailureCode 判断是否属于必须触发安全告警的解密类失败。
func IsDecryptFailureCode(code ErrorCode) bool {
	switch code {
	case ErrDecryptionFailed, ErrUnknownKEKVersion, ErrInvalidPayload, ErrPayloadTooLarge:
		return true
	default:
		return false
	}
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

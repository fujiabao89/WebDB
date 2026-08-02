package credentials

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// ErrAuditFailed 凭证生命周期审计写入失败稳定错误码（ADR-017 §6）。
const ErrAuditFailed ErrorCode = "audit_failed"

// auditWriteTimeout 审计持久化的独立超时上限（与调用方取消解耦，VuXZI）。
const auditWriteTimeout = 5 * time.Second

// AuditedLifecycleManager 为凭证生命周期接入追加式审计（E3-E6, E14-E16）。
// 生命周期操作持久化后写审计；审计写入失败返回 audit_failed（业务操作已持久化，
// 调用方可按 proposal §6.2 重试审计写入）。
type AuditedLifecycleManager struct {
	lm                *LifecycleManager
	audit             metadata.AuditEventStore
	alarm             metadata.SecurityAlarm
	clock             func() time.Time
	newTrace          func() string
	auditWriteTimeout time.Duration // 可注入的审计持久化超时（VuXZI）
}

// NewAuditedLifecycleManager 创建审计感知的凭证生命周期管理器。
func NewAuditedLifecycleManager(
	lm *LifecycleManager,
	audit metadata.AuditEventStore,
	alarm metadata.SecurityAlarm,
) *AuditedLifecycleManager {
	return &AuditedLifecycleManager{
		lm:                lm,
		audit:             audit,
		alarm:             alarm,
		clock:             func() time.Time { return time.Now().UTC() },
		newTrace:          func() string { return uuid.NewString() },
		auditWriteTimeout: auditWriteTimeout,
	}
}

// auditContext 从调用方 ctx 派生审计持久化上下文：剥离取消信号并加上独立超时，
// 确保客户端断开后 E3-E6/E14-E16 审计仍被写入（VuXZI）。
func (m *AuditedLifecycleManager) auditContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), m.auditWriteTimeout)
}

// Create 创建凭证并写入 E3 credential.create。
func (m *AuditedLifecycleManager) Create(ctx context.Context, wsID, actorID uuid.UUID, payload CredentialPayload) (*metadata.CredentialEnvelope, error) {
	env, err := m.lm.Create(ctx, wsID, payload)
	if err != nil {
		return nil, err
	}

	event, err := newLifecycleAuditEvent(
		wsID, metadata.ActorTypeUser, &actorID, nil, nil,
		metadata.ActionCredentialCreate, "credential", env.SecretRef.String(),
		metadata.OutcomeSucceeded,
		metadata.AuditMetadata{
			SecretRef:     strPtr(env.SecretRef.String()),
			SecretVersion: intPtr(env.Version),
			EnvelopeSuite: strPtr(env.EnvelopeSuite),
			KEKVersion:    intPtr(env.KEKVersion),
		},
		m.newTrace(), m.clock(),
	)
	if err != nil {
		return nil, m.auditEventBuildFailed(ctx, wsID, err)
	}
	if err := m.writeAudit(ctx, event); err != nil {
		return nil, err
	}
	return env, nil
}

// Resolve 解析凭证。失败时记录 E14-E16 并触发安全告警（decrypt 类失败）。
func (m *AuditedLifecycleManager) Resolve(ctx context.Context, wsID, secretRef uuid.UUID, version int) (CredentialPayload, error) {
	payload, err := m.lm.Resolve(ctx, wsID, secretRef, version)
	if err == nil {
		return payload, nil
	}

	code := credentialErrorCode(err)
	var action string
	var md metadata.AuditMetadata
	switch code {
	case ErrCredentialNotFound, ErrCredentialRetired, ErrInternalError:
		// E14: credential.lookup 失败（system actor）。
		// ErrInternalError（如 EnvelopeByRef 的数据库错误）发生在查找阶段，
		// 语义上是 lookup 而非 decrypt，避免污染解密失败审计（CodeRabbit #14）。
		action = metadata.ActionCredentialLookup
		md = metadata.AuditMetadata{
			SecretRef: strPtr(secretRef.String()),
			ErrorCode: strPtr(string(code)),
		}
	default:
		// E15/E16: credential.decrypt 失败（system actor）。
		action = metadata.ActionCredentialDecrypt
		md = metadata.AuditMetadata{
			SecretRef:     strPtr(secretRef.String()),
			SecretVersion: intPtr(version),
			ErrorCode:     strPtr(string(code)),
		}
		// E16: unknown_kek_version 需携带 kek_version（Codex P1）。
		var kekErr *KEKVersionError
		if errors.As(err, &kekErr) {
			md.KEKVersion = intPtr(kekErr.Version)
		}
		// 凭证解密失败/未知 KEK 版本必须触发安全告警（ADR-017 §6）。
		if IsDecryptFailureCode(code) {
			metadata.EmitAlarm(m.alarm, ctx, metadata.SecurityAlertEvent{
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
		// 与 Create/Retire 一致：审计事件构建失败 → audit_failed + $SECURITY_ALERT（CodeRabbit #15）。
		return payload, m.auditEventBuildFailed(ctx, wsID, buildErr)
	}
	if auditErr := m.writeAudit(ctx, event); auditErr != nil {
		return payload, auditErr
	}
	return payload, err
}

// Rotate 轮换凭证。成功记录 E4，失败记录 E5。
func (m *AuditedLifecycleManager) Rotate(ctx context.Context, wsID, actorID, secretRef uuid.UUID, expectedVersion int, newPayload CredentialPayload) (*metadata.CredentialEnvelope, error) {
	newEnv, err := m.lm.Rotate(ctx, wsID, secretRef, expectedVersion, newPayload)
	if err != nil {
		// E5 credential.rotate failed
		event, buildErr := newRotateFailedEvent(wsID, actorID, secretRef, expectedVersion, err, m.newTrace(), m.clock())
		if buildErr != nil {
			// 不丢弃 buildErr（CodeRabbit #16）：审计事件构建失败 → audit_failed + $SECURITY_ALERT。
			return nil, m.auditEventBuildFailed(ctx, wsID, buildErr)
		}
		if auditErr := m.writeAudit(ctx, event); auditErr != nil {
			return nil, auditErr
		}
		return nil, err
	}

	// E4 credential.rotate succeeded
	event, buildErr := newRotateSucceededEvent(wsID, actorID, secretRef, expectedVersion, newEnv, m.newTrace(), m.clock())
	if buildErr != nil {
		return nil, m.auditEventBuildFailed(ctx, wsID, buildErr)
	}
	if auditErr := m.writeAudit(ctx, event); auditErr != nil {
		return nil, auditErr
	}
	return newEnv, nil
}

// Retire 退役凭证。成功记录 E6（proposal §8.1 仅定义 succeeded）。
func (m *AuditedLifecycleManager) Retire(ctx context.Context, wsID, actorID, secretRef uuid.UUID, version int) error {
	if err := m.lm.Retire(ctx, wsID, secretRef, version); err != nil {
		return err
	}

	event, buildErr := newRetireSucceededEvent(wsID, actorID, secretRef, version, m.newTrace(), m.clock())
	if buildErr != nil {
		return m.auditEventBuildFailed(ctx, wsID, buildErr)
	}
	if auditErr := m.writeAudit(ctx, event); auditErr != nil {
		return auditErr
	}
	return nil
}

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
		// 从 typed conflict 错误提取实际版本，记录 actual_version（Qodo #4）。
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

// ---- helpers ----------------------------------------------------------------

// writeAudit 写审计；审计追加失败触发 $SECURITY_ALERT 并返回 audit_failed（ADR-017 §6）。
// 审计持久化与调用方取消解耦：即使请求 ctx 已取消，审计事件仍须写入（VuXZI）。
func (m *AuditedLifecycleManager) writeAudit(ctx context.Context, event *metadata.AuditEvent) error {
	auditCtx, cancel := m.auditContext(ctx)
	defer cancel()
	if err := m.audit.AppendAudit(auditCtx, event); err != nil {
		metadata.EmitAlarm(m.alarm, auditCtx, metadata.SecurityAlertEvent{
			TraceID:     event.TraceID,
			WorkspaceID: event.WorkspaceID,
			Code:        string(ErrAuditFailed),
			OccurredAt:  m.clock(),
		})
		// 不把底层审计存储错误文本拼进返回错误（Qodo #1）。
		return fmt.Errorf("%w: audit write failed", ErrAuditFailed)
	}
	return nil
}

// auditEventBuildFailed 统一处理审计事件构建失败（Create/Resolve/Rotate/Retire）：
// 返回 audit_failed 并触发 $SECURITY_ALERT（ADR-017 §6，CodeRabbit #16）。
// 告警使用非取消 context + 独立超时，调用方取消时仍发出 audit_failed（VuXZI）。
func (m *AuditedLifecycleManager) auditEventBuildFailed(ctx context.Context, wsID uuid.UUID, buildErr error) error {
	auditCtx, cancel := m.auditContext(ctx)
	defer cancel()
	metadata.EmitAlarm(m.alarm, auditCtx, metadata.SecurityAlertEvent{
		TraceID:     m.newTrace(),
		WorkspaceID: wsID,
		Code:        string(ErrAuditFailed),
		OccurredAt:  m.clock(),
	})
	// 不把构建错误文本拼进返回错误（Qodo #1）。
	return fmt.Errorf("%w: audit event build failed", ErrAuditFailed)
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
// 供凭证层（audited.go）与执行/连接层（execution.Pipeline、connections.Service）
// 复用同一判定，避免把 credential_not_found/credential_retired 等查找类失败
// 升级为安全告警（Qodo #2 / CodeRabbit #8）。
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

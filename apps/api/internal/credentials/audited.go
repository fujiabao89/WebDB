package credentials

import (
	"context"
	"fmt"
	"time"

	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// ErrAuditFailed 凭证生命周期审计写入失败稳定错误码（ADR-017 §6）。
const ErrAuditFailed ErrorCode = "audit_failed"

// AuditedLifecycleManager 为凭证生命周期接入追加式审计（E3-E6, E14-E16）。
// 生命周期操作持久化后写审计；审计写入失败返回 audit_failed（业务操作已持久化，
// 调用方可按 proposal §6.2 重试审计写入）。
type AuditedLifecycleManager struct {
	lm       *LifecycleManager
	audit    metadata.AuditEventStore
	alarm    metadata.SecurityAlarm
	clock    func() time.Time
	newTrace func() string
}

// NewAuditedLifecycleManager 创建审计感知的凭证生命周期管理器。
func NewAuditedLifecycleManager(
	lm *LifecycleManager,
	audit metadata.AuditEventStore,
	alarm metadata.SecurityAlarm,
) *AuditedLifecycleManager {
	return &AuditedLifecycleManager{
		lm:       lm,
		audit:    audit,
		alarm:    alarm,
		clock:    func() time.Time { return time.Now().UTC() },
		newTrace: func() string { return uuid.NewString() },
	}
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
		return nil, fmt.Errorf("%w: %v", ErrAuditFailed, err)
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
	case ErrCredentialNotFound, ErrCredentialRetired:
		// E14: credential.lookup 失败（system actor）
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
		// 凭证解密失败/未知 KEK 版本必须触发安全告警（ADR-017 §6）。
		if isDecryptFailureCode(code) {
			m.alarm.Alarm(ctx, metadata.SecurityAlertEvent{
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
		return payload, buildErr
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
			return nil, err
		}
		if auditErr := m.writeAudit(ctx, event); auditErr != nil {
			return nil, auditErr
		}
		return nil, err
	}

	// E4 credential.rotate succeeded
	event, buildErr := newRotateSucceededEvent(wsID, actorID, secretRef, expectedVersion, newEnv, m.newTrace(), m.clock())
	if buildErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuditFailed, buildErr)
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
		return fmt.Errorf("%w: %v", ErrAuditFailed, buildErr)
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
func (m *AuditedLifecycleManager) writeAudit(ctx context.Context, event *metadata.AuditEvent) error {
	if err := m.audit.AppendAudit(ctx, event); err != nil {
		m.alarm.Alarm(ctx, metadata.SecurityAlertEvent{
			TraceID:     event.TraceID,
			WorkspaceID: event.WorkspaceID,
			Code:        string(ErrAuditFailed),
			OccurredAt:  m.clock(),
		})
		return fmt.Errorf("%w: %v", ErrAuditFailed, err)
	}
	return nil
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

// isDecryptFailureCode 判断是否属于必须触发安全告警的解密类失败。
func isDecryptFailureCode(code ErrorCode) bool {
	switch code {
	case ErrDecryptionFailed, ErrUnknownKEKVersion, ErrInvalidPayload, ErrPayloadTooLarge:
		return true
	default:
		return false
	}
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

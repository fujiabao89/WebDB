package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// SecurityAlertEvent / SecurityAlarm 由 metadata 包共享定义，
// 避免 credentials/connections 依赖 execution 造成循环依赖。
type SecurityAlertEvent = metadata.SecurityAlertEvent
type SecurityAlarm = metadata.SecurityAlarm

// NewStderrAlarm 创建 stderr 安全告警通道。
func NewStderrAlarm() SecurityAlarm { return metadata.NewStderrSecurityAlarm() }

// hashRawSQL 计算原始 SQL 的 SHA-256 摘要（statement_hash fallback）。
// statement_hash 是脱敏摘要，不含 SQL 正文（proposal §8.3）。
func hashRawSQL(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return hex.EncodeToString(sum[:])
}

// newAuditEvent 构建标准审计事件（强类型 metadata，fail-closed 校验在仓储层）。
// actorType/actorID 遵循 ADR-013 判别器：user 必须有 actor_id，system 必须无 actor_id。
func newAuditEvent(
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

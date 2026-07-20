package metadata

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ---- 仓储接口 ---------------------------------------------------------------

// UserStore 用户仓储。
type UserStore interface {
	CreateUser(ctx context.Context, u *User) error
	UserByID(ctx context.Context, id uuid.UUID) (*User, error)
	UserByEmail(ctx context.Context, email string) (*User, error)
	ListUsers(ctx context.Context, status UserStatus, limit, offset int) ([]User, error)
	UpdateUser(ctx context.Context, u *User) error
}

// WorkspaceStore 工作区仓储。
type WorkspaceStore interface {
	CreateWorkspace(ctx context.Context, ws *Workspace) error
	WorkspaceByID(ctx context.Context, id uuid.UUID) (*Workspace, error)
	ListWorkspaces(ctx context.Context, limit, offset int) ([]Workspace, error)
}

// WorkspaceMemberStore 工作区成员仓储。
type WorkspaceMemberStore interface {
	AddMember(ctx context.Context, m *WorkspaceMember) error
	MemberByWorkspaceAndUser(ctx context.Context, wsID, userID uuid.UUID) (*WorkspaceMember, error)
	ListMembers(ctx context.Context, wsID uuid.UUID) ([]WorkspaceMember, error)
	RemoveMember(ctx context.Context, wsID, userID uuid.UUID) error
}

// CredentialEnvelopeStore 凭证信封仓储。
type CredentialEnvelopeStore interface {
	CreateEnvelope(ctx context.Context, env *CredentialEnvelope) error
	EnvelopeByRef(ctx context.Context, wsID, secretRef uuid.UUID, version int) (*CredentialEnvelope, error)
	ListEnvelopes(ctx context.Context, wsID uuid.UUID) ([]CredentialEnvelope, error)
}

// ConnectionStore 连接仓储。
type ConnectionStore interface {
	CreateConnection(ctx context.Context, conn *Connection) error
	ConnectionByID(ctx context.Context, wsID, id uuid.UUID) (*Connection, error)
	ListConnections(ctx context.Context, wsID uuid.UUID) ([]Connection, error)
	UpdateConnection(ctx context.Context, wsID uuid.UUID, conn *Connection) error
}

// ConnectionPolicyStore 连接策略仓储。
type ConnectionPolicyStore interface {
	CreatePolicy(ctx context.Context, p *ConnectionPolicy) error
	UpdatePolicy(ctx context.Context, p *ConnectionPolicy) error
	PolicyByConnection(ctx context.Context, wsID, connID uuid.UUID) (*ConnectionPolicy, error)
}

// ExecutionStore 执行记录仓储。
type ExecutionStore interface {
	CreateExecution(ctx context.Context, exec *Execution) error
	ExecutionByID(ctx context.Context, wsID, id uuid.UUID) (*Execution, error)
	ExecutionByTraceID(ctx context.Context, wsID uuid.UUID, traceID string) (*Execution, error)
	UpdateExecution(ctx context.Context, wsID uuid.UUID, exec *Execution) error
}

// AuditEventStore 审计事件仓储 — 仅追加写与查询，不支持更新或删除。
// ADR-013: 数据库层拒绝 UPDATE/DELETE/TRUNCATE。
type AuditEventStore interface {
	AppendAudit(ctx context.Context, e *AuditEvent) error
	QueryAudit(ctx context.Context, q AuditQuery) ([]AuditEvent, error)
}

// AuditQuery 审计事件查询条件。
type AuditQuery struct {
	WorkspaceID  uuid.UUID
	ConnectionID *uuid.UUID
	Action       *string
	ResourceType *string
	From         *time.Time
	To           *time.Time
	Limit        int
	Offset       int
}

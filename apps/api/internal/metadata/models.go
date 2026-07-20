// Package metadata 定义 WebDB P0 元数据数据模型与仓储接口。
// ADR-013: P0 限定 8 张表，审计追加写不可变。
package metadata

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ---- 枚举常量 ---------------------------------------------------------------

// UserStatus 用户状态。
type UserStatus string

const (
	UserStatusActive   UserStatus = "active"
	UserStatusDisabled UserStatus = "disabled"
)

// MemberRole 工作区成员角色。
type MemberRole string

const (
	RoleOwner  MemberRole = "owner"
	RoleAdmin  MemberRole = "admin"
	RoleEditor MemberRole = "editor"
	RoleViewer MemberRole = "viewer"
)

// Engine 数据库引擎。
type Engine string

const (
	EnginePostgreSQL Engine = "postgresql"
	EngineMySQL      Engine = "mysql"
)

// Environment 连接环境。
type Environment string

const (
	EnvDevelopment Environment = "development"
	EnvStaging     Environment = "staging"
	EnvProduction  Environment = "production"
)

// ExecutionStatus 执行状态。
type ExecutionStatus string

const (
	ExecStatusPending   ExecutionStatus = "pending"
	ExecStatusRunning   ExecutionStatus = "running"
	ExecStatusCompleted ExecutionStatus = "completed"
	ExecStatusFailed    ExecutionStatus = "failed"
	ExecStatusCancelled ExecutionStatus = "cancelled"
)

// AuditActorType 审计 actor 类型。
type AuditActorType string

const (
	ActorTypeUser   AuditActorType = "user"
	ActorTypeSystem AuditActorType = "system"
)

// AuditOutcome 审计结果。
type AuditOutcome string

const (
	OutcomeSucceeded AuditOutcome = "succeeded"
	OutcomeFailed    AuditOutcome = "failed"
	OutcomeDenied    AuditOutcome = "denied"
	OutcomeCancelled AuditOutcome = "cancelled"
)

// ---- 数据模型 ---------------------------------------------------------------

// User 本地用户。PasswordHash 不序列化到 API 响应。
type User struct {
	ID               uuid.UUID  `json:"id"`
	Email            string     `json:"email"`
	PasswordHash     string     `json:"-"`
	Status           UserStatus `json:"status"`
	IdentityProvider *string    `json:"identity_provider,omitempty"`
	ExternalSubject  *string    `json:"external_subject,omitempty"`
	ExternalTenant   *string    `json:"external_tenant,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// Workspace 工作区/租户。
type Workspace struct {
	ID        uuid.UUID       `json:"id"`
	Name      string          `json:"name"`
	Settings  json.RawMessage `json:"settings"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// WorkspaceMember 工作区成员。
type WorkspaceMember struct {
	WorkspaceID uuid.UUID  `json:"workspace_id"`
	UserID      uuid.UUID  `json:"user_id"`
	Role        MemberRole `json:"role"`
	CreatedAt   time.Time  `json:"created_at"`
}

// CredentialEnvelope 加密凭证信封。所有加密 payload 不序列化到 API 响应。
type CredentialEnvelope struct {
	WorkspaceID   uuid.UUID  `json:"workspace_id"`
	SecretRef     uuid.UUID  `json:"secret_ref"`
	Version       int        `json:"version"`
	Ciphertext    []byte     `json:"-"`
	DataNonce     []byte     `json:"-"`
	WrappedDEK    []byte     `json:"-"`
	WrapNonce     []byte     `json:"-"`
	EnvelopeSuite string     `json:"envelope_suite"`
	KEKVersion    int        `json:"kek_version"`
	CreatedAt     time.Time  `json:"created_at"`
	RetiredAt     *time.Time `json:"retired_at,omitempty"`
}

// Connection 目标数据库连接。不保存明文凭证。
type Connection struct {
	ID            uuid.UUID   `json:"id"`
	WorkspaceID   uuid.UUID   `json:"workspace_id"`
	Name          string      `json:"name"`
	Engine        Engine      `json:"engine"`
	Host          string      `json:"host"`
	Port          int         `json:"port"`
	Database      string      `json:"database"`
	Environment   Environment `json:"environment"`
	SecretRef     uuid.UUID   `json:"secret_ref"`
	SecretVersion int         `json:"secret_version"`
	CreatedBy     uuid.UUID   `json:"created_by"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

// ConnectionPolicy 连接策略。
type ConnectionPolicy struct {
	WorkspaceID        uuid.UUID `json:"workspace_id"`
	ConnectionID       uuid.UUID `json:"connection_id"`
	AllowRead          bool      `json:"allow_read"`
	AllowWrite         bool      `json:"allow_write"`
	AllowExport        bool      `json:"allow_export"`
	StatementTimeoutMs int       `json:"statement_timeout_ms"`
	MaxRows            int       `json:"max_rows"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// Execution SQL 执行记录。
type Execution struct {
	ID              uuid.UUID       `json:"id"`
	WorkspaceID     uuid.UUID       `json:"workspace_id"`
	ConnectionID    uuid.UUID       `json:"connection_id"`
	ActorID         uuid.UUID       `json:"actor_id"`
	DocumentID      *uuid.UUID      `json:"document_id,omitempty"`
	QueryVersionID  *uuid.UUID      `json:"query_version_id,omitempty"`
	StatementHash   string          `json:"statement_hash"`
	Status          ExecutionStatus `json:"status"`
	TraceID         string          `json:"trace_id"`
	StartedAt       time.Time       `json:"started_at"`
	FinishedAt      *time.Time      `json:"finished_at,omitempty"`
	DurationMs      *int            `json:"duration_ms,omitempty"`
	RowCount        *int            `json:"row_count,omitempty"`
	ResultRef       *string         `json:"result_ref,omitempty"`
	ResultExpiresAt *time.Time      `json:"result_expires_at,omitempty"`
	ErrorCode       *string         `json:"error_code,omitempty"`
	ErrorMessage    *string         `json:"error_message,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
}

// AuditEvent 审计事件。仅追加写，不可修改或删除。
type AuditEvent struct {
	ID           uuid.UUID       `json:"id"`
	WorkspaceID  uuid.UUID       `json:"workspace_id"`
	ActorType    AuditActorType  `json:"actor_type"`
	ActorID      *uuid.UUID      `json:"actor_id,omitempty"`
	ConnectionID *uuid.UUID      `json:"connection_id,omitempty"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id"`
	Outcome      AuditOutcome    `json:"outcome"`
	Metadata     json.RawMessage `json:"metadata"`
	TraceID      string          `json:"trace_id"`
	ExecutionID  *uuid.UUID      `json:"execution_id,omitempty"`
	OccurredAt   time.Time       `json:"occurred_at"`
	CreatedAt    time.Time       `json:"created_at"`
}

package seeddemo

import (
	"context"

	"github.com/fujiabao89/webdb/internal/connections"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// ---- 窄接口（测试替身与生产仓储均可注入） ------------------------------------

// identityStore 提供固定 ID 的身份幂等写入与一致性读取。
// 生产实现包装 *sql.DB 的参数化 SQL（受控内部包，任务书 §四）；测试用内存 fake。
type identityStore interface {
	WorkspaceByID(ctx context.Context, id uuid.UUID) (*metadata.Workspace, error)
	createWorkspaceWithID(ctx context.Context, ws *metadata.Workspace) error
	UserByID(ctx context.Context, id uuid.UUID) (*metadata.User, error)
	createUserWithID(ctx context.Context, u *metadata.User) error
	MemberByWorkspaceAndUser(ctx context.Context, wsID, userID uuid.UUID) (*metadata.WorkspaceMember, error)
	addMemberIfAbsent(ctx context.Context, m *metadata.WorkspaceMember) error
	createEnvelopeWithID(ctx context.Context, env *metadata.CredentialEnvelope) error
	createConnectionWithID(ctx context.Context, conn *metadata.Connection) error
	connectionByID(ctx context.Context, wsID, id uuid.UUID) (*metadata.Connection, error)
	envelopeByRef(ctx context.Context, wsID, secretRef uuid.UUID, version int) (*metadata.CredentialEnvelope, error)
}

// credentialCreator 抽象凭证创建能力（生产实现 credentials.LifecycleManager.Create）。
type credentialCreator interface {
	Create(ctx context.Context, wsID, actorID uuid.UUID, payload credentials.CredentialPayload) (*metadata.CredentialEnvelope, error)
}

// connectionCreator 抽象连接创建能力（生产实现 connections.Service.Create，含 owner 门控与 E1 审计）。
type connectionCreator interface {
	Create(ctx context.Context, p connections.Principal, conn *metadata.Connection) (*metadata.Connection, error)
}

// policyWriter 连接策略写入（生产实现 metadata.ConnectionPolicyStore.CreatePolicy）。
type policyWriter interface {
	CreatePolicy(ctx context.Context, p *metadata.ConnectionPolicy) error
}

// connectionReader 连接与信封一致性读取（生产实现 metadata.PGStore）。
type connectionReader interface {
	ConnectionByID(ctx context.Context, wsID, id uuid.UUID) (*metadata.Connection, error)
	ListConnections(ctx context.Context, wsID uuid.UUID) ([]metadata.Connection, error)
}

// policyReader 策略读取（生产实现 metadata.PGStore）。
type policyReader interface {
	PolicyByConnection(ctx context.Context, wsID, connID uuid.UUID) (*metadata.ConnectionPolicy, error)
}

// envelopeReader 信封列表（生产实现 metadata.PGStore）。
type envelopeReader interface {
	ListEnvelopes(ctx context.Context, wsID uuid.UUID) ([]metadata.CredentialEnvelope, error)
}

// credentialResolver 凭证解析（生产实现 credentials.LifecycleManager）。
type credentialResolver interface {
	ResolveCredential(ctx context.Context, workspaceID, secretRef uuid.UUID, secretVersion int) (credentials.CredentialPayload, error)
}

// Deps seed 运行时依赖（便于测试注入 fake；生产由 RunFromEnv 组装）。
type Deps struct {
	Identity    identityStore
	Credentials credentialCreator
	Connector   connectionCreator
	Policies    policyWriter
	ConnReader  connectionReader
	PolicyRead  policyReader
	Envelopes   envelopeReader
	Resolver    credentialResolver
}

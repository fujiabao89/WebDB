package seeddemo

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// ---- 跨租户隔离 fixture（WEB-39） -------------------------------------------
//
// 幂等创建第二合成租户（workspace/user/member/凭证/连接），用于跨租户隔离 E2E：
// 演示 Principal 属于第一 workspace，请求该 foreign 连接必须与随机不存在 ID 一样
// 返回脱敏 connection_not_found，且连接列表不出现该对象。
// 全部为合成数据；凭证为永不解密的占位 envelope（仅满足 FK 约束），不含真实密钥。
//
// 与 ids.go/Compose 注释一致：固定 ID 冲突 fail-closed。写入采用 ON CONFLICT DO NOTHING
// 幂等，但**创建后逐项读回校验**——任何字段漂移（name/email/role/suite/host/secret_ref 等）
// 返回 ErrDemoSeedRefused，不覆盖已有记录。

// demoForeignSecretRef 第二合成工作区 foreign 连接的占位 secret_ref。
const demoForeignSecretRef = "44444444-4444-4444-8444-444444444444"

const (
	foreignWorkspaceName = "Foreign Workspace"
	foreignUserEmail     = "foreign@example.local"
	foreignConnName      = "foreign (PostgreSQL)"
	foreignConnHost      = "foreign-demo-pg"
	foreignConnDatabase  = "foreign_db"
)

// createEnvelopeWithID 幂等插入占位凭证信封（仅满足连接 FK，永不解密）。
func (s *pgIdentityStore) createEnvelopeWithID(ctx context.Context, env *metadata.CredentialEnvelope) error {
	const q = `
		INSERT INTO credential_envelopes
			(workspace_id, secret_ref, version, ciphertext, data_nonce,
			 wrapped_dek, wrap_nonce, envelope_suite, kek_version)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (workspace_id, secret_ref, version) DO NOTHING`
	_, err := s.db.ExecContext(ctx, q,
		env.WorkspaceID, env.SecretRef, env.Version,
		env.Ciphertext, env.DataNonce, env.WrappedDEK, env.WrapNonce,
		env.EnvelopeSuite, env.KEKVersion)
	return err
}

// createConnectionWithID 幂等插入连接行（绕过 owner 门控，仅用于合成隔离 fixture）。
func (s *pgIdentityStore) createConnectionWithID(ctx context.Context, conn *metadata.Connection) error {
	const q = `
		INSERT INTO connections
			(id, workspace_id, name, engine, host, port, database, environment,
			 secret_ref, secret_version, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (id) DO NOTHING`
	_, err := s.db.ExecContext(ctx, q,
		conn.ID, conn.WorkspaceID, conn.Name, string(conn.Engine),
		conn.Host, conn.Port, conn.Database, string(conn.Environment),
		conn.SecretRef, conn.SecretVersion, conn.CreatedBy)
	return err
}

// connectionByID 读回连接（复用 metadata.PGStore 的 workspace 过滤）。
func (s *pgIdentityStore) connectionByID(ctx context.Context, wsID, id uuid.UUID) (*metadata.Connection, error) {
	return s.meta.ConnectionByID(ctx, wsID, id)
}

// envelopeByRef 读回凭证信封。
func (s *pgIdentityStore) envelopeByRef(ctx context.Context, wsID, secretRef uuid.UUID, version int) (*metadata.CredentialEnvelope, error) {
	return s.meta.EnvelopeByRef(ctx, wsID, secretRef, version)
}

// ensureForeignIsolationFixture 幂等创建第二合成租户，创建后读回校验（fail-closed）。
func ensureForeignIsolationFixture(ctx context.Context, s identityStore) error {
	fws := mustParseUUID(DemoForeignWorkspaceID)
	fuser := mustParseUUID(DemoForeignUserID)
	fconn := mustParseUUID(DemoForeignConnectionID)
	fsecret := mustParseUUID(demoForeignSecretRef)

	if err := s.createWorkspaceWithID(ctx, &metadata.Workspace{ID: fws, Name: foreignWorkspaceName, Settings: json.RawMessage("{}")}); err != nil {
		return fmt.Errorf("创建 foreign workspace 失败: %w", err)
	}
	if err := s.createUserWithID(ctx, &metadata.User{ID: fuser, Email: foreignUserEmail, PasswordHash: demoPasswordHash, Status: metadata.UserStatusActive}); err != nil {
		return fmt.Errorf("创建 foreign user 失败: %w", err)
	}
	if err := s.addMemberIfAbsent(ctx, &metadata.WorkspaceMember{WorkspaceID: fws, UserID: fuser, Role: metadata.RoleOwner}); err != nil {
		return fmt.Errorf("创建 foreign member 失败: %w", err)
	}
	if err := s.createEnvelopeWithID(ctx, &metadata.CredentialEnvelope{
		WorkspaceID:   fws,
		SecretRef:     fsecret,
		Version:       1,
		Ciphertext:    []byte{0},
		DataNonce:     []byte{0},
		WrappedDEK:    []byte{0},
		WrapNonce:     []byte{0},
		EnvelopeSuite: "AES256GCM-v1",
		KEKVersion:    1,
	}); err != nil {
		return fmt.Errorf("创建 foreign 凭证 envelope 失败: %w", err)
	}
	if err := s.createConnectionWithID(ctx, &metadata.Connection{
		ID:            fconn,
		WorkspaceID:   fws,
		Name:          foreignConnName,
		Engine:        metadata.EnginePostgreSQL,
		Host:          foreignConnHost,
		Port:          5432,
		Database:      foreignConnDatabase,
		Environment:   metadata.EnvDevelopment,
		SecretRef:     fsecret,
		SecretVersion: 1,
		CreatedBy:     fuser,
	}); err != nil {
		return fmt.Errorf("创建 foreign connection 失败: %w", err)
	}

	// 读回校验：任一固定字段漂移 fail-closed，不覆盖已有记录。
	if ws, err := s.WorkspaceByID(ctx, fws); err != nil {
		return fmt.Errorf("%w: 读回 foreign workspace 失败", ErrDemoSeedRefused)
	} else if ws.Name != foreignWorkspaceName {
		return fmt.Errorf("%w: foreign workspace name 漂移（got %q want %q）", ErrDemoSeedRefused, ws.Name, foreignWorkspaceName)
	}
	if u, err := s.UserByID(ctx, fuser); err != nil {
		return fmt.Errorf("%w: 读回 foreign user 失败", ErrDemoSeedRefused)
	} else if u.Email != foreignUserEmail || u.Status != metadata.UserStatusActive || u.PasswordHash != demoPasswordHash {
		return fmt.Errorf("%w: foreign user 漂移（email=%q status=%q）", ErrDemoSeedRefused, u.Email, u.Status)
	}
	if m, err := s.MemberByWorkspaceAndUser(ctx, fws, fuser); err != nil {
		return fmt.Errorf("%w: 读回 foreign member 失败", ErrDemoSeedRefused)
	} else if m.Role != metadata.RoleOwner {
		return fmt.Errorf("%w: foreign member role 漂移（got %q）", ErrDemoSeedRefused, m.Role)
	}
	env, err := s.envelopeByRef(ctx, fws, fsecret, 1)
	if err != nil {
		return fmt.Errorf("%w: 读回 foreign envelope 失败", ErrDemoSeedRefused)
	}
	if env.EnvelopeSuite != "AES256GCM-v1" || env.KEKVersion != 1 ||
		!bytes.Equal(env.Ciphertext, []byte{0}) || !bytes.Equal(env.DataNonce, []byte{0}) ||
		!bytes.Equal(env.WrappedDEK, []byte{0}) || !bytes.Equal(env.WrapNonce, []byte{0}) {
		return fmt.Errorf("%w: foreign envelope 漂移（suite=%q kek=%d）", ErrDemoSeedRefused, env.EnvelopeSuite, env.KEKVersion)
	}
	conn, err := s.connectionByID(ctx, fws, fconn)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: foreign connection 缺失或不属于第二 workspace", ErrDemoSeedRefused)
		}
		return fmt.Errorf("%w: 读回 foreign connection 失败", ErrDemoSeedRefused)
	}
	if conn.Name != foreignConnName || conn.Engine != metadata.EnginePostgreSQL ||
		conn.Host != foreignConnHost || conn.Port != 5432 ||
		conn.Database != foreignConnDatabase || conn.Environment != metadata.EnvDevelopment ||
		conn.SecretRef != fsecret || conn.SecretVersion != 1 || conn.CreatedBy != fuser {
		return fmt.Errorf("%w: foreign connection 漂移（name=%q engine=%q host=%q port=%d db=%q env=%q secret_ref=%s secret_version=%d）",
			ErrDemoSeedRefused, conn.Name, conn.Engine, conn.Host, conn.Port, conn.Database, conn.Environment, conn.SecretRef, conn.SecretVersion)
	}
	return nil
}

package seeddemo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// ---- 跨租户隔离 fixture（WEB-39） -------------------------------------------
//
// 幂等创建第二合成租户（workspace/user/member/凭证/连接），用于跨租户隔离 E2E：
// 演示 Principal 属于第一 workspace，请求该 foreign 连接必须与随机不存在 ID 一样
// 返回脱敏 connection_not_found，且连接列表不出现该对象。
// 全部为合成数据。凭证经 credentials.LifecycleManager.Create 加密（遵守 ADR-019），
// 连接仍走受控内部 seed 包的固定 ID 参数化写入（ADR-019 仅禁止直接 INSERT credential_envelopes）。
//
// 凭证 secretRef 由 LifecycleManager 随机生成，无法用固定 ID 的 DO NOTHING 幂等；
// 因此连接+凭证采用「读回优先」幂等：连接已存在则仅校验，不存在才创建，避免重复
// seed 累积孤立 envelope。创建后逐项读回校验——任一字段漂移返回 ErrDemoSeedRefused。

const (
	foreignWorkspaceName = "Foreign Workspace"
	foreignUserEmail     = "foreign@example.local"
	foreignConnName      = "foreign (PostgreSQL)"
	foreignConnHost      = "foreign-demo-pg"
	foreignConnDatabase  = "foreign_db"
	// foreign 连接凭证（合成占位：foreign-demo-pg 主机不存在，凭证永不被实际解密/使用）。
	foreignCredUser     = "foreign_reader"
	foreignCredPassword = "!foreign-synthetic-password-do-not-use"
)

// createConnectionWithID 幂等插入连接行（绕过 owner 门控，仅用于合成隔离 fixture）。
// 注意：仅 credential_envelopes 受 ADR-019 约束；连接固定 ID 写入属候选方案允许的受控内部 SQL。
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

// ensureForeignIsolationFixture 幂等创建第二合成租户，创建后读回校验（fail-closed）。
func ensureForeignIsolationFixture(ctx context.Context, deps Deps) error {
	fws := mustParseUUID(DemoForeignWorkspaceID)
	fuser := mustParseUUID(DemoForeignUserID)
	fconn := mustParseUUID(DemoForeignConnectionID)

	// 先拒绝 foreign 工作区的孤立 active envelope（上次 seed 连接创建失败遗留），
	// 避免重复创建新 envelope 造成累积（与 demo 工作区 rejectOrphanEnvelopes 语义一致）。
	if err := rejectOrphanEnvelopes(ctx, deps, fws); err != nil {
		return err
	}

	if err := deps.Identity.createWorkspaceWithID(ctx, &metadata.Workspace{ID: fws, Name: foreignWorkspaceName, Settings: json.RawMessage("{}")}); err != nil {
		return fmt.Errorf("创建 foreign workspace 失败: %w", err)
	}
	if err := deps.Identity.createUserWithID(ctx, &metadata.User{ID: fuser, Email: foreignUserEmail, PasswordHash: demoPasswordHash, Status: metadata.UserStatusActive}); err != nil {
		return fmt.Errorf("创建 foreign user 失败: %w", err)
	}
	if err := deps.Identity.addMemberIfAbsent(ctx, &metadata.WorkspaceMember{WorkspaceID: fws, UserID: fuser, Role: metadata.RoleOwner}); err != nil {
		return fmt.Errorf("创建 foreign member 失败: %w", err)
	}

	// 连接+凭证：读回优先幂等（连接存在则 no-op，不存在才经 LifecycleManager 创建凭证）。
	if err := ensureForeignConnection(ctx, deps, fws, fuser, fconn); err != nil {
		return err
	}

	// 读回校验：任一固定字段漂移 fail-closed，不覆盖已有记录。
	if ws, err := deps.Identity.WorkspaceByID(ctx, fws); err != nil {
		return fmt.Errorf("%w: 读回 foreign workspace 失败: %w", ErrDemoSeedRefused, err)
	} else if ws.Name != foreignWorkspaceName {
		return fmt.Errorf("%w: foreign workspace name 漂移（got %q want %q）", ErrDemoSeedRefused, ws.Name, foreignWorkspaceName)
	}
	if u, err := deps.Identity.UserByID(ctx, fuser); err != nil {
		return fmt.Errorf("%w: 读回 foreign user 失败: %w", ErrDemoSeedRefused, err)
	} else if u.Email != foreignUserEmail || u.Status != metadata.UserStatusActive || u.PasswordHash != demoPasswordHash {
		return fmt.Errorf("%w: foreign user 漂移（email=%q status=%q）", ErrDemoSeedRefused, u.Email, u.Status)
	}
	if m, err := deps.Identity.MemberByWorkspaceAndUser(ctx, fws, fuser); err != nil {
		return fmt.Errorf("%w: 读回 foreign member 失败: %w", ErrDemoSeedRefused, err)
	} else if m.Role != metadata.RoleOwner {
		return fmt.Errorf("%w: foreign member role 漂移（got %q）", ErrDemoSeedRefused, m.Role)
	}
	if err := verifyForeignConnection(ctx, deps, fws, fuser, fconn); err != nil {
		return err
	}
	return nil
}

// ensureForeignConnection 读回优先幂等：连接已存在则 no-op（漂移由 verifyForeignConnection
// 检测），不存在才经 LifecycleManager 创建凭证 + 固定 ID 写入连接。
func ensureForeignConnection(ctx context.Context, deps Deps, fws, fuser, fconn uuid.UUID) error {
	_, err := deps.ConnReader.ConnectionByID(ctx, fws, fconn)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return createForeignConnection(ctx, deps, fws, fuser, fconn)
	default:
		return fmt.Errorf("%w: 读回 foreign connection 失败: %w", ErrDemoSeedRefused, err)
	}
}

// createForeignConnection 经 LifecycleManager 创建凭证 + 固定 ID 写入连接。
func createForeignConnection(ctx context.Context, deps Deps, fws, fuser, fconn uuid.UUID) error {
	env, err := deps.Credentials.Create(ctx, fws, fuser, credentials.CredentialPayload{
		User:     foreignCredUser,
		Password: foreignCredPassword,
	})
	if err != nil {
		return fmt.Errorf("创建 foreign 凭证失败: %w", err)
	}
	conn := &metadata.Connection{
		ID:            fconn,
		WorkspaceID:   fws,
		Name:          foreignConnName,
		Engine:        metadata.EnginePostgreSQL,
		Host:          foreignConnHost,
		Port:          5432,
		Database:      foreignConnDatabase,
		Environment:   metadata.EnvDevelopment,
		SecretRef:     env.SecretRef,
		SecretVersion: env.Version,
		CreatedBy:     fuser,
	}
	if err := deps.Identity.createConnectionWithID(ctx, conn); err != nil {
		return fmt.Errorf("创建 foreign connection 失败: %w", err)
	}
	return nil
}

// verifyForeignConnection 读回校验连接不可变字段与凭证可解析性（fail-closed）。
func verifyForeignConnection(ctx context.Context, deps Deps, fws, fuser, fconn uuid.UUID) error {
	conn, err := deps.ConnReader.ConnectionByID(ctx, fws, fconn)
	if err != nil {
		return fmt.Errorf("%w: 读回 foreign connection 失败: %w", ErrDemoSeedRefused, err)
	}
	if conn.Name != foreignConnName || conn.Engine != metadata.EnginePostgreSQL ||
		conn.Host != foreignConnHost || conn.Port != 5432 ||
		conn.Database != foreignConnDatabase || conn.Environment != metadata.EnvDevelopment ||
		conn.CreatedBy != fuser {
		return fmt.Errorf("%w: foreign connection 漂移（name=%q engine=%q host=%q port=%d db=%q env=%q created_by=%s）",
			ErrDemoSeedRefused, conn.Name, conn.Engine, conn.Host, conn.Port, conn.Database, conn.Environment, conn.CreatedBy)
	}
	// 凭证可解析（未被退役/篡改/缺失）且与合成 foreign 凭证一致。
	payload, err := deps.Resolver.ResolveCredential(ctx, fws, conn.SecretRef, conn.SecretVersion)
	if err != nil {
		return fmt.Errorf("%w: foreign connection 凭证无法解析（版本不匹配/信封缺失/已退役）", ErrDemoSeedRefused)
	}
	if payload.User != foreignCredUser || payload.Password != foreignCredPassword {
		return fmt.Errorf("%w: foreign connection 凭证与合成期望不一致", ErrDemoSeedRefused)
	}
	return nil
}

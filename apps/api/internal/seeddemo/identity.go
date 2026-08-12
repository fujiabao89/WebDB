package seeddemo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// ---- 生产身份存储：固定 ID 参数化写入（受控内部 seed 包） ----------------------
//
// 现有 metadata store 的 CreateUser/CreateWorkspace 不允许指定 ID（由 DB 生成），
// 无法满足"固定合成 UUID"约束，属于任务书 §四允许的"缺少安全接口"情形。
// 因此本包用参数化 SQL 实现固定 ID 的幂等写入：单语句、全参数化、无字符串拼接，
// 遵守现有 Schema 约束（email 唯一、status 枚举、member 复合主键、role 枚举）。
// 读取一致性复用 metadata.PGStore 已验证逻辑。

// pgIdentityStore 基于 *sql.DB 的身份幂等写入 + 复用 PGStore 读取。
type pgIdentityStore struct {
	db   *sql.DB
	meta *metadata.PGStore
}

var _ identityStore = (*pgIdentityStore)(nil)

func (s *pgIdentityStore) WorkspaceByID(ctx context.Context, id uuid.UUID) (*metadata.Workspace, error) {
	return s.meta.WorkspaceByID(ctx, id)
}

func (s *pgIdentityStore) createWorkspaceWithID(ctx context.Context, ws *metadata.Workspace) error {
	settings := ws.Settings
	if settings == nil {
		settings = json.RawMessage("{}")
	}
	const q = `
		INSERT INTO workspaces (id, name, settings)
		VALUES ($1, $2, $3)
		ON CONFLICT (id) DO NOTHING`
	_, err := s.db.ExecContext(ctx, q, ws.ID, ws.Name, settings)
	return err
}

func (s *pgIdentityStore) UserByID(ctx context.Context, id uuid.UUID) (*metadata.User, error) {
	return s.meta.UserByID(ctx, id)
}

func (s *pgIdentityStore) createUserWithID(ctx context.Context, u *metadata.User) error {
	// email 唯一索引（lower(email)）冲突时返回唯一约束错误 → fail-closed（不静默覆盖）。
	const q = `
		INSERT INTO users (id, email, password_hash, status)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO NOTHING`
	_, err := s.db.ExecContext(ctx, q, u.ID, u.Email, u.PasswordHash, string(u.Status))
	return err
}

func (s *pgIdentityStore) MemberByWorkspaceAndUser(ctx context.Context, wsID, userID uuid.UUID) (*metadata.WorkspaceMember, error) {
	return s.meta.MemberByWorkspaceAndUser(ctx, wsID, userID)
}

func (s *pgIdentityStore) addMemberIfAbsent(ctx context.Context, m *metadata.WorkspaceMember) error {
	const q = `
		INSERT INTO workspace_members (workspace_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (workspace_id, user_id) DO NOTHING`
	_, err := s.db.ExecContext(ctx, q, m.WorkspaceID, m.UserID, string(m.Role))
	return err
}

// ---- 幂等编排 ---------------------------------------------------------------

// ensureIdentity 幂等确保演示 workspace/user/member 存在且与固定值一致。
// 冲突 fail-closed，不静默覆盖。
func ensureIdentity(ctx context.Context, s identityStore, cfg Config) error {
	if err := ensureWorkspace(ctx, s, cfg); err != nil {
		return err
	}
	if err := ensureUser(ctx, s, cfg); err != nil {
		return err
	}
	if err := ensureMember(ctx, s, cfg); err != nil {
		return err
	}
	return nil
}

// ensureWorkspace 幂等确保固定 workspace ID 存在且 name 一致。
func ensureWorkspace(ctx context.Context, s identityStore, cfg Config) error {
	ws, err := s.WorkspaceByID(ctx, cfg.WorkspaceID)
	switch {
	case err == nil:
		if ws.Name != demoWorkspaceName {
			return fmt.Errorf("%w: workspace %s 已存在但 name=%q 与演示值 %q 不一致",
				ErrDemoSeedRefused, cfg.WorkspaceID, ws.Name, demoWorkspaceName)
		}
		return nil
	case errors.Is(err, sql.ErrNoRows):
		newWS := &metadata.Workspace{ID: cfg.WorkspaceID, Name: demoWorkspaceName, Settings: json.RawMessage("{}")}
		if err := s.createWorkspaceWithID(ctx, newWS); err != nil {
			return fmt.Errorf("创建演示 workspace 失败: %w", err)
		}
		// TOCTOU 复核（CodeRabbit 回归项）：DO NOTHING 可能因并发插入而未实际写入，
		// 必须回读确认最终值一致，避免静默保留不一致数据。
		got, err := s.WorkspaceByID(ctx, cfg.WorkspaceID)
		if err != nil {
			return fmt.Errorf("创建后复核演示 workspace 失败: %w", err)
		}
		if got.Name != demoWorkspaceName {
			return fmt.Errorf("%w: workspace %s 创建后 name=%q 与演示值不一致（并发写入冲突）",
				ErrDemoSeedRefused, cfg.WorkspaceID, got.Name)
		}
		return nil
	default:
		return fmt.Errorf("读取演示 workspace 失败: %w", err)
	}
}

// ensureUser 幂等确保固定 user ID 存在、active 且 email 一致。
func ensureUser(ctx context.Context, s identityStore, cfg Config) error {
	u, err := s.UserByID(ctx, cfg.UserID)
	switch {
	case err == nil:
		if u.Status != metadata.UserStatusActive {
			return fmt.Errorf("%w: user %s 已存在但 status=%q（必须为 active）", ErrDemoSeedRefused, cfg.UserID, u.Status)
		}
		if !strings.EqualFold(u.Email, demoUserEmail) {
			return fmt.Errorf("%w: user %s 已存在但 email 与演示值不一致", ErrDemoSeedRefused, cfg.UserID)
		}
		return nil
	case errors.Is(err, sql.ErrNoRows):
		newU := &metadata.User{
			ID:           cfg.UserID,
			Email:        demoUserEmail,
			PasswordHash: demoPasswordHash,
			Status:       metadata.UserStatusActive,
		}
		if err := s.createUserWithID(ctx, newU); err != nil {
			// 唯一 email 冲突（另一 user 占用演示邮箱）→ 保持 fail-closed。
			return fmt.Errorf("创建演示 user 失败（可能 email 唯一冲突）: %w", err)
		}
		// TOCTOU 复核（CodeRabbit 回归项）：DO NOTHING 可能因并发插入而未写入，
		// 必须回读确认最终值一致。
		got, err := s.UserByID(ctx, cfg.UserID)
		if err != nil {
			return fmt.Errorf("创建后复核演示 user 失败: %w", err)
		}
		if got.Status != metadata.UserStatusActive || !strings.EqualFold(got.Email, demoUserEmail) {
			return fmt.Errorf("%w: user %s 创建后与演示值不一致（并发写入冲突）", ErrDemoSeedRefused, cfg.UserID)
		}
		return nil
	default:
		return fmt.Errorf("读取演示 user 失败: %w", err)
	}
}

// ensureMember 幂等确保固定 workspace/user 的 owner 成员存在。
func ensureMember(ctx context.Context, s identityStore, cfg Config) error {
	m, err := s.MemberByWorkspaceAndUser(ctx, cfg.WorkspaceID, cfg.UserID)
	switch {
	case err == nil:
		if m.Role != metadata.RoleOwner {
			return fmt.Errorf("%w: member (%s, %s) 已存在但 role=%q（必须为 owner）",
				ErrDemoSeedRefused, cfg.WorkspaceID, cfg.UserID, m.Role)
		}
		return nil
	case errors.Is(err, sql.ErrNoRows):
		nm := &metadata.WorkspaceMember{WorkspaceID: cfg.WorkspaceID, UserID: cfg.UserID, Role: metadata.RoleOwner}
		if err := s.addMemberIfAbsent(ctx, nm); err != nil {
			return fmt.Errorf("创建演示 member 失败: %w", err)
		}
		// TOCTOU 复核（CodeRabbit 回归项）：DO NOTHING 可能因并发插入而未写入，
		// 必须回读确认最终值一致。
		got, err := s.MemberByWorkspaceAndUser(ctx, cfg.WorkspaceID, cfg.UserID)
		if err != nil {
			return fmt.Errorf("创建后复核演示 member 失败: %w", err)
		}
		if got.Role != metadata.RoleOwner {
			return fmt.Errorf("%w: member (%s, %s) 创建后 role=%q 与演示值不一致（并发写入冲突）",
				ErrDemoSeedRefused, cfg.WorkspaceID, cfg.UserID, got.Role)
		}
		return nil
	default:
		return fmt.Errorf("读取演示 member 失败: %w", err)
	}
}

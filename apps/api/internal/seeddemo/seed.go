package seeddemo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fujiabao89/webdb/internal/connections"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// Run 执行演示 seed（幂等、可重复）。cfg 由 LoadConfig 校验产生；deps 注入存储依赖。
//
// 阶段顺序（任务书 §五）：identity → credentials/connections/policies → 最终一致性验证。
// 每阶段可重跑：一致即成功（no-op），固定 ID 冲突 fail-closed，不静默覆盖、不删除数据。
// 凭证创建由 LifecycleManager 原子事务控制，无法与 identity/connection 放入同一事务；
// 通过"连接存在性 + 孤立信封检测"实现可证明的幂等阶段（任务书 §五）。
func Run(ctx context.Context, cfg Config, deps Deps) error {
	logger := slog.Default()

	if err := ensureIdentity(ctx, deps.Identity, cfg); err != nil {
		return err
	}
	// 全局检测 seed 中断遗留的孤立 active envelope（未被任何连接引用）。
	// 无论连接是否已存在都必须 fail-closed（任务书 §五：可安全恢复或明确拒绝），
	// 避免连接全部存在时绕过该检查。
	if err := rejectOrphanEnvelopes(ctx, deps, cfg.WorkspaceID); err != nil {
		return err
	}
	for _, spec := range cfg.Connections {
		if err := ensureConnection(ctx, deps, cfg, spec); err != nil {
			return err
		}
	}
	if err := verifyConnections(ctx, deps, cfg); err != nil {
		return err
	}

	// 成功日志仅报告固定资源类别/数量，不报告 secret、DSN、KEK、token。
	logger.Info("demo seed 完成",
		"workspace_id", cfg.WorkspaceID.String(),
		"user_id", cfg.UserID.String(),
		"connections", len(cfg.Connections))
	return nil
}

// ensureConnection 幂等确保一个演示连接存在且一致；缺失时创建凭证→连接→策略。
func ensureConnection(ctx context.Context, deps Deps, cfg Config, spec ConnectionSpec) error {
	existing, err := deps.ConnReader.ConnectionByID(ctx, cfg.WorkspaceID, spec.ID)
	switch {
	case err == nil:
		return verifyExistingConnection(ctx, deps, cfg, spec, existing)
	case errors.Is(err, sql.ErrNoRows):
		return createConnection(ctx, deps, cfg, spec)
	default:
		return fmt.Errorf("读取演示连接 %s 失败: %w", spec.ID, err)
	}
}

// createConnection 创建凭证→连接→策略。凭证经 LifecycleManager 创建（随机
// secretRef/nonce/DEK、Envelope v1、正确 AAD、E3 审计原子提交）；connection 经
// connections.Service 创建（owner 门控 + E1 审计 + FOR KEY SHARE 强制 active envelope）。
func createConnection(ctx context.Context, deps Deps, cfg Config, spec ConnectionSpec) error {
	// 1. 凭证（LifecycleManager 原子事务 + E3 审计）。
	env, err := deps.Credentials.Create(ctx, cfg.WorkspaceID, cfg.UserID, credentials.CredentialPayload{
		User:     spec.CredentialUser,
		Password: spec.CredentialPassword,
	})
	if err != nil {
		// 根因仅进服务端日志（脱敏），对外保持稳定错误。
		return fmt.Errorf("创建演示凭证失败: %w", err)
	}

	// 2. 连接（owner 门控 + E1 审计原子提交）。secret_ref/version 与实际 envelope 一致。
	conn := &metadata.Connection{
		ID:            spec.ID,
		Name:          spec.Name,
		Engine:        spec.Engine,
		Host:          spec.Host,
		Port:          spec.Port,
		Database:      spec.Database,
		Environment:   spec.Environment,
		SecretRef:     env.SecretRef,
		SecretVersion: env.Version,
	}
	if _, err := deps.Connector.Create(ctx, connections.Principal{UserID: cfg.UserID, WorkspaceID: cfg.WorkspaceID}, conn); err != nil {
		// 连接创建失败 → 事务回滚无连接残留，但凭证已提交（孤立 envelope）。
		// 重跑时 findOrphanEnvelope 会 fail-closed 拒绝（不产生可被错误连接引用的 secret）。
		return fmt.Errorf("创建演示连接 %s 失败: %w", spec.ID, err)
	}

	// 3. 策略（allow_read=true 显式；安全默认；不启用 DML/DDL/export）。
	tr := true
	pol := &metadata.ConnectionPolicy{
		WorkspaceID:        cfg.WorkspaceID,
		ConnectionID:       spec.ID,
		AllowRead:          &tr,
		StatementTimeoutMs: policyStatementTimeoutMs,
		MaxRows:            policyMaxRows,
	}
	if err := deps.Policies.CreatePolicy(ctx, pol); err != nil {
		return fmt.Errorf("创建演示连接策略 %s 失败: %w", spec.ID, err)
	}
	return nil
}

// verifyExistingConnection 验证已存在连接与演示规格、策略、凭证完全一致。
// 任一不一致即 fail-closed（不静默覆盖）。
func verifyExistingConnection(ctx context.Context, deps Deps, cfg Config, spec ConnectionSpec, conn *metadata.Connection) error {
	if !connectionMatches(conn, cfg.WorkspaceID, spec) {
		return fmt.Errorf("%w: 连接 %s 已存在但字段与演示值不一致（workspace/name/engine/host/port/database/environment）",
			ErrDemoSeedRefused, spec.ID)
	}
	pol, err := deps.PolicyRead.PolicyByConnection(ctx, cfg.WorkspaceID, spec.ID)
	if err != nil {
		return fmt.Errorf("读取演示连接策略失败: %w", err)
	}
	if !policyMatches(pol) {
		return fmt.Errorf("%w: 连接 %s 的已存在策略不满足演示安全默认（allow_read=true, max_rows=%d, timeout=%dms）",
			ErrDemoSeedRefused, spec.ID, policyMaxRows, policyStatementTimeoutMs)
	}
	payload, err := deps.Resolver.ResolveCredential(ctx, cfg.WorkspaceID, conn.SecretRef, conn.SecretVersion)
	if err != nil {
		return fmt.Errorf("%w: 连接 %s 的凭证无法解析（KEK/版本不匹配或信封缺失）", ErrDemoSeedRefused, spec.ID)
	}
	if payload.User != spec.CredentialUser || payload.Password != spec.CredentialPassword {
		return fmt.Errorf("%w: 连接 %s 的已存在凭证与演示期望不一致", ErrDemoSeedRefused, spec.ID)
	}
	return nil
}

// connectionMatches 校验连接关键字段与演示规格一致（不含 secret_ref/version，由 Resolve 验证）。
func connectionMatches(c *metadata.Connection, wsID uuid.UUID, spec ConnectionSpec) bool {
	return c != nil &&
		c.WorkspaceID == wsID &&
		c.Name == spec.Name &&
		c.Engine == spec.Engine &&
		c.Host == spec.Host &&
		c.Port == spec.Port &&
		c.Database == spec.Database &&
		c.Environment == spec.Environment
}

// policyMatches 校验策略为显式 allow_read=true 且使用已批准安全默认。
func policyMatches(p *metadata.ConnectionPolicy) bool {
	return p != nil &&
		p.AllowRead != nil && *p.AllowRead &&
		p.StatementTimeoutMs == policyStatementTimeoutMs &&
		p.MaxRows == policyMaxRows
}

// rejectOrphanEnvelopes 存在孤立 active envelope 时 fail-closed 拒绝。
func rejectOrphanEnvelopes(ctx context.Context, deps Deps, wsID uuid.UUID) error {
	orphan, err := findOrphanEnvelope(ctx, deps, wsID)
	if err != nil {
		return err
	}
	if orphan != nil {
		return fmt.Errorf(
			"%w: 检测到未绑定连接的 active 凭证信封（通常是 seed 中途失败遗留）。"+
				"请运行 docker compose down -v 清理演示卷后重试，或人工检查该工作区的凭证信封",
			ErrDemoSeedRefused)
	}
	return nil
}

// findOrphanEnvelope 返回工作区内未被任何连接引用的 active 凭证信封（seed 中断遗留）。
// 返回 nil 表示无孤立信封。
func findOrphanEnvelope(ctx context.Context, deps Deps, wsID uuid.UUID) (*metadata.CredentialEnvelope, error) {
	envs, err := deps.Envelopes.ListEnvelopes(ctx, wsID)
	if err != nil {
		return nil, fmt.Errorf("读取凭证信封失败: %w", err)
	}
	conns, err := deps.ConnReader.ListConnections(ctx, wsID)
	if err != nil {
		return nil, fmt.Errorf("读取连接失败: %w", err)
	}
	referenced := make(map[string]struct{}, len(conns))
	for _, c := range conns {
		referenced[c.SecretRef.String()] = struct{}{}
	}
	for i := range envs {
		if envs[i].RetiredAt == nil {
			if _, ok := referenced[envs[i].SecretRef.String()]; !ok {
				return &envs[i], nil
			}
		}
	}
	return nil, nil
}

// verifyConnections 最终一致性验证：每个演示连接存在、一致且凭证可解析。
func verifyConnections(ctx context.Context, deps Deps, cfg Config) error {
	for _, spec := range cfg.Connections {
		conn, err := deps.ConnReader.ConnectionByID(ctx, cfg.WorkspaceID, spec.ID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: 最终一致性验证失败：演示连接 %s 缺失", ErrDemoSeedRefused, spec.ID)
			}
			return fmt.Errorf("最终一致性验证读取连接失败: %w", err)
		}
		if err := verifyExistingConnection(ctx, deps, cfg, spec, conn); err != nil {
			return err
		}
	}
	return nil
}

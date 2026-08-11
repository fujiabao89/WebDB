//go:build integration
// +build integration

package seeddemo

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/fujiabao89/webdb/internal/connections"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/fujiabao89/webdb/internal/migrate"
)

// ---- helpers -----------------------------------------------------------------

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func metaDSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		envOrDefault("META_DB_HOST", "localhost"),
		envOrDefault("META_DB_PORT", "5432"),
		envOrDefault("META_DB_USER", "webdb"),
		envOrDefault("META_DB_PASSWORD", "change_me"),
		envOrDefault("META_DB_NAME", "webdb_meta"),
		envOrDefault("META_DB_SSLMODE", "disable"),
	)
}

// setupSeedDB 连接测试元数据库并重置迁移（down-to 0 + up）。
func setupSeedDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", metaDSN())
	if err != nil {
		t.Fatalf("连接测试数据库失败: %v", err)
	}
	ctx := context.Background()
	_ = migrate.Run(ctx, db, "down-to", "0")
	if err := migrate.Run(ctx, db, "up"); err != nil {
		t.Fatalf("migration up 失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// setupIntegrationDeps 构造真实生产依赖（PGStore + LifecycleManager + connections.Service）。
func setupIntegrationDeps(t *testing.T) (*sql.DB, Deps) {
	t.Helper()
	db := setupSeedDB(t)
	store := metadata.NewPGStore(db)

	// 测试用合成 KEK（32 字节）。仅用于集成测试环境，非真实密钥。
	t.Setenv("WEBDB_ACTIVE_KEK_VERSION", "1")
	t.Setenv("WEBDB_KEK_V1", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32)))
	kek, err := credentials.NewEnvKEKProvider()
	if err != nil {
		t.Fatalf("KEK Provider 初始化失败: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	alarm := metadata.NewStderrSecurityAlarm()
	lifecycle := credentials.NewLifecycleManager(store, store, store, kek, alarm, logger)
	connSvc := connections.NewService(store, store, store, store, alarm, lifecycle, nil)

	deps := Deps{
		Identity:    &pgIdentityStore{db: db, meta: store},
		Credentials: lifecycle,
		Connector:   connSvc,
		Policies:    store,
		ConnReader:  store,
		PolicyRead:  store,
		Envelopes:   store,
		Resolver:    lifecycle,
	}
	return db, deps
}

func countQuery(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("查询失败 (%s): %v", q, err)
	}
	return n
}

// ---- 测试：空库迁移 + 首次创建 + 二次 no-op ------------------------------------

func TestSeedIntegration_freshThenNoop(t *testing.T) {
	db, deps := setupIntegrationDeps(t)
	ctx := context.Background()
	cfg := newTestConfig()

	if err := Run(ctx, cfg, deps); err != nil {
		t.Fatalf("首次 seed 失败: %v", err)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM workspaces`); n != 1 {
		t.Fatalf("workspaces 数量应为 1，实际 %d", n)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM users`); n != 1 {
		t.Fatalf("users 数量应为 1，实际 %d", n)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM workspace_members`); n != 1 {
		t.Fatalf("workspace_members 数量应为 1，实际 %d", n)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM connections`); n != 2 {
		t.Fatalf("connections 数量应为 2，实际 %d", n)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM connection_policies`); n != 2 {
		t.Fatalf("connection_policies 数量应为 2，实际 %d", n)
	}
	firstEnv := countQuery(t, db, `SELECT count(*) FROM credential_envelopes`)
	if firstEnv != 2 {
		t.Fatalf("credential_envelopes 数量应为 2，实际 %d", firstEnv)
	}
	firstAudit := countQuery(t, db, `SELECT count(*) FROM audit_events`)

	// 二次 seed：no-op，不新增任何 envelope/审计。
	if err := Run(ctx, cfg, deps); err != nil {
		t.Fatalf("二次 seed 失败: %v", err)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM credential_envelopes`); n != firstEnv {
		t.Fatalf("二次 seed 不应新增 envelope（无界新版本），%d != %d", n, firstEnv)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM audit_events`); n != firstAudit {
		t.Fatalf("二次 seed 不应新增审计事件，%d != %d", n, firstAudit)
	}
}

// ---- 测试：凭证可解析（正确 KEK/AAD） -------------------------------------------

func TestSeedIntegration_credentialsResolvable(t *testing.T) {
	_, deps := setupIntegrationDeps(t)
	ctx := context.Background()
	cfg := newTestConfig()

	if err := Run(ctx, cfg, deps); err != nil {
		t.Fatalf("seed 失败: %v", err)
	}
	for _, spec := range cfg.Connections {
		conn, err := deps.ConnReader.ConnectionByID(ctx, cfg.WorkspaceID, spec.ID)
		if err != nil {
			t.Fatalf("读取连接 %s 失败: %v", spec.ID, err)
		}
		payload, err := deps.Resolver.ResolveCredential(ctx, cfg.WorkspaceID, conn.SecretRef, conn.SecretVersion)
		if err != nil {
			t.Fatalf("连接 %s 凭证解析失败: %v", spec.ID, err)
		}
		if payload.User != spec.CredentialUser || payload.Password != spec.CredentialPassword {
			t.Fatalf("连接 %s 凭证 payload 与演示期望不一致: user=%q", spec.ID, payload.User)
		}
	}
}

// ---- 测试：密文/审计不包含明文（canary） -----------------------------------------

func TestSeedIntegration_noPlaintextInDB(t *testing.T) {
	db, deps := setupIntegrationDeps(t)
	ctx := context.Background()
	cfg := newTestConfig()

	if err := Run(ctx, cfg, deps); err != nil {
		t.Fatalf("seed 失败: %v", err)
	}
	secrets := []string{cfg.Connections[0].CredentialPassword, cfg.Connections[1].CredentialPassword}

	// credential_envelopes 所有加密字段不得含明文
	rows, err := db.Query(`SELECT encode(ciphertext,'hex'), encode(data_nonce,'hex'),
		encode(wrapped_dek,'hex'), encode(wrap_nonce,'hex') FROM credential_envelopes`)
	if err != nil {
		t.Fatalf("查询 envelope 失败: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cipher, dataNonce, wrappedDEK, wrapNonce string
		if err := rows.Scan(&cipher, &dataNonce, &wrappedDEK, &wrapNonce); err != nil {
			t.Fatalf("扫描 envelope 失败: %v", err)
		}
		for _, s := range secrets {
			for _, v := range []string{cipher, dataNonce, wrappedDEK, wrapNonce} {
				if strings.Contains(strings.ToLower(v), strings.ToLower(s)) {
					t.Fatalf("envelope 加密字段包含明文密码 canary")
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows 遍历失败: %v", err)
	}

	// audit_events metadata 不得含明文密码
	auditRows, err := db.Query(`SELECT metadata::text FROM audit_events`)
	if err != nil {
		t.Fatalf("查询 audit 失败: %v", err)
	}
	defer auditRows.Close()
	for auditRows.Next() {
		var md string
		if err := auditRows.Scan(&md); err != nil {
			t.Fatalf("扫描 audit 失败: %v", err)
		}
		for _, s := range secrets {
			if strings.Contains(md, s) {
				t.Fatalf("audit metadata 包含明文密码 canary")
			}
		}
	}
	if err := auditRows.Err(); err != nil {
		t.Fatalf("audit rows 遍历失败: %v", err)
	}
}

// ---- 测试：冲突 fail-closed ----------------------------------------------------

func TestSeedIntegration_tamperedConnectionRejected(t *testing.T) {
	db, deps := setupIntegrationDeps(t)
	ctx := context.Background()
	cfg := newTestConfig()

	if err := Run(ctx, cfg, deps); err != nil {
		t.Fatalf("首次 seed 失败: %v", err)
	}
	// 篡改连接 name，模拟固定 ID 字段冲突
	if _, err := db.Exec(`UPDATE connections SET name='tampered' WHERE id=$1`, cfg.Connections[0].ID); err != nil {
		t.Fatalf("篡改连接失败: %v", err)
	}
	if err := Run(ctx, cfg, deps); err == nil {
		t.Fatal("已存在但字段不一致时二次 seed 应 fail-closed")
	}
}

func TestSeedIntegration_orphanEnvelopeRejected(t *testing.T) {
	_, deps := setupIntegrationDeps(t)
	ctx := context.Background()
	cfg := newTestConfig()

	if err := Run(ctx, cfg, deps); err != nil {
		t.Fatalf("首次 seed 失败: %v", err)
	}
	// 额外创建一个未绑定连接的凭证（模拟上次 seed 中断遗留的孤立 envelope）
	// 注意：必须经 LifecycleManager 创建（不允许手工构造 envelope）。
	if _, err := deps.Credentials.Create(ctx, cfg.WorkspaceID, cfg.UserID, credentials.CredentialPayload{
		User: "demo_reader", Password: "orphan-secret",
	}); err != nil {
		t.Fatalf("创建孤立凭证失败: %v", err)
	}
	if err := Run(ctx, cfg, deps); err == nil {
		t.Fatal("存在孤立 active envelope 时二次 seed 应明确拒绝")
	}
}

// P1（Codex）：连接已存在但策略缺失（上次 seed 在策略写入前中断）→ 二次 seed 补建而非拒绝。
func TestSeedIntegration_policyMissingRecovered(t *testing.T) {
	db, deps := setupIntegrationDeps(t)
	ctx := context.Background()
	cfg := newTestConfig()

	if err := Run(ctx, cfg, deps); err != nil {
		t.Fatalf("首次 seed 失败: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM connection_policies WHERE connection_id=$1`, cfg.Connections[0].ID); err != nil {
		t.Fatalf("删除策略失败: %v", err)
	}
	if err := Run(ctx, cfg, deps); err != nil {
		t.Fatalf("策略缺失应作为可恢复阶段补建而非拒绝: %v", err)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM connection_policies WHERE connection_id=$1`, cfg.Connections[0].ID); n != 1 {
		t.Fatalf("缺失策略应被补建，实际 %d", n)
	}
}

// P2（Codex）：已存在固定 ID 策略开启 allow_write 时二次 seed fail-closed。
func TestSeedIntegration_policyWriteEnabledRejected(t *testing.T) {
	db, deps := setupIntegrationDeps(t)
	ctx := context.Background()
	cfg := newTestConfig()

	if err := Run(ctx, cfg, deps); err != nil {
		t.Fatalf("首次 seed 失败: %v", err)
	}
	if _, err := db.Exec(`UPDATE connection_policies SET allow_write=true WHERE connection_id=$1`, cfg.Connections[0].ID); err != nil {
		t.Fatalf("篡改策略失败: %v", err)
	}
	if err := Run(ctx, cfg, deps); err == nil {
		t.Fatal("allow_write=true 的已存在策略应 fail-closed")
	}
}

// ---- 测试：门控先于数据库 --------------------------------------------------------

func TestSeedIntegration_switchGateBeforeDB(t *testing.T) {
	t.Setenv("WEBDB_DEMO_SEED", "") // 缺失
	// 故意不设 META_DB_*：若门控先于 DB 连接，应返回门控错误而非 DB 错误。
	err := RunFromEnv(context.Background())
	if err == nil {
		t.Fatal("WEBDB_DEMO_SEED 缺失时 seed 应拒绝")
	}
	if !strings.Contains(err.Error(), "WEBDB_DEMO_SEED") {
		t.Fatalf("错误应报告开关缺失，实际: %v", err)
	}
}

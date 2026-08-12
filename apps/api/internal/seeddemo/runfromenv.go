package seeddemo

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/fujiabao89/webdb/internal/connections"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
)

// RunFromEnv 从部署环境运行演示 seed（main 的 seed-demo 子命令入口）。
//
// 安全边界：
//   - 门控：WEBDB_DEMO_SEED 必须严格为 "true"，否则拒绝且不触碰数据库。
//   - 演示数据库密码仅从环境读取并交由 LifecycleManager 加密；
//     KEK（WEBDB_KEK_V{N}+WEBDB_ACTIVE_KEK_VERSION）缺失/非法时 fail-closed。
//   - 不接受任何命令行明文密码；不提供通用任意 seed 参数。
func RunFromEnv(ctx context.Context) error {
	if err := validateDemoSwitch(os.Getenv(demoSwitchEnv)); err != nil {
		return err
	}
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		return err
	}

	db, err := openMetaDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()

	store := metadata.NewPGStore(db)

	// KEK：仅从部署环境读取；缺失/非法即拒绝（不输出变量值）。
	kek, err := credentials.NewEnvKEKProvider()
	if err != nil {
		return fmt.Errorf("%w: KEK 配置无效: %v", ErrDemoSeedRefused, err)
	}
	logger := slog.Default()

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
	return Run(ctx, cfg, deps)
}

// buildMetaDSN 通过标准 URL API 构造 PostgreSQL DSN（安全转义），
// 复用 migrate 路径（cmd/server main.metaDSN）的 url.UserPassword 语义。
// 手工拼接 keyword-DSN 会在 password/user/dbname 含空格、引号、反斜杠、@:?#% 等
// 语法字符时改变或破坏解析结果（Codex P1 二轮）；URL 构造对其统一百分号转义，
// 且可被当前 pgx driver/config parser 正确还原。
func buildMetaDSN(host, port, user, password, dbname, sslmode string) string {
	q := url.Values{}
	q.Set("sslmode", sslmode)
	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     net.JoinHostPort(host, port),
		Path:     dbname,
		RawQuery: q.Encode(),
	}
	return u.String()
}

// openMetaDB 连接 WebDB 元数据库（seed 写业务表使用运行时账号 META_DB_USER）。
// 使用调用方 ctx 派生有界超时（CodeRabbit 回归项：seed 路径端到端可取消上下文，
// SIGINT/SIGTERM 可中断 PingContext，避免强杀放大孤立 envelope 失败态）。
func openMetaDB(ctx context.Context) (*sql.DB, error) {
	dsn := buildMetaDSN(
		envOr("META_DB_HOST", "webdb-meta"),
		envOr("META_DB_PORT", "5432"),
		envOr("META_DB_USER", "webdb"),
		envOr("META_DB_PASSWORD", "change_me"),
		envOr("META_DB_NAME", "webdb_meta"),
		envOr("META_DB_SSLMODE", "disable"),
	)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("连接元数据库失败: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("元数据库不可达: %w", err)
	}
	return db, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

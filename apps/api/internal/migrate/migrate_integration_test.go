//go:build integration

package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// metaDSN 返回元数据库（webdb_meta）DSN，与 internal/metadata 集成测试一致。
func metaDSN() string {
	env := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		env("META_DB_HOST", "localhost"), env("META_DB_PORT", "5432"),
		env("META_DB_USER", "webdb"), env("META_DB_PASSWORD", "change_me"),
		env("META_DB_NAME", "webdb_meta"), env("META_DB_SSLMODE", "disable"),
	)
}

// TestMigration00003UpDownUp 验证迁移 00003 在真实 PostgreSQL 上支持 up→down→up：
// up 建索引、down 删索引、再 up 重建索引；结束保持 up 状态（不破坏共享元数据库）。
// 元数据库不可达（本机未提供 webdb_meta）时 skip，由 CI 提供。
func TestMigration00003UpDownUp(t *testing.T) {
	db, err := sql.Open("pgx", metaDSN())
	if err != nil {
		t.Skipf("open meta db: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Skipf("meta db unreachable (need webdb_meta on META_DB_HOST/PORT): %v", err)
	}
	var hasConn bool
	if err := db.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='connections')",
	).Scan(&hasConn); err != nil || !hasConn {
		t.Skip("connections table not present; need seeded metadata db")
	}

	idxExists := func() bool {
		t.Helper()
		var n int
		if err := db.QueryRow(
			"SELECT COUNT(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname='idx_connections_ws_created_at'",
		).Scan(&n); err != nil {
			t.Fatalf("query index: %v", err)
		}
		return n > 0
	}

	ctx := context.Background()
	// 确保已到最新版本（up 无操作；fresh DB 则完整迁移）。
	if err := Run(ctx, db, "up"); err != nil {
		t.Fatalf("up to latest: %v", err)
	}
	if !idxExists() {
		t.Fatal("index missing after up")
	}
	// down 一步 → 00003 Down，索引被删。
	if err := Run(ctx, db, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}
	if idxExists() {
		t.Fatal("index should be dropped after down")
	}
	// up → 00003 Up，索引重建。
	if err := Run(ctx, db, "up"); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	if !idxExists() {
		t.Fatal("index should exist after re-up")
	}
}

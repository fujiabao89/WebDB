package migrate

import (
	"strings"
	"testing"
)

// TestValidateMigrations 校验迁移注册表完整性（goose.CollectMigrations + 文件非空），
// 不连接数据库。
func TestValidateMigrations(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// TestMigration00003ConcurrentIndexDirectives 守卫 00003 的并发索引指令：
// CONCURRENTLY 必须在 Goose NO TRANSACTION 下事务外执行，且 Up/Down 均需
// CONCURRENTLY，索引名与列保持不变（WEB-36 审查）。
func TestMigration00003ConcurrentIndexDirectives(t *testing.T) {
	src, err := migrations.ReadFile("migrations/00003_add_connections_ws_created_at_idx.sql")
	if err != nil {
		t.Fatalf("read 00003: %v", err)
	}
	content := string(src)
	if !strings.Contains(content, "-- +goose NO TRANSACTION") {
		t.Fatal("00003 must declare NO TRANSACTION to allow CREATE/DROP INDEX CONCURRENTLY")
	}
	if !strings.Contains(content, "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_connections_ws_created_at") {
		t.Fatal("00003 Up must use CREATE INDEX CONCURRENTLY IF NOT EXISTS")
	}
	if !strings.Contains(content, "DROP INDEX CONCURRENTLY IF EXISTS idx_connections_ws_created_at") {
		t.Fatal("00003 Down must use DROP INDEX CONCURRENTLY IF EXISTS")
	}
	if !strings.Contains(content, "ON connections (workspace_id, created_at DESC)") {
		t.Fatal("00003 must preserve index name and columns")
	}
}

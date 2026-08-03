//go:build integration
// +build integration

package credentials

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/fujiabao89/webdb/internal/migrate"
	"github.com/google/uuid"
)

// 本文件覆盖 proposal §14.4 的 LIFE-07（并发轮换）与 LIFE-08（事务中间失败回滚），
// 在真实 PostgreSQL 集成环境验证事务原子性与回滚语义。

// ---- helpers ----

// integDSN 用 URL 构造连接串，对 user/password 做 URL 转义，
// 避免含空格/引号/反斜杠的 env 值破坏 keyword/value DSN（qodo #4）。
func integDSN() string {
	host := envOr("META_DB_HOST", "localhost")
	port := envOr("META_DB_PORT", "5432")
	user := envOr("META_DB_USER", "webdb")
	password := envOr("META_DB_PASSWORD", "change_me")
	dbname := envOr("META_DB_NAME", "webdb_meta")
	sslmode := envOr("META_DB_SSLMODE", "disable")

	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   net.JoinHostPort(host, port),
		Path:   "/" + dbname,
	}
	q := u.Query()
	q.Set("sslmode", sslmode)
	u.RawQuery = q.Encode()
	return u.String()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// integDB 用带超时的 context 连接并 Ping，避免 SQL 操作不可取消（qodo #2）。
func integDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", integDSN())
	if err != nil {
		t.Fatalf("连接测试数据库失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		// 关闭已成功打开的 db，避免失败路径资源泄漏
		db.Close()
		t.Fatalf("ping 测试数据库失败: %v", err)
	}
	return db
}

// setupLifecycle 迁移到最新 schema 并创建 user/workspace/member（供 Connection 引用）。
// 不吞掉 down-to 迁移错误（qodo #1）。
func setupLifecycle(t *testing.T) (*sql.DB, *metadata.PGStore, context.Context, *metadata.User, *metadata.Workspace) {
	t.Helper()
	db := integDB(t)
	ctx := context.Background()

	if err := migrate.Run(ctx, db, "down-to", "0"); err != nil {
		t.Fatalf("migration down-to-0 失败: %v", err)
	}
	if err := migrate.Run(ctx, db, "up"); err != nil {
		t.Fatalf("migration up 失败: %v", err)
	}

	store := metadata.NewPGStore(db)

	u := &metadata.User{Email: "lifecycle@example.com", PasswordHash: "hash123"}
	if err := store.CreateUser(ctx, u); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	ws := &metadata.Workspace{Name: "lifecycle-integ"}
	if err := store.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("创建工作区失败: %v", err)
	}

	m := &metadata.WorkspaceMember{WorkspaceID: ws.ID, UserID: u.ID, Role: metadata.RoleOwner}
	if err := store.AddMember(ctx, m); err != nil {
		t.Fatalf("添加成员失败: %v", err)
	}

	return db, store, ctx, u, ws
}

// createTestConnection 创建引用指定凭证版本的 Connection，
// 使 Rotate 的 UPDATE connections 有真实行可作用（CodeRabbit #6）。
func createTestConnection(t *testing.T, store *metadata.PGStore, ctx context.Context, wsID, userID, secretRef uuid.UUID, version int) *metadata.Connection {
	t.Helper()
	conn := &metadata.Connection{
		WorkspaceID:   wsID,
		Name:          "rotate-conn",
		Engine:        metadata.EnginePostgreSQL,
		Host:          "localhost",
		Port:          5432,
		Database:      "testdb",
		Environment:   metadata.EnvDevelopment,
		SecretRef:     secretRef,
		SecretVersion: version,
		CreatedBy:     userID,
	}
	if err := store.CreateConnection(ctx, conn); err != nil {
		t.Fatalf("创建连接失败: %v", err)
	}
	return conn
}

// connectionSecretVersion 查询连接当前引用的 secret_version。
func connectionSecretVersion(t *testing.T, store *metadata.PGStore, ctx context.Context, wsID, connID uuid.UUID) int {
	t.Helper()
	conn, err := store.ConnectionByID(ctx, wsID, connID)
	if err != nil {
		t.Fatalf("查询连接失败: %v", err)
	}
	return conn.SecretVersion
}

// failingConnStore 包装真实 ConnectionTXStore，仅在 UpdateConnectionVersion 注入失败，
// 用于验证 Rotate 在 INSERT 新 envelope 后、COMMIT 前失败时事务完整回滚（LIFE-08）。
type failingConnStore struct {
	metadata.ConnectionTXStore
}

func (f *failingConnStore) UpdateConnectionVersion(ctx context.Context, tx *sql.Tx, wsID, secretRef uuid.UUID, newVersion int) error {
	// 先委托真实实现执行 UPDATE connections，再注入失败，
	// 使测试同时验证真实的连接版本更新与 envelope 插入都在同一事务中被回滚。
	if err := f.ConnectionTXStore.UpdateConnectionVersion(ctx, tx, wsID, secretRef, newVersion); err != nil {
		return err
	}
	return errors.New("injected failure: update connection version")
}

// ---- LIFE-07：并发轮换 ----

// 两个并发 RotateCredential 对同一 secret 以相同 expectedVersion 轮换：
// 恰好一个成功，另一个因 expected_version 不匹配返回 version_conflict。
// 成功轮换后连接引用的 secret_version 应更新为 2。
func TestLifecycleRotateConcurrentPostgres(t *testing.T) {
	db, store, ctx, u, ws := setupLifecycle(t)
	defer db.Close()

	lm := NewLifecycleManager(db, store, store, goodKEK())

	env, err := lm.Create(ctx, ws.ID, CredentialPayload{User: "u", Password: "p"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if env.Version != 1 {
		t.Fatalf("expected initial version 1, got %d", env.Version)
	}

	conn := createTestConnection(t, store, ctx, ws.ID, u.ID, env.SecretRef, env.Version)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := lm.Rotate(ctx, ws.ID, env.SecretRef, 1, CredentialPayload{User: "u2", Password: "p2"})
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	success, conflict := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrVersionConflict):
			conflict++
		default:
			t.Errorf("unexpected rotate error: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("expected exactly 1 success and 1 version_conflict, got success=%d conflict=%d errs=%v",
			success, conflict, errs)
	}

	envs, err := store.ListEnvelopesByRef(ctx, ws.ID, env.SecretRef)
	if err != nil {
		t.Fatalf("list envelopes: %v", err)
	}
	maxVersion := 0
	for _, e := range envs {
		if e.Version > maxVersion {
			maxVersion = e.Version
		}
	}
	if maxVersion != 2 {
		t.Fatalf("expected max version 2 after successful rotate, got %d", maxVersion)
	}

	// 成功轮换后连接引用应指向新版本 2（CodeRabbit #6）
	if got := connectionSecretVersion(t, store, ctx, ws.ID, conn.ID); got != 2 {
		t.Fatalf("expected connection secret_version 2 after rotate, got %d", got)
	}
}

// ---- LIFE-08：事务中间失败回滚 ----

// Rotate 在 INSERT 新 envelope 后、COMMIT 前（UPDATE connections）失败时，
// 事务必须完整回滚：旧版本不受影响、新版本不残留、连接引用保持不变。
func TestLifecycleRotateTxFailureRollbackPostgres(t *testing.T) {
	db, store, ctx, u, ws := setupLifecycle(t)
	defer db.Close()

	lm := NewLifecycleManager(db, store, store, goodKEK())
	env, err := lm.Create(ctx, ws.ID, CredentialPayload{User: "u", Password: "p"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	conn := createTestConnection(t, store, ctx, ws.ID, u.ID, env.SecretRef, env.Version)

	// 用注入失败的 conns 触发 Rotate 中间失败
	failing := &failingConnStore{ConnectionTXStore: store}
	lmFailing := NewLifecycleManager(db, store, failing, goodKEK())
	_, err = lmFailing.Rotate(ctx, ws.ID, env.SecretRef, 1, CredentialPayload{User: "u2", Password: "p2"})
	if err == nil {
		t.Fatal("expected rotate to fail on injected UpdateConnectionVersion error")
	}

	// 验证回滚：仅存 version 1，无 version 2 残留
	envs, err := store.ListEnvelopesByRef(ctx, ws.ID, env.SecretRef)
	if err != nil {
		t.Fatalf("list envelopes: %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("expected 1 envelope after rollback, got %d (versions=%v)", len(envs), collectVersions(envs))
	}
	if envs[0].Version != 1 {
		t.Fatalf("expected only version 1 after rollback, got version %d", envs[0].Version)
	}

	// 旧版本仍可解析（未被轮换污染）
	if _, err := lm.Resolve(ctx, ws.ID, env.SecretRef, 1); err != nil {
		t.Fatalf("resolve v1 after rollback: %v", err)
	}

	// 连接引用保持不变（仍为版本 1）（CodeRabbit #6）
	if got := connectionSecretVersion(t, store, ctx, ws.ID, conn.ID); got != 1 {
		t.Fatalf("expected connection secret_version 1 after rollback, got %d", got)
	}
}

func collectVersions(envs []metadata.CredentialEnvelope) []int {
	out := make([]int, 0, len(envs))
	for _, e := range envs {
		out = append(out, e.Version)
	}
	return out
}

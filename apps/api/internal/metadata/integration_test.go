//go:build integration
// +build integration

package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/fujiabao89/webdb/internal/migrate"
	"github.com/google/uuid"
)

// ---- helpers ---------------------------------------------------------------

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := metaDSN()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("连接测试数据库失败: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping 测试数据库失败: %v", err)
	}
	return db
}

func metaDSN() string {
	host := envOrDefault("META_DB_HOST", "localhost")
	port := envOrDefault("META_DB_PORT", "5432")
	user := envOrDefault("META_DB_USER", "webdb")
	password := envOrDefault("META_DB_PASSWORD", "change_me")
	dbname := envOrDefault("META_DB_NAME", "webdb_meta")
	sslmode := envOrDefault("META_DB_SSLMODE", "disable")

	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		host, port, user, password, dbname, sslmode,
	)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func setupDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	db := testDB(t)
	ctx := context.Background()

	_ = migrate.Run(ctx, db, "down-to", "0")
	if err := migrate.Run(ctx, db, "up"); err != nil {
		t.Fatalf("migration up 失败: %v", err)
	}

	cleanup := func() {
		db.Close()
	}
	return db, cleanup
}

func setupFull(t *testing.T) (*sql.DB, *PGStore, *User, *Workspace, *WorkspaceMember, *CredentialEnvelope, *Connection, func()) {
	t.Helper()
	db := testDB(t)
	ctx := context.Background()

	_ = migrate.Run(ctx, db, "down-to", "0")
	if err := migrate.Run(ctx, db, "up"); err != nil {
		t.Fatalf("migration up 失败: %v", err)
	}

	store := NewPGStore(db)

	u := &User{Email: "full@example.com", PasswordHash: "hash123"}
	if err := store.CreateUser(ctx, u); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	ws := &Workspace{Name: "full-ws"}
	if err := store.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("创建工作区失败: %v", err)
	}

	m := &WorkspaceMember{WorkspaceID: ws.ID, UserID: u.ID, Role: RoleOwner}
	if err := store.AddMember(ctx, m); err != nil {
		t.Fatalf("添加成员失败: %v", err)
	}

	env := &CredentialEnvelope{
		WorkspaceID:   ws.ID,
		SecretRef:     uuid.New(),
		Version:       1,
		Ciphertext:    []byte{1, 2, 3},
		DataNonce:     []byte{4, 5, 6},
		WrappedDEK:    []byte{7, 8, 9},
		WrapNonce:     []byte{10, 11, 12},
		EnvelopeSuite: "aes256-gcm-hkdf-sha256",
		KEKVersion:    1,
	}
	if err := store.CreateEnvelope(ctx, env); err != nil {
		t.Fatalf("创建凭证信封失败: %v", err)
	}

	conn := &Connection{
		WorkspaceID:   ws.ID,
		Name:          "full-conn",
		Engine:        EnginePostgreSQL,
		Host:          "localhost",
		Port:          5432,
		Database:      "testdb",
		Environment:   EnvDevelopment,
		SecretRef:     env.SecretRef,
		SecretVersion: 1,
		CreatedBy:     u.ID,
	}
	if err := store.CreateConnection(ctx, conn); err != nil {
		t.Fatalf("创建连接失败: %v", err)
	}

	cleanup := func() {
		db.Close()
	}
	return db, store, u, ws, m, env, conn, cleanup
}

// ---- migration 测试 --------------------------------------------------------

func TestMigration_UpDownUpUp_NoSideEffects(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	ctx := context.Background()

	_ = migrate.Run(ctx, db, "down-to", "0")
	if err := migrate.Run(ctx, db, "up"); err != nil {
		t.Fatalf("首次 up 失败: %v", err)
	}
	if err := migrate.Run(ctx, db, "down-to", "0"); err != nil {
		t.Fatalf("down 失败: %v", err)
	}
	if err := migrate.Run(ctx, db, "up"); err != nil {
		t.Fatalf("第二次 up 失败: %v", err)
	}
	if err := migrate.Run(ctx, db, "up"); err != nil {
		t.Fatalf("重复 up 失败: %v", err)
	}
}

func TestMigration_Validate(t *testing.T) {
	if err := migrate.Validate(); err != nil {
		t.Fatalf("migration 验证失败: %v", err)
	}
}

// ---- users 约束测试 --------------------------------------------------------

func TestUser_EmptyEmail_Rejected(t *testing.T) {
	db, cleanup := setupDB(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	err := store.CreateUser(ctx, &User{Email: "", PasswordHash: "hash"})
	if err == nil {
		t.Fatal("期望空 email 被拒绝")
	}
}

func TestUser_WhitespaceEmail_Rejected(t *testing.T) {
	db, cleanup := setupDB(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	err := store.CreateUser(ctx, &User{Email: "  test@example.com  ", PasswordHash: "hash"})
	if err == nil {
		t.Fatal("期望首尾空白 email 被拒绝")
	}
}

func TestUser_DuplicateCaseInsensitiveEmail_Rejected(t *testing.T) {
	db, cleanup := setupDB(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	store.CreateUser(ctx, &User{Email: "Test@Example.com", PasswordHash: "hash"})
	err := store.CreateUser(ctx, &User{Email: "test@example.com", PasswordHash: "hash"})
	if err == nil {
		t.Fatal("期望大小写不敏感重复 email 被拒绝")
	}
}

func TestUser_EmptyPasswordHash_Rejected(t *testing.T) {
	db, cleanup := setupDB(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	err := store.CreateUser(ctx, &User{Email: "user@example.com", PasswordHash: ""})
	if err == nil {
		t.Fatal("期望空 password_hash 被拒绝")
	}
}

func TestUser_WhitespacePasswordHash_Rejected(t *testing.T) {
	db, cleanup := setupDB(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	err := store.CreateUser(ctx, &User{Email: "u@ex.com", PasswordHash: "   "})
	if err == nil {
		t.Fatal("期望纯空白 password_hash 被拒绝")
	}
}

func TestUser_IllegalStatus_Rejected(t *testing.T) {
	db, cleanup := setupDB(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	u := &User{Email: "user@example.com", PasswordHash: "hash", Status: "banned"}
	err := store.CreateUser(ctx, u)
	if err == nil {
		t.Fatal("期望非法 status 被拒绝")
	}
}

// ---- workspace_members 约束测试 -------------------------------------------

func TestMember_OrphanWorkspace_Rejected(t *testing.T) {
	db, cleanup := setupDB(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	err := store.AddMember(ctx, &WorkspaceMember{
		WorkspaceID: uuid.New(), UserID: uuid.New(), Role: RoleViewer,
	})
	if err == nil {
		t.Fatal("期望孤儿 workspace member 被拒绝")
	}
}

func TestMember_OrphanUser_Rejected(t *testing.T) {
	db, cleanup := setupDB(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	ws := &Workspace{Name: "orphan-ws"}
	store.CreateWorkspace(ctx, ws)

	err := store.AddMember(ctx, &WorkspaceMember{
		WorkspaceID: ws.ID, UserID: uuid.New(), Role: RoleViewer,
	})
	if err == nil {
		t.Fatal("期望孤儿 user member 被拒绝")
	}
}

func TestMember_NullWorkspaceID_Rejected(t *testing.T) {
	db, cleanup := setupDB(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	err := store.AddMember(ctx, &WorkspaceMember{
		UserID: uuid.New(), Role: RoleViewer,
	})
	if err == nil {
		t.Fatal("期望 NULL workspace_id 被拒绝")
	}
}

func TestMember_IllegalRole_Rejected(t *testing.T) {
	db, cleanup := setupDB(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	u := &User{Email: "role@example.com", PasswordHash: "hash"}
	store.CreateUser(ctx, u)
	ws := &Workspace{Name: "role-ws"}
	store.CreateWorkspace(ctx, ws)

	err := store.AddMember(ctx, &WorkspaceMember{
		WorkspaceID: ws.ID, UserID: u.ID, Role: "superadmin",
	})
	if err == nil {
		t.Fatal("期望非法 role 被拒绝")
	}
}

// ---- credential_envelopes 约束测试 ----------------------------------------

func TestEnvelope_OrphanWorkspace_Rejected(t *testing.T) {
	db, cleanup := setupDB(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	err := store.CreateEnvelope(ctx, &CredentialEnvelope{
		WorkspaceID: uuid.New(), SecretRef: uuid.New(), Version: 1,
		Ciphertext: []byte{1}, DataNonce: []byte{1}, WrappedDEK: []byte{1}, WrapNonce: []byte{1},
		EnvelopeSuite: "aes", KEKVersion: 1,
	})
	if err == nil {
		t.Fatal("期望孤儿 credential envelope 被拒绝")
	}
}

func TestEnvelope_ZeroLengthCiphertext_Rejected(t *testing.T) {
	db, cleanup := setupDB(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	ws := &Workspace{Name: "env-ws"}
	store.CreateWorkspace(ctx, ws)

	err := store.CreateEnvelope(ctx, &CredentialEnvelope{
		WorkspaceID: ws.ID, SecretRef: uuid.New(), Version: 1,
		Ciphertext: []byte{}, DataNonce: []byte{1}, WrappedDEK: []byte{1}, WrapNonce: []byte{1},
		EnvelopeSuite: "aes", KEKVersion: 1,
	})
	if err == nil {
		t.Fatal("期望零长度 ciphertext 被拒绝")
	}
}

func TestEnvelope_NegativeVersion_Rejected(t *testing.T) {
	db, cleanup := setupDB(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	ws := &Workspace{Name: "ver-ws"}
	store.CreateWorkspace(ctx, ws)

	err := store.CreateEnvelope(ctx, &CredentialEnvelope{
		WorkspaceID: ws.ID, SecretRef: uuid.New(), Version: -1,
		Ciphertext: []byte{1}, DataNonce: []byte{1}, WrappedDEK: []byte{1}, WrapNonce: []byte{1},
		EnvelopeSuite: "aes", KEKVersion: 1,
	})
	if err == nil {
		t.Fatal("期望负 version 被拒绝")
	}
}

func TestEnvelope_MissingEnvelopeSuite_Rejected(t *testing.T) {
	db, cleanup := setupDB(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	ws := &Workspace{Name: "suite-ws"}
	store.CreateWorkspace(ctx, ws)

	err := store.CreateEnvelope(ctx, &CredentialEnvelope{
		WorkspaceID: ws.ID, SecretRef: uuid.New(), Version: 1,
		Ciphertext: []byte{1}, DataNonce: []byte{1}, WrappedDEK: []byte{1}, WrapNonce: []byte{1},
		EnvelopeSuite: "", KEKVersion: 1,
	})
	if err == nil {
		t.Fatal("期望空 envelope_suite 被拒绝")
	}
}

// ---- connections 约束测试 -------------------------------------------------

func TestConnection_MissingEnvironment_Rejected(t *testing.T) {
	db, _, _, ws, _, _, _, cleanup := setupFull(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	u := &User{Email: "conn-env@ex.com", PasswordHash: "hash"}
	store.CreateUser(ctx, u)
	store.AddMember(ctx, &WorkspaceMember{WorkspaceID: ws.ID, UserID: u.ID, Role: RoleAdmin})

	env := &CredentialEnvelope{
		WorkspaceID: ws.ID, SecretRef: uuid.New(), Version: 1,
		Ciphertext: []byte{1}, DataNonce: []byte{1}, WrappedDEK: []byte{1}, WrapNonce: []byte{1},
		EnvelopeSuite: "aes", KEKVersion: 1,
	}
	store.CreateEnvelope(ctx, env)

	err := store.CreateConnection(ctx, &Connection{
		WorkspaceID: ws.ID, Name: "no-env", Engine: EnginePostgreSQL,
		Host: "localhost", Port: 5432, Database: "db",
		Environment: "",
		SecretRef:   env.SecretRef, SecretVersion: 1, CreatedBy: u.ID,
	})
	if err == nil {
		t.Fatal("期望缺失 environment 被拒绝")
	}
}

func TestConnection_IllegalEngine_Rejected(t *testing.T) {
	db, _, _, ws, _, _, _, cleanup := setupFull(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	err := store.CreateConnection(ctx, &Connection{
		WorkspaceID: ws.ID, Name: "bad-engine", Engine: "sqlite",
		Host: "localhost", Port: 5432, Database: "db",
		Environment: EnvDevelopment,
		SecretRef:   uuid.New(), SecretVersion: 1, CreatedBy: uuid.New(),
	})
	if err == nil {
		t.Fatal("期望非法 engine 被拒绝")
	}
}

func TestConnection_IllegalEnvironment_Rejected(t *testing.T) {
	db, _, _, ws, _, _, _, cleanup := setupFull(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	err := store.CreateConnection(ctx, &Connection{
		WorkspaceID: ws.ID, Name: "bad-env", Engine: EnginePostgreSQL,
		Host: "localhost", Port: 5432, Database: "db",
		Environment: "testing",
		SecretRef:   uuid.New(), SecretVersion: 1, CreatedBy: uuid.New(),
	})
	if err == nil {
		t.Fatal("期望非法 environment 被拒绝")
	}
}

func TestConnection_CrossWorkspaceEnvelope_Rejected(t *testing.T) {
	db, _, _, ws, _, _, _, cleanup := setupFull(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	ws2 := &Workspace{Name: "ws2"}
	store.CreateWorkspace(ctx, ws2)
	env2 := &CredentialEnvelope{
		WorkspaceID: ws2.ID, SecretRef: uuid.New(), Version: 1,
		Ciphertext: []byte{1}, DataNonce: []byte{1}, WrappedDEK: []byte{1}, WrapNonce: []byte{1},
		EnvelopeSuite: "aes", KEKVersion: 1,
	}
	store.CreateEnvelope(ctx, env2)

	err := store.CreateConnection(ctx, &Connection{
		WorkspaceID: ws.ID, Name: "cross-ws", Engine: EnginePostgreSQL,
		Host: "localhost", Port: 5432, Database: "db",
		Environment: EnvDevelopment,
		SecretRef:   env2.SecretRef, SecretVersion: 1, CreatedBy: uuid.New(),
	})
	if err == nil {
		t.Fatal("期望跨工作区信封引用被拒绝")
	}
}

func TestConnectionWritesRejectRetiredCredential(t *testing.T) {
	db, store, _, _, _, envV1, conn, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	envV2 := &CredentialEnvelope{
		WorkspaceID:   envV1.WorkspaceID,
		SecretRef:     envV1.SecretRef,
		Version:       2,
		Ciphertext:    []byte{1, 2, 3},
		DataNonce:     []byte{4, 5, 6},
		WrappedDEK:    []byte{7, 8, 9},
		WrapNonce:     []byte{10, 11, 12},
		EnvelopeSuite: envV1.EnvelopeSuite,
		KEKVersion:    envV1.KEKVersion,
	}
	if err := store.CreateEnvelope(ctx, envV2); err != nil {
		t.Fatalf("创建第二版凭证失败: %v", err)
	}

	conn.SecretVersion = 2
	if err := store.UpdateConnection(ctx, conn.WorkspaceID, conn); err != nil {
		t.Fatalf("切换现有连接到第二版失败: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRetiredAt(ctx, tx, envV1.WorkspaceID, envV1.SecretRef, 1); err != nil {
		_ = tx.Rollback()
		t.Fatalf("退役第一版失败: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交退役失败: %v", err)
	}

	newConn := *conn
	newConn.ID = uuid.Nil
	newConn.Name = "must-reject-retired-create"
	newConn.SecretVersion = 1
	if err := store.CreateConnection(ctx, &newConn); err == nil {
		t.Fatal("创建连接引用已退役凭证：error = nil，期望 fail-closed")
	} else if !errors.Is(err, ErrEnvelopeNotFound) {
		t.Fatalf("创建连接引用已退役凭证：返回意外错误 %v，期望 ErrEnvelopeNotFound", err)
	}

	conn.SecretVersion = 1
	if err := store.UpdateConnection(ctx, conn.WorkspaceID, conn); err == nil {
		t.Fatal("更新连接引用已退役凭证：error = nil，期望 fail-closed")
	} else if !errors.Is(err, ErrEnvelopeNotFound) {
		t.Fatalf("更新连接引用已退役凭证：返回意外错误 %v，期望 ErrEnvelopeNotFound", err)
	}
}

func TestCountConnectionsByVersionLocksMatchingReferences(t *testing.T) {
	db, store, _, _, _, envV1, _, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	envV2 := &CredentialEnvelope{
		WorkspaceID:   envV1.WorkspaceID,
		SecretRef:     envV1.SecretRef,
		Version:       2,
		Ciphertext:    []byte{1, 2, 3},
		DataNonce:     []byte{4, 5, 6},
		WrappedDEK:    []byte{7, 8, 9},
		WrapNonce:     []byte{10, 11, 12},
		EnvelopeSuite: envV1.EnvelopeSuite,
		KEKVersion:    envV1.KEKVersion,
	}
	if err := store.CreateEnvelope(ctx, envV2); err != nil {
		t.Fatalf("创建第二版凭证失败: %v", err)
	}

	retireTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer retireTx.Rollback()

	count, err := store.CountConnectionsByVersion(ctx, retireTx, envV1.WorkspaceID, envV1.SecretRef, 1)
	if err != nil {
		t.Fatalf("统计连接引用失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("连接引用数 = %d，期望 1", count)
	}

	updateDone := make(chan error, 1)
	go func() {
		tx, beginErr := db.BeginTx(ctx, nil)
		if beginErr != nil {
			updateDone <- beginErr
			return
		}
		if updateErr := store.UpdateConnectionVersion(ctx, tx, envV1.WorkspaceID, envV1.SecretRef, 2); updateErr != nil {
			_ = tx.Rollback()
			updateDone <- updateErr
			return
		}
		updateDone <- tx.Commit()
	}()

	select {
	case err := <-updateDone:
		t.Fatalf("并发连接版本更新未被 FOR SHARE 阻塞: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	if err := retireTx.Rollback(); err != nil {
		t.Fatalf("释放引用锁失败: %v", err)
	}
	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf("释放引用锁后更新失败: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("释放引用锁后连接版本更新仍未完成")
	}
}

// ---- connection_policies 约束测试 -----------------------------------------

func TestPolicy_Upsert_Succeeds(t *testing.T) {
	_, store, _, _, _, _, conn, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	p := &ConnectionPolicy{
		WorkspaceID: conn.WorkspaceID, ConnectionID: conn.ID,
		StatementTimeoutMs: 30000, MaxRows: 500,
	}
	if err := store.CreatePolicy(ctx, p); err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	// 重复创建应被拒绝
	p2 := &ConnectionPolicy{
		WorkspaceID: conn.WorkspaceID, ConnectionID: conn.ID,
		StatementTimeoutMs: 60000, MaxRows: 1000,
	}
	if err := store.CreatePolicy(ctx, p2); err == nil {
		t.Fatal("期望重复策略创建被拒绝")
	}
	// 更新已有策略应成功
	if err := store.UpdatePolicy(ctx, p2); err != nil {
		t.Fatalf("更新策略失败: %v", err)
	}
}

func TestPolicy_NonPositiveMaxRows_Rejected(t *testing.T) {
	_, store, _, _, _, _, conn, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	err := store.CreatePolicy(ctx, &ConnectionPolicy{
		WorkspaceID: conn.WorkspaceID, ConnectionID: conn.ID,
		StatementTimeoutMs: 30000, MaxRows: 0,
	})
	if err == nil {
		t.Fatal("期望非正 max_rows 被拒绝")
	}
}

func TestPolicy_NonPositiveTimeout_Rejected(t *testing.T) {
	_, store, _, _, _, _, conn, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	err := store.CreatePolicy(ctx, &ConnectionPolicy{
		WorkspaceID: conn.WorkspaceID, ConnectionID: conn.ID,
		StatementTimeoutMs: 0, MaxRows: 100,
	})
	if err == nil {
		t.Fatal("期望非正 statement_timeout_ms 被拒绝")
	}
}

func TestPolicy_MissingPolicy_ReturnsNil(t *testing.T) {
	_, store, _, ws, _, _, conn, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	p, err := store.PolicyByConnection(ctx, ws.ID, conn.ID)
	if err != nil {
		t.Fatalf("查询策略失败: %v", err)
	}
	if p != nil {
		t.Fatal("期望缺失策略返回 nil")
	}
}

// ---- executions 约束测试 -------------------------------------------------

func TestExecution_ResultRefWithAutoExpiry(t *testing.T) {
	_, store, _, _, _, _, conn, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	ref := "result-1"
	e := &Execution{
		WorkspaceID: conn.WorkspaceID, ConnectionID: conn.ID,
		ActorID: conn.CreatedBy, StatementHash: "sha256:abc",
		Status: ExecStatusPending, TraceID: "trace-001",
		ResultRef: &ref,
	}
	if err := store.CreateExecution(ctx, e); err != nil {
		t.Fatalf("CreateExecution 应自动设置默认过期: %v", err)
	}
	if e.ResultExpiresAt == nil {
		t.Fatal("期望 ResultExpiresAt 被自动设置为 7 天默认值")
	}
}

func TestExecution_ResultRefWithoutExpiry_RawSQL_Rejected(t *testing.T) {
	db, _, _, _, _, _, conn, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	// 绕过 Go 代码直接 INSERT — DB CHECK 约束应拒绝
	_, err := db.ExecContext(ctx, `
		INSERT INTO executions
			(workspace_id, connection_id, actor_id, statement_hash, status, trace_id, result_ref, result_expires_at)
		VALUES ($1, $2, $3, 'sha256:xyz', 'pending', 'trace-raw-001', 'result-raw', NULL)
	`, conn.WorkspaceID, conn.ID, conn.CreatedBy)
	if err == nil {
		t.Fatal("期望数据库 CHECK 约束拒绝 result_ref 非空但无过期时间")
	}
	t.Logf("DB CHECK 拒绝: %v", err)
}

func TestExecution_CrossWorkspaceActor_Rejected(t *testing.T) {
	db, _, _, _, _, _, conn, cleanup := setupFull(t)
	defer cleanup()
	store := NewPGStore(db)
	ctx := context.Background()

	u := &User{Email: "no-member@ex.com", PasswordHash: "hash"}
	store.CreateUser(ctx, u)

	e := &Execution{
		WorkspaceID: conn.WorkspaceID, ConnectionID: conn.ID,
		ActorID: u.ID, StatementHash: "sha256:abc",
		Status: ExecStatusPending, TraceID: "trace-002",
	}
	err := store.CreateExecution(ctx, e)
	if err == nil {
		t.Fatal("期望非成员 actor 被拒绝")
	}
}

// ---- audit_events 约束测试 ------------------------------------------------

func TestAudit_AppendAndQuery(t *testing.T) {
	_, store, _, ws, _, _, conn, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	event := &AuditEvent{
		WorkspaceID: ws.ID, ActorType: ActorTypeSystem,
		Action: "connection.test", ResourceType: "connection",
		ResourceID: conn.ID.String(), Outcome: OutcomeSucceeded,
		Metadata: json.RawMessage(`{"summary":"test"}`),
		TraceID:  "trace-audit-001", OccurredAt: time.Now().UTC(),
	}
	if err := store.AppendAudit(ctx, event); err != nil {
		t.Fatalf("追加审计失败: %v", err)
	}
	if event.ID == uuid.Nil {
		t.Fatal("期望 AppendAudit 返回非空 ID")
	}

	events, err := store.QueryAudit(ctx, AuditQuery{WorkspaceID: ws.ID, Limit: 10})
	if err != nil {
		t.Fatalf("查询审计失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("期望 1 条审计事件，实际 %d", len(events))
	}
}

func TestAudit_UserActorWithNullActorID_Rejected(t *testing.T) {
	_, store, _, ws, _, _, conn, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	err := store.AppendAudit(ctx, &AuditEvent{
		WorkspaceID: ws.ID, ActorType: ActorTypeUser, ActorID: nil,
		Action: "test", ResourceType: "test", ResourceID: conn.ID.String(),
		Outcome: OutcomeSucceeded, TraceID: "trace-actor-001", OccurredAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("期望 user actor 空 actor_id 被拒绝")
	}
}

func TestAudit_SystemActorWithNonNullActorID_Rejected(t *testing.T) {
	_, store, _, ws, _, _, conn, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	id := uuid.New()
	err := store.AppendAudit(ctx, &AuditEvent{
		WorkspaceID: ws.ID, ActorType: ActorTypeSystem, ActorID: &id,
		Action: "test", ResourceType: "test", ResourceID: conn.ID.String(),
		Outcome: OutcomeSucceeded, TraceID: "trace-actor-002", OccurredAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("期望 system actor 非空 actor_id 被拒绝")
	}
}

func TestAudit_EmptyAction_Rejected(t *testing.T) {
	_, store, _, ws, _, _, _, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	err := store.AppendAudit(ctx, &AuditEvent{
		WorkspaceID: ws.ID, ActorType: ActorTypeSystem,
		Action: "", ResourceType: "conn", ResourceID: "r1",
		Outcome: OutcomeSucceeded, TraceID: "trace-empty-001", OccurredAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("期望空 action 被拒绝")
	}
}

func TestAudit_WhitespaceTraceID_Rejected(t *testing.T) {
	_, store, _, ws, _, _, _, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	err := store.AppendAudit(ctx, &AuditEvent{
		WorkspaceID: ws.ID, ActorType: ActorTypeSystem,
		Action: "test", ResourceType: "conn", ResourceID: "r1",
		Outcome: OutcomeSucceeded, TraceID: "   ", OccurredAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("期望空白 trace_id 被拒绝")
	}
}

func TestAudit_MissingOccurredAt_Rejected(t *testing.T) {
	db, _, _, ws, _, _, _, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	// Go time.Time{} 零值映射为有效时间戳而非 SQL NULL；使用原始 SQL 测试 NOT NULL 约束
	_, err := db.ExecContext(ctx, `
		INSERT INTO audit_events
			(workspace_id, actor_type, action, resource_type, resource_id, outcome, trace_id, occurred_at)
		VALUES ($1, 'system', 'test', 'conn', 'r1', 'succeeded', 'trace-time-001', NULL)
	`, ws.ID)
	if err == nil {
		t.Fatal("期望 NULL occurred_at 被拒绝")
	}
	t.Logf("NULL occurred_at 拒绝: %v", err)
}

func TestAudit_NonObjectMetadata_Rejected(t *testing.T) {
	_, store, _, ws, _, _, _, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	err := store.AppendAudit(ctx, &AuditEvent{
		WorkspaceID: ws.ID, ActorType: ActorTypeSystem,
		Action: "test", ResourceType: "conn", ResourceID: "r1",
		Outcome: OutcomeSucceeded, Metadata: json.RawMessage(`"not_object"`),
		TraceID: "trace-meta-001", OccurredAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("期望非对象 metadata 被拒绝")
	}
}

func TestAudit_UpdateRejected(t *testing.T) {
	db, store, _, ws, _, _, conn, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	event := &AuditEvent{
		WorkspaceID: ws.ID, ActorType: ActorTypeSystem,
		Action: "test.update", ResourceType: "connection",
		ResourceID: conn.ID.String(), Outcome: OutcomeSucceeded,
		TraceID: "trace-update-001", OccurredAt: time.Now().UTC(),
	}
	store.AppendAudit(ctx, event)

	_, err := db.ExecContext(ctx, `UPDATE audit_events SET action = 'modified' WHERE id = $1`, event.ID)
	if err == nil {
		t.Fatal("期望 audit_events UPDATE 被拒绝")
	}
	t.Logf("UPDATE 拒绝: %v", err)
}

func TestAudit_DeleteRejected(t *testing.T) {
	db, store, _, ws, _, _, conn, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	event := &AuditEvent{
		WorkspaceID: ws.ID, ActorType: ActorTypeSystem,
		Action: "test.delete", ResourceType: "connection",
		ResourceID: conn.ID.String(), Outcome: OutcomeSucceeded,
		TraceID: "trace-delete-001", OccurredAt: time.Now().UTC(),
	}
	store.AppendAudit(ctx, event)

	_, err := db.ExecContext(ctx, `DELETE FROM audit_events WHERE id = $1`, event.ID)
	if err == nil {
		t.Fatal("期望 audit_events DELETE 被拒绝")
	}
	t.Logf("DELETE 拒绝: %v", err)
}

func TestAudit_TruncateRejected(t *testing.T) {
	db, _, _, _, _, _, _, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `TRUNCATE TABLE audit_events`)
	if err == nil {
		t.Fatal("期望 audit_events TRUNCATE 被拒绝")
	}
	t.Logf("TRUNCATE 拒绝: %v", err)
}

func TestAudit_OrphanWorkspace_Rejected(t *testing.T) {
	_, store, _, _, _, _, _, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	err := store.AppendAudit(ctx, &AuditEvent{
		WorkspaceID: uuid.New(), ActorType: ActorTypeSystem,
		Action: "test", ResourceType: "conn", ResourceID: "r1",
		Outcome: OutcomeSucceeded, TraceID: "trace-orphan-001", OccurredAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("期望孤儿 workspace audit 被拒绝")
	}
}

func TestAudit_ExecutionWithoutConnection_Rejected(t *testing.T) {
	_, store, _, ws, _, _, _, cleanup := setupFull(t)
	defer cleanup()
	ctx := context.Background()

	eid := uuid.New()
	err := store.AppendAudit(ctx, &AuditEvent{
		WorkspaceID: ws.ID, ActorType: ActorTypeSystem,
		Action: "test", ResourceType: "conn", ResourceID: "r1",
		Outcome: OutcomeSucceeded, TraceID: "trace-exec-noconn-001",
		ExecutionID: &eid, ConnectionID: nil,
		OccurredAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("期望 execution 非空但 connection 为空被拒绝")
	}
}

// ---- 无明文凭证测试 ---------------------------------------------------------

func TestNoPlaintextCredentialsInSchema(t *testing.T) {
	db, cleanup := setupDB(t)
	defer cleanup()
	ctx := context.Background()

	rows, err := db.QueryContext(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'connections'
	`)
	if err != nil {
		t.Fatalf("查询 information_schema 失败: %v", err)
	}
	defer rows.Close()

	disallowed := map[string]bool{
		"password": true, "secret": true, "secret_key": true,
		"credential": true, "credential_data": true,
		"connection_string": true, "dsn": true,
	}
	for rows.Next() {
		var col string
		rows.Scan(&col)
		if disallowed[col] {
			t.Errorf("connections 表中不应存在明文凭证列: %s", col)
		}
	}
}

func TestUser_PasswordHashNotInJSON(t *testing.T) {
	u := &User{ID: uuid.New(), Email: "t@t.com", PasswordHash: "secret"}
	data, _ := json.Marshal(u)
	var m map[string]any
	json.Unmarshal(data, &m)
	if _, ok := m["password_hash"]; ok {
		t.Fatal("password_hash 不应出现在 JSON 序列化中")
	}
}

//go:build integration

package adapter

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 只读数据库边界集成测试（P0-06A §8.3/§14 R5，WEB-35 交付）。
// 依赖 demo PostgreSQL/MySQL（deploy/compose/init 预置，demo_reader 仅 SELECT）。
// 验证：
//  1. 只读事务/会话建立成功且查询正常
//  2. SELECT 副作用（nextval）在只读事务中被数据库拒绝（只读边界生效）
//  3. 连续查询连接复用不残留事务状态
//  4. 只读设置失败（已有事务）时 fail-closed

// TestReadOnlyBoundary_PG_Success 验证 PG 只读事务建立成功且查询正常。
func TestReadOnlyBoundary_PG_Success(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	defer h.Release()

	res := queryMustSucceed(t, h, FirstPageRequest{
		Scope:    UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:      "SELECT id, first_name FROM employees ORDER BY id LIMIT 3",
		PageSize: 100,
		MaxRows:  100,
	})
	if res.ReturnedRows == 0 {
		t.Fatal("预期返回行")
	}
}

// TestReadOnlyBoundary_PG_SideEffectRejected 验证只读事务拒绝 SELECT 副作用
// （nextval 写 sequence；PG read-only 事务拒绝）。证明只读边界生效。
func TestReadOnlyBoundary_PG_SideEffectRejected(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	defer h.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := h.entry.pgPool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	if err := beginReadOnlyPG(ctx, conn); err != nil {
		t.Fatalf("beginReadOnlyPG: %v", err)
	}
	if _, err := conn.Exec(ctx, "SELECT nextval('employees_id_seq')"); err == nil {
		t.Fatal("只读事务应拒绝 nextval 写副作用")
	}
	if err := endReadOnlyPG(ctx, conn); err != nil {
		t.Fatalf("endReadOnlyPG: %v", err)
	}
}

// TestReadOnlyBoundary_PG_ExistingTransactionRejected 验证连接已有事务时
// beginReadOnlyPG fail-closed（不覆盖/回滚未知事务），且错误折叠为
// connection_unavailable（ErrConnectionFailed → 公共 503，D15a）。
func TestReadOnlyBoundary_PG_ExistingTransactionRejected(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	defer h.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := h.entry.pgPool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "BEGIN"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer conn.Exec(ctx, "ROLLBACK")
	err = beginReadOnlyPG(ctx, conn)
	if err == nil {
		t.Fatal("连接已有事务时 beginReadOnlyPG 必须 fail-closed")
	}
	// 真实失败路径（已有事务）必须折叠为 ErrConnectionFailed，而非返回驱动原始错误。
	assertFoldConnectionFailed(t, err)
}

// TestReadOnlyBoundary_PG_NoResidualTransactionAfterQuery 验证成功查询后连接无残余
// 事务：显式 ROLLBACK 且 rolledBack 标志使 defer 兜底不再重复 ROLLBACK（CodeRabbit #7
// 的 PG 语义），池复用连接 TxStatus 必须回到 idle 'I'（CodeRabbit 新 #3 PG 覆盖）。
func TestReadOnlyBoundary_PG_NoResidualTransactionAfterQuery(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	defer h.Release()

	req := FirstPageRequest{
		Scope:    UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:      "SELECT id FROM employees ORDER BY id LIMIT 2",
		PageSize: 100,
		MaxRows:  100,
	}
	queryMustSucceed(t, h, req)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := h.entry.pgPool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	if status := conn.Conn().PgConn().TxStatus(); status != 'I' {
		t.Fatalf("查询后连接 TxStatus = %c, want 'I'（无残余事务，显式回滚且不重复 ROLLBACK）", status)
	}
}

// TestReadOnlyBoundary_PG_ConnectionReuse 验证连续查询连接复用无事务残留
// （第二次查询不受第一次只读事务影响）。
func TestReadOnlyBoundary_PG_ConnectionReuse(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	defer h.Release()

	req := FirstPageRequest{
		Scope:    UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:      "SELECT id FROM employees ORDER BY id LIMIT 2",
		PageSize: 100,
		MaxRows:  100,
	}
	res1 := queryMustSucceed(t, h, req)
	res2 := queryMustSucceed(t, h, req)
	if res1.TotalReturned == 0 || res2.TotalReturned == 0 {
		t.Fatal("连续查询应都成功")
	}
}

// TestReadOnlyBoundary_MySQL_Success 验证 MySQL 只读事务建立成功且查询正常。
func TestReadOnlyBoundary_MySQL_Success(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, myCfg())
	defer h.Release()

	res := queryMustSucceed(t, h, FirstPageRequest{
		Scope:    UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:      "SELECT id, first_name FROM employees ORDER BY id LIMIT 3",
		PageSize: 100,
		MaxRows:  100,
	})
	if res.ReturnedRows == 0 {
		t.Fatal("预期返回行")
	}
}

// TestReadOnlyBoundary_MySQL_NonDefaultSQLModeRejectedBeforeQuery 在真实 MySQL
// session 上设置两种会改变词法边界的 mode，再把该连接归还池。下一次执行必须在
// 同一池化连接上重新读取 mode，并在危险 ECM SQL 到达目标 QueryContext 前拒绝。
func TestReadOnlyBoundary_MySQL_NonDefaultSQLModeRejectedBeforeQuery(t *testing.T) {
	for _, mode := range []string{"NO_BACKSLASH_ESCAPES", "ANSI_QUOTES"} {
		t.Run(mode, func(t *testing.T) {
			m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
			defer m.Close(context.Background())
			cfg := myCfg()
			cfg.ConnectionID = "mysql-mode-" + mode
			cfg.MaxOpen = 1
			cfg.MaxIdle = 1
			h := mustGet(t, m, cfg)
			defer h.Release()

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			conn, err := h.entry.sqlDB.Conn(ctx)
			if err != nil {
				t.Fatalf("conn: %v", err)
			}
			if _, err := conn.ExecContext(ctx, "SET SESSION sql_mode = ?", mode); err != nil {
				conn.Close()
				t.Fatalf("set session sql_mode: %v", err)
			}
			// MaxOpen=1/MaxIdle=1 保证下一次 h.Query 复用刚设置 mode 的 session。
			if err := conn.Close(); err != nil {
				t.Fatalf("return conn to pool: %v", err)
			}

			_, err = h.Query(ctx, FirstPageRequest{
				Scope:    UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
				SQL:      "SELECT 'test\\' /*!50000' FROM t",
				PageSize: 1,
				MaxRows:  1,
			})
			var ae *AdapterError
			if !errors.As(err, &ae) || ae.Code != ErrConnectionFailed {
				t.Fatalf("err = %v, want AdapterError{Code: connection_failed}", err)
			}
			if ae.Message != "mysql session sql_mode unsupported" {
				t.Fatalf("message = %q, want mode gate rejection before target query", ae.Message)
			}
		})
	}
}

// TestReadOnlyBoundary_MySQL_ManualTransactionRejected 验证 MySQL 连接为手动
// 事务模式（autocommit=0，可能有未提交事务）时 beginReadOnlyMySQL fail-closed，
// 且错误折叠为 connection_unavailable。
func TestReadOnlyBoundary_MySQL_ManualTransactionRejected(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, myCfg())
	defer h.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := h.entry.sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET autocommit=0"); err != nil {
		t.Fatalf("set autocommit: %v", err)
	}
	defer conn.ExecContext(ctx, "SET autocommit=1")
	err = beginReadOnlyMySQL(ctx, conn)
	if err == nil {
		t.Fatal("autocommit=0 时 beginReadOnlyMySQL 必须 fail-closed")
	}
	// 真实失败路径（手动事务模式）必须折叠为 ErrConnectionFailed，而非驱动原始错误。
	assertFoldConnectionFailed(t, err)
}

// assertFoldConnectionFailed 断言只读边界失败路径折叠为 ErrConnectionFailed
// （D15a：公共层折叠为 connection_unavailable，不泄露内部原因）。
func assertFoldConnectionFailed(t *testing.T, err error) {
	t.Helper()
	var ae *AdapterError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v (%T)，必须为 *AdapterError（只读失败折叠）", err, err)
	}
	if ae.Code != ErrConnectionFailed {
		t.Fatalf("err.Code = %s, want connection_failed（只读失败折叠，不返回驱动原始错误）", ae.Code)
	}
}

// TestReadOnlyBoundary_MySQL_ConnectionReuse 验证 MySQL 连续查询连接复用无残留。
func TestReadOnlyBoundary_MySQL_ConnectionReuse(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, myCfg())
	defer h.Release()

	req := FirstPageRequest{
		Scope:    UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:      "SELECT id FROM employees ORDER BY id LIMIT 2",
		PageSize: 100,
		MaxRows:  100,
	}
	res1 := queryMustSucceed(t, h, req)
	res2 := queryMustSucceed(t, h, req)
	if res1.TotalReturned == 0 || res2.TotalReturned == 0 {
		t.Fatal("连续查询应都成功")
	}
}

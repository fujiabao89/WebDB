package adapter

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
)

// ---- 可控 fake MySQL driver（CodeRabbit #7 故障注入）----

// fakeMySQLDriver 为 execMySQL 只读回滚故障注入实现可控 driver：不连接真实 MySQL，
// 按语句内容返回 canned 响应，ROLLBACK 可注入失败。测试真实 execMySQL 代码路径
// （beginReadOnlyMySQL → QueryContext → 显式回滚 → fail-closed），非手工构造 AdapterError 自证。
type fakeMySQLDriver struct {
	rollbackErr        error
	autocommitErr      error
	autocommitValue    int64 // 默认 1（autocommit on）
	inTransactionValue int64 // 默认 0（无活动事务；Codex P1：显式 START TRANSACTION 后为 1）
	queryErr           error // 目标只读查询注入错误（取消/超时等）
	conns              []*fakeMySQLDriverConn
}

func (d *fakeMySQLDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use Connector")
}

func (d *fakeMySQLDriver) Connect(context.Context) (driver.Conn, error) {
	c := &fakeMySQLDriverConn{d: d}
	d.conns = append(d.conns, c)
	return c, nil
}

func (d *fakeMySQLDriver) Driver() driver.Driver { return d }

type fakeMySQLDriverConn struct {
	d      *fakeMySQLDriver
	closed bool
}

func (c *fakeMySQLDriverConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("Prepare not used; QueryerContext/ExecerContext provided")
}

func (c *fakeMySQLDriverConn) Close() error { c.closed = true; return nil }
func (c *fakeMySQLDriverConn) Begin() (driver.Tx, error) {
	return nil, errors.New("autocommit mode; Begin unsupported")
}

func (c *fakeMySQLDriverConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "@@autocommit") {
		if c.d.autocommitErr != nil {
			return nil, c.d.autocommitErr
		}
		// beginReadOnlyMySQL 现查询 @@autocommit + @@session.in_transaction（Codex P1）。
		return &fakeMySQLRows{cols: []string{"@@autocommit", "@@session.in_transaction"},
			rows: [][]driver.Value{{c.d.autocommitValue, c.d.inTransactionValue}}}, nil
	}
	// 目标只读查询：可注入错误（取消/超时），否则返回单行 id=1。
	if c.d.queryErr != nil {
		return nil, c.d.queryErr
	}
	return &fakeMySQLRows{cols: []string{"id"}, rows: [][]driver.Value{{int64(1)}}}, nil
}

func (c *fakeMySQLDriverConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "START TRANSACTION READ ONLY") {
		return driver.RowsAffected(0), nil
	}
	if strings.Contains(query, "ROLLBACK") {
		if c.d.rollbackErr != nil {
			return nil, c.d.rollbackErr
		}
		return driver.RowsAffected(0), nil
	}
	return driver.RowsAffected(0), nil
}

type fakeMySQLRows struct {
	cols []string
	rows [][]driver.Value
	pos  int
}

func (r *fakeMySQLRows) Columns() []string { return r.cols }
func (r *fakeMySQLRows) Close() error      { return nil }
func (r *fakeMySQLRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.pos])
	r.pos++
	return nil
}

func fakeMySQLHandle(fd *fakeMySQLDriver) *PoolHandle {
	db := sql.OpenDB(fd)
	return &PoolHandle{entry: &poolEntry{cfg: ConnectConfig{Engine: EngineMySQL}, sqlDB: db}, gen: 1}
}

// TestExecMySQLRollbackFailureFailsClosed 验证数据已全部读入后只读事务回滚失败：
// 必须 fail-closed 为 connection_unavailable（ErrConnectionFailed 折叠 503）、
// 不返回已读入的成功结果、且连接被销毁不归还池（CodeRabbit #7 公共语义）。
func TestExecMySQLRollbackFailureFailsClosed(t *testing.T) {
	fd := &fakeMySQLDriver{rollbackErr: errors.New("injected rollback failure"), autocommitValue: 1}
	handle := fakeMySQLHandle(fd)
	_, err := handle.execQuery(context.Background(), "SELECT id FROM t", nil, 2, 1, 0, 100)
	var ae *AdapterError
	if !errors.As(err, &ae) || ae.Code != ErrConnectionFailed {
		t.Fatalf("err = %v, want AdapterError{Code: connection_failed}（只读回滚失败不得返回成功结果）", err)
	}
	if len(fd.conns) == 0 {
		t.Fatal("fake driver 未建立连接")
	}
	if !fd.conns[0].closed {
		t.Fatal("rollback 失败后连接必须被销毁（discardConn），不得归还池复用")
	}
}

// TestExecMySQLRollbackSuccessReturnsResult 验证回滚成功时返回已读入的结果（对照）。
func TestExecMySQLRollbackSuccessReturnsResult(t *testing.T) {
	fd := &fakeMySQLDriver{autocommitValue: 1}
	handle := fakeMySQLHandle(fd)
	res, err := handle.execQuery(context.Background(), "SELECT id FROM t", nil, 2, 1, 0, 100)
	if err != nil {
		t.Fatalf("回滚成功应返回结果: %v", err)
	}
	if res.ReturnedRows != 1 {
		t.Fatalf("ReturnedRows = %d, want 1", res.ReturnedRows)
	}
}

// TestExecMySQLBeginFailureDestroysConn 验证只读事务建立失败（begin 失败）时连接被
// 销毁（Owner P1-1：只读设置失败连接状态未知 → 不归还池），且错误折叠为
// connection_unavailable。
func TestExecMySQLBeginFailureDestroysConn(t *testing.T) {
	fd := &beginFailMySQLDriver{beginErr: errors.New("injected begin failure")}
	db := sql.OpenDB(fd)
	handle := &PoolHandle{entry: &poolEntry{cfg: ConnectConfig{Engine: EngineMySQL}, sqlDB: db}, gen: 1}
	_, err := handle.execQuery(context.Background(), "SELECT id FROM t", nil, 2, 1, 0, 100)
	var ae *AdapterError
	if !errors.As(err, &ae) || ae.Code != ErrConnectionFailed {
		t.Fatalf("err = %v, want AdapterError{Code: connection_failed}", err)
	}
	if len(fd.conns) == 0 {
		t.Fatal("fake driver 未建立连接")
	}
	if !fd.conns[0].closed {
		t.Fatal("begin 失败后连接必须被销毁")
	}
}

type beginFailMySQLDriver struct {
	beginErr error
	conns    []*beginFailMySQLConn
}

func (d *beginFailMySQLDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use Connector")
}
func (d *beginFailMySQLDriver) Connect(context.Context) (driver.Conn, error) {
	c := &beginFailMySQLConn{d: d}
	d.conns = append(d.conns, c)
	return c, nil
}
func (d *beginFailMySQLDriver) Driver() driver.Driver { return d }

type beginFailMySQLConn struct {
	d      *beginFailMySQLDriver
	closed bool
}

func (c *beginFailMySQLConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("n/a") }
func (c *beginFailMySQLConn) Close() error                        { c.closed = true; return nil }
func (c *beginFailMySQLConn) Begin() (driver.Tx, error)           { return nil, errors.New("n/a") }
func (c *beginFailMySQLConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "@@autocommit") {
		return &fakeMySQLRows{cols: []string{"@@autocommit"}, rows: [][]driver.Value{{int64(1)}}}, nil
	}
	return &fakeMySQLRows{cols: []string{"id"}, rows: [][]driver.Value{{int64(1)}}}, nil
}
func (c *beginFailMySQLConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "START TRANSACTION READ ONLY") {
		return nil, c.d.beginErr
	}
	return driver.RowsAffected(0), nil
}

// TestBeginReadOnlyMySQLAutocommitQueryFailureFoldsConnectionFailed 验证
// SELECT @@autocommit 查询失败时 beginReadOnlyMySQL 折叠为 ErrConnectionFailed
// 且不泄露驱动原始错误（#8：真实失败路径，非手工构造 AdapterError 自证）。
func TestBeginReadOnlyMySQLAutocommitQueryFailureFoldsConnectionFailed(t *testing.T) {
	fd := &fakeMySQLDriver{autocommitErr: errors.New("raw driver autocommit error: secret detail")}
	db := sql.OpenDB(fd)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()
	err = beginReadOnlyMySQL(context.Background(), conn)
	var ae *AdapterError
	if !errors.As(err, &ae) || ae.Code != ErrConnectionFailed {
		t.Fatalf("err = %v, want AdapterError{Code: connection_failed}（只读检查失败折叠）", err)
	}
	if strings.Contains(ae.Message, "raw driver autocommit error") {
		t.Fatalf("折叠 message 不得泄露驱动原始错误: %q", ae.Message)
	}
}

// TestBeginReadOnlyMySQLManualTransactionModeFoldsConnectionFailed 验证 autocommit=0
// （手动事务模式）时 beginReadOnlyMySQL 折叠为 ErrConnectionFailed（真实失败路径）。
func TestBeginReadOnlyMySQLManualTransactionModeFoldsConnectionFailed(t *testing.T) {
	fd := &fakeMySQLDriver{autocommitValue: 0}
	db := sql.OpenDB(fd)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()
	err = beginReadOnlyMySQL(context.Background(), conn)
	var ae *AdapterError
	if !errors.As(err, &ae) || ae.Code != ErrConnectionFailed {
		t.Fatalf("err = %v, want AdapterError{Code: connection_failed}", err)
	}
}

// TestWrapReadOnlyMessageSanitized 验证 wrapReadOnly 使用固定 message（不含驱动原始
// 错误），错误链保留 cause 供 errors.Is/As 判定（只读失败不泄露内部原因，#8）。
func TestWrapReadOnlyMessageSanitized(t *testing.T) {
	err := wrapReadOnly("begin read-only transaction", errors.New("pq: secret connection detail"))
	var ae *AdapterError
	if !errors.As(err, &ae) || ae.Code != ErrConnectionFailed {
		t.Fatalf("err = %v, want AdapterError{Code: connection_failed}", err)
	}
	if ae.Message != "read-only begin read-only transaction failed" {
		t.Fatalf("message = %q, want 固定脱敏 message（不得泄露驱动原始错误）", ae.Message)
	}
}

// TestExecMySQLCancelThenReuse 验证 MySQL 查询取消/超时后：只读事务被回滚、
// 连接未被销毁（回滚成功），后续请求复用同一连接时无事务残留（#6：
// 取消/超时后事务状态与连接复用）。
func TestExecMySQLCancelThenReuse(t *testing.T) {
	fd := &fakeMySQLDriver{autocommitValue: 1, queryErr: context.Canceled}
	handle := fakeMySQLHandle(fd)

	// 第一次查询：取消 → 返回 query_cancelled（mapExecError），defer 回滚（成功→不销毁）。
	_, err := handle.execQuery(context.Background(), "SELECT id FROM t", nil, 2, 1, 0, 100)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消查询应返回 context.Canceled，got %v", err)
	}
	// 第二次查询：同一 handle 复用连接，应成功且无事务残留（@@autocommit 仍为 1）。
	fd.queryErr = nil
	res, err := handle.execQuery(context.Background(), "SELECT id FROM t", nil, 2, 1, 0, 100)
	if err != nil {
		t.Fatalf("取消后连接复用应成功（无事务残留）: %v", err)
	}
	if res.ReturnedRows != 1 {
		t.Fatalf("ReturnedRows = %d, want 1", res.ReturnedRows)
	}
	// 复用验证：fake 驱动只建立一个连接（第一次未被销毁）。
	if len(fd.conns) != 1 {
		t.Fatalf("conns = %d, want 1（取消路径回滚成功不应销毁连接，应复用同一连接）", len(fd.conns))
	}
	if fd.conns[0].closed {
		t.Fatal("取消路径（回滚成功）不应销毁连接")
	}
}

// TestExecMySQLActiveTransactionRejected 验证显式 START TRANSACTION 后（autocommit 仍 1
// 但 @@session.in_transaction=1）beginReadOnlyMySQL 拒绝（Codex P1：避免 START TRANSACTION
// READ ONLY 隐式提交活动可写事务，CT-22 fail-closed）。
func TestExecMySQLActiveTransactionRejected(t *testing.T) {
	fd := &fakeMySQLDriver{autocommitValue: 1, inTransactionValue: 1}
	handle := fakeMySQLHandle(fd)
	_, err := handle.execQuery(context.Background(), "SELECT id FROM t", nil, 2, 1, 0, 100)
	var ae *AdapterError
	if !errors.As(err, &ae) || ae.Code != ErrConnectionFailed {
		t.Fatalf("活动事务下应 fail-closed 为 connection_failed，实际 err=%v", err)
	}
}

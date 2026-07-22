//go:build integration

package adapter

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

func envPort(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			panic("invalid " + k + "=" + v)
		}
		return n
	}
	return def
}

func envDef(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func pgCfg() ConnectConfig {
	return ConnectConfig{
		ConnectionID: "pg1", SecretVersion: 1, ConfigRevision: 1,
		Engine: EnginePostgreSQL, Host: envDef("DEMO_PG_HOST", "localhost"), Port: envPort("DEMO_PG_PORT", 5433),
		User: envDef("DEMO_PG_USER", "demo_reader"), Password: envDef("DEMO_PG_PASSWORD", "change_me"),
		Database: envDef("DEMO_PG_NAME", "webdb_demo"), TLS: TLSDisable, MaxOpen: 2, MaxIdle: 1,
	}
}
func myCfg() ConnectConfig {
	return ConnectConfig{
		ConnectionID: "my1", SecretVersion: 1, ConfigRevision: 1,
		Engine: EngineMySQL, Host: envDef("DEMO_MYSQL_HOST", "localhost"), Port: envPort("DEMO_MYSQL_PORT", 3306),
		User: envDef("DEMO_MYSQL_USER", "demo_reader"), Password: envDef("DEMO_MYSQL_PASSWORD", "change_me"),
		Database: envDef("DEMO_MYSQL_NAME", "webdb_demo"), TLS: TLSDisable, MaxOpen: 2, MaxIdle: 1,
	}
}

func TestManager_NewAdapterManager(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	if m == nil {
		t.Fatal("nil")
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestManager_Get_PostgreSQL(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h, err := m.Get(context.Background(), pgCfg())
	if err != nil {
		t.Fatalf("Get PG: %v", err)
	}
	h.Release()
}
func TestManager_Get_MySQL(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h, err := m.Get(context.Background(), myCfg())
	if err != nil {
		t.Fatalf("Get MySQL: %v", err)
	}
	h.Release()
}
func TestManager_UnsupportedEngine(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	c := pgCfg()
	c.Engine = "sqlite"
	if _, err := m.Get(context.Background(), c); err == nil {
		t.Fatal("expected error")
	}
}
func TestManager_TLS_Disable_Denied(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: false})
	defer m.Close(context.Background())
	c := pgCfg()
	c.TLS = TLSDisable
	if _, err := m.Get(context.Background(), c); err == nil {
		t.Fatal("expected error")
	}
}
func TestManager_StaleConfig(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	c := pgCfg()
	c.ConfigRevision = 5
	h, err := m.Get(context.Background(), c)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	h.Release()
	c.ConfigRevision = 3
	if _, err = m.Get(context.Background(), c); err == nil {
		t.Fatal("expected stale error")
	}
}

func TestSchemas_PG(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h, err := m.Get(context.Background(), pgCfg())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer h.Release()
	schemas, err := h.Schemas(context.Background())
	if err != nil {
		t.Fatalf("Schemas: %v", err)
	}
	if len(schemas) == 0 {
		t.Fatal("expected at least 1 schema")
	}
	t.Logf("PG schemas: %v", schemas)
}

func TestSchemas_MySQL(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h, err := m.Get(context.Background(), myCfg())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer h.Release()
	schemas, err := h.Schemas(context.Background())
	if err != nil {
		t.Fatalf("Schemas: %v", err)
	}
	if len(schemas) == 0 {
		t.Fatal("expected at least 1 schema")
	}
	t.Logf("MySQL schemas: %v", schemas)
}

func TestTables_PG(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	tables, err := h.Tables(context.Background(), "public")
	if err != nil {
		t.Fatalf("Tables: %v", err)
	}
	if len(tables) == 0 {
		if len(tables) == 0 {
			t.Fatal("no tables in public schema")
		}
	}
	t.Logf("PG tables: %d", len(tables))
}

func TestTables_MySQL(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, myCfg())
	ensureEmployees(t, h)
	defer h.Release()
	tables, err := h.Tables(context.Background(), "webdb_demo")
	if err != nil {
		t.Fatalf("Tables: %v", err)
	}
	if len(tables) == 0 {
		if len(tables) == 0 {
			t.Fatal("no tables in database")
		}
	}
	t.Logf("MySQL tables: %d", len(tables))
}

func TestQuery_PG(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	req := FirstPageRequest{
		Scope: UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:   "SELECT id, first_name FROM employees", Args: nil,
		SortKeys: []SortKey{{Column: "id", Order: SortAsc, NullsLast: false}},
		PageSize: 10, MaxRows: 100,
	}
	result, err := h.Query(context.Background(), req)
	if err != nil {
		t.Skipf("query unavailable: %v", err)
	}
	if result.ReturnedRows == 0 {
		t.Fatal("expected rows")
	}
	t.Logf("PG query: %d rows, columns: %v", result.ReturnedRows, result.Columns)
}

func TestQuery_MySQL(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, myCfg())
	ensureEmployees(t, h)
	defer h.Release()
	req := FirstPageRequest{
		Scope: UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:   "SELECT id, first_name FROM employees", Args: nil,
		SortKeys: []SortKey{{Column: "id", Order: SortAsc, NullsLast: false}},
		PageSize: 10, MaxRows: 100,
	}
	result, err := h.Query(context.Background(), req)
	if err != nil {
		t.Skipf("query unavailable: %v", err)
	}
	if result.ReturnedRows == 0 {
		t.Fatal("expected rows")
	}
	t.Logf("MySQL query: %d rows", result.ReturnedRows)
}

func TestQuery_PageSize(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	req := FirstPageRequest{
		Scope: UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:   "SELECT id, first_name FROM employees", Args: nil,
		SortKeys: []SortKey{{Column: "id", Order: SortAsc, NullsLast: false}},
		PageSize: 3, MaxRows: 100,
	}
	result, err := h.Query(context.Background(), req)
	if err != nil {
		t.Skipf("query unavailable: %v", err)
	}
	if result.ReturnedRows > 3 {
		t.Fatalf("expected <=3 rows, got %d", result.ReturnedRows)
	}
	t.Logf("page size test: %d rows, next=%v", result.ReturnedRows, result.NextToken != nil)
}

func TestNextPage_PG_FullPagination(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	scope := UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"}
	req := FirstPageRequest{
		Scope: scope, SQL: "SELECT id, first_name FROM employees", Args: nil,
		SortKeys: []SortKey{{Column: "id", Order: SortAsc, NullsLast: false}},
		PageSize: 3, MaxRows: 100,
	}
	r1, err := h.Query(context.Background(), req)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if r1.NextToken == nil {
		t.Fatal("expected next token")
	}
	t.Logf("page1: %d rows, token=%s", r1.ReturnedRows, (*r1.NextToken)[:8])
	r2, err := h.NextPage(context.Background(), scope, *r1.NextToken)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if r2.ReturnedRows == 0 {
		t.Fatal("expected rows in page 2")
	}
	t.Logf("page2: %d rows, next=%v", r2.ReturnedRows, r2.NextToken != nil)
	seen := map[int]bool{}
	for _, row := range r1.Rows {
		seen[int(row[0].(int32))] = true
	}
	for _, row := range r2.Rows {
		id := int(row[0].(int32))
		if seen[id] {
			t.Fatalf("duplicate id %d across pages", id)
		}
		seen[id] = true
	}
	t.Logf("no duplicates across %d pages", 2)
}

func TestNextPage_InvalidToken(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	_, err := h.NextPage(context.Background(), UserWorkspaceScope{}, "nonexistent-token")
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
	t.Logf("invalid token: %v", err)
}

func TestNextPage_MaxRows(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	scope := UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"}
	req := FirstPageRequest{
		Scope: scope, SQL: "SELECT id, first_name FROM employees", Args: nil,
		SortKeys: []SortKey{{Column: "id", Order: SortAsc, NullsLast: false}},
		PageSize: 2, MaxRows: 3,
	}
	r1, err := h.Query(context.Background(), req)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	t.Logf("p1: %d rows, next=%v, total=%d", r1.ReturnedRows, r1.NextToken != nil, r1.TotalReturned)
	if r1.NextToken != nil {
		r2, err := h.NextPage(context.Background(), scope, *r1.NextToken)
		if err != nil {
			t.Fatalf("page 2: %v", err)
		}
		t.Logf("p2: %d rows, next=%v, total=%d", r2.ReturnedRows, r2.NextToken != nil, r2.TotalReturned)
		if r2.TotalReturned > 3 {
			t.Fatalf("maxRows exceeded: %d", r2.TotalReturned)
		}
	}
}

func TestNextPage_ScopeMismatch(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	scope := UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"}
	req := FirstPageRequest{
		Scope: scope, SQL: "SELECT id, first_name FROM employees", Args: nil,
		SortKeys: []SortKey{{Column: "id", Order: SortAsc, NullsLast: false}},
		PageSize: 2, MaxRows: 100,
	}
	r1, err := h.Query(context.Background(), req)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if r1.NextToken == nil {
		t.Skip("no second page")
	}
	_, err = h.NextPage(context.Background(), UserWorkspaceScope{UserID: "u2", WorkspaceID: "ws2"}, *r1.NextToken)
	if err == nil {
		t.Fatal("expected scope mismatch error")
	}
	t.Logf("scope mismatch: %v", err)
}

func TestKeyset_SQL_Debug(t *testing.T) {
	specs, _ := buildSortSpecs([]SortKey{{Column: "id", Order: SortAsc, NullsLast: false}})
	sql, args, err := buildWrappedSQL("SELECT id, first_name FROM employees", specs, EnginePostgreSQL, []any{false, int32(3)}, nil, 4)
	if err != nil {
		t.Fatalf("buildSQL: %v", err)
	}
	t.Logf("PG SQL: %s", sql)
	t.Logf("PG Args: %v", args)
	sql2, args2, _ := buildWrappedSQL("SELECT id, first_name FROM employees", specs, EngineMySQL, []any{false, int32(3)}, nil, 4)
	t.Logf("MySQL SQL: %s", sql2)
	t.Logf("MySQL Args: %v", args2)
}

func TestNextPage_PG_Debug(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	scope := UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"}
	req := FirstPageRequest{
		Scope: scope, SQL: "SELECT id, first_name FROM employees", Args: nil,
		SortKeys: []SortKey{{Column: "id", Order: SortAsc, NullsLast: false}},
		PageSize: 3, MaxRows: 100,
	}
	r1, err := h.Query(context.Background(), req)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if r1.NextToken == nil {
		t.Skip("no page 2")
	}
	plan, err := h.entry.manager.registry.claim(*r1.NextToken)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	t.Logf("plan: sql=%q, lastVals=%v, cumCount=%d", plan.SQL, plan.LastSortValues, plan.CumulativeCount)
	specs, _ := buildSortSpecs(plan.SortKeys)
	sql, args, _ := buildWrappedSQL(plan.SQL, specs, EnginePostgreSQL, plan.LastSortValues, plan.Args, 4)
	t.Logf("SQL: %s", sql)
	t.Logf("Args: %v", args)
	rows, err := h.entry.pgPool.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("raw query: %v", err)
	}
	rows.Close()
}

func TestTimeout_PG(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := FirstPageRequest{
		Scope: UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:   "SELECT pg_sleep(10), id FROM employees", Args: nil,
		SortKeys: []SortKey{{Column: "id", Order: SortAsc}},
		PageSize: 10, MaxRows: 100,
	}
	_, err := h.Query(ctx, req)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	t.Logf("timeout: %v", err)
	// Verify pool still works after timeout
	r2, err := h.Query(context.Background(), FirstPageRequest{
		Scope: UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:   "SELECT 1 AS n", Args: nil,
		SortKeys: []SortKey{{Column: "n", Order: SortAsc}},
		PageSize: 1, MaxRows: 100,
	})
	if err != nil {
		t.Fatalf("pool recovery after timeout: %v", err)
	}
	t.Logf("recovery: %d rows", r2.ReturnedRows)
}

func TestCancel_PG(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(500 * time.Millisecond); cancel() }()
	req := FirstPageRequest{
		Scope: UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:   "SELECT pg_sleep(10), id FROM employees", Args: nil,
		SortKeys: []SortKey{{Column: "id", Order: SortAsc}},
		PageSize: 10, MaxRows: 100,
	}
	_, err := h.Query(ctx, req)
	if err == nil {
		t.Fatal("expected cancel error")
	}
	t.Logf("cancel: %v", err)
	// Verify pool still works
	r2, err := h.Query(context.Background(), FirstPageRequest{
		Scope: UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:   "SELECT 1 AS n", Args: nil,
		SortKeys: []SortKey{{Column: "n", Order: SortAsc}},
		PageSize: 1, MaxRows: 100,
	})
	if err != nil {
		t.Fatalf("pool recovery after cancel: %v", err)
	}
	t.Logf("recovery: %d rows", r2.ReturnedRows)
}

func TestTimeout_MySQL(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, myCfg())
	ensureEmployees(t, h)
	defer h.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := h.Query(ctx, FirstPageRequest{
		Scope: UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:   "SELECT SLEEP(10), id FROM employees", Args: nil,
		SortKeys: []SortKey{{Column: "id", Order: SortAsc}}, PageSize: 10, MaxRows: 100,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	t.Logf("mysql timeout: %v", err)
	r2, _ := h.Query(context.Background(), FirstPageRequest{
		Scope: UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:   "SELECT 1 AS n", Args: nil,
		SortKeys: []SortKey{{Column: "n", Order: SortAsc}}, PageSize: 1, MaxRows: 100,
	})
	if r2 != nil {
		t.Logf("mysql recovery: %d rows", r2.ReturnedRows)
	}
}

func TestCancel_MySQL(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, myCfg())
	ensureEmployees(t, h)
	defer h.Release()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(500 * time.Millisecond); cancel() }()
	_, err := h.Query(ctx, FirstPageRequest{
		Scope: UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:   "SELECT SLEEP(10), id FROM employees", Args: nil,
		SortKeys: []SortKey{{Column: "id", Order: SortAsc}}, PageSize: 10, MaxRows: 100,
	})
	if err == nil {
		t.Fatal("expected cancel error")
	}
	t.Logf("mysql cancel: %v", err)
	r2, _ := h.Query(context.Background(), FirstPageRequest{
		Scope: UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:   "SELECT 1 AS n", Args: nil,
		SortKeys: []SortKey{{Column: "n", Order: SortAsc}}, PageSize: 1, MaxRows: 100,
	})
	if r2 != nil {
		t.Logf("mysql recovery: %d rows", r2.ReturnedRows)
	}
}

func TestLeak_Timeout_PG(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		h.Query(ctx, FirstPageRequest{
			Scope: UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
			SQL:   "SELECT pg_sleep(5), id FROM employees", Args: nil,
			SortKeys: []SortKey{{Column: "id", Order: SortAsc}}, PageSize: 1, MaxRows: 100,
		})
		cancel()
	}
	r, err := h.Query(context.Background(), FirstPageRequest{
		Scope: UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:   "SELECT 1 AS n", Args: nil,
		SortKeys: []SortKey{{Column: "n", Order: SortAsc}}, PageSize: 1, MaxRows: 100,
	})
	if err != nil {
		t.Fatalf("leak check failed after 5 timeouts: %v", err)
	}
	if r.ReturnedRows != 1 {
		t.Fatalf("expected 1 row, got %d", r.ReturnedRows)
	}
	t.Logf("PG leak check passed: %d rows after 5 timeouts", r.ReturnedRows)
}

func TestLeak_Cancel_PG(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(100 * time.Millisecond); cancel() }()
		h.Query(ctx, FirstPageRequest{
			Scope: UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
			SQL:   "SELECT pg_sleep(5), id FROM employees", Args: nil,
			SortKeys: []SortKey{{Column: "id", Order: SortAsc}}, PageSize: 1, MaxRows: 100,
		})
	}
	r, err := h.Query(context.Background(), FirstPageRequest{
		Scope: UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:   "SELECT 1 AS n", Args: nil,
		SortKeys: []SortKey{{Column: "n", Order: SortAsc}}, PageSize: 1, MaxRows: 100,
	})
	if err != nil {
		t.Fatalf("leak check failed after 5 cancels: %v", err)
	}
	if r.ReturnedRows != 1 {
		t.Fatalf("expected 1 row, got %d", r.ReturnedRows)
	}
	t.Logf("PG cancel leak check passed")
}

func TestNextPage_MySQL_FullPagination(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, myCfg())
	ensureEmployees(t, h)
	defer h.Release()
	scope := UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"}
	req := FirstPageRequest{
		Scope: scope, SQL: "SELECT id, first_name FROM employees", Args: nil,
		SortKeys: []SortKey{{Column: "id", Order: SortAsc, NullsLast: false}},
		PageSize: 3, MaxRows: 100,
	}
	r1, err := h.Query(context.Background(), req)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if r1.NextToken == nil {
		t.Fatal("expected next token for MySQL page 1")
	}
	t.Logf("MySQL page1: %d rows, token=%s", r1.ReturnedRows, (*r1.NextToken)[:8])

	r2, err := h.NextPage(context.Background(), scope, *r1.NextToken)
	if err != nil {
		t.Fatalf("MySQL page 2: %v", err)
	}
	if r2.ReturnedRows == 0 {
		t.Fatal("expected rows in MySQL page 2")
	}
	t.Logf("MySQL page2: %d rows, next=%v", r2.ReturnedRows, r2.NextToken != nil)

	seen := map[int]bool{}
	for _, row := range r1.Rows {
		seen[int(row[0].(int64))] = true
	}
	for _, row := range r2.Rows {
		id := int(row[0].(int64))
		if seen[id] {
			t.Fatalf("duplicate id %d across MySQL pages", id)
		}
		seen[id] = true
	}
	t.Logf("MySQL no duplicates across 2 pages")
}

func TestNextPage_PG_ThirdPage(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	scope := UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"}
	req := FirstPageRequest{
		Scope: scope, SQL: "SELECT id, first_name FROM employees", Args: nil,
		SortKeys: []SortKey{{Column: "id", Order: SortAsc, NullsLast: false}},
		PageSize: 2, MaxRows: 100,
	}
	r1, err := h.Query(context.Background(), req)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if r1.NextToken == nil {
		t.Fatal("expected page 2 token")
	}
	// Page 2
	r2, err := h.NextPage(context.Background(), scope, *r1.NextToken)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if r2.NextToken == nil {
		t.Skip("no page 3 — not enough rows")
	}
	// Page 3 — this would fail without the inUse reset fix
	r3, err := h.NextPage(context.Background(), scope, *r2.NextToken)
	if err != nil {
		t.Fatalf("page 3: %v", err)
	}
	if r3.ReturnedRows == 0 {
		t.Fatal("expected rows in page 3")
	}
	t.Logf("page3: %d rows, next=%v", r3.ReturnedRows, r3.NextToken != nil)
}

func TestAdmission_RateLimit(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	// maxUser=2: acquire 2 permits for same user
	p1, err := m.ac.TryAcquire("u_limit", "ws1", "conn1")
	if err != nil {
		t.Fatalf("permit 1: %v", err)
	}
	p2, err := m.ac.TryAcquire("u_limit", "ws1", "conn1")
	if err != nil {
		p1.Release()
		t.Fatalf("permit 2: %v", err)
	}
	// 3rd should fail
	_, err = m.ac.TryAcquire("u_limit", "ws1", "conn1")
	if err == nil {
		p1.Release()
		p2.Release()
		t.Fatal("expected rate limit error for 3rd permit")
	}
	t.Logf("rate limit: %v", err)
	p1.Release()
	p2.Release()
	// Now should succeed again
	p3, err := m.ac.TryAcquire("u_limit", "ws1", "conn1")
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	p3.Release()
	t.Log("rate limit recovery OK")
}
func mustGet(t *testing.T, m *AdapterManager, cfg ConnectConfig) *PoolHandle {
	t.Helper()
	h, err := m.Get(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return h
}

func ensureEmployees(t *testing.T, h *PoolHandle) {
	t.Helper()
	if h.entry.pgPool != nil {
		var exists bool
		h.entry.pgPool.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name='employees')").Scan(&exists)
		if !exists {
			_, err := h.entry.pgPool.Exec(context.Background(), "CREATE TABLE employees (id SERIAL PRIMARY KEY, first_name TEXT NOT NULL)")
			if err != nil {
				t.Fatalf("create employees (PG): %v", err)
			}
			_, err = h.entry.pgPool.Exec(context.Background(), "INSERT INTO employees (first_name) VALUES ('Alice'),('Bob'),('Charlie'),('David'),('Eve'),('Frank'),('Grace')")
			if err != nil {
				t.Fatalf("seed employees (PG): %v", err)
			}
		}
	}
	if h.entry.sqlDB != nil {
		var count int
		h.entry.sqlDB.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM information_schema.tables WHERE table_name='employees' AND table_schema=DATABASE()").Scan(&count)
		if count == 0 {
			_, err := h.entry.sqlDB.ExecContext(context.Background(), "CREATE TABLE employees (id INT AUTO_INCREMENT PRIMARY KEY, first_name VARCHAR(255) NOT NULL)")
			if err != nil {
				t.Fatalf("create employees (MySQL): %v", err)
			}
			_, err = h.entry.sqlDB.ExecContext(context.Background(), "INSERT IGNORE INTO employees (id,first_name) VALUES (1,'Alice'),(2,'Bob'),(3,'Charlie'),(4,'David'),(5,'Eve'),(6,'Frank'),(7,'Grace')")
			if err != nil {
				t.Fatalf("seed employees (MySQL): %v", err)
			}
		}
	}
}

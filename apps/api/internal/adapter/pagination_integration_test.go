//go:build integration

package adapter

import (
	"context"
	"testing"
	"time"

	"github.com/fujiabao89/webdb/internal/queryplan"
)

// schemaFor 返回目标库的默认 schema（PG=public，MySQL=database）。
func schemaFor(h *PoolHandle) string {
	if h.entry.sqlDB != nil {
		return h.entry.cfg.Database
	}
	return "public"
}

// verifiedIDPlan 为 employees 表按 id 排序构造 VerifiedSortPlan（经可信元数据）。
func verifiedIDPlan(t *testing.T, h *PoolHandle) queryplan.VerifiedSortPlan {
	t.Helper()
	schema := schemaFor(h)
	meta, err := h.LoadTableMetadata(context.Background(), schema, "employees")
	if err != nil {
		t.Fatalf("LoadTableMetadata: %v", err)
	}
	dialect := queryplan.DialectPostgreSQL
	if h.entry.sqlDB != nil {
		dialect = queryplan.DialectMySQL
	}
	snap, err := queryplan.NewSchemaSnapshot(h.entry.cfg.ConnectionID, dialect, h.PoolGeneration(), meta)
	if err != nil {
		t.Fatalf("NewSchemaSnapshot: %v", err)
	}
	plan, err := queryplan.VerifySortPlan(snap,
		&queryplan.QueryShape{BaseSchema: schema, BaseTable: "employees", SelectStar: true},
		[]queryplan.SortKey{{Column: "id", Direction: queryplan.SortAsc}})
	if err != nil {
		t.Fatalf("VerifySortPlan: %v", err)
	}
	return plan
}

// nextPagePlan 从首页结果构造 VerifiedNextPagePlan（模拟服务层续页）。
func nextPagePlan(t *testing.T, plan queryplan.VerifiedSortPlan, sql string, result *QueryResult, pageSize, maxRows int) queryplan.VerifiedNextPagePlan {
	t.Helper()
	specs := plan.SortSpecs()
	lv, err := ExtractLastValues(result.Rows, result.Columns, specs)
	if err != nil {
		t.Fatalf("ExtractLastValues: %v", err)
	}
	np, err := queryplan.NewVerifiedNextPagePlan(plan, lv, sql, nil, pageSize, maxRows, result.TotalReturned)
	if err != nil {
		t.Fatalf("NewVerifiedNextPagePlan: %v", err)
	}
	return np
}

func TestQuery_PG_VerifiedSortPlan(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	plan := verifiedIDPlan(t, h)
	req := FirstPageRequest{
		Scope:    UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:      "SELECT id, first_name FROM employees",
		SortPlan: plan,
		PageSize: 10, MaxRows: 100,
	}
	result, err := h.Query(context.Background(), req)
	if err != nil {
		t.Skipf("query unavailable: %v", err)
	}
	if result.ReturnedRows == 0 {
		t.Fatal("expected rows")
	}
	t.Logf("PG query: %d rows, has_more=%v", result.ReturnedRows, result.HasMore)
}

func TestQuery_MySQL_VerifiedSortPlan(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, myCfg())
	ensureEmployees(t, h)
	defer h.Release()
	plan := verifiedIDPlan(t, h)
	req := FirstPageRequest{
		Scope:    UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:      "SELECT id, first_name FROM employees",
		SortPlan: plan,
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

func TestQuery_PageSize_VerifiedSortPlan(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	plan := verifiedIDPlan(t, h)
	result, err := h.Query(context.Background(), FirstPageRequest{
		Scope:    UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:      "SELECT id, first_name FROM employees",
		SortPlan: plan,
		PageSize: 3, MaxRows: 100,
	})
	if err != nil {
		t.Skipf("query unavailable: %v", err)
	}
	if result.ReturnedRows > 3 {
		t.Fatalf("expected <=3 rows, got %d", result.ReturnedRows)
	}
	t.Logf("page size test: %d rows, has_more=%v", result.ReturnedRows, result.HasMore)
}

func TestNextPage_PG_FullPagination(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	scope := UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"}
	plan := verifiedIDPlan(t, h)
	r1, err := h.Query(context.Background(), FirstPageRequest{
		Scope: scope, SQL: "SELECT id, first_name FROM employees",
		SortPlan: plan, PageSize: 3, MaxRows: 100,
	})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if !r1.HasMore {
		t.Skip("no second page")
	}
	r2, err := h.NextPage(context.Background(), scope, nextPagePlan(t, plan, "SELECT id, first_name FROM employees", r1, 3, 100))
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if r2.ReturnedRows == 0 {
		t.Fatal("expected rows in page 2")
	}
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
	t.Logf("PG no duplicates across pages, total=%d", r2.TotalReturned)
}

func TestNextPage_MySQL_FullPagination(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, myCfg())
	ensureEmployees(t, h)
	defer h.Release()
	scope := UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"}
	plan := verifiedIDPlan(t, h)
	r1, err := h.Query(context.Background(), FirstPageRequest{
		Scope: scope, SQL: "SELECT id, first_name FROM employees",
		SortPlan: plan, PageSize: 3, MaxRows: 100,
	})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if !r1.HasMore {
		t.Skip("no second page")
	}
	r2, err := h.NextPage(context.Background(), scope, nextPagePlan(t, plan, "SELECT id, first_name FROM employees", r1, 3, 100))
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if r2.ReturnedRows == 0 {
		t.Fatal("expected rows in page 2")
	}
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
	t.Logf("MySQL no duplicates across pages, total=%d", r2.TotalReturned)
}

func TestCurrentSchema_PG(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	s, err := h.CurrentSchema(context.Background())
	if err != nil {
		t.Skipf("current_schema unavailable: %v", err)
	}
	if s == "" {
		t.Fatal("current_schema returned empty")
	}
	t.Logf("PG current_schema: %s", s)
}

func TestCurrentSchema_MySQL(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, myCfg())
	defer h.Release()
	s, err := h.CurrentSchema(context.Background())
	if err != nil {
		t.Skipf("current schema unavailable: %v", err)
	}
	if s != h.entry.cfg.Database {
		t.Fatalf("MySQL current schema = %q, want %q", s, h.entry.cfg.Database)
	}
}

func TestNextPage_InvalidPlan(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	if _, err := h.NextPage(context.Background(), UserWorkspaceScope{}, nil); err == nil {
		t.Fatal("expected error for invalid next page plan")
	}
}

func TestKeyset_SQL_Debug(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	plan := verifiedIDPlan(t, h)
	specs, err := sortSpecsFromPlan(plan)
	if err != nil {
		t.Fatalf("sortSpecsFromPlan: %v", err)
	}
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

// TestLoadTableMetadata_MySQL_RejectsWholePrefixUniqueIndex 验证 MySQL 复合前缀唯一索引
// UNIQUE(name(10), tenant_id) 被整体拒绝：不得残留 tenant_id 并被误认为完整唯一约束
// （Codex P1-A）。
func TestLoadTableMetadata_MySQL_RejectsWholePrefixUniqueIndex(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, myCfg())
	defer h.Release()
	if h.entry.sqlDB == nil {
		t.Skip("not a MySQL handle")
	}
	schema := h.entry.cfg.Database
	table := "web38_uniq_pref"
	_, err := h.entry.sqlDB.ExecContext(context.Background(),
		"DROP TABLE IF EXISTS "+table)
	if err != nil {
		t.Skipf("skip: MySQL demo account lacks DDL privilege: %v", err)
	}
	defer func() { _, _ = h.entry.sqlDB.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+table) }()
	_, err = h.entry.sqlDB.ExecContext(context.Background(),
		"CREATE TABLE "+table+" (name VARCHAR(255) NOT NULL, tenant_id INT NOT NULL, "+
			"UNIQUE KEY uq_name_pref (name(10), tenant_id))")
	if err != nil {
		t.Skipf("skip: cannot create prefix unique index table (MySQL %v): %v", schema, err)
	}

	meta, err := h.LoadTableMetadata(context.Background(), schema, table)
	if err != nil {
		t.Fatalf("LoadTableMetadata: %v", err)
	}
	for _, uq := range meta.UniqueConstraints {
		if uq.Name == "uq_name_pref" {
			t.Fatalf("prefix composite unique index must be rejected wholesale, got %+v", uq)
		}
		if uq.Columns[0] == "tenant_id" {
			t.Fatalf("tenant_id must not survive as a complete unique constraint: %+v", uq)
		}
	}
	if len(meta.UniqueConstraints) != 0 {
		t.Fatalf("expected no unique constraints (whole prefix index rejected), got %+v", meta.UniqueConstraints)
	}
}

// TestLoadTableMetadata_MySQL_RejectsWholeFunctionalIndex 验证 MySQL 复合函数/表达式唯一索引
// UNIQUE(tenant_id, (LOWER(name))) 被整体拒绝（MySQL 8.0.13+ EXPRESSION 列）。
func TestLoadTableMetadata_MySQL_RejectsWholeFunctionalIndex(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, myCfg())
	defer h.Release()
	if h.entry.sqlDB == nil {
		t.Skip("not a MySQL handle")
	}
	schema := h.entry.cfg.Database
	table := "web38_uniq_func"
	_, err := h.entry.sqlDB.ExecContext(context.Background(),
		"DROP TABLE IF EXISTS "+table)
	if err != nil {
		t.Skipf("skip: MySQL demo account lacks DDL privilege: %v", err)
	}
	defer func() { _, _ = h.entry.sqlDB.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+table) }()
	_, err = h.entry.sqlDB.ExecContext(context.Background(),
		"CREATE TABLE "+table+" (name VARCHAR(255) NOT NULL, tenant_id INT NOT NULL, "+
			"UNIQUE KEY uq_expr (tenant_id, (LOWER(name))))")
	if err != nil {
		t.Skipf("skip: cannot create functional unique index table (MySQL %v, need 8.0.13+): %v", schema, err)
	}

	meta, err := h.LoadTableMetadata(context.Background(), schema, table)
	if err != nil {
		t.Fatalf("LoadTableMetadata: %v", err)
	}
	for _, uq := range meta.UniqueConstraints {
		if uq.Name == "uq_expr" {
			t.Fatalf("functional unique index must be rejected wholesale, got %+v", uq)
		}
	}
	if len(meta.UniqueConstraints) != 0 {
		t.Fatalf("expected no unique constraints (whole functional index rejected), got %+v", meta.UniqueConstraints)
	}
}

func TestTimeout_PG_SinglePage(t *testing.T) {
	m := NewAdapterManager(ManagerOptions{AllowInsecureLocalDemo: true})
	defer m.Close(context.Background())
	h := mustGet(t, m, pgCfg())
	ensureEmployees(t, h)
	defer h.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := h.Query(ctx, FirstPageRequest{
		Scope:    UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:      "SELECT pg_sleep(10), id FROM employees",
		PageSize: 10, MaxRows: 10, // 单页受限
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	t.Logf("timeout: %v", err)
	r2, err := h.Query(context.Background(), FirstPageRequest{
		Scope:    UserWorkspaceScope{UserID: "u1", WorkspaceID: "ws1"},
		SQL:      "SELECT 1 AS n",
		PageSize: 1, MaxRows: 1, // 单页受限
	})
	if err != nil {
		t.Fatalf("pool recovery after timeout: %v", err)
	}
	t.Logf("recovery: %d rows", r2.ReturnedRows)
}

package adapter

import (
	"database/sql"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/fujiabao89/webdb/internal/queryplan"
)

// fakeRows 实现 rowIter，供纯函数级扫描测试使用。
type fakeRows struct {
	data [][]any
	idx  int
	err  error
}

func (f *fakeRows) Next() bool { return f.idx < len(f.data) }
func (f *fakeRows) Err() error { return f.err }
func (f *fakeRows) Scan(dest ...any) error {
	if f.err != nil {
		return f.err
	}
	if f.idx >= len(f.data) {
		return io.EOF
	}
	row := f.data[f.idx]
	f.idx++
	for i, d := range dest {
		if i >= len(row) {
			break
		}
		if err := setScanDest(d, row[i]); err != nil {
			return err
		}
	}
	return nil
}

func setScanDest(dest, v any) error {
	switch d := dest.(type) {
	case *string:
		switch vv := v.(type) {
		case string:
			*d = vv
		case []byte:
			*d = string(vv)
		default:
			*d = fmt.Sprint(vv)
		}
	case *int:
		switch vv := v.(type) {
		case int:
			*d = vv
		case int64:
			*d = int(vv)
		case float64:
			*d = int(vv)
		default:
			return fmt.Errorf("cannot scan %T into *int", v)
		}
	case *sql.NullInt64:
		switch vv := v.(type) {
		case nil:
			*d = sql.NullInt64{}
		case sql.NullInt64:
			*d = vv
		case int:
			*d = sql.NullInt64{Int64: int64(vv), Valid: true}
		case int64:
			*d = sql.NullInt64{Int64: vv, Valid: true}
		default:
			return fmt.Errorf("cannot scan %T into *sql.NullInt64", v)
		}
	case *sql.NullString:
		switch vv := v.(type) {
		case nil:
			*d = sql.NullString{}
		case sql.NullString:
			*d = vv
		case string:
			*d = sql.NullString{String: vv, Valid: true}
		case []byte:
			*d = sql.NullString{String: string(vv), Valid: true}
		default:
			return fmt.Errorf("cannot scan %T into *sql.NullString", v)
		}
	default:
		return fmt.Errorf("unsupported scan dest %T", dest)
	}
	return nil
}

func TestScanColumns(t *testing.T) {
	t.Parallel()
	rows := &fakeRows{data: [][]any{
		{"id", 1, "NO"},
		{"name", 2, "YES"},
	}}
	cols, err := scanColumns(rows)
	if err != nil {
		t.Fatalf("scanColumns() error = %v", err)
	}
	if len(cols) != 2 {
		t.Fatalf("scanColumns() = %d columns, want 2", len(cols))
	}
	if cols[0] != (queryplan.Column{Name: "id", Ordinal: 1, Nullable: false}) {
		t.Fatalf("cols[0] = %+v", cols[0])
	}
	if cols[1].Nullable != true {
		t.Fatalf("cols[1].Nullable = %v, want true", cols[1].Nullable)
	}
}

func TestScanColumnsPropagatesError(t *testing.T) {
	t.Parallel()
	rows := &fakeRows{err: fmt.Errorf("db down")}
	if _, err := scanColumns(rows); err == nil {
		t.Fatal("scanColumns() error = nil, want propagated error")
	}
}

func TestScanPK(t *testing.T) {
	t.Parallel()
	rows := &fakeRows{data: [][]any{{"id"}, {"tenant_id"}}}
	pk, err := scanPK(rows)
	if err != nil {
		t.Fatalf("scanPK() error = %v", err)
	}
	if len(pk) != 2 || pk[0] != "id" || pk[1] != "tenant_id" {
		t.Fatalf("scanPK() = %v", pk)
	}
}

func TestScanUniquePairsGroupsPreservingOrder(t *testing.T) {
	t.Parallel()
	// 输入乱序，按 (name, column) 分组且保持每约束内列顺序。
	rows := &fakeRows{data: [][]any{
		{"uq_ab", "b"},
		{"uq_ab", "a"},
		{"uq_solo", "x"},
		{"uq_ab", "c"},
	}}
	uqs, err := scanUniquePairs(rows)
	if err != nil {
		t.Fatalf("scanUniquePairs() error = %v", err)
	}
	if len(uqs) != 2 {
		t.Fatalf("scanUniquePairs() = %d constraints, want 2: %+v", len(uqs), uqs)
	}
	if uqs[0].Name != "uq_ab" || len(uqs[0].Columns) != 3 ||
		uqs[0].Columns[0] != "b" || uqs[0].Columns[1] != "a" || uqs[0].Columns[2] != "c" {
		t.Fatalf("uq_ab = %+v, want columns [b a c] preserving scan order", uqs[0])
	}
	if uqs[1].Name != "uq_solo" || len(uqs[1].Columns) != 1 || uqs[1].Columns[0] != "x" {
		t.Fatalf("uq_solo = %+v", uqs[1])
	}
}

func TestScanMySQLUniquePairsRejectsWholePrefixIndex(t *testing.T) {
	t.Parallel()
	// UNIQUE(name(10), tenant_id)：name 带 SUB_PART=10，整个索引必须排除，
	// 不得只过滤 name 行而把 tenant_id 误认为完整唯一约束（Codex P1-A）。
	rows := &fakeRows{data: [][]any{
		{"uq_pref", "name", sql.NullInt64{Int64: 10, Valid: true}, sql.NullString{}},
		{"uq_pref", "tenant_id", sql.NullInt64{}, sql.NullString{}},
	}}
	uqs, err := scanMySQLUniquePairs(rows)
	if err != nil {
		t.Fatalf("scanMySQLUniquePairs() error = %v", err)
	}
	if len(uqs) != 0 {
		t.Fatalf("scanMySQLUniquePairs() = %+v, want whole prefix index excluded (no surviving tenant_id)", uqs)
	}
}

func TestScanMySQLUniquePairsRejectsWholeExpressionIndex(t *testing.T) {
	t.Parallel()
	// UNIQUE((lower(email)), tenant_id)：email 带 EXPRESSION，整个索引必须排除。
	rows := &fakeRows{data: [][]any{
		{"uq_expr", "email", sql.NullInt64{}, sql.NullString{String: "lower(`email`)", Valid: true}},
		{"uq_expr", "tenant_id", sql.NullInt64{}, sql.NullString{}},
	}}
	uqs, err := scanMySQLUniquePairs(rows)
	if err != nil {
		t.Fatalf("scanMySQLUniquePairs() error = %v", err)
	}
	if len(uqs) != 0 {
		t.Fatalf("scanMySQLUniquePairs() = %+v, want whole expression index excluded", uqs)
	}
}

func TestScanMySQLUniquePairsKeepsCleanCompositeIndex(t *testing.T) {
	t.Parallel()
	// UNIQUE(a, b) 无 SUB_PART/EXPRESSION：保留两列（列序按扫描顺序）。
	rows := &fakeRows{data: [][]any{
		{"uq_ab", "a", sql.NullInt64{}, sql.NullString{}},
		{"uq_ab", "b", sql.NullInt64{}, sql.NullString{}},
	}}
	uqs, err := scanMySQLUniquePairs(rows)
	if err != nil {
		t.Fatalf("scanMySQLUniquePairs() error = %v", err)
	}
	if len(uqs) != 1 || uqs[0].Name != "uq_ab" || len(uqs[0].Columns) != 2 ||
		uqs[0].Columns[0] != "a" || uqs[0].Columns[1] != "b" {
		t.Fatalf("scanMySQLUniquePairs() = %+v, want uq_ab=[a b]", uqs)
	}
}

func TestScanMySQLUniquePairsNullExpressionColumn(t *testing.T) {
	t.Parallel()
	// MySQL 8.0.0-8.0.12 无 STATISTICS.EXPRESSION 列：兼容查询以 NULL AS EXPRESSION
	// 替代，expr 读为无效 NullString。前缀索引仍因 SUB_PART 整索引剔除（Codex P1）。
	rows := &fakeRows{data: [][]any{
		{"uq_pref", "name", sql.NullInt64{Int64: 10, Valid: true}, nil},
		{"uq_pref", "tenant_id", sql.NullInt64{}, nil},
		{"uq_clean", "email", sql.NullInt64{}, nil},
	}}
	uqs, err := scanMySQLUniquePairs(rows)
	if err != nil {
		t.Fatalf("scanMySQLUniquePairs() error = %v", err)
	}
	if len(uqs) != 1 || uqs[0].Name != "uq_clean" || len(uqs[0].Columns) != 1 ||
		uqs[0].Columns[0] != "email" {
		t.Fatalf("scanMySQLUniquePairs() = %+v, want only uq_clean=[email] with NULL expression", uqs)
	}
}

func TestScanMySQLUniquePairsMixedIndexes(t *testing.T) {
	t.Parallel()
	// 危险索引与干净索引并存：只保留干净索引，危险索引整组剔除。
	rows := &fakeRows{data: [][]any{
		{"uq_danger", "name", sql.NullInt64{Int64: 5, Valid: true}, sql.NullString{}},
		{"uq_danger", "tenant_id", sql.NullInt64{}, sql.NullString{}},
		{"uq_clean", "email", sql.NullInt64{}, sql.NullString{}},
	}}
	uqs, err := scanMySQLUniquePairs(rows)
	if err != nil {
		t.Fatalf("scanMySQLUniquePairs() error = %v", err)
	}
	if len(uqs) != 1 || uqs[0].Name != "uq_clean" || len(uqs[0].Columns) != 1 ||
		uqs[0].Columns[0] != "email" {
		t.Fatalf("scanMySQLUniquePairs() = %+v, want only uq_clean=[email]", uqs)
	}
}

func TestAssembleTableMetadata(t *testing.T) {
	t.Parallel()
	meta, err := assembleTableMetadata("public", "users",
		[]queryplan.Column{
			{Name: "id", Ordinal: 1, Nullable: false},
			{Name: "email", Ordinal: 2, Nullable: true},
		},
		[]string{"id"},
		map[string][]string{"uq_email": {"email"}},
	)
	if err != nil {
		t.Fatalf("assembleTableMetadata() error = %v", err)
	}
	if meta.Schema != "public" || meta.Table != "users" || len(meta.Columns) != 2 {
		t.Fatalf("assembleTableMetadata() = %+v", meta)
	}
	if meta.PrimaryKey == nil || len(meta.PrimaryKey.Columns) != 1 || meta.PrimaryKey.Columns[0] != "id" {
		t.Fatalf("pk = %+v", meta.PrimaryKey)
	}
	if len(meta.UniqueConstraints) != 1 || meta.UniqueConstraints[0].Name != "uq_email" {
		t.Fatalf("unique = %+v", meta.UniqueConstraints)
	}
	if err := meta.Validate(); err != nil {
		t.Fatalf("assembled metadata invalid: %v", err)
	}
}

func TestAssembleTableMetadataFailsClosedOnMissingColumns(t *testing.T) {
	t.Parallel()
	_, err := assembleTableMetadata("public", "ghost_table", nil, nil, nil)
	if err == nil {
		t.Fatal("assembleTableMetadata() error = nil for empty columns, want fail-closed")
	}
	if !strings.Contains(err.Error(), "no columns") {
		t.Fatalf("assembleTableMetadata() error = %q, want 'no columns'", err)
	}
}

func TestAssembleTableMetadataRejectsDuplicateColumns(t *testing.T) {
	t.Parallel()
	_, err := assembleTableMetadata("public", "users",
		[]queryplan.Column{
			{Name: "id", Ordinal: 1, Nullable: false},
			{Name: "id", Ordinal: 2, Nullable: false},
		},
		[]string{"id"}, nil,
	)
	if err == nil {
		t.Fatal("assembleTableMetadata() error = nil for duplicate columns, want fail-closed")
	}
}

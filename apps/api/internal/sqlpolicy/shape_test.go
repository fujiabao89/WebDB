package sqlpolicy

import (
	"reflect"
	"testing"

	"github.com/fujiabao89/webdb/internal/queryplan"
)

func shapeEq(a, b *queryplan.QueryShape) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.BaseSchema != b.BaseSchema || a.BaseTable != b.BaseTable || a.SelectStar != b.SelectStar {
		return false
	}
	// 空 map 与 nil map 视为相等。
	if len(a.Columns) == 0 && len(b.Columns) == 0 {
		return true
	}
	return reflect.DeepEqual(a.Columns, b.Columns)
}

func TestAnalyzeShapePG(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		sql     string
		wantErr bool
		want    *queryplan.QueryShape
	}{
		{name: "star unqualified", sql: "SELECT * FROM users", want: &queryplan.QueryShape{BaseTable: "users", SelectStar: true}},
		{name: "star schema qualified", sql: "SELECT * FROM public.users", want: &queryplan.QueryShape{BaseSchema: "public", BaseTable: "users", SelectStar: true}},
		{name: "plain columns", sql: "SELECT id, name FROM users", want: &queryplan.QueryShape{BaseTable: "users", Columns: map[string]string{"id": "id", "name": "name"}}},
		{name: "qualified columns", sql: "SELECT u.id, u.name FROM users u", want: &queryplan.QueryShape{BaseTable: "users", Columns: map[string]string{"id": "id", "name": "name"}}},
		{name: "alias exposed name", sql: "SELECT u.id AS user_id FROM users u", want: &queryplan.QueryShape{BaseTable: "users", Columns: map[string]string{"user_id": "id"}}},
		{name: "table star", sql: "SELECT u.* FROM users u", want: &queryplan.QueryShape{BaseTable: "users", SelectStar: true}},
		{name: "table stmt", sql: "TABLE users", want: &queryplan.QueryShape{BaseTable: "users", SelectStar: true}},
		{name: "inner order by allowed", sql: "SELECT * FROM users ORDER BY id", want: &queryplan.QueryShape{BaseTable: "users", SelectStar: true}},
		{name: "where allowed", sql: "SELECT * FROM users WHERE id > 10", want: &queryplan.QueryShape{BaseTable: "users", SelectStar: true}},
		{name: "where plain comparison func on const ok", sql: "SELECT * FROM users WHERE name = 'x'", want: &queryplan.QueryShape{BaseTable: "users", SelectStar: true}},
		{name: "where volatile func rejected", sql: "SELECT * FROM users WHERE random() < 0.5", wantErr: true},
		{name: "where volatile now rejected", sql: "SELECT * FROM users WHERE created_at > now() - interval '1 day'", wantErr: true},

		{name: "computed expression", sql: "SELECT id+1 AS x FROM users", wantErr: true},
		{name: "aggregate", sql: "SELECT count(*) FROM users", wantErr: true},
		{name: "join", sql: "SELECT * FROM a JOIN b ON a.id=b.a_id", wantErr: true},
		{name: "multiple tables", sql: "SELECT * FROM users, orders", wantErr: true},
		{name: "subquery from", sql: "SELECT * FROM (SELECT 1) x", wantErr: true},
		{name: "group by", sql: "SELECT * FROM users GROUP BY id", wantErr: true},
		{name: "having", sql: "SELECT id FROM users GROUP BY id HAVING count(*)>1", wantErr: true},
		{name: "distinct", sql: "SELECT DISTINCT id FROM users", wantErr: true},
		{name: "cte", sql: "WITH c AS (SELECT 1) SELECT * FROM c", wantErr: true},
		{name: "set op", sql: "SELECT * FROM users UNION SELECT * FROM orders", wantErr: true},
		{name: "for update", sql: "SELECT * FROM users FOR UPDATE", wantErr: true},
		{name: "limit", sql: "SELECT * FROM users LIMIT 10", wantErr: true},
		{name: "offset", sql: "SELECT * FROM users OFFSET 5", wantErr: true},
		{name: "duplicate exposed columns", sql: "SELECT id, id FROM users", wantErr: true},
		{name: "no from", sql: "SELECT 1", wantErr: true},
		{name: "window", sql: "SELECT id, row_number() OVER () FROM users", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := AnalyzeShape(DialectPostgreSQL, tt.sql)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("AnalyzeShape(%q) error = nil, want fail-closed", tt.sql)
				}
				return
			}
			if err != nil {
				t.Fatalf("AnalyzeShape(%q) error = %v", tt.sql, err)
			}
			if !shapeEq(got, tt.want) {
				t.Fatalf("AnalyzeShape(%q) = %+v, want %+v", tt.sql, got, tt.want)
			}
		})
	}
}

func TestAnalyzeShapeMySQL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		sql     string
		wantErr bool
		want    *queryplan.QueryShape
	}{
		{name: "star", sql: "SELECT * FROM users", want: &queryplan.QueryShape{BaseTable: "users", SelectStar: true}},
		{name: "qualified star", sql: "SELECT * FROM app.users", want: &queryplan.QueryShape{BaseSchema: "app", BaseTable: "users", SelectStar: true}},
		{name: "plain columns", sql: "SELECT id, name FROM users", want: &queryplan.QueryShape{BaseTable: "users", Columns: map[string]string{"id": "id", "name": "name"}}},
		{name: "qualified columns", sql: "SELECT u.id, u.name FROM users u", want: &queryplan.QueryShape{BaseTable: "users", Columns: map[string]string{"id": "id", "name": "name"}}},
		{name: "alias", sql: "SELECT u.id AS user_id FROM users u", want: &queryplan.QueryShape{BaseTable: "users", Columns: map[string]string{"user_id": "id"}}},
		{name: "table star", sql: "SELECT u.* FROM users u", want: &queryplan.QueryShape{BaseTable: "users", SelectStar: true}},

		{name: "computed", sql: "SELECT id+1 AS x FROM users", wantErr: true},
		{name: "aggregate", sql: "SELECT count(*) FROM users", wantErr: true},
		{name: "join", sql: "SELECT * FROM a JOIN b ON a.id=b.a_id", wantErr: true},
		{name: "multiple tables", sql: "SELECT * FROM users, orders", wantErr: true},
		{name: "subquery from", sql: "SELECT * FROM (SELECT 1) x", wantErr: true},
		{name: "group by", sql: "SELECT * FROM users GROUP BY id", wantErr: true},
		{name: "distinct", sql: "SELECT DISTINCT id FROM users", wantErr: true},
		{name: "cte", sql: "WITH c AS (SELECT 1) SELECT * FROM c", wantErr: true},
		{name: "union", sql: "SELECT * FROM users UNION SELECT * FROM orders", wantErr: true},
		{name: "limit", sql: "SELECT * FROM users LIMIT 10", wantErr: true},
		{name: "duplicate exposed", sql: "SELECT id, id FROM users", wantErr: true},
		{name: "no from", sql: "SELECT 1", wantErr: true},
		{name: "where allowed", sql: "SELECT * FROM users WHERE id > 10", want: &queryplan.QueryShape{BaseTable: "users", SelectStar: true}},
		{name: "where volatile RAND rejected", sql: "SELECT * FROM users WHERE RAND() < 0.5", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := AnalyzeShape(DialectMySQL, tt.sql)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("AnalyzeShape(%q) error = nil, want fail-closed", tt.sql)
				}
				return
			}
			if err != nil {
				t.Fatalf("AnalyzeShape(%q) error = %v", tt.sql, err)
			}
			if !shapeEq(got, tt.want) {
				t.Fatalf("AnalyzeShape(%q) = %+v, want %+v", tt.sql, got, tt.want)
			}
		})
	}
}

func TestAnalyzeShapeUnknownDialect(t *testing.T) {
	t.Parallel()
	if _, err := AnalyzeShape(Dialect("oracle"), "SELECT 1"); err == nil {
		t.Fatal("AnalyzeShape() error = nil for unknown dialect")
	}
}

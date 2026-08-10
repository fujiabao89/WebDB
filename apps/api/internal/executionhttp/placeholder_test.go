package executionhttp

import "testing"

// TestDetectPlaceholderPG 验证 PG 位置占位符 token 级判定（P0-06A §8.1）。
// $N 拒绝；JSONB 操作符 ? / ?| / ?& / :: / @> 放行；字符串/注释/美元引号内放行。
func TestDetectPlaceholderPG(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want bool
	}{
		{"unbound $1", "SELECT * FROM t WHERE id = $1", true},
		{"unbound $100", "SELECT * FROM t WHERE id = $100", true},
		{"jsonb ? operator", "SELECT payload::jsonb ? 'key' FROM t", false},
		{"jsonb ?| operator", "SELECT a ?| ARRAY['x','y'] FROM t", false},
		{"jsonb ?& operator", "SELECT a ?& ARRAY['x','y'] FROM t", false},
		{"cast operator", "SELECT payload::jsonb FROM t", false},
		{"containment operator", "SELECT * FROM t WHERE j @> '{\"a\":1}'", false},
		{"string literal dollar", "SELECT 'it''s $1'", false},
		{"line comment dollar", "SELECT 1 -- $1", false},
		{"block comment dollar", "SELECT /* $1 */ 1", false},
		{"dollar quoted string", "SELECT $$hello $1$$", false},
		{"tagged dollar quoted", "SELECT $tag$x$tag$", false},
		{"no placeholder", "SELECT * FROM t ORDER BY id LIMIT 100", false},
		{"double colon only", "SELECT x::text FROM t", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := detectUnboundPlaceholder("postgresql", c.sql); got != c.want {
				t.Errorf("detectUnboundPlaceholder(pg, %q) = %v, want %v", c.sql, got, c.want)
			}
		})
	}
}

// TestDetectPlaceholderMySQL 验证 MySQL 位置占位符判定。
// 可执行位置的 ? 拒绝；字符串/注释/反引号标识符内放行。
func TestDetectPlaceholderMySQL(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want bool
	}{
		{"unbound ?", "SELECT * FROM t WHERE id = ?", true},
		{"two placeholders", "SELECT * FROM t WHERE a = ? AND b = ?", true},
		{"string literal question", "SELECT '?'", false},
		{"line comment question", "SELECT 1 -- ?", false},
		{"block comment question", "SELECT /* ? */ 1", false},
		{"backtick ident", "SELECT `a?b` FROM t", false},
		{"backslash escaped quote", "SELECT 'it\\'s ?'", false},
		{"backslash path", "SELECT 'C:\\\\temp' FROM t", false},
		{"no placeholder", "SELECT * FROM t LIMIT 100", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := detectUnboundPlaceholder("mysql", c.sql); got != c.want {
				t.Errorf("detectUnboundPlaceholder(mysql, %q) = %v, want %v", c.sql, got, c.want)
			}
		})
	}
}

package sqlpolicy

import (
	"errors"
	"testing"
)

// TestHasUnboundPlaceholderPG 直接验证 sqlpolicy 包的 PG 占位符判定（使用已验证
// DialectPostgreSQL 常量映射，非字符串转换）。$N 拒绝；JSONB ?/?|/?& 与字符串/
// 注释/美元引号内放行。
func TestHasUnboundPlaceholderPG(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want bool
	}{
		{"unbound $1", "SELECT * FROM t WHERE id = $1", true},
		{"jsonb ?", "SELECT payload::jsonb ? 'key' FROM t", false},
		{"jsonb ?|", "SELECT a ?| ARRAY['x','y'] FROM t", false},
		{"jsonb ?&", "SELECT a ?& ARRAY['x','y'] FROM t", false},
		{"dollar quoted $1 inside", "SELECT $$hello $1$$", false},
		{"tagged dollar quoted", "SELECT $tag$x$tag$", false},
		{"string literal", "SELECT 'it''s $1'", false},
		// PG `#` 不是注释：`#>` 后 $1 必须识别（CodeRabbit #21）。
		{"jsonb #> then $1", "SELECT j #> $1 FROM t", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := HasUnboundPlaceholder(DialectPostgreSQL, c.sql)
			if err != nil {
				t.Fatalf("HasUnboundPlaceholder(pg, %q) 不应报错: %v", c.sql, err)
			}
			if got != c.want {
				t.Errorf("HasUnboundPlaceholder(pg, %q) = %v, want %v", c.sql, got, c.want)
			}
		})
	}
}

// TestHasUnboundPlaceholderMySQL 直接验证 MySQL 占位符判定（DialectMySQL 常量）。
// ? 拒绝；# 注释、反斜杠转义、字符串/注释/反引号内放行。
func TestHasUnboundPlaceholderMySQL(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want bool
	}{
		{"unbound ?", "SELECT * FROM t WHERE id = ?", true},
		{"hash comment ?", "SELECT 1 # ?", false},
		{"backslash escaped quote", "SELECT 'it\\'s ?'", false},
		{"backtick ident", "SELECT `a?b` FROM t", false},
		{"string literal", "SELECT '?'", false},
		// MySQL `$` 是合法标识符字符，不得当美元引号跳过区间内 ? 占位符（CodeRabbit 新 #6）。
		{"dollar-like identifier with placeholder", "SELECT a$b$c FROM t WHERE id = ?", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := HasUnboundPlaceholder(DialectMySQL, c.sql)
			if err != nil {
				t.Fatalf("HasUnboundPlaceholder(mysql, %q) 不应报错: %v", c.sql, err)
			}
			if got != c.want {
				t.Errorf("HasUnboundPlaceholder(mysql, %q) = %v, want %v", c.sql, got, c.want)
			}
		})
	}
}

// TestHasUnboundPlaceholderFailClosed 验证 fail-closed（CodeRabbit #21）：未知方言
// 或未闭合词法结构必须返回 error，不得静默按"无占位符"放行。
func TestHasUnboundPlaceholderFailClosed(t *testing.T) {
	cases := []struct {
		name    string
		dialect Dialect
		sql     string
	}{
		{"unknown dialect", Dialect("sqlserver"), "SELECT 1"},
		{"pg unclosed string", DialectPostgreSQL, "SELECT 'abc ?"},
		{"pg unclosed dollar quote", DialectPostgreSQL, "SELECT $$abc $1"},
		{"pg unclosed block comment", DialectPostgreSQL, "SELECT /* $1"},
		{"pg unclosed double quote", DialectPostgreSQL, `SELECT "abc FROM t`},
		{"mysql unclosed string", DialectMySQL, "SELECT 'abc ?"},
		{"mysql unclosed block comment", DialectMySQL, "SELECT /* ?"},
		{"mysql unclosed backtick", DialectMySQL, "SELECT `abc ?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := HasUnboundPlaceholder(c.dialect, c.sql); err == nil {
				t.Errorf("HasUnboundPlaceholder(%s, %q) 应返回 error（fail-closed）", c.dialect, c.sql)
			}
		})
	}
}

// TestHasUnboundPlaceholderUnknownDialectError 验证未知方言返回的 error 可被
// errors.Is 风格判定（调用方 fail-closed 的依据）。
func TestHasUnboundPlaceholderUnknownDialectError(t *testing.T) {
	_, err := HasUnboundPlaceholder(Dialect("unknown"), "SELECT 1")
	if err == nil {
		t.Fatal("未知方言必须返回 error")
	}
	var wantErr interface{ Error() string }
	if !errors.As(err, &wantErr) {
		t.Fatalf("err = %T, want error", err)
	}
}

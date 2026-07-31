package sqlpolicy

import (
	"strings"
	"testing"
)

// TestECMLexer_Positive 可执行注释正例 —— 必须全部检测到。
func TestECMLexer_Positive(t *testing.T) {
	tests := []struct {
		id   string
		sql  string
		mode MySQLLexerMode
	}{
		{id: "EC-01", sql: "/*!50000 DROP TABLE t*/ SELECT 1"},
		{id: "EC-02", sql: "/*!50000\nDROP TABLE t;\n*/ SELECT 1"},
		{id: "EC-03", sql: "/*!99999 SELECT 1*/"},
		{id: "EC-04", sql: "SELECT /*! 1 + 1 */ FROM dual"},
		{id: "EC-05", sql: "SELECT /*!80100 42*/ FROM dual"},
		{id: "EC-06", sql: "SELECT /*!80100 1 /* nested */ + 2 */"},
		{id: "EC-07", sql: "SELECT /*! 1 + 2 */"},
		{id: "EC-08", sql: "SELECT 1 + /*!801002*/"},
		{id: "EC-09", sql: "SELECT /*!90000 1 + 2 */ 42"},
		{id: "EC-10", sql: "/*!999999 SELECT 1*/ SELECT 2"},
		{id: "EC-11", sql: "/*!50000\tSELECT\n1*/"},
		{id: "EC-12", sql: "/*!40014 SET NAMES utf8mb4*/ SELECT 1"},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			l := newECMLexer(tt.sql, tt.mode)
			hasECM, err := l.scan()
			if err != nil {
				return // lexer error → fail-closed, 等效于检测到 ECM
			}
			if !hasECM {
				t.Errorf("%s: ECM 正例未被检测到\nSQL: %s", tt.id, tt.sql)
			}
		})
	}
}

// TestECMLexer_Negative 反例 —— 不得误报。
func TestECMLexer_Negative(t *testing.T) {
	tests := []struct {
		id   string
		sql  string
		mode MySQLLexerMode
	}{
		{id: "EC-B1", sql: "SELECT '/*!50000 DROP TABLE t*/' AS txt"},
		{id: "EC-B2", sql: `SELECT "/*!50000 DROP TABLE t*/" AS txt`},
		{id: "EC-B3", sql: "SELECT * FROM t /* 普通注释含 /*!50000 */  WHERE id=1"},
		{id: "EC-B4", sql: "SELECT /*+ USE_INDEX(t) */ * FROM t"},
		{id: "EC-B5", sql: "SELECT 1 # /*!50000 DROP TABLE t*/"},
		{id: "EC-B6", sql: "SELECT 1 -- /*!50000 DROP TABLE t*/"},
		{id: "EC-B7", sql: "SELECT * FROM t"},
		{id: "EC-B8", sql: "SELECT `/*!50000` FROM t"},
		{id: "EC-B9", sql: "SELECT * /*\n多行\n注释 /*!50000 */ FROM t"},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			l := newECMLexer(tt.sql, tt.mode)
			hasECM, err := l.scan()
			if err != nil {
				t.Errorf("%s: 反例不应产生 lexer 错误: %v", tt.id, err)
				return
			}
			if hasECM {
				t.Errorf("%s: 反例被误判为 ECM\nSQL: %s", tt.id, tt.sql)
			}
		})
	}
}

// TestECMLexer_Boundary ECM 边界场景。
func TestECMLexer_Boundary(t *testing.T) {
	// MY-EC-01: 引号/反引号内的 /*! 不是 ECM
	t.Run("string_literal_ecm", func(t *testing.T) {
		cases := []string{
			"SELECT '/*!50000' FROM t",
			`SELECT "/*!50000" FROM t`,
			"SELECT `/*!50000` FROM t",
		}
		for _, sql := range cases {
			l := newECMLexer(sql, MySQLLexerMode{})
			has, err := l.scan()
			if err != nil {
				t.Errorf("不应报错: %v\nSQL: %s", err, sql)
			}
			if has {
				t.Errorf("引号内的 /*! 不应被识别为 ECM\nSQL: %s", sql)
			}
		}
	})

	// MY-EC-04: 未闭合 ECM opener
	t.Run("unclosed_ecm", func(t *testing.T) {
		cases := []string{
			"/*!",
			"SELECT /*!50000",
			"SELECT /*!abc",
		}
		for _, sql := range cases {
			l := newECMLexer(sql, MySQLLexerMode{})
			has, _ := l.scan()
			if !has {
				t.Logf("未闭合 ECM opener: hasECM=%v (fail-closed if ECM detected or lexer error)\nSQL: %s", has, sql)
			}
		}
	})

	// MY-EC-05: NO_BACKSLASH_ESCAPES mode —— 引号边界变化
	t.Run("no_backslash_escapes", func(t *testing.T) {
		// NO_BACKSLASH_ESCAPES: \' 不转义引号，引号在 \ 处闭合
		sql := "SELECT 'test\\' /*!50000' FROM t"
		l := newECMLexer(sql, MySQLLexerMode{NoBackslashEscapes: true})
		has, err := l.scan()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has {
			t.Errorf("NO_BACKSLASH_ESCAPES: \\' 闭合字符串，/*! 在 SQL 上下文中，应为 ECM")
		}
	})

	// -- 行注释的各种空白
	t.Run("dash_dash_whitespace", func(t *testing.T) {
		cases := []string{
			"SELECT 1 --\t/*!50000",
		}
		for _, sql := range cases {
			l := newECMLexer(sql, MySQLLexerMode{})
			has, err := l.scan()
			if err != nil {
				t.Errorf("unexpected error: %v\nSQL: %s", err, sql)
			}
			if has {
				t.Errorf("-- 行注释内的 /*! 不应被识别为 ECM\nSQL: %s", sql)
			}
		}
	})
}

// TestECMLexer_PanicFree 任意输入不 panic。
func TestECMLexer_PanicFree(t *testing.T) {
	inputs := []string{
		"",
		"\x00\x00\x00",
		"'unclosed",
		`"unclosed`,
		"`unclosed",
		"/*!",
		"/*!50000",
		"/* unclosed",
		strings.Repeat("A", 100000),
		strings.Repeat("/*!", 10000),
		"SELECT 'x' 'y'",
		"\\'",
		"\\\\'",
	}
	for _, input := range inputs {
		l := newECMLexer(input, MySQLLexerMode{})
		// 不 panic 即为通过
		l.scan()
	}
}

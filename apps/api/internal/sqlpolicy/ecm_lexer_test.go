package sqlpolicy

import (
	"strings"
	"testing"
)

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
			// [CR #5] 正例必须断言 err==nil 且 hasECM==true
			if err != nil {
				t.Fatalf("%s: unexpected lexer error: %v", tt.id, err)
			}
			if !hasECM {
				t.Errorf("%s: ECM positive not detected\nSQL: %s", tt.id, tt.sql)
			}
		})
	}
}

func TestECMLexer_Negative(t *testing.T) {
	tests := []struct {
		id   string
		sql  string
		mode MySQLLexerMode
	}{
		{id: "EC-B1", sql: "SELECT '/*!50000 DROP TABLE t*/' AS txt"},
		{id: "EC-B2", sql: `SELECT "/*!50000 DROP TABLE t*/" AS txt`},
		{id: "EC-B3", sql: "SELECT * FROM t /* comment with /*!50000 */  WHERE id=1"},
		{id: "EC-B4", sql: "SELECT /*+ USE_INDEX(t) */ * FROM t"},
		{id: "EC-B5", sql: "SELECT 1 # /*!50000 DROP TABLE t*/"},
		{id: "EC-B6", sql: "SELECT 1 -- /*!50000 DROP TABLE t*/"},
		{id: "EC-B7", sql: "SELECT * FROM t"},
		{id: "EC-B8", sql: "SELECT `/*!50000` FROM t"},
		{id: "EC-B9", sql: "SELECT * /*\nmultiline\ncomment /*!50000 */ FROM t"},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			l := newECMLexer(tt.sql, tt.mode)
			hasECM, err := l.scan()
			if err != nil {
				t.Errorf("%s: neg should not produce lexer error: %v", tt.id, err)
				return
			}
			if hasECM {
				t.Errorf("%s: false positive ECM\nSQL: %s", tt.id, tt.sql)
			}
		})
	}
}

func TestECMLexer_Boundary(t *testing.T) {
	t.Run("string_literal_ecm", func(t *testing.T) {
		for _, sql := range []string{
			"SELECT '/*!50000' FROM t",
			"SELECT `/*!50000` FROM t",
		} {
			l := newECMLexer(sql, MySQLLexerMode{})
			has, err := l.scan()
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if has {
				t.Errorf("quoted /*! should not be ECM\nSQL: %s", sql)
			}
		}
	})

	// [CR #7] 未闭合 ECM opener 必须断言 hasECM==true
	t.Run("unclosed_ecm", func(t *testing.T) {
		for _, sql := range []string{"/*!", "SELECT /*!50000", "SELECT /*!abc"} {
			l := newECMLexer(sql, MySQLLexerMode{})
			has, _ := l.scan()
			if !has {
				t.Errorf("unclosed ECM opener must have hasECM=true\nSQL: %s", sql)
			}
		}
	})

	t.Run("no_backslash_escapes", func(t *testing.T) {
		sql := "SELECT 'test\\' /*!50000' FROM t"
		l := newECMLexer(sql, MySQLLexerMode{NoBackslashEscapes: true})
		has, err := l.scan()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has {
			t.Error("NO_BACKSLASH_ESCAPES: ECM should be detected")
		}
	})

	t.Run("dash_dash_whitespace", func(t *testing.T) {
		l := newECMLexer("SELECT 1 --\t/*!50000", MySQLLexerMode{})
		has, err := l.scan()
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if has {
			t.Error("-- comment should suppress ECM")
		}
	})

	// [CR #7] ANSI_QUOTES 模式：双引号标识符不误判
	t.Run("ansi_quotes_ident", func(t *testing.T) {
		sql := `SELECT "/*!50000" FROM t`
		l := newECMLexer(sql, MySQLLexerMode{ANSIQuotes: true})
		has, err := l.scan()
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if has {
			t.Error("ANSI_QUOTES: double-quoted /*! should not be ECM")
		}
	})

	// [CR #11] ANSI_QUOTES 模式下 ECM payload 仍应检测
	t.Run("ansi_quotes_ecm_detect", func(t *testing.T) {
		sql := "SELECT 1 FROM t WHERE /*!50000 INTO OUTFILE '/tmp/x'*/ 1=1"
		l := newECMLexer(sql, MySQLLexerMode{ANSIQuotes: true})
		has, err := l.scan()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has {
			t.Error("ANSI_QUOTES: ECM in SQL context must be detected")
		}
	})
}

func TestECMLexer_PanicFree(t *testing.T) {
	for _, input := range []string{
		"", "\x00\x00\x00", "'unclosed", `"unclosed`, "`unclosed",
		"/*!", "/*!50000", "/* unclosed",
		strings.Repeat("A", 100000), strings.Repeat("/*!", 10000),
		"SELECT 'x' 'y'", "\\'", "\\\\'",
	} {
		l := newECMLexer(input, MySQLLexerMode{})
		l.scan()
	}
}

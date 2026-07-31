package execution

import (
	"testing"

	"github.com/fujiabao89/webdb/internal/sqlpolicy"
)

func TestEvaluateSQL_PG_Allowed(t *testing.T) {
	_, code := EvaluateSQL(EnginePostgreSQL, "SELECT * FROM t", sqlpolicy.MySQLLexerMode{})
	if code != "" {
		t.Errorf("expected allowed, got: %s", code)
	}
}

func TestEvaluateSQL_PG_Denied_DML(t *testing.T) {
	decision, code := EvaluateSQL(EnginePostgreSQL, "DELETE FROM t", sqlpolicy.MySQLLexerMode{})
	if code == "" {
		t.Fatal("expected denied")
	}
	if decision.Allowed {
		t.Error("should be denied")
	}
	if decision.ReasonCode != sqlpolicy.ReasonNotAllowed {
		t.Errorf("got %s", decision.ReasonCode)
	}
}

func TestEvaluateSQL_PG_MultiStatement(t *testing.T) {
	_, code := EvaluateSQL(EnginePostgreSQL, "SELECT 1; DROP TABLE t", sqlpolicy.MySQLLexerMode{})
	if string(code) != string(sqlpolicy.ReasonMultipleStatements) {
		t.Errorf("got %s", code)
	}
}

func TestEvaluateSQL_PG_ParseError(t *testing.T) {
	_, code := EvaluateSQL(EnginePostgreSQL, "SELEC * FORM t", sqlpolicy.MySQLLexerMode{})
	if string(code) != string(sqlpolicy.ReasonParseError) {
		t.Errorf("got %s", code)
	}
}

func TestEvaluateSQL_MySQL_Allowed(t *testing.T) {
	_, code := EvaluateSQL(EngineMySQL, "SELECT * FROM t", sqlpolicy.MySQLLexerMode{})
	if code != "" {
		t.Errorf("got %s", code)
	}
}

func TestEvaluateSQL_MySQL_ECM(t *testing.T) {
	decision, code := EvaluateSQL(EngineMySQL, "/*!50000 DROP TABLE t*/ SELECT 1", sqlpolicy.MySQLLexerMode{})
	if code == "" {
		t.Fatal("expected denied for ECM")
	}
	if decision.ReasonCode != sqlpolicy.ReasonECMDetected {
		t.Errorf("got %s", decision.ReasonCode)
	}
}

func TestEvaluateSQL_MySQL_Denied_DML(t *testing.T) {
	_, code := EvaluateSQL(EngineMySQL, "INSERT INTO t VALUES(1)", sqlpolicy.MySQLLexerMode{})
	if string(code) != string(sqlpolicy.ReasonNotAllowed) {
		t.Errorf("got %s", code)
	}
}

func TestEvaluateSQL_Empty(t *testing.T) {
	_, code := EvaluateSQL(EnginePostgreSQL, "", sqlpolicy.MySQLLexerMode{})
	if string(code) != string(sqlpolicy.ReasonEmptySQL) {
		t.Errorf("got %s", code)
	}
}

func TestEvaluateSQL_ECM_OmniParserZero(t *testing.T) {
	decision, code := EvaluateSQL(EngineMySQL, "/*!50000 DROP TABLE t*/ SELECT 1", sqlpolicy.MySQLLexerMode{})
	if string(code) != string(sqlpolicy.ReasonECMDetected) {
		t.Errorf("ECM should be detected before parser, got code=%s", code)
	}
	if decision.ReasonCode != sqlpolicy.ReasonECMDetected {
		t.Errorf("got %s", decision.ReasonCode)
	}
}

// [CR #1] 未知引擎必须拒绝
func TestEvaluateSQL_UnknownEngine(t *testing.T) {
	_, code := EvaluateSQL(Engine("sqlite"), "SELECT 1", sqlpolicy.MySQLLexerMode{})
	if string(code) != string(ErrUnsupportedEngine) {
		t.Errorf("expected unsupported_engine, got %s", code)
	}
}

// [CR round2] 非默认 mode 下保守拒绝（Omni 不支持 mode-aware 解析）
func TestEvaluateSQL_MySQL_NoBackslashEscapes_Rejected(t *testing.T) {
	sql := "SELECT 'test\\' /*!50000' FROM t"
	mode := sqlpolicy.MySQLLexerMode{NoBackslashEscapes: true}
	decision, _ := EvaluateSQL(EngineMySQL, sql, mode)
	// 非默认 mode → Omni parser 语义不一致 → fail-closed 拒绝
	if decision.ReasonCode != sqlpolicy.ReasonParseError {
		t.Errorf("expected ReasonParseError for NoBackslashEscapes, got %s", decision.ReasonCode)
	}
}

func TestEvaluateSQL_MySQL_ANSIQuotes_Rejected(t *testing.T) {
	decision, _ := EvaluateSQL(EngineMySQL, "SELECT 1", sqlpolicy.MySQLLexerMode{ANSIQuotes: true})
	if decision.ReasonCode != sqlpolicy.ReasonParseError {
		t.Errorf("expected ReasonParseError for ANSI_QUOTES, got %s", decision.ReasonCode)
	}
}

func TestMapDialect_Unknown(t *testing.T) {
	_, err := mapDialect(Engine("unknown"))
	if err != ErrUnsupportedEngine {
		t.Errorf("expected ErrUnsupportedEngine, got %v", err)
	}
}

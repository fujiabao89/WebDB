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

// [Qodo #1] ECM lexer 应按 mode 正确识别
func TestEvaluateSQL_MySQL_ECM_NoBackslashEscapes(t *testing.T) {
	sql := "SELECT 'test\\' /*!50000' FROM t"
	mode := sqlpolicy.MySQLLexerMode{NoBackslashEscapes: true}
	decision, _ := EvaluateSQL(EngineMySQL, sql, mode)
	if decision.ReasonCode != sqlpolicy.ReasonECMDetected {
		t.Errorf("expected ECM detected with NoBackslashEscapes, got reason=%s", decision.ReasonCode)
	}
}

func TestMapDialect_Unknown(t *testing.T) {
	_, err := mapDialect(Engine("unknown"))
	if err != ErrUnsupportedEngine {
		t.Errorf("expected ErrUnsupportedEngine, got %v", err)
	}
}

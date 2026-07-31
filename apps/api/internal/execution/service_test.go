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

// [CR #17] 非默认 mode 下保守拒绝 — 表驱动负例，完整断言 Allowed/ReasonCode/StableErrorCode
func TestEvaluateSQL_MySQL_NonDefaultMode_Rejected(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		mode     sqlpolicy.MySQLLexerMode
		wantCode sqlpolicy.StableReasonCode
	}{
		{
			name:     "NoBackslashEscapes",
			sql:      "SELECT 'test\\' /*!50000' FROM t",
			mode:     sqlpolicy.MySQLLexerMode{NoBackslashEscapes: true},
			wantCode: sqlpolicy.ReasonParseError,
		},
		{
			name:     "ANSIQuotes",
			sql:      "SELECT 1",
			mode:     sqlpolicy.MySQLLexerMode{ANSIQuotes: true},
			wantCode: sqlpolicy.ReasonParseError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision, stableCode := EvaluateSQL(EngineMySQL, tt.sql, tt.mode)
			if decision.Allowed {
				t.Errorf("decision.Allowed should be false")
			}
			if decision.ReasonCode != tt.wantCode {
				t.Errorf("decision.ReasonCode = %s, want %s", decision.ReasonCode, tt.wantCode)
			}
			if stableCode != StableErrorCode(tt.wantCode) {
				t.Errorf("stableErrorCode = %s, want %s", stableCode, tt.wantCode)
			}
		})
	}
}

func TestMapDialect_Unknown(t *testing.T) {
	_, err := mapDialect(Engine("unknown"))
	if err != ErrUnsupportedEngine {
		t.Errorf("expected ErrUnsupportedEngine, got %v", err)
	}
}

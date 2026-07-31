package execution

import (
	"testing"

	"github.com/fujiabao89/webdb/internal/sqlpolicy"
)

func TestEvaluateSQL_PG_Allowed(t *testing.T) {
	_, code := EvaluateSQL(EnginePostgreSQL, "SELECT * FROM t")
	if code != "" {
		t.Errorf("expected allowed, got: %s", code)
	}
}

func TestEvaluateSQL_PG_Denied_DML(t *testing.T) {
	decision, code := EvaluateSQL(EnginePostgreSQL, "DELETE FROM t")
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
	_, code := EvaluateSQL(EnginePostgreSQL, "SELECT 1; DROP TABLE t")
	if string(code) != string(sqlpolicy.ReasonMultipleStatements) {
		t.Errorf("got %s", code)
	}
}

func TestEvaluateSQL_PG_ParseError(t *testing.T) {
	_, code := EvaluateSQL(EnginePostgreSQL, "SELEC * FORM t")
	if string(code) != string(sqlpolicy.ReasonParseError) {
		t.Errorf("got %s", code)
	}
}

func TestEvaluateSQL_MySQL_Allowed(t *testing.T) {
	_, code := EvaluateSQL(EngineMySQL, "SELECT * FROM t")
	if code != "" {
		t.Errorf("got %s", code)
	}
}

func TestEvaluateSQL_MySQL_ECM(t *testing.T) {
	decision, code := EvaluateSQL(EngineMySQL, "/*!50000 DROP TABLE t*/ SELECT 1")
	if code == "" {
		t.Fatal("expected denied for ECM")
	}
	if decision.ReasonCode != sqlpolicy.ReasonECMDetected {
		t.Errorf("got %s", decision.ReasonCode)
	}
}

func TestEvaluateSQL_MySQL_Denied_DML(t *testing.T) {
	_, code := EvaluateSQL(EngineMySQL, "INSERT INTO t VALUES(1)")
	if string(code) != string(sqlpolicy.ReasonNotAllowed) {
		t.Errorf("got %s", code)
	}
}

func TestEvaluateSQL_Empty(t *testing.T) {
	_, code := EvaluateSQL(EnginePostgreSQL, "")
	if string(code) != string(sqlpolicy.ReasonEmptySQL) {
		t.Errorf("got %s", code)
	}
}

func TestEvaluateSQL_ECM_OmniParserZero(t *testing.T) {
	decision, code := EvaluateSQL(EngineMySQL, "/*!50000 DROP TABLE t*/ SELECT 1")
	if string(code) != string(sqlpolicy.ReasonECMDetected) {
		t.Errorf("ECM should be detected before parser, got code=%s", code)
	}
	if decision.ReasonCode != sqlpolicy.ReasonECMDetected {
		t.Errorf("got %s", decision.ReasonCode)
	}
}

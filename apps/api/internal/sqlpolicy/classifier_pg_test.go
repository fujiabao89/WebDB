package sqlpolicy

import "testing"

// TestClassifyPG PostgreSQL 分类测试（提案 §9.1，47 条）。
func TestClassifyPG(t *testing.T) {
	tests := []struct {
		id       string
		sql      string
		wantKind StatementKind
		wantDeny bool
		features ASTFeatures
	}{
		{id: "PG-01", sql: "SELECT * FROM t", wantKind: StmtSelect},
		{id: "PG-02", sql: "SELECT $1, $2 FROM t WHERE c=$3", wantKind: StmtSelect},
		{id: "PG-03", sql: "select * from t", wantKind: StmtSelect},
		{id: "PG-04", sql: "-- comment\nSELECT * FROM t", wantKind: StmtSelect},
		{id: "PG-05", sql: "/* block */ SELECT * FROM t", wantKind: StmtSelect},
		{id: "PG-06", sql: "SELECT 'DROP TABLE t;' FROM t", wantKind: StmtSelect},
		{id: "PG-07", sql: "WITH cte AS (SELECT * FROM t) SELECT * FROM cte", wantKind: StmtSelect, features: ASTFeatures{HasCTE: true}},
		{id: "PG-08", sql: "SELECT 1 UNION SELECT 2", wantKind: StmtSelect, features: ASTFeatures{HasSetOperation: true}},
		{id: "PG-09", sql: "EXPLAIN SELECT * FROM t", wantKind: StmtExplain},
		{id: "PG-10", sql: "EXPLAIN (FORMAT JSON) SELECT * FROM t", wantKind: StmtExplain},
		{id: "PG-11", sql: "SELECT * FROM t FOR UPDATE", wantKind: StmtSelect, wantDeny: true, features: ASTFeatures{HasLockingClause: true}},
		{id: "PG-12", sql: "SELECT * FROM t FOR SHARE", wantKind: StmtSelect, wantDeny: true, features: ASTFeatures{HasLockingClause: true}},
		{id: "PG-13", sql: "SELECT * FROM t FOR KEY SHARE", wantKind: StmtSelect, wantDeny: true, features: ASTFeatures{HasLockingClause: true}},
		{id: "PG-14", sql: "SELECT * FROM t FOR NO KEY UPDATE", wantKind: StmtSelect, wantDeny: true, features: ASTFeatures{HasLockingClause: true}},
		{id: "PG-15", sql: "SELECT * INTO new_t FROM t", wantKind: StmtSelect, wantDeny: true, features: ASTFeatures{HasSelectInto: true}},
		{id: "PG-16", sql: "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", wantKind: StmtSelect, wantDeny: true, features: ASTFeatures{HasCTE: true, HasModifyingCTE: true}},
		{id: "PG-17", sql: "WITH d AS (INSERT INTO t VALUES(1)) SELECT * FROM d", wantKind: StmtSelect, wantDeny: true, features: ASTFeatures{HasCTE: true, HasModifyingCTE: true}},
		{id: "PG-18", sql: "TABLE t", wantKind: StmtSelect},
		{id: "PG-19", sql: "VALUES (1,2,3)", wantKind: StmtSelect},
		{id: "PG-20", sql: "SELECT 1; DROP TABLE t", wantKind: StmtUnknown, wantDeny: true},
		{id: "PG-21", sql: "SELECT 1; --\nDROP TABLE t", wantKind: StmtUnknown, wantDeny: true},
		{id: "PG-22", sql: "INSERT INTO t VALUES(1)", wantKind: StmtInsert, wantDeny: true},
		{id: "PG-23", sql: "UPDATE t SET c=1", wantKind: StmtUpdate, wantDeny: true},
		{id: "PG-24", sql: "DELETE FROM t", wantKind: StmtDelete, wantDeny: true},
		{id: "PG-25", sql: "CREATE TABLE t (id int)", wantKind: StmtDDL, wantDeny: true},
		{id: "PG-26", sql: "ALTER TABLE t ADD c int", wantKind: StmtDDL, wantDeny: true},
		{id: "PG-27", sql: "DROP TABLE t", wantKind: StmtDDL, wantDeny: true},
		{id: "PG-28", sql: "TRUNCATE t", wantKind: StmtDDL, wantDeny: true},
		{id: "PG-29", sql: "CALL my_proc()", wantKind: StmtCall, wantDeny: true},
		{id: "PG-30", sql: "DO $$ BEGIN END; $$", wantKind: StmtCall, wantDeny: true},
		{id: "PG-31", sql: "COPY t FROM '/tmp/data'", wantKind: StmtOther, wantDeny: true},
		{id: "PG-32", sql: "SET work_mem = '1GB'", wantKind: StmtOther, wantDeny: true},
		{id: "PG-33", sql: "PREPARE p AS SELECT * FROM t", wantKind: StmtOther, wantDeny: true},
		{id: "PG-34", sql: "EXECUTE p", wantKind: StmtOther, wantDeny: true},
		{id: "PG-35", sql: "BEGIN", wantKind: StmtTransaction, wantDeny: true},
		{id: "PG-36", sql: "GRANT SELECT ON t TO u", wantKind: StmtDDL, wantDeny: true},
		{id: "PG-37", sql: "VACUUM t", wantKind: StmtOther, wantDeny: true},
		{id: "PG-38", sql: "EXPLAIN ANALYZE SELECT * FROM t", wantKind: StmtExplain, wantDeny: true, features: ASTFeatures{HasExplainAnalyze: true}},
		{id: "PG-39", sql: "EXPLAIN DELETE FROM t", wantKind: StmtExplain, wantDeny: true, features: ASTFeatures{HasExplainDMLDDL: true}},
		{id: "PG-40", sql: "EXPLAIN EXPLAIN SELECT * FROM t", wantKind: StmtUnknown, wantDeny: true}, // PG 不支持嵌套 EXPLAIN，parse error → fail-closed
		{id: "PG-41", sql: "", wantKind: StmtUnknown, wantDeny: true},
		{id: "PG-42", sql: "-- only comment", wantKind: StmtUnknown, wantDeny: true},
		{id: "PG-43", sql: "SELEC * FORM t", wantKind: StmtUnknown, wantDeny: true},
		{id: "PG-45", sql: "TABLE ONLY t", wantKind: StmtSelect},
		{id: "PG-46", sql: "TABLE t ORDER BY id LIMIT 10", wantKind: StmtSelect},
		{id: "PG-47", sql: "TABLE t FOR UPDATE", wantKind: StmtSelect, wantDeny: true, features: ASTFeatures{HasLockingClause: true}},
		{id: "PG-48", sql: "WITH d AS (DELETE FROM t RETURNING *) TABLE d", wantKind: StmtSelect, wantDeny: true, features: ASTFeatures{HasCTE: true, HasModifyingCTE: true}},
		{id: "PG-49", sql: "TABLE t; DELETE FROM t", wantKind: StmtUnknown, wantDeny: true},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			result := classifyPG(tt.sql)

			if result.StatementKind != tt.wantKind {
				t.Errorf("StatementKind: got %q, want %q", result.StatementKind, tt.wantKind)
			}

			if tt.wantDeny {
				if result.ParseError != nil {
					return
				}
				if result.StatementKind == StmtUnknown {
					return
				}
			}

			checkFeatures(t, result.ASTFeatures, tt.features)
		})
	}
}

func checkFeatures(t *testing.T, got, want ASTFeatures) {
	t.Helper()
	if want.HasCTE && !got.HasCTE {
		t.Error("expected HasCTE=true")
	}
	if want.HasRecursiveCTE && !got.HasRecursiveCTE {
		t.Error("expected HasRecursiveCTE=true")
	}
	if want.HasSetOperation && !got.HasSetOperation {
		t.Error("expected HasSetOperation=true")
	}
	if want.HasLockingClause && !got.HasLockingClause {
		t.Error("expected HasLockingClause=true")
	}
	if want.HasSelectInto && !got.HasSelectInto {
		t.Error("expected HasSelectInto=true")
	}
	if want.HasModifyingCTE && !got.HasModifyingCTE {
		t.Error("expected HasModifyingCTE=true")
	}
	if want.HasExplainAnalyze && !got.HasExplainAnalyze {
		t.Error("expected HasExplainAnalyze=true")
	}
	if want.HasExplainDMLDDL && !got.HasExplainDMLDDL {
		t.Error("expected HasExplainDMLDDL=true")
	}
	if want.HasNestedExplain && !got.HasNestedExplain {
		t.Error("expected HasNestedExplain=true")
	}
	if want.HasIntoOutfile && !got.HasIntoOutfile {
		t.Error("expected HasIntoOutfile=true")
	}
	if want.HasIntoVar && !got.HasIntoVar {
		t.Error("expected HasIntoVar=true")
	}
	if want.HasAssignment && !got.HasAssignment {
		t.Error("expected HasAssignment=true")
	}
}

package sqlpolicy

import "testing"

func TestClassifyPG(t *testing.T) {
	tests := []struct {
		id       string
		sql      string
		wantKind StatementKind
		wantDeny bool
		wantCode StableReasonCode
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
		{id: "PG-11", sql: "SELECT * FROM t FOR UPDATE", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasLockingClause: true}},
		{id: "PG-12", sql: "SELECT * FROM t FOR SHARE", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasLockingClause: true}},
		{id: "PG-13", sql: "SELECT * FROM t FOR KEY SHARE", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasLockingClause: true}},
		{id: "PG-14", sql: "SELECT * FROM t FOR NO KEY UPDATE", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasLockingClause: true}},
		{id: "PG-15", sql: "SELECT * INTO new_t FROM t", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasSelectInto: true}},
		{id: "PG-16", sql: "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasCTE: true, HasModifyingCTE: true}},
		{id: "PG-17", sql: "WITH d AS (INSERT INTO t VALUES(1)) SELECT * FROM d", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasCTE: true, HasModifyingCTE: true}},
		{id: "PG-18", sql: "TABLE t", wantKind: StmtSelect},
		{id: "PG-19", sql: "VALUES (1,2,3)", wantKind: StmtSelect},
		{id: "PG-20", sql: "SELECT 1; DROP TABLE t", wantDeny: true, wantCode: ReasonMultipleStatements},
		{id: "PG-21", sql: "SELECT 1; --\nDROP TABLE t", wantDeny: true, wantCode: ReasonMultipleStatements},
		{id: "PG-22", sql: "INSERT INTO t VALUES(1)", wantKind: StmtInsert, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "PG-23", sql: "UPDATE t SET c=1", wantKind: StmtUpdate, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "PG-24", sql: "DELETE FROM t", wantKind: StmtDelete, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "PG-25", sql: "CREATE TABLE t (id int)", wantKind: StmtDDL, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "PG-26", sql: "ALTER TABLE t ADD c int", wantKind: StmtDDL, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "PG-27", sql: "DROP TABLE t", wantKind: StmtDDL, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "PG-28", sql: "TRUNCATE t", wantKind: StmtDDL, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "PG-29", sql: "CALL my_proc()", wantKind: StmtCall, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "PG-30", sql: "DO $$ BEGIN END; $$", wantKind: StmtCall, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "PG-31", sql: "COPY t FROM '/tmp/data'", wantKind: StmtOther, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "PG-32", sql: "SET work_mem = '1GB'", wantKind: StmtOther, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "PG-33", sql: "PREPARE p AS SELECT * FROM t", wantKind: StmtOther, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "PG-34", sql: "EXECUTE p", wantKind: StmtOther, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "PG-35", sql: "BEGIN", wantKind: StmtTransaction, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "PG-36", sql: "GRANT SELECT ON t TO u", wantKind: StmtDDL, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "PG-37", sql: "VACUUM t", wantKind: StmtOther, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "PG-38", sql: "EXPLAIN ANALYZE SELECT * FROM t", wantKind: StmtExplain, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasExplainAnalyze: true}},
		{id: "PG-39", sql: "EXPLAIN DELETE FROM t", wantKind: StmtExplain, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasExplainDMLDDL: true}},
		{id: "PG-40", sql: "EXPLAIN EXPLAIN SELECT * FROM t", wantDeny: true, wantCode: ReasonParseError},
		{id: "PG-41", sql: "", wantDeny: true, wantCode: ReasonEmptySQL},
		{id: "PG-42", sql: "-- only comment", wantDeny: true, wantCode: ReasonParseError},
		{id: "PG-43", sql: "SELEC * FORM t", wantDeny: true, wantCode: ReasonParseError},
		{id: "PG-45", sql: "TABLE ONLY t", wantKind: StmtSelect},
		{id: "PG-46", sql: "TABLE t ORDER BY id LIMIT 10", wantKind: StmtSelect},
		{id: "PG-47", sql: "TABLE t FOR UPDATE", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasLockingClause: true}},
		{id: "PG-48", sql: "WITH d AS (DELETE FROM t RETURNING *) TABLE d", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasCTE: true, HasModifyingCTE: true}},
		{id: "PG-49", sql: "TABLE t; DELETE FROM t", wantDeny: true, wantCode: ReasonMultipleStatements},
		// [CR #6] 危险函数检测
		{id: "PG-50", sql: "SELECT setval('s', 1)", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasDangerousFunc: true}},
		{id: "PG-51", sql: "SELECT lo_create(0)", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasDangerousFunc: true}},
		{id: "PG-52", sql: "SELECT nextval('s')", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasDangerousFunc: true}},
		// [CR #20] schema 限定危险函数（确保 fcBaseName 只取最后一段）
		{id: "PG-53", sql: "SELECT pg_catalog.nextval('s')", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasDangerousFunc: true}},
		{id: "PG-54", sql: "SELECT pg_catalog.lo_unlink(1)", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasDangerousFunc: true}},
		// [CR #23] 补充大对象写函数
		{id: "PG-55", sql: "SELECT lo_creat(0)", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasDangerousFunc: true}},
		{id: "PG-56", sql: "SELECT lo_from_bytea(0, '')", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasDangerousFunc: true}},
		{id: "PG-57", sql: "SELECT lowrite(0, '')", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasDangerousFunc: true}},
		{id: "PG-58", sql: "SELECT lo_truncate(0, 0)", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasDangerousFunc: true}},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			result := classifyPG(tt.sql)
			if tt.wantKind != "" && result.StatementKind != tt.wantKind {
				t.Errorf("StatementKind: got %q, want %q", result.StatementKind, tt.wantKind)
			}
			// [CR #3] wantDeny 通过 Decide() 验证 Allowed 和 ReasonCode
			if tt.wantDeny {
				dec := Decide(DialectPostgreSQL, tt.sql, MySQLLexerMode{})
				if dec.Allowed {
					t.Errorf("%s: Decide() should deny but allowed", tt.id)
				}
				if tt.wantCode != "" && dec.ReasonCode != tt.wantCode {
					t.Errorf("%s: ReasonCode got %q want %q", tt.id, dec.ReasonCode, tt.wantCode)
				}
			}
			// [CR #3] 双向特征比较
			checkFeaturesBidi(t, result.ASTFeatures, tt.features)
		})
	}
}

func checkFeaturesBidi(t *testing.T, got, want ASTFeatures) {
	t.Helper()
	checks := []struct {
		name      string
		got, want bool
	}{
		{"HasCTE", got.HasCTE, want.HasCTE},
		{"HasRecursiveCTE", got.HasRecursiveCTE, want.HasRecursiveCTE},
		{"HasSetOperation", got.HasSetOperation, want.HasSetOperation},
		{"HasLockingClause", got.HasLockingClause, want.HasLockingClause},
		{"HasSelectInto", got.HasSelectInto, want.HasSelectInto},
		{"HasIntoOutfile", got.HasIntoOutfile, want.HasIntoOutfile},
		{"HasIntoVar", got.HasIntoVar, want.HasIntoVar},
		{"HasAssignment", got.HasAssignment, want.HasAssignment},
		{"HasModifyingCTE", got.HasModifyingCTE, want.HasModifyingCTE},
		{"HasExplainAnalyze", got.HasExplainAnalyze, want.HasExplainAnalyze},
		{"HasExplainDMLDDL", got.HasExplainDMLDDL, want.HasExplainDMLDDL},
		{"HasNestedExplain", got.HasNestedExplain, want.HasNestedExplain},
		{"HasDangerousFunc", got.HasDangerousFunc, want.HasDangerousFunc},
	}
	for _, c := range checks {
		if c.got && !c.want {
			t.Errorf("unexpected %s=true", c.name)
		}
		if c.want && !c.got {
			t.Errorf("expected %s=true", c.name)
		}
	}
}

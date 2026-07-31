package sqlpolicy

import "testing"

func TestClassifyMySQL(t *testing.T) {
	tests := []struct {
		id       string
		sql      string
		wantKind StatementKind
		wantDeny bool
		wantCode StableReasonCode
		features ASTFeatures
	}{
		{id: "MY-01", sql: "SELECT * FROM t", wantKind: StmtSelect},
		{id: "MY-02", sql: "SELECT 1 FROM t WHERE c = 1", wantKind: StmtSelect},
		{id: "MY-03", sql: "SELECT * FROM t FOR UPDATE", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasLockingClause: true}},
		{id: "MY-04", sql: "SELECT * FROM t LOCK IN SHARE MODE", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasLockingClause: true}},
		{id: "MY-05", sql: "SELECT * INTO OUTFILE '/tmp/d' FROM t", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasIntoOutfile: true}},
		{id: "MY-06", sql: "SELECT * INTO DUMPFILE '/tmp/d' FROM t", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasIntoOutfile: true}},
		{id: "MY-07", sql: "SELECT id INTO @var FROM t", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasIntoVar: true}},
		{id: "MY-08", sql: "SELECT @x := id FROM t", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasAssignment: true}},
		{id: "MY-11", sql: "SELECT '/*!50000 DROP TABLE t*/' AS txt", wantKind: StmtSelect},
		{id: "MY-12", sql: "SELECT * FROM t /* comment */ WHERE id=1", wantKind: StmtSelect},
		{id: "MY-14", sql: "WITH cte AS (SELECT * FROM t) SELECT * FROM cte", wantKind: StmtSelect, features: ASTFeatures{HasCTE: true}},
		{id: "MY-15", sql: "WITH d AS (DELETE FROM t) SELECT * FROM d", wantDeny: true, wantCode: ReasonParseError},
		{id: "MY-16", sql: "SELECT 1; DROP TABLE t", wantDeny: true, wantCode: ReasonMultipleStatements},
		{id: "MY-17", sql: "INSERT INTO t VALUES(1)", wantKind: StmtInsert, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-18", sql: "LOAD DATA INFILE '/tmp/d' INTO TABLE t", wantKind: StmtOther, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-19", sql: "HANDLER t OPEN", wantKind: StmtOther, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-20", sql: "DO SLEEP(1)", wantKind: StmtCall, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-21", sql: "LOCK TABLES t READ", wantKind: StmtOther, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-22", sql: "SET @x = 1", wantKind: StmtOther, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-23", sql: "EXPLAIN DELETE FROM t", wantKind: StmtExplain, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasExplainDMLDDL: true}},
		{id: "MY-24", sql: "CALL my_proc()", wantKind: StmtCall, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-25", sql: "CREATE TABLE t (id INT)", wantKind: StmtDDL, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-26", sql: "DROP TABLE t", wantKind: StmtDDL, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-27", sql: "ALTER TABLE t ADD c INT", wantKind: StmtDDL, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-28", sql: "TRUNCATE TABLE t", wantKind: StmtDDL, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-29", sql: "RENAME TABLE t TO t2", wantKind: StmtDDL, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-30", sql: "BEGIN", wantKind: StmtTransaction, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-31", sql: "COMMIT", wantKind: StmtTransaction, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-32", sql: "ROLLBACK", wantKind: StmtTransaction, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-33", sql: "PREPARE stmt FROM 'SELECT 1'", wantKind: StmtOther, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-34", sql: "EXECUTE stmt", wantKind: StmtOther, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-35", sql: "DEALLOCATE PREPARE stmt", wantKind: StmtOther, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-36", sql: "GRANT SELECT ON t TO u", wantKind: StmtDDL, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-37", sql: "REVOKE SELECT ON t FROM u", wantKind: StmtDDL, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-38", sql: "SHOW TABLES", wantKind: StmtOther, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-39", sql: "USE test_db", wantKind: StmtOther, wantDeny: true, wantCode: ReasonNotAllowed},
		{id: "MY-40", sql: "EXPLAIN SELECT * FROM t", wantKind: StmtExplain},
		{id: "MY-41", sql: "EXPLAIN FORMAT=JSON SELECT * FROM t", wantKind: StmtExplain},
		{id: "MY-42", sql: "TABLE t", wantKind: StmtSelect},
		{id: "MY-43", sql: "", wantDeny: true, wantCode: ReasonEmptySQL},
		// [CR round2] WHERE 子句中的赋值检测
		{id: "MY-44", sql: "SELECT * FROM t WHERE (@x := id) > 0", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasAssignment: true}},
		// [CR round2] CTE 内部的赋值检测
		{id: "MY-45", sql: "WITH cte AS (SELECT @x := 1 FROM t) SELECT * FROM cte", wantKind: StmtSelect, wantDeny: true, wantCode: ReasonNotAllowed, features: ASTFeatures{HasCTE: true, HasAssignment: true}},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			result := classifyMySQL(tt.sql)
			if tt.wantKind != "" && result.StatementKind != tt.wantKind {
				t.Errorf("StatementKind: got %q, want %q", result.StatementKind, tt.wantKind)
			}
			// [CR #4] wantDeny 通过 Decide() 验证 Allowed + ReasonCode
			if tt.wantDeny {
				dec := Decide(DialectMySQL, tt.sql, MySQLLexerMode{})
				if dec.Allowed {
					t.Errorf("%s: Decide() should deny but allowed", tt.id)
				}
				if tt.wantCode != "" && dec.ReasonCode != tt.wantCode {
					t.Errorf("%s: ReasonCode got %q want %q", tt.id, dec.ReasonCode, tt.wantCode)
				}
			}
			checkFeaturesBidi(t, result.ASTFeatures, tt.features)
		})
	}
}

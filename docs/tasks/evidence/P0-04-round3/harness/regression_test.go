package main

import (
	"fmt"
	"testing"

	myast "github.com/bytebase/omni/mysql/ast"
	myparser "github.com/bytebase/omni/mysql/parser"
	"github.com/bytebase/omni/pg"
	pgast "github.com/bytebase/omni/pg/ast"
)

type pgTC struct {
	id, sql, expectKind string
	require, forbid     map[string]bool
}

func pgClassify(stmt pg.Statement) (string, map[string]bool) {
	f := make(map[string]bool)
	if stmt.Empty() || stmt.AST == nil { return "EMPTY", f }
	switch x := stmt.AST.(type) {
	case *pgast.SelectStmt:
		if x.IntoClause != nil { f["select_into"] = true }
		if x.LockingClause.Len() > 0 { f["locking"] = true }
		if x.WithClause != nil { f["has_cte"] = true; for _, it := range x.WithClause.Ctes.Items { if c, ok := it.(*pgast.CommonTableExpr); ok { switch c.Ctequery.(type) { case *pgast.InsertStmt, *pgast.UpdateStmt, *pgast.DeleteStmt: f["dml_cte"] = true } } } }
		return "SELECT", f
	case *pgast.InsertStmt: f["dml"] = true; return "DML", f
	case *pgast.UpdateStmt: f["dml"] = true; return "DML", f
	case *pgast.DeleteStmt: f["dml"] = true; return "DML", f
	case *pgast.ExplainStmt:
		f["explain"] = true
		if x.Options != nil { for _, opt := range x.Options.Items { if d, ok := opt.(*pgast.DefElem); ok && d.Defname == "analyze" { f["explain_analyze"] = true } } }
		switch q := x.Query.(type) {
		case *pgast.SelectStmt: f["explain_select"] = true; return "EXPLAIN", f
		case *pgast.InsertStmt, *pgast.UpdateStmt, *pgast.DeleteStmt: f["explain_dml"] = true; f["explain_target_detectable"] = true; return "EXPLAIN", f
		case *pgast.ExplainStmt: f["nested_explain"] = true; return "EXPLAIN", f
		default: if q != nil { f["explain_target_detectable"] = true; f["explain_ddl"] = true }; return "EXPLAIN", f
		}
	case *pgast.CreateStmt, *pgast.CreateTableAsStmt, *pgast.CreateSchemaStmt: f["ddl"] = true; return "DDL", f
	case *pgast.DropStmt: f["ddl"] = true; return "DDL", f
	case *pgast.AlterTableStmt: f["ddl"] = true; return "DDL", f
	case *pgast.TruncateStmt: f["ddl"] = true; return "DDL", f
	case *pgast.VariableSetStmt, *pgast.VariableShowStmt: return "SET", f
	case *pgast.PrepareStmt, *pgast.ExecuteStmt, *pgast.DeallocateStmt: return "OTHER", f
	default: f["unknown_node"] = true; return fmt.Sprintf("UNKNOWN(%T)", x), f
	}
}

func runPGTest(t *testing.T, c pgTC) bool {
	segs := pg.Split(c.sql)
	if len(segs) > 1 {
		if c.expectKind != "MULTI" { t.Errorf("%s: multi stmt via Split", c.id); return false }
		return true
	}
	stmts, err := pg.Parse(c.sql)
	if err != nil {
		if c.expectKind == "PARSE_ERROR" { return true }
		t.Errorf("%s: parse error: %v", c.id, err); return false
	}
	if len(stmts) == 0 { t.Errorf("%s: empty", c.id); return false }
	if len(stmts) > 1 {
		if c.expectKind == "MULTI" { return true }
		t.Errorf("%s: multi stmt via Parse", c.id); return false
	}
	k, fe := pgClassify(stmts[0])
	ok := k == c.expectKind
	if c.require != nil { for rk := range c.require { if !fe[rk] { ok = false; t.Errorf("%s: MISSING %s (feats=%v)", c.id, rk, fe) } } }
	if c.forbid != nil { for fk := range c.forbid { if fe[fk] { ok = false; t.Errorf("%s: FORBIDDEN %s (feats=%v)", c.id, fk, fe) } } }
	if !ok && k == c.expectKind { t.Errorf("%s: feat mismatch (require=%v forbid=%v got=%v)", c.id, c.require, c.forbid, fe) }
	if k != c.expectKind { t.Errorf("%s: kind mismatch: %s != %s (feats=%v)", c.id, k, c.expectKind, fe) }
	return ok
}

func TestPGBaseOra(t *testing.T) {
	cases := []pgTC{
		{"B01","SELECT * FROM t","SELECT",nil,nil},{"B02","SELECT 1","SELECT",nil,nil},
		{"B03","SELECT * FROM t FOR UPDATE","SELECT",map[string]bool{"locking":true},nil},
		{"B04","SELECT * FROM t FOR SHARE","SELECT",map[string]bool{"locking":true},nil},
		{"B05","SELECT * FROM t FOR KEY SHARE","SELECT",map[string]bool{"locking":true},nil},
		{"B06","SELECT * FROM t FOR NO KEY UPDATE","SELECT",map[string]bool{"locking":true},nil},
		{"B07","SELECT * INTO new_t FROM t","SELECT",map[string]bool{"select_into":true},nil},
		{"B08","WITH cte AS (SELECT 1) SELECT * FROM cte","SELECT",map[string]bool{"has_cte":true},map[string]bool{"dml_cte":true}},
		{"B09","WITH cte AS (INSERT INTO t VALUES (1)) SELECT * FROM cte","SELECT",map[string]bool{"has_cte":true,"dml_cte":true},nil},
		{"B10","WITH cte AS (UPDATE t SET x=1) SELECT * FROM cte","SELECT",map[string]bool{"has_cte":true,"dml_cte":true},nil},
		{"B11","WITH cte AS (DELETE FROM t) SELECT * FROM cte","SELECT",map[string]bool{"has_cte":true,"dml_cte":true},nil},
		{"B12","INSERT INTO t VALUES (1)","DML",map[string]bool{"dml":true},nil},
		{"B13","UPDATE t SET x=1","DML",map[string]bool{"dml":true},nil},
		{"B14","DELETE FROM t","DML",map[string]bool{"dml":true},nil},
		{"B15","CREATE TABLE t (id int)","DDL",map[string]bool{"ddl":true},nil},
		{"B16","DROP TABLE t","DDL",map[string]bool{"ddl":true},nil},
		{"B17","ALTER TABLE t ADD COLUMN x int","DDL",map[string]bool{"ddl":true},nil},
		{"B18","TRUNCATE TABLE t","DDL",map[string]bool{"ddl":true},nil},
		{"B19","SET search_path TO public","SET",nil,nil},{"B20","SHOW search_path","SET",nil,nil},
		{"B21","PREPARE p AS SELECT 1","OTHER",nil,nil},{"B22","EXECUTE p","OTHER",nil,nil},{"B23","DEALLOCATE p","OTHER",nil,nil},
		{"B24","SELECT 1; SELECT 2","MULTI",nil,nil},{"B25","SELECT 1; DELETE FROM t","MULTI",nil,nil},
	}
	p, f := 0, 0
	for _, c := range cases { if runPGTest(t, c) { p++ } else { f++ } }
	if p != 25 || f != 0 { t.Errorf("PG base: %d/%d", p, p+f) }
	t.Logf("PG base: %d/25", p)
}

func TestPGExplainOra(t *testing.T) {
	cases := []pgTC{
		{"E01","EXPLAIN SELECT * FROM t","EXPLAIN",map[string]bool{"explain":true,"explain_select":true},map[string]bool{"explain_analyze":true}},
		{"E02","EXPLAIN ANALYZE SELECT * FROM t","EXPLAIN",map[string]bool{"explain":true,"explain_select":true,"explain_analyze":true},nil},
		{"E03","EXPLAIN INSERT INTO t VALUES (1)","EXPLAIN",map[string]bool{"explain_dml":true,"explain_target_detectable":true},nil},
		{"E04","EXPLAIN UPDATE t SET x=1","EXPLAIN",map[string]bool{"explain_dml":true,"explain_target_detectable":true},nil},
		{"E05","EXPLAIN DELETE FROM t","EXPLAIN",map[string]bool{"explain_dml":true,"explain_target_detectable":true},nil},
		{"E06","EXPLAIN CREATE TABLE t (id int)","EXPLAIN",map[string]bool{"explain_target_detectable":true,"explain_ddl":true},nil},
		{"E07","EXPLAIN EXPLAIN SELECT * FROM t","PARSE_ERROR",nil,nil},
	}
	p, f := 0, 0
	for _, c := range cases { if runPGTest(t, c) { p++ } else { f++ } }
	if p != 7 || f != 0 { t.Errorf("PG EXPLAIN: %d/%d", p, p+f) }
	t.Logf("PG EXPLAIN: %d/7", p)
}

func TestPGExplainAnalyzeDeny(t *testing.T) {
	for _, sql := range []string{
		"EXPLAIN ANALYZE SELECT * FROM t",
		"EXPLAIN (ANALYZE true) SELECT * FROM t",
	} {
		_, _, _, a := classifyPGFull(sql)
		if a { t.Errorf("PG EXPLAIN ANALYZE should be denied: %q", sql) }
	}
	for _, sql := range []string{ "EXPLAIN SELECT * FROM t" } {
		_, _, _, a := classifyPGFull(sql)
		if !a { t.Errorf("PG EXPLAIN SELECT should be allowed: %q", sql) }
	}
	for _, sql := range []string{
		"EXPLAIN INSERT INTO t VALUES (1)", "EXPLAIN UPDATE t SET x=1",
		"EXPLAIN DELETE FROM t", "EXPLAIN CREATE TABLE t (id int)",
		"EXPLAIN EXPLAIN SELECT * FROM t",
	} {
		_, _, _, a := classifyPGFull(sql)
		if a { t.Errorf("PG EXPLAIN dangerous should be denied: %q", sql) }
	}
	t.Log("PG EXPLAIN ANALYZE deny: OK")
}

func classifyPGFull(sql string) (string, map[string]bool, bool, bool) {
	f := make(map[string]bool)
	segs := pg.Split(sql)
	if len(segs) > 1 { return "MULTI", f, false, false }
	stmts, err := pg.Parse(sql)
	if err != nil { return "PARSE_ERROR", f, false, false }
	if len(stmts) == 0 { return "EMPTY", f, false, false }
	if len(stmts) > 1 { return "MULTI", f, false, false }
	if stmts[0].Empty() || stmts[0].AST == nil { return "EMPTY", f, false, false }
	k, fe := pgClassify(stmts[0])
	a := allowPG(k, fe)
	return k, fe, true, a
}

func allowPG(kind string, feats map[string]bool) bool {
	danger := []string{"locking", "select_into", "dml_cte", "dml", "ddl", "unknown_node",
		"explain_analyze", "explain_dml", "explain_ddl", "explain_non_select", "nested_explain"}
	for _, d := range danger { if feats[d] { return false } }
	switch kind {
	case "SELECT": return true
	case "EXPLAIN": return feats["explain_select"] && !feats["explain_analyze"]
	default: return false
	}
}

// ========== MySQL feature oracle ==========

type myTC struct {
	id, sql, expectKind string
	require, forbid     map[string]bool
}

func runMYTest(t *testing.T, c myTC) bool {
	nl, err := myparser.Parse(c.sql)
	if err != nil {
		if c.expectKind == "PARSE_ERROR" { return true }
		t.Errorf("%s: parse error: %v", c.id, err); return false
	}
	var nodes []myast.Node; if nl != nil { nodes = nl.Items }
	k, fe := classifyMY(nodes)
	ok := k == c.expectKind
	if c.require != nil { for rk := range c.require { if !fe[rk] { ok = false; t.Errorf("%s: MISSING %s (feats=%v)", c.id, rk, fe) } } }
	if c.forbid != nil { for fk := range c.forbid { if fe[fk] { ok = false; t.Errorf("%s: FORBIDDEN %s (feats=%v)", c.id, fk, fe) } } }
	if !ok && k == c.expectKind { t.Errorf("%s: feat mismatch (want %v forbid %v got %v)", c.id, c.require, c.forbid, fe) }
	if k != c.expectKind { t.Errorf("%s: kind mismatch: %s != %s (feats=%v)", c.id, k, c.expectKind, fe) }
	return ok
}

func TestMYBaseOra(t *testing.T) {
	cases := []myTC{
		{"B01","SELECT * FROM t","SELECT",nil,nil},{"B02","SELECT 1","SELECT",nil,nil},
		{"B03","SELECT * FROM t FOR UPDATE","SELECT",map[string]bool{"locking":true,"for_update":true},nil},
		{"B04","SELECT * FROM t LOCK IN SHARE MODE","SELECT",map[string]bool{"locking":true,"lock_in_share_mode":true},nil},
		{"B05","SELECT * FROM t FOR UPDATE NOWAIT","SELECT",map[string]bool{"locking":true,"for_update":true,"nowait":true},nil},
		{"B06","SELECT * FROM t FOR UPDATE SKIP LOCKED","SELECT",map[string]bool{"locking":true,"for_update":true,"skip_locked":true},nil},
		{"B07","SELECT * FROM t FOR SHARE","SELECT",map[string]bool{"locking":true,"for_share":true},nil},
		{"B08","SELECT * INTO OUTFILE '/tmp/t' FROM t","SELECT",map[string]bool{"into_outfile":true},nil},
		{"B09","SELECT * INTO DUMPFILE '/tmp/t' FROM t","SELECT",map[string]bool{"into_dumpfile":true},nil},
		{"B10","SELECT 1 INTO @var","SELECT",map[string]bool{"into_vars":true},nil},
		{"B11","SELECT @x := id FROM t","SELECT",map[string]bool{"user_var_assign":true},nil},
		{"B11b","SELECT (@x := id) + 1 FROM t","SELECT",map[string]bool{"user_var_assign":true},nil},
		{"B11c","SELECT @x, id FROM t","SELECT",nil,map[string]bool{"user_var_assign":true}},
		{"B12","INSERT INTO t VALUES (1)","DML",map[string]bool{"dml":true},nil},
		{"B13","UPDATE t SET x=1","DML",map[string]bool{"dml":true},nil},
		{"B14","DELETE FROM t","DML",map[string]bool{"dml":true},nil},
		{"B15","REPLACE INTO t VALUES (1)","DML",map[string]bool{"dml":true,"is_replace":true},nil},
		{"B16","CREATE TABLE t (id INT)","DDL",map[string]bool{"ddl":true},nil},
		{"B17","DROP TABLE t","DDL",map[string]bool{"ddl":true},nil},
		{"B18","ALTER TABLE t ADD COLUMN x INT","DDL",map[string]bool{"ddl":true},nil},
		{"B19","TRUNCATE TABLE t","DDL",map[string]bool{"ddl":true},nil},
		{"B20","EXPLAIN SELECT * FROM t","EXPLAIN",map[string]bool{"explain":true,"explain_select":true},map[string]bool{"explain_analyze":true}},
		{"B21","EXPLAIN FORMAT=JSON SELECT * FROM t","EXPLAIN",map[string]bool{"explain":true,"explain_select":true},nil},
		{"B22","SET @x = 1","SET",nil,nil},{"B23","SHOW TABLES","SET",nil,nil},{"B24","USE testdb","SET",nil,nil},
		{"B25","PREPARE p FROM 'SELECT 1'","OTHER",nil,nil},{"B26","EXECUTE p","OTHER",nil,nil},{"B27","DEALLOCATE PREPARE p","OTHER",nil,nil},
		{"B28","BEGIN","OTHER",nil,nil},{"B29","COMMIT","OTHER",nil,nil},{"B30","ROLLBACK","OTHER",nil,nil},
		{"B31","TABLE t","TABLE",map[string]bool{"is_table":true},nil},
		{"B32","TABLE t ORDER BY id LIMIT 10","TABLE",map[string]bool{"is_table":true},nil},
		{"B33","SELECT 1; SELECT 2","MULTI",map[string]bool{"multi_node":true},map[string]bool{"multi_node_danger":true}},
		{"B34","SELECT 1; DELETE FROM t","MULTI_DANGER",map[string]bool{"multi_node":true,"multi_node_danger":true},nil},
		{"B35","CALL myproc(1)","DML",map[string]bool{"dml":true},nil},
		{"B36","DO SLEEP(1)","DML",map[string]bool{"dml":true},nil},
		{"B37","WITH cte AS (SELECT 1) SELECT * FROM cte","SELECT",map[string]bool{"has_cte":true},map[string]bool{"dml_cte":true}},
		{"B38","WITH cte AS (UPDATE t SET x=1) SELECT * FROM cte","PARSE_ERROR",nil,nil},
		{"B39","INSERT INTO t SELECT * FROM s","DML",map[string]bool{"dml":true,"insert_select":true},nil},
		{"C01","SELECT 1; /* comment */ SELECT 2","MULTI",map[string]bool{"multi_node":true},nil},
		{"C02","SELECT 1; -- comment\nDELETE FROM t","MULTI_DANGER",map[string]bool{"multi_node":true,"multi_node_danger":true},nil},
	}
	p, f := 0, 0
	for _, c := range cases { if runMYTest(t, c) { p++ } else { f++ } }
	if p != 43 || f != 0 { t.Errorf("MY base: %d/%d", p, p+f) }
	t.Logf("MY base: %d/43", p)
}

func TestMYExplainOra(t *testing.T) {
	cases := []myTC{
		{"E01","EXPLAIN INSERT INTO t VALUES (1)","EXPLAIN",map[string]bool{"explain":true,"explain_dml":true},nil},
		{"E02","EXPLAIN UPDATE t SET x=1","EXPLAIN",map[string]bool{"explain":true,"explain_dml":true},nil},
		{"E03","EXPLAIN DELETE FROM t","EXPLAIN",map[string]bool{"explain":true,"explain_dml":true},nil},
		{"E04","EXPLAIN CREATE TABLE t (id INT)","PARSE_ERROR",nil,nil},
		{"E05","EXPLAIN EXPLAIN SELECT * FROM t","PARSE_ERROR",nil,nil},
	}
	p, f := 0, 0
	for _, c := range cases { if runMYTest(t, c) { p++ } else { f++ } }
	if p != 5 || f != 0 { t.Errorf("MY EXPLAIN: %d/%d", p, p+f) }
	t.Logf("MY EXPLAIN: %d/5", p)
}

// ========== ECM Quote Boundary ==========

func TestECMQuoteBoundary(t *testing.T) {
	clean := []string{
		"SELECT '/*!50000' FROM t",      // single quote hides
		`SELECT "/*!50000" FROM t`,      // double quote hides
		"SELECT 'it''s /*!50000' FROM t",// paired single quotes, ECM inside
		`SELECT "it""s /*!50000" FROM t`,// paired double quotes, ECM inside
		"SELECT 1 /* block /*!50000 */", // block comment hides
		"SELECT /*+ /*!50000 */ 1",      // optimizer hint hides
		"# /*!50000 line\nSELECT 1",     // hash comment hides
		"-- /*!50000 line\nSELECT 1",    // dash comment hides
	}
	for _, sql := range clean {
		r := ScanECM(sql, ModeDefault)
		if r.Error != nil { t.Errorf("quote clean err: %v for %q", r.Error, sql) }
		if r.HasExecComment { t.Errorf("quote clean fp: %q", sql) }
	}

	hit := []string{
		"SELECT 'done' /*!50000 x*/",     // quote closed, ECM in code
		`SELECT "done" /*!50000 x*/`,     // dquote closed, ECM in code
	}
	for _, sql := range hit {
		r := ScanECM(sql, ModeDefault)
		if r.Error != nil { t.Errorf("quote hit err: %v for %q", r.Error, sql) }
		if !r.HasExecComment { t.Errorf("quote hit missed: %q", sql) }
	}

	// Backtick: ECM inside backtick hidden (tested in ecm_bt_test.go)
	// Backtick closed, ECM in code (tested in ecm_bt_test.go)

	t.Log("ECM quote boundary: OK")
}

// ========== PG Pipeline Fuzz ==========

func FuzzPGPipeline(f *testing.F) {
	for _, s := range []string{"SELECT 1", "SELECT * FROM t", "TABLE t",
		"SELECT * FROM t FOR UPDATE", "EXPLAIN ANALYZE SELECT * FROM t",
		"WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d",
		"SELECT 1; DELETE FROM t", "EXPLAIN DELETE FROM t"} { f.Add(s) }
	f.Fuzz(func(t *testing.T, sql string) {
		defer func() { recover() }()
		k, fe, omniCalled, a := classifyPGFull(sql)
		if a {
			if k != "SELECT" && k != "EXPLAIN" { t.Errorf("adapter+wrong kind=%s", k) }
			if fe["locking"] || fe["select_into"] || fe["dml_cte"] || fe["dml"] || fe["ddl"] ||
				fe["unknown_node"] || fe["explain_analyze"] || fe["explain_dml"] || fe["explain_ddl"] ||
				fe["explain_non_select"] || fe["nested_explain"] {
				t.Errorf("adapter+danger feats=%v", fe)
			}
		}
		if !omniCalled && a { t.Error("adapter without Omni") }
	})
}

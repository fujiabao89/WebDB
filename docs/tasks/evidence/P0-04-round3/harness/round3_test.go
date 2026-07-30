package main

import (
	"fmt"
	"strings"
	"testing"

	myast "github.com/bytebase/omni/mysql/ast"
	myparser "github.com/bytebase/omni/mysql/parser"
	"github.com/bytebase/omni/pg"
	pgast "github.com/bytebase/omni/pg/ast"
)

// ========== ECM recognizer visitor ==========
type af struct{ f func() }
func (v *af) Visit(n myast.Node) myast.Visitor {
	if n == nil { return nil }
	if be, ok := n.(*myast.BinaryExpr); ok && be.Op == myast.BinOpAssign { v.f(); return nil }
	return v
}

// ========== MySQL policy oracle ==========
func allowMySQL(kind string, feats map[string]bool) bool {
	if feats == nil { feats = make(map[string]bool) }
	danger := []string{
		"locking", "for_update", "for_share", "lock_in_share_mode",
		"nowait", "skip_locked",
		"into_outfile", "into_dumpfile", "into_vars",
		"user_var_assign",
		"dml_cte", "modifying_cte",
		"dml", "ddl", "unknown_node",
		"multi_node", "multi_node_danger",
		"explain_analyze", "explain_dml", "explain_ddl",
		"explain_non_select", "nested_explain",
	}
	for _, d := range danger {
		if feats[d] { return false }
	}
	switch kind {
	case "SELECT":
		return true
	case "EXPLAIN":
		return feats["explain_select"] && !feats["explain_analyze"]
	default:
		return false
	}
}

func classifyMY(nodes []myast.Node) (string, map[string]bool) {
	f := make(map[string]bool)
	if len(nodes) == 0 { return "EMPTY", f }
	if len(nodes) > 1 {
		f["multi_node"] = true
		for _, n := range nodes {
			switch n.(type) {
			case *myast.DropTableStmt, *myast.DeleteStmt, *myast.InsertStmt, *myast.UpdateStmt,
				*myast.AlterTableStmt, *myast.CreateTableStmt, *myast.TruncateStmt:
				f["multi_node_danger"] = true; return "MULTI_DANGER", f
			}
		}
		return "MULTI", f
	}
	n := nodes[0]
	switch x := n.(type) {
	case *myast.SelectStmt:
		if x.TableSource != nil { f["is_table"] = true; return "TABLE", f }
		if x.ForUpdate != nil {
			f["locking"] = true
			if x.ForUpdate.LockInShareMode { f["lock_in_share_mode"] = true } else if x.ForUpdate.Share { f["for_share"] = true } else { f["for_update"] = true }
			if x.ForUpdate.NoWait { f["nowait"] = true }
			if x.ForUpdate.SkipLocked { f["skip_locked"] = true }
		}
		if x.Into != nil {
			if x.Into.Outfile != "" { f["into_outfile"] = true }
			if x.Into.Dumpfile != "" { f["into_dumpfile"] = true }
			if len(x.Into.Vars) > 0 { f["into_vars"] = true }
		}
		if len(x.CTEs) > 0 { f["has_cte"] = true; for _, c := range x.CTEs { if c.Select == nil { f["dml_cte"] = true; break } } }
		for _, t := range x.TargetList { myast.Walk(&af{func() { f["user_var_assign"] = true }}, t) }
		return "SELECT", f
	case *myast.InsertStmt: f["dml"] = true; if x.IsReplace { f["is_replace"] = true }; if x.Select != nil { f["insert_select"] = true }; return "DML", f
	case *myast.UpdateStmt: f["dml"] = true; return "DML", f
	case *myast.DeleteStmt: f["dml"] = true; return "DML", f
	case *myast.ExplainStmt:
		f["explain"] = true
		if x.Analyze { f["explain_analyze"] = true }
		switch s := x.Stmt.(type) {
		case *myast.SelectStmt: f["explain_select"] = true; return "EXPLAIN", f
		case *myast.InsertStmt, *myast.UpdateStmt, *myast.DeleteStmt: f["explain_dml"] = true; return "EXPLAIN", f
		case *myast.ExplainStmt: f["nested_explain"] = true; return "EXPLAIN", f
		default: if s != nil { f["explain_non_select"] = true }; return "EXPLAIN", f
		}
	case *myast.CreateTableStmt, *myast.DropTableStmt, *myast.AlterTableStmt, *myast.TruncateStmt: f["ddl"] = true; return "DDL", f
	case *myast.CallStmt, *myast.DoStmt: f["dml"] = true; return "DML", f
	case *myast.TableStmt: f["is_table"] = true; return "TABLE", f
	case *myast.SetStmt, *myast.ShowStmt, *myast.UseStmt: return "SET", f
	case *myast.PrepareStmt, *myast.ExecuteStmt, *myast.DeallocateStmt,
		*myast.BeginStmt, *myast.CommitStmt, *myast.RollbackStmt: return "OTHER", f
	default: f["unknown_node"] = true; return fmt.Sprintf("UNKNOWN(%T)", x), f
	}
}

func classifyMYFull(sql string, mode ECMMode) (string, map[string]bool, bool, bool, bool) {
	f := make(map[string]bool)
	r := ScanECM(sql, mode)
	if r.Error != nil { return "ECM_ERROR", f, false, false, false }
	if r.HasExecComment { return "ECM_DETECTED", f, true, false, false }
	nl, err := myparser.Parse(sql)
	if err != nil { return "PARSE_ERROR", f, false, true, false }
	if nl == nil || len(nl.Items) == 0 { return "EMPTY", f, false, true, false }
	k, fe := classifyMY(nl.Items)
	a := allowMySQL(k, fe)
	return k, fe, false, true, a
}

func pgPolicy(sql string) (string, bool) {
	segs := pg.Split(sql)
	if len(segs) > 1 { return "MULTI", false }
	stmts, err := pg.Parse(sql)
	if err != nil { return "PARSE_ERROR", false }
	if len(stmts) == 0 { return "EMPTY", false }
	if len(stmts) > 1 { return "MULTI", false }
	n := stmts[0].AST
	if n == nil { return "EMPTY", false }
	switch x := n.(type) {
	case *pgast.SelectStmt:
		if x.IntoClause != nil || x.LockingClause.Len() > 0 { return "SELECT", false }
		if x.WithClause != nil { for _, it := range x.WithClause.Ctes.Items { if c, ok := it.(*pgast.CommonTableExpr); ok { switch c.Ctequery.(type) { case *pgast.InsertStmt, *pgast.UpdateStmt, *pgast.DeleteStmt: return "SELECT", false } } } }
		return "SELECT", true
	case *pgast.ExplainStmt:
		switch x.Query.(type) { case *pgast.SelectStmt: return "EXPLAIN", true; default: return "EXPLAIN", false }
	default: return fmt.Sprintf("UNKNOWN(%T)", x), false
	}
}

// ========== RED Fix Tests ==========
func TestREDPolicyFixed(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM t FOR UPDATE", "SELECT * FROM t LOCK IN SHARE MODE",
		"SELECT * FROM t FOR UPDATE NOWAIT", "SELECT * FROM t FOR UPDATE SKIP LOCKED",
		"SELECT * INTO OUTFILE '/tmp/t' FROM t", "SELECT * INTO DUMPFILE '/tmp/t' FROM t",
		"SELECT 1 INTO @var", "SELECT @x := id FROM t", "SELECT (@x := id) + 1 FROM t",
		"WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d",
	} {
		_, _, _, _, a := classifyMYFull(sql, ModeDefault)
		if a { t.Errorf("GREEN FAIL: dangerous SQL still calls Adapter: %q", sql) }
	}
	for _, sql := range []string{"SELECT 1", "SELECT * FROM t", "WITH cte AS (SELECT 1) SELECT * FROM cte"} {
		_, _, _, _, a := classifyMYFull(sql, ModeDefault)
		if !a { t.Errorf("GREEN FAIL: safe SQL blocked: %q", sql) }
	}
	for _, sql := range []string{
		"EXPLAIN ANALYZE SELECT * FROM t", "EXPLAIN INSERT INTO t VALUES (1)",
		"EXPLAIN UPDATE t SET x=1", "EXPLAIN DELETE FROM t",
		"EXPLAIN CREATE TABLE t (id INT)", "EXPLAIN EXPLAIN SELECT * FROM t",
	} {
		_, _, _, _, a := classifyMYFull(sql, ModeDefault)
		if a { t.Errorf("GREEN FAIL: EXPLAIN dangerous calls Adapter: %q", sql) }
	}
	_, _, _, _, a := classifyMYFull("EXPLAIN SELECT * FROM t", ModeDefault)
	if !a { t.Error("GREEN FAIL: EXPLAIN SELECT blocked") }
	t.Log("Policy fix: OK")
}

// ========== SQL Mode Tests ==========
func TestSQLModeDiff(t *testing.T) {
	// Default: \' escapes → string continues, ECM inside string → not hit
	r := ScanECM(`SELECT 'x\'/*!50000 SLEEP(1)*/'`, ModeDefault)
	if r.Error != nil { t.Errorf("default err: %v", r.Error) }
	if r.HasExecComment { t.Error("default: escaped quote, ECM in string should not hit") }

	// NO_BACKSLASH_ESCAPES: \ is literal, ' closes string, ECM in code → HIT
	r = ScanECM(`SELECT 'x\'/*!50000 SLEEP(1)*/'`, ModeNoBackslashEscapes)
	if r.Error != nil { t.Errorf("nobs err: %v", r.Error) }
	if !r.HasExecComment { t.Error("nobs: literal backslash, quote closes, ECM in code should hit") }

	// Unknown mode → error
	r = ScanECM("SELECT 1", ECMMode(99))
	if r.Error == nil { t.Error("unknown mode should error") }

	t.Log("SQL mode diff: OK")
}

// ========== ECM Boundary ==========
func TestECMBoundaryFull(t *testing.T) {
	// Quotes hide ECM
	for _, sql := range []string{
		"SELECT '/*!50000' FROM t",
		`SELECT "/*!50000" FROM t`,
		"SELECT 1 /* block comment */",
		"SELECT /*+ optimizer_hint */ 1",
		"# /*!50000 line comment\nSELECT 1",
		"-- /*!50000 line comment\nSELECT 1",
	} {
		r := ScanECM(sql, ModeDefault)
		if r.Error != nil { t.Errorf("boundary err: %v for %q", r.Error, sql) }
		if r.HasExecComment { t.Errorf("boundary fp: %q", sql) }
	}
	// --\n bypass, --\r\n bypass, single dash bypass
	for _, sql := range []string{
		"--\n/*!50000 SLEEP(10)*/ SELECT 1",
		"--\r\n/*!50000 SLEEP(10)*/ SELECT 1",
		"SELECT 1 -/*!50000 SLEEP(10)+*/ 0",
	} {
		r := ScanECM(sql, ModeDefault)
		if r.Error != nil { t.Errorf("bypass err: %v", r.Error) }
		if !r.HasExecComment { t.Errorf("bypass missed: %q", sql) }
	}
	// Unterminated → error
	for _, sql := range []string{"SELECT 'x", `SELECT "x`, "SELECT `x", "SELECT /* x"} {
		if r := ScanECM(sql, ModeDefault); r.Error == nil { t.Errorf("unterm: %q", sql) }
	}
	// Unclosed ECM
	if r := ScanECM("SELECT /*!50000 DROP TABLE t", ModeDefault); !r.HasExecComment { t.Error("unclosed ECM") }
	if r := ScanECM("SELECT /*! */ 1", ModeDefault); !r.HasExecComment { t.Error("empty ver ECM") }
	t.Log("ECM boundary: OK")
}

// ========== PG TABLE ==========
func TestPGTABLEPolicy(t *testing.T) {
	for _, c := range []struct{ s string; a bool }{
		{"TABLE t", true}, {"TABLE ONLY t", true}, {"TABLE t ORDER BY id LIMIT 10", true},
		{"TABLE t FOR UPDATE", false}, {"WITH d AS (DELETE FROM t RETURNING *) TABLE d", false}, {"TABLE t; DELETE FROM t2", false},
	} {
		_, a := pgPolicy(c.s)
		if a != c.a { t.Errorf("PG TABLE %q: want %v got %v", c.s, c.a, a) }
	}
	t.Log("PG TABLE 6-item: OK")
}

// ========== Adapter Counters ==========
func TestAdapterCounter(t *testing.T) {
	for _, c := range []struct{ s string; m ECMMode; e, o, a bool; k string }{
		{"/*!50000 DROP TABLE t*/ SELECT 1", ModeDefault, true, false, false, "ECM_DETECTED"},
		{"SELECT 'x", ModeDefault, false, false, false, "ECM_ERROR"},
		{"SELECT 1", ECMMode(99), false, false, false, "ECM_ERROR"},
		{"EXPLAIN CREATE TABLE t (id INT)", ModeDefault, false, true, false, "PARSE_ERROR"},
		{"SELECT 1; SELECT 2", ModeDefault, false, true, false, "MULTI"},
		{"DELETE FROM t", ModeDefault, false, true, false, "DML"},
		{"SELECT 1", ModeDefault, false, true, true, "SELECT"},
	} {
		k, _, e, o, a := classifyMYFull(c.s, c.m)
		if e != c.e || o != c.o || a != c.a || k != c.k {
			t.Errorf("%q: e=%v o=%v a=%v k=%s (want e=%v o=%v a=%v k=%s)", c.s, e, o, a, k, c.e, c.o, c.a, c.k)
		}
	}
	t.Log("Adapter counters: OK")
}

// ========== ECM Corpus ==========
func TestECMCorpusR3(t *testing.T) {
	for _, s := range []string{
		"/*!50000 DROP TABLE t*/ SELECT 1", "/*!50000 ALTER TABLE t ADD COLUMN x INT*/ SELECT 1",
		"/*!50000 DELETE FROM t*/ SELECT 1", "/*!50000 INSERT INTO t VALUES (1)*/ SELECT 1",
		"/*!99999 SELECT 1*/", "SELECT /*!80100 42*/ FROM dual", "SELECT /*! 1 + 1 */ FROM dual",
		"/*!50000\nDROP TABLE t;\n*/ SELECT 1", "/*!50000 DROP TABLE t; DELETE FROM t2*/ SELECT 1",
		"/*!50000 WITH cte AS (SELECT 1) DELETE FROM t*/ SELECT 1",
		"SELECT /*!80100 1 /* nested */ + 2 */", "SELECT a, /*!80100 b, */ c FROM t",
	} {
		r := ScanECM(s, ModeDefault)
		if r.Error != nil || !r.HasExecComment { t.Errorf("pos missed: %q", s) }
	}
	for _, s := range []string{
		"SELECT '/*!50000 harmless*/' AS str", "SELECT 1 /* block comment */",
		"SELECT /*+ optimizer_hint */ 1", "# /*!50000 line comment\nSELECT 1",
		"-- /*!50000 line comment\nSELECT 1",
	} {
		r := ScanECM(s, ModeDefault)
		if r.Error != nil || r.HasExecComment { t.Errorf("neg fp: %q", s) }
	}
	t.Log("ECM Corpus: 12 pos 5 neg OK")
}

// ========== Fuzz ==========
func FuzzECMLexer(f *testing.F) {
	for _, s := range []string{"SELECT 1", "/*!50000 DROP*/", "SELECT '/*!'", "--\n/*!*/ SELECT 1", "SELECT 1-'x'", strings.Repeat("'", 100), strings.Repeat("/*", 100)} { f.Add(s) }
	f.Fuzz(func(t *testing.T, sql string) {
		for _, m := range []ECMMode{ModeDefault, ModeNoBackslashEscapes} {
			r := ScanECM(sql, m)
			if r.HasExecComment && r.Error != nil { t.Errorf("both m=%d", m) }
		}
		if r := ScanECM(sql, ECMMode(99)); r.Error == nil { t.Error("unknown mode") }
	})
}

func TestECMBacktickRound3(t *testing.T) {
	bt := "`"
	r := ScanECM("SELECT "+bt+"/*!50000"+bt+" FROM t", ModeDefault)
	if r.Error != nil { t.Errorf("bt err: %v", r.Error) }
	if r.HasExecComment { t.Error("bt fp") }
	r = ScanECM("SELECT "+bt+"x"+bt+" /*!50000 y*/", ModeDefault)
	if r.Error != nil { t.Errorf("bt2 err: %v", r.Error) }
	if !r.HasExecComment { t.Error("bt2 missed") }
	t.Log("ECM backtick: OK")
}

func FuzzMySQLPipeline(f *testing.F) {
	seeds := []string{"SELECT 1", "/*!50000 DROP*/ SELECT 1", "--\n/*!*/ SELECT 1",
		"SELECT 1 -/*!50000 SLEEP(10)+*/ 0", "SELECT * FROM t FOR UPDATE",
		"SELECT * INTO OUTFILE '/tmp/t' FROM t", "SELECT 1 INTO @x",
		"SELECT @x := id FROM t", "SELECT 1; DELETE FROM t",
		"EXPLAIN DELETE FROM t", "EXPLAIN SELECT * FROM t"}
	for _, s := range seeds { f.Add(s) }
	f.Fuzz(func(t *testing.T, sql string) {
		for _, m := range []ECMMode{ModeDefault, ModeNoBackslashEscapes} {
			k, fe, e, o, a := classifyMYFull(sql, m)
			if e && o { t.Errorf("ECM+Omni k=%s", k) }
			if e && a { t.Errorf("ECM+Adapter k=%s", k) }
			if k == "ECM_ERROR" && (o || a) { t.Errorf("err+call o=%v a=%v", o, a) }
			if k == "PARSE_ERROR" && a { t.Error("parse+adapter") }
			if a { // Adapter=true → strong invariants
				if k != "SELECT" && k != "EXPLAIN" { t.Errorf("adapter+wrong kind=%s", k) }
				if fe["locking"] || fe["into_outfile"] || fe["into_dumpfile"] || fe["into_vars"] ||
					fe["user_var_assign"] || fe["dml_cte"] || fe["dml"] || fe["ddl"] || fe["unknown_node"] ||
					fe["multi_node"] || fe["multi_node_danger"] || fe["explain_analyze"] ||
					fe["explain_dml"] || fe["explain_non_select"] || fe["nested_explain"] {
					t.Errorf("adapter+danger feats=%v", fe)
				}
			}
		}
		k, _, _, o, a := classifyMYFull(sql, ECMMode(99))
		if o || a || k != "ECM_ERROR" { t.Errorf("unknown: o=%v a=%v k=%s", o, a, k) }
	})
}

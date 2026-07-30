package main

import "testing"

func TestREDDangerousSelectCallsAdapter(t *testing.T) {
	danger := []string{
		"SELECT * FROM t FOR UPDATE",
		"SELECT * FROM t LOCK IN SHARE MODE",
		"SELECT * FROM t FOR UPDATE NOWAIT",
		"SELECT * FROM t FOR UPDATE SKIP LOCKED",
		"SELECT * INTO OUTFILE '/tmp/t' FROM t",
		"SELECT * INTO DUMPFILE '/tmp/t' FROM t",
		"SELECT 1 INTO @var",
		"SELECT @x := id FROM t",
		"SELECT (@x := id) + 1 FROM t",
		"WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d",
		"SELECT 1; DELETE FROM t",
		"DELETE FROM t",
	}
	for _, sql := range danger {
		_, _, _, _, a := classifyMYFull(sql, ModeDefault)
		if a { t.Errorf("RED: dangerous SQL calls Adapter: %q", sql) }
	}
	safe := []string{"SELECT 1", "SELECT * FROM t", "WITH cte AS (SELECT 1) SELECT * FROM cte"}
	for _, sql := range safe {
		_, _, _, _, a := classifyMYFull(sql, ModeDefault)
		if !a { t.Errorf("RED: safe SQL blocked: %q", sql) }
	}
	explainDeny := []string{
		"EXPLAIN ANALYZE SELECT * FROM t", "EXPLAIN INSERT INTO t VALUES (1)",
		"EXPLAIN UPDATE t SET x=1", "EXPLAIN DELETE FROM t",
		"EXPLAIN CREATE TABLE t (id INT)", "EXPLAIN EXPLAIN SELECT * FROM t",
	}
	for _, sql := range explainDeny {
		_, _, _, _, a := classifyMYFull(sql, ModeDefault)
		if a { t.Errorf("RED: EXPLAIN dangerous calls Adapter: %q", sql) }
	}
	_, _, _, _, a := classifyMYFull("EXPLAIN SELECT * FROM t", ModeDefault)
	if !a { t.Error("RED: EXPLAIN SELECT should be allowed") }
}

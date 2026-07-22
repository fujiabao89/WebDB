package adapter

import (
	"fmt"
	"strings"
)

type sortSpec struct {
	column      string
	asc         bool
	nullsLast   bool
	nullRank    int
	nonNullRank int
}

func buildSortSpecs(keys []SortKey) ([]sortSpec, error) {
	if len(keys) == 0 {
		return nil, newError(ErrUnsupportedQuery, "sort keys required", nil)
	}
	specs := make([]sortSpec, len(keys))
	seen := map[string]bool{}
	for i, k := range keys {
		if !validIdent(k.Column) {
			return nil, newError(ErrUnsupportedQuery, "invalid column: "+k.Column, nil)
		}
		if seen[k.Column] {
			return nil, newError(ErrUnsupportedQuery, "duplicate sort column: "+k.Column, nil)
		}
		seen[k.Column] = true
		s := sortSpec{column: k.Column, asc: k.Order != SortDesc, nullsLast: k.NullsLast}
		if k.NullsLast {
			s.nullRank, s.nonNullRank = 1, 0
		} else {
			s.nullRank, s.nonNullRank = 0, 1
		}
		specs[i] = s
	}
	return specs, nil
}

func validIdent(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for i, r := range s {
		if i == 0 && !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_') {
			return false
		}
		if i > 0 && !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}

func buildWrappedSQL(sql string, specs []sortSpec, engine Engine, lastVals []any, args []any, limit int) (string, []any, error) {
	orderClause := buildOrderByClause(specs, engine)
	allArgs := make([]any, len(args))
	copy(allArgs, args)
	paramIdx := len(allArgs) + 1
	var contClause string
	if len(lastVals) > 0 {
		contClause, paramIdx = buildContinuationClause(specs, engine, paramIdx)
		allArgs = append(allArgs, lastVals...)
	}
	wrapped := fmt.Sprintf("SELECT * FROM (\n%s\n) AS webdb_page", sql)
	if contClause != "" {
		wrapped += "\nWHERE " + contClause
	}
	wrapped += "\nORDER BY " + orderClause
	lp := placeholder(engine, paramIdx)
	wrapped += fmt.Sprintf("\nLIMIT %s", lp)
	allArgs = append(allArgs, limit)
	return wrapped, allArgs, nil
}

func buildOrderByClause(specs []sortSpec, engine Engine) string {
	var parts []string
	for _, s := range specs {
		col := quoteIdent(s.column, engine)
		dir := "ASC"
		if !s.asc {
			dir = "DESC"
		}
		if engine == EnginePostgreSQL {
			n := "NULLS FIRST"
			if s.nullsLast {
				n = "NULLS LAST"
			}
			parts = append(parts, fmt.Sprintf("%s %s %s", col, dir, n))
		} else {
			nr := 0
			if s.nullsLast {
				nr = 1
			}
			parts = append(parts, fmt.Sprintf("CASE WHEN %s IS NULL THEN %d ELSE %d END, %s %s", col, nr, 1-nr, col, dir))
		}
	}
	return strings.Join(parts, ", ")
}

func buildContinuationClause(specs []sortSpec, engine Engine, startIdx int) (string, int) {
	c, idx := buildAfterClause(specs, 0, engine, startIdx)
	return c, idx
}

func buildAfterClause(specs []sortSpec, idx int, engine Engine, pi int) (string, int) {
	if idx >= len(specs) {
		return "FALSE", pi
	}
	s := specs[idx]
	col := quoteIdent(s.column, engine)
	ao := ">"
	if !s.asc {
		ao = "<"
	}
	ph := placeholder(engine, pi)
	pi++
	cr := fmt.Sprintf("(CASE WHEN %s IS NULL THEN %d ELSE %d END)", col, s.nullRank, s.nonNullRank)
	lr := fmt.Sprintf("(CASE WHEN %s IS NULL THEN %d ELSE %d END)", ph, s.nullRank, s.nonNullRank)
	af := fmt.Sprintf("(%s > %s)", cr, lr)
	af += fmt.Sprintf(" OR (%s = %s AND %s = %d AND %s %s %s)", cr, lr, cr, s.nonNullRank, col, ao, ph)
	if idx+1 < len(specs) {
		eq := fmt.Sprintf("(%s = %s AND (%s = %d OR %s = %s))", cr, lr, cr, s.nullRank, col, ph)
		na, npi := buildAfterClause(specs, idx+1, engine, pi)
		af += fmt.Sprintf(" OR (%s AND %s)", eq, na)
		pi = npi
	}
	return af, pi
}

func placeholder(engine Engine, n int) string {
	switch engine {
	case EnginePostgreSQL:
		return fmt.Sprintf("$%d::integer", n)
	case EngineMySQL:
		return "?"
	default:
		return "?"
	}
}

func quoteIdent(ident string, engine Engine) string {
	switch engine {
	case EnginePostgreSQL:
		return "\"" + strings.ReplaceAll(ident, "\"", "\"\"") + "\""
	case EngineMySQL:
		return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
	default:
		return ident
	}
}

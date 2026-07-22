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

type ksBuilder struct {
	engine       Engine
	specs        []sortSpec
	pgN          int
	myNullCounts []int // is-null placeholder count per sort key (MySQL arg duplication)
	myValCounts  []int // value placeholder count per sort key (MySQL arg duplication)
}

func buildWrappedSQL(sql string, specs []sortSpec, engine Engine, lastVals []any, args []any, limit int) (string, []any, error) {
	orderClause := buildOrderByClause(specs, engine)
	allArgs := make([]any, len(args))
	copy(allArgs, args)
	b := &ksBuilder{engine: engine, specs: specs, pgN: len(allArgs) + 1,
		myNullCounts: make([]int, len(specs)), myValCounts: make([]int, len(specs))}
	var contClause string
	if len(lastVals) > 0 {
		contClause = b.buildContinuation()
		// lastVals layout: [isNull0, val0, isNull1, val1, ...] (2 values per key)
		// phNull/phVal 每次引用都重新调用，myNullCounts/myValCounts 记录各 key 的占位符出现次数
		for i := range specs {
			isNull := lastVals[i*2]
			val := lastVals[i*2+1]
			for j := 0; j < b.myNullCounts[i]; j++ {
				allArgs = append(allArgs, isNull)
			}
			for j := 0; j < b.myValCounts[i]; j++ {
				allArgs = append(allArgs, val)
			}
		}
	}
	wrapped := fmt.Sprintf("SELECT * FROM (\n%s\n) AS webdb_page", sql)
	if contClause != "" {
		wrapped += "\nWHERE " + contClause
	}
	wrapped += "\nORDER BY " + orderClause
	if engine == EnginePostgreSQL {
		wrapped += fmt.Sprintf("\nLIMIT $%d", b.pgN)
		allArgs = append(allArgs, limit)
	} else {
		wrapped += "\nLIMIT ?"
		allArgs = append(allArgs, limit)
	}
	return wrapped, allArgs, nil
}

func (b *ksBuilder) phNull(keyIdx int) string {
	b.myNullCounts[keyIdx]++
	if b.engine == EnginePostgreSQL {
		n := b.pgN
		b.pgN++
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

func (b *ksBuilder) phVal(keyIdx int) string {
	b.myValCounts[keyIdx]++
	if b.engine == EnginePostgreSQL {
		n := b.pgN
		b.pgN++
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

func (b *ksBuilder) buildContinuation() string { return b.buildAfter(0) }

func (b *ksBuilder) buildAfter(idx int) string {
	if idx >= len(b.specs) {
		return "FALSE"
	}
	s := b.specs[idx]
	col := quoteIdent(s.column, b.engine)
	ao := ">"
	if !s.asc {
		ao = "<"
	}
	cr := fmt.Sprintf("(CASE WHEN %s IS NULL THEN %d ELSE %d END)", col, s.nullRank, s.nonNullRank)
	// 每次引用 phNull/phVal 都重新调用，确保 MySQL ? 计数与 args — 致
	lr0 := fmt.Sprintf("(CASE WHEN %s THEN %d ELSE %d END)", b.phNull(idx), s.nullRank, s.nonNullRank)
	lr1 := fmt.Sprintf("(CASE WHEN %s THEN %d ELSE %d END)", b.phNull(idx), s.nullRank, s.nonNullRank)
	af := fmt.Sprintf("(%s > %s)", cr, lr0)
	af += fmt.Sprintf(" OR (%s = %s AND %s = %d AND %s %s %s)", cr, lr1, cr, s.nonNullRank, col, ao, b.phVal(idx))
	if idx+1 < len(b.specs) {
		lr2 := fmt.Sprintf("(CASE WHEN %s THEN %d ELSE %d END)", b.phNull(idx), s.nullRank, s.nonNullRank)
		eq := fmt.Sprintf("(%s = %s AND (%s = %d OR %s = %s))", cr, lr2, cr, s.nullRank, col, b.phVal(idx))
		af += fmt.Sprintf(" OR (%s AND %s)", eq, b.buildAfter(idx+1))
	}
	return af
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

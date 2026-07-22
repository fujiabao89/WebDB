package adapter

import (
	"fmt"
	"strings"
)

type nullRank int

const (
	rankNullFirst    nullRank = 0
	rankNonNullFirst nullRank = 1
)

type sortSpec struct {
	column      string
	asc         bool
	nullsLast   bool
	nullRank    int // NULL 的 rank 值
	nonNullRank int // 非 NULL 的 rank 值
}

func buildSortSpecs(keys []SortKey) ([]sortSpec, error) {
	if len(keys) == 0 {
		return nil, newError(ErrUnsupportedQuery, "sort keys required", nil)
	}
	specs := make([]sortSpec, len(keys))
	for i, k := range keys {
		if !validIdent(k.Column) {
			return nil, newError(ErrUnsupportedQuery, "invalid column name: "+k.Column, nil)
		}
		s := sortSpec{column: k.Column, asc: k.Order != SortDesc, nullsLast: k.NullsLast}
		if k.NullsLast {
			s.nullRank, s.nonNullRank = 1, 0
		} else {
			s.nullRank, s.nonNullRank = 0, 1
		}
		specs[i] = s
	}
	// check duplicate columns
	seen := map[string]bool{}
	for _, s := range specs {
		if seen[s.column] {
			return nil, newError(ErrUnsupportedQuery, "duplicate sort column: "+s.column, nil)
		}
		seen[s.column] = true
	}
	return specs, nil
}

func validIdent(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for i, r := range s {
		if i == 0 && !isIdentStart(r) {
			return false
		}
		if i > 0 && !isIdentPart(r) {
			return false
		}
	}
	return true
}
func isIdentStart(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' }
func isIdentPart(r rune) bool  { return isIdentStart(r) || (r >= '0' && r <= '9') }

// buildOrderBy 生成方言 ORDER BY 子句。
func buildOrderBy(specs []sortSpec, engine Engine) string {
	var parts []string
	for _, s := range specs {
		col := quoteIdent(s.column, engine)
		dir := "ASC"
		if !s.asc {
			dir = "DESC"
		}
		nulls := "NULLS FIRST"
		if s.nullsLast {
			nulls = "NULLS LAST"
		}
		parts = append(parts, fmt.Sprintf("%s %s %s", col, dir, nulls))
	}
	return strings.Join(parts, ", ")
}

// buildContinuation 生成 continuation predicate。
func buildContinuation(specs []sortSpec, engine Engine) string {
	return buildAfter(specs, 0, engine)
}

func buildAfter(specs []sortSpec, idx int, engine Engine) string {
	if idx >= len(specs) {
		return "FALSE"
	}
	s := specs[idx]
	col := quoteIdent(s.column, engine)
	ascOp := ">"
	if !s.asc {
		ascOp = "<"
	}

	currentRankExpr := fmt.Sprintf("CASE WHEN %s IS NULL THEN %d ELSE %d END", col, s.nullRank, s.nonNullRank)

	after := fmt.Sprintf("(%s > $L%d)", currentRankExpr, idx)
	after += fmt.Sprintf("\n    OR (%s = $L%d AND %s = %d AND %s %s $V%d)",
		currentRankExpr, idx, currentRankExpr, s.nonNullRank, col, ascOp, idx)

	if idx+1 < len(specs) {
		equal := fmt.Sprintf("(%s = $L%d AND %s = %d AND %s = $V%d)",
			currentRankExpr, idx, currentRankExpr, s.nullRank, col, idx)
		nextAfter := buildAfter(specs, idx+1, engine)
		after += fmt.Sprintf("\n    OR (%s AND %s)", equal, nextAfter)
	}
	return after
}

func quoteIdent(ident string, engine Engine) string {
	switch engine {
	case EnginePostgreSQL:
		return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
	case EngineMySQL:
		return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
	default:
		return ident
	}
}

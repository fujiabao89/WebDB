package adapter

import (
	"fmt"
	"strings"

	"github.com/fujiabao89/webdb/internal/queryplan"
)

type sortSpec struct {
	column      string
	asc         bool
	nullsLast   bool
	nullRank    int
	nonNullRank int
}

// sortSpecsFromPlan 从不可伪造的 VerifiedSortPlan 派生 keyset 排序规格。
// 唯一性证明已在 queryplan.VerifySortPlan 完成；此处不再信任任何客户端唯一性声明。
func sortSpecsFromPlan(plan queryplan.VerifiedSortPlan) ([]sortSpec, error) {
	if !queryplan.IsValidVerifiedPlan(plan) {
		return nil, newError(ErrUnsupportedQuery, "invalid verified sort plan", nil)
	}
	return sortSpecsFromSpecs(plan.SortSpecs())
}

// sortSpecsFromSpecs 从已验证的 SortSpec 切片派生 keyset 排序规格（首页/续页共用）。
func sortSpecsFromSpecs(specs []queryplan.SortSpec) ([]sortSpec, error) {
	if len(specs) == 0 {
		return nil, newError(ErrUnsupportedQuery, "sort specs required", nil)
	}
	out := make([]sortSpec, len(specs))
	for i, s := range specs {
		if !validIdent(s.Column) {
			return nil, newError(ErrUnsupportedQuery, "invalid column: "+s.Column, nil)
		}
		sp := sortSpec{column: s.Column, asc: s.Asc, nullsLast: s.NullsLast}
		if s.NullsLast {
			sp.nullRank, sp.nonNullRank = 1, 0
		} else {
			sp.nullRank, sp.nonNullRank = 0, 1
		}
		out[i] = sp
	}
	return out, nil
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

// ksBuilder 按占位符生成顺序记录 MySQL args，确保多列 keyset 参数顺序正确
type ksBuilder struct {
	engine    Engine
	specs     []sortSpec
	pgN       int
	lastVals  []any // [isNull0, val0, isNull1, val1, ...]
	mysqlArgs []any // MySQL args 按 placeholder 生成顺序追加
}

func buildWrappedSQL(sql string, specs []sortSpec, engine Engine, lastVals []any, args []any, limit int) (string, []any, error) {
	sql = strings.TrimRight(strings.TrimSpace(sql), ";")
	orderClause := buildOrderByClause(specs, engine)
	allArgs := make([]any, len(args))
	copy(allArgs, args)
	b := &ksBuilder{engine: engine, specs: specs, pgN: len(allArgs) + 1, lastVals: lastVals, mysqlArgs: make([]any, 0)}
	var contClause string
	if len(lastVals) > 0 {
		contClause = b.buildContinuation()
		allArgs = append(allArgs, b.mysqlArgs...)
	}
	wrapped := fmt.Sprintf("SELECT * FROM (\n%s\n) AS webdb_page", sql)
	if contClause != "" {
		wrapped += "\nWHERE " + contClause
	}
	if orderClause != "" {
		wrapped += "\nORDER BY " + orderClause
	}
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
	b.mysqlArgs = append(b.mysqlArgs, b.lastVals[keyIdx*2])
	if b.engine == EngineMySQL {
		return "?"
	}
	n := b.pgN
	b.pgN++
	return fmt.Sprintf("$%d", n)
}

func (b *ksBuilder) phVal(keyIdx int) string {
	b.mysqlArgs = append(b.mysqlArgs, b.lastVals[keyIdx*2+1])
	if b.engine == EngineMySQL {
		return "?"
	}
	n := b.pgN
	b.pgN++
	return fmt.Sprintf("$%d", n)
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
	lr0 := fmt.Sprintf("(CASE WHEN %s THEN %d ELSE %d END)", b.phNull(idx), s.nullRank, s.nonNullRank)
	lr1 := fmt.Sprintf("(CASE WHEN %s THEN %d ELSE %d END)", b.phNull(idx), s.nullRank, s.nonNullRank)
	af := fmt.Sprintf("(%s > %s)", cr, lr0)
	af += fmt.Sprintf(" OR (%s = %s AND %s = %d AND %s %s %s)", cr, lr1, cr, s.nonNullRank, col, ao, b.phVal(idx))
	if idx+1 < len(b.specs) {
		lr2 := fmt.Sprintf("(CASE WHEN %s THEN %d ELSE %d END)", b.phNull(idx), s.nullRank, s.nonNullRank)
		eq := fmt.Sprintf("(%s = %s AND (%s = %d OR %s = %s))", cr, lr2, cr, s.nullRank, col, b.phVal(idx))
		af += fmt.Sprintf(" OR (%s AND (%s))", eq, b.buildAfter(idx+1))
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

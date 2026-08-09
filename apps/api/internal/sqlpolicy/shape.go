package sqlpolicy

import (
	"fmt"

	mysqlast "github.com/bytebase/omni/mysql/ast"
	mysqlparser "github.com/bytebase/omni/mysql/parser"
	"github.com/bytebase/omni/pg"
	pgast "github.com/bytebase/omni/pg/ast"

	"github.com/fujiabao89/webdb/internal/queryplan"
)

// AnalyzeShape 保守提取可分页查询形状（ADR-014 单一基础表、可追溯列）。
//
// 仅接受单条 SELECT、单一基础表、目标列表全为普通列引用（或 *）的形状；
// join / 聚合 / GROUP BY / HAVING / DISTINCT / CTE / 集合操作 / 子查询 FROM /
// 内层 LIMIT-OFFSET / 锁定 / INTO / 窗口函数等任何无法可靠证明的形状返回错误。
// 该函数在用户查询执行前运行；返回错误由服务层映射为 unsupported_query。
func AnalyzeShape(dialect Dialect, sql string) (*queryplan.QueryShape, error) {
	switch dialect {
	case DialectPostgreSQL:
		return analyzePGShape(sql)
	case DialectMySQL:
		return analyzeMySQLShape(sql)
	default:
		return nil, fmt.Errorf("analyze shape: unknown dialect %q", dialect)
	}
}

// containsFuncCallPG 遍历 PG WHERE 表达式，发现任何函数调用即返回 true。
// 分页续页会重放原 SQL：volatile 谓词（random()/now() 等）使结果集合随执行变化，
// 无法证明确定性时必须 fail-closed（Codex P1）。
func containsFuncCallPG(n pgast.Node) bool {
	found := false
	pgast.Inspect(n, func(node pgast.Node) bool {
		if _, ok := node.(*pgast.FuncCall); ok {
			found = true
		}
		return true
	})
	return found
}

// containsFuncCallMySQL 遍历 MySQL WHERE 表达式，发现任何函数调用即返回 true
// （RAND()/NOW() 等 volatile 谓词；保守拒绝，Codex P1）。
func containsFuncCallMySQL(e mysqlast.ExprNode) bool {
	found := false
	mysqlast.Inspect(e, func(node mysqlast.Node) bool {
		if _, ok := node.(*mysqlast.FuncCallExpr); ok {
			found = true
		}
		return true
	})
	return found
}

func analyzePGShape(sql string) (*queryplan.QueryShape, error) {
	stmts, err := pg.Parse(sql)
	if err != nil {
		return nil, fmt.Errorf("analyze shape: %w", err)
	}
	if len(stmts) != 1 {
		return nil, fmt.Errorf("analyze shape: expected single statement, got %d", len(stmts))
	}
	sel, ok := stmts[0].AST.(*pgast.SelectStmt)
	if !ok {
		return nil, fmt.Errorf("analyze shape: not a SELECT")
	}
	if err := rejectPGShape(sel); err != nil {
		return nil, err
	}
	if sel.WhereClause != nil && containsFuncCallPG(sel.WhereClause) {
		return nil, fmt.Errorf("analyze shape: volatile predicate (function call in WHERE) not allowed")
	}
	shape := &queryplan.QueryShape{Columns: map[string]string{}}
	if sel.FromClause == nil || sel.FromClause.Len() != 1 {
		return nil, fmt.Errorf("analyze shape: expected exactly one base table in FROM")
	}
	rv, ok := sel.FromClause.Items[0].(*pgast.RangeVar)
	if !ok {
		return nil, fmt.Errorf("analyze shape: FROM is not a single base table")
	}
	shape.BaseSchema = rv.Schemaname
	shape.BaseTable = rv.Relname

	if sel.TargetList == nil || sel.TargetList.Len() == 0 {
		return nil, fmt.Errorf("analyze shape: empty target list")
	}
	seen := make(map[string]bool)
	for _, item := range sel.TargetList.Items {
		rt, ok := item.(*pgast.ResTarget)
		if !ok {
			return nil, fmt.Errorf("analyze shape: unsupported target item %T", item)
		}
		star, base, err := pgColumnRefBase(rt.Val)
		if err != nil {
			return nil, err
		}
		if star {
			shape.SelectStar = true
			continue
		}
		exposed := rt.Name
		if exposed == "" {
			exposed = base
		}
		if seen[exposed] {
			return nil, fmt.Errorf("analyze shape: duplicate exposed column %q", exposed)
		}
		seen[exposed] = true
		shape.Columns[exposed] = base
	}
	if !shape.SelectStar && len(shape.Columns) == 0 {
		return nil, fmt.Errorf("analyze shape: no exposed plain columns")
	}
	if err := shape.Validate(); err != nil {
		return nil, fmt.Errorf("analyze shape: %w", err)
	}
	return shape, nil
}

// pgColumnRefBase 判定 PG 目标项是否为普通列引用：返回 (star, baseColumn, err)。
// * 或 t.* 返回 star=true；普通列引用返回基础列名；表达式返回错误。
func pgColumnRefBase(val pgast.Node) (bool, string, error) {
	switch v := val.(type) {
	case *pgast.A_Star:
		return true, "", nil
	case *pgast.ColumnRef:
		if v.Fields == nil || v.Fields.Len() == 0 {
			return false, "", fmt.Errorf("analyze shape: empty column reference")
		}
		items := v.Fields.Items
		for _, f := range items {
			if _, isStar := f.(*pgast.A_Star); isStar {
				return true, "", nil
			}
			if _, isStr := f.(*pgast.String); !isStr {
				return false, "", fmt.Errorf("analyze shape: unsupported column ref field %T", f)
			}
		}
		last, _ := items[len(items)-1].(*pgast.String)
		return false, last.Str, nil
	default:
		return false, "", fmt.Errorf("analyze shape: target item %T is a computed expression, not a plain column", val)
	}
}

func rejectPGShape(sel *pgast.SelectStmt) error {
	switch {
	case sel.WithClause != nil:
		return fmt.Errorf("analyze shape: CTE not allowed")
	case sel.Op != pgast.SETOP_NONE:
		return fmt.Errorf("analyze shape: set operation not allowed")
	case sel.DistinctClause != nil:
		return fmt.Errorf("analyze shape: DISTINCT not allowed")
	case sel.GroupClause != nil:
		return fmt.Errorf("analyze shape: GROUP BY not allowed")
	case sel.HavingClause != nil:
		return fmt.Errorf("analyze shape: HAVING not allowed")
	case sel.WindowClause != nil:
		return fmt.Errorf("analyze shape: window clause not allowed")
	case sel.LockingClause != nil:
		return fmt.Errorf("analyze shape: locking clause not allowed")
	case sel.IntoClause != nil:
		return fmt.Errorf("analyze shape: SELECT INTO not allowed")
	case sel.LimitCount != nil || sel.LimitOffset != nil:
		return fmt.Errorf("analyze shape: LIMIT/OFFSET not allowed")
	}
	return nil
}

func analyzeMySQLShape(sql string) (*queryplan.QueryShape, error) {
	list, err := mysqlparser.Parse(sql)
	if err != nil {
		return nil, fmt.Errorf("analyze shape: %w", err)
	}
	if list.Len() != 1 {
		return nil, fmt.Errorf("analyze shape: expected single statement, got %d", list.Len())
	}
	sel, ok := list.Items[0].(*mysqlast.SelectStmt)
	if !ok {
		return nil, fmt.Errorf("analyze shape: not a SELECT")
	}
	if sel.ParenSource != nil {
		return nil, fmt.Errorf("analyze shape: parenthesized query not allowed")
	}
	if err := rejectMySQLShape(sel); err != nil {
		return nil, err
	}
	if sel.Where != nil && containsFuncCallMySQL(sel.Where) {
		return nil, fmt.Errorf("analyze shape: volatile predicate (function call in WHERE) not allowed")
	}
	shape := &queryplan.QueryShape{Columns: map[string]string{}}
	if len(sel.From) != 1 {
		return nil, fmt.Errorf("analyze shape: expected exactly one base table in FROM")
	}
	tr, ok := sel.From[0].(*mysqlast.TableRef)
	if !ok {
		return nil, fmt.Errorf("analyze shape: FROM is not a single base table")
	}
	shape.BaseSchema = tr.Schema
	shape.BaseTable = tr.Name

	if len(sel.TargetList) == 0 {
		return nil, fmt.Errorf("analyze shape: empty target list")
	}
	seen := make(map[string]bool)
	for _, item := range sel.TargetList {
		if rt, ok := item.(*mysqlast.ResTarget); ok {
			star, base, err := mysqlExprBase(rt.Val)
			if err != nil {
				return nil, err
			}
			if star {
				shape.SelectStar = true
				continue
			}
			exposed := rt.Name
			if exposed == "" {
				exposed = base
			}
			if seen[exposed] {
				return nil, fmt.Errorf("analyze shape: duplicate exposed column %q", exposed)
			}
			seen[exposed] = true
			shape.Columns[exposed] = base
			continue
		}
		star, base, err := mysqlExprBase(item)
		if err != nil {
			return nil, err
		}
		if star {
			shape.SelectStar = true
			continue
		}
		if seen[base] {
			return nil, fmt.Errorf("analyze shape: duplicate exposed column %q", base)
		}
		seen[base] = true
		shape.Columns[base] = base
	}
	if !shape.SelectStar && len(shape.Columns) == 0 {
		return nil, fmt.Errorf("analyze shape: no exposed plain columns")
	}
	if err := shape.Validate(); err != nil {
		return nil, fmt.Errorf("analyze shape: %w", err)
	}
	return shape, nil
}

// mysqlExprBase 判定 MySQL 目标项是否为普通列引用：返回 (star, baseColumn, err)。
func mysqlExprBase(e mysqlast.ExprNode) (bool, string, error) {
	switch v := e.(type) {
	case *mysqlast.StarExpr:
		return true, "", nil
	case *mysqlast.ColumnRef:
		if v.Star {
			return true, "", nil
		}
		return false, v.Column, nil
	default:
		return false, "", fmt.Errorf("analyze shape: target item %T is a computed expression, not a plain column", e)
	}
}

func rejectMySQLShape(sel *mysqlast.SelectStmt) error {
	switch {
	case len(sel.CTEs) > 0:
		return fmt.Errorf("analyze shape: CTE not allowed")
	case sel.DistinctKind != mysqlast.DistinctNone:
		return fmt.Errorf("analyze shape: DISTINCT not allowed")
	case len(sel.GroupBy) > 0:
		return fmt.Errorf("analyze shape: GROUP BY not allowed")
	case sel.Having != nil:
		return fmt.Errorf("analyze shape: HAVING not allowed")
	case len(sel.WindowClause) > 0:
		return fmt.Errorf("analyze shape: window clause not allowed")
	case sel.ForUpdate != nil:
		return fmt.Errorf("analyze shape: locking clause not allowed")
	case sel.Into != nil:
		return fmt.Errorf("analyze shape: INTO not allowed")
	case sel.Limit != nil:
		return fmt.Errorf("analyze shape: LIMIT/OFFSET not allowed")
	case sel.SetOp != mysqlast.SetOpNone:
		return fmt.Errorf("analyze shape: set operation not allowed")
	case sel.TableSource != nil || sel.ValuesSource != nil:
		return fmt.Errorf("analyze shape: TABLE/VALUES source not allowed")
	}
	return nil
}

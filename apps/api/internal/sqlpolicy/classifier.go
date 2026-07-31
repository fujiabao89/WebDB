package sqlpolicy

import (
	"crypto/sha256"
	"fmt"
	"strings"

	mysqlast "github.com/bytebase/omni/mysql/ast"
	mysqlparser "github.com/bytebase/omni/mysql/parser"
	"github.com/bytebase/omni/pg"
	pgast "github.com/bytebase/omni/pg/ast"
)

func normalizeSQL(sql string) string {
	s := strings.TrimSpace(sql)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

func statementHash(sql string) string {
	h := sha256.Sum256([]byte(normalizeSQL(sql)))
	return fmt.Sprintf("%x", h)
}

// classifyPG 对 PostgreSQL SQL 进行 AST 分类。
func classifyPG(sql string) ClassificationResult {
	normalized := normalizeSQL(sql)
	if normalized == "" {
		return ClassificationResult{
			StatementKind: StmtUnknown,
			StatementHash: statementHash(sql),
			ParseError:    fmt.Errorf("empty sql"),
		}
	}

	stmts, err := pg.Parse(sql)
	if err != nil {
		return ClassificationResult{
			StatementKind: StmtUnknown,
			StatementHash: statementHash(sql),
			ParseError:    err,
		}
	}

	stmtCount := len(stmts)
	if stmtCount == 0 {
		return ClassificationResult{
			StatementKind: StmtUnknown,
			StatementHash: statementHash(sql),
			ParseError:    fmt.Errorf("no statements parsed"),
		}
	}
	if stmtCount > 1 {
		return ClassificationResult{
			StatementKind:  StmtUnknown,
			StatementHash:  statementHash(sql),
			StatementCount: stmtCount,
		}
	}

	stmt := stmts[0]
	if stmt.AST == nil || stmt.Empty() {
		return ClassificationResult{
			StatementKind: StmtUnknown,
			StatementHash: statementHash(sql),
			ParseError:    fmt.Errorf("empty statement"),
		}
	}

	kind, features := classifyPGAST(stmt.AST)
	return ClassificationResult{
		StatementKind:  kind,
		ASTFeatures:    features,
		StatementHash:  statementHash(sql),
		StatementCount: 1,
	}
}

func classifyPGAST(node pgast.Node) (StatementKind, ASTFeatures) {
	var features ASTFeatures

	switch n := node.(type) {
	case *pgast.SelectStmt:
		return classifyPGSelect(n)

	case *pgast.InsertStmt:
		return StmtInsert, features

	case *pgast.UpdateStmt:
		return StmtUpdate, features

	case *pgast.DeleteStmt:
		return StmtDelete, features

	case *pgast.ExplainStmt:
		return classifyPGExplain(n)

	case *pgast.CreateStmt, *pgast.DropStmt, *pgast.AlterTableStmt,
		*pgast.AlterTableMoveAllStmt, *pgast.CreateSchemaStmt,
		*pgast.ViewStmt, *pgast.IndexStmt, *pgast.TruncateStmt,
		*pgast.GrantStmt:
		return StmtDDL, features

	case *pgast.CallStmt, *pgast.DoStmt:
		return StmtCall, features

	case *pgast.TransactionStmt:
		return StmtTransaction, features

	case *pgast.VariableSetStmt, *pgast.VariableShowStmt,
		*pgast.PrepareStmt, *pgast.ExecuteStmt,
		*pgast.CopyStmt, *pgast.VacuumStmt:
		return StmtOther, features

	default:
		return StmtUnknown, features
	}
}

func classifyPGSelect(n *pgast.SelectStmt) (StatementKind, ASTFeatures) {
	var features ASTFeatures

	// WITH 子句 (CTE)
	if n.WithClause != nil && n.WithClause.Ctes != nil && n.WithClause.Ctes.Len() > 0 {
		features.HasCTE = true
		if n.WithClause.Recursive {
			features.HasRecursiveCTE = true
		}
		if hasModifyingCTEPG(n.WithClause.Ctes) {
			features.HasModifyingCTE = true
		}
	}

	// 集合操作 (UNION/INTERSECT/EXCEPT)
	if n.Op != pgast.SETOP_NONE {
		features.HasSetOperation = true
		if n.Larg != nil {
			_, lf := classifyPGSelect(n.Larg)
			features = mergePGFeatures(features, lf)
		}
		if n.Rarg != nil {
			_, rf := classifyPGSelect(n.Rarg)
			features = mergePGFeatures(features, rf)
		}
	}

	// 锁定子句
	if n.LockingClause != nil && n.LockingClause.Len() > 0 {
		features.HasLockingClause = true
	}

	// SELECT INTO
	if n.IntoClause != nil {
		features.HasSelectInto = true
	}

	// [CR #6] 递归检测危险函数调用 (setval, lo_create, SECURITY DEFINER 等)
	if hasDangerousPGFunc(n) {
		features.HasDangerousFunc = true
	}

	return StmtSelect, features
}

func classifyPGExplain(n *pgast.ExplainStmt) (StatementKind, ASTFeatures) {
	var features ASTFeatures

	// 检查 ANALYZE 选项
	if n.Options != nil {
		for _, item := range n.Options.Items {
			if elem, ok := item.(*pgast.DefElem); ok && elem.Defname == "analyze" {
				features.HasExplainAnalyze = true
				break
			}
		}
	}

	// 检查 EXPLAIN 目标
	if n.Query != nil {
		// Query 可能是 RawStmt 包装或直接 Node
		target := n.Query
		if raw, ok := target.(*pgast.RawStmt); ok {
			target = raw.Stmt
		}
		switch target.(type) {
		case *pgast.SelectStmt:
			// EXPLAIN SELECT
		case *pgast.ExplainStmt:
			features.HasNestedExplain = true
		default:
			features.HasExplainDMLDDL = true
		}
	}

	return StmtExplain, features
}

func hasModifyingCTEPG(ctes *pgast.List) bool {
	if ctes == nil {
		return false
	}
	for _, item := range ctes.Items {
		cte, ok := item.(*pgast.CommonTableExpr)
		if !ok {
			continue
		}
		stmt := cte.Ctequery
		if raw, ok := stmt.(*pgast.RawStmt); ok {
			stmt = raw.Stmt
		}
		switch stmt.(type) {
		case *pgast.InsertStmt, *pgast.UpdateStmt, *pgast.DeleteStmt, *pgast.MergeStmt:
			return true
		}
	}
	return false
}

// hasDangerousPGFunc 递归检测 PG AST 中的危险函数调用 [CR #6]。
// 包括 setval, lo_create, lo_import 等可副作用写入的函数。
func hasDangerousPGFunc(n pgast.Node) bool {
	found := false
	pgast.Inspect(n, func(n pgast.Node) bool {
		if fc, ok := n.(*pgast.FuncCall); ok {
			name := strings.ToLower(fcName(fc))
			if name == "setval" || name == "lo_create" ||
				name == "lo_import" || name == "lo_unlink" ||
				name == "pg_catalog.lo_create" || name == "pg_catalog.lo_import" ||
				name == "nextval" || name == "pg_catalog.setval" {
				found = true
				return false // 停止遍历
			}
		}
		return true
	})
	return found
}

// fcName 从 FuncCall 提取函数名。
func fcName(fc *pgast.FuncCall) string {
	if fc.Funcname == nil {
		return ""
	}
	var parts []string
	for _, item := range fc.Funcname.Items {
		if sv, ok := item.(*pgast.String); ok {
			parts = append(parts, sv.Str)
		}
	}
	return strings.Join(parts, ".")
}

func mergePGFeatures(a, b ASTFeatures) ASTFeatures {
	a.HasCTE = a.HasCTE || b.HasCTE
	a.HasRecursiveCTE = a.HasRecursiveCTE || b.HasRecursiveCTE
	a.HasSetOperation = a.HasSetOperation || b.HasSetOperation
	a.HasLockingClause = a.HasLockingClause || b.HasLockingClause
	a.HasSelectInto = a.HasSelectInto || b.HasSelectInto
	a.HasModifyingCTE = a.HasModifyingCTE || b.HasModifyingCTE
	a.HasDangerousFunc = a.HasDangerousFunc || b.HasDangerousFunc
	return a
}

// ---- MySQL classifier ----

func classifyMySQL(sql string) ClassificationResult {
	normalized := normalizeSQL(sql)
	if normalized == "" {
		return ClassificationResult{
			StatementKind: StmtUnknown,
			StatementHash: statementHash(sql),
			ParseError:    fmt.Errorf("empty sql"),
		}
	}

	list, err := mysqlparser.Parse(sql)
	if err != nil {
		return ClassificationResult{
			StatementKind: StmtUnknown,
			StatementHash: statementHash(sql),
			ParseError:    err,
		}
	}

	stmtCount := list.Len()
	if stmtCount == 0 {
		return ClassificationResult{
			StatementKind: StmtUnknown,
			StatementHash: statementHash(sql),
			ParseError:    fmt.Errorf("no statements parsed"),
		}
	}
	if stmtCount > 1 {
		return ClassificationResult{
			StatementKind:  StmtUnknown,
			StatementHash:  statementHash(sql),
			StatementCount: stmtCount,
		}
	}

	node := list.Items[0]
	if node == nil {
		return ClassificationResult{
			StatementKind: StmtUnknown,
			StatementHash: statementHash(sql),
			ParseError:    fmt.Errorf("nil statement node"),
		}
	}

	kind, features := classifyMySQLAST(node)
	return ClassificationResult{
		StatementKind:  kind,
		ASTFeatures:    features,
		StatementHash:  statementHash(sql),
		StatementCount: 1,
	}
}

func classifyMySQLAST(node mysqlast.Node) (StatementKind, ASTFeatures) {
	var features ASTFeatures

	switch n := node.(type) {
	case *mysqlast.SelectStmt:
		return classifyMySQLSelect(n)

	case *mysqlast.InsertStmt:
		return StmtInsert, features

	case *mysqlast.UpdateStmt:
		return StmtUpdate, features

	case *mysqlast.DeleteStmt:
		return StmtDelete, features

	case *mysqlast.CreateTableStmt, *mysqlast.CreateIndexStmt,
		*mysqlast.CreateViewStmt, *mysqlast.CreateDatabaseStmt,
		*mysqlast.AlterTableStmt, *mysqlast.AlterDatabaseStmt,
		*mysqlast.DropTableStmt, *mysqlast.DropDatabaseStmt,
		*mysqlast.DropIndexStmt, *mysqlast.DropViewStmt,
		*mysqlast.TruncateStmt, *mysqlast.RenameTableStmt,
		*mysqlast.GrantStmt, *mysqlast.RevokeStmt,
		*mysqlast.CreateUserStmt, *mysqlast.DropUserStmt,
		*mysqlast.AlterUserStmt, *mysqlast.RenameUserStmt,
		*mysqlast.CreateRoleStmt, *mysqlast.DropRoleStmt,
		*mysqlast.CreateFunctionStmt, *mysqlast.CreateTriggerStmt,
		*mysqlast.CreateEventStmt, *mysqlast.AlterViewStmt,
		*mysqlast.AlterEventStmt, *mysqlast.AlterRoutineStmt,
		*mysqlast.DropRoutineStmt, *mysqlast.DropTriggerStmt,
		*mysqlast.DropEventStmt,
		*mysqlast.CreateTablespaceStmt, *mysqlast.AlterTablespaceStmt,
		*mysqlast.DropTablespaceStmt,
		*mysqlast.CreateServerStmt, *mysqlast.AlterServerStmt,
		*mysqlast.DropServerStmt:
		return StmtDDL, features

	case *mysqlast.ExplainStmt:
		return classifyMySQLExplain(n)

	case *mysqlast.CallStmt, *mysqlast.DoStmt,
		*mysqlast.BeginEndBlock:
		return StmtCall, features

	case *mysqlast.BeginStmt, *mysqlast.CommitStmt,
		*mysqlast.RollbackStmt, *mysqlast.SavepointStmt,
		*mysqlast.SetTransactionStmt, *mysqlast.XAStmt:
		return StmtTransaction, features

	case *mysqlast.SetStmt, *mysqlast.SetPasswordStmt,
		*mysqlast.SetDefaultRoleStmt, *mysqlast.SetRoleStmt,
		*mysqlast.GrantRoleStmt, *mysqlast.RevokeRoleStmt,
		*mysqlast.ShowStmt, *mysqlast.UseStmt,
		*mysqlast.PrepareStmt, *mysqlast.ExecuteStmt,
		*mysqlast.DeallocateStmt,
		*mysqlast.LoadDataStmt,
		*mysqlast.LockTablesStmt, *mysqlast.UnlockTablesStmt,
		*mysqlast.FlushStmt, *mysqlast.KillStmt,
		*mysqlast.HandlerOpenStmt, *mysqlast.HandlerReadStmt,
		*mysqlast.HandlerCloseStmt,
		*mysqlast.AnalyzeTableStmt, *mysqlast.OptimizeTableStmt,
		*mysqlast.CheckTableStmt, *mysqlast.RepairTableStmt,
		*mysqlast.ChecksumTableStmt, *mysqlast.ShutdownStmt,
		*mysqlast.RestartStmt,
		*mysqlast.ValuesStmt,
		*mysqlast.SignalStmt, *mysqlast.ResignalStmt,
		*mysqlast.GetDiagnosticsStmt,
		*mysqlast.CloneStmt,
		*mysqlast.InstallPluginStmt, *mysqlast.UninstallPluginStmt,
		*mysqlast.InstallComponentStmt, *mysqlast.UninstallComponentStmt,
		*mysqlast.CreateSpatialRefSysStmt, *mysqlast.DropSpatialRefSysStmt,
		*mysqlast.CreateResourceGroupStmt, *mysqlast.AlterResourceGroupStmt,
		*mysqlast.DropResourceGroupStmt,
		*mysqlast.CreateLogfileGroupStmt, *mysqlast.AlterLogfileGroupStmt,
		*mysqlast.DropLogfileGroupStmt,
		*mysqlast.AlterInstanceStmt, *mysqlast.LockInstanceStmt,
		*mysqlast.UnlockInstanceStmt,
		*mysqlast.ImportTableStmt, *mysqlast.BinlogStmt,
		*mysqlast.CacheIndexStmt, *mysqlast.LoadIndexIntoCacheStmt,
		*mysqlast.ResetPersistStmt, *mysqlast.SetResourceGroupStmt,
		*mysqlast.HelpStmt,
		*mysqlast.ChangeReplicationSourceStmt, *mysqlast.ChangeReplicationFilterStmt,
		*mysqlast.StartReplicaStmt, *mysqlast.StopReplicaStmt,
		*mysqlast.ResetReplicaStmt, *mysqlast.PurgeBinaryLogsStmt,
		*mysqlast.ResetMasterStmt, *mysqlast.StartGroupReplicationStmt,
		*mysqlast.StopGroupReplicationStmt:
		return StmtOther, features

	case *mysqlast.TableStmt:
		return StmtSelect, features

	default:
		return StmtUnknown, features
	}
}

func classifyMySQLSelect(n *mysqlast.SelectStmt) (StatementKind, ASTFeatures) {
	var features ASTFeatures

	if len(n.CTEs) > 0 {
		features.HasCTE = true
		for _, cte := range n.CTEs {
			if cte.Recursive {
				features.HasRecursiveCTE = true
			}
			// [CR round2] 递归检查 CTE 内部 SELECT 的赋值
			if cte.Select != nil {
				_, cteFeat := classifyMySQLSelect(cte.Select)
				features = mergeMySQLFeatures(features, cteFeat)
			}
		}
		// [CR #10] MySQL DML CTE 被 Omni parser 拒绝为 parse error → fail-closed
	}

	if n.SetOp != mysqlast.SetOpNone {
		features.HasSetOperation = true
		if n.Left != nil {
			_, lf := classifyMySQLSelect(n.Left)
			features = mergeMySQLFeatures(features, lf)
		}
		if n.Right != nil {
			_, rf := classifyMySQLSelect(n.Right)
			features = mergeMySQLFeatures(features, rf)
		}
	}

	if n.ForUpdate != nil {
		features.HasLockingClause = true
	}

	if n.Into != nil {
		if n.Into.Outfile != "" || n.Into.Dumpfile != "" {
			features.HasIntoOutfile = true
		}
		if len(n.Into.Vars) > 0 {
			features.HasIntoVar = true
		}
	}

	// [CR round2] 递归检测赋值：TargetList + WHERE + HAVING + GROUP BY + ORDER BY + FROM 条件
	if hasAssignmentInExprs(n.TargetList) ||
		hasAssignmentInExpr(n.Where) ||
		hasAssignmentInExpr(n.Having) ||
		hasAssignmentInExprs(n.GroupBy) ||
		hasAssignmentInOrderBy(n.OrderBy) ||
		hasAssignmentInFrom(n.From) {
		features.HasAssignment = true
	}

	return StmtSelect, features
}

func classifyMySQLExplain(n *mysqlast.ExplainStmt) (StatementKind, ASTFeatures) {
	var features ASTFeatures

	if n.Analyze {
		// MySQL EXPLAIN ANALYZE — 实际执行，需拒绝
		features.HasExplainAnalyze = true
	}

	if n.Stmt != nil {
		switch n.Stmt.(type) {
		case *mysqlast.SelectStmt:
			// EXPLAIN SELECT
		case *mysqlast.ExplainStmt:
			features.HasNestedExplain = true
		default:
			features.HasExplainDMLDDL = true
		}
	}

	return StmtExplain, features
}

func hasModifyingCTEMySQL(ctes []*mysqlast.CommonTableExpr) bool {
	// [CR #10] 修改型 CTE 由 Omni parser parse error 兜底，本函数仅占位。
	return false
}

// hasAssignmentInExprs 检查表达式列表中是否有 BinOpAssign [CR round2]。
func hasAssignmentInExprs(exprs []mysqlast.ExprNode) bool {
	for _, e := range exprs {
		if hasAssignmentInExpr(e) {
			return true
		}
	}
	return false
}

// hasAssignmentInExpr 递归检查单个表达式树中的 BinOpAssign [CR #18]。
// 覆盖：ParenExpr / BinaryExpr / FuncCallExpr.Args / CaseExpr 各分支 / SubqueryExpr。
func hasAssignmentInExpr(e mysqlast.ExprNode) bool {
	if e == nil {
		return false
	}
	if paren, ok := e.(*mysqlast.ParenExpr); ok {
		if hasAssignmentInExpr(paren.Expr) {
			return true
		}
	}
	if bin, ok := e.(*mysqlast.BinaryExpr); ok {
		if bin.Op == mysqlast.BinOpAssign {
			return true
		}
		if hasAssignmentInExpr(bin.Left) || hasAssignmentInExpr(bin.Right) {
			return true
		}
	}
	// [CR #18] FuncCallExpr: 递归检查函数参数（如 IF(@x:=1, ...)、COALESCE(@x:=1, ...)）
	if fc, ok := e.(*mysqlast.FuncCallExpr); ok {
		for _, arg := range fc.Args {
			if hasAssignmentInExpr(arg) {
				return true
			}
		}
	}
	// [CR #18] CaseExpr: 检查 Operand / Whens(Cond, Result) / Default
	if ce, ok := e.(*mysqlast.CaseExpr); ok {
		if hasAssignmentInExpr(ce.Operand) {
			return true
		}
		for _, wh := range ce.Whens {
			if hasAssignmentInExpr(wh.Cond) || hasAssignmentInExpr(wh.Result) {
				return true
			}
		}
		if hasAssignmentInExpr(ce.Default) {
			return true
		}
	}
	if sub, ok := e.(*mysqlast.SubqueryExpr); ok && sub.Select != nil {
		_, feat := classifyMySQLSelect(sub.Select)
		if feat.HasAssignment {
			return true
		}
	}
	return false
}

// hasAssignmentInOrderBy 检查 ORDER BY 项。
func hasAssignmentInOrderBy(items []*mysqlast.OrderByItem) bool {
	for _, item := range items {
		if item != nil && hasAssignmentInExpr(item.Expr) {
			return true
		}
	}
	return false
}

// hasAssignmentInFrom 检查 FROM 子句中的 JOIN ON 条件、派生表子查询及嵌套 JOIN [CR #18]。
func hasAssignmentInFrom(from []mysqlast.TableExpr) bool {
	for _, te := range from {
		if jc, ok := te.(*mysqlast.JoinClause); ok {
			if hasAssignmentInJoinClause(jc) {
				return true
			}
		}
		// [CR #18] 派生表: (SELECT @x:=1 ...) AS t
		if sub, ok := te.(*mysqlast.SubqueryExpr); ok && sub.Select != nil {
			_, feat := classifyMySQLSelect(sub.Select)
			if feat.HasAssignment {
				return true
			}
		}
	}
	return false
}

// hasAssignmentInJoinClause 递归检查 JOIN 子树的 ON 条件和嵌套 JOIN [CR #18]。
func hasAssignmentInJoinClause(jc *mysqlast.JoinClause) bool {
	if jc == nil {
		return false
	}
	// ON 条件
	if on, ok := jc.Condition.(*mysqlast.OnCondition); ok && on != nil {
		if hasAssignmentInExpr(on.Expr) {
			return true
		}
	}
	// 递归检查嵌套 JOIN (Left/Right 可以是另一个 JoinClause)
	if left, ok := jc.Left.(*mysqlast.JoinClause); ok {
		if hasAssignmentInJoinClause(left) {
			return true
		}
	} else if leftSub, ok := jc.Left.(*mysqlast.SubqueryExpr); ok && leftSub.Select != nil {
		_, feat := classifyMySQLSelect(leftSub.Select)
		if feat.HasAssignment {
			return true
		}
	}
	if right, ok := jc.Right.(*mysqlast.JoinClause); ok {
		if hasAssignmentInJoinClause(right) {
			return true
		}
	} else if rightSub, ok := jc.Right.(*mysqlast.SubqueryExpr); ok && rightSub.Select != nil {
		_, feat := classifyMySQLSelect(rightSub.Select)
		if feat.HasAssignment {
			return true
		}
	}
	return false
}

func mergeMySQLFeatures(a, b ASTFeatures) ASTFeatures {
	a.HasCTE = a.HasCTE || b.HasCTE
	a.HasRecursiveCTE = a.HasRecursiveCTE || b.HasRecursiveCTE
	a.HasSetOperation = a.HasSetOperation || b.HasSetOperation
	a.HasLockingClause = a.HasLockingClause || b.HasLockingClause
	a.HasIntoOutfile = a.HasIntoOutfile || b.HasIntoOutfile
	a.HasIntoVar = a.HasIntoVar || b.HasIntoVar
	a.HasAssignment = a.HasAssignment || b.HasAssignment
	a.HasModifyingCTE = a.HasModifyingCTE || b.HasModifyingCTE
	return a
}

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

func mergePGFeatures(a, b ASTFeatures) ASTFeatures {
	a.HasCTE = a.HasCTE || b.HasCTE
	a.HasRecursiveCTE = a.HasRecursiveCTE || b.HasRecursiveCTE
	a.HasSetOperation = a.HasSetOperation || b.HasSetOperation
	a.HasLockingClause = a.HasLockingClause || b.HasLockingClause
	a.HasSelectInto = a.HasSelectInto || b.HasSelectInto
	a.HasModifyingCTE = a.HasModifyingCTE || b.HasModifyingCTE
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
		}
		// [CR #10] MySQL DML CTE (WITH d AS (DELETE...)) 被 Omni parser
		// 拒绝为 parse error → fail-closed，无需额外检查。
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

	if hasAssignmentInTargets(n.TargetList) {
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
	// [CR #10] MySQL 修改型 CTE 被 Omni parser 拒绝（parse error），
	// 此函数保留作为结构完整性占位。参见 classifyMySQLSelect 中的注释。
	return false
}

func hasAssignmentInTargets(targets []mysqlast.ExprNode) bool {
	for _, t := range targets {
		if bin, ok := t.(*mysqlast.BinaryExpr); ok && bin.Op == mysqlast.BinOpAssign {
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

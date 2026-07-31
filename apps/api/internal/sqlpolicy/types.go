// Package sqlpolicy 提供方言感知的 SQL 安全策略判断。
//
// 组件：
//   - ecm_lexer.go: WebDB 自有 MySQL ECM lexer（确定性词法状态机）
//   - classifier.go: 基于 Omni AST 的方言分类器
//   - policy.go: 策略决策引擎（lexer → AST → PolicyDecision）
//
// ADR-007: 方言 AST 解析，未知即拒绝。
package sqlpolicy

// Dialect 方言标识 —— 从服务端 Connection.Engine 派生，不接受客户端输入。
type Dialect string

const (
	DialectPostgreSQL Dialect = "postgresql"
	DialectMySQL      Dialect = "mysql"
)

// StatementKind 顶层语句类型。
type StatementKind string

const (
	StmtSelect      StatementKind = "SELECT"
	StmtExplain     StatementKind = "EXPLAIN"
	StmtInsert      StatementKind = "INSERT"
	StmtUpdate      StatementKind = "UPDATE"
	StmtDelete      StatementKind = "DELETE"
	StmtDDL         StatementKind = "DDL"
	StmtCall        StatementKind = "CALL"
	StmtTransaction StatementKind = "TRANSACTION"
	StmtOther       StatementKind = "OTHER"
	StmtUnknown     StatementKind = "UNKNOWN"
)

// LexicalFeatures AST 解析前的词法事实。
// PostgreSQL 使用零值；MySQL 必须先完成 ECM lexer 步骤。
type LexicalFeatures struct {
	HasExecComment bool // 可执行 SQL 上下文中的 MySQL /*!...*/ 可执行注释
}

// ASTFeatures AST 特征标记（可与 StatementKind 叠加）。
type ASTFeatures struct {
	HasCTE            bool // WITH 子句
	HasRecursiveCTE   bool // WITH RECURSIVE
	HasSetOperation   bool // UNION/INTERSECT/EXCEPT
	HasLockingClause  bool // FOR UPDATE/FOR SHARE 等
	HasSelectInto     bool // PG SELECT INTO
	HasIntoOutfile    bool // MySQL INTO OUTFILE/DUMPFILE
	HasIntoVar        bool // MySQL SELECT ... INTO @var
	HasAssignment     bool // MySQL @x := ...
	HasExplainAnalyze bool // EXPLAIN ANALYZE
	HasModifyingCTE   bool // 数据修改 CTE
	HasExplainDMLDDL  bool // EXPLAIN 目标是 DML/DDL
	HasNestedExplain  bool // 嵌套 EXPLAIN
}

// ClassificationResult 语句分类结果。
type ClassificationResult struct {
	StatementKind   StatementKind
	LexicalFeatures LexicalFeatures
	ASTFeatures     ASTFeatures
	StatementHash   string // SHA-256(normalized_sql)
	StatementCount  int    // AST 解析出的语句数量
	LexError        error  // lexer 无法可靠判定；非 nil 时不得调用 AST parser
	ParseError      error  // AST 解析错误
}

// StableReasonCode 稳定拒绝原因码。
type StableReasonCode string

const (
	ReasonAllowed            StableReasonCode = "allowed"
	ReasonParseError         StableReasonCode = "sql_parse_error"
	ReasonMultipleStatements StableReasonCode = "multiple_statements"
	ReasonNotAllowed         StableReasonCode = "statement_not_allowed"
	ReasonUnsupported        StableReasonCode = "unsupported_statement"
	ReasonEmptySQL           StableReasonCode = "empty_sql"
	ReasonECMDetected        StableReasonCode = "executable_comment_detected"
)

// PolicyDecision 策略决策。
type PolicyDecision struct {
	Allowed        bool
	ReasonCode     StableReasonCode
	Classification ClassificationResult
}

// MySQLLexerMode 影响词法行为的 MySQL session mode。
// 只能从服务端可信连接/session 配置派生，不接受客户端输入。
type MySQLLexerMode struct {
	NoBackslashEscapes bool
	ANSIQuotes         bool
}

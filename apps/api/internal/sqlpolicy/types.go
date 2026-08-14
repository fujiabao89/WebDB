// Package sqlpolicy 提供方言感知的 SQL 安全策略判断。
//
// 组件：
//   - ecm_lexer.go: WebDB 自有 MySQL ECM lexer（确定性词法状态机）
//   - classifier.go: 基于 Omni AST 的方言分类器
//   - policy.go: 策略决策引擎（lexer → AST → PolicyDecision）
//
// ADR-007: 方言 AST 解析，未知即拒绝。
package sqlpolicy

import (
	"errors"
	"strings"
)

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
	// 危险函数：按名单匹配的写副作用函数 (setval/nextval/lo_create/lo_import/lo_unlink 等)。
	// 不覆盖用户自定义 SECURITY DEFINER 函数，该风险由执行层只读事务和最小权限账号承担。
	HasDangerousFunc bool
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

// SupportedMySQLLexerMode 返回当前 Omni AST 与 ECM lexer 共同支持的唯一
// MySQL 词法模式。生产执行层必须在同一条目标 session 上验证该模式后才执行 SQL。
func SupportedMySQLLexerMode() MySQLLexerMode {
	return MySQLLexerMode{}
}

// MySQLLexerModeFromSession 将可信目标 session 返回的 @@SESSION.sql_mode
// 投影为影响 SQL 解析的模式。仅允许 MySQL 8.0+ 中已知不会改变解析语义的 mode；
// 未知、格式异常或当前 parser 不支持的其他语法 mode 一律返回错误，以便调用方
// fail-closed。ANSI_QUOTES 与 NO_BACKSLASH_ESCAPES 返回明确的 mode，由调用方与
// SupportedMySQLLexerMode 比较。
func MySQLLexerModeFromSession(raw string) (MySQLLexerMode, error) {
	var mode MySQLLexerMode
	if raw == "" {
		return mode, nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return MySQLLexerMode{}, errors.New("mysql session sql_mode contains an empty token")
	}

	for _, part := range strings.Split(raw, ",") {
		name := strings.ToUpper(strings.TrimSpace(part))
		if name == "" {
			return MySQLLexerMode{}, errors.New("mysql session sql_mode contains an empty token")
		}
		switch name {
		case "ANSI_QUOTES":
			mode.ANSIQuotes = true
		case "NO_BACKSLASH_ESCAPES":
			mode.NoBackslashEscapes = true
		case "ANSI":
			// ANSI is a combination mode that includes ANSI_QUOTES.
			mode.ANSIQuotes = true
		case "ALLOW_INVALID_DATES",
			"ERROR_FOR_DIVISION_BY_ZERO",
			"NO_AUTO_VALUE_ON_ZERO",
			"NO_DIR_IN_CREATE",
			"NO_ENGINE_SUBSTITUTION",
			"NO_UNSIGNED_SUBTRACTION",
			"NO_ZERO_DATE",
			"NO_ZERO_IN_DATE",
			"ONLY_FULL_GROUP_BY",
			"PAD_CHAR_TO_FULL_LENGTH",
			"STRICT_ALL_TABLES",
			"STRICT_TRANS_TABLES",
			"TIME_TRUNCATE_FRACTIONAL",
			"TRADITIONAL":
			// 已知不会改变 lexer/AST 解析语义的 MySQL 8.0+ mode。
		case "HIGH_NOT_PRECEDENCE", "IGNORE_SPACE", "PIPES_AS_CONCAT", "REAL_AS_FLOAT":
			return MySQLLexerMode{}, errors.New("mysql session uses an unsupported syntax mode")
		default:
			return MySQLLexerMode{}, errors.New("mysql session uses an unknown sql_mode")
		}
	}
	return mode, nil
}

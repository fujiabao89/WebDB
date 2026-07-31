package sqlpolicy

import "fmt"

// Decide 根据方言对 SQL 进行完整安全策略判断（ADR-007）。
//
// 残余风险（Owner 已接受，见 P0-04 提案 §5.3）：
//   - AST 无法检测 SELECT func() 的副作用
//   - SECURITY DEFINER 函数可能以函数所有者权限写入
//   - 建议后续任务增加数据库层只读保护
//     (PG: default_transaction_read_only=on, MySQL: SET SESSION TRANSACTION READ ONLY)
func Decide(dialect Dialect, sql string, mode MySQLLexerMode) PolicyDecision {
	if sql == "" || normalizeSQL(sql) == "" {
		return PolicyDecision{Allowed: false, ReasonCode: ReasonEmptySQL}
	}

	// [CR #9] 显式 switch，拒绝未知方言
	switch dialect {
	case DialectPostgreSQL:
		return decidePG(sql)
	case DialectMySQL:
		return decideMySQL(sql, mode)
	default:
		return PolicyDecision{Allowed: false, ReasonCode: ReasonUnsupported}
	}
}

func decidePG(sql string) PolicyDecision {
	return applyPolicy(classifyPG(sql))
}

func decideMySQL(sql string, mode MySQLLexerMode) PolicyDecision {
	// [CR round2] Omni MySQL parser 目前不支持 mode-aware 解析；
	// 当连接配置了 ANSI_QUOTES 或 NO_BACKSLASH_ESCAPES 时，
	// 当前 parser 的分词语义与 MySQL server 不一致，
	// 因此保守拒绝。
	// TODO(WEB-13): Omni 支持 mode-aware parsing 后移除此限制。
	if mode.ANSIQuotes || mode.NoBackslashEscapes {
		return PolicyDecision{Allowed: false, ReasonCode: ReasonParseError,
			Classification: ClassificationResult{
				LexError: fmt.Errorf("mysql parser does not support session mode: ANSI_QUOTES=%v NO_BACKSLASH_ESCAPES=%v",
					mode.ANSIQuotes, mode.NoBackslashEscapes),
			}}
	}

	l := newECMLexer(sql, mode)
	hasECM, err := l.scan()
	if err != nil {
		return PolicyDecision{Allowed: false, ReasonCode: ReasonParseError,
			Classification: ClassificationResult{LexError: err}}
	}
	if hasECM {
		return PolicyDecision{Allowed: false, ReasonCode: ReasonECMDetected,
			Classification: ClassificationResult{LexicalFeatures: LexicalFeatures{HasExecComment: true}}}
	}
	result := classifyMySQL(sql)
	result.LexicalFeatures = LexicalFeatures{HasExecComment: false}
	return applyPolicy(result)
}

func applyPolicy(result ClassificationResult) PolicyDecision {
	if result.ParseError != nil || result.LexError != nil {
		return PolicyDecision{Allowed: false, ReasonCode: ReasonParseError, Classification: result}
	}
	if result.StatementCount > 1 {
		return PolicyDecision{Allowed: false, ReasonCode: ReasonMultipleStatements, Classification: result}
	}
	if result.StatementKind == StmtUnknown {
		return PolicyDecision{Allowed: false, ReasonCode: ReasonUnsupported, Classification: result}
	}
	if result.StatementKind != StmtSelect && result.StatementKind != StmtExplain {
		return PolicyDecision{Allowed: false, ReasonCode: ReasonNotAllowed, Classification: result}
	}
	feat := result.ASTFeatures
	if feat.HasLockingClause || feat.HasSelectInto || feat.HasIntoOutfile ||
		feat.HasIntoVar || feat.HasAssignment || feat.HasExplainAnalyze ||
		feat.HasModifyingCTE || feat.HasExplainDMLDDL || feat.HasNestedExplain ||
		feat.HasDangerousFunc {
		return PolicyDecision{Allowed: false, ReasonCode: ReasonNotAllowed, Classification: result}
	}
	return PolicyDecision{Allowed: true, ReasonCode: ReasonAllowed, Classification: result}
}

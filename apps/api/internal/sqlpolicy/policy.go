package sqlpolicy

// Decide 根据方言对 SQL 进行完整安全策略判断。
//
// ADR-007 处理顺序：
//
//	MySQL: ECM lexer → (无 ECM) → Omni MySQL AST → 分类 → PolicyDecision
//	PostgreSQL: 直接 Omni PG AST → 分类 → PolicyDecision
func Decide(dialect Dialect, sql string, mode MySQLLexerMode) PolicyDecision {
	if sql == "" || normalizeSQL(sql) == "" {
		return PolicyDecision{Allowed: false, ReasonCode: ReasonEmptySQL}
	}

	var lexFeatures LexicalFeatures

	if dialect == DialectMySQL {
		l := newECMLexer(sql, mode)
		hasECM, err := l.scan()
		if err != nil {
			return PolicyDecision{
				Allowed:    false,
				ReasonCode: ReasonParseError,
				Classification: ClassificationResult{
					LexError: err,
				},
			}
		}
		if hasECM {
			return PolicyDecision{
				Allowed:    false,
				ReasonCode: ReasonECMDetected,
				Classification: ClassificationResult{
					LexicalFeatures: LexicalFeatures{HasExecComment: true},
				},
			}
		}
		lexFeatures = LexicalFeatures{HasExecComment: false}
	}

	var result ClassificationResult
	if dialect == DialectPostgreSQL {
		result = classifyPG(sql)
	} else {
		result = classifyMySQL(sql)
	}
	result.LexicalFeatures = lexFeatures

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
		feat.HasModifyingCTE || feat.HasExplainDMLDDL || feat.HasNestedExplain {
		return PolicyDecision{Allowed: false, ReasonCode: ReasonNotAllowed, Classification: result}
	}

	return PolicyDecision{Allowed: true, ReasonCode: ReasonAllowed, Classification: result}
}

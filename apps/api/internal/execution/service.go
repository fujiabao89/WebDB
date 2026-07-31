// Package execution 提供 SQL 执行管线集成。
// 安全顺序: 可信身份验证 → SQL policy → 仅 Allowed 后 Adapter → 超时/取消 → 审计。
package execution

import (
	"github.com/fujiabao89/webdb/internal/sqlpolicy"
	"github.com/google/uuid"
)

type AuthenticatedPrincipal struct {
	UserID      uuid.UUID
	WorkspaceID uuid.UUID
}

type StableErrorCode string

const (
	ErrInvalidScope        StableErrorCode = "invalid_scope"
	ErrForbidden           StableErrorCode = "forbidden"
	ErrConnectionNotFound  StableErrorCode = "connection_not_found"
	ErrPolicyNotConfigured StableErrorCode = "policy_not_configured"
	ErrReadNotAllowed      StableErrorCode = "read_not_allowed"
	ErrInternalError       StableErrorCode = "internal_error"
)

type Engine string

const (
	EnginePostgreSQL Engine = "postgresql"
	EngineMySQL      Engine = "mysql"
)

type Environment string

const (
	EnvDevelopment Environment = "development"
	EnvStaging     Environment = "staging"
	EnvProduction  Environment = "production"
)

// EvaluateSQL 对 SQL 进行策略评估（不连接目标数据库）。
// 返回 PolicyDecision 和映射后的业务错误码。
func EvaluateSQL(engine Engine, sql string) (sqlpolicy.PolicyDecision, StableErrorCode) {
	dialect := mapDialect(engine)
	decision := sqlpolicy.Decide(dialect, sql, sqlpolicy.MySQLLexerMode{})

	if decision.Allowed {
		return decision, ""
	}
	return decision, mapPolicyReason(decision.ReasonCode)
}

func mapDialect(e Engine) sqlpolicy.Dialect {
	switch e {
	case EnginePostgreSQL:
		return sqlpolicy.DialectPostgreSQL
	case EngineMySQL:
		return sqlpolicy.DialectMySQL
	default:
		return sqlpolicy.DialectMySQL
	}
}

func mapPolicyReason(r sqlpolicy.StableReasonCode) StableErrorCode {
	switch r {
	case sqlpolicy.ReasonParseError, sqlpolicy.ReasonMultipleStatements,
		sqlpolicy.ReasonNotAllowed, sqlpolicy.ReasonUnsupported,
		sqlpolicy.ReasonECMDetected, sqlpolicy.ReasonEmptySQL:
		return StableErrorCode(r)
	default:
		return ErrInternalError
	}
}

// Package execution 提供 SQL 执行管线集成。
// 安全顺序: 可信身份验证 → SQL policy → 仅 Allowed 后 Adapter → 超时/取消 → 审计。
package execution

import (
	"github.com/fujiabao89/webdb/internal/sqlpolicy"
	"github.com/google/uuid"
)

// AuthenticatedPrincipal 由可信上游提供的已验证身份。
type AuthenticatedPrincipal struct {
	UserID      uuid.UUID
	WorkspaceID uuid.UUID
}

// StableErrorCode 执行服务稳定错误码。
type StableErrorCode string

func (e StableErrorCode) Error() string { return string(e) }

const (
	ErrInvalidScope             StableErrorCode = "invalid_scope"
	ErrForbidden                StableErrorCode = "forbidden"
	ErrConnectionNotFound       StableErrorCode = "connection_not_found"
	ErrPolicyNotConfigured      StableErrorCode = "policy_not_configured"
	ErrReadNotAllowed           StableErrorCode = "read_not_allowed"
	ErrInternalError            StableErrorCode = "internal_error"
	ErrUnsupportedEngine        StableErrorCode = "unsupported_engine"
	ErrRateLimited              StableErrorCode = "rate_limited"
	ErrConnectionBusy           StableErrorCode = "connection_busy"
	ErrExecutionTimeout         StableErrorCode = "execution_timeout"
	ErrExecutionCancelled       StableErrorCode = "execution_cancelled"
	ErrUnsupportedQuery         StableErrorCode = "unsupported_query"
	ErrConnectionConfigConflict StableErrorCode = "connection_config_conflict"
)

// Engine 数据库引擎。
type Engine string

const (
	EnginePostgreSQL Engine = "postgresql"
	EngineMySQL      Engine = "mysql"
)

// Environment 连接环境。
type Environment string

const (
	EnvDevelopment Environment = "development"
	EnvStaging     Environment = "staging"
	EnvProduction  Environment = "production"
)

// EvaluateSQL 对 SQL 进行策略评估（不连接目标数据库）。
// mode 仅对 MySQL 生效；必须从服务端可信连接/session 配置派生，
// 不接受客户端输入（ADR-007）。
func EvaluateSQL(engine Engine, sql string, mode sqlpolicy.MySQLLexerMode) (sqlpolicy.PolicyDecision, StableErrorCode) {
	dialect, err := mapDialect(engine)
	if err != nil {
		return sqlpolicy.PolicyDecision{Allowed: false, ReasonCode: sqlpolicy.ReasonUnsupported},
			ErrUnsupportedEngine
	}

	decision := sqlpolicy.Decide(dialect, sql, mode)

	if decision.Allowed {
		return decision, ""
	}
	return decision, mapPolicyReason(decision.ReasonCode)
}

// mapDialect 将 Engine 映射为 sqlpolicy.Dialect。
// 未知引擎返回错误，不得 fallback 到 MySQL [CR #1]。
func mapDialect(e Engine) (sqlpolicy.Dialect, error) {
	switch e {
	case EnginePostgreSQL:
		return sqlpolicy.DialectPostgreSQL, nil
	case EngineMySQL:
		return sqlpolicy.DialectMySQL, nil
	default:
		return "", ErrUnsupportedEngine
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

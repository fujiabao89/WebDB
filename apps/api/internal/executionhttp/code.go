// Package executionhttp 提供 P0-06B 的公开 HTTP 传输层（P0-06A §5-§12）。
//
// 稳定错误码与 envelope 是本包的公共契约（对齐 P0-06A §12.1 Owner 批准码表）。
// 错误响应为固定安全摘要：禁止包含 SQL、Args、token、host、凭证、原始数据库
// 错误、parser 原始错误、内部 Go error 或 trace_id。
package executionhttp

import "net/http"

// StableErrorCode 稳定错误码（P0-06A §12.1，Owner 已批准码表）。
// 错误响应体仅返回 code 与固定 message（message=code），原始根因只进服务端日志。
type StableErrorCode string

func (e StableErrorCode) Error() string { return string(e) }

const (
	// ErrInvalidRequest 请求体/JSON/字段非法。
	ErrInvalidRequest StableErrorCode = "invalid_request"
	// ErrInvalidScope 作用域无效（UUID 非法、必填标识符缺失）。
	ErrInvalidScope StableErrorCode = "invalid_scope"
	// ErrUnauthorized 未认证/无有效 Principal。
	ErrUnauthorized StableErrorCode = "unauthorized"
	// ErrForbidden 非成员/角色不足 / workspace 不匹配。
	ErrForbidden StableErrorCode = "forbidden"
	// ErrConnectionNotFound 连接不存在/跨工作区/不可见（防枚举）。
	ErrConnectionNotFound StableErrorCode = "connection_not_found"
	// ErrPolicyNotConfigured 策略缺失或关键安全上限无效（404 防枚举）。
	ErrPolicyNotConfigured StableErrorCode = "policy_not_configured"
	// ErrReadNotAllowed 存在策略但 AllowRead≠true。
	ErrReadNotAllowed StableErrorCode = "read_not_allowed"
	// ErrInvalidPageToken token 无效/过期/重放/scope 不匹配。
	ErrInvalidPageToken StableErrorCode = "invalid_page_token"
	// ErrEmptySQL 空 SQL。
	ErrEmptySQL StableErrorCode = "empty_sql"
	// ErrSQLParseError lexer/parser 无法可靠判定。
	ErrSQLParseError StableErrorCode = "sql_parse_error"
	// ErrMultipleStatements 检测到多语句。
	ErrMultipleStatements StableErrorCode = "multiple_statements"
	// ErrStatementNotAllowed 语句类型不允许或含未绑定位置占位符。
	ErrStatementNotAllowed StableErrorCode = "statement_not_allowed"
	// ErrExecutableCommentDetected MySQL 可执行注释。
	ErrExecutableCommentDetected StableErrorCode = "executable_comment_detected"
	// ErrUnsupportedStatement 解析器无法识别 AST 节点。
	ErrUnsupportedStatement StableErrorCode = "unsupported_statement"
	// ErrUnsupportedQuery 需要分页但无唯一排序证明。
	ErrUnsupportedQuery StableErrorCode = "unsupported_query"
	// ErrQueryTimeout 查询超时（504）。
	ErrQueryTimeout StableErrorCode = "query_timeout"
	// ErrQueryCancelled 查询已取消（499，nginx 惯例）。
	ErrQueryCancelled StableErrorCode = "query_cancelled"
	// ErrRateLimited 速率限制（429 + Retry-After）。
	ErrRateLimited StableErrorCode = "rate_limited"
	// ErrConnectionBusy 连接忙（429 + Retry-After）。
	ErrConnectionBusy StableErrorCode = "connection_busy"
	// ErrResultTooLarge 结果超出硬上限（不静默截断）。
	ErrResultTooLarge StableErrorCode = "result_too_large"
	// ErrPaginationCapacityExhausted 分页容量耗尽（429 + Retry-After）。
	ErrPaginationCapacityExhausted StableErrorCode = "pagination_capacity_exhausted"
	// ErrConnectionUnavailable 凭证/KEK/pool/config 内部故障折叠为安全摘要（503）。
	ErrConnectionUnavailable StableErrorCode = "connection_unavailable"
	// ErrDatabaseError 数据库错误（脱敏）。
	ErrDatabaseError StableErrorCode = "database_error"
	// ErrAuditFailed 审计写入失败（fail-closed，扣留结果）。
	ErrAuditFailed StableErrorCode = "audit_failed"
	// ErrInternalError 未预期错误。
	ErrInternalError StableErrorCode = "internal_error"
)

// statusFor 映射稳定错误码到 HTTP 状态（P0-06A §12 Owner 已批准映射）。
func statusFor(code StableErrorCode) int {
	switch code {
	case ErrInvalidRequest, ErrInvalidScope, ErrInvalidPageToken:
		return http.StatusBadRequest
	case ErrUnauthorized:
		return http.StatusUnauthorized
	case ErrForbidden, ErrReadNotAllowed:
		return http.StatusForbidden
	case ErrConnectionNotFound, ErrPolicyNotConfigured:
		return http.StatusNotFound
	case ErrEmptySQL, ErrSQLParseError, ErrMultipleStatements,
		ErrStatementNotAllowed, ErrExecutableCommentDetected,
		ErrUnsupportedStatement, ErrUnsupportedQuery, ErrResultTooLarge:
		return http.StatusUnprocessableEntity
	case ErrQueryTimeout:
		return http.StatusGatewayTimeout
	case ErrQueryCancelled:
		return 499 // nginx 惯例，D15b 已批准
	case ErrRateLimited, ErrConnectionBusy, ErrPaginationCapacityExhausted:
		return http.StatusTooManyRequests
	case ErrConnectionUnavailable:
		return http.StatusServiceUnavailable
	case ErrDatabaseError, ErrAuditFailed, ErrInternalError:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

// retryAfter 返回 429 响应的 Retry-After 秒数。
// 有界且确定（P0-06A §5.3 D15d）：固定 1 秒，客户端据此退避；便于测试。
const retryAfterSeconds = 1

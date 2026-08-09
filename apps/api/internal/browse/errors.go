// Package browse 提供 P0-06D 授权读取服务：已授权连接列表与 Schema 懒加载。
// 授权顺序（WEB-36）：可信 Principal → active 身份 → 成员/角色 → 连接归属 →
// ConnectionPolicy AllowRead → 凭证解析 → Adapter/目标库。
// 任何门禁未通过时不解析凭证、不访问目标库；敏感值不进入 API 响应。
package browse

// StableErrorCode 稳定错误码（对齐 P0-06A §12 Owner 已批准码表）。
type StableErrorCode string

func (e StableErrorCode) Error() string { return string(e) }

const (
	// ErrInvalidScope 作用域无效（UUID 非法、必填标识符缺失）。
	ErrInvalidScope StableErrorCode = "invalid_scope"
	// ErrUnauthorized 未认证/无有效 Principal。
	ErrUnauthorized StableErrorCode = "unauthorized"
	// ErrForbidden 非成员/角色不足。
	ErrForbidden StableErrorCode = "forbidden"
	// ErrConnectionNotFound 连接不存在/跨工作区/不可见（防枚举）。
	ErrConnectionNotFound StableErrorCode = "connection_not_found"
	// ErrPolicyNotConfigured 策略缺失或关键安全上限无效（404 防枚举）。
	ErrPolicyNotConfigured StableErrorCode = "policy_not_configured"
	// ErrReadNotAllowed 存在策略但 AllowRead≠true。
	ErrReadNotAllowed StableErrorCode = "read_not_allowed"
	// ErrResultTooLarge 结果超出硬上限（不静默截断）。
	ErrResultTooLarge StableErrorCode = "result_too_large"
	// ErrRateLimited 速率限制（429 + Retry-After）。
	ErrRateLimited StableErrorCode = "rate_limited"
	// ErrConnectionBusy 连接忙（429 + Retry-After）。
	ErrConnectionBusy StableErrorCode = "connection_busy"
	// ErrConnectionUnavailable 凭证/KEK/pool/config 内部故障折叠为安全摘要（503）。
	ErrConnectionUnavailable StableErrorCode = "connection_unavailable"
	// ErrDatabaseError 目标库错误（脱敏）。
	ErrDatabaseError StableErrorCode = "database_error"
	// ErrQueryTimeout 查询超时（504）。
	ErrQueryTimeout StableErrorCode = "query_timeout"
	// ErrQueryCancelled 查询已取消（499）。
	ErrQueryCancelled StableErrorCode = "query_cancelled"
	// ErrInternalError 未预期错误（500）。
	ErrInternalError StableErrorCode = "internal_error"
)

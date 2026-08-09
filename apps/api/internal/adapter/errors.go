package adapter

import "fmt"

type ErrorCode string

const (
	ErrUnsupportedEngine     ErrorCode = "unsupported_engine"
	ErrUnsupportedCapability ErrorCode = "unsupported_capability"
	ErrInvalidConfig         ErrorCode = "invalid_config"
	ErrConnectionFailed      ErrorCode = "connection_failed"
	ErrConnPoolExhausted     ErrorCode = "connection_busy"
	ErrRateLimited           ErrorCode = "rate_limited"
	ErrQueryTimeout          ErrorCode = "query_timeout"
	ErrQueryCanceled         ErrorCode = "query_cancelled"
	ErrInvalidPageToken      ErrorCode = "invalid_page_token"
	ErrDatabaseError         ErrorCode = "database_error"
	ErrPoolClosed            ErrorCode = "pool_closed"
	ErrStaleConfig           ErrorCode = "stale_config"
	ErrConfigConflict        ErrorCode = "config_conflict"
	ErrUnsupportedQuery      ErrorCode = "unsupported_query"
	ErrResultTooLarge        ErrorCode = "result_too_large"
	ErrPaginationCapacity    ErrorCode = "pagination_capacity_exhausted"
)

type AdapterError struct {
	Code    ErrorCode
	Message string
	cause   error
}

func (e *AdapterError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("[%s] %s", e.Code, e.Message)
	}
	return fmt.Sprintf("[%s]", e.Code)
}

func (e *AdapterError) Unwrap() error { return e.cause }

func newError(code ErrorCode, msg string, cause error) *AdapterError {
	return &AdapterError{Code: code, Message: msg, cause: cause}
}

func wrapError(code ErrorCode, cause error) *AdapterError {
	return &AdapterError{Code: code, Message: "database operation failed", cause: cause}
}

// WrapDatabaseError 包装目标库底层错误并保留 cause 链（WEB-36）。
// 元数据浏览查询用 cause 保留 rows.Err()/rows.Scan 的取消与超时语义，
// 使上层能通过 errors.Is 区分 context 取消/超时与真正的数据库故障，
// 而不是一律映射为 database_error（避免流式读取中的取消被误报为 500）。
func WrapDatabaseError(cause error) error {
	return wrapError(ErrDatabaseError, cause)
}

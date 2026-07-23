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

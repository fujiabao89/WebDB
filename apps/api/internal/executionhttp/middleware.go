package executionhttp

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// RecoverMiddleware 捕获 handler panic，返回 internal_error（500）固定安全摘要，
// 绝不把内部 panic 字符串/堆栈返回给客户端（P0-06A §5.3/§12.3）。
// 原始 panic 值类型与堆栈只进服务端日志；panic 值本身不记录（防 SQL/凭证泄露）。
func RecoverMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("http handler panic",
						"method", r.Method,
						"path", r.URL.Path,
						"panic_type", fmt.Sprintf("%T", rec),
						"stack", string(debug.Stack()))
					writeError(w, ErrInternalError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

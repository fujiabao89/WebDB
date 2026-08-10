package executionhttp

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// RecoverMiddleware 捕获 handler panic，返回 internal_error（500）固定安全摘要，
// 绝不把内部 panic 字符串/堆栈返回给客户端（P0-06A §5.3/§12.3）。
// 日志只记录可信上下文中的脱敏关联字段（CodeRabbit #17）：方法、路径、panic 类型、
// 固定 panic 分类、栈，以及（若已注入）PrincipalMiddleware 验证的 workspace ID；
// panic 值本身不记录（防 SQL/凭证/结果泄露）。
// 说明限制：当前无服务端生成的 request/trace ID helper（trace_id 在 execution
// Pipeline 内部生成，middleware 层不可得），不为此发明新的公共 trace 协议。
// 调用方须将本 middleware 置于 PrincipalMiddleware 之内，使 panic 恢复点能读取
// 已验证 Principal（见 cmd/server 装配）。
func RecoverMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					attrs := []any{
						"method", r.Method,
						"path", r.URL.Path,
						"panic_type", fmt.Sprintf("%T", rec),
						"panic_category", "http_handler",
						"stack", string(debug.Stack()),
					}
					if p, ok := PrincipalFromContext(r.Context()); ok {
						// 已验证的 workspace ID（D01b Principal 注入），非路径未校验值。
						attrs = append(attrs, "workspace_id", p.WorkspaceID.String())
					}
					logger.Error("http handler panic", attrs...)
					writeError(w, ErrInternalError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

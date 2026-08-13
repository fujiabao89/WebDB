package executionhttp

import (
	"net/http"

	"github.com/fujiabao89/webdb/internal/browsehttp"
)

// Register 注册执行与单向续页路由到 mux（P0-06A §8/§9 已批准路径）。
// 浏览路由（connections/schemas/tables/columns）由 browsehttp.Register 装配，
// 由调用方（cmd/server）在统一 mux 上先注册浏览再注册本包执行路由。
// 使用 Go 1.22+ ServeMux 方法模式，非 POST 自动返回 405。
func Register(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/executions", s.handleExecutions)
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/query-pages", s.handleQueryPages)
}

// Handler 返回包含本包执行路由的 http.Handler（供契约测试与独立装配使用）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	Register(mux, s)
	return mux
}

// ComposeHandler 组装浏览 + 执行 + 续页六条已批准路由。
// 这是 WEB-35 的统一传输层装配入口（P0-06A §5.1）：/api/v1 六条路由。
// 调用方需先通过 PrincipalMiddleware 注入可信 Principal。
func ComposeHandler(browseSrv *browsehttp.Server, execSrv *Server) http.Handler {
	mux := http.NewServeMux()
	if browseSrv != nil {
		browsehttp.Register(mux, browseSrv)
	}
	if execSrv != nil {
		Register(mux, execSrv)
	}
	return mux
}

package browsehttp

import "net/http"

// Register 将 WEB-36 浏览路由注册到 mux（P0-06A §6/§7 已批准路径）。
// 路由装配独立于 main.go：WEB-35 合并后由其在服务启动时调用本函数注册。
// 使用 Go 1.22+ ServeMux 方法模式，非 GET 自动返回 405。
func Register(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/connections", s.handleListConnections)
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/connections/{connection_id}/schemas", s.handleListSchemas)
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/connections/{connection_id}/tables", s.handleListTables)
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/connections/{connection_id}/columns", s.handleListColumns)
}

// Handler 返回包含 WEB-36 浏览路由的 http.Handler（供契约测试与独立装配使用）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	Register(mux, s)
	return mux
}

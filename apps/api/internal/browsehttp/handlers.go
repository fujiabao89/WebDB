package browsehttp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/fujiabao89/webdb/internal/browse"
	"github.com/google/uuid"
)

// 超时上限（P0-06A §6 元数据库列表 10s / §7 Schema 浏览目标库 15s）。
const (
	defaultListTimeout   = 10 * time.Second
	defaultBrowseTimeout = 15 * time.Second
)

// PrincipalProvider 从请求解析可信 Principal（D01b，WEB-35 中间件 seam）。
// 返回 ok=false 表示未认证 → unauthorized；浏览器不得自报 actor/角色。
type PrincipalProvider func(r *http.Request) (browse.Principal, bool)

// Server browse HTTP handler 核心。最小 envelope seam（WEB-35 集成时可替换）。
type Server struct {
	svc           *browse.Service
	principal     PrincipalProvider
	logger        *slog.Logger
	listTimeout   time.Duration
	browseTimeout time.Duration
}

// NewServer 创建浏览 handler server。装配错误 fail-fast（F3）：nil browse.Service 或
// nil PrincipalProvider 立即 panic。D01b/CT-18：缺失可信 Principal 配置必须拒绝启动，
// 而非每请求静默返回 401；仅运行时解析失败才返回 unauthorized。
func NewServer(svc *browse.Service, pp PrincipalProvider) *Server {
	if svc == nil {
		panic("browsehttp: nil browse.Service")
	}
	if pp == nil {
		panic("browsehttp: nil PrincipalProvider")
	}
	return &Server{
		svc:           svc,
		principal:     pp,
		logger:        slog.Default(),
		listTimeout:   defaultListTimeout,
		browseTimeout: defaultBrowseTimeout,
	}
}

// authenticate 解析可信 Principal；未认证写 401 并返回 false。
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (browse.Principal, bool) {
	if s.principal == nil {
		writeError(w, browse.ErrUnauthorized)
		return browse.Principal{}, false
	}
	p, ok := s.principal(r)
	if !ok {
		writeError(w, browse.ErrUnauthorized)
		return browse.Principal{}, false
	}
	return p, true
}

// workspaceScope 校验路径 workspace_id：非法 UUID → 400；与 Principal 不一致 → 403（D01b）。
func (s *Server) workspaceScope(w http.ResponseWriter, r *http.Request, p browse.Principal) (uuid.UUID, bool) {
	ws, err := uuid.Parse(r.PathValue("workspace_id"))
	if err != nil {
		writeError(w, browse.ErrInvalidScope)
		return uuid.Nil, false
	}
	if ws != p.WorkspaceID {
		writeError(w, browse.ErrForbidden)
		return uuid.Nil, false
	}
	return ws, true
}

// parseConnectionID 解析路径 connection_id，非法 → 400。
func parseConnectionID(r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("connection_id"))
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// boundListCtx 连接列表请求超时（元数据库，上限 10s）。
func (s *Server) boundListCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return s.boundCtx(r, s.listTimeout, defaultListTimeout)
}

// boundBrowseCtx Schema 浏览请求超时（目标库，上限 15s）。
func (s *Server) boundBrowseCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return s.boundCtx(r, s.browseTimeout, defaultBrowseTimeout)
}

// boundCtx 为请求派生带兜底超时的 context；取消传播到元数据库/目标库。
func (s *Server) boundCtx(r *http.Request, configured, def time.Duration) (context.Context, context.CancelFunc) {
	if configured <= 0 {
		configured = def
	}
	return context.WithTimeout(r.Context(), configured)
}

// respond 统一成功/错误输出：错误折叠为稳定安全摘要，原始错误不进入响应。
func (s *Server) respond(w http.ResponseWriter, data any, err error) {
	if err == nil {
		writeData(w, data)
		return
	}
	var code browse.StableErrorCode
	if errors.As(err, &code) {
		writeError(w, code)
		return
	}
	s.logger.Error("browse handler internal error", "error", err.Error())
	writeError(w, browse.ErrInternalError)
}

// handleListConnections GET /api/v1/workspaces/{workspace_id}/connections
func (s *Server) handleListConnections(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if _, ok = s.workspaceScope(w, r, p); !ok {
		return
	}
	ctx, cancel := s.boundListCtx(r)
	defer cancel()
	out, err := s.svc.ListConnections(ctx, p)
	s.respond(w, out, err)
}

// handleListSchemas GET /api/v1/workspaces/{workspace_id}/connections/{connection_id}/schemas
func (s *Server) handleListSchemas(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if _, ok = s.workspaceScope(w, r, p); !ok {
		return
	}
	connID, ok := parseConnectionID(r)
	if !ok {
		writeError(w, browse.ErrInvalidScope)
		return
	}
	ctx, cancel := s.boundBrowseCtx(r)
	defer cancel()
	out, err := s.svc.ListSchemas(ctx, p, connID)
	s.respond(w, out, err)
}

// handleListTables GET /api/v1/workspaces/{workspace_id}/connections/{connection_id}/tables?schema=<ident>
func (s *Server) handleListTables(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if _, ok = s.workspaceScope(w, r, p); !ok {
		return
	}
	connID, ok := parseConnectionID(r)
	if !ok {
		writeError(w, browse.ErrInvalidScope)
		return
	}
	schema := r.URL.Query().Get("schema")
	if !browse.ValidIdent(schema) {
		writeError(w, browse.ErrInvalidScope)
		return
	}
	ctx, cancel := s.boundBrowseCtx(r)
	defer cancel()
	out, err := s.svc.ListTables(ctx, p, connID, schema)
	s.respond(w, out, err)
}

// handleListColumns GET /api/v1/workspaces/{workspace_id}/connections/{connection_id}/columns?schema=<ident>&table=<ident>
func (s *Server) handleListColumns(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if _, ok = s.workspaceScope(w, r, p); !ok {
		return
	}
	connID, ok := parseConnectionID(r)
	if !ok {
		writeError(w, browse.ErrInvalidScope)
		return
	}
	schema := r.URL.Query().Get("schema")
	table := r.URL.Query().Get("table")
	if !browse.ValidIdent(schema) || !browse.ValidIdent(table) {
		writeError(w, browse.ErrInvalidScope)
		return
	}
	ctx, cancel := s.boundBrowseCtx(r)
	defer cancel()
	out, err := s.svc.ListColumns(ctx, p, connID, schema, table)
	s.respond(w, out, err)
}

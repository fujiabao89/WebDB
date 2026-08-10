package executionhttp

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/fujiabao89/webdb/internal/browse"
	"github.com/fujiabao89/webdb/internal/execution"
	"github.com/fujiabao89/webdb/internal/queryplan"
	"github.com/google/uuid"
)

// defaultRequestTimeout 执行/续页请求级兜底超时（P2-1 审查修复）：
// Pipeline 阶段 A/B/C 的成员/连接/策略元数据库查询无自身超时，元数据库挂起时
// 若无兜底会无限阻塞请求 goroutine 与连接。HTTP 层加有界兜底超时，防止无限挂起。
// 注意（P3 审查）：此兜底是**所有阶段**（含目标库查询）的硬上限；若
// ConnectionPolicy.StatementTimeoutMs 配置大于 60s，查询会被本兜底提前取消并返回
// 504 query_timeout（方向安全 fail-closed）。演示默认 StatementTimeoutMs 较小；
// 生产部署应将兜底超时与策略超时上限对齐，避免策略超时形同虚设。
const defaultRequestTimeout = 60 * time.Second

// Executor 是执行服务的最小 handler 契约。
// 生产实现为 execution.Pipeline（Execute/ExecuteNextPage），测试可注入替身。
type Executor interface {
	Execute(ctx context.Context, req execution.ExecuteRequest) (*execution.ExecuteResult, error)
	ExecuteNextPage(ctx context.Context, req execution.NextPageRequest) (*execution.ExecuteResult, error)
}

// Server 执行/续页 HTTP handler 核心。
// Principal 由 PrincipalMiddleware 注入请求 context（D01b），本结构不持有副本。
type Server struct {
	executor Executor
	logger   *slog.Logger
}

// NewServer 创建执行 handler server。
// 装配错误 fail-fast：nil executor 立即 panic（F3）；Principal 缺失由调用方启动 fatal。
func NewServer(principal browse.Principal, executor Executor) *Server {
	if executor == nil {
		panic("executionhttp: nil Executor")
	}
	if principal.UserID == uuid.Nil || principal.WorkspaceID == uuid.Nil {
		panic("executionhttp: nil principal")
	}
	return &Server{executor: executor, logger: slog.Default()}
}

// authenticate 从请求 context 解析可信 Principal；未认证写 401。
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (browse.Principal, bool) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, ErrUnauthorized)
		return browse.Principal{}, false
	}
	return p, true
}

// workspaceScope 校验路径 workspace_id：非法 UUID → 400；与 Principal 不一致 → 403（D01b）。
func (s *Server) workspaceScope(w http.ResponseWriter, r *http.Request, p browse.Principal) bool {
	ws, err := uuid.Parse(r.PathValue("workspace_id"))
	if err != nil {
		writeError(w, ErrInvalidScope)
		return false
	}
	if ws != p.WorkspaceID {
		writeError(w, ErrForbidden)
		return false
	}
	return true
}

// handleExecutions POST /api/v1/workspaces/{workspace_id}/executions（P0-06A §8）。
func (s *Server) handleExecutions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if !s.workspaceScope(w, r, p) {
		return
	}
	var req ExecuteRequestDto
	if ok, _ := decodeJSONBody(w, r, maxRequestBytes, &req); !ok {
		return
	}
	execReq, code := validateExecuteRequest(&req)
	if code != "" {
		writeError(w, code)
		return
	}

	// 请求级兜底超时（P2-1）：防止元数据库挂起导致无限阻塞。
	ctx, cancel := context.WithTimeout(r.Context(), defaultRequestTimeout)
	defer cancel()
	result, err := s.executor.Execute(ctx, execution.ExecuteRequest{
		Principal:    execution.AuthenticatedPrincipal{UserID: p.UserID, WorkspaceID: p.WorkspaceID},
		ConnectionID: execReq.ConnectionID,
		SQL:          execReq.SQL,
		SortKeys:     execReq.SortKeys,
		PageSize:     execReq.PageSize,
	})
	if err != nil {
		writeError(w, mapExecutionResultError(result, err))
		return
	}
	if result == nil || result.Result == nil {
		writeError(w, ErrInternalError)
		return
	}
	s.writeQueryResponse(w, result)
}

// handleQueryPages POST /api/v1/workspaces/{workspace_id}/query-pages（P0-06A §9）。
// 只接受 opaque token；每页重新授权与独立 Execution/Audit 由执行服务编排。
func (s *Server) handleQueryPages(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if !s.workspaceScope(w, r, p) {
		return
	}
	var req NextPageRequestDto
	if ok, _ := decodeJSONBody(w, r, maxRequestBytes, &req); !ok {
		return
	}
	if strings.TrimSpace(req.NextPageToken) == "" {
		writeError(w, ErrInvalidRequest)
		return
	}

	// 请求级兜底超时（P2-1）：防止元数据库挂起导致无限阻塞。
	ctx, cancel := context.WithTimeout(r.Context(), defaultRequestTimeout)
	defer cancel()
	result, err := s.executor.ExecuteNextPage(ctx, execution.NextPageRequest{
		Principal: execution.AuthenticatedPrincipal{UserID: p.UserID, WorkspaceID: p.WorkspaceID},
		Token:     req.NextPageToken,
	})
	if err != nil {
		writeError(w, mapExecutionResultError(result, err))
		return
	}
	if result == nil || result.Result == nil {
		writeError(w, ErrInternalError)
		return
	}
	s.writeQueryResponse(w, result)
}

// writeQueryResponse 把执行结果转换为 wire DTO + page/audit meta 并写出。
// 序列化失败不返回半成功响应。
func (s *Server) writeQueryResponse(w http.ResponseWriter, result *execution.ExecuteResult) {
	wire, err := toWireResult(string(result.Engine), result.Result)
	if err != nil {
		writeError(w, wireCode(err))
		return
	}
	meta := QueryMetaDto{
		Page: QueryPageDto{
			PageSize:      result.Result.ReturnedRows,
			HasMore:       result.Result.HasMore,
			NextPageToken: result.NextPageToken,
		},
		Audit: AuditReceiptDto{
			State:        result.AuditState,
			AuditEventID: auditIDOrEmpty(result.AuditEventID),
			ExecutionID:  auditIDOrEmpty(result.ExecutionID),
			TraceID:      result.TraceID,
			Outcome:      string(result.Outcome),
		},
	}
	writeDataEnvelope(w, wire, meta)
}

// auditIDOrEmpty 把可选 UUID 转为字符串（nil → 空串）。
func auditIDOrEmpty(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// validateExecuteRequest 校验并规范化第一页请求（P0-06A §8.1）。
// 服务端权威：page_size 只可缩小不可放大（客户端提交上限被钳制）；order_by 仅意图。
// 占位符检测在 execution Pipeline（引擎已知后）执行，本层不做。
func validateExecuteRequest(req *ExecuteRequestDto) (execution.ExecuteRequest, StableErrorCode) {
	if req == nil {
		return execution.ExecuteRequest{}, ErrInvalidRequest
	}
	connID, err := uuid.Parse(strings.TrimSpace(req.ConnectionID))
	if err != nil {
		return execution.ExecuteRequest{}, ErrInvalidRequest
	}
	sqlStr := req.SQL
	if strings.TrimSpace(sqlStr) == "" {
		return execution.ExecuteRequest{}, ErrEmptySQL
	}
	if len(sqlStr) > 64<<10 {
		return execution.ExecuteRequest{}, ErrInvalidRequest // SQL 64 KiB（D06b）
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 100
	}
	if pageSize > 500 {
		pageSize = 500
	}
	sortKeys := make([]queryplan.SortKey, 0, len(req.OrderBy))
	for _, ob := range req.OrderBy {
		if ob.Order != "ASC" && ob.Order != "DESC" {
			return execution.ExecuteRequest{}, ErrInvalidRequest
		}
		if !validIdent(ob.Column) {
			return execution.ExecuteRequest{}, ErrInvalidRequest
		}
		dir := queryplan.SortAsc
		if ob.Order == "DESC" {
			dir = queryplan.SortDesc
		}
		sortKeys = append(sortKeys, queryplan.SortKey{Column: ob.Column, Direction: dir, NullsLast: ob.NullsLast})
	}
	return execution.ExecuteRequest{
		ConnectionID: connID,
		SQL:          sqlStr,
		SortKeys:     sortKeys,
		PageSize:     pageSize,
	}, ""
}

// validIdent 保守校验 SQL 标识符形状（与 queryplan/adapter 一致的白名单）。
func validIdent(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for i, r := range s {
		if i == 0 && !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_') {
			return false
		}
		if i > 0 && !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}

// mapExecutionResultError 映射执行服务返回的稳定错误码到公共码。
// 优先使用 result.ErrorCode（pipeline 已映射）；SQL policy 系列为字符串码。
func mapExecutionResultError(result *execution.ExecuteResult, err error) StableErrorCode {
	if result != nil && result.ErrorCode != "" {
		return mapExecutionCode(result.ErrorCode)
	}
	return ErrInternalError
}

// mapExecutionCode 把 execution 内部稳定码映射为 HTTP 公共码（P0-06A §12.1）。
// 内部凭证/KEK/pool/config 故障折叠为 connection_unavailable（D15a）。
func mapExecutionCode(code execution.StableErrorCode) StableErrorCode {
	switch code {
	case execution.ErrInvalidScope:
		return ErrInvalidScope
	case execution.ErrUnauthorized:
		return ErrUnauthorized
	case execution.ErrForbidden:
		return ErrForbidden
	case execution.ErrConnectionNotFound:
		return ErrConnectionNotFound
	case execution.ErrPolicyNotConfigured:
		return ErrPolicyNotConfigured
	case execution.ErrReadNotAllowed:
		return ErrReadNotAllowed
	case execution.ErrUnsupportedQuery:
		return ErrUnsupportedQuery
	case execution.ErrInvalidPageToken:
		return ErrInvalidPageToken
	case execution.ErrPaginationCapacityExhausted:
		return ErrPaginationCapacityExhausted
	case execution.ErrQueryTimeout, execution.ErrExecutionTimeout:
		return ErrQueryTimeout
	case execution.ErrQueryCancelled, execution.ErrExecutionCancelled:
		return ErrQueryCancelled
	case execution.ErrRateLimited:
		return ErrRateLimited
	case execution.ErrConnectionBusy:
		return ErrConnectionBusy
	case execution.ErrConnectionUnavailable:
		return ErrConnectionUnavailable
	case execution.ErrDatabaseError:
		return ErrDatabaseError
	case execution.ErrResultTooLarge:
		return ErrResultTooLarge
	case execution.ErrAuditFailed:
		return ErrAuditFailed
	case execution.ErrStatementNotAllowed:
		return ErrStatementNotAllowed
	case execution.ErrConnectionConfigConflict:
		return ErrConnectionUnavailable
	case execution.ErrUnsupportedEngine:
		return ErrInternalError
	// SQL policy 系列（mapPolicyReason 返回的字符串码）。
	case "sql_parse_error":
		return ErrSQLParseError
	case "multiple_statements":
		return ErrMultipleStatements
	case "executable_comment_detected":
		return ErrExecutableCommentDetected
	case "unsupported_statement":
		return ErrUnsupportedStatement
	case "empty_sql":
		return ErrEmptySQL
	default:
		return ErrInternalError
	}
}

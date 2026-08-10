package executionhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fujiabao89/webdb/internal/adapter"
	"github.com/fujiabao89/webdb/internal/browse"
	"github.com/fujiabao89/webdb/internal/execution"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

const testConnID = "33333333-3333-3333-3333-333333333333"

// fakeExecutor 测试替身，记录调用并返回预设结果/错误。
type fakeExecutor struct {
	executeFn  func(ctx context.Context, req execution.ExecuteRequest) (*execution.ExecuteResult, error)
	nextPageFn func(ctx context.Context, req execution.NextPageRequest) (*execution.ExecuteResult, error)
	executes   int
	nextPages  int
}

func (f *fakeExecutor) Execute(ctx context.Context, req execution.ExecuteRequest) (*execution.ExecuteResult, error) {
	f.executes++
	if f.executeFn != nil {
		return f.executeFn(ctx, req)
	}
	return nil, execution.ErrInternalError
}

func (f *fakeExecutor) ExecuteNextPage(ctx context.Context, req execution.NextPageRequest) (*execution.ExecuteResult, error) {
	f.nextPages++
	if f.nextPageFn != nil {
		return f.nextPageFn(ctx, req)
	}
	return nil, execution.ErrInternalError
}

func testPrincipal() browse.Principal {
	return browse.Principal{
		UserID:      uuid.MustParse(testUserID),
		WorkspaceID: uuid.MustParse(testWorkID),
	}
}

// newTestHandler 构造带 principal 注入与 panic recovery 的完整 handler。
func newTestHandler(exec Executor) (http.Handler, browse.Principal) {
	p := testPrincipal()
	server := NewServer(exec)
	mux := ComposeHandler(nil, server)
	// PrincipalMiddleware 在 RecoverMiddleware 之外：panic 恢复点能读取已验证 Principal
	//（与 cmd/server 装配一致，CodeRabbit #17）。
	return PrincipalMiddleware(p)(RecoverMiddleware(nil)(mux)), p
}

func execSuccess() *execution.ExecuteResult {
	eid := uuid.New()
	aid := uuid.New()
	return &execution.ExecuteResult{
		PageSize: 100, // 服务端实际分页上限（P0-06A §8.2 page.page_size 语义，CodeRabbit #16）
		Result: &adapter.QueryResult{
			Columns:       []adapter.ColumnInfo{{Name: "id", DataType: "23"}},
			Rows:          [][]any{{int64(1)}},
			ReturnedRows:  1,
			TotalReturned: 1,
		},
		TraceID:      "server-trace",
		ExecutionID:  &eid,
		AuditEventID: &aid,
		AuditState:   "recorded",
		Outcome:      metadata.OutcomeSucceeded,
		Engine:       execution.EnginePostgreSQL,
	}
}

// TestExecutionsUnauthorized 验证未注入 Principal → 401。
func TestExecutionsUnauthorized(t *testing.T) {
	server := NewServer(&fakeExecutor{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+testWorkID+"/executions",
		strings.NewReader(`{"connection_id":"`+testConnID+`","sql":"SELECT 1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	// 不经过 PrincipalMiddleware → 无 context Principal → 401。
	RecoverMiddleware(nil)(server.Handler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestExecutionsWorkspaceMismatch 验证路径 workspace 与 Principal 不一致 → 403（D01b）。
func TestExecutionsWorkspaceMismatch(t *testing.T) {
	handler, _ := newTestHandler(&fakeExecutor{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/99999999-9999-9999-9999-999999999999/executions",
		strings.NewReader(`{"connection_id":"`+testConnID+`","sql":"SELECT 1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// TestExecutionsInvalidBody 验证非法请求体 → 400。
func TestExecutionsInvalidBody(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"malformed json", `{"sql":`},
		{"missing connection id", `{"sql":"SELECT 1"}`},
		{"bad connection id", `{"connection_id":"nope","sql":"SELECT 1"}`},
		{"sql too long", `{"connection_id":"` + testConnID + `","sql":"` + strings.Repeat("x", 64<<10+1) + `"}`},
		{"bad order", `{"connection_id":"` + testConnID + `","sql":"SELECT 1","order_by":[{"column":"id","order":"UP"}]}`},
		{"bad column ident", `{"connection_id":"` + testConnID + `","sql":"SELECT 1","order_by":[{"column":"a;b","order":"ASC"}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			handler, _ := newTestHandler(&fakeExecutor{})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+testWorkID+"/executions", strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}

// TestExecutionsEmptySQL 验证空 SQL → 422 empty_sql。
func TestExecutionsEmptySQL(t *testing.T) {
	handler, _ := newTestHandler(&fakeExecutor{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+testWorkID+"/executions",
		strings.NewReader(`{"connection_id":"`+testConnID+`","sql":"   "}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
}

// TestExecutionsSuccess 验证成功响应结构与审计 receipt 一致（P0-06A §8.2）。
func TestExecutionsSuccess(t *testing.T) {
	exec := &fakeExecutor{executeFn: func(ctx context.Context, req execution.ExecuteRequest) (*execution.ExecuteResult, error) {
		return execSuccess(), nil
	}}
	handler, _ := newTestHandler(exec)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+testWorkID+"/executions",
		strings.NewReader(`{"connection_id":"`+testConnID+`","sql":"SELECT id FROM t","page_size":100}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Columns []struct {
				Name     string `json:"name"`
				WireType string `json:"wire_type"`
			} `json:"columns"`
			Rows          [][]any `json:"rows"`
			ReturnedRows  int     `json:"returned_rows"`
			TotalReturned int     `json:"total_returned"`
		} `json:"data"`
		Meta struct {
			Page struct {
				PageSize int  `json:"page_size"`
				HasMore  bool `json:"has_more"`
			} `json:"page"`
			Audit struct {
				State        string `json:"state"`
				AuditEventID string `json:"audit_event_id"`
				ExecutionID  string `json:"execution_id"`
				TraceID      string `json:"trace_id"`
				Outcome      string `json:"outcome"`
			} `json:"audit"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Data.Columns[0].WireType != "int" {
		t.Errorf("wire_type = %s, want int", resp.Data.Columns[0].WireType)
	}
	if resp.Data.ReturnedRows != 1 || resp.Data.TotalReturned != 1 {
		t.Errorf("counts = %d/%d", resp.Data.ReturnedRows, resp.Data.TotalReturned)
	}
	if resp.Meta.Page.PageSize != 100 {
		t.Errorf("page_size = %d, want 100（服务端实际分页上限，而非当前页行数）", resp.Meta.Page.PageSize)
	}
	if resp.Meta.Audit.State != "recorded" || resp.Meta.Audit.Outcome != "succeeded" {
		t.Errorf("audit = %+v", resp.Meta.Audit)
	}
	if resp.Meta.Audit.AuditEventID == "" || resp.Meta.Audit.ExecutionID == "" {
		t.Error("audit receipt 应包含 audit_event_id 与 execution_id")
	}
	if !strings.HasPrefix(resp.Meta.Audit.TraceID, "server") {
		t.Errorf("trace_id = %q, want 服务端生成（非客户端）", resp.Meta.Audit.TraceID)
	}
}

// TestExecutionsErrorMapping 验证执行服务稳定码到 HTTP 状态映射。
func TestExecutionsErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		code   execution.StableErrorCode
		status int
	}{
		{"forbidden", execution.ErrForbidden, 403},
		{"connection not found", execution.ErrConnectionNotFound, 404},
		{"policy not configured", execution.ErrPolicyNotConfigured, 404},
		{"read not allowed", execution.ErrReadNotAllowed, 403},
		{"unsupported query", execution.ErrUnsupportedQuery, 422},
		{"query timeout", execution.ErrQueryTimeout, 504},
		{"query cancelled", execution.ErrQueryCancelled, 499},
		{"rate limited", execution.ErrRateLimited, 429},
		{"connection busy", execution.ErrConnectionBusy, 429},
		{"connection unavailable", execution.ErrConnectionUnavailable, 503},
		{"database error", execution.ErrDatabaseError, 500},
		{"result too large", execution.ErrResultTooLarge, 422},
		{"audit failed", execution.ErrAuditFailed, 500},
		{"statement not allowed", execution.ErrStatementNotAllowed, 422},
		{"sql parse error", "sql_parse_error", 422},
		{"multiple statements", "multiple_statements", 422},
		{"executable comment", "executable_comment_detected", 422},
		{"internal error", execution.ErrInternalError, 500},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code := c.code
			exec := &fakeExecutor{executeFn: func(ctx context.Context, req execution.ExecuteRequest) (*execution.ExecuteResult, error) {
				return &execution.ExecuteResult{ErrorCode: code}, code
			}}
			handler, _ := newTestHandler(exec)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+testWorkID+"/executions",
				strings.NewReader(`{"connection_id":"`+testConnID+`","sql":"SELECT 1"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != c.status {
				t.Fatalf("status = %d, want %d", rec.Code, c.status)
			}
		})
	}
}

// TestRateLimitedRetryAfter 验证 429 响应携带 Retry-After。
func TestRateLimitedRetryAfter(t *testing.T) {
	exec := &fakeExecutor{executeFn: func(ctx context.Context, req execution.ExecuteRequest) (*execution.ExecuteResult, error) {
		return &execution.ExecuteResult{ErrorCode: execution.ErrRateLimited}, execution.ErrRateLimited
	}}
	handler, _ := newTestHandler(exec)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+testWorkID+"/executions",
		strings.NewReader(`{"connection_id":"`+testConnID+`","sql":"SELECT 1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "1" {
		t.Errorf("Retry-After = %q, want 1", rec.Header().Get("Retry-After"))
	}
}

// TestQueryPages 验证续页：token 缺失 400；成功响应结构一致。
func TestQueryPages(t *testing.T) {
	t.Run("missing token", func(t *testing.T) {
		handler, _ := newTestHandler(&fakeExecutor{})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+testWorkID+"/query-pages",
			strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})
	t.Run("success", func(t *testing.T) {
		exec := &fakeExecutor{nextPageFn: func(ctx context.Context, req execution.NextPageRequest) (*execution.ExecuteResult, error) {
			if req.Token != "opaque-token" {
				t.Errorf("token = %q", req.Token)
			}
			return execSuccess(), nil
		}}
		handler, _ := newTestHandler(exec)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+testWorkID+"/query-pages",
			strings.NewReader(`{"next_page_token":"opaque-token"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
		}
		if exec.nextPages != 1 {
			t.Errorf("nextPages = %d, want 1", exec.nextPages)
		}
	})
}

// TestErrorResponseNoSensitiveCanary 验证错误响应不含 SQL/connection 明文
// （固定安全摘要，P0-06A §12.3）。
func TestErrorResponseNoSensitiveCanary(t *testing.T) {
	exec := &fakeExecutor{executeFn: func(ctx context.Context, req execution.ExecuteRequest) (*execution.ExecuteResult, error) {
		return &execution.ExecuteResult{ErrorCode: execution.ErrForbidden}, execution.ErrForbidden
	}}
	handler, _ := newTestHandler(exec)
	body := `{"connection_id":"` + testConnID + `","sql":"SELECT secret_password FROM vault"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+testWorkID+"/executions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "secret_password") {
		t.Error("错误响应不应包含 SQL 明文")
	}
	if strings.Contains(rec.Body.String(), testConnID) {
		t.Error("错误响应不应包含 connection_id 明文")
	}
	if !strings.Contains(rec.Body.String(), `"code":"forbidden"`) {
		t.Errorf("错误响应应包含稳定 code，body=%s", rec.Body.String())
	}
}

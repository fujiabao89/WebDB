package executionhttp

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRecoverMiddlewareLogsSanitizedCorrelation 验证 panic 日志记录脱敏关联字段
// （已验证 workspace_id + 固定 panic 分类，CodeRabbit #17），且不泄露 panic 正文
// （SQL/凭证/结果）。中间件按 cmd/server 装配顺序：PrincipalMiddleware 在外，
// RecoverMiddleware 在内，使恢复点可读取已验证 Principal。
func TestRecoverMiddlewareLogsSanitizedCorrelation(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("secret query SQL: SELECT password FROM creds")
	})
	p := testPrincipal()
	h := PrincipalMiddleware(p)(RecoverMiddleware(logger)(inner))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic-test", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	logged := buf.String()
	// 已验证 workspace_id 进入日志（关联字段）。
	if !strings.Contains(logged, p.WorkspaceID.String()) {
		t.Errorf("panic 日志应包含已验证 workspace_id（%s）:\n%s", p.WorkspaceID, logged)
	}
	// 固定 panic 分类。
	if !strings.Contains(logged, `"panic_category":"http_handler"`) {
		t.Errorf("panic 日志应包含固定 panic_category:\n%s", logged)
	}
	// 敏感 panic 正文/内容不得进入日志。
	for _, leak := range []string{"secret query SQL", "password", "SELECT"} {
		if strings.Contains(logged, leak) {
			t.Errorf("panic 日志泄露敏感内容 %q:\n%s", leak, logged)
		}
	}
}

// TestRecoverMiddlewareNoPrincipalLogsWithoutWorkspaceID 验证未注入 Principal 时
// panic 日志不伪造 workspace_id（只记录可信上下文字段）。
func TestRecoverMiddlewareNoPrincipalLogsWithoutWorkspaceID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})
	h := RecoverMiddleware(logger)(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic-test", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	logged := buf.String()
	if strings.Contains(logged, "workspace_id") {
		t.Errorf("无已验证 Principal 时不得伪造 workspace_id:\n%s", logged)
	}
}

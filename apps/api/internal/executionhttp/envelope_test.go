package executionhttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStatusFor 验证完整错误码到 HTTP 状态映射（P0-06A §12.1 Owner 批准）。
func TestStatusFor(t *testing.T) {
	cases := []struct {
		code   StableErrorCode
		status int
	}{
		{ErrInvalidRequest, 400},
		{ErrInvalidScope, 400},
		{ErrInvalidPageToken, 400},
		{ErrUnauthorized, 401},
		{ErrForbidden, 403},
		{ErrReadNotAllowed, 403},
		{ErrConnectionNotFound, 404},
		{ErrPolicyNotConfigured, 404},
		{ErrEmptySQL, 422},
		{ErrSQLParseError, 422},
		{ErrMultipleStatements, 422},
		{ErrStatementNotAllowed, 422},
		{ErrExecutableCommentDetected, 422},
		{ErrUnsupportedStatement, 422},
		{ErrUnsupportedQuery, 422},
		{ErrResultTooLarge, 422},
		{ErrQueryTimeout, 504},
		{ErrQueryCancelled, 499},
		{ErrRateLimited, 429},
		{ErrConnectionBusy, 429},
		{ErrPaginationCapacityExhausted, 429},
		{ErrConnectionUnavailable, 503},
		{ErrDatabaseError, 500},
		{ErrAuditFailed, 500},
		{ErrInternalError, 500},
	}
	for _, c := range cases {
		if got := statusFor(c.code); got != c.status {
			t.Errorf("statusFor(%q) = %d, want %d", c.code, got, c.status)
		}
	}
}

// TestWriteErrorEnvelope 验证统一错误 envelope：{error:{code,message}} 且 message=code
// （固定安全摘要，不泄露根因）。
func TestWriteErrorEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, ErrForbidden)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if body.Error.Code != "forbidden" || body.Error.Message != "forbidden" {
		t.Errorf("envelope = %+v, want code=message=forbidden", body)
	}
}

// TestWriteError429RetryAfter 验证 429 响应携带确定性的 Retry-After（P0-06A D15d）。
func TestWriteError429RetryAfter(t *testing.T) {
	for _, code := range []StableErrorCode{ErrRateLimited, ErrConnectionBusy, ErrPaginationCapacityExhausted} {
		rec := httptest.NewRecorder()
		writeError(rec, code)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("%s: status = %d, want 429", code, rec.Code)
		}
		ra := rec.Header().Get("Retry-After")
		if ra != "1" {
			t.Errorf("%s: Retry-After = %q, want \"1\"", code, ra)
		}
	}
	// 非 429 不携带 Retry-After。
	rec := httptest.NewRecorder()
	writeError(rec, ErrInternalError)
	if rec.Header().Get("Retry-After") != "" {
		t.Error("非 429 响应不应携带 Retry-After")
	}
}

// TestWriteDataEnvelopeBudget 验证成功 envelope 的字节预算：超限返回
// result_too_large 且不写出超限 body（P0-06A D06b，不静默截断）。
func TestWriteDataEnvelopeBudget(t *testing.T) {
	// 8 MiB 上限：构造超过上限的 data。
	big := strings.Repeat("x", MaxResponseBytes)
	rec := httptest.NewRecorder()
	writeDataEnvelope(rec, big, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "result_too_large") {
		t.Errorf("超限响应应返回 result_too_large，body = %.200s", rec.Body.String())
	}
}

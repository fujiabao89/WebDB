package executionhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type decodeTarget struct {
	ConnectionID string `json:"connection_id"`
	SQL          string `json:"sql"`
	PageSize     int    `json:"page_size"`
}

// TestDecodeJSONBodyRejectsWrongContentType 验证非 JSON Content-Type 拒绝。
func TestDecodeJSONBodyRejectsWrongContentType(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"sql":"SELECT 1"}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	var out decodeTarget
	_, err := decodeJSONBody(rec, req, 256<<10, &out)
	if !isCode(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want invalid_request", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestDecodeJSONBodyRejectsEmpty 验证空 body 拒绝。
func TestDecodeJSONBodyRejectsEmpty(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	var out decodeTarget
	_, err := decodeJSONBody(rec, req, 256<<10, &out)
	if !isCode(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want invalid_request", err)
	}
}

// TestDecodeJSONBodyRejectsMalformed 验证畸形 JSON 拒绝。
func TestDecodeJSONBodyRejectsMalformed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"sql":`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	var out decodeTarget
	_, err := decodeJSONBody(rec, req, 256<<10, &out)
	if !isCode(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want invalid_request", err)
	}
}

// TestDecodeJSONBodyRejectsMultipleValues 验证多个 JSON 值拒绝（单值解码契约）。
func TestDecodeJSONBodyRejectsMultipleValues(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"sql":"SELECT 1"} {"sql":"SELECT 2"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	var out decodeTarget
	_, err := decodeJSONBody(rec, req, 256<<10, &out)
	if !isCode(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want invalid_request", err)
	}
}

// TestDecodeJSONBodyRejectsTooLarge 验证请求体超过 256 KiB 拒绝（P0-06A D06b）。
func TestDecodeJSONBodyRejectsTooLarge(t *testing.T) {
	body := `{"sql":"` + strings.Repeat("x", 300<<10) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	var out decodeTarget
	_, err := decodeJSONBody(rec, req, 256<<10, &out)
	if !isCode(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want invalid_request", err)
	}
}

// TestDecodeJSONBodyOK 验证合法 JSON 单值成功解码。
func TestDecodeJSONBodyOK(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"connection_id":"11111111-1111-1111-1111-111111111111","sql":"SELECT 1","page_size":100}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	var out decodeTarget
	ok, err := decodeJSONBody(rec, req, 256<<10, &out)
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v, want success", ok, err)
	}
	if out.SQL != "SELECT 1" || out.PageSize != 100 {
		t.Errorf("decoded = %+v", out)
	}
}

// TestDecodeJSONBodyIgnoresUnknownAndForbiddenFields 验证未知/禁止字段被忽略
// （浏览器不得自报 actor/角色/方言/策略，覆盖一律忽略，P0-06A §3/§5.2）。
func TestDecodeJSONBodyIgnoresUnknownAndForbiddenFields(t *testing.T) {
	body := `{"connection_id":"11111111-1111-1111-1111-111111111111","sql":"SELECT 1",` +
		`"engine":"mysql","workspace_id":"22222222-2222-2222-2222-222222222222","user_id":"u","role":"admin",` +
		`"unique":true,"max_rows":9999,"trace_id":"client-trace","args":[1,2]}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	var out decodeTarget
	ok, err := decodeJSONBody(rec, req, 256<<10, &out)
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v, want success (禁止字段忽略)", ok, err)
	}
	if out.SQL != "SELECT 1" {
		t.Errorf("sql = %q, want SELECT 1", out.SQL)
	}
}

// isCode 判断错误是否为指定稳定错误码（codef 带前缀时前缀匹配）。
func isCode(err error, code StableErrorCode) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return msg == string(code) || strings.HasPrefix(msg, string(code)+":")
}

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthHandler_GET(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	healthHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际 %d", rec.Code)
	}

	contentType := rec.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Fatalf("期望 Content-Type application/json，实际 %s", contentType)
	}

	var resp healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("响应 JSON 解析失败: %v", err)
	}

	if resp.Status != "ok" {
		t.Errorf("期望 status=ok，实际 status=%s", resp.Status)
	}
	if resp.Version != version {
		t.Errorf("期望 version=%s，实际 version=%s", version, resp.Version)
	}
	if resp.Time == "" {
		t.Error("time 字段不应为空")
	}
}

func TestHealthHandler_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	rec := httptest.NewRecorder()

	healthHandler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("期望状态码 405，实际 %d", rec.Code)
	}
}

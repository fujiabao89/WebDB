package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// metaDSN 生产配置：迁移使用独立管理员 META_MIGRATE_USER，运行时用户为 webdb_app_runtime（PR37 检定）。
func TestMetaDSN_usesMigrateAdmin(t *testing.T) {
	t.Setenv("META_MIGRATE_USER", "webdb_admin")
	t.Setenv("META_MIGRATE_PASSWORD", "admin_pw")
	t.Setenv("META_DB_USER", "webdb_app_runtime")
	t.Setenv("META_DB_PASSWORD", "app_pw")
	t.Setenv("META_DB_HOST", "meta-host")
	t.Setenv("META_DB_PORT", "5432")

	dsn := metaDSN()
	if !strings.Contains(dsn, "webdb_admin:admin_pw@meta-host:5432") {
		t.Fatalf("metaDSN 应使用 META_MIGRATE_USER 管理账号，got %s", dsn)
	}
	if strings.Contains(dsn, "webdb_app_runtime") {
		t.Fatalf("metaDSN（迁移）不应使用运行时用户 webdb_app_runtime，got %s", dsn)
	}
}

// 未设 META_MIGRATE_* 时回退到 META_DB_USER（本地开发向后兼容）。
func TestMetaDSN_fallsBackToRuntimeUser(t *testing.T) {
	t.Setenv("META_MIGRATE_USER", "")
	t.Setenv("META_MIGRATE_PASSWORD", "")
	t.Setenv("META_DB_USER", "webdb_app_runtime")
	t.Setenv("META_DB_PASSWORD", "app_pw")
	t.Setenv("META_DB_HOST", "meta-host")

	dsn := metaDSN()
	if !strings.Contains(dsn, "webdb_app_runtime:app_pw@meta-host") {
		t.Fatalf("未设 META_MIGRATE_* 时应回退到 META_DB_USER，got %s", dsn)
	}
}

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

// runtimeMetaDSN 只使用最小权限运行时账号（META_DB_USER），即使设置了迁移凭据也
// 不得复用 META_MIGRATE_*（CodeRabbit #3：运行时以 DDL 账号访问元数据库违反最小权限）。
func TestRuntimeMetaDSN_neverUsesMigrateCreds(t *testing.T) {
	t.Setenv("META_MIGRATE_USER", "webdb_admin")
	t.Setenv("META_MIGRATE_PASSWORD", "admin_pw")
	t.Setenv("META_DB_USER", "webdb_app_runtime")
	t.Setenv("META_DB_PASSWORD", "app_pw")
	t.Setenv("META_DB_HOST", "meta-host")
	t.Setenv("META_DB_PORT", "5432")

	dsn, err := runtimeMetaDSN()
	if err != nil {
		t.Fatalf("runtimeMetaDSN 不应报错: %v", err)
	}
	if !strings.Contains(dsn, "webdb_app_runtime:app_pw@meta-host:5432") {
		t.Fatalf("runtimeMetaDSN 应使用 META_DB_USER 运行时账号，got %s", dsn)
	}
	if strings.Contains(dsn, "webdb_admin") {
		t.Fatalf("runtimeMetaDSN 不得使用迁移账号 META_MIGRATE_USER，got %s", dsn)
	}
}

// migrateMetaDSN 生产配置：迁移使用独立管理员 META_MIGRATE_USER（PR37 检定）。
func TestMigrateMetaDSN_usesMigrateAdmin(t *testing.T) {
	t.Setenv("META_MIGRATE_USER", "webdb_admin")
	t.Setenv("META_MIGRATE_PASSWORD", "admin_pw")
	t.Setenv("META_DB_USER", "webdb_app_runtime")
	t.Setenv("META_DB_PASSWORD", "app_pw")
	t.Setenv("META_DB_HOST", "meta-host")
	t.Setenv("META_DB_PORT", "5432")

	dsn, err := migrateMetaDSN()
	if err != nil {
		t.Fatalf("migrateMetaDSN 不应报错: %v", err)
	}
	if !strings.Contains(dsn, "webdb_admin:admin_pw@meta-host:5432") {
		t.Fatalf("migrateMetaDSN 应使用 META_MIGRATE_USER 管理账号，got %s", dsn)
	}
	if strings.Contains(dsn, "webdb_app_runtime") {
		t.Fatalf("migrateMetaDSN 不应使用运行时用户 webdb_app_runtime，got %s", dsn)
	}
}

// 未设 META_MIGRATE_* 时回退到 META_DB_USER（本地开发向后兼容）。
func TestMigrateMetaDSN_fallsBackToRuntimeUser(t *testing.T) {
	t.Setenv("META_MIGRATE_USER", "")
	t.Setenv("META_MIGRATE_PASSWORD", "")
	t.Setenv("META_DB_USER", "webdb_app_runtime")
	t.Setenv("META_DB_PASSWORD", "app_pw")
	t.Setenv("META_DB_HOST", "meta-host")

	dsn, err := migrateMetaDSN()
	if err != nil {
		t.Fatalf("migrateMetaDSN 不应报错: %v", err)
	}
	if !strings.Contains(dsn, "webdb_app_runtime:app_pw@meta-host") {
		t.Fatalf("未设 META_MIGRATE_* 时应回退到 META_DB_USER，got %s", dsn)
	}
}

// 迁移凭据必须成对：只设其一应报错（PR37 八轮审查项）。
func TestMigrateMetaDSN_partialMigrateCreds(t *testing.T) {
	t.Setenv("META_MIGRATE_USER", "webdb_admin")
	t.Setenv("META_MIGRATE_PASSWORD", "")
	_, err := migrateMetaDSN()
	if err == nil {
		t.Fatal("只设 META_MIGRATE_USER 时应返回错误")
	}

	t.Setenv("META_MIGRATE_USER", "")
	t.Setenv("META_MIGRATE_PASSWORD", "admin_pw")
	_, err = migrateMetaDSN()
	if err == nil {
		t.Fatal("只设 META_MIGRATE_PASSWORD 时应返回错误")
	}
}

// AllowInsecureLocalDemo（CodeRabbit #4）：默认关闭；仅显式 true 启用；非法值拒绝。
func TestAllowInsecureLocalDemo_defaultOff(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_LOCAL_DEMO", "")
	got, err := allowInsecureLocalDemo()
	if err != nil {
		t.Fatalf("未设置时不应报错: %v", err)
	}
	if got {
		t.Fatal("未设置 ALLOW_INSECURE_LOCAL_DEMO 时必须默认关闭")
	}
}

func TestAllowInsecureLocalDemo_explicitTrue(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_LOCAL_DEMO", "true")
	got, err := allowInsecureLocalDemo()
	if err != nil {
		t.Fatalf("true 不应报错: %v", err)
	}
	if !got {
		t.Fatal("ALLOW_INSECURE_LOCAL_DEMO=true 必须显式启用")
	}
}

func TestAllowInsecureLocalDemo_invalidRejected(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_LOCAL_DEMO", "banana")
	if _, err := allowInsecureLocalDemo(); err == nil {
		t.Fatal("非法值必须拒绝启动（fail-closed），不得宽松解析")
	}
}

package executionhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fujiabao89/webdb/internal/browse"
	"github.com/google/uuid"
)

const (
	testUserID = "11111111-1111-1111-1111-111111111111"
	testWorkID = "22222222-2222-2222-2222-222222222222"
)

// TestParsePrincipalConfig 验证服务端演示 Principal 配置解析（D01b fail-closed）：
// 缺失或非法配置返回错误（调用方启动 fatal），合法配置解析为可信 Principal。
func TestParsePrincipalConfig(t *testing.T) {
	t.Run("missing config is fatal", func(t *testing.T) {
		t.Setenv(envWorkspaceID, "")
		t.Setenv(envUserID, "")
		if _, err := PrincipalFromEnv(); err == nil {
			t.Fatal("配置缺失应返回错误（启动 fatal）")
		}
	})

	t.Run("partial config is fatal", func(t *testing.T) {
		t.Setenv(envWorkspaceID, testWorkID)
		t.Setenv(envUserID, "")
		if _, err := PrincipalFromEnv(); err == nil {
			t.Fatal("配置不完整应返回错误")
		}
	})

	t.Run("invalid uuid is fatal", func(t *testing.T) {
		t.Setenv(envWorkspaceID, "not-a-uuid")
		t.Setenv(envUserID, testUserID)
		if _, err := PrincipalFromEnv(); err == nil {
			t.Fatal("非法 UUID 应返回错误")
		}
	})

	t.Run("valid config", func(t *testing.T) {
		t.Setenv(envWorkspaceID, testWorkID)
		t.Setenv(envUserID, testUserID)
		p, err := PrincipalFromEnv()
		if err != nil {
			t.Fatalf("合法配置解析失败: %v", err)
		}
		if p.UserID.String() != testUserID || p.WorkspaceID.String() != testWorkID {
			t.Errorf("principal = %+v", p)
		}
	})
}

// TestPrincipalMiddleware 验证中间件注入可信 Principal 到请求 context，
// 且不信任客户端提供的任何身份字段。
func TestPrincipalMiddleware(t *testing.T) {
	p := browse.Principal{
		UserID:      uuid.MustParse(testUserID),
		WorkspaceID: uuid.MustParse(testWorkID),
	}
	mw := PrincipalMiddleware(p)
	var got browse.Principal
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gp, ok := PrincipalFromContext(r.Context())
		if !ok {
			t.Error("context 中无 Principal")
			return
		}
		got = gp
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// 客户端自报身份必须被忽略（中间件以服务端配置为准）。
	req.Header.Set("X-User-Id", "99999999-9999-9999-9999-999999999999")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if got != p {
		t.Errorf("principal = %+v, want %+v（客户端身份被忽略）", got, p)
	}
}

// TestPrincipalFromContextMissing 验证未注入 context 时返回失败（fail-closed → 401）。
func TestPrincipalFromContextMissing(t *testing.T) {
	if _, ok := PrincipalFromContext(context.Background()); ok {
		t.Fatal("空 context 不应返回 Principal")
	}
}

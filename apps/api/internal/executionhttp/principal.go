package executionhttp

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/fujiabao89/webdb/internal/browse"
	"github.com/google/uuid"
)

// 演示 Principal 服务端环境配置（D01b 方案 A：浏览器不自报身份）。
// 配置缺失/非法由调用方拒绝启动（fatal），禁止零值/默认值/客户端身份回退。
const (
	envWorkspaceID = "DEMO_PRINCIPAL_WORKSPACE_ID"
	envUserID      = "DEMO_PRINCIPAL_USER_ID"
)

// PrincipalFromEnv 从服务端环境配置解析固定演示 Principal（D01b）。
// 任一配置缺失或不是合法 UUID 均返回错误 → 调用方启动 fatal（fail-closed）。
// 禁止以零值、默认值或任何客户端提供的身份作为回退。
func PrincipalFromEnv() (browse.Principal, error) {
	ws, err := uuid.Parse(os.Getenv(envWorkspaceID))
	if err != nil {
		return browse.Principal{}, fmt.Errorf("演示 Principal 配置 %s 缺失或非法", envWorkspaceID)
	}
	user, err := uuid.Parse(os.Getenv(envUserID))
	if err != nil {
		return browse.Principal{}, fmt.Errorf("演示 Principal 配置 %s 缺失或非法", envUserID)
	}
	if ws == uuid.Nil || user == uuid.Nil {
		return browse.Principal{}, fmt.Errorf("演示 Principal 配置不得为零值 UUID")
	}
	return browse.Principal{UserID: user, WorkspaceID: ws}, nil
}

type principalKey struct{}

// PrincipalMiddleware 把服务端可信 Principal 注入请求 context。
// 客户端自报的 user/role/workspace 一律忽略（D01b）。
func PrincipalMiddleware(p browse.Principal) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), principalKey{}, p)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// PrincipalFromContext 从请求 context 读取可信 Principal。
// 返回 ok=false 表示未认证（handler 应返回 unauthorized，D01b）。
func PrincipalFromContext(ctx context.Context) (browse.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(browse.Principal)
	if !ok || p.UserID == uuid.Nil || p.WorkspaceID == uuid.Nil {
		return browse.Principal{}, false
	}
	return p, true
}

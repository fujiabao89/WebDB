package browse

import "github.com/google/uuid"

// Principal 由可信服务端上下文派生的已验证身份（D01b）。
// 浏览器不得自报 actor/角色；请求级 workspace_id 覆盖一律忽略。
type Principal struct {
	UserID      uuid.UUID
	WorkspaceID uuid.UUID
}

// valid 校验 Principal 是否来自可信解析（非零值）。
// fail-closed：零值 Principal 视为未认证。
func (p Principal) valid() bool {
	return p.UserID != uuid.Nil && p.WorkspaceID != uuid.Nil
}

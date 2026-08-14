// Package seeddemo 提供 Compose 演示环境的身份/连接/凭证最小 seed。
// 仅限本地演示（WEBDB_DEMO_SEED=true 门控），默认关闭；不注册 HTTP API，
// 不影响 serve 与 migrate。
//
// 安全边界（Owner 冻结约束，WEB-40）：
//   - 固定合成 UUID 全部集中在本文件（单一权威来源，禁止在多处手工复制漂移）。
//   - 演示数据库密码仅从部署环境读取并交由 credentials.LifecycleManager 加密，
//     绝不写入 SQL/文件/命令行/日志；KEK 缺失时 fail-closed。
//   - 所有写操作幂等：一致即成功（no-op），固定 ID 冲突 fail-closed，不静默覆盖。
package seeddemo

import (
	"github.com/google/uuid"
)

// ---- 固定合成 UUID（单一权威来源） ------------------------------------------
//
// 这些是演示环境的固定合成标识，不含真实用户/生产数据。
// 与 deploy/compose 中注入的 DEMO_PRINCIPAL_* 环境变量必须完全一致；
// seed 启动时校验 env 值，不一致即 fail-closed（防漂移）。

const (
	// DemoWorkspaceID 演示工作区固定 UUID。
	DemoWorkspaceID = "f1160d75-26f7-46e0-b3e0-570ea65c232e"
	// DemoUserID 演示 active user 固定 UUID。
	DemoUserID = "73e8c8f0-83e4-4096-8400-15ca15073d6b"
	// DemoPostgresConnectionID 演示 PostgreSQL 连接固定 UUID。
	DemoPostgresConnectionID = "d80a86cb-d4e7-4afb-9ab1-8df572ad9c69"
	// DemoMySQLConnectionID 演示 MySQL 连接固定 UUID。
	DemoMySQLConnectionID = "37760cae-7ddc-408d-b2b0-4505c3ea2243"
	// DemoForeignWorkspaceID 第二合成工作区固定 UUID（WEB-39 跨租户隔离 E2E fixture）。
	DemoForeignWorkspaceID = "aaaaaaa1-0000-4000-8000-000000000001"
	// DemoForeignUserID 第二合成工作区 owner user 固定 UUID（非演示 Principal）。
	DemoForeignUserID = "aaaaaaa2-0000-4000-8000-000000000002"
	// DemoForeignConnectionID 第二合成工作区的 foreign 连接固定 UUID（演示 Principal 不可见）。
	DemoForeignConnectionID = "aaaaaaa3-0000-4000-8000-000000000003"
)

// 解析后的固定 UUID（启动时校验 env 与之一致）。
var (
	demoWorkspaceID = mustParseUUID(DemoWorkspaceID)
	demoUserID      = mustParseUUID(DemoUserID)
	demoPGConnID    = mustParseUUID(DemoPostgresConnectionID)
	demoMySQLConnID = mustParseUUID(DemoMySQLConnectionID)
)

func mustParseUUID(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		panic("seeddemo: 固定合成 UUID 非法: " + s)
	}
	return id
}

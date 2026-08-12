# ADR-019：Compose 演示 bootstrap（migrate + seed 步骤集成）

> 状态：已接受｜日期：2026-08-11｜Owner：fujiabao89（Owner 冻结约束，WEB-40）

## 背景

WEB-35（PR #48）的 Compose 冒烟实测失败：`docker compose up --build -d` 后
`api` 容器反复重启，日志 `演示 Principal 配置 DEMO_PRINCIPAL_WORKSPACE_ID 缺失或非法`。
根因链：

1. Compose 未提供 `migrate up` 步骤，空卷元数据库 `webdb_meta` 无业务表；
2. 无演示身份 seed（workspace / user / workspace_member）；
3. 无与可信 Principal 对应的固定合成 UUID；
4. 无用于连接浏览与只读查询的演示 connection / policy / credential。

本 ADR 记录 WEB-40 在 `deploy/compose` 建立安全、幂等、仅限本地演示的启动链路的决策。
不修改 WEB-35/38 的执行/分页/审计语义，不注册 HTTP 路由。

## 决策

1. **独立 one-shot 迁移服务**：Compose 新增 `api-migrate`，复用 `apps/api` dev 镜像，
   `command: ["migrate", "up"]`，使用独立管理员 `META_MIGRATE_USER/PASSWORD`；
   `depends_on: webdb-meta: service_healthy`；`restart: "no"`。
   API `serve` 本身不自动迁移（延续 ADR-013）。
2. **独立 one-shot 演示 seed 服务**：Compose 新增 `demo-seed`，
   `command: ["seed-demo"]`，仅当 `WEBDB_DEMO_SEED=true` 时执行（严格显式 true，
   缺失/false/非法值拒绝）；`depends_on: api-migrate: service_completed_successfully`；
   `restart: "no"`。生产部署不设置该开关则**绝不自动 seed**。
3. **依赖链 fail-closed**：`webdb-meta healthy → api-migrate completed → demo-seed completed
   → api healthy → web healthy`。migrate/seed 任一失败，下游均不启动。
4. **固定合成 UUID 单一权威来源**：`apps/api/internal/seeddemo/ids.go` 集中定义
   workspace / user / PG connection / MySQL connection 四个固定合成 UUID；
   Compose `DEMO_PRINCIPAL_*` 由 env 注入，`demo-seed` 启动时校验与权威值一致，
   不一致即 fail-closed（防多处手工复制漂移）。
5. **凭证创建仅经 LifecycleManager**：演示数据库密码只从环境读取并交由
   `credentials.LifecycleManager.Create` 加密（Envelope v1、随机 nonce/DEK、
   正确 AAD、E3 审计原子提交）；**禁止**直接 SQL INSERT 手工构造 credential_envelope，
   禁止明文密码进入 SQL/文件/命令行/日志。
6. **KEK 版本化**：Compose 注入 `WEBDB_KEK_V1` + `WEBDB_ACTIVE_KEK_VERSION`
   （非旧的单变量 `WEBDB_KEK`，其无任何代码读取）；缺失/非法时 seed fail-closed。
7. **幂等与部分失败**：身份/连接/策略/凭证写入均幂等——一致即 no-op，固定 ID 冲突
   fail-closed，不静默覆盖；seed 中途失败遗留的孤立 active envelope（未被连接引用）
   在下次 seed 时**明确拒绝**而非静默复用（避免 PG/MySQL 负载相同时误匹配）。
8. **connection/connection_policy 字段遵守现有 Schema 与安全默认**：
   `allow_read=true` 显式、`max_rows=500`、`statement_timeout_ms=5000`、
   不启用 DML/DDL/export；不新增字段或默认值。

## 候选方案与取舍

- **seed 走 HTTP API**：否决。引入攻击面与鉴权复杂性；演示 bootstrap 无此需求。
- **seed 复用现有 metadata store 固定 ID**：现有 `CreateUser`/`CreateWorkspace` 不允许
  指定 ID，无法满足固定合成 UUID，属任务书允许的"缺少安全接口"情形，
  在受控内部 seed 包 `internal/seeddemo` 使用参数化、单语句、无拼接 SQL。
- **复用孤立 envelope 恢复部分失败**：否决。PG/MySQL 演示负载可相同（`demo_reader`），
  无法可靠区分归属；改为明确拒绝并引导 `down -v` 冷启动。
- **`WEBDB_KEK` 单变量**：废弃。与 `credentials.NewEnvKEKProvider` 的版本化
  `WEBDB_KEK_V{N}` 契约不一致且无读取方。

## 后果

- 安全：浏览器/Web 容器永不接触数据库密码、KEK、`secret_ref`/`secret_version`；
  演示密码仅存加密密文；seed 日志只报告固定资源类别/数量。
- 运营：首次启动与重复冷启动均须经过 migrate+seed；迁移/seed 故障会导致上游不启动
  （fail-closed 的可诊断行为，`docker compose logs api-migrate/demo-seed` 可定位）。
- 兼容：`WEBDB_KEK` 单变量被移除；对依赖它的部署需改用 `WEBDB_KEK_V1`。
- 测试：新增 seed 单元/集成测试与 Compose 冒烟、重复冷启动、故障注入验证。

## 验证与回滚/替代条件

- 验证：`docker compose config --quiet`；`docker compose up --build -d` 后
  `api-migrate`/`demo-seed` 均 exit 0、`api` 不重启、`/health` 200、Web healthy、
  元数据库恰有 1 workspace / 1 user / 2 connection / 2 policy / 2 envelope；
  重复冷启动（`down -v` + `up`）再次成功；migrate 失败或 `WEBDB_DEMO_SEED` 缺失时
  `api` 不启动。
- 回滚：移除 `api-migrate`/`demo-seed` 两个服务并把 `api.depends_on` 恢复为原三项；
  移除 `WEBDB_KEK_V1`/`WEBDB_ACTIVE_KEK_VERSION` 注入。

## 相关资料

- WEB-40 Linear Issue
- P0-06A §5.2 D01b（服务端固定演示 Principal，fail-closed）
- ADR-013（迁移机制）、ADR-006/017（KEK 与凭证信封）、ADR-018（compose 目录边界）

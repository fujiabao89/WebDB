# ADR-013：P0 元数据库迁移与 Schema 基线

> 状态：已接受｜日期：2026-07-19｜Owner：WebDB Owner

## 背景

P0-02 需要在 PostgreSQL 16 元数据库中建立可重复验证的迁移和最小数据模型。现有设计稿已给出逻辑实体，但尚未固定迁移工具、P0 表边界、追加式审计的数据库约束，以及连接凭证信封的引用字段。

这些选择会约束 P0-05 的凭证与审计实现，因此必须在 P0-02 进入 Ready 前明确。

## 决策

### 迁移工具与执行方式

- 使用 `pressly/goose/v3`，只采用 SQL migration；具体补丁版本由 `go.mod` / `go.sum` 锁定。
- migration 使用顺序编号，文件同时提供 `Up` 与 `Down`，默认在事务内执行；禁止在 migration 中启用环境变量替换或写入凭证。
- 同一组 SQL migration 由专用迁移命令显式执行并可嵌入 Go 二进制；API 进程启动时不自动迁移，避免运行时账号获得 DDL 权限。
- CI 至少验证 migration 静态校验、空库 `up`、完整 `down`、再次 `up`，以及已到最新版本时重复执行 `up` 无副作用。

### P0 表与租户约束

P0 初始 migration 只建立下列表，不提前创建查询文档、审批、导出或协作表：

| 表 | P0 最小字段与约束 |
| --- | --- |
| `users` | UUID 主键；`email`；`password_hash`；`status`；预留 `identity_provider`、`external_subject`、`external_tenant`；时间戳；`lower(email)` 唯一 |
| `workspaces` | UUID 主键；`name`；对象型 `settings` JSONB；时间戳 |
| `workspace_members` | `workspace_id`、`user_id`、`role`；复合主键；角色限制为 Owner/Admin/Editor/Viewer |
| `credential_envelopes` | `workspace_id`、`secret_ref`、`version` 复合唯一；密文、nonce、wrapped DEK、wrap nonce、版本化 `envelope_suite`、`kek_version`、创建/退役时间 |
| `connections` | UUID 主键；`workspace_id`；`UNIQUE (workspace_id, id)` 复合外键目标；工作区内唯一 `name`；引擎、主机、端口、数据库、环境；`secret_ref` + `secret_version`；创建者与时间戳 |
| `connection_policies` | `workspace_id` + `connection_id`；读/写/导出开关；语句超时与行数上限；P0 默认只读、禁止写入和导出 |
| `executions` | UUID 主键；`workspace_id` + `connection_id`；actor、statement hash、状态、trace ID、开始/结束时间、耗时、行数、脱敏错误码、结果引用与过期时间；非空结果引用必须有过期时间 |
| `audit_events` | UUID 主键；工作区与可空 system actor/connection；action、resource type/id、outcome、对象型脱敏 metadata、trace/execution ID、`occurred_at`；不含 `updated_at` |

所有工作区子资源使用包含 `workspace_id` 的复合外键，数据库层拒绝跨工作区引用；`connections` 必须提供 `UNIQUE (workspace_id, id)` 作为 connection policy、execution 与 audit 的复合外键目标。`audit_events.connection_id` 使用 `(workspace_id, connection_id)` 可空复合外键且禁止级联删除，并建立 `(workspace_id, connection_id, occurred_at)` 索引。`executions` 提供 `UNIQUE (workspace_id, connection_id, id)`；`audit_events.execution_id` 非空时 `connection_id` 必须同时非空，并通过 `(workspace_id, connection_id, execution_id)` 复合外键引用 execution，从而拒绝同一工作区内错误的 connection/execution 组合。

P0 枚举与默认值固定如下；所有列均为 `text NOT NULL`，后续新增值必须通过前向 migration 扩展 `CHECK`：

| 字段 | 允许值 | 默认值 |
| --- | --- | --- |
| `users.status` | `active`, `disabled` | `active` |
| `workspace_members.role` | `owner`, `admin`, `editor`, `viewer` | 无，调用方必须显式提供 |
| `connections.engine` | `postgresql`, `mysql` | 无，调用方必须显式提供 |
| `connections.environment` | `development`, `staging`, `production` | `development` |
| `executions.status` | `pending`, `running`, `completed`, `failed`, `cancelled` | `pending` |
| `audit_events.outcome` | `succeeded`, `failed`, `denied`, `cancelled` | 无，调用方必须显式提供 |

连接端口限制为 1–65535；credential/secret version、超时和行数上限必须为正整数；policy 布尔列均为 `NOT NULL`，默认 `allow_read=true`、`allow_write=false`、`allow_export=false`。`executions` 使用 `CHECK (result_ref IS NULL OR result_expires_at IS NOT NULL)`，访问层为持久化结果写入默认 7 天过期时间；时间使用 UTC `timestamptz`。

### 追加式审计

- 普通应用仓储仅提供 `Append` 与查询能力，不提供 update/delete 方法。
- 数据库对 `audit_events` 的 `UPDATE`、`DELETE`、`TRUNCATE` 安装拒绝触发器；未来拆分数据库角色后，运行时角色只授予 `SELECT` / `INSERT`。
- 审计写入必须携带 workspace、action、resource、outcome、trace ID，并在适用时携带 actor、受租户外键约束且彼此一致的 connection 和 execution 关联。
- `metadata` 仅接受 JSON object；应用层使用允许列表生成脱敏摘要，禁止 SQL 正文、凭证、KEK、目标库结果和原始错误进入审计正文。
- 审计事件不随查询结果过期而删除；保留期或归档策略仍是后续独立决策。

### 凭证字段

- `connections` 仅保存不透明 `secret_ref` 和明确的 `secret_version`，不保存用户名、密码、密文、DEK、KEK 或 nonce。
- 本地信封记录隔离在 `credential_envelopes`；以 `(workspace_id, secret_ref, version)` 作为引用边界。轮换时先追加新版本，再原子更新连接引用；旧版本只标记退役，不覆盖密文。
- `credential_envelopes` 只保存 `ciphertext`、`data_nonce`、`wrapped_dek`、`wrap_nonce`、`envelope_suite`、`kek_version` 和生命周期元数据。`envelope_suite` 是版本化格式标识，必须足以确定数据加密、DEK wrapping、AAD 与 payload 格式；KEK 本体继续只由部署环境注入。
- 具体 AEAD/KDF、payload 编码、KEK 轮换流程及外部 KMS 适配由 P0-05 单独决策和测试；P0-02 不自行固定加密算法。

## 候选方案与取舍

- `golang-migrate/migrate` 同样成熟且支持独立 up/down 文件，但 P0 更需要一份 SQL 文件内成对审查、事务默认值、`validate` 和嵌入式 migration；因此选择 Goose。
- ORM 自动迁移会弱化可审查的 SQL、回滚与数据库约束，不采用。
- 将密文直接放在 `connections` 会扩大普通连接查询的敏感字段暴露面，也不利于追加轮换版本；因此使用独立信封表。
- 只依赖仓储约定不足以防止误更新审计表；因此同时使用数据库拒绝触发器。哈希链和外部归档不属于 P0。

## 后果

- 新增 Goose 和 PostgreSQL Go 驱动依赖时，必须同步更新依赖许可证清单；两者采用 MIT 许可证，与 Apache License 2.0 兼容。
- migration 执行身份与 API 运行身份需保持概念分离；P0 本地 Compose 尚未拆分角色时，拒绝触发器仍提供普通路径防护。
- `Down` 仅用于开发、测试和受控回滚；生产数据回滚优先使用前向修复，禁止未经备份直接执行破坏性 down。
- P0-05 若改变加密封装、密钥版本语义或审计保留策略，必须用新 ADR 替代本决策的对应部分。

## 验证与回滚/替代条件

- 集成测试验证空 PostgreSQL 16 数据库的 `up -> down -> up -> up`、非法枚举值拒绝、跨工作区拒绝（含审计连接关联）、同工作区不同连接的 execution 关联拒绝、无过期时间的非空 `result_ref` 拒绝、审计 update/delete/truncate 拒绝，以及 Schema 中不存在明文凭证字段。
- migration 工具不可维护、许可证不兼容或不能可靠支持事务/嵌入执行时，以新 ADR 替代 Goose。
- Schema 尚未进入共享环境时可通过回退本 ADR 与任务卡恢复 Backlog；进入共享环境后只允许新增前向 migration 修正。

## 相关资料

- [ADR-006：KEK 由部署环境注入](ADR-006-kek-in-deployment-environment.md)
- [ADR-010：查询结果保留 7 天](ADR-010-query-result-retention.md)
- [ADR-011：本地账号优先，后续 OIDC/SSO](ADR-011-local-auth-then-oidc.md)
- [P0-02：元数据库与迁移](../tasks/P0-02-metadata-and-migrations.md)
- [Goose 官方仓库](https://github.com/pressly/goose)

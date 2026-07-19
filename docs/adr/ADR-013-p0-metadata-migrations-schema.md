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
| `users` | UUID 主键；非空、非空串且无首尾空白的 `email`；非空非空白 `password_hash`；`status`；预留 `identity_provider`、`external_subject`、`external_tenant`；时间戳；`lower(email)` 唯一 |
| `workspaces` | UUID 主键；`name`；对象型 `settings` JSONB；时间戳 |
| `workspace_members` | 非空 `workspace_id`、`user_id`、`role`；复合主键；分别外键引用真实 workspace/user；角色限制为 Owner/Admin/Editor/Viewer |
| `credential_envelopes` | `UNIQUE (workspace_id, secret_ref, version)`；`workspace_id` 直接外键引用真实 workspace，三列均非空且 version 为正；非空且非零长度的密文、nonce、wrapped DEK、wrap nonce、版本化 `envelope_suite`、正整数 `kek_version`、创建/退役时间 |
| `connections` | UUID 主键；非空 `workspace_id`；`UNIQUE (workspace_id, id)` 复合外键目标；工作区内唯一 `name`；引擎、主机、端口、数据库、显式环境；受租户约束且非空的 UUID `secret_ref` + 正整数 `secret_version`；非空创建者与时间戳 |
| `connection_policies` | `PRIMARY KEY (workspace_id, connection_id)`，确保每个连接至多一条策略；读/写/导出开关；`statement_timeout_ms` 与 `max_rows`；P0 默认只读、禁止写入和导出，缺失策略时拒绝全部操作 |
| `executions` | UUID 主键；非空 `workspace_id` + `connection_id`；非空 actor、statement hash、状态、非空 trace ID、开始/结束时间、耗时、行数、脱敏错误码、结果引用与过期时间；非空结果引用必须有过期时间 |
| `audit_events` | UUID 主键；非空工作区、`actor_type` 与条件非空 actor、可空 connection；非空非空白 action、resource type/id、outcome、对象型脱敏 metadata、非空 trace ID、可空 execution ID、非空 `occurred_at`；不含 `updated_at` |

所有工作区子资源的 `workspace_id` 均为 `NOT NULL`；所有必选复合外键分量均为 `NOT NULL`，不得依赖 PostgreSQL `MATCH SIMPLE` 接受部分空值。`connections` 必须提供 `UNIQUE (workspace_id, id)` 作为 connection policy、execution 与 audit 的复合外键目标。`connection_policies` 的 `workspace_id`、`connection_id` 由复合主键强制非空并拒绝同一连接的重复或冲突策略；访问层查询不到策略行时必须默认拒绝读、写、导出和执行，不得回退到宽松默认值。`audit_events.workspace_id` 为 `NOT NULL`；只有与事件无关时 `connection_id` 才可为空，并使用 `(workspace_id, connection_id)` `MATCH SIMPLE` 复合外键且禁止级联删除，另建立 `(workspace_id, connection_id, occurred_at)` 索引。`executions.workspace_id` 与 `connection_id` 均为 `NOT NULL`，并提供 `UNIQUE (workspace_id, connection_id, id)`；`audit_events.execution_id` 可空，但非空时 `connection_id` 必须同时非空，并通过 `(workspace_id, connection_id, execution_id)` 复合外键引用 execution，从而拒绝同一工作区内错误的 connection/execution 组合。

`workspace_members.workspace_id` 与 `user_id` 分别使用外键引用 `workspaces(id)` 和 `users(id)` 且禁止级联删除，孤儿成员不能成为后续授权或审计依据。`credential_envelopes.workspace_id` 与 `audit_events.workspace_id` 均使用 `FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT`；因此即使信封尚未被连接引用，或 system audit 没有 actor/connection/execution，仍不能写入不存在或已删除工作区的记录。连接凭证的 `secret_ref UUID NOT NULL` 与 `secret_version INTEGER NOT NULL CHECK (secret_version > 0)` 使用 `FOREIGN KEY (workspace_id, secret_ref, secret_version) REFERENCES credential_envelopes (workspace_id, secret_ref, version) ON DELETE RESTRICT`，数据库层拒绝空引用、不存在或错误版本及其他工作区的信封引用。

`connections.created_by` 与 `executions.actor_id` 为 `NOT NULL`。`audit_events.actor_type` 为 `TEXT NOT NULL`，只允许 `user` / `system`，并使用 `CHECK ((actor_type = 'user' AND actor_id IS NOT NULL) OR (actor_type = 'system' AND actor_id IS NULL))`，禁止用空 actor 冒充用户事件。三类非空用户引用分别使用 `FOREIGN KEY (workspace_id, created_by)` 或 `FOREIGN KEY (workspace_id, actor_id)` 指向 `workspace_members (workspace_id, user_id)` 且禁止级联删除，从而拒绝非成员或错误工作区 actor。

P0 枚举与默认值固定如下；所有列均为 `text NOT NULL`，后续新增值必须通过前向 migration 扩展 `CHECK`：

| 字段 | 允许值 | 默认值 |
| --- | --- | --- |
| `users.status` | `active`, `disabled` | `active` |
| `workspace_members.role` | `owner`, `admin`, `editor`, `viewer` | 无，调用方必须显式提供 |
| `connections.engine` | `postgresql`, `mysql` | 无，调用方必须显式提供 |
| `connections.environment` | `development`, `staging`, `production` | 无，调用方必须显式提供 |
| `executions.status` | `pending`, `running`, `completed`, `failed`, `cancelled` | `pending` |
| `audit_events.actor_type` | `user`, `system` | 无，调用方必须显式提供 |
| `audit_events.outcome` | `succeeded`, `failed`, `denied`, `cancelled` | 无，调用方必须显式提供 |

`users.email` 使用 `TEXT NOT NULL`、`CHECK (email = btrim(email) AND email <> '')` 和 `UNIQUE (lower(email))`，数据库拒绝首尾空白，避免规范化后出现重复本地身份。P0 仅创建本地账号，`password_hash` 使用 `TEXT NOT NULL CHECK (btrim(password_hash) <> '')`；后续支持 OIDC-only 用户时必须通过前向 migration 引入明确 auth-type 判别器并调整该约束。连接端口限制为 1–65535；连接 `environment` 无数据库默认值，创建/导入调用方必须显式分类，避免把未分类的生产库降级成 development；credential/secret version、`statement_timeout_ms` 与 `max_rows` 必须是调用方显式提供的正整数；policy 布尔列均为 `NOT NULL`，默认 `allow_read=true`、`allow_write=false`、`allow_export=false`。`executions` 使用 `CHECK (result_ref IS NULL OR result_expires_at IS NOT NULL)`，访问层为持久化结果写入默认 7 天过期时间；时间使用 UTC `timestamptz`。

### 追加式审计

- 普通应用仓储仅提供 `Append` 与查询能力，不提供 update/delete 方法。
- 数据库对 `audit_events` 的 `UPDATE`、`DELETE`、`TRUNCATE` 安装拒绝触发器；未来拆分数据库角色后，运行时角色只授予 `SELECT` / `INSERT`。
- `audit_events.workspace_id` 必须直接外键引用真实 workspace；`action`、`resource_type`、`resource_id`、`trace_id` 均使用 `TEXT NOT NULL`，并分别以 `CHECK (btrim(action) <> '')`、`CHECK (btrim(resource_type) <> '')`、`CHECK (btrim(resource_id) <> '')`、`CHECK (btrim(trace_id) <> '')` 拒绝空白；`occurred_at` 使用无默认值的 `TIMESTAMPTZ NOT NULL`。即使拒绝发生在资源解析前，调用方也必须写入稳定的目标资源标识和显式事件时间。审计写入还必须携带 outcome，并在适用时携带 actor、受租户外键约束且彼此一致的 connection 和 execution 关联。
- `metadata` 使用 `JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')`；应用层使用允许列表生成脱敏摘要，禁止 SQL 正文、凭证、KEK、目标库结果和原始错误进入审计正文。
- 审计事件不随查询结果过期而删除；保留期或归档策略仍是后续独立决策。

### 凭证字段

- `connections` 仅保存不透明 `secret_ref` 和明确的 `secret_version`，不保存用户名、密码、密文、DEK、KEK 或 nonce。
- 本地信封记录隔离在 `credential_envelopes`；以 `(workspace_id, secret_ref, version)` 作为引用边界。轮换时先追加新版本，再原子更新连接引用；旧版本只标记退役，不覆盖密文。
- `credential_envelopes` 的 `workspace_id UUID NOT NULL`、`secret_ref UUID NOT NULL`、`version INTEGER NOT NULL CHECK (version > 0)` 构成唯一引用边界；`ciphertext`、`data_nonce`、`wrapped_dek`、`wrap_nonce` 均为 `BYTEA NOT NULL`，并分别以 `CHECK (octet_length(ciphertext) > 0)`、`CHECK (octet_length(data_nonce) > 0)`、`CHECK (octet_length(wrapped_dek) > 0)`、`CHECK (octet_length(wrap_nonce) > 0)` 拒绝零长度值；`envelope_suite` 为 `TEXT NOT NULL CHECK (btrim(envelope_suite) <> '')`，`kek_version` 为 `INTEGER NOT NULL CHECK (kek_version > 0)`。这些约束保证被连接引用的信封在结构上完整；`envelope_suite` 必须足以确定数据加密、DEK wrapping、AAD 与 payload 格式，KEK 本体继续只由部署环境注入。
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

- 集成测试验证空 PostgreSQL 16 数据库的 `up -> down -> up -> up`、空/首尾空白用户 email、重复规范化 email、空 password hash 与非法/缺失环境拒绝、所有必选租户外键分量的空值拒绝、孤儿 workspace member、孤儿 credential envelope 与孤儿 system audit 拒绝、重复 connection policy、非正 `max_rows` 及缺失策略时访问层默认拒绝、空/不完整/缺失/错误版本/跨工作区凭证信封引用拒绝、非成员或跨工作区 actor 拒绝、user/system actor 组合不一致拒绝、同工作区不同连接的 execution 关联拒绝、无过期时间的非空 `result_ref` 拒绝、审计缺失/空白 action/resource/time/trace ID 与非对象 metadata 拒绝、审计 update/delete/truncate 拒绝，以及 Schema 中不存在明文凭证字段。
- migration 工具不可维护、许可证不兼容或不能可靠支持事务/嵌入执行时，以新 ADR 替代 Goose。
- Schema 尚未进入共享环境时可通过回退本 ADR 与任务卡恢复 Backlog；进入共享环境后只允许新增前向 migration 修正。

## 相关资料

- [ADR-006：KEK 由部署环境注入](ADR-006-kek-in-deployment-environment.md)
- [ADR-010：查询结果保留 7 天](ADR-010-query-result-retention.md)
- [ADR-011：本地账号优先，后续 OIDC/SSO](ADR-011-local-auth-then-oidc.md)
- [P0-02：元数据库与迁移](../tasks/P0-02-metadata-and-migrations.md)
- [Goose 官方仓库](https://github.com/pressly/goose)

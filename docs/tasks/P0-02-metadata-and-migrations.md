# P0-02：元数据库与迁移

> 状态：Done｜风险：Medium｜依赖：P0-01、ADR-006、ADR-010、ADR-011、ADR-013｜实现者：Claude Code｜独立审查：Codex（7 轮）｜PR：[#10](https://github.com/fujiabao89/WebDB/pull/10)

## 目标与范围

实现 WebDB 元数据库迁移及访问层，覆盖用户、工作区、成员、连接、连接策略、执行和追加式审计的 P0 最小字段。

不实现完整登录、协作状态、变更审批、对象存储导出或生产数据迁移。

## Ready 决策

- 迁移：采用 ADR-013 的 `pressly/goose/v3` SQL-only 顺序 migration；显式迁移命令执行，API 启动不自动迁移。
- 表结构：P0 限定为 `users`、`workspaces`、`workspace_members`、`credential_envelopes`、`connections`、`connection_policies`、`executions`、`audit_events`，并用含 `workspace_id` 的复合约束阻止跨租户引用。
- 枚举：ADR-013 固定用户/执行状态、成员角色、连接引擎/环境和审计结果的允许值、默认值与非空语义；扩展值必须使用前向 migration。
- 追加式审计：仓储只暴露追加/查询，数据库拒绝 `UPDATE`、`DELETE`、`TRUNCATE`；审计 metadata 由允许列表生成脱敏对象。
- 凭证：连接只保存 `secret_ref` + `secret_version`；信封表保存密文、nonce、wrapped DEK、版本化 `envelope_suite` 和 `kek_version`，不保存 KEK 或明文。具体密码学算法由 P0-05 决策。

## 验收标准

| 验收项 | 证据 |
| --- | --- |
| 迁移可在空库 up/down，重复执行安全 | 自动化迁移测试 |
| 工作区成员、连接名称等唯一约束与外键可阻止越权/重复数据；所有必选租户外键分量非空 | 集成测试与空分量负向测试 |
| `connections(workspace_id, id)` 唯一键可作为所有租户连接复合外键目标 | 空库 migration 与外键集成测试 |
| 本地用户 email 非空且无首尾空白、大小写不敏感唯一，password hash 非空；成员引用真实 user/workspace；所有非空创建者/actor 必须是对应工作区成员 | 用户规范化/非空、复合外键、孤儿成员与错误工作区测试 |
| 连接只保存 `secret_ref`/密文元数据，不保存明文密码 | Schema 与日志测试 |
| 凭证信封直接引用真实 workspace，且密文、两类 nonce、wrapped DEK、suite 与 KEK 版本结构完整；连接引用必须指向同工作区存在的准确版本 | workspace 外键、信封非空/非空值约束、三列复合外键及缺失/错误版本测试 |
| 每个连接至多一条策略，`max_rows` 为正数；重复/非法策略被拒绝，缺失策略默认拒绝全部操作 | 复合主键、`CHECK` 与访问层默认拒绝集成测试 |
| `audit_events` 无普通更新/删除路径，元数据脱敏 | 仓储/API 与测试审查 |
| 审计直接引用真实 workspace；action、resource type/id、trace ID 非空非空白，事件时间非空；metadata 只能是 JSON object | workspace 外键、非空/非空白与 `jsonb_typeof` 约束测试 |
| 审计 `actor_type=user/system` 与 actor 空值语义一致 | actor 判别器 `CHECK` 集成测试 |
| 审计连接关联不能跨工作区，且可按工作区/连接/时间检索 | 复合外键与索引集成测试 |
| 审计 connection/execution 必须属于同一工作区的同一连接 | 三列复合外键集成测试 |
| 非空 `result_ref` 必须有过期时间，默认 7 天 | Schema 约束与访问层集成测试 |
| 非法状态、角色、引擎、环境和审计结果被数据库拒绝；连接环境缺失也被拒绝 | `NOT NULL` 与 `CHECK` 约束集成测试 |

## 验证命令

实现完成后至少执行以下命令；integration suite 使用 Compose 的合成元数据库，不接触真实凭证或生产数据。

```bash
go -C apps/api test ./...
go -C apps/api vet ./...
go -C apps/api test -tags=integration ./internal/metadata/...
```

集成测试必须覆盖空库 `up -> down -> up -> up`、空/首尾空白/规范化重复 email 与空 password hash 拒绝、非法枚举及缺失连接环境拒绝、所有必选租户外键分量空值拒绝、孤儿 workspace member/credential envelope/system audit 拒绝、重复 connection policy、非正 `max_rows` 及缺失策略时访问层默认拒绝、空/不完整/缺失/错误版本/跨工作区凭证信封引用拒绝、非成员或跨工作区 actor 拒绝、user/system actor 组合不一致拒绝、同工作区不同连接的 execution 关联拒绝、无过期时间的非空 `result_ref` 拒绝、审计缺失或空白 action/resource/time/trace ID 与非对象 metadata 拒绝、审计 update/delete/truncate 拒绝，以及 Schema/日志中不存在明文凭证字段。

## 升级条件

字段语义、保留期、加密封装或身份模型冲突时更新 ADR 后再实现。P0-05 复用本任务的连接/执行/审计模型。

## 实施摘要

### 最终交付

| 类别 | 内容 |
|------|------|
| 迁移工具 | `pressly/goose/v3` v3.27.2，SQL-only 顺序 migration，`//go:embed` 嵌入 |
| 迁移命令 | `./api migrate <up\|down\|status\|validate>`；API 启动不自动迁移 |
| 数据库驱动 | `github.com/jackc/pgx/v5` v5.10.0 |
| SQL migration | `00001_p0_schema.sql`：8 张 P0 表 + 索引 + 审计拒绝触发器 |
| Go 数据模型 | `internal/metadata/models.go`：8 实体 + 7 组枚举常量 |
| 仓储接口 | `internal/metadata/repo.go`：8 个接口，审计仅 Append/Query |
| PostgreSQL 实现 | `internal/metadata/postgres_repo.go`：PGStore 单一实现 |
| 凭证存储 | 连接仅存 `secret_ref` + `secret_version`；密文隔离在 `credential_envelopes` |
| 审计脱敏 | `sanitizeAuditMetadata()` 按允许列表过滤键值，`looksLikeSQL()`/`looksLikeCredential()` 检测敏感文本 |
| 依赖清单 | `docs/DEPENDENCY-LICENSES.md`：全部依赖 MIT/BSD/Apache 2.0 兼容 |
| CI 集成测试 | PostgreSQL 16 service + `go test -tags=integration` |

### 审查历史

| 轮次 | 审查 SHA | 发现 | 修复提交 |
|------|----------|------|----------|
| 1 | `032585c` | 12 条（P1×2, P2×10） | `b028a56` |
| 2 | `b028a56` | 10 条（P1×4, P2×6） | `fef5d32` |
| 3 | `fef5d32` | 6 条（P1×1, P2×5） | `c1799f5` |
| 4 | `dbfc64f` | 4 条（P2×4） | `dbfc64f` 内修复 |
| 5 | `0de8a0e` | 4 条（P1×1, P2×3） | `0de8a0e` |
| 6 | `fdcd73a` | 4 条（P2×4） | `1aa099f`, `dde2375` |
| 7 | `bd3a98e` | 3 条（P2×3） | `bd3a98e` |

### 验证结果

```text
go test ./...                              → PASS
go vet ./...                               → PASS
gofmt -l .                                 → (clean)
go build ./cmd/server                      → PASS
go test -tags=integration ./internal/metadata/... → PASS (39 tests)
git diff --check                           → OK
CI: 6 checks (含新增集成测试)                → ALL SUCCESS
```

### 已知限制

- AEAD/KDF 算法、凭证 payload 编码、KEK 轮换流程留给 P0-05
- `executions.error_message` 列已从 Schema 移除，仅保留 `error_code`
- 审计保留/归档策略、哈希链完整性校验留给后续任务
- `credential_envelopes` 的加解密实现未包含（仅 Schema 约束）
- `connection_policies` 的访问层默认拒绝逻辑由调用方实现，仓储仅返回 nil

## 交接记录

- 2026-07-19：已读设计稿第 6、7、8、10、11 节，ADR-004/006/010/011、P0-05 与当前 Go/Compose 骨架。通过 ADR-013 固定迁移工具、P0 表边界、追加式审计和凭证字段方案。状态改为 Ready。
- 2026-07-20：完整实施 P0-02，包括 SQL migration、Go 仓储层、39 个集成测试、依赖许可证清单。经过 7 轮 Codex 独立审查，PR #10 所有问题已修复。分支 `feat/P0-02-metadata-migrations-impl`，HEAD `bd3a98e`。
- 未决项（留给 P0-05）：AEAD/KDF、凭证 payload 编码、KEK 轮换与审计保留/归档策略。

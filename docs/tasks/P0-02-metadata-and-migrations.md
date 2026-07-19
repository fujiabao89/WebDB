# P0-02：元数据库与迁移

> 状态：Ready｜风险：High｜依赖：P0-01、ADR-006、ADR-010、ADR-011、ADR-013｜建议实现者：Claude Code｜独立审查：Codex

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
| 本地用户 email 非空；成员引用真实 user/workspace；所有非空创建者/actor 必须是对应工作区成员 | 非空、复合外键、孤儿成员与错误工作区测试 |
| 连接只保存 `secret_ref`/密文元数据，不保存明文密码 | Schema 与日志测试 |
| 凭证信封密文、两类 nonce、wrapped DEK、suite 与 KEK 版本结构完整；连接引用必须指向同工作区存在的准确版本 | 信封非空/非空值约束、三列复合外键及缺失/错误版本测试 |
| 每个连接至多一条策略，`max_rows` 为正数；重复/非法策略被拒绝，缺失策略默认拒绝全部操作 | 复合主键、`CHECK` 与访问层默认拒绝集成测试 |
| `audit_events` 无普通更新/删除路径，元数据脱敏 | 仓储/API 与测试审查 |
| 审计 action、resource type/id、trace ID 非空非空白，事件时间非空；metadata 只能是 JSON object | 非空/非空白与 `jsonb_typeof` 约束测试 |
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

集成测试必须覆盖空库 `up -> down -> up -> up`、空用户 email、非法枚举及缺失连接环境拒绝、所有必选租户外键分量空值拒绝、孤儿 workspace member 拒绝、重复 connection policy、非正 `max_rows` 及缺失策略时访问层默认拒绝、空/不完整/缺失/错误版本/跨工作区凭证信封引用拒绝、非成员或跨工作区 actor 拒绝、user/system actor 组合不一致拒绝、同工作区不同连接的 execution 关联拒绝、无过期时间的非空 `result_ref` 拒绝、审计缺失或空白 action/resource/time/trace ID 与非对象 metadata 拒绝、审计 update/delete/truncate 拒绝，以及 Schema/日志中不存在明文凭证字段。

## 升级条件

字段语义、保留期、加密封装或身份模型冲突时更新 ADR 后再实现。P0-05 复用本任务的连接/执行/审计模型。

## 交接记录

- 2026-07-19：已读设计稿第 6、7、8、10、11 节，ADR-004/006/010/011、P0-05 与当前 Go/Compose 骨架。
- 已通过 ADR-013 固定迁移工具、P0 表边界、追加式审计和凭证字段方案；任务依赖 P0-01 已完成，状态改为 Ready。
- PR #9 第 1 轮独立审查：修复审计缺少租户约束连接关联、结果引用未强制过期、信封算法标识语义不足及设计稿 ADR 索引遗漏。
- PR #9 第 2 轮独立审查：修复同工作区 connection/execution 审计关联不一致风险，并固定 P0 枚举允许值、默认值和非空语义。
- PR #9 第 3 轮独立审查：补齐 PostgreSQL 创建租户连接复合外键所需的 `connections(workspace_id, id)` 唯一键。
- PR #9 GitHub Codex 自动审查：修复连接策略唯一性、凭证信封复合外键、审计 trace/metadata 约束、租户成员 actor 外键及本地用户 email 非空共 6 个 P2。
- PR #9 GitHub Codex 复审：补齐 audit actor 判别器、凭证引用非空、workspace member 两端外键和策略 `max_rows` 字段共 4 个 P2。
- PR #9 GitHub Codex 再审：补齐必选租户外键分量非空、完整信封字段、缺失策略默认拒绝、显式连接环境及审计 action/resource/time 约束，共修复 1 个 P1、4 个 P2。
- 实现时先提交会失败的 migration/约束集成测试，再添加最小 SQL migration 与访问层；新增依赖须更新许可证清单。
- 未决项：AEAD/KDF、凭证 payload 编码、KEK 轮换与审计保留/归档策略留给 P0-05，不阻塞 P0-02。

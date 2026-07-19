# P0-02：元数据库与迁移

> 状态：Ready｜风险：High｜依赖：P0-01、ADR-006、ADR-010、ADR-011、ADR-013｜建议实现者：Claude Code｜独立审查：Codex

## 目标与范围

实现 WebDB 元数据库迁移及访问层，覆盖用户、工作区、成员、连接、连接策略、执行和追加式审计的 P0 最小字段。

不实现完整登录、协作状态、变更审批、对象存储导出或生产数据迁移。

## Ready 决策

- 迁移：采用 ADR-013 的 `pressly/goose/v3` SQL-only 顺序 migration；显式迁移命令执行，API 启动不自动迁移。
- 表结构：P0 限定为 `users`、`workspaces`、`workspace_members`、`credential_envelopes`、`connections`、`connection_policies`、`executions`、`audit_events`，并用含 `workspace_id` 的复合约束阻止跨租户引用。
- 追加式审计：仓储只暴露追加/查询，数据库拒绝 `UPDATE`、`DELETE`、`TRUNCATE`；审计 metadata 由允许列表生成脱敏对象。
- 凭证：连接只保存 `secret_ref` + `secret_version`；信封表保存密文、nonce、wrapped DEK、算法标识和 `kek_version`，不保存 KEK 或明文。具体密码学算法由 P0-05 决策。

## 验收标准

| 验收项 | 证据 |
| --- | --- |
| 迁移可在空库 up/down，重复执行安全 | 自动化迁移测试 |
| 工作区成员、连接名称等唯一约束与外键可阻止越权/重复数据 | 集成测试 |
| 连接只保存 `secret_ref`/密文元数据，不保存明文密码 | Schema 与日志测试 |
| `audit_events` 无普通更新/删除路径，元数据脱敏 | 仓储/API 与测试审查 |

## 验证命令

实现完成后至少执行以下命令；integration suite 使用 Compose 的合成元数据库，不接触真实凭证或生产数据。

```bash
go -C apps/api test ./...
go -C apps/api vet ./...
go -C apps/api test -tags=integration ./internal/metadata/...
```

集成测试必须覆盖空库 `up -> down -> up -> up`、约束与跨工作区拒绝、审计 update/delete/truncate 拒绝，以及 Schema/日志中不存在明文凭证字段。

## 升级条件

字段语义、保留期、加密封装或身份模型冲突时更新 ADR 后再实现。P0-05 复用本任务的连接/执行/审计模型。

## 交接记录

- 2026-07-19：已读设计稿第 6、7、8、10、11 节，ADR-004/006/010/011、P0-05 与当前 Go/Compose 骨架。
- 已通过 ADR-013 固定迁移工具、P0 表边界、追加式审计和凭证字段方案；任务依赖 P0-01 已完成，状态改为 Ready。
- 实现时先提交会失败的 migration/约束集成测试，再添加最小 SQL migration 与访问层；新增依赖须更新许可证清单。
- 未决项：AEAD/KDF、凭证 payload 编码、KEK 轮换与审计保留/归档策略留给 P0-05，不阻塞 P0-02。

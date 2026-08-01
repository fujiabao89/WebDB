# P0 任务卡

任务状态以 GitHub Issue/PR 为准；此目录保存可审查的任务契约与交接事实。实现前必须满足 `Ready`，合并前必须满足 `Done`。

| 优先级 | 任务 | 状态 | 依赖 |
| --- | --- | --- | --- |
| 1 | [P0-03 数据库 Adapter 契约](P0-03-database-adapter-contract.md) | Done | P0-01 的目录/工具链骨架 |
| 1 | [P0-04 SQL 安全执行策略](P0-04-sql-safety-policy.md) | Done | P0-03 的执行边界 |
| 2 | [P0-01 工程骨架与 Compose](P0-01-project-skeleton-and-compose.md) | Done | 无 |
| 2 | [P0-02 元数据库与迁移](P0-02-metadata-and-migrations.md) | Done | P0-01、ADR-013 |
| 2 | [P0-05 凭证与审计基线](P0-05-credentials-and-audit-baseline.md) | Owner Gate | P0-02、P0-04、ADR-013 |
| — | [P0-05A 凭证与审计方案（Owner Gate）](P0-05-proposal-credentials-and-audit.md) | 提议中 | P0-05 |
| — | [P0-05 威胁模型](P0-05-threat-model.md) | 提议中 | P0-05A |
| 3 | [P0-06 最小 Web 工作台](P0-06-minimal-web-workbench.md) | Backlog | P0-01、P0-03、P0-04；集成 P0-02/P0-05 |
| 后续 | [P0-03-followup 连接池可观测性与压力测试](P0-03-followup-pool-observability-and-load-test.md) | Ready | P0-03；P0 结束前 |
| 后续 | [P0-03-followup 查询结果类型规范化](P0-03-followup-result-type-normalization.md) | Ready | P0-03；P0-06 公开结果 API 前 |
| 后续 | [P0-01-followup 依赖许可证清单](P0-01-followup-license-inventory.md) | Ready | P0-01 |

# P0 任务卡

任务状态以 GitHub Issue/PR 为准；此目录保存可审查的任务契约与交接事实。实现前必须满足 `Ready`，合并前必须满足 `Done`。

| 优先级 | 任务 | 状态 | 依赖 |
| --- | --- | --- | --- |
| 1 | [P0-03 数据库 Adapter 契约](P0-03-database-adapter-contract.md) | Done | P0-01 的目录/工具链骨架 |
| 1 | [P0-04 SQL 安全执行策略](P0-04-sql-safety-policy.md) | Ready | P0-03 的执行边界 |
| 2 | [P0-01 工程骨架与 Compose](P0-01-project-skeleton-and-compose.md) | Done | 无 |
| 2 | [P0-02 元数据库与迁移](P0-02-metadata-and-migrations.md) | Done | P0-01、ADR-013 |
| 2 | [P0-05 凭证与审计基线](P0-05-credentials-and-audit-baseline.md) | Backlog | P0-02、P0-04 |
| 3 | [P0-06 最小 Web 工作台](P0-06-minimal-web-workbench.md) | Backlog | P0-01、P0-03、P0-04；集成 P0-02/P0-05 |
| 后续 | [P0-01-followup 依赖许可证清单](P0-01-followup-license-inventory.md) | Ready | P0-01 |

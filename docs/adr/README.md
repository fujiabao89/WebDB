# 架构决策记录（ADR）

每项重要架构、安全、数据或部署取舍使用 [模板](../templates/adr.md) 记录。已接受 ADR 只能由新的 ADR 替代，不能静默改写。

| ADR | 标题 | 状态 |
| --- | --- | --- |
| [001](ADR-001-browser-no-direct-db.md) | 浏览器不直连目标数据库 | 已接受 |
| [002](ADR-002-postgres-and-mysql-only.md) | v1 仅支持 PostgreSQL 和 MySQL | 已接受 |
| [003](ADR-003-crdt-documents-only.md) | CRDT 仅用于协作文档 | 已接受 |
| [004](ADR-004-modular-monolith.md) | 模块化单体优先 | 已接受 |
| [005](ADR-005-production-read-only-by-default.md) | 生产连接默认只读 | 已接受 |
| [006](ADR-006-kek-in-deployment-environment.md) | KEK 由部署环境注入 | 已接受 |
| [007](ADR-007-dialect-aware-sql-parsing.md) | 按方言解析 SQL，未知即拒绝 | 已接受 |
| [008](ADR-008-p0-connection-pool-limits.md) | P0 连接池与执行准入默认值 | 已接受 |
| [009](ADR-009-ssh-topology-deferred.md) | SSH/跳板机连接后置 | 后续评估 |
| [010](ADR-010-query-result-retention.md) | 查询结果保留 7 天 | 已接受 |
| [011](ADR-011-local-auth-then-oidc.md) | 本地账号优先，后续 OIDC/SSO | 已接受 |
| [012](ADR-012-apache-2-license.md) | Apache License 2.0 | 已接受 |
| [013](ADR-013-p0-metadata-migrations-schema.md) | P0 元数据库迁移与 Schema 基线 | 已接受 |
| [014](ADR-014-sort-key-uniqueness-proof.md) | SortKey 唯一性证明与 VerifiedSortPlan | 已接受 |
| [015](ADR-015-continuation-token-ownership.md) | Continuation Token 归属与安全模型 | 已接受 |
| [016](ADR-016-admission-reservation-before-execution.md) | Admission 预留与 Execution 创建时序 | 已接受 |
| [017](ADR-017-p0-credential-envelope-audit-failure.md) | P0 凭证信封加密、KEK 生命周期与审计失败策略 | 提议中 |

# P0-03：数据库 Adapter 契约

> 状态：Done｜风险：High｜依赖：P0-01、ADR-001、ADR-002、ADR-008｜实现者：Claude Code｜独立审查：Codex｜PR：[#12](https://github.com/fujiabao89/WebDB/pull/12)

## 目标与范围

定义并实现 PostgreSQL/MySQL 统一 Adapter 接口：连接测试、Schema 拉取、只读执行、取消、游标/keyset 分页与连接生命周期。先用受控演示库完成端到端测试。

不实现写操作、浏览器直连、通用协议代理、SSH 隧道或无上限结果缓存。

## 验收标准

| 验收项 | 证据 |
| --- | --- |
| PG 14+、MySQL 8.0+ 均完成连接测试和授权内 Schema 枚举 | 两引擎集成测试 |
| 两引擎均能执行受策略允许的只读查询并按服务端游标/keyset 分页 | 分页一致性测试 |
| 超时、取消、错误和客户端断开均会归还连接 | 泄漏/池耗尽回归测试 |
| 执行遵守 ADR-008 的上限、获取超时和 429 行为 | 并发/限额测试 |

## 实施约束与交接

不得在 Adapter 内以字符串判断 SQL 安全性；P0-04 是唯一策略裁决层。先提交接口与测试矩阵供审查，再接入 API。任何驱动能力差异需形成支持矩阵和明确错误码。

## 完成情况

- 完成时间：2026-07-23
- 合并提交：[`925f7024`](https://github.com/fujiabao89/WebDB/commit/925f7024c4c3ced8ee56c29f03c31e1ae27ff61c)
- 实现范围：9 个生产 Go 文件、1 个集成测试文件；合并时包含 29 个 `Test...` 测试函数
- ADR：ADR-008 已追加 PostgreSQL MaxIdle 差异说明
- 验证：PR #12 的 API、Web、Contracts、Repository safety 和 PR contract 检查全部通过；[CI 运行记录](https://github.com/fujiabao89/WebDB/actions/runs/29987480370)
- 后续任务：
  - [P0-03-followup：连接池可观测性与压力测试](P0-03-followup-pool-observability-and-load-test.md)
  - [P0-03-followup：查询结果类型规范化](P0-03-followup-result-type-normalization.md)
- 注：`AdmissionController` 已返回 `ErrRateLimited`，HTTP 429 映射由 P0-04 API 层负责（当前仅 `/health` 端点，未注册业务路由）

## 引擎差异支持矩阵

| 能力 | PostgreSQL (pgxpool v5) | MySQL (database/sql) |
|------|------------------------|----------------------|
| 连接池实现 | `pgxpool.Pool` | `sql.DB` |
| 空闲连接上限 | 无 `MaxIdleConns` API，由 `MaxConnIdleTime`(5min) + `HealthCheckPeriod`(30s) 控制回收 | `SetMaxIdleConns(n)` |
| 连接获取 | `pool.Acquire(ctx)` 显式获取 | `db.Conn(ctx)` 显式获取 |
| 占位符 | `$1, $2, ...` | `?` |
| NULL 排序 | 原生 `NULLS FIRST` / `NULLS LAST` | `CASE WHEN col IS NULL THEN ...` 模拟 |
| Keyset 类型 | 需 `::integer` 显式转换（PG CASE WHEN 要求） | 自动类型推断 |
| 元数据 | `information_schema`，`pgxpool.Query` | `information_schema`，`db.QueryContext` |
| TLS | `tls.Config{ServerName: host}` | `mc.TLSConfig = "true"` |
| 密码编码 | pgx config 结构体字段（非 URL 编码） | DSN URL 编码 |

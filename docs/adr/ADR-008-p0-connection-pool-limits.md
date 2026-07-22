# ADR-008：P0 连接池与执行准入默认值

> 状态：已接受｜日期：2026-07-11
> 修订：2026-07-23（PostgreSQL MaxIdle 差异说明）

## 决策

每目标连接：`max_open=10`、`max_idle=2`、最大生命周期 30 分钟（含抖动）、获取超时 5 秒；并发上限为每用户 2、每工作区 10、每连接 5，超限返回 429，不设无界队列。

## PostgreSQL MaxIdle 差异

- **MySQL**：`database/sql` 通过 `SetMaxIdleConns(n)` 直接实现空闲连接上限。
- **PostgreSQL**：pgxpool v5 不支持 `MaxIdleConns` 等价 API。`MinConns` 为最小保持连接数（非 idle 上限），pgxpool 通过 `MaxConnIdleTime`（5 分钟）和 `HealthCheckPeriod`（30 秒）管理空闲连接回收。

这是 pgxpool 库的已知差异，非适配器 Bug。若无 pgxpool 在此方面的 API 更新或 WebDB 引入自定义连接追踪，PostgreSQL 的空闲连接控制仅能达到时间驱动的回收粒度。届时如需精确数量控制，应记录新 ADR 评估替代方案（如连接池代理、自定义 wrapper）。

## 后果

这些是 POC 起点而非容量承诺。压测后按目标库业务预算校准，所有覆盖值和拒绝事件需可观测并审计。

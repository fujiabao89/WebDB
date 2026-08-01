# ADR-016：Admission 预留与 Execution 创建时序

> 状态：已接受｜日期：2026-07-26｜Owner：fujiabao89｜批准日期：2026-07-27

## 背景

P0-03 `AdmissionController.TryAcquire` 在 `Adapter.Query` 内部执行。P0-04 提案 §4.3 的阶段 B 在准入之前创建 pending Execution。

问题：若 Adapter 返回 rate_limited 或 connection_busy，Execution 已被创建。需明确区分两个错误来源并正确处理。

## 决策

**选择 C：准入保持在 Adapter 内部。Service 处理两种错误的终结逻辑。**

### 错误来源区分

当前 `PoolHandle.Query` 的实际代码执行顺序为：

```text
check() → TryAcquire() → buildSortSpecs() → buildWrappedSQL() → execQuery()
                                                                    ├─ pgPool.Acquire() / db.Conn()
                                                                    └─ Query() / QueryContext()
```

两个错误发生在不同阶段：

#### rate_limited

- **来源**：`AdmissionController.TryAcquire()` 返回 `ErrRateLimited`
- **发生位置**：在 `pgPool.Acquire`/`sqlDB.Conn` 和 SQL Query 之前
- **Execution 状态**：`failed`
- **error_code**：`rate_limited`
- **Audit outcome**：`denied`
- **Adapter.Query 调用次数**：1（已进入 Query 方法，在 TryAcquire 后返回）
- **DB connection acquire 调用次数**：0
- **DB SQL query/executor 调用次数**：0

#### connection_busy

- **来源**：`pgPool.Acquire(ctx)` 或 `sqlDB.Conn(ctx)` 超时，被 `mapAcquireError` 映射为 `ErrConnPoolExhausted`
- **发生位置**：admission permit 已获取之后，在 SQL Query 之前
- **Execution 状态**：`failed`
- **error_code**：`connection_busy`
- **Audit outcome**：`failed`
- 发生过连接获取尝试（`Acquire`/`Conn` 已调用）
- **DB SQL query/executor 调用次数**：0

### Service 层处理

1. Service 调用 Adapter 前可创建 pending Execution。
2. Adapter 返回 `rate_limited` 时，Service 必须立即：
   - 更新 Execution 为 `failed`，`error_code = rate_limited`；
   - 写入脱敏 AuditEvent（`outcome = denied`）。
3. Adapter 返回 `connection_busy` 时，Service 必须立即：
   - 更新 Execution 为 `failed`，`error_code = connection_busy`；
   - 写入脱敏 AuditEvent（`outcome = failed`）。
4. 任一路径不得遗留永久 pending。
5. Execution 状态更新或审计失败遵循 P0-04 已定义的 fail-closed 规则。

### Panic 路径

- Adapter 通过 `defer permit.Release` 释放 permit。
- Service recovery/finalizer 将 Execution 终结为 `failed`。
- 写入脱敏 AuditEvent。
- 不允许永久 `pending`/`running`。

### 超出范围

`AdmissionLease`（Reserve/Confirm/Release 两阶段）不属于当前 P0 决策。仅在压测证明需要跨阶段预留时，通过新 Task + 新 ADR 引入。

## 被拒绝的候选方案

### 选项 A：AdmissionLease 前置

**理由拒绝**：引入 Reserve/Confirm/Release 两阶段状态机增加 Adapter 复杂度。当前 `TryAcquire` + `defer Release` 已满足 P0 需求。压测未证明需要跨阶段预留。

### 选项 B：准入完全前置到服务层

**理由拒绝**：服务层需重复连接池繁忙检测逻辑。Adapter 中的 `AdmissionController` 变为死代码。P0 范围外。

## 后果

- **安全**：准入失败时 Execution 明确标记为 failed，无信息泄露。
- **兼容**：本 ADR 的准入决策本身不引入额外的 Adapter API 变更（ADR-014 的 `VerifiedSortPlan`/`VerifiedNextPagePlan` 与 ADR-015 的 `ContinuationRegistry` 所有权迁移已覆盖所需的 Adapter 接口变化）。
- **运营**：rate_limited 和 connection_busy 的 Execution/AuditEvent 可被分别监控和告警。
- **测试**：用户/工作区/连接限流、取消后 Release、panic 后 Release、Release 幂等。

## 验证

- `rate_limited`：Execution status=failed, error_code=rate_limited, Audit outcome=denied, DB acquire=0, SQL Query=0。
- `connection_busy`：Execution status=failed, error_code=connection_busy, Audit outcome=failed, DB acquire 已尝试, SQL Query=0。
- `context.Canceled` → `Permit.Release` → 无泄漏。
- Panic → `defer permit.Release` → Service finalizer 终结 Execution。
- `Release` 多次调用不 panic。
- 任一路径无永久 pending。

## 回滚/替代条件

若压测或生产事件证明需要跨阶段预留（例如：pending Execution 过多导致数据库膨胀），通过新 Task 重新评估选项 A。

## 迁移影响

- P0-04 Service 层新增 `rate_limited` 和 `connection_busy` 两个独立错误处理分支。
- 本 ADR 的准入决策本身不引入额外的 Adapter 代码变更（ADR-014/015 已覆盖所需的 Adapter 层修改）。

## 相关资料

- [ADR-008](./ADR-008-p0-connection-pool-limits.md)
- [P0-03 任务卡](../tasks/P0-03-database-adapter-contract.md)
- [P0-04 提案](../tasks/P0-04-proposal-contract-and-parser.md) §4.3/§4.3.1

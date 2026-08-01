# ADR-015：Continuation Token 归属与安全模型

> 状态：已接受｜日期：2026-07-26｜Owner：fujiabao89｜批准日期：2026-07-27

## 背景

P0-03 Adapter 实现了私有 `ContinuationRegistry`：`Query` 将 `PagePlan` 存入内存 Registry 并生成 opaque token，`NextPage` 通过 token 取回 plan。

当前问题：
- Adapter token 不绑定 policy version，服务层无法在 NextPage 时重新验证成员资格和连接策略。
- Adapter 私有实现使服务层无法介入 token 生命周期。
- P0-04 提案 §3.2 要求 token 绑定 principal/connection/policy version/generation/statement hash，过期 ≤5 分钟，原子 compare-and-consume 防重放。

选项 B（保持 Adapter 拥有）与上述安全要求矛盾。选项 C（双层 token）增加复杂度且服务重启后内层仍失效。

## 决策

**选择 A：Service 是唯一 token/Registry Owner。**

### 1. 所有权

Service 是唯一 token/Registry Owner。Adapter 不生成、不解析、不保存 continuation token。

### 2. P0 内存 Registry

P0 使用有状态服务端内存 Registry。服务重启后所有 token 失效，返回 `invalid_page_token`。P0 不引入持久化。

### 3. 客户端 token

客户端仅获得 32 字节以上 CSPRNG opaque handle。SQL、Args、SortKeys、结果和 last values **不进入**客户端 token。

Registry key 必须保存 opaque handle 的 SHA-256 digest，不保存原始 bearer handle。原始 handle 只返回客户端一次。token 不得进入日志、审计正文、错误消息、trace attribute 或指标 label。

### 4. ContinuationState 绑定

Registry 保存完整 `ContinuationState`，至少绑定：

- UserID、WorkspaceID
- ConnectionID、pool generation、schema generation
- policy version
- statement hash（防止 SQL 被替换）
- `VerifiedSortPlan`（见 ADR-014）
- SQL、Args（深拷贝；`[]any` 使用有界不可变表示或递归深拷贝）
- last sort values
- cumulative count、PageSize、MaxRows
- `expires_at`（TTL ≤ 5 分钟）

### 5. 容量与驱逐

固定容量默认值：

| 维度 | 上限 |
|------|------|
| global | 10000 |
| per user | 100 |
| per workspace | 500 |
| per connection | 200 |

只允许驱逐处于 `ready` 状态、未被 claim/in-flight 的 token。没有安全驱逐候选时返回 `pagination_capacity_exhausted`。Registry 不得无界增长。

### 6. 原子状态机与终态清理

```
ready → claim（原子 compare-and-consume）→ in-flight（不可恢复为 ready）
```

- 旧 token claim 后永不恢复为 ready。
- reauthorization、数据库执行、新 token 生成或注册失败，旧 token 均保持失效。
- 任意时刻只能有一个有效后继 token。
- 并发 claim 同一 token：仅第一个成功，其余返回 `invalid_page_token`。

**原子 token 旋转**：

有后续页时使用 `Rotate(oldDigest, newDigest, newState)` 原子操作。语义：
- 在同一把锁/同一原子临界区内验证：oldDigest 存在；状态为 in-flight；claim ownership/version 匹配；newDigest 不存在；newState 有效。
- 原子执行：删除旧 in-flight entry → 在同一容量槽位写入新 ready entry → 更新 LRU 和全部维度计数；不产生可观察的中间状态。
- 正常旋转不额外占用 global/user/workspace/connection 配额，也不得为了旋转驱逐其他 token。
- Rotate 成功后，旧 token 永久无效，新 token 成为唯一后继。
- Rotate 失败：删除旧 in-flight；不恢复旧 token；不创建新 token；返回稳定错误；所有容量计数必须一致。

没有后续页时使用原子 `Complete(oldDigest)` 删除旧 in-flight。

reauthorization、查询执行、取消、超时、panic 失败时使用 `Abort(oldDigest)` 删除旧 in-flight，旧 token 不恢复。

`Rotate`、`Complete`、`Abort` 必须幂等或使用 claim version 防止延迟 finalizer 删除错误的新状态。finalizer 只能清理自己持有的旧 claim，不能删除已旋转出的新 token。

**过期清理**：后台 goroutine 定期扫描 `expires_at`，覆盖 ready 和遗留 in-flight 状态。ready 与 in-flight 都计入全部容量配额。

### 7. NextPage 流程

1. 原子 claim token。
2. 重新验证成员资格（用户是否仍在工作区）。
3. 重新验证连接策略（`AllowRead`/`MaxRows`/`StatementTimeoutMs` 是否变更）。
4. 验证 `connection_id`/generation/`policy version`/`statement hash` 未变化。
5. 成员资格撤销、策略变更、generation 变化均使旧 token 失效。
6. 不得从 token 恢复旧的、更宽松的 `MaxRows` 或 timeout。
7. 成功后旋转为新 token；旧 token 立即失效。

### 8. VerifiedNextPagePlan

Adapter 的 `NextPage` 改为接收结构化的 `VerifiedNextPagePlan`（含 last sort values，见 ADR-014），不接受客户端 token。NextPage 不得依据恢复出的裸 SortKeys 重新设置 Unique。

`VerifiedNextPagePlan` 必须不可伪造：零值无效（同 ADR-014 零值约束）；字段不导出、无 raw constructor；只能由成功 claim 和重新授权后的 Service 内部流程生成。

生成入口必须验证以下全部条件：
- 原始 `VerifiedSortPlan` 仍有效（`plan.Valid()` 通过）；
- connection/pool/schema generation 匹配；
- principal/workspace/connection 匹配；
- policy version 未变化；
- last sort values 数量和类型与排序计划匹配；
- cumulative count、PageSize、MaxRows 边界有效。

任一不满足则拒绝生成，旧 token 保持失效。

### 9. 策略变化

- principal/workspace/connection/generation/statement hash 不匹配则拒绝。
- 成员资格撤销则拒绝。
- policy version 变化使旧 token 失效。

## 被拒绝的候选方案

### 选项 B：Adapter 唯一拥有 Continuation

**理由拒绝**：Adapter token 不绑定 policy version，服务层无法重新授权。与 P0-04 安全要求矛盾。

### 选项 C：混合双层 token

**理由拒绝**：两层复杂度，服务重启后内层 Adapter token 仍失效，重新授权不完整。收益不足以抵消复杂度。

## 后果

- **安全**：token 不可伪造、不可重放、绑定完整安全上下文。
- **兼容**：Adapter API 变更。`Query` 不再返回 token。新增 `NextPage(VerifiedNextPagePlan)` 接口。`ContinuationRegistry` 从 Adapter 移除。
- **运营**：服务重启丢失所有 token，需监控 `invalid_page_token` 率。
- **测试**：token claim/replay/expiry、policy version 变更、generation 变更、成员资格变更、并发 claim、容量耗尽、原子 Rotate。

## 验证

- Token 防篡改（修改 handle 后拒绝）。
- TTL 过期后拒绝。
- principal/workspace/connection/policy version 不匹配拒绝。
- generation 变化后旧 token 拒绝。
- 并发 NextPage 同一 token（第二个请求失败）。
- 服务重启后旧 token 返回 `invalid_page_token`。
- NextPage 失败后 token 不恢复，handle 不可重用。
- 配额恰好满时仍能正常 Rotate，token 总数不增加。
- 并发 Rotate 只有一个成功。
- Rotate 与超时清理并发不破坏计数。
- 延迟 finalizer 不会删除新 token。
- Rotate 失败后旧 token 不能复用。
- Complete/Abort 重复调用不 panic、不产生负计数。
- 容量耗尽时返回 `pagination_capacity_exhausted`。
- Registry key 必须为 SHA-256 digest，不保存原始 bearer handle。

## 回滚/替代条件

若 P0 内存 Registry 的服务重启丢失不可接受，通过新 ADR 引入持久化存储，不改变其他安全属性。

## 迁移影响

- 移除 `apps/api/internal/adapter/pagination.go` 中的 `ContinuationRegistry`。
- 新增服务层 `ContinuationRegistry`（`apps/api/internal/execution/` 或独立包）。
- `PoolHandle.Query` 返回值不再包含 token；改为返回内部 `NextPagePlan`。
- `PoolHandle.NextPage` 签名改为接收 `VerifiedNextPagePlan`。

## 相关资料

- [P0-03 任务卡](../tasks/P0-03-database-adapter-contract.md)
- [P0-04 提案](../tasks/P0-04-proposal-contract-and-parser.md) §3.2/§3.3
- [ADR-008](./ADR-008-p0-connection-pool-limits.md)
- [ADR-014](./ADR-014-sort-key-uniqueness-proof.md)

# WEB-25 设计：credential/connection mutation 与 AuditEvent 原子提交

> 状态：设计已记录（未实施）｜日期：2026-08-02｜所属：[WEB-25](https://linear.app/webdb/issue/WEB-25)（P1）｜父任务：[WEB-11](https://linear.app/webdb/issue/WEB-11)（In Progress）
>
> Owner 2026-08-02 决策（选择 C）：**停止生产代码实施**，先完整记录 D11 作用域、事务不变量、接口隔离设计、验收矩阵与风险；新会话从最新 main 按 TDD 实施，最终只创建一个完整 WEB-25 PR。

---

## 1. D11 作用域（Owner 2026-08-02 明确）

**D11 保留**，但作用域精确限定为：

> **同一元数据库内**的 credential/connection mutation 必须与对应 AuditEvent 在同一事务中原子提交；**目标数据库查询完成后的审计（E9-E13）属于外部副作用例外**，沿用 ADR-017 的 post-execution fail-closed 策略（不原子，但 fail-closed 返回 `audit_failed`、触发 E17）。

| 场景 | 是否原子 | 说明 |
|---|---|---|
| credential Create/Rotate/Retire + E3-E6 | ✅ 原子 | 元数据库 mutation + AuditEvent 同一事务 |
| connection Create/Update + E1/E2 | ✅ 原子 | 元数据库 mutation + AuditEvent 同一事务 |
| 目标库查询后审计（E9-E13） | ❌ 外部副作用例外 | post-execution fail-closed、`audit_failed`、E17 保持不变 |
| connection.test（E7/E8） | 例外 | 涉及目标库连接测试，外部副作用，沿用现有 post-commit 语义 |

**明确不做**：
- 不引入跨数据库分布式事务。
- 不改变已批准的稳定错误码（`audit_failed`、`invalid_payload` 等）与 E17 告警语义。
- 不新增 migration、新依赖或公开 API 变化（如需则再次升级 Owner）。

## 2. 事务不变量

1. **原子性**：`Create`/`Rotate`/`Retire` 与 `connections.Create`/`Update` 的元数据库 mutation + 对应 AuditEvent 在同一 `MetadataTx` 内 COMMIT。
2. **失败回滚**：审计 append 失败（或事务内任一 mutation 失败）→ 事务整体 ROLLBACK，无任何 mutation 残留。
3. **无绕过路径**：所有 credential/connection 的元数据库 mutation 必须经过带审计的入口；禁止存在"直接 mutation 不写审计"的公开路径（绕过防护）。
4. **并发与回滚**：并发轮换（LIFE-07）、事务中间失败回滚（LIFE-08）由集成测试覆盖。
5. **外部副作用例外不变**：目标库查询后的审计写入仍为独立后置写入，失败时保持 `audit_failed`、execution 终态、E17 告警。

## 3. 接口隔离设计（不复用宽接口）

**不向通用 `MetadataTx` 直接增加 8 个无差别方法**。采用窄接口组合，由 `pgMetadataTx` 统一实现：

```go
// metadata 包

// AuditTx 仅负责事务内审计追加。
type AuditTx interface {
    AppendAudit(ctx context.Context, e *AuditEvent) error
}

// CredentialMutationTx 仅负责凭证生命周期 mutation。
type CredentialMutationTx interface {
    LockEnvelopeForUpdate(ctx context.Context, wsID, secretRef uuid.UUID) (*CredentialEnvelope, error)
    LockEnvelopeVersion(ctx context.Context, wsID, secretRef uuid.UUID, version int) (*CredentialEnvelope, error)
    InsertEnvelope(ctx context.Context, env *CredentialEnvelope) error
    UpdateRetiredAt(ctx context.Context, wsID, secretRef uuid.UUID, version int) error
}

// ConnectionMutationTx 仅负责连接 mutation。
type ConnectionMutationTx interface {
    CreateConnection(ctx context.Context, conn *Connection) error
    UpdateConnection(ctx context.Context, wsID uuid.UUID, conn *Connection) error
    UpdateConnectionVersion(ctx context.Context, wsID, secretRef uuid.UUID, newVersion int) error
    CountConnectionsByVersion(ctx context.Context, wsID, secretRef uuid.UUID, version int) (int, error)
}

// MetadataTx 组合窄接口 + execution 方法 + 事务控制。
// 既有 execution 原子化（E9-E13）继续使用。
type MetadataTx interface {
    AuditTx
    CredentialMutationTx
    ConnectionMutationTx
    CreateExecution(ctx context.Context, e *Execution) error
    UpdateExecution(ctx context.Context, wsID uuid.UUID, e *Execution) error
    Commit() error
    Rollback() error
}

// TxStore 开启元数据库事务（已有）。
type TxStore interface {
    Begin(ctx context.Context) (MetadataTx, error)
}
```

**依赖边界**（各包只依赖自己需要的组合接口，不泄漏 `*sql.Tx`）：

| 消费方 | 依赖接口 | 用途 |
|---|---|---|
| `credentials.LifecycleManager` | `AuditTx + CredentialMutationTx + ConnectionMutationTx`（即 `MetadataTx` 或显式组合） | Create/Rotate/Retire 原子化 |
| `connections.Service` | `AuditTx + ConnectionMutationTx` | Create/Update 原子化 |
| `execution.Pipeline` | 既有 `MetadataTx`（CreateExecution/UpdateExecution/AppendAudit） | E9-E13 保持 |

**实现**：`pgMetadataTx` 实现上述全部方法（复用 `postgres_credential_repo.go` 与 `postgres_repo.go` 的现有 SQL，内部用 `t.tx`）。**任何接口参数不得出现 `*sql.Tx`**。

## 4. 验收矩阵

| 验收项 | 结果 | 证据（预期测试） |
|---|---|---|
| Create 原子：audit 失败 → envelope 不残留 | 待实施 | credentials 失败注入测试 |
| Rotate 原子：audit 失败 → 新版本不残留、旧版本不变 | 待实施 | credentials 失败注入测试 |
| Retire 原子：audit 失败 → `retired_at` 不变 | 待实施 | credentials 失败注入测试 |
| Connection Create 原子：audit 失败 → 连接不残留 | 待实施 | connections 失败注入测试 |
| Connection Update 原子：audit 失败 → 更新回滚 | 待实施 | connections 失败注入测试 |
| 无绕过：无未审计 mutation 入口 | 待实施 | 入口审计测试 / 代码审查 |
| 并发轮换（LIFE-07） | 已由 WEB-24 覆盖 | `TestLifecycleRotateConcurrentPostgres` |
| 事务中间失败回滚（LIFE-08） | 已由 WEB-24 覆盖 | `TestLifecycleRotateTxFailureRollbackPostgres` |
| E9-E13 外部副作用例外 | 保持 | 既有 execution audit 测试 |
| 本机 + CI 全绿 | 待实施 | gofmt/vet/test/race/metadata+credentials integration |

## 5. 风险记录

| # | 风险 | 缓解 |
|---|---|---|
| R1 | 跨 `metadata`/`credentials`/`connections` 三包接口重构，破坏现有测试面广 | 窄接口组合 + 分阶段 TDD；先 metadata 层再消费方 |
| R2 | 审计失败语义从"post-commit + audit_failed"变为"原子回滚 + audit_failed" | 这是 D11 要求的行为变更；需更新 ADR-017/proposal §6.2 描述并补失败注入测试 |
| R3 | 现有 fake store 测试（`CredentialTXStore`/`ConnectionTXStore` 接收 `*sql.Tx`）需适配窄接口 | 适配测试或保留非事务变体；以编译+全绿为准 |
| R4 | 误改 E9-E13 / E7-E8 外部副作用语义 | 严格限定原子化范围；不改 execution 与 connection.test 路径 |
| R5 | 出现"绕过审计直接 mutation"的路径 | 入口审计测试 + 代码审查禁止；若无法禁止则升级 Owner |

## 6. 实施顺序（新会话，从最新 main 开始，TDD）

1. **metadata 窄事务接口与 `pgMetadataTx`**：定义 `AuditTx`/`CredentialMutationTx`/`ConnectionMutationTx` 并实现（含失败注入辅助）。
2. **credentials create/rotate/retire 原子化**：`LifecycleManager` 改用 `MetadataTx` + 审计钩子；`AuditedLifecycleManager` 事务内写 E3-E6；失败注入测试。
3. **connections create/update 原子化**：`Service.Create/Update` 改用 `AuditTx + ConnectionMutationTx`；失败注入测试。
4. **失败注入、并发、回滚与绕过防护测试**：补全第 4 节验收矩阵。
5. **ADR-017 / proposal 精确澄清 D11 作用域**：更新 §6.2.1/6.2.3/§9.1 与 ADR-017 §5/§6 描述，与实现一致。

**PR 纪律**：按阶段提交 commit，但**最终只创建一个完整 WEB-25 PR**；credentials 与 connections 均满足 D11 前，不得将 PR 设为 Ready、不得合并、不得关闭 WEB-11。

## 7. 升级条件（再次升级 Owner）

- 需要 migration、新依赖、公开 API 变化；
- 无法禁止未审计 mutation 绕过；
- 需改变已批准的错误码/审计契约（超出 D11 作用域澄清）。

## 8. 来源

- Codex P1（3698495131）：D11 声明原子提交 vs 实际 post-commit 冲突。
- Owner 2026-08-02 决策 2 与本次选择 C 指令。
- 参考 `docs/tasks/P0-05-proposal-credentials-and-audit.md` D11（§12）、§6.2.1/6.2.3、§9.1；`docs/adr/ADR-017-p0-credential-envelope-audit-failure.md` §5/§6；`apps/api/internal/metadata/audit_tx.go`。

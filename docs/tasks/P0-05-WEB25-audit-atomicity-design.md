# WEB-25 设计：credential/connection mutation 与 AuditEvent 原子提交

> 状态：设计已记录（未实施）｜日期：2026-08-02｜所属：[WEB-25](https://linear.app/webdb/issue/WEB-25)（P1）｜父任务：[WEB-11](https://linear.app/webdb/issue/WEB-11)（In Progress）
>
> Owner 2026-08-02 决策（选择 C）：**停止生产代码实施**，先完整记录 D11 作用域、事务不变量、接口隔离设计、验收矩阵与风险；新会话从最新 main 按 TDD 实施，最终只创建一个完整 WEB-25 PR。
> 本版本已按 qodo/CodeRabbit 对设计文档的审查意见修订（接口隔离、无绕过路径、行锁语义、AuditTx 契约、验收矩阵、D11 文档同步）。

---

## 1. D11 作用域（Owner 2026-08-02 明确）

**D11 保留**，但作用域精确限定为：

> **同一元数据库内**的 credential/connection mutation 必须与对应 AuditEvent 在同一事务中原子提交；**执行前审计（E9 `sql.execute.denied`，Adapter 调用前）失败时 fail-closed**：不调用 Adapter、返回 `audit_failed`、触发 E17；**目标库执行后的结果审计（E10-E13）属于外部副作用例外**，沿用 ADR-017 的 post-execution fail-closed 策略（不原子，但 fail-closed 返回 `audit_failed`、触发 E17）。

| 场景 | 是否原子 | 说明 |
|---|---|---|
| credential Create/Rotate/Retire + E3-E6 | ✅ 原子 | 元数据库 mutation + AuditEvent 同一事务 |
| connection Create/Update + E1/E2 | ✅ 原子 | 元数据库 mutation + AuditEvent 同一事务 |
| 执行前审计（E9 `sql.execute.denied`） | ❌ 执行前 fail-closed | Adapter 调用 0 次；审计失败返回 `audit_failed`、E17 告警 |
| 目标库执行后结果审计（E10-E13） | ❌ 外部副作用例外 | post-execution fail-closed、`audit_failed`、E17 保持不变 |
| connection.test（E7/E8） | 例外 | 涉及目标库连接测试，外部副作用，沿用现有 post-commit 语义 |

**明确不做**：
- 不引入跨数据库分布式事务。
- 不改变已批准的稳定错误码（`audit_failed`、`invalid_payload` 等）与 E17 告警语义。
- 不新增 migration、新依赖或公开 API 变化（如需则再次升级 Owner）。

## 2. 事务不变量

1. **原子性**：`Create`/`Rotate`/`Retire` 与 `connections.Create`/`Update` 的元数据库 mutation + 对应 AuditEvent 在同一 `CredentialAtomicTx`/`ConnectionAtomicTx` 内 COMMIT。
2. **失败回滚**：审计 append 失败（或事务内任一 mutation 失败）→ 事务整体 ROLLBACK，无任何 mutation 残留。
3. **无绕过路径**：所有 credential/connection 元数据库 mutation 必须经过带审计协调器（`credentials.LifecycleManager` / `connections.Service`）。**协调器独占 `Begin`/`Commit`/`Rollback` 与原始 mutation 方法，强制成对执行 mutation + `AppendAudit`**；外部调用方只能通过强制审计的入口（`AuditedLifecycleManager` / `Service`），无法直接拿到裸事务做"mutation 后提交不写审计"。`pgMetadataTx` 的裸 mutation 方法仅用于既有非原子路径与测试，不作为生产 mutation 入口。**`Commit` 是审计完成的闸门**：`pgMetadataTx` 的 `Commit` 在 credential/connection 原子化路径校验当前事务内已追加匹配当前 mutation 的 AuditEvent，未追加则拒绝提交并回滚（见 §3 接口注释）。
4. **AuditTx 契约（仅追加 / 租户 / 脱敏 / 精确匹配 / operation context）**：`AppendAudit` 仅支持追加。
   - **operation context 绑定**：每个 mutation 关联一个**不可变 operation context**（唯一 mutation ID、规范化 ResourceID、workspace、resource、action、connection、actor/actorType、outcome、traceID），由协调器在 `BeginCredential`/`BeginConnection`/`AppendAudit` 及强制协调器接口间传递并校验。`AuditEvent` 的 `resource_id`、身份字段（actor/workspace/connection）、outcome 与 trace identity 必须与 context **完全匹配**；拒绝缺失、错误资源或错误身份并回滚事务。补覆盖违规输入及租户/connection 隔离的负向测试。
   - **域绑定与事件矩阵（P1）**：`BeginCredential` 强制 `resource=="credential"`，`BeginConnection` 强制 `resource=="connection"`（拒绝跨域）；`action`/`outcome` 必须落在 **原子事务允许矩阵**内（`eventAllowed`，包内私有，§3）：connection.create/update（E1/E2）、credential.create/rotate/retire（E3/E4/E6 成功事件）。**E5（`credential.rotate failed`）不在允许列表（LATEST5-CR-01）**——Rotate 失败分支（无已提交 mutation）先回滚释放行锁、再经 `writeRotateFailedAudit` 独立失败审计入口写入，不进入原子 mutation 事务。拒绝跨域 resource 与错误 action/outcome 组合（例如 resource="connection" + action="connection.test"）。
   - **connection 规则**：connection-scoped mutation（connection.create/update）必须提供并精确匹配 `ConnectionID`；credential-only mutation（credential.create/rotate/retire，E3-E6）允许 connection 为 null。`AppendAudit` 校验与匹配逻辑区分两类操作。
   - 强制包含 actor、workspace、connection、outcome、trace identity；沿现有 `AuditEvent`/`AppendAudit`/metadata 持久化路径校验，**拒绝 credential、KEK、明文密钥、raw arguments、敏感结果与 raw driver error 进入审计正文**。
   - **每个 mutation 必须有且仅有一个字段完全匹配的 AuditEvent**：精确匹配字段集合为 `AuditMatchFields`（workspace、resource、resource_id、action、connection、mutation ID、actor_id、actor_type、outcome、trace_id，见 §3）；`AppendAudit`、`Commit` 闸门与负向测试**复用该集合**。拒绝错误 action/resource、跨租户、缺失或多余事件，校验失败时回滚事务。
   - 补负向验收测试覆盖违规输入、跨租户、connection 缺失/为 null、匹配冲突；验证 `AuditMatchFields` 任一字段不匹配即拒绝（不存在审计绕过）。
5. **并发与回滚**：并发轮换（LIFE-07）、事务中间失败回滚（LIFE-08）由集成测试覆盖；`CountConnectionsByVersion` 必须对匹配行加 `FOR SHARE` 锁（保留既有 retire TOCTOU 防护），并发 `UpdateConnectionVersion` 必须被阻塞直至退役事务结束。**共享序列化点**：对 `credential_envelopes`/`connections` 资源定义显式隔离级别或数据库 advisory-lock 协议作为共享序列化点；**当无匹配连接行（零匹配）时仍须获取该序列化点**（例如锁定目标 envelope 行 + advisory lock），防止并发 `UpdateConnectionVersion` 在零匹配场景绕过退役检查。集成测试须覆盖**初始零匹配**与**单匹配**两种场景。
6. **外部副作用例外不变**：目标库执行后的结果审计（E10-E13）写入仍为独立后置写入，失败时保持 `audit_failed`、execution 终态、E17 告警；执行前审计（E9 `sql.execute.denied`）在 Adapter 调用前写入，审计失败时 fail-closed 阻止 Adapter 调用。**E5 时序边界（LATEST2-CR-01/LATEST3-CR-01）**：`credential.rotate failed` 发生在 Rotate 失败分支（无已提交 mutation），先回滚释放行锁、再经独立失败路径写入；D11 原子范围仅涵盖成功 mutation 与其审计，E5 属原子范围外（审计失败时 fail-closed 返回 `audit_failed`）。`eventAllowed`/`validateOpForCredential` 的原子事务允许列表不含 E5——`BeginCredential` 拒绝 `credential.rotate/failed` op，确保 E5 不会进入原子 mutation 事务（仅经 `writeRotateFailedAudit` 独立失败审计入口写入）。

## 3. 接口隔离设计（窄接口 + 协调器独占事务控制）

**不向通用 `MetadataTx`（execution 专用，现状不变）增加 mutation 方法**；execution 与 credential/connection 使用不同事务类型，由 `pgMetadataTx` 统一实现。

```go
// metadata 包 — 窄事务接口

// MetadataTx execution 专用窄事务（现状不变，供 execution.Pipeline；E9-E13 保持）。
// 与现有 metadata.MetadataTx 契约一致，不增加 mutation 方法。
type MetadataTx interface {
    CreateExecution(ctx context.Context, e *Execution) error
    UpdateExecution(ctx context.Context, wsID uuid.UUID, e *Execution) error
    AppendAudit(ctx context.Context, e *AuditEvent) error
    Commit() error
    Rollback() error
}

// OperationContext 不可变操作上下文：标识一次原子化 mutation（唯一 mutation ID + 规范化
// ResourceID + 身份/结果/trace 标识）。字段私有，仅通过 NewOperationContext 构造、通过
// 只读访问器读取；构造时**复制** ConnectionID（不保存调用方指针）。Begin 后修改原始
// 连接 ID 或调用方上下文不会改变 Commit/AppendAudit 的校验（真正不可变契约，§2.4）。
type OperationContext struct {
    mutationID   string
    workspaceID  uuid.UUID
    resource     string       // "credential" | "connection"
    resourceID   string       // 规范化资源 id：secret_ref 或 connection_id 字符串
    action       string       // create / rotate / retire / update
    connectionID *uuid.UUID   // 复制值；connection-scoped 非 nil，credential-only 为 nil
    actorID      uuid.UUID
    actorType    string
    outcome      string
    traceID      string
}

func NewOperationContext(mutationID string, wsID uuid.UUID, resource, resourceID, action string, connID *uuid.UUID, actorID uuid.UUID, actorType, outcome, traceID string) (*OperationContext, error) {
    c := &OperationContext{
        mutationID:  mutationID,
        workspaceID: wsID,
        resource:    resource,
        resourceID:  resourceID,
        action:      action,
        actorID:     actorID,
        actorType:   actorType,
        outcome:     outcome,
        traceID:     traceID,
    }
    if connID != nil {
        v := *connID // 复制值，不保存指针
        c.connectionID = &v
    }
    if err := c.Validate(); err != nil {
        return nil, err
    }
    return c, nil
}

// Validate 对 OperationContext 做统一 fail-closed 校验：拒绝零值上下文、空 mutationID、
// 非法 resource（非 credential/connection）、connection-scoped 缺 ConnectionID、
// 零 workspace/actor 或空 traceID。NewOperationContext 与 BeginCredential/BeginConnection
// 在执行 mutation 前复用同一校验；校验失败则不继续执行 mutation。
func (c *OperationContext) Validate() error {
    if c == nil {
        return errors.New("operation context: nil")
    }
    if c.mutationID == "" {
        return errors.New("operation context: empty mutation id")
    }
    if c.workspaceID == uuid.Nil {
        return errors.New("operation context: zero workspace")
    }
    if c.resource != "credential" && c.resource != "connection" {
        return fmt.Errorf("operation context: invalid resource %q", c.resource)
    }
    if c.resourceID == "" {
        return errors.New("operation context: empty resource id")
    }
    if c.action == "" {
        return errors.New("operation context: empty action")
    }
    if c.actorType == "" {
        return errors.New("operation context: empty actor type")
    }
    if c.outcome == "" {
        return errors.New("operation context: empty outcome")
    }
    // connection-scoped：connectionID 为 nil 或指向 uuid.Nil 均拒绝（fail-closed）。
    if c.resource == "connection" && (c.connectionID == nil || *c.connectionID == uuid.Nil) {
        return errors.New("operation context: connection-scoped but missing or zero connection id")
    }
    if c.actorID == uuid.Nil || c.traceID == "" {
        return errors.New("operation context: missing actor or trace")
    }
    return nil
}

// 负向测试覆盖：nil 上下文、空 mutationID、零 workspace、非法 resource、空 resourceID、
// 空 action/actorType/outcome、connection-scoped 的 connectionID nil 或 uuid.Nil、
// 缺 actor/trace——断言 mutation 不执行、事务回滚，且保留 fail-closed workspace/connection 拒绝行为。

func (c *OperationContext) MutationID() string        { return c.mutationID }
func (c *OperationContext) WorkspaceID() uuid.UUID    { return c.workspaceID }
func (c *OperationContext) Resource() string          { return c.resource }
func (c *OperationContext) ResourceID() string        { return c.resourceID }
func (c *OperationContext) Action() string            { return c.action }
// ConnectionID 返回防御性副本（value-and-presence），不暴露内部指针：
// 通过访问器无法修改内部 connectionID，Begin 后修改调用方持有的值不影响 Commit/AppendAudit 校验。
func (c *OperationContext) ConnectionID() (uuid.UUID, bool) {
    if c.connectionID == nil {
        return uuid.Nil, false
    }
    return *c.connectionID, true // 返回副本值
}
func (c *OperationContext) ActorID() uuid.UUID        { return c.actorID }
func (c *OperationContext) ActorType() string         { return c.actorType }
func (c *OperationContext) Outcome() string           { return c.outcome }
func (c *OperationContext) TraceID() string           { return c.traceID }

// auditMatchFields 包内**私有的不可变**精确匹配字段契约（数组，外部不可重赋值/修改）。
// AppendAudit、Commit 闸门与负向测试通过私有数组或比较器复用；跨包仅通过 AuditMatchFields() 防御性副本访问。
var auditMatchFields = [...]string{
    "workspace_id", "resource", "resource_id", "action",
    "connection_id", "mutation_id", "actor_id", "actor_type",
    "outcome", "trace_id",
}

// AuditMatchFields 返回精确匹配字段的**防御性副本**（外部修改不影响契约）。
func AuditMatchFields() []string {
    out := make([]string, len(auditMatchFields))
    copy(out, auditMatchFields[:])
    return out
}

// AuditTx 事务内审计追加（仅追加；租户/脱敏/operation context 契约见 §2.4）。
// op 为指针参数以支持 nil fail-closed 契约：先检查 nil，再 Validate，拒绝 nil context。
type AuditTx interface {
    AppendAudit(ctx context.Context, op *OperationContext, e *AuditEvent) error
}

// CredentialMutationTx 凭证生命周期 mutation（不暴露 Commit/Rollback）。
type CredentialMutationTx interface {
    LockEnvelopeForUpdate(ctx context.Context, wsID, secretRef uuid.UUID) (*CredentialEnvelope, error)
    LockEnvelopeVersion(ctx context.Context, wsID, secretRef uuid.UUID, version int) (*CredentialEnvelope, error)
    InsertEnvelope(ctx context.Context, env *CredentialEnvelope) error
    UpdateRetiredAt(ctx context.Context, wsID, secretRef uuid.UUID, version int) error
}

// ConnectionMutationTx 连接 mutation（不暴露 Commit/Rollback）。
// CountConnectionsByVersion 必须对匹配行加 FOR SHARE 锁（保留既有 retire TOCTOU 防护）。
type ConnectionMutationTx interface {
    CreateConnection(ctx context.Context, conn *Connection) error
    UpdateConnection(ctx context.Context, wsID uuid.UUID, conn *Connection) error
    UpdateConnectionVersion(ctx context.Context, wsID, secretRef uuid.UUID, newVersion int) error
    CountConnectionsByVersion(ctx context.Context, wsID, secretRef uuid.UUID, version int) (int, error) // 必须 FOR SHARE
}

// ConnectionRefReadTx Retire 引用检查所需的锁读能力（仅 CountConnectionsByVersion，FOR SHARE）。
// 凭证协调器（LifecycleManager）只需此读能力，不暴露 CreateConnection/UpdateConnection/UpdateConnectionVersion。
type ConnectionRefReadTx interface {
    CountConnectionsByVersion(ctx context.Context, wsID, secretRef uuid.UUID, version int) (int, error) // 必须 FOR SHARE
}

// CredentialAtomicTx credential 原子化组合。内部接口，仅由 credentials.LifecycleManager
// （审计协调器）使用；协调器独占 Begin/Commit/Rollback 与 mutation，强制成对执行 mutation+AppendAudit。
// 只嵌入 ConnectionRefReadTx（Retire 的锁读），不暴露连接写 mutation。
// Commit 语义（防绕过，operation context）：**pgCredentialAtomicTx.Commit**（而非 pgMetadataTx.Commit）
// 使用事务级 operation context 校验——当前事务内已通过 AppendAudit 追加**有且仅有一个字段完全匹配**
// 当前 mutation 的 AuditEvent（workspace、resource、action、connection、mutation 标识全部匹配）；
// 缺失、多余或错误 action/resource/跨租户事件 → 返回错误并回滚。使调用方无法"mutation 后不审计直接提交"。
// 审计闸门仅属于原子 wrapper 的 Commit，**不影响 MetadataTx.Commit / TxStore.Begin（execution.Pipeline 兼容）**。
type CredentialAtomicTx interface {
    AuditTx
    CredentialMutationTx
    ConnectionRefReadTx
    Commit() error
    Rollback() error
}

// ConnectionAtomicTx connection 原子化组合。内部接口，仅由 connections.Service 使用。
// Commit 语义同 CredentialAtomicTx：**pgConnectionAtomicTx.Commit** 实现审计闸门（operation context
// + 精确匹配校验），不影响 MetadataTx.Commit / TxStore.Begin。
type ConnectionAtomicTx interface {
    AuditTx
    ConnectionMutationTx
    Commit() error
    Rollback() error
}

// TxStore 保留现有 Begin(ctx) (MetadataTx, error) 契约不变（execution.Pipeline、PipelineConfig.Tx、
// fakeTxStore 保持兼容）。原子事务方法放入独立 AtomicTxStore，不加入 TxStore。
type TxStore interface {
    Begin(ctx context.Context) (MetadataTx, error) // 既有 execution 路径（保持不变）
}

// AtomicTxStore 供 credential/connection 审计协调器开启原子化事务。
// Begin* 执行 mutation 前先检查 op 为 nil（fail-closed），再调用域校验
// （validateOpForCredential / validateOpForConnection，见下）：强制 resource 匹配域，
// action/outcome 在 E1-E6 事件矩阵内；校验失败则不继续执行 mutation。
// 返回的事务绑定已验证的 op 指针（或其私有字段不可变副本）；后续 mutation/AppendAudit
// 必须携带同一 context，Commit 按其校验精确匹配（§2.4）。
type AtomicTxStore interface {
    BeginCredential(ctx context.Context, op *OperationContext) (CredentialAtomicTx, error) // credential 原子化
    BeginConnection(ctx context.Context, op *OperationContext) (ConnectionAtomicTx, error) // connection 原子化
}

// validateOpForCredential 供 BeginCredential 使用：先 nil/Validate，再强制 resource=="credential"，
// 且 action/outcome 在 E3-E6 允许组合内；拒绝跨域 resource 与非法 action/outcome（P1）。
func validateOpForCredential(op *OperationContext) error {
    if op == nil {
        return errors.New("operation context: nil")
    }
    if err := op.Validate(); err != nil {
        return err
    }
    if op.Resource() != "credential" {
        return fmt.Errorf("operation context: BeginCredential requires resource=credential, got %q", op.Resource())
    }
    if !eventAllowed(op.Resource(), op.Action(), op.Outcome()) {
        return fmt.Errorf("operation context: invalid credential event %q/%q", op.Action(), op.Outcome())
    }
    return nil
}

// validateOpForConnection 供 BeginConnection 使用：先 nil/Validate，再强制 resource=="connection"，
// 且 action/outcome 在 E1-E2 允许组合内；拒绝跨域 resource 与非法 action/outcome（P1）。
func validateOpForConnection(op *OperationContext) error {
    if op == nil {
        return errors.New("operation context: nil")
    }
    if err := op.Validate(); err != nil {
        return err
    }
    if op.Resource() != "connection" {
        return fmt.Errorf("operation context: BeginConnection requires resource=connection, got %q", op.Resource())
    }
    if !eventAllowed(op.Resource(), op.Action(), op.Outcome()) {
        return fmt.Errorf("operation context: invalid connection event %q/%q", op.Action(), op.Outcome())
    }
    return nil
}

// eventAllowed 校验 (resource, action, outcome) 是否在 E1-E6 事件矩阵内（§2.4）。
// 包内私有：AppendAudit/Commit/负向测试复用。
func eventAllowed(resource, action, outcome string) bool {
    switch resource + "/" + action + "/" + outcome {
    case "connection/connection.create/succeeded", // E1
         "connection/connection.update/succeeded", // E2
         "credential/credential.create/succeeded", // E3
         "credential/credential.rotate/succeeded", // E4
         "credential/credential.retire/succeeded": // E6
        return true
    }
    return false
}
```

**依赖边界与事务 owner**（各包只依赖自己需要的组合接口，不泄漏 `*sql.Tx`）：

| 消费方 | 依赖接口 | 事务 owner | 用途 |
|---|---|---|---|
| `credentials.LifecycleManager`（审计协调器） | `AtomicTxStore` + `CredentialAtomicTx` | 协调器独占 Begin/Commit/Rollback，成对 mutation+audit | Create/Rotate/Retire 原子化 |
| `connections.Service`（审计协调器） | `AtomicTxStore` + `ConnectionAtomicTx` | 协调器独占，成对 mutation+audit | Create/Update 原子化 |
| `execution.Pipeline` | `MetadataTx`（现状不变，`TxStore.Begin`） | 既有 execution 路径 | E9-E13 保持 |

**绕过防护落实**：`CredentialAtomicTx`/`ConnectionAtomicTx` 是 `metadata` 包内部/半内部接口，仅由 `credentials`/`connections` 的审计协调器获得并关闭（使用后不再向外传递）。协调器是唯一能在事务内执行 mutation 的入口；API 层（`AuditedLifecycleManager`/`Service`）强制在 `Commit` 前调用 `AppendAudit`。任何绕过（直接 mutation 不审计）通过入口审计测试与代码审查禁止；若实施中无法禁止则按 §7 升级 Owner。

**实现（distinct wrapper types）**：`pgCredentialAtomicTx` 与 `pgConnectionAtomicTx` 为**不同的 wrapper 类型**，各自只实现自己的 mutation/audit/read 接口；**`pgMetadataTx` 不跨域实现两个 atomic 接口，也不嵌入跨域 mutation 接口**。**任何接口参数不得出现 `*sql.Tx`**。
**类型断言测试**：
- **运行时负向断言**：通过 interface type assertion 验证跨域断言失败——`var _ interface{ ConnectionMutationTx } = (CredentialAtomicTx)(nil)` 必须编译失败；测试用 `_, ok := any(credAtomicTx).(ConnectionMutationTx)` 断言 `ok == false`，反之亦然。
- **编译期正向断言**：`var _ CredentialAtomicTx = (*pgCredentialAtomicTx)(nil)`、`var _ ConnectionAtomicTx = (*pgConnectionAtomicTx)(nil)`。
- **不跨域约束**：`pgMetadataTx` 不实现 `CredentialAtomicTx`/`ConnectionAtomicTx`（只实现 `MetadataTx`）；`pgCredentialAtomicTx`/`pgConnectionAtomicTx` 不得暴露跨域 mutation 接口。

## 4. 验收矩阵

| 验收项 | 结果 | 证据（预期测试） |
|---|---|---|
| Create 原子：audit 失败 → envelope 不残留 | **已实施** | `TestCreateAuditFailureRollsBack`（`lifecycle_atomic_test.go`，AppendAudit 失败注入） |
| Rotate 原子：audit 失败 → 新版本不残留、旧版本不变 | **已实施** | `TestRotateAuditFailureRollsBack`（`lifecycle_atomic_test.go`，AppendAudit 失败注入） |
| Retire 原子：audit 失败 → `retired_at` 不变 | **已实施** | `TestRetireAuditFailureRollsBack`（`lifecycle_atomic_test.go`，AppendAudit 失败注入） |
| Connection Create 原子：audit 失败 → 连接不残留 | **已实施** | `TestConnection_CreateAuditFailureRollsBack`（`service_test.go`） |
| Connection Update 原子：audit 失败 → 更新回滚 | **已实施** | `TestConnection_UpdateAuditFailureRollsBack`（`service_test.go`） |
| 事件构建/校验失败（`AppendAudit` 拒绝非法 metadata）→ 回滚 | **已实施** | `TestAtomicTxAppendAuditInvalidMetadataRollsBack`（`atomic_tx_integration_test.go`，AppendAudit 拒绝后事务回滚不提交） |
| mutation 中间失败（锁/约束/DB 错误）→ 回滚 | **已实施** | `TestCreateInsertFailureNoCommit`、`TestRetireInUseNoCommit` |
| BeginTx 失败（`BeginCredential`/`BeginConnection` 开启事务失败）→ 返回错误且无残留 | 待实施 | credentials/connections 失败注入测试 |
| 数据库 Commit 失败（`atx.Commit` 底层 DB 提交失败）→ 不报告成功 | 待实施 | 失败注入测试 |
| Rollback 失败（`atx.Rollback` 底层回滚失败）→ 返回错误 | 待实施 | 失败注入测试 |
| Commit 审计闸门（原子事务缺失匹配 AuditEvent）→ 拒绝提交并回滚 | **已实施** | `TestAtomicTxCommitWithoutAuditRollsBack`、`TestConnectionAtomicTxCommitGate`（集成） |
| 取消（ctx 取消）→ 事务清理 | 待实施 | credentials/connections 取消测试 |
| panic → 事务回滚、连接归还 | 待实施 | panic 恢复测试 |
| 并发 mutation（并发轮换/并发创建） | 部分覆盖 | `TestLifecycleRotateConcurrentPostgres`（WEB-24 LIFE-07） |
| 资源清理（事务结束、连接归还） | 待实施 | 集成测试 |
| 脱敏错误（错误不含敏感信息） | **已实施** | `TestLogStorageFailureRedactsSensitive`、`TestAuditEventBuildFailedLogsRedactedRootCause` |
| 无绕过：无未审计 mutation 入口 | **已实施** | `TestAtomicTxDomainIsolation`（跨域类型断言） |
| 跨域资源拒绝：`BeginCredential` 收 `resource=connection`、`BeginConnection` 收 `resource=credential` → 拒绝且不执行 mutation | **已实施** | 原子入口 `TestBeginCredentialRejectsAtEntry`/`TestBeginConnectionRejectsAtEntry`（直接调用 Begin*，拒绝且不开事务）；validate 层 `TestValidateOpForCredentialCrossDomainRejected` |
| 错误 action/outcome 拒绝：不在原子允许矩阵内的组合（如 connection/connection.test/succeeded、credential.rotate/failed）→ 拒绝且回滚 | **已实施** | 原子入口 `TestBeginCredentialRejectsAtEntry`/`TestBeginConnectionRejectsAtEntry`；validate 层 `TestValidateOpForCredentialInvalidEvent` |
| nil context 拒绝：`Begin*`/`AppendAudit` 传 nil `*OperationContext` → 拒绝且不执行 mutation | **已实施** | 原子入口 `TestBeginCredentialRejectsAtEntry`/`TestBeginConnectionRejectsAtEntry`/`TestAtomicAppendAuditRejectsNilContext`；validate 层 `TestValidateOpForCredentialNil` |
| 审计闸门仅限原子 wrapper：`pgMetadataTx.Commit`（execution 路径）无闸门，execution.Pipeline 保持兼容 | **已实施** | `TestAtomicTxDomainIsolation`（`pgMetadataTx` 不实现原子接口） |
| 原子事务缺失匹配 AuditEvent 时 `pgCredentialAtomicTx.Commit`/`pgConnectionAtomicTx.Commit` 拒绝并回滚 | **已实施** | `TestAtomicTxCommitWithoutAuditRollsBack`、`TestConnectionAtomicTxCommitGate`（集成） |
| 并发轮换（LIFE-07） | **部分覆盖** | `TestLifecycleRotateConcurrentPostgres`（WEB-24；验证并发轮换 + SecretVersion，但未直接调用 `AppendAudit`） |
| 事务中间失败回滚（LIFE-08） | **部分覆盖** | `TestLifecycleRotateTxFailureRollbackPostgres`（WEB-24；真实 UPDATE 与 INSERT 回滚，但未覆盖 `AppendAudit` 失败注入） |
| E9-E13 外部副作用例外 | 保持 | 既有 execution audit 测试 |
| 本机 + CI 全绿（metadata/credentials 集成测试；CI 未执行 connections/execution 集成测试） | 部分（本机单元测试与集成编译全绿；CI API job 仅运行 `go test -p=1 -tags=integration ./internal/metadata/... ./internal/credentials/...`（ci.yml `Metadata integration test`）与 `./internal/adapter/...`（`Adapter integration test`），**不覆盖** connections/execution 集成测试；connections/execution 由单元测试（本机 `go test ./...` 全绿）覆盖） | 从 `apps/api` 目录执行以下命令：<br>① `gofmt -l .` → 无输出<br>② `go vet ./...` → 无错误<br>③ `go test ./...` → 全部 `ok`<br>④ `go test -race ./...` → CI Race test 覆盖（本机 CGO 不可用）<br>⑤ `go test -p=1 -tags=integration ./internal/metadata/... ./internal/credentials/...` → 与 CI API job `Metadata integration test` 一致（本机编译通过，CI 执行） |

> WEB-24 轮换测试仅部分覆盖 D11（并发轮换/回滚语义，未直接调用 `AppendAudit`）。WEB-25 已补充审计原子性单元测试（`lifecycle_atomic_test.go` 的 Create/Rotate/Retire 失败注入、`service_test.go` 的 Create/Update 失败注入、`TestRotateE5WriteFailureFailsClosed` fail-closed）与集成测试（Commit 闸门、`TestAtomicTxAppendAuditWrongContextRejected`）。

## 5. 风险记录

| # | 风险 | 缓解 |
|---|---|---|
| R1 | 跨 `metadata`/`credentials`/`connections` 三包接口重构，破坏现有测试面广 | 窄接口组合 + 分阶段 TDD；先 metadata 层再消费方 |
| R2 | 审计失败语义从"post-commit + audit_failed"变为"原子回滚 + audit_failed" | 这是 D11 要求的行为变更；需更新 ADR-017/proposal §6.2 描述并补失败注入测试 |
| R3 | 现有 fake store 测试（`CredentialTXStore`/`ConnectionTXStore` 接收 `*sql.Tx`）需适配窄接口 | 适配测试或保留非事务变体；以编译+全绿为准 |
| R4 | 误改 E9-E13 / E7-E8 外部副作用语义 | 严格限定原子化范围；不改 execution 与 connection.test 路径 |
| R5 | 出现"绕过审计直接 mutation"的路径 | 协调器独占事务控制 + 入口审计测试 + 代码审查禁止；若无法禁止则升级 Owner |
| R6 | 既有 WEB-24 轮换测试未覆盖审计原子性 | WEB-25 补充 `AppendAudit` 失败注入与并发审计测试 |

## 6. 实施顺序（新会话，从最新 main 开始，TDD）

1. **metadata 窄事务接口与 `pgMetadataTx`**：保留现有 `MetadataTx` 与 `Begin(ctx)`；新增 `AuditTx`/`CredentialMutationTx`/`ConnectionMutationTx`/`ConnectionRefReadTx`/`CredentialAtomicTx`/`ConnectionAtomicTx` 与 `BeginCredential`/`BeginConnection` 并实现（含失败注入辅助）。先写接口编译失败的测试（RED）。
2. **credentials create/rotate/retire 原子化**：`LifecycleManager` 改用 `CredentialAtomicTx`；`AuditedLifecycleManager` 在事务内成对执行 mutation + E3-E6；失败注入测试（`AppendAudit` 失败 → 无残留）。
3. **connections create/update 原子化**：`Service.Create/Update` 改用 `ConnectionAtomicTx`；失败注入测试。
4. **失败注入、并发、回滚、取消/panic/清理与绕过防护测试**：补全 §4 验收矩阵；覆盖 `AppendAudit` 失败、事件校验失败、mutation 中间失败、`Begin`/`Commit`/`Rollback` 失败、取消、panic、并发、资源清理、脱敏错误。
5. **ADR-017 / proposal 精确澄清 D11 作用域（实施前同步）**：更新 proposal §6.2.1/6.2.3/6.3/§9.1 与 ADR-017 §5/§6，明确 credentials/connections mutation 与 AuditEvent 原子提交、审计失败时回滚的适用范围；**移除与原子提交冲突的"mutation 已持久化、审计失败不影响创建结果"描述**。若无法完成同步，记录 Owner 升级并将本设计标记为不可实施依据。

**PR 纪律**：按阶段提交 commit，但**最终只创建一个完整 WEB-25 PR**；credentials 与 connections 均满足 D11（含无绕过路径）前，不得将 PR 设为 Ready、不得合并、不得关闭 WEB-11。

## 7. 升级条件（再次升级 Owner）

- 需要 migration、新依赖、公开 API 变化；
- 无法禁止未审计 mutation 绕过（协调器独占事务控制无法落实）；
- 需改变已批准的错误码/审计契约（超出 D11 作用域澄清）；
- 无法同步 proposal/ADR-017 的 D11 权威文档。

## 8. 部分实施与失败的责任、回滚与 forward-fix

**责任角色**：
- 实现与验收：实施 Agent（按 §6 分阶段 TDD）；独立审查：Codex / CodeRabbit / qodo。
- 文档回退（docs-only PR #36）：由 WEB-25 实施 Agent 在后续实现 PR 中负责修正；若设计文档本身需回退，由提交人 `fujiabao89` 决定。
- 生产代码回滚：由 Owner 决定执行。
- **D11 权威文档同步（不推迟到实施阶段）**：proposal §6.2.x/§9.1 与 ADR-017 §5/6 的 `CredentialAtomicTx`/`ConnectionAtomicTx` 原子提交及审计失败整体回滚语义同步，需 Owner 批准（已接受 ADR 只能由新 ADR 替代）。**本 docs-only PR #36 未完成同步；同步完成前，本设计不作为 WEB-25 实施依据**。若 Owner 拒绝同步，升级 Owner 并将本设计标记为不可实施。

**停止与回滚触发阈值**：
- 任一 P0/P1（数据完整性、审计绕过、越权）在实现 PR 审查中确认 → 停止自动修复，升级 Owner。
- credentials 与 connections 两个原子化中任一个完成而另一个未完成 → **不得**设为 Ready / 合并 / 关闭 WEB-11（PR 纪律 §6）。
- 连续两次同类验证失败、或发现设计 §3 接口无法落实无绕过路径 → 停止并升级 Owner（§7）。

**回滚动作**：
- 功能回滚：`git revert` 实现 PR 合并后的 main commit；若涉及接口迁移，按 §3 保留 `MetadataTx`/`Begin` 兼容契约，回滚不破坏既有 execution 路径。
- 设计文档回退：`git revert` 本 docs-only PR #36 的 main commit（仅文档，无生产影响）。

**forward-fix 目标与负责人**：目标为使 credential/connection mutation 与 AuditEvent 原子提交（D11）落地且无绕过路径；负责人为 WEB-25 实施 Agent，Owner `fujiabao89` 批准。

**重新 Ready 的证据门禁**：
- 通过 §4 验收矩阵全部"待实施"项（含 connections 集成测试、`AppendAudit` 失败注入、取消/panic/清理/脱敏错误）；
- 从 `apps/api` 目录执行全部命令（本机与 CI 一致）全绿：`gofmt -l .`（无输出）、`go vet ./...`（exit 0）、`go test ./...`（全部 ok）、`go test -race ./...`（全部 ok）、`go test -p=1 -tags=integration ./internal/metadata/... ./internal/credentials/... ./internal/connections/... ./internal/execution/...`（全部 ok）；
- CI（gofmt/vet/test/race）全绿；
- 无绕过路径（入口审计测试 + 代码审查结论）；
- proposal/ADR-017 的 D11 权威文档已同步（§6 步骤 5）。

**docs-only PR #36 交接**：本 PR 仅记录设计，无生产代码。后续实现 PR 必须引用本设计文档；若实施中发现设计缺陷，先更新本设计再实现，不得静默偏离。

## 9. 来源

- Codex P1（3698495131）：D11 声明原子提交 vs 实际 post-commit 冲突。
- Owner 2026-08-02 决策 2 与选择 C 指令。
- qodo/CodeRabbit 对设计文档的审查意见（接口隔离、无绕过路径、行锁、AuditTx 契约、验收矩阵、D11 文档同步、Begin 兼容、CredentialAtomicTx 边界、部分实施责任）。
- 参考 `docs/tasks/P0-05-proposal-credentials-and-audit.md` D11（§12）、§6.2.1/6.2.3/6.3、§9.1；`docs/adr/ADR-017-p0-credential-envelope-audit-failure.md` §5/§6；`apps/api/internal/metadata/audit_tx.go`；`apps/api/internal/credentials/lifecycle_integration_test.go`。

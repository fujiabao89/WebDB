# P0-04：契约与解析器提案（修订版）

> 状态：依赖 Spike 已批准；正式依赖与 TDD 待再次审批｜日期：2026-07-23（修订：2026-07-23）｜作者：Claude Code
>
> 修订记录：回应 Owner 审查 2026-07-23，修订解析器策略、执行流拆分、审计语义、错误码和测试矩阵。

---

## 1. 现状证据

### 1.1 现有 Adapter 接口

**`apps/api/internal/adapter/types.go`**：

- `Engine`：`postgresql` / `mysql`
- `FirstPageRequest`：含 `Scope UserWorkspaceScope`、`SQL`、`Args`、`SortKeys`、`PageSize`、`MaxRows`
- `QueryResult`：含 `Columns`、`Rows`、`NextToken`、`ReturnedRows`、`TotalReturned`
- `PoolHandle.Query(ctx, FirstPageRequest) (*QueryResult, error)` — 首页查询
- `PoolHandle.NextPage(ctx, UserWorkspaceScope, token) (*QueryResult, error)` — keyset 续页
- `UserWorkspaceScope`：`UserID` + `WorkspaceID`（注释明确标注"非安全凭证"）

**关键发现**：
- P0-03 Adapter 不执行 SQL 安全裁决（代码注释："P0-03 不实现 SQL 安全裁决（P0-04 职责）"）
- `FirstPageRequest.SQL` 是原始字符串，无任何预检查
- Adapter `PageSize` 上限为 500（超过则钳制），但 **`MaxRows` 仅在请求值 ≤0 时默认 500，正值直接透传** — P0-04 策略层需基于 ConnectionPolicy 实施硬上限
- `NextPage` 验证 token scope/user/connection_id/pool_generation 匹配，但**不重新验证成员资格和连接策略** — P0-04 需补充重新授权

### 1.2 Adapter 稳定错误码

**`apps/api/internal/adapter/errors.go`**：

```
unsupported_engine, unsupported_capability, invalid_config,
connection_failed, connection_busy, rate_limited,
query_timeout, query_cancelled, invalid_page_token,
database_error, pool_closed, stale_config, config_conflict,
unsupported_query, result_too_large, pagination_capacity_exhausted
```

P0-04 需要新增 SQL 解析/策略层面的错误码。`unsupported_query` 目前用于 keyset 分页问题，语义与 P0-04 的"不支持语句类型"不同。

### 1.3 Metadata 模型

**`apps/api/internal/metadata/models.go`**：

- `Connection`：`ID`、`WorkspaceID`、`Engine`、`Environment`、`SecretRef`、`SecretVersion`、`CreatedBy`
- `ConnectionPolicy`：`WorkspaceID`、`ConnectionID`、`AllowRead`（`*bool`）、`AllowWrite`（`*bool`）、`AllowExport`（`*bool`）、`StatementTimeoutMs`（int）、`MaxRows`（int）
- `Execution`：`ID`、`WorkspaceID`、`ConnectionID`、`ActorID`、`StatementHash`、`Status`、`TraceID`、`StartedAt`、`FinishedAt`、`DurationMs`、`RowCount`、`ResultRef`、`ResultExpiresAt`、`ErrorCode`、`ErrorMessage`（`json:"-"`）
- `AuditEvent`：`ID`、`WorkspaceID`、`ActorType`、`ActorID`、`ConnectionID`、`Action`、`ResourceType`、`ResourceID`、`Outcome`、`Metadata`、`TraceID`、`ExecutionID`、`OccurredAt`
- `AuditOutcome`：`succeeded`、`failed`、`denied`、`cancelled`
- `ExecutionStatus`：`pending`、`running`、`completed`、`failed`、`cancelled`

**数据库约束关键事实**（ADR-013）：
- `executions.actor_id` 为 `NOT NULL`，且通过 `FOREIGN KEY (workspace_id, actor_id) REFERENCES workspace_members (workspace_id, user_id)` 约束 —— **非工作区成员无法写入 Execution**
- `audit_events` 使用 `CHECK ((actor_type = 'user' AND actor_id IS NOT NULL) OR (actor_type = 'system' AND actor_id IS NULL))`
- `ConnectionPolicy.AllowRead` 是 `*bool`，nil 时保留 DB 默认值（`allow_read=true`）

### 1.4 当前审计脱敏实现

**`apps/api/internal/metadata/postgres_repo.go:540-586`**：

`sanitizeAuditMetadata()` 当前允许列表仅包含：`summary`、`rows_affected`、`cached`。值类型仅允许 `string`/`float64`/`bool`。字符串截断 500 字符，并通过 `looksLikeSQL()`/`looksLikeCredential()` 检测敏感内容。

P0-04 需要扩展此允许列表以支持执行审计字段（statement_hash、error_code、row_count 等），同时保持脱敏约束。

### 1.5 当前 API 与身份

- **`apps/api/cmd/server/main.go`**：仅 `/health` 路由，无认证中间件，无 Adapter/Repository 注入
- **P0-05**（`docs/tasks/P0-05-credentials-and-audit-baseline.md`）：任务是"凭证与审计基线"（信封加密/引用、轮换、日志脱敏），**不是登录任务**
- P0 阶段无登录任务计划

### 1.6 当前不可用的能力

- P0-05 凭证解密未实现：`Connection.SecretRef`/`SecretVersion` 存在但无法解密为 `ConnectConfig.Password`
- 无法将 `metadata.Connection` 转换为 `adapter.ConnectConfig`
- 无用户会话/身份上下文

---

## 2. 目标与非目标

### 目标

1. PostgreSQL/MySQL 分方言 AST 解析（各自使用对应方言解析器）
2. 单语句限制（拒绝多语句）
3. 只读语句分类（fail-closed：无法判定即拒绝）
4. 工作区、连接、成员、环境、连接策略交集授权
5. statement timeout、MaxRows、取消和 Adapter 准入错误传播
6. 稳定服务/API 业务错误码
7. execution 生命周期和脱敏审计摘要
8. P0-03 Adapter 接入

### 非目标

- DML/DDL 审批或执行
- 登录和身份认证实现（不在 P0 范围内）
- 凭证解密/轮换（P0-05）
- 公开 HTTP SQL 执行路由
- 更新 `packages/contracts`
- SQL 自动改写
- 字符串前缀安全判断
- 浏览器直连
- UI
- 通用数据库协议代理
- 生产写入
- 自动审查闭环

---

## 3. 解析器候选与依赖 Spike 计划

### 3.1 决策依据（ADR-007）

> "PostgreSQL 与 MySQL 分别使用对应方言解析器；不限制为共同 SQL 子集；任何无法可靠解析或判定的语句默认拒绝。"

因此，**不使用多方言通用解析器**。PostgreSQL 和 MySQL 分别选择各自方言专用解析器。

### 3.2 PostgreSQL 候选（纯 Go）

| 维度 | pgparser (pgplex/Bytebase) | Bytebase Omni PG |
|---|---|---|
| **方言 AST 保真度** | ✅ 1:1 映射 PG 17.7 内部节点（goyacc 翻译 gram.y） | ✅ PG 生产就绪，210+ AST 节点，递归下降 |
| **回归测试** | 99.6% 通过 PG ~45,000 条回归测试 | 746+ 回归测试用例 |
| **许可证** | Apache 2.0 | MIT |
| **CGO** | 纯 Go，零外部依赖 | 纯 Go，零依赖 |
| **维护** | 活跃（2026-06，Bytebase 赞助） | 活跃 |
| **Stars** | 28（较新） | — |

**pg_query_go** 不纳入候选：CGO 依赖阻碍 Windows/Linux CI。

**pgparser 为候选方向**：纯 Go、PG 17.7 语法、Apache 2.0、99.6% 回归通过率。但项目较新（28 stars），需本仓库自行验证。

### 3.3 MySQL 候选（纯 Go）

| 维度 | TiDB Parser (`pingcap/tidb/pkg/parser`) |
|---|---|
| **方言 AST 保真度** | ✅ 高度兼容 MySQL 8.0，goyacc 生成 |
| **CTE/窗口函数** | ✅ |
| **INTO OUTFILE/DUMPFILE** | ✅ 可识别 |
| **可执行注释** | 待验证（Spike 验证 lexer 能力） |
| **许可证** | Apache 2.0 |
| **维护** | 活跃（TiDB 持续维护） |
| **依赖体积** | 较大（TiDB monorepo 子模块） |

`xwb1989/sqlparser` 和 `blastrain/vitess-sqlparser` 因停滞维护、不支持 CTE/窗口函数而排除。

**TiDB Parser 为候选方向**：Apache 2.0、纯 Go、生产验证。

### 3.4 依赖 Spike 计划（审批前必须完成）

**不直接批准依赖**。先在当前分支完成以下 Spike，收集证据后再由 Owner 批准：

| Spike 项目 | 验证内容 | 通过标准 |
|---|---|---|
| **模块体积** | `go mod graph` + `go mod why` 分析传递依赖 | 无 GPL/AGPL/SSPL 依赖；直接依赖 ≤5 个 |
| **Windows/Linux 构建** | `GOOS=windows go build ./...` 和 `GOOS=linux go build ./...` | 两平台均成功 |
| **PG 危险语句 AST** | 对 pgparser 执行 20+ 条危险 SQL，检查 AST 节点类型 | 能准确识别 DML/DDL/锁定/SELECT INTO/数据修改 CTE 的 AST 节点 |
| **MySQL 危险语句 AST** | 对 TiDB Parser 执行 20+ 条危险 SQL，检查 AST 节点类型 | 能准确识别 DML/DDL/INTO OUTFILE/锁定/FOR UPDATE/可执行注释的 AST 节点 |
| **MySQL 可执行注释** | `/*!50000 DROP TABLE t*/ SELECT 1` 等 5+ 条变体 | **必须由 MySQL 方言 parser/lexer 可靠识别**可执行注释。原始字符串 `/*!` 检测无法区分字符串字面量，不能作为 ADR-007 方言安全边界。TiDB Parser 无法提供此能力 → Spike 判定该候选**不通过**，评估其他 MySQL 解析器 |
| **单语句检测** | 含分号的 SQL、注释隐藏多语句 | 返回 statement 列表长度 ≥2 时拒绝 |
| **许可证枚举** | 在隔离临时 module 中枚举所有直接和传递依赖的 LICENSE 文件（`go mod download` + 扫描各模块缓存目录）；生成完整报告 | 无 GPL/AGPL/SSPL/未知许可证；候选许可证仅写入 Spike 报告，正式批准依赖后才更新 `docs/DEPENDENCY-LICENSES.md` |

Spike 结果写入 `docs/tasks/P0-04-spike-report.md`（该文件为 Spike 阶段产物，当前尚未创建；本提案冻结后进入 Spike 阶段时生成），通过后 Owner 批准新增依赖。

**Spike 隔离边界**：

- 使用仓库外临时目录中的独立 Go module/harness，并固定候选解析器版本
- 不修改或提交 `apps/api/go.mod`、`apps/api/go.sum`
- 不创建 `apps/api/internal/sqlpolicy/`、`apps/api/internal/execution/` 等正式工程代码
- 不更新 `docs/DEPENDENCY-LICENSES.md`；候选及传递许可证先记录在 Spike 报告中
- 任一候选无法满足 fail-closed、方言 AST、单语句检测或 MySQL 可执行注释识别要求时，结论必须为“不通过”，不得用原始字符串判断替代安全边界
- Spike 阶段不注册 HTTP 路由、不进入 TDD、不启动自动审查闭环

---

## 4. 策略输入/输出契约（修订）

### 4.1 核心类型

```go
// Dialect 方言标识 —— 从服务端 Connection.Engine 派生，不接受客户端输入
type Dialect string
const (
    DialectPostgreSQL Dialect = "postgresql"
    DialectMySQL      Dialect = "mysql"
)

// StatementKind 顶层语句类型 —— CTE/UNION 不是顶层类型，而是 AST 特征
type StatementKind string
const (
    StmtSelect      StatementKind = "SELECT"
    StmtExplain     StatementKind = "EXPLAIN"
    StmtInsert      StatementKind = "INSERT"
    StmtUpdate      StatementKind = "UPDATE"
    StmtDelete      StatementKind = "DELETE"
    StmtDDL         StatementKind = "DDL"
    StmtCall        StatementKind = "CALL"
    StmtTransaction StatementKind = "TRANSACTION"
    StmtOther       StatementKind = "OTHER"
    StmtUnknown     StatementKind = "UNKNOWN" // 解析器无法识别
)

// ASTFeatures AST 特征标记（不与 StatementKind 互斥，可叠加）
type ASTFeatures struct {
    HasCTE            bool // 含 WITH 子句
    HasRecursiveCTE   bool // 含 WITH RECURSIVE
    HasSetOperation   bool // 含 UNION/INTERSECT/EXCEPT
    HasLockingClause  bool // 含 FOR UPDATE/FOR SHARE/FOR KEY SHARE 等
    HasSelectInto     bool // PostgreSQL SELECT INTO（创建表）
    HasIntoOutfile    bool // MySQL INTO OUTFILE/DUMPFILE
    HasIntoVar        bool // MySQL SELECT ... INTO @var
    HasAssignment     bool // MySQL @x := ... 赋值
    HasExecComment     bool // MySQL /*!version*/ 可执行注释
    HasExplainAnalyze  bool // EXPLAIN ANALYZE
    HasModifyingCTE    bool // 含数据修改 CTE（INSERT/UPDATE/DELETE/MERGE 在 WITH 中）
    HasExplainDMLDDL   bool // EXPLAIN 的目标是 DML/DDL
    HasNestedExplain   bool // 嵌套 EXPLAIN（EXPLAIN EXPLAIN ...）
}

// ClassificationResult 语句分类结果
type ClassificationResult struct {
    StatementKind StatementKind
    Features      ASTFeatures
    StatementHash string // SHA-256(normalized_sql)
    StatementCount int   // 解析出的语句数量
    ParseError    error  // 解析错误（nil = 解析成功）
}

// PolicyDecision 策略决策
type PolicyDecision struct {
    Allowed       bool
    ReasonCode    StableReasonCode
    Classification ClassificationResult
}

// StableReasonCode 稳定拒绝原因码（业务码，非 HTTP 状态码）
type StableReasonCode string
const (
    ReasonAllowed            StableReasonCode = "allowed"
    ReasonParseError         StableReasonCode = "sql_parse_error"
    ReasonMultipleStatements StableReasonCode = "multiple_statements"
    ReasonNotAllowed         StableReasonCode = "statement_not_allowed"
    ReasonUnsupported        StableReasonCode = "unsupported_statement"
    ReasonEmptySQL           StableReasonCode = "empty_sql"
)

// 授权阶段错误码（前置于 SQL 策略）
const (
    ReasonInvalidScope      StableReasonCode = "invalid_scope"
    ReasonUnauthorized      StableReasonCode = "unauthorized"
    ReasonForbidden         StableReasonCode = "forbidden"
    ReasonConnectionNotFound StableReasonCode = "connection_not_found"
    ReasonPolicyNotConfigured StableReasonCode = "policy_not_configured"
    ReasonReadNotAllowed    StableReasonCode = "read_not_allowed"
)

// ---- 执行服务输入/输出 ----

// AuthenticatedPrincipal 由可信上游提供的已验证身份。
// P0-04 不实现登录；此结构仅由测试/受信任内部调用方注入。
// 未来登录实现后，由认证中间件填充。
type AuthenticatedPrincipal struct {
    UserID      uuid.UUID // 已认证用户 UUID
    WorkspaceID uuid.UUID // 目标工作区 UUID（从路由/上下文派生）
}

// ---- 执行服务请求类型（注意：与 adapter 包的同名类型语义不同） ----

// ExecuteFirstPageRequest 服务层首次执行请求。
// 与 adapter.FirstPageRequest 的区别：后者由策略层构造并传入 Adapter，
// 本类型携带 Principal、ConnectionID 和请求方上限，这些字段不进入 Adapter。
type ExecuteFirstPageRequest struct {
    Principal      AuthenticatedPrincipal
    ConnectionID   uuid.UUID
    SQL            string
    Args           []any             // 参数化查询参数（视为敏感数据：不记录、不审计、不写入错误响应）
    SortKeys       []adapter.SortKey
    PageSize       int               // 0 = 使用默认值
    RequestMaxRows int               // 0 = 使用策略上限
    RequestTimeout time.Duration     // 0 = 使用策略上限
}

// ExecuteNextPageRequest 服务层续页请求。从服务端 continuation 恢复 SQL/Args/排序/上限，
// 不接受调用方重新提交这些字段。
type ExecuteNextPageRequest struct {
    Principal  AuthenticatedPrincipal
    Token      string
}

// ---- 服务层 → Adapter 字段映射契约 ----
// ExecuteFirstPageRequest 经授权后构造 adapter.FirstPageRequest：
//   adapter.Scope.UserID      ← Principal.UserID
//   adapter.Scope.WorkspaceID ← Principal.WorkspaceID
//   adapter.SQL               ← 原始 SQL（经策略层 AST 分类已通过）
//   adapter.Args              ← 原始 Args（深拷贝，不记录）
//   adapter.SortKeys          ← SortKeys
//   adapter.PageSize          ← min(PageSize, effectiveMaxRows)；PageSize≤0 时使用默认值
//   adapter.MaxRows           ← effectiveMaxRows
// ExecuteNextPageRequest 的 Token 由服务端 continuation registry 解析，
// 恢复原始 ExecuteFirstPageRequest 的 SQL/Args/SortKeys 和策略上限后再重新授权；
// 不将 Token 直接传递给 Adapter。

// ---- Continuation Token 安全属性 ----
// Token 必须由服务端通过认证加密（AEAD）或签名生成，满足：
//   - 绑定 Principal（UserID + WorkspaceID）
//   - 绑定 ConnectionID（防止跨连接重放）
//   - 绑定连接池 generation（连接重建后旧 token 立即失效）
//   - 绑定 StatementHash（防止 SQL 被替换）
//   - 绑定生效的策略版本（AllowRead / MaxRows / StatementTimeoutMs 变更后 token 失效）
//   - 包含明确的过期时间（建议 ≤5 分钟）
//   - 禁止明文携带 SQL、Args、SortKeys 或结果数据
//   - 禁止客户端修改 token 后重放（防篡改）
//   - 每次 NextPage 由服务端通过原子 compare-and-consume 操作消费 token，
//     替换 token 后旧 token 立即失效，防止并发 NextPage 重用同一 token

// 以下由服务端从 Connection 元数据派生，不接受外部输入：
//   - TraceID      string   // 服务端生成
//   - Dialect      Dialect  // 从 Connection.Engine 派生
//   - Environment  string   // 从 Connection.Environment 派生

// AuthorizedExecution 经完整授权的执行上下文。
type AuthorizedExecution struct {
    ExecutionID        uuid.UUID // 新创建的 execution ID
    StatementHash      string
    EffectiveTimeoutMs int
    EffectiveMaxRows   int
    PolicyDecision     PolicyDecision
}
```

### 4.2 关键约束

1. **Dialect/Engine、Environment 从服务端派生**：从 `Connection.Engine` 和 `Connection.Environment` 获取，不接受请求体输入
2. **内部使用 `uuid.UUID`**：传输边界解析字符串 UUID，解析失败 → `invalid_scope`
3. **Workspace/Actor 从可信 Principal 派生**：不信任客户端提交的归属
4. **TraceID 由服务端生成或校验**：不接受客户端原始值
5. **缺失连接策略默认拒绝**：`ConnectionPolicy` 不存在或 `AllowRead != true` → 拒绝
6. **客户端不能提高策略上限 — MaxRows**：
   - 客户端 RequestMaxRows ≤ 0 → 使用 `policy.MaxRows`
   - 客户端 RequestMaxRows > 0 → `effectiveMaxRows = min(policy.MaxRows, RequestMaxRows)`
   - 0、负数或超大值均不得绕过策略上限
7. **客户端不能提高策略上限 — Timeout**：
   - 客户端 RequestTimeout ≤ 0 → 使用 `policy.StatementTimeoutMs`
   - 客户端 RequestTimeout > 0 → `effectiveTimeout = min(policy.StatementTimeoutMs, RequestTimeout)`
   - 零值/负数视为"使用策略上限"；超大值钳制到策略上限；溢出由 Go 类型安全保护
   - 如果调用方 context 已有更短 deadline → 保留更短 deadline（`context.WithTimeout` 语义）
8. **NextPage 重新授权**：每次 NextPage 都重新验证成员资格、连接和策略；NextPage 从服务端 continuation 恢复 SQL/Args/排序/上限，不接受调用方重新提交这些字段
9. **权限撤销即时生效**

### 4.3 执行流拆分（关键修订）

```
请求进入
  │
  ├─ 阶段 A：身份与授权（前置）
  │   ├─ 验证 AuthenticatedPrincipal（UserID/WorkspaceID 合法 UUID）
  │   ├─ 查询 User 存在且 status=active
  │   ├─ 查询 WorkspaceMember（user 是否为 workspace 成员）
  │   ├─ 查询 Connection（归属 workspace，存在）
  │   └─ 查询 ConnectionPolicy（AllowRead=true）
  │
  │   ❌ 阶段 A 任意失败：
  │       → 不创建 Execution（不存在合法 actor/workspace FK 对）
  │       → workspace 已确认存在 → 创建 AuditEvent（actor_type=system, outcome=denied）
  │       → workspace 无法解析或不存在 → 无法写入 AuditEvent（FK 约束），
  │         写入脱敏应用安全日志（含 trace_id、reason_code），
  │         返回原始业务错误码（不改为 audit_failed）
  │       → 返回业务错误码
  │
  ├─ 阶段 B：创建 Execution（pending）
  │   └─ INSERT execution（status=pending, workspace_id, connection_id, actor_id, statement_hash, trace_id）
  │   ❌ INSERT 失败 → 返回 internal_error，不访问目标数据库
  │
  ├─ 阶段 C：SQL AST 策略判断
  │   ├─ 方言解析
  │   ├─ 单语句检查
  │   └─ 只读分类
  │
  │   ❌ 阶段 C 拒绝：
  │       → 更新 execution status=failed, error_code=拒绝原因码
  │       → 创建 AuditEvent（actor_type=user, outcome=denied）
  │       → 不调用 Adapter
  │
  └─ 阶段 D：执行（Adapter）
      ├─ UPDATE execution SET status='running'
      │
      │   ❌ 更新失败 → 返回 internal_error/audit_failed
      │       不获取 Adapter pool，不连接目标数据库，不执行查询
      │       必须可验证：Adapter.Query 调用次数 = 0
      │
      ├─ 仅 running 持久化成功后才获取 Adapter pool
      ├─ Adapter.Query(ctx, ...)
      ├─ 成功 → 更新 execution status=completed, row_count, duration_ms
      │         创建 AuditEvent（outcome=succeeded）
      ├─ 超时 → 更新 execution status=failed, error_code=query_timeout
      │         创建 AuditEvent（outcome=failed）
      ├─ 取消 → 更新 execution status=cancelled, error_code=query_cancelled
      │         创建 AuditEvent（outcome=cancelled）
      └─ DB 错误 → 更新 execution status=failed, error_code=database_error
                   创建 AuditEvent（outcome=failed）
```
### 4.3.1 Adapter 错误码 → 业务错误码 + 审计 outcome 映射

`invalid_page_token` 在 NextPage 前置校验中处理，不进入阶段 D 的 Adapter.Query 流程。`rate_limited` 和 `connection_busy` 需区分两个路径。

| Adapter 错误码 | 触发层 | Execution 状态 | Audit Outcome | 备注 |
|---|---|---|---|---|
| `query_timeout` | Adapter | `failed` | `failed` | |
| `query_cancelled` | Adapter | `cancelled` | `cancelled` | |
| `database_error` | Adapter | `failed` | `failed` | 脱敏原始错误 |
| `rate_limited` | **准入层** | 不创建 execution | — | 阶段 D 准入拦截，execution 未被创建；返回 `rate_limited` + Retry-After |
| `rate_limited` | **Adapter** | `failed` | `failed` | 已在运行中收到限流（极端情况）；必须终结既有 running execution |
| `connection_busy` | **准入层** | 不创建 execution | — | 同上准入拦截 |
| `connection_busy` | **Adapter** | `failed` | `failed` | 同上 Adapter 路径 |
| `result_too_large` | Adapter | `failed` | `failed` | |
| `invalid_page_token` | NextPage 前置 | — | — | NextPage 前置校验拒绝，不进入 Adapter |
| 其他未匹配错误 | Adapter | `failed` | `failed` | fail-closed：未知 Adapter 错误均脱敏为 database_error |

---

## 5. 精确允许/拒绝矩阵

### 5.1 允许类别

| 类别 | PostgreSQL | MySQL | 条件 |
|---|---|---|---|
| 单条普通 SELECT | ✅ | ✅ | ASTFeatures 无锁定/INTO/可执行注释标记 |
| 只读 WITH/CTE | ✅ | ✅ | 所有 CTE 子句均为只读 SELECT；拒绝数据修改 CTE |
| SELECT 集合操作 | ✅ | ✅ | 所有分支均为只读 SELECT |
| 普通 EXPLAIN SELECT | ✅ | ✅ | EXPLAIN 目标为允许的 SELECT；拒绝 EXPLAIN DML/DDL |

### 5.2 必须拒绝的类别

| 类别 | 示例 | PostgreSQL | MySQL |
|---|---|---|---|
| 多语句 | `SELECT 1; DROP TABLE t` | ❌ | ❌ |
| 空语句 | `""` | ❌ | ❌ |
| 仅注释 | `-- comment\n/* block */` | ❌ | ❌ |
| INSERT/UPDATE/DELETE/MERGE | 所有 DML | ❌ | ❌ |
| 所有 DDL | CREATE/ALTER/DROP/TRUNCATE/RENAME | ❌ | ❌ |
| CALL/DO | 存储过程、匿名代码块 | ❌ | ❌ |
| COPY | `COPY ... FROM/TO` | ❌ | N/A |
| LOAD DATA | `LOAD DATA INFILE` | N/A | ❌ |
| SET/RESET | 会话变量设置 | ❌ | ❌ |
| PREPARE/EXECUTE/DEALLOCATE | 预编译语句 | ❌ | ❌ |
| 事务控制 | BEGIN/COMMIT/ROLLBACK/START TRANSACTION | ❌ | ❌ |
| GRANT/REVOKE | 权限管理 | ❌ | ❌ |
| VACUUM/ANALYZE 等管理语句 | 数据库维护 | ❌ | ❌ |
| HANDLER | MySQL 直接访问 | N/A | ❌ |
| LOCK TABLES | MySQL 表锁 | N/A | ❌ |
| DO | MySQL 匿名块 | N/A | ❌ |
| 数据修改 CTE | `WITH d AS (DELETE ...) SELECT * FROM d` | ❌ | ❌ |
| SELECT INTO | `SELECT * INTO new_table FROM t` | ❌ | N/A |
| FOR UPDATE / FOR SHARE | 锁定读取 | ❌ | ❌ |
| FOR KEY SHARE / FOR NO KEY UPDATE | PG 细粒度锁 | ❌ | N/A |
| INTO OUTFILE / DUMPFILE | 文件写入 | N/A | ❌ |
| SELECT ... INTO @var | MySQL 变量赋值 | N/A | ❌ |
| `@x := ...` 赋值 | MySQL 表达式赋值 | N/A | ❌ |
| 可执行版本注释 | `/*!50000 DROP TABLE t*/` | N/A | ❌ |
| EXPLAIN ANALYZE | 实际执行 | ❌ | ❌ |
| EXPLAIN DML/DDL | `EXPLAIN DELETE FROM t` | ❌ | ❌ |
| 嵌套 EXPLAIN | `EXPLAIN EXPLAIN SELECT ...` | ❌ | ❌ |
| 顶层 TABLE | PG `TABLE t`（≈ `SELECT * FROM t`） | ❌ | N/A |
| 顶层 VALUES | PG `VALUES (1,2)`（≈ `SELECT 1,2`） | ❌ | N/A |
| 解析错误 | 任何语法错误 | ❌ | ❌ |
| 未识别 AST 节点 | 解析器返回 unknown | ❌ | ❌ |

> ADR-007 当前只批准可可靠判定的 SELECT/EXPLAIN。P0-04 默认拒绝 TABLE 和 VALUES。Spike 只记录其 AST 形态，不自动改变策略。将来确有需求，由 Owner 明确批准扩展允许集。

### 5.3 SELECT 中函数调用的残余风险（修订）

- AST 无法判断 `SELECT user_function()` 是否有副作用
- **PostgreSQL SECURITY DEFINER 函数**可能以函数所有者权限执行写入操作，即使调用者仅有 SELECT 权限
- **安全不依赖于"SELECT 账号一定不能写"这个前提**

**缓解措施**（P0-04 实施时必须记录）：

1. 测试/演示数据库账号不得拥有危险函数（如 `SECURITY DEFINER` + 写入逻辑的函数）的 `EXECUTE` 权限
2. 执行层评估增加数据库原生只读事务保护（PostgreSQL `default_transaction_read_only=on`；MySQL `SET SESSION TRANSACTION READ ONLY`）
3. 如暂不实施数据库层只读保护，必须记录为 **Owner 接受的残余风险**，并创建后续任务跟踪
4. 文档和错误响应中不声称"保证无副作用"

---

## 6. 稳定错误码（修订）

### 6.1 业务错误码与 HTTP 映射分离开

HTTP 映射等公开路由任务确定后再最终批准。P0-04 先冻结业务错误码。

| 业务错误码 | 含义 | 阶段 | 备注 |
|---|---|---|---|
| `invalid_scope` | 作用域无效 | A | UUID 非法、workspace/user 格式错误 |
| `unauthorized` | 未认证 | A | 无有效 Principal |
| `forbidden` | 非工作区成员 | A | |
| `connection_not_found` | 连接不存在 | A | 统一不存在/跨工作区/不可见，防枚举 |
| `policy_not_configured` | 策略未配置 | A | ConnectionPolicy 行不存在 |
| `read_not_allowed` | 禁止读取 | A | ConnectionPolicy 存在但 AllowRead != true |
| `invalid_page_token` | 分页 token 无效 | D | |
| `sql_parse_error` | SQL 解析失败 | C | 语法错误 |
| `multiple_statements` | 检测到多条语句 | C | |
| `statement_not_allowed` | 语句类型不允许 | C | DML/DDL/管理语句等 |
| `unsupported_statement` | 不支持该语句类型 | C | 解析器无法识别 AST 节点 |
| `query_timeout` | 查询超时 | D | statement_timeout_ms 到期 |
| `query_cancelled` | 查询已取消 | D | 客户端取消或断开 |
| `rate_limited` | 速率限制 | D | 令牌桶耗尽 |
| `connection_busy` | 连接忙 | D | 并发执行上限达到 |
| `result_too_large` | 结果过大 | D | |
| `database_error` | 数据库错误 | D | 脱敏，不暴露原始错误 |
| `audit_failed` | 审计写入失败 | 任意 | fail-closed |
| `internal_error` | 内部错误 | 任意 | 未预期错误 |

### 6.2 HTTP 映射草案（等公开路由时冻结）

| 业务错误码 | 建议 HTTP 状态 | 说明 |
|---|---|---|
| `invalid_scope` | 400 | |
| `unauthorized` | 401 | |
| `forbidden` | 403 | |
| `connection_not_found` | 404 | 统一不存在/跨工作区/不可见 |
| `policy_not_configured` | 404 | 与 connection_not_found 统一（防策略枚举） |
| `read_not_allowed` | 403 | |
| `statement_not_allowed` | 422 | 语义请求被策略拒绝 |
| `sql_parse_error` | 422 | |
| `multiple_statements` | 422 | |
| `unsupported_statement` | 422 | |
| `invalid_page_token` | 400 | |
| `query_timeout` | 504 | 上游超时（非客户端请求超时） |
| `query_cancelled` | 499 | 非标准但广泛使用（nginx 惯例） |
| `rate_limited` | 429 | + Retry-After |
| `connection_busy` | 429 | + Retry-After |
| `result_too_large` | 422 | 语义与请求体过大（413）不同 |
| `database_error` | 500 | |
| `audit_failed` | 500 | |
| `internal_error` | 500 | |

### 6.3 安全约束

- `connection_not_found`、`policy_not_configured` 统一用于不存在、跨工作区和不可见资源，避免 ID 枚举
- 所有错误响应 message 为固定安全摘要，不包含 SQL 正文、Args、主机名、用户名、数据库原始错误、密码

---

## 7. HTTP/API 边界（修订）

### 7.1 决策

- **P0-04 不注册公开 HTTP 路由**
- **P0-04 不更新 `packages/contracts`**
- `ExecuteRequest` 为 Go 内部服务契约，不是公开传输契约
- `workspaceId`、`actorId`、`traceId` 不能成为客户端可信输入
- `args?: unknown[]` 缺少类型和大小约束，留待公开 API 任务时设计
- 公开传输契约留给真正注册执行 API 的任务

### 7.2 内部服务边界

- `ExecutionService` 接口独立于 HTTP 层
- 通过测试验证完整策略 → Adapter 链路
- `AuthenticatedPrincipal` 由测试框架或未来认证中间件注入
- 可实现 handler contract 测试但不注册路由

---

## 8. Execution 与审计语义（修订）

### 8.1 Statement Hash

- 算法：`SHA-256(normalized_sql)`
- 标准化：去除首尾空白、统一换行为 `\n`
- 仅保存 hash（execution 表 statement_hash 字段），不保存原始 SQL 或 args
- 不进入日志或错误响应

### 8.2 Execution 状态转换

```
阶段 B：INSERT → pending
阶段 C 拒绝：pending → failed（error_code=拒绝原因码）
阶段 D 开始：pending → running
阶段 D 成功：running → completed
阶段 D 失败：running → failed（error_code=query_timeout/database_error）
阶段 D 取消：pending/running → cancelled
```

### 8.3 前置授权拒绝（阶段 A）不创建 Execution

理由：`executions.actor_id` 必须是 workspace member 的外键引用。跨工作区、非成员或伪造 actor 的请求无法合法插入。

| 失败阶段 | 创建 Execution？ | Actor Type | Actor ID | Outcome |
|---|---|---|---|---|
| 阶段 A（前置授权） | **否** | `system` | NULL | `denied` |
| 阶段 B（创建 pending）失败 | **否**（插入失败） | `user` | principal.UserID | 取决于能否写入 audit |
| 阶段 C（AST 拒绝） | **是**（已有 pending） | `user` | principal.UserID | `denied` |
| 阶段 D（执行后） | **是**（更新状态） | `user` | principal.UserID | `succeeded`/`failed`/`cancelled` |

### 8.4 审计字段：表列 vs metadata

| 字段 | 存储位置 | 类型 |
|---|---|---|
| `workspace_id` | 表列 | UUID |
| `actor_type` | 表列 | `user` / `system` |
| `actor_id` | 表列 | UUID（可空，仅 system 时为空） |
| `connection_id` | 表列 | UUID（可空） |
| `action` | 表列 | `execute` |
| `resource_type` | 表列 | `connection` |
| `resource_id` | 表列 | connection UUID |
| `outcome` | 表列 | `succeeded`/`failed`/`denied`/`cancelled` |
| `trace_id` | 表列 | 服务端生成的 trace ID |
| `execution_id` | 表列 | UUID（可空，阶段 A 拒绝时为空） |
| `statement_hash` | **metadata JSONB** | string（SHA-256 hex，64 chars） |
| `row_count` | **metadata JSONB** | number（仅 succeeded 时） |
| `duration_ms` | **metadata JSONB** | number |
| `error_code` | **metadata JSONB** | string（failed/denied/cancelled 时） |
| `engine` | **metadata JSONB** | string（`postgresql`/`mysql`） |
| `environment` | **metadata JSONB** | string（`development`/`staging`/`production`） |
| `reason_code` | **metadata JSONB** | string（仅 denied 时） |

### 8.5 扩展 `sanitizeAuditMetadata()` 允许列表

当前允许列表（`postgres_repo.go:554-558`）：

```go
allowed := map[string]bool{
    "summary":       true,
    "rows_affected": true,
    "cached":        true,
}
```

扩展为：

```go
allowed := map[string]bool{
    // 原有
    "summary":       true,
    "rows_affected": true,
    "cached":        true,
    // P0-04 新增执行审计字段
    "statement_hash": true, // SHA-256 hex, 64 chars
    "row_count":      true, // number
    "duration_ms":    true, // number
    "error_code":     true, // 稳定业务错误码（不含原始 DB 错误）
    "engine":         true, // "postgresql" | "mysql"
    "environment":    true, // "development" | "staging" | "production"
    "reason_code":    true, // 拒绝原因码
}
```

**约束**：
- `statement_hash` 必须是 64 字符 hex 格式（单元测试验证）
- `error_code` 必须是稳定业务错误码，不得包含 `pq:` 或 `MySQL Error` 前缀
- `engine`/`environment` 必须是已知枚举值
- `statement_hash`/`error_code`/`reason_code`/`engine`/`environment`/`row_count`/`duration_ms` 为已知稳定枚举或固定格式值，**必须豁免 `looksLikeCredential()` 的通用检测**（当前实现会因含 `token` 子串而误杀 `invalid_page_token`）；其他通用字符串字段仍受 `looksLikeSQL()`/`looksLikeCredential()` 和 500 字符截断保护
- 实施时需同步更新 `sanitizeAuditMetadata()`：为 error_code/reason_code 添加白名单前缀（如 `invalid_`、`sql_`、`query_`、`connection_`、`rate_`、`result_`、`database_`、`audit_`、`internal_`、`statement_`、`multiple_`、`unsupported_`、`read_`、`policy_`、`forbidden`、`unauthorized`、`stale_`、`config_`、`pagination_`），或改用精确枚举匹配
- SQL 正文、Args、结果、原始数据库错误、密码、secret_ref 仍被拒绝

### 8.6 审计写入失败：分阶段 fail-closed（修订）

| 阶段 | 审计写入失败时的行为 |
|---|---|
| **阶段 A（已确认 workspace 存在）与阶段 B（执行前）** | 审计写入失败时不允许访问目标数据库，返回 `audit_failed`；数据库连接未建立。workspace 无法解析或不存在时不尝试写 AuditEvent，仅写脱敏应用安全日志并返回原始业务错误码。 |
| **阶段 C（AST 拒绝）** | 执行未发生。返回 `audit_failed`。若状态更新已成功，Execution 保持 `failed`；不得回退或误报为 `pending`。 |
| **阶段 D 完成后** | 查询已经真实发生，无法回滚这次读取。不返回查询结果给客户端。返回 `audit_failed`。**必须明确告知客户端这不是"未执行"**，且重试可能导致重复执行。 |

**事务边界**：
- Execution 更新与 Audit append **不在同一数据库事务中**（两者都是独立元数据操作）
- 故障注入测试必须覆盖：Execution 创建失败、running 更新失败、Audit append 失败、完成状态更新失败
- 失败顺序和恢复方式在实施时写入详细契约

**重试与结果恢复策略**：
- **禁止自动重试**：审计写入失败不触发自动重试；客户端是否重试由调用方决定
- **阶段 D 完成后审计失败**：Execution 已记录为 `completed`（含 row_count/duration_ms/statement_hash），但结果不返回客户端；客户端可通过 ExecutionID 查询执行状态和审计结果（需 P0-05 认证后提供查询接口）
- **结果引用恢复**：`result_ref` 在 execution 更新为 `completed` 时已持久化；审计 append 独立失败后，是否可通过 ExecutionID 恢复查询结果取决于结果存储的保护策略（脱敏、访问控制、保留期限、加密），这些保护策略属于 P0-03 Adapter 和 P0-05 凭证/存储范围，不在本提案中定义。P0-04 不承诺审计失败后结果一定可恢复
- **副作用 SELECT 风险**：带副作用的函数（如 SECURITY DEFINER）重复执行是已知残余风险；客户端重试由调用方在理解此风险的前提下决定，执行层不承诺幂等

### 8.7 跨工作区/前置拒绝的审计

阶段 A 拒绝时（无法确定合法的 actor/workspace FK 对）：
- workspace 已确认存在时，可以写入 `actor_type=system`、`actor_id=NULL` 的 AuditEvent
- `workspace_id` 只能使用已确认存在且满足外键约束的 workspace UUID
- `connection_id` 仅在已确认连接属于该 workspace 时写入，否则为 NULL
- workspace 无法解析或不存在时，不尝试写 AuditEvent；只写不含原始请求标识、SQL、Args 或凭证的脱敏应用安全日志，并携带服务端 trace ID 与稳定 reason code
- 不能使用任意 resource 标识填充 UUID `workspace_id`，也不能伪造 workspace/member/connection 来满足外键约束
- 上述无法持久化为 AuditEvent 的前置拒绝返回原始业务错误码，不改写为 `audit_failed`

---

## 9. 可执行测试矩阵（修订）

### 9.1 PostgreSQL 分类测试

| ID | 输入 SQL | 预期 StatementKind | 预期 Features | 预期决策 |
|---|---|---|---|---|
| PG-01 | `SELECT * FROM t` | SELECT | — | allowed |
| PG-02 | `SELECT $1, $2 FROM t WHERE c=$3` | SELECT | — | allowed |
| PG-03 | `select * from t` | SELECT | — | allowed |
| PG-04 | `-- comment\nSELECT * FROM t` | SELECT | — | allowed |
| PG-05 | `/* block */ SELECT * FROM t` | SELECT | — | allowed |
| PG-06 | `SELECT 'DROP TABLE t;' FROM t` | SELECT | — | allowed |
| PG-07 | `WITH cte AS (SELECT * FROM t) SELECT * FROM cte` | SELECT | HasCTE | allowed |
| PG-08 | `SELECT 1 UNION SELECT 2` | SELECT | HasSetOperation | allowed |
| PG-09 | `EXPLAIN SELECT * FROM t` | EXPLAIN | — | allowed |
| PG-10 | `EXPLAIN (FORMAT JSON) SELECT * FROM t` | EXPLAIN | — | allowed |
| PG-11 | `SELECT * FROM t FOR UPDATE` | SELECT | HasLockingClause | **denied** |
| PG-12 | `SELECT * FROM t FOR SHARE` | SELECT | HasLockingClause | **denied** |
| PG-13 | `SELECT * FROM t FOR KEY SHARE` | SELECT | HasLockingClause | **denied** |
| PG-14 | `SELECT * FROM t FOR NO KEY UPDATE` | SELECT | HasLockingClause | **denied** |
| PG-15 | `SELECT * INTO new_t FROM t` | SELECT | HasSelectInto | **denied** |
| PG-16 | `WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d` | — | HasCTE + HasModifyingCTE | **denied**（修改性 CTE） |
| PG-17 | `WITH d AS (INSERT INTO t VALUES(1)) SELECT * FROM d` | — | HasCTE + HasModifyingCTE | **denied**（修改性 CTE） |
| PG-18 | `TABLE t` | OTHER | — | **denied**（非 SELECT/EXPLAIN） |
| PG-19 | `VALUES (1,2,3)` | OTHER | — | **denied**（非 SELECT/EXPLAIN） |
| PG-20 | `SELECT 1; DROP TABLE t` | — | — | **denied**（多语句） |
| PG-21 | `SELECT 1; --\nDROP TABLE t` | — | — | **denied**（多语句） |
| PG-22 | `INSERT INTO t VALUES(1)` | INSERT | — | **denied** |
| PG-23 | `UPDATE t SET c=1` | UPDATE | — | **denied** |
| PG-24 | `DELETE FROM t` | DELETE | — | **denied** |
| PG-25 | `CREATE TABLE t (id int)` | DDL | — | **denied** |
| PG-26 | `ALTER TABLE t ADD c int` | DDL | — | **denied** |
| PG-27 | `DROP TABLE t` | DDL | — | **denied** |
| PG-28 | `TRUNCATE t` | DDL | — | **denied** |
| PG-29 | `CALL my_proc()` | CALL | — | **denied** |
| PG-30 | `DO $$ BEGIN END; $$` | CALL | — | **denied** |
| PG-31 | `COPY t FROM '/tmp/data'` | OTHER | — | **denied** |
| PG-32 | `SET work_mem = '1GB'` | OTHER | — | **denied** |
| PG-33 | `PREPARE p AS SELECT * FROM t` | OTHER | — | **denied** |
| PG-34 | `EXECUTE p` | OTHER | — | **denied** |
| PG-35 | `BEGIN` | TRANSACTION | — | **denied** |
| PG-36 | `GRANT SELECT ON t TO u` | DDL | — | **denied** |
| PG-37 | `VACUUM t` | OTHER | — | **denied** |
| PG-38 | `EXPLAIN ANALYZE SELECT * FROM t` | EXPLAIN | HasExplainAnalyze | **denied** |
| PG-39 | `EXPLAIN DELETE FROM t` | EXPLAIN | HasExplainDMLDDL | **denied** |
| PG-40 | `EXPLAIN EXPLAIN SELECT * FROM t` | EXPLAIN | HasNestedExplain | **denied** |
| PG-41 | `""` | — | — | **denied**（空语句） |
| PG-42 | `-- only comment` | — | — | **denied**（仅注释） |
| PG-43 | `SELEC * FORM t` | — | ParseError!=nil | **denied**（解析错误） |
| PG-44 | 极深嵌套 SELECT（>100 层） | — | — | 拒绝或不 panic |

### 9.2 MySQL 分类测试

| ID | 输入 SQL | 预期 StatementKind | 预期 Features | 预期决策 |
|---|---|---|---|---|
| MY-01 | `SELECT * FROM t` | SELECT | — | allowed |
| MY-02 | `SELECT ? FROM t WHERE c = ?` | SELECT | — | allowed |
| MY-03 | `SELECT * FROM t FOR UPDATE` | SELECT | HasLockingClause | **denied** |
| MY-04 | `SELECT * FROM t LOCK IN SHARE MODE` | SELECT | HasLockingClause | **denied** |
| MY-05 | `SELECT * INTO OUTFILE '/tmp/d' FROM t` | SELECT | HasIntoOutfile | **denied** |
| MY-06 | `SELECT * INTO DUMPFILE '/tmp/d' FROM t` | SELECT | HasIntoOutfile | **denied** |
| MY-07 | `SELECT id INTO @var FROM t` | SELECT | HasIntoVar | **denied** |
| MY-08 | `SELECT @x := id FROM t` | SELECT | HasAssignment | **denied** |
| MY-09 | `/*!50000 DROP TABLE t*/ SELECT 1` | — | HasExecComment | **denied**（真正可执行注释） |
| MY-10 | `/*!40014 SET NAMES utf8mb4*/ SELECT 1` | — | HasExecComment | **denied**（真正可执行注释） |
| MY-11 | `SELECT '/*!50000 DROP TABLE t*/' AS txt` | SELECT | — | **allowed**（字符串字面量中的 `/*!`，不是可执行注释，parser 必须能区分） |
| MY-12 | `SELECT * FROM t /* 普通注释含 /*!50000 */  WHERE id=1` | SELECT | — | **allowed**（普通块注释中的 `/*!` 不表示可执行注释） |
| MY-13 | `/*!99999 SELECT 1*/` | — | HasExecComment | **denied**（无法识别的版本号仍为可执行注释语法，fail-closed：必须无条件拒绝；Spike 若不能证明 parser/lexer 可靠拒绝则候选不通过） |
| MY-14 | `WITH cte AS (SELECT * FROM t) SELECT * FROM cte` | SELECT | HasCTE | allowed |
| MY-15 | `WITH d AS (DELETE FROM t) SELECT * FROM d` | — | HasCTE | **denied** |
| MY-16 | `SELECT 1; DROP TABLE t` | — | — | **denied** |
| MY-17 | `INSERT INTO t VALUES(1)` | INSERT | — | **denied** |
| MY-18 | `LOAD DATA INFILE '/tmp/d' INTO TABLE t` | OTHER | — | **denied** |
| MY-19 | `HANDLER t OPEN` | OTHER | — | **denied** |
| MY-20 | `DO SLEEP(1)` | CALL | — | **denied** |
| MY-21 | `LOCK TABLES t READ` | OTHER | — | **denied** |
| MY-22 | `SET @x = 1` | OTHER | — | **denied** |
| MY-20 | `EXPLAIN DELETE FROM t` | EXPLAIN | — | **denied** |

### 9.3 授权测试

| ID | 场景 | 阶段 | 预期错误码 | Execution 创建？ | Audit Actor |
|---|---|---|---|---|---|
| AUTH-01 | Principal UserID 非法 UUID | A | `invalid_scope` | 否 | system |
| AUTH-02 | User 不存在 | A | `unauthorized` | 否 | system |
| AUTH-03 | User status=disabled | A | `forbidden` | 否 | system |
| AUTH-04 | User 非 workspace member | A | `forbidden` | 否 | system |
| AUTH-05 | Connection 不属于 workspace | A | `connection_not_found` | 否 | system |
| AUTH-06 | Connection 不存在 | A | `connection_not_found` | 否 | system |
| AUTH-07 | ConnectionPolicy 不存在 | A | `policy_not_configured` | 否 | system |
| AUTH-08 | AllowRead=false | A | `read_not_allowed` | 否 | system |
| AUTH-09 | AllowRead=true，全部通过 | A→C | — | 是 | user |

### 9.4 NextPage 重新授权测试

| ID | 场景 | 预期 |
|---|---|---|
| NEXT-01 | NextPage 时 member 被移除 | `forbidden`（阶段 A） |
| NEXT-02 | NextPage 时 AllowRead 改为 false | `read_not_allowed`（阶段 A） |
| NEXT-03 | NextPage 时 Connection 被删除 | `connection_not_found`（阶段 A） |
| NEXT-04 | NextPage 时连接 generation 变化 | 拒绝 |
| NEXT-05 | NextPage scope/connection 不匹配 | 拒绝 |

### 9.5 审计与故障注入测试

| ID | 场景 | 预期 |
|---|---|---|
| AUDIT-01 | 成功执行 | execution(completed) + audit(succeeded)，metadata 含 statement_hash/row_count/duration_ms/engine |
| AUDIT-02 | AST 策略拒绝 | execution(failed) + audit(denied)，metadata 含 reason_code/error_code |
| AUDIT-03 | 执行超时 | execution(failed) + audit(failed)，error_code=query_timeout |
| AUDIT-04 | 客户端取消 | execution(cancelled) + audit(cancelled) |
| AUDIT-05 | Execution 创建失败（DB 错误） | 返回 `internal_error`，不访问目标库 |
| AUDIT-06 | running 更新失败 | 返回 `internal_error`/`audit_failed`；**不获取 Adapter pool、不连接目标数据库、不执行查询**；Adapter.Query 调用次数必须 = 0 |
| AUDIT-07 | 阶段 D 完成后 Audit append 失败 | 返回 `audit_failed`，不返回查询结果，明确 message 包含"查询已执行但审计失败" |
| AUDIT-08a | 阶段 A workspace 已确认存在但 AuditEvent append 失败 | 返回 `audit_failed`；不访问目标数据库；不创建 Execution | 
| AUDIT-08b | 阶段 A workspace 无法解析或不存在 | 不尝试写 AuditEvent（FK 约束）；仅写脱敏应用安全日志（含 trace_id/reason_code）；返回原始业务错误码（`invalid_scope` / `connection_not_found`），不改为 `audit_failed` |
| AUDIT-09 | metadata 不含 SQL/Args/密码/原始错误 | canary 检测（在 SQL/Args 中放入标记字符串，验证不会出现在 metadata 中） |
| AUDIT-10 | statement_hash 格式 | 64 字符 hex |
| AUDIT-11 | error_code 不含原始错误前缀 | 不含 `pq:` 、`MySQL Error`、`SQLSTATE` |

### 9.6 Fuzz 不变量

- 任意输入不 panic
- parse error 必须拒绝
- 多 AST statement 必须拒绝
- 未处理节点必须拒绝
- 关键字在字符串/注释中不误判
- 持续 30 秒（`-fuzztime=30s`），seed 包含双方言对抗语料

### 9.7 依赖验证测试

| ID | 验证项 | 通过标准 |
|---|---|---|
| DEP-01 | `GOOS=windows go build ./...` | 成功 |
| DEP-02 | `GOOS=linux go build ./...` | 成功 |
| DEP-03 | 枚举所有直接和传递依赖的 LICENSE 文件（`go mod download` + 扫描各模块缓存目录） | 无 GPL/AGPL/SSPL/未知许可证 |
| DEP-04 | 新增直接依赖数 | ≤5 |
| DEP-05 | Spike 报告许可证清单 | `P0-04-spike-report.md` 含候选解析器及全部传递依赖的许可证信息；`docs/DEPENDENCY-LICENSES.md` 仅在 Owner 正式批准并引入依赖后更新 |

---

## 10. 需要 Owner 决策的阻断项（修订）

| # | 决策项 | 修订后建议 | 原建议 |
|---|---|---|---|
| 1 | **解析器选择** | ❌ 不批准 GoSQLX。对 pgparser + TiDB Parser 完成 **依赖 Spike** 后再审批 | GoSQLX（已撤回） |
| 2 | **Denied 请求创建 Execution** | **分阶段**：前置授权失败不创建 Execution（FK 约束限制）；授权通过后才创建 pending Execution | 全部创建（已撤回） |
| 3 | **公开 HTTP 路由** | **P0-04 不注册**；内部 ExecutionService 仅通过测试验证 | 等 P0-05（前提已修正） |
| 4 | **contracts 更新** | **P0-04 不更新**；Go 内部服务契约为本任务范围 | 更新（已撤回） |
| 5 | **审计写入失败** | **分阶段**：执行前 fail-closed（不访问目标库）；执行后 fail-closed（揭露已执行事实） | 笼统 fail-closed（已修正） |
| 6 | **MySQL 可执行注释** | **必须由解析器/lexer 行为证明**；候选不能可靠识别则 Spike 不通过并评估其他解析器。原始字符串检测只能作为额外保守防护，不得替代方言安全边界 | 依赖解析器 |
| 7 | **登录/P0-05 依赖** | 删除"P0-05 实现登录"假设；P0-04 ActorID 由 AuthenticatedPrincipal（测试/内部调用方注入） | 提及 P0-05 登录 |
| 8 | **数据库只读保护** | 评估执行层增加 `default_transaction_read_only` 或 `SESSION TRANSACTION READ ONLY`；暂不实施则记录 Owner 接受的残余风险 + 后续任务 | 未提及 |

---

## 附录 A：实施步骤（审批 + Spike 通过后）

1. 在仓库外隔离临时 Go module/harness 中执行依赖 Spike → `docs/tasks/P0-04-spike-report.md`；不得修改正式 `go.mod`/`go.sum`
2. Owner 批准新增依赖
3. `go get` 安装选定解析器，验证构建
4. 更新 `docs/DEPENDENCY-LICENSES.md`
5. 创建 `apps/api/internal/sqlpolicy/` 包
6. 扩展 `sanitizeAuditMetadata()` 允许列表（含逐字段单元测试 + 泄漏 canary）
7. TDD：双方言分类测试（RED → GREEN）
8. Fuzz 测试
9. 创建 `apps/api/internal/execution/` 包
10. 授权授权集成测试
11. 审计摘要与故障注入测试
12. 集成测试（双引擎完整链路）
13. 更新任务卡和文档
14. 推送并创建 Draft PR

## 附录 B：修订记录

| 日期 | 修订内容 |
|---|---|
| 2026-07-23 | 初版 |
| 2026-07-23 | 回应 Owner 审查 v1：撤回 GoSQLX 推荐；拆分执行流（前置授权 → 不创建 Execution）；修正 P0-05 前提；修正 SELECT 安全声明；StatementKind 改为 AST 事实模型；扩展审计允许列表；拆分审计 fail-closed；分离业务错误码与 HTTP 映射；暂不更新 contracts；测试矩阵落为可执行表格 |
| 2026-07-23 | 回应 Owner 审查 v2：修 running 更新失败不连目标库 + Adapter 调用次数断言；阶段 A 审计拆为 workspace 存在/不存在两路径；MySQL 可执行注释改为 parser/lexer 必须能力，Spike 不通过即淘汰候选；拆分 FirstPage/NextPage 请求类型；补齐 timeout 上限公式；修正许可证验证方法（枚举 LICENSE 文件）；Bytebase Omni 许可证改为 MIT；TiDB Parser 可执行注释改为待验证；TABLE/VALUES 默认拒绝；Spike 补充单语句检测行 |
| 2026-07-23 | 回应 CodeRabbit 审查：ASTFeatures 扩展 HasModifyingCTE/HasExplainDMLDDL/HasNestedExplain；continuation token 安全属性（绑定 Principal/Connection/策略版本/过期时间/防篡改/防重放）；ExecuteFirstPageRequest 重命名 + Adapter 字段映射契约；授权错误码拆分 policy_not_configured vs read_not_allowed；Adapter→业务错误码+审计 outcome 映射表；审计失败重试与结果恢复策略；MySQL 可执行注释反例（字符串字面量/普通注释/非法版本号）；AUDIT-08 拆分为 workspace 存在/不存在两条独立路径 |

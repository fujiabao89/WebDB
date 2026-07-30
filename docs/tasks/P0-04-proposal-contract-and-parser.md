# P0-04：契约与解析器提案（修订版）

> 状态：架构修订已获 Owner 批准；Round 3（WebDB 自有 ECM lexer + 官方未修改 Omni AST）待执行；Omni 精确版本、正式依赖与 TDD 仍待再次审批｜日期：2026-07-23（修订：2026-07-30）｜作者：Claude Code
>
> 修订记录：回应 Owner 审查 2026-07-23，修订解析器策略、执行流拆分、审计语义、错误码和测试矩阵。
>
> Owner 决策（2026-07-30，批准人：`fujiabao89`）：PostgreSQL `TABLE` 按解析器归一化后的等价 `Select` AST 处理，不要求与源语法 `SELECT` 区分；其余 `Select` 安全门禁不变。
>
> Owner 决策（2026-07-30，批准人：`fujiabao89`）：MySQL 可执行注释由 WebDB 自有、方言感知、fail-closed 的 lexer 在 AST 前识别；AST 使用固定精确版本、未修改且来自 `github.com/bytebase/omni` 官方上游的实现。Omni 上游 ECM API/PR 不再是 Round 3 或正式实施的前置条件。

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

```text
unsupported_engine, unsupported_capability, invalid_config,
connection_failed, connection_busy, rate_limited,
query_timeout, query_cancelled, invalid_page_token,
database_error, pool_closed, stale_config, config_conflict,
unsupported_query, result_too_large, pagination_capacity_exhausted
```

P0-04 需要新增 SQL 解析/策略层面的错误码。`unsupported_query` 已是 Adapter 的稳定 keyset 分页错误码；P0-04 Service 复用同一稳定字符串表示"请求需要分页但缺少可验证的唯一排序计划"，但必须在 Service 阶段 C 自行构造，不能伪装成 Adapter 返回。P0-04 的"不支持语句类型"仍使用独立的 `unsupported_statement`。

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

当前仓库实现仍允许 `summary`、`rows_affected`、`cached`，且三个键均接受 string/float64/bool；字符串最多保留 500 字节，并通过 `looksLikeSQL()`/`looksLikeCredential()` 启发式脱敏。非法 JSON/null/非对象不会在 sanitizer 层返回 Go 错误，而是原样交给持久化约束；未知字段丢弃。`row_count` 尚未进入允许列表。

P0-04 必须先完成尚未实现的基线收紧：移除自由文本 `summary` 及两项启发式函数；`rows_affected`/`row_count` 仅允许 0..2^31-1 整数，`cached` 仅允许 bool；非法 JSON/null/非对象直接返回错误。之后才可按 Owner 批准结果扩展 statement_hash（64 字符 hex）、error_code/reason_code（稳定枚举）、engine/environment（稳定枚举）、duration_ms（非负整数）。不得把计划行为描述为当前实现。

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

1. PostgreSQL/MySQL 分方言 AST 解析（固定官方未修改 Omni 精确版本）
2. MySQL 可执行注释由 WebDB 自有方言 lexer 在 AST 前识别并拒绝
3. 单语句限制（拒绝多语句）
4. 只读语句分类（fail-closed：无法判定即拒绝）
5. 工作区、连接、成员、环境、连接策略交集授权
6. statement timeout、MaxRows、取消和 Adapter 准入错误传播
7. 稳定服务/API 业务错误码
8. execution 生命周期和脱敏审计摘要
9. P0-03 Adapter 接入

### 非目标

- DML/DDL 审批或执行
- 登录和身份认证实现（不在 P0 范围内）
- 凭证解密/轮换（P0-05）
- 公开 HTTP SQL 执行路由
- 更新 `packages/contracts`
- SQL 自动改写
- 将字符串前缀、正则或无上下文的原始子串扫描作为 SQL/ECM 安全边界
- 浏览器直连
- UI
- 通用数据库协议代理
- 生产写入
- 自动审查闭环

---

## 3. 已批准架构与 Round 3 隔离 Spike 计划

### 3.1 决策依据（ADR-007）

> PostgreSQL/MySQL 分别使用对应方言 AST parser；任何无法可靠解析或判定的语句默认拒绝。MySQL 在 AST 前增加 WebDB 自有方言 lexer，用于识别并拒绝可执行注释。

因此，安全裁决由两层组合完成：

1. **MySQL lexical gate**：仅 MySQL 执行，先识别 `/*!...*/` 可执行注释；命中、出错或无法可靠判定时立即 fail-closed。
2. **Dialect AST gate**：PostgreSQL/MySQL 均使用官方未修改 Omni 的对应方言 AST，负责单语句、顶层类型和危险 AST 特征裁决。

字符串前缀、正则或无上下文的 `strings.Contains(sql, "/*!")` 只能造成上下文误判，不满足该 lexical gate。PostgreSQL 不经过 MySQL lexer。

### 3.2 AST 候选：官方未修改 Bytebase Omni

| 维度 | 决策与证据 |
|---|---|
| **Module 来源** | `github.com/bytebase/omni` 官方上游；生产依赖不得使用 fork 或 `replace` 隐式替换 |
| **Round 2 基线** | `v0.0.0-20260728103305-d2f82de1b468`；PG base 25/25、PG EXPLAIN 7/7、MySQL base 43/43 |
| **PostgreSQL TABLE** | Omni 归一化为等价 `Select` AST；Owner 已批准按 `SELECT` 同等策略处理，全部其他 fail-closed 门禁不变 |
| **MySQL ECM** | Omni 基线无公开识别信号；由 §3.3 WebDB 自有 lexer 补齐，因此不再要求 Omni 提供 ECM API |
| **许可证** | Omni 为 MIT；仍须针对 Round 3 精确版本重新枚举全部直接和传递依赖 |
| **正式版本** | 尚未批准；Round 3 必须固定一个官方上游 commit/pseudo-version，通过全部门禁后再由 Owner 批准 |

Round 2 数据只证明基线能力，不等同于正式依赖批准。Round 3 使用的精确官方版本及其 `go.mod`/`go.sum`、许可证和跨平台构建结果必须写入 Spike 报告。

### 3.3 WebDB 自有 MySQL ECM lexer

该组件是安全边界，不是提示器或 SQL 改写器。正式实现放入 WebDB 内部包并暴露稳定的 Go API；Round 3 先在仓库外隔离 harness 中验证相同算法，不提前修改生产代码。

**处理顺序**：

```text
原始 MySQL SQL
  → WebDB MySQL lexer
      → HasExecComment=true / lexer error / mode 不可信：拒绝，不调用 Omni
      → 明确无 ECM：调用官方 Omni MySQL AST
  → 单语句 + AST 危险特征分类
  → 任一未知或失败：拒绝
```

**词法契约**：

- 使用确定性、单趟、无回溯的词法状态机；时间复杂度 O(n)，额外空间 O(1)，发现首个 ECM 后立即停止。
- 仅当 `/*!` 位于可执行 SQL 上下文时设置 `HasExecComment=true`；字符串字面量、引号标识符、普通块注释、优化器 hint、`#` 行注释和有效 `--` 行注释中的相同文本不得误报。
- 必须处理单/双引号、反引号、成对引号、反斜杠、`#`、普通 `/*...*/`、`/*+...*/`，以及 `--` 后 MySQL 认可的空白/控制字符。
- 任何 ECM opener 均拒绝，不解析或执行其正文，不根据服务器版本决定是否放行；未知版本、非法版本、嵌套歧义和未闭合 ECM 均 fail-closed。
- 影响词法行为的 session mode（至少包括 `NO_BACKSLASH_ESCAPES`，以及实现证明需要的其他 mode）只能从服务端可信连接/session 配置派生，不接受客户端声明。mode 未知时必须返回错误并拒绝，或对全部可能模式作保守判断并在任一模式命中时拒绝。
- 不记录原始 SQL、ECM 正文或 lexer 原始错误；只向策略层返回结构化事实/稳定错误。
- 禁止使用 regex、字符串前缀或无上下文原始子串扫描实现上述允许/拒绝判断。

### 3.4 Round 3 通过标准（正式依赖审批前必须完成）

**本次架构批准不等于正式依赖批准。** Round 3 必须在仓库外隔离环境完成：

| Spike 项目 | 验证内容 | 通过标准 |
|---|---|---|
| **官方来源与精确版本** | 固定 `github.com/bytebase/omni` 官方上游 commit/pseudo-version；检查 module graph | 无 fork、无 `replace`；报告记录完整版本和校验信息 |
| **Windows/Linux 构建** | `GOOS=windows GOARCH=amd64 go build ./...` 和 `GOOS=linux GOARCH=amd64 go build ./...` | 两平台均成功 |
| **PG AST 回归** | 重跑 Round 2 PG base/EXPLAIN 及 Owner 批准的 `TABLE` 契约用例 | 全部通过；锁定、修改型 CTE、多语句、未知节点和解析失败继续拒绝 |
| **MySQL AST 回归** | 原样重跑 v7 MySQL base 43 条 | **43/43**；ECM 识别不计入 Omni AST 成绩 |
| **ECM recognition** | WebDB lexer 重跑 v7 ECM 12 个正例、5 个反例 | **12/12 positive、5/5 negative** |
| **ECM 边界** | SQL mode、反斜杠、单双引号、反引号、`#`、`--` 控制空白、普通注释、hint、未知版本、未闭合和嵌套歧义 | 命中/错误均 fail-closed；反例不误报；不 panic |
| **组合调用顺序** | lexer 与 Omni AST 组合 harness | ECM 或 lexer error 时 Omni/Adapter 均不调用；明确无 ECM 后才进入 AST |
| **Fuzz/复杂度** | 对 lexer 和组合分类器执行对抗 seed 与 fuzz | 不 panic、不越界；无超线性扫描或无界内存增长 |
| **许可证枚举** | 枚举精确 Omni 版本全部直接和传递依赖的 LICENSE 文件 | 无 GPL/AGPL/SSPL/未知许可证；生成完整清单 |

Round 3 原始命令、退出码、完整用例结果、官方 Omni 精确版本及许可证清单写入 `docs/tasks/P0-04-spike-report.md`。全部通过后，Owner 才能正式批准该精确依赖并授权进入生产 TDD。Omni 上游 issue/PR 可继续作为社区贡献，但不再阻塞 Round 3。

**Spike 隔离边界**：

- 使用仓库外临时目录中的独立 Go module/harness，并固定官方 Omni 精确版本
- WebDB lexer 算法先在隔离 harness 中验证；不得借 Round 3 提前创建生产 `sqlpolicy/` 或 `execution/` 代码
- 不修改或提交 `apps/api/go.mod`、`apps/api/go.sum`
- 不创建 `apps/api/internal/sqlpolicy/`、`apps/api/internal/execution/` 等正式工程代码
- 不更新 `docs/DEPENDENCY-LICENSES.md`；候选及传递许可证先记录在 Spike 报告中
- 任一门禁无法满足 fail-closed、方言 AST、单语句检测或 MySQL 可执行注释识别要求时，Round 3 结论必须为“不通过”
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

// MySQLLexerMode 只能从服务端实际连接/session 配置派生，不接受客户端输入。
// 无法可靠获得 mode 时，lexer 必须返回错误或按全部可能模式保守判定。
type MySQLLexerMode struct {
    NoBackslashEscapes bool
    ANSIQuotes         bool
}

// LexicalFeatures 是 AST 解析前得到的词法事实。
// PostgreSQL 使用零值；MySQL 必须先完成此步骤。
type LexicalFeatures struct {
    HasExecComment bool // 代码上下文中的 MySQL /*!...*/ 可执行注释
}

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
    HasExplainAnalyze  bool // EXPLAIN ANALYZE
    HasModifyingCTE    bool // 含数据修改 CTE（INSERT/UPDATE/DELETE/MERGE 在 WITH 中）
    HasExplainDMLDDL   bool // EXPLAIN 的目标是 DML/DDL
    HasNestedExplain   bool // 嵌套 EXPLAIN（EXPLAIN EXPLAIN ...）
}

// ClassificationResult 语句分类结果
type ClassificationResult struct {
    StatementKind   StatementKind
    LexicalFeatures LexicalFeatures
    ASTFeatures     ASTFeatures
    StatementHash   string // SHA-256(normalized_sql)
    StatementCount  int   // AST parser 解析出的语句数量
    LexError        error // lexer 无法可靠判定；非 nil 时不得调用 AST parser
    ParseError      error // AST 解析错误（nil = 解析成功）
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
    ReasonParseError         StableReasonCode = "sql_parse_error" // lexer/parser 无法可靠判定
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
    SortKeys       []SortKey         // 请求排序键；由 VerifySortPlan 验证后生成 VerifiedSortPlan（ADR-014）
    PageSize       int               // 0 = 使用默认值
    RequestMaxRows int               // 0 = 使用策略上限
    RequestTimeout time.Duration     // 0 = 使用策略上限
}

// ExecuteNextPageRequest 服务层续页请求。Token 由服务端 Registry 签发（ADR-015），
// 客户端仅持有 opaque handle。调用方不得重新提交 SQL/Args/SortKeys。
type ExecuteNextPageRequest struct {
    Principal  AuthenticatedPrincipal
    Token      string // 由服务端 ContinuationRegistry 签发的 opaque handle
}

// ---- 服务层 → Adapter 字段映射契约（对齐 ADR-014、ADR-015）----
// ExecuteFirstPageRequest 经授权和 VerifySortPlan 后构造 adapter.FirstPageRequest：
//   adapter.Scope.UserID      ← Principal.UserID
//   adapter.Scope.WorkspaceID ← Principal.WorkspaceID
//   adapter.SQL               ← 原始 SQL（经策略层 AST 分类已通过）
//   adapter.Args              ← 原始 Args（深拷贝，不记录）
//   adapter.SortPlan          ← VerifiedSortPlan（ADR-014：由 internal/queryplan 验证生成）
//   adapter.PageSize          ← min(PageSize, effectiveMaxRows)；PageSize≤0 时使用默认值
//   adapter.MaxRows           ← effectiveMaxRows
// ExecuteNextPageRequest 的 Token 由服务端 ContinuationRegistry 解析（ADR-015），
// 从 ContinuationState 恢复 SQL、Args、VerifiedSortPlan、last sort values、累计行数及原有效限制后重新验证成员和策略；
// Adapter 接收结构化的 VerifiedNextPagePlan（含 last sort values），不接受客户端 token。

// ---- Continuation Token 安全属性（对齐 ADR-015）----
// Service 是唯一 token/Registry Owner。Adapter 不生成/解析/保存 token。
// P0 使用有状态服务端 Registry + CSPRNG opaque handle：
//   - 客户端仅获得 32 字节以上 CSPRNG opaque handle
//   - SQL、Args、SortKeys、结果和 last values 不进入客户端 token
//   - Registry 保存完整 ContinuationState（绑定见 ADR-015 §4）
//   - TTL ≤ 5 分钟；容量上限：global=10000, per user=100, per workspace=500, per connection=200
//   - NextPage: 原子 claim → 重新验证成员/连接/策略 → 成功后 Rotate(oldDigest, newDigest, newState)（同一容量槽位原子替换）
//   - 无后续页: Complete(oldDigest) 删除旧 in-flight
//   - 失败/取消/超时/panic: Abort(oldDigest) 删除旧 in-flight
//   - Rotate/Complete/Abort 幂等；finalizer 不能删除已旋转出的新 token
//   - 单次使用；失败后不恢复旧 token
//   - P0 服务重启使所有 token 失效（内存 Registry，不虚假承诺持久化）

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
5. **缺失或无效连接策略默认拒绝**：
   - `ConnectionPolicy` 不存在 → `policy_not_configured`
   - `AllowRead != true` → `read_not_allowed`
   - 定义正数服务常量 `MaxRowsSafetyCap`，且该常量不得大于 `math.MaxInt-1` 或 Adapter/驱动可安全表示上限中的较小值；它是所有来源（数据库、测试替身及未来策略源）共同适用的有限上界
   - `policy.MaxRows <= 0` 或 `policy.MaxRows > MaxRowsSafetyCap` → `policy_not_configured`；即使数据库约束通常阻止部分无效状态，Service 仍须在计算 `effectiveMaxRows`、构造 Adapter request 和调用 Adapter 前 fail-closed，`Adapter.Query=0`、目标数据库访问次数为 0、Audit outcome=`denied`
6. **客户端不能提高策略上限 — MaxRows**：
   - 仅在确认 `0 < policy.MaxRows <= MaxRowsSafetyCap` 后计算有效上限；不得把无效策略值传给 Adapter 触发其默认值回退
   - 客户端 RequestMaxRows ≤ 0 → 使用 `policy.MaxRows`
   - 客户端 RequestMaxRows > 0 → `effectiveMaxRows = min(policy.MaxRows, RequestMaxRows)`
   - 结果必须满足 `0 < effectiveMaxRows <= MaxRowsSafetyCap`；0、负数或超大值均不得绕过策略上限
7. **客户端不能提高策略上限 — Timeout**：
   - 客户端 RequestTimeout ≤ 0 → 使用 `policy.StatementTimeoutMs`
   - 客户端 RequestTimeout > 0 → `effectiveTimeout = min(policy.StatementTimeoutMs, RequestTimeout)`
   - 零值/负数视为"使用策略上限"；超大值钳制到策略上限；溢出由 Go 类型安全保护
   - 如果调用方 context 已有更短 deadline → 保留更短 deadline（`context.WithTimeout` 语义）
8. **NextPage 重新授权**：每次 NextPage 都重新验证成员资格、连接和策略；NextPage 从服务端 continuation 恢复 SQL、Args、VerifiedSortPlan、last sort values、累计行数及原有效限制，不接受调用方重新提交这些字段
9. **effectivePageSize 定义与分页安全判定**：

   ```text
   normalizedPageSize：
   - 请求 PageSize <= 0：使用默认值 100
   - 请求 PageSize > 0：使用请求值
   - 最大钳制为 500（超过则取 500）

   effectivePageSize = min(normalizedPageSize, effectiveMaxRows)
   requiresPagination = effectiveMaxRows > effectivePageSize
   ```

   - effectiveMaxRows 必须是策略上限和请求上限求交后的正数，且不得超过 `MaxRowsSafetyCap`。
   - requested PageSize > effectiveMaxRows 时，effectivePageSize 等于 effectiveMaxRows，属于单页请求。
   - 不允许使用未经规范化的原始 PageSize 进行分页安全判断。
   - 若 `requiresPagination` 为 true（见 ADR-014）：执行前必须取得有效 `VerifiedSortPlan`。无法证明唯一排序时在访问目标数据库前拒绝，不执行第一页。
   - 若 `requiresPagination` 为 false：属于单页受限请求，可不创建 continuation，但行数、字节、超时限制不变；不得发放 token。执行层必须先以 checked-add 计算 `readLimit = effectiveMaxRows + 1`，再最多读取 `readLimit` 行（额外一行仅作 overflow sentinel）；读到 sentinel 时返回 `result_too_large`，丢弃结果且不生成 token。未读到 sentinel 时才可成功返回最多 `effectiveMaxRows` 行。
   - 若有限上界校验失败或 checked-add 无法表示结果，返回 `policy_not_configured` 并 fail-closed：不构造或传递 Adapter `MaxRows`/`readLimit`，`Adapter.Query=0`、目标数据库访问次数为 0、Audit outcome=`denied`。

10. **权限撤销即时生效**

### 4.3 执行流拆分（关键修订）

```text
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
  ├─ 阶段 C：SQL lexical + AST 策略判断
  │   ├─ MySQL：先运行 WebDB 自有 lexer
  │   │   └─ ECM / lexer error / mode 不可信 → fail-closed，不调用 Omni
  │   ├─ lexer 明确无 ECM 后运行官方 Omni 方言 AST parser
  │   ├─ 单语句检查
  │   └─ 只读 AST 分类
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
      ├─ 仅 running 持久化成功后才调用 Adapter.Query
      │
      ├─ Adapter 返回 rate_limited（ADR-016）
      │      → Adapter.Query 调用次数 = 1
      │      → DB connection acquire 调用次数 = 0
      │      → SQL Query 调用次数 = 0
      │      → 更新 execution status=failed, error_code=rate_limited
      │      → 创建脱敏 AuditEvent（outcome=denied）
      │
      ├─ Adapter 返回 connection_busy（ADR-016）
      │      → Adapter.Query 调用次数 = 1
      │      → DB connection acquire 已尝试（超时）
      │      → SQL Query 调用次数 = 0
      │      → 更新 execution status=failed, error_code=connection_busy
      │      → 创建脱敏 AuditEvent（outcome=failed）
      │
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

`invalid_page_token` 在 NextPage 前置校验中处理。`rate_limited` 和 `connection_busy` 的来源和语义见 ADR-016——准入保持在 Adapter 内部，不存在独立的"Service 前置准入层"。

`unsupported_query` 是例外的 **Service 阶段 C 前置错误**：当 `requiresPagination=true` 且缺少或无法验证 `VerifiedSortPlan` 时，Service 在调用 Adapter 前返回该稳定业务错误码。此路径必须断言 `Adapter.Query=0` 和目标数据库访问次数为 0；它复用稳定字符串，不表示 Adapter 实际返回了错误。

| Adapter 错误码 | 触发位置 | Execution 状态 | Audit Outcome | 备注 |
|---|---|---|---|---|
| `rate_limited` | Adapter.TryAcquire | `failed` | `denied` | permit 获取失败；DB acquire/SQL Query 均未发生 |
| `connection_busy` | Adapter.pgPool.Acquire / sqlDB.Conn 超时 | `failed` | `failed` | permit 已获取；DB acquire 已尝试；SQL Query 未发生 |
| `query_timeout` | Adapter SQL 执行超时 | `failed` | `failed` | |
| `query_cancelled` | Adapter context 取消 | `cancelled` | `cancelled` | |
| `database_error` | Adapter SQL 执行 | `failed` | `failed` | 脱敏原始错误 |
| `result_too_large` | Adapter 结果超出限制 | `failed` | `failed` | |
| `invalid_page_token` | NextPage 前置 | — | — | NextPage 前置校验拒绝，不进入 Adapter |
| 其他未匹配错误 | Adapter | `failed` | `failed` | fail-closed：未知 Adapter 错误均脱敏为 database_error |

---

## 5. 精确允许/拒绝矩阵

### 5.1 允许类别

| 类别 | PostgreSQL | MySQL | 条件 |
|---|---|---|---|
| 单条普通 SELECT | ✅ | ✅ | MySQL `LexicalFeatures.HasExecComment=false`；ASTFeatures 无锁定/INTO/赋值等危险标记 |
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
| 可执行版本注释 | `/*!50000 DROP TABLE t*/` | N/A | ❌（WebDB lexer 在 AST 前拒绝） |
| EXPLAIN ANALYZE | 实际执行 | ❌ | ❌ |
| EXPLAIN DML/DDL | `EXPLAIN DELETE FROM t` | ❌ | ❌ |
| 嵌套 EXPLAIN | `EXPLAIN EXPLAIN SELECT ...` | ❌ | ❌ |
| 顶层 TABLE | PG `TABLE t`（≈ `SELECT * FROM t`） | ✅（按等价 `Select` AST 处理） | N/A |
| 顶层 VALUES | PG `VALUES (1,2)`（≈ `SELECT 1,2`） | ❌ | N/A |
| lexer/解析错误 | 任何无法可靠词法判定或解析的输入 | ❌ | ❌ |
| 未识别 AST 节点 | 解析器返回 unknown | ❌ | ❌ |

> ADR-007 只批准可可靠判定的单条 `Select`/`EXPLAIN` AST。根据 2026-07-30 Owner 决策，PostgreSQL `TABLE` 由解析器归一化为等价 `Select` AST 后按 `SELECT` 同等策略处理；`VALUES` 仍默认拒绝。`TABLE` 不获得例外：锁定子句、修改型 CTE、多语句、未知 AST 节点和解析失败仍一律 fail-closed。

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
| `policy_not_configured` | 策略缺失或关键安全上限无效 | A | ConnectionPolicy 行不存在，或 `policy.MaxRows <= 0`；均在 Adapter 前拒绝 |
| `read_not_allowed` | 禁止读取 | A | ConnectionPolicy 存在但 AllowRead != true |
| `invalid_page_token` | 分页 token 无效 | D | |
| `sql_parse_error` | SQL lexer/parser 无法可靠判定 | C | lexer error、mode 不可信或 AST 语法错误；原始错误不外泄 |
| `multiple_statements` | 检测到多条语句 | C | |
| `statement_not_allowed` | 语句类型不允许 | C | DML/DDL/管理语句等 |
| `unsupported_statement` | 不支持该语句类型 | C | 解析器无法识别 AST 节点 |
| `unsupported_query` | 分页请求缺少或无法验证唯一排序计划 | C | Service 前置拒绝；复用 Adapter 稳定字符串，但 `Adapter.Query=0` |
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
| `unsupported_query` | 422 | 分页前置条件不满足；不是数据库执行错误 |
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

```text
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
| 阶段 C（lexical/AST 策略拒绝） | **是**（已有 pending） | `user` | principal.UserID | `denied` |
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

### 8.5 `sanitizeAuditMetadata()` 允许列表

当前仓库实现（P0-04 尚未修改）：

- 允许键为 `summary` / `rows_affected` / `cached`；三个键均接受 string/float64/bool
- string 最多保留 500 字节，并通过 `looksLikeSQL()` / `looksLikeCredential()` 启发式脱敏
- 非法 JSON / null / 非对象在 sanitizer 层原样返回，不产生 Go error；未知字段丢弃
- `row_count` 尚未允许

P0-04 必须先完成的基线收紧（**计划行为，当前未实现**）：

- 移除自由文本 `summary` 和所有通用 string 白名单
- `rows_affected` / `row_count`：仅 0..2^31-1 整数（`toNonNegInt`）
- `cached`：仅 bool
- 非法 JSON / null / 非对象 → error；未知字段 → 丢弃
- 删除 `looksLikeSQL()` / `looksLikeCredential()`，改为逐字段精确格式或枚举校验

完成上述基线收紧后，P0-04 扩展方案仍需 Owner 最终批准：

```go
// P0-04 计划中的严格基线；截至本提案修订日尚未实现：
// rows_affected/row_count: 仅 0..maxAuditCount 整数（toNonNegInt）
// cached: 仅 bool
//
// P0-04 计划新增字段（Owner 待决，不允许重新引入自由文本 summary）：
//   "statement_hash": ..., // SHA-256 hex, 64 chars
//   "duration_ms":    ..., // number
//   "error_code":     ..., // 稳定业务错误码
//   "engine":         ..., // "postgresql" | "mysql"
//   "environment":    ..., // "development" | "staging" | "production"
//   "reason_code":    ..., // 拒绝原因码
```

**约束**：
- `statement_hash` 必须是 64 字符 hex 格式（单元测试验证）
- `error_code` 必须是稳定业务错误码，不得包含 `pq:` 或 `MySQL Error` 前缀
- `engine`/`environment` 必须是已知枚举值
- 新增字符串字段需各自实现精确格式校验（如 statement_hash 为 64 字符 hex），不依赖通用启发式扫描
- 实施时必须删除 `looksLikeSQL()`/`looksLikeCredential()` 并增加回归测试，不能在删除前声称严格基线已经生效
- 实施时需同步更新 `sanitizeAuditMetadata()`：为 error_code/reason_code 添加白名单前缀（如 `invalid_`、`sql_`、`query_`、`connection_`、`rate_`、`result_`、`database_`、`audit_`、`internal_`、`statement_`、`multiple_`、`unsupported_`、`read_`、`policy_`、`forbidden`、`unauthorized`、`stale_`、`config_`、`pagination_`），或改用精确枚举匹配
- SQL 正文、Args、结果、原始数据库错误、密码、secret_ref 仍被拒绝

### 8.6 审计写入失败：分阶段 fail-closed（修订）

| 阶段 | 审计写入失败时的行为 |
|---|---|
| **阶段 A（已确认 workspace 存在）与阶段 B（执行前）** | 审计写入失败时不允许访问目标数据库，返回 `audit_failed`；数据库连接未建立。workspace 无法解析或不存在时不尝试写 AuditEvent，仅写脱敏应用安全日志并返回原始业务错误码。 |
| **阶段 C（lexical/AST 策略拒绝）** | 执行未发生。返回 `audit_failed`。若状态更新已成功，Execution 保持 `failed`；不得回退或误报为 `pending`。 |
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
| PG-18 | `TABLE t` | SELECT（等价 `Select` AST） | — | **allowed**（按 `SELECT` 同等策略） |
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
| PG-45 | `TABLE ONLY t` | SELECT（等价 `Select` AST） | — | **allowed**（按 `SELECT` 同等策略） |
| PG-46 | `TABLE t ORDER BY id LIMIT 10` | SELECT（等价 `Select` AST） | — | **allowed**（按 `SELECT` 同等策略） |
| PG-47 | `TABLE t FOR UPDATE` | SELECT | HasLockingClause | **denied**（TABLE 不豁免锁定子句） |
| PG-48 | `WITH d AS (DELETE FROM t RETURNING *) TABLE d` | — | HasCTE + HasModifyingCTE | **denied**（TABLE 不豁免修改型 CTE） |
| PG-49 | `TABLE t; DELETE FROM t` | — | — | **denied**（多语句，TABLE 不豁免） |
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
| MY-09 | `/*!50000 DROP TABLE t*/ SELECT 1` | — | LexicalFeatures.HasExecComment | **denied**（WebDB lexer 在 AST 前拒绝） |
| MY-10 | `/*!40014 SET NAMES utf8mb4*/ SELECT 1` | — | LexicalFeatures.HasExecComment | **denied**（WebDB lexer 在 AST 前拒绝） |
| MY-11 | `SELECT '/*!50000 DROP TABLE t*/' AS txt` | SELECT | — | **allowed**（字符串字面量中的 `/*!` 不是 ECM，WebDB lexer 必须区分） |
| MY-12 | `SELECT * FROM t /* 普通注释含 /*!50000 */  WHERE id=1` | SELECT | — | **allowed**（普通块注释中的 `/*!` 不表示 ECM） |
| MY-13 | `/*!99999 SELECT 1*/` | — | LexicalFeatures.HasExecComment | **denied**（不按版本是否生效放行） |
| MY-14 | `WITH cte AS (SELECT * FROM t) SELECT * FROM cte` | SELECT | HasCTE | allowed |
| MY-15 | `WITH d AS (DELETE FROM t) SELECT * FROM d` | — | HasCTE | **denied** |
| MY-16 | `SELECT 1; DROP TABLE t` | — | — | **denied** |
| MY-17 | `INSERT INTO t VALUES(1)` | INSERT | — | **denied** |
| MY-18 | `LOAD DATA INFILE '/tmp/d' INTO TABLE t` | OTHER | — | **denied** |
| MY-19 | `HANDLER t OPEN` | OTHER | — | **denied** |
| MY-20 | `DO SLEEP(1)` | CALL | — | **denied** |
| MY-21 | `LOCK TABLES t READ` | OTHER | — | **denied** |
| MY-22 | `SET @x = 1` | OTHER | — | **denied** |
| MY-23 | `EXPLAIN DELETE FROM t` | EXPLAIN | — | **denied** |

Round 3 必须原样重跑 Spike v7 的 MySQL AST base 43 条和 ECM 12 positive/5 negative；上表是最小策略契约，不替代完整 corpus。ECM corpus 还必须增加以下边界断言：

| ID | 场景 | 预期 |
|---|---|---|
| MY-EC-01 | 单/双引号及反引号内出现 `/*!...*/` | `HasExecComment=false`；仅在后续 AST 安全时允许 |
| MY-EC-02 | 普通块注释、optimizer hint、`#` 行注释内出现 `/*!...*/` | `HasExecComment=false` |
| MY-EC-03 | `--` 后为空格、tab、换行、form-feed、vertical-tab 等 MySQL 空白/控制字符，ECM 文本位于该行注释内 | `HasExecComment=false` |
| MY-EC-04 | 未知/非法版本号或未闭合 `/*!` opener | `HasExecComment=true` 或 lexer error；一律拒绝 |
| MY-EC-05 | 同一输入在 `NO_BACKSLASH_ESCAPES` 等可信 mode 下改变引号边界 | 按实际 mode 正确分类；mode 不可信时拒绝 |
| MY-EC-06 | ECM 或 lexer error | Omni parser 调用次数 = 0；Adapter 调用次数 = 0 |

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
| AUDIT-02 | lexical/AST 策略拒绝 | execution(failed) + audit(denied)，metadata 含 reason_code/error_code；不含 SQL/ECM 正文或原始 lexer/parser 错误 |
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
- lexer error、mode 不可信或 `LexicalFeatures.HasExecComment=true` 必须拒绝
- parse error 必须拒绝
- 多 AST statement 必须拒绝
- 未处理节点必须拒绝
- 关键字及 `/*!` 在字符串、引号标识符、普通注释、hint 和行注释中不误判
- MySQL lexer 不出现超线性扫描或随输入无界增长的额外内存
- 持续 30 秒（`-fuzztime=30s`），seed 包含双方言对抗语料、ECM 正反例和 SQL mode 差异输入

### 9.7 依赖验证测试

| ID | 验证项 | 通过标准 |
|---|---|---|
| DEP-01 | `GOOS=windows go build ./...` | 成功 |
| DEP-02 | `GOOS=linux go build ./...` | 成功 |
| DEP-03 | 枚举所有直接和传递依赖的 LICENSE 文件（`go mod download` + 扫描各模块缓存目录） | 无 GPL/AGPL/SSPL/未知许可证 |
| DEP-04 | 新增直接依赖数 | ≤5 |
| DEP-05 | Spike 报告许可证清单 | `P0-04-spike-report.md` 含候选解析器及全部传递依赖的许可证信息；`docs/DEPENDENCY-LICENSES.md` 仅在 Owner 正式批准并引入依赖后更新 |
| DEP-06 | Omni 来源与版本 | `github.com/bytebase/omni` 官方上游精确 commit/pseudo-version；无 fork、无 `replace`；与 Round 3 报告一致 |

### 9.8 分页计划与单页限制测试

| ID | 场景 | 预期 |
|---|---|---|
| PAGE-01 | `effectiveMaxRows > effectivePageSize`，缺少或无效 `VerifiedSortPlan` | Service 阶段 C 返回稳定业务错误码 `unsupported_query`；Execution failed；Audit denied；`Adapter.Query=0`；目标数据库未访问 |
| PAGE-02 | `effectiveMaxRows = effectivePageSize`，无 `VerifiedSortPlan` | 允许单页受限执行；`Adapter.Query=1`；最多读取 `effectiveMaxRows+1` 行；无 overflow sentinel 时最多返回 `effectiveMaxRows` 行；不得返回 continuation token |
| PAGE-03 | requested `PageSize > effectiveMaxRows` | `effectivePageSize` 钳制为 `effectiveMaxRows`；按 PAGE-02/PAGE-08 的 sentinel 规则执行；不得返回 continuation token |
| PAGE-04 | `effectiveMaxRows > effectivePageSize`，`VerifiedSortPlan` 有效，结果确有后续页 | 允许执行；通过 Service Registry 返回 continuation token |
| PAGE-05 | `PageSize <= 0` | 使用默认值 100 后再与 `effectiveMaxRows` 求 min，并据此决定是否要求 `VerifiedSortPlan` |
| PAGE-06 | `PageSize > 500` | 先钳制为 500，再与 `effectiveMaxRows` 求 min，并据此决定是否要求 `VerifiedSortPlan` |
| PAGE-07 | nil/typed-nil/`Valid()=false` 的 `VerifiedSortPlan` | fail-closed；Adapter 不执行目标查询 |
| PAGE-08 | 单页查询实际结果超过限制 | 在 `effectiveMaxRows <= MaxRowsSafetyCap` 已验证后，以 checked-add 得到 `readLimit=effectiveMaxRows+1`；最多读取 `readLimit` 行；检测到 sentinel 即返回 `result_too_large`，Execution status=`failed`、Execution error_code=`result_too_large`、Audit outcome=`failed`，丢弃结果且不得生成 continuation token；数据库读取和内存均保持有界 |
| PAGE-09 | `policy.MaxRows <= 0` 或 `policy.MaxRows > MaxRowsSafetyCap`（损坏数据、测试替身或未来非数据库策略源） | 阶段 A 返回 `policy_not_configured`；不计算 `effectiveMaxRows`，不构造/传递 Adapter `MaxRows`；`Adapter.Query=0`；目标数据库访问次数为 0；Audit outcome=`denied` |
| PAGE-10 | `effectiveMaxRows+1` checked-add 失败（边界测试替身） | 返回 `policy_not_configured`；不构造/传递 Adapter `MaxRows`/`readLimit`；`Adapter.Query=0`；目标数据库访问次数为 0；Audit outcome=`denied` |

---

## 10. 需要 Owner 决策的阻断项（修订）

| # | 决策项 | 修订后建议 | 原建议 |
|---|---|---|---|
| 1 | **解析器选择** | ✅ 架构批准：PostgreSQL/MySQL AST 均使用官方未修改 Omni；精确 commit/pseudo-version 仍须 Round 3 通过后正式审批 | GoSQLX（已撤回） |
| 2 | **Denied 请求创建 Execution** | **分阶段**：前置授权失败不创建 Execution（FK 约束限制）；授权通过后才创建 pending Execution | 全部创建（已撤回） |
| 3 | **公开 HTTP 路由** | **P0-04 不注册**；内部 ExecutionService 仅通过测试验证 | 等 P0-05（前提已修正） |
| 4 | **contracts 更新** | **P0-04 不更新**；Go 内部服务契约为本任务范围 | 更新（已撤回） |
| 5 | **审计写入失败** | **分阶段**：执行前 fail-closed（不访问目标库）；执行后 fail-closed（揭露已执行事实） | 笼统 fail-closed（已修正） |
| 6 | **MySQL 可执行注释** | ✅ 架构批准：WebDB 自有方言 lexer 在 Omni AST 前识别；ECM、lexer error 或 mode 不可信均 fail-closed。禁止 regex/前缀/无上下文子串扫描；Round 3 证据仍待完成 | 依赖解析器 |
| 7 | **登录/P0-05 依赖** | 删除"P0-05 实现登录"假设；P0-04 ActorID 由 AuthenticatedPrincipal（测试/内部调用方注入） | 提及 P0-05 登录 |
| 8 | **数据库只读保护** | 评估执行层增加 `default_transaction_read_only` 或 `SESSION TRANSACTION READ ONLY`；暂不实施则记录 Owner 接受的残余风险 + 后续任务 | 未提及 |

---

## 附录 A：实施步骤（审批 + Spike 通过后）

1. 在仓库外隔离临时 Go module/harness 中实现并验证 WebDB ECM lexer 算法，固定官方 Omni 精确版本，执行 Round 3 → 更新 `docs/tasks/P0-04-spike-report.md`；不得修改正式 `go.mod`/`go.sum`
2. Round 3 全部门禁通过后，由 Owner 书面批准官方 Omni 精确 commit/pseudo-version 及正式依赖
3. `go get` 安装已批准的官方 Omni 版本，验证无 fork/`replace` 并重新构建
4. 更新 `docs/DEPENDENCY-LICENSES.md`
5. 创建 `apps/api/internal/sqlpolicy/` 包，先以 TDD 实现 MySQL lexical gate，再接入 Omni AST
6. 先按 §8.5 收紧 `sanitizeAuditMetadata()` 基线，再扩展获批字段（含逐字段单元测试 + 泄漏 canary）
7. TDD：双方言分类及 lexer→AST 调用顺序测试（RED → GREEN）
8. Fuzz 测试
9. 创建 `apps/api/internal/execution/` 包
10. 授权集成测试
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
| 2026-07-30 | Owner `fujiabao89` 批准 PostgreSQL `TABLE` 按解析器归一化后的等价 `Select` AST 处理；更新 PG-18 为 allowed。锁定子句、修改型 CTE、多语句、未知节点和解析失败继续 fail-closed；`VALUES` 仍默认拒绝。 |
| 2026-07-30 | Owner `fujiabao89` 批准 MySQL 使用 WebDB 自有方言 lexer 前置识别 ECM，随后使用官方未修改 Omni AST。上游 ECM API/PR 不再阻塞；新增 Round 3 组合、mode、fuzz、官方来源和许可证门禁。 |
| 2026-07-30 | 回应 CodeRabbit 审查：为 `MaxRows` 增加有限服务上界和 `effectiveMaxRows+1` checked-add 契约；无效上界或溢出在 Adapter/目标数据库访问前以 `policy_not_configured` fail-closed。 |

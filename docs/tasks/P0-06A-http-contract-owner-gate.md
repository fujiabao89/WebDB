# P0-06A：最小 HTTP 契约、高保真范围映射与 Owner Gate

> **状态：已批准（Owner Gate 通过）｜日期：2026-08-08｜批准人：fujiabao89**
>
> Owner 已对 D01–D18 全部决策逐项给出明确结论（D05a/D08/D11/D13/D14/D15f 为专项决定，其余按推荐选项批准）。本契约作为并发实施任务（WEB-35/36/37/38/39）的共同基线。**批准不等于 API 已实现**：续页路由仍须在 ADR-014/015 目标态迁移完成后才可注册（§9.1）；本文档不自行关闭 WEB-34，Linear 状态由 Owner 更新。
>
> 日期：2026-08-08｜作者：Claude Code｜任务：[WEB-34](https://linear.app/webdb/issue/WEB-34/p0-06a最小-http-契约高保真范围映射与-owner-gate)（父任务 [WEB-12](https://linear.app/webdb/issue/WEB-12/p0-06最小-web-工作台)）
>
> **阅读前须知**：文中"不得""必须"等措辞为**已批准**的契约约束；"目标态"专指 ADR-014/015 已接受但尚未实现的契约，不得理解为已实现。

---

## 1. 状态、任务和权威来源

### 1.1 状态

- **Owner Gate 已通过（2026-08-08，批准人：fujiabao89）**：D01–D18 全部决策已由 Owner 逐项给出明确结论（D05a/D08/D11/D13/D14/D15f 见对应章节，其余按推荐选项批准）。
- 本契约状态为 `已批准（Owner Gate 通过）`，是并发实施任务（WEB-35/36/37/38/39）的共同基线。
- 本文件不自行关闭 WEB-34；Linear WEB-34 状态由 Owner 更新。批准不等于 API 已实现——续页路由仍需 ADR-014/015 迁移完成后方可注册。

### 1.2 任务与验收（Linear WEB-34）

WEB-34 目标：在任何 P0-06 HTTP/前端生产实现之前，冻结最小公开契约，并把现有高保真页面映射到已批准的 P0 能力。该任务只做决策与批准，不实施业务路由、分页或前端接线。验收标准（原文要点）：

1. 所有安全关键值均已冻结，没有"实现时再决定"；
2. 高保真页面不能反向要求 API 暴露敏感字段或弱化服务端策略；
3. 登录、协作、DML/DDL、导出、行编辑、移动端和浏览器直连仍明确排除；
4. 产出可供四个并发实施任务共同引用的批准结论；
5. 未获批准前，所有子任务不得注册新的公开 HTTP 路由。

### 1.3 权威来源（按优先级）

| 层级 | 来源 | 说明 |
|---|---|---|
| 1 | 用户当前指令 | 本任务说明（WEB-34 / P0-06A 范围边界） |
| 2 | 根目录 `AGENTS.md` | Agent 工作协议 |
| 3 | 已接受 ADR | ADR-001/005/007/008/010/013/014/015/016/017 |
| 4 | `webdb-design-draft.md` | 产品与 P0 边界权威设计稿 |
| 5 | 代码注释 | 实际实现注释 |

冲突时以高优先级来源为准；发现冲突时停止并在任务/PR 中列出，不自行选择高风险方案。

### 1.4 证据口径

本文件所有"当前实现"事实均给出精确文件/行号，指向当前工作区代码；`go -C apps/api` 的包路径以 `apps/api` 为 module 根。凡是计划/目标态，均明确标注"尚未实现"，不把目标态写成已实现态。

---

## 2. 背景与现状证据

### 2.1 逐项现状核对（16 项，代码证据）

| # | 检查项 | 结论 | 证据 |
|---|---|---|---|
| 1 | 公开 HTTP 路由是否仍只有 `/health` | **是**，仅 `/health` | `apps/api/cmd/server/main.go:53-54`（`mux.HandleFunc("/health", healthHandler)`）；`main_test.go` 仅覆盖 `/health` |
| 2 | `metadata.Connection` JSON 序列化暴露哪些字段 | 暴露 `id, workspace_id, name, engine, host, port, database, environment, secret_ref, secret_version, created_by, created_at, updated_at`；其中 `host/port/secret_ref/secret_version/created_by/workspace_id` 不在候选公开字段内 | `apps/api/internal/metadata/models.go:137-151` |
| 3 | `ListConnections` 是否存在；`connections.Service` 是否有授权列表能力 | 仓储有 `ListConnections`（`repo.go:47`），但 `connections.Service` 仅暴露 `Create`/`Update`/`Test`，**无授权列表方法** | `apps/api/internal/metadata/repo.go:44-49`；`apps/api/internal/connections/service.go:144/203/259` |
| 4 | Adapter 元数据能力 | 仅 `PoolHandle.Schemas/Tables/Columns`，无授权、无凭证解析、无服务层编排；Adapter 明确不承担 SQL 安全裁决 | `apps/api/internal/adapter/manager.go:368/384/403`；`adapter/types.go:1-3` |
| 5 | `PrimaryKey/ForeignKey/Index/TableMetadata` 是否有加载入口 | 仅类型定义，**无加载方法引用**（grep 全部命中仅定义处） | `apps/api/internal/adapter/metadata.go:29-62` |
| 6 | `ExecuteRequest` 字段 | `Principal`、`ConnectionID`、`SQL`、`Args []any`、`Engine`；`Engine` 必须服务端派生、`Principal` 服务端注入、`Args` 敏感无类型/大小约束、无 `page_size/order_by/timeout/max_rows` 字段 | `apps/api/internal/execution/pipeline.go:138-144` |
| 7 | Pipeline 是否仍为无 continuation 单页、≤500 行 | **是**：`effectiveMaxRows` 钳制 500，`SortKeys=nil`，`PageSize=MaxRows`，不发 token；Adapter `prepareFirstPageSort` 在 `MaxRows<=PageSize` 时 singlePage | `pipeline.go:407-423`；`adapter/manager.go:479-496` |
| 8 | ADR-014 `VerifiedSortPlan`/`SchemaSnapshot` 是否实现 | **尚未实现**：`internal/queryplan` 为空目录；keyset 仍用 `SortKey.Unique` | `pipeline.go:407` 注释；`adapter/keyset.go:17-50`（`buildSortSpecs` 要求 `hasUnique`） |
| 9 | ADR-015 Service-owned registry / `VerifiedNextPagePlan` 是否实现；token 归属 | **尚未实现**：`ContinuationRegistry` 仍在 Adapter 包；`NextPage` 接收裸 `token string`；失败时 `registry.restore` 恢复旧 token（与 ADR-015"claim 后不恢复"目标冲突） | `adapter/pagination.go:26-41`；`adapter/manager.go:498-559` |
| 10 | 取消是否只有 context 传播 | **是**：仅 `context.WithTimeout`，无 active-execution registry，无显式取消路由；取消后审计用 `WithoutCancel` context 兜底 | `pipeline.go:386`、`pipeline.go:683` |
| 11 | `execution_timeout/execution_cancelled` 与 `query_timeout/query_cancelled` 是否差异 | **存在词汇差异**：Service 层稳定码为 `execution_timeout`/`execution_cancelled`；元数据库 audit metadata 写 `query_timeout`/`query_cancelled`；`connections.Service` 映射 `execution_timeout`；已批准 P0-04 §6.1 词汇为 `query_timeout`/`query_cancelled` | `execution/service.go:31-32`；`pipeline.go:698/703`；`connections/service.go:469` |
| 12 | `mapAdapterError` 是否折叠关键错误为 `internal_error` | **是**：仅映射 6 个码；`database_error`/`result_too_large`/`invalid_page_token`/`pagination_capacity_exhausted`/`stale_config`/`pool_closed`/`connection_failed` 全部落到 `ErrInternalError` | `apps/api/internal/execution/pipeline.go:845-871` |
| 13 | `AppendAudit` 是否回填 event ID；`ExecuteResult` 是否暴露审计确认 | `AppendAudit` 用 `RETURNING id, created_at` 回填 `e.ID`（**是**），但 `ExecuteResult` 无 `audit_event_id/state/outcome`，`recordPostExecution` 丢弃 event.ID | `metadata/postgres_repo.go:527-546`；`execution/pipeline.go:147-155`、`pipeline.go:772` |
| 14 | `NextPage` 再访目标库是否有 Execution/Audit 编排 | **无**：Adapter `NextPage` 内部 `execQuery` 再访目标库，不创建 Execution、不写 AuditEvent；Pipeline 从不调用 NextPage | `adapter/manager.go:498-559`（尤其 `:539`） |
| 15 | Pipeline 是否显式验证 active User | **非显式**：Pipeline 只调 `MemberByWorkspaceAndUser`；active 过滤隐含在仓储 SQL `JOIN users u ... AND u.status='active'`，disabled 用户返回 `sql.ErrNoRows→ErrForbidden` | `pipeline.go:175-187`；`metadata/postgres_repo.go:178-194` |
| 16 | 是否有 session 级只读事务保护 | **没有**：仅 `sqlpolicy/policy.go:11` 注释"建议后续任务增加数据库层只读保护"；SELECT 函数副作用为已接受残余风险（P0-04 §5.3 / P0-05 R5） | `apps/api/internal/sqlpolicy/policy.go:5-11` |

### 2.2 关键架构事实

- **执行生命周期**（P0-04 已实现）：阶段 A 身份授权 → 阶段 B 创建 Execution(pending) → 阶段 C SQL 策略 → 阶段 C' 凭证解析 → 阶段 D-0 running → 阶段 D Adapter；审计按 ADR-017 分阶段 fail-closed。
- **策略与执行边界**：MySQL 先过 WebDB 自有 ECM lexer（ADR-007），再进官方未修改 Omni AST；`sqlpolicy` 只放行单条 `SELECT`/`EXPLAIN`；危险特征（锁定、INTO、赋值、修改 CTE、EXPLAIN DML 等）一律拒绝。
- **token 现状**：Adapter 持有内存 Registry，`genToken()` 为 32 字节 CSPRNG hex；但 key 是原始 handle 而非 SHA-256 digest，且不含策略版本绑定、claim 后不恢复语义（均未达 ADR-015 目标态）。
- **连接池/准入**（ADR-008/016）：准入在 Adapter 内部 `TryAcquire`；`rate_limited`/`connection_busy` 在 Adapter 层产生，Service 终结 Execution + 审计。
- **审计**（ADR-013/017）：append-only，强类型 metadata 允许列表，`RETURNING id` 可取得 event ID；`$SECURITY_ALERT` 独立告警通道。

---

## 3. 目标与非目标

### 目标（P0-06A 只设计以下最小能力）

1. 已授权连接列表
2. 已授权 Schema 浏览（schemas/tables/columns 懒加载）
3. 只读 SQL 执行（第一页）
4. 单向服务端续页（在 ADR-014/015 迁移后）
5. 取消（transport abort，方案比较见 §10）
6. 审计确认（audit receipt）

### 明确排除（非目标）

- 登录实现、OIDC、完整会话系统
- DML、DDL、写操作或审批
- 数据导出
- 查询保存、历史、收藏
- 多人协作、CRDT、WebSocket
- 连接创建、修改、删除或凭证管理 API
- 移动端
- 通用审计查询平台
- 浏览器直连数据库
- SSH 隧道
- 任何生产权限或密钥策略变化
- 更新 `packages/contracts`、引入运行时依赖、创建 migration

### 浏览器不得接收或控制

**浏览器绝不接收**（任何响应均不得出现）：
- 数据库密码、连接串、KEK、DEK、nonce
- `secret_ref`、`secret_version`
- 未脱敏数据库错误、原始审计内部错误
- UserID、ActorID、可信角色（由服务端 Principal 派生，浏览器不自报）
- MySQL lexer/session mode
- `SortKey.Unique` 或任何唯一性证明
- continuation token 的内部状态（客户端仅持 opaque handle）

**浏览器可接收但不得控制**（服务端派生；客户端提交覆盖一律忽略）：
- SQL 方言、`Connection.Engine`、`Environment`——**D02 已批准**连接 DTO 公开 `engine`/`environment`，浏览器可接收用于展示，但不得在请求中提交
- `ConnectionPolicy` 上限（`max_rows`/`timeout` 客户端不可覆盖）
- `trace_id`——**D14 已批准**：audit receipt 可含服务端生成的 `trace_id`，仅用于相关性与支持排查；不接受客户端输入，不作为授权或资源访问凭证；错误响应仍不含 trace_id

---

## 4. 设计原则

1. **服务端权威**：方言、Engine、Environment、Policy 上限、Actor、trace 全部由服务端从可信连接/Principal 派生；客户端提交的任何等价字段一律忽略或拒绝。
2. **任一拒绝即拒绝**：WebDB 成员/角色、连接归属、ConnectionPolicy `AllowRead`、目标数据库原生权限的交集；缺任一层即拒绝（AGENTS.md、设计稿 §5.3）。
3. **fail-closed**：无法可靠解析/判定/证明时拒绝；错误响应为固定安全摘要，不含 SQL/Args/原始错误/凭证。
4. **最小暴露**：连接列表用专用安全 DTO；host/port/secret_ref/secret_version/created_by 默认不公开（D03 决策）。
5. **审计不因 HTTP 丢失**：审计失败扣留结果、返回 `audit_failed`、不自动重放（ADR-017 语义延续到 HTTP 边界）。
6. **分页可证明才放行**：续页路由仅在 ADR-014/015 目标态迁移完成后注册；无法证明确定性唯一排序时在访问目标库前返回 `unsupported_query`。
7. **token 是 opaque handle**：不含 SQL/Args/SortKeys/结果/last values；服务端仅存 digest；单次使用、TTL≤5 分钟、服务重启失效、续页重新授权。
8. **诚实反映现状**：同步执行模型下浏览器拿不到服务端 execution ID，取消契约必须如实说明（§10）。
9. **每个物理目标库访问有审计/执行记录**：首页或续页每次实际访问目标库都创建独立 Execution 并在返回页面前持久化 AuditEvent（D11 已批准，§9.4）。

---

## 5. 统一 HTTP 约定

### 5.1 基础前缀与版本

- 基础前缀：`/api/v1`（**D01a 已批准**）。
- 版本路径直接编码在 URL（`/api/v1/...`），不做内容协商版本；理由：简单、可缓存、可回滚。
- `/health` 保留，不改动（`main.go:54`）。

### 5.2 可信 Principal 来源（**D01b 已批准**）

- P0 无登录（ADR-011 仅本地账号/未来 OIDC，均不在 P0）。Compose 演示环境需要一个可信身份注入方式。
- 候选：
  - A（推荐）：服务端中间件从**部署环境配置**解析固定演示 Principal（workspace_id/user_id/role 由服务端决定），浏览器不带身份；请求级覆盖一律拒绝。
  - B：由可信反向代理注入 `X-*` 头，服务端校验并映射——需要演示层代理，超出 P0 Compose。
  - C：Compose 内置 dev-only 固定 token——仍是未认证的伪凭据，且容易被误用于生产。
- **D01b 已批准（方案 A）**：`Principal{UserID, WorkspaceID}` 从服务端固定演示配置注入，浏览器不得自报 actor/角色/方言/策略；任何来自客户端的 `workspace_id` 覆盖必须被忽略（路由中的 `{workspace_id}` 仅用于与 Principal.WorkspaceID 比对，不一致即 403/404）。
- **D01b fail-closed（补强）**：演示 Principal 配置缺失、格式非法、角色无效或指向不存在的 workspace/user 时**拒绝启动（fatal）**；禁止以零值、默认值或任何客户端提供的身份作为回退。运行时无法从可信配置解析出有效 Principal 时，请求返回 `unauthorized`（401）。

### 5.3 传输约定

- Content-Type：`application/json`（UTF-8）。
- 请求体大小上限：256 KiB（**D06b 已批准**）。
- 方法：GET 用于浏览类、POST 用于执行/续页（执行有副作用，禁止 GET）。
- 统一成功 envelope（结果 DTO 直返，不分层）：

  ```json
  { "data": { ... }, "meta": { "page": {...}, "audit": {...} } }
  ```

  - `data` 恒为结果；`meta` 仅在携带分页（`page`）或审计（`audit`）信息的路由**必需**（executions、query-pages）；无分页/审计的浏览类路由（connections、schemas/tables/columns）`meta` **可选、可省略**（§6 连接列表示例省略 `meta`）。

- 统一错误 envelope（固定安全摘要）：

  ```json
  { "error": { "code": "forbidden", "message": "forbidden" } }
  ```

  错误体**禁止**包含：SQL 正文、Args、token、host/database username、凭证或密钥、原始数据库错误、lexer/parser 原始错误、内部 Go error 字符串、trace_id（错误响应一律不含；trace_id 仅出现在成功响应 audit receipt，D14）。
- HTTP 状态：见 §12 错误码映射表；`429` 携带 `Retry-After`（**D15d 已批准**）。

---

## 6. 已授权连接列表契约

已批准路由：`GET /api/v1/workspaces/{workspace_id}/connections`

| 项 | 定义 |
|---|---|
| 授权条件 | 服务端 Principal 解析 → 成员资格（任意可读角色）→ 目标 workspace 与 Principal 一致。所有可读角色（owner/admin/editor/viewer）可见连接列表；**列表本身不要求 AllowRead**（AllowRead 是执行/浏览级授权，见 §7/§8） |
| 请求字段 | 路径：`workspace_id`（仅与 Principal.WorkspaceID 比对）；无 query/body |
| 禁止字段 | 客户端不得提交 `user_id`、`actor_id`、`role`、`workspace_id` 覆盖、`engine` 过滤（如要按引擎过滤列为 D03 附属决策） |
| 成功状态码 | `200 OK` |
| 成功响应 DTO（安全连接 DTO，**D02/D03 已批准**） | 公开字段：`id`、`name`、`engine`、`environment`、`database`。`host`、`port`、`secret_ref`、`secret_version`、`created_by`、`workspace_id`、`created_at`、`updated_at` **不公开** |
| 空数据语义 | `200` + 空数组 `"data": []`；不返回 404（工作区/连接不可见统一为授权拒绝，见错误码） |
| 错误码 | `invalid_scope`(400)、`unauthorized`(401)、`forbidden`(403)、`internal_error`(500) |
| 超时/取消 | 元数据库查询**有界超时**（默认 5s、上限 10s；配置缺失/非法时 fail-closed 拒绝，禁止无界）；HTTP 取消传播到元数据库查询；取消/超时后连接归还 |
| 硬上限 | 响应体上限 8 MiB（D06b 已批准）；列表大小默认 200 行封顶（D06c 已批准）。**超限行为**：行数超过 200 时返回 `result_too_large`（422），**不静默截断**；元数据列表分页留待后续任务 |
| 是否访问目标数据库 | **否**（仅元数据库） |
| 是否创建 Execution | 否 |
| 是否追加 AuditEvent | **D05b 已批准**：连接列表读取**不**写 AuditEvent；保留脱敏指标与服务端日志。连接/凭证变更 E1-E8 仍照常审计 |
| 审计失败是否返回结果 | 不适用（D05b 已批准：不审计） |
| 敏感信息约束 | 绝不返回 `host/port/secret_ref/secret_version`；不返回明文密码/连接串/KEK |

示例响应：
```json
{
  "data": [
    { "id": "uuid", "name": "prod-pg", "engine": "postgresql", "environment": "production", "database": "app" }
  ]
}
```

---

## 7. 已授权 Schema 契约

已批准路由：
- `GET /api/v1/workspaces/{workspace_id}/connections/{connection_id}/schemas`
- `GET /api/v1/workspaces/{workspace_id}/connections/{connection_id}/tables?schema=<ident>`
- `GET /api/v1/workspaces/{workspace_id}/connections/{connection_id}/columns?schema=<ident>&table=<ident>`

| 项 | 定义 |
|---|---|
| 授权条件（每次请求重新检查） | ① 成员资格（可读角色）；② 连接归属该 workspace（`ConnectionByID`，不存在/跨工作区统一 `connection_not_found`）；③ `ConnectionPolicy.AllowRead == true`。**授权通过后才解析凭证**（阶段 C' 顺序，凭证失败 Adapter/目标库 0 次访问） |
| 请求字段 | 路径 `connection_id`；query `schema`、`table`（可选，依赖路由层级） |
| 标识符处理 | HTTP 边界先校验长度（≤63 字符）与字符白名单；`schema`/`table` 作为 **information_schema 查询的值参数**经驱动参数绑定（`pgTables/pgColumns` 用 `table_schema=$1`/`table_name=$2`，`mysqlTables/mysqlColumns` 用 `?`），**不是标识符拼接**。仅当未来出现将用户输入作为标识符拼接的路径时，才需按方言 `quoteIdent`。补充标识符注入负向测试（CT-16） |
| "授权 Schema"语义 | 仅表示**连接级授权 ∩ 目标数据库原生可见性**的交集；P0 **不新增逐 Schema ACL**（**D04 已批准**）。目标库内存在但用户原生权限不可见的 schema/table 不返回，且不提示其存在 |
| 成功状态码 | `200 OK` |
| 成功响应 DTO | schemas：`[{ "name": "public", "catalog": "db" }]`；tables：`[{ "schema","name","type": "TABLE / VIEW" }]`；columns：`[{ "name","ordinal","native_type","nullable","has_default" }]`（对齐 `adapter` Schema/Table/Column 结构，§15） |
| 空数据语义 | `200` + 空数组；不返回 404 |
| 错误码 | `invalid_scope`、`unauthorized`、`forbidden`、`connection_not_found`、`policy_not_configured`(404)、`read_not_allowed`(403)、`connection_busy`、`database_error`、`internal_error` |
| 超时/取消 | 每次浏览操作**有界超时**：目标库连接获取默认 5s、上限 15s（复用 `connAcquireTimeout` 语义）；配置缺失/非法时 fail-closed 拒绝；HTTP 取消传播到目标库并归还连接 |
| 硬上限 | 每层返回条目上限（schemas/tables/columns 各 ≤1000，D06c）；响应体上限（D06b）。**超限行为**：超过上限返回 `result_too_large`（422），**不静默截断** |
| 是否访问目标数据库 | **是**（`PoolHandle.Schemas/Tables/Columns`）；每次浏览重新获取连接，不做无界缓存 |
| 是否创建 Execution | 否（Schema 浏览不是 SQL 执行；不创建 `executions` 行） |
| 是否追加 AuditEvent | **D05a 已批准**：Schema 树逐节点读取**不**写 AuditEvent；保留脱敏指标与服务端日志。未来若新增显式 Schema 刷新任务，再通过独立事件契约决定审计 |
| 审计失败是否返回结果 | 不适用（D05a 已批准：不审计） |
| 敏感信息约束 | 只返回 schema/table/column 元数据；不返回行数据、凭证、host/port |

> **分页唯一性证明限制**：Schema 浏览当前仅暴露 `Schema/Table/Column`（无主键/唯一约束加载入口，证据 #5）。若后续执行续页需要可信主键/唯一约束证明（ADR-014），在无加载入口前**不得生成分页唯一性证明**；该能力归并发 B（WEB-38）。

---

## 8. 只读执行与第一页契约

已批准路由：`POST /api/v1/workspaces/{workspace_id}/executions`

### 8.1 请求

```json
{
  "connection_id": "uuid",
  "sql": "SELECT * FROM t ORDER BY id LIMIT 100",
  "page_size": 100,
  "order_by": [ { "column": "id", "order": "ASC", "nulls_last": false } ]
}
```

| 字段 | 公开？ | 说明 |
|---|---|---|
| `connection_id` | 是 | 必填，UUID；不存在/跨工作区统一 `connection_not_found` |
| `sql` | 是 | 必填；服务端单语句/只读/AST 校验；P0-06 不公开 `args`，**请求 SQL 不得包含未绑定参数占位符（`$N`/`?`/具名参数）**，**禁止客户端自行内联用户输入**；原始 SQL 不进入日志/错误/审计 |
| `page_size` | 是 | 可选；0 用默认；**服务端钳制** ≤ 500 且 ≤ effectiveMaxRows（客户端不可提高上限） |
| `order_by` | 是（意图字段） | 可选；**只是请求意图，不是唯一性证明**；唯一性只由 `VerifySortPlan` 产生（ADR-014） |
| `args` | **D07 已批准**：不公开 | P0-06 **不公开 `args`**（避免冻结参数类型/深度/字节限制）。含 `$N`/`?`/具名参数占位符的 SQL 无绑定值来源，**服务端一律拒绝**（`sql_parse_error`/`statement_not_allowed`），不允许客户端内联值绕过参数化边界 |
| `engine` | 否 | 服务端从 `Connection.Engine` 派生（`pipeline.go:196-200`） |
| `workspace_id`/`user`/`actor`/`trace` | 否 | 服务端 Principal/路由派生；客户端提交覆盖一律忽略 |
| `policy max_rows`/`timeout` | 否 | 服务端策略上限；客户端 `page_size` 只可缩小不可放大 |
| `mysql mode` | 否 | 服务端可信连接配置（`sqlpolicy.MySQLLexerMode`） |
| `unique=true` | 否 | 客户端不得提交 `SortKey.Unique` 或任何唯一性证明（ADR-014） |

### 8.2 响应（第一页 + 审计 receipt）

成功：`200 OK`。

```json
{
  "data": {
    "columns": [ { "name": "id", "wire_type": "int" }, { "name": "name", "wire_type": "text" } ],
    "rows": [ ["1", "a"], ["2", "b"] ],
    "returned_rows": 2,
    "total_returned": 2
  },
  "meta": {
    "page": { "page_size": 100, "has_more": false },
    "audit": { "state": "recorded", "audit_event_id": "uuid", "execution_id": "uuid", "trace_id": "server-generated", "outcome": "succeeded" }
  }
}
```

- `next_page_token` 只在"需要分页 且 VerifiedSortPlan 有效 且确有后续页"时出现；单页受限请求（effectiveMaxRows ≤ page_size）不得发 token（ADR-014）。上例 `has_more=false` 故**不含 token**。
- `columns[].wire_type` 为**必需**字段（D08，决定前端解码）；数据库原生 `data_type`（如 `int4`）为可选辅助字段。整数/decimal 结果以十进制字符串返回（如 `"1"`）。
- `meta.audit` 为**审计 receipt**（§11，**D14 已批准**）：`state`、`audit_event_id`、`execution_id`、服务端生成的 `trace_id`、`outcome`。

### 8.3 语义

| 项 | 定义 |
|---|---|
| 是否访问目标数据库 | 是（阶段 D） |
| 是否创建 Execution | 是（阶段 B，pending→…→终态） |
| 是否追加 AuditEvent | 是（E9-E13） |
| 审计失败是否返回结果 | **否**：`audit_failed`，扣留结果（ADR-017，`pipeline.go:466`） |
| 拒绝路径 | 策略拒绝/凭证失败/上限无效均不访问目标库，`Adapter.Query=0` |
| 分页前置 | `requiresPagination` 时无有效 VerifiedSortPlan → `unsupported_query`（422），目标库 0 访问 |
| 超时 | 服务端 `StatementTimeoutMs` 生效；响应超时见 §12 |
| 取消 | transport abort（D13 已批准）：HTTP 断开取消数据库查询；Execution/审计在独立有界 context 终结；浏览器用本地取消状态 |
| 结果脱敏边界 | P0-06 **不提供结果列值脱敏**；控制 = 连接级授权 ∩ 目标库原生可见性 ∩ 最小权限账号（ADR-001/005/007，§14）。演示库账号不得有读取密钥承载表权限 |

---

## 9. 单向续页契约

已批准路由：`POST /api/v1/workspaces/{workspace_id}/query-pages`

### 9.1 注册前提（硬性）

**在 ADR-014/015 目标态迁移完成之前，不得注册续页 HTTP 路由。** 该路由属并发 B（WEB-38）交付前置；WEB-35 只能先在 DTO/错误码层面预留，不得提前注册可被浏览器调用的续页 handler。

### 9.2 请求

```json
{
  "next_page_token": "opaque-64-hex"
}
```

- 客户端只提交上一步返回的 opaque token；**不得**重新提交 SQL/Args/order_by/unique。
- token 满足（ADR-015 + P0-06A）：
  - ≥32 字节 CSPRNG opaque handle（当前 `genToken` 已是 32B hex，但需改为服务端 Registry 持有 digest）；
  - 服务端仅存 token digest（SHA-256），不存原始 handle；
  - token **不含** SQL、Args、SortKeys、结果、last values；
  - TTL ≤ 5 分钟（绝对过期）；
  - 服务重启后 token 失效 → `invalid_page_token`；
  - 每次续页重新授权（成员/连接/策略/generation 再校验）；
  - 单次使用；claim 后失败、取消、超时、panic **不恢复**旧 token；
  - 成功时原子 `Rotate`（旧 digest→新 digest 同一容量槽位）；
  - 只支持 next；不支持 previous/offset/随机跳页；
  - token 不进入日志、审计正文、trace attribute、指标 label。

### 9.3 响应

与 §8.2 相同的分页 DTO；`next_page_token` 在无后续页时省略。

### 9.4 每页语义（**D11 已批准**）

- **D11 已批准（模型 A）**：每次会实际访问目标数据库的首页或续页请求创建**独立 Execution**，并在返回页面前**持久化对应 AuditEvent**。Continuation token 负责绑定分页链路和累计上限，**不代替** Execution/Audit 事实。
- 每页续页都走 `NextPage` 服务编排（重新授权 → claim → 执行 → 返回页面前持久化审计 receipt），当前 Pipeline 无此编排（证据 #14），须由 WEB-38 交付。

### 9.5 当前与目标态差异（明确，不写成已实现）

| 维度 | 当前实现 | ADR-014/015 目标态 |
|---|---|---|
| Registry 归属 | Adapter 持有（`pagination.go:26`） | Service-owned |
| token key | 原始 handle | SHA-256 digest |
| NextPage 输入 | 裸 `token string` | `VerifiedNextPagePlan` |
| claim 语义 | 失败 `restore` 恢复旧 token | claim 后不可恢复 |
| 策略/generation 绑定 | 不完整 | 完整绑定 |
| 唯一排序证明 | `SortKey.Unique` | `VerifiedSortPlan`（`queryplan` 包） |

---

## 10. 取消契约

### 10.1 候选方案比较

| 方案 | 机制 | 优点 | 缺点/局限 |
|---|---|---|---|
| A：transport abort | 浏览器 `AbortController` 中断请求 → HTTP request context 取消 → 数据库查询取消 + 连接/并发 permit 归还 | 零额外 API、与现有 `context.WithTimeout` 模型一致、无需 execution ID | 同步执行响应完成前浏览器拿不到服务端 execution ID；中断后浏览器通常**收不到** `query_cancelled` 响应；服务端必须自行终结 Execution 并写 E13 |
| B：显式 cancel-by-execution-ID | 先拿 execution ID，再调取消接口 | 可精确取消未完成请求 | P0 同步执行下 execution ID 只在响应返回后可知，**无法在响应前取消**；需要新取消路由 + 状态查询；浏览器需知道 ID |
| C：异步执行 / active-request registry | 服务端维护有界 active execution registry，异步返回 execution ID + 显式取消/轮询 | 支持响应前取消、可展示进度 | 复杂度最高；改变同步模型；registry 本身需有界与安全审计；超 P0 最小范围 |

### 10.2 诚实结论与建议

- 当前是同步执行模型，响应完成前浏览器无法取得服务端 execution ID。因此**不能只加一个表面上的 cancel 路由**就宣称支持取消。
- **D13 已批准：P0 取消仅采用方案 A（transport abort）**。请求 context 必须传播至目标数据库；服务端仍必须（1）终结 Execution 为 `cancelled`，（2）在独立有界 context 下写 E13 `sql.execute cancelled` 审计（`pipeline.go:693-698` 已有 cancelled 路径，且用 `WithoutCancel` context 保证审计不被客户端取消阻断），（3）归还连接与并发 permit（`defer permit.Release` / `defer handle.Release()` 已有）。
- 浏览器在 abort 后通常收不到 `query_cancelled` 响应体，**UI 应使用本地取消状态**（`cancelled` 本地标记 + 服务端轮询/状态接口留待后续），不能依赖响应体来切换 UI 状态。
- 已 claim 的 pagination token 不恢复（ADR-015）。
- **D13 已批准排除**：P0 不提供 cancel-by-execution-ID；显式跨请求取消需先引入异步执行或有界 active-request registry。

### 10.3 服务端取消义务清单（D13 已批准）

1. 数据库查询 context 取消 → 驱动取消；
2. 连接归还（pool handle Release）、并发 permit Release；
3. Execution 终态 `cancelled`（不遗留 pending/running）；
4. E13 审计写入（`query_cancelled`）；
5. 若为续页：旧 token 不恢复（`Abort`）；
6. 审计写入用 `WithoutCancel` context + 独立超时，客户端取消不吞审计。

---

## 11. 审计确认契约

### 11.1 现状核实

- `PGStore.AppendAudit` 用 `RETURNING id, created_at` 回填 `e.ID`（`postgres_repo.go:540-545`）——**event ID 在持久化后确实可获得**。
- 但当前 `ExecuteResult` 只含 `Decision/CredentialResolved/AdapterCalled/Result/ErrorCode/TraceID/ExecutionID`，**不暴露 audit receipt**；`recordPostExecution` 调 `AppendAudit` 后丢弃 `event.ID`（`pipeline.go:772`）。

### 11.2 审计 receipt（**D14 已批准字段集**）

成功响应 `meta.audit`：

```json
{
  "state": "recorded",
  "audit_event_id": "uuid",
  "execution_id": "uuid",
  "trace_id": "server-generated",
  "outcome": "succeeded"
}
```

语义约束：
- `state=recorded` **只能在 AuditEvent 已持久化后返回**；执行后审计失败返回 `audit_failed` 且**不返回查询结果**（ADR-017，`pipeline.go:466`）。
- `state=denied/failed/cancelled` 对应拒绝/失败/取消终态。
- 不自动重放 SQL；失败由客户端决定是否重试（服务端不承诺幂等，SELECT 副作用风险 R5）。
- SQL、Args、结果、原始错误**不得进入审计正文**（ADR-017 禁止字段）。
- **D14 已批准**：receipt 返回 `state`、`audit_event_id`、`execution_id`、服务端生成的 `trace_id` 和 `outcome`。trace_id 仅用于相关性与支持排查，**不接受客户端输入**，也**不作为授权或资源访问凭证**。
- P0-06A **不开放通用审计搜索 API**；审计仅追加写、无普通业务查询接口（ADR-013/017 不变）。

---

## 12. 稳定错误码与 HTTP 状态映射

### 12.1 完整候选错误码表

下表覆盖任务要求的全部候选码。**HTTP 状态映射已随本 Owner Gate（2026-08-08）批准**：业务错误码本身由 P0-04 冻结；状态列即批准值；D15 系行标注对应专项决定。

| 错误码 | 含义 | 阶段 | 批准 HTTP | Owner 决策 |
|---|---|---|---|---|
| `invalid_request` | 请求体/JSON/字段非法 | 边界 | 400 | 已批准 |
| `invalid_scope` | 作用域无效（UUID 非法等） | A | 400 | 已批准 |
| `unauthorized` | 未认证/无有效 Principal | A | 401 | 已批准 |
| `forbidden` | 非成员/角色不足 | A | 403 | 已批准 |
| `connection_not_found` | 连接不存在/跨工作区/不可见（防枚举） | A | 404 | 已批准 |
| `policy_not_configured` | 策略缺失或关键安全上限无效 | A | 404 | 已批准（D15a：404 防枚举） |
| `read_not_allowed` | 存在策略但 AllowRead≠true | A | 403 | 已批准 |
| `invalid_page_token` | token 无效/过期/重放/scope 不匹配 | D | 400 | 已批准 |
| `empty_sql` | 空 SQL | C | 422 | 已批准 |
| `sql_parse_error` | lexer/parser 无法可靠判定 | C | 422 | 已批准 |
| `multiple_statements` | 检测到多语句 | C | 422 | 已批准 |
| `statement_not_allowed` | 语句类型不允许 | C | 422 | 已批准 |
| `executable_comment_detected` | MySQL ECM 检测 | C | 422 | 已批准（D15c：独立公开码） |
| `unsupported_statement` | 解析器无法识别 AST 节点 | C | 422 | 已批准 |
| `unsupported_query` | 需要分页但无唯一排序证明 | C | 422 | 已批准 |
| `query_timeout` | 查询超时 | D | 504 | 已批准（D15f：公共 API 仅 query_timeout） |
| `query_cancelled` | 查询已取消 | D | 499 | 已批准（D15b：499） |
| `rate_limited` | 速率限制 | D | 429 + Retry-After | 已批准（D15d：429 + Retry-After） |
| `connection_busy` | 连接忙 | D | 429 + Retry-After | 已批准（D15d：429 + Retry-After） |
| `result_too_large` | 结果超出限制 | D | 422 | 已批准 |
| `pagination_capacity_exhausted` | 分页容量耗尽 | D | 429 | 已批准（D15d：429 + Retry-After） |
| `connection_unavailable` | 凭证/KEK/pool/config 内部故障折叠 | C' | 503 | 已批准（D15a：新增 503） |
| `database_error` | 数据库错误（脱敏） | D | 500 | 已批准 |
| `audit_failed` | 审计写入失败（fail-closed） | 任意 | 500 | 已批准（P0-04/017） |
| `internal_error` | 未预期错误 | 任意 | 500 | 已批准 |

### 12.2 Owner 错误码决策（Q1-Q6，已全部批准）

> Owner Gate（2026-08-08）已批准 D15a–D15e（按本表与推荐）；D15f 见第 6 条专项决定。

1. **是否新增公共 `connection_unavailable`**（**D15a 已批准**）：将凭证解析、KEK、pool/config 等内部故障折叠为安全摘要（503），避免暴露 `decryption_failed`/`unknown_kek_version` 等内部码给浏览器。已批准：新增 `connection_unavailable`（503）作为外部摘要；`decryption_failed`/`unknown_kek_version`/`credential_not_found`/`credential_retired` 保留在服务端/审计内部，不直接出现在浏览器响应。
2. **`query_cancelled` 是否映射非标准 499**（**D15b 已批准**）：499 是 nginx 惯例，标准 HTTP 无此码；替代为 400/409/499。已批准：**499**，并同步在 `packages/contracts` 与前端处理。
3. **`rate_limited`/`connection_busy` 是否均为 429 + Retry-After**（**D15d 已批准**）：已批准是；`pagination_capacity_exhausted` 同样 429 + Retry-After。
4. **`policy_not_configured` 是否 404**（**D15a 已批准**）：沿用 404 防策略枚举，保持与 `connection_not_found` 一致。
5. **`executable_comment_detected` 独立公开码还是折叠**（**D15c 已批准**）：当前 sqlpolicy 已有独立稳定原因码 `executable_comment_detected`（`sqlpolicy/types.go:81`），审计 metadata 允许列表含 error_code/reason_code。已批准：**保留独立公开码**（利于前端/日志区分），并进入审计允许列表。
6. **D15f 已批准**：公共 API 只使用 `query_timeout` 和 `query_cancelled`；后续实现同步规范新的内部 `Execution.error_code`；已有 `execution_timeout`/`execution_cancelled` 历史记录保持兼容读取，P0 不执行破坏性历史重写。

### 12.3 错误响应安全约束

统一错误响应为**固定安全摘要**，禁止包含：SQL 正文、Args、token、host/database username、凭证或密钥、原始数据库错误（`pq:`/`MySQL Error`）、lexer/parser 原始错误、内部 Go error 字符串、trace_id。服务端可在结构化日志保留脱敏根因。

---

## 13. 结果单元格 wire format

### 13.1 wire 类型（**D08 已批准**）

- 行 = `[][]any`；单元格为 JSON 可表示值。
- **D08 已批准（Owner Gate 2026-08-08）**：公共结果不得直接暴露 `[][]any`；按下列 wire 契约编码（对齐当前 `adapter.ColumnInfo`/`QueryResult`，`types.go:66-78`）：

| 类型 | wire 表示 |
|---|---|
| SQL NULL | JSON `null` |
| 整数（int）及任意精度 decimal | **十进制字符串**（`"123"`/`"123.45"`，防精度丢失） |
| boolean | JSON boolean（`true`/`false`） |
| 有限浮点（float） | JSON number |
| date | `YYYY-MM-DD`（保留原语义，**不 UTC 归一化**） |
| time / time(p) | `HH:MM:SS[.ffffff]`（保留原语义，**不 UTC 归一化**） |
| timestamp / timestamp(p) without time zone | `YYYY-MM-DDTHH:MM:SS[.ffffff]`（无时区后缀，**不转换**） |
| timestamptz / timestamp(p) with time zone | ISO-8601 UTC（`RFC3339`） |
| binary（bytea/blob） | Base64 字符串 |
| JSON/JSONB | 有界 `json_text`（透传 JSON，实施带字节上限） |
| 其他文本/枚举/UUID/网络类型 | 字符串（数据库驱动文本表示） |
| 超大值（> MaxCellBytes） | **拒绝**：返回 `result_too_large`（422），**不静默截断、不提供截断标记路径**；`MaxCellBytes` 默认 256 KiB（`types.go:127`） |
| 列元数据 | 每列携带稳定 `wire_type`（决定前端解码方式） |

- 空数据语义：空数组 `[]` 或空对象，不返回 null 的 data。

### 13.2 上限（**D06 已批准**）

| 上限 | 批准默认 | Owner |
|---|---|---|
| 请求体字节上限 | 256 KiB | 已批准（D06b） |
| SQL 字节上限 | 64 KiB | 已批准（D06b） |
| `args` | 不公开（D07 已批准） | 已批准（D07） |
| 单单元格字节上限 | 256 KiB（沿用 MaxCellBytes） | 已批准（D06a） |
| 单行字节上限 | 1 MiB（含单元格加总） | 已批准（D06a） |
| 单页行数 | 默认 100、最大 500 | 已批准（D06a） |
| 页面响应字节上限 | 8 MiB | 已批准（D06a） |
| 分页 token 上限 | 单 token 状态上限（SQL/Args/last values 深拷贝字节配额，ADR-015 残余风险建议的字节级配额） | 已批准（D12） |

---

## 14. 安全与信任边界

- **浏览器不可信输入**：`workspace_id`、`connection_id`、`sql`、`order_by`、`page_size`、token 均为不可信输入，在服务端完成验证、规范化、参数绑定。
- **可信边界内部**：方言/Engine/Environment/Policy 上限/Actor/trace 全部服务端派生；凭证在授权+策略通过后才解析（阶段 C'），失败时 Adapter/目标库 0 次访问。
- **防枚举**：`connection_not_found`/`policy_not_configured` 统一 404，不区分不存在/跨工作区/不可见；Schema 层不提示目标库内不可见对象的存在。
- **防重放**：pagination token 单次使用 + digest 存储 + 原子 claim/Rotate；claim 后不恢复。
- **防注入**：schema/table 作为 information_schema 查询的**值参数**绑定（非标识符拼接）；SQL 单语句 AST fail-closed；MySQL ECM lexer 前置。
- **审计完整**：append-only；审计失败扣留结果；`$SECURITY_ALERT` 告警；D11 原子提交延续。
- **资源有界**：连接池上限（ADR-008）、准入（ADR-016）、分页容量（ADR-015）、无无界队列/缓存；列表/浏览超限 `result_too_large` 不静默截断（§6/§7）。
- **查询结果脱敏边界**：P0-06 **不提供结果列值脱敏**。安全控制 = 连接级授权 ∩ 目标库原生可见性 ∩ 最小权限账号（ADR-001/005/007）：演示/测试库账号不得授予读取凭据/KEK/密钥承载表或危险函数（SECURITY DEFINER）的权限。服务端结果列脱敏不在 P0-06 范围，如需须新 ADR 并经 Owner 批准。响应 canary 只断言不含 WebDB 自身凭据/KEK/连接串（CT-11 扩展覆盖 API 响应）。
- **残余风险（接受后继续有效）**：SELECT 函数副作用无 session 级只读事务保护（R5）；Go 无法保证内存清零（R1）；服务重启 token 失效（R7）。

---

## 15. 与现有 Go 类型/API 的差异矩阵

| 契约元素 | 现有 Go 类型/API | 差异 |
|---|---|---|
| 连接安全 DTO | `metadata.Connection`（含 host/port/secret_ref/secret_version/created_by） | 需新增专用 DTO，不直接序列化 `Connection` |
| 连接列表 | 仓储 `ListConnections(wsID)`（`repo.go:47`）；Service 无列表方法 | 需 Service 层新增授权列表编排（WEB-36） |
| Schema 浏览 | `PoolHandle.Schemas/Tables/Columns`（`manager.go:368/384/403`） | 需 Service 编排：授权→凭证→Adapter 元数据浏览（WEB-36） |
| 主键/唯一约束 | `PrimaryKey/ForeignKey/Index/TableMetadata` 无加载入口（`metadata.go:29-62`） | 需新增可信 SchemaSnapshot 加载入口（WEB-38，ADR-014） |
| 执行请求 DTO | `execution.ExecuteRequest{Principal,ConnectionID,SQL,Args,Engine}` | 公共 DTO 剥离 Engine/Principal，约束 Args（D07） |
| 第一页执行 | `Pipeline.Execute`（`pipeline.go:161`） | 可复用；新增 HTTP handler + audit receipt 透传 |
| 续页 | Adapter `PoolHandle.NextPage(scope, token)`（`manager.go:498`） | 需 Service-owned registry + `VerifiedNextPagePlan`（WEB-38） |
| 排序证明 | `SortKey.Unique`（`adapter/keyset.go`） | 废弃 → `VerifiedSortPlan`（ADR-014） |
| token | Adapter `ContinuationRegistry`（`pagination.go`） | 迁移到 Service；key 改 digest（ADR-015） |
| 错误码 | `execution.StableErrorCode`（`service.go:21-38`） | 归一化 `query_timeout`/`query_cancelled`（D15f 已批准）；新增 `connection_unavailable`（D15a 已批准） |
| 错误映射 | `mapAdapterError`（`pipeline.go:845-871`） | 需修复 `database_error`/`result_too_large`/`invalid_page_token`/`pagination_capacity_exhausted` 折叠为 internal_error |
| 审计 receipt | `ExecuteResult`（`pipeline.go:147-155`） | 新增 `audit_event_id`/`state`/`trace_id` 字段（D14 已批准） |
| 审计回填 | `AppendAudit RETURNING id`（`postgres_repo.go:540-545`） | 已具备；需把 event.ID 透传到响应 |
| 取消 | 仅 context（`pipeline.go:386`） | 保持 transport abort（D13 已批准）；无 cancel-by-ID |

---

## 16. ADR-014/015 等前置依赖

- **ADR-014（SortKey 唯一性证明）**：`queryplan.VerifiedSortPlan`/`VerifySortPlan`/`SchemaSnapshot` 未实现（证据 #8）。**前置要求**：`effectiveMaxRows > page_size` 的请求执行前必须取得有效 VerifiedSortPlan；无法证明唯一排序 → 目标库访问前 `unsupported_query`。WEB-38 交付。
- **ADR-015（token 归属）**：Service-owned registry、digest key、`VerifiedNextPagePlan`、claim 后不恢复、原子 Rotate 未实现（证据 #9）。**前置要求**：续页 HTTP 路由只能在迁移完成后注册。WEB-38 交付。
- **ADR-016（准入）**：已实现；HTTP 边界复用 `rate_limited`/`connection_busy` 终结逻辑。
- **ADR-017（审计失败）**：已实现；HTTP 边界延续 fail-closed、`audit_failed`、扣留结果。
- **ADR-013（Schema/审计基线）**：已实现；连接安全 DTO 与审计允许列表以 ADR-013 表结构为准。
- **ADR-001/005/007/008/010**：已实现/已接受，HTTP 边界不改变其安全边界；若契约要求修改任一 ADR，必须新 ADR 或修订，不得静默变更。
- **WEB-10（查询结果类型规范化）**：结果单元格 wire 类型（§13）与 WEB-10 相关；本文件 §13 已批准 wire 契约，最终以 WEB-10 交付为准（D08 已批准）。

---

## 17. Owner 决策清单

所有决策已由 Owner（fujiabao89）于 **2026-08-08** 逐项批准。批准人：fujiabao89；日期：2026-08-08。下表"Owner 决定"为批准结论；每个决策含：决策问题、推荐选项、备选项、权衡、下游影响。

| ID | 决策问题 | 推荐选项 | 备选项 | 权衡 | 下游影响 | Owner 决定 |
|---|---|---|---|---|---|---|
| D01a | API 版本与路由 | `/api/v1` + 6 条候选路由 | 其他前缀/扁平路由 | 可回滚、简单 vs 未来兼容 | WEB-35/36/37/38 全部 | **已批准**：`/api/v1` + 6 路由 |
| D01b | Compose 可信 Principal 来源 | 服务端固定演示 Principal（环境注入，浏览器不自报） | 反向代理头；dev token | 简单 vs 真实性/误用于生产风险 | 所有路由的授权起点 | **已批准**：方案 A（服务端固定演示 Principal） |
| D02 | 连接安全 DTO 字段 | 仅 `id,name,engine,environment,database` | 含 host/port/created_at | 最小暴露 vs 前端便利 | WEB-36 | **已批准**：仅 `id,name,engine,environment,database` |
| D03 | `host/port/secret_ref/secret_version/created_by` 是否公开 | 全部不公开 | 按角色公开 host/port | 枚举/敏感面 vs 展示 | WEB-36 | **已批准**：全部不公开 |
| D04 | Schema 授权粒度 | 连接级 AllowRead ∩ 目标库可见性；无逐 Schema ACL | 逐 Schema ACL | 简单 vs 细粒度 | WEB-36 | **已批准**：连接级 ∩ 目标库可见性；无逐 Schema ACL |
| D05a | Schema 浏览是否写审计 | 写审计（浏览外发目标库连接） | 不写审计 | 审计噪声 vs 追溯 | WEB-36 | **已批准（专项）**：P0 不对连接列表或 Schema 树逐节点读取写 AuditEvent；保留脱敏指标与服务端日志；未来显式 Schema 刷新任务另定独立事件契约 |
| D05b | 连接列表读取是否写审计 | 不写 | 写 | 同上 | WEB-36 | **已批准（专项，随 D05a）**：不写 AuditEvent；保留脱敏指标与服务端日志 |
| D06a | page size 默认/上限 | 默认 100、最大 500（≤ policy.MaxRows） | 默认 200/最大 1000 | 内存 vs 交互 | WEB-35 | **已批准**：默认 100、最大 500 |
| D06b | 请求/SQL/响应字节上限 | 请求 256 KiB、SQL 64 KiB、页响应 8 MiB | 其他 | 防滥用 vs 大结果 | WEB-35 | **已批准**：256 KiB / 64 KiB / 8 MiB |
| D06c | cell/row/页容量上限 | cell 256 KiB、row 1 MiB、页行 500 | 其他 | 同上 | WEB-35 | **已批准**：cell 256 KiB、row 1 MiB、页行 500 |
| D07 | `args` 是否进入 P0 | **不公开**（P0-06 冻结参数复杂度） | 公开并冻结类型/深度/字节 | 契约冻结成本 vs 功能 | WEB-35 | **已批准**：P0 不公开 `args` |
| D08 | 结果 wire 类型 | 表驱动：number/ISO-8601/Base64/透传 JSON（§13） | 全部字符串化 | 精度 vs 前端类型负担 | WEB-35、WEB-10 | **已批准（专项）**：公共结果不直接暴露 `[][]any`；整数及 decimal 用十进制字符串、boolean 用 JSON boolean、有限浮点用 JSON number、时间用规范字符串、binary 用 Base64、JSON 用有界 json_text、SQL NULL 用 JSON null；列元数据携带稳定 wire_type |
| D09 | VerifiedSortPlan 前置 | 需要分页即强制 VerifiedSortPlan，否则 `unsupported_query` | 单页硬限制（无续页） | 功能完整 vs 前置依赖 | WEB-35/38 | **已批准**：需要分页即强制 VerifiedSortPlan |
| D10 | token 生命周期与容量 | 5 分钟 TTL、digest、单次、容量（global 10000/user 100/ws 500/conn 200）+ 字节配额 | 其他 | 内存 vs 可用性 | WEB-38 | **已批准**：5 分钟 TTL、digest、单次、容量 + 字节配额 |
| D11 | 分页审计模型 | 每个物理页独立 Execution/Audit | 一个逻辑 Execution 贯穿 | 可审计粒度 vs 聚合度 | WEB-35/38 | **已批准（专项）**：每次实际访问目标库的首页或续页创建独立 Execution，并在返回页面前持久化对应 AuditEvent；token 绑定分页链路与累计上限，不代替 Execution/Audit 事实 |
| D12 | 续页容量字节配额（ADR-015 残余风险） | 引入单状态 + 各级字节上限 | 维持数量上限 | 内存安全 vs 实现成本 | WEB-38 | **已批准**：引入单状态 + 各级字节上限 |
| D13 | 取消模型 | transport abort（方案 A） | 显式 cancel（B/C） | 简单/诚实 vs 响应前取消能力 | WEB-35 | **已批准（专项）**：P0 取消仅用 HTTP transport abort；请求 context 传播至目标库，并在独立有界 context 下终结 Execution 与审计；P0 不提供 cancel-by-execution-ID；显式取消需先引入异步执行或有界 active-request registry |
| D14 | audit receipt 字段 | `state,audit_event_id,execution_id,outcome`；**不含 trace_id** | 含 trace_id | 浏览器 trace 约束 vs 可追溯 | WEB-35 | **已批准（专项）**：receipt 返回 `state`、`audit_event_id`、`execution_id`、服务端生成的 `trace_id`、`outcome`；trace_id 仅用于相关性与支持排查，不接受客户端输入，不作为授权或资源访问凭证 |
| D15a | 公共错误码折叠 | 新增 `connection_unavailable`(503)；内部码不外泄；`policy_not_configured`=404 | 不新增，全部 internal_error | 防信息泄露 vs 诊断性 | WEB-35 | **已批准**：新增 `connection_unavailable`(503)；内部码不外泄；`policy_not_configured`=404 |
| D15b | `query_cancelled` → 499 | 使用 499 | 400/409 | 非标准码 vs 语义明确 | WEB-35 | **已批准**：499 |
| D15c | `executable_comment_detected` | 独立公开码 | 折叠为 `statement_not_allowed` | 诊断 vs 码表简洁 | WEB-35 | **已批准**：独立公开码 |
| D15d | `rate_limited`/`connection_busy`/`pagination_capacity_exhausted` | 均 429 + Retry-After | 仅 429 无头 | 客户端重试语义 | WEB-35 | **已批准**：均 429 + Retry-After |
| D15e | `result_too_large`/`pagination_capacity_exhausted` 区分 | 422 vs 429 | 合并 | 语义准确 | WEB-35 | **已批准**：`result_too_large`=422、`pagination_capacity_exhausted`=429 |
| D15f | `execution_timeout/execution_cancelled` 归一化 | HTTP 边界统一 `query_timeout/query_cancelled`；内部 error_code 是否一并改名 | 维持现状 | 词汇一致性 vs 存量数据 | WEB-35 | **已批准（专项）**：公共 API 只用 `query_timeout`/`query_cancelled`；后续实现同步规范新的内部 `Execution.error_code`；已有 `execution_timeout`/`execution_cancelled` 历史记录保持兼容读取，P0 不破坏性历史重写 |
| D16 | E2E 验收范围 | 双引擎 Compose 主流程（连接→浏览→查询→分页→审计）+ 越权/敏感/分页/取消/审计失败回归 | 仅单元/集成 | 成本 vs 覆盖 | WEB-39 | **已批准**：双引擎主流程 + 回归 |
| D17 | 高保真页面非 P0 元素处理 | 隐藏/禁用并明确标记，Mock 用合成数据 | 移除 | 视觉一致性 vs 误导 | WEB-37 | **已批准**：隐藏/禁用 + 合成 Mock |
| D18 | 既有 ADR 适用性 | ADR-001/005/007/008/010/013/014/015/016/017 原样适用；如修订须新 ADR | 修订个别 ADR | 稳定性 vs 演进 | 全部 | **已批准**：原样适用；修订须新 ADR |

---

## 18. 验收标准矩阵

逐项对应 `docs/tasks/P0-06-minimal-web-workbench.md` 验收标准，标记：提案已覆盖（含 Owner 决策状态）/ 待后续实现验证 / 当前证据不足。

| P0-06 验收项 | 本提案覆盖 | 状态 |
|---|---|---|
| UI 只经 API 显示已授权连接/Schema，绝不暴露连接密钥 | §6/§7 连接安全 DTO + 授权条件；`host/port/secret_ref/secret_version` 默认不公开 | 提案已覆盖（D02/D03 已批准） |
| 编辑器运行只读 SQL；策略拒绝、超时、取消和 429 可理解呈现 | §8/§10/§12 错误码、499、429+Retry-After、本地取消状态 | 提案已覆盖（D15 系已批准） |
| 结果使用服务端分页，单页默认最多 500 行，不全量加载 | §9 续页契约 + §13 页上限默认 100/最大 500；续页路由 ADR-014/015 前置 | 提案已覆盖（D06a/D11 已批准）；**注册前置未达，WEB-38 待实现** |
| P0 主流程可从 Compose 演示库完成：连接→浏览→查询→分页→审计 | §5.2 可信 Principal + §6-§11 全链路 | 已批准（D01b）+ 待后续实现验证（WEB-35/36/37/38/39） |
| 高保真页面不能反向要求 API 暴露敏感字段 | §3 浏览器不得接收清单 + D17 | 提案已覆盖（D17 已批准） |
| 登录/协作/DML/DDL/导出/行编辑/移动端/浏览器直连仍排除 | §3 非目标 | 提案已覆盖 |
| 取消、超时、审计失败语义冻结 | §10/§11/§12 | 提案已覆盖（D13/D14 已批准） |
| 每页续页重新授权与唯一排序证明 | §9.4/§9.5、ADR-014/015 | 待后续实现验证（WEB-38）；当前证据不足（queryplan 空、registry 未迁移） |
| 审计失败扣留结果、`audit_failed` | §11.2 | 提案已覆盖（ADR-017 已实现；receipt 透传待实现） |

---

## 19. 测试计划（后续实施必须具备）

本任务不实施 API；以下为 WEB-35/36/37/38/39 必须覆盖的契约测试与 E2E 矩阵（非当前验收）。

### 19.1 契约/安全测试

| ID | 场景 | 预期 |
|---|---|---|
| CT-01 | 连接列表不泄露敏感字段 | `host/port/secret_ref/secret_version/created_by` 不在响应；canary 检测 |
| CT-02 | 跨工作区/不可见连接防枚举 | 统一 `connection_not_found`(404)，响应不可区分 |
| CT-03 | Schema 每层重新授权 | 成员/连接归属/AllowRead 逐请求校验；授权后凭据才解析 |
| CT-04 | DML/DDL、多语句、MySQL ECM 拒绝 | 422/`statement_not_allowed`/`executable_comment_detected`；Adapter 0 次 |
| CT-05 | 无法证明唯一排序时 Adapter/目标库调用 0 次 | `unsupported_query`(422)，无目标库访问 |
| CT-06 | page_size/max_rows 不可被客户端提高 | 服务端钳制 ≤500 且 ≤ policy.MaxRows |
| CT-07 | token 篡改/重放/过期/服务重启/撤权/策略/generation 变化 | 均 `invalid_page_token`；claim 后不恢复 |
| CT-08 | 取消/超时/panic 后资源释放 | 连接归还、permit Release、无遗留 pending/running |
| CT-09 | 429 + Retry-After | `rate_limited`/`connection_busy`/`pagination_capacity_exhausted` |
| CT-10 | 审计失败扣留结果 | 返回 `audit_failed`，不返回结果，execution 为终态 |
| CT-11 | SQL/Args/password/token/KEK canary 不进入日志/错误/审计/**API 响应** | 全链路扫描（含响应） |
| CT-12 | 错误响应不含内部码/原始错误/trace_id；receipt 的 trace_id 仅服务端生成 | 固定安全摘要 |
| CT-13 | 结果 wire 类型与列 `wire_type` 一致（decimal 字符串、bool、浮点 number、时间字符串、binary Base64、NULL null；date/time/timestamp 无时区不 UTC 归一化） | 解码与 D08 一致 |
| CT-14 | 连接列表/Schema 浏览不产生 AuditEvent（D05a/D05b） | 审计表无对应事件；脱敏指标/日志保留 |
| CT-15 | 请求 SQL 含 `$N`/`?`/具名占位符（无 args 绑定） | 拒绝（`sql_parse_error`/`statement_not_allowed`），Adapter 0 次 |
| CT-16 | schema/table 标识符注入（`;`/`--`/引号/超长） | 长度/字符校验拒绝，Adapter 0 次 |
| CT-17 | 连接列表/Schema 列表超限（>200/>1000） | `result_too_large`(422)，不静默截断 |
| CT-18 | 演示 Principal 缺失/非法/角色无效 | 启动 fatal 或请求 `unauthorized`；无零值/默认/客户端回退 |
| CT-19 | 元数据浏览超时/取消后连接归还 | 超时/取消触发，连接归还，无遗留 |

### 19.2 E2E（Compose）

| ID | 场景 | 预期 |
|---|---|---|
| E2E-01 | PostgreSQL 主流程：连接→浏览→查询→分页→审计 | 全链路成功，receipt 一致 |
| E2E-02 | MySQL 主流程（含 ECM 拒绝路径） | 同上 |
| E2E-03 | 越权/敏感信息/分页/取消/审计失败回归 | 与 CT 对应 |
| E2E-04 | 高保真页面非 P0 入口隐藏/禁用，Mock 合成数据 | 无伪装已完成能力 |

---

## 20. 风险、回滚与前向修复

### 20.1 风险

| # | 风险 | 缓解 |
|---|---|---|
| R-1 | 契约冻结过宽/过窄导致并发任务返工 | Owner 逐项决策（D01-D18 已批准 2026-08-08）；路由在批准前不注册 |
| R-2 | `mapAdapterError` 折叠真实错误为 internal_error（现状证据 #12） | D15a 折叠策略 + WEB-35 修复映射表 |
| R-3 | 续页在 ADR-014/015 迁移前被提前实现 | 本文件 §9.1 硬性前置；并发 B 未交付前注册续页路由即违反契约 |
| R-4 | 取消语义被"表面 cancel 路由"误导 | §10 诚实结论；transport abort + UI 本地取消状态 |
| R-5 | 高保真页面反向要求敏感字段 | §3 + D17；前端不拥有安全决策权 |
| R-6 | trace_id 仅用于相关性与排查 | D14 已批准：receipt 含服务端 trace_id，但不作为授权/资源凭证、不接受客户端输入 |
| R-7 | 本分支非 WEB-34（当前 `chore/WEB-33-...`） | 本任务不提交/推送/建 PR；提交前需由你切换到/创建 WEB-34 分支 |

### 20.2 回滚与前向修复

- 本文件是**纯文档**，无生产行为；回滚即 `git revert`（如已提交）或直接删除，不影响任何运行时代码。
- 前向修复：Owner 审批后，任一决策调整只需更新本文件对应决策行并重新标注日期/批准人；并发任务以最新批准结论为准。
- 不引入 migration、不新增依赖，无 Schema/依赖/安全策略变更。

---

## 21. 后续实施任务边界

| 并发任务 | 范围（以 Owner 批准后的本契约为准） | 本文件明确的前置/边界 |
|---|---|---|
| WEB-35（并发 A） | HTTP 基础、结果 DTO、只读执行、取消、审计 receipt API | 可先建 handler 骨架与 DTO/错误码；**不得注册续页路由**（§9.1）；修复 `mapAdapterError` 折叠 |
| WEB-38（并发 B） | SchemaSnapshot、VerifiedSortPlan、Service-owned 分页、token registry | ADR-014/015 目标态；交付后才允许续页路由注册 |
| WEB-36（并发 C） | 连接安全 DTO、连接级授权、schemas/tables/columns 懒加载 | 复用 §6/§7 契约；不新增逐 Schema ACL |
| WEB-37（并发 D） | 高保真生产化、可访问性、API 接入 | 非 P0 入口隐藏/禁用；Mock 用合成数据 |
| WEB-39（汇合） | Compose E2E、安全回归、视觉验收、独立审查 | 双引擎主流程 + §19 矩阵 |
| WEB-10 | 查询结果类型规范化 | 与 §13 wire 类型对齐 |

---

## 附录：修订记录

| 日期 | 修订内容 |
|---|---|
| 2026-08-08 | 初版 Draft — 提交 Owner Gate（状态：Pending）。全部 D01-D18 待 Owner 决定；未实现任何 API。 |
| 2026-08-08 | 独立审查修复：§12.1 错误码映射批准状态改为"业务码已批准；HTTP 映射待批准"（后随 Owner Gate 批准）；D05a/D05b/D15d 编号引用修正；§8.2 JSON 注释移出；删除含真实 Google token 的 `docs/frontend design/.env`（token 已暴露，需用户轮换）。 |
| 2026-08-08 | **Owner Gate 通过（fujiabao89）**：D01–D18 全部批准。D05a（连接/Schema 浏览不写 AuditEvent）、D08（wire 类型：decimal/整数十进制字符串、bool、浮点 number、时间规范字符串、binary Base64、JSON 有界 json_text、NULL null、列 wire_type）、D11（每物理页独立 Execution + 返回页面前持久化 AuditEvent）、D13（仅 transport abort）、D14（receipt 含服务端 trace_id，不作为授权凭证）、D15f（公共 API 仅 query_timeout/query_cancelled，历史兼容读取）专项决定；状态由 Draft 更新为已批准。 |
| 2026-08-08 | 响应 Greptile（1 条）与 CodeRabbit（13 条）PR 审查：§3 区分"绝不接收/可接收不得控制"（Engine/Environment 可见性）；D01b fail-closed（启动 fatal/unauthorized）；`meta` 必需性；连接/Schema 列表超限 `result_too_large` 不静默截断；元数据超时边界（5s/10s/15s）；标识符处理澄清为 information_schema 值参数绑定（非 quoteIdent 拼接）；`TABLE / VIEW`；查询结果脱敏边界明确（P0 不提供列值脱敏，需新 ADR）；请求示例去占位符并禁止内联参数；响应示例去 token 且加 `wire_type`；date/time/timestamp 无时区与 timestamptz 分开定义；超大值拒绝不截断；CT-11 覆盖响应、新增 CT-15..19。 |

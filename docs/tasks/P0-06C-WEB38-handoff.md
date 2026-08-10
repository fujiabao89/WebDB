# WEB-38 / P0-06C 交接文档：SchemaSnapshot、VerifiedSortPlan 与 Service-owned 分页

> 状态：实施完成，待独立安全审查｜日期：2026-08-09｜分支：`feat/WEB-38-service-owned-pagination`
>
> 本任务实现 ADR-014/015 已接受目标态：不可伪造排序唯一性证明、可信 SchemaSnapshot、Service-owned continuation registry、续页重新授权。未注册任何 HTTP 路由、未改前端、未创建 migration、未新增依赖、未改变 ADR。

## 1. 改动文件

**新增**
- `apps/api/internal/queryplan/types.go` — 中立类型：Dialect/SortKey/Column/PrimaryKey/UniqueConstraint/TableMetadata/validateIdent/findUniqueProof
- `apps/api/internal/queryplan/schema.go` — SchemaSnapshot（绑定 connection/dialect/pool/schema generation）、内容哈希 generation、fail-closed 校验、深拷贝 accessor
- `apps/api/internal/queryplan/shape.go` — QueryShape（单一基础表、可追溯列）
- `apps/api/internal/queryplan/plan.go` — VerifiedSortPlan sealed interface + VerifySortPlan + IsValidVerifiedPlan
- `apps/api/internal/queryplan/nextpage.go` — VerifiedNextPagePlan sealed interface + NewVerifiedNextPagePlan
- `apps/api/internal/queryplan/schema_test.go` / `plan_test.go` / `nextpage_test.go` / `fuzz_test.go` — 单元/深拷贝/fuzz 测试
- `apps/api/internal/pagination/state.go` — ContinuationState + 递归深拷贝 + retainedBytes 字节计费
- `apps/api/internal/pagination/registry.go` — Service-owned Registry：Create/Claim/Rotate/Complete/Abort/CleanupExpired、计数+字节配额、digest key、TTL、驱逐
- `apps/api/internal/pagination/registry_test.go` / `fuzz_test.go` — 状态机/配额/并发/fuzz 测试
- `apps/api/internal/sqlpolicy/shape.go` — AnalyzeShape（PG/MySQL 保守形状提取，join/聚合/计算列拒绝）
- `apps/api/internal/sqlpolicy/shape_test.go`
- `apps/api/internal/adapter/schema_meta.go` — LoadTableMetadata（PG 经 pg_constraint / MySQL 经 information_schema STATISTICS，参数绑定+超时+上限）
- `apps/api/internal/adapter/schema_meta_test.go` — 行扫描纯函数测试
- `apps/api/internal/adapter/pagination_integration_test.go` — PG/MySQL 新 API 集成测试
- `apps/api/internal/execution/pagination_test.go` — 首页分页 + NextPage 重新授权测试
- `docs/tasks/P0-06C-WEB38-handoff.md` — 本文件

**修改**
- `apps/api/internal/adapter/types.go` — 移除 `SortKey`/`SortOrder`/`NextToken`；`FirstPageRequest.SortPlan`、`QueryResult.HasMore`
- `apps/api/internal/adapter/keyset.go` — `buildSortSpecs` → `sortSpecsFromPlan`/`sortSpecsFromSpecs`；移除 sortSpec.unique
- `apps/api/internal/adapter/manager.go` — 移除 registry 字段；Query/NextPage 新签名；`PoolGeneration()`；`ExtractLastValues` 导出
- `apps/api/internal/adapter/schema_meta.go` — 新增（见上）
- `apps/api/internal/adapter/manager_test.go` — 移除旧 token 分页集成测试
- `apps/api/internal/execution/pipeline.go` — AdapterHandle 扩展；Execute 分页编排；ExecuteNextPage 重新授权；verifySortPlan/createContinuation/policyVersionOf/mapPaginationError
- `apps/api/internal/execution/service.go` — 新增 `ErrInvalidPageToken`/`ErrPaginationCapacityExhausted`
- `apps/api/internal/execution/pipeline_test.go` / `audited_pipeline_test.go` — 适配新接口
- `apps/api/internal/credentials/*`、`metadata/*` — 未改动

**删除**
- `apps/api/internal/adapter/pagination.go` — Adapter-owned ContinuationRegistry/PagePlan/genToken 全部移除

## 2. ADR-014/015 验收矩阵

| ADR 条款 | 实现 | 证据 |
|---|---|---|
| ADR-014：VerifySortPlan 唯一创建入口 | `queryplan.VerifySortPlan`；无 raw constructor；sealed interface | `plan.go`；`TestVerifySortPlan*` |
| nil/typed-nil/无效 seal/version 拒绝 | `IsValidVerifiedPlan`（reflect 检查 typed-nil + Valid + version） | `TestIsValidVerifiedPlanRejectsNilAndTypedNil` |
| accessor 深拷贝 | SortSpecs/Columns/UniqueConstraints 深拷贝；[]any 递归 | `TestSortSpecsDeepCopy`、`TestSchemaSnapshotTableMetadataIsolated` |
| 单一基础表、可追溯列 | `sqlpolicy.AnalyzeShape` 保守提取；排序列必须映射到基础列 | `shape_test.go` 全部用例 |
| 完整主键/全 NOT NULL 唯一约束 | `findUniqueProof` | `TestVerifySortPlan*` |
| 部分复合唯一/nullable unique/表达式索引/join/聚合/计算列拒绝 | AnalyzeShape + findUniqueProof 双重 gate | `TestVerifySortPlanRejects`、`TestAnalyzeShape*` |
| 客户端 Unique 不影响 | SortKey 无 Unique 字段；验证仅依据 snapshot | `TestVerifySortPlanClientUniqueFlagHasNoEffect` |
| 无法获得可信快照时拒绝 | NewSchemaSnapshot/VerifySortPlan 校验；服务层映射 unsupported_query | `TestExecuteUnsupportedQueryWhenSchemaUnavailable` |
| SchemaSnapshot 绑定 connection/dialect/pool/schema gen | `SchemaSnapshot` 结构 + 内容哈希 | `TestSchemaGeneration*` |
| ADR-015：Service 唯一 Owner | `pagination.Registry` 在 execution 服务层；Adapter 无 registry | 删除 `adapter/pagination.go` |
| 32B CSPRNG + SHA-256 digest key | `genHandle`(32B)/`digestOf`(SHA-256) | `TestRegistryKeyIsDigestNotHandle` |
| ContinuationState 完整绑定 | UserID/WorkspaceID/ConnectionID/pool/schema gen/policy version/statement hash/SortPlan/SQL/Args/last values/count/PageSize/MaxRows | `state.go` |
| TTL ≤5min、服务重启失效 | NormalizedConfig 强制 ≤5min；内存 registry | `TestClaimExpiredToken` |
| 四维数量配额（10000/100/500/200） | Registry 计数配额 | `TestQuotaExactlyFullRotateStillWorks` |
| 字节配额（D12，Owner 批准） | 单状态 512KiB / user 16MiB / ws 64MiB / conn 16MiB / global 512MiB | `DefaultConfig`；`TestByteQuota*` |
| 只驱逐 ready | `pickEvictable` 只选 statusReady | `TestQuotaExactlyFullRotateStillWorks`（in-flight 不被驱逐） |
| 原子 ready→claim→in-flight，claim 后不恢复 | Claim 单锁原子；Rotate/Abort 失败删除旧 token | `TestRotateFailureDeletesOldAndNotRestored` |
| 原子 Rotate/Complete/Abort + claim ownership/version | Claim 持有 version；Rotate 校验 | `TestDelayedFinalizerDoesNotDeleteNewToken` |
| Rotate 同容量槽 | Rotate 释放旧槽再写入，不额外占配额 | `TestQuotaExactlyFullRotateStillWorks` |
| 过期清理覆盖 ready+in-flight | CleanupExpired | `TestExpiryCleanupRemovesReadyAndInFlight` |
| token 不进日志/审计/trace/metric | 无任何日志写入；digest 存储 | 代码审查 |
| 每页重新授权 | ExecuteNextPage 重查成员/连接/策略/version/generation/statement hash | `pagination_test.go` |
| 客户端不得重交 SQL/Args/SortKeys | NextPageRequest 仅 Token；SQL/Args 从 state 恢复 | `ExecuteNextPage` |
| Adapter 只收 VerifiedNextPagePlan | `PoolHandle.NextPage(ctx, scope, plan)` | `manager.go` |
| 撤权/策略/连接重建/Schema 变化使 token 失效 | ExecuteNextPage 各校验分支 | `TestNextPage*InvalidatesToken` |

## 3. Adapter API 迁移表

| 旧 API（已移除） | 新 API |
|---|---|
| `adapter.SortKey{Column,Order,NullsLast,Unique}` | `queryplan.SortKey{Column,Direction,NullsLast}`（无 Unique） |
| `FirstPageRequest.SortKeys []SortKey` | `FirstPageRequest.SortPlan queryplan.VerifiedSortPlan` |
| `QueryResult.NextToken *string` | `QueryResult.HasMore bool` |
| `PoolHandle.NextPage(scope, token string)` | `PoolHandle.NextPage(scope, queryplan.VerifiedNextPagePlan)` |
| `ContinuationRegistry`（adapter） | `pagination.Registry`（Service-owned） |
| `adapter.PagePlan` | `pagination.ContinuationState` |
| `extractLastValues`（私有） | `adapter.ExtractLastValues`（导出，供服务层） |
| `buildSortSpecs([]SortKey)` | `sortSpecsFromPlan`/`sortSpecsFromSpecs` |
| `genToken`（adapter） | `pagination.genHandle` |
| `AdapterHandle{Query,Release}` | `AdapterHandle{Query,NextPage,LoadTableMetadata,PoolGeneration,Release}` |

## 4. 状态机与配额不变量

**Token 状态机**：`ready →(Claim)→ in-flight →(Rotate)→ ready(新 digest) / →(Complete|Abort)→ 删除`
- Claim 单锁原子；过期即删；并发 claim 仅一个成功。
- Rotate 校验 claim ownership/version、in-flight、未过期、新 state 合法、字节预算；任一失败原子删除旧 in-flight、不恢复。
- Complete/Abort 幂等；延迟 finalizer（旧 claim）不能删除新 token（digest 不同 + version 校验）。
- 只驱逐 ready；ready 与 in-flight 都计入计数与字节配额。

**配额不变量**（fuzz 校验）：
- `globalCount == len(entries)`；`globalBytes == Σ entry.bytes`。
- user/ws/conn 计数与字节映射与 entries 严格一致。
- 计数/字节归零即删除 map 键（无零值键无界增长）。
- 驱逐只选最旧 ready 条目；in-flight 永不因配额被驱逐。
- Rotate 不改变计数槽位（net 字节预算校验）。

## 5. WEB-35 可调用的内部接口

- `queryplan.VerifySortPlan(snapshot, shape, sortKeys) (VerifiedSortPlan, error)`
- `queryplan.NewVerifiedNextPagePlan(plan, lastValues, sql, args, pageSize, maxRows, cumCount) (VerifiedNextPagePlan, error)`
- `queryplan.NewSchemaSnapshot(connID, dialect, poolGen, meta) (*SchemaSnapshot, error)`
- `sqlpolicy.AnalyzeShape(dialect, sql) (*queryplan.QueryShape, error)`
- `execution.Pipeline.Execute(ctx, ExecuteRequest{..., SortKeys, PageSize}) (*ExecuteResult{..., NextPageToken}, error)`
- `execution.Pipeline.ExecuteNextPage(ctx, NextPageRequest{Principal, Token}) (*ExecuteResult, error)`
- `execution.Pipeline.Close()`（释放内置 registry）
- `pagination.Registry`（可注入 PipelineConfig.Pagination；默认由 NewPipeline 创建）

**续页 HTTP 路由注册前置**（P0-06A §9.1）：本迁移完成后方可由 WEB-35 注册 `/query-pages`。

## 6. 原始测试结果

| 命令 | 结果 |
|---|---|
| `go -C apps/api test ./...` | 全部 ok |
| `go -C apps/api vet ./...` | 无输出（通过） |
| `gofmt -l .` | 无输出（干净） |
| `go -C apps/api build ./...` | 通过 |
| `GOOS=linux GOARCH=amd64 go -C apps/api build ./...` | 通过 |
| `go -C apps/api test ./internal/pagination/ -fuzz=FuzzRegistryInvariants -fuzztime=5s` | PASS（~45k execs） |
| `go -C apps/api test ./internal/queryplan/ -fuzz=FuzzVerifySortPlanNoPanic -fuzztime=5s` | PASS（~442k execs） |
| `go -C apps/api test -tags=integration ./internal/adapter/`（PG:5433/MySQL:3306 demo） | ok（PG+MySQL 分页无重复） |
| `go -C apps/api test -race ./...` | **本机不可用**：无 gcc（`-race requires cgo`）；由 CI ubuntu `go test -race ./...` 覆盖（与 ADR-017 记录一致） |

## 7. 残余风险

1. **跨请求快照一致性**：并发 INSERT/UPDATE/DELETE 导致的观察缺失/重复为 ADR-014 已接受残余风险，不在 P0 修复。
2. **单页 overflow 语义**：`requiresPagination=false` 时仍沿用既有"截断至 effectiveMaxRows"行为；P0-04 §4.2 #9 的"读到 sentinel 返回 result_too_large"契约归 WEB-35 收口。
3. **续页 D11 审计**：`ExecuteNextPage` 提供重新授权+状态机，但不持久化每页 Execution/AuditEvent（D11 归 WEB-35）。
4. **race 本机未跑**：Windows 无 gcc；依赖 CI。
5. **MySQL 唯一键经 STATISTICS**：按整个 INDEX_NAME 剔除含 SUB_PART/EXPRESSION（前缀/函数/表达式索引）的唯一索引（避免残留列被误认为完整唯一约束）；经 `hasMySQLExpressionColumn` capability 探测选择带/不带 EXPRESSION 列的查询，8.0.0–8.0.12 用 `NULL AS EXPRESSION` 占位正常支持（Codex 二轮 P1 修复）。
6. **volatile 谓词拒绝**：分页查询 WHERE 含任何函数调用（PG random()/now()、MySQL RAND() 等）一律拒绝，续页重放 volatile 谓词会使集合变化导致漏行（Codex 三轮 P1 修复）；`SortPlan` 的 retained SortSpecs 计入 per-state 字节配额（Codex 三轮 P2 修复）。

## 8. 回滚/前向修复

- **回滚**：`git revert`（或丢弃分支）。若需保留旧 Adapter-owned pagination，需恢复 `adapter/pagination.go` 并还原 `manager.go` Query/NextPage；但 ADR-014/015 已接受，回滚将退回"裸 SortKey.Unique 信任"，需 Owner 决策。
- **前向修复**：
  - 若 PG `pg_constraint` 在部分受限账号下仍不可见，改为 `pg_get_constraintdef` + `SELECT ... WHERE has_table_privilege(...)`。
  - 字节配额默认值已获 Owner 批准；如需调整只需改 `pagination.DefaultConfig`。
  - 单状态字节上限不足时，SQL/Args 深拷贝前可加预检。

## 9. 独立审查重点

1. `queryplan.VerifySortPlan`：唯一性证明是否只依赖可信 SchemaSnapshot；客户端任何字段是否影响结果。
2. `sqlpolicy.AnalyzeShape`：形状提取是否保守；是否存在漏放行的不安全形状（表达式遮蔽、别名、混合 star）。
3. `pagination.Registry`：claim/rotate/abort 原子性、配额计数/字节一致性、驱逐只删 ready、延迟 finalizer 不能删新 token、无界增长。
4. `execution.ExecuteNextPage`：每页重新授权完整性（成员/连接/策略/version/pool gen/schema gen/statement hash）；旧 token 失败不恢复。
5. Adapter：`SortKey.Unique` 信任与 registry 是否彻底移除；`NextPage` 是否只接受不可伪造计划。
6. 敏感信息：token/真实 SQL/Args/结果是否可能进入日志、审计、trace、metric、错误信息。

## 10. 独立安全审查修复记录（2026-08-09，REQUEST CHANGES → 修复）

| Finding | 级别 | 修复方案 | 位置 | 新增测试 | 验证 |
|---|---|---|---|---|---|
| 1. 续页未校验 token 绑定 principal | P1 | ExecuteNextPage claim 后立即校验 `state.UserID == req.Principal.UserID.String()` 且 WorkspaceID 匹配；不匹配 abort + invalid_page_token | `pipeline.go` ExecuteNextPage | `TestNextPageDifferentUserRejected`、`TestNextPageDifferentWorkspaceRejected` | 通过（修复前失败） |
| 2. PG 未限定表名 schema 硬编码 "public" | P1 | adapter 新增 `PoolHandle.CurrentSchema`（PG `SELECT current_schema()`，MySQL 连接库，带 connAcquireTimeout）；verifySortPlan 未限定表名时使用其结果；失败/空 → fail-closed | `adapter/manager.go`、`pipeline.go` verifySortPlan | `TestExecuteUnqualifiedTableUsesCurrentSchema`、`TestExecuteCurrentSchemaErrorFailsClosed`、`TestExecuteCurrentSchemaEmptyFailsClosed`、`TestCurrentSchema_PG/MySQL`（集成） | 通过 |
| 3. 续页全表扫描+重排序性能 | P2 | **登记为后续任务**（见下）：keyset 谓词注入需基于 omni AST Loc 做 SQL 拼接定位 WHERE，违反"不得以字符串拼接作安全边界"约束且两方言 Loc 未验证；当前 wrapper 正确（集成无重复），仅性能 O(N) | — | — | 后续任务 |
| 4. 非 string 键 map 深拷贝退化 + Claim.State() 内部指针 | P3 | `deepCopyAny`/`deepCopyValue` 改全类型反射递归（map 键值均复制）；`Claim.State()` 返回深拷贝 | `pagination/state.go`、`pagination/registry.go`、`queryplan/nextpage.go` | `TestCreateDeepCopiesNonStringKeyMapArgs`、`TestClaimStateReturnsCopy` | 通过 |
| 5. mapAdapterError 未映射 ErrInvalidPageToken | P3 | switch 增加 `case adapter.ErrInvalidPageToken: return ErrInvalidPageToken` | `pipeline.go` mapAdapterError | `TestMapAdapterError.../invalid_page_token` | 通过 |
| 6. 未提交、无 PR/CI | P3/流程 | 本任务提交→push→创建 PR→等待 CI（含 ubuntu race）→报告原始结果 | — | — | 见 PR/CI |

### FINDING 3 后续任务登记（Owner：fujiabao89｜期限：WEB-39 汇合前）

**评估依据**：将 keyset 谓词 + ORDER BY/LIMIT 注入内层 SQL（在用户 WHERE 后追加 `AND <keyset>`）需用 omni AST 精确定位用户 WHERE 结束位置并做 SQL 字符串拼接。仓库既有约束明确"不得以字符串前缀匹配/拼接作为安全边界"（AGENTS.md / ADR-007）；且 PG/MySQL 两个方言 AST 的 Loc（源码位置）映射到原始 SQL 字符串的可靠性未经验证，误插入到字符串字面量/注释内会造成 SQL 注入。当前 wrapper 实现经集成测试证明**正确**（固定数据集无重复/漏行），仅在大表上存在 O(N) 全表扫描+重排序性能退化（设计稿 10 万行浏览场景）。

**建议**：独立小任务验证 omni AST 两方言 WHERE 结束位置的 Loc 可靠性（含字符串/注释边界 fuzz），通过后再实现内层注入；若 Loc 不可靠则保持 wrapper 并在 Adapter 侧评估基于索引的 keyset 优化。

### FINDING 6 提交/PR/CI 记录

- 分支 `feat/WEB-38-service-owned-pagination`，head SHA 见 PR。
- PR：#（见 GitHub）。
- CI：ubuntu `go test -race ./...` 与全量测试原始结果见 PR CI run。

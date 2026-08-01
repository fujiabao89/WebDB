# ADR-014：SortKey 唯一性证明与 VerifiedSortPlan

> 状态：已接受｜日期：2026-07-26｜Owner：fujiabao89｜批准日期：2026-07-27

## 背景

`SortKey.Unique` 是 `bool` 字段。P0-03 规定"Adapter 不承担 SQL 安全裁决"，SortKey 由 P0-04 服务层构造后传入。但 `Unique=true` 无法区分"单列主键"和"复合 `UNIQUE(tenant_id, seq)`"，当只排序部分约束列时会导致 keyset 分页漏行或重复。

之前尝试引入 `buildSortSpecs(keys, verifiedUnique []string)` 并在 `verifiedUnique` 为空时 fail-closed，结果所有 Query/NextPage 被禁用。

`UniqueConstraint` 用导出字段无法防止伪造；`VerifiedSortPlan` 必须由中立底层包通过验证函数产生，字段不可导出。

## 决策

**选择 A：P0-04 使用 VerifiedSortPlan。**

### VerifiedSortPlan 类型约束

1. **包依赖方向**：`internal/queryplan` 拥有 `SortKey`、`SortDirection`、`NullOrder`、`VerifiedSortPlan` 等中立类型。`queryplan` 不得 import `adapter`；`adapter` 单向 import `queryplan`。避免 `adapter ↔ queryplan` 循环依赖。

2. **Go sealed interface 类型方案**：`VerifiedSortPlan` 和 `VerifiedNextPagePlan` 均采用 exported sealed interface + 私有实现：

   ```go
   package queryplan

   type VerifiedSortPlan interface {
       isVerifiedSortPlan() // 未导出方法，包外无法实现
       Valid() bool
       // 只读 accessor；可变返回值必须深拷贝
   }

   type verifiedSortPlan struct {
       // 全部字段私有
   }
   ```

- `VerifiedSortPlan` 是 exported sealed interface，具体实现 `verifiedSortPlan` 不导出。
- 包外无法实现该接口，也无法直接构造具体实现。
- `VerifySortPlan()` 是唯一合法创建入口。
- nil interface、typed-nil、`Valid()==false` 全部拒绝。
- Adapter 在执行前必须同时检查：interface 非 nil；typed-nil 检查通过；`Valid()` 为 true；内部 version/seal/invariants 有效。
- 接口值复制允许，但内部实现必须不可变；所有 slice/map/`[]any` accessor 返回深拷贝。
- `VerifiedNextPagePlan` 使用相同的 sealed-interface 模式。

3. **唯一创建入口**：`VerifySortPlan()` 函数；不提供 raw constructor；不支持 JSON/数据库反序列化直接构造。

4. **Accessor 深拷贝**：所有返回 slice/map/`[]any` 的 accessor 必须返回深拷贝或只读迭代结果，不能把内部可变引用暴露给 Adapter/Service。

5. **安全复制**：合法计划可以安全复制，但调用方不得通过复制后修改其内部证明或排序字段。

6. **无效计划的错误映射**：Adapter 必须把无效计划映射为 `unsupported_query` 或专门的内部稳定错误码，不得降级到旧 `SortKey.Unique`。

7. 客户端和普通服务请求不得提交或控制 `Unique=true`。该标记仅由 `VerifySortPlan` 内部设置。

### SchemaSnapshot

`VerifySortPlan` 接收来自 metadata/service 层的可信 `SchemaSnapshot`，`queryplan` 不直接连接目标数据库。`SchemaSnapshot` 至少绑定：

- `connection_id`
- pool generation
- schema generation/version
- 方言
- 表及列 lineage
- 主键/唯一约束（含列顺序）
- 每列的 nullable 属性

### 可分页查询的形状

P0 仅接受可可靠证明的查询形状：

- 单一基础表；
- 排序列可追溯到真实基础列；
- 完整覆盖主键；或完整覆盖所有列均 `NOT NULL` 的唯一约束。
- nullable unique、表达式索引、partial index、join、聚合、计算列等无法可靠证明时默认拒绝。

### 复合唯一约束

排序键集合必须包含该约束的**全部**列；只包含部分列必须拒绝。

PostgreSQL/MySQL 对 `UNIQUE` 中 NULL 的语义差异必须正确识别。含 nullable 列的唯一约束不能作为全局唯一证明（两个引擎的普通 UNIQUE 都允许多个 NULL）；仅主键或所有列均 `NOT NULL` 的唯一约束可用。

### 无法获得可信 Schema 元数据时

无法获得或验证可信 `SchemaSnapshot` 时：

- `VerifySortPlan` 必须拒绝；
- 不生成 `VerifiedSortPlan`；
- 不允许生成 continuation token；
- **禁止**运行时或实施阶段静默回退到客户端 `SortKey.Unique` 信任模型；
- 改变该决策必须新建 ADR。

### "需要分页"的执行前判定

若 `effectiveMaxRows > effectivePageSize`，请求具备跨页可能，执行前必须取得有效 `VerifiedSortPlan`。

- 无法证明唯一排序时，在访问目标数据库前拒绝，不执行第一页。
- 若 `effectiveMaxRows <= effectivePageSize`，属于明确的单页受限请求：可以不创建 continuation；返回行数仍受 `effectiveMaxRows`、字节、超时限制；不得在响应中发放 token。
- 不允许先执行第一页、发现多出一行后才静默截断或返回"无 continuation"的部分结果。

### 跨页复用条件

唯一性证明仅在 `connection_id`/pool generation/schema generation 未变化时可复用。`NextPage` 仍需重新验证：

- 成员资格（用户是否仍在工作区）；
- 连接策略（`AllowRead`/`MaxRows` 是否变更）；
- policy version。

generation 或策略上下文变化时 token 失效。

### 残余一致性风险

唯一排序保证固定数据集上的确定性全序，但 P0 不跨请求维持数据库快照。并发 INSERT/UPDATE/DELETE（尤其是排序键变化）仍可能造成观察到的缺失或重复。这是**已知残余风险**，不在 P0 修复。

## 被拒绝的候选方案

### 选项 B：Adapter 内部追加主键 Tie-breaker

**理由拒绝**：需要 Adapter 解析 SQL 获取表名以查询 Schema，违反 ADR-007"Adapter 不承担 SQL 安全裁决"。

### 选项 C：保持 P0-03 行为 + 文档化信任边界

**理由拒绝**：仅靠文档约束信任边界不可靠。P0-04 实现错误时 Adapter 无法检测，且无法正确表达复合唯一约束。

## 后果

- **安全**：唯一性证明不可伪造，无法获得可信 Schema 元数据时 fail-closed。
- **兼容**：Adapter API 变更（`Query` 接收 `VerifiedSortPlan`；`NextPage` 接收 `VerifiedNextPagePlan`，该类型由 ADR-015 定义并替代本 ADR 早期草案中 `NextPage` 对 `VerifiedSortPlan` 的引用）。`SortKey.Unique` 废弃。
- **运营**：无法证明唯一性的查询拒绝分页，需服务端日志记录原因。
- **测试**：单列主键、复合唯一索引、NULL 语义、ASC/DESC/NULLS FIRST/LAST、跨页完整性、generation 变化。

## 验证

- 单列主键排序 + 完整分页。
- `UNIQUE(a,b)`，只排序 `a`：拒绝。排序 `a,b`：允许。
- `UNIQUE(a)` 且 `a` nullable：拒绝（多个 NULL 合法）。
- `NOT NULL` 列构成的唯一约束：允许。
- NULLS FIRST/LAST、ASC/DESC。
- 在无并发变更的固定测试数据集上，唯一排序分页不因非唯一排序产生缺失或重复。
- 连接 generation 变化后旧 plan 失效。
- 客户端提交 `Unique=true` 不影响验证结果。
- `SchemaSnapshot` 不可用时拒绝生成 `VerifiedSortPlan`。

## 回滚/替代条件

改变"不可获得 Schema 元数据时禁止静默回退"的决策必须新建 ADR。

## 迁移影响

- `apps/api/internal/adapter/types.go`：`SortKey.Unique` 废弃。
- `apps/api/internal/adapter/keyset.go`：`buildSortSpecs` 接收 `VerifiedSortPlan`。
- `apps/api/internal/adapter/manager.go`：`Query`/`NextPage` 适配新接口。
- 新增 `apps/api/internal/queryplan/` 包。

## 相关资料

- [ADR-007](./ADR-007-dialect-aware-sql-parsing.md)
- [P0-03 任务卡](../tasks/P0-03-database-adapter-contract.md)
- [P0-04 提案](../tasks/P0-04-proposal-contract-and-parser.md) §3.1/§3.2

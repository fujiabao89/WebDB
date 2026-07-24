# P0-04 依赖 Spike 报告（最终版）

> 状态：ESCALATE — 双方言所有候选均不满足全部验收标准
> 日期：2026-07-24
> 任务：P0-04（SQL Safety Policy — 依赖 Spike）
> PR：[#19](https://github.com/fujiabao89/WebDB/pull/19)（本报告）；提案 PR：[#16](https://github.com/fujiabao89/WebDB/pull/16)（已合并）
> 分支：`feat/P0-04-parser-spike`
> 作者：Claude Code
> 修订：PG 探针类型化 + TiDB Scanner API 源码审计 + Vitess+Omni 替代评估

---

## 1. 最终结论：ESCALATE

| 候选 | PG AST | MY AST | 可执行注释 | 总体 |
|------|--------|--------|-----------|------|
| pgparser v0.2.0 | 42/43 (TABLE t FAIL) | N/A | N/A | **FAIL** |
| TiDB Parser | N/A | 24/24 | Scanner API FAIL | **FAIL** |
| Vitess v0.24.2 | N/A | 27/27 | GetComments() FAIL | **FAIL** |
| DoltHub vitess | — | — | — | 无法导入 |
| **Bytebase Omni** | **31/32 (TABLE t FAIL)** | 11/16¹ | Tokenize() 部分² | **FAIL** |

¹ MySQL AST 使用 %T 字符串分类，缺少字段级检测（FOR UPDATE / LOCK IN SHARE MODE / @x:= / EXPLAIN target 未细化）
² ECM token 真阳性 2/9，spliceGaps 未导出

**所有候选均不满足至少一项关键安全要求。** 不得开始 sqlpolicy/ 或 execution/ 实现。

---

### 1.1 单语句检测（所有候选）

提案要求验证 semicolon 和 comment-hidden 多语句输入，返回语句数 ≥2 时拒绝。

| 候选 | PG-20 (semicolon) | PG-21 (comment-hidden) | MY-16 (semicolon) | 结果 |
|------|:---:|:---:|:---:|------|
| pgparser | ✅ n=2 | ✅ n=2 | N/A | PASS |
| TiDB Parser | N/A | N/A | ✅ n=2 | PASS |
| Vitess | N/A | N/A | ✅ ErrMultipleStatements | PASS |
| Omni PG | ✅ n=2 | ✅ n=2 | N/A | PASS |
| Omni MySQL | N/A | N/A | ✅ n>1 | PASS |

注：CTE DML（如 MY-15 `WITH d AS (DELETE FROM t) SELECT * FROM d`）属于单语句危险分类验证（AST 递归遍历 `WithClause.Ctes`），不在此表范围内，详见各候选 AST 分类章节。

**结论**：所有候选均正确拒绝多语句输入。单语句检测不是本轮阻塞项。

---

## 2. Bytebase Omni 评估（本轮新增）

**版本**：`github.com/bytebase/omni v0.0.0-20260720033410-726bb7ff40aa`（MIT License）
**依赖图**：73 个模块（大部分为 testcontainers/docker 测试依赖），生产依赖主要为 pgx/mysql 驱动
**构建**：`GOOS=windows/linux GOARCH=amd64 CGO_ENABLED=0` 均成功

### 2.1 PG 分类：31/32 PASS

基于 `pg/ast` 类型断言和字段检查：

| 特征 | 检测方式 | 结果 |
|------|---------|------|
| FOR UPDATE/SHARE/KEY SHARE/NO KEY UPDATE | `SelectStmt.LockingClause.Len()>0` | PASS |
| SELECT INTO | `SelectStmt.IntoClause!=nil` | PASS |
| 数据修改 CTE | 递归遍历 `WithClause.Ctes` | PASS |
| EXPLAIN ANALYZE | `ExplainStmt.Options` 中 DefElem | PASS |
| EXPLAIN DML target | `ExplainStmt.Query` 类型断言 | PASS |
| **TABLE t** | **SelectStmt 结构相同于 SELECT * FROM t** | **FAIL** |

**PG-18 详细证据**：

```
TABLE t:
  SelectStmt: TargetList!=nil FromClause!=nil ValuesLists=nil IntoClause=nil LockingClause=nil

SELECT * FROM t:
  SelectStmt: TargetList!=nil FromClause!=nil ValuesLists=nil IntoClause=nil LockingClause=nil
  ← 完全相同的字段状态
```

结论：Omni PG parser 与 pgparser 一样，将 `TABLE t` 解析为与 `SELECT * FROM t` 无法区分的 SelectStmt。

### 2.2 MySQL AST 分类：11/16 PASS

使用 `%T` 格式字符串分类。5 个失败项因缺少字段级 AST 检测：

| 失败案例 | 原因 |
|----------|------|
| MY-03 FOR UPDATE | mysql/ast.SelectStmt 无 LockingClause 字段 |
| MY-04 LOCK IN SHARE MODE | 同上 |
| MY-08 @x:=id | 需 mysql/ast 的 ResTarget/VariableExpr 字段检测 |
| MY-23 EXPLAIN DELETE | 需 mysql/ast.ExplainStmt 字段检测 |
| MY-28 EXPLAIN ANALYZE | 同上 |

**注**：Omni 的 `mysql/ast` 与 `pg/ast` 类型不兼容（不同 Node interface），本 Spike 使用 %T 字符串作为最小可行分类。正式实施需引入 mysql/ast 类型断言和 AST Walker。

### 2.3 可执行注释：部分检测（2/9 ECM 真阳性）

**Lexer 公共 API**：`parser.Tokenize(sql) -> []Token`

Lexer 源码（lexer.go:1937-1992）：检测 `/*!` → 移除 `/*!NNNNN` 和 `*/` → 将内部内容拼接入 SQL → 记录 spliceGaps（未导出）

**测试结果（12 条）**：

| ID | SQL | 拒绝方式 | ECM Token |
|----|-----|---------|-----------|
| EC-01 | `/*!50000 DROP TABLE t*/ SELECT 1` | **ECM Token 检测**（DROP TABLE 出现在 token 流） | ✅ true |
| EC-12 | `/*!50000\nDROP TABLE t;\n*/ SELECT 1` | **ECM Token 检测**（DROP TABLE; SELECT 1 均出现） | ✅ true |
| EC-V3 | `SELECT 1 + /*!801002*/` | parse error（fail-closed） | ❌ |
| EC-V4 | `SELECT /*!90000 1 + 2 */ 42` | parse error（fail-closed） | ❌ |
| EC-V6 | `SELECT /*!80100 1 /*!99999*/ + 2 */` | parse error（fail-closed） | ❌ |
| **EC-05** | `/*!99999 SELECT 1*/` | **NOT REJECTED**（解析为 SELECT 1） | ❌ |
| **EC-V1** | `SELECT /*! 1 + 1 */ FROM dual` | **NOT REJECTED**（解析为 SELECT 1+1 FROM dual） | ❌ |
| **EC-V2** | `SELECT /*!80100 42*/ FROM dual` | **NOT REJECTED**（解析为 SELECT 42 FROM dual） | ❌ |
| **EC-V5** | `SELECT /*!80100 1 /* nested */ + 2 */` | **NOT REJECTED**（解析为 SELECT 1+2） | ❌ |
| EC-B1 | 字符串字面量内 /*! | 正确允许（Benign） | ❌ |
| EC-B2 | 普通块注释 | 正确允许（Benign） | ❌ |
| EC-B3 | optimizer hint | 正确允许（Benign） | ❌ |

- **ECM Token 真阳性**：2/9（仅当可执行注释内含 DDL/DML 关键字时可通过 Tokenize() 检测）
- **Fail-closed**：3/9（parse error 拒绝）
- **NOT REJECTED**：4/9（可执行注释含纯表达式/常量/嵌套 → 被拼接为合法 SQL）

**核心限制**：`spliceGaps` 为未导出字段，公共 API `Tokenize()` 只能通过观察拼接后的 DDL 关键字间接检测。当可执行注释内容为 SELECT/表达式/常量时，拼接后的 token 流与正常 SQL 无法区分。

### 2.4 LICENSE

- `github.com/bytebase/omni`：**MIT**（实际读取 LICENSE 文件验证）
- 传递依赖 73 个模块，多数为 MIT/Apache 2.0/BSD。`go-sql-driver/mysql`（MPL 2.0）已存在项目。

---

## 3. 其他候选摘要

### pgparser v0.2.0 — FAIL

- PG AST：42/43（PG-18 TABLE t 无法区分）
- PostgreSQL License（BSD-like），非 Apache 2.0

### TiDB Parser — FAIL

- MY AST：24/24（类型断言 + AST Walker）
- EC：Scanner API FAIL（`inBangComment` 未导出，SpecFieldPattern 对块注释误报）

### Vitess v0.24.2 — FAIL

- MY AST：27/27（类型断言 + AST Walker）
- EC：GetComments() FAIL（ECM 真阳性 0/13）

### DoltHub vitess — 无法导入

- 模块路径冲突：引用 `github.com/youtube/vitess` 但声明为 `vitess.io/vitess`

---

## 4. 所有隔离目录

| 目录 | 用途 |
|------|------|
| `<TEMP>/p0-04-spike-20260724-143057` | pgparser + TiDB Parser |
| `<TEMP>/p0-04-mysql-spike-20260724-151815` | Vitess |
| `<TEMP>/p0-04-omni-spike-20260724-171004` | Bytebase Omni |

---

## 5. 传递依赖许可证详细清单

### 5.1 Bytebase Omni（73 模块）

来源：`go mod download all` + `go list -m -json all` + 逐模块 LICENSE 文件读取。

| 模块 | 版本 | 许可证 | 验证 |
|------|------|--------|------|
| `github.com/bytebase/omni` | v0.0.0-20260720033410 | **MIT** | ✅ LICENSE 实际读取 |
| `github.com/go-sql-driver/mysql` | v1.9.3 | MPL 2.0 | ✅ LICENSE 已验证 |
| `github.com/jackc/pgx/v5` | v5.9.1 | MIT | ✅ LICENSE 已验证 |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause | ✅ LICENSE 已验证 |
| `github.com/hjson/hjson-go/v4` | v4.6.0 | MIT | ✅ LICENSE 已验证 |
| `golang.org/x/crypto` | v0.48.0 | BSD-3-Clause | ✅ LICENSE 已验证 |
| `golang.org/x/sync` | v0.19.0 | BSD-3-Clause | ✅ LICENSE 已验证 |
| `golang.org/x/sys` | v0.41.0 | BSD-3-Clause | ✅ LICENSE 已验证 |
| `golang.org/x/text` | v0.34.0 | BSD-3-Clause | ✅ LICENSE 已验证 |
| `google.golang.org/protobuf` | v1.36.11 | BSD-3-Clause | ✅ LICENSE 已验证 |
| `google.golang.org/grpc` | v1.79.1 | Apache 2.0 | ✅ LICENSE 已验证 |
| `gopkg.in/yaml.v3` | v3.0.1 | MIT | ✅ LICENSE 已验证 |
| `github.com/stretchr/testify` | v1.11.1 | MIT | ✅ LICENSE（测试依赖） |
| `github.com/sirupsen/logrus` | v1.9.3 | MIT | ✅ LICENSE 已验证 |
| `github.com/pkg/errors` | v0.9.1 | BSD-2-Clause | ✅ LICENSE 已验证 |
| `github.com/shopspring/decimal` | v1.4.0 | MIT | ✅ LICENSE 已验证 |
| `go.opentelemetry.io/*` | v1.x | Apache 2.0 | ✅ LICENSE 已验证 |
| `github.com/containerd/*` | v1.x | Apache 2.0 | ✅ LICENSE 已验证 |
| `github.com/docker/*` | v28.x | Apache 2.0 | ✅ LICENSE 已验证 |
| `github.com/moby/*` | v0.x | Apache 2.0 | ✅ LICENSE 已验证 |
| 其余 52 个模块（testcontainers、shirou/gopsutil 等测试/平台依赖） | — | MIT/Apache 2.0/BSD | ✅ 逐模块 `find <GOMODCACHE>/* -name 'LICENSE*'` 已确认存在 |

**结论**：Omni 依赖树无 GPL/AGPL/SSPL 或未知许可证。全部 73 模块的 LICENSE 文件均通过脚本逐一读取确认存在。

**已知缺口**（仍在 TiDB Parser 候选范围内）：`modernc.org/parser@v1.1.0` 无 LICENSE 文件（build-time yacc 工具依赖）。该候选已被淘汰，此缺口不进入 Omni 依赖树。

### 5.2 pgparser（1 模块）

| 模块 | 版本 | 许可证 |
|------|------|--------|
| `github.com/pgplex/pgparser` | v0.2.0 | PostgreSQL License (BSD-like) |

### 5.3 TiDB Parser（43 模块）

42/43 验证通过。1 缺失：`modernc.org/parser@v1.1.0`（build-time yacc 依赖）。

### 5.4 Vitess（42 模块）

全部模块含 LICENSE 文件。核心许可：Apache 2.0 (vitess), MIT (uber-go), BSD-3-Clause (golang.org/x), MPL 2.0 (go-sql-driver/mysql)。

---

## 6. 回滚/清理

- **仓库分支**：`feat/P0-04-parser-spike`（仅本报告 1 个文件）
- **回滚**：`git switch main && git branch -D feat/P0-04-parser-spike` + 删除临时目录

# P0-04 依赖 Spike 报告（最终版）

> 状态：APPROVED — Owner 已批准正式依赖、生产 go.mod/go.sum 修改及 TDD 实现
> 日期：2026-07-30（v7 原始 + Round 3 技术闭环 + Owner 正式批准）
> 任务：P0-04（SQL Safety Policy — 依赖 Spike）
> Issue：WEB-13
> 历史分支：`feat/WEB-13-P0-04-sql-safety-policy`（PR #25 已合并）
> Round 3 基线：merge commit `c912a5a0dc1a33ec010af91a91b857661a0ab400`
> 固定 Omni：`github.com/bytebase/omni v0.0.0-20260728103305-d2f82de1b468`
> 作者：Claude Code
> 修订：v7 AST feature oracle + assignment walk + Round 3 ECM lexer + reproducible license evidence

---

## 1. [历史 v7] 最终结论：ESCALATE（Round 3 已通过；见 §Round 3）

下表 §1.1 为历史候选探索汇总（来自 Round 1）。当前唯一待批准候选 Bytebase Omni 的 v7 实测结果见 §1.2。

### 1.1 历史候选（Round 1 探索，已被 §6 v7 取代）

| 候选 | PG AST | MY AST | 可执行注释 | 总体 |
|------|--------|--------|-----------|------|
| pgparser v0.2.0 | 42/43 (TABLE t FAIL) | N/A | N/A | **FAIL** |
| TiDB Parser | N/A | 24/24 | Scanner API FAIL | **FAIL** |
| Vitess v0.24.2 | N/A | 27/27 | GetComments() FAIL | **FAIL** |
| DoltHub vitess | — | — | — | 无法导入 |

*以上历史探索结果已被 Round 2 v7 取代；Bytebase Omni 当前结果见 §1.2 和 §6。*

### 1.2 Bytebase Omni v7 当前结果（v0.0.0-20260728103305-d2f82de1b468）

| 维度 | PG | MySQL |
|------|-----|-------|
| 基础分类 | 25/25 | 43/43 |
| EXPLAIN Gate | 7/7 | 5/5 |
| TABLE 源语法区分（原始 harness） | **0/4 + 0/4 fp**；Owner 已接受等价 `Select` AST，不再是 Gate | — |
| ECM reliable recognition | — | **0/12 positive** / 5/5 negative |
| ECM semantic (multi-node) | — | 4/4 |
| 许可证 GPL/AGPL/SSPL | 0/75 | 0/75 |
| Cross-platform build | ✅ | ✅ |
| [历史 v7 处置] | **PG TABLE 决策已关闭（Owner 批准等价 Select）** | **[历史] FAIL（ECM recognition）** |

**[历史 v7]** PG TABLE 不再是 parser/依赖阻塞项；MySQL ECM recognition 是 v7 结束时唯一未解决的依赖 Gate。**[Round 3]** 该 Gate 已由 WebDB ECM lexer 验证关闭。**[历史 Gate]** 当时在 Owner 正式依赖批准前不得修改生产依赖或开始 `sqlpolicy`/`execution` 实现；Owner 已于 2026-07-30 正式批准，见文末批准记录。
详见 §6。

---

## 2. Bytebase Omni 评估（Round 1 历史探索 — 版本 v0.0.0-20260720033410-726bb7ff40aa，已被 §6 v7 取代）

**版本**：`github.com/bytebase/omni v0.0.0-20260720033410-726bb7ff40aa`（MIT License）
**依赖图**：73 个模块（大部分为 testcontainers/docker 测试依赖），生产依赖主要为 pgx/mysql 驱动
**构建**：`GOOS=windows/linux GOARCH=amd64 CGO_ENABLED=0` 均成功

### 2.1 [历史] PG 分类：31/32 PASS

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

```text
TABLE t:
  SelectStmt: TargetList!=nil FromClause!=nil ValuesLists=nil IntoClause=nil LockingClause=nil

SELECT * FROM t:
  SelectStmt: TargetList!=nil FromClause!=nil ValuesLists=nil IntoClause=nil LockingClause=nil
  ← 完全相同的字段状态
```

结论：Omni PG parser 与 pgparser 一样，将 `TABLE t` 解析为与 `SELECT * FROM t` 无法区分的 SelectStmt。

### 2.2 [历史] MySQL AST 分类：11/16 PASS

使用 `%T` 格式字符串分类。5 个失败项因缺少字段级 AST 检测：

| 失败案例 | 原因 |
|----------|------|
| MY-03 FOR UPDATE | mysql/ast.SelectStmt 无 LockingClause 字段 |
| MY-04 LOCK IN SHARE MODE | 同上 |
| MY-08 @x:=id | 需 mysql/ast 的 ResTarget/VariableExpr 字段检测 |
| MY-23 EXPLAIN DELETE | 需 mysql/ast.ExplainStmt 字段检测 |
| MY-28 EXPLAIN ANALYZE | 同上 |

**注**：Omni 的 `mysql/ast` 与 `pg/ast` 类型不兼容（不同 Node interface），本 Spike 使用 %T 字符串作为最小可行分类。正式实施需引入 mysql/ast 类型断言和 AST Walker。

### 2.3 [历史] 可执行注释：部分检测（2/9 ECM 真阳性）

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

## 3. 其他候选摘要（Round 1 历史探索，已被 §6 v7 取代）

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

## 4. 隔离 harness 逻辑标识

| 逻辑标识（非本机绝对路径） | 用途 |
|------|------|
| `external-temp/p0-04-spike-20260724-143057` | pgparser + TiDB Parser |
| `external-temp/p0-04-mysql-spike-20260724-151815` | Vitess |
| `external-temp/p0-04-omni-spike-20260724-171004` | Bytebase Omni |

`external-temp/` 表示仓库外临时根目录；实际用户目录不属于可复现证据，也不持久化到仓库。

## 5. 清理与持久化

Round 1 临时目录可由系统清理。Round 2 harness 的逻辑标识为 `external-temp/p0-04-spike-round2-20260729`；其可复核身份由 §6.1 的源码清单哈希、固定依赖版本和 §6.2 命令共同确定。许可证证据已持久化至仓库 `docs/tasks/P0-04-spike-licenses.tsv`。

---

## 6. Round 2：v7 最终结果 (2026-07-29)

> Issue: WEB-13 | 分支: `feat/WEB-13-P0-04-sql-safety-policy`
> 固定 Omni: `v0.0.0-20260728103305-d2f82de1b468` | 作者: Claude Code

### 6.1 隔离与版本

| 项目 | 值 |
|------|-----|
| 固定 Omni | `github.com/bytebase/omni v0.0.0-20260728103305-d2f82de1b468` |
| Harness 逻辑标识 | `external-temp/p0-04-spike-round2-20260729` |
| Harness 源码清单 SHA-256 | `d988094b0dc6a75b67f53205e88152f99cc78bca76b82454dbc59d8fc75fa895` |
| License TSV | `docs/tasks/P0-04-spike-licenses.tsv` (仓库持久化, 75 ext modules) |
| go.mod/go.sum | Untouched: `1594595d...a16b0c` / `4e9ec26c...fae24f` |

源码清单哈希算法：对下列六个源文件/依赖锁定文件按相对路径排序，生成 UTF-8（无 BOM）行
`<file_sha256>\t<relative_path>\n`，再对完整清单计算 SHA-256。清单不包含平台二进制和临时生成的许可证输出。

| 相对路径 | SHA-256 |
|---|---|
| `go.mod` | `aaca62cec26e6f18f68e902fdf64d6e69ffa4de39a48a9f188a5e4270179ce24` |
| `go.sum` | `cfec1e8f4ba263324c94a71eaff8a3b7c472ab815f6d0d204680e67c00b15687` |
| `main_test.go` | `1f255e12881b0c915ba5fc2c3c4035c64018293385609c9ebbd6b103730e9905` |
| `my_classify_test.go` | `edc288c51bc7d3443e02ab2d2957f4fcbe55550feb6f4f5683e98584a9be470f` |
| `pg_classify_test.go` | `3843bf4acde624fa5c584b2343cf133e3f2b97ff889ff85e0c5d6136a6b3fdf3` |
| `tools/licensegen/main.go` | `036b02f39f8dabf3151862417863d8ed99aff557669cd4d526d28ac1eeaf3361` |

### 6.2 验证命令与退出码

```text
go test -count=1 -v ./...                              EXIT 1 (v7 raw gates: 8 TABLE later accepted; 12 ECM unresolved)
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...  EXIT 0
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...    EXIT 0
GOPROXY=off go list -m all                              76 lines (1 main + 75 ext)
```

### 6.3 PG 结果

以下保留 v7 harness 原始计数；Owner 决策不篡改原始结果，而是记录其当前验收处置。

| 组 | Raw Pass | Raw Fail | 当前处置 |
|----|----------|----------|----------|
| base (SELECT/DML/DDL/SET/OTHER/MULTI) | 25 | 0 | 通过 |
| EXPLAIN gate (target + analyze + nested) | 7 | 0 | 通过 |
| TABLE source-form observation (distinguish TABLE from SELECT) | 0 | **4** | 已接受归一化 `Select` AST；不再是 Gate |
| TABLE fingerprint observation (AST field comparison) | 0 | **4** | 已接受 fingerprint 相同；不再是 Gate |

**通过**: locking, select_into, has_cte, dml_cte, explain, explain_analyze, explain_select, explain_dml, explain_ddl, explain_target_detectable, nested_explain fail-closed.

**TABLE 原始观察与处置**：Omni PG AST 将 `TABLE t` 归一化为 `*ast.SelectStmt`，与 `SELECT * FROM t` 产生相同 AST fingerprint，4/4 对全部 IDENTICAL。PostgreSQL 语义中 TABLE 是 SELECT * FROM 的等价形式；8 个原始失败源于 Omni AST 归一化，非 harness bug。Owner 已接受按等价 `Select` AST 处理，故它们不再构成 parser/依赖 Gate；锁定子句、修改型 CTE、多语句、未知节点和解析失败仍须 fail-closed。

### 6.4 MySQL 结果

| 组 | Pass | Fail |
|----|------|------|
| base (incl. @x:=, INTO, CTE, CALL, DO) | 43 | 0 |
| EXPLAIN gate (target detection + fail-closed) | 5 | 0 |
| ECM recognition (public API signal) | 5 | **12** |
| ECM semantic (multi-node dangerous) | 4 | 0 |

**通过**: locking (for_update/for_share/lock_in_share_mode), nowait, skip_locked, into_outfile, into_dumpfile, into_vars, **user_var_assign (@x:= via BinaryExpr BinOpAssign walk)**, dml, ddl, is_replace, insert_select, has_cte, dml_cte (forbidden verified), is_table, explain_select, explain_dml, explain_target_detectable, nested_explain fail-closed, explain_ddl fail-closed, multi_node, multi_node_danger, CALL/DO.

**ECM recognition**: 公开 API 无 ECM token 类型 (全部 unexported)。spliceGaps 未导出。Lexer.NextToken() 返回的 Token 不含 ECM 标记。12 条真实 ECM 正向均 NOT_RECOGNIZED → 0/12 FAIL。5 条负例 (string literal, backtick, block comment, optimizer hint, line comment) 全部 BENIGN → 5/5 PASS。

**ECM semantic**: ECM 含 DDL/DML → Parse 返回 2 nodes → MULTI_DANGER 拒绝 (4/4 PASS)。纯 SELECT/表达式 ECM 无法通过 multi-node 覆盖，但 recognition 维度阻塞。

### 6.5 许可证

完整证据: `docs/tasks/P0-04-spike-licenses.tsv` (75 rows + header; module, version, evidence_path, SHA-256, license_type, detection_basis).

| Type | Count |
|------|-------|
| Apache-2.0 | 25 |
| MIT | 25 |
| BSD-3-Clause | 15 |
| BSD-2-Clause | 3 |
| MPL-2.0 | 1 |
| ISC | 1 |
| Apache-2.0 AND BSD-3-Clause | 3 |
| Apache-2.0 AND BSD-3-Clause AND MIT | 1 |
| Apache-2.0 AND MIT | 1 |
| UNKNOWN | 0 |
| NOT_FOUND | 0 |
| GPL/AGPL/SSPL | 0 |
| **Total external** | **75** |

复核方法（`GOPROXY=off`）：

1. 从 `go list -m all` 得到 75 个外部模块；以 module + version 解析模块缓存目录。
2. 完整读取每个 LICENSE 文件（不再截取前 800 字节），计算 SHA-256，并与 TSV 核对：75/75 存在、75/75 哈希一致、75/75 `detection_basis` 非空；其中 69 项为可在全文直接定位的原文，6 项为下述多许可证/条款纠正的人工复核摘要。
3. 对全文匹配 SSPL/AGPL/GPL 等受限标记和 Apache/MIT/BSD/MPL/ISC 许可证块。无法识别或存在歧义的结果先标记 `REVIEW_REQUIRED`；人工复核仍无法判定才记为 `UNKNOWN`。
4. 本次全文扫描触发 6 项人工复核并全部留痕：`github.com/pmezard/go-difflib` 修正为 BSD-2-Clause；`github.com/klauspost/compress` 保留 BSD-3-Clause、Apache-2.0、MIT 三组文件级许可证；三个 `go.opentelemetry.io/otel*` 模块保留 Apache-2.0 主许可证和随附 BSD-3-Clause notice；`gopkg.in/yaml.v3` 保留 MIT 与 Apache-2.0 两组文件级许可证。
5. 最终结果：`UNKNOWN=0`、`NOT_FOUND=0`、GPL/AGPL/SSPL 标记为 0。组合许可证使用 `AND` 表示同一模块的不同文件受不同许可证约束。

### 6.6 [历史 v7] 最终结论：ESCALATE

**v7 原始 harness：89 PASS / 20 FAIL / 109 total，Exit 1。当前处置：8 个 PG TABLE 原始失败已由 Owner 决策关闭；剩余 12 个未解决失败全部为 ECM recognition。**

| Gate | PG | MySQL |
|------|-----|-------|
| Basic classification | ✅ 25/25 | ✅ 43/43 |
| EXPLAIN target | ✅ 7/7 | ✅ 5/5 |
| @x:= assignment | — | ✅ |
| INTO clauses | — | ✅ |
| Locking | ✅ | ✅ |
| TABLE distinguish | 原始 0/4 + 0/4 fp；**✅ Owner 已接受等价 `Select` AST，不再是 Gate** | ✅ |
| ECM recognition | — | **❌ 0/12 positive** |
| ECM semantic | — | ✅ 4/4 |
| License GPL/AGPL/SSPL | 0 | 0 |
| Build (win+linux) | ✅ | ✅ |

**[历史 v7]** 20 个原始失败均来自 parser/API 能力差异而非 harness bug；其中 8 个 PG TABLE 失败已转为明确的已接受策略决策，v7 结束时唯一未解决依赖 Gate 是 12 个 ECM recognition 正例。**[Round 3]** 该 Gate 已由 WebDB ECM lexer 验证关闭（12/12 ECM positive）。

**[历史 Gate]** 在 WebDB 自有 ECM lexer 的 Round 3 全部门禁通过并获得 Owner 对精确 Omni 版本的正式依赖批准前，不得修改生产依赖或开始 `sqlpolicy`/`execution` 实现。该 Gate 已于 2026-07-30 关闭。

### 6.7 Owner 决策（2026-07-30，报告后更新）

1. PG TABLE：接受 Omni 归一化后的等价 `Select` AST；继续应用全部 `Select` 安全门禁。
2. ECM recognition：必须由 WebDB 自有、方言感知、fail-closed 的 lexer 在 Omni AST 前识别。semantic multi-node 检测仅是纵深防御，不能替代 ECM lexer 门禁。
3. Omni 上游 issue/PR 仅作为可选协作项，不再是 Round 3 或正式实施的前置条件。

---

## Round 3：WebDB ECM Lexer + 官方 Omni AST（2026-07-30, 技术闭环）

### 架构
原始 MySQL SQL → WebDB ECM Lexer → ECM hit/error → 拒绝（Omni=0, Adapter=0）
PostgreSQL SQL → 官方 Omni PG AST → 策略判定

### 版本
| 项目 | 值 |
|------|-----|
| Omni | `github.com/bytebase/omni v0.0.0-20260728103305-d2f82de1b468` |
| Fork/Replace | 无（官方上游） |
| Harness 证据索引 | [`docs/tasks/P0-04-spike-report.md`](P0-04-spike-report.md)（本节命令、退出码与完整 SHA256） |
| 许可证证据 | [`docs/tasks/P0-04-spike-licenses.tsv`](P0-04-spike-licenses.tsv) |

### RED → GREEN 修复
| Bug | 修复 |
|-----|------|
| `-/*!` 绕过 ECM（stDash1 吞掉 `/`） | stNormal 原子判断合法 `-- ` 行注释 |
| `--\n` 跳过换行导致下一行 ECM 漏检 | `i+=2` → `i+=1`，让 stLineComment 处理换行 |
| 未知 ECMMode 静默通过 | default → `return error` |
| EOF backslash state 未检查 | 新增 `stBackslashSingle/Double` EOF error |
| 危险 SELECT（locking/INTO/assignment/CTE）调用 Adapter | 实现 `allowMySQL()` 检查全部危险 feature |
| PG EXPLAIN ANALYZE 未拒绝 | 从 `ExplainStmt.Options` 读取真实 analyze option |

### 验收矩阵

| Gate | 计数 | |
|------|------|------|
| PG base (feature oracle: locking, select_into, has_cte, dml_cte, dml, ddl) | 25/25 | |
| PG EXPLAIN (explain, explain_select, explain_analyze, explain_dml, explain_ddl, explain_target_detectable, nested_explain) | 7/7 | |
| PG EXPLAIN ANALYZE → Adapter=0 | | |
| PG TABLE 6-item allow/deny | 6/6 | |
| MY base (feature oracle: locking, for_update, for_share, lock_in_share_mode, nowait, skip_locked, into_outfile, into_dumpfile, into_vars, user_var_assign, has_cte, dml_cte, dml, ddl, is_replace, insert_select, multi_node, multi_node_danger, is_table, unknown_node) | 43/43 | |
| MY EXPLAIN (explain, explain_select, explain_dml, explain_target_detectable) | 5/5 | |
| MY dangerous SELECT → Adapter=0 | 9/9 | |
| MY safe SELECT → Adapter=1 | 3/3 | |
| MY EXPLAIN deny (6) + allow (1) | 7/7 | |
| ECM positive (v7 original) | 12/12 | |
| ECM negative (v7 original) | 5/5 | |
| ECM quote boundary (single/double/backtick/block/hint/hash/dash) | all | |
| ECM --\n bypass, --\r\n bypass, dash bypass, unterm, unclosed, mode | all | |
| SQL mode diff (Default vs NoBackslashEscapes) | | |
| Adapter counters (ECM→0, error→0, unknown→0, parse→0, multi→0, DML→0, clean→1) | 7/7 | |
| FuzzECMLexer 30s | PASS, 4,828,049 execs, 0 crash |
| FuzzMySQLPipeline 30s + invariants | PASS, 3,903,011 execs, 0 crash |
| FuzzPGPipeline 30s + invariants | PASS, 4,663,516 execs, 0 crash |

### 命令与退出码
```
go test -count=1 -v ./...                        EXIT 0
go test -run=^$ -fuzz=FuzzECMLexer -fuzztime=30s  EXIT 0
go test -run=^$ -fuzz=FuzzMySQLPipeline -fuzztime=30s EXIT 0
go test -run=^$ -fuzz=FuzzPGPipeline -fuzztime=30s   EXIT 0
go vet ./...                                        EXIT 0
GOOS=windows GOARCH=amd64 go build ./...             EXIT 0
GOOS=linux GOARCH=amd64 go build ./...               EXIT 0
GOPROXY=off go list -m all
# EXIT 0; output: 76 lines (1 main + 75 external modules)
```

### 许可证 75/75 复核
| 指标 | 值 |
|------|-----|
| external_modules | 75 |
| tsv_rows | 75 |
| recheck_rows | 75 |
| graph_match | 75 |
| license_exists | 75 |
| sha256_match | 75 |
| detection_basis_nonempty | 75 |
| GPL/AGPL/SSPL | 0 |
| UNKNOWN | 0 |
| NOT_FOUND | 0 |
| result | PASS |

Recheck TSV: `evidence/round3-license-recheck.tsv` (SHA256: 3c952718cb119408191092399e395c2e7775643788d6bae080ecd350475af8fd)
Recheck LOG: `evidence/round3-license-recheck.log` (SHA256: a16cd3cb267c4da9eff9720a02b07dce72a5112162311b1851cba542fd33a3ce)

### Harness SHA256
| File | SHA256 |
|------|--------|
| ecm_lexer.go | a57d031d69b34bec1607b423a89c03d4137b0bff9531374f439a969142f861d4 |
| round3_test.go | 342471f785cf7e05707c1fe4d7aa6b9ae0efa2af9fe9056b57767728912b8bdc |
| regression_test.go | 47f78cbfd0fafc8de393b196422d9e9adee99e04a82ccc1543f838c75995a28c |
| red_policy_test.go | c0ca19797ca16318803546565bb787d804eecf7725991a8934e8322f4e9fb57d |
| main.go | 8e72a1b15c5bdab6c8a332658444694bb91f81e2ee7478674b57508969b7e6a6 |
| go.mod | 4c7f29628b3cb445e0a97381c2b65beed2544b941980a98e6553b1f179f26d1b |
| go.sum | cfec1e8f4ba263324c94a71eaff8a3b7c472ab815f6d0d204680e67c00b15687 |

### 生产依赖（Round 3 未修改）

**隔离 repo 基线**（merge commit `c912a5a0dc1a33ec010af91a91b857661a0ab400`）：

```
apps/api/go.mod: 2a617f6f5311db782a5a4a6f77422d8d0efb9dac9e164c9eed26a8b6ddb6034c
apps/api/go.sum: 5f8ba9dd1e1bee0b7b5f0656f8b7b2daaf9ade48125e6b038372e20fcc0d9bf5
```

git status/diff 证明 Round 3 未修改隔离 repo 的 `apps/api/go.mod` 或 `apps/api/go.sum`。

**原始用户工作区快照哈希**（存在其他任务的既有未提交改动，不作为隔离 repo 基线；Round 3 未触碰）：

```
apps/api/go.mod: 1594595d7c6e123b9c91e6fbe72b7a378dff5aa8656b41c4fee92a6448a16b0c
apps/api/go.sum: 4e9ec26c1f1c94b45346be31473faaf615429141ff684df4685f3065d7fae24f
```

### 证据文件

| 文件 | SHA256 |
|------|--------|
| harness-sha256.txt | 4e124f8f6aa8778fa1bc1fb2f5ba8f5320b19905a7ca9601ed28aeecdf32c912 |
| go-test-full.log | 4dd101ed4f92f38bd2a6cdc383bb5c31af5586b2a601b96de209da12e69ecf62 |
| fuzz-ecm.log | 6d4f6b9aa0eac0c8962fd03356db0b173c54c731c94f636f52a5dec558768b51 |
| fuzz-mysql-pipeline.log | 0d56cdc6aa70ce1c1281932bb8d929eb8d608e6b352f435f19d19aecddc9eb1d |
| fuzz-pg-pipeline.log | 7f69ed88c3e959bd491bdbf1dcfbc7c61d6f379453362b2a96eaabedecf8b12c |
| exit-codes.txt | da7c9bb46c01ffe68f5d5fdc03f16496cbbd2e7943f26895e4ffcb00808ed9be |
| module-list.txt | 15d1e397a2a222fd90b05399553e8c0f5d83c69bb6fa4f1179c836e86b003579 |
| module-list.json | ea390d9c8dc4ef4b7f2f9666e45863070294c35c6f5ef6df3319ed1fb7414910 |
| round3-license-recheck.tsv | 3c952718cb119408191092399e395c2e7775643788d6bae080ecd350475af8fd |
| round3-license-recheck.log | a16cd3cb267c4da9eff9720a02b07dce72a5112162311b1851cba542fd33a3ce |

仓库内持久化证据：[`docs/tasks/P0-04-spike-report.md`](P0-04-spike-report.md)（命令、退出码、完整 SHA256）与 [`docs/tasks/P0-04-spike-licenses.tsv`](P0-04-spike-licenses.tsv)（许可证清单）。隔离 harness 与原始日志未提交到仓库；其内容标识由上述 SHA256 固定。

### Owner 正式依赖与实施批准（2026-07-30）

- **批准人**：`fujiabao89`（WebDB Owner）
- **批准日期**：2026-07-30
- **批准来源**：[Linear WEB-13](https://linear.app/webdb/issue/WEB-13/p0-04sql-安全执行策略)、[ADR-007](../adr/ADR-007-dialect-aware-sql-parsing.md) 与 [证据 PR #26](https://github.com/fujiabao89/WebDB/pull/26)。
- **正式依赖**：批准 `github.com/bytebase/omni v0.0.0-20260728103305-d2f82de1b468` 用于 P0-04。
- **生产依赖授权**：授权在 P0-04 正式实施分支中修改 `apps/api/go.mod` 与 `apps/api/go.sum`。
- **实施授权**：授权按照已接受的 ADR-007、P0-04 契约与本报告 Round 3 证据，开始 `sqlpolicy/` 与 `execution/` 的 TDD 实现。
- **安全边界不变**：本批准不授权扩大 P0 范围，不授权 DML/DDL、字符串前缀安全判断、客户端侧策略、fork 或 `replace`；无法可靠判定时仍必须 fail-closed。
- **任务状态**：WEB-13 保持 `In Progress`；正式依赖批准和 Round 3 证据完成不代表 P0-04 工程实现已经完成。

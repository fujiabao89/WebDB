# ADR-007：按方言解析 SQL，未知即拒绝

> 状态：已接受｜日期：2026-07-11｜修订：2026-07-30｜批准人：`fujiabao89`

## 背景

字符串前缀或共同 SQL 子集无法可靠阻止多语句、注释和方言绕过。

## 决策

PostgreSQL/MySQL 分别使用对应 AST 解析器；只允许可可靠判定的单条 `Select`/`EXPLAIN` AST，无法判定默认拒绝。

PostgreSQL `TABLE` 可被解析器归一化为与 `SELECT * FROM ...` 等价且无法区分的 `Select` AST。此时按 `SELECT` 同等策略处理，不要求从 AST 恢复或区分源语法；只有在该 `Select` AST 通过全部安全门禁时才允许执行。

该决定不扩大其他允许范围。锁定子句、修改型 CTE、多语句、未知 AST 节点和解析失败继续 fail-closed；顶层 `VALUES` 仍拒绝。

MySQL 使用两层安全边界：

1. 在 AST 解析前，由 WebDB 自有的 MySQL 方言 lexer 识别可执行注释。该 lexer 必须是确定性的词法状态机，能够区分 SQL 代码、字符串字面量、引号标识符、普通块注释、优化器 hint、`#` 行注释及满足 MySQL 空白/控制字符规则的 `--` 行注释；不得以字符串前缀、正则或无上下文的原始子串扫描替代。任何处于可执行 SQL 上下文的 `/*!...*/`，无论版本号是否生效、是否合法或是否闭合，均必须标记并拒绝。影响词法行为的 session mode 必须来自服务端可信连接/session 配置；mode 未知、lexer 出错或无法可靠判定时默认拒绝。
2. 只有 lexer 明确未发现可执行注释后，才调用固定精确版本、未修改且来自 `github.com/bytebase/omni` 官方上游的对应方言 AST parser。AST 继续负责语句数量、顶层类型、锁定、赋值、修改型 CTE、`EXPLAIN` 目标及未知节点等裁决。Omni 是否公开可执行注释标记不再构成依赖门禁；生产依赖不得通过 fork 或 `replace` 隐式替换，除非另行取得 Owner 批准。

## 后果

解析、分类和拒绝案例必须有两种引擎的回归与模糊测试。PostgreSQL 回归测试必须覆盖 `TABLE t` 按等价 `Select` AST 放行，以及 `TABLE`/`SELECT` 携带锁定子句、修改型 CTE、多语句、未知节点或解析失败时拒绝。

MySQL 回归与模糊测试必须分别验证 lexer 和 AST parser，并验证两者的调用顺序。lexer 测试至少覆盖可执行注释正例、字符串/标识符/普通注释/hint/行注释反例、反斜杠及引号模式、控制字符、未知版本、未闭合输入和极端长度；任何 lexer 错误、可执行注释或 AST 失败都不得访问目标数据库。固定 Omni 精确版本、跨平台构建和完整许可证清单须在正式依赖批准前由隔离 Spike 证明。DML/DDL 留给独立策略路径。

## 修订记录

| 日期 | 批准人 | 内容 |
|---|---|---|
| 2026-07-30 | `fujiabao89` | PostgreSQL `TABLE` 改为按等价 `Select` AST 处理；保留全部 fail-closed 安全门禁。 |
| 2026-07-30 | `fujiabao89` | MySQL 改为 WebDB 自有方言 lexer 前置识别可执行注释，随后使用官方未修改 Omni AST；Omni 上游 ECM API/PR 不再是实施前置条件。 |

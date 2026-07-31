# P0-04：SQL 安全执行策略

> 状态：Done｜风险：High｜依赖：P0-03、ADR-005、ADR-007、ADR-008｜实现者：Claude Code｜独立审查：Codex + CodeRabbit + Qodo

## 完成信息

| 项目 | 详情 |
| --- | --- |
| 完成日期 | 2026-07-31 |
| 工程 PR | [#27](https://github.com/fujiabao89/WebDB/pull/27) |
| 工程 Merge commit | `64be9bb` |
| 文档收尾 PR | [#28](https://github.com/fujiabao89/WebDB/pull/28) |
| 实现分支 | `feat/WEB-13-sql-safety-implementation` |
| 审查轮次 | 6 轮 CodeRabbit + 1 轮 Qodo（28 条评论，0 条未解决） |
| 实际包 | `apps/api/internal/sqlpolicy/`、`apps/api/internal/execution/` |

## 目标与范围

在 Adapter 调用之前建立服务端 SQL 准入策略：按 PostgreSQL/MySQL 方言 AST 分类，默认仅放行可可靠判定的单条 `SELECT`/`EXPLAIN`，并应用超时、最大行数、并发与取消策略。

不实现 DML/DDL 审批、字符串前缀安全判断或客户端侧策略。

## 验收标准

| 验收项 | 实际证据 |
| --- | --- |
| 多语句、危险语句、解析错误和未知类别均被拒绝 | PG 59 用例 + MySQL 49 用例表驱动测试，全部 PASS（`go -C apps/api test ./internal/sqlpolicy/`） |
| 注释、字符串、大小写和方言绕过不能误放行 | ECM lexer 12 positive + 9 negative + 6 boundary 用例，fuzz 30s 无新路径（证据见 `docs/tasks/evidence/P0-04-round3/`） |
| MySQL ECM 可执行注释 AST 前检测拒绝 | `ecm_lexer.go` 确定性状态机（O(n)/O(1)），`/*!...*/` 在 parser 前检测 |
| PG 危险函数检测 | `dangerousPGFuncs` 13 个函数（setval/nextval/lo_*/lowrite/lo_truncate/lo_truncate64），`fcBaseName` 忽略 schema 限定 |
| MySQL `:=` 赋值检测 | 递归覆盖 TargetList/WHERE/HAVING/GROUP BY/ORDER BY/FROM/JOIN ON/FuncCallExpr.Args/CaseExpr/派生表/嵌套 JOIN |
| `ANSI_QUOTES` / `NoBackslashEscapes` fail-closed | `policy.go` 检测非默认 mode 后返回 `ReasonParseError` |
| 方言 AST 未知/解析失败默认拒绝 | `ReasonUnsupported` / `ReasonParseError`，不使用字符串前缀匹配 |
| ECM 或拒绝时 Adapter 调用次数为 0 | `execution/service.go`: `EvaluateSQL` 仅 `Allowed=true` 时返回空 error code |
| SQL 策略错误有稳定错误码 | `StableErrorCode` 映射 SQL 策略拒绝原因: `sql_parse_error` / `multiple_statements` / `statement_not_allowed` / `unsupported_statement` / `empty_sql` / `executable_comment_detected`；超时和取消由 Adapter 层的 `query_timeout` / `query_cancelled` 处理，最大行数通过 keyset 分页限制 |
| 审计脱敏 | `sanitizeAuditMetadata` 扩展 6 字段 + 类型校验（`audit_sanitize_test.go` 26 用例） |
| PG `TABLE` 按等价 SELECT AST 处理 | `classifyPGAST`: `*pgast.SelectStmt` → `classifyPGSelect`，含 FOR UPDATE 等功能检测 |
| 固定 Omni 版本 + 许可证 | `v0.0.0-20260728103305-d2f82de1b468` (MIT)，记录于 `docs/DEPENDENCY-LICENSES.md` |

## 非目标（仍未改变）

- 不支持 DML/DDL。
- 不注册公开 HTTP 业务路由；ExecutionService 仅供内部 Go 调用。
- 不把 SQL 安全判断下放到客户端；`MySQLLexerMode` 从服务端可信连接配置派生。
- 不依赖字符串前缀或正则作为 AST 安全边界。
- `docs/tasks/evidence/P0-04-round3/harness/` 是依赖与设计验证证据，不是生产实现。

## 验证命令与结果

Go module 位于 `apps/api`，以下命令需从仓库根目录执行。

```bash
# 单元测试
go -C apps/api test ./internal/sqlpolicy/   # PASS (PG 59 + MySQL 49 + ECM 12/9/6)
go -C apps/api test ./internal/execution/   # PASS (13 用例)
go -C apps/api test ./internal/metadata/    # PASS (26 审计脱敏用例)

# 静态检查
go -C apps/api fmt ./...                    # 无输出
go -C apps/api vet ./...                    # 无输出

# 构建（Win + Linux）
cd apps/api && CGO_ENABLED=0 GOOS=windows go build ./...   # PASS
cd apps/api && CGO_ENABLED=0 GOOS=linux go build ./...     # PASS

# CI (commit 64be9bb, run 2026-07-31)
Web / Contracts / API / Detect / Safety / Contract checks 全部 success
```

Fuzz 测试、退出码、许可证复核等完整证据见 [`docs/tasks/evidence/P0-04-round3/`](evidence/P0-04-round3/README.md)。

## 残余风险

| 风险 | 缓解措施 | 后续任务 |
| --- | --- | --- |
| `SELECT func()` 副作用（用户自定义 `SECURITY DEFINER`） | AST 仅按名单匹配，不覆盖自定义函数 | P0-05 执行层加只读事务 |
| Omni MySQL parser 不支持 mode-aware 解析 | `ANSI_QUOTES` / `NoBackslashEscapes` 时 fail-closed 拒绝 | Omni 支持后移除 |
| 函数名大小写绕过 | `strings.ToLower` 归一化 | — |

## 回滚

- **回滚 P0-04 工程实现（PR #27）**：`git revert -m 1 64be9bb`——会移除全部 SQL policy/execution/审计脱敏代码。
- **回滚本文档更新（PR #28）**：`git revert <PR-#28-merge-commit>`——仅撤销文档状态同步，不影响代码。

# WEB-44：可信 MySQL sql_mode 与 SQL policy fail-closed

> 状态：本地实施与独立审查完成，待提交/PR/CI/合并｜风险：High（P1）｜Owner：shelly allen｜期限：2026-08-15

## 目标与非目标

修复生产管线把未验证的 `MySQLLexerMode{}` 传给 SQL policy、随后在实际 mode 未知的 MySQL session 上执行用户 SQL的问题。每次执行必须从同一条池化连接读取并验证 `@@SESSION.sql_mode`；读取失败、未知、不支持或与 policy 不一致时，在用户 SQL 到达 `QueryContext` 前 fail-closed。

本任务不改变 API/Schema、AST/ECM parser、连接池上限、目标数据库全局或 session mode、DML/DDL 范围、ruleset 或 required checks。

## Owner 决策

2026-08-13 用户明确授权“自行修正”。选择读取并验证实际 session mode，不向目标数据库执行 `SET sql_mode`：

1. Policy 与 Adapter 共用 `sqlpolicy.SupportedMySQLLexerMode()` 作为唯一支持模式。
2. `beginReadOnlyMySQL` 在同一 `*sql.Conn` 上先读取 `@@SESSION.sql_mode`，再检查 autocommit、开启只读事务并执行目标查询。
3. 每次从池获取连接后都重新验证；失败时销毁连接，不归还池复用。
4. MySQL 8.0+ 已知且不改变解析语义的 mode 可投影为支持模式；`ANSI_QUOTES`、`NO_BACKSLASH_ESCAPES`、其他不支持语法 mode、未知 token 与畸形值均拒绝。

## 实施与测试证据

- `internal/sqlpolicy/mysql_mode_test.go`：默认 mode、大小写/空白、两个词法 mode、未知 token、空 token与不支持语法 mode。
- `internal/adapter/rollback_fault_test.go`：可控 driver 证明 mode 不一致、未知或读取失败时目标 SQL 调用次数为 0，且连接被销毁；错误消息不泄露驱动原始信息。
- `internal/adapter/readonly_integration_test.go`：真实 MySQL、单连接池复用场景覆盖 `NO_BACKSLASH_ESCAPES`、`ANSI_QUOTES` 及危险输入 `SELECT 'test\\' /*!50000' FROM t`。
- 默认 mode 的成功查询与连接复用回归继续通过。

## 验收矩阵

| 验收项 | 状态 | 证据 |
| --- | --- | --- |
| Owner 选择可信 mode 方案 | 通过 | Linear WEB-44 的 2026-08-13 实施启动评论；本文件“Owner 决策” |
| 生产不再使用未验证零值字面量 | 通过 | `cmd/server/main.go` 使用 `SupportedMySQLLexerMode()`；Adapter 在同一 session 逐次验证 |
| 池化 session 复用后仍验证 | 通过 | `TestReadOnlyBoundary_MySQL_NonDefaultSQLModeRejectedBeforeQuery` 使用 `MaxOpen=1/MaxIdle=1` 设置并复用同一 session |
| 未知/不支持/读取失败 fail-closed | 通过 | `TestExecMySQLSessionModeRejectedFailsClosed`、`TestExecMySQLSessionModeReadFailureFailsClosed`；目标查询计数为 0，连接销毁 |
| 真实 MySQL 覆盖两个词法 mode 与危险 SQL | 通过 | MySQL integration test 两个子用例均 PASS |
| 默认查询、分页、取消、超时、只读回归 | 通过 | 完整 Adapter/Browse integration suite PASS |
| CodeQL alert 复核与处置 | 待 Owner | alert 保持 open；待 PR/CI 和独立审查证据齐全后决定 |
| 独立 Reviewer | 通过 | 独立 Reviewer 结论 `APPROVE`（高置信度），未发现 P0–P3 可操作问题 |

## 验证命令与原始结果

```text
cd apps/api

go test ./internal/sqlpolicy ./internal/adapter ./internal/execution ./cmd/server -count=1
ok github.com/fujiabao89/webdb/internal/sqlpolicy 0.984s
ok github.com/fujiabao89/webdb/internal/adapter 0.724s
ok github.com/fujiabao89/webdb/internal/execution 0.865s
ok github.com/fujiabao89/webdb/cmd/server 7.937s

gofmt -l .
(no output)

go vet ./...
(no output)

go test ./... -count=1
all 14 packages: ok

go test -tags=integration -p=1 ./internal/adapter/... ./internal/browse/... -count=1
ok github.com/fujiabao89/webdb/internal/adapter 9.270s
ok github.com/fujiabao89/webdb/internal/browse 1.280s

go test -tags=integration -p=1 ./internal/adapter -run 'Test(NextPage_MySQL_FullPagination|Timeout_MySQL|Cancel_MySQL|ReadOnlyBoundary_MySQL_(Success|ConnectionReuse))$' -count=1 -v
PASS: timeout + recovery, cancel + recovery, full pagination (6 rows/no duplicates), read-only success, connection reuse

go test -race ./internal/sqlpolicy ./internal/adapter ./internal/execution -count=1
not run: go: -race requires cgo; local Windows environment has CGO_ENABLED=0 and gcc is not installed
```

## 风险与回滚/前向修复

- 当前安全策略会拒绝 parser 尚未证明 mode-aware 的语法 mode；这可能降低部分非默认 MySQL 部署的可用性，但符合“无法可靠判定即拒绝”。
- 回滚整个改动会重新打开 CodeQL 暴露的 mode 不一致风险，不应作为生产回滚方案。若出现兼容性问题，应保持门禁并前向扩展经过测试的 mode-aware lexer/AST，再更新 ADR。
- CodeQL alert #1 保持 open，只有独立审查、CI 与修复数据流证据齐全后，才由授权 Owner 决定是否关闭或 dismiss。

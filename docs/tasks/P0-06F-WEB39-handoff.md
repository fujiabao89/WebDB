# WEB-39 / P0-06F 交接文档：Compose E2E、安全回归、视觉验收与交接

> 状态：**进行中**（累计分页契约、F1、order_by 控件、D16 双引擎+越权、C5 黑盒、Windows 视觉基线均已修复/取证；剩余 Linux Chromium/Ubuntu 视觉基线为外部硬阻塞，另 TTL/panic/generation 仅 Go 测试级）
> 日期：2026-08-14｜分支：`feat/WEB-39-compose-e2e-acceptance`｜基线：`0ccdfdd0689aa94f8da42617f6c032d489f8997b`（upstream/main）
> worktree：`项目开发3-WEB-39`（本仓库的独立 git worktree）

## 1. 本轮修复（响应 REQUEST CHANGES）

- **累计 max_rows 分页响应契约**：分离适配器私有预读哨兵信号 `readAhead`（不可序列化）与公开 `QueryResult.HasMore`。单页路径用 `readAhead` 返回 `ErrResultTooLarge`；公开 `HasMore` 仍受 `total < maxRows` 约束，消除「累计到 500 行时 has_more=true 但无 token」的不一致响应。
- **可复现合成 fixture**：`demo_pagination`（600 行，主键 id）纳入 `deploy/compose/init/demo-pg/01-init.sh` 与 `demo-mysql/01-init.sh`，确定性幂等（`WHERE NOT EXISTS` 空表才插入），用新 project name `webdb-web39b`（干净新卷）验证创建。
- **D16 双引擎与越权 E2E**：MySQL 补 columns + 显式 order_by 小 page_size 续页（独立审计回执、无重复）；600 行分页累计 500 行第 5 页 `has_more=false` 无 token；跨 workspace（403 forbidden）与不可见 connection（404 connection_not_found）防枚举。
- **Windows 视觉基线**：为排序控件补 CSS（`workbench.css` `.sort-control`），重生成 `workbench-desktop-win32.png`，`npm run test:e2e` 通过（非 Linux 基线，Linux 仍外部阻塞）。

## 2. 改动文件

| 文件 | 类型 | 说明 |
|---|---|---|
| `apps/api/internal/adapter/types.go` | 修改 | `QueryResult` 增加私有 `readAhead` 字段（不可序列化） |
| `apps/api/internal/adapter/manager.go` | 修改 | `finalizeResult` 分离 `readAhead` 与 `HasMore`（后者恢复 `total<maxRows` 门控）；`Query` 单页用 `readAhead`→`ErrResultTooLarge` |
| `apps/api/internal/adapter/single_page_test.go` | 修改 | 重写单测断言 readAhead/HasMore 分离 + 累计上限门控 |
| `apps/api/internal/adapter/single_page_integration_test.go` | 新增 | 500 成功 / 600 `ErrResultTooLarge` |
| `apps/api/internal/seeddemo/ids.go` | 修改 | 新增 foreign workspace/user/connection 固定 UUID |
| `apps/api/internal/seeddemo/deps.go` | 修改 | `identityStore` 扩展 envelope/connection 写入 + 读回 |
| `apps/api/internal/seeddemo/foreign_fixture.go` | 新增 | 第二合成租户隔离 fixture（创建后读回校验，漂移 fail-closed，不覆盖已有记录） |
| `apps/api/internal/seeddemo/seed.go` | 修改 | `Run` 调用 `ensureForeignIsolationFixture` |
| `apps/api/internal/seeddemo/seed_test.go` | 修改 | fake 读回 + DO NOTHING；二次 no-op + 漂移 fail-closed 测试 |
| `apps/web/src/App.tsx` | 修改 | F1 A1 `page_size:500` + 显式 order_by 控件 |
| `apps/web/src/App.test.tsx` | 修改 | 2 测试：默认 500 无 order_by；显式排序列 100+order_by |
| `apps/web/src/workbench.css` | 修改 | `.sort-control` 样式（避免浏览器默认样式漂移） |
| `deploy/compose/docker-compose.yml` | 修改 | `web` 注入 `VITE_WEBDB_WORKSPACE_ID` |
| `deploy/compose/init/demo-pg/01-init.sh` | 修改 | `demo_pagination` 600 行确定性 fixture |
| `deploy/compose/init/demo-mysql/01-init.sh` | 修改 | 同上（递归 CTE 幂等插入） |
| `apps/web/e2e/compose-main-flow.spec.ts` | 新增 | 主流程 + UI 运行查询 + UI 显式排序下一页 + 600 行 5 页累计 + MySQL 续页 |
| `apps/web/e2e/compose-security.spec.ts` | 新增 | DTO canary/DML/DDL/ECM/token 篡改重放/跨 workspace 防枚举/storage |
| `docs/tasks/P0-06F-WEB39-handoff.md` | 新增 | 本文件 |

## 3. 验证结果（实际运行）

| 命令 | 结果 | 退出码 |
|---|---|---|
| `gofmt -l .` / `go vet ./...` | 干净 | 0 |
| `go test ./... -count=1` | 14 包 ok | 0 |
| `go test -tags=integration -run TestSinglePageSentinel_PG`（DEMO_PG_PORT=15433） | ok | 0 |
| `npm test` | 41 passed | 0 |
| `npm run typecheck` / `lint` / `build` | 通过 | 0 |
| `npm run test:e2e`（Windows snapshot） | 1 passed | 0 |
| `npm run test:e2e:smoke`（webdb-web39b） | 14 passed | 0 |
| `bash deploy/compose/verify-readonly.sh` | 14/14 | 0 |
| artifact scanner | `safe_to_upload=true` | 0 |

## 4. 证据分层矩阵

| 项 | 层级 |
|---|---|
| 累计 500 行分页第 5 页 has_more=false 无 token | ✅ Compose（600 行 5 页 E2E）+ Go 单测 `TestFinalizeResultHasMoreGatedByCumulativeCap` |
| 单页 600 行→result_too_large（无 data/token） | ✅ Compose + Go 集成 `TestSinglePageSentinel_PG` |
| F1 UI 运行查询 / UI 显式排序下一页 | ✅ Compose |
| MySQL columns + 显式 order_by 续页（独立审计、无重复） | ✅ Compose |
| 跨 workspace 403 / 不可见 connection 404 防枚举 | ✅ Compose |
| 双引擎主流程 / DTO canary / DML/DDL/ECM / token 篡改+重放 | ✅ Compose |
| 服务重启 token 失效 / 并发 token / 429+Retry-After | ✅ Compose |
| 取消/超时黑盒恢复 / permit 未占用 / 无遗留 pending/running | ✅ Compose |
| 审计失败扣留 + 脱敏安全告警 + 恢复 | ✅ Compose |
| 撤权/策略变化旧 token 拒绝 + 恢复 | ✅ Compose |
| 空结果 / 空 Schema | ✅ Compose |
| Windows 视觉基线 | ✅ 本机（workbench-desktop-win32.png 已更新） |
| panic / generation 变化 / TTL 过期 | ⚠️ **Go 测试级**（rollback_fault / TestSchemaGeneration* / TestClaimExpiredToken），非 Compose |
| Linux Chromium/Ubuntu 视觉基线 | ⛔ 外部硬阻塞（本机 Windows，禁止伪造） |

## 5. C5 黑盒故障注入（可复现步骤）

- **环境**：`COMPOSE_PROJECT_NAME=webdb-web39b`，API `127.0.0.1:18080`，确定性凭证 `change_me` + 固定合成 KEK。
- **取消**：`fetch(POST /executions, {body:{sql:"SELECT pg_sleep(10)",page_size:500}, signal})` 1s 后 `AbortController.abort()` → 客户端 `AbortError`。
- **超时**：`SELECT pg_sleep(10)` → 504 `query_timeout`，耗时 ~5044ms（statement_timeout=5000ms）。
- **恢复**：随后 `SELECT id FROM employees` → 200；并发 2 个同查询 → 200,200（permit 未永久占用，每用户上限 2）。
- **无遗留**：`SELECT status,count(*) FROM executions GROUP BY status` → cancelled 1 / completed / failed，无 pending/running。
- **审计失败**：`REVOKE INSERT ON audit_events FROM webdb_app_runtime` → 查询返回 500 `audit_failed` 无 `data`；`finally` 内 `GRANT INSERT ...` 恢复 → 后续查询 200；API 日志出现 `$SECURITY_ALERT trace=… workspace=… code=audit_failed`（脱敏，无 SQL/凭证）。
- **撤权**：`UPDATE connection_policies SET allow_read=false` → 旧 token 400 `invalid_page_token`；`finally` 恢复 `allow_read=true` → 200。
- 时间：2026-08-14（下午，累计分页修复后）。

## 6. 卷删除偏离记录（透明补记）

> 更正：普通 `docker compose down` 不删除命名卷。以下为本任务早期一次偏离（首次随机凭证与持久化卷失配，改用确定性凭证时移除自身合成卷）。

- 卷名：`webdb-web39_webdb-meta-data`、`webdb-web39_demo-pg-data`、`webdb-web39_demo-mysql-data`。
- 命令：`docker volume rm webdb-web39_webdb-meta-data webdb-web39_demo-pg-data webdb-web39_demo-mysql-data`。
- 时间：2026-08-14 下午（首次 E2E 后）。
- 范围：仅 webdb-web39 合成演示数据 + 随机凭证信封，无真实 PII。
- 恢复：以确定性凭证重建空卷并经 migrate+seed 重新生成。
- 无影响证据：全程 `docker ps` 确认 `webdb-p0-*` 5 服务 healthy，未触碰。
- 后续：本任务此后未再删除任何卷；大表 fixture 保留在隔离卷，未污染 webdb-p0。

## 7. 外部阻塞

- **Linux Chromium/Ubuntu 视觉基线**：需 GitHub CI（Ubuntu）或 Linux 环境生成（唯一外部硬阻塞）。
- TTL/panic/generation 的 Compose 级未做（Go 测试已覆盖；TTL 需真实 5 分钟等待，panic 无安全生产触发路径）。

## 8. 未提交/未 push/未建 PR 声明

- 未 commit/push/创建或更新 PR/更新 Linear/GitHub/启动审查/轮询外部状态。
- 未修改原 WEB-34 工作树；未停止/修改用户 `webdb-p0`。
- 未新增测试专用 API/生产后门/panic 开关/弱化安全边界；未删除任何 volume（本轮）；`down` 未 `-v`。

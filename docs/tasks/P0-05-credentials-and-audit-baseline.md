# P0-05：凭证与审计基线

> 状态：In Progress｜风险：High｜依赖：P0-02、P0-04、ADR-006、ADR-010、ADR-013｜建议实现者：Claude Code｜独立审查：Codex
>
> **状态说明（Owner 2026-08-02 决策）**：P0-05 生产实现已完成并合并（PR #30/#31/#32），但收尾审查发现 3 个 P1 缺口，Owner 决策创建修复任务 [WEB-24](https://linear.app/webdb/issue/WEB-24)（并发轮换/回滚集成测试）、[WEB-25](https://linear.app/webdb/issue/WEB-25)（审计原子性）、[WEB-26](https://linear.app/webdb/issue/WEB-26)（凭证字段长度）。**WEB-24/25/26 完成前，WEB-11 保持 In Progress**。[WEB-27](https://linear.app/webdb/issue/WEB-27)（R6 生产角色拆分，截止 2026-08-09）建立追踪后不阻断代码验收。
>
> **子任务**：
> - [WEB-21](https://linear.app/webdb/issue/WEB-21)：P0-05A 凭证与审计方案、威胁模型及 Owner Gate（✅ Done，[PR #30](https://github.com/fujiabao89/WebDB/pull/30) 已合并，合并后 main commit（单父）`3b9e5bd8c9af68fca56b069f3c39ad0b83872511`）
> - [WEB-22](https://linear.app/webdb/issue/WEB-22)：P0-05B 凭证信封加密、版本轮换与 Adapter 接入（✅ Done，[PR #31](https://github.com/fujiabao89/WebDB/pull/31) 已合并，合并后 main commit（单父）`0af2625b6a00c07563e0e8ebf188e2811e1bf571`）
> - [WEB-23](https://linear.app/webdb/issue/WEB-23)：P0-05C 追加式审计、脱敏、故障策略与最终验收（✅ Done，[PR #32](https://github.com/fujiabao89/WebDB/pull/32) 已合并，合并后 main commit（单父）`94eb3ca89a1bfb3e843af7209df45ae1ff37a2c2`）
>
> **方案文档**：[P0-05-proposal-credentials-and-audit.md](P0-05-proposal-credentials-and-audit.md)
> **威胁模型**：[P0-05-threat-model.md](P0-05-threat-model.md)
> **ADR**：[ADR-017](../adr/ADR-017-p0-credential-envelope-audit-failure.md)（已接受）
> **合并后 main CI**：[run 30737988480](https://github.com/fujiabao89/WebDB/actions/runs/30737988480)（success，head SHA `94eb3ca`）

## 目标与范围

实现连接凭证信封加密/引用、轮换版本预留、日志脱敏与追加式审计。覆盖连接测试、执行、拒绝、取消和策略相关事件。

不接入企业 KMS、对象存储审计归档、生产数据导出或真实 KEK。

## 验收标准

| 验收项 | 证据 |
| --- | --- |
| API、浏览器响应、数据库、日志和审计正文均不含明文密码/KEK | canary 敏感信息扫描（`execution/sensitive_audit_test.go`、`credentials/sensitive_test.go`、`metadata/audit_metadata_test.go`） |
| 每次关键操作记录 actor、工作区、连接、动作、结果摘要、时间和 execution/trace ID | 审计集成测试（E1-E16 事件 + 关联正确性） |
| 审计普通业务路径不能更新/删除；敏感值仅记录脱敏摘要 | DB 拒绝 UPDATE/DELETE/TRUNCATE 集成测试；强类型 metadata fail-closed 校验 |
| 加密/解密和审计写入失败显式失败并告警，不静默降级 | 故障注入测试（AUDIT-01~04 + `$SECURITY_ALERT`） |

## 实施状态（WEB-22 于 2026-08-01、WEB-23 于 2026-08-02 已合并）

- E1-E16 追加式审计已接入连接/凭证/SQL 执行生命周期（`metadata.AuditMetadata` 强类型 + `AppendAudit` fail-closed）。
- execution 审计感知管线（`execution.Pipeline`）实现执行前 fail-closed、执行后 `audit_failed` 且 execution 保持终态。
- `$SECURITY_ALERT` 独立安全告警通道覆盖凭证解密失败、未知 KEK 版本、审计写入失败。
- WEB-22 于 2026-08-01、WEB-23 于 2026-08-02 合并，合并后 main CI run <https://github.com/fujiabao89/WebDB/actions/runs/30737988480> 全绿（gofmt / vet / test / race / metadata 集成 / PostgreSQL·MySQL adapter 集成 全部 success）。

### 验证证据分列

**本机验证（2026-08-02，Windows，Go 1.26.5，`GOPROXY=off`；命令均可从仓库根目录直接复制执行，Go module 位于 `apps/api`）**：

| 命令（从仓库根目录执行） | 结果 |
|---|---|
| `go -C apps/api test ./...` | PASS（exit 0，全部缓存 ok） |
| `go -C apps/api test -count=1 ./internal/credentials ./internal/metadata ./internal/connections ./internal/execution` | PASS（exit 0） |
| `go -C apps/api vet ./...` | PASS（exit 0） |
| `GOOS=windows GOARCH=amd64 go -C apps/api build ./...` | PASS（exit 0） |
| `GOOS=linux GOARCH=amd64 go -C apps/api build ./...` | PASS（exit 0） |
| `go -C apps/api test ./internal/credentials -run='^$' -fuzz='^FuzzPayloadDecoder$' -fuzztime=30s` | PASS（exit 0，~31.9s，228k+ execs，无 panic/crash） |
| `go -C apps/api test ./internal/credentials -run='^$' -fuzz='^FuzzAAD$' -fuzztime=30s` | PASS（exit 0，~31.0s，50k+ execs，无 panic/crash） |
| PostgreSQL metadata 集成（本机） | 见下注 |
| `go -C apps/api test -race ./...` | **本机未通过**：Windows `CGO_ENABLED=0`，`-race requires cgo`（exit 2）。**本机不声明 race PASS** |

> 注：本机 PostgreSQL metadata 集成依赖本机 `postgres:16` 服务；本任务收尾不在本机重跑集成，PostgreSQL/MySQL adapter 与 metadata 集成以 main CI 结果为准。

**main CI 验证（run 30737988480，head `94eb3ca`，全部 success）**：

- Repository safety（无被跟踪的 `.env`，AGENTS.md / PR 模板 / CODEOWNERS 存在）
- Contracts checks（typecheck + test）
- Web checks（lint / typecheck / test / build）
- API checks：Format（gofmt）、Vet、Test、**Race test**、Metadata integration test（PostgreSQL）、Adapter integration test（PostgreSQL + MySQL）——全部 success

**本机未运行但 CI 已验证的项目**：

- `go test -race ./...`（本机受 CGO 限制，由 main CI Race test success 覆盖）
- PostgreSQL/MySQL adapter 集成（由 main CI Adapter integration test 覆盖）
- MySQL 本机环境限制：本机 Windows 存在 `localhost:1` 连接环境限制，MySQL adapter 集成未在本机完成，以 CI 为准

**不声明**：真实 KEK、生产凭证或生产数据库验证均未使用；所有验证基于合成/脱敏数据。

## Owner Gate 状态（WEB-21）

| 决策项 | 状态 |
| --- | --- |
| D1-D15 Owner 决策包 | ✅ 已批准（2026-08-01, fujiabao89） |
| ADR-017 | 已接受 |
| 威胁模型 | 已批准 |
| 测试矩阵 | 已编制并执行（WEB-22/WEB-23 测试覆盖本 PR） |
| WEB-22 阻塞 | 已解除 |
| WEB-23 阻塞 | 已解除（WEB-22 已合并，2026-08-01） |

## 最终验收矩阵（WEB-11，2026-08-02）

| 验收项 | 结果 | 代码/测试/CI 证据 |
| --- | --- | --- |
| 凭证 Payload 严格校验 | **部分覆盖（P1 待 Owner）** | `credentials/payload_test.go`：`TestPayload_*`（round-trip、v 版本、空值、非法 UTF-8、未知字段、控制字符、总大小 4096 上限）。**per-field 长度限制未实现**：`validatePayloadFields` 只校验 user/password 非空与 user 控制字符，未校验 proposal §3.2 的 user≤255 / password≤1024 字节；`payload_test.go` 亦无 PAY-06/PAY-07 对应测试。超大字段（总 JSON<4096）可到达 Adapter。**Codex P1，需 Owner 决策补实现+测试或记录有期限例外**（见「残余风险」） |
| AES-256-GCM 信封加密与 AAD | PASS | `credentials/envelope_test.go` + `aad_validation_test.go`：`TestEnvelope_*`（RoundTrip、WrongKEK、DataAAD/WrapAAD、各类篡改）、`TestAAD_*`（48B、CrossWorkspace/SecretRef/Version） |
| KEK Provider 与版本行为 | PASS | `credentials/kek_test.go`：`TestKEK_*`（ActiveKEK、版本、Base64/长度/弱值拒绝、2^24 包装上限、并发） |
| create/resolve/rotate/retire | PASS | `credentials/audited_test.go`：`TestAuditedCredential_*`（CreateWritesE3、RotateSucceeded/Failed、RetireSucceeded、E14-E16） |
| 并发轮换与事务回滚 | **部分覆盖** | 并发 wrap 预留：`credentials/kek_test.go`（`TestKEK_WrappingCounterConcurrent`、`TestKEK_WrapReservationNeverExceedsLimitConcurrently`）；轮换审计事件：`credentials/audited_test.go`（`TestAuditedCredential_RotateSucceededEvent`、`RotateFailedEvent`）。**PostgreSQL 并发轮换（LIFE-07）与事务中间失败回滚（LIFE-08）的直接集成测试缺失**，作为后续任务跟踪（见「残余风险」） |
| 退役引用并发保护 | PASS | `metadata/integration_test.go`：`TestCountConnectionsByVersionLocksMatchingReferences`、`TestConnectionWritesRejectRetiredCredential` |
| Policy 拒绝不解密 | PASS | `execution/audited_pipeline_test.go`：`TestAuditedExecute_PolicyDenied`（Adapter 0 次） |
| 凭证失败 Adapter 0 次 | PASS | `execution/audited_pipeline_test.go`：`TestAuditedExecute_CredentialFailure` |
| E1-E16 追加式审计 | **部分覆盖（P1 待 Owner）** | `metadata/integration_test.go`：`TestAudit_AppendAndQuery`、`TestAudit_UpdateRejected`、`TestAudit_DeleteRejected`、`TestAudit_TruncateRejected`；`execution/audited_pipeline_test.go`：`TestAuditedExecute_*`。**审计原子性契约矛盾未决**：`AuditedLifecycleManager.Create/Rotate/Retire` 与 `connections.Service.Create/Update` 在调用 `writeAudit` 前已提交 mutation（post-commit），而 proposal D11 声明"元数据库变更与 audit 在同一事务中原子提交"；proposal §6.2.1/6.2.3 同时描述 post-commit 行为。**Codex P1，需 Owner 决策：要么将凭证/连接 mutation 与审计写入原子化并加失败注入测试，要么同步 ADR-017/任务的原子性契约**（见「残余风险」） |
| E17 安全告警 | PASS | `execution/sensitive_audit_test.go`：`TestStderrAlarmOutput`；`execution/audited_pipeline_test.go`：`TestAuditedExecute_AlarmFailureDoesNotPanic`；`metadata/security_alert_test.go`（EmitAlarm 相关测试） |
| metadata 事件级允许列表 | PASS | `metadata/audit_metadata_test.go`：`TestAuditMetadataValidate_*`（AllowedFields、UnknownFields、TypeErrors、Malformed、NestedObject、DeadField、Canary） |
| UPDATE/DELETE/TRUNCATE 拒绝 | PASS | `metadata/integration_test.go`：`TestAudit_UpdateRejected` / `TestAudit_DeleteRejected` / `TestAudit_TruncateRejected` |
| 密码/KEK/DEK/SQL 正文脱敏 | PASS | `credentials/sensitive_test.go`、`execution/sensitive_audit_test.go`（`TestAuditedExecute_MetadataNoCanary`/`ErrorNoCanary`）、`metadata/audit_metadata_test.go`（`TestAuditMetadataValidate_Canary`）、`metadata/integration_test.go`（`TestNoPlaintextCredentialsInSchema`） |
| timeout/cancel/错误路径 | PASS | `execution/audited_pipeline_test.go`：`TestAuditedExecute_Timeout`、`TestAuditedExecute_Cancelled`、`TestAuditedExecute_CancelledContextStillPersists` |
| PostgreSQL/MySQL Adapter | PASS | main CI run 30737988480：Adapter integration test（PostgreSQL + MySQL）success |
| go test | PASS | 本机 `go -C apps/api test ./...`（exit 0）+ CI Test success |
| go vet | PASS | 本机 `go -C apps/api vet ./...`（exit 0）+ CI Vet success |
| race | PASS | **main CI** Race test success；**本机未运行**（Windows CGO_ENABLED=0，`-race requires cgo`），见"验证证据分列" |
| fuzz 30s | PASS | 本机 `go -C apps/api test ./internal/credentials -fuzz='^FuzzPayloadDecoder$' -fuzztime=30s` 与 `-fuzz='^FuzzAAD$'` 均 PASS（见"验证证据分列"） |
| Windows/Linux build | PASS | 本机 `GOOS=windows` / `GOOS=linux` `go -C apps/api build ./...` 均 exit 0 |
| 许可证 | PASS | 无新增第三方依赖（proposal §11：全部 Go stdlib） |
| migration | PASS | 无新增 migration（proposal 附录 A：现有 Schema 满足，无需 migration） |

## 残余风险

全部残余风险 R1-R7 与已接受决策见 [P0-05-proposal-credentials-and-audit.md](P0-05-proposal-credentials-and-audit.md) §13 与 [P0-05-threat-model.md](P0-05-threat-model.md) §6，以及 [ADR-017](../adr/ADR-017-p0-credential-envelope-audit-failure.md)。本任务不新增、不改写任何 Owner 决策。要点：

- **凭证 per-field 长度限制未实现（Codex P1 → [WEB-26](https://linear.app/webdb/issue/WEB-26)）**：`validatePayloadFields` 未校验 user≤255 / password≤1024 字节（按 UTF-8 字节数，Owner 已授权实现）。Owner 决策：不修改字段长度契约，按 UTF-8 字节数实现并补 PAY-06/07 边界测试。
- **审计原子性契约矛盾（Codex P1 → [WEB-25](https://linear.app/webdb/issue/WEB-25)）**：D11 保留但明确作用域——同一元数据库内 credential/connection mutation 必须与 AuditEvent 原子提交；目标库查询后审计为外部副作用例外，沿用 ADR-017 post-execution fail-closed。Owner 决策：创建 P1 实现任务修复 post-commit 审计。
- **并发轮换与事务回滚测试缺口（Codex P1 → [WEB-24](https://linear.app/webdb/issue/WEB-24)）**：`credentials/kek_test.go` 覆盖并发 wrap 计数、`credentials/audited_test.go` 覆盖轮换审计事件，但缺少直接验证并发轮换（LIFE-07）与事务中间失败回滚（LIFE-08）的 PostgreSQL 集成测试。Owner 决策：不接受例外，创建 P1 修复任务在真实 PostgreSQL 集成环境补测。
- **R6（审计事件不防内部 DBA 篡改 → [WEB-27](https://linear.app/webdb/issue/WEB-27)）**：D15 已批准创建"生产环境数据库角色拆分"任务。Owner 2026-08-02 决策已创建 WEB-27（Backend/Security Owner，截止 2026-08-09，首次 production-like 部署前完成）；建立追踪关系后**不阻断 WEB-11 代码验收**，但阻断首次 production-like 部署。
- KEK 丢失等于对应 envelope 永久失去解密能力（ADR-017 后果）。
- 本任务全部验证基于合成/脱敏数据；未使用真实 KEK、生产凭证或生产数据库。

## 回滚与前向修复

- **本收尾文档 PR**：仅影响文档状态，回滚/前向修复只涉及文档，不影响任何生产行为。
- **功能回滚**（引用实际 commit——`3b9e5bd`/`0af2625`/`94eb3ca` 均为**单父提交**，非双父 merge commit，用普通 `git revert <sha>` 即可，**无需 `-m 1`**；**注意顺序**）：由于 WEB-23 修改了 WEB-22 引入的 credential/execution 文件，且本收尾 PR 修改了同一批文档，直接按任意顺序 `git revert` 会触发 modify/delete 冲突，需人工解决。完整回滚顺序：
  1. 先回滚本收尾 PR（仅影响文档状态）：`git revert` 本收尾 PR 合并后的 main commit（该 PR 的提交 `896044a`、`e34a884`、`4ebe183`、`d00c6d0` 合并为一个提交）；
  2. `git revert 94eb3ca89a1bfb3e843af7209df45ae1ff37a2c2`（WEB-23 / PR #32，追加式审计/脱敏/故障策略）；
  3. `git revert 0af2625b6a00c07563e0e8ebf188e2811e1bf571`（WEB-22 / PR #31，凭证信封/轮换/Adapter 接入）；
  4. WEB-21（仅文档设计门，可选）：`git revert 3b9e5bd8c9af68fca56b069f3c39ad0b83872511`（无生产行为）。
  - **凭证回滚禁令**：`credential_envelopes` 表虽仅追加写，但**一旦写入新信封，禁止回滚到不支持 `SealEnvelope`/`OpenEnvelope`/`ResolveCredential` 的版本**（WEB-22 之前的代码无这些能力，回滚后已有信封将无法解析/解密）。回滚前须验证兼容性，或保留可处理旧信封的前向版本；`audit_events` 仅追加写，回滚代码不影响已写入审计数据。
- **KEK 紧急轮换（两阶段）**：见 proposal §15.4 / ADR-017「验证与回滚」。

## 升级条件

密钥轮换、保留期、审计字段或日志平台选择发生变化时，先更新 ADR/威胁模型。

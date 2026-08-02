# P0-05：凭证与审计基线

> 状态：Done｜完成日期：2026-08-02｜风险：High｜依赖：P0-02、P0-04、ADR-006、ADR-010、ADR-013｜建议实现者：Claude Code｜独立审查：Codex
>
> **子任务**：
> - [WEB-21](https://linear.app/webdb/issue/WEB-21)：P0-05A 凭证与审计方案、威胁模型及 Owner Gate（✅ Done，[PR #30](https://github.com/fujiabao89/WebDB/pull/30) 已合并，merge commit `3b9e5bd8c9af68fca56b069f3c39ad0b83872511`）
> - [WEB-22](https://linear.app/webdb/issue/WEB-22)：P0-05B 凭证信封加密、版本轮换与 Adapter 接入（✅ Done，[PR #31](https://github.com/fujiabao89/WebDB/pull/31) 已合并，merge commit `0af2625b6a00c07563e0e8ebf188e2811e1bf571`）
> - [WEB-23](https://linear.app/webdb/issue/WEB-23)：P0-05C 追加式审计、脱敏、故障策略与最终验收（✅ Done，[PR #32](https://github.com/fujiabao89/WebDB/pull/32) 已合并，merge commit `94eb3ca89a1bfb3e843af7209df45ae1ff37a2c2`）
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

## 实施状态（WEB-22/WEB-23，2026-08-02 已合并）

- E1-E16 追加式审计已接入连接/凭证/SQL 执行生命周期（`metadata.AuditMetadata` 强类型 + `AppendAudit` fail-closed）。
- execution 审计感知管线（`execution.Pipeline`）实现执行前 fail-closed、执行后 `audit_failed` 且 execution 保持终态。
- `$SECURITY_ALERT` 独立安全告警通道覆盖凭证解密失败、未知 KEK 版本、审计写入失败。
- WEB-22/WEB-23 已于 2026-08-02 合并，合并后 main CI run <https://github.com/fujiabao89/WebDB/actions/runs/30737988480> 全绿（gofmt / vet / test / race / metadata 集成 / PostgreSQL·MySQL adapter 集成 全部 success）。

### 验证证据分列

**本机验证（2026-08-02，Windows，Go 1.26.5，`GOPROXY=off`）**：

| 命令 | 结果 |
|---|---|
| `go test ./...` | PASS（exit 0，全部缓存 ok） |
| `go test -count=1 ./internal/credentials ./internal/metadata ./internal/connections ./internal/execution` | PASS（exit 0） |
| `go vet ./...` | PASS（exit 0） |
| `GOOS=windows GOARCH=amd64 go build ./...` | PASS（exit 0） |
| `GOOS=linux GOARCH=amd64 go build ./...` | PASS（exit 0） |
| `go test ./internal/credentials -run='^$' -fuzz='^FuzzPayloadDecoder$' -fuzztime=30s` | PASS（exit 0，~31.9s，228k+ execs，无 panic/crash） |
| `go test ./internal/credentials -run='^$' -fuzz='^FuzzAAD$' -fuzztime=30s` | PASS（exit 0，~31.0s，50k+ execs，无 panic/crash） |
| PostgreSQL metadata 集成（本机） | 见下注 |
| `go test -race ./...` | **本机未通过**：Windows `CGO_ENABLED=0`，`-race requires cgo`（exit 2）。**本机不声明 race PASS** |

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
| 凭证 Payload 严格校验 | PASS | `credentials/payload_test.go`：`TestPayload_*`（round-trip、v 版本、空值、超长、非法 UTF-8、未知字段、控制字符） |
| AES-256-GCM 信封加密与 AAD | PASS | `credentials/envelope_test.go` + `aad_validation_test.go`：`TestEnvelope_*`（RoundTrip、WrongKEK、DataAAD/WrapAAD、各类篡改）、`TestAAD_*`（48B、CrossWorkspace/SecretRef/Version） |
| KEK Provider 与版本行为 | PASS | `credentials/kek_test.go`：`TestKEK_*`（ActiveKEK、版本、Base64/长度/弱值拒绝、2^24 包装上限、并发） |
| create/resolve/rotate/retire | PASS | `credentials/audited_test.go`：`TestAuditedCredential_*`（CreateWritesE3、RotateSucceeded/Failed、RetireSucceeded、E14-E16） |
| 并发轮换与事务回滚 | PASS | `credentials/kek_test.go`（并发计数）+ `metadata/integration_test.go`（FOR SHARE/FOR KEY SHARE，见下） |
| 退役引用并发保护 | PASS | `metadata/integration_test.go`：`TestCountConnectionsByVersionLocksMatchingReferences`、`TestConnectionWritesRejectRetiredCredential` |
| Policy 拒绝不解密 | PASS | `execution/audited_pipeline_test.go`：`TestAuditedExecute_PolicyDenied`（Adapter 0 次） |
| 凭证失败 Adapter 0 次 | PASS | `execution/audited_pipeline_test.go`：`TestAuditedExecute_CredentialFailure` |
| E1-E16 追加式审计 | PASS | `metadata/integration_test.go`：`TestAudit_AppendAndQuery`、`TestAudit_UpdateRejected`、`TestAudit_DeleteRejected`、`TestAudit_TruncateRejected`；`execution/audited_pipeline_test.go`：`TestAuditedExecute_*` |
| E17 安全告警 | PASS | `metadata/security_alert_test.go`：`TestStderrAlarmOutput`；`execution/audited_pipeline_test.go`：`TestAuditedExecute_AlarmFailureDoesNotPanic` |
| metadata 事件级允许列表 | PASS | `metadata/audit_metadata_test.go`：`TestAuditMetadataValidate_*`（AllowedFields、UnknownFields、TypeErrors、Malformed、NestedObject、DeadField、Canary） |
| UPDATE/DELETE/TRUNCATE 拒绝 | PASS | `metadata/integration_test.go`：`TestAudit_UpdateRejected` / `TestAudit_DeleteRejected` / `TestAudit_TruncateRejected` |
| 密码/KEK/DEK/SQL 正文脱敏 | PASS | `credentials/sensitive_test.go`、`execution/sensitive_audit_test.go`（`TestAuditedExecute_MetadataNoCanary`/`ErrorNoCanary`）、`metadata/audit_metadata_test.go`（`TestAuditMetadataValidate_Canary`）、`metadata/integration_test.go`（`TestNoPlaintextCredentialsInSchema`） |
| timeout/cancel/错误路径 | PASS | `execution/audited_pipeline_test.go`：`TestAuditedExecute_Timeout`、`TestAuditedExecute_Cancelled`、`TestAuditedExecute_CancelledContextStillPersists` |
| PostgreSQL/MySQL Adapter | PASS | main CI run 30737988480：Adapter integration test（PostgreSQL + MySQL）success |
| go test | PASS | 本机 `go test ./...`（exit 0）+ CI Test success |
| go vet | PASS | 本机 `go vet ./...`（exit 0）+ CI Vet success |
| race | PASS | **main CI** Race test success；**本机未运行**（Windows CGO_ENABLED=0，`-race requires cgo`），见"验证证据分列" |
| fuzz 30s | PASS | 本机 `FuzzPayloadDecoder` 30s、`FuzzAAD` 30s 均 PASS（见"验证证据分列"） |
| Windows/Linux build | PASS | 本机 `GOOS=windows` / `GOOS=linux` `go build ./...` 均 exit 0 |
| 许可证 | PASS | 无新增第三方依赖（proposal §11：全部 Go stdlib） |
| migration | PASS | 无新增 migration（proposal 附录 A：现有 Schema 满足，无需 migration） |

## 残余风险

全部残余风险 R1-R7 与已接受决策见 [P0-05-proposal-credentials-and-audit.md](P0-05-proposal-credentials-and-audit.md) §13 与 [P0-05-threat-model.md](P0-05-threat-model.md) §6，以及 [ADR-017](../adr/ADR-017-p0-credential-envelope-audit-failure.md)。本任务不新增、不改写任何 Owner 决策。要点：

- R6（审计事件不防内部 DBA 篡改）已创建生产数据库角色拆分后续任务（D15）。
- KEK 丢失等于对应 envelope 永久失去解密能力（ADR-017 后果）。
- 本任务全部验证基于合成/脱敏数据；未使用真实 KEK、生产凭证或生产数据库。

## 回滚与前向修复

- **本收尾文档 PR**：仅影响文档状态，回滚/前向修复只涉及文档，不影响任何生产行为。
- **功能回滚**（引用实际 merge commit）：
  - WEB-22：`git revert 0af2625b6a00c07563e0e8ebf188e2811e1bf571`（凭证信封/轮换/Adapter 接入）
  - WEB-23：`git revert 94eb3ca89a1bfb3e843af7209df45ae1ff37a2c2`（追加式审计/脱敏/故障策略）
  - WEB-21（仅文档设计门）：`git revert 3b9e5bd8c9af68fca56b069f3c39ad0b83872511`
  - credential_envelopes 与 audit_events 仅追加写，代码回滚不影响已写入数据。
- **KEK 紧急轮换（两阶段）**：见 proposal §15.3 / ADR-017「验证与回滚」。

## 升级条件

密钥轮换、保留期、审计字段或日志平台选择发生变化时，先更新 ADR/威胁模型。

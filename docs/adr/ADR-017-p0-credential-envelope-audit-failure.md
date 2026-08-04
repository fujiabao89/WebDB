# ADR-017：P0 凭证信封加密、KEK 生命周期与审计失败策略

> 状态：已接受｜日期：2026-08-01｜作者：Claude Code｜批准人：fujiabao89
>
> Owner 已于 2026-08-01 对 D1-D15 全部决策做出明确批准。本 ADR 的决策内容已冻结。

## 背景

P0-02 已在 `credential_envelopes` 表中建立加密字段 Schema（ciphertext、data_nonce、wrapped_dek、wrap_nonce、envelope_suite、kek_version），但 AEAD 算法、DEK 生成方式、AAD 编码、KEK 版本行为和审计失败策略尚未决策。P0-04 已定义了 SQL 执行的生命周期（阶段 A-D）和审计脱敏基线。

WEB-21（P0-05A）已完成方案设计与 Owner Gate 审批。

WEB-21（P0-05A）需要在任何生产实现之前冻结以下决策。

## 决策

### 1. Credential Payload

- **Schema v1**：`{"v": 1, "user": "<db_username>", "password": "<db_password>"}`
- 编码：JSON (UTF-8)，4096 字节上限
- 仅满足 Adapter `ConnectConfig{User, Password}` 的当前需求
- 未知字段拒绝；空 user/password 拒绝；user 禁止控制字符

### 2. 信封加密方案

- **数据加密**：AES-256-GCM（256-bit DEK，96-bit nonce，128-bit tag）
- **DEK 包装**：AES-256-GCM（使用 KEK，96-bit nonce，独立 wrap AAD 禁止 nil）
- **每 KEK 加密上限**：2^24 次。P0 使用进程内原子计数器（重启归零），达到上限后拒绝新包装但仍可解密。跨实例部署时各实例独立计数，实际总量可能略超 2^24，但仍在 GCM nonce 安全边界（2^32）内。此上限旨在防止持续运行期间的 nonce 碰撞，非不可绕过的硬配额
- **Nonce 生成**：`crypto/rand.Read`（失败时 fail-closed）
- **AAD**：版本化确定性二进制编码（48 bytes），绑定 `version_tag` + `workspace_id` + `secret_ref` + `secret_version` + `envelope_suite_tag` + `kek_version`（大端序）。数据 AAD 与 Wrap AAD 独立构造，Wrap AAD 禁止为 nil
- **`envelope_suite`**：`"AES256GCM-v1"`（精确匹配，未知值拒绝）
- **Go stdlib only**：不新增第三方依赖

### 3. KEK Provider

- **注入方式**：环境变量 `WEBDB_KEK_V{N}`（RFC 4648 padded Base64 严格解码，32 bytes）
- **当前写入版本**：由 `WEBDB_ACTIVE_KEK_VERSION` 环境变量显式指定，禁止自动选择最大版本
- **启动验证**：解析并验证所有 KEK；`WEBDB_ACTIVE_KEK_VERSION` 指向的版本必须有对应变量；无有效 KEK → fatal
- **拒绝弱值**：`change_me`、空字符串、非 Base64、长度错误 → fatal
- **版本行为**：读取按 `kek_version` 查找对应 KEK；未知版本拒绝；写入始终使用 `WEBDB_ACTIVE_KEK_VERSION` 指定的版本
- **安全约束**：KEK 不进入仓库、镜像、DB、日志、错误、审计、API 响应或普通测试夹具

### 4. 凭证生命周期

- **创建**：生成 secret_ref UUID → version=1 → 加密 → INSERT
- **读取**：授权 → 查找 envelope（拒绝 `retired_at IS NOT NULL` 的版本用于普通执行，返回 `credential_retired`）→ 验证 AAD → 解密 DEK → 解密 payload → 验证 schema。退役版本仅审计追溯允许解密（通过直接查询 envelope 表，不经过正常凭证解析路径）
- **轮换**：expected_version + SELECT FOR UPDATE + INSERT 新版本 + UPDATE connections（同一事务，固定锁顺序：先 envelope 后 connections）
- **退役**：先验证无连接引用（被引用时拒绝，返回 `credential_in_use`）；通过后 SET retired_at=now()；不删除密文
- **并发**：事务隔离 + 唯一约束 `(workspace_id, secret_ref, version)` + expected_version 双重保护；失败回滚

### 5. 审计事件

- **E1-E16 持久化审计**：connection.create/update、credential.create/rotate（expected_version 不匹配时返回 version_conflict, outcome=failed）/retire、connection.test、sql.execute（denied/succeeded/failed/timeout/cancelled）、credential.lookup/decrypt 失败、unknown KEK version
- **E17**：审计写入失败作为独立安全告警通道，不写回失败的审计系统
- **Metadata 允许列表**：合并 16 字段（6 P0-05 新增 + 10 P0-04 现有），使用事件级精确格式校验，禁止自由文本
- **禁止字段**：SQL 正文、密码、KEK/DEK、nonce、连接串、目标库结果、原始数据库错误

### 6. 审计失败策略

- **分阶段 fail-closed**：与 P0-04 §8.6 一致
- 执行前审计失败：不调用 Adapter，返回 `audit_failed`
- 执行后审计失败：不返回结果，返回 `audit_failed`，execution 已记录为终态（`completed`/`failed`/`cancelled`，与超时/取消/失败路径一致）
- 禁止自动重试；禁止静默降级
- 凭证解密失败、未知 KEK 版本和审计写入失败 → `$SECURITY_ALERT`

### 7. 调用顺序

凭证解析（新阶段 C'）在 SQL 策略通过（阶段 C）之后、Adapter 调用（阶段 D）之前执行。凭证失败时 Adapter 调用次数 = 0。

## 候选方案与取舍

- **AES-256-GCM vs AES-KWP**：GCM 在 Go stdlib 中有原生实现，KWP (RFC 5649) 需自行实现。选择 GCM 避免引入自行实现的密码学代码。
- **GCM vs XChaCha20-Poly1305**：后者需 `golang.org/x/crypto` 依赖。GCM 在 stdlib 中且充分安全。
- **环境变量 vs 配置文件 vs KMS**：环境变量最简单且符合 ADR-006；KMS 为企业版后续任务。
- **启动验证 vs 首次使用验证**：启动验证确保部署者立即发现配置错误，而非运行时异常。

## 后果

- 无需新增 Schema migration（现有 `credential_envelopes` 表已完整）
- 无需新增第三方依赖（全部 Go stdlib）
- KEK 轮换需要在所有 WebDB 实例上同步更新环境变量并修改 `WEBDB_ACTIVE_KEK_VERSION`
- 丢失 KEK 环境变量等同于永久失去对应 envelope 的解密能力
- 审计事件至少保留 90 天（从 `occurred_at` 起算），P0 阶段不实施自动删除（D12 已批准）
- 精确清理机制和归档策略另建独立任务

## 实施与验证状态（WEB-22/WEB-23，2026-08-02 已合并）

WEB-22（P0-05B）实现了凭证信封加密、KEK Provider 与生命周期；WEB-23（P0-05C）实现了本 ADR 定义的追加式审计、脱敏与故障策略，已于 2026-08-02 合并（PR #31 / #32）。

- **强类型审计 metadata**：`metadata.AuditMetadata` 仅接受 ADR-017 D10 已批准的 16 字段（6 P0-05 新增 + 10 P0-04 现有）+ credential.rotate 专用 expected_version/actual_version，共 18 个 JSON 字段，事件级 fail-closed 校验（`ValidateAuditEventMetadata`）。畸形 JSON、未知字段、错误类型、超长值一律拒绝，不再依赖 `looksLikeSQL`/`looksLikeCredential` 启发式作为安全边界。
- **事件接入**：E1/E2（connection.create/update）、E3-E6（credential.create/rotate/retire）、E7/E8（connection.test）、E9-E13（sql.execute denied/succeeded/failed/timeout/cancelled）、E14-E16（credential.lookup/decrypt 失败、unknown KEK version）已接入对应 orchestration seam。E14-E16 由执行管线阶段 C'（`execution.Pipeline.recordCredentialFailure`）作为唯一权威写入者记录，携带连接上下文（WEB-25 澄清，Qodo-13），凭证解析层不再重复写入。
- **审计失败策略**：执行前（阶段 C/C'）审计失败 fail-closed（Adapter 调用 0 次，返回 `audit_failed`）；执行后审计失败不返回结果、返回 `audit_failed`、execution 已记录为终态（`completed`/`failed`/`cancelled`，与超时/取消/失败路径一致）；禁止自动重试。
- **安全告警**：凭证解密失败、未知 KEK 版本、审计写入失败触发 `$SECURITY_ALERT`（独立通道，不递归写回审计，不含敏感字段）。
- **append-only**：`audit_events` 表数据库层拒绝 UPDATE/DELETE/TRUNCATE（集成测试覆盖）；跨工作区 actor/connection/execution 关联由复合外键拒绝（集成测试覆盖）。
- **D11 原子提交（WEB-25 实施，2026-08-04，PR #38 已合并）**：credential Create/Rotate/Retire 与 connection Create/Update 的元数据库 mutation 与对应 AuditEvent（E1-E6）在同一事务原子提交（`CredentialAtomicTx`/`ConnectionAtomicTx` + Commit 审计闸门）；审计写入失败时整体回滚、无 mutation 残留，返回 `audit_failed` 并触发 `$SECURITY_ALERT`。**D11 原子范围仅涵盖成功 mutation 与其审计（E1-E4/E6）**；E5（`credential.rotate failed`）发生在 Rotate 失败分支（事务中无已提交 mutation），先回滚释放行锁、再经独立失败路径写入（审计持久化失败时 fail-closed 返回 `audit_failed`），属原子范围外。执行前审计（E9 `sql.execute.denied`，发生在 Adapter 调用前）失败时 fail-closed：不调用 Adapter、返回 `audit_failed`；目标库执行后的结果审计（E10-E13）与 connection.test（E7/E8）为 post-commit 外部副作用例外（不原子，fail-closed 返回 `audit_failed`）。不新增 migration、不改变稳定错误码/E17 语义。
- **验证（合并后 main CI）**：WEB-23 合并 CI run <https://github.com/fujiabao89/WebDB/actions/runs/30737988480>（head `94eb3ca`）、最新 main CI run <https://github.com/fujiabao89/WebDB/actions/runs/30896499396>（head `756a086`，含 WEB-25 代码实施）均全部 success：gofmt / vet / test / **race** / metadata 集成 / PostgreSQL·MySQL adapter 集成均为 **CI 覆盖**。
- **本机验证（2026-08-03 收尾重跑，Windows，Go 1.26.5，`GOPROXY=off`，命令从仓库根目录执行）**：`go -C apps/api test ./...`、`go -C apps/api vet ./...`、Windows/Linux `go -C apps/api build ./...` 均 exit 0；`go -C apps/api test ./internal/credentials -run='^$' -fuzz='^FuzzPayloadDecoder$' -fuzztime=30s`（~34.7s / 218695 execs）与 `-fuzz='^FuzzAAD$'`（~31.2s / 46497 execs）均 **PASS**（无 panic/crash，exit 0）。
- **本机 race 限制**：Windows `CGO_ENABLED=0`，`go -C apps/api test -race ./...` 报 `-race requires cgo`（exit 2），**本机不声明 race PASS**；由 main CI Race test success 覆盖。
- **P1/R6 修复（WEB-11 收尾审查创建，均已合并）**：WEB-26 凭证 per-field 长度限制（PR #34）、WEB-24 并发轮换/回滚 PostgreSQL 集成测试（PR #35）、WEB-25 审计原子性 D11（设计 PR #36 + 代码实施 PR #38）、WEB-27 生产角色拆分（PR #37）。

## 验证与回滚

- WEB-22/WEB-23 的测试矩阵覆盖正常、边界、故障注入、并发、fuzz 和跨平台场景
- 回滚：`git revert 0af2625b6a00c07563e0e8ebf188e2811e1bf571`（WEB-22 / PR #31）、`git revert 94eb3ca89a1bfb3e843af7209df45ae1ff37a2c2`（WEB-23 / PR #32）；credential_envelopes 与 audit_events 仅追加写，数据不受影响
- KEK 紧急轮换（两阶段）：(1) 所有实例添加 `WEBDB_KEK_V{N+1}` 并滚动重启（加载新 KEK，仍用旧版写入）；(2) 确认全部正常后更新 `WEBDB_ACTIVE_KEK_VERSION={N+1}` 并再次滚动重启（切换写入版本）。回滚时恢复 ACTIVE 为旧版值

## 相关资料

- [ADR-006：KEK 由部署环境注入](ADR-006-kek-in-deployment-environment.md)
- [ADR-010：查询结果保留 7 天](ADR-010-query-result-retention.md)
- [ADR-013：P0 元数据库迁移与 Schema 基线](ADR-013-p0-metadata-migrations-schema.md)
- [P0-05 凭证与审计方案](../tasks/P0-05-proposal-credentials-and-audit.md)
- [P0-05 威胁模型](../tasks/P0-05-threat-model.md)

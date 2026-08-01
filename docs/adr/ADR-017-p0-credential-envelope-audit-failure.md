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

- **数据加密**：AES-256-GCM（256-bit DEK，96-bit nonce）
- **DEK 包装**：AES-256-GCM（使用 KEK，96-bit nonce）
- **Nonce 生成**：`crypto/rand.Read`（失败时 fail-closed）
- **AAD**：Canonical JSON 绑定 `workspace_id`、`secret_ref`、`secret_version`、`envelope_suite`、`kek_version`
- **`envelope_suite`**：`"AES256GCM-v1"`（精确匹配，未知值拒绝）
- **Go stdlib only**：不新增第三方依赖

### 3. KEK Provider

- **注入方式**：环境变量 `WEBDB_KEK_V{N}`（Base64 编码，32 bytes）
- **启动验证**：解析并验证所有 KEK；无有效 KEK → fatal
- **拒绝弱值**：`change_me`、空字符串、非 Base64、长度错误 → fatal
- **版本行为**：写入使用最高版本；读取按 `kek_version` 查找对应 KEK；未知版本拒绝
- **安全约束**：KEK 不进入仓库、镜像、DB、日志、错误、审计、API 响应或普通测试夹具

### 4. 凭证生命周期

- **创建**：生成 secret_ref UUID → version=1 → 加密 → INSERT
- **读取**：授权 → 查找 envelope → 验证 AAD → 解密 DEK → 解密 payload → 验证 schema
- **轮换**：INSERT 新版本 + UPDATE connections（同一事务，SELECT FOR UPDATE）
- **退役**：SET retired_at=now()；不删除密文；仍可解密
- **并发**：事务隔离；失败回滚

### 5. 审计事件

- **17 类事件**：connection.create/update、credential.create/rotate/retire、connection.test、sql.execute（denied/succeeded/failed/timeout/cancelled）、credential.lookup/decrypt 失败、unknown KEK version、audit.write 失败
- **Metadata 允许列表**：14 个字段，精确格式校验，禁止自由文本
- **禁止字段**：SQL 正文、密码、KEK/DEK、nonce、连接串、目标库结果、原始数据库错误

### 6. 审计失败策略

- **分阶段 fail-closed**：与 P0-04 §8.6 一致
- 执行前审计失败：不调用 Adapter，返回 `audit_failed`
- 执行后审计失败：不返回结果，返回 `audit_failed`，execution 已记录为 `completed`
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
- KEK 轮换需要在所有 WebDB 实例上同步更新环境变量
- 丢失 KEK 环境变量等同于永久失去对应 envelope 的解密能力
- 审计事件保留期需 Owner 单独决策（推荐 P0 阶段 90 天）

## 验证与回滚

- WEB-22/WEB-23 的测试矩阵覆盖正常、边界、故障注入、并发、fuzz 和跨平台场景
- 回滚：`git revert` WEB-22/WEB-23 合并提交；credential_envelopes 仅追加写，数据不受影响
- KEK 紧急轮换：添加新环境变量 → 重启 → 新凭证使用新 KEK

## 相关资料

- [ADR-006：KEK 由部署环境注入](ADR-006-kek-in-deployment-environment.md)
- [ADR-010：查询结果保留 7 天](ADR-010-query-result-retention.md)
- [ADR-013：P0 元数据库迁移与 Schema 基线](ADR-013-p0-metadata-migrations-schema.md)
- [P0-05 凭证与审计方案](../tasks/P0-05-proposal-credentials-and-audit.md)
- [P0-05 威胁模型](../tasks/P0-05-threat-model.md)

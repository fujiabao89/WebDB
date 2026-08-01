# P0-05：凭证与审计威胁模型

> 状态：已批准（Owner Gate 通过）｜日期：2026-08-01｜作者：Claude Code｜批准人：fujiabao89
>
> 配合 `P0-05-proposal-credentials-and-audit.md`。本威胁模型必须在 Owner 批准后才能作为 WEB-22/WEB-23 的安全基线。

---

## 1. 资产清单

| 资产 | 机密性 | 完整性 | 可用性 | 存储位置 |
|---|---|---|---|---|
| **KEK**（密钥加密密钥） | 极高 | 极高 | 高 | 部署环境变量（进程内存） |
| **DEK**（数据加密密钥） | 极高 | 极高 | 中 | wrapped 形式在 `credential_envelopes.wrapped_dek` |
| **凭证明文**（user, password） | 极高 | 高 | 中 | 仅进程内存，不持久化 |
| **Credential Envelope**（ciphertext, nonce, wrapped DEK） | 中 | 极高 | 高 | `credential_envelopes` 表 |
| **Secret Ref/Version** | 低 | 极高 | 高 | `connections` 和 `credential_envelopes` 表 |
| **Execution/Audit Metadata** | 中 | 极高 | 高 | `executions` 和 `audit_events` 表 |

---

## 2. 信任边界

```text
┌──────────────────────────────────────────────────────────┐
│  浏览器/API 客户端                                        │
│  - 不拥有 KEK/DEK/密码                                    │
│  - 不直接连接目标数据库                                   │
│  - 不可信输入（workspace_id, connection_id, SQL, args）    │
└────────────────────┬─────────────────────────────────────┘
                     │ HTTPS (不可信网络)
┌────────────────────▼─────────────────────────────────────┐
│  WebDB API 服务 (Go)                                     │
│  - 持有 KEK（进程内存）                                   │
│  - 加解密、凭证解析、KEK Provider                         │
│  - SQL 策略、执行、审计                                   │
│  - 信任边界内部：凭证从不解密直到授权+SQL策略通过          │
└──────┬──────────────────────────────────┬────────────────┘
       │ TLS + 受限账号                    │ TLS + 受限账号
┌──────▼──────────┐              ┌────────▼───────────────┐
│  元数据库 (PG)   │              │  目标 PG/MySQL          │
│  - 密文存储      │              │  - 凭证明文用于连接     │
│  - 审计追加      │              │  - 业务数据             │
│  - 不可信输入    │              │  - 不可信（结果不入审计)│
└─────────────────┘              └────────────────────────┘
       │
┌──────▼──────────┐
│  部署环境        │
│  - KEK 注入      │
│  - 操作系统/内核  │
│  - Docker/Compose │
│  - 信任边界：KEK 来源必须可信                              │
└─────────────────┘
       │
┌──────▼──────────┐
│  日志与监控      │
│  - 结构化日志     │
│  - 安全告警       │
│  - 不得包含 KEK/DEK/密码/SQL 正文                         │
└─────────────────┘
```

---

## 3. 攻击者能力假设

| 假设 | 能力 | 备注 |
|---|---|---|
| **A1: 网络攻击者** | 可嗅探浏览器↔API 流量（MITM） | HTTPS 缓解；P0 部署在私有网络内 |
| **A2: 恶意客户端** | 可发送任意 API 请求、伪造 UUID、跨 workspace | RBAC、workspace 隔离、外键约束缓解 |
| **A3: 元数据库访问者** | 可读取 `credential_envelopes` 表（含密文） | 无 KEK 无法解密；DBA 可修改/删除触发器 |
| **A4: 目标数据库访问者** | 可获得由 WebDB 使用的数据库账号 | 最小权限账号；账号密码在 WebDB 内存中 |
| **A5: 主机入侵者** | 可读取进程内存、环境变量 | 可获取 KEK 和凭证明文；缓解措施有限 |
| **A6: 日志访问者** | 可读取应用日志、错误输出 | 日志脱敏防止密钥泄漏 |

**明确不假设的前提**：
- 部署主机本身是安全的（root 入侵不在 P0 威胁模型防护范围内）
- TLS 证书有效且未被攻破
- Docker 守护进程可信
- PostgreSQL 的 SUPERUSER 账号由运维管理

---

## 4. 数据流与密钥流

```text
KEK 流:
  环境变量 → KEKProvider(进程内存) → AES-GCM(wrap) → wrapped_dek(DB)
                                   → AES-GCM(open) → DEK(进程内存)

DEK 流:
  crypto/rand → DEK(32B) → AES-GCM(seal) → ciphertext(DB)
                          → AES-GCM(wrap by KEK) → wrapped_dek(DB)

凭证明文流:
  API → 授权(A) → Execution(B) → SQL策略(C) → ★解密(C') → Adapter(D)
                                                      │
                                            ConnectConfig{User,Password}
                                                      │
                                            Adapter → 目标数据库连接

审计流:
  每个阶段 → sanitizeAuditMetadata() → INSERT INTO audit_events
                                      → 失败 → audit_failed / $SECURITY_ALERT
```

---

## 5. 威胁矩阵

### 5.1 KEK 相关威胁

| # | 威胁 | 触发条件 | 影响 | 缓解措施 | 验证测试 |
|---|---|---|---|---|---|
| T1 | KEK 从环境变量泄漏 | 主机入侵、配置错误、日志误记录 | 所有 envelope 可被解密 | KEK 不进入日志/错误/DB；启动验证拒绝弱值 | KEK-05, 日志扫描 |
| T2 | 弱默认 KEK | 部署者未设置环境变量 | 加密形同虚设 | 启动时检测并 fatal（无默认值；`change_me` 等拒绝） | KEK-02, KEK-05 |
| T3 | KEK 版本混淆 | `kek_version` 指向错误密钥 | 解密失败或使用错误密钥 | `kek_version` 绑定在 AAD 中，GCM 认证失败 | ENC-03 |
| T4 | 未知 KEK 版本 | envelope 引用的 `kek_version` 无对应环境变量 | 历史数据无法解密 | 启动时警告；运行时返回 `unknown_kek_version`；不尝试解密 | KEK-07 |

### 5.2 密文完整性威胁

| # | 威胁 | 触发条件 | 影响 | 缓解措施 | 验证测试 |
|---|---|---|---|---|---|
| T5 | 密文替换（跨workspace） | 攻击者将 envelope A 的密文复制到 workspace B | 可能解密为错误凭证明文 | AAD 绑定 `workspace_id`；GCM 认证失败 | ENC-10 |
| T6 | 密文替换（跨secret_ref） | 攻击者在同一 workspace 内替换 | 连接获得错误凭证 | AAD 绑定 `secret_ref`；GCM 认证失败 | ENC-11 |
| T7 | 密文替换（跨版本） | 攻击者用旧版本密文替换新版本 | 回退到旧凭证 | AAD 绑定 `secret_version`；GCM 认证失败 | ENC-12 |
| T8 | AAD 混淆/伪造 | AAD 构造错误或字段缺失 | GCM 认证失败或绕过 | 版本化确定性二进制编码（48 bytes）；AAD 字段验证 | ENC-03, ENC-04, ENC-05 |
| T9 | Ciphertext 篡改 | 数据库中密文被修改 | 解密失败或解密为错误数据 | GCM 认证标签（16 bytes AEAD tag） | ENC-06 |
| T10 | Nonce 篡改 | data_nonce 或 wrap_nonce 被修改 | 解密失败 | GCM 认证失败；与密文一起认证 | ENC-07, ENC-09 |
| T11 | Wrapped DEK 篡改 | wrapped_dek 被修改 | 无法恢复 DEK | GCM 认证失败 | ENC-08 |

### 5.3 Nonce 重用威胁

| # | 威胁 | 触发条件 | 影响 | 缓解措施 | 验证测试 |
|---|---|---|---|---|---|
| T12 | Data nonce 重用 | 同一 DEK 下 data_nonce 重复 | GCM 安全性完全破坏（可恢复明文、伪造密文） | `crypto/rand` 生成 96-bit nonce；每次加密新 DEK | ENC-17, fuzz |
| T13 | Wrap nonce 重用 | 同一 KEK 下 wrap_nonce 重复 | DEK 可被恢复 | `crypto/rand` 生成；概率 ≈ 2^-96 | ENC-18, fuzz |

### 5.4 Downgrade 威胁

| # | 威胁 | 触发条件 | 影响 | 缓解措施 | 验证测试 |
|---|---|---|---|---|---|
| T14 | Suite downgrade | 攻击者修改 `envelope_suite` 为弱算法 | 使用弱算法解密 | `envelope_suite` 绑定在 AAD 中；未知值拒绝 | ENC-13 |
| T15 | KEK version downgrade | 攻击者修改 `kek_version` 指向旧 KEK | 使用可能已泄露的旧 KEK | `kek_version` 绑定在 AAD 中；KEK 版本不匹配时 GCM 失败 | ENC-03 |

### 5.5 日志与审计泄漏威胁

| # | 威胁 | 触发条件 | 影响 | 缓解措施 | 验证测试 |
|---|---|---|---|---|---|
| T16 | 日志泄漏 KEK/密码 | 错误消息或日志包含敏感字段 | 密钥/凭证暴露给日志访问者 | 结构化日志；敏感字段自动脱敏；代码审查禁止 `%v`/`%+v` 打印凭证结构体 | INT-04, INT-05 |
| T17 | 错误响应泄漏 | API 错误响应包含密码或数据库错误 | 凭证暴露给客户端 | 稳定错误码；message 为固定安全摘要 | INT-06 |
| T18 | 审计 metadata 注入 | 攻击者构造 metadata 使敏感数据通过允许列表 | SQL/密码进入审计 | 精确格式校验（非启发式）；逐字段类型/格式约束 | INT-07 |
| T19 | 审计 metadata 含原始错误 | `error_code` 包含 `pq:` 或 `MySQL Error` 前缀 | 数据库结构信息泄漏 | `error_code` 必须是稳定枚举值 | AUDIT-11（P0-04 已有） |

### 5.6 凭证生命周期威胁

| # | 威胁 | 触发条件 | 影响 | 缓解措施 | 验证测试 |
|---|---|---|---|---|---|
| T20 | 凭证解密过早 | SQL 策略拒绝后仍解密凭证 | 绕过策略仍获取凭证明文 | 阶段 C'（凭证解析）在阶段 C（SQL 策略）**之后** | INT-01 |
| T21 | 凭证失败后仍调 Adapter | 解密失败但代码路径继续执行 | 空密码或错误密码连接目标库 | 阶段 C' 失败 → Adapter.Query=0 | INT-02, INT-03 |
| T22 | 轮换部分失败 | 事务中间失败 | 连接引用不一致 | 事务回滚；旧版本不变 | LIFE-08, LIFE-07 |
| T23 | 并发轮换 | 两个轮换同时执行 | 版本号冲突或连接引用混乱 | SELECT FOR UPDATE；事务隔离 | LIFE-07 |
| T24 | 退役版本仍被引用 | 新连接引用已退役版本 | 使用旧凭证连接 | 应用层拒绝引用 `retired_at IS NOT NULL` 的版本 | 应用层约束 |

### 5.7 审计失败与告警威胁

| # | 威胁 | 触发条件 | 影响 | 缓解措施 | 验证测试 |
|---|---|---|---|---|---|
| T25 | 审计静默失败 | 审计写入失败但返回成功 | 操作无审计记录 | fail-closed；`audit_failed` 错误码 | AUDIT-01, AUDIT-02 |
| T26 | 审计写入失败时仍执行 | 审计失败后继续调用 Adapter | 无审计记录的操作被执行 | 分阶段 fail-closed；执行前审计失败不调用 Adapter | AUDIT-01 |
| T27 | 阶段 D 完成后重复执行 | 客户端因 `audit_failed` 重试 | SELECT 副作用重复 | 不自动重试；客户端收到明确 message | AUDIT-02 |
| T28 | 安全告警自身失败 | 告警系统不可用 | 安全事件无人知晓 | 写入 stderr/syslog 作为最终 fallback | 基础设施测试 |

### 5.8 基础设施威胁

| # | 威胁 | 触发条件 | 影响 | 缓解措施 | 验证测试 |
|---|---|---|---|---|---|
| T29 | 数据库备份泄漏 | 元数据库备份被窃取 | 所有 envelope 密文和 metadata 泄漏 | 备份不含 KEK；无 KEK 无法解密密文 | 备份不含环境变量 |
| T30 | Panic/取消/超时路径 | 异常退出时明文未清理 | 凭证明文残留在内存 dump | Go 语言限制；最小化明文生命周期；不承诺可靠清零 (R1) | race test |
| T31 | 随机源失败 | `crypto/rand.Read` 返回 error | 无法生成密钥材料 | fail-closed；不回退弱随机源 | 模拟故障 |

---

## 6. 残余风险

| # | 风险 | 不可缓解原因 | 接受条件 |
|---|---|---|---|
| R1 | Go GC 移动内存导致明文残留 | Go 语言设计限制 | 文档记录；最小化明文生命周期（D15 条件接受） |
| R2 | `crypto/rand` 在极端熵耗尽时返回错误 | 系统级故障，概率极低 | fail-closed，不降级弱随机源（D15 条件接受） |
| R3 | 96-bit GCM nonce 在 >2^32 次加密后可能重用 | 密码学概率极限 | 加入每 KEK 2^24 次加密上限，远低于 nonce 重用阈值（D15 接受） |
| R4 | KEK 环境变量在进程内存中可被调试器读取 | 需要主机 root 权限 | 部署环境保护（D15 条件接受） |
| R5 | `SELECT func()` 副作用（含 SECURITY DEFINER） | AST 无法判断函数副作用 | P0-04 已有缓解（沿用 P0-04 既有决策） |
| R6 | 审计事件不防内部 DBA 篡改（触发器可被 SUPERUSER 绕过） | 数据库权限模型的根本限制 | 创建生产角色拆分任务（D15） |
| R7 | 服务重启后 Continuation Token 全部失效 | ADR-015 已接受的限制 | 内存 Registry 的设计取舍（沿用 ADR-015） |

> 上述 R1-R7 编号、内容和接受条件与 `P0-05-proposal-credentials-and-audit.md` §13 残余风险清单严格一致。

---

## 7. 已接受的限制（Owner D1-D15 已批准）

以下限制已由 Owner 明确批准或由相关 ADR 接受：

1. **不使用企业 KMS**：KEK 由环境变量注入（ADR-006 已接受）
2. **不保证内存清零**：Go 语言限制，明文最小化生命周期缓解（ADR-017 已接受，D15 条件接受）
3. **审计触发器可被 SUPERUSER 绕过**：生产部署需拆分数据库角色（ADR-013 已实现，D15 创建生产角色拆分任务）
4. **服务重启使 Continuation Token 失效**：ADR-015 已接受
5. **不防止主机 root 入侵**：超出 P0 威胁模型范围。主机入侵可导致 KEK/密码的内存读取（R4）、随机源操纵（R2）等多重风险，但 P0 无法防护具有 root 权限的攻击者

---

## 8. 测试编号对应

| 测试类别 | 测试 ID 范围 | 覆盖威胁 |
|---|---|---|
| Payload | PAY-01–PAY-12 | T20（凭证解密过早）、T21（凭证失败后调 Adapter） |
| 加密 | ENC-01–ENC-18 | T3（KEK 版本混淆）、T5-T11（密文完整性）、T12-T13（nonce 重用）、T14-T15（downgrade） |
| KEK Provider | KEK-01–KEK-08 | T1（KEK 泄漏）、T2（弱默认 KEK）、T4（未知 KEK 版本）、T31（随机源失败） |
| 生命周期 | LIFE-01–LIFE-09 | T22（轮换部分失败）、T23（并发轮换）、T24（退役版本引用） |
| 集成断言 | INT-01–INT-10 | T16（日志泄漏）、T17（错误响应泄漏）、T18（审计 metadata 注入）、T20、T21 |
| 审计故障注入 | AUDIT-01–AUDIT-04 | T25（审计静默失败）、T26（审计失败时仍执行）、T27（阶段 D 后重复执行） |
| 质量门禁 | QA-01–QA-08 | T28（安全告警失败）、T29（备份泄漏）、T30（panic/取消/超时） |

T19（审计 metadata 含原始错误）由 P0-04 的 AUDIT-11 覆盖（`docs/tasks/P0-04-proposal-contract-and-parser.md` §9.5）。

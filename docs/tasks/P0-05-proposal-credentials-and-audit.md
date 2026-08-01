# P0-05：凭证信封加密、KEK 生命周期与审计失败策略

> 状态：已批准（Owner Gate 通过）｜日期：2026-08-01｜作者：Claude Code｜批准人：fujiabao89
>
> Owner 已对 D1-D15 全部决策做出明确决定。本方案冻结 P0-05 的安全设计基线。
> WEB-22（凭证信封加密）可基于本方案启动生产实现。WEB-23（审计接入）须等待 WEB-22 完成。

---

## 1. 现状证据

### 1.1 现有 Schema（ADR-013，已实现）

`credential_envelopes` 表已包含所有必需的加密字段：

| 列 | 类型 | 约束 |
|---|---|---|
| `workspace_id` | UUID NOT NULL | FK → workspaces(id) |
| `secret_ref` | UUID NOT NULL | 凭证引用三元组之一 |
| `version` | INTEGER NOT NULL | CHECK (version > 0) |
| `ciphertext` | BYTEA NOT NULL | CHECK (octet_length > 0) |
| `data_nonce` | BYTEA NOT NULL | CHECK (octet_length > 0) |
| `wrapped_dek` | BYTEA NOT NULL | CHECK (octet_length > 0) |
| `wrap_nonce` | BYTEA NOT NULL | CHECK (octet_length > 0) |
| `envelope_suite` | TEXT NOT NULL | CHECK (btrim <> '') |
| `kek_version` | INTEGER NOT NULL | CHECK (kek_version > 0) |
| `created_at` | TIMESTAMPTZ | |
| `retired_at` | TIMESTAMPTZ | 可空 |

`connections` 表仅保存 `secret_ref` + `secret_version`，通过三列复合外键引用信封。

### 1.2 现有 Adapter 连接配置

`apps/api/internal/adapter/types.go:80-96` — `ConnectConfig`：

```go
type ConnectConfig struct {
    ConnectionID   string
    SecretVersion  int
    ConfigRevision int64
    Engine         Engine
    Host           string
    Port           int
    User           string   // ← 需要从凭证 payload 解密
    Password       string   // ← 需要从凭证 payload 解密
    Database       string
    TLS            TLSMode
    MaxOpen        int
    MaxIdle        int
    MaxPageBytes   int
    MaxCellBytes   int
}
```

**关键事实**：Adapter 当前需要的凭证字段是 `User` 和 `Password`。TLS 材料（CA 证书、客户端证书等）在 P0 阶段不属于 Adapter 需求。

### 1.3 现有执行生命周期（P0-04，已实现）

```text
阶段 A: 身份与授权（前置）
阶段 B: 创建 Execution（pending）
阶段 C: SQL lexical + AST 策略判断
阶段 D: 执行（Adapter）
```

凭证解密发生在阶段 D 之前、阶段 C 之后——即在授权通过、SQL 策略允许后，才解析凭证并传给 Adapter。

### 1.4 审计脱敏（WEB-23 已实现，替代 P0-04 启发式）

WEB-23 已将审计脱敏改为强类型 `metadata.AuditMetadata`（16 字段允许列表 + E5 专用 expected_version/actual_version），`ValidateAuditEventMetadata` 做事件级 fail-closed 校验：畸形 JSON、未知字段、错误类型、超长值一律拒绝。`looksLikeSQL()`/`looksLikeCredential()` 启发式不再作为审计安全边界。扩展字段（`statement_hash`、`duration_ms`、`error_code`、`reason_code`、`engine`、`environment`）及凭证字段（`secret_ref`、`secret_version`、`old_version`、`new_version`、`envelope_suite`、`kek_version`）均已实现。

### 1.5 当前可用能力（WEB-22/WEB-23 已实现）

- 凭证加解密已实现：`Connection.SecretRef`/`SecretVersion` 通过信封解密为 `ConnectConfig.User`/`ConnectConfig.Password`
- KEK Provider 已实现（环境变量注入，ADR-006）
- 凭证生命周期（创建/轮换/退役）已实现，并接入 E3-E6 审计
- 审计事件接入已完成：E1-E16 持久化审计 + E17 独立安全告警通道

---

## 2. 目标与非目标

### 目标

1. 定义 Credential Payload 最小化契约
2. 固定信封加密方案（AEAD、DEK wrapping、AAD、nonce）
3. 定义 KEK Provider 接口与部署注入行为
4. 定义凭证创建、读取、轮换、退役生命周期
5. 定义审计事件契约与 metadata 允许列表
6. 定义各执行阶段审计失败策略
7. 建立威胁模型与可执行测试矩阵
8. 建立 Owner Gate 决策表

### 非目标

- 不修改生产代码、go.mod/go.sum、Schema 或部署配置
- 不接入企业 KMS
- 不实现登录/OIDC、DML/DDL、公开 HTTP SQL API
- 不改变 ADR-010 的 7 天结果保留策略
- 不在 P0 实施审计事件的自动删除或精确归档机制（保留期 D12 已批准：至少 90 天）

---

## 3. Credential Payload 契约

### 3.1 设计依据

Adapter `ConnectConfig` 当前需要的凭证字段：
- `User` (string) — 数据库用户名
- `Password` (string) — 数据库密码

P0 不实现 TLS 客户端证书、SSH 密钥或 OAuth token。这些是 P1+ 的扩展。

### 3.2 Payload Schema v1

```json
{
  "v": 1,
  "user": "db_username",
  "password": "db_password"
}
```

| 字段 | 必需 | 类型 | 大小限制 | 说明 |
|---|---|---|---|---|
| `v` | 是 | integer | — | Payload schema 版本，当前固定为 1 |
| `user` | 是 | string | 1..255 字节 | 数据库用户名 |
| `password` | 是 | string | 1..1024 字节 | 数据库密码 |

### 3.3 编码与规范化

- 编码格式：**JSON**（UTF-8）
- 规范化：**user 和 password 保持原值，不执行 TrimSpace 或 Unicode 规范化。** 原始字节直接用于连接目标数据库。
- 序列化：使用 `json.Marshal`，无缩进，按 Go `struct` 字段顺序
- `v` 字段必须为 `1`（正整数），其他值拒绝
- 未知字段：**拒绝**（使用 `json.Decoder` + `DisallowUnknownFields`）
- 重复字段：Go `encoding/json` 默认使用最后一个值，payload 验证应使用 `json.Decoder` 逐 token 解析检测重复键

**大小上限**：
- 单个 payload JSON：**4096 字节**（含 `v`、`user`、`password` 字段和 JSON 结构）
- 超限行为：拒绝加密，返回 `payload_too_large`

**非法输入行为**：
- 空字符串 `user`/`password`：拒绝，返回 `invalid_payload`
- 非法 UTF-8：拒绝，返回 `invalid_payload`
- 控制字符（U+0000–U+001F，不含 U+0009 Tab）：`password` 允许（密码可能含控制字符），`user` 拒绝

### 3.4 明文生命周期

- 明文仅在解密后、`ConnectConfig` 构造期间存在于内存
- 解密后立即构造 `ConnectConfig`，不保存到任何持久化存储
- `ConnectConfig.Password` 传递给 Adapter 后，调用方不保留引用
- 明文不得进入：API 响应、浏览器、日志、错误消息、审计 metadata、数据库
- **Go 语言限制**：Go 的 GC 可能复制/移动内存，无法保证可靠清零。实现时应在使用后立即将局部变量置为 `""`，使用 `runtime.KeepAlive` 防止过早回收，并在文档中如实记录此限制。

---

## 4. 信封加密方案

### 4.1 候选方案

| 方案 | 数据加密 | DEK 包装 | 依赖 | 评价 |
|---|---|---|---|---|
| **A（推荐）** | AES-256-GCM (256-bit DEK) | AES-256-GCM (256-bit KEK) | Go stdlib (`crypto/aes`, `crypto/cipher`) | 简单、无外部依赖、stdlib 审计良好 |
| B | AES-256-GCM | AES-KWP (RFC 5649) | 需自行实现或第三方库 | RFC 3394/5649 更标准但 Go 无 stdlib 支持 |
| C | XChaCha20-Poly1305 | AES-256-GCM | 需 `golang.org/x/crypto` 或第三方 | 更大 nonce 但增加依赖 |

**推荐方案 A**，理由：
1. **零新增依赖**：Go stdlib 提供 `crypto/aes`、`crypto/cipher`、`crypto/rand`
2. **充分安全**：AES-256-GCM 提供 AEAD（认证加密），96-bit nonce 在 2^32 次加密内安全
3. **简单审计**：stdlib 实现经过大量审查，减少攻击面

### 4.2 推荐方案详细参数

| 参数 | 值 | 说明 |
|---|---|---|
| 数据加密算法 | **AES-256-GCM** | AEAD，提供机密性+完整性+认证 |
| DEK 长度 | **256 bits (32 bytes)** | `crypto/rand` 生成 |
| DEK 包装算法 | **AES-256-GCM** | 使用 KEK 作为密钥 |
| 数据 nonce 长度 | **96 bits (12 bytes)** | GCM 标准 nonce 大小 |
| Wrap nonce 长度 | **96 bits (12 bytes)** | 用于 DEK wrapping 的 nonce |
| Nonce 生成 | `crypto/rand.Read` | CSPRNG |
| AAD 编码 | **版本化确定性二进制编码**（见 §4.4） | 48 bytes，大端序 |
| KDF | **无** | KEK 已是高熵 256-bit 密钥，不需要密码型 KDF |

### 4.3 随机源失败行为

- `crypto/rand.Read` 在 Linux 上使用 `getrandom(2)`，在 Windows 上使用 `ProcessPrng`
- 系统熵池耗尽（极罕见）时 `crypto/rand.Read` 返回 error
- **行为**：返回 `internal_error`，不执行加密，不写入数据库，不调用 Adapter
- 不允许回退到弱随机源（如 `math/rand`）

### 4.4 AAD 规范编码（Owner 决策 D4：二进制编码）

**数据 AAD**（用于 payload 加密的 AES-256-GCM Seal/Open）和 **Wrap AAD**（用于 DEK 包装的 AES-256-GCM Seal/Open）均使用**版本化确定性二进制编码**。

#### 4.4.1 二进制编码格式

AAD 为以下字段按固定顺序以大端序（big-endian）拼接的字节序列：

```text
AAD = version_tag(4B) || workspace_id(16B) || secret_ref(16B) || secret_version(4B) || envelope_suite_tag(4B) || kek_version(4B)
```

| 字段 | 大小 | 编码 | 示例值 |
|---|---|---|---|
| `version_tag` | 4 bytes | 固定 `0x00000001`（AAD 格式版本 1） | `00 00 00 01` |
| `workspace_id` | 16 bytes | UUID 原始字节（`uuid.UUID` 的 `[16]byte`） | — |
| `secret_ref` | 16 bytes | UUID 原始字节 | — |
| `secret_version` | 4 bytes | int32 大端序 | `00 00 00 01` (v1) |
| `envelope_suite_tag` | 4 bytes | 枚举映射（`0x00000001` = AES256GCM-v1） | `00 00 00 01` |
| `kek_version` | 4 bytes | int32 大端序 | `00 00 00 01` (V1) |

总长度：**48 bytes**。所有整数字段使用大端序（big-endian），UUID 字段使用 RFC 4122 原始 16 字节表示。

#### 4.4.2 数据 AAD 与 Wrap AAD

- **数据 AAD**：上述完整 48-byte 序列。在 `AES-256-GCM-Seal(DEK, data_nonce, plaintext, data_aad)` 中使用。
- **Wrap AAD**：独立的 48-byte 序列，字段值与数据 AAD 相同，但作为独立参数传入 `AES-256-GCM-Seal(KEK, wrap_nonce, DEK, wrap_aad)`。**Wrap AAD 禁止为 nil。**

#### 4.4.3 绑定目的

| 字段 | 防止的攻击 |
|---|---|
| `workspace_id` | 跨工作区密文替换 |
| `secret_ref` | 跨 secret 密文替换 |
| `secret_version` | 跨版本密文替换 |
| `envelope_suite_tag` | Suite 混淆/downgrade |
| `kek_version` | KEK 版本混淆 |
| `version_tag` | AAD 格式演进兼容 |

### 4.5 加密流程

```text
1. DEK ← crypto/rand(32 bytes)
2. data_nonce ← crypto/rand(12 bytes)
3. data_aad ← binaryEncode(version_tag=1, workspace_id, secret_ref, secret_version, suite_tag, kek_version)
4. plaintext ← json.Marshal(payload)  // 验证 schema 后
5. ciphertext ← AES-256-GCM-Seal(DEK, data_nonce, plaintext, data_aad)
   // ciphertext 包含 128-bit GCM 认证标签（附加在末尾 16 bytes）

6. wrap_nonce ← crypto/rand(12 bytes)
7. wrap_aad ← binaryEncode(version_tag=1, workspace_id, secret_ref, secret_version, suite_tag, kek_version)
   // wrap_aad 与 data_aad 内容相同但独立构造，禁止为 nil
8. wrapped_dek ← AES-256-GCM-Seal(KEK, wrap_nonce, DEK, wrap_aad)

9. 持久化: (ciphertext, data_nonce, wrapped_dek, wrap_nonce, envelope_suite, kek_version)
```

**每 KEK 加密次数上限**：单个 KEK 版本最多用于 `2^24` 次 DEK 包装操作（约 1677 万次），达到上限后该 KEK 版本拒绝新的包装请求（仍可用于解密），强制部署者轮换 KEK。此上限远低于 GCM nonce 重用的安全阈值（2^32），提供充足安全余量。

**计数实现**：P0 使用进程内原子计数器（`atomic.Uint64`），在 Seal 前通过 CAS 原子预留额度。额度按包装尝试计数；一旦预留，后续随机源、加密、持久化或事务提交失败也不归还，因为安全上限约束的是 nonce/包装尝试而非成功写入行数。服务重启后计数器归零——此上限旨在防止持续运行期间的 nonce 碰撞，而非提供不可绕过的硬配额。此限制记录为残余风险（R3 注释）。生产环境应通过监控 DEK 包装速率并在接近阈值时提前预警。跨实例部署时各实例独立计数，实际包装总量可能略超 2^24，但仍在 GCM 安全边界内。

### 4.6 解密流程

```text
1. 验证 envelope_suite 为已知版本 → 否则返回 unknown_envelope_suite
2. 验证 kek_version 有对应 KEK → 否则返回 unknown_kek_version（见 §5）
3. data_aad ← binaryEncode(version_tag=1, workspace_id, secret_ref, secret_version, suite_tag, kek_version)
4. wrap_aad ← binaryEncode(version_tag=1, workspace_id, secret_ref, secret_version, suite_tag, kek_version)
5. DEK ← AES-256-GCM-Open(KEK, wrap_nonce, wrapped_dek, wrap_aad)
   → 失败：返回 decryption_failed（不区分 DEK 或 payload 失败，防 oracles）
6. plaintext ← AES-256-GCM-Open(DEK, data_nonce, ciphertext, data_aad)
   → 失败：返回 decryption_failed
6. 验证 payload schema → 失败：返回 invalid_payload
7. 返回 CredentialPayload{User, Password}
```

### 4.7 密文篡改行为

- GCM 认证失败时返回统一的 `decryption_failed` 错误
- 不区分"DEK 解密失败"和"payload 解密失败"（防止 padding oracle 类攻击）
- 不返回原始解密错误详情
- 错误消息不包含 nonce、ciphertext、DEK 或密钥材料

### 4.8 `envelope_suite` 版本语义

```text
"AES256GCM-v1"
  │         │
  │         └─ 格式版本（v1 = 当前版本）
  └─ 算法标识（AES-256-GCM 数据加密 + AES-256-GCM DEK 包装）
```

- `envelope_suite` 必须精确匹配；前缀匹配、大小写不敏感匹配均不允许
- 未知 `envelope_suite` 值时：拒绝解密，返回 `unknown_envelope_suite`
- 未来算法升级时使用新 suite 字符串（如 `AES256GCM-v2` 或 `XCHACHA20POLY1305-v1`），旧 suite 继续支持解密，写入始终使用当前版本

### 4.9 密码学约束

- 禁止自行设计算法或非标准加密格式
- 禁止使用 ECB、CBC、CTR 模式
- 禁止使用 MD5、SHA-1
- 禁止使用 `math/rand` 生成密钥材料
- 禁止使用固定 nonce 或计数器 nonce
- Go stdlib 能满足全部需求，不建议新增第三方加密库

---

## 5. KEK Provider

### 5.1 接口契约

```go
// KEKProvider 提供版本化 KEK 访问。
// P0 实现从部署环境变量注入；未来可扩展为 KMS 实现。
type KEKProvider interface {
    // ActiveKEK 返回当前写入加密使用的 KEK 版本和密钥。
    ActiveKEK() (version int, key []byte, err error)

    // GetKEK 返回指定版本的 KEK（用于历史解密）。
    // 版本不存在时返回 ErrUnknownKEKVersion。
    GetKEK(version int) ([]byte, error)
}
```

### 5.2 部署环境注入

- KEK 通过环境变量 `WEBDB_KEK_V1`、`WEBDB_KEK_V2` 等注入
- 变量名格式：`WEBDB_KEK_V{version}`（version 为正整数）
- **当前写入版本由独立环境变量 `WEBDB_ACTIVE_KEK_VERSION` 显式指定**（值为正整数），不得自动选择最大版本号
- 环境变量为空：启动时 **fatal**，拒绝启动
- `WEBDB_ACTIVE_KEK_VERSION` 指向的版本必须有对应的 `WEBDB_KEK_V{N}` 变量，否则 fatal
- 有效的 KEK 版本至少有 `V1`

### 5.3 KEK 编码与长度

- KEK 编码：**Base64 标准编码**（RFC 4648 §4，含 padding `=`）
- 解码：**严格解码**，拒绝非标准字符和缺失 padding，解码后长度必须为 **32 bytes (256 bits)**
- 拒绝行为：
  - 长度不为 32 bytes → fatal 拒绝启动
  - 非有效 Base64 → fatal 拒绝启动
  - 出现 `change_me`、`test_key`、空字符串等弱值 → fatal 拒绝启动
- `WEBDB_KEK_V1` 不能与 `WEBDB_KEK_V2` 相同（交叉验证）

### 5.4 启动验证

- **启动时验证**：解析并验证所有 `WEBDB_KEK_V*` 环境变量
- 最小要求：至少存在 `WEBDB_KEK_V1`
- 验证失败 → 进程退出（`log.Fatal`），不进入就绪状态
- 已有 Envelope 中引用的 `kek_version` 如果没有对应的环境变量 → 记录警告但不阻止启动（历史数据可能在 KEK 轮换后仍存在）

### 5.5 KEK 版本行为

| 场景 | 行为 |
|---|---|
| `kek_version` 未知且无对应 env var | 返回 `unknown_kek_version`，不尝试解密 |
| `kek_version` 存在且对应 env var 有有效值 | 正常解密 |
| 写入新 envelope | 始终使用 `WEBDB_ACTIVE_KEK_VERSION` 显式指定的版本 |

**当前写入版本**：由 `WEBDB_ACTIVE_KEK_VERSION` 环境变量显式指定，**禁止自动选择最大版本号**。

**KEK 版本不得回退到旧版本进行写入**。要切换写入版本，必须修改 `WEBDB_ACTIVE_KEK_VERSION` 并重启服务。

### 5.6 KEK 安全约束

KEK 不得出现在：
- 代码仓库
- Docker 镜像
- 元数据库
- 日志
- 错误消息
- 审计事件
- API/浏览器响应
- 普通测试夹具

单元测试只能使用显式标记的合成临时密钥，通过 `crypto/rand` 生成或使用显式标注的独立固定值。**禁止从本文档复制任何密钥字面量：**

```go
// 仅测试用：合成 256-bit 密钥（32 字节）
// 实际测试应使用以下方式之一：
//   1. var testKEK = make([]byte, 32); _, _ = rand.Read(testKEK)
//   2. var testKEK = []byte("独立且明确的固定非密钥标记值--32B")
// 禁止使用任何可被误认为真实密钥的字面量
```

---

## 6. 凭证生命周期

### 6.1 状态模型

```text
                     ┌──────────┐
          create ──→ │  active  │
                     └────┬─────┘
                          │
              ┌───────────┼───────────┐
              │           │           │
          rotate      retire      (保留历史)
              │           │           │
              ▼           ▼           ▼
         new version   retired    旧版本可解密但
         (active)      (不可用于   **不得用于普通执行**
                       新连接，   （仅审计追溯）
                       被引用时
                       不得退役)
```

### 6.2 操作定义

#### 6.2.1 创建新 Secret（CreateCredential）

```text
1. 验证调用者有 workspace 的 owner/admin 角色
2. 生成 secret_ref = new UUID
3. version = 1
4. 验证 payload schema
5. 生成 DEK + nonces
6. 使用 ActiveKEK 加密
7. INSERT INTO credential_envelopes (workspace_id, secret_ref, version, ...)
8. 写入审计事件: action="credential.create", outcome="succeeded"
   → 审计写入失败：INSERT 已持久化，返回 audit_failed；客户端可重试审计写入。
      创建操作不提供幂等键——每次 CreateCredential 生成新的 secret_ref 和 envelope，
      重试必须基于上一步返回的 secret_ref 判断是否已创建，不得重复调用 CreateCredential
```

#### 6.2.2 读取指定版本（ResolveCredential）

```text
1. 验证调用者有 workspace 成员资格
2. SELECT FROM credential_envelopes WHERE (workspace_id, secret_ref, secret_version) = (...)
3. 行不存在 → audit: credential.lookup.fail, 返回 credential_not_found
4. 行存在且 `retired_at IS NOT NULL` → 普通执行路径返回 credential_retired
   （审计追溯路径允许继续解密，通过独立查询接口，不经过此执行流程）
5. 验证 envelope_suite 已知；将 `envelope_suite` 映射为 `suite_tag`（枚举 → 4-byte 大端序）
6. data_aad ← binaryEncode(version_tag=1, workspace_id, secret_ref, secret_version, suite_tag, kek_version)
6. wrap_aad ← binaryEncode(version_tag=1, workspace_id, secret_ref, secret_version, suite_tag, kek_version)
7. KEK ← KEKProvider.GetKEK(row.kek_version)
8. KEK 未知 → audit: credential.decrypt.fail (error_code=unknown_kek_version), 返回 unknown_kek_version
9. DEK ← GCM-Open(KEK, wrap_nonce, wrapped_dek, wrap_aad)
10. plaintext ← GCM-Open(DEK, data_nonce, ciphertext, data_aad)
11. 解密失败（步骤 9 或 10）→ audit: credential.decrypt.fail (error_code=decryption_failed), 返回 decryption_failed
12. 验证 payload schema
13. 返回 CredentialPayload
```

> **约束**：步骤 10-13 任一失败，Adapter 调用次数必须为 0。步骤 3（行不存在）或步骤 4（已退役）时不执行任何加密操作。

#### 6.2.3 轮换（RotateCredential）

```text
1. 验证调用者角色
2. 验证 payload schema
3. BEGIN TRANSACTION
4.   SELECT secret_ref FROM credential_envelopes
        WHERE workspace_id = $1 AND secret_ref = $2 FOR UPDATE
     → 行不存在或不属于该 workspace：ROLLBACK，返回 credential_not_found，
       写入审计: credential.rotate.fail (error_code=credential_not_found)，流程终止
5.   新 version = MAX(version) + 1（在持锁事务内原子计算，步骤 4 已确认行存在）
6.   生成新 DEK + nonces
7.   使用 ActiveKEK 加密新 payload
8.   INSERT INTO credential_envelopes (新版本, retired_at=NULL)
9.   UPDATE connections SET secret_version = 新 version
        WHERE workspace_id = $1 AND secret_ref = $2
10. COMMIT
11. 写入审计事件: credential.rotate.success
    → 审计写入失败：事务已 COMMIT，返回 audit_failed；客户端可重试审计写入。
       轮换冲突检测：调用方须在请求中提供 expected_version（当前连接引用的 secret_version），
       服务端在步骤 4 的 SELECT FOR UPDATE 之后对比行中的 MAX(version)；
       若 expected_version 已落后（即已有其他轮换成功），当前请求的 payload 未保存，
       ROLLBACK 并返回 version_conflict（outcome=failed），metadata 含
       expected_version 和 actual_version。调用方须重新获取最新版本后重试。
       版本匹配时才继续计算新版本并插入。expected_version + 唯一约束双重保护。
```

**轮换失败行为**：
- 事务中间失败 → 回滚，旧版本不受影响，连接引用保持不变
- 写入审计事件: `credential.rotate.fail`

**并发轮换**：
- 使用 `SELECT ... FOR UPDATE` 锁定 credential_envelopes 行
- 固定锁顺序：始终先锁 `credential_envelopes` 再锁 `connections`（防止死锁）
- 事务隔离级别保证只有一个成功；失败的事务回滚，客户端可重试
- `expected_version` + 唯一约束 `(workspace_id, secret_ref, version)` 双重保护

#### 6.2.4 退役（RetireCredential）

```text
1. 验证调用者角色
2. BEGIN TRANSACTION
3.   SELECT version FROM credential_envelopes
        WHERE workspace_id = $1 AND secret_ref = $2 AND version = $3
        AND retired_at IS NULL FOR UPDATE
     → 行不存在或已退役：ROLLBACK，返回 credential_not_found
4.   -- 在同一持锁事务内锁定并检查现有引用
     SELECT id FROM connections
     WHERE workspace_id = $1 AND secret_ref = $2 AND secret_version = $3
     FOR SHARE
     → 计数 > 0：ROLLBACK，返回 credential_in_use
5.   UPDATE credential_envelopes SET retired_at = now()
     WHERE workspace_id = $1 AND secret_ref = $2 AND version = $3
6. COMMIT
7. 写入审计事件: credential.retire
```

- 步骤 4 的 `FOR SHARE` 会阻塞并发连接版本更新，直至退役事务结束。
- 连接创建/更新引用时必须以 `FOR KEY SHARE` 锁定且验证目标 envelope 为 active；若先等待退役事务，唤醒后重新检查 `retired_at` 并拒绝已退役版本。该写入侧约束同时关闭“引用检查为 0 后新建引用”的窗口。
- 退役操作与引用检查在同一事务中原子完成。

#### 6.2.5 连接引用更新

```text
1. BEGIN TRANSACTION
2.   SELECT version FROM credential_envelopes
        WHERE workspace_id = $ws AND secret_ref = $ref AND version = $new_version
        AND retired_at IS NULL FOR KEY SHARE
     → 行不存在或已退役：ROLLBACK，返回 credential_not_found 或 credential_retired
3.   UPDATE connections SET secret_version = $new_version
     WHERE workspace_id = $ws AND id = $conn_id AND secret_ref = $ref
4. COMMIT
```

- 步骤 2 对目标 envelope 加 `FOR KEY SHARE` 锁，与退役的 `FOR UPDATE` 互斥；锁等待结束后重新检查 active 条件，防止并发退役在引用检查和 UPDATE 之间完成。
- 引用必须属于同一 workspace。
- 引用的 version 必须在 credential_envelopes 中存在且 `retired_at IS NULL`（步骤 2 在持锁事务内验证）。

### 6.3 状态转换表

| 操作 | 前置状态 | 后置状态 | 事务？ | 审计事件 |
|---|---|---|---|---|
| Create | — | version=1, active | 否（单 INSERT）† | `credential.create` |
| Read | active 或 retired | 不变 | 否 | 无（除非失败） |
| Rotate | 旧版本 active | 新版本 active，旧版本不变 | 是 | `credential.rotate` |
| Retire | active | retired | 是（SELECT FOR UPDATE + 引用检查 + UPDATE 在同一事务） | `credential.retire` |

> † Create 为单条 INSERT 无显式事务边界；若后续审计写入失败，envelope 行已持久化且审计失败不影响创建结果。Retire 使用显式事务以保证引用检查和退役更新的原子性。

### 6.4 错误场景

| 场景 | 行为 |
|---|---|
| 解密失败（GCM 认证失败） | `decryption_failed`，不返回任何明文 |
| 引用不存在版本 | `credential_not_found`（防枚举） |
| 跨 workspace 访问 | `credential_not_found`（防枚举） |
| 引用已退役版本（普通执行） | **拒绝**（Owner D8），返回 `credential_retired` |
| 引用已退役版本（审计追溯） | 允许（通过直接查询 envelope 表，不经过正常凭证解析路径） |
| 被引用的版本退役 | **拒绝**（Owner D8），返回 `credential_in_use` |
| 并发轮换冲突 | 一个成功，其余回滚 |

---

## 7. 与 P0-04/Adapter 的调用顺序

### 7.1 完整执行顺序（含凭证解析）

```text
请求进入
  │
  ├─ 阶段 A：身份与授权（前置）
  │   └─ 验证 Principal、成员资格、Connection、ConnectionPolicy
  │   ❌ 失败 → 不创建 Execution、不写 Audit（workspace 不存在时）
  │
  ├─ 阶段 B：创建 Execution（pending）
  │   └─ INSERT execution（status=pending）
  │
  ├─ 阶段 C：SQL lexical + AST 策略判断
  │   ├─ MySQL ECM lexer → Omni AST
  │   └─ 单语句 + 只读分类
  │   ❌ 拒绝 → Execution=failed, Audit(denied), Adapter.Query=0
  │
  ├─ ★ 阶段 C'：凭证解析（新增）
  │   ├─ 验证 secret_ref + secret_version 存在
  │   ├─ 验证 envelope_suite 已知
  │   ├─ 获取 KEK
  │   ├─ 解密 DEK
  │   ├─ 解密 payload
  │   ├─ 验证 payload schema
  │   └─ 构造 ConnectConfig{User, Password}
  │   ❌ 任何失败：
  │       → 不调用 Adapter（Adapter.Query=0）
  │       → 不接触目标数据库
  │       → Execution=failed, error_code=对应错误码
  │       → Audit（outcome=failed，metadata 含 error_code）
  │
  ├─ 阶段 D-0：执行状态更新
  │   └─ UPDATE execution SET status='running'
  │   ❌ 失败 → 返回 internal_error/audit_failed，Adapter.Query=0
  │
  └─ 阶段 D：执行（Adapter）
      └─ Adapter.Query(ConnectConfig{User, Password, ...})
```

### 7.2 关键安全断言

| # | 断言 | 验证方式 |
|---|---|---|
| 1 | SQL 策略拒绝时不允许解密凭证 | 测试：SQL 策略拒绝 → credential 解密代码路径不可达 |
| 2 | 凭证失败时 Adapter.Query 调用次数 = 0 | 测试：mock Adapter 计数 |
| 3 | 凭证失败时目标数据库访问次数 = 0 | 测试：mock pool handle |
| 4 | 授权失败时凭证解密代码路径不可达 | 代码审查 + 测试 |
| 5 | ConnectConfig.Password 不进入日志/错误/审计 | 敏感信息扫描测试 |

### 7.3 与其他阶段的兼容性

本方案新增的阶段 C'（凭证解析）完全在阶段 C（SQL 策略）通过后、阶段 D（Adapter）之前执行，**不改变 P0-04 的 fail-closed 边界**：

- 阶段 A-C 的拒绝行为不变
- MySQL lexer mode 从服务端 `PipelineConfig` 注入，执行请求不得提供或覆盖该模式
- Adapter `ConfigRevision` 使用持久化 `connections.updated_at` 的微秒值；连接配置更新与凭证轮换通过 SQL 保证时间戳至少递增 1 微秒
- ADR-014 `VerifiedSortPlan` 尚未迁移时，不传入伪造的排序键；仅允许受策略 `max_rows` 约束且不产生 continuation token 的单页请求
- Adapter 的 `rate_limited`/`connection_busy`/超时/取消逻辑不变
- 执行状态转换不变
- 审计 outcome 枚举不变

---

## 8. 审计事件契约

### 8.1 事件矩阵

| # | 事件 | `action` | `outcome` | `actor_type` | `connection_id` | `execution_id` | metadata |
|---|---|---|---|---|---|---|---|
| E1 | 连接创建 | `connection.create` | `succeeded` | `user` | 新连接 ID | NULL | `engine`, `environment` |
| E2 | 连接更新 | `connection.update` | `succeeded` | `user` | 连接 ID | NULL | `environment` |
| E3 | 凭证创建 | `credential.create` | `succeeded` | `user` | NULL | NULL | `secret_ref`(UUID), `secret_version`, `envelope_suite`, `kek_version` |
| E4 | 凭证轮换成功 | `credential.rotate` | `succeeded` | `user` | NULL | NULL | `secret_ref`, `old_version`, `new_version`, `envelope_suite`, `kek_version` |
| E5 | 凭证轮换失败 | `credential.rotate` | `failed` | `user` | NULL | NULL | `secret_ref`, `error_code`, `expected_version`（如适用）, `actual_version`（如适用） |
| E6 | 凭证退役 | `credential.retire` | `succeeded` | `user` | NULL | NULL | `secret_ref`, `version` |
| E7 | 连接测试成功 | `connection.test` | `succeeded` | `user` | 连接 ID | NULL | `engine`, `environment`, `duration_ms` |
| E8 | 连接测试失败 | `connection.test` | `failed` | `user` | 连接 ID | NULL | `engine`, `environment`, `error_code` |
| E9 | SQL 策略拒绝 | `sql.execute` | `denied` | `user` | 连接 ID | execution ID | `statement_hash`, `reason_code`, `engine` |
| E10 | 执行成功 | `sql.execute` | `succeeded` | `user` | 连接 ID | execution ID | `statement_hash`, `row_count`, `duration_ms`, `engine`, `environment` |
| E11 | 执行失败 | `sql.execute` | `failed` | `user` | 连接 ID | execution ID | `statement_hash`, `error_code`, `engine`, `environment` |
| E12 | 执行超时 | `sql.execute` | `failed` | `user` | 连接 ID | execution ID | `statement_hash`, `error_code`(`query_timeout`), `engine` |
| E13 | 执行取消 | `sql.execute` | `cancelled` | `user` | 连接 ID | execution ID | `statement_hash`, `error_code`(`query_cancelled`), `engine` |
| E14 | 凭证查找失败 | `credential.lookup` | `failed` | `system` | 连接 ID | NULL | `secret_ref`, `error_code` |
| E15 | 凭证解密失败 | `credential.decrypt` | `failed` | `system` | 连接 ID | NULL | `secret_ref`, `secret_version`, `error_code` |
| E16 | 未知 KEK 版本 | `credential.decrypt` | `failed` | `system` | 连接 ID | NULL | `secret_ref`, `secret_version`, `kek_version`, `error_code` |

> **E17（审计写入失败）**：不持久化为 audit_events 行（失败的审计系统不可写入）。作为独立安全告警通过应用日志/监控通道发出，携带 `trace_id`、`error_code` 和发生时间。此告警通道独立于 `audit_events` 表，不受审计表触发器或写入失败的影响。

### 8.2 Metadata 允许列表（合并字段表，16 字段）

下表列出 P0-05 方案涉及的完整 metadata 字段。其中 6 个为 P0-05 新增凭证字段，7 个为 P0-04 现有字段（已在此表中），另有 3 个 P0-04 现有字段（`summary`、`rows_affected`、`cached`）仅由 P0-04 维护且不适用于凭证事件，未重复列出。WEB-23 实现时需将 sanitizer 扩展至完整 16 字段（6 新增 + 10 现有）并添加兼容性测试：

| 键 | 类型 | 约束 | 适用事件 | 来源 |
|---|---|---|---|---|
| `secret_ref` | string | UUID 格式 (36 chars) | E3-E6, E14-E16 | P0-05 新增 |
| `secret_version` | integer | > 0 | E3, E16 | P0-05 新增 |
| `old_version` | integer | > 0 | E4 | P0-05 新增 |
| `new_version` | integer | > old_version | E4 | P0-05 新增 |
| `envelope_suite` | string | 精确枚举值 | E3, E4 | P0-05 新增 |
| `kek_version` | integer | > 0 | E3, E4, E16 | P0-05 新增 |
| `statement_hash` | string | 64 char hex | E9-E13 | P0-04 现有 |
| `row_count` | integer | 0..2^31-1 | E10 | P0-04 现有 |
| `duration_ms` | integer | ≥ 0 | E7, E10 | P0-04 现有 |
| `error_code` | string | 稳定错误码枚举 | E5, E8, E11-E17 | P0-04 现有 |
| `reason_code` | string | 稳定拒绝原因码 | E9 | P0-04 现有 |
| `engine` | string | `postgresql` \| `mysql` | E1, E3, E4, E7-E13 | P0-04 现有 |
| `environment` | string | `development` \| `staging` \| `production` | E1, E2, E7, E8, E10, E11 | P0-04 现有 |

> P0-04 另有 `summary`、`rows_affected`、`cached` 三个字段由 P0-04 维护且不适用于凭证事件类型。完整合并后的 sanitizer 允许列表为 16 字段。

### 8.3 禁止字段

以下字段**绝对禁止**进入审计 metadata：

- SQL 正文（包括规范化 SQL）
- 密码、密码 hash
- KEK 明文、DEK 明文
- nonce、wrapped DEK
- 完整连接串（host、port 可出现在表列，不得进入 metadata）
- 目标数据库查询结果
- 原始数据库错误消息（`pq:` 前缀、`MySQL Error` 等）
- 未经允许列表校验的用户输入

### 8.4 稳定错误码（新增）

在 P0-04 基础上新增以下凭证相关错误码：

| 错误码 | 含义 | 阶段 |
|---|---|---|
| `decryption_failed` | 信封解密失败（GCM 认证失败或 payload 损坏） | C' |
| `unknown_envelope_suite` | 未知的 envelope_suite 值 | C' |
| `unknown_kek_version` | 未知的 KEK 版本 | C' |
| `invalid_payload` | Payload schema 验证失败 | C' |
| `payload_too_large` | Payload 超过大小上限 | C' |
| `credential_not_found` | 凭证 envelope 不存在 | C' |
| `credential_retired` | 凭证版本已退役（普通执行路径拒绝） | C' |
| `version_conflict` | 轮换 expected_version 不匹配（其他轮换已先完成） | C' |
| `credential_in_use` | 凭证版本被连接引用，无法退役 | C' |

---

## 9. 审计失败策略

### 9.1 分阶段矩阵

元数据库内部操作（Execution 状态更新、AuditEvent append）在同一数据库事务中原子提交。对目标数据库的外部查询遵循 P0-04 已批准的失败矩阵（`rate_limited`/`connection_busy`/`query_timeout`/`query_cancelled`/`database_error`），不在本方案中改变。

| 阶段 | 审计写入失败时 | 是否调用 Adapter | 是否返回结果 | Execution 最终状态 | 客户端错误码 | 安全告警 | 是否允许重试 | 事务边界 |
|---|---|---|---|---|---|---|---|---|
| **阶段 A**（workspace 已确认存在） | 不访问目标数据库 | 否 | 否 | N/A（无 Execution） | `audit_failed` | 是（E17 通道） | 否 | AuditEvent 单条 INSERT |
| **阶段 A**（workspace 无法解析） | 仅写应用安全日志 | 否 | 否 | N/A | 原始业务错误码 | 否 | 否 | N/A |
| **阶段 B**（Execution 创建） | 不访问目标数据库 | 否 | 否 | 取决于 INSERT 是否成功 | `internal_error` | 是（E17 通道） | 否 | Execution INSERT + AuditEvent INSERT 原子 |
| **阶段 C**（SQL 策略拒绝） | 返回 `audit_failed` | 否 | 否 | `failed` | `audit_failed` | 是（E17 通道） | 否 | Execution UPDATE + AuditEvent INSERT 原子 |
| **阶段 C'**（凭证解析失败） | 返回 `audit_failed` | 否 | 否 | `failed` | `audit_failed` | 是（E17 通道） | 否 | Execution UPDATE + AuditEvent INSERT 原子 |
| **阶段 D-0**（running 更新失败） | 返回 `audit_failed` | 否 | 否 | `pending`（未更新） | `audit_failed` | 是（E17 通道） | 否 | Execution UPDATE 单条 |
| **阶段 D 完成后** | 返回 `audit_failed` | 已调用 | **否**（不返回查询结果） | `completed`（Execution UPDATE 在独立事务中已提交） | `audit_failed` | 是（E17 通道） | 客户端决定 | Execution UPDATE 独立事务；AuditEvent INSERT 后置独立事务 |
| **拒绝事件**（rate_limited/connection_busy） | 返回 `audit_failed` | 已调用（失败）* | 否 | `failed` | `audit_failed` | 是（E17 通道） | 否 | Execution UPDATE + AuditEvent INSERT 原子 |
| **取消/超时事件** | 返回 `audit_failed` | 已调用（已取消/超时） | 否 | `cancelled`/`failed` | `audit_failed` | 是（E17 通道） | 客户端决定 | Execution UPDATE + AuditEvent INSERT 原子 |
| **告警系统自身失败** | 写入 stderr/syslog | N/A | N/A | N/A | N/A | 基础设施级告警 | N/A | N/A |

> \* `rate_limited` 和 `connection_busy` 发生在 Adapter 内部的 `TryAcquire`/pool acquire 阶段（P0-04 §4.3.1: `Adapter.Query=1`），但目标数据库连接未建立（DB acquire/SQL Query 均为 0 或超时）。标记为"已调用（失败）"以准确反映 Adapter 被调用但未成功执行查询的事实。

### 9.2 关键原则

1. **禁止静默降级**：审计写入失败必须返回 `audit_failed`，不返回 `succeeded`
2. **禁止自动重试**：审计写入失败不触发服务端自动重试（避免重复执行副作用）
3. **阶段 D 后特殊处理**：查询已真实执行，不返回结果给客户端，但 execution 已记录为 `completed`。客户端可通过 ExecutionID 查询状态（需 P0-05 认证后提供接口），但不承诺审计失败后结果一定可恢复
4. **$SECURITY_ALERT**：凭证解密失败、未知 KEK 版本和审计写入失败必须产生安全告警

### 9.3 与 P0-04 契约的一致性

本方案的审计失败策略与 P0-04 §8.6 完全一致：
- 执行前 fail-closed（不访问目标数据库）
- 执行后 fail-closed（揭示已执行事实）
- 不自动重试
- `audit_failed` 作为稳定错误码

**无 ESCALATE 条件**。

---

## 10. 保留和删除边界

### 10.1 现有策略（保持不变）

- 查询结果默认保留 7 天（ADR-010）
- 执行元信息按其独立策略保留（ADR-010）
- 审计事件不随结果过期删除（ADR-013）
- 审计表仅 append/query，数据库拒绝 UPDATE/DELETE/TRUNCATE（ADR-013）

### 10.2 审计保留期（Owner 决策 D12）

- **至少保留 90 天**（从 `occurred_at` 起算）。
- **P0 阶段不实施自动删除**。精确清理机制、归档策略和权限边界另建独立任务。
- 审计事件保持 append-only（ADR-013 约束，数据库拒绝 UPDATE/DELETE/TRUNCATE）。

### 10.3 凭证 envelope 保留

- 退役的 credential_envelopes 不自动删除
- 只要 KEK 版本仍在环境变量中，历史密文就可以解密
- 删除 KEK 版本（从环境变量移除）等同于永久失去对应 envelope 的解密能力

---

## 11. 依赖与许可证

### 11.1 新增依赖评估

| 依赖 | 必要性 | 许可证 | 新增？ |
|---|---|---|---|
| Go stdlib `crypto/aes` | AES 加密 | Go BSD-style | 否 |
| Go stdlib `crypto/cipher` | GCM 模式 | Go BSD-style | 否 |
| Go stdlib `crypto/rand` | CSPRNG | Go BSD-style | 否 |
| Go stdlib `crypto/sha256` | AAD/指纹 | Go BSD-style | 否 |
| Go stdlib `encoding/base64` | KEK 编码 | Go BSD-style | 否 |
| Go stdlib `encoding/json` | Payload/AAD 编解码 | Go BSD-style | 否 |

**结论：零新增第三方依赖。** 全部使用 Go 标准库。

### 11.2 外部网络需求

- 无需下载任何模块或外部资源
- 无需访问外部 API 或服务
- 所有密码学操作在 WebDB 进程内完成

---

## 12. Owner 决策记录

以下决策已于 2026-08-01 由 Owner `fujiabao89` 逐项批准。

| ID | 决策项 | Owner 决定 | 批准 |
|---|---|---|---|
| D1 | Credential Payload schema/version | **修改后批准**：v1 `{v, user, password}`；保持 user/password 原值，禁止 TrimSpace/Unicode 规范化 | ✅ |
| D2 | AEAD 算法 | **批准**：AES-256-GCM，256-bit DEK，96-bit crypto/rand nonce，128-bit GCM tag | ✅ |
| D3 | DEK wrapping 算法 | **修改后批准**：AES-256-GCM wrapping；使用独立 wrap AAD，禁止 nil AAD；每 KEK 加密次数上限 2^24 | ✅ |
| D4 | AAD 字段与编码 | **修改后批准**：保留 5 个绑定字段 + version_tag（共 6 字段）；版本化确定性二进制编码（48 bytes），不采用 Canonical JSON | ✅ |
| D5 | KEK 格式 | **批准**：RFC 4648 padded Base64 严格解码，结果必须为 32 bytes | ✅ |
| D6 | KEK 版本行为 | **修改后批准**：`WEBDB_KEK_V{N}` + 显式 `WEBDB_ACTIVE_KEK_VERSION`；禁止自动选择最大版本 | ✅ |
| D7 | 轮换并发语义 | **修改后批准**：稳定行 SELECT FOR UPDATE + expected_version + 唯一约束 + 固定锁顺序（先 envelope 后 connections） | ✅ |
| D8 | 退役规则 | **拒绝原方案**：retired 版本不得用于普通执行；被引用版本不得直接退役（须先解除所有引用） | ❌→✅ |
| D9 | 审计事件枚举 | **修改后批准**：E1-E16 持久化审计；E17 作为独立安全告警通道，不写回失败的审计系统 | ✅ |
| D10 | Metadata 允许列表 | **修改后批准**：事件级精确 metadata schema；统一为 16 字段（6 P0-05 新增 + 10 P0-04 现有） | ✅ |
| D11 | 审计失败策略 | **修改后批准**：元数据库变更与 audit 在同一事务中原子提交；外部查询执行后遵循 P0-04 失败矩阵 | ✅ |
| D12 | 审计保留期 | **修改后批准**：至少保留 90 天；P0 不实施自动删除；精确清理另建独立任务 | ✅ |
| D13 | Schema migration | **条件批准**：按 D8/D12 决定时无需 migration；否则重新升级 Owner | ✅ |
| D14 | 新增第三方依赖 | **批准**：不新增第三方依赖，不自行实现密码算法 | ✅ |
| D15 | 残余风险 | **逐项批准**：R1/R2/R4 条件接受；R3 加入 KEK 调用上限后接受；R5/R7 沿用既有决策；R6 创建生产角色拆分任务 | ✅ |

---

## 13. 残余风险

| # | 风险 | 严重程度 | 缓解措施 | Owner 决定 (D15) |
|---|---|---|---|---|
| R1 | Go 无法保证可靠内存清零 | 低 | 使用后置 nil、`runtime.KeepAlive`、最小化明文生命周期；文档如实记录 | ✅ 条件接受 |
| R2 | `crypto/rand` 在极端熵耗尽时返回错误 | 极低 | fail-closed，不降级 | ✅ 条件接受 |
| R3 | 96-bit GCM nonce 在 >2^32 次加密后可能重用 | 极低 | 加入每 KEK 2^24 次加密上限（§4.5），远低于 nonce 重用阈值 | ✅ 接受（加入 KEK 调用上限后） |
| R4 | KEK 环境变量在进程内存中可被调试器读取 | 中 | 需要主机 root 权限；部署环境保护 | ✅ 条件接受 |
| R5 | `SELECT func()` 副作用（含 SECURITY DEFINER） | 中 | P0-04 已有缓解 | ✅ 沿用 P0-04 既有决策 |
| R6 | 审计事件不防内部 DBA 篡改（触发器可被 SUPERUSER 绕过） | 中 | **创建独立后续任务：生产环境数据库角色拆分** | ✅ 创建生产角色拆分任务 |
| R7 | 服务重启后 Continuation Token 全部失效 | 低 | ADR-015 已接受此限制 | ✅ 沿用 ADR-015 |

---

## 14. 测试矩阵（WEB-22/WEB-23 可执行）

### 14.1 Payload 测试

| ID | 场景 | 预期 |
|---|---|---|
| PAY-01 | 正常 payload round-trip | 加密后解密一致 |
| PAY-02 | v=1 正常 payload | 通过 |
| PAY-03 | v=2 未知版本 | `invalid_payload` |
| PAY-04 | 缺失 user 字段 | `invalid_payload` |
| PAY-05 | 缺失 password 字段 | `invalid_payload` |
| PAY-06 | user 超长 (>255 bytes) | `invalid_payload` |
| PAY-07 | password 超长 (>1024 bytes) | `invalid_payload` |
| PAY-08 | 总 payload 超限 (>4096 bytes) | `payload_too_large` |
| PAY-09 | 非法 UTF-8 | `invalid_payload` |
| PAY-10 | 未知字段 | `invalid_payload` |
| PAY-11 | user 含控制字符 | `invalid_payload` |
| PAY-12 | password 含控制字符 | 允许 |

### 14.2 加密测试

| ID | 场景 | 预期 |
|---|---|---|
| ENC-01 | 正常加密+解密 round-trip | 明文一致 |
| ENC-02 | 错误 KEK 解密 | `decryption_failed` |
| ENC-03 | 错误 AAD（workspace_id 不匹配） | `decryption_failed` |
| ENC-04 | 错误 AAD（secret_ref 不匹配） | `decryption_failed` |
| ENC-05 | 错误 AAD（secret_version 不匹配） | `decryption_failed` |
| ENC-06 | ciphertext 被篡改（翻转 1 bit） | `decryption_failed` |
| ENC-07 | data_nonce 被篡改 | `decryption_failed` |
| ENC-08 | wrapped_dek 被篡改 | `decryption_failed` |
| ENC-09 | wrap_nonce 被篡改 | `decryption_failed` |
| ENC-10 | 跨 workspace 替换密文 | `decryption_failed` |
| ENC-11 | 跨 secret_ref 替换密文 | `decryption_failed` |
| ENC-12 | 跨版本替换密文 | `decryption_failed` |
| ENC-13 | 未知 envelope_suite | `unknown_envelope_suite` |
| ENC-14 | 解密结果不是有效 JSON | `invalid_payload` |
| ENC-15 | 解密结果 schema 不匹配 | `invalid_payload` |
| ENC-16 | 零长度 ciphertext（DB 约束） | 数据库拒绝 |
| ENC-17 | nonce 唯一性：100 次加密 | 所有 data_nonce 唯一 |
| ENC-18 | nonce 唯一性：100 次 DEK wrap | 所有 wrap_nonce 唯一 |

### 14.3 KEK Provider 测试

| ID | 场景 | 预期 |
|---|---|---|
| KEK-01 | 有效 KEK V1 | 正常返回 |
| KEK-02 | 无任何 KEK 环境变量 | 启动 fatal |
| KEK-03 | KEK 非有效 Base64 | 启动 fatal |
| KEK-04 | KEK 长度 != 32 bytes | 启动 fatal |
| KEK-05 | KEK = `change_me` | 启动 fatal |
| KEK-06 | 多版本 KEK (V1, V2)，`WEBDB_ACTIVE_KEK_VERSION=1` | ActiveKEK 返回 V1（由 ACTIVE 显式指定，不自动选择 V2） |
| KEK-07 | GetKEK(未知版本) | `unknown_kek_version` |
| KEK-08 | V1 和 V2 相同值 | 启动 fatal |

### 14.4 生命周期测试

| ID | 场景 | 预期 |
|---|---|---|
| LIFE-01 | 创建凭证 → 读取 | round-trip 成功 |
| LIFE-02 | 轮换：旧版本 → 新版本 | 新版本可解密，旧版本仍可解密 |
| LIFE-03 | 连接引用切换到新版本 | 连接测试成功 |
| LIFE-04 | 退役旧版本 | retired_at 已设置，仍可解密 |
| LIFE-05 | 引用不存在版本 | `credential_not_found` |
| LIFE-06 | 跨 workspace 创建 | DB FK 拒绝 |
| LIFE-07 | 并发轮换 | 一个成功，其余回滚 |
| LIFE-08 | 事务中间失败（DB 错误） | 旧版本不变，rollback |
| LIFE-09 | 连接尝试引用其他 workspace 的 secret_ref | DB FK 拒绝 |
| LIFE-10 | 退役后创建/更新连接引用该版本 | fail-closed 拒绝 |
| LIFE-11 | 退役引用检查与连接版本更新并发 | `FOR SHARE` 阻塞更新直至退役事务结束 |

### 14.5 集成断言测试

| ID | 场景 | 预期 |
|---|---|---|
| INT-01 | SQL 策略拒绝时不解密凭证 | credential 代码路径不可达 |
| INT-02 | 凭证失败时 Adapter.Query = 0 | mock Adapter 调用计数 |
| INT-03 | 凭证失败时 DB 访问次数 = 0 | mock pool handle |
| INT-04 | API 响应不含 password | 敏感信息扫描 |
| INT-05 | 日志不含 password/KEK | 日志扫描 |
| INT-06 | 错误不含 password/KEK/DEK | 错误响应扫描 |
| INT-07 | 审计 metadata 不含 password | canary 检测 |
| INT-08 | audit UPDATE 被拒绝 | DB 触发器测试 |
| INT-09 | audit DELETE 被拒绝 | DB 触发器测试 |
| INT-10 | audit TRUNCATE 被拒绝 | DB 触发器测试 |
| INT-11 | Adapter 并发准入拒绝 | 稳定错误码 `rate_limited`（HTTP 429 映射输入） |

### 14.6 审计故障注入测试

| ID | 场景 | 预期 |
|---|---|---|
| AUDIT-01 | 阶段 A 审计写入失败 | `audit_failed`，不访问目标库 |
| AUDIT-02 | 阶段 D 完成后审计写入失败 | `audit_failed`，不返回结果 |
| AUDIT-03 | 阶段 C 拒绝后审计写入失败 | `audit_failed` |
| AUDIT-04 | credential.decrypt 失败审计 | `audit_failed`，Adapter.Query=0 |

### 14.7 质量门禁

| ID | 验证项 | 预期 |
|---|---|---|
| QA-01 | `go test ./...` | PASS |
| QA-02 | `go vet ./...` | PASS |
| QA-03 | `go test -race ./...` | PASS |
| QA-04 | `go test -fuzz=. -fuzztime=30s` | 无 panic、无越界 |
| QA-05 | `GOOS=windows go build ./...` | PASS |
| QA-06 | `GOOS=linux go build ./...` | PASS |
| QA-07 | 许可证检查 | 无新增非兼容许可证 |
| QA-08 | 敏感信息扫描 | 无 KEK/password 泄漏 |

---

## 15. 回滚与前向修复

### 15.1 回滚 WEB-22

- **代码回滚**：`git revert <WEB-22 merge commit>`
- **数据回滚**：credential_envelopes 表仅追加，回滚代码不会破坏历史数据
- **KEK 回滚**：保留旧版 KEK 环境变量，新 envelope 仍可解密

### 15.2 回滚 WEB-23

- **代码回滚**：`git revert <WEB-23 merge commit>`
- **审计数据**：审计事件不可变（append-only），回滚代码不影响已写入的审计数据

### 15.3 KEK 紧急轮换

1. 在所有实例上添加新 `WEBDB_KEK_V{N+1}` 环境变量（但不修改 `WEBDB_ACTIVE_KEK_VERSION`）
2. 滚动重启所有实例（此时仍使用旧版 KEK 写入，新版 KEK 已加载可用于解密）
3. 确认所有实例正常运行后，更新 `WEBDB_ACTIVE_KEK_VERSION={N+1}` 并再次滚动重启
4. 新 envelope 使用新 KEK 加密；旧版 KEK 保留用于历史解密
5. 回滚：将 `WEBDB_ACTIVE_KEK_VERSION` 改回旧值并重启；旧 envelope 不受影响

> **P0 不实施 DEK 重包装**：data_aad 包含 `kek_version`（D4），若用新 KEK 重新包装 DEK 后更新 envelope 的 `kek_version` 字段，data_aad 会因 `kek_version` 变化而不同，导致已有 ciphertext 的 GCM 认证失败。P0 阶段 KEK 轮换通过**追加新 envelope 版本**（生成新 DEK、重新加密 payload）实现，而非重包装已有 DEK。旧 KEK 版本必须保留，否则对应 envelope 永久无法解密。DEK 重包装能力留给后续独立任务。

---

## 附录 A：与现有 Schema 的兼容性

**结论：现有 Schema 完全满足本方案需求，无需 migration。**

- `credential_envelopes` 已包含所有必需字段
- `connections` 的 `secret_ref` + `secret_version` + 三列复合外键已足够
- `audit_events` 的 metadata JSONB 足够存储所有允许字段
- 无需新增表、列或约束

## 附录 B：修订记录

| 日期 | 修订内容 |
|---|---|
| 2026-08-01 | 初版 — 提交 Owner Gate |
| 2026-08-01 | WEB-23 实施：审计 metadata 强类型化（§1.4/§1.5 更新），E1-E17 接入完成，安全告警通道落地 |

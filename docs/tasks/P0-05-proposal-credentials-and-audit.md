# P0-05：凭证信封加密、KEK 生命周期与审计失败策略

> 状态：提议中（Owner Gate）｜日期：2026-08-01｜作者：Claude Code
>
> 本方案冻结 P0-05 的凭证加密、KEK 注入、信封版本、轮换和审计失败语义。
> **在 Owner 明确批准前，不得开始 WEB-22/WEB-23 的生产实现。**

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

### 1.4 现有审计脱敏（P0-04，已实现）

`sanitizeAuditMetadata()` 当前允许列表：`summary`、`rows_affected`、`row_count`、`cached`、`statement_hash`、`duration_ms`、`error_code`、`reason_code`、`engine`、`environment`。

启发式检测 `looksLikeSQL()` 和 `looksLikeCredential()` 仍存在（计划在 P0-04 后续收紧中移除）。

### 1.5 当前不可用的能力

- 凭证加解密未实现：`Connection.SecretRef`/`SecretVersion` 存在，但无法解密为 `ConnectConfig.User`/`ConnectConfig.Password`
- KEK Provider 未实现
- 凭证生命周期（创建/轮换/退役）未实现
- 审计事件接入未完成（仅 execute 相关事件已定义）

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
- 不擅自固定审计事件保留期

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
- 规范化：编码前去除 `user` 和 `password` 的首尾空白（`strings.TrimSpace`）
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
| AAD 编码 | **Canonical JSON**（见 §4.4） | 键排序、无空格 |
| KDF | **无** | KEK 已是高熵 256-bit 密钥，不需要密码型 KDF |

### 4.3 随机源失败行为

- `crypto/rand.Read` 在 Linux 上使用 `getrandom(2)`，在 Windows 上使用 `ProcessPrng`
- 系统熵池耗尽（极罕见）时 `crypto/rand.Read` 返回 error
- **行为**：返回 `internal_error`，不执行加密，不写入数据库，不调用 Adapter
- 不允许回退到弱随机源（如 `math/rand`）

### 4.4 AAD 规范编码

AAD 使用 Canonical JSON 编码，绑定以下字段：

```json
{"envelope_suite":"AES256GCM-v1","kek_version":1,"secret_ref":"550e8400-e29b-41d4-a716-446655440000","secret_version":1,"workspace_id":"660e8400-e29b-41d4-a716-446655440001"}
```

> UUID 统一使用小写、带连字符的 36 字符标准格式（RFC 4122 §3）。AAD 序列化时必须直接使用 `uuid.UUID.String()` 输出，不得自行去除连字符或转换大小写。加密与解密端必须使用完全相同的 AAD 规范化规则。

**AAD 字段**：

| 字段 | 来源 | 目的 |
|---|---|---|
| `workspace_id` | `credential_envelopes.workspace_id` | 防止跨工作区密文替换 |
| `secret_ref` | `credential_envelopes.secret_ref` | 防止跨 secret 密文替换 |
| `secret_version` | `credential_envelopes.version` | 防止跨版本密文替换 |
| `envelope_suite` | `credential_envelopes.envelope_suite` | 防止 suite 混淆/downgrade |
| `kek_version` | `credential_envelopes.kek_version` | 防止 KEK 版本混淆 |

**Canonical JSON 规则**：
- 所有键按字典序排列
- 无空格、无换行
- 字符串使用 UTF-8，Unicode 转义使用小写 `\uXXXX`
- 整数不含前导零

**不允许的 AAD 行为**：
- AAD 为空：拒绝
- AAD 字段缺失：拒绝
- AAD 含未知字段：拒绝
- AAD 与 envelope 行的实际值不匹配：解密失败（GCM 认证失败）

### 4.5 加密流程

```text
1. DEK ← crypto/rand(32 bytes)
2. data_nonce ← crypto/rand(12 bytes)
3. AAD ← canonicalJSON(workspace_id, secret_ref, secret_version, envelope_suite, kek_version)
4. plaintext ← json.Marshal(payload)  // 验证 schema 后
5. ciphertext ← AES-256-GCM-Seal(DEK, data_nonce, plaintext, AAD)
   // ciphertext 包含 GCM 认证标签（附加在末尾 16 bytes）

6. wrap_nonce ← crypto/rand(12 bytes)
7. wrapped_dek ← AES-256-GCM-Seal(KEK, wrap_nonce, DEK, nil)
   // DEK wrapping 不需要 AAD（DEK 关系已在 envelope 行中绑定）

8. 持久化: (ciphertext, data_nonce, wrapped_dek, wrap_nonce, envelope_suite, kek_version)
```

### 4.6 解密流程

```text
1. 验证 envelope_suite 为已知版本 → 否则返回 unknown_envelope_suite
2. 验证 kek_version 有对应 KEK → 否则返回 unknown_kek_version（见 §5）
3. AAD ← canonicalJSON(workspace_id, secret_ref, secret_version, envelope_suite, kek_version)
4. DEK ← AES-256-GCM-Open(KEK, wrap_nonce, wrapped_dek, nil)
   → 失败：返回 decryption_failed（不区分 DEK 或 payload 失败，防 oracles）
5. plaintext ← AES-256-GCM-Open(DEK, data_nonce, ciphertext, AAD)
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
- 环境变量为空：启动时 **fatal**，拒绝启动
- 有效的 KEK 版本至少有 `V1`

### 5.3 KEK 编码与长度

- KEK 编码：**Base64 标准编码**（RFC 4648 §4，无换行）
- 解码后长度：**32 bytes (256 bits)**
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
| 写入新 envelope | 始终使用 `ActiveKEK()` 返回的当前版本 |

**当前写入版本**：环境变量中最大版本号对应的密钥（如同时有 V1 和 V2，则使用 V2 作为写入版本）。

**KEK 版本不得回退到旧版本进行写入**。

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
         new version   retired    旧版本仍可解密
         (active)      (不可用于   （直到 KEK 版本淘汰）
                       新连接)
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
4. 验证 envelope_suite 已知
5. AAD ← canonicalJSON(row)
6. KEK ← KEKProvider.GetKEK(row.kek_version)
7. KEK 未知 → audit: credential.decrypt.fail (error_code=unknown_kek_version), 返回 unknown_kek_version
8. DEK ← GCM-Open(KEK, wrap_nonce, wrapped_dek, nil)
9. plaintext ← GCM-Open(DEK, data_nonce, ciphertext, AAD)
10. 解密失败 → audit: credential.decrypt.fail (error_code=decryption_failed), 返回 decryption_failed
11. 验证 payload schema
12. 返回 CredentialPayload
```

> **约束**：步骤 8-11 任一失败，Adapter 调用次数必须为 0。步骤 2 的行不存在时，不执行步骤 5-11 的任何加密操作。

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
       轮换幂等性：调用方须在请求中提供 expected_version（当前连接引用的 secret_version），
       服务端在步骤 3 的 SELECT FOR UPDATE 之后对比行中的 MAX(version)；
       若 expected_version 已落后（即已有其他轮换成功），返回现有最新版本信息
      （credential.rotate.already_rotated, outcome=succeeded），不执行插入。
       版本匹配时才继续计算新版本并插入。这样避免依赖唯一约束作为幂等机制，
       也防止重试产生误报的版本冲突错误
```

**轮换失败行为**：
- 事务中间失败 → 回滚，旧版本不受影响，连接引用保持不变
- 写入审计事件: `credential.rotate.fail`

**并发轮换**：
- 两个并发轮换：事务隔离级别保证只有一个成功
- 使用 `SELECT ... FOR UPDATE` 锁定 credential_envelopes 行
- 失败的事务回滚，客户端可重试

#### 6.2.4 退役（RetireCredential）

```text
1. 验证调用者角色
2. UPDATE credential_envelopes SET retired_at = now()
   WHERE workspace_id = $1 AND secret_ref = $2 AND version = $3 AND retired_at IS NULL
3. 不影响 connections 引用
4. 写入审计事件: credential.retire
```

- 退役记录不覆盖或删除历史密文
- 退役后仍可解密（用于审计追溯）
- 退役版本不应被新连接引用（应用层约束）

#### 6.2.5 连接引用更新

```text
UPDATE connections SET secret_version = $new_version
WHERE workspace_id = $ws AND id = $conn_id AND secret_ref = $ref
```

- 引用必须属于同一 workspace
- 引用的 version 必须在 credential_envelopes 中存在且 `retired_at IS NULL`（应用层约束）
- 应用层必须验证连接引用不能指向其他 workspace 的 secret_ref

### 6.3 状态转换表

| 操作 | 前置状态 | 后置状态 | 事务？ | 审计事件 |
|---|---|---|---|---|
| Create | — | version=1, active | 否（单 INSERT） | `credential.create` |
| Read | active 或 retired | 不变 | 否 | 无（除非失败） |
| Rotate | 旧版本 active | 新版本 active，旧版本不变 | 是 | `credential.rotate` |
| Retire | active | retired | 否 | `credential.retire` |

### 6.4 错误场景

| 场景 | 行为 |
|---|---|
| 解密失败（GCM 认证失败） | `decryption_failed`，不返回任何明文 |
| 引用不存在版本 | `credential_not_found`（防枚举） |
| 跨 workspace 访问 | `credential_not_found`（防枚举） |
| 引用已退役版本（读取） | 允许（用于审计追溯） |
| 引用已退役版本（新连接） | 应用层拒绝 |
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
| E5 | 凭证轮换失败 | `credential.rotate` | `failed` | `user` | NULL | NULL | `secret_ref`, `error_code` |
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
| E17 | 审计写入失败告警 | `audit.write` | `failed` | `system` | NULL | NULL | `error_code`（写入应用安全日志） |

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
| `credential_retired` | 凭证版本已退役（用于新连接引用时） | C' |

---

## 9. 审计失败策略

### 9.1 分阶段矩阵

| 阶段 | 审计写入失败时 | 是否调用 Adapter | 是否返回结果 | Execution 最终状态 | 客户端错误码 | 安全告警 | 是否允许重试 |
|---|---|---|---|---|---|---|---|
| **阶段 A**（workspace 已确认存在） | 不访问目标数据库 | 否 | 否 | N/A（无 Execution） | `audit_failed` | 是 | 否（自动重试禁止） |
| **阶段 A**（workspace 无法解析） | 仅写应用安全日志 | 否 | 否 | N/A | 原始业务错误码 | 否 | 否 |
| **阶段 B**（Execution 创建） | 不访问目标数据库 | 否 | 否 | 取决于 INSERT 是否成功 | `internal_error` | 是 | 否 |
| **阶段 C**（SQL 策略拒绝） | 返回 `audit_failed` | 否 | 否 | `failed` | `audit_failed` | 是 | 否 |
| **阶段 C'**（凭证解析失败） | 返回 `audit_failed` | 否 | 否 | `failed` | `audit_failed` | 是 | 否 |
| **阶段 D-0**（running 更新失败） | 返回 `audit_failed` | 否 | 否 | `pending`（未更新） | `audit_failed` | 是 | 否 |
| **阶段 D 完成后** | 返回 `audit_failed` | 已调用 | **否**（不返回查询结果） | `completed` | `audit_failed` | 是 | 客户端决定 |
| **拒绝事件**（rate_limited/connection_busy） | 返回 `audit_failed` | 已调用（失败） | 否 | `failed` | `audit_failed` | 是 | 否 |
| **取消/超时事件** | 返回 `audit_failed` | 已调用（已取消/超时） | 否 | `cancelled`/`failed` | `audit_failed` | 是 | 客户端决定 |
| **告警系统自身失败** | 写入 stderr/syslog | N/A | N/A | N/A | N/A | 基础设施级告警 | N/A |

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

### 10.2 审计保留期（Owner 决策）

审计事件的保留期尚未决定。选项：

| 选项 | 保留期 | 影响 |
|---|---|---|
| A | 与结果相同（7 天） | 审计追溯能力有限 |
| B | 较长（90 天/1 年） | 需要清理任务和存储预算 |
| C | 不自动删除 | 需要归档策略和存储增长管理 |

**建议**：Owner 在 P0 阶段先决定一个初始审计保留期。P0 阶段默认建议 **90 天**。

审计保留期决定后，WEB-23 需根据选择实现对应的清理或归档机制。具体清理方式（受控删除、归档后删除、仅归档）和权限边界取决于 Owner 决策，不得在本任务中固定；在 Owner 决策完成前，审计事件保持 append-only（ADR-013 约束，拒绝 UPDATE/DELETE/TRUNCATE），清理机制暂不定义实现细节。

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

## 12. Owner 决策包

以下决策必须由 Owner 明确批准。**不得代替 Owner 填写批准结果。**

| ID | 决策项 | 可选方案 | 推荐方案 | 安全依据 | 新依赖 | Owner 决定 | 批准人 | 日期 |
|---|---|---|---|---|---|---|---|---|
| D1 | Credential Payload schema/version | v1: `{v, user, password}` | **v1** | 最小化原则，仅满足 Adapter 当前需求 | 无 | | | |
| D2 | AEAD 算法 | A: AES-256-GCM; B: AES-256-GCM + AES-KWP | **A: AES-256-GCM** | stdlib 零依赖，充分安全 | 无 | | | |
| D3 | DEK wrapping 算法 | A: AES-256-GCM; B: AES-KWP (RFC 5649) | **A: AES-256-GCM** | 简单一致，避免自行实现 RFC 3394 | 无 | | | |
| D4 | AAD 字段与编码 | 当前 5 字段 + Canonical JSON | **5 字段+Canonical JSON** | 绑定所有关键上下文防替换 | 无 | | | |
| D5 | KEK 格式 | Base64 编码，256-bit | **Base64, 256-bit** | 标准编码，足够强度 | 无 | | | |
| D6 | KEK 版本行为 | 环境变量 `WEBDB_KEK_V{N}`，启动验证 | **环境变量，启动验证** | ADR-006 决策，最小化运维复杂度 | 无 | | | |
| D7 | 轮换并发语义 | SELECT FOR UPDATE，事务隔离 | **SELECT FOR UPDATE** | PostgreSQL 保证原子性 | 无 | | | |
| D8 | 退役后解密 | A: 允许; B: 禁止 | **A: 允许** | 审计追溯需要 | 无 | | | |
| D9 | 审计事件枚举（E1-E17） | 17 类事件 | **17 类事件** | 覆盖所有关键路径 | 无 | | | |
| D10 | Metadata 允许列表 | 14 个字段 | **14 字段** | 精确格式校验，无自由文本 | 无 | | | |
| D11 | 审计失败策略矩阵 | §9.1 的分阶段矩阵 | **分阶段 fail-closed** | 与 P0-04 契约一致 | 无 | | | |
| D12 | 审计保留期 | 7天/90天/1年/不删除 | **90天（P0 建议）** | 平衡追溯与存储 | 无 | | | |
| D13 | 是否需要 Schema migration | 否（现有 Schema 足够） | **否** | credential_envelopes 已包含所有字段 | 无 | | | |
| D14 | 是否需要新增第三方依赖 | 否（全部 stdlib） | **否** | Go stdlib 满足全部需求 | 无 | | | |
| D15 | 残余风险接受 | §13 残余风险清单 | **接受或拒绝各风险** | 逐项评估 | 无 | | | |

---

## 13. 残余风险

| # | 风险 | 严重程度 | 缓解措施 | 是否可接受 |
|---|---|---|---|---|
| R1 | Go 无法保证可靠内存清零 | 低 | 使用后置 nil、`runtime.KeepAlive`、最小化明文生命周期；文档如实记录 | Owner 决定 |
| R2 | `crypto/rand` 在极端熵耗尽时返回错误 | 极低 | fail-closed，不降级 | Owner 决定 |
| R3 | 96-bit GCM nonce 在 >2^32 次加密后可能重用 | 极低 | 单个 secret 的加密次数远低于此阈值；每次加密生成新 DEK | Owner 决定 |
| R4 | KEK 环境变量在进程内存中可被调试器读取 | 中 | 需要主机 root 权限；部署环境保护 | Owner 决定 |
| R5 | `SELECT func()` 副作用（含 SECURITY DEFINER） | 中 | P0-04 已有缓解：测试账号不授予危险函数权限；可增加只读事务保护 | 已由 P0-04 跟踪 |
| R6 | 审计事件不防内部 DBA 篡改（触发器可被 SUPERUSER 绕过） | 中 | P0 Compose 环境未拆分角色；生产部署应拆分 | Owner 决定 |
| R7 | 服务重启后 Continuation Token 全部失效 | 低 | ADR-015 已接受此限制 | 已接受 |

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
| KEK-06 | 多版本 KEK (V1, V2) | ActiveKEK 返回最高版本 |
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

1. 添加新 `WEBDB_KEK_V{N+1}` 环境变量
2. 重启 WebDB 服务
3. 新凭证自动使用新 KEK 加密
4. 旧版 KEK 保留用于历史解密
5. 可选：使用 API 批量重新加密旧 envelope（用新 KEK 重新包装 DEK）

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

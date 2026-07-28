# WebDB 项目代码审查报告

> 本文是 2026-07-26 审查环境的时间点归档；后续修复可能使其中结论失效。
>
> 审查分支：`feat/WEB-13-P0-04-sql-safety-policy`
> 审查日期：2026-07-26
> 审查依据：`AGENTS.md`、`CLAUDE.md`、`webdb-design-draft.md`、ADR-001/002/005/006/007/008/009/010/013、P0-02/03/04 任务卡
> 审查范围：初始基线全量审查（98 个文件，约 11,353 行）

---

## 1. 审查范围

| 项目 | 详情 |
| ---- | ---- |
| 当前分支 | `feat/WEB-13-P0-04-sql-safety-policy` |
| 对比目标分支 | 当时的浅克隆审查环境未获取基础分支 ref，因此无法生成分支差异；这不表示仓库不存在 `main` |
| 最新提交 | `d335a74 docs(p0-04): address second-round CodeRabbit review` |
| 审查文件 | 全部 98 个文件、约 11,353 行（视为初始基线审查） |
| 未提交修改 | 无（`working tree clean`） |
| 重点审查模块 | `apps/api/internal/adapter/`、`apps/api/internal/metadata/`、`apps/api/internal/migrate/`、`apps/api/cmd/server/`、`deploy/compose/`、`.github/workflows/`、`apps/web/`、`packages/contracts/` |
| 未能审查内容 | `go.sum` 完整传递依赖哈希、`apps/web/package-lock.json`（1,724 行）、`WebDB项目AI协作开发体系方案.md`（非约束文件） |

> 说明：当时的审查环境仅获取当前分支的单个初始提交，无法做分支间差异审查。本次按"初始基线全量审查"对待，重点对照 `AGENTS.md`、ADRs 与 `webdb-design-draft.md` 的 P0 安全边界。

## 2. 已读取的项目约束

| 约束文件 | 适用范围 | 核心规则 |
| ---- | ---- | ---- |
| `AGENTS.md` | 全工作区 | P0 仅交付 PG/MySQL 连接、Schema 拉取、只读 SQL、服务端分页、审计；浏览器不得直连 DB；SQL 必须服务端最小权限执行；单语句+方言 AST 可靠判定+超时/行数/取消；不可靠判定默认拒绝；不得用字符串前缀匹配作安全边界；审计追加写并记录操作者/workspace/连接/trace；KEK/密钥不得进 API/响应/日志/审计正文/错误信息 |
| `CLAUDE.md` | 全工作区 | 优先级：AGENTS.md > CLAUDE.md > 设计/ADR；PR 必须使用 `.github/PULL_REQUEST_TEMPLATE.md` 五个章节；Claude 不得批准/合并自己的 PR；连续失败两次或达到三轮必须升级人工 |
| `webdb-design-draft.md` | 全工作区 | v1 仅 PG 14+/MySQL 8.0+；P0 不含登录/DML/DDL/SSH；每连接池 `max_open=10/max_idle=2/30min/5s`；并发上限用户 2/工作区 10/连接 5，超限 429；无界队列禁止；`audit_events` DB 层拒绝 update/delete/truncate；任意浏览器请求不能获得目标库明文密码 |
| `ADR-001` | 适配层/前端 | 仅 API 可经 TLS+最小权限账号连目标库；浏览器只调 WebDB API |
| `ADR-002` | 适配层 | P0/v1 仅支持 PG 14+/MySQL 8.0+ |
| `ADR-005` | 适配层/策略 | 生产连接默认拒绝 DDL/DML；任一层（环境策略/目标库权限/WebDB 授权）拒绝即拒绝 |
| `ADR-006` | 部署/凭证 | KEK 由部署环境注入；不得出现在配置/日志/错误/审计正文/测试夹具/镜像/仓库 |
| `ADR-007` | SQL 策略层 | PG/MySQL 分别使用对应 AST 解析器；只允许可可靠判定的单条 `SELECT`/`EXPLAIN`；不得用字符串前缀或共同 SQL 子集作安全判定 |
| `ADR-008` | 适配层池管理 | 每目标连接 `max_open=10/max_idle=2/30min/5s`；并发 2/10/5；超限 429；不设无界队列 |
| `ADR-009` | 网络拓扑 | P0/P1 仅支持私网/VPN 直连；不得以临时 shell 隧道绕过 |
| `ADR-010` | 结果保留 | 持久化结果默认 7 天；非空 `result_ref` 必须有过期时间；审计不随结果删除 |
| `ADR-013` | 元数据 Schema | `pressly/goose` SQL-only 顺序迁移；API 启动不自动迁移；所有工作区子资源 `workspace_id` 非空；复合外键分量非空；`audit_events` DB 层拒绝 update/delete/truncate；`credential_envelopes` 仅存密文+nonce+wrapped DEK+suite+kek_version；`executions` `CHECK (result_ref IS NULL OR result_expires_at IS NOT NULL)`；缺省策略默认 `allow_read=true/allow_write=false/allow_export=false` |
| `P0-03 任务卡` | 适配层 | 已完成（PR #12）；`AdmissionController` 已返回 `ErrRateLimited`，HTTP 429 映射由 P0-04 负责；超时/取消/错误/客户端断开必须归还连接 |
| `P0-04 任务卡` | SQL 策略层 | 当前分支任务；按 PG/MySQL 方言 AST 分类，默认仅放行可可靠判定的单条 `SELECT`/`EXPLAIN`；多语句/危险语句/解析错误/未知类别均拒绝；不实现 DML/DDL 审批；不实现字符串前缀判断 |
| `P0-04 提案` | SQL 策略层 | 详细契约：`Dialect` 服务端派生、`StatementKind`+`ASTFeatures`、`StableReasonCode`、`AuthenticatedPrincipal`、Token 安全属性（绑定 principal/conn/generation/hash/策略版本/过期/原子 compare-and-consume）；客户端不能提高策略上限；NextPage 重新授权 |

## 3. 审查结论

| 项目 | 结论 |
| ---- | ---- |
| 是否建议合并 | **存在 P0，禁止合并** |
| P0 数量 | 7 |
| P1 数量 | 16 |
| P2 数量 | 14 |
| P3 数量 | 6 |
| 最主要的风险 | (1) KEK 默认弱值 `change_me` 进入 compose 与镜像 env；(2) `main.go` 完全无优雅关停，违反"客户端断开时连接必须归还"硬约束；(3) 审计脱敏在 JSON 解析失败时回退为原始 raw，可被构造畸形 JSON 绕过白名单；(4) 当前分支任务名为 P0-04 SQL 安全策略，但代码中**完全未实现 ADR-007 要求的方言 AST 解析**，且 `keyset.go` 用字符串去分号作为安全前置，违反"不得用字符串前缀匹配作为安全边界" |
| 是否需要补充测试 | 是。必须补充：跨工作区 execution/audit 关联拒绝、跨工作区 actor 拒绝、DESC+NullsLast keyset、`creating` panic 路径、`-race` 检测、审计脱敏失败路径、优雅关停 |

## 4. 详细问题

### P0 问题（阻断合并）

#### `[P0] KEK 默认弱值进入 compose 与镜像环境变量`

* **文件位置**：`deploy/compose/docker-compose.yml:122`
* **违反约束**：`AGENTS.md` "不提交密钥…或生产日志"；`ADR-006` "KEK 不得出现在配置、日志、错误、审计正文或测试夹具"；`webdb-design-draft.md` "任意浏览器请求均无法获得或恢复目标数据库明文密码"
* **问题描述**：`WEBDB_KEK: ${WEBDB_KEK:-change_me}` 为 KEK 提供默认弱值。KEK 是加密所有目标数据库凭证的主密钥，一旦使用 `change_me`，所有 `credential_envelopes` 的密文都可被离线解出。`apps/api/env.example:13` 同样写 `WEBDB_KEK=change_me_in_production`。
* **触发场景**：运维忘记设置 `WEBDB_KEK` env，compose 仍可启动，API 用 `change_me` 作为 KEK 解密所有凭证信封；攻击者获取元数据库快照后即可用已知 KEK 解出全部目标库密码。
* **实际影响**：所有目标数据库凭证可被解密，等同于全量凭证泄露；违反 P0 安全边界"任意浏览器请求均无法获得或恢复目标数据库明文密码"。
* **修复建议**：改为 `${WEBDB_KEK:?WEBDB_KEK 必须设置}` 强制失败；`env.example` 中 KEK 行删除默认值或改为 `<必须由部署环境注入>`，并在 README 中说明本地 dev 用法。
* **验证方式**：`WEBDB_KEK= docker compose config` 应报错；`WEBDB_KEK=valid_value docker compose config` 应成功；grep 仓库确认无 `change_me` 作为 KEK 默认值。

#### `[P0] main.go 无优雅关停，违反"客户端断开时连接必须归还"硬约束`

* **文件位置**：`apps/api/cmd/server/main.go:47-66`
* **违反约束**：`AGENTS.md` 审查清单"超时、取消、错误和客户端断开时，数据库连接是否始终归还"；`P0-03 任务卡` 验收要点
* **问题描述**：`runServe()` 仅调用 `server.ListenAndServe()`，**未注册 `signal.Notify(SIGTERM, SIGINT)`，未实现 `server.Shutdown(ctx)`**。容器收到 SIGTERM 时，进行中的 HTTP 请求与底层 DB 连接被强行中断；`log.Fatalf` 路径跳过所有 defer。同时缺少 `ReadHeaderTimeout`（slowloris 缓解）与 `MaxHeaderBytes`。
* **触发场景**：K8s/compose 滚动更新、`docker stop`、Ctrl+C 时，正在执行的目标库查询被立即杀死，pgxpool/`sql.DB` 的 `Close()` 不会被调用，连接池中正在执行的查询对应的连接可能不被正确归还。
* **实际影响**：违反 AGENTS.md 审查清单硬约束；目标库连接可能泄漏；审计事件可能丢失（写入中途被杀）；进行中 Execution 状态可能停在 `running` 永不更新。
* **修复建议**：
  ```go
  srv := &http.Server{...ReadHeaderTimeout: 5*time.Second, MaxHeaderBytes: 1<<20}
  errCh := make(chan error, 1)
  go func() { errCh <- srv.ListenAndServe() }()
  sigCh := make(chan os.Signal, 1)
  signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
  select {
    case <-sigCh:
      ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
      defer cancel()
      return srv.Shutdown(ctx)
    case err := <-errCh:
      return err
  }
  ```
* **验证方式**：启动 API，发起一个 30s 查询，中途 `kill -TERM <pid>`，应等待查询完成或 30s 超时后退出，且 API 日志记录 Shutdown 完成；新增 `TestServer_GracefulShutdown` 单测。

#### `[P0] 审计脱敏在 JSON 解析失败时回退为原始 raw，可被构造畸形 JSON 绕过白名单`

* **文件位置**：`apps/api/internal/metadata/postgres_repo.go:547-551`
* **违反约束**：`AGENTS.md` "密钥、敏感参数和结果是否未出现在 API、浏览器响应、日志、审计正文和错误信息中"；`ADR-013` "审计 metadata 必须是脱敏对象"
* **问题描述**：
  ```go
  var m map[string]any
  if err := json.Unmarshal(raw, &m); err != nil || m == nil {
      return raw  // ← 解析失败时直接返回原始 raw
  }
  ```
  当 metadata 是畸形 JSON（如 `"{\"password\":\"x\""` 缺尾花括号）时，`json.Unmarshal` 失败，函数返回原始 `raw`。虽然 DB 层 `CHECK (jsonb_typeof(metadata) = 'object')` 会拒绝非对象，但**对 JSON 对象内部的字段白名单过滤被完全绕过**——只要 JSON 在语法上合法但结构异常（如 `{"password":"x"}` 这种正常对象），过滤逻辑会跳过非白名单 key（`password` 不在 allowed 里会被丢弃），但若调用方传入 `{"summary":"SELECT * FROM users; DROP TABLE x"}`，`looksLikeSQL` 启发式可被变形（大小写、注释、Unicode 同形字符）绕过。**根本问题是 fail-open：解析失败时返回原值，依赖 DB 兜底**。
* **触发场景**：上层服务传入未严格构造的 metadata（如序列化错误、并发写入竞争、第三方库返回非预期类型），`json.Unmarshal` 失败 → 原始 JSON 进入 SQL 参数；若 DB CHECK 未覆盖此路径（例如未来修改 CHECK），原始敏感数据进入审计。
* **实际影响**：审计正文可能包含 SQL/凭证/明文结果，违反 ADR-013 与 AGENTS.md 安全边界；当前依赖 DB CHECK 兜底，但应用层 fail-open 是反模式。
* **修复建议**：解析失败时返回 `json.RawMessage("{}")` 或返回错误让调用方决策：
  ```go
  if err := json.Unmarshal(raw, &m); err != nil {
      return json.RawMessage("{}")  // fail-closed
  }
  if m == nil { return json.RawMessage("{}") }
  ```
* **验证方式**：新增单测 `TestSanitizeAuditMetadata_MalformedJSON` 传入 `{"bad json"`，断言返回 `{}`；新增 `TestSanitizeAuditMetadata_NestedObject` 传入 `{"summary":{"nested":"x"}}`，断言 nested 被丢弃或整体拒绝。

#### `[P0] demo-mysql init 脚本将 MySQL root 密码放入 argv`

* **文件位置**：`deploy/compose/init/demo-mysql/01-init.sh:32`
* **违反约束**：`AGENTS.md` "密钥、敏感参数和结果是否未出现在 API、浏览器响应、日志、审计正文和错误信息中"（argv 暴露属于同类风险）；脚本自身在 `verify-readonly.sh:42-43` 显式用 `MYSQL_PWD` env 避免 argv 暴露，自相矛盾
* **问题描述**：`mysql -u root -p"${MYSQL_ROOT_PASSWORD}" "${MYSQL_DB}" <<EOSQL` —— 密码经 `-p` 参数进入进程命令行，可通过 `ps aux`、`/proc/<pid>/cmdline`、容器 inspect 输出可见。
* **触发场景**：MySQL 容器 init 阶段，任何能读取 `/proc` 或运行 `docker exec demo-mysql ps aux` 的主体（包括容器内被攻陷的进程）可读取 root 密码。
* **实际影响**：MySQL root 密码泄露；root 可绕过只读账号限制，直接修改数据或禁用审计触发器（`DROP TRIGGER audit_events_immutable`）。
* **修复建议**：改用 `MYSQL_PWD` env 或 `--defaults-extra-file`：
  ```bash
  MYSQL_PWD="${MYSQL_ROOT_PASSWORD}" mysql -u root "${MYSQL_DB}" <<EOSQL
  ```
* **验证方式**：`docker exec demo-mysql cat /proc/1/cmdline` 不应包含密码；`ps aux | grep mysql` 不应出现 `-p` 参数。

#### `[P0] CI 中数据库密码进入 argv 与 CI 日志`

* **文件位置**：`.github/workflows/ci.yml:140, 153, 160, 166`
* **违反约束**：`AGENTS.md` "密钥、敏感参数…是否未出现在…日志"
* **问题描述**：`PGPASSWORD=change_me psql -h localhost -U webdb ...` 与 `mysql -u root -pchange_me ...` 多处。`PGPASSWORD` 作为命令前缀 env 不会进 argv（OK），但 `mysql -pchange_me` 会进 argv 与日志。`change_me` 虽是测试密码，但模式会污染生产脚本。
* **触发场景**：CI 日志公开（误配 `actions/permissions: contents: read` 仍可见于 PR 评论）；模式被复制到生产部署脚本。
* **实际影响**：测试 DB 密码泄露；模式扩散到生产；违反 AGENTS.md 安全清单。
* **修复建议**：所有 MySQL 调用改用 `MYSQL_PWD` env；PG 调用保持 `PGPASSWORD` env（已正确）；为所有密码变量加 `:?` 强制。
* **验证方式**：CI 日志 grep `change_me`、`-pchange_me`、`-p change_me` 应为空。

#### `[P0] 当前 P0-04 分支未实现 ADR-007 要求的方言 AST 解析，且 keyset.go 用字符串去分号作为安全前置`

* **文件位置**：`apps/api/internal/adapter/keyset.go:76`、`apps/api/internal/adapter/manager.go:419-473`（`Query` 方法无 SQL 安全校验）
* **违反约束**：`ADR-007` "PostgreSQL 与 MySQL 分别使用对应方言解析器；不限制为共同 SQL 子集；任何无法可靠解析或判定的语句默认拒绝"；`AGENTS.md` "不得用字符串前缀匹配作为安全边界"；`P0-04 任务卡` "按 PostgreSQL/MySQL 方言 AST 分类"
* **问题描述**：当前分支名为 `feat/P0-04-sql-safety-policy`，但：
  1. `keyset.go:76` `sql = strings.TrimRight(strings.TrimSpace(sql), ";")` —— 仅以字符串方式去除尾部分号；
  2. `keyset.go:86` `fmt.Sprintf("SELECT * FROM (\n%s\n) AS webdb_page", sql)` —— 不校验 `sql` 是否为 SELECT，也不拦截 `WITH ... DELETE ... RETURNING`（PG 数据修改 CTE）、`SELECT ... INTO OUTFILE`（MySQL）等危险形式；
  3. `manager.go` 的 `Query` 方法无任何 AST 解析、语句分类或策略裁决；
  4. 仓库中不存在 `apps/api/internal/sqlpolicy/` 或 `apps/api/internal/execution/` 目录（按 P0-04 提案 §3.4 应有 Spike 报告与正式工程代码）。

  虽然包注释声明 "P0-03 不实现 SQL 安全裁决（P0-04 职责）"，但**当前分支就是 P0-04**，应在分支内交付或在 PR 描述中明确标注"仅完成提案，未实现解析器"。
* **触发场景**：调用方在 P0-04 分支上直接调用 `PoolHandle.Query`，传入 `WITH d AS (DELETE FROM users RETURNING *) SELECT * FROM d`（PG 数据修改 CTE）或 `SELECT * FROM users; DROP TABLE audit_events --`，keyset.go 仅去尾分号，第二条语句可能被驱动执行（取决于多语句配置）。
* **实际影响**：当前分支未达成 P0-04 任务卡的验收标准（"多语句、危险语句、解析错误和未知类别均被拒绝"）；任何接入此分支的调用方都暴露在 SQL 注入/越权写入风险下。
* **修复建议**：
  - 短期：在 `Query` 入口加显式 `panic("P0-04 SQL policy not yet implemented; do not call in production")` 或返回 `ErrUnsupported`，防止误用；
  - 在 `keyset.go:75` `buildWrappedSQL` 入口加注释明确"调用方必须先经 P0-04 策略层裁决"；
  - 中期：按 P0-04 提案 §3.4 完成 Spike 与正式解析器实现，或在本 PR 描述中明确标注"仅交付提案文档，解析器实现待后续 PR"。
* **验证方式**：grep 仓库确认无生产代码路径可在无 AST 裁决下调用 `PoolHandle.Query`；若实现解析器，需有 PG/MySQL 双方言各 20+ 危险 SQL 的回归测试。

#### `[P0] 审计 metadata 脱敏启发式可被简单变形绕过，且会误删合法摘要`

* **文件位置**：`apps/api/internal/metadata/postgres_repo.go:689-708`（`looksLikeSQL`/`looksLikeCredential`）
* **违反约束**：`AGENTS.md` "不得用字符串前缀匹配作为安全边界"；`ADR-013` "审计 metadata 必须是脱敏对象"
* **问题描述**：
  - `looksLikeSQL` 用 `\b(SELECT|INSERT|UPDATE|...)\b` 正则匹配 SQL 关键词。`"user selected export"`、`"create connection summary"` 等合法摘要会被误判为敏感并替换为 `[redacted: sensitive content]`，**破坏审计完整性**；
  - `looksLikeCredential` 用 `strings.Contains(strings.ToLower(s), "password")` 等子串匹配。`p@ssword`、`passw0rd`、`PWD`、`AKIA...`、`-----BEGIN PRIVATE KEY-----` 等常见凭证格式不被识别，**可被变形绕过**；
  - 这违反了 AGENTS.md "不得用字符串前缀匹配作为安全边界" 的精神——虽然这里是审计脱敏而非 SQL 安全判定，但同样的启发式失败模式适用于凭证泄露检测。
* **触发场景**：
  - 误报：合法审计摘要 `"summary": "user created new connection"` 被替换为 `[redacted]`，审计丢失关键信息；
  - 漏报：攻击者构造 `"summary": "PWD=abc123"` 或 `"summary": "AKIAIOSFODNN7EXAMPLE"`，不被识别为凭证，进入审计正文。
* **实际影响**：审计完整性受损（误报）或凭证泄露（漏报）；违反 AGENTS.md 安全清单。
* **修复建议**：
  - 移除 `looksLikeSQL` 关键词黑名单；改为"白名单字段 + 长度上限（500 字符）+ DB 端 size 限制"作为唯一写入决策；
  - 凭证检测改用更严格的正则（如 `(?i)^(password|pwd|secret|token|api[_-]?key)\s*[:=]`）或完全依赖白名单（任何非白名单字段都不写入）；
  - 启发式仅作为辅助告警（log warn）而非写入决策。
* **验证方式**：新增单测覆盖：`"user created connection"` 不被误删、`"PWD=abc"` 被拒绝、`"AKIA..."` 被拒绝。

### P1 问题（必须修复）

#### `[P1] 元数据库默认 sslmode=disable`

* **文件位置**：`apps/api/cmd/server/main.go:76`
* **违反约束**：`ADR-001` "API 必须承担鉴权、策略、审计、取消和连接池责任"；通用工程安全
* **问题描述**：`metaDSN()` 默认 `sslmode=disable`，意味着元数据库连接默认无加密。元数据库存储 `credential_envelopes`、`audit_events`、`executions` 等敏感数据，未加密连接可被中间人窃听。
* **触发场景**：运维忘记设置 `META_DB_SSLMODE`，API 与元数据库之间的连接明文传输，凭证密文、审计事件可被网络抓包获取。
* **实际影响**：元数据库内容泄露风险；违反 ADR-001 隐含的传输安全要求。
* **修复建议**：默认改为 `prefer` 或 `require`；本地 dev 通过 `AllowInsecureLocalDemo` 显式 opt-in。
* **验证方式**：不设置 `META_DB_SSLMODE` 启动，应尝试 TLS；新增 `TestMetaDSN_DefaultSSLMODE` 单测。

#### `[P1] nginx.conf 缺失安全响应头、TLS、body 限制与速率限制`

* **文件位置**：`apps/web/nginx.conf:1-28`（全文）
* **违反约束**：`ADR-001` API 承担安全责任；通用 Web 安全最佳实践
* **问题描述**：
  1. `listen 80` 仅 HTTP，无 TLS；
  2. 缺失 `X-Content-Type-Options nosniff`、`X-Frame-Options DENY`、`Strict-Transport-Security`、`Content-Security-Policy`、`Referrer-Policy`、`server_tokens off`；
  3. `/api/` 反代缺 `proxy_set_header X-Forwarded-Proto`、`proxy_connect_timeout`、`client_max_body_size`、`limit_req`；
  4. `index.html` 未显式 `no-cache`，SPA 升级可能命中旧入口。
* **触发场景**：生产部署后，前端可被点击劫持、MIME 嗅探；nginx 版本号泄露；大 body 攻击 API。
* **实际影响**：前端安全边界缺失；API 暴露于 DoS 与点击劫持。
* **修复建议**：补齐安全头、`client_max_body_size 10m`、`limit_req_zone`、`proxy_*_timeout`；`server_tokens off`；`index.html` 加 `no-cache`。
* **验证方式**：`curl -I http://localhost:3000/` 检查响应头；新增 nginx 配置 lint。

#### `[P1] Web prod 容器以 root 运行`

* **文件位置**：`apps/web/Dockerfile:36-39`
* **违反约束**：通用容器安全最佳实践
* **问题描述**：prod target 使用 `nginx:1.28-alpine` 但未设 `USER nginx`，nginx master 以 root 运行；`COPY --from=builder /src/dist /usr/share/nginx/html` 未带 `--chown`，文件属主 root。
* **触发场景**：容器内被攻陷的进程获得 root 权限，可修改 nginx 配置或逃逸。
* **实际影响**：容器提权面；违反最小权限原则。
* **修复建议**：改用 `nginxinc/nginx-unprivileged` 或 `USER nginx`；`COPY --chown=nginx:nginx`。
* **验证方式**：`docker run --rm <image> id` 应显示非 root；`docker inspect` 检查 User 字段。

#### `[P1] 镜像未按 digest 固定，构建不可复现`

* **文件位置**：`apps/api/Dockerfile:14,28,38`、`apps/web/Dockerfile:12,25,36`、`deploy/compose/docker-compose.yml:19,42`
* **违反约束**：`AGENTS.md` "不要通过删除/跳过测试使 CI 通过"（隐含可复现性）；通用供应链安全
* **问题描述**：`golang:1.26-bookworm`、`node:22-alpine`、`nginx:1.28-alpine`、`postgres:16-alpine` 均用 tag 而非 digest；而 `demo-mysql` 用 `mysql:8.4@sha256:...` 固定 digest，**做法不一致**。
* **触发场景**：上游推送同名恶意/缺陷镜像，CI/构建被污染。
* **实际影响**：构建不可复现；供应链攻击面。
* **修复建议**：所有基础镜像统一按 digest 固定。
* **验证方式**：grep Dockerfile/compose 确认所有 `FROM`/`image:` 含 `@sha256:`。

#### `[P1] CI 缺少安全扫描与依赖审计`

* **文件位置**：`.github/workflows/ci.yml`（全文）
* **违反约束**：`AGENTS.md` "改动 API、数据模型、权限、SQL 策略、部署、审计或安全边界，必须同时更新相应 ADR/设计/测试"；`P0-01 followup` 任务卡要求 license inventory
* **问题描述**：CI 仅跑 `gofmt -l`、`go vet`、`go test`、`npm run build`，**无 `gosec`/`Semgrep`/`govulncheck`/`npm audit`/`Trivy`/`SBOM`/license 检查/`-race`/覆盖率阈值**。
* **触发场景**：依赖漏洞、Go 代码安全缺陷、npm 包漏洞均不被发现。
* **实际影响**：安全漏洞进入生产；违反 AGENTS.md 安全审查要求。
* **修复建议**：新增 gosec、govulncheck、npm audit、Trivy 镜像扫描、license check job；`go test -race -coverprofile=coverage.out` 并设 ≥70% 阈值。
* **验证方式**：CI 新增 job 名称 `security-scan`、`dependency-audit`、`license-check`、`race-test`。

#### `[P1] admission maps 永久增长，`idle()` 从未被调用`

* **文件位置**：`apps/api/internal/adapter/admission.go:103-110`（`ensure` 只增不删）、`admission.go:31-35`（`idle` 已实现但无调用方）、`apps/api/internal/adapter/manager.go:76-87`（`cleanupLoop` 不清理 admission）
* **违反约束**：`webdb-design-draft.md` "不设无界队列"；`ADR-008` "可观测并审计拒绝事件"
* **问题描述**：`AdmissionController.users/workspaces/connections` 三个 map 在 `ensure()` 中只增不删；`limiter.idle()` 方法已实现但全包无调用方；`AdapterManager.cleanupLoop`（每 60s）只清理 `registry`，不清理 admission。每个新 user/workspace/connection 都会创建新 `*limiter` 并永久驻留。
* **触发场景**：长期运行后，大量历史 user/workspace/connection 的 `limiter` 对象驻留内存。
* **实际影响**：内存泄漏；高负载下可能 OOM；违反"无界队列禁止"精神。
* **修复建议**：在 `cleanupLoop` 中调用 `admission.gc(idleDuration)`，删除 `idle()` 为 true 的 limiter；或在 `TryAcquire` 路径顺带清理。
* **验证方式**：新增 `TestAdmissionController_GC`：创建 1000 个不同 userID，等待 idle 阈值后触发 gc，断言 map size 减少；`go test -race` 验证并发安全。

#### `[P1] MySQL 池未设置 `ConnMaxIdleTime`，与 ADR-008 "可配置 lifetime" 不符`

* **文件位置**：`apps/api/internal/adapter/manager.go:280-282`
* **违反约束**：`ADR-008` "每目标连接 max_open=10、max_idle=2、最大生命周期 30 分钟（含抖动）、获取超时 5 秒"
* **问题描述**：`createMySQL` 设置了 `SetMaxOpenConns`/`SetMaxIdleConns`/`SetConnMaxLifetime`，但**未设置 `SetConnMaxIdleTime`**（已 grep 确认全包无 `ConnMaxIdleTime`）。`database/sql` 默认 0 = 永不超时，可能导致长闲置连接被中间设备静默断开后仍被认为有效。
* **触发场景**：MySQL 连接闲置超 5 分钟后被 AWS RDS proxy/防火墙静默断开，下次 Ping 才发现错误。
* **实际影响**：偶发连接错误；与 PG 路径（`MaxConnIdleTime=5min`）行为不一致。
* **修复建议**：`db.SetConnMaxIdleTime(5 * time.Minute)` 与 PG 对齐。
* **验证方式**：新增 `TestCreateMySQL_ConnMaxIdleTime` 断言配置被设置。

#### `[P1] `createMySQL` 不校验端口范围，`createPG` 校验`

* **文件位置**：`apps/api/internal/adapter/manager.go:235-237`（PG 校验 `1..65535`）、`manager.go:270`（MySQL 直接 `fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)`）
* **违反约束**：通用工程正确性
* **问题描述**：`cfg.Port <= 0` 时 MySQL DSN 为 `host:-1`，`sql.Open` 不一定立即报错，错误延迟到连接时才暴露，错误信息不友好。
* **触发场景**：上游传入 `Port=0` 或负值。
* **实际影响**：连接失败错误难定位；与 PG 路径行为不一致。
* **修复建议**：在 `createPool` 入口统一校验 `cfg.Port >= 1 && cfg.Port <= 65535`。
* **验证方式**：新增 `TestCreatePool_InvalidPort` 断言返回 `ErrInvalidConfig`。

#### `[P1] `execPG`/`execMySQL` 额外行先物化再丢弃，可致 OOM`

* **文件位置**：`apps/api/internal/adapter/manager.go:619-627`（PG）、`manager.go:688-694`（MySQL）
* **违反约束**：`webdb-design-draft.md` "限制结果/导出大小，避免全量缓存"；`AGENTS.md` "限制结果/导出大小"
* **问题描述**：在判定"额外行（`rc >= effPage`）"之前，先调用 `rows.Values()`（PG）或 `rows.Scan(ptrs...)`（MySQL）完整物化该行所有列。注释（`manager.go:623`）写"只计数不拷贝"与实际行为不符。若额外行包含大 BLOB（如 100MB），即便最终只计数也会造成瞬时内存峰值。
* **触发场景**：用户查询 `SELECT * FROM large_blobs_table LIMIT 100`，第 101 行是大 BLOB，被完整物化后丢弃。
* **实际影响**：瞬时内存峰值；高并发下可能 OOM。
* **修复建议**：在 scan 之前先 `rows.Next()` 检查是否有额外行，若 `rc >= effPage` 则直接 break 不 scan；或用 `rows.ColumnTypes()` 估算行大小后决定是否 scan。
* **验证方式**：新增 `TestQuery_ExtraRowLargeBlob`：插入 100 行小数据 + 1 行 50MB BLOB，查询 `LIMIT 100`，断言内存峰值 < 10MB。

#### `[P1] `keyset.go` 未限制 sort keys 数量，可构造指数级 WHERE 子句 DoS`

* **文件位置**：`apps/api/internal/adapter/keyset.go:51-64`（`validIdent` 白名单但无数量限制）、`keyset.go:122-143`（`buildAfter` 递归生成 WHERE）
* **违反约束**：`webdb-design-draft.md` "不设无界队列"；通用 DoS 防护
* **问题描述**：`validIdent` 对单个 column 做白名单校验，但**不限制 sort keys 数量**。客户端可发送数百个 SortKey，`buildAfter` 递归生成数百个 `(col IS NOT NULL AND col > ?) OR (col IS NULL AND ...)` 子句，最终 WHERE 子句长度指数级增长。
* **触发场景**：客户端发送 `SortKeys: [{Column:"c1"},{Column:"c2"},...,{Column:"c100"}]`。
* **实际影响**：SQL 解析器拒绝过长 SQL（PG 默认 1GB，但内存先耗尽）；MySQL `max_allowed_packet` 拒绝；DoS。
* **修复建议**：在 `buildWrappedSQL` 入口加 `if len(specs) > 10 { return ErrUnsupportedQuery }`。
* **验证方式**：新增 `TestBuildWrappedSQL_TooManySortKeys` 断言返回错误。

#### `[P1] `manager_test.go` 中 `demo_reader` 执行 DDL/DML，与最小权限原则矛盾`

* **文件位置**：`apps/api/internal/adapter/manager_test.go:741-770`（`ensureEmployees` 用 `demo_reader` 执行 `CREATE TABLE`/`INSERT`）
* **违反约束**：`AGENTS.md` "SQL 必须由服务端以最小权限账号执行；只读"；`webdb-design-draft.md` "任意浏览器请求均无法获得或恢复目标数据库明文密码"（隐含只读账号）
* **问题描述**：测试用 `demo_reader` 账号执行 DDL/DML，意味着测试数据库的只读账号被授予了写权限。若该账号被复用到非测试场景或测试数据库被攻击者访问，将形成越权写入。`deploy/compose/init/demo-pg/01-init.sh` 与 `demo-mysql/01-init.sh` 都明确 `GRANT SELECT`，但测试用同一账号执行 DDL，说明 init 脚本或测试 setup 存在额外授权。
* **触发场景**：测试运行时 `demo_reader` 实际拥有写权限，攻击者通过测试暴露的端口（127.0.0.1:5433/3306）可写入数据。
* **实际影响**：违反只读账号原则；测试夹具与生产配置不一致。
* **修复建议**：测试改用独立的 `demo_ddl` 用户（仅在测试 setup 阶段用 superuser 预置数据），或在 `ensureEmployees` 中显式用 superuser 连接。
* **验证方式**：`verify-readonly.sh` 应能验证 `demo_reader` 无写权限；测试改用 `demo_ddl` 后通过。

#### `[P1] `mysql.go`/`postgres.go` 错误码映射错误，查询超时被报为池耗尽`

* **文件位置**：`apps/api/internal/adapter/mysql.go:12,33,59`、`apps/api/internal/adapter/postgres.go:12,33,59`、`apps/api/internal/adapter/manager.go:582-590`（`mapAcquireError` 把 `DeadlineExceeded` 映射为 `ErrConnPoolExhausted`）
* **违反约束**：通用工程正确性；`P0-03 任务卡` "稳定错误码"
* **问题描述**：`mysql.go`/`postgres.go` 的 `Schemas/Tables/Columns` 函数在查询错误时调用 `mapAcquireError`，但 `mapAcquireError` 是为连接获取错误设计的，把 `context.DeadlineExceeded` 映射为 `ErrConnPoolExhausted`。这些是查询错误而非获取错误，应使用 `mapExecError`（映射为 `ErrQueryTimeout`）。
* **触发场景**：schema 拉取超时时，上层收到 `ErrConnPoolExhausted`，可能误判为池满而扩容，实际是查询慢。
* **实际影响**：错误码语义错误，误导上层重试逻辑与告警。
* **修复建议**：`mysql.go`/`postgres.go` 中的 `mapAcquireError` 改为 `mapExecError`。
* **验证方式**：新增 `TestSchemas_QueryTimeout` 断言返回 `ErrQueryTimeout`。

#### `[P1] `creating` channel 在 `createPool` panic 时永不清理，导致永久阻塞`

* **文件位置**：`apps/api/internal/adapter/manager.go:60`（`creating sync.Map`）、`manager.go:146`（`createPool` 无 defer 清理）
* **违反约束**：通用工程正确性
* **问题描述**：`createPool` 函数无 `defer` 清理 `creating` 中的 channel。若 `pgxpool.New`/`sql.Open` 等发生 panic（罕见但非零概率），`creating` 中的 channel 永不关闭、永不删除，所有后续同 cid 的 `Get` 永久阻塞在 `<-ch`。
* **触发场景**：pgxpool 内部 panic、OOM、驱动 bug。
* **实际影响**：单个 cid 的所有后续查询永久阻塞；级联影响。
* **修复建议**：在 `createPool` 外层包 `defer func() { m.creating.Delete(cid); if r := recover(); r != nil { ... } }()`。
* **验证方式**：新增 `TestCreatePool_Panic` 模拟 panic，断言后续 `Get` 不阻塞。

#### `[P1] `goose.SetBaseFS` 全局状态，多 goroutine 并发调用竞争`

* **文件位置**：`apps/api/internal/migrate/migrate.go:23,33,45`
* **违反约束**：通用工程正确性
* **问题描述**：`goose.SetBaseFS(migrations)` 修改 goose 库的全局状态。`Run`/`Status`/`Validate` 三个函数都调用它，若多 goroutine 并发调用（例如测试中并行调用 `Validate` 与 `Run`），会竞争全局 FS。
* **触发场景**：测试并行调用 `migrate.Validate()` 与 `migrate.Run(ctx, db, "up")`。
* **实际影响**：数据竞争；`go test -race` 报错。
* **修复建议**：用 `goose.NewProvider`（v3.20+）创建独立实例；或加 `sync.Mutex` 保护。
* **验证方式**：`go test -race -tags=integration ./internal/migrate/...` 通过。

#### `[P1] `Validate` 不解析 SQL 语法，只检查 goose 指令字符串`

* **文件位置**：`apps/api/internal/migrate/migrate.go:54-70`
* **违反约束**：`ADR-013` "CI 必须验证 `up→down→up→up` 重复执行无副作用"
* **问题描述**：`Validate` 只检查文件非空、含 `-- +goose Up`/`-- +goose Down` 字符串，**未做 SQL 词法/语法解析**。一个 SQL 语法错误的 migration（如缺分号、引用不存在的表）会通过 `Validate` 但在执行时失败。
* **触发场景**：CI 运行 `migrate.Validate`，开发者误以为通过即安全。
* **实际影响**：语法错误的 migration 进入 main 分支，生产迁移失败。
* **修复建议**：在 CI 中至少跑一次 `up` 验证（已有 `up→down→up→up` 集成测试，应确保 CI 执行）；或用 `pg_query_go` 解析每条 SQL。
* **验证方式**：CI 中 `go test -tags=integration ./internal/migrate/...` 必须执行。

#### `[P1] 测试默认密码 `change_me` 进入源码`

* **文件位置**：`apps/api/internal/metadata/integration_test.go:40`、`apps/api/internal/adapter/manager_test.go:36,43`
* **违反约束**：`AGENTS.md` "不提交密钥、真实用户数据、导出文件、`.env` 或生产日志"
* **问题描述**：`password := envOrDefault("META_DB_PASSWORD", "change_me")` 与 `manager_test.go` 中的 `"change_me"` 字面量。虽为测试 DB 密码，但**出现在源码中**，模式会被复制。
* **触发场景**：开发者复制测试模式到生产脚本。
* **实际影响**：违反 AGENTS.md "不提交密钥"原则；模式扩散。
* **修复建议**：去掉默认值，强制测试环境显式提供 `META_DB_PASSWORD`；或改为不含语义的占位符（如 `test-only-do-not-use`）。
* **验证方式**：grep 仓库 `change_me` 应仅出现在 env.example 与 compose（dev 默认）中。

#### `[P1] 多步写操作缺事务边界`

* **文件位置**：`apps/api/internal/metadata/repo.go`（接口层无 `WithTx`）、`apps/api/internal/metadata/postgres_repo.go`（所有方法都用 `s.DB` 自动提交）
* **违反约束**：`ADR-013` "轮换时先追加新版本再原子更新连接引用"；通用数据一致性
* **问题描述**：`CreateConnection` + `CreatePolicy`、`CreateExecution` + `AppendAudit`、凭证轮换等组合操作若由调用方分两次调用，无法保证原子性。仓储接口未暴露 `WithTx`/`BeginTx`。
* **触发场景**：`CreateConnection` 成功但 `CreatePolicy` 失败，连接无策略；凭证轮换追加新版本后连接引用更新失败，版本不一致。
* **实际影响**：数据不一致；违反 ADR-013 凭证轮换原子性要求。
* **修复建议**：在 `PGStore` 上新增 `RunInTx(ctx, fn func(*PGStore) error) error`，把 `*sql.DB` 与 `*sql.Tx` 统一抽象。
* **验证方式**：新增 `TestRunInTx_RollbackOnError`、`TestRunInTx_CommitOnSuccess`。

#### `[P1] `ListUsers`/`ListWorkspaces` 缺 LIMIT 上界裁剪`

* **文件位置**：`apps/api/internal/metadata/postgres_repo.go:95`、`postgres_repo.go:155`
* **违反约束**：`webdb-design-draft.md` "限制结果/导出大小"
* **问题描述**：`ListUsers`/`ListWorkspaces` 接受调用方传入的 `limit/offset`，未做上界裁剪。`QueryAudit` 做了 `q.Limit > 1000 → 1000`（`postgres_repo.go:615-617`），但 List 方法没有。
* **触发场景**：调用方传入 `limit=1000000`，导致 OOM 或慢查询。
* **实际影响**：资源耗尽；DoS。
* **修复建议**：所有 List 方法统一加上界，如 `if limit > 1000 { limit = 1000 }`。
* **验证方式**：新增 `TestListUsers_LimitCapped` 断言 `limit=1000000` 返回 ≤1000 行。

### P2 问题（建议修复）

| # | 文件:行 | 问题 |
|---|---------|------|
| 1 | `apps/api/internal/adapter/types.go:130-143,145-147` | `PoolStats.AcquireTimeouts`、`ManagerStats`、`constantTimeEq` 是死代码，从未被写入或调用，误导审查者 |
| 2 | `apps/api/internal/adapter/manager.go:342` | `PoolHandle.Release()` 是 no-op，但测试普遍调用，API 误导 |
| 3 | `apps/api/internal/adapter/manager.go:723` | `&[]string{"_"}[0]` hacky 占位符，可读性差 |
| 4 | `apps/api/internal/adapter/manager.go:182-186` | 池替换时未调用 `invalidateByPool`，stale token 占用内存至自然过期 |
| 5 | `apps/api/internal/adapter/pagination.go:140-159` | `claim` 后若 panic，`inUse` 永不重置，token 永久驻留 |
| 6 | `apps/api/internal/adapter/manager.go:224-226` | `isLocalHost` 硬编码魔字符串 `demo-pg`/`demo-mysql`，生产误配置可绕过 TLS |
| 7 | `apps/api/internal/metadata/postgres_repo.go:414-415` | `PolicyByConnection` 返回 `(nil, nil)` 与其它方法 not-found 行为不一致，易让上层 `err != nil` 判断失误 |
| 8 | `apps/api/internal/metadata/postgres_repo.go:520-528` | `UpdateExecution` 在 `ResultRef=nil` 时不清理 `ResultExpiresAt`，可能写入无意义过期时间 |
| 9 | `apps/api/internal/metadata/integration_test.go:62,78,144` | `_ = migrate.Run(... down-to 0)` 忽略错误，可能掩盖 down 失败 |
| 10 | `apps/api/internal/migrate/migrations/00002_statement_hash_check.sql:3` | `'legacy-migration'` 占位符破坏 `statement_hash` 语义（应为 `algo:hex`） |
| 11 | `apps/api/internal/migrate/migrations/00001_p0_schema.sql:19-20` | `users.email` 允许中间空白/tab/换行（仅拒绝首尾空白） |
| 12 | `apps/api/internal/metadata/integration_test.go` | 缺少跨工作区 execution 关联、跨工作区 audit connection/execution 关联、跨工作区 actor 拒绝测试（ADR-013 验证清单要求） |
| 13 | `packages/contracts/src/index.ts:47,73-80` | `maxRows` 无上限声明；`AuditEvent.action` 为自由 string，metadata 为 `Record<string,unknown>`，未提升为必填结构化字段 |
| 14 | `deploy/compose/docker-compose.yml:146-164` | compose 内 `web` 未传 `VITE_API_URL`，Vite dev server 默认无 `/api` 代理，App.tsx 健康检查恒失败 |

### P3 问题（可选优化）

| # | 文件:行 | 问题 |
|---|---------|------|
| 1 | `apps/api/internal/adapter/manager.go:766-803` | `copyAndMeasure` reflect 默认分支对未知类型估算 64 字节，可能严重低估实际内存 |
| 2 | `apps/api/internal/adapter/manager.go:251-252` | `MaxConnIdleTime`、`HealthCheckPeriod` 硬编码不可配 |
| 3 | `apps/api/internal/metadata/postgres_repo.go:352,532` | `res.RowsAffected()` 错误被吞 |
| 4 | `apps/api/internal/migrate/migrate.go:22` | `Run` 不强制 ctx 超时，调用方可能传入无超时 context |
| 5 | `.github/workflows/pr-policy.yml:35` | `grep -Eq 'TODO\|待填写'` 误报风险高，合法 ADR 链接含 TODO 会被拒 |
| 6 | `apps/api/internal/adapter/postgres.go:9` | `pgSchemas` 未过滤 `pg_toast`、`pg_temp_*` 等系统 schema |

## 5. 测试建议

| 测试场景 | 输入条件 | 预期结果 | 推荐类型 |
|----------|----------|----------|----------|
| 优雅关停 | 启动 API，发起 30s 查询，中途 `kill -TERM` | 等待查询完成或 30s 超时后退出，连接归还 | 集成测试 |
| 审计脱敏失败路径 | 传入畸形 JSON metadata | 返回 `{}`，不返回原始 raw | 单元测试 |
| 跨工作区 execution 关联 | actor 是 ws2 成员，execution 属于 ws1 | DB FK 拒绝 | 集成测试 |
| 跨工作区 audit connection 关联 | audit 引用 ws2 的 connection | DB FK 拒绝 | 集成测试 |
| DESC+NullsLast keyset | DESC 排序 + NULL 最后 | keyset 正确续页 | 集成测试 |
| `creating` panic 路径 | 模拟 `pgxpool.New` panic | 后续 `Get` 不阻塞，返回错误 | 单元测试 |
| admission GC | 创建 1000 个不同 userID，等待 idle | gc 后 map size 减少 | 单元测试（`-race`） |
| sort keys 数量上限 | 发送 100 个 SortKey | 返回 `ErrUnsupportedQuery` | 单元测试 |
| 额外行大 BLOB | 100 行小数据 + 1 行 50MB BLOB，`LIMIT 100` | 内存峰值 < 10MB | 集成测试 |
| MySQL `ConnMaxIdleTime` | 创建 MySQL 池 | 配置被设置为 5min | 单元测试 |
| KEK 强制设置 | `WEBDB_KEK` 未设置 | compose config 报错 | 部署测试 |
| demo-mysql 密码不进 argv | 容器运行时 `ps aux` | 无 `-p` 参数 | 部署测试 |
| SQL AST 拒绝多语句 | `SELECT 1; DROP TABLE x` | 拒绝（待 P0-04 解析器实现） | 单元测试 |
| SQL AST 拒绝数据修改 CTE | `WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d` | 拒绝 | 单元测试 |
| nginx 安全头 | `curl -I` | 含 `X-Content-Type-Options`、`X-Frame-Options` | 集成测试 |
| Web prod 容器非 root | `docker run --rm <image> id` | 非 root | 部署测试 |

## 6. 未发现问题的关键区域

| 模块 | 检查内容 | 结论 |
|------|----------|------|
| `apps/api/internal/adapter/errors.go` | `wrapError` 屏蔽底层错误细节 | 良好，`Message = "database operation failed"` 不泄露细节 |
| `apps/api/internal/adapter/metadata.go` | 类型定义清晰，`omitempty` 合理 | 无问题 |
| `apps/api/internal/adapter/pagination.go` token 生成 | `genToken` 用 `crypto/rand` 32 字节 | 符合 GO-CRYPTO-001 |
| `apps/api/internal/metadata/models.go` 凭证字段 | `PasswordHash`、`Ciphertext`、`WrappedDEK` 等标记 `json:"-"` | 良好，不进入 API 响应 |
| `apps/api/internal/migrate/migrations/00001_p0_schema.sql` 复合外键 | `workspace_id` 非空、复合 FK 分量非空 | 符合 ADR-013 |
| `audit_events` 不可变触发器 | `BEFORE UPDATE/DELETE/TRUNCATE` 拒绝 | 符合 ADR-013 |
| `credential_envelopes` 字段约束 | 所有 BYTEA `octet_length > 0`、`version > 0` | 符合 ADR-013 |
| `deploy/compose/docker-compose.yml` 网络隔离 | `webdb-frontend`/`webdb-backend` 分离，`web` 仅在前端，`api` 跨两网 | 符合 ADR-001 |
| `deploy/compose/docker-compose.yml` 端口绑定 | 全部 `127.0.0.1:` | 不暴露外网 |
| `deploy/compose/verify-readonly.sh` | 用 `MYSQL_PWD`/`PGPASSWORD` env 避免 argv | 良好（与 init 脚本自相矛盾，见 P0 #4） |
| `apps/api/internal/adapter/manager.go` 资源归还 | `defer conn.Release()`、`defer rows.Close()` | 即使 query/scan 中途返回错误也触发，符合 AGENTS.md |
| `apps/api/internal/adapter/manager.go` 结果大小限制 | `MaxPageBytes=16MB`、`MaxCellBytes=2MB` 双重限制 | 良好 |
| `apps/api/internal/adapter/manager.go` `copyAndMeasure` `[]byte` 深拷贝 | 显式 `copy(dst, src)` | 避免驱动 buffer 复用问题 |
| `apps/api/internal/metadata/postgres_repo.go` 参数化查询 | 所有查询用 `$N` 占位符 | 无 SQL 注入 |
| `apps/api/internal/metadata/postgres_repo.go` 审计 `OccurredAt` 零值检查 | `if e.OccurredAt.IsZero()` | 良好 |

## 7. 最终遗漏检查

| 检查项 | 结果 |
|--------|------|
| 是否读取了所有项目约束 | ✅ 已读取 AGENTS.md、CLAUDE.md、webdb-design-draft.md、ADR-001/002/005/006/007/008/009/010/013、P0-02/03/04 任务卡、P0-04 提案 |
| 是否审查了全部变更文件 | ✅ 98 个文件全覆盖，重点深入 adapter/metadata/migrate/cmd/server/compose/CI/web/contracts |
| 是否检查了相关调用链 | ✅ 跨文件验证了 `PoolHandle.Query` → `buildWrappedSQL` → `execPG/MySQL`、`AppendAudit` → `sanitizeAuditMetadata` → DB CHECK、`createPool` → `creating` channel → `Get` |
| 是否检查了测试 | ✅ 已审查 `manager_test.go`、`integration_test.go`、`main_test.go`，识别出测试覆盖缺口与测试自身安全问题 |
| 是否检查了安全和权限 | ✅ 跨工作区 FK、审计不可变、凭证信封、KEK、密码 argv、TLS、容器 root、nginx 安全头均已检查 |
| 是否检查了数据库和数据一致性 | ✅ Schema 约束、复合 FK、CHECK、事务边界、连接池配置、keyset 分页、admission 控制均已检查 |
| 是否存在重复或缺乏依据的问题 | ✅ 已合并同根因问题（如 `mapAcquireError` 误用在 mysql.go/postgres.go 合并为一条 P1）；所有问题均有具体 file:line 与约束依据 |
| 是否在发现前几个问题后提前结束 | ✅ 已完成全 98 文件审查，未提前结束 |

## 最终结论

**存在 P0，禁止合并**

7 个 P0 问题中：
- 4 个为安全边界硬约束违反（KEK 默认弱值、密码进 argv、审计脱敏 fail-open、审计启发式可绕过）；
- 1 个为运行时正确性硬约束违反（无优雅关停导致连接不归还）；
- 1 个为本分支任务（P0-04）核心交付物缺失（方言 AST 解析未实现，且 `keyset.go` 用字符串去分号作为安全前置，违反 ADR-007）；
- 1 个为审计完整性（`looksLikeSQL` 误删合法摘要，破坏审计完整性）。

### 建议处理顺序

1. 立即修复 P0 #1（KEK）、#4（demo-mysql argv）、#5（CI argv）—— 配置/脚本层修复，风险低；
2. 修复 P0 #2（优雅关停）—— 影响 P0-03 已交付的连接归还验收；
3. 修复 P0 #3、#7（审计脱敏）—— 应用层 fail-closed；
4. 明确 P0 #6（AST 解析）的处理策略：要么在本 PR 内交付解析器，要么在 PR 描述中明确标注"仅交付提案文档，解析器实现待后续 PR"，并在 `Query` 入口加 panic 防止误用。

完成所有 P0 修复后，需重新进行一轮安全审查方可合并。

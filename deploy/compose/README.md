# Docker Compose — WebDB P0 本地部署

存放 P0 最小本地部署定义。所有服务仅绑定 `127.0.0.1`，不含真实凭证或生产连接信息。

## 服务一览

| 服务 | 用途 | 镜像 | 宿主端口 → 容器端口 | 依赖 |
|------|------|------|---------------------|------|
| `webdb-meta` | WebDB 元数据库（PostgreSQL 16） | `postgres:16-alpine` | `5432:5432` | — |
| `demo-pg` | 演示 PostgreSQL + 合成数据 + 只读账号 | `postgres:16-alpine` | `5433:5432` | — |
| `demo-mysql` | 演示 MySQL 8.4 LTS + 合成数据 + 只读账号 | `mysql:8.4`（固定 digest） | `3306:3306` | — |
| `api-migrate` | one-shot 迁移（`api migrate up`，退出即成功） | 本地构建 `apps/api`（dev target） | — | webdb-meta (healthy) |
| `demo-seed` | one-shot 演示 seed（`WEBDB_DEMO_SEED=true`，退出即成功） | 本地构建 `apps/api`（dev target） | — | api-migrate (completed) |
| `api` | Go API/执行服务（唯一可连接 DB 的组件） | 本地构建 `apps/api`（dev target） | `8080:8080` | webdb-meta, demo-pg, demo-mysql, api-migrate, demo-seed |
| `web` | React 前端 Vite 开发服务器 | 本地构建 `apps/web`（dev target） | `3000:5173` | api |

**启动依赖链（fail-closed）：**

```text
webdb-meta healthy → api-migrate completed → demo-seed completed → api healthy → web healthy
```

- `api-migrate` 使用独立管理员 `META_MIGRATE_USER/PASSWORD` 执行 `migrate up`；失败时 `demo-seed`/`api`/`web` 均不启动（`service_completed_successfully`）。
- `demo-seed` 仅在 `WEBDB_DEMO_SEED=true` 时执行；生产部署不设置该开关则**绝不自动 seed**。
- `api`/`web` 依赖 `demo-seed` 成功退出后才启动，保证空卷首次启动即有业务表、演示身份与可授权连接。

Web 与 API 通过 `webdb-frontend` 通信；只有 API 和数据库服务加入 `webdb-backend`。API 是两个网络之间的唯一应用边界，浏览器和 Web 容器不能通过 Compose 后端网络直连数据库。健康检查见各服务定义的 `healthcheck`。

## 安全约束

- **浏览器不直连数据库**。所有数据库端口仅绑定 `127.0.0.1`，外部不可达。
- `env.example` 是示例模板，**不含真实密钥**。所有值为 `change_me` 占位符。
- 真实 `.env` 不会被提交（已在 `.gitignore` 中排除）。
- 演示数据库仅含合成数据（`departments` / `employees` 表，`example.local` 邮箱）。

## 快速启动

### 1. 准备环境变量

**Linux / macOS：**
```bash
cp deploy/compose/env.example deploy/compose/.env
```

**Windows PowerShell：**
```powershell
Copy-Item deploy/compose/env.example deploy/compose/.env
```

默认值可满足本地开发。需要自定义时编辑 `.env` 文件。

### 2. 启动

```bash
docker compose -f deploy/compose/docker-compose.yml up -d --build --wait
```

首次启动会拉取镜像、构建 API/Web 镜像并初始化数据库。

### 3. 查看状态

```bash
docker compose -f deploy/compose/docker-compose.yml ps
```

5 个常驻服务（`webdb-meta`、`demo-pg`、`demo-mysql`、`api`、`web`）均应显示 `healthy`；
`api-migrate` 与 `demo-seed` 为 one-shot 服务，成功退出后显示 `exited (0)`（这是预期状态，
不是启动失败）。

### 4. 健康检查

```bash
# API
curl http://127.0.0.1:8080/health
# → {"status":"ok","version":"0.1.0","time":"..."}

# Web
curl -o /dev/null -w "%{http_code}" http://127.0.0.1:3000/
# → 200
```

### 5. 只读权限验证

```bash
bash deploy/compose/verify-readonly.sh
```

验证内容：错误密码被拒绝、正确密码可 SELECT、INSERT/UPDATE/DELETE/DDL 被拒绝。

### 6. 查看日志

```bash
docker compose -f deploy/compose/docker-compose.yml logs
docker compose -f deploy/compose/docker-compose.yml logs demo-pg
```

### 7. 停止

```bash
docker compose -f deploy/compose/docker-compose.yml down
```

默认**不删除**持久化卷（合成数据保留）。

### 8. 重建纯合成测试环境

需要授权后才能删除卷并重建：

```bash
docker compose -f deploy/compose/docker-compose.yml down -v
docker compose -f deploy/compose/docker-compose.yml up -d --build --wait
```

> `-v` 会删除 `webdb-p0_webdb-meta-data`、`webdb-p0_demo-pg-data`、`webdb-p0_demo-mysql-data` 三个合成数据卷。**未经明确授权不得执行**。

## 演示身份 seed

首次启动时 `demo-seed` 会在元数据库创建以下**固定合成**演示资源（不含真实 PII）：

| 资源 | 固定 UUID | 说明 |
|------|-----------|------|
| workspace | `f1160d75-26f7-46e0-b3e0-570ea65c232e` | `Demo Workspace` |
| user | `73e8c8f0-83e4-4096-8400-15ca15073d6b` | `demo@example.local`，status=active |
| workspace_member | (workspace, user) | role=owner |
| connection（PostgreSQL） | `d80a86cb-d4e7-4afb-9ab1-8df572ad9c69` | `demo-pg:5432/webdb_demo`，账号 `demo_reader` |
| connection（MySQL） | `37760cae-7ddc-408d-b2b0-4505c3ea2243` | `demo-mysql:3306/webdb_demo`，账号 `DEMO_MYSQL_USER` |
| connection_policy ×2 | — | allow_read=true，max_rows=500，statement_timeout=5000ms |
| credential_envelope ×2 | 由 LifecycleManager 生成 | 演示只读密码经 Envelope v1 加密（仅存密文） |

**安全说明：**

- 演示数据库密码仅经 `DEMO_PG_READER_PASSWORD` / `DEMO_MYSQL_READER_PASSWORD` / `DEMO_MYSQL_USER` 环境变量注入，交由 `LifecycleManager` 加密，**绝不写入 SQL/文件/命令行/日志**。
- KEK 使用版本化 `WEBDB_KEK_V1` + `WEBDB_ACTIVE_KEK_VERSION`（见 `env.example`）；缺失/非法时 seed fail-closed，`api`/`web` 不启动。
- 固定合成 UUID 以 `apps/api/internal/seeddemo/ids.go` 为**唯一权威来源**；`demo-seed` 启动时校验 `DEMO_PRINCIPAL_*` 与其一致，不一致即拒绝（防漂移）。

**幂等与冲突语义：**

- 可重复执行：已存在且一致的数据视为成功（no-op），不重复创建，不新增无界凭证版本。
- 固定 ID 已存在但字段不一致 → seed 失败（fail-closed），不静默覆盖未知数据。
- seed 中途失败遗留未绑定连接的凭证信封 → 二次 seed 明确拒绝，需 `down -v` 冷启动或人工检查。

**开关：** `WEBDB_DEMO_SEED` 仅严格接受 `true`；缺失、`false` 或非法值均拒绝。生产部署不设置该开关，因此**绝不自动 seed**。

## 只读账号

| 数据库 | 用户名 | 密码来源 | 权限 |
|--------|--------|----------|------|
| PostgreSQL | `demo_reader` | `DEMO_PG_READER_PASSWORD` 环境变量 | 仅 SELECT（含 TEMPORARY 已撤销） |
| MySQL | `demo_reader`（可通过 `DEMO_MYSQL_USER` 自定义） | `DEMO_MYSQL_READER_PASSWORD` 环境变量 | 仅 SELECT |

API 服务使用上述只读账号连接演示数据库，不持有管理员凭证。
只读账号密码通过初始化脚本安全设置，支持单引号、双引号、空格、反斜杠等特殊字符。
验证脚本从容器运行时环境读取凭证，与 Compose 实际传值一致。

## 文件说明

```
deploy/compose/
├── docker-compose.yml       # 服务定义
├── env.example               # 环境变量模板（不含真实密钥）
├── README.md                 # 本文件
├── verify-readonly.sh        # 只读权限验证脚本
├── verify-prod-roles.sh      # 生产角色拆分验证脚本（R6 / WEB-27）
└── init/
    ├── demo-pg/01-init.sh        # PostgreSQL 初始化
    ├── demo-mysql/01-init.sh     # MySQL 初始化
    └── prod-roles/01-create-prod-roles.sh # 生产角色拆分（R6 / WEB-27）
```

## 生产角色拆分（R6 / WEB-27）

> ⚠️ **部署边界**：`init/prod-roles/01-create-prod-roles.sh` 与 `verify-prod-roles.sh` 是**生产部署脚本**，不属于本地 Compose 配置。执行前必须显式设置 `WEBDB_PRODUCTION_DEPLOY=1`，避免被误认为本地开发初始化。本地开发请使用 `demo-*` init 脚本。
>
> **例外声明**：本章节属于 **ADR-018（有期限例外，截止 2026-09-30 或独立生产部署路径建立时取先）**——P0 阶段尚无独立生产部署路径，生产角色脚本暂驻 `deploy/compose/`，到期必须迁移。详见 [ADR-018](../../docs/adr/ADR-018-prod-roles-under-compose-exception.md)。

`audit_events` 的 UPDATE/DELETE/TRUNCATE 拒绝触发器（`deny_audit_mutation`）可被 SUPERUSER 绕过。
生产角色拆分创建最小权限运行时角色，使审计不可篡改不依赖单一超级用户：

| 角色 | 用途 | audit_events 权限 |
|------|------|-------------------|
| `webdb_app_runtime` | 应用运行时连接（业务表 + 审计写入） | `SELECT` + `INSERT`（无 UPDATE/DELETE/TRUNCATE） |
| `webdb_audit_writer` | 独立审计写入连接（可选） | `SELECT` + `INSERT`（无 UPDATE/DELETE/TRUNCATE） |

执行步骤（首次 production-like 部署前必须完成；在**目标 PostgreSQL 可连接的主机**执行，需 `psql`）：

```bash
# 前置：工作目录切换至 deploy/compose（脚本与 README 在此目录）
cd deploy/compose

# 前置：管理员凭证、目标库、端点（生产勿用占位符密码）
export WEBDB_PRODUCTION_DEPLOY=1
export POSTGRES_USER=webdb_admin          # 管理员角色（不得用保留角色名）
export POSTGRES_PASSWORD='<管理员密码>'     # 非空且非 change_me
export POSTGRES_DB=webdb_meta
export PGHOST='<目标主机>'   # 替换为实际目标主机（引号防止未加引号尖括号触发 Bash 重定向解析）
export PGPORT=5432
export WEBDB_APP_PASSWORD='<应用角色密码>'   # 非空且非 change_me
export WEBDB_AUDIT_PASSWORD='<审计角色密码>' # 非空且非 change_me

# 1. 创建/收敛角色（幂等；重跑会应用当前密码）
./init/prod-roles/01-create-prod-roles.sh

# 2. 验证：非 SUPERUSER 角色对 audit_events 的 UPDATE/DELETE/TRUNCATE 被拒绝
#    （含独立触发器验证角色，确认由 deny_audit_mutation 拒绝而非仅 ACL）
#    第 10 步为"密码不落日志"负向测试，需可读取服务器日志：
#    export VERIFY_PROD_LOG_SOURCE='<输出服务器日志到 stdout 的命令>'
./verify-prod-roles.sh
```

**安全说明（PR37 审查后）：**

- **密码机制**：密码一律经 psql `\password` 设置（明文只经 stdin 管道进入 psql）。脚本**强制 `password_encryption=scram-sha-256`**（非 scram 立即退出）并**强制要求 `setsid` 可用**（`\password` 有控制终端时读 `/dev/tty` 而非 stdin，交互式重跑会挂起；缺失 setsid 即拒绝执行，不回退），故校验器为 `SCRAM-SHA-256$...`，明文密码不进 SQL 文本、不落日志。已实测 PostgreSQL 在 `log_statement=all` 下**不会**对 `CREATE/ALTER ROLE ... PASSWORD` 子句脱敏，因此禁用 `format('%L')`/`\getenv` 拼接含明文的 DDL。
- **负向测试**：`verify-prod-roles.sh` 第 10 步在 `log_statement=all` 下用哨兵密码验证明文不进入日志（含正对照证明日志记录/读取有效），并对 LOG_PROBE 做登录断言确认密码实际生效。需超级用户，且需指定日志来源：生产环境设置 `VERIFY_PROD_LOG_SOURCE`（输出日志行的命令字符串）；compose 本地验证会自动探测 `webdb-meta` 容器并走 `docker logs`（命令数组直接调用）。无法确定日志来源时该步骤明确失败，不静默通过。
- **连接断言**：verify 步骤 0 对 `WEBDB_APP_USER`/`WEBDB_AUDIT_USER` 做 `SELECT current_user` 断言（必须是目标非超级用户、`is_superuser=off`），fail-closed 防止误用超级用户连接。
- **API 连接**：生产 API 的 `META_DB_USER` 已配置为 `webdb_app_runtime`（运行时非超级用户，独立审计连接用 `webdb_audit_writer`）；迁移需 DDL 权限，使用独立的 `META_MIGRATE_USER`/`META_MIGRATE_PASSWORD`（管理员），见 `docker-compose.yml` 与 `metaDSN()`（未设时回退 `META_DB_*` 保持本地开发向后兼容）。
- **清理回归测试**：`test-prod-roles-cleanup.sh` 覆盖 verify 的成功 / 失败 / 中断三条清理路径（中断为 TERM 真实信号，退出码与零残留断言），并覆盖 create 缺失 setsid 的失败路径。需与 verify 相同的环境变量，且生产角色已创建。
- **所有权 fail-closed**：`01-create-prod-roles.sh` 在收敛前检测 `webdb_app_runtime`/`webdb_audit_writer` 是否持有数据库/模式/对象所有权，若持有则拒绝继续（错误提示先由受控管理员转移所有权）。成员关系收敛会撤销**所有**涉及任一生产角色的未批准父角色成员关系，不只两者之间。
- 约束：`POSTGRES_USER` 不得使用保留角色名（`webdb_app_runtime` / `webdb_audit_writer`）。
- 角色拆分完成后，API 应改用 `webdb_app_runtime` 连接，不再使用 SUPERUSER。

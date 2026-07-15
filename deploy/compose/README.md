# Docker Compose — WebDB P0 本地部署

存放 P0 最小本地部署定义。所有服务仅绑定 `127.0.0.1`，不含真实凭证或生产连接信息。

## 服务一览

| 服务 | 用途 | 镜像 | 宿主端口 → 容器端口 | 依赖 |
|------|------|------|---------------------|------|
| `webdb-meta` | WebDB 元数据库（PostgreSQL 16） | `postgres:16-alpine` | `5432:5432` | — |
| `demo-pg` | 演示 PostgreSQL + 合成数据 + 只读账号 | `postgres:16-alpine` | `5433:5432` | — |
| `demo-mysql` | 演示 MySQL 8.4 LTS + 合成数据 + 只读账号 | `mysql:8.4`（固定 digest） | `3306:3306` | — |
| `api` | Go API/执行服务（唯一可连接 DB 的组件） | 本地构建 `apps/api`（dev target） | `8080:8080` | webdb-meta, demo-pg, demo-mysql |
| `web` | React 前端 Vite 开发服务器 | 本地构建 `apps/web`（dev target） | `3000:5173` | api |

所有服务加入隔离网络 `webdb-net`（bridge）。健康检查见各服务定义的 `healthcheck`。

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

5 个服务均应显示 `healthy`。

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

## 只读账号

| 数据库 | 用户名 | 密码来源 | 权限 |
|--------|--------|----------|------|
| PostgreSQL | `demo_reader` | `DEMO_PG_READER_PASSWORD` 环境变量 | 仅 SELECT |
| MySQL | `demo_reader`（可通过 `DEMO_MYSQL_USER` 自定义） | `DEMO_MYSQL_READER_PASSWORD` 环境变量 | 仅 SELECT |

只读账号密码通过初始化脚本安全设置，支持单引号、双引号、空格、反斜杠等特殊字符。

## 文件说明

```
deploy/compose/
├── docker-compose.yml   # 服务定义
├── env.example           # 环境变量模板（不含真实密钥）
├── README.md             # 本文件
├── verify-readonly.sh    # 只读权限验证脚本
└── init/
    ├── demo-pg/01-init.sh    # PostgreSQL 初始化
    └── demo-mysql/01-init.sh # MySQL 初始化
```

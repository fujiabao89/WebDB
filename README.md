# WebDB

WebDB 是面向团队的自托管数据库工作台。当前处于 P0 阶段，目标范围是 PostgreSQL/MySQL 连接、Schema 拉取、只读 SQL、服务端分页和追加式审计。P0-01 已完成工程骨架和 Docker Compose 本地环境；P0-02 已完成元数据库迁移；P0-03 已完成数据库 Adapter（双引擎连接池、Schema 拉取、SQL 透传执行与 keyset 分页）；P0-04 已完成服务端 SQL 安全策略（方言 AST 分类 + ECM lexer + Policy 决策引擎 + 内部执行编排）；P0-05 已完成凭证与审计基线（信封加密、轮换、追加式审计与脱敏）；P0-06 已完成最小 Web 工作台（连接列表、Schema 浏览、只读执行与服务端分页）。API 公开 `/health` 与 `/api/v1` 下 6 条路由（连接列表、Schema/表/列浏览、执行与续页）。

## 仓库结构

```text
apps/web/           React + TypeScript 前端和健康页
apps/api/           Go API 模块化单体和健康端点
packages/contracts/ 前后端共享 TypeScript 契约
deploy/compose/     本地 Compose、合成演示数据和只读验证脚本
docs/adr/           已接受的架构决策记录
docs/tasks/         P0 任务卡与交接状态
```

## 快速启动

需要 Docker Engine 与 Docker Compose v2。先复制本地环境模板；数据库密码为 `change_me` 占位值（仅本地演示），`WEBDB_KEK_V1` 必须生成并替换（占位值非法，缺失/非法时 `api`/`web` 不启动）。

Linux / macOS：

```bash
cp deploy/compose/env.example deploy/compose/.env
openssl rand -base64 32   # 将输出粘贴到 .env 的 WEBDB_KEK_V1=（32 字节 base64）
docker compose -f deploy/compose/docker-compose.yml up -d --build --wait
docker compose -f deploy/compose/docker-compose.yml ps
```

Windows PowerShell：

```powershell
Copy-Item deploy/compose/env.example deploy/compose/.env
# 生成 32 字节 base64 KEK 并粘贴到 .env 的 WEBDB_KEK_V1=；例如在 Git Bash 运行：openssl rand -base64 32
docker compose -f deploy/compose/docker-compose.yml up -d --build --wait
docker compose -f deploy/compose/docker-compose.yml ps
```

启动后：

- Web 健康页：`http://127.0.0.1:3000`
- API 健康端点：`http://127.0.0.1:8080/health`
- Web 代理健康端点：`http://127.0.0.1:3000/api/health`

验证演示 PostgreSQL/MySQL 的 TCP 密码认证和只读权限：

```bash
bash deploy/compose/verify-readonly.sh
```

停止服务但保留合成数据卷：

```bash
docker compose -f deploy/compose/docker-compose.yml down
```

更完整的端口、凭证覆盖和测试卷重建说明见 [Compose 文档](deploy/compose/README.md)。

## P0 安全边界

- 浏览器和 Web 容器不得直连目标数据库，也不得接收数据库密码、KEK 或明文密钥。
- API 是唯一允许连接目标数据库的应用组件；目标数据库账号必须遵循最小权限。
- SQL 安全策略默认拒绝无法可靠解析的语句，并要求单语句、方言 AST 判定、超时、行数上限和取消能力。
- 仓库不得提交真实凭证、`.env`、真实用户数据、导出文件或生产日志；演示数据全部为合成数据。
- 当前 Compose 面向本地开发，端口仅绑定 `127.0.0.1`，不能直接作为生产部署配置。
- 元数据库连接池仅接受显式环境配置（`META_DB_MAX_OPEN_CONNS`/`META_DB_MAX_IDLE_CONNS`/`META_DB_CONN_MAX_LIFETIME`，Owner 批准 2026-08：10/2/30m）；缺失、非法或 idle > open 时 API 拒绝启动（fail-closed），不回退到 database/sql 无界默认。

## 开发验证

```bash
# API
go -C apps/api test ./...
go -C apps/api vet ./...

# Web
npm --prefix apps/web ci
npm --prefix apps/web run typecheck
npm --prefix apps/web test
npm --prefix apps/web run build

# Contracts
npm --prefix packages/contracts ci
npm --prefix packages/contracts run typecheck
npm --prefix packages/contracts test
```

CI 还会执行格式检查、仓库安全检查和 PR 契约检查。不要通过删除或跳过测试使 CI 通过。

## 文档导航

- [产品与 P0 权威设计稿](webdb-design-draft.md)
- [文档总索引](docs/README.md)
- [架构决策记录](docs/adr/README.md)
- [P0 任务状态](docs/tasks/README.md)
- [P0-01 最终验收记录](docs/tasks/P0-01-project-skeleton-and-compose.md)
- [P0-03 最终验收记录](docs/tasks/P0-03-database-adapter-contract.md)
- [AI 协作规则](AGENTS.md)

项目采用 Apache License 2.0（ADR-012）；`LICENSE`、`NOTICE` 与第三方依赖清单 [DEPENDENCY-LICENSES.md](docs/DEPENDENCY-LICENSES.md) 已就位。依赖许可证的 CI 自动核查仍在 [P0-01-followup](docs/tasks/P0-01-followup-license-inventory.md) 跟踪中。

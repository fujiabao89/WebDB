# WebDB

WebDB 是面向团队的自托管数据库工作台。当前处于 P0 阶段，目标范围是 PostgreSQL/MySQL 连接、Schema 拉取、只读 SQL、服务端分页和追加式审计。P0-01 已完成工程骨架、Docker Compose 本地环境、健康检查和演示数据库只读权限基线；当前 API 只提供健康端点，尚未实现数据库工作台业务功能。

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

需要 Docker Engine 与 Docker Compose v2。先复制本地环境模板；模板仅含 `change_me` 占位值，实际 `.env` 不得提交。

```powershell
Copy-Item deploy/compose/env.example deploy/compose/.env
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

## 开发验证

```bash
# API
cd apps/api
go test ./...
go vet ./...

# Web
cd apps/web
npm ci
npm run typecheck
npm test
npm run build

# Contracts
cd packages/contracts
npm ci
npm run typecheck
npm test
```

CI 还会执行格式检查、仓库安全检查和 PR 契约检查。不要通过删除或跳过测试使 CI 通过。

## 文档导航

- [产品与 P0 权威设计稿](webdb-design-draft.md)
- [文档总索引](docs/README.md)
- [架构决策记录](docs/adr/README.md)
- [P0 任务状态](docs/tasks/README.md)
- [P0-01 最终验收记录](docs/tasks/P0-01-project-skeleton-and-compose.md)
- [AI 协作规则](AGENTS.md)

许可证与第三方依赖清单尚未完成，状态见 [P0-01-followup](docs/tasks/P0-01-followup-license-inventory.md)；在该后续任务完成前，不应宣称仓库已完成 Apache 2.0 发布材料。

# P0-01：工程骨架与 Docker Compose

> 状态：Done｜风险：Medium｜依赖：无｜实现者：Claude Code｜PR：[#2](https://github.com/fujiabao89/WebDB/pull/2)

## 目标与范围

建立 `apps/web`、`apps/api`、`packages/contracts`、`deploy/compose` 的可运行最小工程，以及元数据库、演示 PostgreSQL、演示 MySQL、API、Web 的 Compose 编排与健康检查。

不实现登录、协作、真实生产连接、DML/DDL 或业务功能。

## 验收标准

| 验收项 | 最终证据 | 结果 |
| --- | --- | --- |
| 一条命令启动最小本地环境 | `docker compose -f deploy/compose/docker-compose.yml up -d --build --wait`，5/5 服务健康 | 通过 |
| 各服务有明确端口、依赖、健康检查和不含真实密钥的示例配置 | `docker compose config` 通过；所有宿主端口仅绑定 `127.0.0.1` | 通过 |
| 演示 PG/MySQL 使用合成数据与受限账号 | `verify-readonly.sh`：TCP 密码认证及 PG/MySQL SELECT、DML、DDL 权限验证 13/13 通过 | 通过 |
| 前端、后端和契约目录有最小构建与测试入口 | GitHub Actions 的 Web、API、Contracts、Detect、PR contract、Repository safety 共 6 项检查通过 | 通过 |
| API 与 Web 健康端点可用 | API `/health` 返回 200；Web `/` 及 `/api/health` 返回 200 | 通过 |
| 停止、重启和日志安全满足要求 | 重启后 5/5 服务健康且合成数据保留；日志未发现密码或 KEK 泄露 | 通过 |

## 约束、依赖与升级

使用模块化单体（ADR-004）；不得提交真实凭证（ADR-006）。任何镜像、网络、端口或新增依赖取舍须记录。P0-02/P0-03/P0-04 可在目录和工具链就绪后并行。

## Done

P0-01 已于 2026-07-16 通过 PR #2 合并。

| 项目 | 最终值 |
| --- | --- |
| 最终提交 | `18c3144e51555a8a29af82d74569b7e8b2efc384` |
| 合并提交 | `4a257cf240c97d9ee314b64f1910928f9eae0062` |
| CI | 6/6 成功 |
| 独立审查 | Codex Review 未发现重大问题 |

### 部署与工具链取舍

- Go 版本由 `apps/api/go.mod` 管理，当前为 Go 1.26.0；CI 使用 `go-version-file`。
- Compose 的 API 使用 `golang:1.26-bookworm` `dev` target，保留本地调试所需 shell；生产 `prod` target 使用 distroless nonroot。
- Web 容器内 Vite 端口为 5173，宿主映射为 `127.0.0.1:3000:5173`；生产 target 使用 nginx。
- 演示 MySQL 使用 8.4 LTS 并固定镜像 digest；元数据库和演示 PostgreSQL 使用 PostgreSQL 16 Alpine。
- `LICENSE`、`NOTICE` 和第三方依赖许可证清单延期到 [P0-01-followup](P0-01-followup-license-inventory.md)，不视为已完成。

### 最终验证命令

```bash
docker compose -f deploy/compose/docker-compose.yml config
docker compose -f deploy/compose/docker-compose.yml up -d --build --wait
bash deploy/compose/verify-readonly.sh
curl http://127.0.0.1:8080/health
curl http://127.0.0.1:3000/api/health
```

完整原始结果、审查修复记录、停止/重启验证和回滚说明保留在 PR #2。

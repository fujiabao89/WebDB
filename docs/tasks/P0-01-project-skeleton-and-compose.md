# P0-01：工程骨架与 Docker Compose

> 状态：Ready｜风险：Medium｜依赖：无｜建议实现者：Claude Code

## 目标与范围

建立 `apps/web`、`apps/api`、`packages/contracts`、`deploy/compose` 的可运行最小工程，以及元数据库、演示 PostgreSQL、演示 MySQL、API、Web 的 Compose 编排与健康检查。

不实现登录、协作、真实生产连接、DML/DDL 或业务功能。

## 验收标准

| 验收项 | 证据 |
| --- | --- |
| 一条 `docker compose up -d` 启动最小本地环境 | Compose 集成测试与健康检查 |
| 各服务有明确端口、依赖、健康检查和不含密钥的示例配置 | `docker compose config` 与 README |
| 演示 PG/MySQL 使用仅限本地的合成数据与受限账号 | Compose/初始化脚本审查 |
| 前后端/API 契约目录有最小构建与测试入口 | CI 通过 |

## 约束、依赖与升级

使用模块化单体（ADR-004）；不得提交真实凭证（ADR-006）。任何镜像、网络、端口或新增依赖取舍须记录。P0-02/P0-03/P0-04 可在目录和工具链就绪后并行。

## Done

验证命令、服务日志脱敏检查、Compose 停止/重启行为和交接摘要均附 PR。

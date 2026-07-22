# WebDB P0 依赖许可证清单

> 状态：P0（Go/Docker 已完成，npm 包待补充）｜更新日期：2026-07-21
>
> 本项目采用 Apache License 2.0。以下列出 Go 后端和 Docker 镜像依赖，均与 AL2.0 兼容。npm 前端依赖未包含。

## Go 依赖（apps/api）

| 包 | 版本 | 许可证 | 类型 |
|---|---|---|---|
| github.com/google/uuid | v1.6.0 | BSD 3-Clause | direct |
| github.com/jackc/pgx/v5 | v5.10.0 | MIT | direct |
| github.com/pressly/goose/v3 | v3.27.2 | MIT | direct |
| github.com/go-sql-driver/mysql | v1.10.0 | MPL-2.0 | direct |
| filippo.io/edwards25519 | v1.2.0 | BSD 3-Clause | indirect |
| github.com/jackc/pgpassfile | v1.0.0 | MIT | indirect |
| github.com/jackc/pgservicefile | v0.0.0 | MIT | indirect |
| github.com/jackc/puddle/v2 | v2.2.2 | MIT | indirect |
| github.com/mfridman/interpolate | v0.0.2 | MIT | indirect |
| github.com/sethvargo/go-retry | v0.3.0 | Apache 2.0 | indirect |
| go.uber.org/multierr | v1.11.0 | MIT | indirect |
| golang.org/x/sync | v0.21.0 | BSD 3-Clause | indirect |
| golang.org/x/text | v0.37.0 | BSD 3-Clause | indirect |

## Docker 镜像

| 镜像 | 用途 | 许可证 |
|---|---|---|
| postgres:16-alpine | 元数据库/演示 PG | PostgreSQL License |
| mysql:8.4 | 演示 MySQL | GPLv2 |
| golang:1.26-bookworm | API 构建/dev | BSD-style |
| gcr.io/distroless/static-debian12:nonroot | API 生产 | Apache 2.0 |
| node:22-alpine | Web 构建/dev | MIT |
| nginx:1.28-alpine | Web 生产 | BSD-style |

## 合规说明

- Go 依赖均与 AL2.0 兼容（MIT/BSD/Apache 2.0）。
- PostgreSQL/nginx/node Docker 镜像均与 AL2.0 兼容。
- MySQL GPLv2 仅用于 Compose 演示容器，不嵌入 WebDB，不与项目代码链接。
- 无 AGPL、SSPL 或商业受限许可证。

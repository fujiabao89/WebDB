# WebDB P0 依赖许可证清单

> 状态：P0（已实现）｜更新日期：2026-07-21
>
> 本项目采用 Apache License 2.0。所有依赖均与 AL2.0 兼容。

## Go 依赖（apps/api）

| 包 | 版本 | 许可证 | 类型 |
|---|---|---|---|
| github.com/google/uuid | v1.6.0 | BSD 3-Clause | direct |
| github.com/jackc/pgx/v5 | v5.10.0 | MIT | direct |
| github.com/pressly/goose/v3 | v3.27.2 | MIT | direct |
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
| golang:1.26-bookworm | 构建/dev 运行 | BSD-style |
| gcr.io/distroless/static-debian12:nonroot | 生产运行 | Apache 2.0 |

## 合规说明

- 所有 Go 依赖许可证与项目 Apache License 2.0 兼容。
- PostgreSQL License 与 AL2.0 兼容（BSD 风格）。
- MySQL GPLv2 仅用于演示容器，不嵌入 WebDB。
- 无 AGPL、SSPL 或商业受限许可证。

# ADR-018：生产角色拆分脚本暂驻 deploy/compose 的有期限例外

> 状态：已接受（有期限例外）｜日期：2026-08-03｜Owner：WebDB Owner｜PR：[#37](https://github.com/fujiabao89/WebDB/pull/37)

## 背景

`README.md` 规定 `deploy/compose/` 仅用于本地开发与演示，不作为生产部署配置。WEB-27（R6）引入的生产角色拆分脚本（`init/prod-roles/01-create-prod-roles.sh`）与验证脚本（`verify-prod-roles.sh`、`test-prod-roles-cleanup.sh`）及对应文档目前位于 `deploy/compose/` 下。

PR #37 审查要求：将这些生产部署说明迁出 Compose 文档至仓库已批准的生产部署路径；若无法迁移，补充有期限的 Owner/ADR 例外并更新目录规则。

P0 阶段仓库尚无独立的"生产部署路径"（无 `deploy/prod` 等目录、无生产配置基线）。强行移动脚本会破坏现有引用、Compose 文档与验证流程，且没有可迁移的成熟目标位置。

## 决策

批准一个**有期限例外**：生产角色拆分脚本与文档在以下日期之前**暂驻 `deploy/compose/`**，届时必须完成迁移：

- **截止日期：2026-09-30**，或**独立生产部署路径建立时**（两者取先）。

例外期间必须遵守以下目录规则与边界：

- `deploy/compose/README.md` 的 `## 生产角色拆分（R6 / WEB-27）` 章节保留，但明确标注为"生产部署脚本例外（ADR-018）"，并保持 `WEBDB_PRODUCTION_DEPLOY=1` 显式确认门禁；不得把该章节当作本地 Compose 初始化配置。
- 脚本本身继续要求 `WEBDB_PRODUCTION_DEPLOY=1`，未设置即拒绝执行。
- 任何新增的"生产专用"配置（如独立生产 compose 文件、生产连接配置基线）应放到独立于 `deploy/compose/` 的路径，并更新本 ADR 的替代条件。

## 候选方案与取舍

- **迁移到独立生产路径**：放弃——P0 无目标路径，且当前脚本依赖 `deploy/compose` 的相对引用与验证流程；迁移会在无替代结构的情况下引入不完整的新目录。
- **移出 Compose 文档但保留脚本**：部分可行，但文档与脚本同处一处更利于运维按同一 PR 核对；且文档引用脚本的相对路径。
- **仅维持边界警告、不加 ADR**：放弃——审查要求正式化例外（时间限制 + Owner/ADR 记录），仅注释不足以满足。

## 后果

- 安全：Compose 文档同时描述本地开发与生产角色脚本，存在被误读为本地配置的风险；已用 `WEBDB_PRODUCTION_DEPLOY` 门禁 + 章节边界标注缓解，并有明确截止日期兜底。
- 运营：生产角色脚本与本地 Compose 同目录，运维需注意区分；截止前必须完成迁移。
- 兼容：无 API/Schema/数据影响；纯部署结构与文档变更。

## 验证与回滚/替代条件

- 验证：`docker compose config` 不执行 prod-roles 脚本；脚本在未设 `WEBDB_PRODUCTION_DEPLOY` 时拒绝执行；截止日前由 Owner 复核迁移进度。
- 回滚/替代：建立独立生产部署路径（`deploy/prod` 或等价目录）并迁移脚本与文档后，本 ADR 标记为"已替代"。**截止日（2026-09-30 或独立生产部署路径建立之日，取先）到期后必须完成迁移**；任何延期都必须在截止日前通过**新的 ADR** 批准，Owner 不得单独决定延期。

## 相关资料

- [README 部署边界说明](../../deploy/compose/README.md)
- [ADR-013：P0 元数据库迁移与 Schema 基线](ADR-013-p0-metadata-migrations-schema.md)

# P0-01-followup：依赖许可证清单

> 状态：Ready｜风险：Low｜依赖：P0-01｜Owner：fujiabao89｜建议实现者：管理员｜来源：PR #2 Codex Review #3572601443

## 目标

按照 ADR-012 要求，为 WebDB 项目建立第三方依赖许可证清单（dependency license inventory）。当前 `apps/web`、`apps/api`、`packages/contracts` 均引入了 npm/Go 依赖，但未记录许可证合规性。

## 范围

- 扫描所有 `node_modules` 和 Go 依赖的生产依赖许可证
- 更新 `LICENSE` 和 `NOTICE` 文件
- 创建 `docs/third-party-licenses.md` 清单

## 来源 Review Thread

- Codex Review ID: 3572601443（P2）
- PR: https://github.com/fujiabao89/WebDB/pull/2
- 延期依据：许可证合规是跨模块基础设施关注点，非 P0-01 独有。不阻塞 Compose 一键启动、只读安全边界或 P0-01 验收标准。

## 验收标准

| 验收项 | 证据 |
| --- | --- |
| 所有生产依赖的许可证已记录 | 清单文件 |
| 无 GPL/AGPL 等与 Apache 2.0 冲突的许可证 | 审查记录 |
| CI 可自动检查许可证合规 | CI job |

## Done

PR 中附带许可证清单和 CI 验证命令。

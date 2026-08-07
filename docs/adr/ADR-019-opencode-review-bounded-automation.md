# ADR-019：opencode 自动 PR 审查的受限自动化边界

> 状态：已接受｜日期：2026-08-07｜Owner：WebDB Owner｜PR：[#41](https://github.com/fujiabao89/WebDB/pull/41)

## 背景

WEB-32 引入 `opencode-review.yml`：当 `pull_request` 事件发生时，用 opencode agent 以 DeepSeek 模型对 PR 做自动代码审查并发布评论。相比纯只读的 CI，该 workflow 引入三类需要记录与把关的边界：

- **job 级 `pull-requests: write`**：用于向 PR 发布审查评论；
- **外部网络访问**：调用 DeepSeek API（`DEEPSEEK_API_KEY`）；
- **密钥处理**：`DEEPSEEK_API_KEY` 在 workflow 中的存储与暴露面。

按 `AGENTS.md`「必须升级人工的情况」（权限/身份策略、外部网络访问）与「任务协议 #4」（改动权限、部署、安全边界必须同步 ADR/设计/测试），本 ADR 记录该边界的决策与约束。

## 决策

1. **触发器与适用范围**：仅 `pull_request`（opened/synchronize/reopened/ready_for_review），在同仓库 PR 上运行。fork PR 不传递 secrets、且 `github.head_ref` 在 `origin` 无对应 ref，因此按设计**跳过**审查（`HAS_SECRET` guard + `Notice when review is skipped`），不将本 check 设为 required 门禁。本 job 只产出审查意见，不阻断合并。
2. **权限**：工作流级 `permissions: contents: read`；job 级 `contents: read` + `pull-requests: write`（发评论）。**禁止 `id-token`**（不使用 OIDC 换取 app token，而是 `use_github_token: true` 直接用最小 GITHUB_TOKEN）。
3. **密钥最小暴露**：`DEEPSEEK_API_KEY` 仅在 `Run OpenCode review` 单步 `env` 注入；job env 只保留 `HAS_SECRET: ${{ secrets.DEEPSEEK_API_KEY != '' }}` 布尔派生，不暴露 secret 值。guard 通过该布尔值中转（`if` 不能直接用 secrets context）。
4. **agent 能力边界**：`edit`/`write`/`webfetch` 一律 deny；`bash` 仅允许只读 git 子命令白名单（`log`/`diff`/`show`/`status`/`rev-parse`/`ls-files`）；`external_directory` 仅放行 `/tmp/**` 与 `$HOME/**`。agent 无法写仓库、无法访问外部网络。
5. **审查规则源可信**：审查会话前删除工作区全部指令文件 `AGENTS.md`/`CLAUDE.md`/`CONTEXT.md`（含子目录与 `.opencode/`），仅保留从 `base.sha` 拉取的可信根 `AGENTS.md`（写入工作区与 opencode 全局配置目录）；并设 `OPENCODE_DISABLE_PROJECT_CONFIG=true` 禁用项目级规则与 `.opencode/` 配置加载，阻断恶意 PR 的嵌套规则注入。
6. **回归测试**：`.github/scripts/test-opencode-workflow.sh` 静态断言 workflow 不变量（固定 SHA、权限边界、secret 暴露面、guard 等），并由 workflow 内 `Validate workflow invariants` 步骤与 `ci.yml` 独立 job 双重执行，降低对被测 workflow 自身的依赖。
7. **自校验边界**：对 `pull_request` 事件 GitHub 总是运行 PR 提供的 workflow 文件，merge-ref 自校验只防误改、**不是安全边界**；真正的控制是：本 check 不作为 required 门禁、最小 token、以及人工审查 workflow 文件的 diff。

## 候选方案与取舍

- **fork PR 也执行审查**：放弃——fork 不传递 secrets、`origin` 无 fork 分支 ref，且 token 只读无法发布评论；改为跳过并在 run 输出 `::notice::` 说明。
- **将 secret 放在 job env**：放弃——所有步骤（checkout/config/validate）都能读到该 secret；改为 Run 步骤单步 env + job env 仅布尔派生。
- **改用 `pull_request_target` 以获得 secrets**：放弃——`pull_request_target` 以 base workflow + 完整 secrets 运行，攻击面显著更大，非 P0 所需。
- **从工作区直接读取审查规则**：放弃——opencode action 内部会重新 checkout PR head 分支（`checkoutLocalBranch`），工作区修改会被丢弃；改为 base.sha 拉取 + 全局配置目录 + `OPENCODE_DISABLE_PROJECT_CONFIG`。

## 后果

- 安全：最小 token（contents: read + pull-requests: write）、secret 仅单步注入且经布尔中转、agent 只读、规则源固定为 base 可信版本；`OPENCODE_DISABLE_PROJECT_CONFIG` 关闭项目级配置注入面。
- 运营：fork PR 或 secret 缺失时 job 空转但有显式 notice；每次 PR 触发一次外部 API 调用（有少量成本与延迟）；`git fetch`/`git checkout` 依赖仓库公开可读（private 仓库需另行授予读取凭据，本 ADR 不覆盖）。
- 运营依赖：opencode action 对 `pull_request` 事件会校验触发者（`actor`）在仓库的写权限，无写权限的触发者（如自动化 bot 提交）会让 job 失败（历史 run 31166464070 即为 bot actor 触发失败）。本 workflow 由仓库 Owner/具写权限协作者触发时正常；这属于 action 外部行为，非本 workflow 可配置项。
- 兼容：无 API/Schema/数据影响；纯 CI/审查自动化变更。

## 验证与回滚/替代条件

- 验证：`test-opencode-workflow.sh` 在真实文件上通过、多个变异样例全部被拒绝；YAML 语法与 UTF-8 编码校验通过；真实 run 成功发布审查评论（如 run 31167460098、31170609542）。
- 回滚/替代：移除 workflow 文件即可恢复无自动审查状态；本 ADR 的边界调整（如放宽 agent 权限、改 secret 暴露面、接入 `pull_request_target`）必须先由新的 ADR 批准，不得静默修改。

## 相关资料

- [opencode-review.yml](../../.github/workflows/opencode-review.yml)
- [test-opencode-workflow.sh / validate-opencode-workflow.sh](../../.github/scripts/)
- [AGENTS.md「Agent Code Review 规则」](../../AGENTS.md)
- [opencode GitHub Action（固定 SHA `d7b115f6…`，release v1.18.15）](https://github.com/anomalyco/opencode/tree/d7b115f623760e68a4749d16508a9eca350f246f/github)

# TRAE Work 代码审查流程

## 目的与边界

本流程适用于 **TRAE Work 桌面版的 Code 模式**。TRAE Work 用于本地变更或
GitHub Pull Request 的辅助审查，但不会产生可加入 GitHub ruleset 的 required
status check，也不替代 GitHub CI、CodeRabbit 或独立 Reviewer。
TRAE Work 输出的文本 `APPROVE` 不是 GitHub 平台 approval。

| 配置 | 职责 | 权威性 |
| --- | --- | --- |
| `AGENTS.md` | WebDB 产品、安全、实施和审查政策 | 权威来源 |
| `.trae/rules/project_rules.md` | 引导兼容 TRAE 客户端加载权威规则 | 兼容层 |
| `.trae/skills/webdb-code-review/SKILL.md` | 可重复执行的本地/PR 审查步骤 | 执行流程 |
| GitHub CI 与 CodeRabbit | 自动检查和增量审查 | 合并门禁 |
| 独立 Reviewer | 语义审查和最终合并判断 | 人工责任 |

TRAE Work 审查默认只读。除非用户在审查结束后明确要求修复，否则不得修改代码、
提交、推送、评论、批准、解决对话或合并 PR。

## 一次性桌面端配置

1. 使用 TRAE Work **桌面版**，切换到左上角的 **Code 模式**。
2. 打开 **设置 → 规则 → 导入设置**。
3. 开启 **将 `AGENTS.md` 包含在上下文中**。
4. 新建 Code 任务，避免旧对话继续使用启用前的上下文。
5. 连接 GitHub 时，如果授权页提供 **Repository access**，选择
   **Only select repositories** 并只授权 `WebDB`；不要授权全部仓库。
6. 如需核验 Linear Task，通过 TRAE Work 用户侧的受支持 Connector/MCP 配置
   **只读**访问；凭证只保存在客户端，不得写入仓库或对话。
7. 本地审查选择预期工作目录；云端审查选择 GitHub 上的 `WebDB` 仓库。
8. 在项目 Skill 列表或 `/` 命令中确认 `webdb-code-review` 可用。

Web 版和移动版不能作为本流程的完整配置入口。需要读取本地 `AGENTS.md`、本地
分支或执行验证命令时，使用桌面版 Code 模式。

## 仓库配置

以下文件必须跟随仓库版本控制：

```text
AGENTS.md
.trae/rules/project_rules.md
.trae/skills/webdb-code-review/SKILL.md
docs/TRAE-WORK-CODE-REVIEW.md
```

`AGENTS.md` 是唯一权威审查政策。兼容规则和 Skill 只负责确保 TRAE Work 找到并
执行它，不复制完整政策。发生冲突时按 `AGENTS.md` 的优先级处理。

## 选择运行方式

### 本地任务

适合审查未推送分支或需要使用本机依赖的任务：

1. 在 TRAE Work 桌面版创建 **Code** 任务。
2. 运行环境选择 **本地（电脑）**。
3. 选择当前 Task 的独立 worktree，避免混入其他分支改动。
4. 确认当前分支和目标基线，随后调用 `webdb-code-review`。

### GitHub 云端任务

适合审查已推送的 PR：

1. 在 TRAE Work 创建 **Code** 任务。
2. 运行环境选择 **云端**。
3. 选择已授权的 `WebDB` 仓库。
4. 指定 PR 编号，要求读取 PR 的实际 base/head、Linear Task、完整 diff 和 CI。
5. 不提供 GitHub token、数据库凭证、`.env`、生产日志或真实数据。

TRAE Work 的 GitHub 集成用于读取仓库、运行 Code 任务和管理 PR，不代表它会像
CodeRabbit 一样自动监听每个 PR。每次审查都必须显式发起。

## 发起审查

优先在新 Code 任务中调用：

```text
使用 webdb-code-review 审查 WebDB PR #<number>。
先读取根目录和适用目录的 AGENTS.md、真实 Linear Task、相关 ADR/设计、
PR 的实际 base/head、完整 diff、测试、CI 和未解决审查对话。
本轮只输出审查意见，不修改文件，不提交、推送、评论、批准或合并。
```

如果审查本地分支，将第一行改为：

```text
使用 webdb-code-review 审查当前分支相对最新 origin/main 的完整差异。
```

TRAE Work 必须先确认 Task、审查范围和可读取的证据。Task 不存在、refs 无法确认、
无法通过只读连接直接核验 Task、高风险事实缺少证据或需要真实凭证时，结论必须为
`ESCALATE`；不得仅把 PR 正文中的 Task ID 当作存在性证据。

## 处理审查结果

1. 核对每条 finding 是否由当前 diff 引入。
2. 核对文件、行号、触发条件、影响和证据是否真实。
3. P0/P1 必须阻断；P2 在本 PR 修复或建立有 Owner 和期限的 Linear Task；P3
   不阻断。
4. 需要修复时另行明确授权，先写能复现问题的失败测试，再做最小改动。
5. 修复后使用新的 Code 任务重新运行完整审查，避免旧上下文偏向原结论。
6. 实现 Agent 不得担任自己 PR 的最终独立 Reviewer。

TRAE Work 的文字 `APPROVE` 不是 GitHub 平台批准。只有 GitHub CI、CodeRabbit、
对话解决和独立 Reviewer 等仓库门禁全部满足后，才可由有权限的人决定合并。

## PR 交接证据

在 PR 的“验证证据”或“AI 协作与交接”中记录：

```markdown
- TRAE Work 模式：Code / 本地或云端
- 审查目标：PR #<number> 或 `<head>...<base>`
- 审查时 HEAD：`<commit-sha>`
- 使用 Skill：`webdb-code-review`
- `AGENTS.md` 导入：已确认 / 未确认
- 审查结论：APPROVE / REQUEST CHANGES / ESCALATE
- Findings：P0 <n> / P1 <n> / P2 <n> / P3 <n>
- 已处理项：<对应提交、测试或 Linear Task>
- 未验证风险：<没有则写“无”>
```

不得伪造 TRAE Work 审查、命令、测试、CI、finding 数量或平台 approval。

## WEB-19 首次启用

`WEB-19` 是一次性启用 PR。若 TRAE Work 当前任务无法加载尚未合并的项目 Skill，
PR 必须记录“TRAE Work 未运行（首次启用）”，并继续依赖现有 GitHub CI、
CodeRabbit 和独立 Reviewer。该例外在 `WEB-19` 合并后失效。

## 故障排查

- **未读取 `AGENTS.md`：**确认使用桌面版；开启导入开关；新建 Code 任务。
- **未发现 Skill：**确认文件名严格为
  `.trae/skills/webdb-code-review/SKILL.md`，重新打开项目或新建任务。
- **只能看到 Work 模式文件：**切换到 Code 模式并重新选择本地或 GitHub 仓库。
- **模型卡在加载规则：**新建 Code 任务并切换受支持的内置模型；不要删除
  `AGENTS.md` 或弱化安全规则来绕过。
- **GitHub 操作不可用：**确认 GitHub 仅授权了 `WebDB`，并确认当前任务为 Code
  模式；不要在对话中粘贴访问令牌。
- **Linear Task 无法核验：**检查用户侧只读 Connector/MCP；不要把 Linear token
  写入 `.trae`、`.env`、PR 或对话。

## 回滚与维护

这些文件只影响 AI 协作流程，不改变运行时代码。若 Skill 无法被 TRAE Work 识别，
可在独立 Linear Task 中回滚 `.trae/skills/webdb-code-review`，并保留
`AGENTS.md`、GitHub CI 和 CodeRabbit。不得通过删除 WebDB 安全边界、关闭 CI 或
移除 CodeRabbit required check 来降低审查噪声。

# TRAE 代码审查流程

## 目的与边界

TRAE 用于提交 Pull Request 前的本地预审，帮助实现者在代码进入 GitHub
前发现当前 Task 引入的问题。它不替代 GitHub CI、CodeRabbit 或独立 Reviewer，
也不产生可加入 GitHub ruleset 的 required status check。

职责边界：

| 环节 | 主要职责 | 是否为 GitHub 合并门禁 |
| --- | --- | --- |
| TRAE | 本地分支差异预审、修改摘要、修复前自检 | 否 |
| GitHub CI | 构建、测试、静态检查和 PR contract | 是 |
| CodeRabbit | GitHub 上的独立增量审查与合并前检查 | 是 |
| 独立 Reviewer | 评估语义、风险、例外和最终合并决定 | 是 |

TRAE 审查默认只读。除非用户明确要求进入修复流程，否则不得修改文件、提交、
推送、创建 PR、批准或合并 PR，也不得更改 GitHub ruleset。

## 仓库配置

项目级规则位于：

```text
.trae/rules/project_rules.md
```

TRAE 审查前必须读取：

1. 当前 Linear Task、验收标准和非目标。
2. 根目录及相关子目录的 `AGENTS.md`。
3. 相关已接受 ADR 与 `webdb-design-draft.md` 章节。
4. `.github/coderabbit-review-guidelines.md`。
5. 当前分支相对 `origin/main` 的完整差异、相关测试和已运行的 CI 证据。

如果 TRAE 没有自动加载项目规则，应在 TRAE 的 AI Management → Rules 中确认
项目规则已启用，再开始审查。不得用个人规则覆盖仓库安全边界。

## 审查前准备

必须满足：

- 已有真实 Linear Issue，分支名符合
  `feat|fix|chore/WEB-<issue-number>-<slug>`。
- 一个分支只对应一个 Task。
- 已明确目标、非目标和验收标准。
- 已获取最新的远端基础分支引用。
- 本地没有会混入本次审查范围的无关修改。
- 未向 TRAE 提供真实凭证、生产数据、未脱敏日志或导出数据。

建议先执行只读检查：

```powershell
git status --short
git branch --show-current
git fetch origin main
git diff --stat origin/main...HEAD
git diff --check origin/main...HEAD
```

`git fetch` 会访问外部网络。按照 `AGENTS.md`，需要在已获人工许可并确认远端为
预期仓库后执行。不得在审查过程中连接生产数据库或读取真实密钥。

## 在 TRAE 中发起审查

1. 打开 TRAE 的智能代码审查入口。
2. 审查范围选择“分支间差异”。
3. 当前分支选择本次 Task 分支。
4. 目标分支选择 `origin/main`。
5. 模式选择“总结并审查”。
6. 确认项目规则 `.trae/rules/project_rules.md` 已加载。
7. 将当前 Task/Issue、验收标准和非目标作为本次审查上下文。
8. 开始审查，等待完整结果后再处理 findings。

只审查某次提交或未提交修改时，必须在审查记录中明确范围；这两种范围不能替代
创建 PR 前的完整分支差异审查。

## 审查结果处理

按 `.trae/rules/project_rules.md` 的证据格式核验每条 finding，不直接接受 AI
结论：

| 级别 | 处理要求 |
| --- | --- |
| P0 | 停止自动修复，阻断并升级人工 |
| P1 | 先复现，再做最小修复并补回归测试；无 Owner 例外不得进入 PR |
| P2 | 本 PR 修复，或创建有 Owner 和期限的后续 Linear Task |
| P3 | 不阻断；仅在明确有价值时处理 |

对于每条 finding：

1. 核对它是否由当前 diff 引入。
2. 核对文件、行号、触发条件、影响及引用的 Task/ADR 是否真实。
3. 行为修复遵循测试先行：先增加能证明问题的失败测试，再做最小实现。
4. 运行与风险匹配的格式化、静态检查、测试和构建。
5. 不通过删除、跳过或弱化测试、CI、权限或安全检查来消除 finding。
6. 证据不足、规则冲突或涉及人工批准事项时输出 `ESCALATE`，不要猜测。

完成修复后，重新对当前分支与最新 `origin/main` 执行一次“总结并审查”。最多进行
两轮自动修复；第二轮仍存在 P0/P1、同类失败连续两次或关键证据不足时，停止并升级
人工。

## PR 交接证据

TRAE 不产生 GitHub required check，因此不能只写“TRAE 已通过”。在 PR 的
“验证证据”或“AI 协作与交接”中记录：

```markdown
- TRAE 审查范围：当前分支 vs `origin/main`
- TRAE 审查模式：总结并审查
- 审查时 HEAD：`<commit-sha>`
- 审查结论：APPROVE / REQUEST CHANGES / ESCALATE
- Findings：P0 <n> / P1 <n> / P2 <n> / P3 <n>
- 已处理项：<对应提交或测试>
- 未验证风险：<没有则写“无”>
```

只有同时满足以下条件，才可创建或更新 Draft PR：

- TRAE 未留下未处理的 P0/P1。
- P2 已修复或已有符合 `AGENTS.md` 的后续 Task。
- 本地验证命令及真实结果已记录。
- PR 标题、分支名和正文使用同一个 Linear Issue ID。
- PR 正文完整填写 `.github/PULL_REQUEST_TEMPLATE.md`。

首次引入本规则的 `WEB-19` PR 是一次性启用例外：如果当前 TRAE 会话尚不能加载
未合并的项目规则，应在 PR 中如实记录“TRAE 未运行（首次启用）”，不得伪造
`APPROVE` 或 findings 数量。该 PR 仍必须通过现有 GitHub CI、CodeRabbit 和独立
Reviewer；例外在 `WEB-19` 合并后失效，后续 Task 不得沿用。

PR 创建后仍必须等待 GitHub CI、CodeRabbit 和独立 Reviewer；TRAE 的文本
`APPROVE` 不授权任何 Agent 在 GitHub 上批准或合并。

## 停止与升级人工

遇到下列情况立即停止：

- 生产连接、DML/DDL、数据导出/删除、权限提升或密钥/身份策略。
- 外部网络访问、真实凭证、真实用户数据、生产日志或未脱敏结果。
- 新增或改变 API、Schema、加密、SQL 解析器、连接池默认值、依赖许可证。
- SQL 无法可靠解析，或权限/架构规则互相冲突。
- P0 finding、同类失败连续两次、两轮修复后仍有 P1，或关键事实无法验证。

停止时记录已读资料、审查范围、当前 HEAD、已验证事实、失败尝试、剩余风险和下一条
安全命令，交由 Owner 或独立 Reviewer 决定。

## 回滚与维护

这两个文件只影响 TRAE 的项目级提示与协作流程，不改变运行时代码。若规则导致明显
误报，可在独立 Task 中回滚或最小化调整 `.trae/rules/project_rules.md`；不得通过
删除 P0 安全边界、关闭 GitHub CI 或移除 CodeRabbit required check 来降低噪声。

当 `AGENTS.md`、ADR、设计稿路径、PR contract 或 GitHub 门禁发生变化时，应同步
检查本流程中的引用是否仍然有效，避免复制内容长期漂移。

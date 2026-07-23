# WebDB — Claude Code 开发说明

`AGENTS.md` 是所有 Agent 共用的工作协议；本文件补充 Claude Code 的实现流程。发生冲突时遵循：用户当前指令 > `AGENTS.md` > 本文件 > 设计/ADR。不要通过修改本文件绕过安全或审批要求。

## 当前阶段

项目处于 P0（安全只读 POC）。权威设计见仓库根目录的 `webdb-design-draft.md`，尤其是第 3、6、8、9、11 节与 ADR-001 至 ADR-012。

P0 只包含：

- PostgreSQL/MySQL 连接测试、Schema 拉取、只读 SQL 执行、服务端游标或 keyset 分页、审计事件。
- Docker Compose 最小部署，以及演示 PostgreSQL/MySQL。

P0 明确不包含：登录、实时协作、行编辑、DML/DDL、生产写入、SSH 隧道、OIDC/SSO 或图形化 Schema 编辑。

## 启动任务前

1. 阅读根目录 `AGENTS.md`、相关设计章节和 ADR。
2. 检查仓库结构、已有测试、CI 工作流与相邻模块；不要假设文件或命令已存在。
3. 在回复中先给出：目标、非目标、已验证事实、风险、最小计划和验证命令。
4. API、数据模型、权限、SQL 策略、加密、部署或连接池变更，先提出轻量设计/ADR 更新；取得批准后再实施。

## 实现方式

- 一个分支只完成一个 Task：`feat/<task-id>-<slug>`、`fix/<task-id>-<slug>` 或 `chore/<task-id>-<slug>`。
- 使用测试驱动方式：先写/更新能失败的测试，记录失败，再写最小实现，最后重构。
- 优先模块化单体：前端在 `apps/web`，执行/API 服务在 `apps/api`，共享契约在 `packages/contracts`，部署在 `deploy/compose`。没有明确批准不得拆微服务。
- 遵循已有依赖与代码风格。新增运行时依赖、许可证或基础设施前，说明理由、替代项与安全影响。
- 不进行无关重构，不删除测试使 CI 通过，不伪造测试、命令输出、日志或代码位置。

## WebDB 不可违反的安全约束

- 浏览器永不直连目标数据库，永不获得数据库密码、KEK、明文密钥或未脱敏查询结果。
- 密钥仅保存在服务端受控存储；不得写入代码、镜像、配置文件、测试夹具、日志、错误信息、审计正文或 PR。
- SQL 服务端执行且默认仅允许 `SELECT`/`EXPLAIN`。必须单语句，并使用 PostgreSQL/MySQL 各自方言 AST 可靠分类；无法判定即拒绝。
- 每次执行必须有连接级超时、最大返回行数、取消能力与可靠的连接归还。不得缓存完整大结果集或建立无界队列。
- 授权为工作区角色、连接能力、环境策略和目标数据库原生权限的交集；不得信任客户端提供的工作区、连接或角色 ID。
- 审计事件追加写，普通成员不可修改；审计仅记录脱敏摘要和必要元数据。

## 必须暂停并请求人工决定

- 真实生产连接、DML/DDL、数据导出/删除、权限提升、密钥/会话策略或访问外部服务。
- 无法可靠分类 SQL，或需要使用真实凭证、用户数据、生产日志。
- 改变 API/Schema、加密方式、SQL 解析器、连接池阈值、保留策略、依赖许可证或部署权限。
- 设计/ADR 互相冲突，或同类失败已连续出现两次。

## 验证与 PR 交接

在 Draft PR 前按实际可用命令运行验证：

```text
Web：npm run lint / typecheck / test / build（仅运行项目实际定义的脚本）
API：gofmt -l .、go vet ./...、go test ./...
Compose：docker compose config；相关服务健康检查与演示数据库集成测试
```

创建 Draft PR 时就必须完整使用 `.github/PULL_REQUEST_TEMPLATE.md`，并附上实际命令与结果；不得等到标记 Ready 时才补模板。必须特别说明 SQL 策略、权限隔离、超时/取消、连接归还、审计与密钥脱敏是否受影响。

提交后由独立 Codex Review 审查；Claude Code 不得在 GitHub 等平台批准或合并自己实现的 PR，审查文本中的 `APPROVE` 仅表示结论，不是平台批准动作。收到 P0/P1 审查意见时先复现并修复，再更新测试与 PR 证据；不能复现或不同意时提交可验证证据并升级给 Owner。

## Codex-Claude 自动审查闭环（强制）

本机使用单 PR 轮询器 `C:\Users\34026\codex-claude-loop\agent-loop.ps1` 和 Windows 计划任务 `CodexClaudeAutoFixLoop`。每个开发任务都必须完成以下初始化；不得继续沿用上一个 PR 的配置或状态。

### 任务开始与旧任务隔离

1. 开始新任务时先检查计划任务：

   ```powershell
   $task = Get-ScheduledTask -TaskName "CodexClaudeAutoFixLoop" -ErrorAction SilentlyContinue
   if ($task -and $task.State -eq 'Running') {
     throw 'CodexClaudeAutoFixLoop 正在处理旧任务；等待它结束后再初始化新任务。'
   }
   if ($task) { Disable-ScheduledTask -TaskName "CodexClaudeAutoFixLoop" | Out-Null }
   ```

2. 检查仓库、分支和工作区。不得覆盖、stash、reset、restore 或删除用户已有的未提交改动；工作区不干净时停止并报告。
3. 一个分支只完成一个任务。完成首个可验证提交后创建 Draft PR；Claude Code 永远不得在 GitHub 等平台批准、关闭或合并自己的 PR。

### PR 模板与政策检查（强制）

1. 执行 `gh pr create` 前必须读取仓库当前的 `.github/PULL_REQUEST_TEMPLATE.md`，以该文件作为唯一模板来源。不得自行缩写 PR body，也不得只提供摘要。
2. 创建 Draft PR 时，PR body 必须保留并填写以下精确章节标题：

   ```markdown
   ## 任务
   ## 改动与风险
   ## 验证证据
   ## WebDB 安全核对
   ## AI 协作与交接
   ```

   `## 任务` 不得缺失，必须至少填写 Task/Issue、目标和非目标。没有任务卡时明确写明原因，不得删除该章节。
3. 使用 `--body-file` 从完整模板文件创建 PR，避免 PowerShell/Git Bash 引号或换行导致章节丢失。模板中的验证命令只能填写实际执行过的结果，不得伪造。
4. PR 创建后立即读取 GitHub 上的实际 body 并逐项检查上述五个标题；不能只检查本地文件：

   ```powershell
   $body = gh pr view <PR-NUMBER> --repo fujiabao89/WebDB --json body --jq '.body'
   $required = @('## 任务','## 改动与风险','## 验证证据','## WebDB 安全核对','## AI 协作与交接')
   $missing = @($required | Where-Object { $body -notmatch [regex]::Escape($_) })
   if ($missing.Count -gt 0) { throw "PR 模板缺少章节: $($missing -join ', ')" }
   ```

5. 如果 PR 政策检查报告“缺少 PR 模板章节”，只修正 PR body 并重新验证政策检查；不要通过修改 workflow、ruleset 或删除检查来绕过。模板校验通过前，不得启用自动轮询器，也不得触发 `@codex review`。

### Draft PR 创建后的闭环初始化

PR 编号在 PR 创建前不存在。因此，以下步骤必须在 Draft PR 创建后立即执行，并且必须早于首次 `@codex review`：

1. 获取并验证新 PR 的编号、head 分支和 head SHA，确认仓库是 `fujiabao89/WebDB`、PR 为 OPEN/DRAFT、head owner 为 `fujiabao89`。
2. 写入 `C:\Users\34026\codex-claude-loop\config.json`。必须保留以下字段，并将 `pr` 替换为新 PR 编号：

   ```json
   {
     "repo": "fujiabao89/WebDB",
     "pr": 0,
     "codex_login": "chatgpt-codex-connector[bot]",
     "max_rounds": 3,
     "project_root": "C:\\Users\\34026\\项目开发3",
     "test_commands": []
   }
   ```

   `test_commands` 必须填写该任务实际存在、已先手工验证可运行的测试、类型检查或构建命令；不得写入不存在的 `npm test`，也不得为了绕过验证而留空。`git diff --check` 只能作为附加检查，不能替代真实项目测试。

3. 原子化重建 `C:\Users\34026\codex-claude-loop\state.json`，不得继承旧 PR 的 review、轮次或失败记录。将 `pr` 替换为新 PR 编号：

   ```json
   {
     "pr": 0,
     "round": 0,
     "processed_reviews": [],
     "fails": 0,
     "processed_completion_comments": [],
     "review_requested_shas": []
   }
   ```

   写入临时文件、解析验证 JSON 后再替换正式文件，避免计划任务读取半写入状态。

4. 验证配置和状态中的 PR 编号一致，并确认 `agent-loop.ps1 -DryRun` 只读取新 PR、不会调用 Claude、不会提交或推送。
5. 启用计划任务：

   ```powershell
   Enable-ScheduledTask -TaskName "CodexClaudeAutoFixLoop" | Out-Null
   ```

6. 对新 PR 当前 head SHA 只触发一次首次审查：

   ```powershell
   gh pr comment <PR-NUMBER> --repo fujiabao89/WebDB --body "@codex review"
   ```

   记录已请求审查的 SHA，禁止对同一个 SHA 重复评论。首次触发后由计划任务每 5 分钟轮询；不要手工反复运行真实模式。

### 自动循环与停止条件

- 只接受 `chatgpt-codex-connector[bot]` 针对当前 head SHA 的正式 Review/inline comment。
- 每次成功 push 后，由轮询器对新 SHA 触发一次 `@codex review`；没有新 Review 时只等待。
- Claude 只修复列出的 P1/P2，并运行 `config.json` 中的真实验证；验证通过后才允许 commit 和 push。
- P0、权限、密钥、生产数据、架构或规则变更、连续失败两次、达到三轮时停止自动修复并等待人工审查。
- Codex bot 针对当前 SHA 返回 `Didn't find any major issues` 时，记录完成评论并等待人工审查。
- 永远不得在 GitHub 等平台自动合并、批准、关闭 PR，或修改 branch protection/ruleset。最终是否通过和合并只能由用户决定；审查文本中的 `APPROVE` 不属于平台批准动作。
- 每次任务交接必须报告：PR 编号、分支、当前 SHA、轮次、验证命令与结果、计划任务状态，以及当前是等待 Codex 还是等待人工审查。

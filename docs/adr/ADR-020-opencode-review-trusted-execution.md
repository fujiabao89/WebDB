# ADR-020：opencode 自动审查的受信执行架构（拟替代 ADR-019）

> 状态：提议（拟替代 ADR-019，接受后生效；未经 Owner 批准不视为已生效）｜日期：2026-08-07｜Owner：待 Owner 确认｜PR：[#41](https://github.com/fujiabao89/WebDB/pull/41)
>
> **拟替代关系**：本 ADR 提议替代 [ADR-019](ADR-019-opencode-review-bounded-automation.md) 中关于 agent 权限、secret/token 暴露、外部服务与执行架构的决策；需 Owner 明确批准后方可视为替代 ADR-019（届时 ADR-019 相应标记"已替代"）。ADR-019 在批准前仍为"已接受"。

## 待 Owner 决策

- **是否信任所有同仓库分支作者修改 workflow**：当前 `pull_request` 触发会执行 PR 提供的 workflow/脚本（含本次重写的 `opencode-review.yml` 与自检脚本）；同仓库 PR 可获得 secret（`DEEPSEEK_API_KEY`、`GITHUB_TOKEN`）；当前 `Validate workflow invariants` 自检与候选 workflow 同源，**不是安全边界**。若 Owner 不接受该信任假设，需另行设计从 base/default 分支执行的可信 harness；不得擅自选用 `pull_request_target`。
- 批准 ADR-020（转为"已接受"并标记 ADR-019 已替代）。
- 受信 Task 来源（Linear 凭据等）的接入授权。
- 独立门禁的 Owner、期限与后续 Task（见决策 8）。

## 背景

WEB-32 的 `opencode-review.yml` 原基于 `anomalyco/opencode/github` action。独立审查（opencode 自评、CodeRabbit）与本轮设计核对发现 P0/P1/P2 问题：

- **P0**：agent 的 bash 白名单含 `git diff*`/`git show*` 通配，可比较任意文件系统路径或用 `--output` 写文件；该 action 内部在"工作区脏"时会自动 `git add && git commit && git push`，且模型进程同时持有 `DEEPSEEK_API_KEY` 与 `GITHUB_TOKEN`。
- **P1**：action 仍查询 `releases/latest`、使用 `actions/cache@v4` 并执行 `curl | bash` 安装脚本，运行时随上游漂移；"固定 wrapper SHA"不等于"供应链已固定"。
- **P1**：静态校验器用"文本任意位置出现"式 grep，无法放行/拒绝 pull_request_target、uses 被替换、bash 带写副作用等变异。
- **P1**：模型被要求"读取 Task/验收标准/CI"，但无 Linear 连接、无法读 GitHub check rollup；Linear WEB-32 描述/评论为空，存在把 PR 正文/旧 bot 评论冒充验收证据的风险。
- **P2**：ci.yml 从 PR 可控的 `GITHUB_SHA` 同时读取测试脚本、校验器与候选 workflow，不是独立门禁。
- **P2**：未显式关闭 session 分享，公开评论含 OpenCode session 链接。
- **P2**：输出格式仅靠 prompt 约束，无程序校验。

## 决策

1. **agent 完全禁用 bash**；不再使用 `git diff*`/`git show*` 通配白名单。agent 仅通过 `read`/`glob`/`grep`/`list` 读取工作区与受信上下文；`edit`/`write`/`webfetch` 一律 deny。
2. **受信上下文准备**：模型启动前由受信 workflow 步骤生成只读上下文到 `$RUNNER_TEMP/review-context/`：base 的可信 `AGENTS.md`、完整 PR diff、PR 元数据、CI check rollup、证据限制说明。模型不持有 `GITHUB_TOKEN`，只能读这些文件与工作区。
3. **执行方式无自动提交路径**：弃用 `anomalyco/opencode/github` action 的 `github run` 子命令（其含 `branchIsDirty → git add && git commit && git push`）。改为直接调用固定版本的 `opencode run --no-share`，该子命令无自动 commit/push 逻辑。**已验证的 CLI 参数行为**：v1.18.15 的 `-f/--file` 为 array 选项，会贪婪吞并其后所有位置参数，故消息文本必须置于 `-f` 之前（`opencode run <消息> -f <文件>`）；`-f <文件> <消息>` 会把消息误当文件路径导致 `File not found: <消息>`（2026-08-08 在真实 runner 与本地 v1.18.15 均复现）。
4. **固定 CLI 运行时**：固定 `opencode-linux-x64.tar.gz` v1.18.15 并校验 sha256（`d842e0e8…`，已实际下载计算）。安装步骤下载固定 URL 压缩包 + `sha256sum -c` 校验，无 `curl | bash`、无 `latest`、无可变 tag。工作流内所有 action 只允许 `actions/checkout@11d5960a…`（完整 SHA）。
5. **模型与发布分离**：模型步骤只持 `DEEPSEEK_API_KEY`（不持 `GITHUB_TOKEN`）；输出写入 `$RUNNER_TEMP`。受信发布步骤持 `GITHUB_TOKEN`，只发布已通过格式校验的输出，不追加 footer/session 链接。
6. **Task/CI 证据 fail closed**：受信上下文提供真实 CI check rollup；Linear WEB-32 为空且本任务未获 Linear 凭据授权，审查输出须为 `ESCALATE`/证据不足，不得声称完成协议要求。新增 Linear 凭据/外部网络/身份策略属高风险，须先升级 Owner 并经新 ADR 批准。
7. **输出格式程序校验**：新增 `.github/scripts/validate-review-output.py` 对模型输出做确定性解析。**严格 finding 校验**：Findings `<details>` 块必需（含零 finding 场景）；声明数量 == heading 数 == 成功解析数；每条 heading 必须为 `[P0-3]` + 非空标题 + 含 `/` 的 repo 相对路径 + 正整数行号；每条 finding 必须有五个非空字段（事实/触发/影响/依据/最小修复），字段名存在但内容为空也拒绝，且不得靠其他段落出现字段名绕过；零 finding 时徽章说明必须含"本轮未发现由当前变更引入的可操作问题"（其他区域出现该短语不能替代）。**严格验收矩阵**：必须存在 `<details>` 块、表头、分隔行、至少一条数据行；每条数据行三个非空列，结果列 ∈ 通过/失败/证据不足；只有表头的空表拒绝。**details 结构严格扫描（状态机）**：`<details>`/`</details>` 标签必须独占一行且严格等于小写精确串（拒绝 `<details open>`、`<details >`、`<details class="x">`、`<DETAILS>`、`</details >` 及标签与其他文字同行）；opening/closing 各恰好 2 个、严格配对且不允许嵌套（多余/缺失 closing 拒绝）；两个顶层块依次为 Findings 与验收标准矩阵，每块第一个非空内容行必须是精确对应 `<summary>`；`<summary>` 只允许作为对应顶层块首行，未知/额外/块外 summary 一律 fail closed；块内容经状态机提取后交给 finding/矩阵语义解析，不做跨块全文 `re.search`。**完整文档布局**：状态机返回两块的 opening/closing 行范围，块外非空行必须恰好为徽章行（第一行）、置信度行（倒数第二个非空行）、结论行（最后一个非空行），其余块外非空内容（finding、Markdown 标题、表格、说明、HTML 等）一律拒绝；徽章与首个 `<details>` 之间、两个 `<details>` 之间、末个 `</details>` 与置信度之间、置信度与结论之间只能有空行。**徽章整行语法（fullmatch）**：第一行必须完整匹配 `**✅ APPROVE/❌ REQUEST CHANGES/⚠️ ESCALATE** — N 个 P0 / N 个 P1 / N 个 P2 / N 个 P3 待处理` 后可跟可选全角括号说明；P0-P3 各一次且顺序固定，不允许 P4/P10/未知级别/额外计数/任意前缀或后缀/括号结束后尾文；说明中不允许出现类似 P0-P9 的计数格式（防绕过）；计数必须与解析出的 findings 一致。**置信度完整行语法**：全文必须且只能存在一条独立的 `置信度：` 行，必须为倒数第二个非空行，且完整匹配 `置信度：高/中/低；仍未验证的风险：<非空内容>`；矩阵/finding 等区域出现关键词不能替代正式置信度行。**结论**：恰好一个独立 `结论：` 行、必须为最后一个非空行、与首行徽章和 `--required` 一致，不得有先前矛盾结论行。**required conclusion 程序约束**：受信上下文步骤计算 required conclusion，`CI_EVIDENCE_UNAVAILABLE` 或 `TASK_EVIDENCE_UNAVAILABLE` 存在时 required 必须为 ESCALATE，经 `--required` 传入校验器并拒绝不一致的徽章/结论（不依赖 prompt）。不合格最多重试 2 次，仍失败则 job fail closed。
7a. **CI/Task 证据 fail closed**：CI rollup 用结构化 JSON（`fetch-ci-rollup.sh`）。**退出码策略**：exit 0 + 合法非空数组（全 success）→ 可用；exit 8 + 合法含 pending 数组 → 可用（review job 自身 pending 不误判）；exit 1 + 合法数组且含 failure/cancelled 状态 → 可用，否则 fail closed；exit 2/4 及未知退出码，无论 stdout 是否合法 JSON → `CI_EVIDENCE_UNAVAILABLE`。**JSON schema 策略**：除 `json.load` 外须确认顶层为数组、元素为对象、含 name/state/description/link/bucket 字符串字段；`{}`/`null`/字符串/缺字段/非 JSON 均视为不可用；**空数组视为无可用 CI 证据**，写入 marker 并程序化强制 ESCALATE。无受信 Task 来源（未接入 Linear 等，未经 Owner 批准不得新增身份/网络方案）时写 `TASK_EVIDENCE_UNAVAILABLE`。任一 marker 存在 → required conclusion 即 ESCALATE——自动审查暂时不能输出 APPROVE，直到 Owner 批准可信 Task 获取方案（该限制未解决）。
7b. **session 分享关闭机制**：opencode 配置 `share` 字段仅接受 `manual|auto|disabled` 字符串（已从 core schema 确认，布尔值会使整个配置加载失败）。故在 opencode.json 设 `"share": "disabled"`（schema 合法值），并辅以模型步骤的 `--no-share`（run.ts 的分享条件依赖 `!args.share`）。`SHARE` 环境变量不被 `opencode run` 读取，不使用。
8. **回归测试为"非安全自检"，独立门禁为阻塞项**：本 PR 内 `test-opencode-workflow.sh`/`validate-opencode-workflow.sh` 随 PR 首次引入，base 无受保护版本，故 ci.yml 不设独立门禁（避免从 PR 可控来源取脚本造成假独立），仅由 workflow 内部 `Validate workflow invariants` 步骤作非安全自检。**此项为阻塞项**：目前无 Owner、期限与后续 Task 授权，**不得**声称"有期限例外"或"以后接入"；需 Owner 提供独立门禁的 Owner、期限与后续 Linear Task，方可安排从 base 取受信版本的门禁。

## 候选方案与取舍

- **继续使用 `anomalyco/opencode/github` action**：放弃——无法满足 P0（bash 全禁 + 模型不持 token + 无自动提交）与 P1（固定 CLI/无 curl|bash/无 latest），且该 action 的评论发布/自动提交行为不可拆分。
- **`pull_request_target` 实现 base 侧校验**：放弃（本 PR 内）——需新增替代 ADR + Owner 决策，且不 checkout 候选、不向候选提供 secrets、只经 API 喂给受信校验器；本 PR 未获该授权，故诚实保留为非安全自检并移除"独立边界"表述。
- **用 `opencode github run` 并依赖其现有权限断言**：放弃——该子命令在 actor 无写权限时失败、且存在自动 commit/push 路径，不符合 P0。
- **模型直接读工作区 git 历史**：放弃——bash 全禁后不可行；改由受信步骤经 GitHub API 生成 PR diff 供模型读取。

## 后果

- 安全：模型无法修改工作区、无法写仓库、无法发布评论（无 GITHUB_TOKEN）；secret 仅在模型步骤注入且不进入发布步骤；运行时固定到 v1.18.15 + sha256；session 分享关闭；输出经程序校验后才发布。
- 运营：模型通过外部 DeepSeek API 发送 prompt 与审查上下文（明文请求体；GitHub 会记录其提交的评论）。OpenCode session 分享已关闭，不再生成公开 session 链接；历史已发布的 session 链接需 Owner 审计/撤销，仅当发现真实密钥泄漏时才轮换对应长期密钥。
- 兼容：无 API/Schema/数据影响；纯 CI/审查自动化变更。`opencode run` 的可用性（无 TTY 下的输出捕获、配置加载）需在真实 runner 上验证。

## 验证与回滚/替代条件

- 验证：聚合入口 `test-opencode-review.sh`（非安全自检）依次运行 `test-opencode-workflow.sh`（20 项对抗样例）、`test-review-output.sh`（98 例：含 required-conclusion、严格 finding 字段/路径/行号校验、验收矩阵/结论结构、结论语义如 P0-P2+APPROVE 拒/矩阵失败或证据不足+APPROVE 拒/RC 需阻断依据、结构块恰好 1+1 与顺序、details 结构状态机扫描（属性/大小写/嵌套/多余 closing/同行/额外 summary 全拒）、完整文档布局（块外仅徽章/置信度/结论三行，块外 finding/标题/表格/说明全拒）、徽章整行 fullmatch（P4/P10/前缀/后缀/括号后尾文全拒）、置信度完整行（唯一、倒数第二非空行、级别高/中/低、风险非空））、`test-fetch-ci-rollup.sh`（16 例：退出码×JSON schema 矩阵、正向分步断言与参数解析）；`validate-opencode-workflow.sh` 通过；YAML/UTF-8 校验；`python -m py_compile`；`git diff --check`；actionlint/shellcheck（如环境可用）。
- 回滚/替代：移除本 workflow 或改回 reviewer-only 配置即可恢复无自动审查；本 ADR 的边界调整（放宽 agent 权限、恢复 session 分享、接入 `pull_request_target`、新增 Linear 凭据）必须先经新的 ADR 批准。

## 相关资料

- [opencode-review.yml](../../.github/workflows/opencode-review.yml)
- [validate-opencode-workflow.sh / test-opencode-workflow.sh / validate-review-output.py](../../.github/scripts/)
- [ADR-019：opencode 自动 PR 审查的受限自动化边界](ADR-019-opencode-review-bounded-automation.md)
- [opencode CLI v1.18.15 release](https://github.com/anomalyco/opencode/releases/tag/v1.18.15)

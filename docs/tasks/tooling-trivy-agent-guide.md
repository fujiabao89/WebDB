# Trivy 配置任务：MVP 发布前安全基线

> 状态：In Review｜Linear Task：`WEB-42`｜风险：High｜分支：`chore/WEB-42-trivy-baseline`｜独立审查：待未参与实现的安全 Reviewer

## 任务定位

本任务为 WebDB 建立 Trivy 发布前基线，覆盖当前 checkout、Dockerfile/配置、生产依赖许可证以及 API/Web 两个生产镜像。

首期只有 Secret 扫描立即阻断；漏洞、错误配置、许可证和镜像发现先报告并建立基线。扫描器自身运行失败必须失败，不能被“报告模式”吞掉。

## 当前仓库事实

- 现有 `.github/workflows/ci.yml` 没有 CVE、容器镜像、IaC、许可证或通用 Secret 扫描。
- `apps/api/Dockerfile` 和 `apps/web/Dockerfile` 都有 `prod` target；Compose 使用的是 `dev` target。发布扫描必须构建两个 `prod` 镜像。
- Compose 的 PostgreSQL 使用可变 tag，MySQL 使用 digest。第三方运行时镜像先在 main/定时任务报告，不让上游镜像变化无条件阻断普通 PR。
- `docs/DEPENDENCY-LICENSES.md` 尚未覆盖 npm；许可证扫描只能提供线索，不能替代 ADR-012 的人工兼容性决策。
- `ecc`、`superpowers` 是没有 `.gitmodules` 映射的 gitlink，不属于 WebDB 主仓库源码；CI 默认 checkout 不会取得其内容，扫描必须明确排除并记录原因。
- Trivy 的 Secret 扫描默认允许/忽略 Markdown；本仓库大量安全交接在 Markdown 中，因此必须显式扫描 Markdown。

## 启动前必须完成

1. 创建真实 Linear Task；一个分支和 PR 只完成本 Trivy 接入。
2. Owner 明确批准本任务的扫描策略、GitHub Action 引入和许可证处理边界。
3. 读取 `AGENTS.md`、ADR-012、`docs/DEPENDENCY-LICENSES.md`、`docs/tasks/P0-01-followup-license-inventory.md`、两个 Dockerfile和 Compose 文件。
4. 基于最新 `main` 创建独立 worktree/分支，保留当前工作区已有未提交内容，不混入本 PR。
5. 重新核对 Action 和 Trivy release 来源。2026-03 Trivy 生态发生过标签强推供应链事件，第三方 Action 必须固定完整 commit SHA，Trivy 二进制版本禁止使用 `latest`。

截至 2026-08-13 已核验的参考值：

- `aquasecurity/trivy-action` release `v0.36.0` 的 peeled commit：`ed142fd0673e97e23eac54620cfb913e5ce36c25`。
- Trivy：`v0.73.0`，GitHub release 标记为 immutable，发布时间 2026-08-03。

执行任务时必须重新从官方仓库核验；如来源、签名或版本状态无法确认，停止并 `ESCALATE`。

## 目标

- PR/current checkout：扫描 Secret、依赖漏洞、Dockerfile/配置错误和生产依赖许可证。
- 构建后：扫描 API 与 Web 的 `prod` 镜像，包括 OS/library 漏洞、Secret 和 image config。
- main/定时：报告 PostgreSQL/MySQL 等第三方运行时镜像状态。
- 对扫描范围、门禁阈值、基线和例外建立可审计规则。
- 提供本地复现命令和合成故障注入证据。

## 非目标

- 不扫描真实数据库、生产 Registry、运行主机或 Kubernetes。
- 不声称扫描了 Git 历史；`fs` 只覆盖当前 checkout。
- 不升级应用依赖、基础镜像或 GitHub Actions 来消除发现。
- 不修改 API、Schema、SQL 策略、密钥方案、审计或部署语义。
- 不把 Trivy 描述成自研 Go/TS 代码的 SAST；CodeQL负责代码数据流分析。
- 不自动覆盖许可证清单，不自行作出法律兼容性结论。
- 初期不上传 SARIF，不增加 `security-events: write`，不创建 PAT。
- 不使用 `pull_request_target`。

## 允许修改的文件

```text
.github/workflows/trivy.yml       # 新增，Trivy Agent 独占
trivy.yaml                        # 新增，通用配置
trivy-secret.yaml                 # 新增，Secret 规则/allow 规则
docs/tasks/<本任务文件>.md         # 仅本任务证据与交接；不得顺手改其他设计/ADR
```

禁止修改：

```text
.github/workflows/ci.yml
.github/workflows/pr-policy.yml
apps/web/package.json
apps/web/package-lock.json
apps/api/go.mod
apps/api/go.sum
apps/**/Dockerfile
deploy/compose/**
```

如果完成配置必须越过上述边界，停止并由 Owner 拆分新的修复 Task。

## 工作流设计

新增独立 `.github/workflows/trivy.yml`：

- 触发：`pull_request`、`push` 到 `main`、每周 schedule、`workflow_dispatch`。
- 权限：只允许 `contents: read`。
- 使用 `pull_request`，禁止 `pull_request_target`。
- 配置 `concurrency`，同分支旧运行 `cancel-in-progress: true`。
- 所有第三方 Action 固定官方完整 commit SHA，并在注释中标出 release 版本。
- `aquasecurity/trivy-action` 显式指定经过核验的 Trivy 版本，不使用 `latest`。
- 不向 Trivy Action 传 PAT、数据库密码、KEK 或仓库 Secret。
- 不修改共享 `ci.yml`，不把 CodeQL/Playwright 配成 `needs`。
- Secret 门禁的 GitHub Action `with:` 必须显式传入 `trivy-config: trivy.yaml`、`scan-type: fs` 和 `scan-ref: .`，不得依赖工作目录或隐式配置发现。`trivy-action` 不支持 `secret-config` input；`trivy.yaml` 必须通过 `secret.config: trivy-secret.yaml` 引用 Secret 配置（或显式设置等价的 `TRIVY_SECRET_CONFIG` 环境变量）。

### Job 1：`repo-secret-gate`

- 目标：当前 checkout。
- scanner：`secret`，所有 severity。
- 从第一天开始 `exit-code=1`。
- `trivy-secret.yaml` 必须关闭 Markdown 的默认 allow 规则，使任务文档和交接文档也被检查。
- 不自定义 `skip-patterns`；Trivy 自定义该字段会替换默认列表而不是合并。
- 明确排除 `ecc`、`superpowers` gitlink。
- Secret 原文报告不得上传 artifact、粘贴到 PR 或 Linear。

真实 Secret 命中后，先撤销/轮换凭证，再评估 Git 历史清理；不得用 ignore 让它变绿。阶段 A 对任何 finding 都不得创建 ignore/allow 例外；疑似规则误报应修正规则或 fixture 并升级 Owner。合成 canary 必须保持可检出，验证后只删除对应临时文件。阶段 B/C 的例外只能按后文另立管理 Task。

### Job 2：`repo-security-baseline`

拆成清晰步骤或矩阵，避免一个 exit code 混淆所有策略：

- Vulnerability：扫描 `apps/api/go.mod`、`apps/web/package-lock.json`、`packages/contracts/package-lock.json`，HIGH/CRITICAL 先报告。
- Misconfiguration：扫描 Dockerfile和受支持配置，先报告。多阶段 Dockerfile 的 builder/dev root 告警必须语义分诊，不能为了绿灯顺手改变容器行为。
- License：只评估生产依赖，先报告；先使用完整 SHA 固定的官方 `actions/setup-go` 按 `apps/api/go.mod` 配置 Go，再执行 `go -C apps/api mod download` 填充 module cache（禁止 `tidy`，禁止修改 `go.mod/go.sum`）。npm lockfile只读扫描，不为扫描修改 lockfile。验证结果中至少出现一个已知 Go 生产依赖和一个 npm 生产依赖，防止 false green。

报告模式使用 Trivy 的 findings exit code 0，但不得使用 `continue-on-error`、`|| true` 或捕获所有非零退出；下载失败、配置解析失败、目标不存在等工具错误必须使 Job 失败。

许可证发现规则：

- Forbidden、Restricted、Unknown：人工复核并升级 Owner。
- Reciprocal：人工复核，不自动阻断；不得因 MySQL GPL 等运行时镜像对 GPL 做全局 ignore。
- 扫描结果不得自动覆盖 `docs/DEPENDENCY-LICENSES.md`、`LICENSE` 或 `NOTICE`。

### Job 3：`prod-image-baseline`

分别构建并扫描：

```bash
docker build --target prod -t webdb-api:trivy-${GITHUB_SHA} apps/api
docker build --target prod -t webdb-web:trivy-${GITHUB_SHA} apps/web
```

要求：

- 两个镜像分别出具目标明确的扫描结果。
- 扫描 OS/library vulnerability、文件系统 Secret，并显式启用默认关闭的 image metadata/config scanners：`--image-config-scanners misconfig,secret`（或等价的 `image.image-config-scanners: [misconfig, secret]` 配置）。普通 `--scanners` 不能替代该开关。
- JSON/详细报告必须分别证明两个镜像存在 image-config Secret 扫描目标，以及由 image config 转换出的 Dockerfile/misconfiguration 扫描目标；只显示镜像名不足以验收。
- 初期 findings 只报告，扫描器错误仍失败。
- 不扫描 Compose 的 dev target 来冒充发布镜像。
- 不推送镜像，不登录 Registry。

### Job 4：`runtime-image-baseline`

仅在 `main` push、schedule 或手动运行时扫描 Compose 引用的 PostgreSQL/MySQL 等第三方镜像。初期只报告。每次 pull/scan 后把实际 `RepoDigest`/image ID 记录到 Job summary和交接，保证可重现；可变 PostgreSQL tag 的 pin/升级属于独立任务，不能让无代码变化的上游镜像直接阻断普通 PR。

## 配置与例外规则

`trivy.yaml` 至少明确：

- 有界 timeout。
- 扫描域排除 `ecc`、`superpowers`、构建产物和依赖缓存。
- `secret.config` 明确指向 `trivy-secret.yaml`；不得把不存在的 `secret-config` 当作 Action input。
- severity、format、ignorefile 和 scanner 行为不依赖隐式默认值。
- 不隐藏 UNKNOWN；由人工复核。
- `image.image-config-scanners` 明确包含 `misconfig`、`secret`；如果选择 CLI 参数实现，配置文档也要说明该必选参数。

阶段 A 禁止创建根 `.trivyignore`、`.trivyignore.yaml` 或任何 findings 豁免；当前只有 Secret findings 会阻断，其他类别先报告和分诊，因此不需要用 ignore 换取绿灯。真实 Secret 始终禁止 ignore。

如果阶段 B/C 确实需要有期限例外，必须另开 Owner 批准的管理 Task：优先缩小扫描 target，并为对应 Job 使用独立路径的 ignore 文件且显式传入 `--ignorefile`；每项记录精确 finding ID、Linear Issue、Owner、理由、到期日和前向修复计划。根文本 `.trivyignore` 按 ID 全局过滤，不能限制 package/path/PURL，不得用于安全门禁；实验性的 `.trivyignore.yaml` 只有在锁定 Trivy 版本并完成边界故障注入后才能另行评估。

## 分阶段门禁

| 阶段 | Secret | 漏洞 | Misconfiguration | License |
| --- | --- | --- | --- | --- |
| A：本 PR + 3～7 天基线 | 任意真实命中阻断 | 报告 | 报告 | 报告、人工复核 |
| B：MVP RC | 继续阻断 | `prod` repo/image 中有修复版本的 Critical 阻断 | 确认非误报后的 Critical 可阻断 | 继续人工 |
| C：MVP 后 | 继续阻断 | 可修复 High/Critical 阻断 | High/Critical 阻断 | Owner 批准分类后再门禁 |

Trivy 不提供可靠的“只阻断新增 finding”通用语义。历史发现无法立即清零时，只能建立逐项、有期限的基线，不能宣称已经实现新增问题门禁。

所有 required check/ruleset 修改必须在基线稳定后的独立管理任务中完成，本 Agent 不得修改分支保护。

## 验证与合成故障注入

实现 Agent 必须记录原始命令、exit code 和摘要。至少验证：

```bash
docker compose -f deploy/compose/docker-compose.yml config --quiet
docker build --target prod -t webdb-api:trivy-test apps/api
docker build --target prod -t webdb-web:trivy-test apps/web
trivy --version
trivy fs --config trivy.yaml .
trivy image --config trivy.yaml webdb-api:trivy-test
trivy image --config trivy.yaml webdb-web:trivy-test
```

命令参数可按最终配置拆分 scanner，但本地与 CI 必须使用同一版本、配置和门禁语义。

必须增加以下不入库的合成验证：

1. 在 worktree 内新建一个明确命名、未跟踪的临时目录，创建 Trivy 官方可识别的合成 Token fixture，并用与 CI 相同的仓库扫描命令确认 Secret gate 非零退出；随后只删除该精确临时文件和空目录。不得提交 fixture，不得用 `git clean` 批量清理，也不得把真实凭证写进公开 Git 历史。
2. 在临时 Markdown 中放入相同合成 fixture，证明 Markdown 实际被扫描。除本地 CLI 复现外，CI 中还必须使用同一 pinned Action和同一 `trivy-config`/`scan-type`/`scan-ref` inputs，并通过该 `trivy-config` 引用同一 Secret 配置，动态拼接一个不入库的合成 canary，断言 Markdown 命中时 exit 1、删除后 exit 0。canary 不得完整出现在 workflow 文件、日志或 artifact。
3. 使用不存在 target 或无效临时配置，证明报告模式下扫描器运行错误仍然失败。
4. 用临时 `FROM alpine:latest` Dockerfile证明 misconfiguration 路径被执行；不得为此修改生产 Dockerfile。
5. 核对 license 报告至少包含已知 Go 与 npm 生产依赖。
6. 核对两个镜像报告的 Target 分别是 API/Web `prod` 镜像，并分别出现 image-config Secret 与 converted-Dockerfile misconfiguration 扫描类型。
7. 断言本 PR 未创建或加载任何 ignore 文件；一旦发现 ignore，本阶段验收失败并拆到独立管理 Task。

PR 必须有一次真实 GitHub Actions 完整运行证据。Secret 原文、合成 fixture 内容和可能的敏感匹配不得上传 artifact。

## 验收标准

| 验收项 | 必须证据 |
| --- | --- |
| 独立 workflow，最小权限，无 `pull_request_target` | workflow diff |
| Action 固定完整官方 SHA，Trivy 版本固定 | release provenance + workflow |
| Secret 当前 checkout + Markdown 实际阻断 | 本地 + CI 同一 Action inputs 的合成负向验证 |
| Go、Web npm、Contracts npm 依赖均进入扫描域 | 报告 Target/覆盖断言 |
| API/Web 两个 `prod` 镜像及其 image config 均被扫描 | build/scan 原始结果与扫描类型断言 |
| findings 报告模式不吞扫描器错误 | 无效配置/目标负向验证 |
| 许可证仅报告并人工复核 | 扫描结果 + Linear 分诊 |
| 阶段 A 未创建或加载 findings ignore | diff + workflow/config 检查 |
| 未修改应用依赖、镜像和安全契约 | diff |
| 未上传 Secret 报告、未新增 PAT | workflow 与 artifact 检查 |

## WEB-42 实施证据（2026-08-13）

### 前置条件与来源核验

- Linear：`WEB-42` 已创建并记录 Owner 批准边界；分支为 `chore/WEB-42-trivy-baseline`，基线为 `upstream/main@ae123bfee36fa4cc229e4977b9d942180186ad6d`。
- `git ls-remote https://github.com/aquasecurity/trivy-action.git refs/tags/v0.36.0 refs/tags/v0.36.0^{}`：annotated tag `a9c7b0f06e461e9d4b4d1711f154ee024b8d7ab8`，peeled commit `ed142fd0673e97e23eac54620cfb913e5ce36c25`。
- GitHub release API：Trivy Action `v0.36.0` 和 Trivy `v0.73.0` 均为 immutable release；对应提交签名验证为 `verified=true / reason=valid`。Trivy release 发布于 `2026-08-03T10:47:14Z`，commit 为 `40c73e5d6166dcc0346a1ab4e94499d1572854e4`。
- 本地 Trivy archive `trivy_0.73.0_windows-64bit.zip`：release checksums 期望与实际 SHA-256 均为 `d2d3ad5292aae470a03eb6506db86fce81b1894592b8451cadaf60eaa22f2025`；`trivy --version` 输出 `Version: 0.73.0`，exit `0`。
- Workflow 额外使用的官方 Action：`actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1`（v7.0.1）、`actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e`（v7.0.0）。

### 本地命令、退出码与摘要

| 原始命令/验证 | Exit | 摘要 |
| --- | ---: | --- |
| 静态 workflow/config 契约脚本（必需文件、触发器、权限、完整 SHA、四个 job、Secret config、image config scanners、无 ignore input、无报告错误吞噬） | 先 `1`，实现后 `0` | 红灯为缺少三份 Trivy 文件；实现后 13 项断言全部 PASS |
| `actionlint .github/workflows/trivy.yml`（actionlint v1.7.12，release checksum 已核对） | `0` | `[]`，未发现 workflow 语法/表达式错误 |
| `docker compose -f deploy/compose/docker-compose.yml config --quiet` | `0` | Compose 静态配置有效 |
| `go -C apps/api mod download` | `0` | 填充 module cache；未执行 tidy，`go.mod/go.sum` 未改变 |
| `trivy fs --config trivy.yaml --scanners secret --severity UNKNOWN,LOW,MEDIUM,HIGH,CRITICAL --format table --exit-code 1 .` | `0` | 当前 checkout 无 Secret finding |
| 上述同一命令 + 未跟踪的普通文本与 Markdown 合成 canary | `1`（预期） | 两个目标各命中 `github-pat`；输出仅显示掩码；随后只删除两个精确临时文件和空目录 |
| 上述同一命令（canary 精确清理后） | `0` | 证明清理后恢复绿灯且未批量清理工作区 |
| `trivy fs --config trivy.yaml .` | `0` | 阶段 A 报告模式完成 vuln/misconfig/secret/license 全扫描，扫描器未报错 |
| 三个依赖入口分别执行 `trivy fs --config trivy.yaml --scanners license --format json --exit-code 0 <target>` | 均 `0` | ArtifactName 分别为 API go.mod、Web package-lock、Contracts package-lock；Go 14 个生产许可证记录（含 `github.com/google/uuid`），Web 7 个（含 `react`），Contracts 仅有 devDependency 因而生产记录为 0 |
| `trivy fs --config .trivy-config-that-does-not-exist.yaml --scanners vuln --exit-code 0 apps/api/go.mod` | `1`（预期） | 证明 findings 报告模式未把显式配置加载错误转换为成功 |
| 临时 `FROM alpine:latest` Dockerfile + `trivy fs --config trivy.yaml --scanners misconfig --format json --exit-code 0 <temp-dir>` | `0` | JSON 出现 `Dockerfile / config / dockerfile`，随后精确删除 fixture |
| `docker build --target prod -t webdb-api:trivy-test apps/api` | `0` | API `prod` target 构建成功 |
| `docker build --target prod -t webdb-web:trivy-test apps/web` | `0` | Web `prod` target 构建成功 |
| 两个 `prod` target 以动态合成 label（不推送）重新构建，再分别执行 `trivy image --config trivy.yaml --scanners vuln,secret,misconfig --format json --exit-code 0 <image>` | 均 `0` | API/Web ArtifactName 分离；每份报告均有 1 个 image-config Secret target 和 1 个 converted Dockerfile `config/dockerfile` target；JSON 位于临时目录且验证后删除 |
| `docker compose ... config --images` + 精确引用断言 | `0` | 确认 `postgres:16-alpine` 与 digest-pinned MySQL 均为当前 Compose 第三方运行时镜像 |
| GitHub Actions `Trivy security baseline` [run 31686191786](https://github.com/fujiabao89/WebDB/actions/runs/31686191786) | `success` | PR #51：Repository secret gate、repository vuln/config/license、API/Web prod image 三个 PR job 全部成功；runtime image job 按设计仅在 main/schedule/manual 运行，因此本次 PR event 为 skipped |
| GitHub Actions `PR policy` [run 31686494619](https://github.com/fujiabao89/WebDB/actions/runs/31686494619) | `success` | PR 标题、分支、Linear ID 与模板必填章节契约通过 |

### 阶段 A 发现与人工分诊

- Repository vulnerability：API `golang.org/x/text v0.37.0` 命中 1 个 HIGH（`CVE-2026-56852`，fixed `0.39.0`）；Web lockfile 当前没有 HIGH/CRITICAL；Contracts 当前无生产依赖。本任务按非目标不升级依赖。
- `prod` image vulnerability：API 1 HIGH；Web 2 CRITICAL（同一 `CVE-2026-31789` 分别影响 `libcrypto3`/`libssl3`）和 27 HIGH。阶段 A 仅报告；基础镜像/依赖升级须由独立修复 Task 处理。
- Misconfiguration：API `DS-0026` LOW；Web `DS-0002` HIGH、`DS-0013` MEDIUM、`DS-0026` LOW。多阶段 Dockerfile/运行时语义需人工判断，本任务未修改生产 Dockerfile。
- License：Go 13 个 Notice/LOW、1 个 Reciprocal/MEDIUM（`github.com/go-sql-driver/mysql`，MPL-2.0）；Web 7 个 Notice/LOW；无 Forbidden、Restricted 或 Unknown；Contracts 没有生产依赖。该结果只作为 ADR-012 人工复核线索，不构成法律结论，也未覆盖任何许可证清单。
- Ignore：未创建 `.trivyignore`/`.trivyignore.yaml`；`trivy.yaml` 以 `ignorefile: null` 显式禁用 findings ignore 加载；workflow 未传 `trivyignores` 或 `--ignorefile`。
- Secret 结果、合成 token 原文和 JSON 报告均未上传 artifact；workflow 未读取 repository secrets、未传 PAT、未申请 `security-events: write`。

### 待补证据与前向处理

- PR [#51](https://github.com/fujiabao89/WebDB/pull/51) 已转为 Ready；最新已完成的真实 Trivy PR run `31686191786` 结论为 `success`。独立安全语义审查仍未完成：CodeRabbit 在 Ready 后因 review limit 暂停约 115 分钟，Qodo 因试用结束暂停，GitHub `reviewDecision` 仍为 `REVIEW_REQUIRED`。实施 Agent 不得用自审替代该门槛，也不自行批准或合并。
- 已创建关联整改 Task `WEB-45`，由 Owner 在 `2026-08-18`（阶段 A 第 5 天）前复核漏洞/镜像/misconfiguration 基线；required check、阶段 B 门禁、依赖或基础镜像升级均在独立管理/修复 Task 中处理。
- 回滚仅删除 `.github/workflows/trivy.yml`、`trivy.yaml`、`trivy-secret.yaml` 及本节交接记录；如未来发现真实 Secret，必须先轮换/撤销凭证，删除扫描器不能恢复安全性。

## 独立审查要求

这是高风险安全配置 PR，必须由未参与实现的 Reviewer 进行“高强度工具链语义审查”，不能只验证 YAML 可解析。

Reviewer 必须读取 Task、完整 diff、`AGENTS.md`、ADR-012、威胁模型、Dockerfile、Compose、官方 Trivy 文档与供应链公告，并至少复跑版本核验、Secret 合成负向测试、两个 `prod` 镜像扫描和错误路径测试。

重点检查：

1. `pull_request_target`、过宽 Token、PAT、Secret artifact 或不可信输入执行。
2. Action SHA 是否来自官方 release 的 peeled commit，Trivy 二进制是否精确固定。
3. 是否真的扫描三个依赖入口、Go module cache、两个 `prod` 镜像及其 image config，而不是 dev 镜像或空目录。
4. 报告模式是否错误吞掉工具故障。
5. 本阶段是否创建或加载了任何越界 ignore；存在即要求拆分到独立管理 Task。
6. Markdown 与文档中的 Secret 是否在扫描范围。
7. License 结果是否被误当成法律结论，是否存在全局 GPL 豁免。
8. 是否越界修改依赖、镜像、API、Schema、SQL 或密钥策略。
9. 是否错误声称覆盖 Git 历史、自研代码语义或全部 Compose 安全属性。

以下情况至少 P1 阻断：真实 Secret、使用受污染/无法确认来源的 Action、扫描器错误被吞、未扫描发布镜像、上传 Secret 结果、无期限的宽泛 ignore。Action provenance、许可证兼容性或生产镜像范围无法安全确认时输出 `ESCALATE`。

Reviewer 最终按 `AGENTS.md` 输出 findings、验收矩阵及 `APPROVE / REQUEST CHANGES / ESCALATE`，附置信度和未验证风险。

## 回滚、交接与并行边界

- 回滚仅移除本任务新增的 Trivy workflow/config/ignore；不通过升级或降级业务依赖“回滚扫描”。
- 如果发现真实 Secret，回滚扫描器不能恢复安全性，必须先轮换/撤销凭证并升级人工。
- 交接记录扫描版本、Action SHA、扫描域、各 scanner 结果、例外、CI run、风险和下一阶段门禁日期。
- Trivy Agent 不修改 `.github/workflows/ci.yml`、Playwright 文件或 GitHub ruleset。
- Playwright 使用独立 workflow 并运行 Compose dev；Trivy 构建独立命名的 `prod` 镜像，避免容器/端口冲突。
- CodeQL 使用远端 Default Setup。Trivy 初期不上传 SARIF，避免与 CodeQL 权限和结果分类发生耦合。

## 官方参考

- [Trivy Action](https://github.com/aquasecurity/trivy-action)
- [Trivy 2026 供应链安全公告](https://github.com/aquasecurity/trivy/security/advisories/GHSA-69fq-xp46-6x23)
- [Secret scanner](https://trivy.dev/docs/latest/guide/scanner/secret/)
- [Misconfiguration scanner](https://trivy.dev/docs/latest/scanner/misconfiguration/)
- [License scanner](https://trivy.dev/docs/latest/scanner/license/)
- [Filtering and ignore files](https://trivy.dev/docs/dev/docs/configuration/filtering/)

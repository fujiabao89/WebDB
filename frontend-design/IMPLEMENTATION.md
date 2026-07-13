# Claude Code 实施说明

## 任务边界

本文件用于实现 `docs/tasks/P0-06-minimal-web-workbench.md`。它不批准新增 API、改变数据模型、引入新的运行时依赖，亦不批准任何 DML/DDL、导出、身份系统或外部服务。

## 建议组件结构

组件名可按仓库实际约定调整，但职责必须保持分离：

```text
WebDbWorkbench
├── TopBar
│   ├── ConnectionIdentity
│   ├── EnvironmentReadOnlyBadge
│   └── ApiHealthStatus
├── ConnectionSchemaRail
│   ├── AuthorizedConnectionList
│   ├── SchemaTree
│   └── SchemaRefreshStatus
├── QueryWorkspace
│   ├── QueryTabs
│   ├── ExecutionToolbar
│   ├── MonacoQueryEditor
│   └── QueryResults
│       ├── ExecutionSummary
│       ├── ResultsDataGrid
│       └── ServerPagination
├── ExecutionInspector
│   ├── PolicyDecision
│   ├── ExecutionMetadata
│   └── AuditTimeline
└── TrustFooter
```

页面容器负责编排；网络请求、分页 token、取消控制器和错误归一化放在 hooks/service 层，不塞进展示组件。不要在前端实现 SQL AST 判定。

## 中文界面与 locale 结构

- 第一版默认 `zh-CN`，所有用户可见的 UI 文案使用中文；SQL、对象名、ID、错误码和产品名保持原文。
- 即使 P0 暂不展示语言切换控件，也要把文案放入统一的 `zh-CN` message catalog，并让组件通过 message key 获取文案。
- 若仓库已有国际化方案则复用；若没有，先用无运行时依赖的类型化字典/上下文完成最小结构。不得未经批准新增 i18n 或字体依赖。
- 为 `en` 预留同一组稳定 message key。不要在组件内使用 `locale === "zh-CN" ? ... : ...`。
- locale 初始化和持久化仅属于 UI 偏好；不得上传数据库相关信息，也不得影响服务端审计事实。
- 切换语言时更新 `<html lang>`，并确保工具栏、状态徽标、分页和错误提示不溢出或遮挡。

示意结构（按项目实际约定调整）：

```text
src/i18n/
├── messages.zh-CN.ts   # P0 必须完整
├── messages.en.ts      # 可在启用英文切换时补齐
└── locale.ts           # locale、message key 与格式化边界
```

日期、时间、数字和金额显示使用 locale-aware formatter；API 传输值和 SQL 结果原值不得因界面语言被修改。

## 状态模型

查询执行至少建模为互斥状态：

```text
idle → submitting → running → succeeded
                    ├────────→ policy_rejected
                    ├────────→ timed_out
                    ├────────→ rate_limited
                    ├────────→ failed
                    └→ cancelling → cancelled | failed
```

- execution ID 一旦由服务端返回，就在后续成功/失败/取消状态中保留。
- 新运行必须使旧分页 token 失效；翻页不能重新执行或修改 SQL。
- 客户端断开/组件卸载时取消未完成的 HTTP 请求，但不能把本地 abort 冒充服务端已取消。
- 结果只保存当前页和必要的前后页 token，不缓存完整结果集。

## API 适配原则

以 `packages/contracts` 的真实契约为准；契约不存在时先停止并创建/批准契约任务，不在组件中发明 URL 或响应结构。

UI 需要的最小视图模型如下，仅用于组件边界，不是新增 API 提案：

```ts
type ConnectionSummary = {
  id: string;
  displayName: string;
  dialect: "postgresql" | "mysql";
  version?: string;
  environment: "production" | "staging" | "development";
  readOnly: boolean;
};

type ExecutionView = {
  executionId: string;
  traceId?: string;
  status:
    | "running"
    | "succeeded"
    | "policy_rejected"
    | "timed_out"
    | "cancelled"
    | "rate_limited"
    | "failed";
  durationMs?: number;
  returnedRows?: number;
  timeoutMs: number;
  maxRows: number;
  safeMessage?: string;
};

type ResultPage = {
  columns: Array<{ key: string; label: string; dataType?: string }>;
  rows: Array<Record<string, unknown>>;
  nextPageToken?: string;
  previousPageToken?: string;
  pageSize: number;
};
```

禁止字段：数据库密码、连接串、主机/端口、KEK、明文密钥、原始驱动错误、未脱敏审计 metadata。

## 实施顺序（测试先行）

1. 建立应用壳、中文 message catalog 与设计令牌，完成 `zh-CN` 的 1440×1024 布局快照/视觉测试。
2. 用合成 fixture 实现连接列表、Schema 树的加载/空/失败状态。
3. 接入 Monaco 和运行工具栏，覆盖键盘与按钮操作。
4. 接入服务端执行状态；覆盖策略拒绝、超时、取消、429 和脱敏失败。
5. 实现只保存当前页的服务端分页结果表。
6. 实现执行检查器和审计摘要。
7. 完成端到端主流程：连接 → Schema → 运行 → 分页 → 审计。

不要为了视觉稿先造一套临时生产 API。API 尚未就绪时，fixture 必须位于测试/Story 环境并清楚标记为合成数据。

## 建议稳定选择器

仅在确有测试需要时添加：

```text
workbench-shell
connection-list
schema-tree
query-editor
run-query
cancel-query
execution-status
results-grid
pagination-prev
pagination-next
execution-inspector
audit-timeline
```

选择器不能编码数据库 ID、用户 ID 或其他敏感值。

## 验收矩阵

| 验收项 | 自动化证据 |
| --- | --- |
| 与 A 版布局、色彩、密度一致 | 1440×1024 视觉回归；关键令牌单测 |
| 默认中文且可安全扩展英文 | `zh-CN` 文案完整性测试；message key 一致性；`lang` 断言 |
| 中英文混排不破坏布局 | 中文默认视觉回归；启用英文时补充英文视觉回归；长文案溢出测试 |
| Monaco 中文注释与英文 SQL 对齐 | 光标、选择、行号和全角字符浏览器测试 |
| 只显示授权连接/Schema | 组件测试 + API/E2E 越权响应测试 |
| 浏览器不出现数据库凭据/地址 | 网络响应与 DOM 断言 |
| Run/Cancel 状态可靠 | 组件测试 + E2E；重复运行、取消竞态 |
| 策略拒绝/超时/取消/429 可理解 | 每类服务端状态的可见文本断言 |
| 服务端分页且单页 ≤500 | 请求契约断言；DOM 仅含当前页 |
| 不存在导出、编辑、DML/DDL 入口 | DOM/可访问树负向断言 |
| 审计只展示脱敏摘要 | fixture 与 DOM 敏感字段负向断言 |
| 键盘和读屏可用 | axe/等价工具 + 键盘 E2E（仅使用仓库已批准工具） |

## 完成定义

- 实际可用的 lint、typecheck、test、build 全部通过并记录原始结果。
- 在支持的浏览器完成中文界面的 1440×1024 截图，与 `quiet-precision.png` 对照视觉语言而非逐字文本。
- 记录视觉回归环境实际使用的中文/英文字体；若 CI 与本机字体不同，不得直接接受大面积基线漂移。
- PR 中列出与 Stitch 参考的有意差异，尤其是删除导出和隐藏连接地址。
- SQL 策略、权限、API、Schema、审计保留策略和依赖许可证均未被前端任务擅自改变。

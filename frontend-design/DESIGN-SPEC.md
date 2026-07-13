# Quiet Precision 视觉与交互规范

## 设计原则

Quiet Precision 是一个安静、精确、高密度的工程工作台。界面应让用户一眼确认“连接到哪里、是否生产环境、是否只读、刚才执行了什么、服务端如何裁决”。层级主要依靠浅色表面、细边框、紧凑间距和排版建立，不使用营销式大卡片。

## 设计令牌

实现优先复用 `design-tokens.css` 中的 CSS 自定义属性。

### 色彩

| 用途 | 值 | 规则 |
| --- | --- | --- |
| 主操作/安全状态 | `#00685f` | Run、选中状态、健康与成功；不要大面积铺色 |
| 主操作悬停 | `#008378` | 仅交互反馈 |
| 主文字 | `#131b2e` | 标题、正文、数据 |
| 次文字 | `#3d4947` | 标签、辅助说明 |
| 页面表面 | `#faf8ff` | 应用背景 |
| 内容表面 | `#ffffff` | 编辑器、结果区、检查器 |
| 低层表面 | `#f2f3ff` | 左栏、工具栏、表头 |
| 中层表面 | `#eaedff` | 选中行、次级控件 |
| 边框 | `#bcc9c6` | 1px 分区线和控件边框 |
| 生产/拒绝 | `#ba1a1a` | 只用于生产危险、策略拒绝、失败 |
| 生产底色 | `#ffdad6` | 与错误文字配对 |
| 警告/超时 | `#924628` | 限制、超时、取消等非成功状态 |
| 成功底色 | `#d1fae5` | 策略通过、成功摘要 |

所有状态不得只靠颜色表达，必须同时提供文本和图标/形状。

### 字体与中英文混排

MVP 默认语言为简体中文。中文和英文字形在相同字号下会有明显视觉差异：中文更接近方形、笔画密度更高，英文更窄且大小写形成更强节奏。因此不能只把英文替换成中文而保持所有宽度和字距不变。

- UI 混排字体栈：`Inter`, `PingFang SC`, `Microsoft YaHei UI`, `Noto Sans CJK SC`, `Noto Sans SC`, system-ui, sans-serif。浏览器会优先用 Inter 渲染拉丁字母和数字，中文字符回退到平台中文字体。
- SQL 与数据字体栈：`JetBrains Mono`, `Cascadia Mono`, `SFMono-Regular`, `Noto Sans Mono CJK SC`, `Microsoft YaHei UI`, monospace。SQL 关键字、标识符和数值保持等宽；必须专门检查中文 SQL 注释的全角宽度、光标位置和选择范围。
- 中文正文：14px / 22px；紧凑辅助文字最低 12px / 18px。不要用 11px 承载关键中文信息。
- 英文正文：13–14px / 18–20px；数据与代码 12–13px / 18–20px。
- 中文分区标题：12px、600，不增加字距，不强制大写。
- 英文分区标题：11px、700、5% 字距，可使用大写。
- 中文按钮通常比英文按钮更紧凑，但状态说明可能更长；按钮使用 `min-width` 与水平 padding，不写死宽度。
- 页面内不使用大于 24px 的展示型标题。

不通过公共 CDN 加载字体。若要捆绑 Inter、Noto 或其他字体文件，必须先确认许可证、包体积、缓存策略和仓库依赖规则；未获批准时使用上述系统回退字体。视觉回归环境必须记录操作系统和实际加载字体，因为 Windows、macOS 与 Linux 的中文字形、字重和基线会有差异。

### 中文优先与语言切换

- 默认 locale：`zh-CN`；根元素使用 `lang="zh-CN"`。
- 架构上允许切换 `en`，切换后同步更新根元素 `lang`，但语言开关是否在 P0 可见由任务范围决定。
- 文案集中在 message catalog 中，禁止在 JSX 中散落中英文条件判断。
- 数据库对象名、SQL、execution/trace ID、错误码和产品名 `WebDB` 不翻译。
- 状态采用“中文说明 + 稳定错误码”，例如 `策略拒绝 · POLICY_REJECTED`，便于支持和审计定位。
- 禁止仅靠缩写节省中文空间。必要时优先增加可用宽度或安全截断，并保留完整的可访问名称。

关键文案基线：

| English reference | 默认中文 |
| --- | --- |
| Authorized connections | 已授权连接 |
| Production · Read only | 生产环境 · 只读 |
| API Healthy | API 正常 |
| Run / Cancel | 运行 / 取消 |
| Timeout / Max rows | 超时时间 / 最大行数 |
| Read-only policy passed | 服务端只读策略已通过 |
| Query results | 查询结果 |
| Execution details | 执行详情 |
| Audit log | 审计记录 |
| Previous / Next | 上一页 / 下一页 |
| No database credentials reach the browser | 数据库凭据不会进入浏览器 |

### 形状、间距与边界

- 4px 基础网格；常用间距 4/8/12/16/24px
- 控件圆角 2px，状态胶囊最大 8px；避免大圆角卡片
- 分区使用 1px 实线边框；不使用玻璃、渐变或厚重阴影
- 触控不是 P0 目标，但所有可点击控件可操作区域至少 28×28px
- 键盘焦点使用 2px `#00685f` 外轮廓，不能移除默认焦点而不替代

## 1440 × 1024 桌面布局

```text
┌──────────────────────────── Top bar 48px ─────────────────────────────┐
│ WebDB | connection/dialect | PRODUCTION · READ ONLY | API health      │
├──── Left 240px ────┬────────── Fluid workspace ──────────┬─ Right 280px ┤
│ connections        │ tabs + execution toolbar             │ execution   │
│ authorized schema  │ Monaco editor (about 50%)            │ details     │
│ tree               ├──────────────────────────────────────┤ audit facts │
│ refresh state      │ results summary + paginated grid     │             │
├────────────────────┴──────────────────────────────────────┴─────────────┤
│ Trust/status footer 32px                                               │
└─────────────────────────────────────────────────────────────────────────┘
```

- 顶栏和底栏固定；三个主列在其间填满可用高度。
- 左栏基准 240px，允许用户调整时限制在 220–360px。
- 右栏基准 280px，允许折叠；展开范围 280–400px。
- 中间区域最小宽度 720px。P0 不设计移动端；窄于 1120px 时允许应用级横向滚动，不把数据表压成卡片。
- 中文模式与英文模式必须共用同一布局骨架。工具栏和状态区使用弹性布局；不得为了适配单一语言写死文本容器宽度。
- Monaco 和结果区默认各占中间区域约一半高度，分隔条可调整时需保存为本地 UI 偏好，不进入审计。

## 区域规范

### 顶栏

- 左侧：WebDB、当前连接的显示名、数据库类型与版本。
- 中间/右侧：醒目的 `PRODUCTION · READ ONLY`，以及 API 健康状态。
- 不显示目标数据库地址、端口、用户名、密码、连接串或密钥。
- 环境徽标在所有页面状态下都不能被滚动隐藏。

### 连接与 Schema 树

- 只渲染 API 返回的授权连接和对象。
- 当前连接用浅表面底色、左侧主色标记和加粗文字共同表达。
- Schema、表和列有不同图标；支持展开/收起、加载、空、失败和刷新状态。
- 刷新必须显示进行中状态并防止重复触发；错误不可回显敏感连接信息。

### SQL 工作区

- 使用 Monaco，显示行号和 PostgreSQL/MySQL 对应语法模式。
- 工具栏固定包含 Run、Cancel、timeout、max rows 与服务端只读策略状态。
- Run 的可用性由连接/请求状态决定；前端不得自行声称 SQL “安全”。
- 运行中禁用重复 Run，并启用 Cancel；取消请求完成前显示 `Cancelling…`。
- 快捷键建议：`Ctrl/Cmd + Enter` 运行，`Esc` 取消；必须有可见按钮等价操作。

### 结果区

- 顶部显示执行状态、行数、耗时和 execution/trace ID。
- 表头固定；数据单行紧凑展示，数值右对齐，长值截断并提供安全的完整值查看方式。
- 分页由服务端 token/cursor 驱动；显示当前范围、上一页/下一页和 page size。
- page size 不得超过 500；不得在浏览器先获取全量结果再本地分页。
- P0 不提供导出、下载、行编辑、批量选择或图表入口。

### 执行检查器

- 显示服务端可公开的 execution ID、trace ID、actor 显示名、workspace、connection 显示名、方言、裁决、耗时和行数上限。
- 审计仅展示脱敏摘要；不展示 SQL 全文、绑定参数值、凭据或未脱敏结果。
- 事件按时间排序并使用成功、拒绝、超时、取消等明确标签。

### 底栏

- 固定显示 `Browser → WebDB API → restricted database account`。
- 中文默认显示 `数据库凭据不会进入浏览器`；英文模式显示 `No database credentials reach the browser`。

## 状态与错误

| 状态 | 必须展示 |
| --- | --- |
| 初始 | 编辑器可用；结果区给出运行提示 |
| 连接/Schema 加载 | 局部骨架或进度，不能冻结整个应用 |
| 运行中 | Run 禁用、Cancel 启用、已用时间可见 |
| 成功 | 绿色状态、行数、耗时、执行 ID、分页控件 |
| 策略拒绝 | 红色 `POLICY_REJECTED`、服务端安全说明、可修正建议；不回显内部解析细节 |
| 超时 | 琥珀色 `TIMED_OUT`、实际阈值、可重试入口 |
| 取消 | 中性/琥珀色 `CANCELLED`，保留 execution ID |
| 429 | `并发执行已达上限`、可重试时间（若服务端提供） |
| 普通失败 | 脱敏错误、trace ID、重试入口；禁止展示堆栈或连接串 |
| 空结果 | 成功状态与 `0 rows`，不是错误 |

## 可访问性

- 文本与背景至少满足 WCAG 2.1 AA 对比度。
- 使用语义化 `header/nav/main/aside/footer/table`；树使用正确的 tree/treeitem 状态。
- 所有图标按钮必须有可访问名称；状态变化使用 `aria-live="polite"`，严重错误使用 `role="alert"`。
- 页面语言必须与当前 locale 一致；中英文切换后更新 `document.documentElement.lang`。
- 完整键盘路径：连接 → Schema → 编辑器 → Run/Cancel → 结果分页 → 执行详情。
- 数据表提供列标题关联；不能仅靠 tooltip 暴露关键数据。

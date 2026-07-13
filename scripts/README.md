# WebDB Auto-Fix 脚本

Codex 审查 → Claude Code 自动修复 → 再审查 → 循环直到通过或转人工。

## 前置条件

1. **gh CLI 已登录** — `gh auth status`
2. **claude CLI 已安装** — `claude --version`（已确认 2.1.204）
3. 在仓库根目录 `C:\Users\34026\项目开发3` 下执行

## 快速开始

```bash
# 单次检查所有打开的 PR
./scripts/auto-fix.sh

# 只检查指定 PR
./scripts/auto-fix.sh 2

# 持续监视（每 5 分钟）
./scripts/auto-fix-watch.sh

# 自定义间隔（每 10 分钟）
./scripts/auto-fix-watch.sh 600
```

## 工作流程

```
Codex 审查 → auto-fix.sh 检测
  ├── P0 → needs-human-review + 通知 → 停止
  ├── P1/P2 → Claude 修复 → 推送 → 更新标签 → @codex review
  │   └── Codex 重审 → P1/P2 → 再次修复（最多 3 轮）
  │   └── Codex 重审 → 无问题 → human-review → 等待人工
  └── 无问题 → human-review（若之前修复过）
```

## 停止条件

| 条件 | 处理 |
|------|------|
| P0/CRITICAL | 立即停止，加 `needs-human-review` 标签 |
| 已完成 3 轮 | 加 `human-review` 标签，不再修复 |
| Codex 审查通过（👍） | 加 `human-review` 标签 |
| 审查无 P0/P1/P2 标记 | 标记已处理 |

## 状态追踪

- `.auto-fix/pr-<N>-last-review` — 记录已处理的最新 review ID
- PR 标签 `auto-fix-round-1/2/3` — 轮次追踪
- PR 标签 `human-review` / `needs-human-review` — 人工审查标记

## Claude Code 安全约束

修复时禁止：改架构/API/Schema、动密钥凭证、改 workflow、无关重构、合并 PR

## 设为 Windows 计划任务

```powershell
schtasks /create /tn "WebDB Auto-Fix" /tr "'C:\Program Files\Git\bin\bash.exe' -c 'cd /c/Users/34026/项目开发3 && bash scripts/auto-fix.sh'" /sc minute /mo 5
```

## 持久化日志

```bash
./scripts/auto-fix.sh 2>&1 | tee -a auto-fix.log
```

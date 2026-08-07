#!/usr/bin/env bash
# 静态断言 .github/workflows/opencode-review.yml 的安全不变量。
# 用法: validate-opencode-workflow.sh [workflow-file]
# 默认文件: .github/workflows/opencode-review.yml（相对本脚本所在目录）。
# 失败时退出码非 0；成功输出 OK。
set -euo pipefail

workflow="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/workflows/opencode-review.yml}"
failures=0

fail() {
  echo "FAIL: $1"
  failures=$((failures + 1))
}

# check <描述> <grep -F 模式> [file]
check() {
  local desc=$1 pattern=$2 file=${3:-$workflow}
  if ! grep -Fq -- "$pattern" "$file"; then
    fail "$desc（缺少：$pattern）"
  fi
}

# check_absent <描述> <grep -F 模式> [file]
check_absent() {
  local desc=$1 pattern=$2 file=${3:-$workflow}
  if grep -Fq -- "$pattern" "$file"; then
    fail "$desc（不应出现：$pattern）"
  fi
}

# --- 固定 SHA（P3-3 已在线核验归属） ---
check "opencode action 固定 SHA" 'anomalyco/opencode/github@d7b115f623760e68a4749d16508a9eca350f246f'
check "checkout 固定 SHA" 'actions/checkout@11d5960a326750d5838078e36cf38b85af677262'

# --- 权限边界 ---
check "use_github_token 开启" 'use_github_token: true'
check "job 权限含 contents: read" 'contents: read'
check_absent "job 权限不含 id-token" 'id-token'
check "agent edit deny" '"edit": "deny"'
check "agent write deny" '"write": "deny"'
check "agent webfetch deny" '"webfetch": "deny"'

# bash 白名单不得含写/状态变更 git 命令（仅允许只读子命令）。
if grep -E '"git [^"]*": "allow"' "$workflow" |
  grep -qE 'git (push|reset|checkout|clean|merge|rebase|add|commit|restore|switch|stash|clone|pull|fetch|rm|branch|tag|config|update-index|apply|am)'; then
  fail "agent bash 白名单含写/状态变更 git 命令"
fi

# --- secret 防护（P3-2）：raw secret 仅出现在 Run 步骤 env ---
if [ "$(grep -Fc 'DEEPSEEK_API_KEY: ${{ secrets.DEEPSEEK_API_KEY }}' "$workflow")" -ne 1 ]; then
  fail "raw secret 必须只在 Run 步骤 env 出现一次（P3-2）"
fi

# guard 存在：job env 布尔派生 + Run 步骤 if 引用（P2-3）。
check "secret 存在性 guard 存在" 'secrets.DEEPSEEK_API_KEY !='
check "Run 步骤 if 引用 HAS_SECRET" 'if: ${{ env.HAS_SECRET'

# --- P2-1 规则源保护 ---
check "禁用项目级配置 Flag" 'OPENCODE_DISABLE_PROJECT_CONFIG'
check "删除嵌套 AGENTS.md 命令" '-iname'
check "skip-worktree 隐藏规则变更" 'git update-index --skip-worktree'

# --- P3-1 base.sha 经 env 传递 ---
check "BASE_SHA 从 env 取用" 'BASE_SHA: ${{ github.event.pull_request.base.sha }}'
check_absent "run 脚本不得直插 base.sha 模板" 'ref=${{ github.event.pull_request.base.sha }}'

if (( failures > 0 )); then
  echo "opencode-review workflow 不变量校验失败：$failures 项"
  exit 1
fi

echo "OK: opencode-review workflow invariants satisfied"

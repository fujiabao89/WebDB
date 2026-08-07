#!/usr/bin/env bash
# 回归/对抗测试：校验 opencode-review.yml 的安全不变量，并证明校验器能拒绝被破坏的样例。
# 用法: test-opencode-workflow.sh [workflow-file]
# 真实文件通过 → 每个被破坏的样例都被拒绝 → 输出通过。
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
workflow="${1:-$script_dir/../workflows/opencode-review.yml}"
validator="$script_dir/validate-opencode-workflow.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

failures=0

# 1) 真实 workflow 必须通过全部断言。
if ! bash "$validator" "$workflow" >/dev/null; then
  echo "FAIL (期望通过): 真实 opencode-review.yml 不变量校验失败"
  failures=$((failures + 1))
fi

# 2) 对抗样例：每个变异都必须被校验器拒绝，证明断言真正生效。
expect_fail() { # expect_fail <名称> <sed 表达式>
  local name=$1 expr=$2
  local mutated="$tmp/mutated.yml"
  sed "$expr" "$workflow" > "$mutated"
  if bash "$validator" "$mutated" >/dev/null 2>&1; then
    echo "FAIL (期望拒绝): $name"
    failures=$((failures + 1))
  else
    echo "ok (被拒绝): $name"
  fi
}

# 去掉 opencode 固定 SHA → 应拒绝
expect_fail "去掉 opencode 固定 SHA" \
  's|anomalyco/opencode/github@d7b115f623760e68a4749d16508a9eca350f246f|anomalyco/opencode/github@0000000000000000000000000000000000000000|'
# 放开 edit 权限 → 应拒绝
expect_fail "放开 edit 权限" \
  's|"edit": "deny"|"edit": "allow"|'
# 允许 git push → 应拒绝
expect_fail "bash 白名单允许 git push" \
  's|"git log\*": "allow"|"git push*": "allow"|'
# secret 泄漏到 job env（raw secret 出现两次）→ 应拒绝
expect_fail "raw secret 泄漏到 job env" \
  's|HAS_SECRET:|DEEPSEEK_API_KEY: ${{ secrets.DEEPSEEK_API_KEY }}\n      HAS_SECRET:|'
# 删除项目级配置禁用 Flag → 应拒绝
expect_fail "删除 OPENCODE_DISABLE_PROJECT_CONFIG" \
  '/OPENCODE_DISABLE_PROJECT_CONFIG/d'
# 删除嵌套 AGENTS.md 清理命令 → 应拒绝
expect_fail "删除嵌套 AGENTS.md 清理命令" \
  '/-iname/d'
# base.sha 模板直插脚本 → 应拒绝
expect_fail "base.sha 模板直插脚本" \
  's|"\$GITHUB_API_URL/repos/\$GITHUB_REPOSITORY/contents/AGENTS.md?ref=\$BASE_SHA"|"$GITHUB_API_URL/repos/$GITHUB_REPOSITORY/contents/AGENTS.md?ref=${{ github.event.pull_request.base.sha }}"|'

if (( failures > 0 )); then
  echo "opencode workflow 静态断言测试失败：$failures 项"
  exit 1
fi

echo "opencode workflow 静态断言测试通过。"

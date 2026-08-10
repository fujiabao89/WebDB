#!/usr/bin/env bash
# 回归/对抗测试（finding 3 要求的 14 项 + 证据/CI/required/聚合约束额外项，共 20 项对抗样例）：
# 校验 opencode-review.yml 的不变量，并证明校验器能拒绝每个被破坏的样例（修复前失败、修复后通过）。
# 用法: test-opencode-workflow.sh [workflow-file]
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
workflow="${1:-$script_dir/../workflows/opencode-review.yml}"
validator="$script_dir/validate-opencode-workflow.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

failures=0

# 1) 真实 workflow 必须通过全部断言（含 YAML 结构）。
if ! bash "$validator" "$workflow" >/dev/null; then
  echo "FAIL (期望通过): 真实 opencode-review.yml 不变量校验失败"
  failures=$((failures + 1))
fi

# 1b) 合法配置的 YAML/UTF-8 必须通过（python yaml.safe_load + 无 BOM）。
PYTHON_BIN="${PYTHON_BIN:-}"
if [ -z "$PYTHON_BIN" ]; then
  for candidate in python3 python; do
    if command -v "$candidate" >/dev/null 2>&1 && "$candidate" -c 'import yaml, sys' >/dev/null 2>&1; then
      PYTHON_BIN=$candidate
      break
    fi
  done
fi
if [ -n "$PYTHON_BIN" ] && ! "$PYTHON_BIN" -c "
import yaml,sys
yaml.safe_load(open(sys.argv[1], encoding='utf-8'))
print('yaml ok')
" "$workflow" >/dev/null 2>&1; then
  echo "FAIL (期望通过): workflow YAML 解析失败"
  failures=$((failures + 1))
fi
if [ "$(head -c 3 "$workflow" | od -An -tx1 | tr -d ' \n')" = "efbbbf" ]; then
  echo "FAIL (期望通过): workflow 含 UTF-8 BOM"
  failures=$((failures + 1))
fi

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

# 2) pull_request_target 被拒绝
expect_fail "pull_request 改为 pull_request_target" \
  's|^  pull_request:|  pull_request_target:|'
# 3) 实际 uses 被替换、正确 SHA 仅在注释中
expect_fail "checkout uses 替换为 v4、SHA 仅在注释" \
  's|uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262|uses: actions/checkout@v4 # 原固定 11d5960a326750d5838078e36cf38b85af677262|'
# 4) 任意 workflow/job 权限扩大
expect_fail "workflow 级新增 pull-requests: write" \
  '0,/^permissions:/s|^permissions:|permissions:\n  pull-requests: write|'
# 5) id-token / 额外 write scope
expect_fail "job 级新增 id-token: write" \
  's|      pull-requests: write|      pull-requests: write\n      id-token: write|'
# 6) raw secret 出现在非预期步骤（在受信步骤 env 注入 DEEPSEEK_API_KEY）
expect_fail "raw secret 泄漏到受信步骤" \
  's|          GH_TOKEN: \${{ github.token }}|          GH_TOKEN: ${{ github.token }}\n          DEEPSEEK_API_KEY: ${{ secrets.DEEPSEEK_API_KEY }}|'
# 7) bash 不是 deny
expect_fail "bash 改为 allow" \
  's|"bash": "deny"|"bash": "allow"|'
# 8) edit/write/webfetch 不是 deny
expect_fail "write 改为 allow" \
  's|"write": "deny"|"write": "allow"|'
# 9) share 未显式 false
expect_fail "关闭 session 分享被移除（--no-share → --share）" \
  's|--no-share|--share|'
expect_fail "配置新增 share 键（可能使整个配置加载失败）" \
  's|"external_directory"|"share": false,\n              "external_directory"|'
# 10) latest / 可变 tag / 未固定 nested action / 无 checksum 安装
expect_fail "checkout 改用 @latest" \
  's|actions/checkout@11d5960a326750d5838078e36cf38b85af677262|actions/checkout@latest|'
expect_fail "CLI 校验和改为零值（无校验）" \
  's|d842e0e8c622c672a481b7dc6f0329009b64db96b2ba6041e56f4f93f0293b1c|0000000000000000000000000000000000000000000000000000000000000000|'
expect_fail "引入未固定 nested action actions/cache" \
  's|      - name: Validate workflow invariants|      - uses: actions/cache@v4\n      - name: Validate workflow invariants|'
# 11) 候选 workflow/脚本被当作可执行 harness；Validate 步骤必须调用聚合入口
expect_fail "Validate 步骤不调用聚合入口（改为直接调用子测试）" \
  's|test-opencode-review.sh|test-opencode-workflow.sh|'
# 11b) 聚合脚本删除任一子测试调用 → 静态校验须失败（经 OPENCODE_REVIEW_AGGREGATE 传变异副本）
mutated_agg="$tmp/agg-mutated.sh"
sed '/test-review-output.sh/d' "$script_dir/test-opencode-review.sh" > "$mutated_agg"
if OPENCODE_REVIEW_AGGREGATE="$mutated_agg" bash "$validator" "$workflow" >/dev/null 2>&1; then
  echo "FAIL (期望拒绝): 聚合脚本删除 test-review-output.sh 调用"; failures=$((failures+1))
else
  echo "ok (被拒绝): 聚合脚本删除 test-review-output.sh 调用"
fi
# 12) Task/CI 证据缺失时输出 ESCALATE/证据不足
expect_fail "上下文步骤删除 ESCALATE 提示" \
  '/ESCALATE/d'
# 12b) 删除 TASK_EVIDENCE_UNAVAILABLE → 应拒绝
expect_fail "删除 TASK_EVIDENCE_UNAVAILABLE" \
  '/TASK_EVIDENCE_UNAVAILABLE/d'
# 12c) 删除 fetch-ci-rollup 调用 → 应拒绝
expect_fail "删除 fetch-ci-rollup 调用" \
  '/fetch-ci-rollup.sh/d'
# 12d) 移除校验器 --required 程序约束（不再从 required-conclusion.txt 读取）→ 应拒绝
expect_fail "移除校验器 --required 程序约束" \
  's|--required \$(cat "\$ctx/required-conclusion.txt")|--required|'
# 13) 模型输出格式错误时重试并最终 fail closed
expect_fail "移除模型输出重试循环" \
  '/for attempt in 1 2 3/d'
expect_fail "移除模型输出 fail closed" \
  '/fail closed/d'
# 14) 合法配置与真实 workflow YAML 均通过（1/1b 已覆盖）。

if (( failures > 0 )); then
  echo "opencode workflow 静态断言测试失败：$failures 项"
  exit 1
fi

echo "opencode workflow 静态断言测试通过。"

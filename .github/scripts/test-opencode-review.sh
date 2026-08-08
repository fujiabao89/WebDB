#!/usr/bin/env bash
# 聚合测试入口（finding 4）：依次运行 opencode-review workflow 相关的全部确定性测试。
# 由 workflow 的 Validate workflow invariants 步骤调用。
# 【非安全自检】候选-controlled：脚本随 PR 提供、与候选 workflow 同源，只防良性漂移，
# 不是独立安全门禁；独立门禁的 Owner/期限/后续 Task 仍为阻塞项（见 ADR-020 决策 8）。
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
failures=0

run_suite() {
  local name=$1 cmd=$2
  echo "=== $name ==="
  if bash "$script_dir/$cmd" >/dev/null 2>&1; then
    echo "ok: $name"
  else
    echo "FAIL: $name"
    failures=$((failures + 1))
  fi
}

run_suite "test-opencode-workflow.sh" "test-opencode-workflow.sh"
run_suite "test-review-output.sh" "test-review-output.sh"
run_suite "test-fetch-ci-rollup.sh" "test-fetch-ci-rollup.sh"

if (( failures > 0 )); then
  echo "opencode-review 聚合测试失败：$failures 项"
  exit 1
fi
echo "opencode-review 聚合测试通过。"

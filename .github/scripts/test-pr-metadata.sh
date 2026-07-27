#!/usr/bin/env bash

set -euo pipefail

validator="$(dirname "$0")/validate-pr-metadata.sh"
failures=0

expect_pass() {
  local name=$1
  shift
  if ! bash "$validator" "$@" >/dev/null; then
    echo "FAIL (expected pass): $name"
    failures=$((failures + 1))
  fi
}

expect_fail() {
  local name=$1
  shift
  if bash "$validator" "$@" >/dev/null 2>&1; then
    echo "FAIL (expected rejection): $name"
    failures=$((failures + 1))
  fi
}

expect_pass \
  "Linear title, branch and body match" \
  "[WEB-14] P0-04 parser candidate spike" \
  "feat/WEB-14-P0-04-parser-spike" \
  "## 任务
- Task / Issue：WEB-14（P0-04）"

expect_pass \
  "Conventional Commit text remains optional after Linear prefix" \
  "[WEB-7] docs(p0-03): update adapter documentation" \
  "chore/WEB-7-P0-03-documentation-update" \
  "## 任务
- Task / Issue：WEB-7"

expect_fail \
  "old Conventional-only title" \
  "feat(api): add read-only query policy" \
  "feat/WEB-13-P0-04-sql-safety-policy" \
  "Task / Issue: WEB-13"

expect_fail \
  "branch without Linear ID" \
  "[WEB-13] P0-04 SQL safety policy" \
  "feat/P0-04-sql-safety-policy" \
  "Task / Issue: WEB-13"

expect_fail \
  "title and branch IDs differ" \
  "[WEB-13] P0-04 SQL safety policy" \
  "feat/WEB-14-P0-04-parser-spike" \
  "Task / Issue: WEB-13"

expect_fail \
  "body does not contain matching ID" \
  "[WEB-13] P0-04 SQL safety policy" \
  "feat/WEB-13-P0-04-sql-safety-policy" \
  "Task / Issue: P0-04"

expect_fail \
  "matching ID appears outside Task / Issue" \
  "[WEB-13] P0-04 SQL safety policy" \
  "feat/WEB-13-P0-04-sql-safety-policy" \
  "## 任务
- Task / Issue：P0-04
- Related to WEB-13"

expect_fail \
  "invalid zero issue number" \
  "[WEB-0] repository policy" \
  "chore/WEB-0-repository-policy" \
  "Task / Issue: WEB-0"

expect_fail \
  "unsupported branch prefix" \
  "[WEB-7] documentation update" \
  "docs/WEB-7-documentation-update" \
  "Task / Issue: WEB-7"

if (( failures > 0 )); then
  exit 1
fi

echo "PR metadata policy tests passed."

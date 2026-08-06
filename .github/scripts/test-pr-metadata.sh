#!/usr/bin/env bash

set -euo pipefail

validator="${PR_POLICY_VALIDATOR:-$(dirname "$0")/validate-pr-metadata.sh}"
failures=0
required_sections_suffix='
## 改动与风险
## 验证证据
## WebDB 安全核对
## AI 协作与交接'

expect_pass() {
  local name=$1
  local title=$2
  local head_ref=$3
  local body=$4
  if ! bash "$validator" "$title" "$head_ref" "${body}${required_sections_suffix}" >/dev/null; then
    echo "FAIL (expected pass): $name"
    failures=$((failures + 1))
  fi
}

expect_fail() {
  local name=$1
  local title=$2
  local head_ref=$3
  local body=$4
  if bash "$validator" "$title" "$head_ref" "${body}${required_sections_suffix}" >/dev/null 2>&1; then
    echo "FAIL (expected rejection): $name"
    failures=$((failures + 1))
  fi
}

expect_fail_raw() {
  local name=$1
  local title=$2
  local head_ref=$3
  local body=$4
  if bash "$validator" "$title" "$head_ref" "$body" >/dev/null 2>&1; then
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

expect_pass \
  "fenced examples do not override the canonical task field" \
  "[WEB-13] P0-04 SQL safety policy" \
  "feat/WEB-13-P0-04-sql-safety-policy" \
  "## 任务
- Task / Issue：WEB-13（P0-04）

## 验证证据

\`\`\`text
- Task / Issue：WEB-14
\`\`\`"

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
  "matching ID in fenced code cannot mask a mismatched task field" \
  "[WEB-13] P0-04 SQL safety policy" \
  "feat/WEB-13-P0-04-sql-safety-policy" \
  "## 任务
- Task / Issue：WEB-14

## 验证证据

\`\`\`text
- Task / Issue：WEB-13
\`\`\`"

expect_fail \
  "matching ID in an HTML comment cannot mask a mismatched task field" \
  "[WEB-13] P0-04 SQL safety policy" \
  "feat/WEB-13-P0-04-sql-safety-policy" \
  "## 任务
- Task / Issue：WEB-14
<!--
- Task / Issue：WEB-13
-->"

expect_fail \
  "duplicate canonical task fields" \
  "[WEB-13] P0-04 SQL safety policy" \
  "feat/WEB-13-P0-04-sql-safety-policy" \
  "## 任务
- Task / Issue：WEB-13
- Task / Issue：WEB-13"

expect_fail \
  "canonical task field outside the task section" \
  "[WEB-13] P0-04 SQL safety policy" \
  "feat/WEB-13-P0-04-sql-safety-policy" \
  "## 改动与风险
- Task / Issue：WEB-13"

expect_fail \
  "lowercase suffix cannot extend a valid issue ID" \
  "[WEB-13] P0-04 SQL safety policy" \
  "feat/WEB-13-P0-04-sql-safety-policy" \
  "## 任务
- Task / Issue：WEB-13foo"

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

expect_fail_raw \
  "a tilde fence cannot close a backtick fence" \
  "[WEB-13] P0-04 SQL safety policy" \
  "feat/WEB-13-P0-04-sql-safety-policy" \
  "\`\`\`text
~~~
## 任务
- Task / Issue：WEB-13
## 改动与风险
## 验证证据
## WebDB 安全核对
## AI 协作与交接"

expect_fail_raw \
  "a shorter fence cannot close a longer fence" \
  "[WEB-13] P0-04 SQL safety policy" \
  "feat/WEB-13-P0-04-sql-safety-policy" \
  "\`\`\`\`text
\`\`\`
## 任务
- Task / Issue：WEB-13
## 改动与风险
## 验证证据
## WebDB 安全核对
## AI 协作与交接"

workflow="${PR_POLICY_WORKFLOW:-$(dirname "$0")/../workflows/pr-policy.yml}"
if grep -Fq 'uses: actions/checkout@' "$workflow"; then
  echo "FAIL: PR policy must not use checkout because malformed gitlinks can break credential cleanup"
  failures=$((failures + 1))
fi

if ! grep -Fq 'permissions:' "$workflow" ||
  ! grep -Fq 'contents: read' "$workflow" ||
  ! grep -Fq 'pull-requests: read' "$workflow"; then
  echo "FAIL: PR policy must keep explicit read-only permissions"
  failures=$((failures + 1))
fi

if ! grep -Fq 'trusted_ref=${{ github.event.pull_request.base.sha }}' "$workflow"; then
  echo "FAIL: required PR validation must load policy files from the trusted base commit"
  failures=$((failures + 1))
fi

if ! grep -Fq 'candidate_ref=$GITHUB_SHA' "$workflow"; then
  echo "FAIL: candidate policy tests must target the pull request merge commit"
  failures=$((failures + 1))
fi

if ! grep -Fq 'PR_POLICY_VALIDATOR: ${{ runner.temp }}/pr-policy-candidate/.github/scripts/validate-pr-metadata.sh' "$workflow"; then
  echo "FAIL: trusted regression tests must exercise the candidate validator"
  failures=$((failures + 1))
fi

if (( failures > 0 )); then
  exit 1
fi

echo "PR metadata policy tests passed."

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

python_command=${PYTHON:-python3}
if ! "$python_command" --version >/dev/null 2>&1; then
  python_command=python
fi

# Keep the parser and its adversarial fixture in this fetched test artifact so
# the trusted base suite does not depend on files supplied only by the PR.
assert_pr_policy_workflow() {
  "$python_command" - "$1" <<'PY'
from __future__ import annotations

import ast
import re
import shlex
import sys
from pathlib import Path


class AssertionFailure(Exception):
    pass


DECOY_WORKFLOW_FIXTURE = r"""# Decoys that satisfied the old whole-file grep checks:
# trusted_ref=${{ github.event.pull_request.base.sha }}
# candidate_ref=$GITHUB_SHA
# PR_POLICY_VALIDATOR: ${{ runner.temp }}/pr-policy-candidate/.github/scripts/validate-pr-metadata.sh

name: Invalid PR policy fixture

on:
  pull_request:

permissions:
  contents: read
  pull-requests: read

jobs:
  unused:
    runs-on: ubuntu-latest
    steps:
      - name: Unused decoy
        env:
          PR_POLICY_VALIDATOR: ${{ runner.temp }}/pr-policy-candidate/.github/scripts/validate-pr-metadata.sh
        run: |
          trusted_ref=${{ github.event.pull_request.base.sha }}
          candidate_ref=$GITHUB_SHA

  validate:
    name: PR contract
    runs-on: ubuntu-latest
    steps:
      - name: Fetch trusted and candidate policy files
        run: |
          # trusted_ref=${{ github.event.pull_request.base.sha }}
          # candidate_ref=$GITHUB_SHA
          trusted_ref=$GITHUB_SHA
          candidate_ref=${{ github.event.pull_request.head.sha }}
          "$GITHUB_API_URL/repos/$GITHUB_REPOSITORY/contents/$source_path?ref=$ref"

      - run: |
          trusted_ref=${{ github.event.pull_request.base.sha }}
          candidate_ref=$GITHUB_SHA

      - name: Validate Linear metadata and required PR sections with trusted policy
        run: |
          # bash "$RUNNER_TEMP/pr-policy-trusted/.github/scripts/validate-pr-metadata.sh" "$PR_TITLE" "$PR_HEAD_REF" "$PR_BODY"
          bash "$RUNNER_TEMP/pr-policy-candidate/.github/scripts/validate-pr-metadata.sh" "$PR_TITLE" "$PR_HEAD_REF" "$PR_BODY"

      - name: Unused candidate env decoy
        env:
          PR_POLICY_VALIDATOR: ${{ runner.temp }}/pr-policy-candidate/.github/scripts/validate-pr-metadata.sh
        run: |
          bash "$RUNNER_TEMP/pr-policy-trusted/.github/scripts/test-pr-metadata.sh"

      - name: Test candidate policy against the trusted contract
        env:
          PR_POLICY_VALIDATOR: ${{ runner.temp }}/pr-policy-trusted/.github/scripts/validate-pr-metadata.sh
          PR_POLICY_WORKFLOW: ${{ runner.temp }}/pr-policy-candidate/.github/workflows/pr-policy.yml
        run: |
          bash "$RUNNER_TEMP/pr-policy-candidate/.github/scripts/test-pr-metadata.sh"
"""

REORDERED_INLINE_WORKFLOW_FIXTURE = r"""# Same policy as the trusted workflow, but with every target step written in a
# different legal field order (run/env before name) and inline run scalars, so
# the parser must keep handling those variants while the fetch semantics stay
# identical to the trusted workflow.
jobs:
  validate:
    steps:
      - run: |
          set -euo pipefail
          fetch_policy_file() {
            local ref=$1
            local source_path=$2
            local destination=$3
            local response
            local content
            response="$(
              curl --fail --silent --show-error \
                --header "Authorization: Bearer $GH_TOKEN" \
                "$GITHUB_API_URL/repos/$GITHUB_REPOSITORY/contents/$source_path?ref=$ref"
            )"
            content="$(
              printf '%s' "$response" |
                jq --exit-status --raw-output \
                  'select(.encoding == "base64" and (.content | type == "string") and (.content | length > 0)) | .content'
            )"
            printf '%s' "$content" | tr -d '\n' | base64 --decode >"$destination"
          }
          trusted_root="$RUNNER_TEMP/pr-policy-trusted"
          candidate_root="$RUNNER_TEMP/pr-policy-candidate"
          trusted_ref=${{ github.event.pull_request.base.sha }}
          candidate_ref=$GITHUB_SHA
          fetch_policy_file \
            "$trusted_ref" \
            '.github/workflows/pr-policy.yml' \
            "$trusted_root/.github/workflows/pr-policy.yml"
          fetch_policy_file \
            "$trusted_ref" \
            '.github/scripts/test-pr-metadata.sh' \
            "$trusted_root/.github/scripts/test-pr-metadata.sh"
          fetch_policy_file \
            "$trusted_ref" \
            '.github/scripts/validate-pr-metadata.sh' \
            "$trusted_root/.github/scripts/validate-pr-metadata.sh"
          fetch_policy_file \
            "$candidate_ref" \
            '.github/workflows/pr-policy.yml' \
            "$candidate_root/.github/workflows/pr-policy.yml"
          fetch_policy_file \
            "$candidate_ref" \
            '.github/scripts/test-pr-metadata.sh' \
            "$candidate_root/.github/scripts/test-pr-metadata.sh"
          fetch_policy_file \
            "$candidate_ref" \
            '.github/scripts/validate-pr-metadata.sh' \
            "$candidate_root/.github/scripts/validate-pr-metadata.sh"
        name: Fetch trusted and candidate policy files

      - run: 'bash "$RUNNER_TEMP/pr-policy-trusted/.github/scripts/validate-pr-metadata.sh" "$PR_TITLE" "$PR_HEAD_REF" "$PR_BODY"'
        name: Validate Linear metadata and required PR sections with trusted policy

      - env:
          PR_POLICY_VALIDATOR: ${{ runner.temp }}/pr-policy-candidate/.github/scripts/validate-pr-metadata.sh
          PR_POLICY_WORKFLOW: ${{ runner.temp }}/pr-policy-candidate/.github/workflows/pr-policy.yml
        run: 'bash "$RUNNER_TEMP/pr-policy-trusted/.github/scripts/test-pr-metadata.sh"'
        name: Test candidate policy against the trusted contract
"""

UNREACHABLE_BAIT_FIXTURE = r"""# A candidate that hides the required trusted_ref/candidate_ref assignments (and
# the API URL / jq text) inside an unreachable `if false; then ... fi` block,
# then runs the actual fetch against the candidate ref. Old text matching in
# require_command accepted this because the required strings still appeared.
name: Invalid PR policy fixture

on:
  pull_request:

permissions:
  contents: read
  pull-requests: read

jobs:
  validate:
    name: PR contract
    runs-on: ubuntu-latest
    steps:
      - name: Fetch trusted and candidate policy files
        env:
          GH_TOKEN: ${{ github.token }}
        run: |
          set -euo pipefail
          trusted_root="$RUNNER_TEMP/pr-policy-trusted"
          candidate_root="$RUNNER_TEMP/pr-policy-candidate"
          if false; then
            trusted_ref=${{ github.event.pull_request.base.sha }}
            candidate_ref=$GITHUB_SHA
            "$GITHUB_API_URL/repos/$GITHUB_REPOSITORY/contents/$source_path?ref=$ref"
            jq --exit-status --raw-output \
              'select(.encoding == "base64" and (.content | type == "string") and (.content | length > 0)) | .content'
          fi
          trusted_ref=$GITHUB_SHA
          candidate_ref=${{ github.event.pull_request.head.sha }}
          fetch_policy_file() {
            local ref=$1
            local source_path=$2
            local destination=$3
            local response
            local content
            response="$(
              curl --fail --silent --show-error \
                --header "Authorization: Bearer $GH_TOKEN" \
                "$GITHUB_API_URL/repos/$GITHUB_REPOSITORY/contents/$source_path?ref=$ref"
            )"
            content="$(
              printf '%s' "$response" |
                jq --exit-status --raw-output \
                  'select(.encoding == "base64" and (.content | type == "string") and (.content | length > 0)) | .content'
            )"
            printf '%s' "$content" | tr -d '\n' | base64 --decode >"$destination"
          }
          fetch_policy_file \
            "$trusted_ref" \
            '.github/workflows/pr-policy.yml' \
            "$trusted_root/.github/workflows/pr-policy.yml"
          fetch_policy_file \
            "$trusted_ref" \
            '.github/scripts/test-pr-metadata.sh' \
            "$trusted_root/.github/scripts/test-pr-metadata.sh"
          fetch_policy_file \
            "$trusted_ref" \
            '.github/scripts/validate-pr-metadata.sh' \
            "$trusted_root/.github/scripts/validate-pr-metadata.sh"
          fetch_policy_file \
            "$candidate_ref" \
            '.github/workflows/pr-policy.yml' \
            "$candidate_root/.github/workflows/pr-policy.yml"
          fetch_policy_file \
            "$candidate_ref" \
            '.github/scripts/test-pr-metadata.sh' \
            "$candidate_root/.github/scripts/test-pr-metadata.sh"
          fetch_policy_file \
            "$candidate_ref" \
            '.github/scripts/validate-pr-metadata.sh' \
            "$candidate_root/.github/scripts/validate-pr-metadata.sh"

      - name: Validate Linear metadata and required PR sections with trusted policy
        run: |
          bash "$RUNNER_TEMP/pr-policy-trusted/.github/scripts/validate-pr-metadata.sh" "$PR_TITLE" "$PR_HEAD_REF" "$PR_BODY"

      - name: Test candidate policy against the trusted contract
        env:
          PR_POLICY_VALIDATOR: ${{ runner.temp }}/pr-policy-candidate/.github/scripts/validate-pr-metadata.sh
          PR_POLICY_WORKFLOW: ${{ runner.temp }}/pr-policy-candidate/.github/workflows/pr-policy.yml
        run: |
          bash "$RUNNER_TEMP/pr-policy-trusted/.github/scripts/test-pr-metadata.sh"
"""

OVERRIDE_BAIT_FIXTURE = r"""# The required trusted_ref/candidate_ref assignments are present but then
# overridden by wrong values before the fetch calls run, so the real fetch would
# use the merge ref for everything. Text matching accepted this because the
# correct assignment strings still appeared.
name: Invalid PR policy fixture

on:
  pull_request:

permissions:
  contents: read
  pull-requests: read

jobs:
  validate:
    name: PR contract
    runs-on: ubuntu-latest
    steps:
      - name: Fetch trusted and candidate policy files
        env:
          GH_TOKEN: ${{ github.token }}
        run: |
          set -euo pipefail
          trusted_root="$RUNNER_TEMP/pr-policy-trusted"
          candidate_root="$RUNNER_TEMP/pr-policy-candidate"
          trusted_ref=${{ github.event.pull_request.base.sha }}
          candidate_ref=$GITHUB_SHA
          trusted_ref=$GITHUB_SHA
          candidate_ref=${{ github.event.pull_request.head.sha }}
          fetch_policy_file() {
            local ref=$1
            local source_path=$2
            local destination=$3
            local response
            local content
            response="$(
              curl --fail --silent --show-error \
                --header "Authorization: Bearer $GH_TOKEN" \
                "$GITHUB_API_URL/repos/$GITHUB_REPOSITORY/contents/$source_path?ref=$ref"
            )"
            content="$(
              printf '%s' "$response" |
                jq --exit-status --raw-output \
                  'select(.encoding == "base64" and (.content | type == "string") and (.content | length > 0)) | .content'
            )"
            printf '%s' "$content" | tr -d '\n' | base64 --decode >"$destination"
          }
          fetch_policy_file \
            "$trusted_ref" \
            '.github/workflows/pr-policy.yml' \
            "$trusted_root/.github/workflows/pr-policy.yml"
          fetch_policy_file \
            "$trusted_ref" \
            '.github/scripts/test-pr-metadata.sh' \
            "$trusted_root/.github/scripts/test-pr-metadata.sh"
          fetch_policy_file \
            "$trusted_ref" \
            '.github/scripts/validate-pr-metadata.sh' \
            "$trusted_root/.github/scripts/validate-pr-metadata.sh"
          fetch_policy_file \
            "$candidate_ref" \
            '.github/workflows/pr-policy.yml' \
            "$candidate_root/.github/workflows/pr-policy.yml"
          fetch_policy_file \
            "$candidate_ref" \
            '.github/scripts/test-pr-metadata.sh' \
            "$candidate_root/.github/scripts/test-pr-metadata.sh"
          fetch_policy_file \
            "$candidate_ref" \
            '.github/scripts/validate-pr-metadata.sh' \
            "$candidate_root/.github/scripts/validate-pr-metadata.sh"

      - name: Validate Linear metadata and required PR sections with trusted policy
        run: |
          bash "$RUNNER_TEMP/pr-policy-trusted/.github/scripts/validate-pr-metadata.sh" "$PR_TITLE" "$PR_HEAD_REF" "$PR_BODY"

      - name: Test candidate policy against the trusted contract
        env:
          PR_POLICY_VALIDATOR: ${{ runner.temp }}/pr-policy-candidate/.github/scripts/validate-pr-metadata.sh
          PR_POLICY_WORKFLOW: ${{ runner.temp }}/pr-policy-candidate/.github/workflows/pr-policy.yml
        run: |
          bash "$RUNNER_TEMP/pr-policy-trusted/.github/scripts/test-pr-metadata.sh"
"""

COMMENT_BAIT_FIXTURE = r"""# The fetch_policy_file() body carries every required fetch snippet only inside
# comments, so a text-only body check would pass while the real function does
# nothing.
name: Invalid PR policy fixture

on:
  pull_request:

permissions:
  contents: read
  pull-requests: read

jobs:
  validate:
    name: PR contract
    runs-on: ubuntu-latest
    steps:
      - name: Fetch trusted and candidate policy files
        env:
          GH_TOKEN: ${{ github.token }}
        run: |
          set -euo pipefail
          trusted_root="$RUNNER_TEMP/pr-policy-trusted"
          candidate_root="$RUNNER_TEMP/pr-policy-candidate"
          trusted_ref=${{ github.event.pull_request.base.sha }}
          candidate_ref=$GITHUB_SHA
          fetch_policy_file() {
            # local ref=$1
            # "$GITHUB_API_URL/repos/$GITHUB_REPOSITORY/contents/$source_path?ref=$ref"
            # jq --exit-status --raw-output
            # 'select(.encoding == "base64" and (.content | type == "string") and (.content | length > 0)) | .content'
            # printf '%s' "$content" | tr -d '\n' | base64 --decode >"$destination"
            echo "no-op fetch"
          }
          fetch_policy_file \
            "$trusted_ref" \
            '.github/workflows/pr-policy.yml' \
            "$trusted_root/.github/workflows/pr-policy.yml"
          fetch_policy_file \
            "$trusted_ref" \
            '.github/scripts/test-pr-metadata.sh' \
            "$trusted_root/.github/scripts/test-pr-metadata.sh"
          fetch_policy_file \
            "$trusted_ref" \
            '.github/scripts/validate-pr-metadata.sh' \
            "$trusted_root/.github/scripts/validate-pr-metadata.sh"
          fetch_policy_file \
            "$candidate_ref" \
            '.github/workflows/pr-policy.yml' \
            "$candidate_root/.github/workflows/pr-policy.yml"
          fetch_policy_file \
            "$candidate_ref" \
            '.github/scripts/test-pr-metadata.sh' \
            "$candidate_root/.github/scripts/test-pr-metadata.sh"
          fetch_policy_file \
            "$candidate_ref" \
            '.github/scripts/validate-pr-metadata.sh' \
            "$candidate_root/.github/scripts/validate-pr-metadata.sh"

      - name: Validate Linear metadata and required PR sections with trusted policy
        run: |
          bash "$RUNNER_TEMP/pr-policy-trusted/.github/scripts/validate-pr-metadata.sh" "$PR_TITLE" "$PR_HEAD_REF" "$PR_BODY"

      - name: Test candidate policy against the trusted contract
        env:
          PR_POLICY_VALIDATOR: ${{ runner.temp }}/pr-policy-candidate/.github/scripts/validate-pr-metadata.sh
          PR_POLICY_WORKFLOW: ${{ runner.temp }}/pr-policy-candidate/.github/workflows/pr-policy.yml
        run: |
          bash "$RUNNER_TEMP/pr-policy-trusted/.github/scripts/test-pr-metadata.sh"
"""

INDENTED_OVERRIDE_BAIT_FIXTURE = r"""# A compliant top-level assignment is kept, but the ref is overridden inside an
# executable indented block before the fetch calls run, so the effective value
# at call time is the merge ref. Collecting only zero-indent assignments (the
# previous behaviour) skipped the override and accepted this.
name: Invalid PR policy fixture

on:
  pull_request:

permissions:
  contents: read
  pull-requests: read

jobs:
  validate:
    name: PR contract
    runs-on: ubuntu-latest
    steps:
      - name: Fetch trusted and candidate policy files
        env:
          GH_TOKEN: ${{ github.token }}
        run: |
          set -euo pipefail
          trusted_root="$RUNNER_TEMP/pr-policy-trusted"
          candidate_root="$RUNNER_TEMP/pr-policy-candidate"
          trusted_ref=${{ github.event.pull_request.base.sha }}
          candidate_ref=$GITHUB_SHA
          if true; then
            trusted_ref=$GITHUB_SHA
            candidate_ref=${{ github.event.pull_request.head.sha }}
          fi
          fetch_policy_file() {
            local ref=$1
            local source_path=$2
            local destination=$3
            local response
            local content
            response="$(
              curl --fail --silent --show-error \
                --header "Authorization: Bearer $GH_TOKEN" \
                "$GITHUB_API_URL/repos/$GITHUB_REPOSITORY/contents/$source_path?ref=$ref"
            )"
            content="$(
              printf '%s' "$response" |
                jq --exit-status --raw-output \
                  'select(.encoding == "base64" and (.content | type == "string") and (.content | length > 0)) | .content'
            )"
            printf '%s' "$content" | tr -d '\n' | base64 --decode >"$destination"
          }
          fetch_policy_file \
            "$trusted_ref" \
            '.github/workflows/pr-policy.yml' \
            "$trusted_root/.github/workflows/pr-policy.yml"
          fetch_policy_file \
            "$trusted_ref" \
            '.github/scripts/test-pr-metadata.sh' \
            "$trusted_root/.github/scripts/test-pr-metadata.sh"
          fetch_policy_file \
            "$trusted_ref" \
            '.github/scripts/validate-pr-metadata.sh' \
            "$trusted_root/.github/scripts/validate-pr-metadata.sh"
          fetch_policy_file \
            "$candidate_ref" \
            '.github/workflows/pr-policy.yml' \
            "$candidate_root/.github/workflows/pr-policy.yml"
          fetch_policy_file \
            "$candidate_ref" \
            '.github/scripts/test-pr-metadata.sh' \
            "$candidate_root/.github/scripts/test-pr-metadata.sh"
          fetch_policy_file \
            "$candidate_ref" \
            '.github/scripts/validate-pr-metadata.sh' \
            "$candidate_root/.github/scripts/validate-pr-metadata.sh"

      - name: Validate Linear metadata and required PR sections with trusted policy
        run: |
          bash "$RUNNER_TEMP/pr-policy-trusted/.github/scripts/validate-pr-metadata.sh" "$PR_TITLE" "$PR_HEAD_REF" "$PR_BODY"

      - name: Test candidate policy against the trusted contract
        env:
          PR_POLICY_VALIDATOR: ${{ runner.temp }}/pr-policy-candidate/.github/scripts/validate-pr-metadata.sh
          PR_POLICY_WORKFLOW: ${{ runner.temp }}/pr-policy-candidate/.github/workflows/pr-policy.yml
        run: |
          bash "$RUNNER_TEMP/pr-policy-trusted/.github/scripts/test-pr-metadata.sh"
"""

EXPORT_OVERRIDE_BAIT_FIXTURE = r"""# A compliant assignment is kept but the ref is rewritten with bash builtins
# (export / printf -v) that a plain-assignment matcher cannot see, so the real
# fetch would use the merge ref for the trusted files.
name: Invalid PR policy fixture

on:
  pull_request:

permissions:
  contents: read
  pull-requests: read

jobs:
  validate:
    name: PR contract
    runs-on: ubuntu-latest
    steps:
      - name: Fetch trusted and candidate policy files
        env:
          GH_TOKEN: ${{ github.token }}
        run: |
          set -euo pipefail
          trusted_root="$RUNNER_TEMP/pr-policy-trusted"
          candidate_root="$RUNNER_TEMP/pr-policy-candidate"
          trusted_ref=${{ github.event.pull_request.base.sha }}
          candidate_ref=$GITHUB_SHA
          export trusted_ref=$GITHUB_SHA
          printf -v candidate_ref "$GITHUB_SHA"
          fetch_policy_file() {
            local ref=$1
            local source_path=$2
            local destination=$3
            response="$(curl --fail --silent --show-error --header "Authorization: Bearer $GH_TOKEN" "$GITHUB_API_URL/repos/$GITHUB_REPOSITORY/contents/$source_path?ref=$ref")"
            content="$(printf '%s' "$response" | jq --exit-status --raw-output 'select(.encoding == "base64" and (.content | type == "string") and (.content | length > 0)) | .content')"
            printf '%s' "$content" | tr -d '\n' | base64 --decode >"$destination"
          }
          fetch_policy_file "$trusted_ref" '.github/workflows/pr-policy.yml' "$trusted_root/.github/workflows/pr-policy.yml"
          fetch_policy_file "$trusted_ref" '.github/scripts/test-pr-metadata.sh' "$trusted_root/.github/scripts/test-pr-metadata.sh"
          fetch_policy_file "$trusted_ref" '.github/scripts/validate-pr-metadata.sh' "$trusted_root/.github/scripts/validate-pr-metadata.sh"
          fetch_policy_file "$candidate_ref" '.github/workflows/pr-policy.yml' "$candidate_root/.github/workflows/pr-policy.yml"
          fetch_policy_file "$candidate_ref" '.github/scripts/test-pr-metadata.sh' "$candidate_root/.github/scripts/test-pr-metadata.sh"
          fetch_policy_file "$candidate_ref" '.github/scripts/validate-pr-metadata.sh' "$candidate_root/.github/scripts/validate-pr-metadata.sh"

      - name: Validate Linear metadata and required PR sections with trusted policy
        run: |
          bash "$RUNNER_TEMP/pr-policy-trusted/.github/scripts/validate-pr-metadata.sh" "$PR_TITLE" "$PR_HEAD_REF" "$PR_BODY"

      - name: Test candidate policy against the trusted contract
        env:
          PR_POLICY_VALIDATOR: ${{ runner.temp }}/pr-policy-candidate/.github/scripts/validate-pr-metadata.sh
          PR_POLICY_WORKFLOW: ${{ runner.temp }}/pr-policy-candidate/.github/workflows/pr-policy.yml
        run: |
          bash "$RUNNER_TEMP/pr-policy-trusted/.github/scripts/test-pr-metadata.sh"
"""

DECLARE_EVAL_BAIT_FIXTURE = r"""# A compliant assignment is kept but the ref is rewritten through declare, read,
# eval and source, which can run arbitrary commands and reassign the variables.
name: Invalid PR policy fixture

on:
  pull_request:

permissions:
  contents: read
  pull-requests: read

jobs:
  validate:
    name: PR contract
    runs-on: ubuntu-latest
    steps:
      - name: Fetch trusted and candidate policy files
        env:
          GH_TOKEN: ${{ github.token }}
        run: |
          set -euo pipefail
          trusted_root="$RUNNER_TEMP/pr-policy-trusted"
          candidate_root="$RUNNER_TEMP/pr-policy-candidate"
          trusted_ref=${{ github.event.pull_request.base.sha }}
          candidate_ref=$GITHUB_SHA
          declare trusted_ref=$GITHUB_SHA
          read candidate_ref <<< "$GITHUB_SHA"
          eval "trusted_ref=\$GITHUB_SHA"
          source /etc/profile
          . /etc/profile
          fetch_policy_file() {
            local ref=$1
            local source_path=$2
            local destination=$3
            response="$(curl --fail --silent --show-error --header "Authorization: Bearer $GH_TOKEN" "$GITHUB_API_URL/repos/$GITHUB_REPOSITORY/contents/$source_path?ref=$ref")"
            content="$(printf '%s' "$response" | jq --exit-status --raw-output 'select(.encoding == "base64" and (.content | type == "string") and (.content | length > 0)) | .content')"
            printf '%s' "$content" | tr -d '\n' | base64 --decode >"$destination"
          }
          fetch_policy_file "$trusted_ref" '.github/workflows/pr-policy.yml' "$trusted_root/.github/workflows/pr-policy.yml"
          fetch_policy_file "$trusted_ref" '.github/scripts/test-pr-metadata.sh' "$trusted_root/.github/scripts/test-pr-metadata.sh"
          fetch_policy_file "$trusted_ref" '.github/scripts/validate-pr-metadata.sh' "$trusted_root/.github/scripts/validate-pr-metadata.sh"
          fetch_policy_file "$candidate_ref" '.github/workflows/pr-policy.yml' "$candidate_root/.github/workflows/pr-policy.yml"
          fetch_policy_file "$candidate_ref" '.github/scripts/test-pr-metadata.sh' "$candidate_root/.github/scripts/test-pr-metadata.sh"
          fetch_policy_file "$candidate_ref" '.github/scripts/validate-pr-metadata.sh' "$candidate_root/.github/scripts/validate-pr-metadata.sh"

      - name: Validate Linear metadata and required PR sections with trusted policy
        run: |
          bash "$RUNNER_TEMP/pr-policy-trusted/.github/scripts/validate-pr-metadata.sh" "$PR_TITLE" "$PR_HEAD_REF" "$PR_BODY"

      - name: Test candidate policy against the trusted contract
        env:
          PR_POLICY_VALIDATOR: ${{ runner.temp }}/pr-policy-candidate/.github/scripts/validate-pr-metadata.sh
          PR_POLICY_WORKFLOW: ${{ runner.temp }}/pr-policy-candidate/.github/workflows/pr-policy.yml
        run: |
          bash "$RUNNER_TEMP/pr-policy-trusted/.github/scripts/test-pr-metadata.sh"
"""


def indentation(line: str) -> int:
    prefix = line[: len(line) - len(line.lstrip())]
    if "\t" in prefix:
        raise AssertionFailure("tabs are not supported in workflow indentation")
    return len(prefix)


def content(line: str) -> str:
    return line.lstrip()


def ignored(line: str) -> bool:
    stripped = content(line)
    return not stripped or stripped.startswith("#")


def scalar(value: str) -> str:
    value = value.strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in {"'", '"'}:
        parsed = ast.literal_eval(value)
        if not isinstance(parsed, str):
            raise AssertionFailure(f"expected string scalar: {value}")
        return parsed
    return value


def find_key(
    lines: list[str], key: str, expected_indent: int, start: int, end: int
) -> int:
    matches = [
        index
        for index in range(start, end)
        if not ignored(lines[index])
        and indentation(lines[index]) == expected_indent
        and content(lines[index]) == f"{key}:"
    ]
    if len(matches) != 1:
        raise AssertionFailure(
            f"expected exactly one {key!r} key at indentation {expected_indent}"
        )
    return matches[0]


def block_end(lines: list[str], start: int, parent_indent: int, end: int) -> int:
    for index in range(start + 1, end):
        if not ignored(lines[index]) and indentation(lines[index]) <= parent_indent:
            return index
    return end


def parse_validate_steps(source: str) -> list[dict[str, object]]:
    lines = source.splitlines()
    jobs = find_key(lines, "jobs", 0, 0, len(lines))
    jobs_end = block_end(lines, jobs, 0, len(lines))
    validate = find_key(lines, "validate", 2, jobs + 1, jobs_end)
    validate_end = block_end(lines, validate, 2, jobs_end)
    steps = find_key(lines, "steps", 4, validate + 1, validate_end)
    steps_end = block_end(lines, steps, 4, validate_end)

    starts = [
        index
        for index in range(steps + 1, steps_end)
        if not ignored(lines[index])
        and indentation(lines[index]) == 6
        and re.match(r"-[ ]+", content(lines[index]))
    ]
    parsed: list[dict[str, object]] = []
    for position, start in enumerate(starts):
        end = starts[position + 1] if position + 1 < len(starts) else steps_end
        first_field = content(lines[start])[1:].lstrip()
        step: dict[str, object] = {
            "name": "",
            "env": {},
            "run": [],
        }
        index = start
        while index < end:
            if index == start:
                stripped = first_field
            else:
                line = lines[index]
                if ignored(line) or indentation(line) != 8:
                    index += 1
                    continue
                stripped = content(line)
            name_match = re.fullmatch(r"name:[ ]*(.+)", stripped)
            if name_match:
                step["name"] = scalar(name_match.group(1))
                index += 1
                continue
            if stripped == "env:":
                env_end = block_end(lines, index, 8, end)
                env = step["env"]
                assert isinstance(env, dict)
                for env_index in range(index + 1, env_end):
                    env_line = lines[env_index]
                    if ignored(env_line) or indentation(env_line) != 10:
                        continue
                    match = re.fullmatch(
                        r"([A-Za-z_][A-Za-z0-9_]*):[ ]*(.*)", content(env_line)
                    )
                    if match:
                        env[match.group(1)] = scalar(match.group(2))
                index = env_end
                continue
            block_run = re.fullmatch(r"run:[ ]*([|>])[-+]?", stripped)
            if block_run:
                run_end = block_end(lines, index, 8, end)
                block = lines[index + 1 : run_end]
                block_indent = min(
                    (indentation(item) for item in block if content(item)), default=10
                )
                step["run"] = [
                    item[block_indent:] if content(item) else "" for item in block
                ]
                index = run_end
                continue
            inline_run = re.fullmatch(r"run:[ ]*(.+)", stripped)
            if inline_run:
                step["run"] = [scalar(inline_run.group(1))]
                index += 1
                continue
            index += 1
        parsed.append(step)
    return parsed


def target_step(
    steps: list[dict[str, object]], name: str
) -> dict[str, object]:
    matches = [step for step in steps if step["name"] == name]
    if len(matches) != 1:
        raise AssertionFailure(f"expected exactly one target step named {name!r}")
    return matches[0]


def commands(step: dict[str, object]) -> list[str]:
    run = step["run"]
    assert isinstance(run, list)
    return [
        line.strip()
        for line in run
        if isinstance(line, str)
        and line.strip()
        and not line.lstrip().startswith("#")
    ]


# The three policy files that must be fetched from the trusted base ref and,
# independently, from the candidate merge ref. Bindings are verified
# structurally in assert_fetch_semantics, not by searching for command text.
FETCH_SOURCE_PATHS = (
    ".github/workflows/pr-policy.yml",
    ".github/scripts/test-pr-metadata.sh",
    ".github/scripts/validate-pr-metadata.sh",
)


def run_lines(step: dict[str, object]) -> list[str]:
    run = step["run"]
    assert isinstance(run, list)
    lines = [line for line in run if isinstance(line, str)]
    if not lines:
        raise AssertionFailure("fetch step does not define a run block")
    return lines


def fetch_function_lines(
    lines: list[str],
) -> tuple[int, list[str], int]:
    start = next(
        (
            index
            for index, line in enumerate(lines)
            if line.rstrip() == "fetch_policy_file() {"
        ),
        None,
    )
    if start is None:
        raise AssertionFailure("fetch step does not define fetch_policy_file()")
    body: list[str] = []
    index = start + 1
    while index < len(lines):
        if lines[index].strip() == "}":
            return start, body, index + 1
        body.append(lines[index])
        index += 1
    raise AssertionFailure("fetch_policy_file() is never closed")


def joined_shell_words(lines: list[str], start: int) -> tuple[str, int]:
    parts: list[str] = []
    index = start
    while index < len(lines):
        line = lines[index].rstrip()
        continued = line.endswith("\\")
        if continued:
            line = line[:-1].rstrip()
        parts.append(line)
        index += 1
        if not continued:
            break
    return " ".join(parts), index


def fetch_calls(
    lines: list[str], body_start: int, body_end: int
) -> list[tuple[str, str, str]]:
    calls: list[tuple[str, str, str]] = []
    index = 0
    while index < len(lines):
        if index == body_start:
            index = body_end
            continue
        stripped = lines[index].strip()
        if stripped == "fetch_policy_file" or stripped.startswith(
            "fetch_policy_file "
        ):
            statement, index = joined_shell_words(lines, index)
            words = shlex.split(statement)
            if len(words) != 4 or words[0] != "fetch_policy_file":
                raise AssertionFailure(
                    f"fetch_policy_file call must carry three arguments: {statement!r}"
                )
            calls.append((words[1], words[2], words[3]))
        else:
            index += 1
    return calls


ASSIGN_RE = re.compile(r"^(trusted_ref|candidate_ref)=(.*)$")


def assert_fetch_semantics(fetch: dict[str, object]) -> None:
    lines = run_lines(fetch)
    body_start, body, body_end = fetch_function_lines(lines)

    # Reject bash constructs that could rewrite a ref variable without looking
    # like a plain assignment, either directly (export/declare/typeset/
    # printf -v/read) or indirectly (eval/source/. can run anything).
    rewrite_patterns = (
        re.compile(r"\bexport\b.*\b(?:trusted_ref|candidate_ref)\b"),
        re.compile(r"\b(?:declare|typeset)\b.*\b(?:trusted_ref|candidate_ref)\b"),
        re.compile(r"printf\s+-v\s+(?:trusted_ref|candidate_ref)\b"),
        re.compile(r"\bread\b.*\b(?:trusted_ref|candidate_ref)\b"),
        re.compile(r"\beval\b"),
        re.compile(r"\bsource\b"),
        re.compile(r"^\.\s"),
    )
    for line in lines:
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        for pattern in rewrite_patterns:
            if pattern.search(line):
                raise AssertionFailure(
                    "fetch step must not rewrite trusted_ref/candidate_ref via "
                    "bash builtins (export/declare/typeset/printf -v/read/eval/"
                    f"source): {line.strip()}"
                )

    # Collect every assignment to the ref variables outside the function body,
    # at any indentation. Exactly one assignment per variable must appear
    # before the first fetch_policy_file() call with the expected value; a
    # duplicate, later, or indented-block assignment could override the trusted
    # ref before the calls run. The first_call bound (not the function
    # definition) is what matters, so a valid workflow may define the function
    # before the assignments.
    first_call = next(
        (
            index
            for index, line in enumerate(lines)
            if not (body_start <= index < body_end)
            and line.strip().startswith("fetch_policy_file")
        ),
        len(lines),
    )
    assign_indexes: dict[str, list[int]] = {}
    assign_values: dict[str, list[str]] = {}
    for index, line in enumerate(lines):
        if body_start <= index < body_end:
            continue
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        match = ASSIGN_RE.match(line.strip())
        if not match:
            continue
        variable, value = match.group(1), match.group(2)
        assign_indexes.setdefault(variable, []).append(index)
        assign_values.setdefault(variable, []).append(value)
    for variable, expected in (
        ("trusted_ref", "${{ github.event.pull_request.base.sha }}"),
        ("candidate_ref", "$GITHUB_SHA"),
    ):
        indexes = assign_indexes.get(variable, [])
        if len(indexes) != 1:
            raise AssertionFailure(
                f"fetch step must assign {variable} exactly once before the "
                f"first fetch_policy_file() call, found {len(indexes)}"
            )
        if indexes[0] >= first_call:
            raise AssertionFailure(
                f"fetch step must assign {variable} before the first "
                "fetch_policy_file() call"
            )
        if assign_values[variable][0] != expected:
            raise AssertionFailure(
                f"fetch step must assign {variable}={expected}, got "
                f"{variable}={assign_values[variable][0]}"
            )

    # Check executable function content only, ignoring comments and blank
    # lines (matching commands()), so bait text hidden inside comments cannot
    # satisfy the required fetch snippets.
    body_lines = [
        line.strip()
        for line in body
        if line.strip() and not line.lstrip().startswith("#")
    ]
    body_text = "\n".join(body_lines)
    for required in (
        "local ref=$1",
        "?ref=$ref",
        "jq --exit-status --raw-output",
        '.encoding == "base64"',
        "base64 --decode",
    ):
        if required not in body_text:
            raise AssertionFailure(
                "fetch_policy_file() body is missing required fetch logic: "
                f"{required}"
            )

    calls = fetch_calls(lines, body_start, body_end)
    trusted = [call for call in calls if call[0] == "$trusted_ref"]
    candidate = [call for call in calls if call[0] == "$candidate_ref"]
    if len(trusted) != 3 or len(candidate) != 3:
        raise AssertionFailure(
            "fetch step must call fetch_policy_file exactly three times with "
            "$trusted_ref and three times with $candidate_ref"
        )
    for group, ref, root in (
        (trusted, "$trusted_ref", "$trusted_root"),
        (candidate, "$candidate_ref", "$candidate_root"),
    ):
        fetched = sorted(call[1] for call in group)
        if fetched != sorted(FETCH_SOURCE_PATHS):
            raise AssertionFailure(
                f"fetch_policy_file calls with {ref} must fetch exactly "
                f"{sorted(FETCH_SOURCE_PATHS)}, got {fetched}"
            )
        for call in group:
            if not call[2].startswith(f"{root}/"):
                raise AssertionFailure(
                    f"fetch_policy_file destination for {call[1]} must live under "
                    f"{root}, got {call[2]}"
                )


def require_command(step: dict[str, object], expected: str) -> None:
    if expected not in commands(step):
        raise AssertionFailure(
            f"step {step['name']!r} does not run required command: {expected}"
        )


def assert_workflow(source: str) -> None:
    steps = parse_validate_steps(source)
    fetch = target_step(steps, "Fetch trusted and candidate policy files")
    assert_fetch_semantics(fetch)

    validation = target_step(
        steps, "Validate Linear metadata and required PR sections with trusted policy"
    )
    require_command(
        validation,
        'bash "$RUNNER_TEMP/pr-policy-trusted/.github/scripts/'
        'validate-pr-metadata.sh" "$PR_TITLE" "$PR_HEAD_REF" "$PR_BODY"',
    )

    candidate = target_step(
        steps, "Test candidate policy against the trusted contract"
    )
    env = candidate["env"]
    assert isinstance(env, dict)
    if env.get("PR_POLICY_VALIDATOR") != (
        "${{ runner.temp }}/pr-policy-candidate/.github/scripts/"
        "validate-pr-metadata.sh"
    ):
        raise AssertionFailure(
            "candidate policy step does not use the candidate validator path"
        )
    if env.get("PR_POLICY_WORKFLOW") != (
        "${{ runner.temp }}/pr-policy-candidate/.github/workflows/pr-policy.yml"
    ):
        raise AssertionFailure(
            "candidate policy step does not use the candidate workflow path"
        )
    require_command(
        candidate,
        'bash "$RUNNER_TEMP/pr-policy-trusted/.github/scripts/test-pr-metadata.sh"',
    )


try:
    source = (
        DECOY_WORKFLOW_FIXTURE
        if sys.argv[1] == "--decoy"
        else REORDERED_INLINE_WORKFLOW_FIXTURE
        if sys.argv[1] == "--valid-variants"
        else UNREACHABLE_BAIT_FIXTURE
        if sys.argv[1] == "--unreachable-bait"
        else OVERRIDE_BAIT_FIXTURE
        if sys.argv[1] == "--override-bait"
        else COMMENT_BAIT_FIXTURE
        if sys.argv[1] == "--comment-bait"
        else INDENTED_OVERRIDE_BAIT_FIXTURE
        if sys.argv[1] == "--indented-override-bait"
        else EXPORT_OVERRIDE_BAIT_FIXTURE
        if sys.argv[1] == "--export-override-bait"
        else DECLARE_EVAL_BAIT_FIXTURE
        if sys.argv[1] == "--declare-eval-bait"
        else Path(sys.argv[1]).read_text(encoding="utf-8")
    )
    assert_workflow(source)
except (AssertionFailure, OSError, SyntaxError, UnicodeError, ValueError) as error:
    print(f"FAIL: {error}", file=sys.stderr)
    raise SystemExit(1)
PY
}

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

if ! assert_pr_policy_workflow "$workflow"; then
  echo "FAIL: PR policy workflow trust-boundary structure is invalid"
  failures=$((failures + 1))
fi

if ! assert_pr_policy_workflow --valid-variants; then
  echo "FAIL: structured workflow assertion rejected valid field ordering or inline run"
  failures=$((failures + 1))
fi

if assert_pr_policy_workflow --decoy >/dev/null 2>&1; then
  echo "FAIL: structured workflow assertion accepted decoy comments or unused steps"
  failures=$((failures + 1))
fi

if assert_pr_policy_workflow --unreachable-bait >/dev/null 2>&1; then
  echo "FAIL: structured workflow assertion accepted commands hidden in an unreachable block"
  failures=$((failures + 1))
fi

if assert_pr_policy_workflow --override-bait >/dev/null 2>&1; then
  echo "FAIL: structured workflow assertion accepted duplicate or later ref overrides"
  failures=$((failures + 1))
fi

if assert_pr_policy_workflow --comment-bait >/dev/null 2>&1; then
  echo "FAIL: structured workflow assertion accepted fetch text hidden in function comments"
  failures=$((failures + 1))
fi

if assert_pr_policy_workflow --indented-override-bait >/dev/null 2>&1; then
  echo "FAIL: structured workflow assertion accepted indented ref overrides"
  failures=$((failures + 1))
fi

if assert_pr_policy_workflow --export-override-bait >/dev/null 2>&1; then
  echo "FAIL: structured workflow assertion accepted export/printf -v ref rewrites"
  failures=$((failures + 1))
fi

if assert_pr_policy_workflow --declare-eval-bait >/dev/null 2>&1; then
  echo "FAIL: structured workflow assertion accepted declare/read/eval/source ref rewrites"
  failures=$((failures + 1))
fi

if (( failures > 0 )); then
  exit 1
fi

echo "PR metadata policy tests passed."

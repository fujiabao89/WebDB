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
        name_match = re.fullmatch(r"name:[ ]*(.+)", first_field)
        step: dict[str, object] = {
            "name": scalar(name_match.group(1)) if name_match else "",
            "env": {},
            "run": [],
        }
        index = start + 1
        while index < end:
            line = lines[index]
            if ignored(line) or indentation(line) != 8:
                index += 1
                continue
            stripped = content(line)
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
            if re.fullmatch(r"run:[ ]*[|>][-+]?", stripped):
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


def require_command(step: dict[str, object], expected: str) -> None:
    if expected not in commands(step):
        raise AssertionFailure(
            f"step {step['name']!r} does not run required command: {expected}"
        )


def assert_workflow(source: str) -> None:
    steps = parse_validate_steps(source)
    fetch = target_step(steps, "Fetch trusted and candidate policy files")
    require_command(fetch, "trusted_ref=${{ github.event.pull_request.base.sha }}")
    require_command(fetch, "candidate_ref=$GITHUB_SHA")
    require_command(
        fetch,
        '"$GITHUB_API_URL/repos/$GITHUB_REPOSITORY/contents/$source_path?ref=$ref"',
    )

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

if assert_pr_policy_workflow --decoy >/dev/null 2>&1; then
  echo "FAIL: structured workflow assertion accepted decoy comments or unused steps"
  failures=$((failures + 1))
fi

if (( failures > 0 )); then
  exit 1
fi

echo "PR metadata policy tests passed."

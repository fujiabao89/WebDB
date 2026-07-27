#!/usr/bin/env bash

set -euo pipefail

pr_title=${1-}
head_ref=${2-}
pr_body=${3-}

title_pattern='^\[(WEB-[1-9][0-9]*)\][[:space:]]+.+$'
branch_pattern='^(feat|fix|chore)/(WEB-[1-9][0-9]*)-[A-Za-z0-9][A-Za-z0-9-]*$'

if [[ ! $pr_title =~ $title_pattern ]]; then
  echo "PR 标题必须以 Linear Issue ID 开头，例如：[WEB-14] P0-04 parser spike"
  exit 1
fi
title_issue=${BASH_REMATCH[1]}

if [[ ! $head_ref =~ $branch_pattern ]]; then
  echo "PR 分支必须使用 feat|fix|chore/WEB-编号-描述，例如：feat/WEB-14-P0-04-parser-spike"
  exit 1
fi
branch_issue=${BASH_REMATCH[2]}

if [[ $title_issue != "$branch_issue" ]]; then
  echo "PR 标题中的 $title_issue 与分支中的 $branch_issue 不一致。"
  exit 1
fi

body_issue_pattern="^[[:space:]]*-[[:space:]]*Task[[:space:]]*/[[:space:]]*Issue[：:][[:space:]]*${title_issue}([^A-Z0-9-]|$)"
if ! printf '%s\n' "$pr_body" | grep -Eq "$body_issue_pattern"; then
  echo "PR 正文的 Task / Issue 必须包含与标题、分支一致的 ${title_issue}。"
  exit 1
fi

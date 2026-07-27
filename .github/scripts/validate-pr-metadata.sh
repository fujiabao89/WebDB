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

task_heading_pattern='^##[[:space:]]+任务[[:space:]]*$'
section_heading_pattern='^##[[:space:]]+'
fence_pattern='^[[:space:]]*(```|~~~)'
task_line_pattern='^[[:space:]]*-[[:space:]]*Task[[:space:]]*/[[:space:]]*Issue:[[:space:]]*(WEB-[1-9][0-9]*)(.*)$'
in_task_section=0
in_fence=0
in_html_comment=0
task_issue_count=0
body_issue=''

while IFS= read -r line || [[ -n $line ]]; do
  if (( in_html_comment )); then
    if [[ $line == *'-->'* ]]; then
      in_html_comment=0
    fi
    continue
  fi

  if (( in_fence )); then
    if [[ $line =~ $fence_pattern ]]; then
      in_fence=0
    fi
    continue
  fi

  if [[ $line == *'<!--'* ]]; then
    if [[ $line != *'-->'* ]]; then
      in_html_comment=1
    fi
    continue
  fi

  if [[ $line =~ $fence_pattern ]]; then
    in_fence=1
    continue
  fi

  if [[ $line =~ $task_heading_pattern ]]; then
    in_task_section=1
    continue
  fi
  if [[ $line =~ $section_heading_pattern ]]; then
    in_task_section=0
    continue
  fi

  normalized_line=${line//：/:}
  if (( in_task_section )) && [[ $normalized_line =~ $task_line_pattern ]]; then
    candidate_issue=${BASH_REMATCH[1]}
    issue_suffix=${BASH_REMATCH[2]}
    if [[ ! $issue_suffix =~ ^[A-Za-z0-9-] ]]; then
      task_issue_count=$((task_issue_count + 1))
      body_issue=$candidate_issue
    fi
  fi
done <<< "$pr_body"

if (( task_issue_count != 1 )); then
  echo "PR 正文的 ## 任务 区段必须恰好包含一条有效的 Task / Issue。"
  exit 1
fi

if [[ $body_issue != "$title_issue" ]]; then
  echo "PR 正文 Task / Issue 中的 $body_issue 与标题、分支中的 $title_issue 不一致。"
  exit 1
fi

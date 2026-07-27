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

required_sections=('任务' '改动与风险' '验证证据' 'WebDB 安全核对' 'AI 协作与交接')
required_section_pattern='^##[[:space:]]+(任务|改动与风险|验证证据|WebDB 安全核对|AI 协作与交接)[[:space:]]*$'
section_heading_pattern='^##[[:space:]]+'
fence_pattern='^[[:blank:]]{0,3}(`{3,}|~{3,})'
task_line_pattern='^[[:space:]]*-[[:space:]]*Task[[:space:]]*/[[:space:]]*Issue:[[:space:]]*(WEB-[1-9][0-9]*)(.*)$'
declare -A seen_sections=()
in_task_section=0
in_fence=0
fence_char=''
fence_length=0
in_html_comment=0
task_issue_count=0
body_issue=''

while IFS= read -r line || [[ -n $line ]]; do
  line=${line%$'\r'}

  if (( in_html_comment )); then
    if [[ $line == *'-->'* ]]; then
      in_html_comment=0
    fi
    continue
  fi

  if (( in_fence )); then
    closing_fence_pattern="^[[:blank:]]{0,3}${fence_char}{${fence_length},}[[:blank:]]*$"
    if [[ $line =~ $closing_fence_pattern ]]; then
      in_fence=0
      fence_char=''
      fence_length=0
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
    fence_marker=${BASH_REMATCH[1]}
    in_fence=1
    fence_char=${fence_marker:0:1}
    fence_length=${#fence_marker}
    continue
  fi

  if [[ $line =~ $required_section_pattern ]]; then
    section_name=${BASH_REMATCH[1]}
    seen_sections["$section_name"]=1
    if [[ $section_name == '任务' ]]; then
      in_task_section=1
    else
      in_task_section=0
    fi
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

for required_section in "${required_sections[@]}"; do
  if [[ -z ${seen_sections["$required_section"]+present} ]]; then
    echo "缺少 PR 模板章节：## $required_section"
    exit 1
  fi
done

if (( task_issue_count != 1 )); then
  echo "PR 正文的 ## 任务 区段必须恰好包含一条有效的 Task / Issue。"
  exit 1
fi

if [[ $body_issue != "$title_issue" ]]; then
  echo "PR 正文 Task / Issue 中的 $body_issue 与标题、分支中的 $title_issue 不一致。"
  exit 1
fi

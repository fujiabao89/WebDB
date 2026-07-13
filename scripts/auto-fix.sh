#!/bin/bash
# =============================================================================
# WebDB Auto-Fix — 轮询 Codex 审查并自动调用 Claude Code 修复
# =============================================================================
# 用法:
#   ./scripts/auto-fix.sh              # 单次检查所有打开的 PR
#   ./scripts/auto-fix.sh <PR-NUMBER>  # 只检查指定 PR
#
# 依赖: gh CLI (已登录), claude CLI (已登录)
#
# 工作流程:
#   1. 查找打开的 PR → 获取最新 Codex 审查
#   2. 判断审查是否未处理（通过 .auto-fix/ 状态文件追踪）
#   3. 分类 P0 → 升级人工 / P1/P2 → Claude 修复
#   4. 修复后推送 → 更新轮次标签 → 触发 @codex review
#   5. 停止条件: P0 / 超轮次 / 审查无问题
# =============================================================================

set -euo pipefail

# ========== 配置 ==========
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO="fujiabao89/WebDB"
# 优先使用环境变量，其次 git rev-parse，最后从脚本位置推导
PROJECT_DIR="${WEBDB_PROJECT_DIR:-$(git rev-parse --show-toplevel 2>/dev/null || echo "$(cd "$SCRIPT_DIR/.." && pwd)")}"
STATE_DIR="$PROJECT_DIR/.auto-fix"
MAX_ROUNDS=3
CODEX_LOGIN="chatgpt-codex-connector[bot]"

# ========== 初始化 ==========
mkdir -p "$STATE_DIR"
cd "$PROJECT_DIR"

# 确保 gh 已登录
gh auth status >/dev/null 2>&1 || { echo "❌ gh 未登录，请先运行: gh auth login"; exit 1; }

# ========== 辅助函数 ==========

# 获取 PR 的当前自动修复轮次（从 label 读取）
get_round() {
    local pr=$1
    local labels
    labels=$(gh pr view "$pr" --json labels -q '.labels[].name' 2>/dev/null || echo "")
    local round=0
    for i in $(seq 1 $MAX_ROUNDS); do
        if echo "$labels" | grep -q "auto-fix-round-$i"; then
            round=$i
        fi
    done
    echo "$round"
}

# 检查 PR 是否来自 fork
is_fork() {
    local pr=$1
    local head_owner
    head_owner=$(gh pr view "$pr" --json headRepositoryOwner --jq '.headRepositoryOwner.login' 2>/dev/null)
    [ "$head_owner" != "fujiabao89" ]
}

# 获取 Codex 对该 PR 的最新审查
get_latest_codex_review() {
    local pr=$1
    gh api "repos/$REPO/pulls/$pr/reviews" --jq "
        [.[] | select(.user.login == \"$CODEX_LOGIN\" and .state == \"COMMENTED\" and .submitted_at != null)]
        | sort_by(.submitted_at)
        | last
    " 2>/dev/null
}

# 获取审查中的行内评论内容
get_review_comments() {
    local pr=$1
    local review_id=$2
    gh api "repos/$REPO/pulls/$pr/reviews/$review_id/comments" --jq '
        .[]
        | "---\n**文件:** `\(.path)` 行 \(.line // "N/A")\n\n\(.body)\n"
    ' 2>/dev/null
}

# 检查审查是否已被处理（通过 .auto-fix/ 状态文件）
is_review_unprocessed() {
    local pr=$1
    local review_id=$2
    local state_file="$STATE_DIR/pr-${pr}-last-review"

    if [ -f "$state_file" ]; then
        local last_id
        last_id=$(cat "$state_file")
        if [ "$review_id" -le "$last_id" ]; then
            return 1  # 已处理
        fi
    fi
    return 0  # 未处理
}

# 标记审查为已处理
mark_review_processed() {
    local pr=$1
    local review_id=$2
    echo "$review_id" > "$STATE_DIR/pr-${pr}-last-review"
}

# 分类审查严重级别
classify_severity() {
    local content=$1

    # 检测 P0 / CRITICAL
    if echo "$content" | grep -qiE '\!\[P0 Badge\]|P0-critical|severity.*critical|CRITICAL|credential.*exposed|data.*loss|security.*vulnerability'; then
        echo "P0"
        return
    fi

    # 检测 P1 / HIGH
    if echo "$content" | grep -qiE '\!\[P1 Badge\]|P1-orange|severity.*high\b'; then
        echo "P1_P2"
        return
    fi

    # 检测 P2 / MEDIUM
    if echo "$content" | grep -qiE '\!\[P2 Badge\]|P2-yellow|severity.*medium'; then
        echo "P1_P2"
        return
    fi

    echo "NONE"
}

# 执行 Claude Code 修复
run_claude_fix() {
    local pr=$1
    local review_file=$2
    local branch
    branch=$(gh pr view "$pr" --json headRefName -q '.headRefName')
    local STASHED=false

    # 退出时恢复 stash（如有）
    cleanup_stash() {
        if $STASHED; then
            echo "📦 恢复之前保存的工作区变更..."
            git stash pop 2>/dev/null || echo "⚠️  无法恢复 stash（可能有冲突，请手动 git stash list 查看）"
        fi
    }
    trap cleanup_stash RETURN

    echo ""
    echo "┌──────────────────────────────────────────────┐"
    echo "│ 🤖 Claude Code 自动修复                       │"
    echo "│ PR: #$pr  分支: $branch                        │"
    echo "└──────────────────────────────────────────────┘"
    echo ""

    # 保存当前工作区（如有未提交变更）
    if ! git diff --quiet 2>/dev/null || ! git diff --cached --quiet 2>/dev/null; then
        echo "📦 保存当前工作区变更..."
        git stash push -m "auto-fix: 切换分支前自动保存" 2>/dev/null && STASHED=true
    fi

    # 检出 PR 分支
    git fetch origin "$branch" 2>/dev/null
    git checkout "$branch" 2>/dev/null || { echo "❌ 无法切换到分支 $branch"; return 1; }
    git pull origin "$branch" 2>/dev/null

    # 保存当前审查内容
    local review_content
    review_content=$(cat "$review_file")

    # 通过 stdin 传入 prompt（claude -p 从 stdin 读取）
    # 注意: claude 命令在项目目录执行以读取 CLAUDE.md 上下文
    local claude_output
    claude_output=$(echo "$review_content" | claude -p "你是 WebDB 项目的自动修复 Agent。阅读以下 Codex 审查意见，仅修复 P1(HIGH) 和 P2(MEDIUM) 问题。禁止: 改架构/API/Schema、动密钥凭证、改 CI/CD 配置、无关重构。修复完成后不要提交或推送，脚本会自动处理后续步骤。" --dangerously-skip-permissions --max-turns 15 --output-format text 2>&1) || true

    echo "$claude_output"

    # 检查是否有变更
    if ! git diff --quiet -- . ':(exclude).auto-fix' 2>/dev/null || ! git diff --cached --quiet -- . ':(exclude).auto-fix' 2>/dev/null; then
        echo ""
        echo "📦 检测到代码变更，提交并推送..."
        # 暂存所有变更（含新文件），排除元数据目录
        git add --all -- . ':!'.auto-fix' ':!'.codex' ':!'frontend-design'
        git commit -m "fix: 根据 Codex 审查意见自动修复 (PR #$pr)

Claude Code 根据 chatgpt-codex-connector 的审查意见修复了 P1/P2 级别问题。
此提交由 auto-fix.sh 自动生成。"
        git push origin "$branch"
        echo "✅ 修复已推送到 origin/$branch"
        return 0
    else
        echo ""
        echo "ℹ️  没有检测到代码变更"
        return 1
    fi
}

# ========== 核心: 处理单个 PR ==========
process_pr() {
    local pr=$1

    echo ""
    echo "══════════════════════════════════════════════"
    echo "  PR #$pr"
    echo "══════════════════════════════════════════════"

    # ── 守卫: fork PR ──
    if is_fork "$pr"; then
        echo "⏭️  来自 fork 仓库，跳过（安全策略）"
        return
    fi

    # ── 守卫: 轮次上限 ──
    local round
    round=$(get_round "$pr")
    echo "自动修复轮次: $round / $MAX_ROUNDS"

    if [ "$round" -ge "$MAX_ROUNDS" ]; then
        if ! gh pr view "$pr" --json labels -q '.labels[].name' 2>/dev/null | grep -q "human-review"; then
            echo "🛑 已达最大轮次 → 添加 human-review 标签"
            gh pr edit "$pr" --add-label "human-review"
            gh pr comment "$pr" --body "## 🔍 自动修复已达上限

已完成 **$MAX_ROUNDS 轮**自动修复。请进行人工审查决定是否合并。

> 🤖 此评论由 auto-fix.sh 自动生成"
        fi
        return
    fi

    # ── 获取最新 Codex 审查 ──
    local review_json
    review_json=$(get_latest_codex_review "$pr")

    if [ -z "$review_json" ] || [ "$review_json" = "null" ]; then
        echo "ℹ️  没有 Codex 审查记录"
        return
    fi

    local review_id
    review_id=$(echo "$review_json" | node -e "process.stdin.on('data',d=>{console.log(JSON.parse(d).id)})" 2>/dev/null || echo "0")
    local review_time
    review_time=$(echo "$review_json" | node -e "process.stdin.on('data',d=>{console.log(JSON.parse(d).submitted_at)})" 2>/dev/null || echo "")

    echo "Codex 审查 ID: $review_id ($review_time)"

    # ── 检查是否已处理 ──
    if ! is_review_unprocessed "$pr" "$review_id"; then
        echo "ℹ️  此审查已处理过，跳过"
        return
    fi

    # ── 获取审查评论内容 ──
    local review_file
    review_file=$(mktemp)
    get_review_comments "$pr" "$review_id" > "$review_file"

    if [ ! -s "$review_file" ]; then
        echo "ℹ️  审查无具体评论（Codex 可能只给了 👍），标记已处理"
        mark_review_processed "$pr" "$review_id"
        rm -f "$review_file"

        # Codex 审查无问题 → 可人工审查
        if ! gh pr view "$pr" --json labels -q '.labels[].name' 2>/dev/null | grep -q "human-review"; then
            echo "✅ Codex 审查通过，添加 human-review 标签"
            gh pr edit "$pr" --add-label "human-review"
        fi
        return
    fi

    local review_content
    review_content=$(cat "$review_file")

    echo ""
    echo "── Codex 审查摘要（前 10 行）──"
    head -10 "$review_file"
    echo "..."

    # ── 分类严重级别 ──
    local severity
    severity=$(classify_severity "$review_content")
    echo ""
    echo "严重级别判定: $severity"

    case "$severity" in
        P0)
            echo ""
            echo "🚨 发现 P0/CRITICAL 问题！自动修复不处理 P0，转人工。"
            gh pr edit "$pr" --add-label "needs-human-review"
            gh pr comment "$pr" --body "## ⚠️ P0/CRITICAL — 需要人工介入

Codex 审查发现了 **P0 / CRITICAL** 级别的问题，自动修复流程已暂停。

请人工审查: https://github.com/$REPO/pull/$pr#pullrequestreview-$review_id

> 🤖 此评论由 auto-fix.sh 自动生成"
            mark_review_processed "$pr" "$review_id"
            ;;

        P1_P2)
            echo "🔧 发现 P1/P2 问题，启动 Claude Code 修复..."

            if run_claude_fix "$pr" "$review_file"; then
                local new_round=$((round + 1))
                if [ "$round" -gt 0 ]; then
                    gh pr edit "$pr" --remove-label "auto-fix-round-$round" 2>/dev/null || true
                fi
                gh pr edit "$pr" --add-label "auto-fix-round-$new_round"
                echo "✅ 轮次: $round → $new_round"

                if [ "$new_round" -ge "$MAX_ROUNDS" ]; then
                    gh pr edit "$pr" --add-label "human-review"
                    gh pr comment "$pr" --body "## 🔍 自动修复已达上限 ($MAX_ROUNDS 轮)

Claude Code 已完成最后一轮自动修复。请进行人工审查。

> 🤖 此评论由 auto-fix.sh 自动生成"
                fi

                echo "📢 触发 Codex 重新审查..."
                gh pr comment "$pr" --body "@codex review"

                mark_review_processed "$pr" "$review_id"
            else
                echo "⚠️  修复未产生代码变更，保留审查状态供下次重试"
            fi
            ;;

        NONE)
            echo "ℹ️  审查中未发现 P0/P1/P2 级别问题"
            mark_review_processed "$pr" "$review_id"

            # 之前修复过 → 审查通过 → 可人工审查
            if [ "$round" -gt 0 ]; then
                if ! gh pr view "$pr" --json labels -q '.labels[].name' 2>/dev/null | grep -q "human-review"; then
                    echo "✅ 经过 $round 轮修复后 Codex 审查通过，添加 human-review"
                    gh pr edit "$pr" --add-label "human-review"
                fi
            fi
            ;;
    esac

    rm -f "$review_file"
}

# ========== 主入口 ==========
main() {
    echo "WebDB Auto-Fix — $(date '+%Y-%m-%d %H:%M:%S')"
    echo "仓库: $REPO"
    echo ""

    git fetch origin 2>/dev/null

    if [ $# -ge 1 ]; then
        process_pr "$1"
    else
        local open_prs
        open_prs=$(gh pr list --state open --json number -q '.[].number' 2>/dev/null)

        if [ -z "$open_prs" ]; then
            echo "没有打开的 PR"
            exit 0
        fi

        for pr in $open_prs; do
            process_pr "$pr"
        done
    fi

    echo ""
    echo "══════════════════════════════════════════════"
    echo "  完成 — $(date '+%Y-%m-%d %H:%M:%S')"
    echo "══════════════════════════════════════════════"
}

main "$@"

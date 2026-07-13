#!/bin/bash
# =============================================================================
# WebDB Auto-Fix Watch — 持续监视模式
# =============================================================================
# 用法:
#   ./scripts/auto-fix-watch.sh            # 每 5 分钟检查一次
#   ./scripts/auto-fix-watch.sh 300        # 自定义间隔（秒）
#
# 按 Ctrl+C 停止监视
# =============================================================================

set -euo pipefail

INTERVAL="${1:-300}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
AUTO_FIX="$SCRIPT_DIR/auto-fix.sh"

if [ ! -f "$AUTO_FIX" ]; then
    echo "❌ 找不到 auto-fix.sh，请确保两个脚本在同一目录"
    exit 1
fi

echo "╔══════════════════════════════════════════════════════╗"
echo "║  WebDB Auto-Fix Watch — 持续监视模式                 ║"
echo "║  间隔: ${INTERVAL}s    按 Ctrl+C 停止                ║"
echo "╚══════════════════════════════════════════════════════╝"
echo ""

trap 'echo ""; echo "👋 监视已停止"; exit 0' INT TERM

while true; do
    echo "━━━━━━━━━━━━━━━━━━━━ $(date '+%H:%M:%S') ━━━━━━━━━━━━━━━━━━━━"
    bash "$AUTO_FIX" || echo "⚠️  auto-fix 执行异常（错误码: $?），继续监视..."
    echo ""
    echo "下次检查: ${INTERVAL}秒后"
    sleep "$INTERVAL"
done

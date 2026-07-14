#!/usr/bin/env bash
# Windows 原生入口：统一交给 PowerShell，避免 Git Bash/WSL 路径与 JSON 解析差异。
set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
exec powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$SCRIPT_DIR/auto-fix.ps1" "$@"

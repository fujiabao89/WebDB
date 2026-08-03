#!/bin/bash
# 回归测试：verify-prod-roles.sh 的清理路径（成功 / 失败 / 中断）+ create 缺失 setsid 失败路径
# ============================================================
# 覆盖：
#   1) 成功：verify 完整执行 → exit 0 且零残留
#   2) 失败：数据创建后注入 exit 3 → 保留原始退出码 3 且零残留
#   3) 中断：注入 kill -TERM "$$" 真实信号 → TERM trap（cleanup_and_exit）→ exit 130 且零残留
#   4) 缺失 setsid：create 应立即拒绝（fail-closed）且给出明确错误
# 说明：自动/后台环境下 bash 继承 SIG_IGN，无法用 SIGINT 复现真实 Ctrl-C；SIGTERM 不受影响，
#       故中断项用 TERM 自信号验证完整信号驱动路径（trap 接线 → 清理 → exit 130）。
# 前置：生产角色已创建（webdb_app_runtime / webdb_audit_writer），psql 与 setsid 可连接/可用。
# 用法：与 verify-prod-roles.sh 相同的环境变量（POSTGRES_USER/PASSWORD、DB、PGHOST/PGPORT、
#       WEBDB_APP_*、WEBDB_AUDIT_*、VERIFY_PROD_LOG_SOURCE）。不修改生产角色。
set -euo pipefail

fail() { echo "回归失败: $*" >&2; exit 1; }

# 私有工作区（mktemp -d），存放生成的脚本与日志；退出时清理
WS=$(mktemp -d "${TMPDIR:-/tmp}/prod_roles_cleanup.XXXXXX") || fail "无法创建临时工作区"
trap 'rm -rf "$WS"' EXIT

ADMIN_USER="${POSTGRES_USER:-postgres}"
ADMIN_PASSWORD="${POSTGRES_PASSWORD:-}"
DB_NAME="webdb_meta"
PGHOST="${PGHOST:-localhost}"
PGPORT="${PGPORT:-5432}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
VERIFY_SCRIPT="$SCRIPT_DIR/verify-prod-roles.sh"
CREATE_SCRIPT="$SCRIPT_DIR/init/prod-roles/01-create-prod-roles.sh"
[ -f "$VERIFY_SCRIPT" ] || fail "找不到 verify 脚本: $VERIFY_SCRIPT"
[ -f "$CREATE_SCRIPT" ] || fail "找不到 create 脚本: $CREATE_SCRIPT"

# 残留检查：合成 FK 数据与临时验证角色应全部清理
check_residuals() {
  local ws users roles
  ws=$(PGPASSWORD="$ADMIN_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" -tA \
    -c "SELECT count(*) FROM workspaces WHERE name LIKE 'verify-insert-%';" | tr -d ' \n')
  users=$(PGPASSWORD="$ADMIN_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" -tA \
    -c "SELECT count(*) FROM users WHERE email LIKE 'verify-insert-%';" | tr -d ' \n')
  roles=$(PGPASSWORD="$ADMIN_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" -tA \
    -c "SELECT count(*) FROM pg_roles WHERE rolname LIKE 'webdb_audit_probe_%' OR rolname LIKE 'webdb_pw_probe_%';" | tr -d ' \n')
  [ "$ws" = "0" ] && [ "$users" = "0" ] && [ "$roles" = "0" ] \
    || fail "残留未清理: workspaces=$ws users=$users probe_roles=$roles"
}

echo "=== 回归 1/4：成功路径（exit 0 + 零残留） ==="
set +e
bash "$VERIFY_SCRIPT" >"$WS/success.log" 2>&1
rc=$?
set -e
if [ "$rc" -ne 0 ]; then tail -8 "$WS/success.log"; fail "成功路径 exit=$rc 期望 0"; fi
check_residuals
echo "OK: 成功路径 exit 0 且零残留"

echo "=== 回归 2/4：失败路径（数据创建后注入 exit 3，保留退出码 + 零残留） ==="
cp "$VERIFY_SCRIPT" "$WS/fail.sh"
sed -i 's/^WM_CREATED=1$/WM_CREATED=1\nexit 3  # 注入失败点/' "$WS/fail.sh"
set +e
bash "$WS/fail.sh" >"$WS/fail.log" 2>&1
rc=$?
set -e
if [ "$rc" -ne 3 ]; then tail -8 "$WS/fail.log"; fail "失败路径 exit=$rc 期望 3"; fi
check_residuals
echo "OK: 失败路径保留退出码 3 且零残留"

echo "=== 回归 3/4：中断路径（注入 kill -TERM \"\$\$\" 真实信号，exit 130 + 零残留） ==="
cp "$VERIFY_SCRIPT" "$WS/int.sh"
sed -i 's/^WM_CREATED=1$/WM_CREATED=1\nkill -TERM "$$"  # 注入中断（真实信号路径）/' "$WS/int.sh"
set +e
bash "$WS/int.sh" >"$WS/int.log" 2>&1
rc=$?
set -e
if [ "$rc" -ne 130 ]; then tail -8 "$WS/int.log"; fail "中断路径 exit=$rc 期望 130"; fi
check_residuals
echo "OK: 中断路径（TERM 信号驱动）exit 130 且零残留"

echo "=== 回归 4/4：缺失 setsid 失败路径（create 应拒绝且不修改角色） ==="
# 用只含临时空目录的私有 PATH 模拟无 setsid 环境：
#   - 避免原 PATH 过滤管道（tr|grep|paste）在 set -euo pipefail 下无匹配返回非零导致脚本退出，
#   - 避免过滤后 PATH 仍含其他位置的 setsid；
#   - 密码通过子 shell 的 export 传入（不作为 env 工具 argv，避免出现在进程列表）。
mkdir -p "$WS/emptybin"
BASH_ABS=$(command -v bash)
# 前置断言：私有 PATH 下 command -v setsid 必须失败，再运行 CREATE_SCRIPT
if PATH="$WS/emptybin" command -v setsid >/dev/null 2>&1; then
  fail "前置断言失败：私有 PATH 下仍能定位 setsid"
fi
set +e
(
  export WEBDB_PRODUCTION_DEPLOY=1
  export POSTGRES_USER="$ADMIN_USER" POSTGRES_PASSWORD="$ADMIN_PASSWORD" POSTGRES_DB="$DB_NAME"
  export WEBDB_APP_PASSWORD='X@1' WEBDB_AUDIT_PASSWORD='Y@2'
  export PATH="$WS/emptybin"
  "$BASH_ABS" "$CREATE_SCRIPT"
) >"$WS/setsid.log" 2>&1
rc=$?
set -e
if [ "$rc" -eq 0 ]; then fail "缺失 setsid 时 create 应失败（got exit 0）"; fi
grep -q "需要 setsid" "$WS/setsid.log" || fail "缺失 setsid 错误信息缺失: $(tail -3 "$WS/setsid.log")"
echo "OK: 缺失 setsid 时 create 拒绝（exit=$rc）且给出明确错误"

echo ""
echo "清理路径回归全部通过（成功 / 失败 / 中断）+ 缺失 setsid 拒绝。"

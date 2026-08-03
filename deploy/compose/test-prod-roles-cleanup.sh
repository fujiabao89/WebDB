#!/bin/bash
# 回归测试：verify-prod-roles.sh 的清理路径（成功 / 失败 / 中断）——PR37 二轮审查项
# ============================================================
# 覆盖三个清理结果：
#   1) 成功：verify 完整执行 → exit 0 且零残留
#   2) 失败：数据创建后注入 exit 3 → 保留原始退出码 3 且零残留
#   3) 中断：调用 INT/TERM trap 处理器 cleanup_and_exit → exit 130 且零残留
# 说明：自动/后台环境下 bash 继承 SIG_IGN，无法复现真实终端 Ctrl-C 向进程组投递的
#       SIGINT；故第 3 项确定性注入处理器调用，覆盖 trap 所调用的清理逻辑。
# 前置：生产角色已创建（webdb_app_runtime / webdb_audit_writer），psql 可连接。
# 用法：与 verify-prod-roles.sh 相同的环境变量（POSTGRES_USER/PASSWORD、DB、PGHOST/PGPORT、
#       WEBDB_APP_*、WEBDB_AUDIT_*、VERIFY_PROD_LOG_SOURCE）。不修改生产角色。
set -euo pipefail

fail() { echo "回归失败: $*" >&2; exit 1; }

ADMIN_USER="${POSTGRES_USER:-postgres}"
ADMIN_PASSWORD="${POSTGRES_PASSWORD:-}"
DB_NAME="webdb_meta"
PGHOST="${PGHOST:-localhost}"
PGPORT="${PGPORT:-5432}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
VERIFY_SCRIPT="$SCRIPT_DIR/verify-prod-roles.sh"
[ -f "$VERIFY_SCRIPT" ] || fail "找不到 verify 脚本: $VERIFY_SCRIPT"

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

echo "=== 回归 1/3：成功路径（exit 0 + 零残留） ==="
set +e
bash "$VERIFY_SCRIPT" >/tmp/prod_roles_success.log 2>&1
rc=$?
set -e
if [ "$rc" -ne 0 ]; then tail -8 /tmp/prod_roles_success.log; fail "成功路径 exit=$rc 期望 0"; fi
check_residuals
echo "OK: 成功路径 exit 0 且零残留"

echo "=== 回归 2/3：失败路径（数据创建后注入 exit 3，保留退出码 + 零残留） ==="
cp "$VERIFY_SCRIPT" /tmp/prod_roles_fail.sh
sed -i 's/^WM_CREATED=1$/WM_CREATED=1\nexit 3  # 注入失败点/' /tmp/prod_roles_fail.sh
set +e
bash /tmp/prod_roles_fail.sh >/tmp/prod_roles_fail.log 2>&1
rc=$?
set -e
if [ "$rc" -ne 3 ]; then tail -8 /tmp/prod_roles_fail.log; fail "失败路径 exit=$rc 期望 3"; fi
check_residuals
echo "OK: 失败路径保留退出码 3 且零残留"

echo "=== 回归 3/3：中断路径（调用 INT/TERM 处理器，exit 130 + 零残留） ==="
cp "$VERIFY_SCRIPT" /tmp/prod_roles_int.sh
sed -i 's/^WM_CREATED=1$/WM_CREATED=1\ncleanup_and_exit  # 模拟中断路径/' /tmp/prod_roles_int.sh
set +e
bash /tmp/prod_roles_int.sh >/tmp/prod_roles_int.log 2>&1
rc=$?
set -e
if [ "$rc" -ne 130 ]; then tail -8 /tmp/prod_roles_int.log; fail "中断路径 exit=$rc 期望 130"; fi
check_residuals
echo "OK: 中断路径 exit 130 且零残留"

echo ""
echo "清理路径回归全部通过（成功 / 失败 / 中断）。"

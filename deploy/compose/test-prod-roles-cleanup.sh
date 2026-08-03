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
APP_PASSWORD="${WEBDB_APP_PASSWORD:-}"
AUDIT_PASSWORD="${WEBDB_AUDIT_PASSWORD:-}"
DB_NAME="webdb_meta"
PGHOST="${PGHOST:-localhost}"
PGPORT="${PGPORT:-5432}"

# 密码非空且非占位符（与 verify-prod-roles.sh 一致；密码只经环境变量传入，
# 绝不作命令行参数、不写入日志）。PR37 五轮审查项。
validate_pw() {
  local name="$1" val="${2:-}"
  if [ -z "$val" ] || [ "$val" = "change_me" ] || [ "$val" = "changeme" ]; then
    fail "$name 必须为非空且非占位符"
  fi
}
validate_pw POSTGRES_PASSWORD "$ADMIN_PASSWORD"
validate_pw WEBDB_APP_PASSWORD "$APP_PASSWORD"
validate_pw WEBDB_AUDIT_PASSWORD "$AUDIT_PASSWORD"

# 目标数据库约束：回归测试会创建/删除临时角色与合成数据，只允许部署文档规定的 loopback 或
# 已批准的 Compose 服务目标（localhost/127.0.0.1/::1/webdb-meta）与端口 5432；
# 未知目标必须在任何 psql/脚本调用前立即失败，防止误连远程/生产库（PR37 六轮审查项）。
validate_target() {
  case "$PGHOST" in
    localhost|127.0.0.1|::1|webdb-meta) ;;
    *) fail "PGHOST 必须为 loopback 或已批准的 Compose 服务目标（localhost/127.0.0.1/::1/webdb-meta），当前: [$PGHOST]；拒绝连接以防误操作远程/生产库" ;;
  esac
  case "$PGPORT" in
    5432) ;;
    *) fail "PGPORT 必须为 5432（Compose 默认映射），当前: [$PGPORT]" ;;
  esac
  # PGHOSTADDR 会绕过 PGHOST 直接指定连接地址（libpq），必须为空（PR37 七轮审查项）
  if [ -n "${PGHOSTADDR:-}" ]; then
    fail "PGHOSTADDR 必须为空（会绕过 PGHOST 直接指定连接地址），当前: [$PGHOSTADDR]"
  fi
  # PGSERVICE/PGSERVICEFILE/PGSYSCONFDIR 可通过服务文件指定 hostaddr 绕过 PGHOST，必须为空（PR37 八轮审查项）
  if [ -n "${PGSERVICE:-}" ] || [ -n "${PGSERVICEFILE:-}" ] || [ -n "${PGSYSCONFDIR:-}" ]; then
    fail "PGSERVICE/PGSERVICEFILE/PGSYSCONFDIR 必须为空（服务文件可含 hostaddr 绕过 PGHOST）；当前 PGSERVICE=[${PGSERVICE:-}] PGSERVICEFILE=[${PGSERVICEFILE:-}] PGSYSCONFDIR=[${PGSYSCONFDIR:-}]"
  fi
}
validate_target

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

echo "=== 回归 1/6：成功路径（exit 0 + 零残留） ==="
set +e
bash "$VERIFY_SCRIPT" >"$WS/success.log" 2>&1
rc=$?
set -e
if [ "$rc" -ne 0 ]; then tail -8 "$WS/success.log"; fail "成功路径 exit=$rc 期望 0"; fi
check_residuals
echo "OK: 成功路径 exit 0 且零残留"

echo "=== 回归 2/6：失败路径（数据创建后注入 exit 3，保留退出码 + 零残留） ==="
cp "$VERIFY_SCRIPT" "$WS/fail.sh"
sed -i 's/^WM_CREATED=1$/WM_CREATED=1\nexit 3  # 注入失败点/' "$WS/fail.sh"
set +e
bash "$WS/fail.sh" >"$WS/fail.log" 2>&1
rc=$?
set -e
if [ "$rc" -ne 3 ]; then tail -8 "$WS/fail.log"; fail "失败路径 exit=$rc 期望 3"; fi
check_residuals
echo "OK: 失败路径保留退出码 3 且零残留"

echo "=== 回归 3/6：中断路径（注入 kill -TERM \"\$\$\" 真实信号，exit 130 + 零残留） ==="
cp "$VERIFY_SCRIPT" "$WS/int.sh"
sed -i 's/^WM_CREATED=1$/WM_CREATED=1\nkill -TERM "$$"  # 注入中断（真实信号路径）/' "$WS/int.sh"
set +e
bash "$WS/int.sh" >"$WS/int.log" 2>&1
rc=$?
set -e
if [ "$rc" -ne 130 ]; then tail -8 "$WS/int.log"; fail "中断路径 exit=$rc 期望 130"; fi
check_residuals
echo "OK: 中断路径（TERM 信号驱动）exit 130 且零残留"

echo "=== 回归 4/6：缺失 setsid 失败路径（create 应拒绝且不修改角色） ==="
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
  export WEBDB_APP_PASSWORD="$APP_PASSWORD"
  export WEBDB_AUDIT_PASSWORD="$AUDIT_PASSWORD"
  export PATH="$WS/emptybin"
  "$BASH_ABS" "$CREATE_SCRIPT"
) >"$WS/setsid.log" 2>&1
rc=$?
set -e
if [ "$rc" -eq 0 ]; then fail "缺失 setsid 时 create 应失败（got exit 0）"; fi
grep -q "需要 setsid" "$WS/setsid.log" || fail "缺失 setsid 错误信息缺失: $(tail -3 "$WS/setsid.log")"
echo "OK: 缺失 setsid 时 create 拒绝（exit=$rc）且给出明确错误"

echo "=== 回归 5/6：目标约束负向回归（远程 PGHOST / 非法端口连接前拒绝） ==="
if ( PGHOST="203.0.113.10" PGPORT="5432" validate_target ) >/dev/null 2>&1; then
  fail "负向断言失败：远程 PGHOST=[203.0.113.10] 应被拒绝"
fi
if ( PGHOST="localhost" PGPORT="9999" validate_target ) >/dev/null 2>&1; then
  fail "负向断言失败：非法端口 9999 应被拒绝"
fi
if ( PGHOSTADDR="203.0.113.20" validate_target ) >/dev/null 2>&1; then
  fail "负向断言失败：非空 PGHOSTADDR 应被拒绝"
fi
echo "OK: 远程 PGHOST / 非法端口 / 非空 PGHOSTADDR 在连接前被拒绝"

echo "=== 回归 6/6：服务文件绕过负向回归（PGSERVICEFILE 含 hostaddr 连接前拒绝） ==="
# 临时 pg_service.conf：条目含 hostaddr，验证 PGSERVICE/PGSERVICEFILE/PGSYSCONFDIR 被连接前拒绝
cat >"$WS/pg_service.conf" <<EOF
[malicious]
hostaddr=203.0.113.30
port=5432
EOF
if ( PGSERVICEFILE="$WS/pg_service.conf" validate_target ) >/dev/null 2>&1; then
  fail "负向断言失败：PGSERVICEFILE 非空应被拒绝"
fi
if ( PGSERVICE="malicious" validate_target ) >/dev/null 2>&1; then
  fail "负向断言失败：PGSERVICE 非空应被拒绝"
fi
if ( PGSYSCONFDIR="$WS" validate_target ) >/dev/null 2>&1; then
  fail "负向断言失败：PGSYSCONFDIR 非空应被拒绝"
fi
echo "OK: PGSERVICEFILE/PGSERVICE/PGSYSCONFDIR 在连接前被拒绝"

echo ""
echo "清理路径回归全部通过（成功 / 失败 / 中断 / 缺失 setsid / 目标约束负向回归 / 服务文件绕过）。"

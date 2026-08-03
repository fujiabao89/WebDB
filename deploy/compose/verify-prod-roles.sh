#!/bin/bash
# 验证生产角色拆分（WEB-27 / R6）
# ============================================================
# 断言：
#   1) 非 SUPERUSER 运行时角色对 audit_events 的 UPDATE/DELETE/TRUNCATE 被拒绝（ACL）；
#   2) audit_events 的 SELECT/INSERT 允许，UPDATE/DELETE/TRUNCATE 无权限；
#   3) 独立验证角色（临时创建、仅验证期授予写权限、非 SUPERUSER）对 audit_events 的
#      UPDATE/DELETE/TRUNCATE 由 deny_audit_mutation **触发器**拒绝（而非 ACL）。
# 用法：需要管理员凭证（POSTGRES_USER/POSTGRES_PASSWORD）创建临时验证角色。
set -e

APP_USER="${WEBDB_APP_USER:-webdb_app_runtime}"
APP_PASSWORD="${WEBDB_APP_PASSWORD:-}"
AUDIT_USER="${WEBDB_AUDIT_USER:-webdb_audit_writer}"
AUDIT_PASSWORD="${WEBDB_AUDIT_PASSWORD:-}"
ADMIN_USER="${POSTGRES_USER:-postgres}"
ADMIN_PASSWORD="${POSTGRES_PASSWORD:-}"
# 安全固定：prod 元数据库名固定为 webdb_meta，不随环境变量拼接进 SQL 标识符（防注入）。
DB_NAME="webdb_meta"
PGHOST="${PGHOST:-localhost}"
PGPORT="${PGPORT:-5432}"

fail() { echo "失败: $*" >&2; exit 1; }

# 密码非空且非占位符（与 01-create-prod-roles.sh 一致）
validate_pw() {
  local name="$1" val="${2:-}"
  if [ -z "$val" ] || [ "$val" = "change_me" ] || [ "$val" = "changeme" ]; then
    fail "$name 必须为非空且非占位符"
  fi
}
validate_pw WEBDB_APP_PASSWORD "$APP_PASSWORD"
validate_pw WEBDB_AUDIT_PASSWORD "$AUDIT_PASSWORD"
validate_pw POSTGRES_PASSWORD "$ADMIN_PASSWORD"

echo "=== 1. audit_events UPDATE 应被拒绝（应用角色，ACL） ==="
if PGPASSWORD="$APP_PASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$APP_USER" -d "$DB_NAME" \
    -c "UPDATE audit_events SET action='x' WHERE false;" >/dev/null 2>&1; then
  fail "应用角色 UPDATE audit_events 未被拒绝"
fi
echo "OK: UPDATE 被拒绝"

echo "=== 2. audit_events DELETE 应被拒绝（应用角色，ACL） ==="
if PGPASSWORD="$APP_PASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$APP_USER" -d "$DB_NAME" \
    -c "DELETE FROM audit_events WHERE false;" >/dev/null 2>&1; then
  fail "应用角色 DELETE audit_events 未被拒绝"
fi
echo "OK: DELETE 被拒绝"

echo "=== 3. audit_events TRUNCATE 应被拒绝（应用角色，ACL） ==="
if PGPASSWORD="$APP_PASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$APP_USER" -d "$DB_NAME" \
    -c "TRUNCATE audit_events;" >/dev/null 2>&1; then
  fail "应用角色 TRUNCATE audit_events 未被拒绝"
fi
echo "OK: TRUNCATE 被拒绝"

echo "=== 4. 应用角色 audit_events 权限：SELECT/INSERT=t，UPDATE/DELETE/TRUNCATE=f ==="
res=$(PGPASSWORD="$APP_PASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$APP_USER" -d "$DB_NAME" -tA \
  -c "SELECT has_table_privilege(current_user,'audit_events','SELECT'),has_table_privilege(current_user,'audit_events','INSERT'),has_table_privilege(current_user,'audit_events','UPDATE'),has_table_privilege(current_user,'audit_events','DELETE'),has_table_privilege(current_user,'audit_events','TRUNCATE');")
[ "$res" = "t|t|f|f|f" ] || fail "应用角色 audit_events 权限错误: got [$res], want t|t|f|f|f"
echo "OK: t|t|f|f|f"

echo "=== 5. 审计写入角色 audit_events 权限：SELECT/INSERT=t，UPDATE/DELETE/TRUNCATE=f ==="
res=$(PGPASSWORD="$AUDIT_PASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$AUDIT_USER" -d "$DB_NAME" -tA \
  -c "SELECT has_table_privilege(current_user,'audit_events','SELECT'),has_table_privilege(current_user,'audit_events','INSERT'),has_table_privilege(current_user,'audit_events','UPDATE'),has_table_privilege(current_user,'audit_events','DELETE'),has_table_privilege(current_user,'audit_events','TRUNCATE');")
[ "$res" = "t|t|f|f|f" ] || fail "审计写入角色 audit_events 权限错误: got [$res], want t|t|f|f|f"
echo "OK: t|t|f|f|f"

echo "=== 6. 审计写入角色 DELETE/TRUNCATE 应被拒绝（ACL） ==="
if PGPASSWORD="$AUDIT_PASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$AUDIT_USER" -d "$DB_NAME" \
    -c "DELETE FROM audit_events WHERE false;" >/dev/null 2>&1; then
  fail "审计写入角色 DELETE audit_events 未被拒绝"
fi
if PGPASSWORD="$AUDIT_PASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$AUDIT_USER" -d "$DB_NAME" \
    -c "TRUNCATE audit_events;" >/dev/null 2>&1; then
  fail "审计写入角色 TRUNCATE audit_events 未被拒绝"
fi
echo "OK: DELETE/TRUNCATE 被拒绝"

echo "=== 7. 独立验证角色（非 SUPERUSER 但持有 audit_events 写权限）→ deny_audit_mutation 触发器拒绝 ==="
PROBE=""
PROBE_PW=""
# 失败/中断（EXIT/INT/TERM）也清理临时验证角色；幂等、不覆盖原始退出状态
cleanup_probe() {
  if [ -n "$PROBE" ]; then
    PGPASSWORD="$ADMIN_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" \
      -v ON_ERROR_STOP=1 >/dev/null 2>&1 -c "DROP ROLE IF EXISTS \"$PROBE\";" || true
  fi
}
trap cleanup_probe EXIT INT TERM
PROBE="webdb_audit_probe_$$"
PROBE_PW="probe_tmp_$$"
# 创建临时验证角色（仅验证期），授予 audit_events 全部写权限（验证触发器而非 ACL）
PGPASSWORD="$ADMIN_PASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 >/dev/null 2>&1 \
  -c "DROP ROLE IF EXISTS \"$PROBE\";" \
  -c "CREATE ROLE \"$PROBE\" WITH LOGIN PASSWORD '$PROBE_PW' NOSUPERUSER NOBYPASSRLS;" \
  -c "GRANT CONNECT ON DATABASE \"$DB_NAME\" TO \"$PROBE\";" \
  -c "GRANT USAGE ON SCHEMA public TO \"$PROBE\";" \
  -c "GRANT SELECT, INSERT, UPDATE, DELETE, TRUNCATE ON audit_events TO \"$PROBE\";"

probe_try() {
  local op="$1"
  local out
  # -w 禁止密码提示；timeout 防止连接挂起
  out=$(timeout 8 env PGPASSWORD="$PROBE_PW" psql -w -h "$PGHOST" -p "$PGPORT" -U "$PROBE" -d "$DB_NAME" \
    -v ON_ERROR_STOP=0 -c "BEGIN; $op; ROLLBACK;" 2>&1 || true)
  # deny_audit_mutation 触发器对不同操作输出不同消息（不可修改/不可删除/不可清空），
  # 统一匹配前缀 "audit_events 不可"。
  if echo "$out" | grep -q "audit_events 不可"; then
    echo "OK: probe $op 被 deny_audit_mutation 触发器拒绝"
  else
    fail "probe $op 未被触发器拒绝（期望 deny_audit_mutation，实际: $out）"
  fi
}
probe_try "UPDATE audit_events SET action='x' WHERE false;"
probe_try "DELETE FROM audit_events WHERE false;"
probe_try "TRUNCATE audit_events;"

# 清理临时验证角色
PGPASSWORD="$ADMIN_PASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 >/dev/null 2>&1 \
  -c "DROP ROLE IF EXISTS \"$PROBE\";"

echo ""
echo "生产角色拆分验证全部通过（ACL + deny_audit_mutation 触发器双重确认）。"

#!/bin/bash
# 验证生产角色拆分（WEB-27 / R6）
# ============================================================
# 断言：非 SUPERUSER 运行时角色对 audit_events 的 UPDATE/DELETE/TRUNCATE 被拒绝
#       （权限不足 + deny_audit_mutation 触发器双重防护），且 SELECT/INSERT 按角色允许。
# 用法：WEBDB_APP_PASSWORD=... ./verify-prod-roles.sh
set -e

APP_USER="${WEBDB_APP_USER:-webdb_app_runtime}"
APP_PASSWORD="${WEBDB_APP_PASSWORD:-change_me}"
AUDIT_USER="${WEBDB_AUDIT_USER:-webdb_audit_writer}"
AUDIT_PASSWORD="${WEBDB_AUDIT_PASSWORD:-change_me}"
DB_NAME="${WEBDB_META_DB:-webdb_meta}"
PGHOST="${PGHOST:-localhost}"
PGPORT="${PGPORT:-5432}"

fail() { echo "失败: $*" >&2; exit 1; }

echo "=== 1. audit_events UPDATE 应被拒绝（应用角色） ==="
if PGPASSWORD="$APP_PASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$APP_USER" -d "$DB_NAME" \
    -c "UPDATE audit_events SET action='x' WHERE false;" >/dev/null 2>&1; then
  fail "应用角色 UPDATE audit_events 未被拒绝"
fi
echo "OK: UPDATE 被拒绝"

echo "=== 2. audit_events DELETE 应被拒绝（应用角色） ==="
if PGPASSWORD="$APP_PASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$APP_USER" -d "$DB_NAME" \
    -c "DELETE FROM audit_events WHERE false;" >/dev/null 2>&1; then
  fail "应用角色 DELETE audit_events 未被拒绝"
fi
echo "OK: DELETE 被拒绝"

echo "=== 3. audit_events TRUNCATE 应被拒绝（应用角色） ==="
if PGPASSWORD="$APP_PASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$APP_USER" -d "$DB_NAME" \
    -c "TRUNCATE audit_events;" >/dev/null 2>&1; then
  fail "应用角色 TRUNCATE audit_events 未被拒绝"
fi
echo "OK: TRUNCATE 被拒绝"

echo "=== 4. 应用角色应具备 audit_events SELECT+INSERT，且无 UPDATE ==="
res=$(PGPASSWORD="$APP_PASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$APP_USER" -d "$DB_NAME" -tA \
  -c "SELECT has_table_privilege(current_user,'audit_events','SELECT'), has_table_privilege(current_user,'audit_events','INSERT'), has_table_privilege(current_user,'audit_events','UPDATE');")
[ "$res" = "t|t|f" ] || fail "应用角色 audit_events 权限错误: got [$res], want t|t|f"
echo "OK: SELECT=t INSERT=t UPDATE=f"

echo "=== 5. 审计写入角色应具备 audit_events SELECT+INSERT，且无 UPDATE ==="
res=$(PGPASSWORD="$AUDIT_PASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$AUDIT_USER" -d "$DB_NAME" -tA \
  -c "SELECT has_table_privilege(current_user,'audit_events','SELECT'), has_table_privilege(current_user,'audit_events','INSERT'), has_table_privilege(current_user,'audit_events','UPDATE');")
[ "$res" = "t|t|f" ] || fail "审计写入角色 audit_events 权限错误: got [$res], want t|t|f"
echo "OK: SELECT=t INSERT=t UPDATE=f"

echo "=== 6. audit_events SELECT 可执行（应用角色） ==="
PGPASSWORD="$APP_PASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$APP_USER" -d "$DB_NAME" \
  -c "SELECT count(*) FROM audit_events;" >/dev/null
echo "OK: SELECT 允许"

echo ""
echo "生产角色拆分验证全部通过。"

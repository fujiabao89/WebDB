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
# 失败/中断（EXIT/INT/TERM）也清理临时验证角色；幂等
cleanup_probe() {
  if [ -n "$PROBE" ]; then
    # 角色可能在正常路径已清理：DROP ROLE IF EXISTS 幂等成功则结束
    if PGPASSWORD="$ADMIN_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" \
      -v ON_ERROR_STOP=1 >/dev/null 2>&1 -c "DROP ROLE IF EXISTS \"$PROBE\";"; then
      return 0
    fi
    # 角色存在但持有权限 → DROP OWNED 撤销权限后重试
    if PGPASSWORD="$ADMIN_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" \
      -v ON_ERROR_STOP=1 >/dev/null 2>&1 \
      -c "DROP OWNED BY \"$PROBE\";" \
      -c "DROP ROLE IF EXISTS \"$PROBE\";"; then
      return 0
    fi
    echo "警告: 临时验证角色 $PROBE 清理失败（DROP OWNED/DROP ROLE 返回非零）" >&2
    return 1
  fi
  return 0
}
# EXIT：执行清理但**不覆盖**原始退出状态（bash EXIT trap 不改变退出码）
# INT/TERM：清理后以 130 中断退出，不继续成功执行
cleanup_and_exit() {
  cleanup_probe || true
  exit 130
}
trap cleanup_probe EXIT
trap cleanup_and_exit INT TERM
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

# 清理临时验证角色（DROP OWNED 撤销权限后再 DROP ROLE）
PGPASSWORD="$ADMIN_PASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 >/dev/null 2>&1 \
  -c "DROP OWNED BY \"$PROBE\";" \
  -c "DROP ROLE IF EXISTS \"$PROBE\";"

echo "=== 8. 实际 SELECT/INSERT 验证（应用角色，事务内回滚） ==="
# 管理员预置最小合法 FK 合成数据，验证 INSERT 权限真实可用（audit_events 有复合外键）
# -q 抑制 INSERT 状态行；head -1 取 RETURNING 的 id；tr 去除空白
WS_ID=$(PGPASSWORD="$ADMIN_PASSWORD" psql -w -q -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" -tA \
  -c "INSERT INTO workspaces(name) VALUES ('verify-insert-ws-$$') RETURNING id;" | head -1 | tr -d '[:space:]')
USER_ID=$(PGPASSWORD="$ADMIN_PASSWORD" psql -w -q -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" -tA \
  -c "INSERT INTO users(email,password_hash) VALUES ('verify-insert-$$@example.local','hash') RETURNING id;" | head -1 | tr -d '[:space:]')
[ -n "$WS_ID" ] && [ -n "$USER_ID" ] || fail "预置合成 FK 数据失败"
PGPASSWORD="$ADMIN_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 >/dev/null \
  -c "INSERT INTO workspace_members(workspace_id,user_id,role) VALUES ('$WS_ID','$USER_ID','owner');"

# 实际 SELECT LIMIT 0（应用角色）
if ! PGPASSWORD="$APP_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$APP_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 \
  -c "SELECT * FROM audit_events LIMIT 0;" >/dev/null 2>&1; then
  fail "应用角色实际 SELECT audit_events LIMIT 0 失败"
fi
echo "OK: 应用角色实际 SELECT LIMIT 0 允许"

# 实际 INSERT（应用角色，事务内回滚；FK 引用合成 workspace/member）
if ! PGPASSWORD="$APP_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$APP_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 \
  -c "BEGIN; INSERT INTO audit_events(workspace_id,actor_type,actor_id,action,resource_type,resource_id,outcome,metadata,trace_id,occurred_at) VALUES ('$WS_ID','user','$USER_ID','connection.create','connection','verify','succeeded','{}','verify-trace',now()); ROLLBACK;" >/dev/null 2>&1; then
  fail "应用角色实际 INSERT audit_events（事务回滚）失败"
fi
echo "OK: 应用角色实际 INSERT audit_events 允许（事务回滚，无残留）"

# 清理合成数据（逆序）
PGPASSWORD="$ADMIN_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 >/dev/null 2>&1 \
  -c "DELETE FROM workspace_members WHERE workspace_id='$WS_ID' AND user_id='$USER_ID';" \
  -c "DELETE FROM users WHERE id='$USER_ID';" \
  -c "DELETE FROM workspaces WHERE id='$WS_ID';" || true

echo ""
echo "生产角色拆分验证全部通过（ACL + deny_audit_mutation 触发器 + 实际 SELECT/INSERT 双重确认）。"

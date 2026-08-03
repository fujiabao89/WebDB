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
# 合成 FK 数据与负向测试临时角色状态（先初始化，清理只删除实际创建的资源）
WS_ID=""
USER_ID=""
WM_CREATED=""
LOG_PROBE=""

# 清理临时验证角色（幂等；无角色/成功返回 0，清理失败返回 1）
cleanup_probe() {
  if [ -z "$PROBE" ]; then
    return 0
  fi
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
}

# 清理负向日志测试临时角色（幂等）
cleanup_log_probe() {
  if [ -z "$LOG_PROBE" ]; then
    return 0
  fi
  PGPASSWORD="$ADMIN_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" \
    -v ON_ERROR_STOP=1 >/dev/null 2>&1 \
    -c "DROP OWNED BY \"$LOG_PROBE\";" \
    -c "DROP ROLE IF EXISTS \"$LOG_PROBE\";"
}

# 清理合成 FK 数据（逆序：workspace_members → users → workspaces；只清理实际创建的）
cleanup_data() {
  local c_rc=0
  if [ -n "$WM_CREATED" ]; then
    PGPASSWORD="$ADMIN_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" \
      -v ON_ERROR_STOP=1 >/dev/null 2>&1 \
      -c "DELETE FROM workspace_members WHERE workspace_id='$WS_ID' AND user_id='$USER_ID';" || c_rc=1
  fi
  if [ -n "$USER_ID" ]; then
    PGPASSWORD="$ADMIN_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" \
      -v ON_ERROR_STOP=1 >/dev/null 2>&1 \
      -c "DELETE FROM users WHERE id='$USER_ID';" || c_rc=1
  fi
  if [ -n "$WS_ID" ]; then
    PGPASSWORD="$ADMIN_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" \
      -v ON_ERROR_STOP=1 >/dev/null 2>&1 \
      -c "DELETE FROM workspaces WHERE id='$WS_ID';" || c_rc=1
  fi
  return "$c_rc"
}

# 统一清理：失败/中断（EXIT/INT/TERM）也清理；失败路径保留原始退出状态，
# 正常路径清理失败则脚本整体失败（不再用 || true 掩盖，见 PR37 审查）。
cleanup_all() {
  local orig_rc=$?
  cleanup_probe || true
  cleanup_log_probe || true
  local data_rc=0
  cleanup_data || data_rc=1
  if [ "$orig_rc" -ne 0 ]; then
    return "$orig_rc"   # 失败路径：不覆盖原始退出码
  fi
  return "$data_rc"     # 正常路径：清理失败则整体非零退出
}
# INT/TERM：清理后以 130 中断退出，不继续成功执行
cleanup_and_exit() {
  cleanup_probe || true
  cleanup_log_probe || true
  cleanup_data || true
  exit 130
}
trap cleanup_all EXIT
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

# 清理临时验证角色（DROP OWNED 撤销权限后再 DROP ROLE）；标记已清理，EXIT trap 不再重复
PGPASSWORD="$ADMIN_PASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 >/dev/null 2>&1 \
  -c "DROP OWNED BY \"$PROBE\";" \
  -c "DROP ROLE IF EXISTS \"$PROBE\";"
PROBE=""

echo "=== 8. 实际 SELECT/INSERT 验证（应用角色，事务内回滚） ==="
# 管理员预置最小合法 FK 合成数据，验证 INSERT 权限真实可用（audit_events 有复合外键）
# -q 抑制 INSERT 状态行；head -1 取 RETURNING 的 id；tr 去除空白
# WS_ID/USER_ID/WM_CREATED 已在脚本顶部初始化；清理 trap 只删除实际创建的资源
WS_ID=$(PGPASSWORD="$ADMIN_PASSWORD" psql -w -q -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" -tA \
  -c "INSERT INTO workspaces(name) VALUES ('verify-insert-ws-$$') RETURNING id;" | head -1 | tr -d '[:space:]')
[ -n "$WS_ID" ] || fail "预置合成 FK 数据失败：workspaces 未创建"
USER_ID=$(PGPASSWORD="$ADMIN_PASSWORD" psql -w -q -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" -tA \
  -c "INSERT INTO users(email,password_hash) VALUES ('verify-insert-$$@example.local','hash') RETURNING id;" | head -1 | tr -d '[:space:]')
[ -n "$USER_ID" ] || fail "预置合成 FK 数据失败：users 未创建"
PGPASSWORD="$ADMIN_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 >/dev/null \
  -c "INSERT INTO workspace_members(workspace_id,user_id,role) VALUES ('$WS_ID','$USER_ID','owner');"
WM_CREATED=1

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

echo "=== 9. 审计写入角色实际 SELECT/INSERT 验证（事务内回滚） ==="
if ! PGPASSWORD="$AUDIT_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$AUDIT_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 \
  -c "SELECT * FROM audit_events LIMIT 0;" >/dev/null 2>&1; then
  fail "审计写入角色实际 SELECT audit_events LIMIT 0 失败"
fi
echo "OK: 审计写入角色实际 SELECT LIMIT 0 允许"
if ! PGPASSWORD="$AUDIT_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$AUDIT_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 \
  -c "BEGIN; INSERT INTO audit_events(workspace_id,actor_type,actor_id,action,resource_type,resource_id,outcome,metadata,trace_id,occurred_at) VALUES ('$WS_ID','user','$USER_ID','connection.create','connection','verify','succeeded','{}','verify-trace',now()); ROLLBACK;" >/dev/null 2>&1; then
  fail "审计写入角色实际 INSERT audit_events（事务回滚）失败"
fi
echo "OK: 审计写入角色实际 INSERT audit_events 允许（事务回滚，无残留）"

# 正常路径：显式清理合成数据；清理失败不得被掩盖（EXIT trap 兜底失败/中断路径）
if cleanup_data; then
  WS_ID=""; USER_ID=""; WM_CREATED=""
else
  fail "合成 FK 数据清理失败（workspace_members/users/workspaces 可能残留）"
fi

echo "=== 10. 密码不得出现在完整语句日志中（负向测试，log_statement=all） ==="
# 验证生产角色密码经 \password 设置后，明文口令在 log_statement=all 下不会进入服务器日志
# （实测 PostgreSQL 不对 CREATE/ALTER ROLE 的 PASSWORD 子句脱敏，故必须证明 \password 只发送
#   SCRAM 校验器）。需要可读取的服务器日志来源：
#     VERIFY_PROD_LOG_SOURCE：输出日志行的命令（生产推荐）；否则自动用 docker logs 定位 webdb-meta。
# 日志来源无法确定时本步骤明确失败，不静默通过。
log_cmd=""
if [ -n "${VERIFY_PROD_LOG_SOURCE:-}" ]; then
  log_cmd="$VERIFY_PROD_LOG_SOURCE"
else
  meta_container=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -m1 'webdb-meta')
  if [ -n "$meta_container" ]; then
    log_cmd="docker logs --since 10m $meta_container 2>&1"
  fi
fi
[ -n "$log_cmd" ] || fail "无法确定服务器日志来源：请设置 VERIFY_PROD_LOG_SOURCE（输出日志行的命令）"

LOG_PROBE="webdb_pw_probe_$$"
CTRL_SENTINEL="VERIFY_LOG_CTRL_$$_plain"     # 正对照：明文 CREATE ROLE PASSWORD 应出现在日志
PASS_SENTINEL="VERIFY_LOG_PASS_$$_secret"    # 被测机制：\password 设置，不得出现在日志

# 创建临时角色（不设密码；\password 设置密码供登录）
PGPASSWORD="$ADMIN_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" \
  -v ON_ERROR_STOP=1 >/dev/null 2>&1 \
  -c "DROP ROLE IF EXISTS \"$LOG_PROBE\";" \
  -c "CREATE ROLE \"$LOG_PROBE\" LOGIN;"

# 正对照：同一会话开启 log_statement=all，明文 CREATE ROLE ... PASSWORD 应被记录
if ! PGPASSWORD="$ADMIN_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" \
    -v ON_ERROR_STOP=1 >/dev/null 2>&1 \
    -c "SET log_statement='all';" \
    -c "DROP ROLE IF EXISTS \"${LOG_PROBE}_ctrl\";" \
    -c "CREATE ROLE \"${LOG_PROBE}_ctrl\" WITH LOGIN PASSWORD '$CTRL_SENTINEL';" \
    -c "DROP ROLE \"${LOG_PROBE}_ctrl\";"; then
  fail "无法开启 log_statement=all 或执行正对照（需超级用户）；负向测试中止"
fi

# 被测机制：与 01-create-prod-roles.sh 相同的 \password（会话内开启 log_statement=all）
printf '%s\n%s\n' "$PASS_SENTINEL" "$PASS_SENTINEL" | psql -w -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" \
  -v ON_ERROR_STOP=1 -c "SET log_statement='all';" -c '\password '"$LOG_PROBE" >/dev/null 2>&1

# 读取日志（最多等 5s 让日志落盘），断言正对照出现、被测哨兵不出现
LOGS=""
for _ in 1 2 3 4 5; do
  LOGS=$(eval "$log_cmd" 2>/dev/null || true)
  echo "$LOGS" | grep -qF "$CTRL_SENTINEL" && break
  sleep 1
done
if echo "$LOGS" | grep -qF "$PASS_SENTINEL"; then
  fail "哨兵密码 $PASS_SENTINEL 出现在服务器日志中（密码泄漏）"
fi
if ! echo "$LOGS" | grep -qF "$CTRL_SENTINEL"; then
  fail "正对照哨兵未出现在日志中——无法证明 log_statement=all 与日志读取有效，负向测试结果不可信"
fi
echo "OK: 哨兵密码未出现在日志中（log_statement=all）；\\password 仅发送 SCRAM 校验器"

# 清理临时角色（EXIT trap 兜底；\password 已设密码，DROP OWNED 再 DROP）
PGPASSWORD="$ADMIN_PASSWORD" psql -w -h "$PGHOST" -p "$PGPORT" -U "$ADMIN_USER" -d "$DB_NAME" \
  -v ON_ERROR_STOP=1 >/dev/null 2>&1 \
  -c "DROP OWNED BY \"$LOG_PROBE\";" \
  -c "DROP ROLE IF EXISTS \"$LOG_PROBE\";"
LOG_PROBE=""

echo ""
echo "生产角色拆分验证全部通过（ACL + deny_audit_mutation 触发器 + 实际 SELECT/INSERT 双重确认 + 密码不落日志负向测试）。"

#!/bin/bash
# 生产元数据库角色拆分（WEB-27 / R6）
# ============================================================
# 目标：使 audit_events 的 UPDATE/DELETE/TRUNCATE 由"拒绝触发器 + 最小权限"双重防护，
#       且生产运行时不再依赖 SUPERUSER 连接（SUPERUSER 可绕过触发器）。
# 创建最小权限角色：
#   - webdb_app_runtime  : 应用运行时连接角色（业务表 + audit_events SELECT/INSERT）
#   - webdb_audit_writer : 独立审计写入连接角色（仅 audit_events SELECT/INSERT）
# 密码通过环境变量 WEBDB_APP_PASSWORD / WEBDB_AUDIT_PASSWORD 注入；
# 使用 psql \getenv + format() %L 安全引用，不做 shell 插值、不使用 dollar-quote，
# 密码含任意特殊字符（含 $tag$ 序列）均安全。
# 所有角色对 audit_events 仅授予 SELECT/INSERT，不授予 UPDATE/DELETE/TRUNCATE。
set -e

# 拒绝保留角色名与管理员重名（若 POSTGRES_USER 被设为保留角色名，入口点会先以超级用户
# 创建同名角色，下方 IF NOT EXISTS 跳过受限角色创建，API 将持有管理员权限，破坏最小权限边界）。
for reserved in webdb_app_runtime webdb_audit_writer; do
  if [ "$POSTGRES_USER" = "$reserved" ]; then
    echo "错误: POSTGRES_USER 不能使用保留角色名 $reserved，请改用其他管理员用户名（如 webdb_admin）" >&2
    exit 1
  fi
done

APP_PASSWORD="${WEBDB_APP_PASSWORD:-change_me}"
AUDIT_PASSWORD="${WEBDB_AUDIT_PASSWORD:-change_me}"
export APP_PASSWORD AUDIT_PASSWORD
export PGPASSWORD="$POSTGRES_PASSWORD"

# 第一步：创建应用运行时角色
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" <<'EOSQL'
\getenv app_password APP_PASSWORD
SELECT format('CREATE ROLE webdb_app_runtime WITH LOGIN PASSWORD %L', :'app_password')
WHERE NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'webdb_app_runtime')
\gexec
EOSQL

# 第二步：创建审计写入角色
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" <<'EOSQL'
\getenv audit_password AUDIT_PASSWORD
SELECT format('CREATE ROLE webdb_audit_writer WITH LOGIN PASSWORD %L', :'audit_password')
WHERE NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'webdb_audit_writer')
\gexec
EOSQL

# 第三步：授权（最小权限；audit_events 仅 SELECT/INSERT）
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" <<'EOSQL'
GRANT CONNECT ON DATABASE webdb_meta TO webdb_app_runtime, webdb_audit_writer;
GRANT USAGE ON SCHEMA public TO webdb_app_runtime, webdb_audit_writer;
-- 撤销 PUBLIC 默认 TEMPORARY，防止受限角色建临时表越权
REVOKE TEMPORARY ON DATABASE webdb_meta FROM PUBLIC;

-- audit_events：仅 SELECT/INSERT；不授予 UPDATE/DELETE/TRUNCATE
-- （与 deny_audit_mutation 拒绝触发器构成双重防护，见 ADR-013）
GRANT SELECT, INSERT ON audit_events TO webdb_app_runtime, webdb_audit_writer;

-- 审计写入角色：读取 executions/connections 以配合 audit_events 复合外键校验
GRANT SELECT ON executions, connections TO webdb_audit_writer;

-- 应用运行时角色：P0 业务表读写（凭证/审计表按 P0 边界）
GRANT SELECT, INSERT, UPDATE, DELETE ON users, workspaces, workspace_members,
    credential_envelopes, connections, connection_policies, executions
    TO webdb_app_runtime;
EOSQL

echo "生产角色拆分完成：webdb_app_runtime / webdb_audit_writer"

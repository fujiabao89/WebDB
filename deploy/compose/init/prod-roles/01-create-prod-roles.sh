#!/bin/bash
# 生产元数据库角色拆分（WEB-27 / R6）— 生产部署脚本
# ============================================================
# ⚠️ 部署边界：本脚本用于 production-like 部署，**不属于 deploy/compose 的本地开发配置**。
#    执行前必须显式设置 WEBDB_PRODUCTION_DEPLOY=1，避免被误认为本地 Compose 初始化。
# 目标：audit_events 的 UPDATE/DELETE/TRUNCATE 由"拒绝触发器 + 最小权限"双重防护，
#       生产运行时不再依赖 SUPERUSER（SUPERUSER 可绕过触发器）。
# 幂等：重复执行收敛到最小权限——显式设置 NOSUPERUSER/NOBYPASSRLS 等安全属性，
#       撤销未批准的角色成员关系与对象权限，保留声明式 GRANT。
# 密码：WEBDB_APP_PASSWORD / WEBDB_AUDIT_PASSWORD / POSTGRES_PASSWORD 必须非空且非占位符，
#       校验失败在任何 psql 前退出。
set -e

# --- 部署边界确认 ---
if [ "${WEBDB_PRODUCTION_DEPLOY:-0}" != "1" ]; then
  echo "错误: 本脚本是生产部署脚本（R6 / WEB-27）。请显式设置 WEBDB_PRODUCTION_DEPLOY=1 确认后执行。" >&2
  exit 1
fi

# --- 保留角色名与管理员重名校验 ---
for reserved in webdb_app_runtime webdb_audit_writer; do
  if [ "$POSTGRES_USER" = "$reserved" ]; then
    echo "错误: POSTGRES_USER 不能使用保留角色名 $reserved，请改用其他管理员用户名（如 webdb_admin）" >&2
    exit 1
  fi
done

# --- 密码校验（非空且非占位符）---
validate_pw() {
  local name="$1" val="${2:-}"
  if [ -z "$val" ] || [ "$val" = "change_me" ] || [ "$val" = "changeme" ]; then
    echo "错误: $name 必须为非空且非占位符（不得为 change_me / changeme）" >&2
    exit 1
  fi
}
validate_pw WEBDB_APP_PASSWORD "${WEBDB_APP_PASSWORD:-}"
validate_pw WEBDB_AUDIT_PASSWORD "${WEBDB_AUDIT_PASSWORD:-}"
validate_pw POSTGRES_PASSWORD "${POSTGRES_PASSWORD:-}"
# 目标库必须为 webdb_meta（与 verify-prod-roles.sh 一致；非 webdb_meta 立即失败且不写入权限）
if [ "${POSTGRES_DB:-}" != "webdb_meta" ]; then
  echo "错误: POSTGRES_DB 必须为 webdb_meta（生产元数据库），当前: ${POSTGRES_DB:-<空>}" >&2
  exit 1
fi
# 明文密码不通过格式生成 SQL 进日志：PostgreSQL 对 CREATE/ALTER ROLE 的 PASSWORD 子句
# 在语句日志中自动脱敏（显示为 ********）。不依赖 PGOPTIONS log_statement（受限管理员可连接）。
export APP_PASSWORD="$WEBDB_APP_PASSWORD"
export AUDIT_PASSWORD="$WEBDB_AUDIT_PASSWORD"
export PGPASSWORD="$POSTGRES_PASSWORD"

# --- 创建/更新角色（幂等：已存在则跳过创建，但总是应用当前密码）---
# 密码经 \getenv + format %L 安全引用。
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" <<'EOSQL'
\getenv app_password APP_PASSWORD
SELECT format('CREATE ROLE webdb_app_runtime WITH LOGIN PASSWORD %L', :'app_password')
WHERE NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'webdb_app_runtime')
\gexec
SELECT format('ALTER ROLE webdb_app_runtime WITH LOGIN PASSWORD %L', :'app_password')
\gexec
EOSQL

psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" <<'EOSQL'
\getenv audit_password AUDIT_PASSWORD
SELECT format('CREATE ROLE webdb_audit_writer WITH LOGIN PASSWORD %L', :'audit_password')
WHERE NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'webdb_audit_writer')
\gexec
SELECT format('ALTER ROLE webdb_audit_writer WITH LOGIN PASSWORD %L', :'audit_password')
\gexec
EOSQL

# --- 最小权限收敛 + 授权（幂等；重复执行收敛）---
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" <<'EOSQL'
-- 显式安全属性：非 SUPERUSER、非 BYPASSRLS、非创建者
ALTER ROLE webdb_app_runtime NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION;
ALTER ROLE webdb_audit_writer NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION;

-- 收敛（幂等）：撤销已存在的角色成员关系与直接对象/所有权权限，收敛到最小权限
REVOKE ALL PRIVILEGES ON DATABASE webdb_meta FROM webdb_app_runtime, webdb_audit_writer;
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM webdb_app_runtime, webdb_audit_writer;
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM webdb_app_runtime, webdb_audit_writer;
REVOKE ALL PRIVILEGES ON SCHEMA public FROM webdb_app_runtime, webdb_audit_writer;
REVOKE webdb_app_runtime FROM webdb_audit_writer;
REVOKE webdb_audit_writer FROM webdb_app_runtime;
-- 撤销 PUBLIC 的 CONNECT/USAGE/TEMPORARY（最小权限，审计边界）
REVOKE CONNECT ON DATABASE webdb_meta FROM PUBLIC;
REVOKE USAGE ON SCHEMA public FROM PUBLIC;
REVOKE TEMPORARY ON DATABASE webdb_meta FROM PUBLIC;
-- 撤销 PUBLIC 对 audit_events 的一切权限（审计不可变边界）
REVOKE ALL PRIVILEGES ON audit_events FROM PUBLIC;

GRANT CONNECT ON DATABASE webdb_meta TO webdb_app_runtime, webdb_audit_writer;
GRANT USAGE ON SCHEMA public TO webdb_app_runtime, webdb_audit_writer;

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

echo "生产角色拆分完成（幂等收敛到最小权限）：webdb_app_runtime / webdb_audit_writer"

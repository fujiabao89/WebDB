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
# 密码一律经 psql \password 设置：psql 在客户端按 SCRAM 计算口令校验器，
# 发送给服务器的 SQL 文本与日志中只有校验器（SCRAM-SHA-256$...），明文密码不进 SQL 文本、不落日志。
# 实测 PostgreSQL 在 log_statement=all 下**不会**对 CREATE/ALTER ROLE 的 PASSWORD 子句脱敏，
# 因此不能再用 format('%L')/getenv 拼接含明文的 DDL；\password 的明文只经 stdin 管道进入 psql。
export PGPASSWORD="$POSTGRES_PASSWORD"

# --- 创建角色（幂等：已存在则跳过；不带密码，密码随后经 \password 设置）---
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" <<'EOSQL'
SELECT 'CREATE ROLE webdb_app_runtime WITH LOGIN'
WHERE NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'webdb_app_runtime')
\gexec
SELECT 'CREATE ROLE webdb_audit_writer WITH LOGIN'
WHERE NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'webdb_audit_writer')
\gexec
EOSQL

# --- 应用当前密码（幂等重跑也刷新；明文只经 stdin 管道交给 \password，不进入 SQL 文本/命令行/日志）---
set_password() {
  local role="$1" pw="$2"
  if ! printf '%s\n%s\n' "$pw" "$pw" | psql -v ON_ERROR_STOP=1 -w -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
      -c '\password '"$role" >/dev/null 2>&1; then
    echo "错误: 设置 $role 密码失败（psql \\password 返回非零）" >&2
    exit 1
  fi
}
set_password webdb_app_runtime "$WEBDB_APP_PASSWORD"
set_password webdb_audit_writer "$WEBDB_AUDIT_PASSWORD"

# --- 最小权限收敛 + 授权（幂等；重复执行收敛）---
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" <<'EOSQL'
-- 所有权 fail-closed：任一生产角色若持有数据库/模式/对象所有权，拒绝继续。
-- 所有权可绕过 ACL/RLS 边界，必须先由受控管理员转移（PR37 审查项）。
DO $$
DECLARE n int;
BEGIN
  SELECT count(*) INTO n FROM (
    SELECT datdba AS o FROM pg_catalog.pg_database
      WHERE datdba IN ('webdb_app_runtime'::regrole, 'webdb_audit_writer'::regrole)
    UNION ALL
    SELECT nspowner FROM pg_catalog.pg_namespace
      WHERE nspowner IN ('webdb_app_runtime'::regrole, 'webdb_audit_writer'::regrole)
    UNION ALL
    SELECT relowner FROM pg_catalog.pg_class
      WHERE relowner IN ('webdb_app_runtime'::regrole, 'webdb_audit_writer'::regrole)
        AND relkind IN ('r','S','v','m','p')
    UNION ALL
    SELECT proowner FROM pg_catalog.pg_proc
      WHERE proowner IN ('webdb_app_runtime'::regrole, 'webdb_audit_writer'::regrole)
    UNION ALL
    SELECT typowner FROM pg_catalog.pg_type
      WHERE typowner IN ('webdb_app_runtime'::regrole, 'webdb_audit_writer'::regrole)
  ) o;
  IF n > 0 THEN
    RAISE EXCEPTION 'webdb_app_runtime/webdb_audit_writer 仍持有数据库/模式/对象所有权（% 处），拒绝继续；请先用受控管理员转移所有权', n;
  END IF;
END
$$;

-- 显式安全属性：非 SUPERUSER、非 BYPASSRLS、非创建者
ALTER ROLE webdb_app_runtime NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION;
ALTER ROLE webdb_audit_writer NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION;

-- 收敛（幂等）：撤销已存在的角色成员关系与直接对象权限，收敛到最小权限
REVOKE ALL PRIVILEGES ON DATABASE webdb_meta FROM webdb_app_runtime, webdb_audit_writer;
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM webdb_app_runtime, webdb_audit_writer;
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM webdb_app_runtime, webdb_audit_writer;
REVOKE ALL PRIVILEGES ON SCHEMA public FROM webdb_app_runtime, webdb_audit_writer;
-- 撤销**所有**未批准的父角色成员关系（不只两者之间）：任一生产角色作为 member 或被授予者
-- 出现即移除，收敛到无父角色/无被授权者（PR37 审查项）。
SELECT format('REVOKE %I FROM %I', m.roleid::regrole, m.member::regrole)
FROM pg_catalog.pg_auth_members m
WHERE m.member IN ('webdb_app_runtime'::regrole, 'webdb_audit_writer'::regrole)
   OR m.roleid IN ('webdb_app_runtime'::regrole, 'webdb_audit_writer'::regrole)
\gexec
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

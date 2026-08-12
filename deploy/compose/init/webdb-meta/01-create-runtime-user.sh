#!/bin/bash
# webdb-meta 初始化：创建运行时最小权限账号 webdb_app_runtime
#
# API 运行时连接元数据库使用 webdb_app_runtime（非超级用户），
# 与迁移管理员 webdb（META_MIGRATE_USER）分离（WEB-40 runtime/migration 分离）。
# 生产环境该账号由 init/prod-roles/01-create-prod-roles.sh（ADR-018）创建并收敛最小权限，
# 本脚本仅为本地 Compose 演示提供可用账号（幂等）。
#
# 密码经 WEBDB_APP_PASSWORD 环境变量注入；使用 psql \getenv + format() %L 安全引用，
# 不做 shell 插值、不用 dollar-quote 分隔符，密码不进 argv、任意特殊字符均安全。
set -e

# 拒绝管理员与运行时账号重名：若 POSTGRES_USER 被设为 webdb_app_runtime，
# 入口点会先以超级用户身份创建该角色，下方 IF NOT EXISTS 将跳过受限角色创建，
# API 连接的 webdb_app_runtime 会持有管理员权限，破坏最小权限边界。
if [ "$POSTGRES_USER" = "webdb_app_runtime" ]; then
  echo "错误: POSTGRES_USER 不能使用保留的运行时账号 webdb_app_runtime，请改用其他管理员用户名（如 webdb）" >&2
  exit 1
fi

RUNTIME_PASSWORD="${WEBDB_APP_PASSWORD:-change_me}"
# 通过环境变量传递给 psql（\getenv 读取），密码不进 argv、不做 shell 插值
export RUNTIME_PASSWORD

export PGPASSWORD="$POSTGRES_PASSWORD"

# 支持远程连接：本脚本既用于 webdb-meta 首次初始化（本地 socket），
# 也被 api-bootstrap one-shot 服务复用（POSTGRES_HOST 指向 webdb-meta，升级/已有卷场景）。
PSQL_OPTS=(-v ON_ERROR_STOP=1)
if [ -n "${POSTGRES_HOST:-}" ]; then
  PSQL_OPTS+=(-h "$POSTGRES_HOST" -p "${POSTGRES_PORT:-5432}")
fi

# 第一步：创建角色并设置密码（幂等；CREATE 仅不存在时执行，ALTER 仅已存在时执行）
psql "${PSQL_OPTS[@]}" -U "$POSTGRES_USER" -d "$POSTGRES_DB" <<'EOSQL'
\getenv runtime_password RUNTIME_PASSWORD
SELECT format('CREATE ROLE webdb_app_runtime WITH LOGIN PASSWORD %L', :'runtime_password')
WHERE NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'webdb_app_runtime')
\gexec
SELECT format('ALTER ROLE webdb_app_runtime WITH LOGIN PASSWORD %L', :'runtime_password')
WHERE EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'webdb_app_runtime')
\gexec
EOSQL

# 第二步：基础授权 + 默认权限（无需变量替换）
# 显式安全属性：非 SUPERUSER、非 BYPASSRLS、非创建者
# 默认权限针对 webdb（migrate 以该角色建表），后续迁移创建的表/序列自动授予运行时账号
psql "${PSQL_OPTS[@]}" -U "$POSTGRES_USER" -d "$POSTGRES_DB" <<'EOSQL'
ALTER ROLE webdb_app_runtime NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION;
GRANT CONNECT ON DATABASE webdb_meta TO webdb_app_runtime;
GRANT USAGE ON SCHEMA public TO webdb_app_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE webdb IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO webdb_app_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE webdb IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO webdb_app_runtime;
-- 已有对象授权（Codex P1）：api-bootstrap 在 api-migrate 建表之后运行，
-- ALTER DEFAULT PRIVILEGES 只影响未来对象；此处 GRANT ON ALL TABLES/SEQUENCES
-- 覆盖已有元数据库卷升级场景（空卷 init 无表时幂等 no-op）。
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO webdb_app_runtime;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO webdb_app_runtime;
EOSQL

#!/bin/bash
# 演示 PostgreSQL 初始化：合成数据与只读账号
# 所有数据均为合成数据，不含真实 PII
# 只读账号密码通过 DEMO_PG_READER_PASSWORD 环境变量传入
# 使用 PL/pgSQL format() %L 安全引用，正确处理单引号、反斜杠等特殊字符
set -e

# 拒绝管理员与只读角色重名：若 POSTGRES_USER 被设为 demo_reader，
# 入口点会先以超级用户身份创建该角色，下方 IF NOT EXISTS 将跳过受限角色创建，
# API 连接的 demo_reader 会持有管理员权限，破坏 P0 只读边界。
if [ "$POSTGRES_USER" = "demo_reader" ]; then
  echo "错误: POSTGRES_USER 不能使用保留的只读角色名 demo_reader，请改用其他管理员用户名（如 demo_admin）" >&2
  exit 1
fi

READER_PASSWORD="${DEMO_PG_READER_PASSWORD:-change_me}"
# SQL 字符串转义：加倍单引号
READER_PW_SAFE="${READER_PASSWORD//\'/\'\'}"

export PGPASSWORD="$POSTGRES_PASSWORD"

# 第一步：创建只读角色（使用 format() %L 安全处理密码中的所有特殊字符）
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" <<EOSQL
DO \$webdb_init\$
DECLARE
  pw text := '${READER_PW_SAFE}';
BEGIN
  IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'demo_reader') THEN
    EXECUTE format('CREATE ROLE demo_reader WITH LOGIN PASSWORD %L', pw);
  END IF;
END
\$webdb_init\$;
EOSQL

# 第二步：DDL、DML 和权限授予（无需变量替换）
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" <<'EOSQL'
GRANT CONNECT ON DATABASE webdb_demo TO demo_reader;
GRANT USAGE ON SCHEMA public TO demo_reader;
-- 撤销 PUBLIC 默认的 TEMPORARY 权限，确保 demo_reader 不能创建临时表
REVOKE TEMPORARY ON DATABASE webdb_demo FROM PUBLIC;

CREATE TABLE IF NOT EXISTS departments (
    id   SERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS employees (
    id            SERIAL PRIMARY KEY,
    first_name    TEXT NOT NULL,
    last_name     TEXT NOT NULL,
    email         TEXT NOT NULL UNIQUE,
    department_id INT REFERENCES departments(id),
    hire_date     DATE NOT NULL DEFAULT CURRENT_DATE,
    salary        NUMERIC(10, 2) NOT NULL DEFAULT 0
);

INSERT INTO departments (name) VALUES
    ('Engineering'),
    ('Product'),
    ('Design'),
    ('Operations')
ON CONFLICT (name) DO NOTHING;

INSERT INTO employees (first_name, last_name, email, department_id, hire_date, salary) VALUES
    ('Alice',  'Chen',    'alice.chen@example.local',    1, '2024-01-15', 85000.00),
    ('Bob',    'Li',      'bob.li@example.local',        1, '2024-03-01', 92000.00),
    ('Carol',  'Wang',    'carol.wang@example.local',    2, '2024-02-15', 78000.00),
    ('David',  'Zhang',   'david.zhang@example.local',   1, '2024-04-10', 88000.00),
    ('Eve',    'Liu',     'eve.liu@example.local',       3, '2024-05-01', 75000.00),
    ('Frank',  'Yang',    'frank.yang@example.local',    4, '2024-06-01', 65000.00),
    ('Grace',  'Zhao',    'grace.zhao@example.local',    2, '2024-07-01', 82000.00),
    ('Henry',  'Sun',     'henry.sun@example.local',     1, '2024-08-15', 95000.00),
    ('Iris',   'Xu',      'iris.xu@example.local',       3, '2024-09-01', 72000.00),
    ('Jack',   'Huang',   'jack.huang@example.local',    4, '2024-10-01', 68000.00);

GRANT SELECT ON ALL TABLES IN SCHEMA public TO demo_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO demo_reader;
EOSQL

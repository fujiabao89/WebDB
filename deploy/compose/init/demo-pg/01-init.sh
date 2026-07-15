#!/bin/bash
# 演示 PostgreSQL 初始化：合成数据与只读账号
# 所有数据均为合成数据，不含真实 PII
# 只读账号密码通过 DEMO_PG_READER_PASSWORD 环境变量传入
# 使用 psql :'var' 语法安全传递密码，避免 SQL 注入和特殊字符问题
set -e

READER_PASSWORD="${DEMO_PG_READER_PASSWORD:-change_me}"

psql -v ON_ERROR_STOP=1 \
  -v reader_pw="$READER_PASSWORD" \
  --username "$POSTGRES_USER" \
  --dbname "$POSTGRES_DB" \
<<'EOSQL'
-- 创建只读账号（密码通过 psql -v 安全传入，单引号等特殊字符被正确处理）
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'demo_reader') THEN
    CREATE ROLE demo_reader WITH LOGIN PASSWORD :'reader_pw';
  END IF;
END
$$;

-- 授予连接权限
GRANT CONNECT ON DATABASE webdb_demo TO demo_reader;
GRANT USAGE ON SCHEMA public TO demo_reader;

-- 示例部门表
CREATE TABLE IF NOT EXISTS departments (
    id   SERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

-- 示例员工表
CREATE TABLE IF NOT EXISTS employees (
    id            SERIAL PRIMARY KEY,
    first_name    TEXT NOT NULL,
    last_name     TEXT NOT NULL,
    email         TEXT NOT NULL UNIQUE,
    department_id INT REFERENCES departments(id),
    hire_date     DATE NOT NULL DEFAULT CURRENT_DATE,
    salary        NUMERIC(10, 2) NOT NULL DEFAULT 0
);

-- 合成数据
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

-- 授予只读权限
GRANT SELECT ON ALL TABLES IN SCHEMA public TO demo_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO demo_reader;
EOSQL

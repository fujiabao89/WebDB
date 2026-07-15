#!/bin/bash
# 演示 MySQL 8.4 初始化：合成数据与只读账号
# 所有数据均为合成数据，不含真实 PII
# 只读账号由 MYSQL_USER/MYSQL_PASSWORD 环境变量在容器启动时创建
# 本脚本撤销镜像自动授予的全部权限，仅保留 SELECT
# MYSQL_USER / MYSQL_DATABASE 经正则校验，防止 SQL 注入
set -euo pipefail

READER_USER="${MYSQL_USER:-demo_reader}"
MYSQL_DB="${MYSQL_DATABASE:-webdb_demo}"

# 校验只允许安全的数据库标识符字符（字母、数字、下划线、连字符）
if ! [[ "$READER_USER" =~ ^[a-zA-Z0-9_-]+$ ]]; then
  echo "错误：MYSQL_USER 包含不安全字符：$READER_USER" >&2
  exit 1
fi
if ! [[ "$MYSQL_DB" =~ ^[a-zA-Z0-9_-]+$ ]]; then
  echo "错误：MYSQL_DATABASE 包含不安全字符：$MYSQL_DB" >&2
  exit 1
fi

mysql -u root -p"${MYSQL_ROOT_PASSWORD}" <<EOSQL
-- 撤销 MySQL 镜像自动授予的全部权限，仅保留 SELECT
REVOKE ALL PRIVILEGES, GRANT OPTION FROM '${READER_USER}'@'%';
FLUSH PRIVILEGES;

-- 仅授予 SELECT 权限（数据库名使用反引号防止保留字冲突）
GRANT SELECT ON \`${MYSQL_DB}\`.* TO '${READER_USER}'@'%';
FLUSH PRIVILEGES;

-- 示例部门表
CREATE TABLE IF NOT EXISTS departments (
    id   INT AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(255) NOT NULL UNIQUE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 示例员工表
CREATE TABLE IF NOT EXISTS employees (
    id            INT AUTO_INCREMENT PRIMARY KEY,
    first_name    VARCHAR(255) NOT NULL,
    last_name     VARCHAR(255) NOT NULL,
    email         VARCHAR(255) NOT NULL UNIQUE,
    department_id INT,
    hire_date     DATE NOT NULL DEFAULT (CURRENT_DATE),
    salary        DECIMAL(10, 2) NOT NULL DEFAULT 0,
    FOREIGN KEY (department_id) REFERENCES departments(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 合成数据
INSERT IGNORE INTO departments (name) VALUES
    ('Engineering'),
    ('Product'),
    ('Design'),
    ('Operations');

INSERT IGNORE INTO employees (first_name, last_name, email, department_id, hire_date, salary) VALUES
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
EOSQL

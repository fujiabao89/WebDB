#!/bin/bash
# 演示 MySQL 8.4 初始化：合成数据与只读账号
# 所有数据均为合成数据，不含真实 PII
# 只读账号通过 DEMO_MYSQL_READER_PASSWORD 在 init 脚本中安全创建
# 绕过 MySQL Docker 入口点对特殊字符的限制，支持单引号、双引号等
# 主体放入显式子 shell：MySQL 官方入口点 source 非可执行 .sh，
# 子 shell 可防止 set -euo pipefail 污染父入口点的 shell 状态。
(
set -euo pipefail

READER_USER="${MYSQL_USER:-demo_reader}"
READER_PASSWORD="${DEMO_MYSQL_READER_PASSWORD:-change_me}"
MYSQL_DB="${MYSQL_DATABASE:-webdb_demo}"

# 校验标识符只允许安全字符
if ! [[ "$READER_USER" =~ ^[a-zA-Z0-9_-]+$ ]]; then
  echo "错误：MYSQL_USER 包含不安全字符：$READER_USER" >&2
  exit 1
fi
if ! [[ "$MYSQL_DB" =~ ^[a-zA-Z0-9_-]+$ ]]; then
  echo "错误：MYSQL_DATABASE 包含不安全字符：$MYSQL_DB" >&2
  exit 1
fi

# MySQL 字符串转义：反斜杠和单引号分别加倍
READER_PW_SQL="${READER_PASSWORD//\\/\\\\}"
READER_PW_SQL="${READER_PW_SQL//\'/\'\'}"

# 对数据库名中的 _ 进行转义，防止 MySQL GRANT 将其视为单字符通配符
MYSQL_DB_GRANT="${MYSQL_DB//_/\\_}"

mysql -u root -p"${MYSQL_ROOT_PASSWORD}" "${MYSQL_DB}" <<EOSQL
-- 创建只读账号并设置密码（支持单引号、双引号、空格、反斜杠等特殊字符）
CREATE USER IF NOT EXISTS '${READER_USER}'@'%' IDENTIFIED BY '${READER_PW_SQL}';
ALTER USER '${READER_USER}'@'%' IDENTIFIED BY '${READER_PW_SQL}';

-- 仅授予 SELECT 权限
REVOKE ALL PRIVILEGES, GRANT OPTION FROM '${READER_USER}'@'%';
GRANT SELECT ON \`${MYSQL_DB_GRANT}\`.* TO '${READER_USER}'@'%';
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

-- 类型矩阵合成表（WEB-10 查询结果类型规范化集成测试；demo_reader 仅 SELECT 该表）
CREATE TABLE IF NOT EXISTS webdb_type_matrix (
    id             INT PRIMARY KEY,
    c_char         CHAR(8),
    c_varchar      VARCHAR(64),
    c_text         TEXT,
    c_longtext     LONGTEXT,
    c_binary       BINARY(4),
    c_varbinary    VARBINARY(16),
    c_blob         BLOB,
    c_longblob     LONGBLOB,
    c_json         JSON,
    c_null_col     VARCHAR(16),
    c_signed_int   INT,
    c_unsigned_int INT UNSIGNED,
    c_bigint       BIGINT,
    c_decimal      DECIMAL(20,4),
    c_float        FLOAT,
    c_double       DOUBLE,
    c_bool         BOOLEAN,
    c_bit          BIT(8),
    c_date         DATE,
    c_datetime     DATETIME,
    c_timestamp    TIMESTAMP NULL DEFAULT NULL,
    c_time         TIME,
    c_empty_str    VARCHAR(16),
    c_empty_binary VARBINARY(16),
    c_invalid_utf8 VARBINARY(16),
    c_tinytext     TINYTEXT,
    c_mediumtext   MEDIUMTEXT,
    c_tinyblob     TINYBLOB,
    c_mediumblob   MEDIUMBLOB,
    c_enum         ENUM('red','green','blue'),
    c_set          SET('a','b','c')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

DELETE FROM webdb_type_matrix;
INSERT INTO webdb_type_matrix (id, c_char, c_varchar, c_text, c_longtext,
    c_binary, c_varbinary, c_blob, c_longblob, c_json, c_null_col,
    c_signed_int, c_unsigned_int, c_bigint, c_decimal, c_float, c_double,
    c_bool, c_bit, c_date, c_datetime, c_timestamp, c_time,
    c_empty_str, c_empty_binary, c_invalid_utf8,
    c_tinytext, c_mediumtext, c_tinyblob, c_mediumblob, c_enum, c_set) VALUES
    (1, 'ch', 'hello', 'text body', 'long text body',
        X'01020304', X'0102FF', X'DEADBEEF', X'00FF', '{"a":1}', NULL,
        -42, 3000000000, 9223372036854775807, 12345.6789, 1.5, 2.25,
        TRUE, b'10101010', '2026-08-09', '2026-08-09 12:34:56', '2026-08-09 12:34:56', '12:34:56',
        '', X'', X'FFFE00',
        'tiny text', 'medium text', X'0102', X'ABCDEF', 'green', 'a,c'),
    (2, 'ab', 'world', 'second row text', 'second row long text',
        X'0A0B0C0D', X'FF01', X'11223344', X'AABB', '{"b":2}', NULL,
        99, 1, -9223372036854775808, 0.0001, 0.5, 1.5,
        FALSE, b'00000001', '2025-01-02', '2025-01-02 03:04:05', '2025-01-02 03:04:05', '03:04:05',
        '', X'', X'0102FF',
        'tiny 2', 'medium 2', X'0A0B', X'010203', 'red', 'b');
EOSQL
) || exit $?

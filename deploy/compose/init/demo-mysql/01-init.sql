-- 演示 MySQL 8.4 初始化：合成数据与只读账号
-- 所有数据均为合成数据，不含真实 PII

-- 授予只读权限（账号已由 MYSQL_USER/MYSQL_PASSWORD 环境变量创建）
GRANT SELECT ON webdb_demo.* TO 'demo_reader'@'%';
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

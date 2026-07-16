#!/bin/bash
# P0-01 只读账号权限验证脚本
# 使用 TCP 连接 + 密码认证，真实验证密码正确性和只读权限
# 从容器运行时环境变量读取凭证，与 Compose 实际传给容器的值一致
# 密码通过 export 注入环境变量，docker compose exec 只传变量名，
# 不在宿主进程参数（/proc/*/cmdline）中暴露密码值。
# 验证：
#   1. 错误密码认证失败
#   2. 正确密码 SELECT 成功
#   3. INSERT、UPDATE、DELETE、永久 DDL、临时表 DDL 被拒绝
# 用法：
#   docker compose -f deploy/compose/docker-compose.yml up -d --wait
#   bash deploy/compose/verify-readonly.sh
set -euo pipefail

PASS=0
FAIL=0
RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

pass() { echo -e "  ${GREEN}✅ PASS${NC}: $1"; PASS=$((PASS + 1)); }
fail() { echo -e "  ${RED}❌ FAIL${NC}: $1"; FAIL=$((FAIL + 1)); }

COMPOSE="docker compose -f deploy/compose/docker-compose.yml"

# 从容器运行时读取凭证，与 Compose 传值一致
PG_READER_PASSWORD=$(${COMPOSE} exec -T demo-pg printenv DEMO_PG_READER_PASSWORD 2>/dev/null || echo "")
MYSQL_READER_PASSWORD=$(${COMPOSE} exec -T demo-mysql printenv DEMO_MYSQL_READER_PASSWORD 2>/dev/null || echo "")
MYSQL_READER_USER=$(${COMPOSE} exec -T demo-mysql printenv MYSQL_USER 2>/dev/null || echo "demo_reader")

if [ -z "$PG_READER_PASSWORD" ]; then
  fail "无法从 demo-pg 容器读取 DEMO_PG_READER_PASSWORD"
fi
if [ -z "$MYSQL_READER_PASSWORD" ]; then
  fail "无法从 demo-mysql 容器读取 DEMO_MYSQL_READER_PASSWORD"
fi

# 将密码 export 到环境变量，后续 docker compose exec 只通过 -e VARNAME 传递
# 避免密码值出现在宿主进程的 /proc/*/cmdline 中
export PGPASSWORD="$PG_READER_PASSWORD"
export MYSQL_PWD="$MYSQL_READER_PASSWORD"

echo "=== P0-01 只读账号权限验证（TCP 密码认证）==="
echo ""

# ============================================================
# PostgreSQL 只读账号验证（TCP + PGPASSWORD）
# ============================================================
echo "--- PostgreSQL demo_reader ---"

# 0. 错误密码必须认证失败
if ${COMPOSE} exec -T -e PGPASSWORD="wrong_pw_123" demo-pg \
    psql --host=demo-pg -U demo_reader -d webdb_demo -c "SELECT 1;" > /dev/null 2>&1; then
  fail "PostgreSQL 错误密码认证未拒绝 — 可能存在 trust 认证漏洞"
else
  pass "PostgreSQL 错误密码认证被正确拒绝"
fi

# 1. 正确密码 SELECT 成功（PGPASSWORD 来自环境变量，不暴露在 argv）
if ${COMPOSE} exec -T -e PGPASSWORD demo-pg \
    psql --host=demo-pg -U demo_reader -d webdb_demo -c "SELECT count(*) FROM employees;" > /dev/null 2>&1; then
  pass "PostgreSQL SELECT 成功"
else
  fail "PostgreSQL SELECT 失败 — 只读账号应能执行 SELECT"
fi

# 2. INSERT 被拒绝
if ${COMPOSE} exec -T -e PGPASSWORD demo-pg \
    psql --host=demo-pg -U demo_reader -d webdb_demo -c "INSERT INTO departments(name) VALUES ('test_insert');" > /dev/null 2>&1; then
  fail "PostgreSQL INSERT 未被拒绝"
else
  pass "PostgreSQL INSERT 被正确拒绝"
fi

# 3. UPDATE 被拒绝
if ${COMPOSE} exec -T -e PGPASSWORD demo-pg \
    psql --host=demo-pg -U demo_reader -d webdb_demo -c "UPDATE employees SET salary = 0 WHERE id = 1;" > /dev/null 2>&1; then
  fail "PostgreSQL UPDATE 未被拒绝"
else
  pass "PostgreSQL UPDATE 被正确拒绝"
fi

# 4. DELETE 被拒绝
if ${COMPOSE} exec -T -e PGPASSWORD demo-pg \
    psql --host=demo-pg -U demo_reader -d webdb_demo -c "DELETE FROM employees WHERE id = 1;" > /dev/null 2>&1; then
  fail "PostgreSQL DELETE 未被拒绝"
else
  pass "PostgreSQL DELETE 被正确拒绝"
fi

# 5. 永久 DDL 被拒绝
if ${COMPOSE} exec -T -e PGPASSWORD demo-pg \
    psql --host=demo-pg -U demo_reader -d webdb_demo -c "CREATE TABLE test_ddl (id INT);" > /dev/null 2>&1; then
  fail "PostgreSQL 永久 DDL 未被拒绝"
else
  pass "PostgreSQL 永久 DDL 被正确拒绝"
fi

# 6. 临时表 DDL 被拒绝
if ${COMPOSE} exec -T -e PGPASSWORD demo-pg \
    psql --host=demo-pg -U demo_reader -d webdb_demo -c "CREATE TEMP TABLE test_temp (id INT);" > /dev/null 2>&1; then
  fail "PostgreSQL CREATE TEMP TABLE 未被拒绝"
else
  pass "PostgreSQL CREATE TEMP TABLE 被正确拒绝"
fi

echo ""

# ============================================================
# MySQL 只读账号验证（TCP + 密码认证）
# ============================================================
echo "--- MySQL ${MYSQL_READER_USER} ---"

# 0. 错误密码必须认证失败
if ${COMPOSE} exec -T -e MYSQL_PWD="wrong_pw_123" demo-mysql \
    mysql --protocol=tcp -u "${MYSQL_READER_USER}" webdb_demo -e "SELECT 1;" > /dev/null 2>&1; then
  fail "MySQL 错误密码认证未拒绝"
else
  pass "MySQL 错误密码认证被正确拒绝"
fi

# 1. 正确密码 SELECT 成功（MYSQL_PWD 来自环境变量，不暴露在 argv）
if ${COMPOSE} exec -T -e MYSQL_PWD demo-mysql \
    mysql --protocol=tcp -u "${MYSQL_READER_USER}" webdb_demo -e "SELECT count(*) FROM employees;" > /dev/null 2>&1; then
  pass "MySQL SELECT 成功"
else
  fail "MySQL SELECT 失败 — 只读账号应能执行 SELECT"
fi

# 2. INSERT 被拒绝
if ${COMPOSE} exec -T -e MYSQL_PWD demo-mysql \
    mysql --protocol=tcp -u "${MYSQL_READER_USER}" webdb_demo -e "INSERT INTO departments(name) VALUES ('test_insert');" > /dev/null 2>&1; then
  fail "MySQL INSERT 未被拒绝"
else
  pass "MySQL INSERT 被正确拒绝"
fi

# 3. UPDATE 被拒绝
if ${COMPOSE} exec -T -e MYSQL_PWD demo-mysql \
    mysql --protocol=tcp -u "${MYSQL_READER_USER}" webdb_demo -e "UPDATE employees SET salary = 0 WHERE id = 1;" > /dev/null 2>&1; then
  fail "MySQL UPDATE 未被拒绝"
else
  pass "MySQL UPDATE 被正确拒绝"
fi

# 4. DELETE 被拒绝
if ${COMPOSE} exec -T -e MYSQL_PWD demo-mysql \
    mysql --protocol=tcp -u "${MYSQL_READER_USER}" webdb_demo -e "DELETE FROM employees WHERE id = 1;" > /dev/null 2>&1; then
  fail "MySQL DELETE 未被拒绝"
else
  pass "MySQL DELETE 被正确拒绝"
fi

# 5. DDL 被拒绝
if ${COMPOSE} exec -T -e MYSQL_PWD demo-mysql \
    mysql --protocol=tcp -u "${MYSQL_READER_USER}" webdb_demo -e "CREATE TABLE test_ddl (id INT);" > /dev/null 2>&1; then
  fail "MySQL DDL 未被拒绝"
else
  pass "MySQL DDL 被正确拒绝"
fi

echo ""

# ============================================================
# 结果汇总
# ============================================================
echo "=== 结果: ${PASS} 通过, ${FAIL} 失败 ==="
if [ "$FAIL" -gt 0 ]; then
  echo -e "${RED}验证未通过 — 存在 ${FAIL} 项失败${NC}"
  exit 1
else
  echo -e "${GREEN}全部验证通过 ✅${NC}"
  exit 0
fi

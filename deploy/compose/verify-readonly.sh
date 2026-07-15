#!/bin/bash
# P0-01 只读账号权限验证脚本
# 使用 docker compose exec 在容器内执行测试，无需本地安装 psql/mysql 客户端
# 验证 demo_reader 账号：
#   1. 可以执行 SELECT
#   2. 不能执行 INSERT、UPDATE、DELETE
#   3. 不能执行 DDL (CREATE/DROP/ALTER)
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
PG_READER_PASSWORD="${DEMO_PG_READER_PASSWORD:-change_me}"
MYSQL_READER_PASSWORD="${DEMO_MYSQL_PASSWORD:-change_me}"

echo "=== P0-01 只读账号权限验证 ==="
echo ""

# ============================================================
# PostgreSQL 只读账号验证
# ============================================================
echo "--- PostgreSQL demo_reader ---"

# 1. SELECT 应该成功
if ${COMPOSE} exec -T demo-pg psql -U demo_reader -d webdb_demo -c "SELECT count(*) FROM employees;" > /dev/null 2>&1; then
  pass "PostgreSQL SELECT 成功"
else
  fail "PostgreSQL SELECT 失败 — 只读账号应能执行 SELECT"
fi

# 2. INSERT 应该被拒绝
if ${COMPOSE} exec -T demo-pg psql -U demo_reader -d webdb_demo -c "INSERT INTO departments(name) VALUES ('test_insert');" > /dev/null 2>&1; then
  fail "PostgreSQL INSERT 未被拒绝 — 只读账号不应能 INSERT"
else
  pass "PostgreSQL INSERT 被正确拒绝"
fi

# 3. UPDATE 应该被拒绝
if ${COMPOSE} exec -T demo-pg psql -U demo_reader -d webdb_demo -c "UPDATE employees SET salary = 0 WHERE id = 1;" > /dev/null 2>&1; then
  fail "PostgreSQL UPDATE 未被拒绝 — 只读账号不应能 UPDATE"
else
  pass "PostgreSQL UPDATE 被正确拒绝"
fi

# 4. DELETE 应该被拒绝
if ${COMPOSE} exec -T demo-pg psql -U demo_reader -d webdb_demo -c "DELETE FROM employees WHERE id = 1;" > /dev/null 2>&1; then
  fail "PostgreSQL DELETE 未被拒绝 — 只读账号不应能 DELETE"
else
  pass "PostgreSQL DELETE 被正确拒绝"
fi

# 5. DDL (CREATE TABLE) 应该被拒绝
if ${COMPOSE} exec -T demo-pg psql -U demo_reader -d webdb_demo -c "CREATE TABLE test_ddl (id INT);" > /dev/null 2>&1; then
  fail "PostgreSQL DDL (CREATE TABLE) 未被拒绝 — 只读账号不应能执行 DDL"
else
  pass "PostgreSQL DDL (CREATE TABLE) 被正确拒绝"
fi

echo ""

# ============================================================
# MySQL 只读账号验证
# ============================================================
echo "--- MySQL demo_reader ---"

# 1. SELECT 应该成功
if ${COMPOSE} exec -T demo-mysql mysql --protocol=tcp -u demo_reader -p"${MYSQL_READER_PASSWORD}" webdb_demo -e "SELECT count(*) FROM employees;" > /dev/null 2>&1; then
  pass "MySQL SELECT 成功"
else
  fail "MySQL SELECT 失败 — 只读账号应能执行 SELECT"
fi

# 2. INSERT 应该被拒绝
if ${COMPOSE} exec -T demo-mysql mysql --protocol=tcp -u demo_reader -p"${MYSQL_READER_PASSWORD}" webdb_demo -e "INSERT INTO departments(name) VALUES ('test_insert');" > /dev/null 2>&1; then
  fail "MySQL INSERT 未被拒绝 — 只读账号不应能 INSERT"
else
  pass "MySQL INSERT 被正确拒绝"
fi

# 3. UPDATE 应该被拒绝
if ${COMPOSE} exec -T demo-mysql mysql --protocol=tcp -u demo_reader -p"${MYSQL_READER_PASSWORD}" webdb_demo -e "UPDATE employees SET salary = 0 WHERE id = 1;" > /dev/null 2>&1; then
  fail "MySQL UPDATE 未被拒绝 — 只读账号不应能 UPDATE"
else
  pass "MySQL UPDATE 被正确拒绝"
fi

# 4. DELETE 应该被拒绝
if ${COMPOSE} exec -T demo-mysql mysql --protocol=tcp -u demo_reader -p"${MYSQL_READER_PASSWORD}" webdb_demo -e "DELETE FROM employees WHERE id = 1;" > /dev/null 2>&1; then
  fail "MySQL DELETE 未被拒绝 — 只读账号不应能 DELETE"
else
  pass "MySQL DELETE 被正确拒绝"
fi

# 5. DDL (CREATE TABLE) 应该被拒绝
if ${COMPOSE} exec -T demo-mysql mysql --protocol=tcp -u demo_reader -p"${MYSQL_READER_PASSWORD}" webdb_demo -e "CREATE TABLE test_ddl (id INT);" > /dev/null 2>&1; then
  fail "MySQL DDL (CREATE TABLE) 未被拒绝 — 只读账号不应能执行 DDL"
else
  pass "MySQL DDL (CREATE TABLE) 被正确拒绝"
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

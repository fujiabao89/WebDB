#!/bin/bash
# 元数据库连接池配置插值验证脚本
#
# 验证 deploy/compose/docker-compose.yml 中 api 服务的
#   META_DB_MAX_OPEN_CONNS / META_DB_MAX_IDLE_CONNS / META_DB_CONN_MAX_LIFETIME
# 支持 Compose .env 插值：未设置时回退默认 10 / 2 / 30m；显式设置时透传自定义值。
# 非法值（非整数、idle > open）由 API 启动校验 fail-closed（apps/api/cmd/server/metaPoolConfig，
# 由 cmd/server/main_test.go 的 TestMetaPoolConfig_* 单元测试覆盖，不在本脚本重复）。
#
# 用法：
#   bash deploy/compose/verify-meta-pool-config.sh
# 无需启动任何容器；只渲染 compose 配置并断言插值结果。
set -euo pipefail

PASS=0
FAIL=0
RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

pass() { echo -e "  ${GREEN}✅ PASS${NC}: $1"; PASS=$((PASS + 1)); }
fail() { echo -e "  ${RED}❌ FAIL${NC}: $1"; FAIL=$((FAIL + 1)); }

# 脚本应在仓库根目录运行；允许从任意目录调用。
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
COMPOSE_FILE="deploy/compose/docker-compose.yml"

# 从渲染后的 compose config 中提取 api 服务环境变量值。
# 用法：get_env_var NAME < rendered-config
# 说明：META_DB_MAX_* 仅在 api 服务注入（api-migrate/demo-seed 无），故全局唯一。
get_env_var() {
  local name="$1"
  # 匹配 "      META_DB_MAX_OPEN_CONNS: 10" 这类行，取冒号后的值；
  # compose config 渲染数值/时长时可能加引号（"10"），一并剥离。
  grep -E "^[[:space:]]+${name}:" | head -n1 | sed -E "s/^[[:space:]]+${name}:[[:space:]]*//; s/^\"//; s/\"$//"
}

# 场景 1：未设置插值变量 → 应回退默认 10 / 2 / 30m
echo "▶ 场景 1：未设置插值变量 → 回退默认 10 / 2 / 30m"
rendered_default=$(env -u META_DB_MAX_OPEN_CONNS -u META_DB_MAX_IDLE_CONNS -u META_DB_CONN_MAX_LIFETIME \
  docker compose -f "${REPO_ROOT}/${COMPOSE_FILE}" config)
got_open=$(printf '%s\n' "$rendered_default" | get_env_var META_DB_MAX_OPEN_CONNS)
got_idle=$(printf '%s\n' "$rendered_default" | get_env_var META_DB_MAX_IDLE_CONNS)
got_life=$(printf '%s\n' "$rendered_default" | get_env_var META_DB_CONN_MAX_LIFETIME)
if [ "$got_open" = "10" ] && [ "$got_idle" = "2" ] && [ "$got_life" = "30m" ]; then
  pass "默认渲染 open=$got_open idle=$got_idle lifetime=$got_life"
else
  fail "默认渲染 open=$got_open idle=$got_idle lifetime=$got_life（want 10 / 2 / 30m）"
fi

# 场景 2：显式设置自定义值 → 透传
echo "▶ 场景 2：显式设置自定义值 → 透传"
rendered_custom=$(META_DB_MAX_OPEN_CONNS=25 META_DB_MAX_IDLE_CONNS=5 META_DB_CONN_MAX_LIFETIME=45m \
  docker compose -f "${REPO_ROOT}/${COMPOSE_FILE}" config)
got_open=$(printf '%s\n' "$rendered_custom" | get_env_var META_DB_MAX_OPEN_CONNS)
got_idle=$(printf '%s\n' "$rendered_custom" | get_env_var META_DB_MAX_IDLE_CONNS)
got_life=$(printf '%s\n' "$rendered_custom" | get_env_var META_DB_CONN_MAX_LIFETIME)
if [ "$got_open" = "25" ] && [ "$got_idle" = "5" ] && [ "$got_life" = "45m" ]; then
  pass "自定义渲染 open=$got_open idle=$got_idle lifetime=$got_life"
else
  fail "自定义渲染 open=$got_open idle=$got_idle lifetime=$got_life（want 25 / 5 / 45m）"
fi

# 场景 3：未提供变量时 compose config 语法有效
echo "▶ 场景 3：compose config 语法校验"
if env -u META_DB_MAX_OPEN_CONNS -u META_DB_MAX_IDLE_CONNS -u META_DB_CONN_MAX_LIFETIME \
  docker compose -f "${REPO_ROOT}/${COMPOSE_FILE}" config --quiet 2>/dev/null; then
  pass "docker compose config --quiet 通过"
else
  fail "docker compose config --quiet 失败"
fi

echo
if [ "$FAIL" -eq 0 ]; then
  echo -e "${GREEN}全部 ${PASS} 项通过${NC}"
else
  echo -e "${RED}${FAIL} 项失败${NC}"
  exit 1
fi

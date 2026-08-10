#!/usr/bin/env bash
# fetch-ci-rollup.sh 的确定性测试（finding 1/2/3，共 16 例）：受控 stub gh 覆盖退出码×JSON
# schema 矩阵、正向断言（expect_saved 分步校验 + 攻击样例）与参数解析，不依赖真实 GitHub API。
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
wrapper="$script_dir/fetch-ci-rollup.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
failures=0

PYTHON_BIN="${PYTHON_BIN:-}"
if [ -z "$PYTHON_BIN" ]; then
  for c in python3 python; do
    if command -v "$c" >/dev/null 2>&1 && "$c" -c 'import json' >/dev/null 2>&1; then PYTHON_BIN=$c; break; fi
  done
fi
[ -n "$PYTHON_BIN" ] || { echo "FAIL: 未找到 python3/python"; exit 1; }

# 构造 stub gh：echo 指定内容并以指定退出码退出
make_stub() { # make_stub <name> <stdout> <exit_code>
  local name=$1 stdout=$2 exit_code=$3
  cat > "$tmp/$name" <<EOF
#!/usr/bin/env bash
printf '%s\n' '$stdout'
exit $exit_code
EOF
  chmod +x "$tmp/$name"
}

# 正向断言：分别验证 wrapper exit 0、文件存在且非空、无 marker、合法 JSON、含期望 state/bucket
# 返回 0 = 通过；1 = 失败（由调用方累加 failures）。不用 "cmd && grep-marker 短路成 ok" 写法。
expect_saved() { # expect_saved <描述> <stub> <expected_state> <expected_bucket>
  local desc=$1 stub=$2 expected_state=$3 expected_bucket=$4
  local out="$tmp/out_$stub.json"
  if ! bash "$wrapper" 1 fujiabao89/WebDB "$out" "$tmp/$stub" >/dev/null 2>&1; then
    echo "FAIL (wrapper 应 exit 0): $desc"; return 1
  fi
  if [ ! -s "$out" ]; then echo "FAIL (输出文件为空): $desc"; return 1; fi
  if grep -q 'CI_EVIDENCE_UNAVAILABLE' "$out"; then echo "FAIL (不应含 marker): $desc"; return 1; fi
  if ! "$PYTHON_BIN" -c 'import json,sys; json.load(open(sys.argv[1]))' "$out" 2>/dev/null; then
    echo "FAIL (输出非合法 JSON): $desc"; return 1
  fi
  if ! grep -q "\"$expected_state\"" "$out" || ! grep -q "\"$expected_bucket\"" "$out"; then
    echo "FAIL (缺少期望 state=$expected_state/bucket=$expected_bucket): $desc"; return 1
  fi
  echo "ok: $desc"; return 0
}
expect_unavailable() { # expect_unavailable <描述> <stub>
  local desc=$1 stub=$2
  if bash "$wrapper" 1 fujiabao89/WebDB "$tmp/out" "$tmp/$stub" >/dev/null 2>&1; then
    echo "FAIL (期望不可用): $desc"; failures=$((failures+1))
  elif grep -q 'CI_EVIDENCE_UNAVAILABLE' "$tmp/out"; then
    echo "ok (不可用): $desc"
  else
    echo "FAIL (未写 marker): $desc"; failures=$((failures+1))
  fi
}

# 合法数组
OK_ARRAY='[{"name":"CI","state":"SUCCESS","description":"ok","link":"http://x","bucket":"success"}]'
PENDING_ARRAY='[{"name":"review","state":"PENDING","description":"running","link":"http://y","bucket":"pending"}]'
FAIL_ARRAY='[{"name":"CI","state":"FAILURE","description":"boom","link":"http://z","bucket":"failure"}]'
MISSING_FIELD='[{"name":"CI","state":"SUCCESS"}]'

# --- Finding 2：退出码 × schema 矩阵 ---
make_stub gh0_ok "$OK_ARRAY" 0
if ! expect_saved "exit 0 + 合法非空数组通过" gh0_ok SUCCESS success; then failures=$((failures+1)); fi

make_stub gh8_pending "$PENDING_ARRAY" 8
if ! expect_saved "exit 8 + 合法 pending 数组通过" gh8_pending PENDING pending; then failures=$((failures+1)); fi

make_stub gh1_fail "$FAIL_ARRAY" 1
if ! expect_saved "exit 1 + 明确失败 check 数组通过" gh1_fail FAILURE failure; then failures=$((failures+1)); fi

# F2 攻击样例：wrapper 明确返回非零并写 marker → expect_saved 必须报告失败
cat > "$tmp/ghattack" <<'EOF'
#!/usr/bin/env bash
printf 'CI_EVIDENCE_UNAVAILABLE\n'
exit 1
EOF
chmod +x "$tmp/ghattack"
if expect_saved "攻击样例（exit 1 + marker）应被报告失败" ghattack SUCCESS success >/dev/null 2>&1; then
  echo "FAIL: 攻击样例未被 expect_saved 识别为失败"; failures=$((failures+1))
else
  echo "ok (被识别): 攻击样例 exit 1 + marker 被 expect_saved 报告失败"
fi

make_stub gh1_empty '[]' 1
expect_unavailable "exit 1 + 空数组拒绝" gh1_empty

make_stub gh1_nofail "$PENDING_ARRAY" 1
expect_unavailable "exit 1 + 无失败状态数组拒绝" gh1_nofail

make_stub gh2_ok "$OK_ARRAY" 2
expect_unavailable "exit 2 + 合法 JSON 拒绝" gh2_ok

make_stub gh4_empty '[]' 4
expect_unavailable "exit 4 + [] 拒绝" gh4_empty

make_stub gh4_ok "$OK_ARRAY" 4
expect_unavailable "exit 4 + 合法非空数组拒绝" gh4_ok

make_stub gh0_obj '{}' 0
expect_unavailable "exit 0 + {} 拒绝" gh0_obj

make_stub gh0_null 'null' 0
expect_unavailable "exit 0 + null 拒绝" gh0_null

make_stub gh0_missing "$MISSING_FIELD" 0
expect_unavailable "exit 0 + 缺字段数组拒绝" gh0_missing

make_stub gh0_badjson 'not-json' 0
expect_unavailable "exit 0 + 非 JSON 拒绝" gh0_badjson

# --- Finding 3：参数解析 ---
# 省略 gh_bin 时正确使用 PATH 中的 stub gh
cat > "$tmp/gh" <<EOF
#!/usr/bin/env bash
printf '%s\n' '$OK_ARRAY'
exit 0
EOF
chmod +x "$tmp/gh"
OLD_PATH="$PATH"
export PATH="$tmp:$PATH"
if bash "$wrapper" 1 fujiabao89/WebDB "$tmp/out" >/dev/null 2>&1 && grep -q '"SUCCESS"' "$tmp/out"; then
  echo "ok: 省略 gh_bin 使用 PATH 中的 stub gh"
else
  echo "FAIL: 省略 gh_bin 应使用 PATH 中的 gh"; failures=$((failures+1))
fi
export PATH="$OLD_PATH"

# 少于三个参数 → exit 2
if bash "$wrapper" 1 >/dev/null 2>&1; then
  echo "FAIL: 少于三个参数应 exit 2"; failures=$((failures+1))
else
  ec=$?
  if [ "$ec" -eq 2 ]; then echo "ok: 少于三个参数 exit 2"; else echo "FAIL: exit 应为 2，实际 $ec"; failures=$((failures+1)); fi
fi

# 额外参数只追加一次，不夹带原始位置参数（stub 把 $@ 编码进合法 JSON 的 name 字段）
cat > "$tmp/ghargs" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "[{\"name\":\"ARGS $*\",\"state\":\"SUCCESS\",\"description\":\"ok\",\"link\":\"http://x\",\"bucket\":\"success\"}]"
exit 0
EOF
chmod +x "$tmp/ghargs"
if bash "$wrapper" 7 fujiabao89/WebDB "$tmp/out" "$tmp/ghargs" --extra1 --extra2 >/dev/null 2>&1 \
   && grep -q 'ARGS pr checks 7 --repo fujiabao89/WebDB --json name,state,description,link,bucket --extra1 --extra2' "$tmp/out"; then
  echo "ok: 额外参数只追加一次（原位置参数作为正确 positional 传入）"
else
  echo "FAIL: 额外参数未正确追加"; failures=$((failures+1))
fi

if (( failures > 0 )); then
  echo "fetch-ci-rollup 测试失败：$failures 项"
  exit 1
fi
echo "fetch-ci-rollup 测试通过。"

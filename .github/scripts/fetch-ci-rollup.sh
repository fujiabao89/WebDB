#!/usr/bin/env bash
# 受信步骤：获取 PR 的 CI check rollup 结构化 JSON（finding 1/2/3）。
#
# 退出码与 JSON schema 联合校验，明确处理 gh pr checks 的退出码：
#   exit 0 + 合法非空数组（全 success）              → 证据可用
#   exit 8 + 合法非空数组（含 pending）              → 证据可用（review job 自身 pending 不误判）
#   exit 1 + 合法非空数组且含 failure/cancelled 状态 → 证据可用（有真实失败）
#   exit 1 + 空数组 / 无 failure 状态                → fail closed（无法解释失败原因）
#   其它退出码（2/4/未知）：无论 stdout 是否合法 JSON → CI_EVIDENCE_UNAVAILABLE
# 空数组视为"没有可用 CI 证据"，写入 marker 并程序化强制 ESCALATE。
# 部分/未知形状的 JSON 不得当作完整 rollup。
#
# JSON schema 校验（除 json.load 外）：顶层必须是数组；元素必须是对象；
# 每个元素必须有 name/state/description/link/bucket 字符串字段。
# stderr 仅捕获用于诊断，不输出其内容（避免泄露 token/敏感信息）。
#
# 用法: fetch-ci-rollup.sh <pr_number> <repo> <output_file> [gh_bin] [gh 额外参数...]
#   退出码 0 = 证据可用；1 = CI_EVIDENCE_UNAVAILABLE；2 = 参数错误
set -euo pipefail

if [ "$#" -lt 3 ]; then
  echo "usage: fetch-ci-rollup.sh <pr_number> <repo> <output_file> [gh_bin] [gh 额外参数...]" >&2
  exit 2
fi

PR_NUMBER=$1
REPO=$2
OUT_FILE=$3
if [ "$#" -eq 3 ]; then
  GH_BIN=gh
  shift 3
else
  GH_BIN=$4
  shift 4
fi

PYTHON_BIN="${PYTHON_BIN:-}"
if [ -z "$PYTHON_BIN" ]; then
  for candidate in python3 python; do
    if command -v "$candidate" >/dev/null 2>&1 && "$candidate" -c 'import json' >/dev/null 2>&1; then
      PYTHON_BIN=$candidate
      break
    fi
  done
fi
if [ -z "$PYTHON_BIN" ]; then
  echo "FAIL: 未找到可用的 python3/python" >&2
  exit 1
fi

err_tmp="$(mktemp)"
trap 'rm -f "$err_tmp"' EXIT

set +e
rollup="$("$GH_BIN" pr checks "$PR_NUMBER" --repo "$REPO" \
  --json name,state,description,link,bucket "$@" 2>"$err_tmp")"
ec=$?
set -e

fail_unavailable() {
  printf '%s\n' "CI_EVIDENCE_UNAVAILABLE" > "$OUT_FILE"
  echo "ci rollup unavailable (gh exit=$ec, stderr captured for diagnostics)" >&2
  exit 1
}

# 1) 未知退出码（2/4 等）：无论 stdout 是否合法 JSON，都视为证据不可用
case "$ec" in
  0|1|8) ;;
  *) fail_unavailable ;;
esac

# 2) JSON schema 校验：非 json.load 单独判断，需满足数组/对象/字段类型
schema="$("$PYTHON_BIN" -c '
import json, sys
try:
    data = json.loads(sys.argv[1])
except Exception:
    print("BAD:INVALID_JSON"); sys.exit(0)
if not isinstance(data, list):
    print("BAD:NOT_ARRAY"); sys.exit(0)
if len(data) == 0:
    print("EMPTY"); sys.exit(0)
for item in data:
    if not isinstance(item, dict):
        print("BAD:NOT_OBJECT"); sys.exit(0)
    for f in ("name", "state", "description", "link", "bucket"):
        if not isinstance(item.get(f), str):
            print("BAD:FIELD_%s" % f); sys.exit(0)
buckets = [i.get("bucket", "") for i in data]
states = [i.get("state", "") for i in data]
failure = sum(1 for b, s in zip(buckets, states)
              if b in ("failure", "cancelled") or s.upper() in ("FAILURE", "CANCELLED", "ERROR"))
pending = sum(1 for b, s in zip(buckets, states)
              if b == "pending" or s.upper() == "PENDING")
print("OK total=%d failure=%d pending=%d" % (len(data), failure, pending))
' "$rollup")"

case "$schema" in
  BAD:*|EMPTY) fail_unavailable ;;
esac

failure="$(printf '%s' "$schema" | sed -n 's/^OK total=[0-9]* failure=\([0-9]*\) pending=[0-9]*$/\1/p')"

# 3) exit 1 语义：只在存在 failure/cancelled 状态时接受；无法解释则 fail closed
if [ "$ec" -eq 1 ] && [ "${failure:-0}" -le 0 ]; then
  fail_unavailable
fi

# 到这里：exit 0/8 + 合法非空数组，或 exit 1 + 有 failure → 证据可用
printf '%s\n' "$rollup" > "$OUT_FILE"
echo "ci rollup saved (gh exit=$ec)"
exit 0

#!/usr/bin/env bash
# 静态断言 .github/workflows/opencode-review.yml 的安全不变量。
# 用法: validate-opencode-workflow.sh [workflow-file]
# 默认文件: .github/workflows/opencode-review.yml（相对本脚本所在目录）。
# 失败时退出码非 0；成功输出 OK。
set -euo pipefail

workflow="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/workflows/opencode-review.yml}"
failures=0

# 结构断言需要能 import yaml 的 Python 解释器；优先 python3（GitHub Actions runner），
# 回退 python（Windows 本地）。逐个候选实测可用（能 import yaml）才采用，
# 避免 Windows Store 的 python3 存根被误判为可用。
PYTHON_BIN="${PYTHON_BIN:-}"
if [ -z "$PYTHON_BIN" ]; then
  for candidate in python3 python; do
    if command -v "$candidate" >/dev/null 2>&1 && "$candidate" -c 'import yaml, sys' >/dev/null 2>&1; then
      PYTHON_BIN=$candidate
      break
    fi
  done
  if [ -z "$PYTHON_BIN" ]; then
    echo "FAIL: 未找到可用的 python3/python（需支持 yaml）"
    exit 1
  fi
fi

fail() {
  echo "FAIL: $1"
  failures=$((failures + 1))
}

# check <描述> <grep -F 模式> [file]
check() {
  local desc=$1 pattern=$2 file=${3:-$workflow}
  if ! grep -Fq -- "$pattern" "$file"; then
    fail "$desc（缺少：$pattern）"
  fi
}

# check_absent <描述> <grep -F 模式> [file]
check_absent() {
  local desc=$1 pattern=$2 file=${3:-$workflow}
  if grep -Fq -- "$pattern" "$file"; then
    fail "$desc（不应出现：$pattern）"
  fi
}

# --- 固定 SHA（P3-3 已在线核验归属） ---
check "opencode action 固定 SHA" 'anomalyco/opencode/github@d7b115f623760e68a4749d16508a9eca350f246f'
check "checkout 固定 SHA" 'actions/checkout@11d5960a326750d5838078e36cf38b85af677262'

# --- 权限边界 ---
check "use_github_token 开启" 'use_github_token: true'
# pull-requests: write 在整个文件中只可能出现在 job 级 permissions 块
# （workflow 级仅 contents: read），故存在即证明 job 级权限存在——这是轻量静态校验，
# 不做 YAML 结构解析，断言的是"至少存在一处 job 级 pull-requests: write"。
check "job 级 permissions 存在（含 pull-requests: write）" 'pull-requests: write'
check "permissions 含 contents: read" 'contents: read'
check_absent "job 权限不含 id-token" 'id-token'
check "agent edit deny" '"edit": "deny"'
check "agent write deny" '"write": "deny"'
check "agent webfetch deny" '"webfetch": "deny"'

# bash 白名单不得含写/状态变更 git 命令（仅允许只读子命令）。
if grep -E '"git [^"]*": "allow"' "$workflow" |
  grep -qE 'git (push|reset|checkout|clean|merge|rebase|add|commit|restore|switch|stash|clone|pull|fetch|rm|branch|tag|config|update-index|apply|am)'; then
  fail "agent bash 白名单含写/状态变更 git 命令"
fi

# --- secret 防护（P3-2）：raw secret 仅出现在 Run 步骤 env ---
if [ "$(grep -Fc 'DEEPSEEK_API_KEY: ${{ secrets.DEEPSEEK_API_KEY }}' "$workflow")" -ne 1 ]; then
  fail "raw secret 必须只在 Run 步骤 env 出现一次（P3-2）"
fi

# guard 存在：job env 布尔派生 + Run 步骤 if 引用（P2-3）。
check "secret 存在性 guard 存在" 'secrets.DEEPSEEK_API_KEY !='
check "Run 步骤 if 引用 HAS_SECRET" 'if: ${{ env.HAS_SECRET'

# --- P2-1 规则源保护 ---
check "禁用项目级配置 Flag" 'OPENCODE_DISABLE_PROJECT_CONFIG'
check "删除嵌套 AGENTS.md 命令" '-iname'
check "skip-worktree 隐藏规则变更" 'git update-index --skip-worktree'

# --- P3-1 base.sha 经 env 传递 ---
check "BASE_SHA 从 env 取用" 'BASE_SHA: ${{ github.event.pull_request.base.sha }}'
check_absent "run 脚本不得直插 base.sha 模板" 'ref=${{ github.event.pull_request.base.sha }}'

# --- YAML 结构断言（python3）：grep 只能验证文本存在，无法验证配置位置/值，
#     如"仅保留注释但删除实际配置"会漏检，故按 YAML 层级断言 workflow/job 权限、
#     Run 步骤的 env 与 if、job env 的 secret 暴露面。 ---
if ! struct_output="$("$PYTHON_BIN" - "$workflow" <<'PYEOF' 2>&1
import sys
import yaml

data = yaml.safe_load(open(sys.argv[1], encoding="utf-8"))
errors = []
review = (data.get("jobs") or {}).get("review")

def err(msg):
    errors.append(msg)

if review is None:
    err("缺少 jobs.review")
else:
    wf_perms = data.get("permissions") or {}
    if set(wf_perms.items()) != {("contents", "read")}:
        err("workflow 级 permissions 必须仅为 contents: read，实际: %r" % (wf_perms,))
    job_perms = review.get("permissions") or {}
    if set(job_perms.items()) != {("contents", "read"), ("pull-requests", "write")}:
        err("job 级 permissions 必须为 contents: read + pull-requests: write，实际: %r" % (job_perms,))
    job_env = review.get("env") or {}
    if "DEEPSEEK_API_KEY" in job_env:
        err("raw DEEPSEEK_API_KEY 不得出现在 job env（P3-2）")
    if "HAS_SECRET" not in job_env or "secrets.DEEPSEEK_API_KEY !=" not in str(job_env.get("HAS_SECRET")):
        err("job env 必须含 HAS_SECRET 布尔派生（secrets.DEEPSEEK_API_KEY !=）")
    runs = [s for s in (review.get("steps") or []) if s.get("name") == "Run OpenCode review"]
    if len(runs) != 1:
        err("必须恰有一个 Run OpenCode review 步骤")
    else:
        r = runs[0]
        renv = r.get("env") or {}
        if renv.get("OPENCODE_DISABLE_PROJECT_CONFIG") != "true":
            err("Run 步骤 env 必须含 OPENCODE_DISABLE_PROJECT_CONFIG: true（仅注释不算数）")
        if "DEEPSEEK_API_KEY" not in renv:
            err("Run 步骤 env 必须注入 DEEPSEEK_API_KEY")
        if "HAS_SECRET" not in (r.get("if") or ""):
            err("Run 步骤 if 必须引用 HAS_SECRET")

for line in errors:
    print("STRUCT FAIL: %s" % line)
sys.exit(1 if errors else 0)
PYEOF
)"; then
  while IFS= read -r line; do
    [ -n "$line" ] && fail "$line"
  done <<< "$struct_output"
fi

if (( failures > 0 )); then
  echo "opencode-review workflow 不变量校验失败：$failures 项"
  exit 1
fi

echo "OK: opencode-review workflow invariants satisfied"

#!/usr/bin/env bash
# 静态校验 .github/workflows/opencode-review.yml 的安全不变量（finding 3）。
# 用法: validate-opencode-workflow.sh [workflow-file]
# 通过 YAML 结构断言 + 少量确定性 grep，精确校验事件类型/权限/step 顺序/uses/版本校验和/
# secret 注入位置/bash deny/share false/无自动提交能力。不做"文本任意位置出现"式弱断言。
set -euo pipefail

workflow="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/workflows/opencode-review.yml}"
failures=0

fail() {
  echo "FAIL: $1"
  failures=$((failures + 1))
}

check() {
  local desc=$1 pattern=$2
  if ! grep -Fq -- "$pattern" "$workflow"; then
    fail "$desc（缺少：$pattern）"
  fi
}

check_absent() {
  local desc=$1 pattern=$2
  if grep -Fq -- "$pattern" "$workflow"; then
    fail "$desc（不应出现：$pattern）"
  fi
}

# --- 确定性 grep：固定 SHA / 禁止危险模式 ---
check "checkout 固定 SHA" 'actions/checkout@11d5960a326750d5838078e36cf38b85af677262'
check_absent "禁止 curl|bash" '| bash'
check_absent "禁止 git push" 'git push'
check_absent "禁止 git commit" 'git commit'
check_absent "禁止 latest / 可变 tag" '@latest'
check_absent "禁止 actions/cache（未固定嵌套 action）" 'actions/cache@'

# 聚合测试入口必须引用全部三个子测试（finding 4）
aggregate="${OPENCODE_REVIEW_AGGREGATE:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/test-opencode-review.sh}"
for sub in test-opencode-workflow.sh test-review-output.sh test-fetch-ci-rollup.sh; do
  grep -q -- "$sub" "$aggregate" || fail "聚合入口必须调用 $sub（finding 4）"
done

# --- YAML 结构断言（python）：事件/权限/step/uses/secret 位置/agent 配置 ---
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

if ! struct_output="$("$PYTHON_BIN" - "$workflow" <<'PYEOF' 2>&1
import sys
import yaml

data = yaml.safe_load(open(sys.argv[1], encoding="utf-8"))
errors = []

def err(msg):
    errors.append(msg)

# on 键：PyYAML 1.1 把 on 解析为 True；GitHub Actions 用 YAML 1.2，需显式处理两键。
triggers = data.get("on")
if triggers is None:
    triggers = data.get(True)  # YAML 1.1 下 on 被解析为 True
if not isinstance(triggers, dict):
    err("缺少 on 触发块")
    triggers = {}
pr = triggers.get("pull_request")
if pr is None and isinstance(triggers, dict):
    err("必须只通过 pull_request 触发")
else:
    for key in triggers:
        if key != "pull_request":
            err("不允许的触发类型：%s" % key)
    if "pull_request_target" in triggers or "pull_request_target" in (triggers.get("pull_request") or {}):
        err("禁止 pull_request_target")
    pr_types = (pr or {}).get("types") or []
    if pr_types != ["opened", "synchronize", "reopened", "ready_for_review"]:
        err("pull_request types 必须恰好为 opened/synchronize/reopened/ready_for_review，实际: %r" % (pr_types,))

# 权限：workflow 级 = {contents: read}；job 级 = {contents: read, pull-requests: write}
wf_perms = data.get("permissions") or {}
if set(wf_perms.items()) != {("contents", "read")}:
    err("workflow 级 permissions 必须仅为 contents: read，实际: %r" % (wf_perms,))

jobs = data.get("jobs") or {}
if set(jobs.keys()) != {"review"}:
    err("必须只有 job：review，实际: %r" % (list(jobs.keys()),))
job = jobs.get("review")
if job is None:
    err("缺少 jobs.review")
else:
    job_perms = job.get("permissions") or {}
    if set(job_perms.items()) != {("contents", "read"), ("pull-requests", "write")}:
        err("job 级 permissions 必须为 contents: read + pull-requests: write，实际: %r" % (job_perms,))

    job_env = job.get("env") or {}
    if "DEEPSEEK_API_KEY" in job_env or "GITHUB_TOKEN" in job_env or "GH_TOKEN" in job_env:
        err("job env 不得含 raw secret/token（P0/P3-2）")
    if "HAS_SECRET" not in job_env or "secrets.DEEPSEEK_API_KEY !=" not in str(job_env.get("HAS_SECRET")):
        err("job env 必须含 HAS_SECRET 布尔派生")

    steps = job.get("steps") or []
    expected = [
        "Checkout repository",
        "Remove persisted git credentials",
        "Validate workflow invariants",
        "Prepare trusted review context",
        "Install pinned OpenCode CLI",
        "Configure review agent permissions",
        "Run OpenCode review with output validation",
        "Publish review comment",
        "Notice when review is skipped",
    ]
    got = [s.get("name") for s in steps]
    if got != expected:
        err("step 名称/数量/顺序必须为 %s，实际: %r" % (expected, got))

    for s in steps:
        uses = s.get("uses") or ""
        if uses and uses != "actions/checkout@11d5960a326750d5838078e36cf38b85af677262":
            err("不允许的 uses：%s" % uses)

    model_step = next((s for s in steps if s.get("name") == "Run OpenCode review with output validation"), None)
    ctx_step = next((s for s in steps if s.get("name") == "Prepare trusted review context"), None)
    pub_step = next((s for s in steps if s.get("name") == "Publish review comment"), None)
    cfg_step = next((s for s in steps if s.get("name") == "Configure review agent permissions"), None)
    inst_step = next((s for s in steps if s.get("name") == "Install pinned OpenCode CLI"), None)
    self_step = next((s for s in steps if s.get("name") == "Validate workflow invariants"), None)
    if self_step is None:
        err("缺少 Validate workflow invariants 自检步骤")
    elif "test-opencode-review.sh" not in (self_step.get("run") or ""):
        err("自检步骤必须调用聚合入口 test-opencode-review.sh（finding 4）")

    with open(sys.argv[1], encoding="utf-8") as fh:
        raw = fh.read()
    secret_count = raw.count("DEEPSEEK_API_KEY: ${{ secrets.DEEPSEEK_API_KEY }}")
    if secret_count != 1:
        err("raw DEEPSEEK_API_KEY 必须只在模型步骤 env 出现一次（P3-2），实际 %d 次" % secret_count)

    if model_step is None:
        err("缺少模型步骤 Run OpenCode review with output validation")
    else:
        menv = model_step.get("env") or {}
        if "DEEPSEEK_API_KEY" not in menv:
            err("模型步骤 env 必须注入 DEEPSEEK_API_KEY")
        if "GITHUB_TOKEN" in menv or "GH_TOKEN" in menv:
            err("模型步骤 env 不得持有 GITHUB_TOKEN/GH_TOKEN（P0：模型进程不持有 token）")
        if menv.get("OPENCODE_DISABLE_PROJECT_CONFIG") != "true":
            err("模型步骤 env 必须 OPENCODE_DISABLE_PROJECT_CONFIG: true")
        run = model_step.get("run") or ""
        if "github run" in run or "github-run" in run:
            err("模型步骤不得使用 github-run 子命令（含自动 commit/push 路径，P0）")
        if "--no-share" not in run:
            err("模型步骤 run 必须使用 --no-share 关闭 session 分享")
        if "validate-review-output.py" not in run:
            err("模型步骤必须调用 validate-review-output.py 做确定性格式校验")
        if "for attempt in 1 2 3" not in run:
            err("模型步骤必须有限次数重试（for attempt in 1 2 3）")
        if "fail closed" not in run or "exit 1" not in run:
            err("模型步骤重试仍失败时必须 fail closed（exit 1）")

    if ctx_step is None:
        err("缺少受信上下文准备步骤 Prepare trusted review context")
    else:
        cenv = ctx_step.get("env") or {}
        if "GH_TOKEN" not in cenv and "GITHUB_TOKEN" not in cenv:
            err("上下文准备步骤必须持有 GH_TOKEN/GITHUB_TOKEN")
        if "DEEPSEEK_API_KEY" in cenv:
            err("上下文准备步骤不得持有 DEEPSEEK_API_KEY")
        crun = ctx_step.get("run") or ""
        if "review-context" not in crun:
            err("上下文准备步骤必须写入 review-context")
        if "fetch-ci-rollup.sh" not in crun:
            err("上下文准备步骤必须调用 fetch-ci-rollup.sh（结构化 CI rollup，exit 0/8 保留，finding 一）")
        if "TASK_EVIDENCE_UNAVAILABLE" not in crun:
            err("上下文准备步骤必须写 TASK_EVIDENCE_UNAVAILABLE（无受信 Task 来源，finding 三）")
        if "required-conclusion" not in crun:
            err("上下文准备步骤必须计算 required-conclusion（finding 二）")
        if "ESCALATE" not in crun:
            err("上下文准备步骤必须提示证据不足时 required 为 ESCALATE")
        # 模型 prompt 同样要求 ESCALATE/证据不足/CI_EVIDENCE_UNAVAILABLE/TASK_EVIDENCE_UNAVAILABLE
        if model_step is not None:
            mrun = model_step.get("run") or ""
            if "ESCALATE" not in mrun or "证据不足" not in mrun:
                err("模型步骤 prompt 必须要求证据不足时输出 ESCALATE（finding 4）")
            if "CI_EVIDENCE_UNAVAILABLE" not in mrun or "TASK_EVIDENCE_UNAVAILABLE" not in mrun:
                err("模型步骤 prompt 必须要求 CI/Task 证据不足时输出 ESCALATE（P2）")
            if "--required" not in mrun or "$(cat" not in mrun or "required-conclusion" not in mrun:
                err("模型步骤必须用 $(cat \"$ctx/required-conclusion.txt\") 读取受信 required conclusion 并经 --required 传给校验器（程序约束，finding 二）")

    if pub_step is None:
        err("缺少受信评论发布步骤 Publish review comment")
    else:
        penv = pub_step.get("env") or {}
        if "GH_TOKEN" not in penv and "GITHUB_TOKEN" not in penv:
            err("评论发布步骤必须持有 GH_TOKEN/GITHUB_TOKEN")
        if "DEEPSEEK_API_KEY" in penv:
            err("评论发布步骤不得持有 DEEPSEEK_API_KEY")
        if "model-output.validated.md" not in (pub_step.get("run") or ""):
            err("评论发布步骤必须只发布已校验的输出 model-output.validated.md")

    if cfg_step is None:
        err("缺少 Configure review agent permissions 步骤")
    else:
        cfg = cfg_step.get("run") or ""
        for pat in ['"edit": "deny"', '"write": "deny"', '"webfetch": "deny"', '"bash": "deny"', '"share": "disabled"']:
            if pat not in cfg:
                err("agent 配置缺少：%s" % pat)
        if '"share": false' in cfg:
            err("agent 配置 share 键必须用 schema 合法值 disabled（布尔 false 会使整个配置加载失败）")
        if '"read": "allow"' not in cfg:
            err("agent 配置必须允许 read")
        if "external_directory" not in cfg:
            err("agent 配置必须包含 external_directory")

    if inst_step is None:
        err("缺少 Install pinned OpenCode CLI 步骤")
    else:
        irun = inst_step.get("run") or ""
        if "v1.18.15" not in irun:
            err("安装步骤必须固定 CLI 版本 v1.18.15")
        if "sha256sum" not in irun or "d842e0e8c622c672a481b7dc6f0329009b64db96b2ba6041e56f4f93f0293b1c" not in irun:
            err("安装步骤必须校验 CLI sha256（d842e0e8…）")

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

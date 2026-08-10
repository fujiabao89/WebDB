#!/usr/bin/env bash
# validate-review-output.py 的确定性测试（finding 一/二/语义/结构，共 98 例）：覆盖合法通过、
# 证据不足强制 ESCALATE、徽章/结论一致性、finding 字段/路径/行号严格校验、验收矩阵结构、
# 结论位置、结论语义（P0-P2+APPROVE 拒、矩阵失败/证据不足+APPROVE 拒、RC 需阻断依据、
# ESCALATE 合法）、结构块（恰好 1 Findings + 1 矩阵、顺序、无未知块）、
# details 结构严格扫描（<details open>/属性/大小写/嵌套/多余 closing/同行/额外 summary 均拒）、
# 徽章 P0-P3 计数严格解析（恰好 4 项、顺序固定、重复/缺失/额外均拒）、
# 完整文档布局（块外仅徽章/置信度/结论三行，块外 finding/标题/表格/说明均拒）、
# 徽章整行 fullmatch（拒绝 P4/P10/未知级别/前缀/后缀/括号后尾文）、
# 置信度完整行（唯一、倒数第二非空行、级别高/中/低、风险非空）、空输出。
# 样例在脚本内生成（保存于仓库测试中），不依赖真实模型输出。
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
validator="$script_dir/validate-review-output.py"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
failures=0

PYTHON_BIN="${PYTHON_BIN:-}"
if [ -z "$PYTHON_BIN" ]; then
  for c in python3 python; do
    if command -v "$c" >/dev/null 2>&1 && "$c" -c 'import json' >/dev/null 2>&1; then PYTHON_BIN=$c; break; fi
  done
fi
if [ -z "$PYTHON_BIN" ]; then echo "FAIL: 未找到 python3/python"; exit 1; fi

# 构造合法审查 markdown（结论 APPROVE/REQUEST CHANGES/ESCALATE）
make_review() { # make_review <conclusion> <out_file>
  local concl=$1 out=$2
  local count=0
  case "$concl" in
    APPROVE)
      badge='**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）'
      finding=''
      ;;
    "REQUEST CHANGES")
      count=1
      badge='**❌ REQUEST CHANGES** — 1 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理'
      finding='### [P0] 问题 — `apps/api/x.go:10`
事实：xxx
触发：xxx
影响：xxx
依据：xxx
最小修复：xxx'
      ;;
    ESCALATE)
      badge='**⚠️ ESCALATE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题；证据不足）'
      finding=''
      ;;
  esac
  cat > "$out" <<EOF
$badge

<details>
<summary>📋 Findings（$count 条）</summary>

$finding

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

置信度：高；仍未验证的风险：无
结论：$concl
EOF
}

expect_pass() { # expect_pass <描述> <args...>
  local desc=$1; shift
  if "$PYTHON_BIN" "$validator" "$@" >/dev/null 2>&1; then
    echo "ok: $desc"
  else
    echo "FAIL (期望通过): $desc"; failures=$((failures+1))
  fi
}
expect_fail() { # expect_fail <描述> <args...>
  local desc=$1; shift
  if "$PYTHON_BIN" "$validator" "$@" >/dev/null 2>&1; then
    echo "FAIL (期望拒绝): $desc"; failures=$((failures+1))
  else
    echo "ok (被拒绝): $desc"
  fi
}

make_review APPROVE "$tmp/ok-approve.md"
make_review "REQUEST CHANGES" "$tmp/ok-rc.md"
make_review ESCALATE "$tmp/esc.md"

# 1) 正常合法 APPROVE 通过
expect_pass "合法 APPROVE 通过" "$tmp/ok-approve.md"
# 2) 正常合法 REQUEST CHANGES 通过
expect_pass "合法 REQUEST CHANGES 通过" "$tmp/ok-rc.md"
# 3) 证据不足 + ESCALATE 通过
expect_pass "证据不足 + ESCALATE 通过" --required ESCALATE "$tmp/esc.md"
# 4) 证据不足 + APPROVE 拒绝
expect_fail "证据不足 + APPROVE 拒绝" --required ESCALATE "$tmp/ok-approve.md"
# 5) 证据不足 + REQUEST CHANGES 拒绝
expect_fail "证据不足 + REQUEST CHANGES 拒绝" --required ESCALATE "$tmp/ok-rc.md"
# 6) 徽章与最终结论不一致拒绝
sed 's|结论：APPROVE|结论：ESCALATE|' "$tmp/ok-approve.md" > "$tmp/bad-mismatch.md"
expect_fail "徽章与最终结论不一致拒绝" "$tmp/bad-mismatch.md"
# 7) finding 缺必要字段拒绝
sed '/影响：/d' "$tmp/ok-rc.md" > "$tmp/bad-field.md"
expect_fail "finding 缺必要字段拒绝" "$tmp/bad-field.md"
# 8) 空输出拒绝
: > "$tmp/empty.md"
expect_fail "空输出拒绝" "$tmp/empty.md"

# --- Finding 1 新增负向测试 ---
# 生成带真实矩阵/字段的合法 RC 作为变异基底（ok-rc.md 已含 1 条合法 finding + 真实矩阵）

# 9) 裸 ### [P1]（无标题/路径/行号）拒绝
cat > "$tmp/bare.md" <<'EOF'
**❌ REQUEST CHANGES** — 1 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理

<details>
<summary>📋 Findings（1 条）</summary>

### [P1]

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

置信度：高；仍未验证的风险：无
结论：REQUEST CHANGES
EOF
expect_fail "裸 ### [P1] 拒绝" "$tmp/bare.md"

# 10) finding 有标题但无路径/行号 拒绝
sed 's|### \[P0\] 问题 — `apps/api/x.go:10`|### [P1] 只有标题没有路径|' "$tmp/ok-rc.md" > "$tmp/nopath.md"
expect_fail "finding 有标题但无路径/行号 拒绝" "$tmp/nopath.md"

# 11) 字段名存在但内容为空 拒绝
sed 's|事实：xxx|事实：|' "$tmp/ok-rc.md" > "$tmp/emptyfield.md"
expect_fail "字段名存在但内容为空 拒绝" "$tmp/emptyfield.md"

# 12) 声明 1 条但成功解析 0 条 拒绝（heading 含路径但无行号）
sed 's|### \[P0\] 问题 — `apps/api/x.go:10`|### [P1] 标题 — `apps/api/x.go`|' "$tmp/ok-rc.md" > "$tmp/noline.md"
expect_fail "声明 1 条但成功解析 0 条 拒绝" "$tmp/noline.md"

# 13) heading 数与成功解析数不一致 拒绝（声明 2 条，仅 1 条合法）
sed 's|### \[P0\] 问题 — `apps/api/x.go:10`|### [P0] 问题 — `apps/api/x.go:10`\n### [P1] 裸|; s|Findings（1 条）|Findings（2 条）|' "$tmp/ok-rc.md" > "$tmp/count.md"
expect_fail "heading 数与成功解析数不一致 拒绝" "$tmp/count.md"

# 14) 缺 Findings 块 拒绝
sed '/<summary>.*Findings/,/<\/details>/d' "$tmp/ok-rc.md" > "$tmp/nofindings.md"
expect_fail "缺 Findings 块 拒绝" "$tmp/nofindings.md"

# 15) 空验收矩阵（无数据行）拒绝
sed '/| 功能正确 | 通过 | 通过测试 |/d' "$tmp/ok-rc.md" > "$tmp/emptymatrix.md"
expect_fail "空验收矩阵 拒绝" "$tmp/emptymatrix.md"

# 16) 缺矩阵分隔行 拒绝
sed '/| --- | --- | --- |/d' "$tmp/ok-rc.md" > "$tmp/nosep.md"
expect_fail "缺矩阵分隔行 拒绝" "$tmp/nosep.md"

# 17) 矩阵结果值非法 拒绝
sed 's/| 功能正确 | 通过 |/| 功能正确 | 不确定 |/' "$tmp/ok-rc.md" > "$tmp/badres.md"
expect_fail "矩阵结果值非法 拒绝" "$tmp/badres.md"

# 18) 多个结论行 拒绝
sed 's|结论：REQUEST CHANGES|结论：APPROVE\n结论：REQUEST CHANGES|' "$tmp/ok-rc.md" > "$tmp/multiconcl.md"
expect_fail "多个结论行 拒绝" "$tmp/multiconcl.md"

# 19) 结论不是最后一个非空行 拒绝
printf '%s\n' '附加说明' >> "$tmp/ok-rc.md"
expect_fail "结论不是最后一个非空行 拒绝" "$tmp/ok-rc.md"

# --- F1：finding 路径/标题严格校验 ---
make_heading_review() { # make_heading_review <heading_line> <out_file>
  local heading=$1 out=$2
  cat > "$out" <<EOF
**❌ REQUEST CHANGES** — 1 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理

<details>
<summary>📋 Findings（1 条）</summary>

$heading
事实：xxx
触发：xxx
影响：xxx
依据：xxx
最小修复：xxx

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

置信度：高；仍未验证的风险：无
结论：REQUEST CHANGES
EOF
}

# 应通过：合法 repo 相对路径（根文件、嵌套、含空格、反引号包裹）
for case in "标题 — README.md:10" "标题 — AGENTS.md:5" "标题 — src/main.go:20" \
            "标题 — docs/frontend design/example.md:3" "标题 — \`src/main.go:20\`"; do
  make_heading_review "### [P0] $case" "$tmp/pass_${case//[^A-Za-z]/}.md"
  expect_pass "合法路径通过: $case" "$tmp/pass_${case//[^A-Za-z]/}.md"
done

# 应拒绝：空标题 / 非法路径 / 非法行号
reject_heading() { # reject_heading <描述> <heading_line>
  local desc=$1 heading=$2
  make_heading_review "$heading" "$tmp/rej.md"
  expect_fail "$desc" "$tmp/rej.md"
}
reject_heading "空标题 + src/x.go:10 拒绝" '### [P0]  — src/x.go:10'
reject_heading "/etc/passwd:1 绝对路径 拒绝" '### [P0] 标题 — /etc/passwd:1'
reject_heading "../outside/file:1 拒绝" '### [P0] 标题 — ../outside/file:1'
reject_heading "src/../../outside:1 拒绝" '### [P0] 标题 — src/../../outside:1'
reject_heading "C:\\Windows\\file:1 拒绝" '### [P0] 标题 — C:\Windows\file:1'
reject_heading "C:/Windows/file:1 拒绝" '### [P0] 标题 — C:/Windows/file:1'
reject_heading "UNC 路径 拒绝" '### [P0] 标题 — \\server\share\file:1'
reject_heading "空路径 :1 拒绝" '### [P0] 标题 — :1'
reject_heading ".:1 拒绝" '### [P0] 标题 — .:1'
reject_heading "..:1 拒绝" '### [P0] 标题 — ..:1'
reject_heading "行号 0 拒绝" '### [P0] 标题 — src/main.go:0'
reject_heading "负行号 拒绝" '### [P0] 标题 — src/main.go:-1'
reject_heading "非数字行号 拒绝" '### [P0] 标题 — src/main.go:abc'

# --- 结论语义校验（P1）：finding/矩阵结果与最终结论的语义关系 ---
make_semantic() { # make_semantic <conclusion> <badge> <finding_level|''> <matrix_result> <out_file>
  local concl=$1 badge=$2 lvl=$3 mres=$4 out=$5
  local finding="" count=0
  if [ -n "$lvl" ]; then
    count=1
    finding="### [$lvl] 问题 — \`src/x.go:10\`
事实：xxx
触发：xxx
影响：xxx
依据：xxx
最小修复：xxx"
  fi
  cat > "$out" <<EOF
$badge

<details>
<summary>📋 Findings（$count 条）</summary>

$finding

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | $mres | 测试 |

</details>

置信度：高；仍未验证的风险：无
结论：$concl
EOF
}

# APPROVE 不允许待处理 P0/P1/P2
make_semantic APPROVE '**✅ APPROVE** — 1 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理' P0 通过 "$tmp/s_p0.md"
expect_fail "P0 待处理 + APPROVE 拒绝" "$tmp/s_p0.md"
make_semantic APPROVE '**✅ APPROVE** — 0 个 P0 / 1 个 P1 / 0 个 P2 / 0 个 P3 待处理' P1 通过 "$tmp/s_p1.md"
expect_fail "P1 待处理 + APPROVE 拒绝" "$tmp/s_p1.md"
make_semantic APPROVE '**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 1 个 P2 / 0 个 P3 待处理' P2 通过 "$tmp/s_p2.md"
expect_fail "P2 待处理 + APPROVE 拒绝" "$tmp/s_p2.md"
# 仅 P3 + APPROVE 允许
make_semantic APPROVE '**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 1 个 P3 待处理' P3 通过 "$tmp/s_p3.md"
expect_pass "仅 P3 待处理 + APPROVE 允许" "$tmp/s_p3.md"
# 矩阵存在失败/证据不足 + APPROVE 拒绝
make_semantic APPROVE '**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）' '' 失败 "$tmp/s_mfail.md"
expect_fail "矩阵含'失败' + APPROVE 拒绝" "$tmp/s_mfail.md"
make_semantic APPROVE '**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）' '' 证据不足 "$tmp/s_minsuf.md"
expect_fail "矩阵含'证据不足' + APPROVE 拒绝" "$tmp/s_minsuf.md"
# P0/P1 + REQUEST CHANGES 允许
make_semantic 'REQUEST CHANGES' '**❌ REQUEST CHANGES** — 1 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理' P0 通过 "$tmp/s_rc.md"
expect_pass "P0 待处理 + REQUEST CHANGES 允许" "$tmp/s_rc.md"
# 证据不足 + ESCALATE 允许；确认问题 + required ESCALATE 时 ESCALATE 优先
make_semantic ESCALATE '**⚠️ ESCALATE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题；证据不足）' '' 通过 "$tmp/s_esc.md"
expect_pass "证据不足 + ESCALATE 允许" "$tmp/s_esc.md"
make_semantic ESCALATE '**⚠️ ESCALATE** — 0 个 P0 / 1 个 P1 / 0 个 P2 / 0 个 P3 待处理（证据不足）' P1 通过 "$tmp/s_esc_p1.md"
expect_pass "确认 P1 + required ESCALATE → ESCALATE 优先" --required ESCALATE "$tmp/s_esc_p1.md"
expect_fail "确认 P1 + required ESCALATE → APPROVE 仍拒绝" --required ESCALATE "$tmp/s_p1.md"

# --- 新发现一：重复结构块校验（恰好 1 Findings + 1 矩阵；顺序；无未知块） ---
cat > "$tmp/dupfind_p1.md" <<'EOF'
**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）

<details>
<summary>📋 Findings（0 条）</summary>

</details>

<details>
<summary>📋 Findings（1 条）</summary>

### [P1] 问题 — `src/x.go:10`
事实：xxx
触发：xxx
影响：xxx
依据：xxx
最小修复：xxx

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

置信度：高；仍未验证的风险：无
结论：APPROVE
EOF
expect_fail "两个 Findings 且第二个含 P1 + APPROVE 拒绝" "$tmp/dupfind_p1.md"

cat > "$tmp/dupfind_empty.md" <<'EOF'
**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）

<details>
<summary>📋 Findings（0 条）</summary>

</details>

<details>
<summary>📋 Findings（0 条）</summary>

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

置信度：高；仍未验证的风险：无
结论：APPROVE
EOF
expect_fail "两个 Findings 均为空 拒绝" "$tmp/dupfind_empty.md"

cat > "$tmp/dupmtrx_fail.md" <<'EOF'
**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）

<details>
<summary>📋 Findings（0 条）</summary>

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 失败 | 测试失败 |

</details>

置信度：高；仍未验证的风险：无
结论：APPROVE
EOF
expect_fail "两个矩阵且第二个含失败 + APPROVE 拒绝" "$tmp/dupmtrx_fail.md"

cat > "$tmp/dupmtrx_same.md" <<'EOF'
**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）

<details>
<summary>📋 Findings（0 条）</summary>

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

置信度：高；仍未验证的风险：无
结论：APPROVE
EOF
expect_fail "两个矩阵内容相同 拒绝" "$tmp/dupmtrx_same.md"

cat > "$tmp/mtrx_before.md" <<'EOF'
**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

<details>
<summary>📋 Findings（0 条）</summary>

</details>

置信度：高；仍未验证的风险：无
结论：APPROVE
EOF
expect_fail "矩阵出现在 Findings 之前 拒绝" "$tmp/mtrx_before.md"

cat > "$tmp/third_details.md" <<'EOF'
**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）

<details>
<summary>📋 Findings（0 条）</summary>

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

<details>
<summary>其他未知块</summary>
内容
</details>

置信度：高；仍未验证的风险：无
结论：APPROVE
EOF
expect_fail "出现第三个未知 <details> 块 拒绝" "$tmp/third_details.md"

# --- 新发现二：REQUEST CHANGES 反向语义校验 ---
make_semantic "REQUEST CHANGES" '**❌ REQUEST CHANGES** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）' '' 通过 "$tmp/rc_nores.md"
expect_fail "0 findings + 矩阵全通过 + REQUEST CHANGES 拒绝" "$tmp/rc_nores.md"
make_semantic "REQUEST CHANGES" '**❌ REQUEST CHANGES** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 1 个 P3 待处理' P3 通过 "$tmp/rc_p3.md"
expect_fail "仅 P3 + 矩阵全通过 + REQUEST CHANGES 拒绝" "$tmp/rc_p3.md"
make_semantic "REQUEST CHANGES" '**❌ REQUEST CHANGES** — 0 个 P0 / 1 个 P1 / 0 个 P2 / 0 个 P3 待处理' P1 通过 "$tmp/rc_p1.md"
expect_pass "P1 + REQUEST CHANGES 允许" "$tmp/rc_p1.md"
make_semantic "REQUEST CHANGES" '**❌ REQUEST CHANGES** — 0 个 P0 / 0 个 P1 / 1 个 P2 / 0 个 P3 待处理' P2 通过 "$tmp/rc_p2.md"
expect_pass "P2 + REQUEST CHANGES 允许" "$tmp/rc_p2.md"
make_semantic "REQUEST CHANGES" '**❌ REQUEST CHANGES** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）' '' 失败 "$tmp/rc_mfail.md"
expect_pass "0 findings + 矩阵含失败 + REQUEST CHANGES 允许" "$tmp/rc_mfail.md"

# --- 新发现三：details/summary 结构严格扫描（防 <details open>/<DETAILS>/嵌套等绕过） ---
# 基础模板：两个正常块之后追加一个变体块；旧实现因全文精确计数 2 而错误接受
make_structure_case() { # make_structure_case <open_tag> <out_file>
  local open_tag=$1 out=$2
  cat > "$out" <<EOF
**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）

<details>
<summary>📋 Findings（0 条）</summary>

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

$open_tag
<summary>附加 Findings</summary>

### [P1] 未被解析的问题 — src/x.go:10
事实：xxx
触发：xxx
影响：xxx
依据：xxx
最小修复：xxx

</details>

置信度：高；仍未验证的风险：无
结论：APPROVE
EOF
}

# 已知绕过样例：details-open-extra-P1（<details open> 附加 P1 块）
make_structure_case '<details open>' "$tmp/details-open-extra-P1+APPROVE.md"
expect_fail "<details open> 附加 P1 块 拒绝" "$tmp/details-open-extra-P1+APPROVE.md"
# 已知绕过样例：<details >（标签内多余空格）
make_structure_case '<details >' "$tmp/details-space.md"
expect_fail "<details > 拒绝" "$tmp/details-space.md"
make_structure_case '<details class="x">' "$tmp/details-attr.md"
expect_fail "details 标签带其他属性 拒绝" "$tmp/details-attr.md"
make_structure_case '<DETAILS>' "$tmp/details-upper.md"
expect_fail "<DETAILS> 大写变体 拒绝" "$tmp/details-upper.md"

# 已知绕过样例：nested-details（矩阵 <details> 嵌套进 Findings 块）
cat > "$tmp/nested-details+APPROVE.md" <<'EOF'
**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）

<details>
<summary>📋 Findings（0 条）</summary>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

</details>

置信度：高；仍未验证的风险：无
结论：APPROVE
EOF
expect_fail "Findings 与矩阵嵌套 拒绝" "$tmp/nested-details+APPROVE.md"

# 缺少 closing（第二个块无 </details>）
cat > "$tmp/missing-close.md" <<'EOF'
**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）

<details>
<summary>📋 Findings（0 条）</summary>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

置信度：高；仍未验证的风险：无
结论：APPROVE
EOF
expect_fail "缺少 closing 拒绝" "$tmp/missing-close.md"

# 多余 closing（两个块之后追加 </details>）
cat > "$tmp/extra-close.md" <<'EOF'
**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）

<details>
<summary>📋 Findings（0 条）</summary>

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

</details>

置信度：高；仍未验证的风险：无
结论：APPROVE
EOF
expect_fail "多余 closing 拒绝" "$tmp/extra-close.md"

# closing 带空格（替换全部 </details>）
sed 's|</details>|</details >|' "$tmp/ok-approve.md" > "$tmp/close-space.md"
expect_fail "closing 带空格或属性 拒绝" "$tmp/close-space.md"

# details 标签与其他文字同一行
cat > "$tmp/inline-tag.md" <<'EOF'
**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）

<details>
<summary>📋 Findings（0 条）</summary>

</details>

附注 <details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

置信度：高；仍未验证的风险：无
结论：APPROVE
EOF
expect_fail "details 标签与其他文字同行 拒绝" "$tmp/inline-tag.md"

# summary 不是块内第一个非空行
cat > "$tmp/summary-not-first.md" <<'EOF'
**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）

<details>
一些内容
<summary>📋 Findings（0 条）</summary>

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

置信度：高；仍未验证的风险：无
结论：APPROVE
EOF
expect_fail "summary 不是块内第一个非空行 拒绝" "$tmp/summary-not-first.md"

# 一个块中出现额外 summary
cat > "$tmp/extra-summary.md" <<'EOF'
**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）

<details>
<summary>📋 Findings（0 条）</summary>

<summary>额外内容</summary>

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

置信度：高；仍未验证的风险：无
结论：APPROVE
EOF
expect_fail "块内出现额外 summary 拒绝" "$tmp/extra-summary.md"

# 正常两个顶层、配对、顺序正确的结构：允许
expect_pass "两个顶层 details 配对且顺序正确 允许" "$tmp/ok-approve.md"

# --- 新发现四：徽章 P0-P3 计数严格解析（防重复覆盖） ---
make_badge_review() { # make_badge_review <badge> <out_file>
  local badge=$1 out=$2
  cat > "$out" <<EOF
$badge

<details>
<summary>📋 Findings（0 条）</summary>

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

置信度：高；仍未验证的风险：无
结论：APPROVE
EOF
}

# 已知绕过样例：duplicate-P1-badge-count（重复 P1，最后一个覆盖前一个）
make_badge_review '**✅ APPROVE** — 0 个 P0 / 1 个 P1 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）' "$tmp/duplicate-P1-badge-count+APPROVE.md"
expect_fail "重复 P1 徽章计数 拒绝" "$tmp/duplicate-P1-badge-count+APPROVE.md"
make_badge_review '**✅ APPROVE** — 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）' "$tmp/badge-missing-p0.md"
expect_fail "缺少 P0 计数 拒绝" "$tmp/badge-missing-p0.md"
make_badge_review '**✅ APPROVE** — 0 个 P0 / 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）' "$tmp/badge-dup-same.md"
expect_fail "同级重复且数值相同 拒绝" "$tmp/badge-dup-same.md"
make_badge_review '**✅ APPROVE** — 0 个 P1 / 0 个 P0 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）' "$tmp/badge-order.md"
expect_fail "P0-P3 顺序错误 拒绝" "$tmp/badge-order.md"
make_badge_review '**✅ APPROVE** — 本轮未发现由当前变更引入的可操作问题' "$tmp/badge-no-count.md"
expect_fail "仅'未发现'但无四项计数 拒绝" "$tmp/badge-no-count.md"
# 精确 P0/P1/P2/P3 四项且与 Findings 一致：允许
make_semantic APPROVE '**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 1 个 P3 待处理' P3 通过 "$tmp/badge-exact-ok.md"
expect_pass "精确四项徽章计数且与 Findings 一致 允许" "$tmp/badge-exact-ok.md"

# --- 新发现五：完整文档布局（块外非空内容一律拒绝） ---
# 已知绕过样例：outside-findings-P1（矩阵后块外追加完整 P1）
cat > "$tmp/outside-findings-P1+APPROVE.md" <<'EOF'
**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）

<details>
<summary>📋 Findings（0 条）</summary>

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

### [P1] 块外未解析问题 — src/x.go:10
事实：错误
触发：执行
影响：失败
依据：复现
最小修复：修复

置信度：高；仍未验证的风险：无
结论：APPROVE
EOF
expect_fail "矩阵后块外 P1 + APPROVE 拒绝" "$tmp/outside-findings-P1+APPROVE.md"

# 徽章与 Findings 块之间追加完整 P1
cat > "$tmp/outside-before-details.md" <<'EOF'
**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）

### [P1] 徽章与块间 P1 — src/x.go:10
事实：错误
触发：执行
影响：失败
依据：复现
最小修复：修复

<details>
<summary>📋 Findings（0 条）</summary>

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

置信度：高；仍未验证的风险：无
结论：APPROVE
EOF
expect_fail "徽章与 Findings 之间追加 P1 拒绝" "$tmp/outside-before-details.md"

# 两个 details 块之间追加普通文字
cat > "$tmp/outside-between.md" <<'EOF'
**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）

<details>
<summary>📋 Findings（0 条）</summary>

</details>

块间说明文字

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

置信度：高；仍未验证的风险：无
结论：APPROVE
EOF
expect_fail "两个 details 块之间追加文字 拒绝" "$tmp/outside-between.md"

# 矩阵后（置信度前）追加普通 Markdown 标题
sed '/^置信度：/i ## 块外标题' "$tmp/ok-approve.md" > "$tmp/outside-heading.md"
expect_fail "矩阵后块外 Markdown 标题 拒绝" "$tmp/outside-heading.md"

# 合法布局：块外仅有徽章、置信度、结论三行
expect_pass "块外仅徽章/置信度/结论的合法布局 允许" "$tmp/esc.md"

# --- 新发现六：徽章完整行语法（fullmatch，拒绝 P4/尾文/前缀） ---
# 已知绕过样例：extra-P4-badge-count（四项计数后追加 P4）
make_badge_review '**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理 / 99 个 P4（本轮未发现由当前变更引入的可操作问题）' "$tmp/extra-P4-badge-count+APPROVE.md"
expect_fail "四项计数后追加 P4 拒绝" "$tmp/extra-P4-badge-count+APPROVE.md"
make_badge_review '**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理 / 1 个 P10（本轮未发现由当前变更引入的可操作问题）' "$tmp/badge-p10.md"
expect_fail "追加 P10 未知级别 拒绝" "$tmp/badge-p10.md"
make_badge_review '**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理 附加尾文（本轮未发现由当前变更引入的可操作问题）' "$tmp/badge-trailing.md"
expect_fail "四项计数后追加非括号尾文 拒绝" "$tmp/badge-trailing.md"
make_badge_review '**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）后文' "$tmp/badge-after-note.md"
expect_fail "全角说明括号结束后追加文字 拒绝" "$tmp/badge-after-note.md"

# 徽章前有文字（第一行非徽章）
cat > "$tmp/badge-prefix.md" <<'EOF'
前置标题
**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）

<details>
<summary>📋 Findings（0 条）</summary>

</details>

<details>
<summary>✅ 验收标准矩阵</summary>

| 验收项 | 结果 | 证据 |
| --- | --- | --- |
| 功能正确 | 通过 | 通过测试 |

</details>

置信度：高；仍未验证的风险：无
结论：APPROVE
EOF
expect_fail "徽章前有文字 拒绝" "$tmp/badge-prefix.md"

# 严格四项计数、无说明：允许（非零 finding，避免零 finding 说明约束干扰）
make_semantic 'REQUEST CHANGES' '**❌ REQUEST CHANGES** — 1 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理' P0 通过 "$tmp/badge-plain-ok.md"
expect_pass "严格四项计数无说明 允许" "$tmp/badge-plain-ok.md"
# 严格四项计数、合法非空说明：允许
make_semantic APPROVE '**✅ APPROVE** — 0 个 P0 / 0 个 P1 / 0 个 P2 / 0 个 P3 待处理（本轮未发现由当前变更引入的可操作问题）' '' 通过 "$tmp/badge-note-ok.md"
expect_pass "严格四项计数合法说明 允许" "$tmp/badge-note-ok.md"

# --- 新发现七：置信度完整行语法（唯一、倒数第二非空行、级别高/中/低、风险非空） ---
# 已知绕过样例：missing-confidence-line-spoofed-in-matrix（删置信度行，关键词放入矩阵）
sed '/^置信度：/d; s/| 功能正确 | 通过 | 通过测试 |/| 功能正确 | 通过 | 置信度 仍未验证的风险 |/' "$tmp/ok-approve.md" > "$tmp/missing-confidence-line-spoofed-in-matrix.md"
expect_fail "删置信度行关键词放矩阵 拒绝" "$tmp/missing-confidence-line-spoofed-in-matrix.md"
# 删置信度行，关键词放入 finding
make_semantic 'REQUEST CHANGES' '**❌ REQUEST CHANGES** — 0 个 P0 / 1 个 P1 / 0 个 P2 / 0 个 P3 待处理' P1 通过 "$tmp/conf-in-finding.md"
sed '/^置信度：/d; s/事实：xxx/事实：置信度仍未验证的风险/' "$tmp/conf-in-finding.md" > "$tmp/conf-in-finding2.md"
expect_fail "删置信度行关键词放 finding 拒绝" "$tmp/conf-in-finding2.md"
# 置信度不是倒数第二个非空行（其后追加说明）
sed '/^置信度：/a 附加说明' "$tmp/ok-approve.md" > "$tmp/conf-not-second-last.md"
expect_fail "置信度行不是倒数第二个非空行 拒绝" "$tmp/conf-not-second-last.md"
# 出现两条置信度行
sed '/^置信度：/a 置信度：高；仍未验证的风险：重复' "$tmp/ok-approve.md" > "$tmp/conf-two.md"
expect_fail "出现两条置信度行 拒绝" "$tmp/conf-two.md"
# 置信度级别非法
sed 's/^置信度：高/置信度：极高/' "$tmp/ok-approve.md" > "$tmp/conf-bad-level.md"
expect_fail "置信度级别非法 拒绝" "$tmp/conf-bad-level.md"
# 风险内容为空
sed 's/^置信度：高；仍未验证的风险：无/置信度：高；仍未验证的风险：/' "$tmp/ok-approve.md" > "$tmp/conf-empty-risk.md"
expect_fail "置信度风险内容为空 拒绝" "$tmp/conf-empty-risk.md"
# 正确的高/中/低三种格式：允许
for level in 高 中 低; do
  sed "s/^置信度：高/置信度：$level/" "$tmp/ok-approve.md" > "$tmp/conf-${level}.md"
  expect_pass "置信度级别 ${level} 允许" "$tmp/conf-${level}.md"
done

# 最终结论后追加内容（放最后：会污染 ok-approve.md，后续用例不再依赖它）
printf '%s\n' '额外内容' >> "$tmp/ok-approve.md"
expect_fail "最终结论后追加内容 拒绝" "$tmp/ok-approve.md"

if (( failures > 0 )); then
  echo "review-output 测试失败：$failures 项"
  exit 1
fi
echo "review-output 测试通过。"

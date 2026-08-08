#!/usr/bin/env python3
"""校验 opencode 审查输出的确定性格式与 required conclusion（P2，finding 二/七）。

用法:
  validate-review-output.py <model-output.md>
  validate-review-output.py --required APPROVE|REQUEST CHANGES|ESCALATE <model-output.md>

退出码 0 = 合格；非 0 = 不合格，并在 stderr 打印原因。
--required 由受信 workflow 步骤根据可信上下文计算（CI/Task 证据不足时必须为 ESCALATE），
本脚本据此拒绝与 required 不一致的徽章与最终结论，不依赖 prompt。
只做静态文本解析，不读取任何真实环境变量或凭据。
"""
import argparse
import re
import sys

FINDING_FIELDS = ("事实", "触发", "影响", "依据", "最小修复")
CONCLUSIONS = ("APPROVE", "REQUEST CHANGES", "ESCALATE")
FINDINGS_SUMMARY_RE = re.compile(r"^<summary>📋 Findings（(\d+)\s*条）</summary>$")
MATRIX_SUMMARY = "<summary>✅ 验收标准矩阵</summary>"
# 徽章整行语法（新发现六）：严格 徽章 — N 个 P0 / N 个 P1 / N 个 P2 / N 个 P3 待处理
# + 可选全角括号说明；不允许 P4/P10/未知级别/任意前缀后缀
BADGE_RE = re.compile(
    r"^\*\*(?P<concl>✅ APPROVE|❌ REQUEST CHANGES|⚠️ ESCALATE)\*\*"
    r" — (?P<p0>\d+) 个 P0 / (?P<p1>\d+) 个 P1 / (?P<p2>\d+) 个 P2 / (?P<p3>\d+) 个 P3 待处理"
    r"(?:（(?P<note>[^）]+)）)?$"
)
# 置信度完整行语法（新发现七）：级别 高/中/低 + 非空风险内容
CONFIDENCE_RE = re.compile(r"^置信度：(高|中|低)；仍未验证的风险：(.+)$")


def _scan_structure(text: str):
    """严格扫描审查输出的 <details> 结构（新发现三：防属性/大小写/嵌套绕过）。

    规则：
    - 标签必须独占一行，strip 后严格等于 <details> 或 </details>（大小写敏感、
      无属性/无多余空格）；<details open>、<details >、<details class="x">、
      <DETAILS>、</details > 及标签与其他文字同行一律拒绝；
    - opening 必须恰好 2 个、closing 必须恰好 2 个；严格配对
      （open→close→open→close），不允许嵌套、多余/缺失 closing；
    - 两个顶层块依次为 Findings、验收标准矩阵；每个块第一个非空内容行必须是
      对应的精确 <summary>（Findings（N 条）/ ✅ 验收标准矩阵）；
    - <summary> 只允许作为对应顶层块的首个非空内容行；未知/额外/块外 summary
      一律拒绝（fail closed）。

    返回 (findings_declared, findings_content, matrix_content, opens, closes, errors)；
    对应块结构不合格时字段为 None/空列表（错误均已记录到 errors）。
    opens/closes 为两个顶层块的 opening/closing 行索引列表，供上层做完整布局校验。
    findings_content/matrix_content 为 summary 行之后、块内非空内容行的拼接，
    供 finding 条目与矩阵语义解析使用，避免跨块全文 re.search。
    """
    errors = []
    lines = text.splitlines()
    tags: list[tuple[str, int]] = []  # ('open'|'close', 行索引)
    for idx, line in enumerate(lines):
        stripped = line.strip()
        if not stripped:
            continue
        lower = stripped.lower()
        det = re.search(r"</?details", lower)
        if det is not None:
            if det.start() != 0:
                errors.append(f"第 {idx + 1} 行：details 标签必须独占一行（不得与其他文字同行）")
                continue
            if lower.startswith("</details"):
                if stripped != "</details>":
                    errors.append(f"第 {idx + 1} 行：closing 标签必须严格为 </details>（拒绝大小写/属性/空格变体）：{stripped!r}")
                    continue
                tags.append(("close", idx))
            else:
                if stripped != "<details>":
                    errors.append(f"第 {idx + 1} 行：opening 标签必须严格为 <details>（拒绝大小写/属性/空格变体）：{stripped!r}")
                    continue
                tags.append(("open", idx))
    opens = [i for k, i in tags if k == "open"]
    closes = [i for k, i in tags if k == "close"]
    if len(opens) != 2 or len(closes) != 2:
        errors.append(f"必须恰好 2 个 <details> 与 2 个 </details>（实际 {len(opens)} open / {len(closes)} close）")
        return None, None, None, [], [], errors
    if not (opens[0] < closes[0] < opens[1] < closes[1]):
        errors.append("details 块必须严格配对且不允许嵌套（顺序须为 open→close→open→close）")
        return None, None, None, [], [], errors

    def first_nonempty(a: int, b: int):
        for i in range(a + 1, b):
            if lines[i].strip():
                return i
        return None

    def content_between(a: int, b: int) -> list[str]:
        return [lines[i].strip() for i in range(a + 1, b) if lines[i].strip()]

    s1 = first_nonempty(opens[0], closes[0])
    s2 = first_nonempty(opens[1], closes[1])
    b1 = content_between(opens[0], closes[0])
    b2 = content_between(opens[1], closes[1])

    findings_declared = None
    findings_content = None
    matrix_content = None
    if s1 is None or FINDINGS_SUMMARY_RE.match(b1[0]) is None:
        errors.append("Findings 块第一个非空内容行必须严格为 <summary>📋 Findings（N 条）</summary>")
    else:
        findings_declared = int(FINDINGS_SUMMARY_RE.match(b1[0]).group(1))
        findings_content = "\n".join(b1[1:])
    if s2 is None or b2[0] != MATRIX_SUMMARY:
        errors.append("矩阵块第一个非空内容行必须严格为 <summary>✅ 验收标准矩阵</summary>")
    else:
        matrix_content = "\n".join(b2[1:])

    # summary 只允许作为对应顶层块的首个非空内容行；其余位置一律拒绝
    allowed = {s1, s2}
    for idx, line in enumerate(lines):
        if re.search(r"<summary", line.lower()) and idx not in allowed:
            errors.append(f"第 {idx + 1} 行：<summary> 只允许作为对应顶层 details 块的首个非空内容行（未知/额外 summary 一律拒绝）")

    return findings_declared, findings_content, matrix_content, opens, closes, errors


def _valid_repo_path(path: str) -> bool:
    """确定性语法校验 repo 相对路径（不访问文件系统）。

    合法：README.md / go.mod / src/main.go / docs/frontend design/example.md
    拒绝：空、绝对路径（/）、UNC（\\\\）、Windows 盘符（C:）、反斜杠、
         按 / 分割出现空分量/. /.. 的路径。
    """
    if not path:
        return False
    if path.startswith("/"):
        return False
    if path.startswith("\\\\"):
        return False
    if re.match(r"^[A-Za-z]:", path):
        return False
    if "\\" in path:
        return False
    parts = path.split("/")
    if any(p in ("", ".", "..") for p in parts):
        return False
    return True


def parse(text: str, required: str | None = None):
    lines = text.splitlines()
    errors: list[str] = []
    badge_conclusion: str | None = None
    badge_counts: dict[int, int] = {}
    matrix_results: list[str] = []

    if not lines:
        return ["输出为空"]

    # 1. 第一行必须是完整徽章行（fullmatch：严格四项计数 + 可选全角括号说明）
    first = lines[0].strip()
    m = BADGE_RE.fullmatch(first)
    badge_note: str | None = None
    if not m:
        errors.append("第一行必须是完整徽章行（**✅ APPROVE/❌ REQUEST CHANGES/⚠️ ESCALATE** — N 个 P0 / N 个 P1 / N 个 P2 / N 个 P3 待处理，可选全角括号说明；不允许 P4/未知级别/任意前缀后缀）")
    else:
        badge_conclusion = {"✅ APPROVE": "APPROVE", "❌ REQUEST CHANGES": "REQUEST CHANGES", "⚠️ ESCALATE": "ESCALATE"}[m.group("concl")]
        badge_counts = {0: int(m.group("p0")), 1: int(m.group("p1")), 2: int(m.group("p2")), 3: int(m.group("p3"))}
        badge_note = m.group("note")
        if badge_note is not None and re.search(r"P\s*\d", badge_note):
            errors.append("徽章说明中不允许出现类似 P0-P9 的级别计数格式（防止绕过）")

    # 2. 结构块校验（新发现三）：严格状态机扫描，恰好 2 个顶层 <details>
    # （Findings → 验收标准矩阵），不允许属性/大小写变体/嵌套/多余 closing
    findings_declared, findings_content, matrix_content, opens, closes, struct_errors = _scan_structure(text)
    errors.extend(struct_errors)
    if findings_content is not None:
        declared = findings_declared
        block = findings_content
    else:
        declared, block = 0, ""

    # 2a. 切分 finding 条目并严格解析 heading：[P0-3] + 非空标题 + repo相对路径:行号
    parsed = []
    heading_re = re.compile(r"^###\s+\[P([0-3])\]\s+.*$", re.M)
    heads = list(heading_re.finditer(block))
    for idx, h in enumerate(heads):
        body = block[h.end(): heads[idx + 1].start() if idx + 1 < len(heads) else len(block)]
        # 剥离 markdown 反引号（路径可能被 `` 包裹）
        hclean = block[h.start():h.end()].replace("`", "")
        heading_txt = block[h.start():h.end()].strip()
        # 分步解析：先 [P级别]，再从末尾解析 路径:行号，剩余为标题
        m = re.match(r"^###\s+\[P([0-3])\]\s+(.*)$", hclean)
        if not m:
            errors.append(f"finding heading 必须为 [P0-3] + 非空标题 + repo相对路径:行号：{heading_txt[:60]}")
            continue
        lvl = m.group(1)
        rest = m.group(2).strip()
        lm = re.search(r":(\d+)\s*$", rest)
        if not lm:
            errors.append(f"finding heading 末尾必须为 ':正整数行号'：{heading_txt[:60]}")
            continue
        line = int(lm.group(1))
        before = rest[:lm.start()].strip()
        if "—" not in before:
            errors.append(f"finding heading 必须含 '—' 分隔（标题 — 路径:行号）：{heading_txt[:60]}")
            continue
        title, path = (p.strip() for p in before.rsplit("—", 1))
        if not title:
            errors.append(f"finding 标题必须真正非空（仅空格不算）：{heading_txt[:60]}")
            continue
        if not _valid_repo_path(path):
            errors.append(f"finding 路径必须是合法 repo 相对路径（拒绝绝对/UNC/盘符/.././/空分量）：{heading_txt[:60]}")
            continue
        if line <= 0:
            errors.append(f"finding 行号必须为正整数：{heading_txt[:60]}")
            continue
        for field in FINDING_FIELDS:
            # 逐行检查：字段行存在且冒号后有非空白内容（不允许空字段跨行吞并下一行）
            field_ok = False
            for fline in body.splitlines():
                if fline.strip().startswith(f"{field}："):
                    field_ok = bool(fline.split(f"{field}：", 1)[1].strip())
                    break
            if not field_ok:
                errors.append(f"finding [P{lvl}] {title[:30]} 缺少或空字段：{field}")
        parsed.append({"lvl": int(lvl), "title": title, "path": path, "line": line})

    # 2b. 声明 == heading 数 == 成功解析数
    if declared != len(heads):
        errors.append(f"Findings 声明 {declared} 条，heading 实际 {len(heads)} 条")
    if declared != len(parsed):
        errors.append(f"Findings 声明 {declared} 条，成功解析 {len(parsed)} 条")

    # 2c. 零 finding 时徽章说明必须含声明短语（全文其他区域出现该短语不能替代）
    if declared == 0 and (badge_note is None or "本轮未发现由当前变更引入的可操作问题" not in badge_note):
        errors.append("零 finding 时徽章说明必须包含'本轮未发现由当前变更引入的可操作问题'")

    # 2d. 徽章计数与实际 finding 一致
    if badge_counts:
        actual_level = {}
        for p in parsed:
            actual_level[p["lvl"]] = actual_level.get(p["lvl"], 0) + 1
        mismatch = [l for l in range(4) if badge_counts.get(l, 0) != actual_level.get(l, 0)]
        if mismatch:
            errors.append(f"徽章 P0-P3 计数 {badge_counts} 与实际 findings {actual_level} 在级别 {mismatch} 不一致")

    # 2e. 实际待处理 P0-P3 计数（供结论语义校验）
    pending: dict[int, int] = {}
    for p in parsed:
        pending[p["lvl"]] = pending.get(p["lvl"], 0) + 1

    # 2f. 完整文档布局（新发现五）：块外非空行只允许徽章行、置信度行、结论行。
    # 徽章必须是第一行；置信度必须是倒数第二个非空行；结论必须是最后一个非空行。
    # 块外不允许 finding/Markdown 标题/表格/说明/HTML 等任何其他非空内容。
    nonempty = [i for i, l in enumerate(lines) if l.strip()]
    if opens and closes:
        block_rows = set(range(opens[0], closes[0] + 1)) | set(range(opens[1], closes[1] + 1))
        outside = [i for i in nonempty if i not in block_rows]
        if len(outside) != 3:
            errors.append("块外非空行必须恰好为 徽章行、置信度行、结论行（两个 details 块之外不允许任何其他非空内容）")
        else:
            badge_row, conf_row, concl_row = outside
            if badge_row != 0:
                errors.append("第一行必须是徽章行（之前不允许 BOM、标题或其他非空内容）")
            if conf_row != nonempty[-2]:
                errors.append("置信度行必须是倒数第二个非空行")
            if concl_row != nonempty[-1]:
                errors.append("结论行必须是最后一个非空行")

    # 3. 验收标准矩阵（必需：表头 + 分隔行 + ≥1 数据行，三列非空，结果 ∈ 通过/失败/证据不足）
    if matrix_content is not None:
        mlines = [l.strip() for l in matrix_content.splitlines() if l.strip().startswith("|")]
        if not mlines or "| 验收项 | 结果 | 证据 |" not in mlines[0]:
            errors.append("验收矩阵缺少表头 | 验收项 | 结果 | 证据 |")
        elif len(mlines) < 2 or not re.search(r"\| *-+ *\| *-+ *\| *-+ *\|", mlines[1]):
            errors.append("验收矩阵缺少分隔行 | --- | --- | --- |")
        else:
            data_rows = mlines[2:]
            matrix_results: list[str] = []
            if not data_rows:
                errors.append("验收矩阵为空（无数据行）")
            for row in data_rows:
                cells = [c.strip() for c in row.strip().strip("|").split("|")]
                if len(cells) != 3 or not all(cells):
                    errors.append(f"验收矩阵数据行必须含三个非空列：{row[:60]}")
                elif cells[1] not in ("通过", "失败", "证据不足"):
                    errors.append(f"验收矩阵结果列非法（须通过/失败/证据不足）：{row[:60]}")
                else:
                    matrix_results.append(cells[1])

    # 4. 置信度行（新发现七）：全文唯一、完整行语法、倒数第二个非空行。
    # 级别限 高/中/低；风险内容 strip 后非空；矩阵/finding 等区域的关键词不能替代。
    conf_rows = [i for i, l in enumerate(lines) if l.strip().startswith(("置信度：", "置信度:"))]
    if len(conf_rows) != 1:
        errors.append("全文必须且只能存在一条独立的'置信度：'行（矩阵/finding 中的关键词不能替代）")
    else:
        cidx = conf_rows[0]
        cm = CONFIDENCE_RE.match(lines[cidx].strip())
        if not cm:
            errors.append("置信度行必须完整匹配：置信度：高/中/低；仍未验证的风险：<非空内容>")
        elif not cm.group(2).strip():
            errors.append("置信度行的'仍未验证的风险'内容必须非空")
        if cidx != nonempty[-2]:
            errors.append("置信度行必须是倒数第二个非空行")

    # 5. 结论：恰好一个独立行，且为最后一个非空行，与徽章/required 一致
    concl_lines = [i for i, l in enumerate(lines)
                   if re.match(r"^结论[：:]\s*(APPROVE|REQUEST CHANGES|ESCALATE)\s*$", l.strip())]
    if len(concl_lines) != 1:
        errors.append("必须恰好有一个独立的'结论：'行")
    else:
        ci = concl_lines[0]
        if not nonempty or ci != nonempty[-1]:
            errors.append("'结论：'行必须是最后一个非空行")
        else:
            final_concl = re.match(r"^结论[：:]\s*(APPROVE|REQUEST CHANGES|ESCALATE)\s*$", lines[ci].strip()).group(1)
            if badge_conclusion and badge_conclusion != final_concl:
                errors.append(f"徽章结论 {badge_conclusion} 与最终结论 {final_concl} 不一致")
            if required is not None:
                if required not in CONCLUSIONS:
                    errors.append(f"required conclusion 非法：{required}")
                if final_concl != required:
                    errors.append(f"required conclusion 为 {required}，最终结论却是 {final_concl}（证据不足时必须 ESCALATE）")
                if badge_conclusion and badge_conclusion != required:
                    errors.append(f"required conclusion 为 {required}，徽章结论却是 {badge_conclusion}")
            # 结论语义校验：基于解析出的 finding 计数与矩阵结果，非全文关键词
            if final_concl == "APPROVE":
                if pending.get(0, 0) or pending.get(1, 0) or pending.get(2, 0):
                    errors.append("APPROVE 不允许存在待处理 P0/P1/P2（fail-closed）")
                if "失败" in matrix_results:
                    errors.append("APPROVE 不允许验收矩阵存在'失败'")
                if "证据不足" in matrix_results:
                    errors.append("APPROVE 不允许验收矩阵存在'证据不足'")
            if final_concl == "REQUEST CHANGES":
                # 反向语义（新发现二）：RC 必须具有阻断依据（P0/P1/P2 或矩阵'失败'）
                has_blocking = bool(pending.get(0, 0) or pending.get(1, 0) or pending.get(2, 0)
                                    or "失败" in matrix_results)
                if not has_blocking:
                    errors.append("REQUEST CHANGES 缺少阻断依据（需待处理 P0/P1/P2 或验收矩阵存在'失败'）")

    return errors


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--required", choices=CONCLUSIONS, default=None,
                        help="受信步骤计算出的 required conclusion（CI/Task 证据不足时必须为 ESCALATE）")
    parser.add_argument("file", help="模型输出 markdown 文件")
    args = parser.parse_args()
    try:
        with open(args.file, encoding="utf-8") as fh:
            text = fh.read()
    except OSError as e:
        print(f"无法读取输出文件: {e}", file=sys.stderr)
        return 1
    errors = parse(text, required=args.required)
    if errors:
        for e in errors:
            print(f"格式不合格: {e}", file=sys.stderr)
        return 1
    print("OK: 审查输出格式合格")
    return 0


if __name__ == "__main__":
    sys.exit(main())

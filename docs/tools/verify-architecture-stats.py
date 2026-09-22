#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""文档级统计断言：校验架构 v1.1.2 的 FR 计数与 §2.8 统计表。

断言（对应 KM-17 复核 P-3 / P-8）：
  A1 §2.8 每行 15 列合计 == 该行所印合计（v1.0 = 70、v1.1 新增 = +25、v1.1 = 95）
  A2 §2.8 逐列满足 "v1.0 条数 + v1.1 新增 == v1.1 条数"
  A3 §2.4 FR 表机械统计 == 95 条（P0 94 / P1 1），且逐组与 §2.8 第 3 行一致
  A4 §2.8 / §18 / 附录 A / 附录 B 声明的功能需求条数均为 95
  A5 全篇不存在作为"本版数字"的 96 / 70 / +26（历史引文与更正说明除外，白名单见下）

用法：python3 docs/tools/verify-architecture-stats.py docs/agent-fleet-architecture-v1.1.2.md
退出码 0 = 全部断言通过；1 = 有断言失败。
"""
import re
import sys

FAIL = []


def check(cond, msg):
    print(("PASS  " if cond else "FAIL  ") + msg)
    if not cond:
        FAIL.append(msg)


def parse_table(doc, header_prefix):
    """返回以 header_prefix 开头的表头之后、直到空行/非表格行之前的表格行（已按 | 切分）。"""
    lines = doc.split("\n")
    out = []
    started = False
    for ln in lines:
        if not started:
            if ln.strip().startswith(header_prefix):
                started = True
            continue
        if not ln.strip().startswith("|"):
            break
        cells = [c.strip() for c in ln.strip().strip("|").split("|")]
        out.append(cells)
    return out


def to_int(s):
    s = s.replace("**", "").replace("+", "").strip()
    if s in ("0", ""):
        return 0
    return int(s)


def main(path):
    doc = open(path, encoding="utf-8").read()

    # ---------- A3 §2.4 FR table ----------
    groups = {}
    prios = {}
    seen = set()
    for ln in doc.split("\n"):
        if not ln.strip().startswith("|"):
            continue
        cells = [c.strip() for c in ln.strip().strip("|").split("|")]
        if len(cells) < 4:
            continue
        m = re.fullmatch(r"\*{0,2}(FR-(\d+)\.(\d+))(【新增】)?\*{0,2}", cells[0])
        if not m:
            continue
        fid, grp = m.group(1), int(m.group(2))
        if fid in seen:
            FAIL.append("A3 FR 编号重复：%s" % fid)
        seen.add(fid)
        groups[grp] = groups.get(grp, 0) + 1
        prio = cells[2].replace("**", "").strip()
        prios[prio] = prios.get(prio, 0) + 1
    total = len(seen)
    check(total == 95, "A3 §2.4 机械统计合计 == 95（实测 %d）" % total)
    check(prios.get("P0", 0) == 94 and prios.get("P1", 0) == 1,
          "A3 优先级 P0==94 / P1==1（实测 P0=%d P1=%d）" % (prios.get("P0", 0), prios.get("P1", 0)))
    per_group = [groups.get(g, 0) for g in range(1, 16)]
    check(per_group == [10, 6, 4, 3, 2, 8, 5, 7, 10, 7, 6, 8, 9, 5, 5],
          "A3 逐组计数 == [10,6,4,3,2,8,5,7,10,7,6,8,9,5,5]（实测 %s）" % per_group)

    # ---------- A1/A2 §2.8 table ----------
    rows = {}
    for cells in parse_table(doc, "| 组 | FR-1 |"):
        if len(cells) != 17 or cells[1] == "---":
            continue
        name = cells[0].replace("**", "").strip()
        nums = [to_int(c) for c in cells[1:16]]
        printed = to_int(cells[16])
        rows[name] = (nums, printed)
    for name, (nums, printed) in rows.items():
        check(sum(nums) == printed,
              "A1 §2.8 行「%s」15 列合计 == 所印合计（列和 %d vs 所印 %d）" % (name, sum(nums), printed))
    v10 = rows.get("v1.0 条数")
    added = rows.get("v1.1 新增")
    v11 = rows.get("v1.1 条数")
    check(v10 is not None and added is not None and v11 is not None, "A2 §2.8 三行齐备")
    if v10 and added and v11:
        check(v10[0] == [8, 6, 4, 3, 2, 6, 4, 5, 6, 5, 3, 6, 5, 4, 3], "A2 v1.0 行与 v1.0 基线一致")
        check(added[0] == [2, 0, 0, 0, 0, 2, 1, 2, 4, 2, 3, 2, 4, 1, 2], "A2 v1.1 新增行与逐组差值一致")
        col_ok = all(a + b == c for a, b, c in zip(v10[0], added[0], v11[0]))
        check(col_ok, "A2 逐列满足 v1.0 + v1.1 新增 == v1.1 条数")
        check(v11[0] == per_group, "A2 §2.8 v1.1 行 == §2.4 机械统计逐组计数")

    # ---------- A4 declarations ----------
    check("共 **95 条**子项，其中 **P0 = 94 条、P1 = 1 条**" in doc, "A4 §2.8 要点声明 95 / P0 94 / P1 1")
    check("**15 组功能需求 95 条子项**" in doc, "A4 §18 声明 95 条子项")
    check(doc.count("（95 条，P0 94 / P1 1）") + doc.count("（95 / P0 94 / P1 1）") >= 2,
          "A4 附录 A / 附录 B 声明 95 / P0 94 / P1 1")

    # ---------- A5 residual numbers ----------
    white = ("P-8", "残留", "更正", "原表述", "0 命中", "曾", "v1.1.1", "v1.0")
    bad = []
    for i, ln in enumerate(doc.split("\n"), 1):
        for num in ("96 条", "96 项", "70 条", "+26"):
            if num in ln and not any(w in ln for w in white):
                bad.append((i, num, ln.strip()[:90]))
    check(not bad, "A5 无「作为本版数字」的 96/70/+26 残留（历史引文与更正说明除外）")
    for b in bad:
        print("      行 %d 命中 %s：%s" % b)

    print()
    if FAIL:
        print("断言失败 %d 项" % len(FAIL))
        return 1
    print("全部统计断言通过")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1] if len(sys.argv) > 1 else "docs/agent-fleet-architecture-v1.1.2.md"))

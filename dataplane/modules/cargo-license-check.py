#!/usr/bin/env python3
"""检查 cargo 依赖许可（ADR 0002 / CLAUDE.md「扩展数据类型模块」）。

用法：cargo metadata --format-version 1 | python3 -I dataplane/modules/cargo-license-check.py

许可表达式按 SPDX 的 OR / AND 解析：OR 只要有一个分支全部在白名单内即通过，
AND 要求所有成员都在白名单内；"WITH <exception>" 只看主许可。
退出码：全部通过为 0；存在未通过或无许可信息的依赖为 1。
"""
import json
import re
import sys

ALLOWED = {"MIT", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "ICU", "CC0-1.0"}


def ok(expr: str) -> bool:
    expr = expr.replace("(", " ").replace(")", " ").replace("/", " OR ")
    for alt in re.split(r"\s+OR\s+", expr.strip()):
        tokens = [t for t in re.split(r"\s+AND\s+", alt.strip()) if t]
        tokens = [re.split(r"\s+WITH\s+", t)[0].strip() for t in tokens]
        if tokens and all(t in ALLOWED for t in tokens):
            return True
    return False


def main() -> int:
    meta = json.load(sys.stdin)
    bad = 0
    rows = []
    for pkg in sorted(meta["packages"], key=lambda p: p["name"]):
        lic = pkg.get("license") or ""
        status = "ok" if lic and ok(lic) else "FAIL"
        if status == "FAIL":
            bad += 1
        rows.append((pkg["name"], pkg["version"], lic or "(无)", status))
    width = max(len(r[0]) for r in rows)
    for name, ver, lic, status in rows:
        print(f"{status:4}  {name:<{width}}  {ver:<10}  {lic}")
    print(f"共 {len(rows)} 个包，未通过 {bad} 个")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())

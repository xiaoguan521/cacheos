#!/usr/bin/env bash
# 校验每个当前产品版本都有 CSAF VEX 记录，并覆盖 versions.yaml 中登记的 CVE。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CURRENT="$(awk '/^current:/{print $2}' "$ROOT/dataplane/versions.yaml")"
VEX="$ROOT/security/vex/$CURRENT.csaf.json"
[[ -s "$VEX" ]] || { echo "check-vex FAILED: missing $VEX" >&2; exit 1; }

python3 - "$ROOT/dataplane/versions.yaml" "$VEX" "$CURRENT" <<'PY'
import json
import re
import sys
from pathlib import Path

versions, vex_path = map(Path, sys.argv[1:3])
current = sys.argv[3]
doc = json.loads(vex_path.read_text())
meta = doc.get("document", {})
assert meta.get("csaf_version") == "2.0", "document.csaf_version must be 2.0"
assert meta.get("category") == "csaf_vex", "document.category must be csaf_vex"
assert current in str(meta.get("title", "")), "VEX title does not name current product version"
assert isinstance(doc.get("product_tree"), dict), "product_tree is required"
vulns = doc.get("vulnerabilities", [])
assert isinstance(vulns, list), "vulnerabilities must be an array"

text = versions.read_text()
match = re.search(rf"(?ms)^  - product: {re.escape(current)}\s*\n(.*?)(?=^  - product:|\Z)", text)
assert match, f"versions.yaml has no release entry for {current}"
expected = set(re.findall(r"CVE-\d{4}-\d+", match.group(1)))
declared = set()
for vuln in vulns:
    if isinstance(vuln, dict):
        for key in ("cve", "id"):
            if isinstance(vuln.get(key), str) and vuln[key].startswith("CVE-"):
                declared.add(vuln[key])
assert expected <= declared, f"missing CVE entries in VEX: {sorted(expected - declared)}"
print(f"check-vex OK ({current}, {len(vulns)} vulnerability records)")
PY

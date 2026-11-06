#!/usr/bin/env bash
# 版本一致性校验（规格书 12 节）：versions.yaml、UPSTREAM、patches 目录、安装包 BUILDINFO 与 SBOM 必须一致。
#
# 用法：scripts/check-versions.sh [<sycache-<版本>-<架构>.tar.gz>]
#   不带参数：只校验仓库内的 versions.yaml、UPSTREAM 与 patches 目录。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck disable=SC1091
source "$ROOT/dataplane/UPSTREAM"
Y="$ROOT/dataplane/versions.yaml"
fail() { echo "check-versions FAILED: $*" >&2; exit 1; }

current="$(awk '/^current:/{print $2}' "$Y")"
[[ -n "$current" ]] || fail "versions.yaml has no current"
# current 对应的 release 条目（取到下一个 "- product:" 为止）
entry="$(awk -v v="$current" '/^  - product:/{on=($3==v)} on' "$Y")"
[[ -n "$entry" ]] || fail "versions.yaml has no release entry for current $current"
grep -q "upstream: valkey $UPSTREAM_VERSION\$" <<<"$entry" || fail "release $current upstream != valkey $UPSTREAM_VERSION"
grep -q "upstream_commit: $UPSTREAM_COMMIT\$" <<<"$entry" || fail "release $current upstream_commit != $UPSTREAM_COMMIT"

listed="$(sed -nE 's/^ +- file: (.*)$/\1/p' <<<"$entry" | sort)"
present="$(cd "$ROOT/dataplane/patches" && ls ./*.patch | xargs -n1 basename | sort)"
[[ "$listed" == "$present" ]] || fail "patches in versions.yaml ($(echo $listed)) != dataplane/patches ($(echo $present))"
# 每个 CVE 补丁必须在 cves 中登记
for p in $present; do
  [[ "$p" =~ ^1[0-9]{3}-cve-([0-9]{4}-[0-9]+)\.patch$ ]] || continue
  grep -q "CVE-${BASH_REMATCH[1]}" <<<"$entry" || fail "$p is not listed under cves of $current"
done

if [[ $# -ge 1 ]]; then
  W="$(mktemp -d)"; trap 'rm -rf "$W"' EXIT
  tar -C "$W" -xzf "$1" ./opt/sycache/share/BUILDINFO ./opt/sycache/share/sbom.spdx.json
  # shellcheck disable=SC1091
  BI_FILE="$W/buildinfo.vars"
  sed 's/^/BI_/' "$W/opt/sycache/share/BUILDINFO" > "$BI_FILE"
  source "$BI_FILE"
  [[ "$BI_product_version" == "$current" ]] || fail "BUILDINFO product_version $BI_product_version != $current"
  [[ "$BI_upstream_version" == "$UPSTREAM_VERSION" ]] || fail "BUILDINFO upstream_version mismatch"
  [[ "$BI_upstream_commit" == "$UPSTREAM_COMMIT" ]] || fail "BUILDINFO upstream_commit mismatch"
  [[ "$(tr ' ' '\n' <<<"$BI_patches" | sort)" == "$present" ]] || fail "BUILDINFO patches mismatch"
  python3 - "$W/opt/sycache/share/sbom.spdx.json" "$current" "$UPSTREAM_VERSION" <<'EOF' || fail "SBOM mismatch"
import json, sys
doc = json.load(open(sys.argv[1]))
by = {p["name"]: p["versionInfo"] for p in doc["packages"]}
assert by.get("sycache") == sys.argv[2], by.get("sycache")
assert by.get("valkey") == sys.argv[3], by.get("valkey")
EOF
fi
echo "check-versions OK ($current → valkey $UPSTREAM_VERSION)"

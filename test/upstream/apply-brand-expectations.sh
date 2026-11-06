#!/usr/bin/env bash
# 将上游 Tcl 测试中与产品公开品牌强绑定的断言同步到 sycache。
#
# 这是测试适配层，不修改产品源码或协议行为。每个替换都校验精确出现次数，
# 上游源码变化时立即失败，避免把真实行为差异静默吞掉。
set -euo pipefail

SRC="${1:?usage: $0 <upstream-source-dir>}"

replace_exact() {
  local file="$1" old="$2" new="$3" expected="$4"
  python3 - "$SRC/$file" "$old" "$new" "$expected" <<'PY'
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
old, new, expected = sys.argv[2], sys.argv[3], int(sys.argv[4])
text = path.read_text()
actual = text.count(old)
if actual != expected:
    raise SystemExit(
        f"{path}: expected {expected} occurrences of {old!r}, found {actual}"
    )
path.write_text(text.replace(old, new))
PY
}

replace_exact \
  tests/unit/other.tcl \
  'assert_equal valkey [dict get $hello server]' \
  'assert_equal sycache [dict get $hello server]' \
  1
replace_exact \
  tests/unit/other.tcl \
  'assert_equal "valkey" [dict get $hello server]' \
  'assert_equal "sycache" [dict get $hello server]' \
  1
replace_exact \
  tests/unit/other.tcl \
  'assert_match {*Valkey ver.*} $lolwut_output' \
  'assert_match {*SyCache ver.*} $lolwut_output' \
  4
replace_exact \
  tests/unit/other.tcl \
  'assert_match "*/valkey-server" [lindex $cmdline 1]' \
  'assert_match "*/sycache-server" [lindex $cmdline 1]' \
  1
replace_exact \
  tests/unit/cluster/slot-ownership.tcl \
  '*Valkey is now ready to exit, bye bye*' \
  '*SyCache is now ready to exit, bye bye*' \
  1
replace_exact \
  tests/integration/dual-channel-replication.tcl \
  '*Loading RDB produced by Valkey version*' \
  '*Loading RDB produced by SyCache version*' \
  1

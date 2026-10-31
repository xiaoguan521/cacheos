#!/usr/bin/env bash
# 改名扫描（规格书 5.6）：在已打 patch 的源码树中查找残留的 valkey 字符串字面量。
# 只允许 dataplane/rename/rename.py 中 KEEP 规则列出的协议 / 格式 / 模块 API 字面量。
# 升级上游时，上游新增的品牌字串会在这里暴露，必须先补规则再构建。
#
# 用法：scripts/rename-scan.sh <已打 patch 的源码目录>
set -euo pipefail
SRC="${1:?usage: rename-scan.sh <src-dir>}"
cd "$SRC/src"

hits=$(grep -rnE '"[^"]*[Vv][Aa][Ll][Kk][Ee][Yy][^"]*"' --include=*.c --include=*.h --include=commands.def . \
  | grep -v '^\./unit/' \
  | grep -v '^\./modules/hello' \
  | grep -vE '^[^:]+:[0-9]+:[[:space:]]*#[[:space:]]*include' \
  | grep -vE '^[^:]+:[0-9]+:[[:space:]]*(/\*|\*|//)' \
  | grep -v '^\./fmacros\.h:' \
  | grep -v '^\./valkeymodule\.h:' \
  | grep -v '^\./redismodule\.h:' \
  | grep -vE 'ValkeyModule_|VALKEYMODULE' \
  | grep -vE '^\./(rdb|aof|valkey-check-rdb|valkey-check-aof)\.c:.*"VALKEY"' \
  | grep -vE '^\./rdb\.c:.*"valkey-ver"' \
  | grep -vE '^\./modules/lua/script_lua\.c:.*"VALKEY_VERSION(_NUM)?"' \
  | grep -vE '^\./modules/lua/debug_lua\.c:.*\("valkey",' \
  | grep -vE '^\./cli_common\.c:.*"valkeys?://"' \
  || true)

if [[ -n "$hits" ]]; then
  echo "rename-scan FAILED: unexpected 'valkey' string literals (add a rule to dataplane/rename/rename.py):"
  echo "$hits"
  exit 1
fi
echo "rename-scan OK"

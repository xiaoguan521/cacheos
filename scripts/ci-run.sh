#!/usr/bin/env bash
# 在 GitHub Actions 中运行一条命令：失败时把输出末尾以 ::error:: 注解上报（注解可通过 API 读取，日志不一定能），
# 成功时把以 "NOTICE:" 开头的输出行转成 ::notice:: 注解。本地运行时只是普通执行。
#
# 用法：scripts/ci-run.sh <标题> <命令> [参数...]
set -uo pipefail
TITLE="$1"; shift
LOG="$(mktemp)"
"$@" 2>&1 | tee "$LOG"
rc=${PIPESTATUS[0]}
enc() { sed -e 's/%/%25/g' | sed -e ':a;N;$!ba;s/\n/%0A/g' | sed -e 's/\r/%0D/g'; }
if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
  grep -E '^NOTICE:' "$LOG" | sed -E 's/^NOTICE: ?//' | head -20 | while IFS= read -r l; do
    echo "::notice title=$TITLE::$(printf '%s' "$l" | enc)"
  done
  if [[ $rc -ne 0 ]]; then
    echo "::error title=$TITLE (exit $rc)::$(grep -vE '^::' "$LOG" | tail -60 | cut -c1-300 | enc)"
  fi
fi
rm -f "$LOG"
exit "$rc"

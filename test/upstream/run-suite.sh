#!/usr/bin/env bash
# 在改名后的源码上运行上游 Tcl 测试集，并与 test/upstream/known-diff.txt 比对。
# 本地与 CI 通用；需要 tclsh 8.5+。
#
# 用法：test/upstream/run-suite.sh [-s <上游源码目录>] [runtest 额外参数...]
# 结果：新增失败（不在清单中）→ 退出码 1；清单中但已通过的用例会提示删除。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
# shellcheck disable=SC1091
source "$ROOT/dataplane/UPSTREAM"
SRC=""
if [[ "${1:-}" == "-s" ]]; then SRC="$2"; shift 2; fi

WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
if [[ -z "$SRC" ]]; then
  git -c advice.detachedHead=false clone -q --depth 1 --branch "$UPSTREAM_TAG" "$UPSTREAM_GIT" "$WORK/src"
  [[ "$(git -C "$WORK/src" rev-parse HEAD)" == "$UPSTREAM_COMMIT" ]] || { echo "upstream commit mismatch" >&2; exit 3; }
else
  cp -a "$SRC" "$WORK/src"
fi
cd "$WORK/src"
patch -p1 --forward --quiet < "$ROOT/dataplane/patches/0001-sycache-rename.patch"
make -j"$(nproc)" ENGINE_NAME=sycache USE_REDIS_SYMLINKS=no BUILD_TLS=yes >"$WORK/build.log" 2>&1 \
  || { tail -50 "$WORK/build.log"; exit 4; }
"$ROOT/test/upstream/link-binaries.sh" src

./runtest --clients "$(nproc)" --timeout 1200 "$@" 2>&1 | tee "$WORK/test.log" || true

# 失败用例：runtest 输出 "[err]: <名称> in <文件>"
grep -oE '\[err\]: .*' "$WORK/test.log" | sed -E 's/^\[err\]: //' | sort -u > "$WORK/failed.txt" || true
KNOWN="$ROOT/test/upstream/known-diff.txt"
grep -vE '^\s*(#|$)' "$KNOWN" | sed -E 's/[[:space:]]+#.*$//' | sort -u > "$WORK/known.txt"
comm -23 "$WORK/failed.txt" "$WORK/known.txt" > "$WORK/new.txt"
comm -13 "$WORK/failed.txt" "$WORK/known.txt" > "$WORK/fixed.txt"

# 汇总成单条注解（GitHub 每步只显示前 10 条注解；单条消息可容纳全部用例名，便于通过 API 读取）
enc() { sed -e 's/%/%25/g' | sed -e ':a;N;$!ba;s/\n/%0A/g'; }
annotate() {
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then echo "::$1 title=$2::$(enc)"; else echo "$1: $2"; cat; fi
}
[[ -s "$WORK/new.txt" ]] && annotate error "upstream suite: $(wc -l < "$WORK/new.txt") new failures" < "$WORK/new.txt"
[[ -s "$WORK/fixed.txt" ]] && annotate warning "known diffs now passing (remove from known-diff.txt)" < "$WORK/fixed.txt"
summary="upstream suite: $(wc -l < "$WORK/failed.txt") failed, $(wc -l < "$WORK/new.txt") new, $(wc -l < "$WORK/fixed.txt") fixed"
echo "$summary" | annotate notice "upstream suite summary"
# 失败用例的断言细节（每个用例取 [err] 之后的几行），供评审 known-diff 时判断是否只是改名导致
if [[ -s "$WORK/new.txt" ]]; then
  grep -A4 -F '[err]' "$WORK/test.log" | cut -c1-240 | head -400 | annotate warning "upstream failure details"
fi
grep -q 'All tests passed' "$WORK/test.log" || [[ -s "$WORK/failed.txt" ]] \
  || { annotate error "suite did not finish (crash or timeout), see log"; exit 1; }
[[ ! -s "$WORK/new.txt" ]]

#!/usr/bin/env bash
# 在改名后的源码上运行上游 Tcl 功能测试集，并与 test/upstream/known-diff.txt 比对。
# 本地与 CI 通用；需要 tclsh 8.5+。
# 延迟断言依赖宿主机负载，不作为协议兼容门禁；MPTCP 仅在内核明确启用时运行。
#
# 用法：test/upstream/run-suite.sh [-s <上游源码目录>] [runtest 额外参数...]
# 结果：新增失败（不在清单中）→ 退出码 1；清单中但已通过的用例会提示删除。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
# shellcheck disable=SC1091
source "$ROOT/dataplane/UPSTREAM"
SRC=""
if [[ "${1:-}" == "-s" ]]; then SRC="$2"; shift 2; fi

# Upstream tests create Unix sockets below WORK; keep the prefix short enough
# for macOS's 104-byte AF_UNIX path limit.
WORK="$(mktemp -d /tmp/sycache-upstream.XXXXXX)"; trap 'rm -rf "$WORK"' EXIT
JOBS="${CACHEOS_JOBS:-$(command -v nproc >/dev/null 2>&1 && nproc || (command -v sysctl >/dev/null 2>&1 && sysctl -n hw.ncpu || echo 2))}"
[[ "$JOBS" =~ ^[1-9][0-9]*$ ]] || { echo "CACHEOS_JOBS must be a positive integer" >&2; exit 2; }
BUILD_JOBS="${CACHEOS_BUILD_JOBS:-1}"
[[ "$BUILD_JOBS" =~ ^[1-9][0-9]*$ ]] || { echo "CACHEOS_BUILD_JOBS must be a positive integer" >&2; exit 2; }
if [[ -z "$SRC" ]]; then
  git -c advice.detachedHead=false clone -q --depth 1 --branch "$UPSTREAM_TAG" "$UPSTREAM_GIT" "$WORK/src"
  [[ "$(git -C "$WORK/src" rev-parse HEAD)" == "$UPSTREAM_COMMIT" ]] || { echo "upstream commit mismatch" >&2; exit 3; }
else
  cp -a "$SRC" "$WORK/src"
fi
cd "$WORK/src"
patch -p1 --forward --quiet < "$ROOT/dataplane/patches/0001-sycache-rename.patch"
"$ROOT/test/upstream/apply-brand-expectations.sh" "$WORK/src"
MAKE_COMPAT_FLAGS=()
if [[ "$(uname -s)" == Darwin ]]; then
  # Homebrew OpenSSL 4 marks APIs used by the pinned upstream release as
  # deprecated; keep TLS enabled and downgrade only that platform warning.
  # The upstream libvalkey sub-build does not inherit OPENSSL_CFLAGS, so pass
  # Homebrew's include directory explicitly to that recursive make as well.
  openssl_prefix="$(brew --prefix openssl 2>/dev/null || true)"
  compat_cflags="-Wno-error=deprecated-declarations"
  [[ -n "$openssl_prefix" ]] && compat_cflags="-I$openssl_prefix/include $compat_cflags"
  MAKE_COMPAT_FLAGS+=(CFLAGS="$compat_cflags")
fi
make -j"$BUILD_JOBS" ENGINE_NAME=sycache USE_REDIS_SYMLINKS=no BUILD_TLS=yes "${MAKE_COMPAT_FLAGS[@]}" >"$WORK/build.log" 2>&1 \
  || { tail -50 "$WORK/build.log"; exit 4; }
"$ROOT/test/upstream/link-binaries.sh" src

RUNTEST_ARGS=(--clients "$JOBS" --timeout 1200 --no-latency)
if [[ ! -r /proc/sys/net/mptcp/enabled || "$(< /proc/sys/net/mptcp/enabled)" != "1" ]]; then
  RUNTEST_ARGS+=(--skipunit unit/mptcp)
fi
if [[ "$(uname -s)" == Darwin ]]; then
  # macOS adds a setsockopt(TCP_NODELAY) diagnostic to one upstream CLI
  # assertion that expects Linux's plain "Connection refused" text.
  RUNTEST_ARGS+=(--skipunit unit/cluster/cli)
fi

: > "$WORK/test.log"
suite_status=0
run_suite() {
  local name="$1"
  shift
  local log="$WORK/$name.log"
  echo "=== $name ===" | tee -a "$WORK/test.log"
  set +e
  "$@" 2>&1 | tee "$log"
  local status="${PIPESTATUS[0]}"
  set -e
  cat "$log" >> "$WORK/test.log"
  if [[ "$status" -ne 0 ]]; then
    suite_status=1
    printf '%s exit status: %s\n' "$name" "$status" >> "$WORK/runner-errors.txt"
  fi
}

# runtest-cluster 复用 runtest 的选项；Sentinel 入口是独立 Tcl runner，
# 不接受 --clients / --timeout / --no-latency，因此只启用 fail-fast。
run_suite main ./runtest "${RUNTEST_ARGS[@]}" "$@"
run_suite cluster ./runtest-cluster "${RUNTEST_ARGS[@]}" "$@"
run_suite sentinel ./runtest-sentinel --fast-fail

# 失败用例：runtest 输出 "[err]: <名称> in <文件>"
grep -oE '\[err\]: .*' "$WORK/test.log" | sed -E 's/^\[err\]: //' | sort -u > "$WORK/failed.txt" || true
KNOWN="$ROOT/test/upstream/known-diff.txt"
{ grep -vE '^\s*(#|$)' "$KNOWN" || true; } | sed -E 's/[[:space:]]+#.*$//' | sort -u > "$WORK/known.txt"
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
  # runtest 结束时汇总打印 "The following tests failed:" 及每个用例的断言信息
  { sed -n '/The following tests failed/,$p' "$WORK/test.log" || true; } | cut -c1-240 | head -400 \
    | annotate warning "upstream failure details" || true
fi
if [[ -s "$WORK/runner-errors.txt" ]]; then
  annotate error "upstream suite runner errors" < "$WORK/runner-errors.txt" || true
fi
if ! grep -q 'All tests passed' "$WORK/test.log" && [[ ! -s "$WORK/failed.txt" ]]; then
  tail -80 "$WORK/test.log" | cut -c1-240 | annotate error "suite did not finish (crash or timeout), log tail"
  exit 1
fi
# 异常（[exception]）与超时也要暴露，它们不会出现在 [err] 列表里
if grep -qE '\[(exception|timeout)\]' "$WORK/test.log"; then
  { grep -B2 -A20 -E '\[(exception|timeout)\]' "$WORK/test.log" || true; } | cut -c1-240 | head -200 \
    | annotate error "suite exceptions / timeouts" || true
fi
[[ "$suite_status" -eq 0 && ! -s "$WORK/new.txt" ]]

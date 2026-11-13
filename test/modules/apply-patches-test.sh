#!/usr/bin/env bash
# apply-patches.sh 与 search patch 的测试：假源码树、失败条件、幂等、真实 patch 的结构。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT="$ROOT/dataplane/modules/apply-patches.sh"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
fail() { echo "  ✗ $*" >&2; exit 1; }

mk_src() { rm -rf "$1"; mkdir -p "$1/sub"; printf 'one\ntwo\nthree\n' > "$1/sub/f.txt"; }
mkdir -p "$T/patches/demo"
mk_src "$T/a"; mk_src "$T/b"
( cd "$T" && cp -r a b2 && sed -i.bak 's/two/TWO/' b2/sub/f.txt && rm b2/sub/f.txt.bak && diff -u a/sub/f.txt b2/sub/f.txt | sed '1s#.*#--- a/sub/f.txt#;2s#.*#+++ b/sub/f.txt#' > patches/demo/0001-x.patch || true )
[[ -s "$T/patches/demo/0001-x.patch" ]] || fail "没造出测试 patch"

# 1. 正常应用
bash "$SCRIPT" demo "$T/a" "$T/patches" >/dev/null
grep -q TWO "$T/a/sub/f.txt" || fail "patch 未生效"
[[ -s "$T/a/.cacheos-patches" ]] || fail "没有写标记"
# 2. 幂等：再次调用跳过，内容不变
bash "$SCRIPT" demo "$T/a" "$T/patches" >/dev/null || fail "重复调用应成功"
[[ "$(grep -c TWO "$T/a/sub/f.txt")" == 1 ]] || fail "重复应用了"
echo "  ✓ 应用与幂等"
# 3. 上游内容变了 → 大声失败，且不改文件
printf 'one\nCHANGED\nthree\n' > "$T/b/sub/f.txt"
if bash "$SCRIPT" demo "$T/b" "$T/patches" >/dev/null 2>"$T/err"; then fail "patch 不适用时应失败"; fi
grep -q "无法应用" "$T/err" || fail "错误信息不对: $(cat "$T/err")"
grep -q CHANGED "$T/b/sub/f.txt" && [[ ! -e "$T/b/.cacheos-patches" ]] || fail "失败后不应改动源码或写标记"
echo "  ✓ 不适用时失败"
# 4. 标记与当前 patch 不一致 → 失败
echo deadbeef > "$T/a/.cacheos-patches"
if bash "$SCRIPT" demo "$T/a" "$T/patches" >/dev/null 2>&1; then fail "标记不一致应失败"; fi
echo "  ✓ 标记不一致时失败"
# 5. 没有 patch 的模块：什么也不做；非法模块名 / 缺目录失败
mk_src "$T/c"; bash "$SCRIPT" json "$T/c" "$T/patches" >/dev/null && [[ ! -e "$T/c/.cacheos-patches" ]] || fail "无 patch 应无操作"
if bash "$SCRIPT" '../x' "$T/c" "$T/patches" >/dev/null 2>&1; then fail "非法模块名应失败"; fi
if bash "$SCRIPT" demo "$T/nope" "$T/patches" >/dev/null 2>&1; then fail "缺源码目录应失败"; fi
echo "  ✓ 无 patch / 非法参数"

# 6. 真实的 search patch：固定了两个 commit，且 build-modules.sh 在构建前调用了 apply-patches.sh
P="$ROOT/dataplane/modules/patches/search/0001-pin-build-inputs.patch"
[[ -s "$P" ]] || fail "缺 $P"
grep -q '^+.*f8381f3331d9c56a9792f9b4a35f61c41108c39e' "$P" || fail "patch 未固定 highwayhash 提交"
grep -q '^+.*845e54d32d86ee1bb872ebb5b1c755e6e3a94c34' "$P" || fail "patch 未固定 googletest 提交"
grep -q '^+.*is not the pinned commit' "$P" || fail "patch 缺少提交核对"
grep -qE 'apply-patches.sh' "$ROOT/dataplane/build-modules.sh" || fail "build-modules.sh 未调用 apply-patches.sh"
# 应用失败必须使构建失败：调用处不得吞掉退出码
if grep -nE 'apply-patches.sh.*(\|\| *true|2>/dev/null *\|\| *true)' "$ROOT/dataplane/build-modules.sh"; then fail "不得忽略 patch 应用失败"; fi
echo "  ✓ search patch 固定提交，构建前强制应用"
echo "✓ apply-patches 测试通过"

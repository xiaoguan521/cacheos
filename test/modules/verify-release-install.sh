#!/usr/bin/env bash
# 在干净的发行版容器里，用已发布的安装包真实安装 sycache + sycache-modules 并验证（ADR 0002）。
#
# 用法（容器内，root）：verify-release-install.sh <deb|rpm|tar|reject-deb> <资产目录>
#   deb / rpm   ：安装主包 + 模块包，检查布局与依赖，三个模块都能加载并执行基本命令，卸载模块包后主包保留
#   tar         ：解压主包与模块包到同一前缀，同样加载并执行
#   reject-deb  ：在 glibc 低于 2.38 的发行版上，模块包必须因依赖不满足而拒绝安装（主包仍可装）
set -euo pipefail

MODE="${1:?mode}"; ASSETS="${2:?assets dir}"
ROOT=/home/atwasoft/sycache
fail() { echo "  ✗ $*" >&2; exit 1; }
ok() { echo "  ✓ $*"; }

case "$(uname -m)" in x86_64) DEB_ARCH=amd64; ARCH=x86_64 ;; aarch64) DEB_ARCH=arm64; ARCH=aarch64 ;; *) fail "unsupported arch" ;; esac
pick() { local f; f="$(ls "$ASSETS"/$1 2>/dev/null | head -1)"; [[ -n "$f" ]] || fail "资产缺失: $1"; echo "$f"; }

echo "glibc: $(ldd --version 2>&1 | head -1)"
install_deb() { apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq python3 ca-certificates >/dev/null; }

case "$MODE" in
  deb)
    install_deb
    BASE="$(pick "sycache_*_${DEB_ARCH}.deb")"; MOD="$(pick "sycache-modules_*_${DEB_ARCH}.deb")"
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$BASE" "$MOD" >/dev/null || fail "apt 安装失败"
    ;;
  rpm)
    command -v dnf >/dev/null || fail "需要 dnf"
    dnf install -y -q python3 >/dev/null
    BASE="$(pick "sycache-[0-9]*.${ARCH}.rpm")"; MOD="$(pick "sycache-modules-*.${ARCH}.rpm")"
    dnf install -y -q "$BASE" "$MOD" >/dev/null || fail "dnf 安装失败"
    ;;
  tar)
    install_deb
    BASE="$(pick "sycache-[0-9]*-${ARCH}.tar.gz")"; MOD="$(pick "sycache-modules-*-${ARCH}.tar.gz")"
    tar -C / -xzf "$BASE"; tar -C / -xzf "$MOD"
    ln -sfn . "$ROOT/current"
    ;;
  reject-deb)
    install_deb
    BASE="$(pick "sycache_*_${DEB_ARCH}.deb")"; MOD="$(pick "sycache-modules_*_${DEB_ARCH}.deb")"
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$BASE" >/dev/null || fail "主包应能在旧发行版安装"
    if DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$MOD" >/tmp/reject.log 2>&1; then fail "模块包不应在 glibc < 2.38 的系统上安装成功"; fi
    grep -E "libc6|libstdc\+\+6|Depends|depends" /tmp/reject.log | head -5 || true
    [[ ! -e "$ROOT/modules/json.so" ]] || fail "被拒绝后不应留下模块文件"
    ok "模块包被依赖检查拒绝，主包不受影响"
    exit 0
    ;;
  *) fail "未知模式 $MODE" ;;
esac
ok "安装成功（$MODE）"

# 1. 布局
for m in json search bloom; do
  [[ -x "$ROOT/modules/$m.so" ]] || fail "缺少 modules/$m.so"
  [[ -s "$ROOT/share/modules/$m.spdx.json" ]] || fail "缺少 $m 的 SBOM"
  [[ -n "$(find "$ROOT/licenses/modules/$m" -type f -print -quit)" ]] || fail "缺少 $m 的许可文本"
done
[[ -s "$ROOT/share/MODULES-BUILDINFO" ]] || fail "缺少 MODULES-BUILDINFO"
ok "模块文件、许可文本、SBOM 与构建来源齐全"

# 2. 动态库依赖都能解析
for m in json search bloom; do
  if ldd "$ROOT/modules/$m.so" | grep -q "not found"; then ldd "$ROOT/modules/$m.so"; fail "$m.so 有未解析的动态库"; fi
done
ok "动态库依赖全部可解析"

# 3. 以实例真实路径形态加载三个模块并执行基本命令
CLI="$ROOT/bin/sycache-cli"; PORT=7391; D="$(mktemp -d)"
"$ROOT/bin/sycache-server" --port "$PORT" --dir "$D" --save "" --daemonize no \
  --loadmodule "$ROOT/current/modules/json.so" \
  --loadmodule "$ROOT/current/modules/bloom.so" \
  --loadmodule "$ROOT/current/modules/search.so" >"$D/server.log" 2>&1 &
PID=$!; trap 'kill $PID 2>/dev/null || true' EXIT
up=0; for _ in $(seq 1 60); do "$CLI" -p "$PORT" PING >/dev/null 2>&1 && { up=1; break; }; sleep 0.5; done
[[ $up -eq 1 ]] || { grep -v "Defined Info Field" "$D/server.log" | tail -30 | cut -c1-300; fail "服务端未能启动"; }
c() { "$CLI" -p "$PORT" "$@"; }
mods="$(c MODULE LIST | tr 'A-Z' 'a-z')"
for w in json bf search; do grep -q "$w" <<<"$mods" || fail "MODULE LIST 缺少 $w"; done
[[ "$(c JSON.SET k '$' '{"a":1}')" == OK ]] || fail "JSON.SET"
[[ "$(c JSON.GET k '$.a')" == "[1]" ]] || fail "JSON.GET 期望 [1]"
[[ "$(c BF.ADD f x)" == 1 ]] || fail "BF.ADD"
[[ "$(c BF.EXISTS f x)" == 1 ]] || fail "BF.EXISTS"
[[ "$(c FT.CREATE idx ON HASH PREFIX 1 d: SCHEMA t TAG)" == OK ]] || fail "FT.CREATE"
c HSET d:1 t hello >/dev/null
sleep 1
out="$(c FT.SEARCH idx '@t:{hello}')"
grep -q "d:1" <<<"$out" || { echo "$out"; fail "FT.SEARCH 未找到文档"; }
ok "JSON / 布隆 / 搜索 三个模块加载并执行基本命令"
kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; trap - EXIT

# 4. 卸载模块包：主包保留，模块文件移除
case "$MODE" in
  deb) DEBIAN_FRONTEND=noninteractive apt-get remove -y -qq sycache-modules >/dev/null ;;
  rpm) dnf remove -y -q sycache-modules >/dev/null ;;
  tar) rm -rf "$ROOT/modules" ;;
esac
[[ ! -e "$ROOT/modules/json.so" ]] || fail "卸载后模块文件仍在"
[[ -x "$ROOT/bin/sycache-server" ]] || fail "卸载模块包不应影响主包"
ok "卸载模块包后主包保留"

#!/usr/bin/env bash
# build-modules-package.sh 的 tar 部分测试（deb / rpm 需要 dpkg-deb / rpmbuild，在 CI 的 modules.yml 里验证）。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT="$ROOT/dataplane/packaging/build-modules-package.sh"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
fail() { echo "  ✗ $*" >&2; exit 1; }
for n in json bloom; do
  mkdir -p "$T/dist/licenses/$n"; echo so >"$T/dist/valkey-$n.so"; echo lic >"$T/dist/licenses/$n/LICENSE"; echo '{}' >"$T/dist/sbom-$n.spdx.json"
done
ARCH=x86_64 "$SCRIPT" "$T/dist" "$T/out" -f tar -V 1.2.3 >/dev/null
tarball="$T/out/sycache-modules-1.2.3-x86_64.tar.gz"
[[ -f "$tarball" ]] || fail "没有生成 $tarball"
list="$(tar -tzf "$tarball")"
for want in home/atwasoft/sycache/modules/json.so home/atwasoft/sycache/modules/bloom.so \
  home/atwasoft/sycache/licenses/modules/json/LICENSE home/atwasoft/sycache/share/modules/bloom.spdx.json home/atwasoft/sycache/share/MODULES-BUILDINFO; do
  grep -q "$want" <<<"$list" || fail "包里缺 $want"
done
# 不得带主包的文件：本包与 sycache 主包不重叠
if grep -qE 'sycache/(bin|conf|instances)/|systemd|logrotate' <<<"$list"; then fail "不应包含主包文件"; fi
if grep -q 'search.so' <<<"$list"; then fail "没有 search 产物时不应出现 search"; fi
bi="$(tar -xzOf "$tarball" ./home/atwasoft/sycache/share/MODULES-BUILDINFO)"
grep -q '^product_version=1.2.3$' <<<"$bi" && grep -q '^modules=json:1.0.3,bloom:1.0.1$' <<<"$bi" || fail "BUILDINFO 内容不对: $bi"
echo "  ✓ tar 布局与 BUILDINFO"
if "$SCRIPT" "$T/empty" "$T/out2" -f tar >/dev/null 2>&1; then fail "没有模块产物应失败"; fi
mkdir -p "$T/none"; if ARCH=x86_64 "$SCRIPT" "$T/none" "$T/out2" -f tar >/dev/null 2>&1; then fail "空目录应失败"; fi
if ARCH=x86_64 "$SCRIPT" "$T/dist" "$T/out2" -f tar -V bad >/dev/null 2>&1; then fail "非法版本应失败"; fi
echo "  ✓ 失败条件"
echo "✓ build-modules-package 测试通过"

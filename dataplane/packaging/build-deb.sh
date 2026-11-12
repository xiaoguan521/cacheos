#!/usr/bin/env bash
# 由构建产物 tar.gz 打 DEB 包（包内文件与 tar.gz、RPM 完全相同）。
#
# 用法：dataplane/packaging/build-deb.sh <sycache-<版本>-<架构>.tar.gz> <输出目录>
# 依赖由二进制的 NEEDED 动态库推导：兼容构建（静态 OpenSSL）只依赖 libc6 与 libsystemd0。
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
TAR="${1:?usage: build-deb.sh <package.tar.gz> <out-dir>}"; OUT="${2:?out-dir}"
mkdir -p "$OUT"
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
R="$WORK/root"; mkdir -p "$R"
tar -C "$R" -xzf "$TAR"
# shellcheck disable=SC1091
BI_FILE="$WORK/buildinfo.vars"
sed 's/^/BI_/' "$R/home/atwasoft/sycache/share/BUILDINFO" > "$BI_FILE"
source "$BI_FILE"

# 版本：1.0.0-rc.1 → 1.0.0~rc.1（~ 使预发布版排在正式版之前）
DEB_VERSION="${BI_product_version/-/\~}-1"
case "$BI_arch" in
  x86_64) DEB_ARCH=amd64 ;;
  aarch64) DEB_ARCH=arm64 ;;
  loongarch64) DEB_ARCH=loong64 ;;
  *) echo "unsupported arch $BI_arch" >&2; exit 2 ;;
esac

# NEEDED → Debian 包名；不认识的库直接失败，避免漏写依赖
declare -A MAP=([libc.so.6]="libc6 (>= ${BI_glibc_required})" [libm.so.6]="" [libdl.so.2]="" [libpthread.so.0]=""
                [librt.so.1]="" [libssl.so.3]="libssl3 | libssl3t64" [libcrypto.so.3]=""
                [libsystemd.so.0]="libsystemd0")
deps=()
while read -r lib; do
  [[ -v "MAP[$lib]" ]] || { echo "unmapped shared library dependency: $lib" >&2; exit 3; }
  [[ -n "${MAP[$lib]}" ]] && deps+=("${MAP[$lib]}")
done < <(for b in sycache-server sycache-cli sycache-benchmark; do
           readelf -d "$R/home/atwasoft/sycache/bin/$b" | sed -nE 's/.*NEEDED.*\[(.*)\]/\1/p'; done | sort -u)
DEPENDS="$(printf '%s\n' "${deps[@]}" | sort -u | paste -sd, - | sed 's/,/, /g')"

mkdir -p "$R/DEBIAN"
cat > "$R/DEBIAN/control" <<EOF
Package: sycache
Version: $DEB_VERSION
Architecture: $DEB_ARCH
Maintainer: CacheOS <support@cacheos.invalid>
Section: database
Priority: optional
Pre-Depends: passwd
Depends: $DEPENDS
Installed-Size: $(du -sk --exclude=DEBIAN "$R" | cut -f1)
Description: CacheOS sycache - Redis-compatible in-memory data store
 Data plane of CacheOS: standalone, replication, sentinel and native cluster.
 Engine $BI_upstream_version ($BI_upstream_commit), TLS: $BI_tls. Provenance: /home/atwasoft/sycache/share/BUILDINFO.
EOF
echo /etc/logrotate.d/sycache > "$R/DEBIAN/conffiles"

# 维护脚本：与 RPM 共用 packaging/scripts，按 dpkg 的参数约定转调
{ echo '#!/bin/sh'; echo 'set -e'; sed '1d' "$HERE/scripts/preinstall.sh"; } > "$R/DEBIAN/preinst"
{ echo '#!/bin/sh'; echo 'set -e'; echo '[ "$1" = "configure" ] || exit 0'; sed '1d' "$HERE/scripts/postinstall.sh"; } > "$R/DEBIAN/postinst"
{ echo '#!/bin/sh'; echo 'set -e'; echo 'case "$1" in remove) set -- remove ;; *) set -- upgrade ;; esac'
  sed '1d' "$HERE/scripts/preremove.sh"; } > "$R/DEBIAN/prerm"
{ echo '#!/bin/sh'; echo 'set -e'; echo 'case "$1" in remove|purge) set -- remove ;; *) set -- upgrade ;; esac'
  sed '1d' "$HERE/scripts/postremove.sh"; } > "$R/DEBIAN/postrm"
chmod 0755 "$R/DEBIAN/preinst" "$R/DEBIAN/postinst" "$R/DEBIAN/prerm" "$R/DEBIAN/postrm"

DEB="$OUT/sycache_${DEB_VERSION}_${DEB_ARCH}.deb"
dpkg-deb --root-owner-group -Zxz --build "$R" "$DEB" >/dev/null
echo "built $DEB"

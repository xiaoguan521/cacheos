#!/usr/bin/env bash
# 构建「扩展数据类型模块」独立可选包 sycache-modules（ADR 0002，方案 B）：tar.gz、DEB、RPM。
#
# 用法：build-modules-package.sh <模块产物目录> <输出目录> [-f tar,deb,rpm] [-V <产品版本>]
#   模块产物目录：build-modules.sh 与 collect-licenses.py 的输出（valkey-<名>.so、licenses/<名>/、sbom-<名>.spdx.json）
#   模块在哪些名字下装：json search bloom，目录里有哪个 .so 就装哪个。
#
# 为什么是独立包：模块在 ubuntu 24.04 级别的工具链上构建，需要 glibc ≥ 2.38 / libstdc++ ≥ 13.1，
# 不能放进承诺 glibc 2.17 的 sycache 主包。本包只面向满足这些条件的发行版；包依赖由模块的 NEEDED 与符号版本推导，
# 不认识的库直接失败。装在主包的目录树里（/home/atwasoft/sycache/{modules,licenses/modules,share/modules}），
# 主包的 current 即指向该目录，实例经 <Root>/current/modules/<名>.so 加载。与主包文件不重叠。
# 卸载本包前须先停用相关实例：其配置里的 loadmodule 会指向已不存在的文件。
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
DIST="${1:?usage: build-modules-package.sh <modules-dist> <out-dir> [-f tar,deb,rpm] [-V version]}"; OUT="${2:?out-dir}"; shift 2
FORMATS="tar,deb,rpm"; VERSION="$(awk '/^current:/{print $2}' "$ROOT/dataplane/versions.yaml")"
while getopts "f:V:" opt; do case "$opt" in f) FORMATS="$OPTARG" ;; V) VERSION="$OPTARG" ;; *) exit 2 ;; esac; done
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.]+)?$ ]] || { echo "bad product version: $VERSION" >&2; exit 2; }
OBJDUMP="${OBJDUMP:-objdump}"
mkdir -p "$OUT"; OUT="$(cd "$OUT" && pwd)"; DIST="$(cd "$DIST" && pwd)"
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
R="$WORK/root"; P="$R/home/atwasoft/sycache"; mkdir -p "$P"
ARCH="${ARCH:-$(uname -m)}"
case "$ARCH" in x86_64|aarch64) ;; arm64) ARCH=aarch64 ;; *) echo "unsupported arch $ARCH" >&2; exit 2 ;; esac

want=(); for n in json search bloom; do [[ -f "$DIST/valkey-$n.so" ]] && want+=("$n"); done
[[ ${#want[@]} -gt 0 ]] || { echo "ERROR: $DIST 里没有任何模块 .so" >&2; exit 2; }
"$HERE/install-modules.sh" "$DIST" "$P" "${want[@]}" >/dev/null

# 汇总：模块清单、需要的最高 glibc / libstdc++、NEEDED 库
maxver() { # <GLIBC|GLIBCXX>
  { for n in "${want[@]}"; do "$OBJDUMP" -T "$P/modules/$n.so" 2>/dev/null || true; done; } \
    | { grep -oE "$1_[0-9]+(\.[0-9]+)+" || true; } | sed "s/$1_//" | sort -Vu | tail -1
}
needed() { for n in "${want[@]}"; do readelf -d "$P/modules/$n.so" | sed -nE 's/.*NEEDED.*\[(.*)\]/\1/p'; done | sort -u; }
UPSTREAM_VERSION="$(source "$ROOT/dataplane/UPSTREAM"; echo "$UPSTREAM_VERSION")"
mods=""; for n in "${want[@]}"; do v="$(awk -v n="$n" '$1==n{print $2}' "$ROOT/dataplane/modules/MODULES")"; mods="${mods:+$mods,}$n:$v"; done
GLIBC_REQ="$(maxver GLIBC)"; GLIBCXX_REQ="$(maxver GLIBCXX)"
{
  echo "product=cacheos-sycache-modules"
  echo "product_version=$VERSION"
  echo "upstream_version=$UPSTREAM_VERSION"
  echo "arch=$ARCH"
  echo "modules=$mods"
  echo "glibc_required=${GLIBC_REQ:-}"
  echo "glibcxx_required=${GLIBCXX_REQ:-}"
} > "$P/share/MODULES-BUILDINFO"
BASE="sycache-modules-$VERSION"

build_tar() {
  tar -C "$R" --owner=0 --group=0 -czf "$OUT/$BASE-$ARCH.tar.gz" .
  cp "$P/share/MODULES-BUILDINFO" "$OUT/MODULES-BUILDINFO-$ARCH"
  echo "built $OUT/$BASE-$ARCH.tar.gz"
}

# GLIBCXX 符号版本 → 提供它的最低 GCC（libstdc++6 包版本）。不认识的版本直接失败，避免依赖写错。
gcc_for_glibcxx() {
  case "$1" in
    "") echo "" ;;
    3.4.2[0-9]) echo 5.1 ;; 3.4.30) echo 11.1 ;; 3.4.31) echo 12.1 ;; 3.4.32) echo 13.1 ;; 3.4.33) echo 14.1 ;; 3.4.34) echo 15.1 ;;
    *) echo "ERROR: 不认识的 GLIBCXX_$1，请先在 gcc_for_glibcxx 中登记对应的 GCC 版本" >&2; return 1 ;;
  esac
}

build_deb() {
  command -v dpkg-deb >/dev/null || { echo "ERROR: 需要 dpkg-deb" >&2; return 1; }
  local deb_version="${VERSION/-/\~}-1" deb_arch gcc deps=() lib
  case "$ARCH" in x86_64) deb_arch=amd64 ;; aarch64) deb_arch=arm64 ;; esac
  gcc="$(gcc_for_glibcxx "$GLIBCXX_REQ")" || return 1
  while read -r lib; do
    case "$lib" in
      libc.so.6|ld-linux-*.so.*|libm.so.6|libmvec.so.1|libdl.so.2|libpthread.so.0|librt.so.1) deps+=("libc6 (>= ${GLIBC_REQ})") ;;
      libgcc_s.so.1) deps+=("libgcc-s1") ;;
      libstdc++.so.6) deps+=("libstdc++6 (>= ${gcc:-13.1})") ;;
      libssl.so.3|libcrypto.so.3) deps+=("libssl3 | libssl3t64") ;;
      libsystemd.so.0) deps+=("libsystemd0") ;;
      *) echo "unmapped shared library dependency: $lib" >&2; return 3 ;;
    esac
  done < <(needed)
  local depends; depends="sycache (>= ${VERSION/-/\~}), $(printf '%s\n' "${deps[@]}" | sort -u | paste -sd, - | sed 's/,/, /g')"
  mkdir -p "$R/DEBIAN"
  cat > "$R/DEBIAN/control" <<EOF
Package: sycache-modules
Version: $deb_version
Architecture: $deb_arch
Maintainer: CacheOS <support@cacheos.invalid>
Section: database
Priority: optional
Depends: $depends
Installed-Size: $(du -sk --exclude=DEBIAN "$R" | cut -f1)
Description: CacheOS sycache extension data types (optional)
 JSON, bloom filter and search/vector modules for sycache ($mods). Requires glibc >= ${GLIBC_REQ}
 and libstdc++ for GCC >= ${gcc:-13.1}; not available on older distributions. Provenance: /home/atwasoft/sycache/share/MODULES-BUILDINFO.
EOF
  dpkg-deb --root-owner-group -Zxz --build "$R" "$OUT/sycache-modules_${deb_version}_${deb_arch}.deb" >/dev/null
  rm -rf "$R/DEBIAN"
  echo "built $OUT/sycache-modules_${deb_version}_${deb_arch}.deb"
}

build_rpm() {
  command -v rpmbuild >/dev/null || { echo "ERROR: 需要 rpmbuild" >&2; return 1; }
  local v="${VERSION%%-*}" rel files
  if [[ "$VERSION" == *-* ]]; then rel="0.${VERSION#*-}"; else rel="1"; fi
  rel="${rel//-/.}"
  files="$(cd "$R" && find ./home/atwasoft/sycache/modules ./home/atwasoft/sycache/licenses/modules ./home/atwasoft/sycache/share/modules -mindepth 1 \( -type f -o -type l \) | sed 's#^\.##' | sort)"
  dirs="$(cd "$R" && find ./home/atwasoft/sycache/modules ./home/atwasoft/sycache/licenses/modules ./home/atwasoft/sycache/share/modules -type d | sed 's#^\.#%dir #' | sort)"
  cat > "$WORK/sycache-modules.spec" <<EOF
Name:           sycache-modules
Version:        $v
Release:        $rel
Summary:        CacheOS sycache extension data types (optional)
License:        BSD-3-Clause and other permissive third-party licenses (see /home/atwasoft/sycache/licenses/modules)
URL:            https://cacheos.invalid
Requires:       sycache >= $v
# 库依赖由 rpmbuild 的自动依赖分析从 .so 推导（glibc 符号版本、libstdc++、libssl、libsystemd）
AutoReqProv:    yes
%global __os_install_post %{nil}
%global debug_package %{nil}
%global _build_id_links none

%description
JSON, bloom filter and search/vector modules for sycache ($mods). Needs glibc >= ${GLIBC_REQ}; not available on older distributions.
Provenance: /home/atwasoft/sycache/share/MODULES-BUILDINFO.

%install
mkdir -p %{buildroot}
cp -a $R/. %{buildroot}/

%files
$dirs
/home/atwasoft/sycache/share/MODULES-BUILDINFO
$files
EOF
  rpmbuild -bb --quiet --define "_topdir $WORK/rpm" --define "_rpmdir $OUT" \
    --define "_build_name_fmt %%{NAME}-%%{VERSION}-%%{RELEASE}.%%{ARCH}.rpm" "$WORK/sycache-modules.spec"
  echo "built $(ls "$OUT"/sycache-modules-"$v"-"$rel".*.rpm)"
}

for f in ${FORMATS//,/ }; do
  case "$f" in tar) build_tar ;; deb) build_deb ;; rpm) build_rpm ;; *) echo "unknown format $f" >&2; exit 2 ;; esac
done

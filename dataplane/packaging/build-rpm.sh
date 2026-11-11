#!/usr/bin/env bash
# 由构建产物 tar.gz 打 RPM 包（包内文件与 tar.gz、DEB 完全相同）。在兼容构建容器中执行（需要 rpmbuild）。
#
# 用法：dataplane/packaging/build-rpm.sh <sycache-<版本>-<架构>.tar.gz> <输出目录>
# 依赖由 rpmbuild 的自动依赖分析从二进制推导（静态 OpenSSL 时只有 glibc 与 libsystemd）。
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
TAR="${1:?usage: build-rpm.sh <package.tar.gz> <out-dir>}"; OUT="${2:?out-dir}"
mkdir -p "$OUT"; OUT="$(cd "$OUT" && pwd)"
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
R="$WORK/root"; mkdir -p "$R"
tar -C "$R" -xzf "$TAR"
# shellcheck disable=SC1091
BI_FILE="$WORK/buildinfo.vars"
sed 's/^/BI_/' "$R/home/atwasoft/sycache/share/BUILDINFO" > "$BI_FILE"
source "$BI_FILE"

# 版本：1.0.0 → Version 1.0.0 Release 1；1.0.0-rc.1 → Version 1.0.0 Release 0.rc.1（预发布排在正式版之前）
V="${BI_product_version%%-*}"
if [[ "$BI_product_version" == *-* ]]; then REL="0.${BI_product_version#*-}"; else REL="1"; fi
REL="${REL//-/.}"

FILES="$(cd "$R" && find . -mindepth 1 \( -type f -o -type l \) | sed 's#^\.##' | sort \
  | sed -e 's#^/etc/logrotate.d/sycache$#%config(noreplace) &#')"
DIRS="$(cd "$R" && find ./home/atwasoft/sycache -type d | sed 's#^\.#%dir #' | sort)"

script() { sed '1d' "$HERE/scripts/$1"; }
cat > "$WORK/sycache.spec" <<EOF
Name:           sycache
Version:        $V
Release:        $REL
Summary:        CacheOS sycache - Redis-compatible in-memory data store
License:        Proprietary; includes BSD-3-Clause and other third-party components (see /home/atwasoft/sycache/licenses)
URL:            https://cacheos.invalid
AutoReqProv:    yes
# 安装前脚本创建系统用户；用文件依赖，兼容 RHEL 系（shadow-utils）与 openEuler（shadow）
Requires(pre):  /usr/sbin/useradd /usr/sbin/groupadd /usr/bin/getent
%global __os_install_post %{nil}
%global debug_package %{nil}
%global _build_id_links none

%description
Data plane of CacheOS: standalone, replication, sentinel and native cluster.
Engine $BI_upstream_version ($BI_upstream_commit), TLS: $BI_tls. Provenance: /home/atwasoft/sycache/share/BUILDINFO.

%install
mkdir -p %{buildroot}
cp -a $R/. %{buildroot}/

%pre
$(script preinstall.sh)

%post
$(script postinstall.sh)

%preun
if [ "\$1" -eq 0 ]; then set -- remove; else set -- upgrade; fi
$(script preremove.sh)

%postun
if [ "\$1" -eq 0 ]; then set -- remove; else set -- upgrade; fi
$(script postremove.sh)

%files
$DIRS
$FILES
EOF
rpmbuild -bb --quiet --define "_topdir $WORK/rpm" --define "_rpmdir $OUT" \
  --define "_build_name_fmt %%{NAME}-%%{VERSION}-%%{RELEASE}.%%{ARCH}.rpm" "$WORK/sycache.spec"
echo "built $(ls "$OUT"/sycache-"$V"-"$REL".*.rpm)"

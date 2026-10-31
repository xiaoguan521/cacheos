#!/usr/bin/env bash
# 构建 sycache 数据面（规格书 5.3）。
#
# 用法：
#   dataplane/build-sycache.sh [-s <上游源码目录>] [-o <输出目录>] [-t openssl|tongsuo|none] [-V <产品版本>] [-S]
#
# 不指定 -s 时按 dataplane/UPSTREAM 从官方 GitHub 仓库拉取发布标签，并校验提交哈希。
# -t openssl（默认）：BUILD_TLS=yes，链接系统 OpenSSL 3.x。
#    tongsuo：国密变体，OPENSSL_PREFIX 指向已编译的铜锁（TONGSUO_PREFIX 环境变量），P6 实现。
#    none：不带 TLS（仅开发调试用，不得发布）。
# -V：产品版本，默认取 dataplane/versions.yaml 的 current。
# -S：兼容构建。OpenSSL 按 dataplane/DEPS 拉取 LTS 源码并静态链接，二进制只依赖 glibc（与 libsystemd）。
#     在 glibc 2.17 的构建环境（dataplane/build-env/）中执行，即可覆盖 CentOS 7、麒麟 V10、统信 UOS、openEuler
#     与较新的发行版。OPENSSL_SRC 指向已检出的源码时跳过拉取；MAX_GLIBC 设置后校验二进制不依赖更高版本的 glibc 符号。
# USE_SYSTEMD：默认 auto（有 libsystemd-dev 时启用 Type=notify），发布构建环境必须为 yes。
# 产物：<输出目录>/sycache-<产品版本>-<架构>.tar.gz，解压到 / 得到 /opt/sycache 目录树；同目录输出 BUILDINFO。
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
# shellcheck disable=SC1091
source "$HERE/UPSTREAM"

SRC=""; OUT="$PWD/dist"; TLS_LIB="openssl"; STATIC=0
VERSION="$(awk '/^current:/{print $2}' "$HERE/versions.yaml")"
while getopts "s:o:t:V:S" opt; do
  case "$opt" in
    s) SRC="$OPTARG" ;;
    o) OUT="$OPTARG" ;;
    t) TLS_LIB="$OPTARG" ;;
    V) VERSION="$OPTARG" ;;
    S) STATIC=1 ;;
    *) exit 2 ;;
  esac
done
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.]+)?$ ]] || { echo "bad product version: $VERSION" >&2; exit 2; }
mkdir -p "$OUT"; OUT="$(cd "$OUT" && pwd)"

TLS_OPTS=()
case "$TLS_LIB" in
  openssl) TLS_OPTS=(BUILD_TLS=yes) ;;
  tongsuo) [[ -n "${TONGSUO_PREFIX:-}" ]] || { echo "TONGSUO_PREFIX is required for -t tongsuo" >&2; exit 2; }
           TLS_OPTS=(BUILD_TLS=yes "OPENSSL_PREFIX=$TONGSUO_PREFIX") ;;
  none)    TLS_OPTS=(BUILD_TLS=no) ;;
  *) echo "TLS_LIB must be openssl, tongsuo or none" >&2; exit 2 ;;
esac

WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT

# 静态 OpenSSL：拉取 → 校验提交哈希 → no-shared 编译到 $WORK/openssl，再让 Valkey 链接 .a
if [[ $STATIC -eq 1 ]]; then
  [[ "$TLS_LIB" == openssl ]] || { echo "-S only applies to -t openssl" >&2; exit 2; }
  # shellcheck disable=SC1091
  source "$HERE/DEPS"
  if [[ -z "${OPENSSL_SRC:-}" ]]; then
    OPENSSL_SRC="$WORK/openssl-src"
    git -c advice.detachedHead=false clone -q --depth 1 --branch "$OPENSSL_TAG" "$OPENSSL_GIT" "$OPENSSL_SRC"
    got="$(git -C "$OPENSSL_SRC" rev-parse HEAD)"
    [[ "$got" == "$OPENSSL_COMMIT" ]] || { echo "openssl commit mismatch: $got != $OPENSSL_COMMIT" >&2; exit 3; }
  fi
  OSSL="$WORK/openssl"
  (cd "$OPENSSL_SRC" && ./Configure --prefix="$OSSL" --libdir=lib --openssldir=/etc/pki/tls \
      no-shared no-tests no-docs no-apps -fPIC >"$WORK/openssl.log" 2>&1 \
    && make -j"$(nproc)" >>"$WORK/openssl.log" 2>&1 && make install_sw >>"$WORK/openssl.log" 2>&1) \
    || { tail -50 "$WORK/openssl.log" >&2; exit 4; }
  TLS_OPTS=(BUILD_TLS=yes "OPENSSL_PREFIX=$OSSL"
            "LIBSSL_LIBS=$OSSL/lib/libssl.a" "LIBCRYPTO_LIBS=$OSSL/lib/libcrypto.a -ldl -pthread")
  TLS_LIB="openssl-$OPENSSL_VERSION-static"
fi

if [[ -z "$SRC" ]]; then
  git -c advice.detachedHead=false clone -q --depth 1 --branch "$UPSTREAM_TAG" "$UPSTREAM_GIT" "$WORK/src"
  got="$(git -C "$WORK/src" rev-parse HEAD)"
  [[ "$got" == "$UPSTREAM_COMMIT" ]] || { echo "upstream commit mismatch: $got != $UPSTREAM_COMMIT" >&2; exit 3; }
else
  cp -a "$SRC" "$WORK/src"
fi
rm -rf "$WORK/src/.git"
cd "$WORK/src"
grep -q "#define VALKEY_VERSION \"$UPSTREAM_VERSION\"" src/version.h || { echo "source is not valkey $UPSTREAM_VERSION" >&2; exit 3; }

# 改名 patch 必须恰好一个；安全补丁（1xxx-cve-*.patch）按文件名顺序在其后应用
patch -p1 --forward --quiet < "$HERE/patches/0001-sycache-rename.patch"
for p in "$HERE"/patches/1*-cve-*.patch; do
  [[ -e "$p" ]] || continue
  patch -p1 --forward --quiet < "$p"
done
"$ROOT/scripts/rename-scan.sh" "$WORK/src"

# 把打完 patch 的源码树提交成一个固定作者与时间的本地 git 提交：
# 二进制中的 sha（--version、启动日志 commit=）因此唯一对应「上游 + 全部 patch」的源码树，且可复现；
# 否则上游的 mkreleasehdr.sh 在非 git 目录下会报 sha=00000000、modified=1。
export SOURCE_DATE_EPOCH=1767225600   # 2026-01-01T00:00:00Z，同时让 BUILD_ID 可复现
git init -q && git add -A
GIT_AUTHOR_DATE="@$SOURCE_DATE_EPOCH +0000" GIT_COMMITTER_DATE="@$SOURCE_DATE_EPOCH +0000" \
  git -c user.name=cacheos-build -c user.email=build@cacheos.invalid commit -qm "sycache $VERSION"
SRC_SHA="$(git rev-parse --short=8 HEAD)"

ARCH="$(uname -m)"
MAKE_OPTS=(
  ENGINE_NAME=sycache
  USE_REDIS_SYMLINKS=no
  MALLOC=jemalloc
  "USE_SYSTEMD=${USE_SYSTEMD:-auto}"
  "SERVER_CFLAGS=-DSYCACHE_VERSION=\\\"$VERSION\\\""
  "${TLS_OPTS[@]}"
)
make -j"$(nproc)" "${MAKE_OPTS[@]}" >"$WORK/build.log" 2>&1 || { tail -50 "$WORK/build.log" >&2; exit 4; }

if [[ $STATIC -eq 1 ]]; then
  for b in src/sycache-server src/sycache-cli src/sycache-benchmark; do
    if readelf -d "$b" | grep -E 'NEEDED.*lib(ssl|crypto)\.so' >/dev/null; then
      echo "$b still links OpenSSL dynamically" >&2; exit 5
    fi
  done
fi
if [[ -n "${MAX_GLIBC:-}" ]]; then
  for b in src/sycache-server src/sycache-cli src/sycache-benchmark; do
    need="$(objdump -T "$b" | grep -oE 'GLIBC_[0-9]+(\.[0-9]+)+' | sed 's/GLIBC_//' | sort -Vu | tail -1)"
    [[ "$(printf '%s\n%s\n' "$need" "$MAX_GLIBC" | sort -V | tail -1)" == "$MAX_GLIBC" ]] \
      || { echo "$b needs glibc $need > MAX_GLIBC $MAX_GLIBC" >&2; exit 5; }
  done
fi

# 安装到暂存根目录：/opt/sycache/{bin,conf,share,licenses}
STAGE="$WORK/stage"; P="$STAGE/opt/sycache"
make -C src install "${MAKE_OPTS[@]}" PREFIX="$P" >>"$WORK/build.log" 2>&1
# 剥离调试符号：调试信息单独输出 sycache-debuginfo 包，供崩溃分析，不进安装包
DBG="$WORK/debug/opt/sycache/lib/debug"; mkdir -p "$DBG"
for b in sycache-server sycache-cli sycache-benchmark; do
  objcopy --only-keep-debug "$P/bin/$b" "$DBG/$b.debug"
  strip --strip-unneeded "$P/bin/$b"
  objcopy --add-gnu-debuglink="$DBG/$b.debug" "$P/bin/$b"
done
mkdir -p "$P/conf" "$P/share" "$P/licenses" "$P/instances"
"$HERE/packaging/make-conf.sh" valkey.conf > "$P/conf/sycache.conf.default"
"$HERE/packaging/make-conf.sh" sentinel.conf > "$P/conf/sycache-sentinel.conf.default"
cp COPYING "$P/licenses/VALKEY-COPYING"
for d in deps/*/; do   # 随 Valkey 捆绑的第三方库许可证
  n="$(basename "$d")"; [[ "$n" == gtest-parallel ]] && continue   # 仅测试使用，不进二进制
  for f in "$d"COPYING* "$d"LICENSE* "$d"COPYRIGHT*; do
    [[ -f "$f" ]] && install -D -m 0644 "$f" "$P/licenses/deps/$n/$(basename "$f")"
  done
done
cp "$ROOT/docs/NOTICE" "$P/licenses/NOTICE"
install -D -m 0644 "$HERE/packaging/sycache@.service" "$STAGE/usr/lib/systemd/system/sycache@.service"
install -D -m 0644 "$HERE/packaging/sycache-sentinel@.service" "$STAGE/usr/lib/systemd/system/sycache-sentinel@.service"
install -D -m 0644 "$HERE/packaging/sycache-thp.service" "$STAGE/usr/lib/systemd/system/sycache-thp.service"
install -D -m 0644 "$HERE/packaging/90-sycache.conf" "$STAGE/usr/lib/sysctl.d/90-sycache.conf"
install -D -m 0644 "$HERE/packaging/sycache.logrotate" "$STAGE/etc/logrotate.d/sycache"

# BUILDINFO：真实上游版本与补丁清单，供 SBOM、VEX 与控制台「关于」页使用（规格书 12 节）
{
  echo "product=cacheos-sycache"
  echo "product_version=$VERSION"
  echo "upstream=valkey"
  echo "upstream_version=$UPSTREAM_VERSION"
  echo "upstream_commit=$UPSTREAM_COMMIT"
  echo "source_tree_sha=$SRC_SHA"
  echo "tls=$TLS_LIB"
  echo "arch=$ARCH"
  echo "glibc_required=$(objdump -T "$P/bin/sycache-server" | grep -oE 'GLIBC_[0-9]+(\.[0-9]+)+' | sed 's/GLIBC_//' | sort -Vu | tail -1)"
  echo "patches=$(cd "$HERE/patches" && ls ./*.patch | xargs -n1 basename | tr '\n' ' ' | sed 's/ $//')"
  echo "patch_sha256=$(cd "$HERE/patches" && cat ./*.patch | sha256sum | cut -d' ' -f1)"
} > "$P/share/BUILDINFO"

python3 "$ROOT/scripts/gen-sbom.py" "$WORK/src" "$P/share/BUILDINFO" "$P/share/sbom.spdx.json"

TAR="$OUT/sycache-$VERSION-$ARCH.tar.gz"
tar -C "$STAGE" --owner=0 --group=0 -czf "$TAR" .
tar -C "$WORK/debug" --owner=0 --group=0 -czf "$OUT/sycache-debuginfo-$VERSION-$ARCH.tar.gz" .
cp "$P/share/BUILDINFO" "$OUT/BUILDINFO"
cp "$P/share/sbom.spdx.json" "$OUT/sycache-$VERSION-$ARCH.spdx.json"
echo "built $TAR"

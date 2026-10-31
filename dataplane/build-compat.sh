#!/usr/bin/env bash
# 在 glibc 2.17 容器中做兼容构建（静态 OpenSSL LTS），并生成 RPM 与 DEB 包。
#
# 用法：dataplane/build-compat.sh [-o <输出目录>] [-s <上游源码目录>]
# 需要 docker；镜像由 dataplane/build-env/Dockerfile 构建（首次运行自动构建，按宿主架构选择基础镜像）。
# 产物：<输出目录>/sycache-<版本>-<架构>.tar.gz、sycache-debuginfo-*.tar.gz、*.rpm、*.deb、BUILDINFO
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/dist-compat"; SRC=""
while getopts "o:s:" opt; do
  case "$opt" in
    o) OUT="$OPTARG" ;;
    s) SRC="$OPTARG" ;;
    *) exit 2 ;;
  esac
done
mkdir -p "$OUT"; OUT="$(cd "$OUT" && pwd)"
ARCH="$(uname -m)"
IMAGE="cacheos-build:el7-$ARCH"

docker image inspect "$IMAGE" >/dev/null 2>&1 \
  || docker build -q -t "$IMAGE" --build-arg ARCH="$ARCH" "$ROOT/dataplane/build-env"

MOUNTS=(-v "$ROOT:/repo:ro" -v "$OUT:/out")
SRC_OPT=()
if [[ -n "$SRC" ]]; then
  MOUNTS+=(-v "$(cd "$SRC" && pwd):/upstream:ro"); SRC_OPT=(-s /upstream)
fi
docker run --rm "${MOUNTS[@]}" "$IMAGE" bash -euo pipefail -c "
  /repo/dataplane/build-sycache.sh -S ${SRC_OPT[*]} -o /out
  /repo/dataplane/packaging/build-rpm.sh /out/sycache-*-$ARCH.tar.gz /out
  chown -R $(id -u):$(id -g) /out
"
# DEB 在宿主机上打（dpkg-deb 不依赖 glibc 版本，包内二进制与 RPM 完全相同）
if command -v dpkg-deb >/dev/null; then
  "$ROOT/dataplane/packaging/build-deb.sh" "$(ls "$OUT"/sycache-[0-9]*-"$ARCH".tar.gz)" "$OUT"
else
  echo "dpkg-deb not found on host: skip DEB" >&2
fi
ls -l "$OUT"

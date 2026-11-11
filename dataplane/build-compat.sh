#!/usr/bin/env bash
# 在 glibc 2.17 容器中做兼容构建（静态 OpenSSL LTS），并生成 RPM 与 DEB 包。
#
# 用法：dataplane/build-compat.sh [-o <输出目录>] [-s <上游源码目录>]
# 需要 docker；默认使用 dataplane/build-env/Dockerfile 首次自动构建。
# 设置 CACHEOS_BUILD_IMAGE 时改为拉取预构建的兼容构建镜像（例如 GHCR 中的 el7 标签）。
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
case "$(uname -m)" in
  x86_64|amd64) ARCH=x86_64 ;;
  aarch64|arm64) ARCH=aarch64 ;;
  ppc64le) ARCH=ppc64le ;;
  s390x) ARCH=s390x ;;
  *) echo "unsupported host architecture: $(uname -m)" >&2; exit 2 ;;
esac
IMAGE="${CACHEOS_BUILD_IMAGE:-cacheos-build:el7-$ARCH}"

if [[ -n "${CACHEOS_BUILD_IMAGE:-}" ]]; then
  docker pull "$IMAGE"
elif ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  docker build -q -t "$IMAGE" --build-arg ARCH="$ARCH" "$ROOT/dataplane/build-env"
fi

MOUNTS=(-v "$ROOT:/repo:ro" -v "$OUT:/out")
BUILD_ARGS=(-S -o /out)
if [[ -n "$SRC" ]]; then
  MOUNTS+=(-v "$(cd "$SRC" && pwd):/upstream:ro")
  BUILD_ARGS+=(-s /upstream)
fi
docker run --rm "${MOUNTS[@]}" "$IMAGE" bash -euo pipefail -c "
  /repo/dataplane/build-sycache.sh ${BUILD_ARGS[*]}
  /repo/dataplane/packaging/build-rpm.sh /out/sycache-[0-9]*-$ARCH.tar.gz /out
  chown -R $(id -u):$(id -g) /out
"
# DEB 在宿主机上打（dpkg-deb 不依赖 glibc 版本，包内二进制与 RPM 完全相同）
if command -v dpkg-deb >/dev/null; then
  "$ROOT/dataplane/packaging/build-deb.sh" "$(ls "$OUT"/sycache-[0-9]*-"$ARCH".tar.gz)" "$OUT"
else
  echo "dpkg-deb not found on host: skip DEB" >&2
fi
ls -l "$OUT"

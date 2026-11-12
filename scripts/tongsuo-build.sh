#!/usr/bin/env bash
# P6 铜锁变体构建入口。
#
# 该脚本只负责把已准备好的 Tongsuo 工具链接入统一数据面构建流程；
# 认证、国密算法互操作和目标 CPU/OS 适配仍需在对应实机完成。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${CACHEOS_TONGSUO_OUT:-$ROOT/dist-tongsuo}"
SRC="${UPSTREAM_SRC:-}"

: "${TONGSUO_PREFIX:?TONGSUO_PREFIX must point to the Tongsuo installation prefix}"
[[ -x "$TONGSUO_PREFIX/bin/openssl" ]] || {
  echo "missing Tongsuo executable: $TONGSUO_PREFIX/bin/openssl" >&2
  exit 2
}

args=(-o "$OUT" -t tongsuo)
if [[ -n "$SRC" ]]; then
  args+=(-s "$SRC")
fi
"$ROOT/dataplane/build-sycache.sh" "${args[@]}"

grep -qx 'tls=tongsuo' "$OUT/BUILDINFO" || {
  echo "BUILDINFO does not identify a Tongsuo build" >&2
  exit 1
}
package="$(find "$OUT" -maxdepth 1 -type f -name 'sycache-[0-9]*-*.tar.gz' ! -name '*debuginfo*' -print -quit)"
[[ -n "$package" ]] || { echo "missing Tongsuo release tarball" >&2; exit 1; }
for binary in sycache-server sycache-cli sycache-benchmark; do
  tar -tzf "$package" "./home/atwasoft/sycache/bin/$binary" >/dev/null
done

echo "Tongsuo build verified: $OUT"

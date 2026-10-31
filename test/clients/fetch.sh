#!/usr/bin/env bash
# 按 clients.lock 拉取客户端库源码到缓存目录（默认 ~/.cache/cacheos-clients），已存在且哈希一致则跳过。
# 用法：test/clients/fetch.sh [缓存目录]   输出缓存目录的绝对路径
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
CACHE="${1:-${CLIENTS_CACHE:-$HOME/.cache/cacheos-clients}}"
mkdir -p "$CACHE"
grep -vE '^\s*(#|$)' "$HERE/clients.lock" | while read -r name url tag commit; do
  dir="$CACHE/$name"
  if [[ -d "$dir/.git" && "$(git -C "$dir" rev-parse HEAD)" == "$commit" ]]; then continue; fi
  rm -rf "$dir"
  if [[ "$tag" == "-" ]]; then
    git clone -q "$url" "$dir" && git -C "$dir" -c advice.detachedHead=false checkout -q "$commit"
  else
    git -c advice.detachedHead=false clone -q --depth 1 --branch "$tag" "$url" "$dir"
  fi
  got="$(git -C "$dir" rev-parse HEAD)"
  [[ "$got" == "$commit" ]] || { echo "$name: commit mismatch $got != $commit" >&2; exit 3; }
done
cd "$CACHE" && pwd

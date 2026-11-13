#!/usr/bin/env bash
# 对已检出的模块源码应用 dataplane/modules/patches/<模块>/*.patch（ADR 0002）。
# 用法：apply-patches.sh <模块名> <源码目录> [patch 根目录]
# - 按文件名排序逐个 `patch -p1 --forward`；任何一个应用失败都以非零退出（并还原已改动的文件不在本脚本职责内：
#   调用方应丢弃该源码目录），不允许“部分应用后继续构建”。
# - 源码目录里写入 .cacheos-patches（patch 内容的 sha256）。再次调用时：标记一致则跳过（源码被缓存复用），
#   标记不一致则失败（源码已按另一组 patch 改过，须删除缓存目录重新下载）。
# - 没有该模块的 patch 目录或目录里没有 .patch 文件时什么也不做。
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
name="${1:?用法: apply-patches.sh <模块名> <源码目录> [patch 根目录]}"
src="${2:?缺少源码目录}"
root="${3:-$HERE/patches}"
[[ "$name" =~ ^[a-z]+$ ]] || { echo "ERROR: 非法模块名: $name" >&2; exit 2; }
[[ -d "$src" ]] || { echo "ERROR: 源码目录不存在: $src" >&2; exit 2; }

patches=()
if [[ -d "$root/$name" ]]; then
  while IFS= read -r p; do patches+=("$p"); done < <(find "$root/$name" -maxdepth 1 -name '*.patch' -type f | LC_ALL=C sort)
fi
[[ ${#patches[@]} -gt 0 ]] || exit 0

sum="$(cat "${patches[@]}" | sha256sum | cut -d' ' -f1)"
marker="$src/.cacheos-patches"
if [[ -f "$marker" ]]; then
  if [[ "$(cat "$marker")" == "$sum" ]]; then
    echo "✓ $name 的 patch 已应用过（$sum），跳过"
    exit 0
  fi
  echo "ERROR: $src 已按另一组 patch 修改过（标记 $(cat "$marker")，当前 $sum），请删除该源码目录后重新下载" >&2
  exit 1
fi

for p in "${patches[@]}"; do
  # 先干跑，失败时不改动任何文件
  if ! patch -p1 --forward --dry-run -s -d "$src" < "$p" >/dev/null 2>&1; then
    echo "ERROR: patch 无法应用到 $name 源码: $p（上游内容与 patch 预期不符，请重新生成 patch）" >&2
    exit 1
  fi
  patch -p1 --forward -s -d "$src" < "$p" || { echo "ERROR: 应用 patch 失败: $p" >&2; exit 1; }
  echo "  已应用 $(basename "$p")"
done
echo "$sum" > "$marker"
echo "✓ $name：已应用 ${#patches[@]} 个 patch"

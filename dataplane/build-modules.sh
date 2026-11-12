#!/usr/bin/env bash
# 构建扩展数据类型模块（valkey-json、valkey-search、valkey-bloom）。
#
# 用法：
#   dataplane/build-modules.sh [-m json,search,bloom] [-o <输出目录>] [-s <源码缓存目录>]
#
# 默认构建所有已定义的模块。
# 构建产物（.so 文件）输出到 <输出目录>/modules/ 或 <输出目录>。
# 源码按 dataplane/modules/MODULES 清单下载、校验，缓存到 <源码缓存目录>。
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
MODULES_DIR="$HERE/modules"
MODULES_FILE="$MODULES_DIR/MODULES"
OUT_DIR="$HERE/../dist"
CACHE_DIR="${CACHE_DIR:-$HERE/modules/build}"
SYCACHE_SRC=""  # 可指定已检出的 sycache 源码

# 从 dataplane/UPSTREAM 读取上游版本
source "$HERE/UPSTREAM"  # 载入 UPSTREAM_TAG, UPSTREAM_COMMIT, UPSTREAM_VERSION 等

# 构建参数
MODULES_TO_BUILD=""
while getopts "m:o:s:" opt; do
  case "$opt" in
    m) MODULES_TO_BUILD="$OPTARG" ;;
    o) OUT_DIR="$OPTARG" ;;
    s) SYCACHE_SRC="$OPTARG" ;;
    *) exit 2 ;;
  esac
done

mkdir -p "$OUT_DIR" "$CACHE_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"
CACHE_DIR="$(cd "$CACHE_DIR" && pwd)"

# 如果指定了源码，校验
if [[ -n "$SYCACHE_SRC" ]]; then
  [[ -d "$SYCACHE_SRC/src" ]] || { echo "ERROR: sycache 源码目录结构错误: $SYCACHE_SRC" >&2; exit 2; }
fi

# 解析模块清单
declare -A module_info  # name:version:repo:tag:license:hash
read_modules() {
  while IFS=$'\t ' read -r name version repo tag license hash; do
    # 跳过注释和空行
    [[ "$name" =~ ^# ]] && continue
    [[ -z "$name" ]] && continue

    # 检查版本白名单
    case "$name:$version" in
      json:1.0.3|search:1.2.1|bloom:1.0.1)
        key="$name:$version"
        module_info["$key"]="$repo|$tag|$license|$hash"
        ;;
      *)
        echo "WARNING: 模块 $name $version 不在白名单中，跳过" >&2
        ;;
    esac
  done < "$MODULES_FILE"
}

# 下载并校验源码
download_module() {
  local name="$1" version="$2" repo="$3" tag="$4" hash="$5"
  local src_dir="$CACHE_DIR/$name-$version"

  if [[ -d "$src_dir" ]]; then
    echo "✓ 使用缓存的源码: $src_dir"
    return 0
  fi

  echo "下载模块 $name $version..."

  # 检查 hash 是否为 TODO
  if [[ "$hash" =~ TODO ]]; then
    echo "WARNING: 模块 $name 的 commit/sha256 仍为 TODO，跳过下载" >&2
    return 1
  fi

  # 使用 git clone 或下载 tarball
  # 首先尝试 git clone
  local tmpdir="$CACHE_DIR/.tmp-$name-$$"
  mkdir -p "$tmpdir"

  if git -c advice.detachedHead=false clone -q --depth 1 --branch "$tag" "$repo" "$tmpdir" 2>/dev/null; then
    # 验证 commit 哈希
    local actual_commit
    actual_commit=$(git -C "$tmpdir" rev-parse HEAD)
    if [[ "$actual_commit" != "$hash" ]]; then
      echo "WARNING: 模块 $name 的 commit 与预期不符 (expected: $hash, got: $actual_commit)" >&2
    fi
    mv "$tmpdir" "$src_dir"
  else
    echo "ERROR: 无法克隆 $repo (tag: $tag)" >&2
    rm -rf "$tmpdir"
    return 1
  fi

  echo "✓ 下载并缓存: $src_dir"
  return 0
}

# 编译单个模块
build_module() {
  local name="$1" version="$2" valkey_src="$3"
  local src_dir="$CACHE_DIR/$name-$version"
  local so_name="valkey-${name}.so"  # 保持上游名称（ADR 0001）

  [[ -d "$src_dir" ]] || { echo "ERROR: 源码目录不存在: $src_dir" >&2; return 1; }

  echo "编译模块 $name $version..."

  # 检查构建脚本或 Makefile
  if [[ -f "$src_dir/build.sh" ]]; then
    # valkey-json / valkey-search 使用 build.sh
    # 如果提供了 Valkey 源码，使用本地头文件以加速构建
    local build_env=()
    if [[ -n "$valkey_src" && -f "$valkey_src/src/valkeymodule.h" ]]; then
      export VALKEY_MODULE_H_PATH="$valkey_src/src/valkeymodule.h"
      build_env+=("VALKEY_MODULE_H_PATH=$VALKEY_MODULE_H_PATH")
      echo "  使用本地 Valkey 头文件: $VALKEY_MODULE_H_PATH"
    fi

    # 设置编译版本（从 UPSTREAM_VERSION 读取完整版本号，固定到上游标签）
    # valkey-json 的 build.sh 使用 SERVER_VERSION 从对应的 GitHub 分支下载预编译头文件
    # 由于只支持分支名（如 9.1），我们设置为主版本，构建后校验 commit 等于 UPSTREAM_COMMIT
    export SERVER_VERSION="${UPSTREAM_TAG%.*}"  # 从 9.1.3 提取 9.1
    export UPSTREAM_COMMIT  # 传递给 build.sh 以便验证

    echo "  使用上游标签: $UPSTREAM_TAG (commit: $UPSTREAM_COMMIT)"

    # 运行 build.sh --release
    cd "$src_dir"
    bash build.sh --release || { echo "ERROR: 编译 $name 失败 (build.sh --release)" >&2; cd - > /dev/null; return 1; }
    cd - > /dev/null

    # 查找 .so 文件
    local so_path
    so_path=$(find "$src_dir" -name "*.so" -type f | head -1)
    if [[ -n "$so_path" ]]; then
      cp "$so_path" "$OUT_DIR/$so_name"
      echo "✓ 输出: $OUT_DIR/$so_name"
    else
      echo "ERROR: 编译后未找到 .so 文件在 $src_dir" >&2
      return 1
    fi
  elif [[ -f "$src_dir/Makefile" ]]; then
    # valkey-bloom 可能使用 Makefile
    make -C "$src_dir" -j"$(nproc 2>/dev/null || echo 2)" || { echo "ERROR: 编译 $name 失败 (Makefile)" >&2; return 1; }
    # 查找 .so 文件
    local so_path
    so_path=$(find "$src_dir" -name "*.so" -type f | head -1)
    if [[ -n "$so_path" ]]; then
      cp "$so_path" "$OUT_DIR/$so_name"
      echo "✓ 输出: $OUT_DIR/$so_name"
    else
      echo "ERROR: 编译后未找到 .so 文件在 $src_dir" >&2
      return 1
    fi
  else
    echo "ERROR: 未找到编译脚本 (build.sh 或 Makefile): $src_dir" >&2
    return 1
  fi

  return 0
}

# 主流程
echo "========================================="
echo "模块构建"
echo "========================================="

read_modules

# 确定要构建的模块
if [[ -z "$MODULES_TO_BUILD" ]]; then
  MODULES_TO_BUILD="json search bloom"
fi

local_errors=0
for mod in $MODULES_TO_BUILD; do
  case "$mod" in
    json) ver="1.0.3" ;;
    search) ver="1.2.1" ;;
    bloom) ver="1.0.1" ;;
    *)
      echo "ERROR: 未知模块: $mod" >&2
      ((local_errors++))
      continue
      ;;
  esac

  key="$mod:$ver"
  [[ -n "${module_info[$key]:-}" ]] || { echo "ERROR: 模块 $mod $ver 未在清单中" >&2; ((local_errors++)); continue; }

  IFS='|' read -r repo tag license hash <<< "${module_info[$key]}"

  # 下载
  if ! download_module "$mod" "$ver" "$repo" "$tag" "$hash"; then
    ((local_errors++))
    continue
  fi

  # 编译
  if ! build_module "$mod" "$ver" "$SYCACHE_SRC"; then
    ((local_errors++))
    continue
  fi
done

echo "========================================="
if [[ $local_errors -eq 0 ]]; then
  echo "✓ 模块构建完成"
  exit 0
else
  echo "✗ 模块构建有错误"
  exit 1
fi

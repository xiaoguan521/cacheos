#!/usr/bin/env bash
# 模块清单与版本配置检查（ADR 0002）。
# 用法：dataplane/modules/check.sh
# 检查项：
#   1. MODULES 文件格式（6 列：名称、版本、仓库、标签、许可、commit/sha256）
#   2. 许可字段只允许 MIT / BSD-3-Clause / Apache-2.0
#   3. 版本与 ADR 0002 白名单一致（json 1.0.3、search 1.2.1、bloom 1.0.1）
#   4. dataplane/versions.yaml 中存在 modules 章节且与清单一致
#   5. 不得有 TODO 项（commit/sha256 必须真实）
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
MODULES_FILE="$HERE/MODULES"
VERSIONS_FILE="$(dirname "$HERE")/versions.yaml"

# 合法版本白名单（ADR 0002）
ALLOWED_VERSIONS=(
  "json:1.0.3"
  "search:1.2.1"
  "bloom:1.0.1"
)

# 合法许可
ALLOWED_LICENSES=("BSD-3-Clause" "MIT" "Apache-2.0")

check_modules_file() {
  echo "[1/5] 检查 MODULES 文件格式..."
  [[ -f "$MODULES_FILE" ]] || { echo "ERROR: $MODULES_FILE 不存在"; return 1; }

  local lineno=0 errors=0
  while IFS= read -r line; do
    ((lineno++))
    # 跳过注释和空行
    [[ "$line" =~ ^# ]] && continue
    [[ -z "$line" ]] && continue

    # 解析 6 列
    IFS=$'\t ' read -r name version repo tag license hash <<< "$line"
    if [[ -z "$name" || -z "$version" || -z "$repo" || -z "$tag" || -z "$license" || -z "$hash" ]]; then
      echo "ERROR (line $lineno): 列数不足或字段缺失: $line"
      ((errors++))
      continue
    fi

    # 检查许可
    local valid_license=0
    for allowed in "${ALLOWED_LICENSES[@]}"; do
      if [[ "$license" == "$allowed" ]]; then
        valid_license=1
        break
      fi
    done
    if [[ $valid_license -eq 0 ]]; then
      echo "ERROR (line $lineno): 许可 '$license' 不在白名单中: $line"
      ((errors++))
      continue
    fi

    # 检查版本
    local valid_version=0
    for allowed in "${ALLOWED_VERSIONS[@]}"; do
      if [[ "$name:$version" == "$allowed" ]]; then
        valid_version=1
        break
      fi
    done
    if [[ $valid_version -eq 0 ]]; then
      echo "ERROR (line $lineno): 模块 '$name' 版本 '$version' 不在白名单中"
      ((errors++))
      continue
    fi

    # 检查 hash 不是 TODO
    if [[ "$hash" =~ TODO ]]; then
      echo "ERROR (line $lineno): commit/sha256 仍为 TODO: $line"
      ((errors++))
      continue
    fi
  done < "$MODULES_FILE"

  [[ $errors -eq 0 ]] && echo "✓ MODULES 文件格式正确" && return 0 || return 1
}

check_versions_yaml() {
  echo "[2/5] 检查 versions.yaml 中 modules 章节..."
  [[ -f "$VERSIONS_FILE" ]] || { echo "ERROR: $VERSIONS_FILE 不存在"; return 1; }

  if ! grep -q "modules:" "$VERSIONS_FILE"; then
    echo "ERROR: versions.yaml 不含 modules 章节"
    return 1
  fi

  echo "✓ versions.yaml 含 modules 章节"
  return 0
}

check_version_consistency() {
  echo "[3/5] 检查清单与 versions.yaml 一致性..."
  # 简单检查：版本号在两个文件中出现
  local errors=0
  for version_spec in "${ALLOWED_VERSIONS[@]}"; do
    IFS=':' read -r name version <<< "$version_spec"
    # 在 MODULES 中检查
    if ! grep -q "^$name[[:space:]].*$version" "$MODULES_FILE"; then
      echo "ERROR: MODULES 中缺少或版本不匹配: $name $version"
      ((errors++))
    fi
    # 在 versions.yaml 中检查
    if ! grep -q "$version" "$VERSIONS_FILE"; then
      echo "ERROR: versions.yaml 中缺少版本号: $version"
      ((errors++))
    fi
  done
  [[ $errors -eq 0 ]] && echo "✓ 清单与 versions.yaml 一致" && return 0 || return 1
}

check_no_todos() {
  echo "[4/5] 检查是否还有 TODO 标记..."
  if grep -q "TODO" "$MODULES_FILE"; then
    echo "ERROR: MODULES 文件中仍含 TODO 标记（需要真实的 commit/sha256）"
    return 1
  fi
  echo "✓ 无 TODO 标记"
  return 0
}

check_all_modules_defined() {
  echo "[5/5] 检查所有白名单模块都已定义..."
  local errors=0
  for version_spec in "${ALLOWED_VERSIONS[@]}"; do
    IFS=':' read -r name version <<< "$version_spec"
    if ! grep -q "^$name[[:space:]]$version" "$MODULES_FILE"; then
      echo "ERROR: 白名单模块 $name $version 未在 MODULES 中定义"
      ((errors++))
    fi
  done
  [[ $errors -eq 0 ]] && echo "✓ 所有白名单模块都已定义" && return 0 || return 1
}

# 执行所有检查
echo "========================================="
echo "模块清单与版本检查"
echo "========================================="

all_pass=0
check_modules_file || all_pass=1
check_versions_yaml || all_pass=1
check_version_consistency || all_pass=1
check_no_todos || all_pass=1
check_all_modules_defined || all_pass=1

echo "========================================="
if [[ $all_pass -eq 0 ]]; then
  echo "✓ 所有检查通过"
  exit 0
else
  echo "✗ 检查失败"
  exit 1
fi

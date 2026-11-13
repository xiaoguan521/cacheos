#!/usr/bin/env bash
# install-modules.sh 的测试：用假的模块产物验证安装布局、权限与失败条件。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT="$ROOT/dataplane/packaging/install-modules.sh"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
fail() { echo "  ✗ $*" >&2; exit 1; }

mkmod() { # <dist> <name> [skip-license|skip-sbom]
  local d="$1" n="$2"
  mkdir -p "$d/licenses/$n/third_party/lib"
  echo so >"$d/valkey-$n.so"
  echo lic >"$d/licenses/$n/LICENSE"; echo lic2 >"$d/licenses/$n/third_party/lib/LICENSE"
  echo '{}' >"$d/sbom-$n.spdx.json"
  [[ "${3:-}" == skip-license ]] && rm -rf "$d/licenses/$n"
  [[ "${3:-}" == skip-sbom ]] && rm -f "$d/sbom-$n.spdx.json"
  return 0
}

# 1. 正常安装：.so 改名并设为 0755，许可文本与 SBOM 到位，输出 BUILDINFO 用的 modules 行
mkmod "$T/dist" json; mkmod "$T/dist" bloom
P="$T/stage/home/atwasoft/sycache"; mkdir -p "$P"
line="$("$SCRIPT" "$T/dist" "$P" json bloom)"
[[ -f "$P/modules/json.so" && -f "$P/modules/bloom.so" ]] || fail "模块文件未安装到 modules/<名称>.so"
[[ "$(stat -c %a "$P/modules/json.so" 2>/dev/null || stat -f %Lp "$P/modules/json.so")" == 755 ]] || fail "模块文件应为 0755"
[[ -f "$P/licenses/modules/json/LICENSE" && -f "$P/licenses/modules/json/third_party/lib/LICENSE" ]] || fail "许可文本未安装"
[[ -f "$P/share/modules/json.spdx.json" ]] || fail "SBOM 未安装"
[[ "$line" == "json bloom" ]] || fail "输出的模块列表应为 'json bloom'，实际: $line"
echo "  ✓ 正常安装"

# 2. 缺 .so / 缺许可文本 / 缺 SBOM / 未知模块名 都必须失败，且不留下半成品
for bad in nosor skip-license skip-sbom; do
  rm -rf "$T/d2" "$T/s2"; mkdir -p "$T/s2"
  mkmod "$T/d2" json "$bad"; [[ "$bad" == nosor ]] && rm -f "$T/d2/valkey-json.so"
  if "$SCRIPT" "$T/d2" "$T/s2" json >/dev/null 2>&1; then fail "$bad：应失败"; fi
  [[ ! -e "$T/s2/modules/json.so" ]] || fail "$bad：失败后不应留下模块文件"
done
if "$SCRIPT" "$T/dist" "$T/s2" graph >/dev/null 2>&1; then fail "未知模块名应失败"; fi
if "$SCRIPT" "$T/dist" "$T/s2" '../x' >/dev/null 2>&1; then fail "带路径的模块名应失败"; fi
echo "  ✓ 缺文件与非法名称都失败"

# 3. 不带模块参数时什么都不装，输出为空
rm -rf "$T/s3"; mkdir -p "$T/s3"
[[ -z "$("$SCRIPT" "$T/dist" "$T/s3")" && ! -e "$T/s3/modules" ]] || fail "无模块时不应安装任何东西"
echo "  ✓ 无模块不安装"
echo "✓ install-modules 测试通过"

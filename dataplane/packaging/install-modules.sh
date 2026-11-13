#!/usr/bin/env bash
# 把扩展数据类型模块装进安装包暂存根目录（ADR 0002）。
#
# 用法：install-modules.sh <模块产物目录> <暂存的 sycache 根目录> [模块名...]
#   模块产物目录：build-modules.sh 与 collect-licenses.py 的输出（valkey-<名>.so、licenses/<名>/、sbom-<名>.spdx.json）
#   暂存根目录：  <STAGE>/home/atwasoft/sycache
#
# 布局（随产品版本，签名的升级包同样带上它们）：
#   modules/<名>.so                 实例经 <Root>/current/modules/<名>.so 加载（文件名是给人看的部分，模块 ABI 与命令名不变）
#   licenses/modules/<名>/…         上游与第三方许可文本
#   share/modules/<名>.spdx.json    模块 SBOM
# 先全部校验再安装：任何一个模块缺 .so、许可文本或 SBOM、或名称不在白名单，整体失败，不留半成品。
# 标准输出只打印已安装的模块名（空格分隔），供 BUILDINFO 使用；日志走标准错误。
set -euo pipefail

DIST="${1:?usage: install-modules.sh <modules-dist> <stage-sycache-root> [module...]}"
P="${2:?stage root}"; shift 2
ALLOWED=" json search bloom "
OBJDUMP="${OBJDUMP:-objdump}"; STRIP="${STRIP:-strip}"

# 符号版本上限：设置 MAX_GLIBC（发布构建为 2.17）时，模块需要的 glibc 与 libstdc++ 符号版本不得更高，
# 否则承诺的老发行版（CentOS 7、麒麟 V10、统信 UOS、openEuler）上加载不了。MAX_GLIBCXX 默认 3.4.19（devtoolset 之前的系统库）。
max_ver() { # <so> <GLIBC|GLIBCXX>
  "$OBJDUMP" -T "$1" 2>/dev/null | grep -oE "$2_[0-9]+(\.[0-9]+)+" | sed "s/$2_//" | sort -Vu | tail -1
}
over() { # <found> <max>：found 为空或不高于 max 返回 1
  [[ -n "$1" && "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -1)" != "$2" ]]
}

for n in "$@"; do
  [[ "$ALLOWED" == *" $n "* ]] || { echo "ERROR: 模块 $n 不在白名单" >&2; exit 2; }
  [[ -f "$DIST/valkey-$n.so" ]] || { echo "ERROR: 缺少 $DIST/valkey-$n.so" >&2; exit 2; }
  [[ -d "$DIST/licenses/$n" && -n "$(find "$DIST/licenses/$n" -type f -print -quit)" ]] \
    || { echo "ERROR: 模块 $n 没有许可文本（先运行 collect-licenses.py）" >&2; exit 2; }
  [[ -f "$DIST/sbom-$n.spdx.json" ]] || { echo "ERROR: 模块 $n 没有 SBOM" >&2; exit 2; }
  if [[ -n "${MAX_GLIBC:-}" ]]; then
    g="$(max_ver "$DIST/valkey-$n.so" GLIBC)"; x="$(max_ver "$DIST/valkey-$n.so" GLIBCXX)"
    if over "$g" "$MAX_GLIBC"; then
      echo "ERROR: 模块 $n 需要 glibc $g，高于上限 $MAX_GLIBC；须在兼容构建环境（glibc 2.17）里构建模块" >&2; exit 5
    fi
    if over "$x" "${MAX_GLIBCXX:-3.4.19}"; then
      echo "ERROR: 模块 $n 需要 GLIBCXX_$x，高于上限 ${MAX_GLIBCXX:-3.4.19}；须静态链接 libstdc++ 或在兼容环境里构建" >&2; exit 5
    fi
  fi
done

out=()
for n in "$@"; do
  install -d -m 0755 "$P/modules" "$P/licenses/modules" "$P/share/modules"
  install -m 0755 "$DIST/valkey-$n.so" "$P/modules/$n.so"
  # 剥离符号：search 带符号约 147 MB，剥离后约 21 MB；与 sycache-server 一样只对 ELF 做
  if file -b "$P/modules/$n.so" | grep -q 'ELF'; then
    "$STRIP" --strip-unneeded "$P/modules/$n.so" || { echo "ERROR: 剥离 $n.so 失败" >&2; exit 5; }
  fi
  rm -rf "$P/licenses/modules/$n"
  cp -R "$DIST/licenses/$n" "$P/licenses/modules/$n"
  install -m 0644 "$DIST/sbom-$n.spdx.json" "$P/share/modules/$n.spdx.json"
  echo "  ✓ 模块 $n 已装入安装包暂存目录" >&2
  out+=("$n")
done
echo "${out[*]:-}"

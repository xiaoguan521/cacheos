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

for n in "$@"; do
  [[ "$ALLOWED" == *" $n "* ]] || { echo "ERROR: 模块 $n 不在白名单" >&2; exit 2; }
  [[ -f "$DIST/valkey-$n.so" ]] || { echo "ERROR: 缺少 $DIST/valkey-$n.so" >&2; exit 2; }
  [[ -d "$DIST/licenses/$n" && -n "$(find "$DIST/licenses/$n" -type f -print -quit)" ]] \
    || { echo "ERROR: 模块 $n 没有许可文本（先运行 collect-licenses.py）" >&2; exit 2; }
  [[ -f "$DIST/sbom-$n.spdx.json" ]] || { echo "ERROR: 模块 $n 没有 SBOM" >&2; exit 2; }
done

out=()
for n in "$@"; do
  install -d -m 0755 "$P/modules" "$P/licenses/modules" "$P/share/modules"
  install -m 0755 "$DIST/valkey-$n.so" "$P/modules/$n.so"
  rm -rf "$P/licenses/modules/$n"
  cp -R "$DIST/licenses/$n" "$P/licenses/modules/$n"
  install -m 0644 "$DIST/sbom-$n.spdx.json" "$P/share/modules/$n.spdx.json"
  echo "  ✓ 模块 $n 已装入安装包暂存目录" >&2
  out+=("$n")
done
echo "${out[*]:-}"

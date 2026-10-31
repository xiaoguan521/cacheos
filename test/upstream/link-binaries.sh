#!/usr/bin/env bash
# 上游 Tcl 测试集按 src/valkey-* 调用程序。sycache 依据可执行文件名判断运行模式
# （sycache-sentinel / sycache-check-rdb / sycache-check-aof），软链会把 argv[0] 变成 valkey-*，
# 所以这里生成转调脚本，用 exec -a 以 sycache-* 的名字启动对应程序。
set -euo pipefail
SRC="${1:?usage: link-binaries.sh <src-dir>}"
cd "$SRC"
for name in server cli benchmark sentinel check-rdb check-aof; do
  target="sycache-$name"
  [[ -e "$target" ]] || { echo "missing $SRC/$target" >&2; exit 1; }
  printf '#!/usr/bin/env bash\nexec -a "%s/%s" "%s/%s" "$@"\n' "$PWD" "$target" "$PWD" "$target" > "valkey-$name"
  chmod +x "valkey-$name"
done
echo "linked upstream test names to sycache binaries in $SRC"

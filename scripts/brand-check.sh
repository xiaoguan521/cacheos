#!/usr/bin/env bash
# 品牌门禁：产品代码、界面文案、打包与部署文件中不得出现 valkey / redis 产品名。
# 允许：
#   - 「兼容 Redis 协议」类表述与客户端库名（Jedis、redis-py、go-redis 等），统一写作 "Redis-compatible" / "兼容 Redis"；
#   - 关于页与 NOTICE 中的归因句 "based on Valkey (BSD-3-Clause)"。
# 不检查：dataplane/rename、dataplane/patches、dataplane/UPSTREAM、构建与测试脚本（需要引用上游名）、docs/。
set -euo pipefail
cd "$(dirname "$0")/.."
SCOPE=()
for d in cmd internal web/src deploy api/proto \
         dataplane/packaging/sycache@.service dataplane/packaging/sycache-sentinel@.service \
         dataplane/packaging/90-sycache.conf dataplane/packaging/sycache.logrotate \
         dataplane/packaging/sycache-thp.service dataplane/packaging/scripts; do
  [[ -e "$d" ]] && SCOPE+=("$d")
done
hits=$(grep -rniI 'valkey' "${SCOPE[@]}" 2>/dev/null \
  | grep -v 'Valkey (BSD-3-Clause)' || true)
if [[ -n "$hits" ]]; then
  echo "brand-check FAILED: 'valkey' found in product scope:"
  echo "$hits"
  exit 1
fi
echo "brand-check OK (${#SCOPE[@]} paths)"

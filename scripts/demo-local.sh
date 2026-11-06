#!/usr/bin/env bash
# P4 单机数据面演示：同一台 Linux 主机启动 3 主 3 从集群与 3 个哨兵。
#
# 这是不依赖控制台的现场数据面演示脚本，覆盖集群创建、客户端写入、
# 原生故障切换和哨兵探活。控制台的参数变更、滚动升级与 License 超额
# 拦截仍由 web/e2e/ui-acceptance.cjs 在已初始化控制台上验收。
#
# 用法：
#   SYCACHE_BIN=/opt/sycache/bin scripts/demo-local.sh
#   KEEP=1 KILL_MASTER=0 BASE_PORT=17001 scripts/demo-local.sh
set -euo pipefail

BIN_DIR="${SYCACHE_BIN:-/opt/sycache/bin}"
BASE_PORT="${BASE_PORT:-17001}"
KEEP="${KEEP:-0}"
KILL_MASTER="${KILL_MASTER:-1}"
RUN="$(mktemp -d -t cacheos-demo.XXXXXX)"

[[ "$BASE_PORT" =~ ^[0-9]+$ ]] && (( BASE_PORT >= 1024 && BASE_PORT <= 60000 )) || {
  echo "BASE_PORT must be between 1024 and 60000" >&2
  exit 2
}
for binary in sycache-server sycache-sentinel sycache-cli; do
  [[ -x "$BIN_DIR/$binary" ]] || { echo "missing executable: $BIN_DIR/$binary" >&2; exit 2; }
done

DATA_PORTS=()
SENTINEL_PORTS=()
for n in 0 1 2 3 4 5; do DATA_PORTS+=("$((BASE_PORT + n))"); done
for n in 0 1 2; do SENTINEL_PORTS+=("$((BASE_PORT + 100 + n))"); done

cleanup() {
  [[ "$KEEP" == 1 ]] && return
  for port in "${SENTINEL_PORTS[@]}" "${DATA_PORTS[@]}"; do
    "$BIN_DIR/sycache-cli" -h 127.0.0.1 -p "$port" shutdown nosave >/dev/null 2>&1 || true
  done
  rm -rf "$RUN"
}
trap cleanup EXIT

wait_ping() {
  local port="$1"
  for _ in $(seq 1 80); do
    if "$BIN_DIR/sycache-cli" -h 127.0.0.1 -p "$port" ping >/dev/null 2>&1; then return 0; fi
    sleep 0.1
  done
  echo "port $port did not become ready" >&2
  return 1
}

echo "== CacheOS P4 local demo"
echo "run directory: $RUN"

for port in "${DATA_PORTS[@]}"; do
  dir="$RUN/$port"
  mkdir -p "$dir"
  "$BIN_DIR/sycache-server" \
    --bind 127.0.0.1 --port "$port" --daemonize yes \
    --dir "$dir" --dbfilename dump.rdb --appendonly no --save "" \
    --pidfile "$dir/server.pid" --logfile "$dir/server.log" \
    --cluster-enabled yes --cluster-config-file "$dir/nodes.conf" \
    --cluster-node-timeout 2000 --cluster-announce-ip 127.0.0.1
  wait_ping "$port"
done

nodes=()
for port in "${DATA_PORTS[@]}"; do nodes+=("127.0.0.1:$port"); done
"$BIN_DIR/sycache-cli" --cluster create "${nodes[@]}" --cluster-replicas 1 --cluster-yes

for n in 0 1 2; do
  port="${SENTINEL_PORTS[$n]}"
  dir="$RUN/$port"
  mkdir -p "$dir"
  cat > "$dir/sentinel.conf" <<EOF
bind 127.0.0.1
protected-mode yes
port $port
dir $dir
sentinel monitor cacheos-demo 127.0.0.1 ${DATA_PORTS[0]} 2
sentinel down-after-milliseconds cacheos-demo 1000
sentinel failover-timeout cacheos-demo 5000
sentinel parallel-syncs cacheos-demo 1
EOF
  "$BIN_DIR/sycache-sentinel" "$dir/sentinel.conf" --daemonize yes \
    --pidfile "$dir/sentinel.pid" --logfile "$dir/sentinel.log"
  wait_ping "$port"
done

"$BIN_DIR/sycache-cli" -c -h 127.0.0.1 -p "${DATA_PORTS[0]}" set demo:before-failover "$(date -u +%FT%TZ)"
cluster_info="$("$BIN_DIR/sycache-cli" -h 127.0.0.1 -p "${DATA_PORTS[1]}" cluster info)"
# Some builds emit RESP text with CRLF even on Unix; match the state field prefix.
grep -q '^cluster_state:ok' <<<"$cluster_info" || { echo "cluster is not healthy" >&2; exit 1; }
echo "cluster: 3 masters + 3 replicas, state=ok"

for port in "${SENTINEL_PORTS[@]}"; do
  "$BIN_DIR/sycache-cli" -h 127.0.0.1 -p "$port" sentinel get-master-addr-by-name cacheos-demo >/dev/null
done
echo "sentinel: 3 monitors responding"

if [[ "$KILL_MASTER" == 1 ]]; then
  master_pid_file="$RUN/${DATA_PORTS[0]}/server.pid"
  master_pid="$(<"$master_pid_file")"
  [[ "$master_pid" =~ ^[0-9]+$ ]] || { echo "invalid master pid" >&2; exit 1; }
  kill -TERM "$master_pid"

  new_master=""
  for _ in $(seq 1 80); do
    nodes_view="$("$BIN_DIR/sycache-cli" -h 127.0.0.1 -p "${DATA_PORTS[1]}" cluster nodes 2>/dev/null || true)"
    new_master="$(awk '$3 ~ /master/ && $9 ~ /^0(-|$)/ && $8 !~ /fail/ { split($2,a,":"); split(a[2],b,"@"); print b[1]; exit }' <<<"$nodes_view")"
    [[ -n "$new_master" && "$new_master" != "${DATA_PORTS[0]}" ]] && break
    sleep 0.25
  done
  [[ -n "$new_master" && "$new_master" != "${DATA_PORTS[0]}" ]] || {
    echo "cluster did not promote a new owner for slot 0" >&2
    exit 1
  }
  "$BIN_DIR/sycache-cli" -c -h 127.0.0.1 -p "$new_master" get demo:before-failover >/dev/null
  echo "failover: slot 0 promoted to port $new_master"
fi

echo "P4 data-plane demo OK"
if [[ "$KEEP" == 1 ]]; then
  echo "processes kept; cleanup manually under $RUN"
fi

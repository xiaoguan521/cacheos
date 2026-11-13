#!/usr/bin/env bash
# valkey-search 1.2.1 模块烟雾测试（ADR 0002）。
#
# 用法：SEARCH_SO=<path> test/modules/search-smoke.sh <sycache-server-binary>
#
# 严格模式：任何一项不符合预期都直接失败，不降级为警告。
#   1. 加载模块
#   2. FT.CREATE（VECTOR HNSW + TAG）、HSET、FT.SEARCH KNN 的最近邻顺序、TAG 过滤的混合查询
#   3. FT.INFO 看到索引
#   4. 主从复制：从实例也加载模块，主上新增文档后，从上 KNN 能查到
#   5. RDB 往返：BGSAVE → 重启 → 索引与数据仍可 KNN 查询
#
# 命令通过内嵌 python3 的最小 RESP 客户端发送，以便传递二进制向量；
# 参数写成 @vec:a,b,c,d 会被转换为 float32 小端字节串。
set -euo pipefail

SYCACHE_BIN="${1:-}"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
SEARCH_SO="${SEARCH_SO:-$ROOT/dataplane/modules/dist/valkey-search.so}"

TMPDIR_TEST=$(mktemp -d)
PORT_MASTER=7381
PORT_REPLICA=7382
PIDS=()

cleanup() {
  for pid in "${PIDS[@]:-}"; do
    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
  done
  sleep 1
  rm -rf "$TMPDIR_TEST"
}
trap cleanup EXIT

fail() {
  echo "  ✗ $*" >&2
  for f in "$TMPDIR_TEST"/*.log; do
    [[ -f "$f" ]] && { echo "--- $(basename "$f") (末尾 40 行) ---"; grep -v "Defined Info Field" "$f" | tail -60 | cut -c1-300; }
  done
  exit 1
}

# vcmd <port> <cmd...>：发送一条命令，每个回复元素一行输出；错误回复以 ERR: 开头
vcmd() {
  python3 -I - "$@" <<'PY'
import socket, struct, sys
port = int(sys.argv[1]); args = sys.argv[2:]
def enc(a):
    if a.startswith("@vec:"):
        vals = [float(x) for x in a[5:].split(",")]
        return struct.pack("<%df" % len(vals), *vals)
    return a.encode()
s = socket.create_connection(("127.0.0.1", port), timeout=10)
payload = b"*%d\r\n" % len(args)
for a in args:
    b = enc(a)
    payload += b"$%d\r\n" % len(b) + b + b"\r\n"
s.sendall(payload)
f = s.makefile("rb")
out = []
def read():
    line = f.readline().rstrip(b"\r\n")
    t, rest = line[:1], line[1:]
    if t in (b"+", b":"):
        out.append(rest.decode())
    elif t == b"-":
        out.append("ERR:" + rest.decode())
    elif t == b"$":
        n = int(rest)
        if n < 0:
            out.append("(nil)")
        else:
            data = f.read(n + 2)[:n]
            out.append(data.decode("latin1") if all(32 <= c < 127 or c in (9, 10, 13) for c in data) else "<bin>")
    elif t == b"*":
        for _ in range(max(int(rest), 0)):
            read()
    elif t in (b"%", b"~"):
        for _ in range(int(rest) * (2 if t == b"%" else 1)):
            read()
    elif t == b"_":
        out.append("(nil)")
    else:
        out.append("?:" + line.decode("latin1"))
read()
print("\n".join(out))
PY
}

wait_ready() {
  local port="$1" i
  for i in $(seq 1 60); do
    if [[ "$(vcmd "$port" PING 2>/dev/null || true)" == "PONG" ]]; then return 0; fi
    sleep 0.5
  done
  return 1
}

start_instance() { # <port> <name> [extra args...]
  local port="$1" name="$2"; shift 2
  mkdir -p "$TMPDIR_TEST/$name"
  "$SYCACHE_BIN" --port "$port" --dir "$TMPDIR_TEST/$name" --dbfilename dump.rdb \
    --loadmodule "$SEARCH_SO" --save "" --daemonize no "$@" \
    >"$TMPDIR_TEST/$name.log" 2>&1 &
  PIDS+=("$!")
  if ! wait_ready "$port"; then
    echo "--- $name.log ---"; tail -50 "$TMPDIR_TEST/$name.log"
    fail "$name 启动或模块加载失败"
  fi
}

# 轮询 KNN 查询，直到输出包含期望的 key（索引回填是异步的）
knn_expect() { # <port> <query> <expected-first-key> [vec]
  local port="$1" query="$2" want="$3" vec="${4:-0.9,0.1,0,0}" out i first
  for i in $(seq 1 40); do
    out=$(vcmd "$port" FT.SEARCH idx "$query" PARAMS 2 q "@vec:$vec" DIALECT 2 2>&1 || true)
    first=$(printf '%s\n' "$out" | grep -m1 '^doc:' || true)
    if [[ "$first" == "$want" ]]; then return 0; fi
    sleep 0.5
  done
  echo "--- 最后一次输出 ---"; printf '%s\n' "$out"
  return 1
}

echo "========================================="
echo "valkey-search 1.2.1 烟雾测试"
echo "========================================="

echo "[1/6] 检查依赖..."
[[ -x "$SYCACHE_BIN" ]] || fail "sycache-server 不可执行: $SYCACHE_BIN"
[[ -f "$SEARCH_SO" ]] || fail "模块不存在: $SEARCH_SO"
command -v python3 >/dev/null || fail "需要 python3"
echo "  ✓ 依赖检查通过"

echo "[2/6] 启动主实例并确认模块已加载..."
start_instance "$PORT_MASTER" master
mods=$(vcmd "$PORT_MASTER" MODULE LIST)
grep -qi 'search' <<<"$mods" || fail "MODULE LIST 中没有 search: $mods"
echo "  ✓ 模块已加载"

echo "[3/6] FT.CREATE、写入、KNN 与混合查询..."
res=$(vcmd "$PORT_MASTER" FT.CREATE idx ON HASH PREFIX 1 doc: SCHEMA \
  embedding VECTOR HNSW 6 TYPE FLOAT32 DIM 4 DISTANCE_METRIC L2 category TAG)
[[ "$res" == "OK" ]] || fail "FT.CREATE 失败: $res"
vcmd "$PORT_MASTER" HSET doc:1 embedding @vec:1,0,0,0 category A >/dev/null
vcmd "$PORT_MASTER" HSET doc:2 embedding @vec:0,1,0,0 category B >/dev/null
vcmd "$PORT_MASTER" HSET doc:3 embedding @vec:0,0,1,0 category A >/dev/null
knn_expect "$PORT_MASTER" '*=>[KNN 2 @embedding $q]' doc:1 || fail "KNN 最近邻不是 doc:1"
echo "  ✓ KNN 最近邻正确（doc:1）"
out=$(vcmd "$PORT_MASTER" FT.SEARCH idx '*=>[KNN 2 @embedding $q]' PARAMS 2 q @vec:0.9,0.1,0,0 DIALECT 2)
second=$(printf '%s\n' "$out" | grep '^doc:' | sed -n 2p)
[[ "$second" == "doc:2" ]] || fail "KNN 第二近邻应为 doc:2，实际: $second"
echo "  ✓ KNN 第二近邻正确（doc:2）"
out=$(vcmd "$PORT_MASTER" FT.SEARCH idx '(@category:{A})=>[KNN 2 @embedding $q]' PARAMS 2 q @vec:0.9,0.1,0,0 DIALECT 2)
grep -q '^doc:1$' <<<"$out" || fail "TAG 过滤结果缺少 doc:1: $out"
if grep -q '^doc:2$' <<<"$out"; then fail "TAG=A 的混合查询不应返回 doc:2: $out"; fi
echo "  ✓ TAG 过滤的混合查询正确"

echo "[4/6] FT.INFO..."
info=$(vcmd "$PORT_MASTER" FT.INFO idx)
grep -q 'idx' <<<"$info" || fail "FT.INFO 没有索引 idx: $info"
echo "  ✓ FT.INFO 返回索引信息"

echo "[5/6] 主从复制..."
start_instance "$PORT_REPLICA" replica
res=$(vcmd "$PORT_REPLICA" REPLICAOF 127.0.0.1 "$PORT_MASTER")
[[ "$res" == "OK" ]] || fail "REPLICAOF 失败: $res"
linked=0
for i in $(seq 1 120); do
  if vcmd "$PORT_REPLICA" INFO replication | grep -q 'master_link_status:up'; then linked=1; break; fi
  sleep 0.5
done
if [[ $linked -ne 1 ]]; then
  echo "--- 从实例 INFO replication / persistence ---"
  vcmd "$PORT_REPLICA" INFO replication | tr -d '\r' || true
  vcmd "$PORT_REPLICA" INFO persistence | tr -d '\r' | grep -E '^(loading|async_loading|rdb_)' || true
  fail "从实例未能与主实例建立链接"
fi
echo "  ✓ 复制链接已建立"
vcmd "$PORT_MASTER" HSET doc:4 embedding @vec:0,0,0,1 category B >/dev/null
knn_expect "$PORT_REPLICA" '*=>[KNN 1 @embedding $q]' doc:4 "0,0,0,0.9" \
  || fail "从实例上 KNN 未能查到主上新增的 doc:4（索引或数据未复制）"
echo "  ✓ 从实例 KNN 查到主上新增的文档"

echo "[6/6] RDB 往返..."
vcmd "$PORT_MASTER" BGSAVE >/dev/null
for i in $(seq 1 60); do
  if vcmd "$PORT_MASTER" INFO persistence | grep -q 'rdb_bgsave_in_progress:0'; then break; fi
  sleep 0.5
done
[[ -s "$TMPDIR_TEST/master/dump.rdb" ]] || fail "RDB 文件未生成"
vcmd "$PORT_MASTER" SHUTDOWN NOSAVE >/dev/null 2>&1 || true
sleep 1
start_instance "$PORT_MASTER" master
info=$(vcmd "$PORT_MASTER" FT.INFO idx)
grep -q 'idx' <<<"$info" || fail "重启后索引 idx 未恢复: $info"
knn_expect "$PORT_MASTER" '*=>[KNN 2 @embedding $q]' doc:1 \
  || fail "重启后 KNN 查询结果不正确"
echo "  ✓ 重启后索引与数据恢复，KNN 正确"

echo "========================================="
echo "✓ 所有测试通过"

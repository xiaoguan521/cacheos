#!/usr/bin/env bash
# valkey-bloom 1.0.1 模块烟雾测试（ADR 0002）。
#
# 用法：BLOOM_SO=<path> test/modules/bloom-smoke.sh <sycache-server-binary>
#
# 严格模式：任何一项不符合预期都直接失败。
#   1. 加载模块
#   2. BF.RESERVE / BF.ADD / BF.EXISTS / BF.MADD / BF.MEXISTS / BF.CARD / BF.INFO，已加入元素无假阴性，假阳性率在界内
#   3. 主从复制：从实例也加载模块，主上添加的元素在从上可查到
#   4. RDB 往返：BGSAVE → 重启 → 过滤器与元素仍在
#
# 命令通过内嵌 python3 的最小 RESP 客户端发送。
set -euo pipefail

SYCACHE_BIN="${1:-}"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
BLOOM_SO="${BLOOM_SO:-$ROOT/dataplane/modules/dist/valkey-bloom.so}"

TMPDIR_TEST=$(mktemp -d)
PORT_MASTER=7391
PORT_REPLICA=7392
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
    --loadmodule "$BLOOM_SO" --save "" --daemonize no "$@" \
    >"$TMPDIR_TEST/$name.log" 2>&1 &
  PIDS+=("$!")
  if ! wait_ready "$port"; then
    echo "--- $name.log ---"; tail -50 "$TMPDIR_TEST/$name.log"
    fail "$name 启动或模块加载失败"
  fi
}

echo "========================================="
echo "valkey-bloom 1.0.1 烟雾测试"
echo "========================================="

echo "[1/5] 检查依赖..."
[[ -x "$SYCACHE_BIN" ]] || fail "sycache-server 不可执行: $SYCACHE_BIN"
[[ -f "$BLOOM_SO" ]] || fail "模块不存在: $BLOOM_SO"
command -v python3 >/dev/null || fail "需要 python3"
echo "  ✓ 依赖检查通过"

echo "[2/5] 启动主实例并确认模块已加载..."
start_instance "$PORT_MASTER" master
mods=$(vcmd "$PORT_MASTER" MODULE LIST)
grep -qi 'bf' <<<"$mods" || fail "MODULE LIST 中没有 bloom 模块: $mods"
echo "  ✓ 模块已加载"

echo "[3/5] 基础命令..."
res=$(vcmd "$PORT_MASTER" BF.RESERVE bf 0.01 1000)
[[ "$res" == "OK" ]] || fail "BF.RESERVE 失败: $res"
[[ "$(vcmd "$PORT_MASTER" BF.ADD bf first)" == "1" ]] || fail "BF.ADD 新元素应返回 1"
[[ "$(vcmd "$PORT_MASTER" BF.ADD bf first)" == "0" ]] || fail "BF.ADD 重复元素应返回 0"
[[ "$(vcmd "$PORT_MASTER" BF.EXISTS bf first)" == "1" ]] || fail "BF.EXISTS 已加入元素应为 1"
echo "  ✓ BF.RESERVE / BF.ADD / BF.EXISTS"
added=(); for i in $(seq 1 200); do added+=("in-$i"); done
res=$(vcmd "$PORT_MASTER" BF.MADD bf "${added[@]}")
[[ "$(grep -c '^1$' <<<"$res")" == "200" ]] || fail "BF.MADD 200 个新元素应全部返回 1"
res=$(vcmd "$PORT_MASTER" BF.MEXISTS bf "${added[@]}")
[[ "$(grep -c '^1$' <<<"$res")" == "200" ]] || fail "已加入元素出现假阴性"
echo "  ✓ BF.MADD / BF.MEXISTS：200 个元素无假阴性"
absent=(); for i in $(seq 1 200); do absent+=("out-$i"); done
res=$(vcmd "$PORT_MASTER" BF.MEXISTS bf "${absent[@]}")
fp=$(grep -c '^1$' <<<"$res" || true)
[[ "$fp" -le 10 ]] || fail "假阳性过多: 200 个未加入元素中 $fp 个命中（误差率 0.01，容忍上限 10）"
echo "  ✓ 假阳性 $fp/200（界内）"
card=$(vcmd "$PORT_MASTER" BF.CARD bf)
[[ "$card" == "201" ]] || fail "BF.CARD 应为 201，实际: $card"
info=$(vcmd "$PORT_MASTER" BF.INFO bf)
grep -q '^1000$' <<<"$info" || fail "BF.INFO 中没有容量 1000: $info"
echo "  ✓ BF.CARD=201，BF.INFO 容量正确"

echo "[4/5] 主从复制..."
start_instance "$PORT_REPLICA" replica
res=$(vcmd "$PORT_REPLICA" REPLICAOF 127.0.0.1 "$PORT_MASTER")
[[ "$res" == "OK" ]] || fail "REPLICAOF 失败: $res"
linked=0
for i in $(seq 1 120); do
  if vcmd "$PORT_REPLICA" INFO replication | grep -q 'master_link_status:up'; then linked=1; break; fi
  sleep 0.5
done
if [[ $linked -ne 1 ]]; then
  vcmd "$PORT_REPLICA" INFO replication | tr -d '\r' || true
  fail "从实例未能与主实例建立链接"
fi
echo "  ✓ 复制链接已建立"
[[ "$(vcmd "$PORT_MASTER" BF.ADD bf after-sync)" == "1" ]] || fail "主上 BF.ADD 失败"
ok=0
for i in $(seq 1 40); do
  if [[ "$(vcmd "$PORT_REPLICA" BF.EXISTS bf after-sync)" == "1" ]]; then ok=1; break; fi
  sleep 0.5
done
[[ $ok -eq 1 ]] || fail "从实例查不到主上新增的元素"
[[ "$(vcmd "$PORT_REPLICA" BF.CARD bf)" == "202" ]] || fail "从实例 BF.CARD 应为 202"
echo "  ✓ 从实例元素与基数与主实例一致"

echo "[5/5] RDB 往返..."
vcmd "$PORT_MASTER" BGSAVE >/dev/null
for i in $(seq 1 60); do
  if vcmd "$PORT_MASTER" INFO persistence | grep -q 'rdb_bgsave_in_progress:0'; then break; fi
  sleep 0.5
done
[[ -s "$TMPDIR_TEST/master/dump.rdb" ]] || fail "RDB 文件未生成"
vcmd "$PORT_MASTER" SHUTDOWN NOSAVE >/dev/null 2>&1 || true
sleep 1
start_instance "$PORT_MASTER" master
[[ "$(vcmd "$PORT_MASTER" BF.EXISTS bf after-sync)" == "1" ]] || fail "重启后元素丢失"
res=$(vcmd "$PORT_MASTER" BF.MEXISTS bf "${added[@]}")
[[ "$(grep -c '^1$' <<<"$res")" == "200" ]] || fail "重启后出现假阴性"
[[ "$(vcmd "$PORT_MASTER" BF.CARD bf)" == "202" ]] || fail "重启后 BF.CARD 不是 202"
echo "  ✓ 重启后过滤器与元素完整"

echo "========================================="
echo "✓ 所有测试通过"

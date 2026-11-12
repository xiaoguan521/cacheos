#!/usr/bin/env bash
# valkey-json 1.0.3 模块烟雾测试（ADR 0002）。
#
# 用法：test/modules/json-smoke.sh [<sycache-server-binary>]
#
# 测试项：
#   1. 启动 sycache-server 并加载 valkey-json.so 模块
#   2. 基础功能：JSON.SET、JSON.GET、JSON.DEL
#   3. JSONPath 查询：JSON.GET with path
#   4. RDB 往返：BGSAVE → 重启 → 数据完整性
#   5. 主从复制一致性（可选，需要两个实例）
#
# 需要 redis-cli 或 valkey-cli，及 sycache-server 二进制。
set -euo pipefail

SYCACHE_BIN="${1:-}"
if [[ -z "$SYCACHE_BIN" ]]; then
  # 尝试常见的位置
  for path in /home/atwasoft/sycache/bin/sycache-server /usr/local/bin/sycache-server; do
    if [[ -x "$path" ]]; then
      SYCACHE_BIN="$path"
      break
    fi
  done
fi

SYCACHE_CLI=""
if [[ -n "$SYCACHE_BIN" ]]; then
  SYCACHE_CLI="${SYCACHE_BIN%sycache-server}/redis-cli"
  if [[ ! -x "$SYCACHE_CLI" ]]; then
    SYCACHE_CLI="${SYCACHE_BIN%sycache-server}/sycache-cli"
  fi
fi
# 备用：直接用 redis-cli
if [[ -z "$SYCACHE_CLI" ]] || [[ ! -x "$SYCACHE_CLI" ]]; then
  SYCACHE_CLI="redis-cli"
fi

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
MODULES_DIR="$ROOT/dataplane/modules"
JSON_SO="${JSON_SO:-$MODULES_DIR/dist/valkey-json.so}"

# 临时工作目录
TMPDIR_TEST=$(mktemp -d)
trap "rm -rf '$TMPDIR_TEST'" EXIT

PORT_MASTER=7379
PORT_SLAVE=7380
RDB_FILE="$TMPDIR_TEST/dump.rdb"
LOG_FILE="$TMPDIR_TEST/server.log"

# 检查依赖
check_deps() {
  echo "[1/6] 检查依赖..."

  if [[ ! -f "$SYCACHE_BIN" ]]; then
    echo "✗ sycache-server 不存在: $SYCACHE_BIN"
    return 1
  fi

  if [[ ! -f "$JSON_SO" ]]; then
    echo "✗ valkey-json.so 不存在: $JSON_SO"
    echo "  请先运行: dataplane/build-modules.sh"
    return 1
  fi

  if ! command -v "$SYCACHE_CLI" >/dev/null 2>&1 && ! command -v redis-cli >/dev/null 2>&1; then
    echo "✗ 未找到 redis-cli 或 valkey-cli"
    return 1
  fi

  echo "✓ 依赖检查通过"
  return 0
}

# 启动主实例
start_master() {
  echo "[2/6] 启动主实例 (端口 $PORT_MASTER)..."

  "$SYCACHE_BIN" \
    --port "$PORT_MASTER" \
    --loadmodule "$JSON_SO" \
    --dir "$TMPDIR_TEST" \
    --dbfilename "dump.rdb" \
    --logfile "$LOG_FILE" \
    --loglevel notice \
    --daemonize no \
    --save "" \
    > "$TMPDIR_TEST/master.log" 2>&1 &

  local pid=$!
  sleep 1  # 等待启动

  if ! kill -0 $pid 2>/dev/null; then
    echo "✗ sycache-server 启动失败 (PID: $pid)"
    cat "$TMPDIR_TEST/master.log"
    return 1
  fi

  echo "✓ 主实例启动成功 (PID: $pid)"
  echo "$pid" > "$TMPDIR_TEST/master.pid"
  return 0
}

# 停止实例
stop_instance() {
  local pid_file="$1"
  if [[ -f "$pid_file" ]]; then
    local pid=$(cat "$pid_file")
    kill $pid 2>/dev/null || true
    sleep 1
  fi
}

# 基础功能测试
test_basic_commands() {
  echo "[3/6] 测试基础命令..."

  # JSON.SET
  result=$("$SYCACHE_CLI" -p "$PORT_MASTER" JSON.SET mykey . '{"name":"John","age":30}' 2>&1)
  if [[ ! "$result" =~ "OK" ]]; then
    echo "✗ JSON.SET 失败: $result"
    return 1
  fi
  echo "  ✓ JSON.SET 成功"

  # JSON.GET
  result=$("$SYCACHE_CLI" -p "$PORT_MASTER" JSON.GET mykey . 2>&1)
  if [[ ! "$result" =~ "John" ]]; then
    echo "✗ JSON.GET 失败: $result"
    return 1
  fi
  echo "  ✓ JSON.GET 成功"

  # JSON.GET 带 JSONPath
  result=$("$SYCACHE_CLI" -p "$PORT_MASTER" JSON.GET mykey $.name 2>&1)
  if [[ ! "$result" =~ "John" ]]; then
    echo "✗ JSON.GET with JSONPath 失败: $result"
    return 1
  fi
  echo "  ✓ JSON.GET with JSONPath 成功"

  # JSON.DEL
  result=$("$SYCACHE_CLI" -p "$PORT_MASTER" JSON.DEL mykey $.age 2>&1)
  if [[ ! "$result" =~ "1" ]]; then
    echo "✗ JSON.DEL 失败: $result"
    return 1
  fi
  echo "  ✓ JSON.DEL 成功"

  return 0
}

# RDB 往返测试
test_rdb_roundtrip() {
  echo "[4/6] 测试 RDB 往返..."

  # 写入数据
  "$SYCACHE_CLI" -p "$PORT_MASTER" JSON.SET key1 . '{"data":"test"}' > /dev/null

  # 触发 BGSAVE
  "$SYCACHE_CLI" -p "$PORT_MASTER" BGSAVE > /dev/null
  sleep 2  # 等待 RDB 保存

  # 验证 RDB 文件存在
  if [[ ! -f "$RDB_FILE" ]]; then
    echo "✗ RDB 文件未生成: $RDB_FILE"
    return 1
  fi
  echo "  ✓ RDB 文件生成成功"

  # 关闭主实例
  stop_instance "$TMPDIR_TEST/master.pid"
  sleep 1

  # 重启主实例（应加载 RDB 中的数据）
  "$SYCACHE_BIN" \
    --port "$PORT_MASTER" \
    --loadmodule "$JSON_SO" \
    --dir "$TMPDIR_TEST" \
    --dbfilename "dump.rdb" \
    --logfile "$LOG_FILE" \
    > "$TMPDIR_TEST/master-restart.log" 2>&1 &

  local pid=$!
  sleep 2

  if ! kill -0 $pid 2>/dev/null; then
    echo "✗ 重启 sycache-server 失败"
    return 1
  fi
  echo "$pid" > "$TMPDIR_TEST/master.pid"

  # 验证数据恢复
  result=$("$SYCACHE_CLI" -p "$PORT_MASTER" JSON.GET key1 . 2>&1)
  if [[ ! "$result" =~ "test" ]]; then
    echo "✗ RDB 数据恢复失败: $result"
    return 1
  fi
  echo "  ✓ RDB 数据恢复成功"

  return 0
}

# 模块加载验证
test_module_loaded() {
  echo "[5/6] 验证模块加载..."

  result=$("$SYCACHE_CLI" -p "$PORT_MASTER" INFO modules 2>&1)
  if [[ ! "$result" =~ "json" ]]; then
    echo "✗ 模块未在 INFO modules 中: $result"
    return 1
  fi
  echo "  ✓ 模块已正确加载"

  return 0
}

# 清理
cleanup() {
  echo "[6/6] 清理..."
  stop_instance "$TMPDIR_TEST/master.pid"
  echo "✓ 清理完成"
}

# 主流程
echo "========================================="
echo "valkey-json 1.0.3 烟雾测试"
echo "========================================="

all_pass=0
check_deps || all_pass=1
[[ $all_pass -eq 0 ]] && start_master || all_pass=1
[[ $all_pass -eq 0 ]] && test_module_loaded || all_pass=1
[[ $all_pass -eq 0 ]] && test_basic_commands || all_pass=1
[[ $all_pass -eq 0 ]] && test_rdb_roundtrip || all_pass=1
cleanup

echo "========================================="
if [[ $all_pass -eq 0 ]]; then
  echo "✓ 所有测试通过"
  exit 0
else
  echo "✗ 测试失败"
  exit 1
fi

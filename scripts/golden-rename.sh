#!/usr/bin/env bash
# 改名黄金验收（规格书 5.6）：在真实运行环境中断言用户看得到的地方都是 sycache，且协议兼容字段保持上游值。
#
# 用法：scripts/golden-rename.sh <sycache-<版本>-<架构>.tar.gz> [基准端口，默认 17001]
# 不需要 root：把安装包解压到临时目录，用非特权端口启动单机实例与哨兵。
set -euo pipefail
PKG="${1:?usage: golden-rename.sh <package.tar.gz> [base-port]}"
PORT="${2:-17001}"; SPORT=$((PORT + 1))
ROOT_DIR="$(mktemp -d)"; RUN="$ROOT_DIR/run"; mkdir -p "$RUN"
tar -C "$ROOT_DIR" -xzf "$PKG"
P="$ROOT_DIR/opt/sycache"; B="$P/bin"
# shellcheck disable=SC1091
source <(sed 's/^/BI_/' "$P/share/BUILDINFO")
VER="$BI_product_version"

cleanup() {
  "$B/sycache-cli" -p "$PORT" shutdown nosave >/dev/null 2>&1 || true
  "$B/sycache-cli" -p "$SPORT" shutdown nosave >/dev/null 2>&1 || true
  rm -rf "$ROOT_DIR"
}
trap cleanup EXIT

fails=0
ok()   { echo "  ok   $1"; }
fail() { echo "  FAIL $1"; fails=$((fails + 1)); }
expect_eq()   { [[ "$2" == "$3" ]] && ok "$1" || fail "$1: got '$2', want '$3'"; }
expect_has()  { grep -qF -- "$3" <<<"$2" && ok "$1" || fail "$1: missing '$3'"; }
# 不得出现作为单词的 valkey / Valkey / VALKEY（标识符如 valkeyConnect 不算）
no_brand()    { if grep -qE '(^|[^A-Za-z0-9_])[Vv]alkey([^A-Za-z0-9_]|$)|(^|[^A-Za-z0-9_])VALKEY([^A-Za-z0-9_]|$)' <<<"$2"; then
                  fail "$1: contains valkey: $(grep -oE '.{0,30}[Vv][Aa][Ll][Kk][Ee][Yy].{0,30}' <<<"$2" | head -3 | tr '\n' '|')"
                else ok "$1"; fi; }

echo "== 安装包内容"
expect_eq "bin 目录只有 6 个 sycache 程序" "$(ls "$B" | tr '\n' ' ')" \
  "sycache-benchmark sycache-check-aof sycache-check-rdb sycache-cli sycache-sentinel sycache-server "
no_brand "安装包文件名" "$(cd "$ROOT_DIR" && find . | grep -v '/licenses/')"
no_brand "默认配置模板" "$(cat "$P"/conf/*.default)"

echo "== 二进制字符串"
# 允许：协议 / 格式字面量（RDB 魔数 VALKEY、valkey-ver、Lua 的 VALKEY_VERSION、ldb 命令 valkey）、
# 断言中的源文件名（*.c）、C 标识符（valkeyConnect、ValkeyModule_* 等，不是单词边界匹配）
for b in sycache-server sycache-cli sycache-benchmark; do
  left="$(strings "$B/$b" | grep -E '(^|[^A-Za-z0-9_])([Vv]alkey|VALKEY)([^A-Za-z0-9_]|$)' \
    | grep -vxE 'VALKEY|valkey-ver|VALKEY_VERSION(_NUM)?|valkey' \
    | grep -vE '^[A-Za-z0-9_/.-]*valkey[A-Za-z0-9_-]*\.c$' || true)"
  [[ -z "$left" ]] && ok "strings $b 只含白名单" || fail "strings $b: $(head -3 <<<"$left" | tr '\n' '|')"
done

echo "== 命令行输出"
v="$("$B/sycache-server" --version)"
expect_has "sycache-server --version" "$v" "SyCache server v=$VER "
expect_has "--version 中的源码树 sha" "$v" "sha=$BI_source_tree_sha:0 "
expect_eq "sycache-cli --version" "$("$B/sycache-cli" --version)" "sycache-cli $VER (git:$BI_source_tree_sha)"
expect_eq "sycache-benchmark --version" "$("$B/sycache-benchmark" --version)" "sycache-benchmark $VER (git:$BI_source_tree_sha)"
expect_has "sycache-check-aof --version" "$("$B/sycache-check-aof" --version)" "sycache-check-aof v=$VER "
no_brand "sycache-server --help" "$("$B/sycache-server" --help 2>&1)"
no_brand "sycache-cli --help" "$("$B/sycache-cli" --help 2>&1)"
no_brand "sycache-benchmark --help" "$("$B/sycache-benchmark" --help 2>&1 || true)"

echo "== 单机实例"
"$B/sycache-server" --port "$PORT" --daemonize yes --dir "$RUN" --logfile "$RUN/server.log" \
  --pidfile "$RUN/server.pid" --save "" --appendonly no
for _ in $(seq 50); do "$B/sycache-cli" -p "$PORT" ping >/dev/null 2>&1 && break; sleep 0.1; done
pid="$(cat "$RUN/server.pid")"
expect_eq "进程名 comm" "$(cat /proc/"$pid"/comm)" "sycache-server"
expect_has "进程标题" "$(tr '\0' ' ' </proc/"$pid"/cmdline)" "sycache-server *:$PORT"
info="$("$B/sycache-cli" -p "$PORT" info server | tr -d '\r')"
expect_has "INFO server_name" "$info" "server_name:sycache"
expect_has "INFO sycache_version 为产品版本" "$info" "sycache_version:$VER"
expect_has "INFO sycache_engine_version 为上游版本" "$info" "sycache_engine_version:$BI_upstream_version"
expect_has "INFO redis_version 保持兼容值" "$info" "redis_version:7.2.4"
no_brand "INFO all" "$("$B/sycache-cli" -p "$PORT" info all | grep -v '^executable:')"
hello="$("$B/sycache-cli" -p "$PORT" -3 hello 3)"
expect_has "HELLO server" "$hello" "server sycache"
expect_has "HELLO version 保持上游值（客户端特性判断）" "$hello" "version $BI_upstream_version"
lua="$("$B/sycache-cli" -p "$PORT" eval 'return {server.SERVER_NAME, server.VALKEY_VERSION, server.REDIS_VERSION}' 0 | tr '\n' ' ')"
expect_eq "Lua 版本变量" "$lua" "sycache $BI_upstream_version 7.2.4 "
no_brand "COMMAND DOCS" "$("$B/sycache-cli" -p "$PORT" command docs)"
no_brand "LATENCY DOCTOR" "$("$B/sycache-cli" -p "$PORT" latency doctor)"
no_brand "MEMORY DOCTOR" "$("$B/sycache-cli" -p "$PORT" memory doctor)"
no_brand "错误回复" "$("$B/sycache-cli" -p "$PORT" nosuchcommand 2>&1)"
# RDB 文件格式保持上游：魔数与 valkey-ver 字段不变，迁移工具与上游可互读
"$B/sycache-cli" -p "$PORT" set golden 1 >/dev/null
"$B/sycache-cli" -p "$PORT" save >/dev/null
expect_has "RDB 魔数保持上游" "$(head -c 9 "$RUN/dump.rdb")" "VALKEY"
expect_has "sycache-check-rdb 可校验" "$(cd "$RUN" && "$B/sycache-check-rdb" dump.rdb 2>&1)" "RDB looks OK"
expect_has "启动日志" "$(cat "$RUN/server.log")" "SyCache version=$VER"
no_brand "服务日志" "$(cat "$RUN/server.log")"

echo "== 哨兵"
printf 'port %s\ndir %s\nsentinel monitor golden 127.0.0.1 %s 1\n' "$SPORT" "$RUN" "$PORT" > "$RUN/sentinel.conf"
"$B/sycache-sentinel" "$RUN/sentinel.conf" --daemonize yes --logfile "$RUN/sentinel.log" --pidfile "$RUN/sentinel.pid"
for _ in $(seq 50); do "$B/sycache-cli" -p "$SPORT" ping >/dev/null 2>&1 && break; sleep 0.1; done
spid="$(cat "$RUN/sentinel.pid")"
expect_has "哨兵进程标题" "$(tr '\0' ' ' </proc/"$spid"/cmdline)" "[sentinel]"
expect_has "哨兵模式" "$("$B/sycache-cli" -p "$SPORT" info server | tr -d '\r')" "server_mode:sentinel"
no_brand "哨兵日志" "$(cat "$RUN/sentinel.log")"

echo
if [[ $fails -gt 0 ]]; then echo "golden-rename FAILED ($fails)"; exit 1; fi
echo "golden-rename OK"

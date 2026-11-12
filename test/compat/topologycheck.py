#!/usr/bin/env python3
"""Shared, non-destructive helpers for Sentinel and Cluster comparisons."""
import argparse
from collections import Counter
from dataclasses import dataclass
import json
import os
import re
import secrets
import socket
import sys
import time

import cmdcheck


# Existing data cases are reused only from these groups. Script cases are an
# explicit allowlist: in particular the original SCRIPT FLUSH case is excluded.
DATA_GROUPS = {
    "connection", "string", "keys", "hash", "list", "set", "zset", "bitmap",
    "hll", "geo", "stream", "pubsub", "transaction", "script", "server",
}
SAFE_SCRIPT_CASES = {
    "EVAL_RO / EVALSHA_RO",
    "Lua 内置库 cjson / cmsgpack / bit / struct",
    "Lua 错误处理 redis.pcall / redis.error_reply / redis.status_reply",
}


class BlockedCommand(Exception):
    pass


class SkipCase(Exception):
    pass


@dataclass
class Case:
    group: str
    name: str
    function: object
    scope: str
    ext: bool = False

    @property
    def key(self):
        return "%s / %s" % (self.group, self.name)


@dataclass
class ResultValue:
    """Optional comparable, normalized result plus human-readable detail."""
    value: object
    detail: str = ""


@dataclass
class Auth:
    user: object
    password: object
    tls: object
    timeout: float


def data_cases():
    result = []
    for group, name, function, ext, admin in cmdcheck.CASES:
        if admin or group not in DATA_GROUPS:
            continue
        if (group, name) == ("connection", "AUTH 错误口令被拒绝"):
            result.append(Case(group, name, auth_case, "data", ext))
            continue
        if (group, name) == ("hash", "HGETEX / HSETEX / HGETDEL"):
            result.extend([
                Case("hash", "HGETEX", hash_getex, "data", True),
                Case("hash", "HSETEX", hash_setex, "data", True),
                Case("hash", "HGETDEL", hash_getdel, "data", True),
            ])
            continue
        if (group, name) == ("script", "EVAL / EVALSHA / SCRIPT LOAD / EXISTS / FLUSH"):
            result.append(Case("script", "EVAL / EVALSHA / SCRIPT LOAD / EXISTS（不清空缓存）",
                               eval_case, "data"))
            continue
        if group == "script" and name not in SAFE_SCRIPT_CASES:
            continue
        result.append(Case(group, name, function, "data", ext))
    return result


def auth_case(t):
    if not t.target.password:
        raise SkipCase("未配置数据节点口令")
    unauth = Auth(None, None, t.target.tls, t.target.timeout)
    conn = t.target.open_at((t.target.host, t.target.port), unauth)
    try:
        conn.call("PING")
    except cmdcheck.RespError as exc:
        t.ok("noauth" in str(exc).lower() or "authentication" in str(exc).lower(),
             "未认证应返回 NOAUTH，实际 %s" % exc)
    else:
        raise SkipCase("默认用户无需认证")
    try:
        conn.call("AUTH", "wrong-password-" + secrets.token_hex(12))
    except cmdcheck.RespError as exc:
        t.ok("wrongpass" in str(exc).lower() or "invalid" in str(exc).lower(),
             "错误口令应被拒绝，实际 %s" % exc)
    else:
        raise AssertionError("错误口令不应认证成功")


def hash_getex(t):
    key = t.k("hgetex")
    t.eq(t("HSET", key, "a", "v"), 1)
    t.eq(t("HGETEX", key, "EX", 100, "FIELDS", 1, "a"), ["v"])
    t.ok(0 < t("HTTL", key, "FIELDS", 1, "a")[0] <= 100, "HGETEX 应设置字段过期时间")


def hash_setex(t):
    key = t.k("hsetex")
    t.eq(t("HSETEX", key, "EX", 100, "FIELDS", 1, "a", "v"), 1)
    t.eq(t("HGET", key, "a"), "v")
    t.ok(0 < t("HTTL", key, "FIELDS", 1, "a")[0] <= 100, "HSETEX 应设置字段过期时间")


def hash_getdel(t):
    key = t.k("hgetdel")
    t.eq(t("HSET", key, "a", "v"), 1)
    t.eq(t("HGETDEL", key, "FIELDS", 1, "a"), ["v"])
    t.eq(t("HGET", key, "a"), None)


def eval_case(t):
    key = t.k("lua-safe")
    t.eq(t("SET", key, "v"), "OK")
    script = "return redis.call('GET', KEYS[1])"
    t.eq(t("EVAL", script, 1, key), "v")
    sha = t("SCRIPT", "LOAD", script)
    t.eq(len(sha), 40)
    t.eq(t("EVALSHA", sha, 1, key), "v")
    t.eq(t("SCRIPT", "EXISTS", sha), [1])


def command_name(args):
    command = str(args[0]).upper()
    if command in {"ACL", "CLIENT", "CLUSTER", "COMMAND", "CONFIG", "LATENCY", "MEMORY", "MODULE",
                   "OBJECT", "PUBSUB", "SCRIPT", "SENTINEL", "SLOWLOG", "XGROUP", "XINFO"} and len(args) > 1:
        command += " " + str(args[1]).upper()
    if command == "SENTINEL CONFIG" and len(args) > 2:
        command += " " + str(args[2]).upper()
    return command


def guard_command(args):
    if not args:
        raise BlockedCommand("空命令")
    command = str(args[0]).upper()
    sub = str(args[1]).upper() if len(args) > 1 else ""
    blocked = {
        "FLUSHDB", "FLUSHALL", "SHUTDOWN", "DEBUG", "SAVE", "BGSAVE", "BGREWRITEAOF",
        "SWAPDB", "MIGRATE", "REPLICAOF", "SLAVEOF", "FUNCTION",
    }
    if command in blocked:
        raise BlockedCommand("默认禁止执行 " + command)
    if command == "SCRIPT" and sub not in {"LOAD", "EXISTS"}:
        raise BlockedCommand("默认禁止执行 SCRIPT " + sub)
    if command == "CONFIG" and sub != "GET":
        raise BlockedCommand("默认禁止执行 CONFIG " + sub)
    if command == "CLIENT" and sub in {"PAUSE", "UNPAUSE", "KILL"}:
        raise BlockedCommand("默认禁止执行 CLIENT " + sub)
    if command == "ACL" and sub not in {"WHOAMI", "LIST", "CAT", "GETUSER", "LOG", "GENPASS"}:
        raise BlockedCommand("默认禁止执行 ACL " + sub)
    if command == "MODULE" and sub != "LIST":
        raise BlockedCommand("默认禁止执行 MODULE " + sub)
    if command == "SENTINEL" and sub not in {
        "HELP", "MASTERS", "MASTER", "REPLICAS", "SLAVES", "SENTINELS", "GET-MASTER-ADDR-BY-NAME",
        "CKQUORUM", "MYID", "INFO-CACHE", "PENDING-SCRIPTS", "CONFIG", "IS-MASTER-DOWN-BY-ADDR",
    }:
        raise BlockedCommand("默认禁止执行 SENTINEL " + sub)
    if command == "SENTINEL" and sub == "CONFIG" and (len(args) < 3 or str(args[2]).upper() != "GET"):
        raise BlockedCommand("默认只允许 SENTINEL CONFIG GET")
    if command == "SENTINEL" and sub == "IS-MASTER-DOWN-BY-ADDR" and (len(args) != 6 or str(args[5]) != "*"):
        raise BlockedCommand("SENTINEL 探测只允许使用 *，不参与投票")
    if command == "CLUSTER" and sub not in {
        "INFO", "HELP", "NODES", "SLOTS", "SHARDS", "MYID", "MYSHARDID", "KEYSLOT", "REPLICAS",
        "SLAVES", "LINKS", "COUNTKEYSINSLOT", "GETKEYSINSLOT", "COUNT-FAILURE-REPORTS",
    }:
        raise BlockedCommand("默认禁止执行 CLUSTER " + sub)


class GuardedConn(cmdcheck.Conn):
    def __init__(self, host, port, auth, commands):
        self.commands = commands
        try:
            super().__init__(host, port, auth.user, auth.password, auth.tls, auth.timeout)
        except Exception:
            self.close()
            raise

    def send(self, *args):
        guard_command(args)
        self.commands[command_name(args)] += 1
        return super().send(*args)


class GuardedTarget:
    def __init__(self, name, endpoint, auth, commands, spec=None):
        self.name = name
        self.host, self.port = endpoint
        self.user, self.password, self.tls, self.timeout = auth.user, auth.password, auth.tls, auth.timeout
        self.auth, self.commands, self.spec = auth, commands, spec
        self.extra = []
        self.main = GuardedConn(self.host, self.port, auth, commands)

    def new_conn(self):
        return self.open_at((self.host, self.port))

    def open_at(self, endpoint, auth=None):
        conn = GuardedConn(endpoint[0], endpoint[1], auth or self.auth, self.commands)
        self.extra.append(conn)
        return conn

    def close(self):
        for conn in self.extra + [self.main]:
            conn.close()
        self.extra = []


def endpoint(text):
    host, sep, port = text.strip().rpartition(":")
    if not sep or not host:
        raise ValueError("地址应为 host:port: " + text)
    if host.startswith("[") and host.endswith("]"):
        host = host[1:-1]
    try:
        port = int(port)
    except ValueError:
        raise ValueError("无效端口: " + text)
    if not 1 <= port <= 65535:
        raise ValueError("端口应在 1–65535: " + text)
    return host, port


def target_name(name):
    if not re.fullmatch(r"[A-Za-z][A-Za-z0-9_-]*", name):
        raise ValueError("目标名称仅支持字母、数字、下划线和连字符，且须以字母开头: " + name)
    return name


def auth_for(name, args, sentinel=False):
    suffix = name.upper().replace("-", "_")
    user = os.environ.get("CMDCHECK_USER_" + suffix) or os.environ.get("CMDCHECK_USER") or None
    password = os.environ.get("CMDCHECK_PASSWORD_" + suffix) or os.environ.get("CMDCHECK_PASSWORD") or None
    if sentinel:
        user = os.environ.get("CMDCHECK_SENTINEL_USER_" + suffix) or os.environ.get("CMDCHECK_SENTINEL_USER") or user
        password = (os.environ.get("CMDCHECK_SENTINEL_PASSWORD_" + suffix) or
                    os.environ.get("CMDCHECK_SENTINEL_PASSWORD") or password)
    return Auth(user, password, cmdcheck.make_tls(args), args.timeout)


def parser(description, examples):
    ap = argparse.ArgumentParser(description=description, epilog=examples,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("-t", "--target", action="append", help="被测目标，可重复；不指定时使用本机 demo 拓扑")
    ap.add_argument("--group", help="只运行指定分组，逗号分隔")
    ap.add_argument("--only", help="只运行名称包含该文本的用例")
    ap.add_argument("--list", action="store_true", help="仅列出用例，不连接服务端")
    ap.add_argument("--json", metavar="文件", help="保存完整 JSON 结果（不包含密码）")
    ap.add_argument("--timeout", type=float, default=5.0, help="单次读写超时秒数，默认 5")
    ap.add_argument("--tls", action="store_true")
    ap.add_argument("--tls-ca")
    ap.add_argument("--tls-cert")
    ap.add_argument("--tls-key")
    ap.add_argument("--tls-insecure", action="store_true", help="不校验 TLS 证书，仅供测试")
    ap.add_argument("-v", "--verbose", action="store_true", help="显示全部用例")
    return ap


def select_cases(cases, args):
    groups = {s.strip() for s in args.group.split(",") if s.strip()} if args.group else None
    selected = [c for c in cases if (not groups or c.group in groups) and
                (not args.only or args.only.lower() in c.name.lower())]
    if not selected:
        raise ValueError("没有匹配的用例")
    return selected


def list_cases(cases):
    for case in cases:
        print("%-13s %s%s" % (case.group, case.name, " [扩展]" if case.ext else ""))
    print("共 %d 个用例；可执行 --group 或 --only 筛选" % len(cases))


def run_case(case, spec, seed, commands):
    target = None
    context = None
    status, detail, value = "PASS", "", None
    start = time.monotonic()
    try:
        address, auth = spec.endpoint_for(case.scope)
        target = GuardedTarget(spec.name, address, auth, commands, spec)
        context = cmdcheck.Ctx(target, "{cmdcheck-%s}" % seed)
        result = case.function(context)
        if isinstance(result, str) and result.startswith("SKIP:"):
            status, detail = "SKIP", result[5:].strip()
        elif isinstance(result, str):
            detail = result
        elif isinstance(result, ResultValue):
            detail, value = result.detail, result.value
    except SkipCase as exc:
        status, detail = "SKIP", str(exc)
    except cmdcheck.RespError as exc:
        status, detail = cmdcheck.classify(exc)
    except AssertionError as exc:
        status, detail = "FAIL", str(exc)
    except (OSError, socket.timeout, BlockedCommand) as exc:
        status, detail = "FAIL", "%s: %s" % (type(exc).__name__, exc)
    except Exception as exc:
        status, detail = "FAIL", "%s: %s" % (type(exc).__name__, exc)
    finally:
        # A failed case can leave MULTI or RESP3 state on its connection. Use a
        # fresh connection for cleanup, and follow redirects for each key.
        if context and context.keys:
            failures = []
            for key in dict.fromkeys(context.keys):
                try:
                    cleanup_key((target.host, target.port), target.auth, commands, key)
                except Exception as exc:
                    failures.append("%s: %s" % (key, exc))
            if failures:
                status = "FAIL"
                detail = (detail + "; " if detail else "") + "清理失败: " + "; ".join(failures)
        if target:
            target.close()
    return {"status": status, "detail": detail, "seconds": round(time.monotonic() - start, 3), "value": value}


def cleanup_key(address, auth, commands, key):
    try:
        return redirect_call(address, auth, commands, "UNLINK", key)
    except cmdcheck.RespError as exc:
        status, _ = cmdcheck.classify(exc)
        if status not in {"UNSUPPORTED", "NOPERM"}:
            raise
        return redirect_call(address, auth, commands, "DEL", key)


def redirect_call(address, auth, commands, *args):
    ask = False
    for _ in range(8):
        conn = GuardedConn(address[0], address[1], auth, commands)
        try:
            if ask:
                conn.call("ASKING")
            return conn.call(*args)
        except cmdcheck.RespError as exc:
            parts = str(exc).split()
            if len(parts) != 3 or parts[0] not in {"MOVED", "ASK"}:
                raise
            ask = parts[0] == "ASK"
            redirect = parts[2]
            if redirect.startswith(":"):
                redirect = address[0] + redirect
            address = endpoint(redirect)
        finally:
            conn.close()
    raise RuntimeError("重定向次数过多")


def run(cases, specs, seed, args):
    records = {spec.name: spec.commands for spec in specs}
    results = {spec.name: {} for spec in specs}
    for case in cases:
        for spec in specs:
            results[spec.name][case.key] = run_case(case, spec, seed, records[spec.name])

    names = [spec.name for spec in specs]
    width = max([len(case.key) for case in cases] + [4])
    print("测试前缀: {cmdcheck-%s}:*" % seed)
    for spec in specs:
        print("目标 %-12s %s" % (spec.name, spec.describe()))
    print("\n%-*s  %s" % (width, "用例", "  ".join("%-12s" % n for n in names)))
    print("-" * (width + 2 + 14 * len(names)))
    differences = []
    for case in cases:
        entries = [results[name][case.key] for name in names]
        statuses = [entry["status"] for entry in entries]
        values = [entry["value"] for entry in entries]
        differs = len(set(statuses)) > 1 or (all(s == "PASS" for s in statuses) and
                                            all(v is not None for v in values) and
                                            any(v != values[0] for v in values[1:]))
        if differs:
            differences.append(case.key)
        if args.verbose or differs or any(s != "PASS" for s in statuses):
            print("%-*s  %s%s" % (width, case.key,
                  "  ".join("%-12s" % s for s in statuses), "  ← 不一致" if differs else ""))
            for name, entry in zip(names, entries):
                if entry["detail"] and (args.verbose or entry["status"] != "PASS" or differs):
                    print("    [%s] %s" % (name, entry["detail"]))
                if differs and entry["value"] is not None:
                    print("    [%s] 比较值: %s" % (name, json.dumps(entry["value"], ensure_ascii=False)))

    print("\n汇总:")
    bad = bool(differences)
    for name in names:
        counts = Counter(item["status"] for item in results[name].values())
        print("%-12s %s" % (name, " ".join("%s=%d" % (s, counts[s]) for s in
                                               ("PASS", "FAIL", "UNSUPPORTED", "NOPERM", "SKIP"))))
        if counts["FAIL"] or counts["NOPERM"]:
            bad = True
        print("  尝试发送的命令（含发现、认证和清理）: " + " ".join(sorted(records[name])))
    print("结果不一致: %d 项" % len(differences))
    if args.json:
        data = {"prefix": seed, "targets": {s.name: s.metadata() for s in specs}, "results": results,
                "differences": differences, "attempted_commands": {k: dict(v) for k, v in records.items()}}
        try:
            with open(args.json, "w", encoding="utf-8") as fh:
                json.dump(data, fh, ensure_ascii=False, indent=2)
        except OSError as exc:
            print("无法写入 JSON 结果: %s" % exc, file=sys.stderr)
            return 2
        print("结果已写入 %s" % args.json)
    return 1 if bad else 0


def new_seed():
    return secrets.token_hex(8)

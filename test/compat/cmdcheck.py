#!/usr/bin/env python3
"""命令支持情况验证：用同一组用例对一个或多个 Redis 协议服务端做功能验证并对比。

只依赖 Python 3.8+ 标准库。用例覆盖连接、字符串、键、哈希、列表、集合、有序集合、位图、
HyperLogLog、地理、流、发布订阅、事务、脚本与函数、ACL、服务器管理、集群。

安全性：
  * 所有测试键都带随机前缀 "{cmdcheck-<随机>}:"（花括号让键落在同一个集群槽），用例结束即删除；
  * 绝不执行 FLUSHDB / FLUSHALL / SHUTDOWN / DEBUG / SAVE / SWAPDB；
  * 会改动服务端状态的管理类用例（CONFIG SET、BGSAVE、ACL 临时用户等）默认不跑，需要 --admin。

用法示例：
  # 只测一个实例
  CMDCHECK_PASSWORD=xxx ./cmdcheck.py -t sycache=127.0.0.1:6379
  # 同一套用例对比两个实例（口令可按名称单独指定）
  CMDCHECK_PASSWORD_REDIS=aaa CMDCHECK_PASSWORD_SYCACHE=bbb \\
      ./cmdcheck.py -t redis=10.0.0.1:6379 -t sycache=10.0.0.2:6379 --json out.json
  ./cmdcheck.py --list                 # 列出全部用例
  ./cmdcheck.py -t x=127.0.0.1:6379 --group string,hash

口令只从环境变量读取（CMDCHECK_USER / CMDCHECK_PASSWORD / CMDCHECK_PASSWORD_<名称大写>），
不接受命令行参数，避免出现在进程列表与 shell 历史里。

状态：PASS 通过；FAIL 命令存在但行为与预期不符；UNSUPPORTED 服务端不认识该命令；
NA 扩展 / 新版本命令不支持（不算失败）；NOPERM 账号无权执行；SKIP 前置条件不满足。
退出码：0 全部通过；1 有 FAIL 或核心命令 UNSUPPORTED；2 参数或连接错误。
"""
import argparse
import json
import os
import random
import socket
import ssl
import sys
import time

# --------------------------------------------------------------------------- RESP 客户端


class RespError(Exception):
    """服务端返回的错误回复。"""


class Conn:
    def __init__(self, host, port, user=None, password=None, tls=None, timeout=5.0):
        self.host, self.port, self.timeout = host, port, timeout
        self.user, self.password, self.tls = user, password, tls
        self.sock = None
        self.buf = b""
        self.connect()

    def connect(self):
        s = socket.create_connection((self.host, self.port), timeout=self.timeout)
        if self.tls is not None:
            s = self.tls.wrap_socket(s, server_hostname=self.host)
        s.settimeout(self.timeout)
        self.sock, self.buf = s, b""
        if self.password:
            if self.user:
                self.call("AUTH", self.user, self.password)
            else:
                self.call("AUTH", self.password)

    def close(self):
        try:
            if self.sock:
                self.sock.close()
        finally:
            self.sock = None

    # 编码与发送
    @staticmethod
    def _enc(a):
        if isinstance(a, bytes):
            return a
        if isinstance(a, float):
            a = repr(a)
        return str(a).encode()

    def send(self, *args):
        out = [b"*%d\r\n" % len(args)]
        for a in args:
            b = self._enc(a)
            out.append(b"$%d\r\n%s\r\n" % (len(b), b))
        self.sock.sendall(b"".join(out))

    # 解码
    def _fill(self):
        d = self.sock.recv(65536)
        if not d:
            raise ConnectionError("连接被对端关闭")
        self.buf += d

    def _line(self):
        while True:
            i = self.buf.find(b"\r\n")
            if i >= 0:
                line, self.buf = self.buf[:i], self.buf[i + 2:]
                return line
            self._fill()

    def _exact(self, n):
        while len(self.buf) < n + 2:
            self._fill()
        data, self.buf = self.buf[:n], self.buf[n + 2:]
        return data

    def read(self):
        """读取一条回复；保留二进制（bytes），错误回复作为 RespError 对象返回。"""
        line = self._line()
        t, rest = line[:1], line[1:]
        if t == b"+":
            return rest.decode()
        if t == b"-":
            return RespError(rest.decode(errors="replace"))
        if t == b":":
            return int(rest)
        if t == b"_":
            return None
        if t == b"#":
            return rest == b"t"
        if t == b",":
            return float(rest)
        if t == b"(":
            return int(rest)
        if t in (b"$", b"=", b"!"):
            n = int(rest)
            if n < 0:
                return None
            data = self._exact(n)
            if t == b"!":
                return RespError(data.decode(errors="replace"))
            if t == b"=":
                data = data[4:]  # 去掉 "txt:" 之类的格式前缀
            return data
        if t in (b"*", b"~", b">"):
            n = int(rest)
            if n < 0:
                return None
            return [self.read() for _ in range(n)]
        if t == b"%":
            n = int(rest)
            d = {}
            for _ in range(n):
                k = _txt(self.read())
                d[k] = self.read()
            return d
        if t == b"|":  # 属性：读取后丢弃，再读真正的回复
            for _ in range(int(rest)):
                self.read()
                self.read()
            return self.read()
        raise ValueError("无法解析的回复类型 %r" % line[:20])

    def call_raw(self, *args):
        self.send(*args)
        r = self.read()
        if isinstance(r, RespError):
            raise r
        return r

    def call(self, *args):
        return _txt(self.call_raw(*args))


def _txt(v):
    """bytes 递归转 str；其余保持。"""
    if isinstance(v, bytes):
        return v.decode("utf-8", errors="replace")
    if isinstance(v, list):
        return [_txt(x) for x in v]
    if isinstance(v, dict):
        return {k: _txt(x) for k, x in v.items()}
    return v


def pairs(flat):
    """RESP2 的扁平数组 / RESP3 的 map 统一成 dict。"""
    if isinstance(flat, dict):
        return flat
    return {flat[i]: flat[i + 1] for i in range(0, len(flat), 2)}


# --------------------------------------------------------------------------- 用例框架

CASES = []  # (group, name, func, ext, admin)


def case(group, name, ext=False, admin=False):
    def deco(f):
        CASES.append((group, name, f, ext, admin))
        return f
    return deco


class Ctx:
    """给每个用例的上下文：t(...) 发送命令；t.k(x) 生成测试键。"""

    def __init__(self, target, tag):
        self.target, self.tag, self.c = target, tag, target.main
        self.keys = []
        self.info = {}

    def __call__(self, *a):
        return self.c.call(*a)

    def raw(self, *a):
        return self.c.call_raw(*a)

    def k(self, name):
        key = "%s:%s" % (self.tag, name)
        self.keys.append(key)
        return key

    def conn(self):
        return self.target.new_conn()

    def eq(self, actual, expected, what=""):
        if actual != expected:
            raise AssertionError("%s期望 %r，实际 %r" % (what + " " if what else "", expected, actual))

    def ok(self, cond, what):
        if not cond:
            raise AssertionError(what)

    def near(self, actual, expected, tol, what=""):
        if abs(float(actual) - float(expected)) > tol:
            raise AssertionError("%s期望约 %r，实际 %r" % (what + " " if what else "", expected, actual))

    def raises(self, prefix, *args):
        """期望该命令返回以 prefix 开头（不区分大小写，子串匹配）的错误。"""
        try:
            self.c.call_raw(*args)
        except RespError as e:
            if prefix.lower() not in str(e).lower():
                raise AssertionError("期望错误包含 %r，实际 %r" % (prefix, str(e)))
            return
        raise AssertionError("期望返回错误 %r，但命令成功" % prefix)

    def info_sec(self, section):
        out = {}
        for line in self("INFO", section).splitlines():
            if ":" in line and not line.startswith("#"):
                k, v = line.split(":", 1)
                out[k] = v
        return out

    def close_all(self):
        pass


class Target:
    def __init__(self, name, host, port, user, password, tls, timeout):
        self.name, self.host, self.port = name, host, port
        self.user, self.password, self.tls, self.timeout = user, password, tls, timeout
        self.main = Conn(host, port, user, password, tls, timeout)
        self.extra = []

    def new_conn(self):
        c = Conn(self.host, self.port, self.user, self.password, self.tls, self.timeout)
        self.extra.append(c)
        return c

    def close(self):
        for c in self.extra + [self.main]:
            c.close()
        self.extra = []

    def reconnect(self):
        for c in self.extra:
            c.close()
        self.extra = []
        try:
            self.main.close()
        except Exception:
            pass
        self.main = Conn(self.host, self.port, self.user, self.password, self.tls, self.timeout)


def classify(e):
    msg = str(e)
    low = msg.lower()
    if "unknown command" in low or "unknown subcommand" in low or "unknown or disabled command" in low \
            or "unknown sub command" in low:
        return "UNSUPPORTED", msg
    if low.startswith("noperm") or "no permissions" in low:
        return "NOPERM", msg
    return "FAIL", msg


def run_case(target, group, name, f, ext, admin_flag, tag_seed):
    tag = "{cmdcheck-%s}" % tag_seed
    t = Ctx(target, tag)
    start = time.time()
    status, detail, from_server = "PASS", "", False
    try:
        r = f(t)
        if isinstance(r, str) and r.startswith("SKIP:"):
            status, detail = "SKIP", r[5:].strip()
        elif isinstance(r, str):
            detail = r
    except RespError as e:
        status, detail, from_server = classify(e) + (True,)
    except AssertionError as e:
        status, detail = "FAIL", str(e)
    except (ConnectionError, socket.timeout, OSError) as e:
        status, detail = "FAIL", "连接错误：%s" % e
        try:
            target.reconnect()
        except Exception:
            pass
    except Exception as e:  # 用例自身的问题也要报告出来，不能悄悄吞掉
        status, detail = "FAIL", "%s: %s" % (type(e).__name__, e)
    finally:
        # 清理本用例创建的键；失败了也要清，避免残留
        try:
            if t.keys:
                target.main.call_raw("UNLINK", *t.keys)
        except Exception:
            try:
                target.main.call_raw("DEL", *t.keys)
            except Exception:
                pass
        for c in target.extra:
            c.close()
        target.extra = []
    if ext and from_server and status in ("UNSUPPORTED", "FAIL"):
        status = "NA"
    return status, detail, time.time() - start


# =========================================================================== 用例：连接


@case("connection", "PING")
def _(t):
    t.eq(t("PING"), "PONG")
    t.eq(t("PING", "hello"), "hello")


@case("connection", "ECHO")
def _(t):
    t.eq(t("ECHO", "你好 sycache"), "你好 sycache")


@case("connection", "SELECT / DBSIZE")
def _(t):
    t.eq(t("SELECT", 0), "OK")
    t.ok(isinstance(t.raw("DBSIZE"), int), "DBSIZE 应返回整数")


@case("connection", "CLIENT SETNAME / GETNAME / ID / INFO / LIST")
def _(t):
    t.eq(t("CLIENT", "SETNAME", "cmdcheck"), "OK")
    t.eq(t("CLIENT", "GETNAME"), "cmdcheck")
    t.ok(t.raw("CLIENT", "ID") > 0, "CLIENT ID 应为正整数")
    t.ok("name=cmdcheck" in t("CLIENT", "INFO"), "CLIENT INFO 应包含 name=cmdcheck")
    t.ok("name=cmdcheck" in t("CLIENT", "LIST"), "CLIENT LIST 应包含当前连接")


@case("connection", "CLIENT SETINFO", ext=True)
def _(t):
    t.eq(t("CLIENT", "SETINFO", "LIB-NAME", "cmdcheck"), "OK")
    t.eq(t("CLIENT", "SETINFO", "LIB-VER", "1.0"), "OK")


@case("connection", "HELLO 2 / HELLO 3 (RESP3)")
def _(t):
    c = t.conn()
    h2 = pairs(_txt(c.call_raw("HELLO", 2)))
    t.eq(h2.get("proto"), 2, "HELLO 2 proto")
    c2 = t.conn()
    h3 = c2.call_raw("HELLO", 3)
    t.ok(isinstance(h3, dict), "HELLO 3 应返回 map")
    t.eq(h3.get("proto"), 3, "HELLO 3 proto")
    k = t.k("resp3")
    c2.call_raw("HSET", k, "a", "1")
    r = c2.call_raw("HGETALL", k)
    t.ok(isinstance(r, dict), "RESP3 下 HGETALL 应返回 map，实际 %s" % type(r).__name__)
    t.eq(_txt(c2.call_raw("HGET", k, "a")), "1")
    t.info["server_field"] = _txt(h3.get("server"))
    return "HELLO server=%s version=%s" % (_txt(h3.get("server")), _txt(h3.get("version")))


@case("connection", "RESET")
def _(t):
    c = t.conn()
    c.call("CLIENT", "SETNAME", "x")
    t.eq(c.call("RESET"), "RESET")
    if t.target.password:  # RESET 会清除认证状态，这是预期行为
        try:
            c.call_raw("PING")
            raise AssertionError("RESET 后应回到未认证状态")
        except RespError as e:
            t.ok("noauth" in str(e).lower(), "RESET 后应返回 NOAUTH，实际 %s" % e)
        c.call("AUTH", *([t.target.user] if t.target.user else []), t.target.password)
    t.eq(c.call("CLIENT", "GETNAME"), None)


@case("connection", "AUTH 错误口令被拒绝")
def _(t):
    if not t.target.password:
        return "SKIP: 未设置口令（CMDCHECK_PASSWORD），无法验证认证"
    c = Conn(t.target.host, t.target.port, None, None, t.target.tls, t.target.timeout)
    try:
        try:
            c.call_raw("PING")
            return "SKIP: 默认用户无需认证"
        except RespError as e:
            t.ok("noauth" in str(e).lower() or "authentication" in str(e).lower(), "未认证应返回 NOAUTH，实际 %s" % e)
        try:
            c.call_raw("AUTH", "wrong-password-%d" % random.randint(0, 1 << 30))
        except RespError as e:
            t.ok("wrongpass" in str(e).lower() or "invalid" in str(e).lower(), "错误口令应被拒绝，实际 %s" % e)
        else:
            raise AssertionError("错误口令不应认证成功")
    finally:
        c.close()


# =========================================================================== 用例：字符串


@case("string", "SET / GET / DEL / EXISTS")
def _(t):
    k = t.k("s")
    t.eq(t("SET", k, "v"), "OK")
    t.eq(t("GET", k), "v")
    t.eq(t("EXISTS", k), 1)
    t.eq(t("DEL", k), 1)
    t.eq(t("GET", k), None)


@case("string", "SET NX / XX / GET / KEEPTTL / EX / PX")
def _(t):
    k = t.k("s")
    t.eq(t("SET", k, "1", "NX"), "OK")
    t.eq(t("SET", k, "2", "NX"), None)
    t.eq(t("SET", k, "3", "XX"), "OK")
    t.eq(t("SET", k, "4", "GET"), "3")
    t.eq(t("SET", k, "5", "EX", 100), "OK")
    t.ok(0 < t.raw("TTL", k) <= 100, "EX 后 TTL 应在 (0,100]")
    t.eq(t("SET", k, "6", "KEEPTTL"), "OK")
    t.ok(t.raw("TTL", k) > 0, "KEEPTTL 应保留 TTL")
    t.eq(t("SET", k, "7", "PX", 100000), "OK")
    t.ok(t.raw("PTTL", k) > 0, "PX 后 PTTL > 0")


@case("string", "SET EXAT / PXAT", ext=False)
def _(t):
    k = t.k("s")
    t.eq(t("SET", k, "v", "EXAT", int(time.time()) + 100), "OK")
    t.ok(0 < t.raw("TTL", k) <= 100, "EXAT TTL")
    t.eq(t("SET", k, "v", "PXAT", int(time.time() * 1000) + 100000), "OK")
    t.ok(0 < t.raw("PTTL", k) <= 100000, "PXAT PTTL")


@case("string", "过期时间到点后键被删除")
def _(t):
    k = t.k("exp")
    t("SET", k, "v", "PX", 100)
    time.sleep(0.3)
    t.eq(t("GET", k), None)


@case("string", "SETNX / SETEX / PSETEX / GETSET")
def _(t):
    k = t.k("s")
    t.eq(t("SETNX", k, "a"), 1)
    t.eq(t("SETNX", k, "b"), 0)
    t.eq(t("SETEX", k, 100, "c"), "OK")
    t.eq(t("PSETEX", k, 100000, "d"), "OK")
    t.eq(t("GETSET", k, "e"), "d")


@case("string", "GETEX / GETDEL")
def _(t):
    k = t.k("s")
    t("SET", k, "v")
    t.eq(t("GETEX", k, "EX", 100), "v")
    t.ok(0 < t.raw("TTL", k) <= 100, "GETEX EX 应设置 TTL")
    t.eq(t("GETEX", k, "PERSIST"), "v")
    t.eq(t.raw("TTL", k), -1)
    t.eq(t("GETDEL", k), "v")
    t.eq(t("EXISTS", k), 0)


@case("string", "APPEND / STRLEN / GETRANGE / SETRANGE / SUBSTR")
def _(t):
    k = t.k("s")
    t.eq(t("APPEND", k, "Hello"), 5)
    t.eq(t("APPEND", k, " World"), 11)
    t.eq(t("STRLEN", k), 11)
    t.eq(t("GETRANGE", k, 0, 4), "Hello")
    t.eq(t("GETRANGE", k, -5, -1), "World")
    t.eq(t("SETRANGE", k, 6, "Redis"), 11)
    t.eq(t("GET", k), "Hello Redis")
    t.eq(t("SUBSTR", k, 0, 4), "Hello")


@case("string", "INCR / DECR / INCRBY / DECRBY / INCRBYFLOAT")
def _(t):
    k = t.k("n")
    t.eq(t("INCR", k), 1)
    t.eq(t("INCRBY", k, 10), 11)
    t.eq(t("DECR", k), 10)
    t.eq(t("DECRBY", k, 4), 6)
    t.near(t("INCRBYFLOAT", k, "1.5"), 7.5, 1e-9)
    t("SET", t.k("str"), "abc")
    t.raises("not an integer", "INCR", t.keys[-1])


@case("string", "MSET / MGET / MSETNX")
def _(t):
    a, b, c = t.k("a"), t.k("b"), t.k("c")
    t.eq(t("MSET", a, "1", b, "2"), "OK")
    t.eq(t("MGET", a, b, c), ["1", "2", None])
    t.eq(t("MSETNX", a, "x", c, "y"), 0)
    t.eq(t("MSETNX", c, "y", t.k("d"), "z"), 1)


@case("string", "LCS", ext=True)
def _(t):
    a, b = t.k("a"), t.k("b")
    t("MSET", a, "ohmytext", b, "mynewtext")
    t.eq(t("LCS", a, b), "mytext")
    t.eq(t("LCS", a, b, "LEN"), 6)


@case("string", "二进制安全（含 \\0 与 \\r\\n）")
def _(t):
    k = t.k("bin")
    data = b"a\x00b\r\nc\xff"
    t.c.call_raw("SET", k, data)
    t.eq(t.c.call_raw("GET", k), data)
    t.eq(t("STRLEN", k), len(data))


@case("string", "大值（1MiB）读写")
def _(t):
    k = t.k("big")
    data = b"x" * (1 << 20)
    t.c.call_raw("SET", k, data)
    t.eq(t("STRLEN", k), len(data))
    t.eq(len(t.c.call_raw("GET", k)), len(data))


# =========================================================================== 用例：键


@case("keys", "EXPIRE / PEXPIRE / EXPIREAT / TTL / PTTL / PERSIST")
def _(t):
    k = t.k("k")
    t("SET", k, "v")
    t.eq(t("EXPIRE", k, 100), 1)
    t.ok(0 < t.raw("TTL", k) <= 100, "TTL")
    t.eq(t("PEXPIRE", k, 100000), 1)
    t.eq(t("EXPIREAT", k, int(time.time()) + 100), 1)
    t.eq(t("PERSIST", k), 1)
    t.eq(t.raw("TTL", k), -1)
    t.eq(t.raw("TTL", t.k("nokey")), -2)


@case("keys", "EXPIRE NX / XX / GT / LT 选项")
def _(t):
    k = t.k("k")
    t("SET", k, "v")
    t.eq(t("EXPIRE", k, 100, "NX"), 1)
    t.eq(t("EXPIRE", k, 200, "NX"), 0)
    t.eq(t("EXPIRE", k, 200, "GT"), 1)
    t.eq(t("EXPIRE", k, 300, "LT"), 0)
    t.eq(t("EXPIRE", k, 50, "XX"), 1)


@case("keys", "EXPIRETIME / PEXPIRETIME")
def _(t):
    k = t.k("k")
    ts = int(time.time()) + 100
    t("SET", k, "v")
    t("EXPIREAT", k, ts)
    t.eq(t.raw("EXPIRETIME", k), ts)
    t.eq(t.raw("PEXPIRETIME", k), ts * 1000)


@case("keys", "TYPE")
def _(t):
    for typ, cmd in (("string", ("SET", "1")), ("list", ("RPUSH", "1")), ("set", ("SADD", "1")),
                     ("zset", ("ZADD", "1", "m")), ("hash", ("HSET", "f", "v")), ("stream", ("XADD",))):
        k = t.k("t-" + typ)
        if typ == "stream":
            t("XADD", k, "*", "f", "v")
        else:
            t(cmd[0], k, *cmd[1:])
        t.eq(t("TYPE", k), typ)
    t.eq(t("TYPE", t.k("none")), "none")


@case("keys", "RENAME / RENAMENX / COPY")
def _(t):
    a, b = t.k("a"), t.k("b")
    t("SET", a, "v")
    t.eq(t("RENAME", a, b), "OK")
    t.eq(t("EXISTS", a), 0)
    t("SET", a, "w")
    t.eq(t("RENAMENX", a, b), 0)
    t.eq(t("COPY", b, t.k("c")), 1)
    t.eq(t("GET", t.keys[-1]), "v")
    t.eq(t("COPY", b, t.keys[-1]), 0)
    t.eq(t("COPY", b, t.keys[-1], "REPLACE"), 1)


@case("keys", "UNLINK / TOUCH / RANDOMKEY")
def _(t):
    a, b = t.k("a"), t.k("b")
    t("MSET", a, 1, b, 2)
    t.eq(t("TOUCH", a, b), 2)
    t.ok(t("RANDOMKEY") is not None, "库中有键时 RANDOMKEY 不应为空")
    t.eq(t("UNLINK", a, b), 2)


@case("keys", "OBJECT ENCODING / IDLETIME / REFCOUNT")
def _(t):
    k = t.k("o")
    t("SET", k, "123")
    t.eq(t("OBJECT", "ENCODING", k), "int")
    t.ok(t.raw("OBJECT", "IDLETIME", k) >= 0, "IDLETIME")
    t.ok(t.raw("OBJECT", "REFCOUNT", k) >= 1, "REFCOUNT")
    t("SET", k, "a string that is long enough" * 3)
    t.eq(t("OBJECT", "ENCODING", k), "raw")


@case("keys", "DUMP / RESTORE")
def _(t):
    a, b = t.k("a"), t.k("b")
    t("HSET", a, "f", "v")
    d = t.c.call_raw("DUMP", a)
    t.ok(isinstance(d, bytes) and len(d) > 0, "DUMP 应返回序列化数据")
    t.eq(t.c.call("RESTORE", b, 0, d), "OK")
    t.eq(t("HGET", b, "f"), "v")
    t.raises("BUSYKEY", "RESTORE", b, 0, d)
    t.eq(t.c.call("RESTORE", b, 0, d, "REPLACE"), "OK")


@case("keys", "SCAN MATCH / COUNT / TYPE")
def _(t):
    names = [t.k("scan%d" % i) for i in range(30)]
    t("MSET", *[x for n in names for x in (n, "v")])
    seen, cur = set(), "0"
    while True:
        cur, ks = t("SCAN", cur, "MATCH", t.tag + ":scan*", "COUNT", 10)
        seen.update(ks)
        if cur == "0":
            break
    t.eq(seen, set(names))
    cur, ks = t("SCAN", "0", "MATCH", t.tag + ":scan*", "COUNT", 1000, "TYPE", "string")
    t.ok(len(ks) > 0, "SCAN TYPE string 应有结果")


@case("keys", "KEYS 模式")
def _(t):
    t("MSET", t.k("kx1"), 1, t.k("kx2"), 2, t.k("ky"), 3)
    t.eq(sorted(t("KEYS", t.tag + ":kx*")), sorted([t.keys[0], t.keys[1]]))


@case("keys", "SORT / SORT_RO")
def _(t):
    k = t.k("l")
    t("RPUSH", k, 3, 1, 2)
    t.eq(t("SORT", k), ["1", "2", "3"])
    t.eq(t("SORT", k, "DESC", "LIMIT", 0, 2), ["3", "2"])
    t("RPUSH", t.k("al"), "b", "a", "c")
    t.eq(t("SORT", t.keys[-1], "ALPHA"), ["a", "b", "c"])
    t.eq(t("SORT", k, "STORE", t.k("dst")), 3)
    t.eq(t("SORT_RO", k), ["1", "2", "3"])


@case("keys", "OBJECT FREQ 需要 LFU 策略时给出明确错误")
def _(t):
    k = t.k("f")
    t("SET", k, "v")
    try:
        t.raw("OBJECT", "FREQ", k)
    except RespError as e:
        t.ok("lfu" in str(e).lower(), "非 LFU 策略下应提示 LFU，实际 %s" % e)


# =========================================================================== 用例：哈希


@case("hash", "HSET / HGET / HMGET / HGETALL / HDEL / HEXISTS / HLEN")
def _(t):
    k = t.k("h")
    t.eq(t("HSET", k, "a", 1, "b", 2), 2)
    t.eq(t("HGET", k, "a"), "1")
    t.eq(t("HMGET", k, "a", "b", "c"), ["1", "2", None])
    t.eq(pairs(t("HGETALL", k)), {"a": "1", "b": "2"})
    t.eq(t("HEXISTS", k, "a"), 1)
    t.eq(t("HLEN", k), 2)
    t.eq(sorted(t("HKEYS", k)), ["a", "b"])
    t.eq(sorted(t("HVALS", k)), ["1", "2"])
    t.eq(t("HDEL", k, "a"), 1)
    t.eq(t("HMSET", k, "c", 3), "OK")


@case("hash", "HINCRBY / HINCRBYFLOAT / HSETNX / HSTRLEN")
def _(t):
    k = t.k("h")
    t.eq(t("HINCRBY", k, "n", 5), 5)
    t.near(t("HINCRBYFLOAT", k, "f", "1.5"), 1.5, 1e-9)
    t.eq(t("HSETNX", k, "n", 9), 0)
    t.eq(t("HSETNX", k, "z", "abc"), 1)
    t.eq(t("HSTRLEN", k, "z"), 3)


@case("hash", "HRANDFIELD / HSCAN")
def _(t):
    k = t.k("h")
    t("HSET", k, *[x for i in range(20) for x in ("f%d" % i, i)])
    t.eq(len(t("HRANDFIELD", k, 5)), 5)
    t.eq(len(t("HRANDFIELD", k, -30)), 30)
    got, cur = {}, "0"
    while True:
        cur, flat = t("HSCAN", k, cur, "COUNT", 5)
        got.update(pairs(flat))
        if cur == "0":
            break
    t.eq(len(got), 20)


@case("hash", "哈希字段过期 HEXPIRE / HTTL / HPERSIST", ext=True)
def _(t):
    k = t.k("h")
    t("HSET", k, "a", 1, "b", 2)
    t.eq(t("HEXPIRE", k, 100, "FIELDS", 1, "a"), [1])
    t.ok(0 < t("HTTL", k, "FIELDS", 1, "a")[0] <= 100, "HTTL")
    t.eq(t("HPERSIST", k, "FIELDS", 1, "a"), [1])


@case("hash", "HGETEX / HSETEX / HGETDEL", ext=True)
def _(t):
    k = t.k("h")
    t.eq(t("HSETEX", k, "EX", 100, "FIELDS", 1, "a", "v"), 1)
    t.eq(t("HGETEX", k, "PERSIST", "FIELDS", 1, "a"), ["v"])
    t.eq(t("HGETDEL", k, "FIELDS", 1, "a"), ["v"])


# =========================================================================== 用例：列表


@case("list", "LPUSH / RPUSH / LRANGE / LLEN / LINDEX / LPOP / RPOP")
def _(t):
    k = t.k("l")
    t.eq(t("RPUSH", k, "a", "b", "c"), 3)
    t.eq(t("LPUSH", k, "z"), 4)
    t.eq(t("LRANGE", k, 0, -1), ["z", "a", "b", "c"])
    t.eq(t("LLEN", k), 4)
    t.eq(t("LINDEX", k, 1), "a")
    t.eq(t("LPOP", k), "z")
    t.eq(t("RPOP", k), "c")
    t.eq(t("LPOP", k, 2), ["a", "b"])


@case("list", "LSET / LINSERT / LREM / LTRIM / LPUSHX / RPUSHX")
def _(t):
    k = t.k("l")
    t("RPUSH", k, "a", "b", "c", "b")
    t.eq(t("LSET", k, 0, "A"), "OK")
    t.eq(t("LINSERT", k, "BEFORE", "c", "x"), 5)
    t.eq(t("LREM", k, 0, "b"), 2)
    t.eq(t("LRANGE", k, 0, -1), ["A", "x", "c"])
    t.eq(t("LTRIM", k, 0, 1), "OK")
    t.eq(t("LRANGE", k, 0, -1), ["A", "x"])
    t.eq(t("LPUSHX", t.k("none"), "v"), 0)
    t.eq(t("RPUSHX", k, "y"), 3)


@case("list", "LPOS / LMOVE / RPOPLPUSH / LMPOP")
def _(t):
    a, b = t.k("a"), t.k("b")
    t("RPUSH", a, "a", "b", "c", "b")
    t.eq(t("LPOS", a, "b"), 1)
    t.eq(t("LPOS", a, "b", "RANK", -1), 3)
    t.eq(t("LPOS", a, "b", "COUNT", 0), [1, 3])
    t.eq(t("LMOVE", a, b, "LEFT", "RIGHT"), "a")
    t.eq(t("RPOPLPUSH", a, b), "b")
    t.eq(t("LRANGE", b, 0, -1), ["b", "a"])
    t.eq(t("LMPOP", 1, a, "LEFT", "COUNT", 2), [a, ["b", "c"]])


@case("list", "BLPOP / BRPOP / BLMOVE / BLMPOP")
def _(t):
    a, b = t.k("a"), t.k("b")
    t.eq(t("BLPOP", a, "0.1"), None)
    t("RPUSH", a, "x", "y")
    t.eq(t("BLPOP", a, 1), [a, "x"])
    t.eq(t("BRPOP", a, 1), [a, "y"])
    t("RPUSH", a, "m")
    t.eq(t("BLMOVE", a, b, "LEFT", "LEFT", 1), "m")
    t("RPUSH", a, "n")
    t.eq(t("BLMPOP", 1, 1, a, "LEFT"), [a, ["n"]])


@case("list", "阻塞命令被另一连接唤醒")
def _(t):
    k = t.k("wake")
    c1, c2 = t.conn(), t.conn()
    c1.send("BLPOP", k, 3)
    time.sleep(0.2)
    c2.call("RPUSH", k, "hello")
    t.eq(_txt(c1.read()), [k, "hello"])


# =========================================================================== 用例：集合


@case("set", "SADD / SREM / SMEMBERS / SISMEMBER / SCARD")
def _(t):
    k = t.k("s")
    t.eq(t("SADD", k, "a", "b", "c"), 3)
    t.eq(sorted(t("SMEMBERS", k)), ["a", "b", "c"])
    t.eq(t("SISMEMBER", k, "a"), 1)
    t.eq(t("SMISMEMBER", k, "a", "z"), [1, 0])
    t.eq(t("SCARD", k), 3)
    t.eq(t("SREM", k, "a"), 1)


@case("set", "SPOP / SRANDMEMBER / SMOVE")
def _(t):
    a, b = t.k("a"), t.k("b")
    t("SADD", a, "1", "2", "3", "4")
    t.eq(len(t("SPOP", a, 2)), 2)
    t.eq(len(t("SRANDMEMBER", a, -5)), 5)
    t("SADD", a, "9")
    t.eq(t("SMOVE", a, b, "9"), 1)
    t.eq(t("SISMEMBER", b, "9"), 1)


@case("set", "SUNION / SINTER / SDIFF 及 STORE / SINTERCARD")
def _(t):
    a, b = t.k("a"), t.k("b")
    t("SADD", a, "1", "2", "3")
    t("SADD", b, "2", "3", "4")
    t.eq(sorted(t("SUNION", a, b)), ["1", "2", "3", "4"])
    t.eq(sorted(t("SINTER", a, b)), ["2", "3"])
    t.eq(t("SDIFF", a, b), ["1"])
    t.eq(t("SUNIONSTORE", t.k("u"), a, b), 4)
    t.eq(t("SINTERSTORE", t.k("i"), a, b), 2)
    t.eq(t("SDIFFSTORE", t.k("d"), a, b), 1)
    t.eq(t("SINTERCARD", 2, a, b), 2)
    t.eq(t("SINTERCARD", 2, a, b, "LIMIT", 1), 1)


@case("set", "SSCAN")
def _(t):
    k = t.k("s")
    t("SADD", k, *["m%d" % i for i in range(30)])
    got, cur = set(), "0"
    while True:
        cur, ms = t("SSCAN", k, cur, "COUNT", 7)
        got.update(ms)
        if cur == "0":
            break
    t.eq(len(got), 30)


# =========================================================================== 用例：有序集合


@case("zset", "ZADD / ZSCORE / ZRANK / ZREVRANK / ZCARD / ZINCRBY / ZREM")
def _(t):
    k = t.k("z")
    t.eq(t("ZADD", k, 1, "a", 2, "b", 3, "c"), 3)
    t.eq(t("ZSCORE", k, "b"), "2")
    t.eq(t("ZRANK", k, "c"), 2)
    t.eq(t("ZREVRANK", k, "c"), 0)
    t.eq(t("ZCARD", k), 3)
    t.near(t("ZINCRBY", k, "1.5", "a"), 2.5, 1e-9)
    t.eq(t("ZREM", k, "a"), 1)
    t.eq(t("ZMSCORE", k, "b", "none"), ["2", None])


@case("zset", "ZADD NX / XX / GT / LT / CH / INCR")
def _(t):
    k = t.k("z")
    t("ZADD", k, 5, "a")
    t.eq(t("ZADD", k, "NX", 9, "a"), 0)
    t.eq(t("ZADD", k, "XX", "CH", 6, "a"), 1)
    t.eq(t("ZADD", k, "GT", "CH", 4, "a"), 0)
    t.eq(t("ZADD", k, "LT", "CH", 4, "a"), 1)
    t.eq(t("ZADD", k, "INCR", 2, "a"), "6")


@case("zset", "ZRANGE (BYSCORE / BYLEX / REV / LIMIT / WITHSCORES)")
def _(t):
    k = t.k("z")
    t("ZADD", k, 1, "a", 2, "b", 3, "c", 4, "d")
    t.eq(t("ZRANGE", k, 0, -1), ["a", "b", "c", "d"])
    t.eq(t("ZRANGE", k, 0, 1, "WITHSCORES"), ["a", "1", "b", "2"])
    t.eq(t("ZRANGE", k, "2", "3", "BYSCORE"), ["b", "c"])
    t.eq(t("ZRANGE", k, "(1", "+inf", "BYSCORE", "LIMIT", 0, 2), ["b", "c"])
    t.eq(t("ZRANGE", k, 0, -1, "REV"), ["d", "c", "b", "a"])
    t.eq(t("ZRANGE", k, "[a", "[c", "BYLEX"), ["a", "b", "c"])
    t.eq(t("ZRANGESTORE", t.k("dst"), k, 0, 1), 2)


@case("zset", "ZRANGEBYSCORE / ZREVRANGE / ZCOUNT / ZLEXCOUNT / ZRANGEBYLEX")
def _(t):
    k = t.k("z")
    t("ZADD", k, 0, "a", 0, "b", 0, "c")
    t.eq(t("ZRANGEBYSCORE", k, "-inf", "+inf"), ["a", "b", "c"])
    t.eq(t("ZREVRANGE", k, 0, 1), ["c", "b"])
    t.eq(t("ZCOUNT", k, 0, 0), 3)
    t.eq(t("ZLEXCOUNT", k, "[a", "[b"), 2)
    t.eq(t("ZRANGEBYLEX", k, "-", "+"), ["a", "b", "c"])
    t.eq(t("ZREVRANGEBYSCORE", k, "+inf", "-inf"), ["c", "b", "a"])


@case("zset", "ZPOPMIN / ZPOPMAX / BZPOPMIN / BZPOPMAX / ZMPOP / BZMPOP")
def _(t):
    k = t.k("z")
    t("ZADD", k, 1, "a", 2, "b", 3, "c", 4, "d", 5, "e")
    t.eq(t("ZPOPMIN", k), ["a", "1"])
    t.eq(t("ZPOPMAX", k), ["e", "5"])
    t.eq(t("BZPOPMIN", k, 1), [k, "b", "2"])
    t.eq(t("BZPOPMAX", k, 1), [k, "d", "4"])
    t("ZADD", k, 1, "x")
    t.eq(t("ZMPOP", 1, k, "MIN"), [k, [["x", "1"]]])
    t.eq(t("BZMPOP", 1, 1, k, "MIN"), [k, [["c", "3"]]])
    t.eq(t("BZPOPMIN", k, "0.1"), None)


@case("zset", "ZUNIONSTORE / ZINTERSTORE (WEIGHTS / AGGREGATE) / ZUNION / ZINTER / ZDIFF / ZINTERCARD")
def _(t):
    a, b = t.k("a"), t.k("b")
    t("ZADD", a, 1, "x", 2, "y")
    t("ZADD", b, 3, "y", 4, "z")
    t.eq(t("ZUNIONSTORE", t.k("u"), 2, a, b), 3)
    t.eq(t("ZSCORE", t.keys[-1], "y"), "5")
    t.eq(t("ZINTERSTORE", t.k("i"), 2, a, b, "WEIGHTS", 2, 3, "AGGREGATE", "MAX"), 1)
    t.eq(t("ZSCORE", t.keys[-1], "y"), "9")
    t.eq(t("ZUNION", 2, a, b), ["x", "z", "y"])
    t.eq(t("ZINTER", 2, a, b), ["y"])
    t.eq(t("ZDIFF", 2, a, b), ["x"])
    t.eq(t("ZINTERCARD", 2, a, b), 1)


@case("zset", "ZRANDMEMBER / ZSCAN / ZREMRANGEBYRANK / ZREMRANGEBYSCORE / ZREMRANGEBYLEX")
def _(t):
    k = t.k("z")
    t("ZADD", k, *[x for i in range(20) for x in (i, "m%02d" % i)])
    t.eq(len(t("ZRANDMEMBER", k, 3)), 3)
    cur, got = "0", 0
    while True:
        cur, flat = t("ZSCAN", k, cur, "COUNT", 5)
        got += len(flat) // 2
        if cur == "0":
            break
    t.eq(got, 20)
    t.eq(t("ZREMRANGEBYRANK", k, 0, 4), 5)
    t.eq(t("ZREMRANGEBYSCORE", k, 5, 9), 5)
    t.eq(t("ZREMRANGEBYLEX", k, "[m10", "[m14"), 5)
    t.eq(t("ZCARD", k), 5)


@case("zset", "ZRANK WITHSCORE", ext=True)
def _(t):
    k = t.k("z")
    t("ZADD", k, 1, "a")
    t.eq(t("ZRANK", k, "a", "WITHSCORE"), [0, "1"])


# =========================================================================== 用例：位图 / HLL / 地理


@case("bitmap", "SETBIT / GETBIT / BITCOUNT / BITPOS")
def _(t):
    k = t.k("b")
    t.eq(t("SETBIT", k, 7, 1), 0)
    t.eq(t("GETBIT", k, 7), 1)
    t.eq(t("SETBIT", k, 15, 1), 0)
    t.eq(t("BITCOUNT", k), 2)
    t.eq(t("BITCOUNT", k, 0, 0), 1)
    t.eq(t("BITCOUNT", k, 0, 7, "BIT"), 1)
    t.eq(t("BITPOS", k, 1), 7)


@case("bitmap", "BITOP AND / OR / XOR / NOT")
def _(t):
    a, b, d = t.k("a"), t.k("b"), t.k("d")
    t.c.call_raw("SET", a, b"\xf0")
    t.c.call_raw("SET", b, b"\x0f")
    t.eq(t("BITOP", "AND", d, a, b), 1)
    t.eq(t.c.call_raw("GET", d), b"\x00")
    t("BITOP", "OR", d, a, b)
    t.eq(t.c.call_raw("GET", d), b"\xff")
    t("BITOP", "XOR", d, a, b)
    t.eq(t.c.call_raw("GET", d), b"\xff")
    t("BITOP", "NOT", d, a)
    t.eq(t.c.call_raw("GET", d), b"\x0f")


@case("bitmap", "BITFIELD / BITFIELD_RO")
def _(t):
    k = t.k("bf")
    t.eq(t("BITFIELD", k, "SET", "u8", 0, 200, "GET", "u8", 0), [0, 200])
    t.eq(t("BITFIELD", k, "INCRBY", "u8", 0, 100, "OVERFLOW", "SAT", "INCRBY", "u8", 0, 100), [44, 144])
    t.eq(t("BITFIELD_RO", k, "GET", "u8", 0), [144])


@case("hll", "PFADD / PFCOUNT / PFMERGE")
def _(t):
    a, b = t.k("a"), t.k("b")
    t.eq(t("PFADD", a, "x", "y", "z"), 1)
    t.eq(t("PFADD", a, "x"), 0)
    t.eq(t("PFCOUNT", a), 3)
    t("PFADD", b, "z", "w")
    t.eq(t("PFMERGE", t.k("m"), a, b), "OK")
    t.eq(t("PFCOUNT", t.keys[-1]), 4)


@case("geo", "GEOADD / GEODIST / GEOPOS / GEOHASH")
def _(t):
    k = t.k("g")
    t.eq(t("GEOADD", k, 13.361389, 38.115556, "Palermo", 15.087269, 37.502669, "Catania"), 2)
    t.near(t("GEODIST", k, "Palermo", "Catania", "km"), 166.2742, 0.01)
    pos = t("GEOPOS", k, "Palermo", "none")
    t.near(pos[0][0], 13.361389, 1e-4)
    t.eq(pos[1], None)
    t.eq(t("GEOHASH", k, "Palermo"), ["sqc8b49rny0"])


@case("geo", "GEOSEARCH / GEOSEARCHSTORE / GEORADIUS / GEORADIUSBYMEMBER")
def _(t):
    k = t.k("g")
    t("GEOADD", k, 13.361389, 38.115556, "Palermo", 15.087269, 37.502669, "Catania")
    t.eq(t("GEOSEARCH", k, "FROMLONLAT", 15, 37, "BYRADIUS", 200, "km", "ASC"), ["Catania", "Palermo"])
    t.eq(t("GEOSEARCH", k, "FROMMEMBER", "Palermo", "BYBOX", 400, 400, "km", "ASC"), ["Palermo", "Catania"])
    t.eq(t("GEOSEARCHSTORE", t.k("dst"), k, "FROMLONLAT", 15, 37, "BYRADIUS", 100, "km"), 1)
    t.eq(t("GEORADIUS", k, 15, 37, 100, "km"), ["Catania"])
    t.eq(sorted(t("GEORADIUSBYMEMBER", k, "Palermo", 200, "km")), ["Catania", "Palermo"])


# =========================================================================== 用例：流


@case("stream", "XADD / XLEN / XRANGE / XREVRANGE / XDEL / XTRIM")
def _(t):
    k = t.k("x")
    t.eq(t("XADD", k, "1-1", "f", "v1"), "1-1")
    t.eq(t("XADD", k, "2-1", "f", "v2"), "2-1")
    t.eq(t("XADD", k, "3-1", "f", "v3"), "3-1")
    t.eq(t("XLEN", k), 3)
    t.eq(t("XRANGE", k, "-", "+"), [["1-1", ["f", "v1"]], ["2-1", ["f", "v2"]], ["3-1", ["f", "v3"]]])
    t.eq(t("XRANGE", k, "-", "+", "COUNT", 1), [["1-1", ["f", "v1"]]])
    t.eq(t("XREVRANGE", k, "+", "-", "COUNT", 1), [["3-1", ["f", "v3"]]])
    t.eq(t("XDEL", k, "2-1"), 1)
    t.eq(t("XTRIM", k, "MAXLEN", 1), 1)
    t.eq(t("XLEN", k), 1)
    t.ok(t("XADD", k, "*", "a", "b") != "", "XADD * 应返回自动 ID")


@case("stream", "XREAD（含 BLOCK）")
def _(t):
    k = t.k("x")
    t("XADD", k, "1-1", "f", "v")
    t.eq(t("XREAD", "COUNT", 1, "STREAMS", k, "0"), [[k, [["1-1", ["f", "v"]]]]])
    t.eq(t("XREAD", "BLOCK", 100, "STREAMS", k, "$"), None)


@case("stream", "消费组 XGROUP / XREADGROUP / XACK / XPENDING / XCLAIM / XAUTOCLAIM")
def _(t):
    k = t.k("x")
    t("XADD", k, "1-1", "f", "v1")
    t("XADD", k, "2-1", "f", "v2")
    t.eq(t("XGROUP", "CREATE", k, "g", "0"), "OK")
    t.eq(t("XREADGROUP", "GROUP", "g", "c1", "COUNT", 2, "STREAMS", k, ">"),
         [[k, [["1-1", ["f", "v1"]], ["2-1", ["f", "v2"]]]]])
    p = t("XPENDING", k, "g")
    t.eq(p[0], 2)
    t.eq(len(t("XPENDING", k, "g", "-", "+", 10)), 2)
    t.eq(len(t("XCLAIM", k, "g", "c2", 0, "1-1")), 1)
    r = t("XAUTOCLAIM", k, "g", "c3", 0, "0")
    t.eq(r[0], "0-0")
    t.eq(t("XACK", k, "g", "1-1", "2-1"), 2)
    t.eq(t("XGROUP", "CREATECONSUMER", k, "g", "c9"), 1)
    t.eq(t("XGROUP", "DELCONSUMER", k, "g", "c9"), 0)
    t.eq(t("XGROUP", "SETID", k, "g", "0"), "OK")
    t.eq(t("XGROUP", "DESTROY", k, "g"), 1)


@case("stream", "XINFO STREAM / GROUPS / CONSUMERS / XSETID")
def _(t):
    k = t.k("x")
    t("XADD", k, "1-1", "f", "v")
    t("XGROUP", "CREATE", k, "g", "0")
    t("XREADGROUP", "GROUP", "g", "c", "STREAMS", k, ">")
    t.eq(pairs(t("XINFO", "STREAM", k))["length"], 1)
    t.eq(pairs(t("XINFO", "GROUPS", k)[0])["name"], "g")
    t.eq(pairs(t("XINFO", "CONSUMERS", k, "g")[0])["name"], "c")
    t.eq(t("XSETID", k, "9-9"), "OK")


# =========================================================================== 用例：发布订阅


@case("pubsub", "SUBSCRIBE / PUBLISH / UNSUBSCRIBE")
def _(t):
    ch = t.k("ch")
    sub = t.conn()
    sub.send("SUBSCRIBE", ch)
    t.eq(_txt(sub.read()), ["subscribe", ch, 1])
    t.eq(t("PUBLISH", ch, "hi"), 1)
    t.eq(_txt(sub.read()), ["message", ch, "hi"])
    sub.send("UNSUBSCRIBE", ch)
    t.eq(_txt(sub.read()), ["unsubscribe", ch, 0])


@case("pubsub", "PSUBSCRIBE / PUNSUBSCRIBE")
def _(t):
    ch = t.k("pch")
    sub = t.conn()
    sub.send("PSUBSCRIBE", t.tag + ":pch*")
    t.eq(_txt(sub.read())[0], "psubscribe")
    t.eq(t("PUBLISH", ch, "x"), 1)
    t.eq(_txt(sub.read()), ["pmessage", t.tag + ":pch*", ch, "x"])
    sub.send("PUNSUBSCRIBE")
    t.eq(_txt(sub.read())[0], "punsubscribe")


@case("pubsub", "PUBSUB CHANNELS / NUMSUB / NUMPAT")
def _(t):
    ch = t.k("ch2")
    sub = t.conn()
    sub.send("SUBSCRIBE", ch)
    sub.read()
    t.ok(ch in t("PUBSUB", "CHANNELS", t.tag + ":*"), "PUBSUB CHANNELS 应包含频道")
    t.eq(t("PUBSUB", "NUMSUB", ch), [ch, 1])
    t.ok(isinstance(t.raw("PUBSUB", "NUMPAT"), int), "NUMPAT 应为整数")


@case("pubsub", "分片发布订阅 SSUBSCRIBE / SPUBLISH", ext=True)
def _(t):
    ch = t.k("sch")
    sub = t.conn()
    sub.send("SSUBSCRIBE", ch)
    t.eq(_txt(sub.read())[0], "ssubscribe")
    t.eq(t("SPUBLISH", ch, "m"), 1)
    t.eq(_txt(sub.read()), ["smessage", ch, "m"])


@case("pubsub", "键空间通知（需 --admin 才会改配置）", admin=True)
def _(t):
    old = t("CONFIG", "GET", "notify-keyspace-events")[1]
    try:
        t("CONFIG", "SET", "notify-keyspace-events", "KEA")
        sub = t.conn()
        k = t.k("kn")
        sub.send("PSUBSCRIBE", "__keyspace@0__:" + k)
        sub.read()
        t("SET", k, "v")
        m = _txt(sub.read())
        t.eq(m[0], "pmessage")
        t.eq(m[3], "set")
    finally:
        t("CONFIG", "SET", "notify-keyspace-events", old)


# =========================================================================== 用例：事务


@case("transaction", "MULTI / EXEC")
def _(t):
    k = t.k("tx")
    t.eq(t("MULTI"), "OK")
    t.eq(t("SET", k, 1), "QUEUED")
    t.eq(t("INCR", k), "QUEUED")
    t.eq(t("EXEC"), ["OK", 2])


@case("transaction", "DISCARD")
def _(t):
    k = t.k("tx")
    t("MULTI")
    t("SET", k, 1)
    t.eq(t("DISCARD"), "OK")
    t.eq(t("EXISTS", k), 0)


@case("transaction", "WATCH 冲突时 EXEC 返回空")
def _(t):
    k = t.k("tx")
    t("SET", k, 1)
    t.eq(t("WATCH", k), "OK")
    t.conn().call("SET", k, 2)
    t("MULTI")
    t("SET", k, 3)
    t.eq(t("EXEC"), None)
    t.eq(t("GET", k), "2")
    t("WATCH", k)
    t.eq(t("UNWATCH"), "OK")


@case("transaction", "事务内语法错误导致 EXECABORT")
def _(t):
    t("MULTI")
    try:
        t.c.call_raw("SET")
    except RespError:
        pass
    t.raises("EXECABORT", "EXEC")


# =========================================================================== 用例：脚本与函数


@case("script", "EVAL / EVALSHA / SCRIPT LOAD / EXISTS / FLUSH")
def _(t):
    k = t.k("lua")
    t.eq(t("EVAL", "return redis.call('SET', KEYS[1], ARGV[1])", 1, k, "v"), "OK")
    t.eq(t("EVAL", "return redis.call('GET', KEYS[1])", 1, k), "v")
    sha = t("SCRIPT", "LOAD", "return {KEYS[1], ARGV[1], 42}")
    t.eq(len(sha), 40)
    t.eq(t("EVALSHA", sha, 1, k, "a"), [k, "a", 42])
    t.eq(t("SCRIPT", "EXISTS", sha, "0" * 40), [1, 0])
    t.eq(t("SCRIPT", "FLUSH"), "OK")
    t.raises("NOSCRIPT", "EVALSHA", sha, 1, k, "a")


@case("script", "EVAL_RO / EVALSHA_RO")
def _(t):
    k = t.k("lua")
    t("SET", k, "v")
    t.eq(t("EVAL_RO", "return redis.call('GET', KEYS[1])", 1, k), "v")
    t.raises("not allowed", "EVAL_RO", "return redis.call('SET', KEYS[1], 1)", 1, k)
    sha = t("SCRIPT", "LOAD", "return 1")
    t.eq(t("EVALSHA_RO", sha, 0), 1)


@case("script", "Lua 内置库 cjson / cmsgpack / bit / struct")
def _(t):
    t.eq(t("EVAL", "return cjson.encode({1,2,3})", 0), "[1,2,3]")
    t.eq(t("EVAL", "return cjson.decode('{\"a\":5}').a", 0), 5)
    t.ok(len(t.c.call_raw("EVAL", "return cmsgpack.pack({1,2})", 0)) > 0, "cmsgpack.pack")
    t.eq(t("EVAL", "return bit.band(12, 10)", 0), 8)
    t.eq(t("EVAL", "return struct.pack('>I', 1)", 0), "\x00\x00\x00\x01")


@case("script", "Lua 错误处理 redis.pcall / redis.error_reply / redis.status_reply")
def _(t):
    t.eq(t("EVAL", "return redis.status_reply('FINE')", 0), "FINE")
    t.raises("custom", "EVAL", "return redis.error_reply('custom')", 0)
    t.raises("script", "EVAL", "return redis.call('NOSUCHCMD')", 0)


@case("script", "FUNCTION LOAD / FCALL / FCALL_RO / LIST / DELETE")
def _(t):
    lib = "cc%s" % "".join(random.choice("abcdef0123456789") for _ in range(8))
    code = ("#!lua name=%s\n"
            "redis.register_function('%s_get', function(keys, args) return redis.call('GET', keys[1]) end)\n"
            "redis.register_function{function_name='%s_ro', callback=function(keys, args) "
            "return redis.call('GET', keys[1]) end, flags={'no-writes'}}\n") % (lib, lib, lib)
    k = t.k("fn")
    try:
        t.eq(t("FUNCTION", "LOAD", code), lib)
        t("SET", k, "v")
        t.eq(t("FCALL", lib + "_get", 1, k), "v")
        t.eq(t("FCALL_RO", lib + "_ro", 1, k), "v")
        t.ok(lib in str(t("FUNCTION", "LIST")), "FUNCTION LIST 应包含库")
        t.eq(t("FUNCTION", "DELETE", lib), "OK")
    finally:
        try:
            t.c.call_raw("FUNCTION", "DELETE", lib)
        except RespError:
            pass


# =========================================================================== 用例：服务器


@case("server", "INFO（各分段与关键字段）")
def _(t):
    srv = t.info_sec("server")
    t.ok("redis_version" in srv, "INFO server 应保留 redis_version（给客户端 / 监控读取）")
    t.ok("redis_mode" in srv, "INFO server 应有 redis_mode")
    for sec, key in (("memory", "used_memory"), ("stats", "total_commands_processed"),
                     ("clients", "connected_clients"), ("persistence", "loading"),
                     ("replication", "role"), ("keyspace", None), ("commandstats", None)):
        out = t("INFO", sec)
        t.ok(out.startswith("#"), "INFO %s 应返回分段" % sec)
        if key:
            t.ok(key + ":" in out, "INFO %s 应包含 %s" % (sec, key))
    ver = [k + "=" + v for k, v in srv.items() if k.endswith("_version") or k in ("os", "arch_bits")]
    return "；".join(ver)


@case("server", "INFO 全量 / everything / default")
def _(t):
    for arg in (None, "ALL", "EVERYTHING", "DEFAULT"):
        out = t("INFO") if arg is None else t("INFO", arg)
        t.ok("redis_version:" in out, "INFO %s 应包含 redis_version" % arg)


@case("server", "DBSIZE / TIME / LASTSAVE")
def _(t):
    t.ok(t.raw("DBSIZE") >= 0, "DBSIZE")
    sec, usec = t("TIME")
    t.near(sec, time.time(), 10, "TIME 秒")
    t.ok(0 <= int(usec) < 1000000, "TIME 微秒")
    t.ok(t.raw("LASTSAVE") > 0, "LASTSAVE")


@case("server", "COMMAND COUNT / INFO / GETKEYS / LIST / DOCS")
def _(t):
    n = t.raw("COMMAND", "COUNT")
    t.ok(n > 150, "COMMAND COUNT 应 > 150，实际 %s" % n)
    info = t("COMMAND", "INFO", "get")
    t.eq(info[0][0], "get")
    t.eq(t("COMMAND", "GETKEYS", "SET", "a", "b"), ["a"])
    names = t("COMMAND", "LIST")
    t.ok("get" in names and "xadd" in names, "COMMAND LIST 应包含 get / xadd")
    docs = t("COMMAND", "DOCS", "get")
    t.ok(len(docs) > 0, "COMMAND DOCS get")
    return "命令总数 %s" % n


@case("server", "CONFIG GET")
def _(t):
    t.eq(t("CONFIG", "GET", "maxmemory")[0], "maxmemory")
    r = pairs(t("CONFIG", "GET", "max*"))
    t.ok("maxmemory" in r and "maxclients" in r, "CONFIG GET max* 应匹配多项")
    for p in ("appendonly", "save", "maxmemory-policy", "lazyfree-lazy-expire", "protected-mode",
              "loglevel", "logfile", "pidfile", "dir", "port", "bind", "tcp-backlog", "hz"):
        t.ok(len(t("CONFIG", "GET", p)) == 2, "CONFIG GET %s 应返回参数" % p)


@case("server", "MEMORY USAGE / STATS / DOCTOR / MALLOC-STATS")
def _(t):
    k = t.k("m")
    t("SET", k, "v" * 100)
    t.ok(t.raw("MEMORY", "USAGE", k) > 100, "MEMORY USAGE")
    t.ok(len(t("MEMORY", "STATS")) > 0, "MEMORY STATS")
    t.ok(isinstance(t("MEMORY", "DOCTOR"), str), "MEMORY DOCTOR")


@case("server", "SLOWLOG LEN / GET")
def _(t):
    t.ok(t.raw("SLOWLOG", "LEN") >= 0, "SLOWLOG LEN")
    t.ok(isinstance(t("SLOWLOG", "GET", 5), list), "SLOWLOG GET")


@case("server", "LATENCY LATEST / HISTORY / DOCTOR")
def _(t):
    t.ok(isinstance(t("LATENCY", "LATEST"), list), "LATENCY LATEST")
    t.ok(isinstance(t("LATENCY", "DOCTOR"), str), "LATENCY DOCTOR")


@case("server", "ROLE / WAIT")
def _(t):
    role = t("ROLE")
    t.ok(role[0] in ("master", "slave", "sentinel"), "ROLE 应为 master / slave")
    t.ok(isinstance(t.raw("WAIT", 0, 10), int), "WAIT 应返回整数")


@case("server", "LOLWUT / MODULE LIST / OBJECT HELP")
def _(t):
    t.ok(len(t("LOLWUT")) > 0, "LOLWUT")
    t.ok(isinstance(t("MODULE", "LIST"), list), "MODULE LIST")
    t.ok(len(t("OBJECT", "HELP")) > 0, "OBJECT HELP")


@case("server", "WAITAOF", ext=True)
def _(t):
    t.ok(isinstance(t("WAITAOF", 0, 0, 0), list), "WAITAOF")


# =========================================================================== 用例：管理（--admin）


@case("admin", "CONFIG SET 往返并还原", admin=True)
def _(t):
    old = t("CONFIG", "GET", "maxmemory-samples")[1]
    new = "7" if old != "7" else "6"
    try:
        t.eq(t("CONFIG", "SET", "maxmemory-samples", new), "OK")
        t.eq(t("CONFIG", "GET", "maxmemory-samples")[1], new)
    finally:
        t("CONFIG", "SET", "maxmemory-samples", old)
    t.eq(t("CONFIG", "GET", "maxmemory-samples")[1], old)


@case("admin", "CONFIG SET lazyfree-lazy-expire 热修改并还原", admin=True)
def _(t):
    old = t("CONFIG", "GET", "lazyfree-lazy-expire")[1]
    new = "no" if old == "yes" else "yes"
    try:
        t.eq(t("CONFIG", "SET", "lazyfree-lazy-expire", new), "OK")
        t.eq(t("CONFIG", "GET", "lazyfree-lazy-expire")[1], new)
    finally:
        t("CONFIG", "SET", "lazyfree-lazy-expire", old)


@case("admin", "CONFIG REWRITE（产品的热生效路径）", admin=True)
def _(t):
    try:
        t.eq(t("CONFIG", "REWRITE"), "OK")
    except RespError as e:
        if "without a config file" in str(e).lower():
            return "SKIP: 服务端没有使用配置文件启动"
        raise


@case("admin", "CONFIG RESETSTAT", admin=True)
def _(t):
    t.eq(t("CONFIG", "RESETSTAT"), "OK")


@case("admin", "BGSAVE 并等待完成", admin=True)
def _(t):
    t("SET", t.k("bg"), "v")
    try:
        r = t("BGSAVE")
    except RespError as e:
        if "already in progress" in str(e).lower():
            r = "already"
        else:
            raise
    t.ok(r == "already" or "background saving started" in r.lower(), "BGSAVE 返回：%s" % r)
    for _i in range(100):
        if t.info_sec("persistence").get("rdb_bgsave_in_progress") == "0":
            break
        time.sleep(0.1)
    p = t.info_sec("persistence")
    t.eq(p.get("rdb_bgsave_in_progress"), "0")
    t.eq(p.get("rdb_last_bgsave_status"), "ok")


@case("admin", "BGREWRITEAOF", admin=True)
def _(t):
    if t.info_sec("persistence").get("aof_enabled") != "1":
        return "SKIP: 未开启 AOF"
    try:
        t("BGREWRITEAOF")
    except RespError as e:
        if "already in progress" not in str(e).lower() and "scheduled" not in str(e).lower():
            raise


@case("admin", "ACL 临时用户 SETUSER / GETUSER / WHOAMI / LIST / CAT / GENPASS / DELUSER / LOG", admin=True)
def _(t):
    user = "cmdcheck_" + "".join(random.choice("abcdef0123456789") for _ in range(6))
    try:
        t.eq(t("ACL", "SETUSER", user, "on", ">tmp-pass-1", "~" + t.tag + ":*", "+get", "+set", "+ping"), "OK")
        t.ok(user in str(t("ACL", "LIST")), "ACL LIST 应包含临时用户")
        t.ok(len(t("ACL", "GETUSER", user)) > 0, "ACL GETUSER")
        c = Conn(t.target.host, t.target.port, user, "tmp-pass-1", t.target.tls, t.target.timeout)
        try:
            t.eq(c.call("PING"), "PONG")
            t.eq(c.call("SET", t.k("acl"), "v"), "OK")
            try:
                c.call_raw("GET", "other-key-without-permission")
                raise AssertionError("越权访问其他键应被拒绝")
            except RespError as e:
                t.ok("noperm" in str(e).lower(), "应返回 NOPERM，实际 %s" % e)
            try:
                c.call_raw("FLUSHDB")
                raise AssertionError("未授权命令应被拒绝")
            except RespError as e:
                t.ok("noperm" in str(e).lower(), "应返回 NOPERM，实际 %s" % e)
        finally:
            c.close()
        t.ok(len(t("ACL", "CAT")) > 5, "ACL CAT")
        t.eq(len(t("ACL", "GENPASS")), 64)
        t.ok(isinstance(t("ACL", "LOG"), list), "ACL LOG")
        t.ok(len(t("ACL", "WHOAMI")) > 0, "ACL WHOAMI")
    finally:
        try:
            t.c.call_raw("ACL", "DELUSER", user)
        except RespError:
            pass


@case("admin", "CLIENT PAUSE / UNPAUSE", admin=True)
def _(t):
    t.eq(t("CLIENT", "PAUSE", 50, "WRITE"), "OK")
    t.eq(t("CLIENT", "UNPAUSE"), "OK")


# =========================================================================== 用例：集群


@case("cluster", "CLUSTER（非集群时应明确拒绝；集群时校验基本信息）")
def _(t):
    enabled = t.info_sec("cluster").get("cluster_enabled") == "1"
    if not enabled:
        t.raises("cluster support disabled", "CLUSTER", "INFO")
        return "单机模式：CLUSTER 命令被明确拒绝"
    info = t("CLUSTER", "INFO")
    t.ok("cluster_state:ok" in info, "cluster_state 应为 ok")
    t.ok(len(t("CLUSTER", "NODES")) > 0, "CLUSTER NODES")
    t.ok(len(t("CLUSTER", "MYID")) == 40, "CLUSTER MYID")
    t.eq(t.raw("CLUSTER", "KEYSLOT", "foo"), 12182)
    t.ok(isinstance(t("CLUSTER", "SLOTS"), list), "CLUSTER SLOTS")
    t.ok(isinstance(t("CLUSTER", "SHARDS"), list), "CLUSTER SHARDS")
    t.ok(isinstance(t.raw("CLUSTER", "COUNTKEYSINSLOT", 0), int), "COUNTKEYSINSLOT")
    return "集群模式"


# --------------------------------------------------------------------------- 驱动

OK_STATES = ("PASS", "SKIP", "NOPERM", "NA")
SYMBOL = {"PASS": "✔", "FAIL": "✘", "UNSUPPORTED": "✘", "NA": "–", "NOPERM": "⊘", "SKIP": "·"}


def make_tls(args):
    if not args.tls:
        return None
    ctx = ssl.create_default_context(cafile=args.tls_ca) if args.tls_ca else ssl.create_default_context()
    if args.tls_insecure:
        ctx.check_hostname = False
        ctx.verify_mode = ssl.CERT_NONE
    if args.tls_cert:
        ctx.load_cert_chain(args.tls_cert, args.tls_key)
    return ctx


def parse_target(spec):
    name, _, addr = spec.partition("=")
    if not addr:
        name, addr = "target", spec
    host, _, port = addr.rpartition(":")
    if not host:
        host, port = addr, "6379"
    return name, host, int(port)


def server_fingerprint(tg):
    out = {}
    try:
        info = tg.main.call("INFO", "server")
        for line in info.splitlines():
            if ":" in line and not line.startswith("#"):
                k, v = line.split(":", 1)
                if k.endswith("_version") or k in ("redis_mode", "os", "arch_bits", "process_id"):
                    out[k] = v
        out["command_count"] = tg.main.call_raw("COMMAND", "COUNT")
    except Exception as e:
        out["error"] = str(e)
    return out


def command_names(tg):
    try:
        return set(tg.main.call("COMMAND", "LIST"))
    except Exception:
        try:
            return {c[0] for c in tg.main.call("COMMAND")}
        except Exception:
            return None


def main():
    ap = argparse.ArgumentParser(description="Redis 协议命令支持情况验证（可对比多个实例）",
                                 formatter_class=argparse.RawDescriptionHelpFormatter, epilog=__doc__)
    ap.add_argument("-t", "--target", action="append", metavar="[名称=]主机:端口",
                    help="被测实例，可重复；默认 local=127.0.0.1:6379")
    ap.add_argument("--group", help="只跑指定分组，逗号分隔")
    ap.add_argument("--only", help="只跑名称包含该子串的用例（不区分大小写）")
    ap.add_argument("--admin", action="store_true", help="同时运行会修改服务端状态的管理类用例")
    ap.add_argument("--list", action="store_true", help="列出全部用例后退出")
    ap.add_argument("--json", metavar="文件", help="把完整结果写入 JSON 文件")
    ap.add_argument("--timeout", type=float, default=5.0, help="单次读写超时秒数，默认 5")
    ap.add_argument("--strict-ext", action="store_true", help="扩展命令不支持也算失败")
    ap.add_argument("-v", "--verbose", action="store_true", help="显示每个用例的通过详情")
    ap.add_argument("--tls", action="store_true")
    ap.add_argument("--tls-ca")
    ap.add_argument("--tls-cert")
    ap.add_argument("--tls-key")
    ap.add_argument("--tls-insecure", action="store_true", help="不校验服务端证书（仅测试用）")
    args = ap.parse_args()

    if args.list:
        for g, n, _, ext, admin in CASES:
            print("%-12s %s%s%s" % (g, n, "  [扩展]" if ext else "", "  [--admin]" if admin else ""))
        print("共 %d 个用例" % len(CASES))
        return 0

    groups = set(x.strip() for x in args.group.split(",")) if args.group else None
    selected = [c for c in CASES
                if (not groups or c[0] in groups)
                and (not args.only or args.only.lower() in c[1].lower())]
    if not selected:
        print("没有匹配的用例", file=sys.stderr)
        return 2

    tls = make_tls(args)
    user = os.environ.get("CMDCHECK_USER") or None
    targets = []
    for spec in (args.target or ["local=127.0.0.1:6379"]):
        name, host, port = parse_target(spec)
        pw = os.environ.get("CMDCHECK_PASSWORD_" + name.upper().replace("-", "_")) \
            or os.environ.get("CMDCHECK_PASSWORD") or None
        try:
            targets.append(Target(name, host, port, user, pw, tls, args.timeout))
        except (OSError, RespError) as e:
            print("无法连接 %s (%s:%s)：%s" % (name, host, port, e), file=sys.stderr)
            return 2

    seed = "%08x" % random.getrandbits(32)
    results = {tg.name: {} for tg in targets}
    order = []
    fingerprints = {}
    cmdsets = {}
    for tg in targets:
        fingerprints[tg.name] = server_fingerprint(tg)
        cmdsets[tg.name] = command_names(tg)

    print("测试前缀：{cmdcheck-%s}:*（用例结束即清理）" % seed)
    for tg in targets:
        print("目标 %-10s %s:%s  %s" % (tg.name, tg.host, tg.port,
                                        " ".join("%s=%s" % kv for kv in fingerprints[tg.name].items())))
    print()

    t0 = time.time()
    for g, n, f, ext, admin in selected:
        key = "%s / %s" % (g, n)
        order.append((key, g, n, ext))
        for tg in targets:
            if admin and not args.admin:
                results[tg.name][key] = ("SKIP", "管理类用例，加 --admin 才运行", 0.0)
                continue
            results[tg.name][key] = run_case(tg, g, n, f, ext, args.admin, seed)

    # ---- 逐条输出
    width = max(len(k) for k, *_ in order)
    names = [tg.name for tg in targets]
    print("%-*s  %s" % (width, "用例", "  ".join("%-12s" % x for x in names)))
    print("-" * (width + 2 + 14 * len(names)))
    for key, g, n, ext in order:
        sts = [results[nm][key][0] for nm in names]
        differ = len(set(sts)) > 1
        show = args.verbose or differ or any(s in ("FAIL", "UNSUPPORTED") for s in sts)
        if not show and not args.verbose:
            continue
        cells = "  ".join("%-12s" % (SYMBOL[s] + " " + s) for s in sts)
        print("%-*s  %s%s" % (width, key, cells, "  ← 结果不一致" if differ else ""))
        for nm in names:
            st, detail, _ = results[nm][key]
            if detail and (st in ("FAIL", "UNSUPPORTED", "NOPERM") or args.verbose):
                print("    [%s] %s" % (nm, detail))

    # ---- 汇总
    print()
    print("按分组汇总（通过/总数；NA = 扩展命令不支持，不算失败）")
    groups_seen = []
    for key, g, n, ext in order:
        if g not in groups_seen:
            groups_seen.append(g)
    print("%-12s %s" % ("分组", "  ".join("%-18s" % x for x in names)))
    for g in groups_seen:
        cells = []
        for nm in names:
            rs = [results[nm][k][0] for k, gg, _, _ in order if gg == g]
            cells.append("%-18s" % ("%d/%d" % (rs.count("PASS"), len(rs)) +
                                    (" FAIL:%d" % (rs.count("FAIL") + rs.count("UNSUPPORTED"))
                                     if rs.count("FAIL") + rs.count("UNSUPPORTED") else "") +
                                    (" NA:%d" % rs.count("NA") if rs.count("NA") else "")))
        print("%-12s %s" % (g, "  ".join(cells)))

    bad = {}
    for nm in names:
        sts = [s for s, _, _ in results[nm].values()]
        bad[nm] = sts.count("FAIL") + sts.count("UNSUPPORTED") + (sts.count("NA") if args.strict_ext else 0)
        print("%-10s PASS=%d FAIL=%d UNSUPPORTED=%d NA=%d NOPERM=%d SKIP=%d" % (
            nm, sts.count("PASS"), sts.count("FAIL"), sts.count("UNSUPPORTED"), sts.count("NA"),
            sts.count("NOPERM"), sts.count("SKIP")))

    # ---- 命令集合对比
    if len(targets) >= 2:
        base = names[0]
        for nm in names[1:]:
            a, b = cmdsets[base], cmdsets[nm]
            if a is None or b is None:
                continue
            print("\n命令集合对比（COMMAND LIST）：%s %d 条，%s %d 条" % (base, len(a), nm, len(b)))
            if a - b:
                print("  仅 %s 有：%s" % (base, " ".join(sorted(a - b))))
            if b - a:
                print("  仅 %s 有：%s" % (nm, " ".join(sorted(b - a))))
            if a == b:
                print("  完全一致")

    print("\n耗时 %.1fs" % (time.time() - t0))

    if args.json:
        data = {"prefix": seed, "fingerprints": fingerprints,
                "commands": {nm: sorted(cmdsets[nm]) if cmdsets[nm] else None for nm in names},
                "results": {nm: {k: {"status": v[0], "detail": v[1], "seconds": round(v[2], 3)}
                                 for k, v in results[nm].items()} for nm in names}}
        with open(args.json, "w", encoding="utf-8") as fh:
            json.dump(data, fh, ensure_ascii=False, indent=2)
        print("结果已写入 %s" % args.json)

    for tg in targets:
        tg.close()
    return 1 if any(bad.values()) else 0


if __name__ == "__main__":
    sys.exit(main())

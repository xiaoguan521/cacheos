#!/usr/bin/env python3
"""sycache 行为等价与互通测试（规格书 5.6）。只用 Python 标准库，自带最小 RESP 客户端。

覆盖：单机命令与脚本、主从复制、原生集群（MOVED 重定向）、哨兵自动切换；
设置 VALKEY_BIN 时额外验证与上游 Valkey 的混合复制与 RDB 互读（证明协议与磁盘格式未被改名破坏）。

用法：
  SYCACHE_BIN=/opt/sycache/bin [VALKEY_BIN=<上游 valkey src 目录>] python3 test/compat/compat_test.py -v
"""
import os
import shutil
import socket
import ssl
import subprocess
import tempfile
import time
import unittest

SYCACHE_BIN = os.environ.get("SYCACHE_BIN", "/opt/sycache/bin")
VALKEY_BIN = os.environ.get("VALKEY_BIN", "")
BASE_PORT = int(os.environ.get("COMPAT_BASE_PORT", "18100"))


# ---------------- 最小 RESP2 客户端 ----------------
class RespError(Exception):
    pass


class Conn:
    def __init__(self, port, host="127.0.0.1", timeout=5, tls=None):
        self.sock = socket.create_connection((host, port), timeout=timeout)
        if tls is not None:
            self.sock = tls.wrap_socket(self.sock, server_hostname="localhost")
        self.buf = b""

    def close(self):
        self.sock.close()

    def __del__(self):  # 测试里大量一次性连接，回收时关闭，避免 ResourceWarning
        try:
            self.sock.close()
        except Exception:  # noqa: BLE001
            pass

    def _line(self):
        while b"\r\n" not in self.buf:
            chunk = self.sock.recv(65536)
            if not chunk:
                raise ConnectionError("connection closed")
            self.buf += chunk
        line, self.buf = self.buf.split(b"\r\n", 1)
        return line

    def _read(self):
        line = self._line()
        t, rest = line[:1], line[1:]
        if t == b"+":
            return rest.decode()
        if t == b"-":
            raise RespError(rest.decode())
        if t == b":":
            return int(rest)
        if t == b"$":
            n = int(rest)
            if n < 0:
                return None
            while len(self.buf) < n + 2:
                self.buf += self.sock.recv(65536)
            data, self.buf = self.buf[:n], self.buf[n + 2:]
            return data.decode(errors="replace")
        if t == b"*":
            n = int(rest)
            return None if n < 0 else [self._read() for _ in range(n)]
        raise RespError(f"unexpected reply type {line!r}")

    def cmd(self, *args):
        out = [b"*%d\r\n" % len(args)]
        for a in args:
            b = a if isinstance(a, bytes) else str(a).encode()
            out.append(b"$%d\r\n%s\r\n" % (len(b), b))
        self.sock.sendall(b"".join(out))
        return self._read()


def info(conn, section="server"):
    return dict(line.split(":", 1) for line in conn.cmd("INFO", section).splitlines() if ":" in line)


def wait_until(fn, timeout=30, step=0.2, what="condition"):
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        try:
            if fn():
                return
        except Exception as e:  # noqa: BLE001 —— 等待期间的连接错误属正常
            last = e
        time.sleep(step)
    raise AssertionError(f"timeout waiting for {what} (last error: {last})")


# ---------------- 进程管理 ----------------
class Cluster:
    """记录本测试启动的所有进程，测试结束统一关闭。"""

    def __init__(self):
        self.dir = tempfile.mkdtemp(prefix="sycache-compat-")
        self.procs = []
        self.logs = []

    def start(self, binary, port, *extra, conf_lines=()):
        d = os.path.join(self.dir, str(port))
        os.makedirs(d, exist_ok=True)
        conf = os.path.join(d, "node.conf")
        with open(conf, "w") as f:
            f.write(f"port {port}\nbind 127.0.0.1\ndir {d}\nsave \"\"\nappendonly no\n")
            for line in conf_lines:
                f.write(line + "\n")
        log = open(os.path.join(d, "out.log"), "w")
        self.logs.append(log)
        p = subprocess.Popen([binary, conf, *extra], stdout=log, stderr=subprocess.STDOUT, cwd=d)
        self.procs.append(p)
        wait_until(lambda: Conn(port).cmd("PING") == "PONG", timeout=10, what=f"{binary} on {port}")
        return d

    def stop(self):
        for p in self.procs:
            p.terminate()
        for p in self.procs:
            try:
                p.wait(timeout=10)
            except subprocess.TimeoutExpired:
                p.kill()
        for log in self.logs:
            log.close()
        shutil.rmtree(self.dir, ignore_errors=True)


def sy(name):
    return os.path.join(SYCACHE_BIN, name)


def vk(name):
    return os.path.join(VALKEY_BIN, name)


class CompatBase(unittest.TestCase):
    port = BASE_PORT

    @classmethod
    def next_port(cls):
        CompatBase.port += 1
        return CompatBase.port

    def setUp(self):
        self.env = Cluster()

    def tearDown(self):
        self.env.stop()


class StandaloneTest(CompatBase):
    def test_commands_and_scripts(self):
        p = self.next_port()
        self.env.start(sy("sycache-server"), p)
        c = Conn(p)
        self.assertEqual(c.cmd("SET", "k", "v", "EX", "100"), "OK")
        self.assertEqual(c.cmd("GET", "k"), "v")
        self.assertGreater(c.cmd("TTL", "k"), 0)
        self.assertEqual(c.cmd("HSET", "h", "a", "1", "b", "2"), 2)
        self.assertEqual(sorted(c.cmd("HKEYS", "h")), ["a", "b"])
        self.assertEqual(c.cmd("RPUSH", "l", "x", "y", "z"), 3)
        self.assertEqual(c.cmd("LRANGE", "l", "0", "-1"), ["x", "y", "z"])
        self.assertEqual(c.cmd("ZADD", "z", "1", "a", "2", "b"), 2)
        self.assertEqual(c.cmd("XADD", "s", "1-1", "f", "v"), "1-1")
        self.assertEqual(c.cmd("MULTI"), "OK")
        c.cmd("INCR", "n")
        c.cmd("INCR", "n")
        self.assertEqual(c.cmd("EXEC"), [1, 2])
        # Lua：redis.* 与 server.* 两套入口、版本变量保持上游语义
        self.assertEqual(c.cmd("EVAL", "return redis.call('GET', KEYS[1])", "1", "k"), "v")
        self.assertEqual(c.cmd("EVAL", "return server.call('GET', KEYS[1])", "1", "k"), "v")
        self.assertEqual(c.cmd("EVAL", "return server.VALKEY_VERSION_NUM >= 0x00090000", "0"), 1)
        lib = "#!lua name=compat\nserver.register_function('echo1', function(keys, args) return args[1] end)"
        self.assertEqual(c.cmd("FUNCTION", "LOAD", lib), "compat")
        self.assertEqual(c.cmd("FCALL", "echo1", "0", "hi"), "hi")
        # ACL：危险命令可按类别禁用（规格书 9.1 基线依赖此能力）
        self.assertEqual(c.cmd("ACL", "SETUSER", "app", "on", ">pw", "~*", "+@all", "-@dangerous"), "OK")
        a = Conn(p)
        self.assertEqual(a.cmd("AUTH", "app", "pw"), "OK")
        with self.assertRaises(RespError):
            a.cmd("FLUSHALL")
        self.assertEqual(info(c)["server_name"], "sycache")

    def test_replication(self):
        p1, p2 = self.next_port(), self.next_port()
        self.env.start(sy("sycache-server"), p1)
        self.env.start(sy("sycache-server"), p2, conf_lines=[f"replicaof 127.0.0.1 {p1}"])
        prim, rep = Conn(p1), Conn(p2)
        wait_until(lambda: info(rep, "replication").get("master_link_status") == "up", what="replica link")
        prim.cmd("SET", "rk", "rv")
        wait_until(lambda: rep.cmd("GET", "rk") == "rv", what="replicated key")
        with self.assertRaises(RespError):
            rep.cmd("SET", "x", "1")  # 从节点只读


class ClusterTest(CompatBase):
    def test_native_cluster(self):
        ports = [self.next_port() for _ in range(3)]
        for p in ports:
            self.env.start(sy("sycache-server"), p, conf_lines=["cluster-enabled yes", "cluster-node-timeout 3000"])
        out = subprocess.run(
            [sy("sycache-cli"), "--cluster", "create", *[f"127.0.0.1:{p}" for p in ports], "--cluster-yes"],
            capture_output=True, text=True, timeout=60)
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        self.assertNotIn("valkey", (out.stdout + out.stderr).lower())
        wait_until(lambda: all("cluster_state:ok" in Conn(p).cmd("CLUSTER", "INFO") for p in ports),
                   what="cluster_state ok on every node")
        # 普通客户端按 MOVED 重定向写入 100 个键，三个分片都有数据
        for i in range(100):
            port = ports[0]
            for _ in range(3):
                try:
                    Conn(port).cmd("SET", f"key:{i}", str(i))
                    break
                except RespError as e:
                    self.assertTrue(str(e).startswith("MOVED "), e)  # 错误前缀不得改名
                    port = int(str(e).split()[2].rsplit(":", 1)[1])
        sizes = [Conn(p).cmd("DBSIZE") for p in ports]
        self.assertEqual(sum(sizes), 100)
        self.assertTrue(all(s > 0 for s in sizes), sizes)
        check = subprocess.run([sy("sycache-cli"), "--cluster", "check", f"127.0.0.1:{ports[0]}"],
                               capture_output=True, text=True, timeout=30)
        self.assertIn("All 16384 slots covered", check.stdout)


class SentinelTest(CompatBase):
    def test_sentinel_failover(self):
        p1, p2 = self.next_port(), self.next_port()
        sp = [self.next_port() for _ in range(3)]
        self.env.start(sy("sycache-server"), p1)
        self.env.start(sy("sycache-server"), p2, conf_lines=[f"replicaof 127.0.0.1 {p1}"])
        wait_until(lambda: info(Conn(p2), "replication").get("master_link_status") == "up", what="replica link")
        for s in sp:
            self.env.start(sy("sycache-sentinel"), s, conf_lines=[
                f"sentinel monitor mymaster 127.0.0.1 {p1} 2",
                "sentinel down-after-milliseconds mymaster 2000",
                "sentinel failover-timeout mymaster 10000"])
        s0 = Conn(sp[0])
        self.assertEqual(s0.cmd("SENTINEL", "GET-MASTER-ADDR-BY-NAME", "mymaster"), ["127.0.0.1", str(p1)])
        wait_until(lambda: int(s0.cmd("SENTINEL", "MASTER", "mymaster")[
            s0.cmd("SENTINEL", "MASTER", "mymaster").index("num-other-sentinels") + 1]) == 2,
            what="sentinels discover each other")
        wait_until(lambda: int(s0.cmd("SENTINEL", "MASTER", "mymaster")[
            s0.cmd("SENTINEL", "MASTER", "mymaster").index("num-slaves") + 1]) == 1, what="replica discovered")
        self.assertEqual(s0.cmd("SENTINEL", "FAILOVER", "mymaster"), "OK")
        wait_until(lambda: s0.cmd("SENTINEL", "GET-MASTER-ADDR-BY-NAME", "mymaster") == ["127.0.0.1", str(p2)],
                   what="failover to replica")
        wait_until(lambda: info(Conn(p2), "replication")["role"] == "master", what="new primary role")


def make_certs(d):
    """用 openssl 命令行生成测试用 CA、服务端与客户端证书（RSA 2048，1 天有效）。"""
    def run(*args):
        subprocess.run(["openssl", *args], cwd=d, check=True, capture_output=True)
    run("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-subj", "/CN=CacheOS Test CA",
        "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign,cRLSign",
        "-addext", "subjectKeyIdentifier=hash",
        "-keyout", "ca.key", "-out", "ca.crt")
    for name in ("server", "client"):
        run("req", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=localhost", "-keyout", f"{name}.key",
            "-out", f"{name}.csr")
        with open(os.path.join(d, f"{name}.ext"), "w") as f:
            f.write("subjectAltName=DNS:localhost,IP:127.0.0.1\n"
                    "keyUsage=critical,digitalSignature,keyEncipherment\n"
                    "authorityKeyIdentifier=keyid,issuer\n"
                    "subjectKeyIdentifier=hash\n"
                    "extendedKeyUsage=serverAuth,clientAuth\n")
        run("x509", "-req", "-in", f"{name}.csr", "-CA", "ca.crt", "-CAkey", "ca.key", "-CAcreateserial",
            "-days", "1", "-extfile", f"{name}.ext", "-out", f"{name}.crt")
    return {k: os.path.join(d, k) for k in ("ca.crt", "server.crt", "server.key", "client.crt", "client.key")}


@unittest.skipUnless(shutil.which("openssl"), "openssl CLI not found")
class TlsTest(CompatBase):
    """业务链路与复制链路 TLS（规格书 9.1）。"""

    def tls_conf(self, c, port):
        return [f"tls-port {port}", "port 0", f"tls-cert-file {c['server.crt']}", f"tls-key-file {c['server.key']}",
                f"tls-ca-cert-file {c['ca.crt']}", "tls-auth-clients yes", "tls-replication yes",
                f"tls-client-cert-file {c['client.crt']}", f"tls-client-key-file {c['client.key']}"]

    def client_ctx(self, c, with_cert=True):
        ctx = ssl.create_default_context(cafile=c["ca.crt"])
        if with_cert:
            ctx.load_cert_chain(c["client.crt"], c["client.key"])
        return ctx

    def start_tls(self, port, c, extra=()):
        d = os.path.join(self.env.dir, str(port))
        os.makedirs(d, exist_ok=True)
        conf = os.path.join(d, "node.conf")
        with open(conf, "w") as f:
            f.write(f"bind 127.0.0.1\ndir {d}\nsave \"\"\nappendonly no\n")
            for line in [*self.tls_conf(c, port), *extra]:
                f.write(line + "\n")
        log = open(os.path.join(d, "out.log"), "w")
        self.env.logs.append(log)
        self.env.procs.append(subprocess.Popen([sy("sycache-server"), conf], stdout=log, stderr=subprocess.STDOUT))
        wait_until(lambda: Conn(port, tls=self.client_ctx(c)).cmd("PING") == "PONG", timeout=10, what="tls server")

    def test_mutual_tls_and_tls_replication(self):
        c = make_certs(self.env.dir)
        p1, p2 = self.next_port(), self.next_port()
        self.start_tls(p1, c)
        self.start_tls(p2, c, extra=[f"replicaof 127.0.0.1 {p1}"])
        prim, rep = Conn(p1, tls=self.client_ctx(c)), Conn(p2, tls=self.client_ctx(c))
        prim.cmd("SET", "tk", "tv")
        wait_until(lambda: rep.cmd("GET", "tk") == "tv", what="replication over TLS")
        # 不带客户端证书的连接被拒绝（tls-auth-clients yes）
        with self.assertRaises((ssl.SSLError, ConnectionError, OSError)):
            Conn(p1, tls=self.client_ctx(c, with_cert=False)).cmd("PING")
        # sycache-cli 的 TLS 参数与上游一致
        out = subprocess.run([sy("sycache-cli"), "-p", str(p1), "--tls", "--cacert", c["ca.crt"], "--cert",
                              c["client.crt"], "--key", c["client.key"], "GET", "tk"],
                             capture_output=True, text=True, timeout=10)
        self.assertEqual(out.stdout.strip(), "tv", out.stderr)


@unittest.skipUnless(VALKEY_BIN, "VALKEY_BIN not set: skip mixed-version interop with upstream Valkey")
class UpstreamInteropTest(CompatBase):
    """与上游 Valkey 互通：证明复制协议（REPLCONF version）与 RDB 格式未被改名破坏，支持滚动迁移。"""

    def test_upstream_replica_of_sycache(self):
        p1, p2 = self.next_port(), self.next_port()
        self.env.start(sy("sycache-server"), p1)
        self.env.start(vk("valkey-server"), p2, conf_lines=[f"replicaof 127.0.0.1 {p1}"])
        Conn(p1).cmd("SET", "a", "1")
        wait_until(lambda: Conn(p2).cmd("GET", "a") == "1", what="upstream replica sync")

    def test_sycache_replica_of_upstream(self):
        p1, p2 = self.next_port(), self.next_port()
        self.env.start(vk("valkey-server"), p1)
        self.env.start(sy("sycache-server"), p2, conf_lines=[f"replicaof 127.0.0.1 {p1}"])
        Conn(p1).cmd("SET", "b", "2")
        wait_until(lambda: Conn(p2).cmd("GET", "b") == "2", what="sycache replica sync")

    def test_rdb_both_ways(self):
        p1 = self.next_port()
        d = self.env.start(sy("sycache-server"), p1)
        c = Conn(p1)
        c.cmd("HSET", "h", "f", "v")
        c.cmd("SAVE")
        rdb = os.path.join(d, "dump.rdb")
        p2 = self.next_port()
        d2 = os.path.join(self.env.dir, str(p2))
        os.makedirs(d2)
        shutil.copy(rdb, os.path.join(d2, "dump.rdb"))
        self.env.start(vk("valkey-server"), p2)
        self.assertEqual(Conn(p2).cmd("HGET", "h", "f"), "v")
        Conn(p2).cmd("SET", "back", "1")
        Conn(p2).cmd("SAVE")
        p3 = self.next_port()
        d3 = os.path.join(self.env.dir, str(p3))
        os.makedirs(d3)
        shutil.copy(os.path.join(d2, "dump.rdb"), os.path.join(d3, "dump.rdb"))
        self.env.start(sy("sycache-server"), p3)
        self.assertEqual(Conn(p3).cmd("GET", "back"), "1")


if __name__ == "__main__":
    unittest.main()

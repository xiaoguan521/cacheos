#!/usr/bin/env python3
"""集群槽位迁移 + 模块数据一致性测试（ADR 0002）。

用法：cluster-smoke.py <module: json|bloom> <sycache-server> <module.so>

两个启用集群的主节点各加载模块，把一个槽位从 A 迁到 B（SETSLOT + MIGRATE），
验证模块数据迁移后在 B 上可读、A 上返回 MOVED。search 不在此脚本范围：
其索引是节点本地的，集群模式需另行设计（见 ADR 0002 修订记录）。
"""
import atexit, os, shutil, socket, subprocess, sys, tempfile, time

module, server, so = sys.argv[1], os.path.abspath(sys.argv[2]), os.path.abspath(sys.argv[3])
PA, PB = 7401, 7402
tmp = tempfile.mkdtemp()
procs = []


def cleanup():
    for p in procs:
        p.kill()
    time.sleep(0.5)
    shutil.rmtree(tmp, ignore_errors=True)


atexit.register(cleanup)


def fail(msg):
    print("  ✗ " + msg, file=sys.stderr)
    for n in ("a", "b"):
        try:
            print("--- %s.log ---" % n)
            print(open(os.path.join(tmp, n + ".log")).read()[-2500:])
        except OSError:
            pass
    sys.exit(1)


def cmd(port, *args):
    s = socket.create_connection(("127.0.0.1", port), timeout=10)
    data = b"*%d\r\n" % len(args)
    for a in args:
        b = a if isinstance(a, bytes) else str(a).encode()
        data += b"$%d\r\n" % len(b) + b + b"\r\n"
    s.sendall(data)
    f = s.makefile("rb")

    def read():
        line = f.readline().rstrip(b"\r\n")
        t, rest = line[:1], line[1:]
        if t in (b"+", b":"):
            return rest.decode()
        if t == b"-":
            return "ERR:" + rest.decode()
        if t == b"$":
            n = int(rest)
            return None if n < 0 else f.read(n + 2)[:n].decode("latin1")
        if t == b"*":
            return [read() for _ in range(max(int(rest), 0))]
        if t == b"_":
            return None
        return "?" + line.decode("latin1")

    r = read()
    s.close()
    return r


def start(name, port):
    d = os.path.join(tmp, name)
    os.makedirs(d)
    p = subprocess.Popen(
        [server, "--port", str(port), "--dir", d, "--save", "", "--daemonize", "no",
         "--cluster-enabled", "yes", "--cluster-config-file", "nodes.conf",
         "--cluster-node-timeout", "5000", "--loadmodule", so],
        stdout=open(os.path.join(tmp, name + ".log"), "w"), stderr=subprocess.STDOUT)
    procs.append(p)
    for _ in range(60):
        try:
            if cmd(port, "PING") == "PONG":
                return
        except OSError:
            pass
        time.sleep(0.5)
    fail("%s 启动失败" % name)


def check(cond, msg):
    if not cond:
        fail(msg)
    print("  ✓ " + msg)


print("[1/4] 启动两个集群节点并加载模块 %s" % module)
start("a", PA)
start("b", PB)
check(module in str(cmd(PA, "MODULE", "LIST")).lower().replace("valkey", ""), "MODULE LIST 含 %s" % module)

print("[2/4] 组建集群")
check(cmd(PA, "CLUSTER", "ADDSLOTSRANGE", 0, 8191) == "OK", "A 负责 0-8191")
check(cmd(PB, "CLUSTER", "ADDSLOTSRANGE", 8192, 16383) == "OK", "B 负责 8192-16383")
cmd(PA, "CLUSTER", "MEET", "127.0.0.1", PB)
for _ in range(60):
    if "cluster_state:ok" in cmd(PA, "CLUSTER", "INFO") and "cluster_state:ok" in cmd(PB, "CLUSTER", "INFO"):
        break
    time.sleep(0.5)
else:
    fail("集群状态未变为 ok")
check(True, "cluster_state:ok")
ida, idb = cmd(PA, "CLUSTER", "MYID"), cmd(PB, "CLUSTER", "MYID")

# 选一个落在 A 的槽位的 key（哈希标签保证槽位稳定）
key = slot = None
for i in range(1000):
    k = "{t%d}:m" % i
    sl = int(cmd(PA, "CLUSTER", "KEYSLOT", k))
    if sl < 8192:
        key, slot = k, sl
        break

print("[3/4] 在 A 写入模块数据（key=%s slot=%d）" % (key, slot))
if module == "json":
    check(cmd(PA, "JSON.SET", key, "$", '{"n":1,"tags":["a","b"],"nested":{"x":true}}') == "OK", "JSON.SET")
    want = lambda p: cmd(p, "JSON.GET", key, "$.tags[1]") == '["b"]'
else:
    check(cmd(PA, "BF.RESERVE", key, "0.01", 1000) == "OK", "BF.RESERVE")
    check(cmd(PA, "BF.ADD", key, "item1") == "1", "BF.ADD item1")
    check(cmd(PA, "BF.ADD", key, "item2") == "1", "BF.ADD item2")
    want = lambda p: cmd(p, "BF.EXISTS", key, "item1") == "1" and cmd(p, "BF.EXISTS", key, "item2") == "1" \
        and cmd(p, "BF.EXISTS", key, "nope") == "0"
check(want(PA), "迁移前在 A 上读取正确")

print("[4/4] 把槽位 %d 从 A 迁到 B" % slot)
check(cmd(PB, "CLUSTER", "SETSLOT", slot, "IMPORTING", ida) == "OK", "B SETSLOT IMPORTING")
check(cmd(PA, "CLUSTER", "SETSLOT", slot, "MIGRATING", idb) == "OK", "A SETSLOT MIGRATING")
res = cmd(PA, "MIGRATE", "127.0.0.1", PB, "", 0, 5000, "KEYS", key)
check(res == "OK", "MIGRATE 模块类型 key 成功: %s" % res)
for p in (PA, PB):
    check(cmd(p, "CLUSTER", "SETSLOT", slot, "NODE", idb) == "OK", "SETSLOT NODE B（%d）" % p)
check(want(PB), "迁移后在 B 上模块数据一致")
moved = cmd(PA, "EXISTS", key)
check(str(moved).startswith("ERR:MOVED") or moved == "0", "A 上不再持有该 key（%s）" % moved)
print("✓ 所有测试通过")

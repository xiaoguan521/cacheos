#!/usr/bin/env python3
"""Compare actual Cluster commands and same-slot data operations.

Test keys use a random shared hash tag and are cleaned. EVAL/SCRIPT LOAD can
add Lua cache entries; no SCRIPT FLUSH, slot migration, failover or cluster
configuration changes are performed. Passwords are read from the environment.
"""
from collections import Counter
import re
import sys
import time

import cmdcheck
import topologycheck as common


DEFAULT_TARGETS = ["redis=127.0.0.1:7100", "sycache=127.0.0.1:8100"]
CASES = []


def case(name, group="cluster"):
    def register(fn):
        CASES.append(common.Case(group, name, fn, "cluster"))
        return fn
    return register


class ClusterSpec:
    def __init__(self, raw, args):
        name, sep, address = raw.partition("=")
        if not sep:
            raise ValueError("目标格式应为 name=host:port")
        self.name = common.target_name(name)
        self.seed = common.endpoint(address)
        self.data_auth = common.auth_for(self.name, args)
        self.commands = Counter()
        self.owner = None
        self.slot = None
        self.slots = []

    def discover(self, seed):
        conn = common.GuardedConn(*self.seed, self.data_auth, self.commands)
        try:
            info = conn.call("CLUSTER", "INFO")
            if "cluster_state:ok" not in info:
                raise ValueError("集群状态不是 ok")
            self.slot = int(conn.call("CLUSTER", "KEYSLOT", "{cmdcheck-%s}:probe" % seed))
            self.slots = conn.call("CLUSTER", "SLOTS")
            slot = slot_range(self.slots, self.slot)
            self.owner = node_endpoint(slot[2], self.seed[0])
        finally:
            conn.close()
        # Confirm the advertised address is reachable before running cases.
        conn = common.GuardedConn(*self.owner, self.data_auth, self.commands)
        try:
            if conn.call("PING") != "PONG":
                raise ValueError("槽位主节点不可用")
        finally:
            conn.close()

    def endpoint_for(self, scope):
        return self.owner, self.data_auth

    def describe(self):
        return "seed=%s:%s test_slot=%s owner=%s:%s" % (*self.seed, self.slot, *self.owner)

    def metadata(self):
        return {"seed": self.seed, "test_slot": self.slot, "owner": self.owner}


def slot_range(slots, slot):
    for item in slots:
        if int(item[0]) <= slot <= int(item[1]):
            return item
    raise ValueError("没有找到槽位 %d 的主节点" % slot)


def node_endpoint(node, fallback):
    host = node[0] or fallback
    if host == "?":
        raise ValueError("集群没有公布可连接的节点地址")
    return str(host), int(node[1])


def node_rows(text):
    return [line.split() for line in text.splitlines() if line.strip()]


@case("CLUSTER INFO")
def _(t):
    info = dict(line.split(":", 1) for line in t("CLUSTER", "INFO").splitlines() if ":" in line)
    t.eq(info.get("cluster_state"), "ok")
    t.eq(int(info.get("cluster_slots_assigned", 0)), 16384)
    known = int(info.get("cluster_known_nodes", 0))
    t.ok(known > 0, "集群节点数应大于 0")
    return common.ResultValue({"nodes": known, "assigned_slots": 16384}, "节点数 %d，槽位全部分配" % known)


@case("CLUSTER HELP")
def _(t):
    t.ok(len(t("CLUSTER", "HELP")) > 0, "CLUSTER HELP 应返回帮助")


@case("CLUSTER NODES")
def _(t):
    nodes = node_rows(t("CLUSTER", "NODES"))
    t.ok(nodes and all(len(node) >= 8 for node in nodes), "CLUSTER NODES 格式异常")
    masters = sum("master" in node[2].split(",") for node in nodes)
    replicas = sum(bool({"slave", "replica"}.intersection(node[2].split(","))) for node in nodes)
    t.ok(masters > 0, "集群没有主节点")
    t.ok(all(node[7] == "connected" for node in nodes), "存在未连接节点")
    return common.ResultValue({"masters": masters, "replicas": replicas}, "%d 主 / %d 副本" % (masters, replicas))


@case("CLUSTER SLOTS")
def _(t):
    slots = sorted(t("CLUSTER", "SLOTS"), key=lambda row: int(row[0]))
    next_slot = 0
    primaries = set()
    replicas = set()
    for row in slots:
        start, end = int(row[0]), int(row[1])
        t.eq(start, next_slot, "槽位范围不连续")
        t.ok(end >= start and len(row) >= 3, "槽位范围格式异常")
        primaries.add(tuple(node_endpoint(row[2], t.target.host)))
        replicas.update(tuple(node_endpoint(replica, t.target.host)) for replica in row[3:])
        next_slot = end + 1
    t.eq(next_slot, 16384, "槽位覆盖")
    return common.ResultValue({"primaries": len(primaries), "replicas": len(replicas), "slots": next_slot},
                              "%d 主 / %d 副本，覆盖 %d 槽" % (len(primaries), len(replicas), next_slot))


@case("CLUSTER SHARDS")
def _(t):
    shards = t("CLUSTER", "SHARDS")
    t.ok(isinstance(shards, list) and shards, "CLUSTER SHARDS 应返回非空列表")
    for item in shards:
        shard = cmdcheck.pairs(item)
        t.ok("slots" in shard and "nodes" in shard, "SHARDS 应包含 slots 和 nodes")
    return common.ResultValue(len(shards), "分片数 %d" % len(shards))


@case("CLUSTER MYID")
def _(t):
    t.ok(bool(re.fullmatch(r"[0-9a-fA-F]{40}", t("CLUSTER", "MYID"))), "MYID 应为 40 位十六进制")


@case("CLUSTER MYSHARDID")
def _(t):
    t.ok(bool(re.fullmatch(r"[0-9a-fA-F]{40}", t("CLUSTER", "MYSHARDID"))), "MYSHARDID 应为 40 位十六进制")


@case("CLUSTER KEYSLOT")
def _(t):
    first, second = t.k("slot-a"), t.k("slot-b")
    t.eq(t("CLUSTER", "KEYSLOT", first), t.target.spec.slot)
    t.eq(t("CLUSTER", "KEYSLOT", second), t.target.spec.slot)
    t.eq(t("CLUSTER", "KEYSLOT", "foo"), 12182)


def replicas_case(t, subcommand):
    primary_id = t("CLUSTER", "MYID")
    replicas = t("CLUSTER", subcommand, primary_id)
    t.ok(isinstance(replicas, list), "%s 应返回列表" % subcommand)
    for item in replicas:
        t.ok(len(item.split()) >= 8, "%s 返回的节点格式异常" % subcommand)
    return common.ResultValue(len(replicas), "当前槽位主节点的副本数 %d" % len(replicas))


@case("CLUSTER REPLICAS")
def _(t):
    return replicas_case(t, "REPLICAS")


@case("CLUSTER SLAVES")
def _(t):
    return replicas_case(t, "SLAVES")


@case("CLUSTER LINKS")
def _(t):
    links = t("CLUSTER", "LINKS")
    t.ok(isinstance(links, list), "CLUSTER LINKS 应返回列表")
    return "连接数 %d" % len(links)


@case("CLUSTER COUNT-FAILURE-REPORTS")
def _(t):
    node_id = t("CLUSTER", "MYID")
    count = t("CLUSTER", "COUNT-FAILURE-REPORTS", node_id)
    t.ok(isinstance(count, int) and count >= 0, "故障报告数应为非负整数")


@case("CLUSTER COUNTKEYSINSLOT")
def _(t):
    key = t.k("slot-count")
    t.eq(t("SET", key, "value", "EX", 60), "OK")
    count = t("CLUSTER", "COUNTKEYSINSLOT", t.target.spec.slot)
    t.ok(isinstance(count, int) and count >= 1, "目标槽位应包含测试键")


@case("CLUSTER GETKEYSINSLOT")
def _(t):
    key = t.k("slot-keys")
    t.eq(t("SET", key, "value", "EX", 60), "OK")
    count = t("CLUSTER", "COUNTKEYSINSLOT", t.target.spec.slot)
    if count > 10000:
        raise common.SkipCase("目标槽位键数超过 10000，避免读取过多现有键")
    keys = t("CLUSTER", "GETKEYSINSLOT", t.target.spec.slot, max(1, count))
    t.ok(key in keys, "未返回测试键")


@case("MOVED 重定向和目标节点读写")
def _(t):
    spec = t.target.spec
    owner = spec.owner
    candidates = [node_endpoint(row[2], spec.seed[0]) for row in spec.slots]
    wrong = next((address for address in candidates if address != owner), None)
    if not wrong:
        raise common.SkipCase("只有一个主节点，无法验证 MOVED")
    key = t.k("moved")
    t.eq(t("SET", key, "redirect-ok", "EX", 60), "OK")
    conn = t.target.open_at(wrong, spec.data_auth)
    try:
        conn.call("GET", key)
    except cmdcheck.RespError as exc:
        parts = str(exc).split()
        t.ok(len(parts) == 3 and parts[0] == "MOVED", "期望 MOVED，实际 %s" % exc)
        t.eq(int(parts[1]), spec.slot)
        redirected = common.endpoint(parts[2] if not parts[2].startswith(":") else wrong[0] + parts[2])
        t.eq(redirected[1], owner[1], "重定向端口")
        target = t.target.open_at(redirected, spec.data_auth)
        t.eq(target.call("GET", key), "redirect-ok")
    else:
        raise AssertionError("向非槽位主节点发送 GET 应返回 MOVED")


@case("跨槽 MGET 返回 CROSSSLOT")
def _(t):
    first = t.k("cross-a")
    second = "{%s-other}:cross-b" % t.tag[1:-1]
    t.ok(t("CLUSTER", "KEYSLOT", first) != t("CLUSTER", "KEYSLOT", second), "测试键必须位于不同槽")
    t.raises("CROSSSLOT", "MGET", first, second)


@case("副本 READONLY / READWRITE 和复制读取")
def _(t):
    spec = t.target.spec
    row = slot_range(spec.slots, spec.slot)
    if len(row) < 4:
        raise common.SkipCase("当前测试槽位没有副本")
    replica = node_endpoint(row[3], spec.seed[0])
    key = t.k("readonly")
    t.eq(t("SET", key, "replica-ok", "EX", 60), "OK")
    conn = t.target.open_at(replica, spec.data_auth)
    t.eq(conn.call("READONLY"), "OK")
    deadline = time.monotonic() + 5
    value = None
    while time.monotonic() < deadline:
        value = conn.call("GET", key)
        if value == "replica-ok":
            break
        time.sleep(0.05)
    t.eq(value, "replica-ok")
    t.eq(conn.call("READWRITE"), "OK")
    try:
        conn.call("GET", key)
    except cmdcheck.RespError as exc:
        t.ok(str(exc).startswith("MOVED "), "READWRITE 后应重定向到主节点")
    else:
        raise AssertionError("READWRITE 后副本读取应返回 MOVED")


def main():
    ap = common.parser("对比 Cluster 命令、重定向和集群数据命令", """\
示例：
  CMDCHECK_PASSWORD=xxx python3 test/compat/cluster_cmdcheck.py
  CMDCHECK_PASSWORD_REDIS=aaa CMDCHECK_PASSWORD_SYCACHE=bbb \\
    python3 test/compat/cluster_cmdcheck.py -t redis=127.0.0.1:7100 \\
      -t sycache=127.0.0.1:8100 --json cluster-result.json

密码只从 CMDCHECK_PASSWORD[_目标名] 环境变量读取，用户名使用 CMDCHECK_USER[_目标名]。
常用数据命令在测试键槽位的主节点执行；单独验证 MOVED、CROSSSLOT 和副本只读。
默认不会更改集群拓扑、触发故障切换或执行 SCRIPT FLUSH；EVAL/SCRIPT LOAD 会增加 Lua 缓存。""")
    args = ap.parse_args()
    cases = CASES + common.data_cases()
    try:
        selected = common.select_cases(cases, args)
        if args.list:
            common.list_cases(selected)
            return 0
        if args.timeout <= 0:
            raise ValueError("--timeout 必须大于 0")
        specs = [ClusterSpec(raw, args) for raw in (args.target or DEFAULT_TARGETS)]
        if len({spec.name for spec in specs}) != len(specs):
            raise ValueError("目标名称不得重复")
        seed = common.new_seed()
        for spec in specs:
            spec.discover(seed)
    except (ValueError, OSError, cmdcheck.RespError) as exc:
        print("参数或连接错误: %s" % exc, file=sys.stderr)
        return 2
    return common.run(selected, specs, seed, args)


if __name__ == "__main__":
    sys.exit(main())

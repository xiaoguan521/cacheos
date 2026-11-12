#!/usr/bin/env python3
"""Compare actual Sentinel commands and data-plane behavior on multiple services.

Only per-run test keys are written and cleaned. EVAL/SCRIPT LOAD can add Lua
cache entries; no SCRIPT FLUSH, Sentinel configuration changes, or failover is
performed. Sentinel and data-node passwords are read from environment variables.
"""
from collections import Counter
import re
import sys
import time

import cmdcheck
import topologycheck as common


DEFAULT_TARGETS = [
    "redis=127.0.0.1:26379,127.0.0.1:26380,127.0.0.1:26381/redis-ha",
    "sycache=127.0.0.1:27379,127.0.0.1:27380,127.0.0.1:27381/sycache-ha",
]
CASES = []


def case(name, group="sentinel", scope="sentinel"):
    def register(fn):
        CASES.append(common.Case(group, name, fn, scope))
        return fn
    return register


class SentinelSpec:
    def __init__(self, raw, args):
        name, sep, location = raw.partition("=")
        if not sep:
            raise ValueError("目标格式应为 name=host:port[,host:port...]/master-name")
        self.name = common.target_name(name)
        addresses, sep, self.master_name = location.rpartition("/")
        if not sep or not self.master_name or not addresses:
            raise ValueError("目标格式应为 name=host:port[,host:port...]/master-name")
        self.sentinels = [common.endpoint(item) for item in addresses.split(",")]
        self.sentinel_auth = common.auth_for(self.name, args, sentinel=True)
        self.data_auth = common.auth_for(self.name, args)
        self.commands = Counter()
        self.sentinel = self.master = None
        self.replicas = []

    def discover(self):
        errors = []
        for address in self.sentinels:
            conn = None
            try:
                conn = common.GuardedConn(*address, self.sentinel_auth, self.commands)
                master = conn.call("SENTINEL", "GET-MASTER-ADDR-BY-NAME", self.master_name)
                if not isinstance(master, list) or len(master) != 2 or not master[0]:
                    raise ValueError("Sentinel 未发现主节点 %s" % self.master_name)
                self.sentinel, self.master = address, (master[0], int(master[1]))
                try:
                    replicas = conn.call("SENTINEL", "REPLICAS", self.master_name)
                except cmdcheck.RespError as exc:
                    if cmdcheck.classify(exc)[0] == "UNSUPPORTED":
                        try:
                            replicas = conn.call("SENTINEL", "SLAVES", self.master_name)
                        except cmdcheck.RespError:
                            replicas = []
                    elif cmdcheck.classify(exc)[0] == "NOPERM":
                        replicas = []
                    else:
                        raise
                for item in replicas:
                    fields = cmdcheck.pairs(item)
                    flags = set(fields.get("flags", "").split(","))
                    if not flags.intersection({"s_down", "o_down", "disconnected"}):
                        self.replicas.append((fields["ip"], int(fields["port"])))
                break
            except (OSError, ValueError, KeyError, cmdcheck.RespError) as exc:
                self.sentinel = self.master = None
                self.replicas = []
                errors.append("%s:%s %s" % (*address, exc))
            finally:
                if conn:
                    conn.close()
        if not self.master:
            raise ValueError("无法通过 Sentinel 发现主节点: " + "; ".join(errors))
        conn = common.GuardedConn(*self.master, self.data_auth, self.commands)
        try:
            role = conn.call("ROLE")
            if not role or role[0] != "master":
                raise ValueError("Sentinel 返回的地址不是主节点: %s" % (self.master,))
        finally:
            conn.close()

    def endpoint_for(self, scope):
        return (self.sentinel, self.sentinel_auth) if scope == "sentinel" else (self.master, self.data_auth)

    def describe(self):
        return "Sentinel=%s:%s master=%s/%s:%s replicas=%d" % (
            *self.sentinel, self.master_name, *self.master, len(self.replicas))

    def metadata(self):
        return {"sentinels": self.sentinels, "master_name": self.master_name,
                "master": self.master, "replicas": self.replicas}


def fields(value):
    return cmdcheck.pairs(value)


@case("PING / ROLE / INFO server")
def _(t):
    t.eq(t("PING"), "PONG")
    role = t("ROLE")
    t.eq(role[0], "sentinel")
    t.ok(t.target.spec.master_name in role[1], "ROLE 未包含被监控的主节点")
    server = t.info_sec("server")
    t.eq(server.get("redis_mode"), "sentinel")


@case("SENTINEL HELP")
def _(t):
    t.ok(len(t("SENTINEL", "HELP")) > 0, "SENTINEL HELP 应返回帮助")


@case("SENTINEL MASTERS")
def _(t):
    masters = [fields(item) for item in t("SENTINEL", "MASTERS")]
    t.ok(any(item.get("name") == t.target.spec.master_name for item in masters), "未找到被监控的主节点")
    return "监控主节点数 %d" % len(masters)


@case("SENTINEL MASTER")
def _(t):
    spec = t.target.spec
    master = fields(t("SENTINEL", "MASTER", spec.master_name))
    t.eq((master.get("ip"), int(master.get("port", 0))), spec.master)
    t.ok("master" in master.get("flags", "").split(","), "主节点 flags 不含 master")
    t.ok(not set(master.get("flags", "").split(",")).intersection({"s_down", "o_down", "disconnected"}),
         "主节点状态异常: " + master.get("flags", ""))


@case("SENTINEL GET-MASTER-ADDR-BY-NAME")
def _(t):
    spec = t.target.spec
    reply = t("SENTINEL", "GET-MASTER-ADDR-BY-NAME", spec.master_name)
    t.eq((reply[0], int(reply[1])), spec.master)


def replicas_case(t, subcommand):
    entries = [fields(item) for item in t("SENTINEL", subcommand, t.target.spec.master_name)]
    for item in entries:
        t.ok(bool(item.get("ip")) and int(item.get("port", 0)) > 0, "副本缺少地址")
    return common.ResultValue(len(entries), "副本数 %d" % len(entries))


@case("SENTINEL REPLICAS")
def _(t):
    return replicas_case(t, "REPLICAS")


@case("SENTINEL SLAVES")
def _(t):
    return replicas_case(t, "SLAVES")


@case("SENTINEL SENTINELS")
def _(t):
    entries = [fields(item) for item in t("SENTINEL", "SENTINELS", t.target.spec.master_name)]
    for item in entries:
        t.ok(bool(item.get("ip")) and int(item.get("port", 0)) > 0, "其他哨兵缺少地址")
    return common.ResultValue(len(entries), "其他哨兵数 %d" % len(entries))


@case("SENTINEL CKQUORUM")
def _(t):
    reply = t("SENTINEL", "CKQUORUM", t.target.spec.master_name)
    t.ok(isinstance(reply, str) and reply.startswith("OK"), "法定票数不可用: %s" % reply)
    return reply


@case("SENTINEL MYID")
def _(t):
    t.ok(bool(re.fullmatch(r"[0-9a-fA-F]{40}", t("SENTINEL", "MYID"))), "MYID 应为 40 位十六进制")


@case("SENTINEL INFO-CACHE")
def _(t):
    reply = t("SENTINEL", "INFO-CACHE", t.target.spec.master_name)
    t.ok(isinstance(reply, list), "INFO-CACHE 应返回列表")


@case("SENTINEL PENDING-SCRIPTS")
def _(t):
    t.ok(isinstance(t("SENTINEL", "PENDING-SCRIPTS"), list), "PENDING-SCRIPTS 应返回列表")


@case("SENTINEL CONFIG GET")
def _(t):
    reply = fields(t("SENTINEL", "CONFIG", "GET", "resolve-hostnames"))
    t.ok("resolve-hostnames" in reply, "未返回 resolve-hostnames")


@case("SENTINEL IS-MASTER-DOWN-BY-ADDR（只读探测）")
def _(t):
    spec = t.target.spec
    reply = t("SENTINEL", "IS-MASTER-DOWN-BY-ADDR", spec.master[0], spec.master[1], 0, "*")
    t.ok(isinstance(reply, list) and len(reply) == 3, "探测回复格式异常")
    t.eq(reply[0], 0, "当前主节点应在线")


@case("多个 Sentinel 的主节点发现一致")
def _(t):
    spec = t.target.spec
    if len(spec.sentinels) < 2:
        raise common.SkipCase("只提供了一个 Sentinel 地址")
    for address in spec.sentinels:
        conn = t.target.open_at(address, spec.sentinel_auth)
        found = conn.call("SENTINEL", "GET-MASTER-ADDR-BY-NAME", spec.master_name)
        t.eq((found[0], int(found[1])), spec.master, "Sentinel %s:%s" % address)
    return common.ResultValue(len(spec.sentinels), "检查了 %d 个 Sentinel" % len(spec.sentinels))


@case("ROLE / INFO replication", group="replication", scope="data")
def _(t):
    spec = t.target.spec
    t.eq(t("ROLE")[0], "master")
    if not spec.replicas:
        raise common.SkipCase("Sentinel 未发现在线副本")
    conn = t.target.open_at(spec.replicas[0], spec.data_auth)
    t.eq(conn.call("ROLE")[0], "slave")
    status = dict(line.split(":", 1) for line in conn.call("INFO", "replication").splitlines() if ":" in line)
    t.eq(status.get("master_link_status"), "up")


@case("主节点写入 / 副本读取 / WAIT", group="replication", scope="data")
def _(t):
    spec = t.target.spec
    if not spec.replicas:
        raise common.SkipCase("Sentinel 未发现在线副本")
    key = t.k("replication")
    t.eq(t("SET", key, "sentinel-replication", "EX", 60), "OK")
    replica = t.target.open_at(spec.replicas[0], spec.data_auth)
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline and replica.call("GET", key) != "sentinel-replication":
        time.sleep(0.05)
    t.eq(replica.call("GET", key), "sentinel-replication")
    t.ok(t("WAIT", 1, 1000) >= 1, "WAIT 未确认副本接收")


def main():
    ap = common.parser("对比 Sentinel 命令和经 Sentinel 发现的主从数据命令", """\
示例：
  CMDCHECK_PASSWORD=xxx python3 test/compat/sentinel_cmdcheck.py
  CMDCHECK_PASSWORD_REDIS=aaa CMDCHECK_PASSWORD_SYCACHE=bbb \\
    python3 test/compat/sentinel_cmdcheck.py -t redis=127.0.0.1:26379/redis-ha \\
      -t sycache=127.0.0.1:27379/sycache-ha --json sentinel-result.json

密码只从环境变量读取。Sentinel 密码可通过 CMDCHECK_SENTINEL_PASSWORD[_目标名] 单独指定；
数据节点使用 CMDCHECK_PASSWORD[_目标名]。支持 CMDCHECK_USER 和 CMDCHECK_SENTINEL_USER。
默认不会触发故障切换、修改配置或执行 SCRIPT FLUSH；EVAL/SCRIPT LOAD 会增加 Lua 缓存。""")
    args = ap.parse_args()
    cases = CASES + common.data_cases()
    try:
        selected = common.select_cases(cases, args)
        if args.list:
            common.list_cases(selected)
            return 0
        if args.timeout <= 0:
            raise ValueError("--timeout 必须大于 0")
        specs = [SentinelSpec(raw, args) for raw in (args.target or DEFAULT_TARGETS)]
        if len({spec.name for spec in specs}) != len(specs):
            raise ValueError("目标名称不得重复")
        for spec in specs:
            spec.discover()
    except (ValueError, OSError, cmdcheck.RespError) as exc:
        print("参数或连接错误: %s" % exc, file=sys.stderr)
        return 2
    return common.run(selected, specs, common.new_seed(), args)


if __name__ == "__main__":
    sys.exit(main())

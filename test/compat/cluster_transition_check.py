#!/usr/bin/env python3
"""Compare failover and one-slot migration on the local demo clusters.

This is intentionally separate from the read-only/default cluster comparison:
it briefly stops one demo primary and changes the owner of one empty slot.
Only a randomly named test key is moved. The original primary and slot owner
are restored when possible. Requires an explicit --execute flag.
"""
import argparse
import binascii
from collections import Counter
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import time

import cmdcheck
import topologycheck as common


ROOT = Path(__file__).resolve().parents[2]
COMPOSE_FILE = ROOT / "demo" / "docker-compose-cluster.yml"
TARGETS = (("redis", ("127.0.0.1", 7100)), ("sycache", ("127.0.0.1", 8100)))


class CheckFailure(Exception):
    pass


def require(condition, message):
    if not condition:
        raise CheckFailure(message)


def call(address, auth, *args):
    conn = cmdcheck.Conn(address[0], address[1], auth.user, auth.password, auth.tls, auth.timeout)
    try:
        return conn.call(*args)
    finally:
        conn.close()


def wait_for(fn, description, timeout=90, retry=(OSError, cmdcheck.RespError)):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            if fn():
                return
        except retry as exc:
            last = exc
        time.sleep(0.2)
    raise CheckFailure("等待 %s 超时%s" % (description, ": " + str(last) if last else ""))


def parse_info(text):
    return dict(line.split(":", 1) for line in text.splitlines() if ":" in line and not line.startswith("#"))


def address(node):
    host = node[0]
    require(host == "127.0.0.1", "仅允许对本机 demo 集群执行中断测试: %r" % (node,))
    return host, int(node[1])


def slot_owner(conn, slot):
    for row in conn.call("CLUSTER", "SLOTS"):
        if int(row[0]) <= slot <= int(row[1]):
            return row
    raise CheckFailure("槽位 %s 没有主节点" % slot)


def owner_at(witness, auth, slot):
    conn = cmdcheck.Conn(witness[0], witness[1], auth.user, auth.password, auth.tls, auth.timeout)
    try:
        info = parse_info(conn.call("CLUSTER", "INFO"))
        if info.get("cluster_state") != "ok" or info.get("cluster_slots_assigned") != "16384":
            return None
        return slot_owner(conn, slot)[2][2]
    finally:
        conn.close()


def replica_link(replica, primary, auth):
    data = parse_info(call(replica, auth, "INFO", "replication"))
    return (data.get("role") == "slave" and data.get("master_host") == primary[0] and
            data.get("master_port") == str(primary[1]) and data.get("master_link_status") == "up")


def read_replica(replica, auth, key):
    conn = cmdcheck.Conn(replica[0], replica[1], auth.user, auth.password, auth.tls, auth.timeout)
    try:
        conn.call("READONLY")
        return conn.call("GET", key)
    finally:
        conn.close()


def set_canary(primary, auth, key, value, state):
    conn = cmdcheck.Conn(primary[0], primary[1], auth.user, auth.password, auth.tls, auth.timeout)
    try:
        require(conn.call("SET", key, value, "NX", "EX", 600) == "OK", "测试键已存在，拒绝覆盖")
        state["created"] = True
        require(conn.call("WAIT", 1, 5000) >= 1, "测试键未得到副本确认")
    finally:
        conn.close()


def compose_services(engine):
    result = subprocess.run(["docker", "compose", "-f", str(COMPOSE_FILE), "config", "--format", "json"],
                            check=True, capture_output=True, text=True, timeout=30)
    config = json.loads(result.stdout)
    services = {}
    for name, service in config["services"].items():
        env = service.get("environment", {})
        if env.get("ENGINE") == engine and env.get("MODE") == "cluster":
            require(service.get("network_mode") == "host", "仅允许操作 demo 的 host 网络节点")
            running = subprocess.run(["docker", "compose", "-f", str(COMPOSE_FILE), "ps", "-q", name],
                                     check=True, capture_output=True, text=True, timeout=30).stdout.strip()
            require(bool(running), "demo 节点未运行: " + name)
            actual = subprocess.run(["docker", "inspect", "--format", "{{json .Config.Env}}", running],
                                    check=True, capture_output=True, text=True, timeout=30)
            actual_env = dict(item.split("=", 1) for item in json.loads(actual.stdout) if "=" in item)
            require(all(actual_env.get(key) == env.get(key) for key in ("ENGINE", "MODE", "PORT")),
                    "运行中的容器配置与 demo Compose 不一致: " + name)
            services[int(env["PORT"])] = name
    require(len(services) == 6, "%s demo 集群应有 6 个数据节点" % engine)
    return services


def topology(seed, auth, services):
    conn = cmdcheck.Conn(seed[0], seed[1], auth.user, auth.password, auth.tls, auth.timeout)
    try:
        info = parse_info(conn.call("CLUSTER", "INFO"))
        require(info.get("cluster_state") == "ok" and info.get("cluster_slots_assigned") == "16384",
                "集群不健康或槽位不完整")
        slots = conn.call("CLUSTER", "SLOTS")
        nodes = [line.split() for line in conn.call("CLUSTER", "NODES").splitlines() if line.strip()]
    finally:
        conn.close()
    require(all(len(node) >= 8 and node[7] == "connected" for node in nodes), "集群存在未连接节点")
    require(not any("[" in field and ("->-" in field or "-<-" in field)
                    for node in nodes for field in node[8:]), "集群已有正在迁移的槽位")
    primaries = {}
    for row in slots:
        require(len(row) >= 4, "每个主节点至少需要一个副本")
        primary = address(row[2])
        replicas = [address(replica) for replica in row[3:]]
        require(primary[1] in services and all(replica[1] in services for replica in replicas),
                "节点不属于当前 demo Compose 项目")
        primaries[row[2][2]] = {"id": row[2][2], "address": primary,
                                 "replicas": [(address(replica), replica[2]) for replica in row[3:]]}
    require(len(primaries) >= 3, "集群至少需要 3 个主节点")
    for primary in primaries.values():
        require(any(replica_link(replica, primary["address"], auth) for replica, _ in primary["replicas"]),
                "主节点 %s 没有已连接的副本" % (primary["address"],))
    return slots, list(primaries.values())


def wait_for_topology(seed, auth, services):
    wait_for(lambda: topology(seed, auth, services), "切回后的集群拓扑和副本信息收敛",
             timeout=45, retry=(OSError, cmdcheck.RespError, CheckFailure))


def key_for_range(start, end, prefix):
    for _ in range(10000):
        tag = "%s-%s" % (prefix, secrets.token_hex(8))
        slot = binascii.crc_hqx(tag.encode(), 0) % 16384
        if start <= slot <= end:
            return "{%s}:key" % tag, slot
    raise CheckFailure("无法生成目标槽位中的测试键")


def cleanup_canary(witnesses, auth, key):
    last = None
    for witness in witnesses:
        try:
            common.redirect_call(witness, auth, Counter(), "DEL", key)
            return
        except Exception as exc:
            last = exc
    raise CheckFailure("测试键清理失败（有 600 秒 TTL）: %s" % last)


def demo_compose(action, service):
    return subprocess.run(["docker", "compose", "-f", str(COMPOSE_FILE), action, service],
                          check=True, timeout=45, text=True)


def failover(engine, seed, auth, services):
    slots, primaries = topology(seed, auth, services)
    row = slots[0]
    primary = address(row[2])
    primary_id = row[2][2]
    replica, replica_id = address(row[3]), row[3][2]
    require(replica_link(replica, primary, auth), "候选副本复制链路未就绪")
    witness = next(item["address"] for item in primaries if item["id"] != primary_id)
    key, slot = key_for_range(int(row[0]), int(row[1]), "cacheos-failover")
    require(call(primary, auth, "CLUSTER", "KEYSLOT", key) == slot, "测试键槽位计算不一致")
    service = services[primary[1]]
    stopped = False
    test_error = None
    state = {"created": False}
    try:
        set_canary(primary, auth, key, "before-failover", state)
        wait_for(lambda: read_replica(replica, auth, key) == "before-failover", "副本读取测试键", timeout=30)
        stopped = True
        demo_compose("stop", service)
        wait_for(lambda: owner_at(witness, auth, slot) == replica_id, "集群自动提升副本", timeout=90)
        require(call(replica, auth, "GET", key) == "before-failover", "自动故障转移后测试数据不可读")
        conn = cmdcheck.Conn(replica[0], replica[1], auth.user, auth.password, auth.tls, auth.timeout)
        try:
            require(conn.call("SET", key, "after-failover", "EX", 600) == "OK", "新主节点写入失败")
        finally:
            conn.close()
    except BaseException as exc:
        test_error = exc
    finally:
        if stopped:
            try:
                demo_compose("start", service)
            except Exception as exc:
                test_error = CheckFailure("原主节点启动失败: %s; 原错误: %s" % (exc, test_error))
    try:
        if test_error:
            raise test_error
        wait_for(lambda: replica_link(primary, replica, auth), "原主节点以副本身份重新加入", timeout=90)
        wait_for(lambda: read_replica(primary, auth, key) == "after-failover", "原主节点同步测试数据", timeout=30)
        require(call(primary, auth, "CLUSTER", "FAILOVER") == "OK", "CLUSTER FAILOVER 未返回 OK")
        wait_for(lambda: owner_at(witness, auth, slot) == primary_id, "手动切回原主节点", timeout=90)
        require(call(primary, auth, "GET", key) == "after-failover", "切回后测试数据不可读")
        wait_for(lambda: replica_link(replica, primary, auth), "原副本重新连接主节点", timeout=90)
        wait_for_topology(seed, auth, services)
        return {"slot": slot, "original_primary": primary[1], "promoted_replica": replica[1],
                "automatic_failover": "PASS", "manual_failback": "PASS", "data": "preserved"}
    finally:
        if state["created"]:
            cleanup_canary([witness, primary, replica], auth, key)


def local_slot_keys(node, auth, slot):
    count = call(node, auth, "CLUSTER", "COUNTKEYSINSLOT", slot)
    require(isinstance(count, int) and count >= 0, "无法检查槽位键数")
    if count > 1:
        raise CheckFailure("槽位 %s 含 %s 个键，拒绝影响非测试数据" % (slot, count))
    return call(node, auth, "CLUSTER", "GETKEYSINSLOT", slot, 2) if count else []


def check_only_canary(nodes, auth, slot, key, expected_holder=None):
    holders = []
    for node in nodes:
        keys = local_slot_keys(node["address"], auth, slot)
        require(not keys or keys == [key], "槽位 %s 出现非测试键，已停止操作" % slot)
        if keys:
            holders.append(node["id"])
    require(len(holders) <= 1, "测试键出现在多个主节点，拒绝继续")
    if expected_holder is not None:
        require(holders == [expected_holder], "测试键不在预期主节点: %s" % holders)
    return holders


def move_one_slot(auth, slot, key, value, source, destination, primaries):
    check_only_canary(primaries, auth, slot, key, source["id"])
    src, dst = source["address"], destination["address"]
    require(call(dst, auth, "CLUSTER", "SETSLOT", slot, "IMPORTING", source["id"]) == "OK",
            "目标节点不能进入 IMPORTING")
    require(call(src, auth, "CLUSTER", "SETSLOT", slot, "MIGRATING", destination["id"]) == "OK",
            "源节点不能进入 MIGRATING")
    migrate = ["MIGRATE", dst[0], dst[1], "", 0, 5000]
    if auth.password:
        migrate.extend(["AUTH2", auth.user, auth.password] if auth.user else ["AUTH", auth.password])
    migrate.extend(["KEYS", key])
    require(call(src, auth, *migrate) == "OK", "MIGRATE 测试键失败")
    check_only_canary(primaries, auth, slot, key, destination["id"])

    try:
        call(src, auth, "GET", key)
    except cmdcheck.RespError as exc:
        fields = str(exc).split()
        require(len(fields) == 3 and fields[0] == "ASK" and int(fields[1]) == slot,
                "迁移中应返回 ASK，实际: %s" % exc)
        require(common.endpoint(fields[2]) == dst, "ASK 重定向目标不正确")
    else:
        raise CheckFailure("迁移中的源节点没有返回 ASK")
    conn = cmdcheck.Conn(dst[0], dst[1], auth.user, auth.password, auth.tls, auth.timeout)
    try:
        require(conn.call("ASKING") == "OK", "ASKING 失败")
        require(conn.call("GET", key) == value, "ASKING 后目标节点读取失败")
    finally:
        conn.close()

    # Only the known canary was migrated; recheck before changing ownership.
    check_only_canary(primaries, auth, slot, key, destination["id"])
    order = [destination] + [node for node in primaries if node["id"] not in {source["id"], destination["id"]}] + [source]
    for node in order:
        require(call(node["address"], auth, "CLUSTER", "SETSLOT", slot, "NODE", destination["id"]) == "OK",
                "无法更新槽位归属")
    for node in primaries:
        wait_for(lambda node=node: owner_at(node["address"], auth, slot) == destination["id"],
                 "槽位归属收敛", timeout=45)
    try:
        call(src, auth, "GET", key)
    except cmdcheck.RespError as exc:
        fields = str(exc).split()
        require(len(fields) == 3 and fields[0] == "MOVED" and int(fields[1]) == slot,
                "迁移完成后应返回 MOVED，实际: %s" % exc)
        require(common.endpoint(fields[2]) == dst, "MOVED 重定向目标不正确")
    else:
        raise CheckFailure("迁移完成后源节点没有返回 MOVED")
    require(call(dst, auth, "GET", key) == value, "迁移完成后目标节点读取失败")


def recover_slot(auth, slot, key, original, other, primaries):
    # Never move or delete a key that was not created by this test.
    holders = check_only_canary(primaries, auth, slot, key)
    for node in primaries:
        if node["id"] in holders:
            conn = cmdcheck.Conn(*node["address"], auth.user, auth.password, auth.tls, auth.timeout)
            try:
                conn.call("ASKING")
                require(conn.call("DEL", key) == 1, "回滚时无法删除测试键")
            finally:
                conn.close()
    check_only_canary(primaries, auth, slot, key)
    for node in (original, other):
        require(call(node["address"], auth, "CLUSTER", "SETSLOT", slot, "STABLE") == "OK", "无法清除迁移状态")
    # Check again immediately before reassigning; refuse if another client wrote.
    require(not check_only_canary(primaries, auth, slot, key), "槽位仍有测试键，拒绝回滚")
    for node in [original] + [n for n in primaries if n["id"] != original["id"]]:
        require(call(node["address"], auth, "CLUSTER", "SETSLOT", slot, "NODE", original["id"]) == "OK",
                "无法恢复原槽位归属")
    for node in primaries:
        wait_for(lambda node=node: owner_at(node["address"], auth, slot) == original["id"], "恢复原槽位归属", timeout=45)


def migrate(engine, seed, auth, services):
    slots, primaries = topology(seed, auth, services)
    row = slots[0]
    original = next(node for node in primaries if node["id"] == row[2][2])
    destination = next(node for node in primaries if node["id"] != original["id"])
    key = slot = None
    for _ in range(256):
        candidate, candidate_slot = key_for_range(int(row[0]), int(row[1]), "cacheos-migrate")
        if all(call(node["address"], auth, "CLUSTER", "COUNTKEYSINSLOT", candidate_slot) == 0
               for node in primaries):
            key, slot = candidate, candidate_slot
            break
    require(key is not None, "没有找到安全的空槽位")
    require(call(original["address"], auth, "CLUSTER", "KEYSLOT", key) == slot, "测试键槽位计算不一致")
    state = {"created": False}
    touched = False
    try:
        set_canary(original["address"], auth, key, "migration-canary", state)
        check_only_canary(primaries, auth, slot, key, original["id"])
        touched = True
        move_one_slot(auth, slot, key, "migration-canary", original, destination, primaries)
        move_one_slot(auth, slot, key, "migration-canary", destination, original, primaries)
        require(call(original["address"], auth, "GET", key) == "migration-canary", "迁回后测试数据不可读")
        topology(seed, auth, services)
        return {"slot": slot, "from": original["address"][1], "to": destination["address"][1],
                "migration": "PASS", "ask_redirect": "PASS", "moved_redirect": "PASS", "restored": True}
    except BaseException as exc:
        if touched:
            try:
                recover_slot(auth, slot, key, original, destination, primaries)
                state["created"] = False
            except Exception as recovery:
                state["created"] = False  # Leave the expiring canary in place for inspection.
                raise CheckFailure("迁槽失败: %s；自动恢复失败: %s；请检查槽位 %s 和测试键 %s（600 秒到期）" %
                                   (exc, recovery, slot, key)) from exc
        raise
    finally:
        if state["created"]:
            cleanup_canary([original["address"], destination["address"]], auth, key)


def main():
    parser = argparse.ArgumentParser(description="比较本机 demo 集群的故障转移和单槽迁移（会短暂影响服务）")
    parser.add_argument("--execute", action="store_true", help="确认运行会停止主节点并改变槽位归属的测试")
    parser.add_argument("--only", choices=("failover", "migration"), help="只运行其中一种测试")
    parser.add_argument("--json", metavar="文件", help="保存结果，不包含口令")
    parser.add_argument("--timeout", type=float, default=10.0, help="单次网络操作超时，默认 10 秒")
    args = parser.parse_args()
    if not args.execute:
        parser.error("此测试会短暂停止主节点并迁移槽位；确认后请传 --execute")
    if args.timeout <= 0:
        parser.error("--timeout 必须大于 0")
    # common.auth_for expects the TLS attributes used by the other tools.
    args.tls = False
    args.tls_ca = args.tls_cert = args.tls_key = args.tls_insecure = None
    results = {}
    failed = False
    for engine, seed in TARGETS:
        auth = common.auth_for(engine, args)
        results[engine] = {}
        try:
            services = compose_services(engine)
            topology(seed, auth, services)
        except Exception as exc:
            print("%s 预检查失败: %s" % (engine, exc), file=sys.stderr, flush=True)
            results[engine]["preflight"] = {"status": "FAIL", "detail": str(exc)}
            failed = True
            continue
        for name, fn in (("failover", failover), ("migration", migrate)):
            if args.only and args.only != name:
                continue
            print("[%s] 开始 %s" % (engine, name), flush=True)
            start = time.monotonic()
            try:
                detail = fn(engine, seed, auth, services)
                results[engine][name] = {"status": "PASS", "seconds": round(time.monotonic() - start, 2),
                                         "detail": detail}
                print("[%s] %s PASS: %s" % (engine, name, detail), flush=True)
            except Exception as exc:
                message = str(exc)
                if auth.password:
                    message = message.replace(auth.password, "[redacted]")
                results[engine][name] = {"status": "FAIL", "seconds": round(time.monotonic() - start, 2),
                                         "detail": message}
                print("[%s] %s FAIL: %s" % (engine, name, message), file=sys.stderr, flush=True)
                failed = True
                # Do not start another disruptive test if this topology failed.
                break
    if args.json:
        try:
            with open(args.json, "w", encoding="utf-8") as f:
                json.dump(results, f, ensure_ascii=False, indent=2)
            print("结果已写入 %s" % args.json)
        except OSError as exc:
            print("无法写入结果: %s" % exc, file=sys.stderr)
            return 2
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())

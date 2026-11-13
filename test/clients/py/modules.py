#!/usr/bin/env python3
"""redis-py 对扩展数据类型模块（JSON / 布隆过滤器 / 搜索与向量，ADR 0002）的兼容性用例。
由 test/clients/module_clients_test.py 提供拓扑端口；MODULE 指定本轮验证的模块。
每个用例独立运行：失败只记录不中断，最后统一汇总，一次运行就能看到全部不兼容项。"""
import json
import os
import struct
import sys
import traceback

import redis
from redis.cluster import ClusterNode, RedisCluster

T = json.loads(os.environ["TOPOLOGY"])
MODULE = os.environ["MODULE"]
H = "127.0.0.1"
failures = []


def case(name):
    def deco(fn):
        try:
            fn()
            print(f"ok   {name}")
        except Exception as e:  # noqa: BLE001 —— 记录后继续，汇总时统一判定
            failures.append(name)
            print(f"FAIL {name}: {type(e).__name__}: {e}")
            traceback.print_exc(limit=3, file=sys.stdout)
        return fn
    return deco


def known(name, reason):
    """已知与 Redis 官方模块的差异：期望它按已知方式失败；意外通过时报错，提醒更新已知差异清单。"""
    def deco(fn):
        try:
            fn()
        except Exception as e:  # noqa: BLE001
            print(f"known-diff {name}: {reason} ({type(e).__name__}: {e})")
            return fn
        failures.append(name)
        print(f"FAIL {name}: 已知差异意外消失（{reason}），请更新 test/clients/modules-known-diff.txt 与此用例")
        return fn
    return deco


def eq(got, want, what=""):
    if got != want:
        raise AssertionError(f"{what} got {got!r}, want {want!r}")


def vec(*xs):
    return struct.pack("<%df" % len(xs), *xs)


def json_cases():
    for proto in (2, 3):
        r = redis.Redis(host=H, port=T["standalone"], protocol=proto, decode_responses=True)
        k = f"py:json:{proto}"
        p = f"json RESP{proto}"
        case(f"{p} set")(lambda: eq(r.json().set(k, "$", {"n": 1, "tags": ["a", "b"], "nested": {"x": True}}), True))
        case(f"{p} get path")(lambda: eq(r.json().get(k, "$.tags[1]"), ["b"]))
        case(f"{p} get whole")(lambda: eq(r.json().get(k)["nested"], {"x": True}))
        case(f"{p} arrappend")(lambda: eq(r.json().arrappend(k, "$.tags", "c"), [3]))
        if proto == 2:
            case(f"{p} numincrby")(lambda: eq(r.json().numincrby(k, "$.n", 2), [3]))
        else:
            # valkey-json 的 NUMINCRBY 在 RESP3 下仍回复 JSON 文本（"[3]"），而 redis-py 在 RESP3 下不再解码（无模块回调），
            # 官方 Redis JSON 在 RESP3 下回复数组；调用方需自行 json.loads。
            known(f"{p} numincrby", "RESP3 returns JSON text instead of an array")(
                lambda: eq(r.json().numincrby(k, "$.n", 2), [3]))
        case(f"{p} type")(lambda: eq(r.json().type(k, "$.tags"), ["array"]))
        case(f"{p} strlen")(lambda: eq(r.json().strlen(k, "$.tags[0]"), [1]))
        case(f"{p} mget")(lambda: (r.json().set(k + ":2", "$", {"n": 7}), eq(r.json().mget([k, k + ":2"], "$.n")[1], [7]))[1])
        case(f"{p} delete path")(lambda: eq(r.json().delete(k, "$.nested"), 1))
        case(f"{p} deleted path is empty")(lambda: eq(r.json().get(k, "$.nested"), []))
        case(f"{p} NX on existing path")(lambda: eq(r.json().set(k, "$.n", 5, nx=True), None))

    @case("json cluster client routes by slot")
    def _():
        rc = RedisCluster(startup_nodes=[ClusterNode(H, p) for p in T["cluster"]], decode_responses=True)
        for i in range(60):
            rc.json().set(f"py:jc:{i}", "$", {"i": i})
        eq([rc.json().get(f"py:jc:{i}", "$.i") for i in range(60)], [[i] for i in range(60)], "cluster get")
        eq(rc.json().arrappend("py:jc:0", "$.l", 1), [], "append to a missing path")


def raw_field(info, name):
    """BF.INFO 的原始回复：RESP2 是 [键, 值, ...] 列表，RESP3 是映射；redis-py 在 RESP3 下不做转换。"""
    if hasattr(info, name.lower()):
        return getattr(info, name.lower())
    if isinstance(info, dict):
        return info.get(name)
    flat = list(info)
    return flat[flat.index(name) + 1] if name in flat else None


def bloom_cases():
    for proto in (2, 3):
        r = redis.Redis(host=H, port=T["standalone"], protocol=proto, decode_responses=True)
        k = f"py:bf:{proto}"
        p = f"bloom RESP{proto}"
        case(f"{p} reserve")(lambda: eq(r.bf().reserve(k, 0.01, 1000), True))
        case(f"{p} add new")(lambda: eq(r.bf().add(k, "a"), 1))
        case(f"{p} add existing")(lambda: eq(r.bf().add(k, "a"), 0))
        case(f"{p} exists")(lambda: eq(r.bf().exists(k, "a"), 1))
        case(f"{p} not exists")(lambda: eq(r.bf().exists(k, "zzz"), 0))
        case(f"{p} madd")(lambda: eq(r.bf().madd(k, "b", "c"), [1, 1]))
        case(f"{p} mexists")(lambda: eq(r.bf().mexists(k, "a", "b", "nope"), [1, 1, 0]))
        case(f"{p} card")(lambda: eq(r.bf().card(k), 3))
        case(f"{p} info capacity/items")(lambda: (eq(raw_field(r.bf().info(k), "Capacity"), 1000, "capacity"),
                                                  eq(raw_field(r.bf().info(k), "Number of items inserted"), 3, "items")))

    @case("bloom cluster client routes by slot")
    def _():
        rc = RedisCluster(startup_nodes=[ClusterNode(H, p) for p in T["cluster"]], decode_responses=True)
        for i in range(40):
            k = f"py:bfc:{i}"
            rc.bf().reserve(k, 0.01, 100)
            rc.bf().add(k, f"v{i}")
        eq([rc.bf().exists(f"py:bfc:{i}", f"v{i}") for i in range(40)], [1] * 40, "exists across slots")


def search_cases():
    from redis.commands.search.field import TagField, VectorField
    from redis.commands.search.index_definition import IndexDefinition, IndexType
    from redis.commands.search.query import Query

    for proto in (2, 3):
        @case(f"search RESP{proto} create index, KNN, hybrid filter, info, drop")
        def _(proto=proto):
            r = redis.Redis(host=H, port=T["standalone"], protocol=proto)  # 向量是二进制，不解码
            idx, pre = f"pyidx{proto}", f"pydoc{proto}:"
            ft = r.ft(idx)
            ft.create_index(
                [VectorField("v", "HNSW", {"TYPE": "FLOAT32", "DIM": 4, "DISTANCE_METRIC": "L2"}), TagField("cat")],
                definition=IndexDefinition(prefix=[pre], index_type=IndexType.HASH))
            r.hset(pre + "1", mapping={"v": vec(1, 0, 0, 0), "cat": "A"})
            r.hset(pre + "2", mapping={"v": vec(0, 1, 0, 0), "cat": "B"})
            r.hset(pre + "3", mapping={"v": vec(0, 0, 1, 0), "cat": "A"})
            q = Query("*=>[KNN 2 @v $q AS score]").sort_by("score").return_fields("score").dialect(2)
            import time
            for _i in range(40):  # 回填是异步的
                res = ft.search(q, {"q": vec(0.9, 0.1, 0, 0)})
                if res.total >= 2:
                    break
                time.sleep(0.5)
            ids = [d.id.decode() if isinstance(d.id, bytes) else d.id for d in res.docs]
            eq(ids, [pre + "1", pre + "2"], "KNN order")
            hq = Query("(@cat:{A})=>[KNN 2 @v $q AS score]").sort_by("score").return_fields("score").dialect(2)
            res = ft.search(hq, {"q": vec(0.9, 0.1, 0, 0)})
            ids = [d.id.decode() if isinstance(d.id, bytes) else d.id for d in res.docs]
            eq(ids, [pre + "1", pre + "3"], "hybrid TAG filter")
            info = ft.info()
            if not info:
                raise AssertionError("empty FT.INFO")
            ft.dropindex()


if __name__ == "__main__":
    print(f"redis-py {redis.__version__} module={MODULE}")
    {"json": json_cases, "bloom": bloom_cases, "search": search_cases}[MODULE]()
    print(f"summary: {len(failures)} failed")
    sys.exit(1 if failures else 0)

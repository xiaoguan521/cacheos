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


def eq(got, want, what=""):
    if got != want:
        raise AssertionError(f"{what} got {got!r}, want {want!r}")


def vec(*xs):
    return struct.pack("<%df" % len(xs), *xs)


def json_cases():
    for proto in (2, 3):
        @case(f"json RESP{proto} set/get/path/arrappend/numincrby/type/del/mget")
        def _(proto=proto):
            r = redis.Redis(host=H, port=T["standalone"], protocol=proto, decode_responses=True)
            k = f"py:json:{proto}"
            eq(r.json().set(k, "$", {"n": 1, "tags": ["a", "b"], "nested": {"x": True}}), True, "set")
            eq(r.json().get(k, "$.tags[1]"), ["b"], "get path")
            eq(r.json().get(k)["nested"], {"x": True}, "get whole")
            eq(r.json().arrappend(k, "$.tags", "c"), [3], "arrappend")
            eq(r.json().numincrby(k, "$.n", 2), [3], "numincrby")
            eq(r.json().type(k, "$.tags"), ["array"], "type")
            eq(r.json().strlen(k, "$.tags[0]"), [1], "strlen")
            r.json().set(k + ":2", "$", {"n": 7})
            eq(r.json().mget([k, k + ":2"], "$.n"), [[3], [7]], "mget")
            eq(r.json().delete(k, "$.nested"), 1, "delete")
            eq(r.json().get(k, "$.nested"), [], "deleted path")
            eq(r.json().set(k, "$.n", 5, nx=True), None, "NX on existing path")

    @case("json cluster client routes by slot")
    def _():
        rc = RedisCluster(startup_nodes=[ClusterNode(H, p) for p in T["cluster"]], decode_responses=True)
        for i in range(60):
            rc.json().set(f"py:jc:{i}", "$", {"i": i})
        eq([rc.json().get(f"py:jc:{i}", "$.i") for i in range(60)], [[i] for i in range(60)], "cluster get")
        eq(rc.json().arrappend("py:jc:0", "$.l", 1), [None], "append to missing path")


def bloom_cases():
    for proto in (2, 3):
        @case(f"bloom RESP{proto} reserve/add/exists/madd/mexists/card/info")
        def _(proto=proto):
            r = redis.Redis(host=H, port=T["standalone"], protocol=proto, decode_responses=True)
            k = f"py:bf:{proto}"
            eq(r.bf().reserve(k, 0.01, 1000), True, "reserve")
            eq(r.bf().add(k, "a"), 1, "add new")
            eq(r.bf().add(k, "a"), 0, "add existing")
            eq(r.bf().exists(k, "a"), 1, "exists")
            eq(r.bf().exists(k, "zzz"), 0, "not exists")
            eq(r.bf().madd(k, "b", "c"), [1, 1], "madd")
            eq(r.bf().mexists(k, "a", "b", "nope"), [1, 1, 0], "mexists")
            eq(r.bf().card(k), 3, "card")
            info = r.bf().info(k)
            eq(info.capacity, 1000, "info capacity")
            eq(info.insertedNum, 3, "info items")

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

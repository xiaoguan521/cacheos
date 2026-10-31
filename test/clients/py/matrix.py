#!/usr/bin/env python3
"""redis-py 客户端对 sycache 的兼容性用例。由 test/clients/client_matrix_test.py 提供拓扑端口与证书。"""
import json
import os
import sys

import redis
from redis.cluster import ClusterNode, RedisCluster
from redis.sentinel import Sentinel

T = json.loads(os.environ["TOPOLOGY"])
H = "127.0.0.1"


def check(cond, msg):
    if not cond:
        print(f"FAIL: {msg}")
        sys.exit(1)
    print(f"ok   {msg}")


def standalone():
    for proto in (2, 3):
        r = redis.Redis(host=H, port=T["standalone"], protocol=proto, decode_responses=True)
        check(r.ping(), f"standalone RESP{proto} ping")
        r.set(f"py:k{proto}", "v", ex=60)
        check(r.get(f"py:k{proto}") == "v", f"standalone RESP{proto} set/get")
        r.hset("py:h", mapping={"a": "1", "b": "2"})
        check(r.hgetall("py:h") == {"a": "1", "b": "2"}, f"standalone RESP{proto} hash")
        pipe = r.pipeline(transaction=True)
        pipe.incr("py:n").incr("py:n")
        check(pipe.execute()[-1] >= 2, f"standalone RESP{proto} MULTI pipeline")
        script = r.register_script("return redis.call('GET', KEYS[1])")
        check(script(keys=[f"py:k{proto}"]) == "v", f"standalone RESP{proto} EVALSHA")
    r = redis.Redis(host=H, port=T["standalone"], decode_responses=True)
    p = r.pubsub()
    p.subscribe("py:ch")
    p.get_message(timeout=1)
    r.publish("py:ch", "hello")
    msg = p.get_message(timeout=2)
    check(msg and msg["data"] == "hello", "standalone pub/sub")
    check(r.info("server")["redis_version"] == "7.2.4", "INFO redis_version keeps compat value")


def cluster():
    rc = RedisCluster(startup_nodes=[ClusterNode(H, p) for p in T["cluster"]], decode_responses=True)
    for i in range(200):
        rc.set(f"py:c:{i}", i)
    check(all(rc.get(f"py:c:{i}") == str(i) for i in range(200)), "cluster 200 keys across slots")
    check(rc.mget(["py:{t}:1", "py:{t}:2"]) == [None, None], "cluster hash-tag MGET")


def sentinel():
    s = Sentinel([(H, p) for p in T["sentinels"]], socket_timeout=2)
    host, port = s.discover_master(T["master_name"])
    check(port == T["sentinel_primary"], "sentinel discover_master")
    m = s.master_for(T["master_name"], decode_responses=True)
    m.set("py:s", "1")
    check(m.get("py:s") == "1", "sentinel master_for write/read")


def tls():
    c = T["certs"]
    r = redis.Redis(host=H, port=T["tls"], ssl=True, ssl_ca_certs=c["ca.crt"], ssl_certfile=c["client.crt"],
                    ssl_keyfile=c["client.key"], decode_responses=True)
    r.set("py:tls", "1")
    check(r.get("py:tls") == "1", "TLS mutual auth set/get")


if __name__ == "__main__":
    print(f"redis-py {redis.__version__}")
    standalone()
    cluster()
    sentinel()
    tls()

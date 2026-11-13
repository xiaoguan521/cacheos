#!/usr/bin/env python3
"""扩展数据类型模块的客户端兼容测试（ADR 0002，CLAUDE.md「每个模块首次纳入须通过客户端兼容」）。

用真实客户端库（redis-py 6.4.0、go-redis 9.7.3，源码按 clients.lock 拉取）通过它们自带的模块 API
（JSON、BF、FT）访问加载了模块的 sycache：单机 RESP2 / RESP3，以及原生集群里按槽位路由（search 不支持集群，跳过）。

用法：MODULE=json|search|bloom MODULE_SO=<valkey-<名>.so> SYCACHE_BIN=<包里的 bin 目录> \
      python3 test/clients/module_clients_test.py -v
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "..", "compat"))
import compat_test as ct  # noqa: E402  复用进程管理与 RESP 客户端

MODULE = os.environ.get("MODULE", "")
MODULE_SO = os.path.abspath(os.environ.get("MODULE_SO", ""))
CACHE = subprocess.run([os.path.join(HERE, "fetch.sh")], check=True, capture_output=True, text=True).stdout.strip()


@unittest.skipUnless(MODULE in ("json", "search", "bloom") and os.path.isfile(MODULE_SO),
                     "set MODULE=json|search|bloom and MODULE_SO=<module .so>")
class ModuleClients(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        ct.CompatBase.port = int(os.environ.get("CLIENTS_BASE_PORT", "18500"))
        cls.env = ct.Cluster()
        nxt = ct.CompatBase.next_port
        load = [f"loadmodule {MODULE_SO}"]
        cls.topo = {"standalone": nxt(), "cluster": []}
        cls.env.start(ct.sy("sycache-server"), cls.topo["standalone"], conf_lines=load)
        if MODULE != "search":  # search 的索引是节点本地的，集群模式暂不支持（ADR 0002）
            cls.topo["cluster"] = [nxt() for _ in range(3)]
            for p in cls.topo["cluster"]:
                cls.env.start(ct.sy("sycache-server"), p, conf_lines=load + ["cluster-enabled yes", "cluster-node-timeout 3000"])
            subprocess.run([ct.sy("sycache-cli"), "--cluster", "create", *[f"127.0.0.1:{p}" for p in cls.topo["cluster"]],
                            "--cluster-yes"], check=True, capture_output=True, timeout=60)
            ct.wait_until(lambda: all("cluster_state:ok" in ct.Conn(p).cmd("CLUSTER", "INFO") for p in cls.topo["cluster"]),
                          what="cluster ok")

    @classmethod
    def tearDownClass(cls):
        cls.env.stop()

    def run_client(self, argv, env_extra, cwd=None):
        env = {**os.environ, "TOPOLOGY": json.dumps(self.topo), "MODULE": MODULE, **env_extra}
        out = subprocess.run(argv, env=env, cwd=cwd, capture_output=True, text=True, timeout=600)
        sys.stdout.write(out.stdout)
        oks = [l for l in out.stdout.splitlines() if l.startswith("ok ")]
        print(f"NOTICE: {MODULE} {os.path.basename(argv[-1])}: {len(oks)} checks passed")
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)

    def test_redis_py(self):
        self.run_client([sys.executable, os.path.join(HERE, "py", "modules.py")], {"PYTHONPATH": os.path.join(CACHE, "redis-py")})

    @unittest.skipUnless(shutil.which("go"), "go toolchain not found")
    def test_go_redis(self):
        work = tempfile.mkdtemp(prefix="sycache-gomodclient-")
        try:
            for f in ("main.go", "go.mod"):
                shutil.copy(os.path.join(HERE, "gomod", f), work)
            reps = {"github.com/redis/go-redis/v9": "go-redis", "github.com/cespare/xxhash/v2": "xxhash",
                    "github.com/dgryski/go-rendezvous": "go-rendezvous", "github.com/bsm/ginkgo/v2": "ginkgo",
                    "github.com/bsm/gomega": "gomega"}
            for mod, d in reps.items():
                src = os.path.join(CACHE, d)
                if not os.path.exists(os.path.join(src, "go.mod")):  # go-rendezvous 没有 go.mod：复制并补上
                    dst = os.path.join(work, "mods", d)
                    shutil.copytree(src, dst, ignore=shutil.ignore_patterns(".git"))
                    with open(os.path.join(dst, "go.mod"), "w") as f:
                        f.write(f"module {mod}\n\ngo 1.12\n")
                    src = dst
                subprocess.run(["go", "mod", "edit", f"-replace={mod}={src}"], cwd=work, check=True)
            goenv = {"GOFLAGS": "-mod=mod", "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local", "GOWORK": "off"}
            b = subprocess.run(["go", "build", "-o", "modclient", "."], cwd=work, env={**os.environ, **goenv},
                               capture_output=True, text=True, timeout=300)
            self.assertEqual(b.returncode, 0, b.stdout + b.stderr)
            self.run_client([os.path.join(work, "modclient")], {})
        finally:
            shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""客户端兼容矩阵（规格书 5.6）：用真实客户端库连接 sycache 的单机、集群、哨兵与 TLS 拓扑。

客户端源码按 clients.lock 拉取到缓存目录（test/clients/fetch.sh），离线运行：
  - redis-py：纯 Python，直接以 PYTHONPATH 引用源码
  - go-redis：以 go.mod replace 指向缓存目录，GOPROXY=off 构建

用法：SYCACHE_BIN=<安装包 bin 目录> [CLIENTS_CACHE=<缓存目录>] python3 test/clients/client_matrix_test.py -v
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
import compat_test as ct  # noqa: E402  复用进程管理、RESP 客户端与证书生成

CACHE = subprocess.run([os.path.join(HERE, "fetch.sh")], check=True, capture_output=True, text=True,
                       env={**os.environ}).stdout.strip()


class ClientMatrix(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        ct.CompatBase.port = int(os.environ.get("CLIENTS_BASE_PORT", "18300"))
        cls.env = ct.Cluster()
        nxt = ct.CompatBase.next_port
        topo = {"master_name": "mymaster"}
        # 单机
        topo["standalone"] = nxt()
        cls.env.start(ct.sy("sycache-server"), topo["standalone"])
        # TLS 单机（双向认证）
        certs = ct.make_certs(cls.env.dir)
        topo["certs"] = certs
        topo["tls"] = nxt()
        tls = ct.TlsTest("test_mutual_tls_and_tls_replication")
        tls.env = cls.env
        tls.start_tls(topo["tls"], certs)
        # 原生集群 3 主
        topo["cluster"] = [nxt() for _ in range(3)]
        for p in topo["cluster"]:
            cls.env.start(ct.sy("sycache-server"), p, conf_lines=["cluster-enabled yes", "cluster-node-timeout 3000"])
        subprocess.run([ct.sy("sycache-cli"), "--cluster", "create", *[f"127.0.0.1:{p}" for p in topo["cluster"]],
                        "--cluster-yes"], check=True, capture_output=True, timeout=60)
        ct.wait_until(lambda: all("cluster_state:ok" in ct.Conn(p).cmd("CLUSTER", "INFO") for p in topo["cluster"]),
                      what="cluster ok")
        # 哨兵：1 主 1 从 3 哨兵
        prim, rep = nxt(), nxt()
        topo["sentinel_primary"] = prim
        cls.env.start(ct.sy("sycache-server"), prim)
        cls.env.start(ct.sy("sycache-server"), rep, conf_lines=[f"replicaof 127.0.0.1 {prim}"])
        topo["sentinels"] = [nxt() for _ in range(3)]
        for s in topo["sentinels"]:
            cls.env.start(ct.sy("sycache-sentinel"), s, conf_lines=[f"sentinel monitor mymaster 127.0.0.1 {prim} 2"])
        ct.wait_until(lambda: ct.Conn(topo["sentinels"][0]).cmd(
            "SENTINEL", "GET-MASTER-ADDR-BY-NAME", "mymaster") == ["127.0.0.1", str(prim)], what="sentinel ready")
        cls.topo = topo

    @classmethod
    def tearDownClass(cls):
        cls.env.stop()

    def run_client(self, argv, env_extra, cwd=None):
        env = {**os.environ, "TOPOLOGY": json.dumps(self.topo), **env_extra}
        out = subprocess.run(argv, env=env, cwd=cwd, capture_output=True, text=True, timeout=300)
        sys.stdout.write(out.stdout)
        oks = [l for l in out.stdout.splitlines() if l.startswith("ok ")]
        first = out.stdout.splitlines()[0] if out.stdout else argv[-1]
        print(f"NOTICE: {first}: {len(oks)} checks passed")
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)

    def test_redis_py(self):
        self.run_client([sys.executable, os.path.join(HERE, "py", "matrix.py")],
                        {"PYTHONPATH": os.path.join(CACHE, "redis-py")})

    @unittest.skipUnless(shutil.which("go"), "go toolchain not found")
    def test_go_redis(self):
        work = tempfile.mkdtemp(prefix="sycache-goclient-")
        try:
            shutil.copy(os.path.join(HERE, "go", "main.go"), work)
            shutil.copy(os.path.join(HERE, "go", "go.mod"), work)
            reps = {"github.com/redis/go-redis/v9": "go-redis", "github.com/cespare/xxhash/v2": "xxhash",
                    "github.com/dgryski/go-rendezvous": "go-rendezvous", "github.com/bsm/ginkgo/v2": "ginkgo",
                    "github.com/bsm/gomega": "gomega"}
            for mod, d in reps.items():
                src = os.path.join(CACHE, d)
                if not os.path.exists(os.path.join(src, "go.mod")):
                    # go-rendezvous 早于 Go modules，没有 go.mod：复制一份并补上，replace 才能指向本地目录
                    dst = os.path.join(work, "mods", d)
                    shutil.copytree(src, dst, ignore=shutil.ignore_patterns(".git"))
                    with open(os.path.join(dst, "go.mod"), "w") as f:
                        f.write(f"module {mod}\n\ngo 1.12\n")
                    src = dst
                subprocess.run(["go", "mod", "edit", f"-replace={mod}={src}"], cwd=work, check=True)
            goenv = {"GOFLAGS": "-mod=mod", "GOPROXY": "off", "GOSUMDB": "off",
                     "GOTOOLCHAIN": "local", "GOWORK": "off"}
            b = subprocess.run(["go", "build", "-o", "matrix", "."], cwd=work, env={**os.environ, **goenv},
                               capture_output=True, text=True, timeout=300)
            self.assertEqual(b.returncode, 0, b.stdout + b.stderr)
            self.run_client([os.path.join(work, "matrix")], {})
        finally:
            shutil.rmtree(work, ignore_errors=True)


    @unittest.skipUnless(os.environ.get("JAVA_CLIENTS") == "1" and shutil.which("mvn"),
                         "set JAVA_CLIENTS=1 (needs mvn and access to a Maven repository)")
    def test_java_clients(self):
        work = tempfile.mkdtemp(prefix="sycache-javaclient-")
        try:
            shutil.copytree(os.path.join(HERE, "java"), work, dirs_exist_ok=True)
            b = subprocess.run(["mvn", "-B", "-q", "package", "-DskipTests"], cwd=work, capture_output=True,
                               text=True, timeout=900)
            self.assertEqual(b.returncode, 0, (b.stdout + b.stderr)[-4000:])
            c = self.topo["certs"]
            sep = os.pathsep
            cp = sep.join([os.path.join(work, "target", "classes"), os.path.join(work, "target", "lib", "*")])
            self.run_client(["java", "-cp", cp, "cacheos.ClientMatrix"], {
                "STANDALONE_PORT": str(self.topo["standalone"]), "TLS_PORT": str(self.topo["tls"]),
                "CLUSTER_PORTS": ",".join(map(str, self.topo["cluster"])),
                "SENTINEL_PORTS": ",".join(map(str, self.topo["sentinels"])),
                "MASTER_NAME": self.topo["master_name"],
                "TLS_CA": c["ca.crt"], "TLS_CERT": c["client.crt"], "TLS_KEY": c["client.key"]})
        finally:
            shutil.rmtree(work, ignore_errors=True)

    @unittest.skipUnless(os.environ.get("NODE_CLIENTS") == "1" and shutil.which("node") and shutil.which("npm"),
                         "set NODE_CLIENTS=1 (needs node and npm)")
    def test_node_clients(self):
        work = tempfile.mkdtemp(prefix="sycache-nodeclient-")
        try:
            source = os.path.join(HERE, "node")
            for name in ("package.json", "matrix.mjs"):
                shutil.copy(os.path.join(source, name), work)
            shutil.copy(os.path.join(source, "package-lock.json"), work)
            install = subprocess.run(["npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"],
                                     cwd=work, capture_output=True, text=True, timeout=600)
            self.assertEqual(install.returncode, 0, (install.stdout + install.stderr)[-6000:])
            self.run_client(["node", os.path.join(work, "matrix.mjs")], {})
        finally:
            shutil.rmtree(work, ignore_errors=True)

    @unittest.skipUnless(os.environ.get("DOTNET_CLIENTS") == "1" and shutil.which("dotnet"),
                         "set DOTNET_CLIENTS=1 (needs .NET SDK 8)")
    def test_stackexchange_redis(self):
        work = tempfile.mkdtemp(prefix="sycache-dotnetclient-")
        try:
            source = os.path.join(HERE, "dotnet")
            for name in ("ClientMatrix.csproj", "Program.cs"):
                shutil.copy(os.path.join(source, name), work)
            b = subprocess.run(["dotnet", "build", "--configuration", "Release", "--nologo"],
                               cwd=work, capture_output=True, text=True, timeout=900)
            self.assertEqual(b.returncode, 0, (b.stdout + b.stderr)[-6000:])
            self.run_client(["dotnet", "run", "--configuration", "Release", "--no-build", "--nologo"], {}, cwd=work)
        finally:
            shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    unittest.main()

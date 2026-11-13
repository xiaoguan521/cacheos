#!/usr/bin/env python3
"""collect-licenses.py 的单元测试：用假源码树验证许可文本收集与 SBOM 内容。
用法：python3 -I test/modules/collect-licenses-test.py
"""
import importlib.util, json, os, sys, tempfile, unittest

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("cl", os.path.join(HERE, "..", "..", "dataplane", "modules", "collect-licenses.py"))
cl = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cl)


def fake_common(d):
    w(d + "/common/LGPL-2.1", "lgpl text")
    cl.COMMON_LICENSES = d + "/common"


def w(path, text="x"):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    open(path, "w").write(text)


def fake_grpc(d, skip=()):
    """假的 gRPC 检出：根目录 + 各静态链接组件的许可文本（upb 与上游一致，没有自己的许可文件）。"""
    g = os.path.join(d, "grpc")
    w(g + "/LICENSE", "grpc apache")
    w(g + "/NOTICE.txt", "grpc notice")
    for rel, _label, _lic, covered in cl.GRPC_COMPONENTS:
        if rel in skip:
            continue
        if covered:
            os.makedirs(os.path.join(g, rel), exist_ok=True)
        else:
            w(os.path.join(g, rel, "LICENSE.md" if rel.endswith("cares/cares") else "LICENSE"), "text of " + rel)
    return g


class T(unittest.TestCase):
    def test_root_and_third_party(self):
        with tempfile.TemporaryDirectory() as d:
            src, out = os.path.join(d, "src"), os.path.join(d, "out")
            w(src + "/LICENSE", "root license")
            w(src + "/third_party/hnswlib/LICENSE", "apache")
            w(src + "/third_party/snowball/COPYING", "bsd")
            w(src + "/third_party/nolicense/readme.txt")
            fake_common(d)
            res = cl.collect("search", "1.2.1", "BSD-3-Clause", src, out, deps=[], grpc_dir=fake_grpc(d))
            self.assertTrue(os.path.isfile(out + "/licenses/search/LICENSE"))
            self.assertTrue(os.path.isfile(out + "/licenses/search/third_party/hnswlib/LICENSE"))
            self.assertTrue(os.path.isfile(out + "/licenses/search/third_party/snowball/COPYING"))
            self.assertEqual(res["missing"], ["third_party/nolicense"])

    def test_missing_root_license_is_error(self):
        with tempfile.TemporaryDirectory() as d:
            src = os.path.join(d, "src"); os.makedirs(src)
            with self.assertRaises(SystemExit):
                cl.collect("json", "1.0.3", "BSD-3-Clause", src, os.path.join(d, "out"), deps=[])

    def test_sbom_deterministic_and_content(self):
        deps = [{"name": "serde", "version": "1.0.0", "license": "MIT OR Apache-2.0"}]
        with tempfile.TemporaryDirectory() as d:
            src = os.path.join(d, "src"); w(src + "/LICENSE")
            os.environ["SOURCE_DATE_EPOCH"] = "1700000000"
            cl.collect("bloom", "1.0.1", "BSD-3-Clause", src, d + "/o1", deps=deps)
            cl.collect("bloom", "1.0.1", "BSD-3-Clause", src, d + "/o2", deps=deps)
            a = open(d + "/o1/sbom-bloom.spdx.json").read()
            self.assertEqual(a, open(d + "/o2/sbom-bloom.spdx.json").read())
            doc = json.loads(a)
            names = {p["name"] for p in doc["packages"]}
            self.assertEqual(names, {"valkey-bloom", "serde"})
            self.assertEqual(doc["spdxVersion"], "SPDX-2.3")

    def test_search_declares_libsystemd_dynamic_link(self):
        with tempfile.TemporaryDirectory() as d:
            src = os.path.join(d, "src"); w(src + "/LICENSE")
            fake_common(d)
            cl.collect("search", "1.2.1", "BSD-3-Clause", src, d + "/o", deps=[], grpc_dir=fake_grpc(d))
            self.assertTrue(os.path.isfile(d + "/o/licenses/search/libsystemd-LGPL-2.1.txt"))
            doc = json.load(open(d + "/o/sbom-search.spdx.json"))
            p = [p for p in doc["packages"] if p["name"] == "libsystemd"]
            self.assertEqual(len(p), 1)
            self.assertEqual(p[0]["licenseDeclared"], "LGPL-2.1-or-later")
            self.assertTrue(any(r["relationshipType"] == "DYNAMIC_LINK" for r in doc["relationships"]))

    def test_search_collects_grpc_static_components(self):
        with tempfile.TemporaryDirectory() as d:
            src = os.path.join(d, "src"); w(src + "/LICENSE")
            fake_common(d)
            cl.collect("search", "1.2.1", "BSD-3-Clause", src, d + "/o", deps=[], grpc_dir=fake_grpc(d))
            base = d + "/o/licenses/search/grpc"
            for f in ("LICENSE", "NOTICE.txt"):
                self.assertTrue(os.path.isfile(base + "/" + f), f)
            for name in ("abseil-cpp", "protobuf", "c-ares", "zlib", "utf8_range", "address_sorting",
                         "envoy-api", "xds"):
                self.assertTrue(os.listdir(base + "/third_party/" + name), name)
            self.assertEqual(open(base + "/third_party/c-ares/LICENSE.md").read(), "text of third_party/cares/cares")
            doc = json.load(open(d + "/o/sbom-search.spdx.json"))
            pk = {p["name"]: p for p in doc["packages"]}
            for name in ("grpc", "abseil-cpp", "protobuf", "c-ares", "zlib", "upb", "utf8_range", "envoy-api", "xds"):
                self.assertIn(name, pk)
            self.assertEqual(pk["grpc"]["versionInfo"], "1.70.1")
            self.assertEqual(pk["zlib"]["licenseDeclared"], "Zlib")
            self.assertIn("protobuf", pk["upb"]["comment"])
            deps = [r for r in doc["relationships"] if r["relationshipType"] == "STATIC_LINK"]
            self.assertGreaterEqual(len(deps), len(cl.GRPC_COMPONENTS) + 1)

    def test_search_requires_grpc_and_every_component_license(self):
        with tempfile.TemporaryDirectory() as d:
            src = os.path.join(d, "src"); w(src + "/LICENSE")
            fake_common(d)
            with self.assertRaises(SystemExit):  # 没有 gRPC 检出
                cl.collect("search", "1.2.1", "BSD-3-Clause", src, d + "/o1", deps=[])
            with self.assertRaises(SystemExit):  # 缺某个组件的许可文本
                cl.collect("search", "1.2.1", "BSD-3-Clause", src, d + "/o2", deps=[],
                           grpc_dir=fake_grpc(d, skip=("third_party/abseil-cpp",)))

    def test_json_and_bloom_do_not_need_grpc(self):
        with tempfile.TemporaryDirectory() as d:
            src = os.path.join(d, "src"); w(src + "/LICENSE")
            cl.collect("json", "1.0.3", "BSD-3-Clause", src, d + "/o", deps=[])
            self.assertFalse(os.path.exists(d + "/o/licenses/json/grpc"))

    def test_grpc_pin_constants(self):
        self.assertEqual(cl.GRPC_TAG, "v1.70.1")
        self.assertRegex(cl.GRPC_COMMIT, r"^[0-9a-f]{40}$")

    def test_find_grpc_checkout(self):
        with tempfile.TemporaryDirectory() as d:
            self.assertIsNone(cl.find_grpc_checkout(d))
            g = d + "/.build-release/.deps/grpc"
            w(g + "/LICENSE")
            self.assertEqual(cl.find_grpc_checkout(d), g)


class CrateFallbackTest(unittest.TestCase):
    """发布的 crate 不带许可文本时的两种补法：同一提交的兄弟 crate、入库的 extra-licenses。"""

    def mk_crate(self, root, name, sha, with_license):
        d = os.path.join(root, name)
        w(d + "/.cargo_vcs_info.json", '{"git": {"sha1": "%s"}}' % sha)
        if with_license:
            w(d + "/LICENSE", "bsd text")
        return d

    def test_sibling_in_same_commit_and_extra_dir(self):
        with tempfile.TemporaryDirectory() as d:
            src = os.path.join(d, "src"); w(src + "/LICENSE")
            crates = os.path.join(d, "crates")
            main = self.mk_crate(crates, "vm", "aaa", True)
            macros = self.mk_crate(crates, "vm-macros", "aaa", False)  # 同一提交，继承 vm 的许可文本
            other = self.mk_crate(crates, "rel", "bbb", False)  # 没有兄弟，用 extra-licenses
            lone = self.mk_crate(crates, "lone", "ccc", False)  # 两种都补不上：记为缺失
            w(d + "/extra/rel-1.0/LICENSE-MIT", "mit text")
            deps = [{"name": "vm", "version": "1.0", "license": "BSD-3-Clause", "dir": main},
                    {"name": "vm-macros", "version": "1.0", "license": "BSD-3-Clause", "dir": macros},
                    {"name": "rel", "version": "1.0", "license": "MIT", "dir": other},
                    {"name": "lone", "version": "1.0", "license": "MIT", "dir": lone}]
            res = cl.collect("bloom", "1.0.1", "BSD-3-Clause", src, d + "/out", deps=deps, extra_dir=d + "/extra")
            base = d + "/out/licenses/bloom/crates/"
            self.assertTrue(os.path.isfile(base + "vm-macros-1.0/LICENSE"))
            self.assertTrue(os.path.isfile(base + "rel-1.0/LICENSE-MIT"))
            self.assertEqual(res["missing"], ["crates/lone-1.0"])


class MarkdownTest(unittest.TestCase):
    def test_bloom_table_is_sorted_linked_and_deterministic(self):
        deps = [
            {"name": "serde", "version": "1.0.1", "license": "MIT OR Apache-2.0", "repository": "https://github.com/serde-rs/serde"},
            {"name": "bincode", "version": "1.3.3", "license": "MIT", "repository": None},
        ]
        a = cl.markdown_crates("bloom", "1.0.1", "BSD-3-Clause", "https://github.com/valkey-io/valkey-bloom",
                               "43b4e704bc8d6bd5f65529c8bd7e8488f2d54080", deps)
        self.assertEqual(a, cl.markdown_crates("bloom", "1.0.1", "BSD-3-Clause", "https://github.com/valkey-io/valkey-bloom",
                                               "43b4e704bc8d6bd5f65529c8bd7e8488f2d54080", list(reversed(deps))))
        self.assertLess(a.index("bincode"), a.index("serde"))  # 按名称排序
        self.assertIn("https://crates.io/crates/serde/1.0.1", a)
        self.assertIn("https://github.com/serde-rs/serde", a)
        self.assertIn("| bincode | 1.3.3 | MIT |", a)
        self.assertIn("2 个", a)  # 依赖总数写在文件里，数量变化会让 diff 失败
        self.assertIn("collect-licenses.py", a)  # 注明由脚本生成


if __name__ == "__main__":
    unittest.main()

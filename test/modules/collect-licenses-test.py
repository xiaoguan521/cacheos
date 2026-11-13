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


class T(unittest.TestCase):
    def test_root_and_third_party(self):
        with tempfile.TemporaryDirectory() as d:
            src, out = os.path.join(d, "src"), os.path.join(d, "out")
            w(src + "/LICENSE", "root license")
            w(src + "/third_party/hnswlib/LICENSE", "apache")
            w(src + "/third_party/snowball/COPYING", "bsd")
            w(src + "/third_party/nolicense/readme.txt")
            fake_common(d)
            res = cl.collect("search", "1.2.1", "BSD-3-Clause", src, out, deps=[])
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
            cl.collect("search", "1.2.1", "BSD-3-Clause", src, d + "/o", deps=[])
            self.assertTrue(os.path.isfile(d + "/o/licenses/search/libsystemd-LGPL-2.1.txt"))
            doc = json.load(open(d + "/o/sbom-search.spdx.json"))
            p = [p for p in doc["packages"] if p["name"] == "libsystemd"]
            self.assertEqual(len(p), 1)
            self.assertEqual(p[0]["licenseDeclared"], "LGPL-2.1-or-later")
            self.assertTrue(any(r["relationshipType"] == "DYNAMIC_LINK" for r in doc["relationships"]))


if __name__ == "__main__":
    unittest.main()

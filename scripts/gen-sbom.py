#!/usr/bin/env python3
"""生成 sycache 安装包的 SPDX 2.3 SBOM（规格书 12 节）。只用 Python 标准库。

用法：gen-sbom.py <已打 patch 的上游源码目录> <BUILDINFO> <输出 json>

内容：产品包、上游引擎（真实版本与提交哈希）、随引擎捆绑的第三方库、静态链接的 OpenSSL；
时间戳取 SOURCE_DATE_EPOCH，同一输入生成逐字节相同的文件。
"""
import datetime
import json
import os
import re
import sys


def read(path):
    with open(path, encoding="utf-8", errors="replace") as f:
        return f.read()


def macro(text, name):
    m = re.search(rf"#define\s+{name}\s+\"?([^\"\s]+)\"?", text)
    return m.group(1) if m else None


def dep_versions(src):
    deps = os.path.join(src, "deps")
    out = {}
    jv = os.path.join(deps, "jemalloc", "VERSION")
    if os.path.exists(jv):
        out["jemalloc"] = read(jv).strip().split("-")[0]
    lua = os.path.join(deps, "lua", "src", "lua.h")
    if os.path.exists(lua):
        m = re.search(r'#define LUA_RELEASE\s+"Lua ([0-9.]+)"', read(lua))
        out["lua"] = m.group(1) if m else None
    lv = os.path.join(deps, "libvalkey", "include", "valkey", "valkey.h")
    if os.path.exists(lv):
        t = read(lv)
        parts = [macro(t, f"LIBVALKEY_VERSION_{p}") for p in ("MAJOR", "MINOR", "PATCH")]
        out["libvalkey"] = ".".join(parts) if all(parts) else None
    return out


# 名称 → (许可证, purl 模板, 下载位置)；版本未知时 purl 不带版本
BUNDLED = {
    "jemalloc": ("BSD-2-Clause", "pkg:github/jemalloc/jemalloc", "https://github.com/jemalloc/jemalloc"),
    "lua": ("MIT", "pkg:generic/lua", "https://www.lua.org/"),
    "libvalkey": ("BSD-3-Clause", "pkg:github/valkey-io/libvalkey", "https://github.com/valkey-io/libvalkey"),
    "linenoise": ("BSD-2-Clause", "pkg:github/antirez/linenoise", "https://github.com/antirez/linenoise"),
    "hdr_histogram": ("BSD-2-Clause OR CC0-1.0", "pkg:github/HdrHistogram/HdrHistogram_c",
                      "https://github.com/HdrHistogram/HdrHistogram_c"),
    "fpconv": ("BSL-1.0", "pkg:github/night-shift/fpconv", "https://github.com/night-shift/fpconv"),
    "fast_float": ("Apache-2.0 OR MIT OR BSL-1.0", "pkg:github/fastfloat/fast_float",
                   "https://github.com/fastfloat/fast_float"),
}


def pkg(spdx_id, name, version, license_, purl, location, supplier="NOASSERTION", comment=None):
    p = {
        "SPDXID": spdx_id,
        "name": name,
        "versionInfo": version or "NOASSERTION",
        "supplier": supplier,
        "downloadLocation": location,
        "filesAnalyzed": False,
        "licenseConcluded": license_,
        "licenseDeclared": license_,
        "copyrightText": "NOASSERTION",
    }
    if purl:
        p["externalRefs"] = [{"referenceCategory": "PACKAGE-MANAGER", "referenceType": "purl",
                              "referenceLocator": f"{purl}@{version}" if version else purl}]
    if comment:
        p["comment"] = comment
    return p


def main():
    if len(sys.argv) != 4:
        sys.exit(__doc__)
    src, buildinfo, out = sys.argv[1:]
    bi = dict(line.split("=", 1) for line in read(buildinfo).splitlines() if "=" in line)
    epoch = int(os.environ.get("SOURCE_DATE_EPOCH", "0"))
    created = datetime.datetime.fromtimestamp(epoch, datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")

    product = pkg("SPDXRef-Package-sycache", "sycache", bi["product_version"], "LicenseRef-CacheOS-Proprietary",
                  None, "NOASSERTION", supplier="Organization: CacheOS",
                  comment=f"patches: {bi.get('patches', '')}; patch_sha256: {bi.get('patch_sha256', '')}; "
                          f"source_tree_sha: {bi.get('source_tree_sha', '')}; tls: {bi.get('tls', '')}")
    engine = pkg("SPDXRef-Package-valkey", "valkey", bi["upstream_version"], "BSD-3-Clause",
                 "pkg:github/valkey-io/valkey", "git+https://github.com/valkey-io/valkey.git@" + bi["upstream_commit"],
                 supplier="Organization: Valkey contributors (Linux Foundation)",
                 comment="modified by CacheOS: string literals and version macros only, see patches")
    packages, rels = [product, engine], []
    rels.append({"spdxElementId": "SPDXRef-DOCUMENT", "relationshipType": "DESCRIBES",
                 "relatedSpdxElement": product["SPDXID"]})
    rels.append({"spdxElementId": product["SPDXID"], "relationshipType": "CONTAINS",
                 "relatedSpdxElement": engine["SPDXID"]})

    versions = dep_versions(src)
    for name in sorted(BUNDLED):
        if not os.path.isdir(os.path.join(src, "deps", name)):
            continue
        lic, purl, loc = BUNDLED[name]
        p = pkg(f"SPDXRef-Package-{name.replace('_', '-')}", name, versions.get(name), lic, purl, loc,
                comment=f"bundled in valkey {bi['upstream_version']} deps/{name}")
        packages.append(p)
        rels.append({"spdxElementId": engine["SPDXID"], "relationshipType": "CONTAINS",
                     "relatedSpdxElement": p["SPDXID"]})

    m = re.match(r"openssl-([0-9.]+)-static", bi.get("tls", ""))
    if m:
        p = pkg("SPDXRef-Package-openssl", "openssl", m.group(1), "Apache-2.0", "pkg:github/openssl/openssl",
                "https://github.com/openssl/openssl", supplier="Organization: OpenSSL Foundation",
                comment="statically linked")
        packages.append(p)
        rels.append({"spdxElementId": product["SPDXID"], "relationshipType": "STATIC_LINK",
                     "relatedSpdxElement": p["SPDXID"]})

    doc = {
        "spdxVersion": "SPDX-2.3",
        "dataLicense": "CC0-1.0",
        "SPDXID": "SPDXRef-DOCUMENT",
        "name": f"sycache-{bi['product_version']}-{bi['arch']}",
        "documentNamespace": f"https://cacheos.invalid/spdx/sycache-{bi['product_version']}-{bi['arch']}-"
                             f"{bi.get('patch_sha256', '')[:16]}",
        "creationInfo": {"created": created, "creators": ["Tool: cacheos-gen-sbom"]},
        "hasExtractedLicensingInfos": [{
            "licenseId": "LicenseRef-CacheOS-Proprietary",
            "extractedText": "Proprietary. See /home/atwasoft/sycache/licenses/NOTICE for third-party components.",
        }],
        "packages": packages,
        "relationships": rels,
    }
    with open(out, "w", encoding="utf-8") as f:
        json.dump(doc, f, ensure_ascii=False, indent=2, sort_keys=True)
        f.write("\n")


if __name__ == "__main__":
    main()

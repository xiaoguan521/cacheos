#!/usr/bin/env python3
"""收集扩展模块的许可文本并生成 SPDX 2.3 SBOM（ADR 0002）。只用标准库，外部命令一律切片调用。

用法：collect-licenses.py <json|search|bloom> <输出目录> [--src <已检出的模块源码目录>] [--markdown <LICENSES.md 输出路径，仅 bloom>]

输出：
  <输出目录>/licenses/<模块>/…           上游 LICENSE/COPYING/NOTICE，以及 third_party/ 下各库、bloom 各 crate 的许可文本
  <输出目录>/sbom-<模块>.spdx.json        模块 + 直接依赖 + 动态链接的系统库；时间戳取 SOURCE_DATE_EPOCH，输出可复现

search 另收集静态链接的 gRPC 及其子模块许可（licenses/search/grpc/…，SBOM 列为 STATIC_LINK）：优先取源码目录下
.build-*/.deps/grpc（构建用的同一份检出），没有则按固定标签 v1.70.1 + commit 克隆并核对。
不带 --src 时按 dataplane/modules/MODULES 浅克隆固定标签，并核对 commit，不一致即失败。
模块根目录没有许可文本视为错误；third_party 下缺许可文本的库记入 SBOM 的 comment 并在标准输出告警。
"""
import datetime, glob, json, os, re, shutil, subprocess, sys, tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
LICENSE_RE = re.compile(r"^(LICEN[CS]E|COPYING|NOTICE|UNLICENSE|COPYRIGHT)", re.I)

# 动态链接的系统库（CLAUDE.md 允许的 LGPL 例外，目前仅 libsystemd，由 CI 的 readelf 检查确认）
DYNAMIC_LIBS = {"search": [("libsystemd", "LGPL-2.1-or-later", "https://github.com/systemd/systemd")]}


# valkey-search 构建时由 submodules/CMakeLists.txt 从固定标签克隆 gRPC 并静态链接进 .so（见 ADR 0002）。
# 许可文本取自与构建同一份检出（<源码>/.build-release/.deps/grpc）；缓存命中没有检出时，按下面的固定标签 + commit 克隆。
GRPC_TAG, GRPC_VERSION = "v1.70.1", "1.70.1"
GRPC_COMMIT = "5e099002c1600c580ebe1e6741f8ff8b182ffea4"
# (gRPC 检出内的相对路径, 名称, SPDX 许可, covered_by)；covered_by 非 None 表示该目录没有独立许可文件，由所述组件的许可覆盖
GRPC_COMPONENTS = [
    ("third_party/abseil-cpp", "abseil-cpp", "Apache-2.0", None),
    ("third_party/protobuf", "protobuf", "BSD-3-Clause", None),
    ("third_party/cares/cares", "c-ares", "MIT", None),
    ("third_party/zlib", "zlib", "Zlib", None),
    ("third_party/upb", "upb", "BSD-3-Clause", "protobuf"),
    ("third_party/utf8_range", "utf8_range", "MIT", None),
    ("third_party/address_sorting", "address_sorting", "BSD-3-Clause", None),
    ("third_party/envoy-api", "envoy-api", "Apache-2.0", None),
    ("third_party/xds", "xds", "Apache-2.0", None),
]


def find_grpc_checkout(src):
    for d in sorted(glob.glob(os.path.join(src, ".build-*", ".deps", "grpc"))):
        if os.path.isfile(os.path.join(d, "LICENSE")):
            return d
    return None


def clone_grpc(dst):
    subprocess.run(["git", "-c", "advice.detachedHead=false", "clone", "-q", "--branch", GRPC_TAG, "--depth", "1",
                    "--recurse-submodules", "--shallow-submodules", "--single-branch",
                    "https://github.com/grpc/grpc", dst], check=True)
    head = subprocess.run(["git", "-C", dst, "rev-parse", "HEAD"], capture_output=True, text=True,
                          check=True).stdout.strip()
    if head != GRPC_COMMIT:
        sys.exit("ERROR: gRPC 标签 %s 指向 %s，与固定的 %s 不一致" % (GRPC_TAG, head, GRPC_COMMIT))
    return dst


def collect_grpc(grpc_dir, lic_out):
    """复制 gRPC 及其静态链接组件的许可文本到 lic_out/grpc；返回 [(名称, 许可, covered_by)]。缺文本即失败。"""
    base = os.path.join(lic_out, "grpc")
    if not copy_licenses(grpc_dir, base):
        sys.exit("ERROR: gRPC 检出 %s 没有 LICENSE" % grpc_dir)
    found, missing = [], []
    for rel, name, lic, covered in GRPC_COMPONENTS:
        if covered:
            found.append((name, lic, covered))
        elif copy_licenses(os.path.join(grpc_dir, rel), os.path.join(base, "third_party", name)):
            found.append((name, lic, None))
        else:
            missing.append(rel)
    if missing:
        sys.exit("ERROR: gRPC 静态链接组件缺许可文本，不能发布: " + ", ".join(missing))
    return found


def license_files(d):
    try:
        return sorted(f for f in os.listdir(d) if LICENSE_RE.match(f) and os.path.isfile(os.path.join(d, f)))
    except OSError:
        return []


def copy_licenses(src_dir, dst_dir):
    files = license_files(src_dir)
    for f in files:
        os.makedirs(dst_dir, exist_ok=True)
        shutil.copy2(os.path.join(src_dir, f), os.path.join(dst_dir, f))
    return files


def spdx_pkg(sid, name, version, lic, location="NOASSERTION", comment=None):
    p = {"SPDXID": sid, "name": name, "versionInfo": version or "NOASSERTION", "supplier": "NOASSERTION",
         "downloadLocation": location, "filesAnalyzed": False, "licenseConcluded": lic, "licenseDeclared": lic,
         "copyrightText": "NOASSERTION"}
    if comment:
        p["comment"] = comment
    return p


def sid(prefix, name, version=""):
    return "SPDXRef-%s-%s" % (prefix, re.sub(r"[^A-Za-z0-9.-]", "-", name + ("-" + version if version else "")))


COMMON_LICENSES = os.environ.get("COMMON_LICENSES_DIR", "/usr/share/common-licenses")


def vcs_sha(crate_dir):
    try:
        with open(os.path.join(crate_dir, ".cargo_vcs_info.json"), encoding="utf-8") as f:
            return json.load(f).get("git", {}).get("sha1")
    except (OSError, ValueError):
        return None


def collect(module, version, license_, src, out, deps, repo="NOASSERTION", commit="", extra_dir=None, grpc_dir=None):
    """收集许可文本并写 SBOM。deps 为 [{name, version, license, dir?}]；返回 {"missing": [...]}。"""
    lic_out = os.path.join(out, "licenses", module)
    if not copy_licenses(src, lic_out):
        sys.exit("ERROR: 模块 %s 的源码根目录没有 LICENSE/COPYING 文件，不能发布" % module)

    missing = []
    tp = os.path.join(src, "third_party")
    if os.path.isdir(tp):
        for name in sorted(os.listdir(tp)):
            d = os.path.join(tp, name)
            if os.path.isdir(d) and not copy_licenses(d, os.path.join(lic_out, "third_party", name)):
                missing.append("third_party/" + name)

    extra_dir = extra_dir or os.path.join(HERE, module, "extra-licenses")
    by_sha = {}  # 提交 → 带许可文本的 crate 目录；子 crate（如 xxx-macros）没带 LICENSE 时继承同一提交的根目录许可
    for dep in deps:
        d = dep.get("dir")
        if d and license_files(d) and vcs_sha(d):
            by_sha.setdefault(vcs_sha(d), d)
    for dep in deps:
        d = dep.get("dir")
        if not d:
            continue
        key = "%s-%s" % (dep["name"], dep["version"])
        dst = os.path.join(lic_out, "crates", key)
        if copy_licenses(d, dst):
            continue
        sibling = by_sha.get(vcs_sha(d) or "")
        if sibling and copy_licenses(sibling, dst):
            continue
        # 入库的 extra-licenses/<名>-<版本>/：原样取自上游仓库并记录来源（SOURCE.txt）
        if copy_licenses(os.path.join(extra_dir, key), dst):
            shutil.copy2(os.path.join(extra_dir, key, "SOURCE.txt"), os.path.join(dst, "SOURCE.txt")) if os.path.isfile(os.path.join(extra_dir, key, "SOURCE.txt")) else None
            continue
        missing.append("crates/%s" % key)

    grpc_parts = []
    if module == "search":  # gRPC 及其子模块静态链接进 libsearch.so，必须随包附许可文本
        if not grpc_dir:
            sys.exit("ERROR: search 需要 gRPC 检出目录以收集静态链接组件的许可文本")
        grpc_parts = collect_grpc(grpc_dir, lic_out)

    if module in DYNAMIC_LIBS:  # 动态链接的 LGPL 库：发布包须附其许可文本（CLAUDE.md）
        lgpl = os.path.join(COMMON_LICENSES, "LGPL-2.1")
        if not os.path.isfile(lgpl):
            sys.exit("ERROR: 找不到 %s，无法附上 libsystemd 的许可文本" % lgpl)
        shutil.copy2(lgpl, os.path.join(lic_out, "libsystemd-LGPL-2.1.txt"))

    epoch = int(os.environ.get("SOURCE_DATE_EPOCH", "0"))
    created = datetime.datetime.fromtimestamp(epoch, datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    main = spdx_pkg("SPDXRef-Package-module", "valkey-" + module, version, license_,
                    ("git+%s@%s" % (repo, commit)) if commit else repo,
                    comment=("许可文本缺失的 third_party 目录: " + ", ".join(missing)) if missing else None)
    packages, rels = [main], [{"spdxElementId": "SPDXRef-DOCUMENT", "relationshipType": "DESCRIBES",
                               "relatedSpdxElement": main["SPDXID"]}]
    for dep in sorted(deps, key=lambda x: (x["name"], x["version"])):
        p = spdx_pkg(sid("Package", dep["name"], dep["version"]), dep["name"], dep["version"],
                     dep.get("license") or "NOASSERTION")
        packages.append(p)
        rels.append({"spdxElementId": main["SPDXID"], "relationshipType": "DEPENDS_ON", "relatedSpdxElement": p["SPDXID"]})
    if grpc_parts:
        g = spdx_pkg(sid("Package", "grpc", GRPC_VERSION), "grpc", GRPC_VERSION, "Apache-2.0",
                     "git+https://github.com/grpc/grpc@" + GRPC_COMMIT, comment="静态链接，固定标签 " + GRPC_TAG)
        packages.append(g)
        rels.append({"spdxElementId": main["SPDXID"], "relationshipType": "STATIC_LINK", "relatedSpdxElement": g["SPDXID"]})
        for name, lic, covered in sorted(grpc_parts):
            p = spdx_pkg(sid("Package", name, "grpc"), name, None, lic, "git+https://github.com/grpc/grpc@" + GRPC_COMMIT,
                         comment=("gRPC %s 的子模块，静态链接" % GRPC_TAG) +
                                 (("；没有独立许可文件，由 %s 的许可覆盖" % covered) if covered else ""))
            packages.append(p)
            rels.append({"spdxElementId": main["SPDXID"], "relationshipType": "STATIC_LINK", "relatedSpdxElement": p["SPDXID"]})
    for name, lic, loc in DYNAMIC_LIBS.get(module, []):
        p = spdx_pkg(sid("Package", name), name, None, lic, loc, comment="动态链接的系统库，发布包不含其二进制")
        packages.append(p)
        rels.append({"spdxElementId": main["SPDXID"], "relationshipType": "DYNAMIC_LINK", "relatedSpdxElement": p["SPDXID"]})

    doc = {"spdxVersion": "SPDX-2.3", "dataLicense": "CC0-1.0", "SPDXID": "SPDXRef-DOCUMENT",
           "name": "valkey-%s-%s" % (module, version),
           "documentNamespace": "https://cacheos.invalid/spdx/valkey-%s-%s-%s" % (module, version, commit[:16] or "local"),
           "creationInfo": {"created": created, "creators": ["Tool: cacheos-collect-licenses"]},
           "packages": packages, "relationships": rels}
    os.makedirs(out, exist_ok=True)
    with open(os.path.join(out, "sbom-%s.spdx.json" % module), "w", encoding="utf-8") as f:
        json.dump(doc, f, ensure_ascii=False, indent=2, sort_keys=True)
        f.write("\n")
    for m in missing:
        print("  WARNING: %s 没有许可文本" % m, file=sys.stderr)
    return {"missing": missing}


def markdown_crates(module, version, license_, repo, commit, deps):
    """生成 Rust 模块的 LICENSES.md：模块自身 + 全部依赖 crate（名称、版本、许可、来源链接）。输出只取决于输入，可在 CI 里与入库文件比对。"""
    lines = [
        f"# valkey-{module} {version} 许可审查",
        "",
        "本文件由 dataplane/modules/collect-licenses.py 根据 `cargo metadata --locked`（Cargo.lock 已入库）生成，请勿手改；",
        "CI 会重新生成并与本文件比对，依赖变化后须更新。依赖许可另由 dataplane/modules/cargo-license-check.py 在构建时按白名单校验。",
        "",
        f"- 模块：valkey-{module} {version}，{license_}",
        f"- 上游：{repo}（提交 {commit}）",
        f"- 许可文本：{repo}/blob/{version}/LICENSE",
        f"- 依赖 crate（不含模块自身）共 {len(deps)} 个，许可文本随发布包附在 licenses/modules/{module}/crates/ 下",
        "",
        "| crate | 版本 | 许可（SPDX 表达式） | 来源 |",
        "|---|---|---|---|",
    ]
    for d in sorted(deps, key=lambda x: (x["name"], x["version"])):
        links = [f"https://crates.io/crates/{d['name']}/{d['version']}"]
        if d.get("repository"):
            links.append(d["repository"])
        lines.append(f"| {d['name']} | {d['version']} | {d.get('license') or 'NOASSERTION'} | {' ; '.join(links)} |")
    return "\n".join(lines) + "\n"


def read_manifest(module):
    for line in open(os.path.join(HERE, "MODULES"), encoding="utf-8"):
        f = line.split()
        if f and not f[0].startswith("#") and f[0] == module:
            return {"version": f[1], "repo": f[2], "tag": f[3], "license": f[4], "commit": f[5]}
    sys.exit("ERROR: MODULES 中没有 " + module)


def cargo_deps(src):
    lock = os.path.join(HERE, "bloom", "Cargo.lock")
    if os.path.isfile(lock):
        shutil.copy2(lock, os.path.join(src, "Cargo.lock"))
    r = subprocess.run(["cargo", "metadata", "--format-version", "1", "--locked"], cwd=src,
                       capture_output=True, text=True)
    if r.returncode != 0:
        sys.exit("ERROR: cargo metadata 失败: " + r.stderr[-500:])
    meta = json.loads(r.stdout)
    roots = set(meta["workspace_members"])
    return [{"name": p["name"], "version": p["version"], "license": p.get("license"), "repository": p.get("repository"),
             "dir": os.path.dirname(p["manifest_path"])} for p in meta["packages"] if p["id"] not in roots]


def main():
    args = sys.argv[1:]
    src = None
    if "--src" in args:
        i = args.index("--src")
        src = os.path.abspath(args[i + 1])
        del args[i:i + 2]
    markdown = None
    if "--markdown" in args:
        i = args.index("--markdown")
        markdown = os.path.abspath(args[i + 1])
        del args[i:i + 2]
    if len(args) != 2 or args[0] not in ("json", "search", "bloom"):
        sys.exit(__doc__)
    module, out = args[0], os.path.abspath(args[1])
    m = read_manifest(module)
    tmp = None
    try:
        if src is None:
            tmp = tempfile.mkdtemp()
            src = os.path.join(tmp, module)
            subprocess.run(["git", "-c", "advice.detachedHead=false", "clone", "-q", "--depth", "1", "--branch",
                            m["tag"], m["repo"], src], check=True)
            head = subprocess.run(["git", "-C", src, "rev-parse", "HEAD"], capture_output=True, text=True,
                                  check=True).stdout.strip()
            if head != m["commit"]:
                sys.exit("ERROR: %s 标签 %s 指向 %s，与 MODULES 锁定的 %s 不一致" % (module, m["tag"], head, m["commit"]))
        deps = cargo_deps(src) if module == "bloom" else []
        grpc = None
        if module == "search":
            grpc = find_grpc_checkout(src)
            if grpc:
                print("  使用构建时的 gRPC 检出: " + grpc)
            else:  # 缓存命中，没有构建目录：按固定标签与 commit 克隆
                if tmp is None:
                    tmp = tempfile.mkdtemp()
                print("  没有构建目录，按固定标签克隆 gRPC %s ..." % GRPC_TAG)
                grpc = clone_grpc(os.path.join(tmp, "grpc"))
        res = collect(module, m["version"], m["license"], src, out, deps, m["repo"], m["commit"], grpc_dir=grpc)
        lost = [x for x in res["missing"] if x.startswith("crates/")]
        if lost:  # 发布的二进制里有这些 crate 的代码，必须附其许可文本
            sys.exit("ERROR: 以下 crate 没有许可文本，请在 dataplane/modules/%s/extra-licenses/<名>-<版本>/ 补上（原样取自上游并写 SOURCE.txt）：%s" % (module, ", ".join(lost)))
        if markdown:
            if module != "bloom":
                sys.exit("ERROR: --markdown 只适用于 Rust 模块（bloom）；json 与 search 的 LICENSES.md 为人工核对后手写")
            with open(markdown, "w", encoding="utf-8") as f:
                f.write(markdown_crates(module, m["version"], m["license"], m["repo"].removesuffix(".git"), m["commit"], deps))
        print("✓ %s：许可文本与 SBOM 写入 %s" % (module, out))
    finally:
        if tmp:
            shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    main()

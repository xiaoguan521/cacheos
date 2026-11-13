#!/usr/bin/env python3
"""收集扩展模块的许可文本并生成 SPDX 2.3 SBOM（ADR 0002）。只用标准库，外部命令一律切片调用。

用法：collect-licenses.py <json|search|bloom> <输出目录> [--src <已检出的模块源码目录>]

输出：
  <输出目录>/licenses/<模块>/…           上游 LICENSE/COPYING/NOTICE，以及 third_party/ 下各库、bloom 各 crate 的许可文本
  <输出目录>/sbom-<模块>.spdx.json        模块 + 直接依赖 + 动态链接的系统库；时间戳取 SOURCE_DATE_EPOCH，输出可复现

不带 --src 时按 dataplane/modules/MODULES 浅克隆固定标签，并核对 commit，不一致即失败。
模块根目录没有许可文本视为错误；third_party 下缺许可文本的库记入 SBOM 的 comment 并在标准输出告警。
"""
import datetime, json, os, re, shutil, subprocess, sys, tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
LICENSE_RE = re.compile(r"^(LICEN[CS]E|COPYING|NOTICE|UNLICENSE|COPYRIGHT)", re.I)

# 动态链接的系统库（CLAUDE.md 允许的 LGPL 例外，目前仅 libsystemd，由 CI 的 readelf 检查确认）
DYNAMIC_LIBS = {"search": [("libsystemd", "LGPL-2.1-or-later", "https://github.com/systemd/systemd")]}


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


def collect(module, version, license_, src, out, deps, repo="NOASSERTION", commit=""):
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

    for dep in deps:
        d = dep.get("dir")
        if d and not copy_licenses(d, os.path.join(lic_out, "crates", "%s-%s" % (dep["name"], dep["version"]))):
            missing.append("crates/%s-%s" % (dep["name"], dep["version"]))

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
    return [{"name": p["name"], "version": p["version"], "license": p.get("license"),
             "dir": os.path.dirname(p["manifest_path"])} for p in meta["packages"] if p["id"] not in roots]


def main():
    args = sys.argv[1:]
    src = None
    if "--src" in args:
        i = args.index("--src")
        src = os.path.abspath(args[i + 1])
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
        collect(module, m["version"], m["license"], src, out, deps, m["repo"], m["commit"])
        print("✓ %s：许可文本与 SBOM 写入 %s" % (module, out))
    finally:
        if tmp:
            shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    main()

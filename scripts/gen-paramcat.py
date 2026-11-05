#!/usr/bin/env python3
"""从上游 src/config.c 生成参数目录 internal/paramcat/catalog_gen.go（CacheOS 规格书 4 节、6.3）。

参数的类型、取值范围、默认值、是否需重启（IMMUTABLE_CONFIG）全部取自上游源码，升级上游时重新生成即可；
哪些参数开放给控制台修改（EXPOSED）由本文件人工维护：只开放与业务调优相关的参数，
端口、目录、口令、TLS 文件、复制关系等由控制台编排自动生成，不允许直接改。

用法：scripts/gen-paramcat.py <上游源码目录> > internal/paramcat/catalog_gen.go
"""
import re
import sys

# 开放给控制台修改的参数（分组 → 参数名），顺序即界面展示顺序
EXPOSED = {
    "内存": ["maxmemory", "maxmemory-policy", "maxmemory-samples", "maxmemory-eviction-tenacity", "active-expire-effort",
             "lazyfree-lazy-eviction", "lazyfree-lazy-expire", "lazyfree-lazy-server-del", "lazyfree-lazy-user-del",
             "lazyfree-lazy-user-flush", "activedefrag", "active-defrag-ignore-bytes", "active-defrag-threshold-lower",
             "active-defrag-threshold-upper", "active-defrag-cycle-min", "active-defrag-cycle-max", "jemalloc-bg-thread"],
    "持久化": ["save", "appendonly", "appendfsync", "auto-aof-rewrite-percentage", "auto-aof-rewrite-min-size",
               "aof-use-rdb-preamble", "aof-timestamp-enabled", "rdbcompression", "rdbchecksum",
               "stop-writes-on-bgsave-error", "no-appendfsync-on-rewrite", "aof-load-truncated", "rdb-del-sync-files"],
    "连接与网络": ["maxclients", "timeout", "tcp-keepalive", "tcp-backlog", "io-threads", "client-output-buffer-limit",
                   "client-query-buffer-limit", "proto-max-bulk-len", "maxmemory-clients"],
    "复制": ["repl-backlog-size", "repl-backlog-ttl", "repl-timeout", "repl-diskless-sync", "repl-diskless-sync-delay",
             "repl-diskless-sync-max-replicas", "repl-diskless-load", "replica-serve-stale-data", "replica-lazy-flush",
             "min-replicas-to-write", "min-replicas-max-lag", "replica-priority", "dual-channel-replication-enabled"],
    "集群": ["cluster-node-timeout", "cluster-require-full-coverage", "cluster-allow-reads-when-down",
             "cluster-allow-pubsubshard-when-down", "cluster-replica-validity-factor", "cluster-migration-barrier",
             "cluster-allow-replica-migration"],
    "监控与诊断": ["commandlog-execution-slower-than", "commandlog-slow-execution-max-len", "latency-monitor-threshold", "latency-tracking",
                   "notify-keyspace-events", "busy-reply-threshold"],
    "数据结构编码": ["hash-max-listpack-entries", "hash-max-listpack-value", "list-max-listpack-size",
                     "list-compress-depth", "set-max-intset-entries", "set-max-listpack-entries",
                     "set-max-listpack-value", "zset-max-listpack-entries", "zset-max-listpack-value",
                     "stream-node-max-bytes", "stream-node-max-entries"],
    "其他": ["databases", "hz"],
}

# 多值 / 特殊参数的类型与校验方式（上游为 createSpecialConfig，没有统一的上下界）
SPECIAL = {
    "save": ("save", "3600 1 300 100 60 10000", False),
    "client-output-buffer-limit": ("client-output-buffer-limit", "normal 0 0 0 slave 268435456 67108864 60 pubsub 33554432 8388608 60", False),
    "notify-keyspace-events": ("keyspace-events", "", False),
}

NUM_TYPES = {"Int", "UInt", "Long", "ULong", "LongLong", "ULongLong", "SizeT", "SSizeT", "TimeT", "OffT"}


def split_args(s):
    args, depth, cur, q = [], 0, [], False
    i = 0
    while i < len(s):
        c = s[i]
        if q:
            cur.append(c)
            if c == "\\":
                cur.append(s[i + 1]); i += 1
            elif c == '"':
                q = False
        elif c == '"':
            q = True; cur.append(c)
        elif c in "({":
            depth += 1; cur.append(c)
        elif c in ")}":
            depth -= 1; cur.append(c)
        elif c == "," and depth == 0:
            args.append("".join(cur).strip()); cur = []
        else:
            cur.append(c)
        i += 1
    if cur:
        args.append("".join(cur).strip())
    return args


def configs(src):
    start = src.index("standardConfig static_configs[]")
    body = src[start:]
    for m in re.finditer(r"create(\w+)Config\(", body):
        kind = m.group(1)
        i = m.end(); depth = 1; q = False
        j = i
        while depth:
            c = body[j]
            if q:
                if c == "\\": j += 1
                elif c == '"': q = False
            elif c == '"': q = True
            elif c == "(": depth += 1
            elif c == ")": depth -= 1
            j += 1
        yield kind, split_args(body[i:j - 1])


def enums(src):
    out = {}
    for m in re.finditer(r"configEnum (\w+)\[\] = \{(.*?)\{NULL, 0\}\};", src, re.S):
        out[m.group(1)] = re.findall(r'\{"([^"]+)",\s*(\w+)\}', m.group(2))
    return out


DEFINES = {}


def cint(v):
    v = v.strip()
    for _ in range(5):  # 展开 CONFIG_DEFAULT_* 等宏
        if v in DEFINES:
            v = DEFINES[v]
        else:
            break
    consts = {"INT_MAX": 2**31 - 1, "INT_MIN": -2**31, "LONG_MAX": 2**63 - 1, "LONG_MIN": -2**63,
              "LLONG_MAX": 2**63 - 1, "LLONG_MIN": -2**63, "ULONG_MAX": 2**64 - 1, "ULLONG_MAX": 2**64 - 1,
              "UINT_MAX": 2**32 - 1, "SIZE_MAX": 2**64 - 1, "LONG_LONG_MAX": 2**63 - 1}
    if v in consts:
        return consts[v]
    v = re.sub(r"\b(\d+)(ULL|LL|UL|L|U)\b", r"\1", v, flags=re.I)
    v = v.replace("(long long)", "").replace("(unsigned long long)", "")
    try:
        return int(eval(v, {"__builtins__": {}}, {}))
    except Exception:
        return None


def go_str(s):
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'


def main():
    root = sys.argv[1]
    src = open(f"{root}/src/config.c", encoding="utf-8").read()
    for h in ("server.h", "config.h", "cluster.h"):
        try:
            for m in re.finditer(r"^#define (\w+) +\(?([-\w*() ]+?)\)?\s*(?:/\*.*)?$", open(f"{root}/src/{h}", encoding="utf-8").read(), re.M):
                DEFINES.setdefault(m.group(1), m.group(2).strip())
        except FileNotFoundError:
            pass
    ens = enums(src)
    found = {}
    for kind, a in configs(src):
        name = a[0].strip('"')
        flags = a[2] if len(a) > 2 else ""
        alias = a[1].strip().strip('"') if a[1].strip() != "NULL" else ""
        p = {"name": name, "alias": alias, "restart": "IMMUTABLE_CONFIG" in flags, "kind": kind, "min": None, "max": None,
             "default": "", "enum": [], "memory": False, "percent": False}
        if kind == "Bool":
            p["type"] = "bool"; p["default"] = "yes" if a[4].strip() == "1" else "no"
        elif kind in NUM_TYPES:
            p["type"] = "int"; p["min"] = cint(a[3]); p["max"] = cint(a[4]); dv = cint(a[6])
            p["default"] = "" if dv is None else str(dv)
            p["memory"] = "MEMORY_CONFIG" in a[7]; p["percent"] = "PERCENT_CONFIG" in a[7]
        elif kind == "Enum":
            pairs = ens.get(a[3].strip(), [])
            p["type"] = "enum"; p["enum"] = [n for n, _ in pairs]
            dv = a[5].strip()
            p["default"] = next((n for n, c in pairs if c == dv), "")
        elif kind in ("String", "SDS"):
            p["type"] = "string"; p["default"] = a[5].strip().strip('"') if a[5].strip() != "NULL" else ""
        elif kind == "Special":
            p["type"] = "special"
        else:
            continue
        found[name] = p

    out = ["// Code generated by scripts/gen-paramcat.py from upstream src/config.c. DO NOT EDIT.", "",
           "package paramcat", "", "var catalog = []Param{"]
    for group, names in EXPOSED.items():
        for n in names:
            if n not in found:
                sys.exit(f"gen-paramcat: {n} not found in upstream config.c")
            p = found[n]
            typ = p["type"]
            fmt = ""
            dflt = p["default"]
            if typ == "special":
                if n not in SPECIAL:
                    sys.exit(f"gen-paramcat: special parameter {n} needs a SPECIAL entry")
                fmt, dflt, _ = SPECIAL[n]
            if typ == "string" and n in SPECIAL:
                fmt, dflt, _ = SPECIAL[n]
                typ = "special"
            fields = [f"Name: {go_str(n)}", f"Group: {go_str(group)}", f"Type: {go_str(typ)}",
                      f"Restart: {'true' if p['restart'] else 'false'}", f"Default: {go_str(dflt)}"]
            if p["min"] is not None:
                fields.append(f"Min: {p['min']}")
            if p["max"] is not None and p["max"] <= 2**63 - 1:
                fields.append(f"Max: {p['max']}")
            if p["memory"]:
                fields.append("Memory: true")
            if p["percent"]:
                fields.append("Percent: true")
            if p["enum"]:
                fields.append("Enum: []string{" + ", ".join(go_str(e) for e in p["enum"]) + "}")
            if fmt:
                fields.append(f"Format: {go_str(fmt)}")
            if p["alias"]:
                fields.append(f"Alias: {go_str(p['alias'])}")
            out.append("\t{" + ", ".join(fields) + "},")
    out.append("}")
    print("\n".join(out))


if __name__ == "__main__":
    main()

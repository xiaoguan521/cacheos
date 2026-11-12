#!/usr/bin/env python3
"""把 Valkey 源码树改名为 sycache（规格书 5.1）。

只修改 C 字符串字面量与 version.h / asciilogo.h 中的宏值；另为标准 Redis 客户端补充
INFO 的 redis_mode 兼容别名。不改数据结构、函数签名、命令语义、RDB/AOF 格式或模块 ABI。
脚本是生成 dataplane/patches/0001-sycache-rename.patch 的唯一来源：scripts/gen-patch.sh
在干净的上游源码上运行本脚本，再用 diff 得到 patch。

用法：rename.py <valkey 源码目录>

规则分三类（详见 docs/adr/0001-rename-boundaries.md）：
  1. KEEP      —— 协议、磁盘格式、模块 API 相关的字面量，原样保留。
  2. SPECIAL   —— 个别需要精确替换的字面量（INFO 字段、外部链接等）。
  3. GENERIC   —— 其余字面量中的 Valkey / VALKEY / valkey 依次换成 SyCache / SYCACHE / sycache。
"""
import pathlib
import re
import sys

# ---------- 范围 ----------
# 只处理会编译进产品二进制的源码；示例模块与单元测试不进安装包。
# commands.def 由 src/commands/*.json 生成，其中的命令说明会通过 COMMAND DOCS 返回给用户
INCLUDE_GLOBS = ["src/*.c", "src/*.h", "src/commands.def", "src/modules/lua/*.c", "src/modules/lua/*.h"]
SKIP_FILES = {
    # 编译期弃用提示中的函数名（valkey_strlcpy 等），不进二进制
    "src/fmacros.h",
    # 模块 API 头文件：第三方模块按此编译，必须与上游一致
    "src/valkeymodule.h",
    "src/redismodule.h",
}

# ---------- KEEP：协议与格式 ----------
# (文件, 字面量内容)；文件为 None 表示所有文件。
KEEP_EXACT = {
    # RDB / AOF 文件头魔数：改了会导致与上游、与迁移工具互不认识数据文件
    ("src/rdb.c", "VALKEY"),
    ("src/aof.c", "VALKEY"),
    ("src/valkey-check-rdb.c", "VALKEY"),
    ("src/valkey-check-aof.c", "VALKEY"),
    # RDB 辅助字段名：磁盘格式的一部分
    ("src/rdb.c", "valkey-ver"),
    # Lua 全局变量名：用户脚本可能引用
    ("src/modules/lua/script_lua.c", "VALKEY_VERSION"),
    ("src/modules/lua/script_lua.c", "VALKEY_VERSION_NUM"),
    # Lua 调试器（ldb）的命令名：用户在调试会话中输入
    ("src/modules/lua/debug_lua.c", "valkey"),
    # cli / benchmark 的 -u 参数接受的 URI 协议名：改名会让已有的 valkey:// 连接串失效（上游测试集
    # "Valid Connection Scheme" 与 benchmark URI 用例即因此失败）。redis:// 与 rediss:// 本来就支持，
    # 帮助文本统一改为展示 redis://（见 SPECIAL）。
    ("src/cli_common.c", "valkey://"),
    ("src/cli_common.c", "valkeys://"),
}
# 含这些子串的字面量一律保留：模块 API 符号名（dlsym 按名字查找）
KEEP_SUBSTR = ["ValkeyModule_", "VALKEYMODULE"]

# ---------- SPECIAL：精确替换 ----------
# INFO server 中两行版本字段（server.c）：
#   valkey_version        -> sycache_engine_version（值仍为上游版本：Lua 的 VALKEY_VERSION_NUM、
#                            valkey-cli 的特性判断都依赖它）
#   valkey_release_stage  -> sycache_version（值换成产品版本，见 version.h 的 SYCACHE_VERSION）
# 读取该字段的两处（Lua 引擎、cli）同步改名，保证语义不变。
SPECIAL = [
    ("src/server.c", "valkey_version:%s\\r\\n", "sycache_engine_version:%s\\r\\n"),
    ("src/server.c", "valkey_release_stage:%s\\r\\n", "sycache_version:%s\\r\\n"),
    ("src/modules/lua/engine_lua.c", "valkey_version", "sycache_engine_version"),
    ("src/valkey-cli.c", "\\nvalkey_version:", "\\nsycache_engine_version:"),
    # 外部链接：不指向上游社区
    (None, "https://github.com/valkey-io/valkey/issues", "your CacheOS support channel"),
    (None, "https://valkey.io/commands/slowlog", "the SLOWLOG command documentation"),
    # -u 参数帮助文本：展示通用的 redis:// / rediss:// 写法（sycache:// 不是可解析的协议名）
    (None, "on format valkey://user:password@", "on format redis://user:password@"),
    (None, "the scheme 'valkeys'", "the scheme 'rediss'"),
    (None, "-u valkey://default:", "-u redis://default:"),
    # 依赖库名：不是产品名，换成中性描述
    (None, "support from libvalkey", "support in the client library"),
]

# 宏参数替换：只把「给人看的版本号」换成产品版本 SYCACHE_VERSION。
# 复制协议（REPLCONF version）、HELLO、RDB、Lua、LOLWUT 仍用上游 VALKEY_VERSION，保证协议兼容。
MACRO_ARGS = [
    # --version / getVersion()：sycache-server、sycache-check-rdb、sycache-check-aof
    ("src/server.c", 'build=%llx", VALKEY_VERSION,', 'build=%llx", SYCACHE_VERSION,'),
    # 启动横幅与启动日志
    ("src/server.c", "snprintf(buf, 1024 * 16, ascii_logo, VALKEY_VERSION,",
     "snprintf(buf, 1024 * 16, ascii_logo, SYCACHE_VERSION,"),
    ("src/server.c", 'just started", VALKEY_VERSION,', 'just started", SYCACHE_VERSION,'),
    # INFO server：sycache_version 行（原 valkey_release_stage 行）
    ("src/server.c", '"sycache_version:%s\\r\\n", VALKEY_RELEASE_STAGE,', '"sycache_version:%s\\r\\n", SYCACHE_VERSION,'),
    # cli / benchmark 的 --version
    ("src/cli_common.c", '"%s", VALKEY_VERSION);', '"%s", SYCACHE_VERSION);'),
    # 配置错误与崩溃日志
    ("src/config.c", "FATAL CONFIG FILE ERROR (Version %s) ***\\n\", VALKEY_VERSION",
     "FATAL CONFIG FILE ERROR (Version %s) ***\\n\", SYCACHE_VERSION"),
    ("src/sentinel.c", "FATAL CONFIG FILE ERROR (Version %s) ***\\n\", VALKEY_VERSION",
     "FATAL CONFIG FILE ERROR (Version %s) ***\\n\", SYCACHE_VERSION"),
    ("src/debug.c", 'si_code: %d", VALKEY_VERSION,', 'si_code: %d", SYCACHE_VERSION,'),
]

# Valkey emits `server_mode` in branded mode. Keep that product-facing field
# and expose `redis_mode` for clients and monitoring in every mode. Extended
# Redis compatibility already emits `redis_mode`, so don't duplicate it.
SNIPPET_REPLACEMENTS = [
    (
        "src/server.c",
        '                "availability_zone:%s\\r\\n", server.availability_zone));\n\n'
        '        /* Conditional properties */',
        '                "availability_zone:%s\\r\\n", server.availability_zone));\n'
        '        /* Redis-compatible mode alias in branded mode. */\n'
        '        if (!server.extended_redis_compat) {\n'
        '            info = sdscatfmt(info, "redis_mode:%s\\r\\n", mode);\n'
        '        }\n'
        '        /* Conditional properties */',
    ),
]

VERSION_H_APPEND = '''
/* CacheOS 产品版本（规格书 12 节）：只用于 --version、启动日志与 INFO 的 sycache_version。
 * 由构建脚本通过 SERVER_CFLAGS=-DSYCACHE_VERSION=... 传入，取自 dataplane/versions.yaml。 */
#ifndef SYCACHE_VERSION
#define SYCACHE_VERSION "0.0.0-dev"
#endif
'''

LOGO = r'''const char *ascii_logo =
    "                                                                     \n"
    "   ____        ____           _                                      \n"
    "  / ___| _   _/ ___|__ _  ___| |__   ___     SyCache %s (%s/%d) %s bit\n"
    "  \\___ \\| | | | |   / _` |/ __| '_ \\ / _ \\                         \n"
    "   ___) | |_| | |__| (_| | (__| | | |  __/   Running in %s mode\n"
    "  |____/ \\__, |\\____\\__,_|\\___|_| |_|\\___|   Port: %d\n"
    "         |___/                               PID: %ld               \n"
    "                                                                     \n\n";
'''

GENERIC = [("Valkey", "SyCache"), ("VALKEY", "SYCACHE"), ("valkey", "sycache")]


def string_literals(text):
    """返回 C 源码中所有字符串字面量内容的 (start, end) 区间（不含引号）。

    跳过注释、字符字面量与预处理 #include 行。
    """
    spans = []
    i, n = 0, len(text)
    line_start = True
    while i < n:
        c = text[i]
        if line_start:
            m = re.match(r"[ \t]*#[ \t]*include\b[^\n]*", text[i:])
            if m:
                i += m.end()
                continue
        line_start = False
        if c == "\n":
            line_start = True
            i += 1
        elif text.startswith("//", i):
            j = text.find("\n", i)
            i = n if j < 0 else j
        elif text.startswith("/*", i):
            j = text.find("*/", i + 2)
            i = n if j < 0 else j + 2
        elif c == "'":
            j = i + 1
            while j < n and text[j] != "'":
                j += 2 if text[j] == "\\" else 1
            i = j + 1
        elif c == '"':
            j = i + 1
            while j < n and text[j] != '"':
                j += 2 if text[j] == "\\" else 1
            spans.append((i + 1, j))
            i = j + 1
        else:
            i += 1
    return spans


def keep(rel, lit):
    if (rel, lit) in KEEP_EXACT or (None, lit) in KEEP_EXACT:
        return True
    return any(s in lit for s in KEEP_SUBSTR)


def rewrite_literal(rel, lit):
    if keep(rel, lit):
        return lit
    for f, old, new in SPECIAL:
        if f in (None, rel) and old in lit:
            lit = lit.replace(old, new)
    for old, new in GENERIC:
        lit = lit.replace(old, new)
    return lit


def rewrite_file(rel, text):
    out, last = [], 0
    for s, e in string_literals(text):
        out.append(text[last:s])
        out.append(rewrite_literal(rel, text[s:e]))
        last = e
    out.append(text[last:])
    return "".join(out)


def must_replace(text, old, new, rel):
    if text.count(old) != 1:
        sys.exit(f"rename: expected exactly one '{old}' in {rel}, found {text.count(old)}")
    return text.replace(old, new)


def main():
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    root = pathlib.Path(sys.argv[1]).resolve()
    files = sorted({p for g in INCLUDE_GLOBS for p in root.glob(g)})
    changed = 0
    for p in files:
        rel = p.relative_to(root).as_posix()
        if rel in SKIP_FILES:
            continue
        text = p.read_text(encoding="utf-8")
        new = rewrite_file(rel, text)
        for f, old, rep in MACRO_ARGS:
            if f == rel:
                new = must_replace(new, old, rep, rel)
        for f, old, rep in SNIPPET_REPLACEMENTS:
            if f == rel:
                new = must_replace(new, old, rep, rel)
        if rel == "src/version.h":
            # SERVER_NAME / SERVER_TITLE 已由通用规则改为 "sycache" / "SyCache"
            for want in ('#define SERVER_NAME "sycache"', '#define SERVER_TITLE "SyCache"'):
                if want not in new:
                    sys.exit(f"rename: {rel} lacks {want}")
            new += VERSION_H_APPEND
        if rel == "src/asciilogo.h":
            start = new.index("const char *ascii_logo =")
            end = new.index(";", new.index('\\n\\n"', start)) + 2
            new = new[:start] + LOGO + new[end:]
        if new != text:
            p.write_text(new, encoding="utf-8")
            changed += 1
    print(f"rename: {changed} files changed")


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""模块命令 × 内置账号 ACL 矩阵（ADR 0002「ACL 与安全」评估）。

用法：python3 -I test/modules/acl-matrix.py <sycache-server> <module.so> <输出.md>

做法：用临时实例加载模块，枚举模块注册的全部命令（COMMAND LIST FILTERBY MODULE），
读取每条命令的 flags / ACL 类别，再用 ACL DRYRUN 判断内置账号能否执行。
账号规则从 internal/agent/instance/render.go 中直接提取（WebCLIRules / AgentRules / ReaderRules），
避免与代码漂移；提取不到就失败。

结论判定（退出码）：
  - Web 命令行账号 sycache-webcli 可执行、但命令带 admin / dangerous / scripting / blocking / pubsub 标志，
    或命令名含 debug / _ 前缀的内部命令 → 记为“需处理”，退出码 1。
  - agent 管理账号 sycache-agent 可执行任何模块命令 → 记为“需处理”（该账号不得读取 value），退出码 1。
"""
import os
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import time

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
RENDER = os.path.join(ROOT, "internal", "agent", "instance", "render.go")


def extract_rules(var):
    src = open(RENDER, encoding="utf-8").read()
    m = re.search(r"var %s = \[\]string\{(.*?)\}\n" % var, src, re.S)
    if not m:
        sys.exit("无法从 render.go 提取 %s" % var)
    rules = re.findall(r'"([^"]+)"', m.group(1))
    if not rules:
        sys.exit("%s 为空" % var)
    return rules


class Conn:
    def __init__(self, port, user=None, pw=None):
        self.s = socket.create_connection(("127.0.0.1", port), timeout=10)
        self.f = self.s.makefile("rb")

    def cmd(self, *args):
        out = b"*%d\r\n" % len(args)
        for a in args:
            b = a if isinstance(a, bytes) else str(a).encode()
            out += b"$%d\r\n" % len(b) + b + b"\r\n"
        self.s.sendall(out)
        return self._read()

    def _read(self):
        line = self.f.readline().rstrip(b"\r\n")
        t, rest = line[:1], line[1:]
        if t == b"+":
            return rest.decode()
        if t == b":":
            return int(rest)
        if t == b"-":
            return Exception(rest.decode())
        if t == b"$":
            n = int(rest)
            return None if n < 0 else self.f.read(n + 2)[:n].decode("latin1")
        if t in (b"*", b"~", b">"):
            n = int(rest)
            return None if n < 0 else [self._read() for _ in range(n)]
        if t == b"%":
            n = int(rest)
            d = {}
            for _ in range(n):
                k = self._read()
                d[k] = self._read()
            return d
        if t == b"_":
            return None
        return line.decode("latin1")


def wait_ready(port):
    for _ in range(80):
        try:
            if Conn(port).cmd("PING") == "PONG":
                return True
        except OSError:
            pass
        time.sleep(0.5)
    return False


def info_fields(item):
    """COMMAND INFO 的单条结果 → (name, arity, flags, acl_categories)。"""
    if isinstance(item, dict):
        return item.get("name"), item.get("arity"), item.get("flags", []), item.get("acl_categories", [])
    return item[0], item[1], item[2], item[6] if len(item) > 6 else []


def main():
    server, so, out_path = sys.argv[1:4]
    web, agent, reader = (extract_rules(v) for v in ("WebCLIRules", "AgentRules", "ReaderRules"))
    tmp = tempfile.mkdtemp()
    port = 7400
    acl = (
        "user default on nopass ~* &* +@all\n"
        "user webcli on nopass %s\nuser agent on nopass %s\nuser reader on nopass %s\n"
        % (" ".join(web), " ".join(agent), " ".join(reader))
    )
    open(os.path.join(tmp, "users.acl"), "w").write(acl)
    log = open(os.path.join(tmp, "server.log"), "wb")
    proc = subprocess.Popen(
        [server, "--port", str(port), "--dir", tmp, "--aclfile", os.path.join(tmp, "users.acl"),
         "--loadmodule", so, "--save", "", "--daemonize", "no"],
        stdout=log, stderr=subprocess.STDOUT)
    try:
        if not wait_ready(port):
            print(open(os.path.join(tmp, "server.log"), errors="replace").read()[-3000:])
            sys.exit("实例启动或模块加载失败")
        c = Conn(port)
        mods = c.cmd("MODULE", "LIST")
        names = []
        for m in mods:
            d = m if isinstance(m, dict) else dict(zip(m[0::2], m[1::2]))
            names.append(d.get("name"))
        print("已加载模块:", names)
        cmds = []
        for mod in names:
            if mod == "lua":  # 内置静态模块，不是被测对象
                continue
            got = c.cmd("COMMAND", "LIST", "FILTERBY", "MODULE", mod)
            if isinstance(got, Exception) or not got:
                sys.exit("COMMAND LIST FILTERBY MODULE %s 失败: %s" % (mod, got))
            cmds += got
        if not cmds:
            sys.exit("没有枚举到任何模块命令")
        rows, problems = [], []
        for name in sorted(cmds):
            info = c.cmd("COMMAND", "INFO", name)
            if isinstance(info, Exception) or not info or info[0] is None:
                rows.append((name, "?", "?", "?", "?", "?"))
                continue
            _, arity, flags, cats = info_fields(info[0])
            n = max(abs(int(arity)) - 1, 0)
            dummy = ["k%d" % i for i in range(n)]
            res = {}
            for user in ("webcli", "agent", "reader"):
                r = c.cmd("ACL", "DRYRUN", user, name, *dummy)
                res[user] = "允许" if r == "OK" else "拒绝"
            flags_s = ",".join(str(x) for x in flags)
            cats_s = ",".join(str(x) for x in cats)
            risky = (set(x.lstrip("@") for x in map(str, cats)) | set(map(str, flags))) & {
                "admin", "dangerous", "scripting", "blocking", "pubsub", "noscript"}
            internal = "debug" in name.lower() or re.search(r"\.[_]", name) is not None
            if res["webcli"] == "允许" and (risky or internal):
                problems.append("webcli 可执行风险命令 %s（标志/类别: %s %s）" % (name, flags_s, cats_s))
            if res["agent"] == "允许":
                problems.append("agent 管理账号可执行模块命令 %s" % name)
            rows.append((name, flags_s, cats_s, res["webcli"], res["agent"], res["reader"]))
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
        shutil.rmtree(tmp, ignore_errors=True)

    lines = ["# 模块命令 ACL 矩阵: %s" % os.path.basename(so), "",
             "| 命令 | flags | ACL 类别 | sycache-webcli | sycache-agent | sycache-reader |", "|---|---|---|---|---|---|"]
    lines += ["| " + " | ".join(r) + " |" for r in rows]
    lines += ["", "命令总数: %d" % len(rows), ""]
    if problems:
        lines += ["## 需处理", ""] + ["- " + p for p in problems]
    text = "\n".join(lines) + "\n"
    open(out_path, "w", encoding="utf-8").write(text)
    print(text)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())

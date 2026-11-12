#!/usr/bin/env python3
"""Focused tests for topology comparison safety and cleanup behavior."""
from collections import Counter
from contextlib import redirect_stdout
import io
import os
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import cmdcheck
import topologycheck as check


class GuardTests(unittest.TestCase):
    def test_global_mutations_are_blocked(self):
        forbidden = [
            ("FLUSHALL",), ("FLUSHDB",), ("SCRIPT", "FLUSH"), ("CONFIG", "SET", "x", "y"),
            ("FUNCTION", "DELETE", "x"), ("ACL", "SETUSER", "x"),
            ("SENTINEL", "FAILOVER", "x"), ("SENTINEL", "CONFIG", "SET", "x", "y"),
            ("SENTINEL", "IS-MASTER-DOWN-BY-ADDR", "127.0.0.1", 6379, 1, "voter-id"),
            ("CLUSTER", "SETSLOT", 1, "NODE", "id"), ("CLUSTER", "FAILOVER"),
        ]
        for command in forbidden:
            with self.subTest(command=command), self.assertRaises(check.BlockedCommand):
                check.guard_command(command)

    def test_safe_reads_and_isolated_writes_are_allowed(self):
        allowed = [
            ("SET", "{test}:key", "v"), ("SCRIPT", "LOAD", "return 1"), ("CONFIG", "GET", "x"),
            ("SENTINEL", "CONFIG", "GET", "resolve-hostnames"),
            ("SENTINEL", "IS-MASTER-DOWN-BY-ADDR", "127.0.0.1", 6379, 0, "*"),
            ("CLUSTER", "INFO"), ("CLUSTER", "GETKEYSINSLOT", 1, 10),
        ]
        for command in allowed:
            with self.subTest(command=command):
                check.guard_command(command)

    def test_reused_data_cases_exclude_flush_and_split_hash_extensions(self):
        cases = check.data_cases()
        names = {case.name for case in cases}
        self.assertNotIn("EVAL / EVALSHA / SCRIPT LOAD / EXISTS / FLUSH", names)
        self.assertNotIn("FUNCTION LOAD / FCALL / FCALL_RO / LIST / DELETE", names)
        self.assertTrue({"HGETEX", "HSETEX", "HGETDEL"} <= names)
        self.assertIn("EVAL / EVALSHA / SCRIPT LOAD / EXISTS（不清空缓存）", names)

    def test_addresses_and_named_credentials(self):
        self.assertEqual(check.endpoint("[::1]:6379"), ("::1", 6379))
        with self.assertRaises(ValueError):
            check.endpoint("localhost:70000")
        args = SimpleNamespace(tls=False, timeout=2)
        with patch.dict(os.environ, {"CMDCHECK_PASSWORD": "data", "CMDCHECK_SENTINEL_PASSWORD_REDIS": "sentinel"},
                        clear=True):
            self.assertEqual(check.auth_for("redis", args).password, "data")
            self.assertEqual(check.auth_for("redis", args, sentinel=True).password, "sentinel")


class CleanupTests(unittest.TestCase):
    def test_cleanup_follows_ask_and_sends_asking(self):
        events = []

        class FakeConn:
            def __init__(self, host, port, auth, commands):
                self.port = port
                self.asked = False

            def call(self, *args):
                events.append((self.port, args))
                if self.port == 7100:
                    raise cmdcheck.RespError("ASK 123 127.0.0.1:7101")
                if args == ("ASKING",):
                    self.asked = True
                    return "OK"
                if not self.asked:
                    raise AssertionError("ASKING must be sent on the same connection")
                return 1

            def close(self):
                pass

        with patch.object(check, "GuardedConn", FakeConn):
            result = check.redirect_call(("127.0.0.1", 7100), None, Counter(), "DEL", "key")
        self.assertEqual(result, 1)
        self.assertEqual(events, [(7100, ("DEL", "key")), (7101, ("ASKING",)), (7101, ("DEL", "key"))])

    def test_cleanup_falls_back_to_del_when_unlink_is_denied(self):
        with patch.object(check, "redirect_call", side_effect=[cmdcheck.RespError("NOPERM denied"), 1]) as call:
            self.assertEqual(check.cleanup_key(("127.0.0.1", 6379), None, Counter(), "key"), 1)
        self.assertEqual(call.call_args_list[0].args[-2:], ("UNLINK", "key"))
        self.assertEqual(call.call_args_list[1].args[-2:], ("DEL", "key"))

    def test_failed_case_still_cleans_keys_and_closes_its_target(self):
        events = []

        class FakeTarget:
            def __init__(self, name, address, auth, commands, spec):
                self.name, (self.host, self.port), self.auth = name, address, auth
                self.main = object()
                self.extra = []

            def close(self):
                events.append("closed")

        def failing_case(t):
            t.k("temporary")
            raise AssertionError("case failed")

        spec = SimpleNamespace(name="demo", endpoint_for=lambda scope: (("127.0.0.1", 6379), "auth"))
        case = check.Case("test", "failure", failing_case, "data")
        with patch.object(check, "GuardedTarget", FakeTarget), patch.object(check, "cleanup_key") as cleanup:
            result = check.run_case(case, spec, "abc", Counter())
        self.assertEqual(result["status"], "FAIL")
        self.assertIn("case failed", result["detail"])
        self.assertEqual(cleanup.call_args.args[-1], "{cmdcheck-abc}:temporary")
        self.assertEqual(events, ["closed"])


class ReportTests(unittest.TestCase):
    def test_different_normalized_values_are_reported(self):
        case = check.Case("sentinel", "replicas", None, "sentinel")
        specs = [SimpleNamespace(name="a", commands=Counter(), describe=lambda: "a"),
                 SimpleNamespace(name="b", commands=Counter(), describe=lambda: "b")]
        values = [dict(status="PASS", detail="", seconds=0, value=1),
                  dict(status="PASS", detail="", seconds=0, value=2)]
        args = SimpleNamespace(verbose=False, json=None)
        with patch.object(check, "run_case", side_effect=values), redirect_stdout(io.StringIO()) as out:
            status = check.run([case], specs, "abc", args)
        self.assertEqual(status, 1)
        self.assertIn("结果不一致: 1 项", out.getvalue())


if __name__ == "__main__":
    unittest.main()

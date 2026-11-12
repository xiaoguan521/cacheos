#!/usr/bin/env python3
"""Safety tests for the opt-in cluster transition checks."""
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import cluster_transition_check as transition
import cmdcheck


class TransitionSafetyTests(unittest.TestCase):
    def setUp(self):
        self.source = {"id": "source", "address": ("127.0.0.1", 7100)}
        self.destination = {"id": "destination", "address": ("127.0.0.1", 7101)}
        self.other = {"id": "other", "address": ("127.0.0.1", 7102)}
        self.primaries = [self.source, self.destination, self.other]
        self.auth = SimpleNamespace(user=None, password="example", tls=None, timeout=2)

    def test_non_test_key_stops_recovery_before_any_mutation(self):
        with patch.object(transition, "local_slot_keys", side_effect=[[], ["customer-key"]]), \
             patch.object(transition, "call") as call:
            with self.assertRaisesRegex(transition.CheckFailure, "非测试键"):
                transition.recover_slot(self.auth, 123, "{test}:key", self.source, self.destination, self.primaries)
        call.assert_not_called()

    def test_more_than_one_key_stops_before_listing_or_migrating(self):
        with patch.object(transition, "call", return_value=2) as call:
            with self.assertRaisesRegex(transition.CheckFailure, "拒绝影响非测试数据"):
                transition.local_slot_keys(self.source["address"], self.auth, 123)
        self.assertEqual(call.call_count, 1)
        self.assertEqual(call.call_args.args[-2:], ("COUNTKEYSINSLOT", 123))

    def test_move_transfers_only_the_named_canary(self):
        commands = []
        phase = {"assigned": False}
        key, slot = "{test}:key", 123

        def fake_call(address, auth, *args):
            commands.append((address, args))
            if args[0] == "MIGRATE":
                return "OK"
            if args[:2] == ("CLUSTER", "SETSLOT"):
                if args[3] == "NODE" and address == self.source["address"]:
                    phase["assigned"] = True
                return "OK"
            if args[0] == "GET" and address == self.source["address"]:
                prefix = "MOVED" if phase["assigned"] else "ASK"
                raise cmdcheck.RespError("%s %s 127.0.0.1:7101" % (prefix, slot))
            if args[0] == "GET" and address == self.destination["address"]:
                return "value"
            raise AssertionError((address, args))

        class FakeConn:
            def __init__(self, *args):
                self.asked = False

            def call(self, *args):
                if args == ("ASKING",):
                    self.asked = True
                    return "OK"
                if args == ("GET", key) and self.asked:
                    return "value"
                raise AssertionError(args)

            def close(self):
                pass

        with patch.object(transition, "call", side_effect=fake_call), \
             patch.object(transition, "check_only_canary"), \
             patch.object(transition.cmdcheck, "Conn", FakeConn), \
             patch.object(transition, "owner_at", return_value="destination"):
            transition.move_one_slot(self.auth, slot, key, "value", self.source, self.destination, self.primaries)
        migrate = [args for _, args in commands if args[0] == "MIGRATE"]
        self.assertEqual(len(migrate), 1)
        self.assertEqual(migrate[0][-2:], ("KEYS", key))
        self.assertNotIn("COPY", migrate[0])
        self.assertNotIn("REPLACE", migrate[0])

    def test_stopped_service_is_restarted_when_stop_fails(self):
        row = [0, 5460, ["127.0.0.1", 7100, "source"], ["127.0.0.1", 7104, "replica"]]
        primary = {"id": "source", "address": ("127.0.0.1", 7100)}
        witness = {"id": "other", "address": ("127.0.0.1", 7102)}
        actions = []

        def fake_compose(action, service):
            actions.append((action, service))
            if action == "stop":
                raise RuntimeError("stop timed out")

        def fake_set(primary_address, auth, key, value, state):
            state["created"] = True

        with patch.object(transition, "topology", return_value=([row], [primary, witness])), \
             patch.object(transition, "replica_link", return_value=True), \
             patch.object(transition, "key_for_range", return_value=("{test}:key", 123)), \
             patch.object(transition, "call", return_value=123), \
             patch.object(transition, "set_canary", side_effect=fake_set), \
             patch.object(transition, "wait_for"), \
             patch.object(transition, "demo_compose", side_effect=fake_compose), \
             patch.object(transition, "cleanup_canary") as cleanup:
            with self.assertRaisesRegex(RuntimeError, "stop timed out"):
                transition.failover("redis", ("127.0.0.1", 7100), self.auth, {7100: "redis-node-1"})
        self.assertEqual(actions, [("stop", "redis-node-1"), ("start", "redis-node-1")])
        cleanup.assert_called_once()

    def test_topology_waits_for_replica_information_to_converge(self):
        with patch.object(transition, "topology", side_effect=[
                transition.CheckFailure("每个主节点至少需要一个副本"), (["slots"], ["primary"]),
        ]) as topology, patch.object(transition.time, "sleep"):
            transition.wait_for_topology(("127.0.0.1", 7100), self.auth, {7100: "redis-node-1"})
        self.assertEqual(topology.call_count, 2)


if __name__ == "__main__":
    unittest.main()

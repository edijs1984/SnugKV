#!/usr/bin/env python3

import argparse
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("snug-cluster-bootstrap.py")
SPEC = importlib.util.spec_from_file_location("snug_cluster_bootstrap", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
bootstrap = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(bootstrap)


class BootstrapGenerationTests(unittest.TestCase):
    def test_parse_nodes_requires_exactly_three_unique_nodes(self):
        self.assertEqual(
            bootstrap.parse_nodes("127.0.0.1:7000,127.0.0.1:7001,127.0.0.1:7002"),
            ["127.0.0.1:7000", "127.0.0.1:7001", "127.0.0.1:7002"],
        )
        with self.assertRaises(ValueError):
            bootstrap.parse_nodes("127.0.0.1:7000,127.0.0.1:7001")
        with self.assertRaises(ValueError):
            bootstrap.parse_nodes("127.0.0.1:7000,127.0.0.1:7000,127.0.0.1:7002")

    def test_slot_ranges_cover_every_slot_once(self):
        nodes = ["a:1", "b:2", "c:3"]
        ranges = bootstrap.deterministic_slot_ranges(nodes)
        covered = []
        for range_text, owner in ranges.items():
            start_text, end_text = range_text.split("-", 1)
            start, end = int(start_text), int(end_text)
            self.assertIn(owner, nodes)
            covered.extend(range(start, end + 1))
        self.assertEqual(covered, list(range(bootstrap.SLOT_COUNT)))

    def test_generate_sharded_writes_same_slot_map_to_all_nodes(self):
        with tempfile.TemporaryDirectory() as tmp:
            args = argparse.Namespace(
                nodes="127.0.0.1:7000,127.0.0.1:7001,127.0.0.1:7002",
                output=tmp,
                password="pw",
                control_auth="control",
                mode="sharded",
                group_id="ignored",
                primary_index=0,
                failover_timeout_ms=1000,
            )
            self.assertEqual(bootstrap.generate(args), 0)
            manifest = json.loads(Path(tmp, "manifest.json").read_text())
            self.assertEqual(manifest["mode"], "sharded")
            self.assertIsNone(manifest["primary"])
            self.assertEqual(len(manifest["slot_map"]), 3)
            for index in range(3):
                cfg = json.loads(Path(tmp, f"node-{index}.json").read_text())
                self.assertEqual(cfg["cluster_slots"], manifest["slot_map"])
                self.assertNotIn("failover_peers", cfg)

    def test_generate_ha_derives_peers_quorum_and_primary(self):
        with tempfile.TemporaryDirectory() as tmp:
            args = argparse.Namespace(
                nodes="127.0.0.1:7100,127.0.0.1:7101,127.0.0.1:7102",
                output=tmp,
                password="pw",
                control_auth="control",
                mode="ha",
                group_id="group-a",
                primary_index=1,
                failover_timeout_ms=750,
            )
            self.assertEqual(bootstrap.generate(args), 0)
            manifest = json.loads(Path(tmp, "manifest.json").read_text())
            self.assertEqual(manifest["primary"], "127.0.0.1:7101")
            self.assertEqual(manifest["slot_map"], {"0-16383": "127.0.0.1:7101"})
            for index, node in enumerate(manifest["nodes"]):
                cfg = json.loads(Path(tmp, f"node-{index}.json").read_text())
                self.assertEqual(cfg["cluster_slots"], manifest["slot_map"])
                self.assertEqual(cfg["failover_group_id"], "group-a")
                self.assertEqual(cfg["failover_quorum"], 2)
                self.assertEqual(cfg["failover_advertise_addr"], node)
                self.assertEqual(
                    sorted(cfg["failover_peers"]),
                    sorted(candidate for candidate in manifest["nodes"] if candidate != node),
                )
                self.assertEqual(cfg["masterauth"], "pw")


if __name__ == "__main__":
    unittest.main()

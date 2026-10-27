#!/usr/bin/env python3
"""Checks the generated configs against the fairness rules in PLAN.md, for
every cell, without running anything:

    python3 -m unittest -v test_cells        (from benchi/v021-cdc-parity/matrix)

A rule PLAN.md states and no test here checks is a rule nobody has verified.
"""
import glob
import json
import os
import re
import shutil
import tempfile
import unittest

import cells


def load(path):
    with open(path) as f:
        return json.load(f)


class Generated(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.mkdtemp(prefix="matrix-test-")
        for c in cells.CELLS:
            cells.gen(c, os.path.join(cls.tmp, c.id))

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.tmp)

    def cell_dir(self, c):
        return os.path.join(self.tmp, c.id)

    def tool(self, c, arm):
        return load(os.path.join(self.cell_dir(c), "tools", f"{arm}.yml"))["services"]["sut"]

    def test_every_yaml_is_json(self):
        for c in cells.CELLS:
            for p in glob.glob(os.path.join(self.cell_dir(c), "**", "*.yml"), recursive=True):
                load(p)

    def test_resource_parity(self):
        """Every SUT: same cpuset variable, same memory limit, no swap; every
        other container on the infra cpuset."""
        for c in cells.CELLS:
            for a in c.arms():
                s = self.tool(c, a)
                self.assertEqual(s["cpuset"], "${SUT_CPUSET}", (c.id, a))
                self.assertEqual(s["mem_limit"], cells.SUT_MEM, (c.id, a))
                self.assertEqual(s["memswap_limit"], cells.SUT_MEM, (c.id, a))
                self.assertEqual(s["container_name"], cells.SUT, (c.id, a))
            infra = load(os.path.join(self.cell_dir(c), "infra.yml"))["services"]
            for name, s in infra.items():
                self.assertEqual(s["cpuset"], "${INFRA_CPUSET}", (c.id, name))

    def test_shipped_tier_sets_no_tuning_knob(self):
        tuning = re.compile(r"sdk\.batch|compression|max\.batch\.size|max\.queue\.size|poll\.interval|HEAP_OPTS|Xmx|"
                            r"BATCH_SIZE|LINGER|MAX_POLL|linger\.ms|batch\.size")
        for c in cells.CELLS:
            for a in c.arms():
                if cells.arm_tier(a) != "s":
                    continue
                d = self.cell_dir(c)
                blobs = [json.dumps(self.tool(c, a))]
                blobs += [open(p).read() for p in glob.glob(os.path.join(d, "pipelines", a, "*"))]
                blobs += [open(p).read() for p in glob.glob(os.path.join(d, "connect", a, "*"))]
                blobs += [open(p).read() for p in glob.glob(os.path.join(d, "dbzserver", a, "*"))]
                for b in blobs:
                    # S3 flush.size is required by the Confluent sink (no
                    # default) and is not matched by the pattern.
                    m = tuning.search(b)
                    self.assertIsNone(m, f"{c.id} {a}: shipped tier sets {m and m.group(0)}")

    def test_tuned_tier_tunes_every_side(self):
        """Never tune one side more than the other: if one side has a tuned
        arm in a cell, every side has one, and each touches at least one knob."""
        for c in cells.CELLS:
            tuned = [a for a in c.arms() if cells.arm_tier(a) == "t"]
            if not tuned:
                continue
            sides = {cells.arm_side(a) for a in c.arms() if cells.arm_side(a) not in ("conduit-classic", "dbzserver-native")}
            self.assertEqual({cells.arm_side(a) for a in tuned}, sides, c.id)
            d = self.cell_dir(c)
            for a in tuned:
                blob = json.dumps(self.tool(c, a))
                for p in glob.glob(os.path.join(d, "*", a, "*")):
                    blob += open(p).read()
                self.assertRegex(blob, r"sdk\.batch\.size|max\.batch\.size|batch\.size|BATCH_SIZE", (c.id, a))

    def test_delivery_semantics(self):
        """acks=all on every Kafka producer; no exactly-once anywhere."""
        for c in cells.CELLS:
            d = self.cell_dir(c)
            for a in c.arms():
                side = cells.arm_side(a)
                blob = json.dumps(self.tool(c, a))
                for p in glob.glob(os.path.join(d, "*", a, "*")):
                    blob += open(p).read()
                self.assertNotIn("exactly.once", blob, (c.id, a))
                self.assertNotRegex(blob, r"acks\W+[01]\b", (c.id, a))
                if side == "connect":
                    self.assertIn('"CONNECT_PRODUCER_ACKS": "all"', blob)
                if "kafka" in c.dests and side.startswith("conduit"):
                    self.assertIn('"acks": "all"', blob)
                if "kafka" in c.dests and side == "dbzserver":
                    self.assertIn("debezium.sink.kafka.producer.acks=all", blob)

    def test_equal_parallelism(self):
        """PG source: every side captures every table with one slot per
        table (Conduit pipelines, Connect source connectors), except Debezium
        Server, which runs one connector per process by design (PLAN.md)."""
        for c in cells.CELLS:
            if c.source != "pg":
                continue
            d = self.cell_dir(c)
            for a in c.arms():
                side = cells.arm_side(a)
                slots = cells.slot_names(c, a)
                if side.startswith("conduit"):
                    p = load(os.path.join(d, "pipelines", a, "pipelines.yml"))["pipelines"]
                    self.assertEqual(len(p), c.par, (c.id, a))
                elif side == "connect":
                    srcs = [load(f) for f in glob.glob(os.path.join(d, "connect", a, "*src*.json"))]
                    self.assertEqual(len(srcs), c.par, (c.id, a))
                else:
                    self.assertEqual(len(slots), 1)
                for s in slots:
                    self.assertLessEqual(len(s), 63)
                    self.assertRegex(s, r"^[a-z0-9_]+$")

    def test_slots_are_unique_per_arm(self):
        for c in cells.CELLS:
            seen = set()
            for a in c.arms():
                for s in cells.slot_names(c, a):
                    self.assertNotIn(s, seen, (c.id, a))
                    seen.add(s)

    def test_kafka_source_parallelism_matches_by_tier(self):
        for c in cells.CELLS:
            if c.source != "kafka":
                continue
            d = self.cell_dir(c)
            for tier in ("s", "t"):
                want = c.par if tier == "t" else 1
                pipes = load(os.path.join(d, "pipelines", f"conduit-{tier}", "pipelines.yml"))["pipelines"]
                self.assertEqual(len(pipes), want, (c.id, tier))
                sinks = [load(f)["config"] for f in glob.glob(os.path.join(d, "connect", f"connect-{tier}", "*sink*.json"))]
                self.assertTrue(sinks)
                for s in sinks:
                    self.assertEqual(s["tasks.max"], str(want), (c.id, tier))

    def test_hooks_reference_containers_that_exist(self):
        for c in cells.CELLS:
            d = self.cell_dir(c)
            infra = load(os.path.join(d, "infra.yml"))["services"]
            names = {s["container_name"] for s in infra.values()}
            b = load(os.path.join(d, "bench.yml"))
            for step, hs in b["tests"][0]["steps"].items():
                for h in hs:
                    self.assertIn(h["container"], names, (c.id, step, h["name"]))
                    for t in h.get("tools", []):
                        self.assertIn(t, b["tools"])

    def test_latency_is_measured_where_promised(self):
        for c in cells.CELLS:
            if c.mode != "ramp":
                continue
            infra = load(os.path.join(self.cell_dir(c), "infra.yml"))["services"]
            cmd = infra["sampler"]["command"]
            self.assertNotIn("-latency-every", cmd, c.id)
            if "pg" in c.dests:
                self.assertIn("-pg-latency", cmd)
                self.assertIn("CREATE TRIGGER landed", open(os.path.join(self.cell_dir(c), "init.sql")).read())

    def test_kafka_broker_uses_append_time(self):
        for c in cells.CELLS:
            infra = load(os.path.join(self.cell_dir(c), "infra.yml"))["services"]
            if "kafka" in infra:
                self.assertEqual(infra["kafka"]["environment"]["KAFKA_LOG_MESSAGE_TIMESTAMP_TYPE"], "LogAppendTime")

    def test_estimate_and_shards_cover_every_cell(self):
        for k in (2, 4, 8):
            sh = cells.shard(k)
            placed = [cid for _, p in sh for cid, _ in p]
            want = [c.id for c in cells.CELLS] + [c.id for c in cells.CELLS if c.repeat]
            self.assertEqual(sorted(placed), sorted(want))
            for _, p in sh:  # a repeat never lands on the instance of its original
                ids = [cid for cid, _ in p]
                self.assertEqual(len(ids), len(set(ids)))


if __name__ == "__main__":
    unittest.main()

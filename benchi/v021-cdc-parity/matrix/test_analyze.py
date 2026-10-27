#!/usr/bin/env python3
"""Synthetic runs through analyze.py: the verdict rule, saturation validity,
ramp tracking. python3 -m unittest -v test_analyze"""
import json
import os
import shutil
import tempfile
import unittest

import analyze
import cells


def write_run(root, cid, label, arm, rate, offered, warm=180, window=300, start=1_000_000):
    rd = os.path.join(root, cid, label)
    os.makedirs(rd)
    with open(os.path.join(rd, "samples.jsonl"), "w") as f:
        for i in range(0, warm + window + 60, 5):
            t = start + i * 1000
            f.write(json.dumps({"ts_ms": t, "src_events": int(offered * i) + 1,
                                "sinks": {"kafka:x": {"count": int(rate * i) + 1}}}) + "\n")
    json.dump({"arm": arm, "label": label, "exit_code": 0, "hooks": {"TOOL_START_MS": start - 20000}},
              open(os.path.join(rd, "run.json"), "w"))


class Verdicts(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.cell = cells.BY_ID["A2-pg-kafka-p1-b64"]
        cells.gen(self.cell, os.path.join(self.tmp, self.cell.id, "cell"))

    def tearDown(self):
        shutil.rmtree(self.tmp)

    def test_resolved_when_far_outside_floor(self):
        rates = {"conduit-s": 50000, "conduit-s-aa": 50200, "connect-s": 40000, "conduit-t": 60000,
                 "connect-t": 59900, "dbzserver-s": 45000, "dbzserver-t": 52000, "conduit-classic-s": 9000}
        for r in range(1, 6):
            for arm, v in rates.items():
                jitter = 1 + 0.002 * ((r * 7 + len(arm)) % 3 - 1)
                write_run(self.tmp, self.cell.id, f"r{r}-{arm}", arm, v * jitter, offered=200000)
        md = analyze.report(self.tmp)
        res = json.load(open(os.path.join(self.tmp, "analysis.json")))[self.cell.id]
        v = res["verdicts"]
        self.assertEqual(v["conduit-s vs connect-s"]["verdict"], "RESOLVED", md)
        self.assertGreater(v["conduit-s vs connect-s"]["median_ratio"], 1.2)
        # 60000 vs 59900 is inside a ~0.4%+ floor
        self.assertNotEqual(v["conduit-t vs connect-t"]["verdict"], "RESOLVED")
        self.assertIn("conduit-s vs dbzserver-s", v)
        self.assertNotIn("conduit-s vs conduit-classic-s", v)

    def test_unsaturated_capacity_is_invalid(self):
        for r in range(1, 6):
            write_run(self.tmp, self.cell.id, f"r{r}-conduit-s", "conduit-s", 30000, offered=30000)
        analyze.report(self.tmp)
        res = json.load(open(os.path.join(self.tmp, "analysis.json")))[self.cell.id]
        self.assertIsNone(res["arms"]["conduit-s"]["summary"])  # kept up: not a capacity number
        runs = [r for r in res["runs"] if r["arm"] == "conduit-s"]
        self.assertTrue(all(r["valid"] is False for r in runs))


if __name__ == "__main__":
    unittest.main()

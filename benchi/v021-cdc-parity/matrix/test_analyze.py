#!/usr/bin/env python3
"""Synthetic runs through analyze.py: the pre-registered rule, lower bounds,
borrowed floors, cross-instance agreement, ramp ordinals, the harness check
and the smoke gate.   python3 -m unittest -v test_analyze"""
import json
import os
import shutil
import tempfile
import unittest

import analyze
import cells


def write_run(root, cdir, label, arm, rate, offered, warm=180, window=300, start=1_000_000, verify=None, exit_code=0):
    rd = os.path.join(root, cdir, label)
    os.makedirs(rd)
    with open(os.path.join(rd, "samples.jsonl"), "w") as f:
        for i in range(0, warm + window + 60, 5):
            t = start + i * 1000
            f.write(json.dumps({"ts_ms": t, "src": {"t_ms": t + 3, "events": int(offered * i) + 1},
                                "sinks": {"kafka:x": {"t_ms": t + 40, "count": int(rate * i) + 1}}}) + "\n")
    json.dump({"arm": arm, "label": label, "exit_code": exit_code, "hooks": {"TOOL_START_MS": start - 20000},
               "verify": verify or {}}, open(os.path.join(rd, "run.json"), "w"))


class Rule(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()

    def tearDown(self):
        shutil.rmtree(self.tmp)

    def cell(self, cid, cdir=None, meta_patch=None):
        c = cells.BY_ID[cid]
        d = cdir or cid
        cells.gen(c, os.path.join(self.tmp, d, "cell"))
        if meta_patch:
            p = os.path.join(self.tmp, d, "cell", "cell.json")
            m = json.load(open(p))
            m.update(meta_patch)
            json.dump(m, open(p, "w"))
        return c

    def rounds(self, cdir, rates, offered=400000, n=5):
        for r in range(1, n + 1):
            for arm, v in rates.items():
                jitter = 1 + 0.004 * ((r * 7 + len(arm)) % 3 - 1)
                write_run(self.tmp, cdir, f"r{r}-{arm}", arm, v * jitter, offered)

    def verdicts(self):
        _, out = analyze.report(self.tmp)
        return out

    def test_resolved_needs_more_than_the_floor_and_every_round(self):
        c = self.cell("A2-pg-kafka-p1-b64")
        rates = {a: 50000 for a in c.arms}
        rates.update({"conduit-avro-s": 50000, "connect-avro-s": 40000, "dbzserver-avro-s": 48500})
        self.rounds(c.id, rates)
        v = self.verdicts()["cells"][c.id]["result"]["verdicts"]
        self.assertEqual(v["conduit-avro-s vs connect-avro-s"]["final"], "RESOLVED")
        self.assertTrue(v["conduit-avro-s vs connect-avro-s"]["primary"])
        # 3% consistent difference is inside the pre-registered 5% minimum floor
        self.assertEqual(v["conduit-avro-s vs dbzserver-avro-s"]["final"], "UNRESOLVED")
        # like serialization only on Kafka: no JSON-vs-Avro pair
        self.assertNotIn("conduit-avro-s vs connect-s", v)

    def test_kept_up_is_a_lower_bound_not_a_ratio(self):
        c = self.cell("A2-pg-kafka-p1-b64")
        rates = {a: 30000 for a in c.arms}
        rates["conduit-avro-s"] = 60000  # equal to the loader: kept up
        rates["connect-avro-s"] = 30000  # saturated below it
        self.rounds(c.id, rates, offered=60000)
        out = self.verdicts()["cells"][c.id]
        runs = [r for r in out["runs"][0] if r["arm"] == "conduit-avro-s"]
        self.assertTrue(all(r["valid"] is False and r["lower_bound"] for r in runs))
        v = out["result"]["verdicts"]["conduit-avro-s vs connect-avro-s"]["final"]
        self.assertTrue(v.startswith("LOWER-BOUND: a >="), v)

    def test_borrowed_floor_is_doubled(self):
        c = self.cell("A2-pg-kafka-p4-b64")  # holdout shape: Conduit twin only, no comparator twin
        rates = {a: 50000 for a in c.arms}
        rates["conduit-avro-s-aa"] = 50000
        rates["connect-avro-s"] = 45000   # 11%: above 5%, below 2x
        self.rounds(c.id, rates)
        v = self.verdicts()["cells"][c.id]["result"]["verdicts"]["conduit-avro-s vs connect-avro-s"]
        self.assertGreaterEqual(v["per_instance"][0]["floor"], 0.05)

    def test_instances_must_agree(self):
        c = self.cell("A2-pg-kafka-p1-b64")
        self.cell("A2-pg-kafka-p1-b64", "A2-pg-kafka-p1-b64-rep1")
        base = {a: 50000 for a in c.arms}
        a = dict(base, **{"connect-avro-s": 40000})
        b = dict(base, **{"connect-avro-s": 60000})
        self.rounds(c.id, a)
        self.rounds("A2-pg-kafka-p1-b64-rep1", b)
        v = self.verdicts()["cells"][c.id]["result"]["verdicts"]["conduit-avro-s vs connect-avro-s"]["final"]
        self.assertEqual(v, "UNRESOLVED (instances disagree)")

    def test_ramp_is_ordinal(self):
        c = self.cell("A1-pg-kafka-ramp")
        rp = cells.RAMP_STEPS
        for r in range(1, 6):
            for arm in c.arms:
                rd = os.path.join(self.tmp, c.id, f"r{r}-{arm}")
                os.makedirs(rd)
                # conduit tracks up to 100k, connect to 50k
                cap = 100000 if arm == "conduit-avro-s" else 50000
                rows, t, src, dst = [], 1_000_000, 0, 0
                for step in range(len(rp) + 1):
                    target = rp[0] if step == 0 else rp[step - 1]
                    dur = cells.RAMP_WARM if step == 0 else cells.RAMP_STEP
                    for _ in range(0, dur, 5):
                        src += target * 5
                        dst += min(target, cap) * 5
                        t += 5000
                        rows.append({"ts_ms": t, "src": {"t_ms": t, "events": src, "step": step, "target": target},
                                     "sinks": {"kafka:x": {"t_ms": t, "count": dst}}})
                with open(os.path.join(rd, "samples.jsonl"), "w") as f:
                    f.write("\n".join(json.dumps(x) for x in rows))
                json.dump({"arm": arm, "label": f"r{r}-{arm}", "exit_code": 0, "hooks": {}}, open(os.path.join(rd, "run.json"), "w"))
        out = self.verdicts()["cells"][c.id]
        v = out["result"]["verdicts"]["conduit-avro-s vs connect-avro-s"]
        self.assertEqual(v["final"], "RESOLVED (ordinal)")
        run = next(r for r in out["runs"][0] if r["arm"] == "conduit-avro-s")
        self.assertEqual(run["highest_tracked_rate"], 100000)

    def test_refuses_another_harness(self):
        c = self.cell("A2-pg-kafka-p1-b64", meta_patch={"harness_hash": "0000000000000000"})
        self.rounds(c.id, {a: 1000 for a in c.arms}, n=1)
        with self.assertRaises(SystemExit):
            analyze.report(self.tmp)

    def test_gate_fails_on_any_unverified_record(self):
        c = self.cell("H-pg-kafka-verify")
        for arm in c.arms:
            ok = arm != "connect-avro-s"
            write_run(self.tmp, c.id, f"r1-{arm}", arm, 1000, 1000, verify={"kafka": {"ok": ok, "summary": "missing 3"}})
        g = analyze.gate(self.verdicts())
        self.assertFalse(g["pass"])
        self.assertTrue(any("connect-avro-s" in f for f in g["failures"]))

    def test_gate_fails_when_avro_arms_differ_on_the_wire(self):
        runs = [{"arm": "conduit-avro-s", "hooks": {"WIRE_KEY_BYTES_COUNT": 8, "WIRE_VALUE_BYTES_COUNT": 74, "WIRE_HEADERS_COUNT": 0}},
                {"arm": "connect-avro-s", "hooks": {"WIRE_KEY_BYTES_COUNT": 8, "WIRE_VALUE_BYTES_COUNT": 75, "WIRE_HEADERS_COUNT": 0}},
                {"arm": "conduit-s", "hooks": {"WIRE_KEY_BYTES_COUNT": 8, "WIRE_VALUE_BYTES_COUNT": 400, "WIRE_HEADERS_COUNT": 0}}]
        self.assertEqual(analyze.wire_failures("x", [runs]), [])
        runs.append({"arm": "dbzserver-avro-s", "hooks": {"WIRE_KEY_BYTES_COUNT": 8, "WIRE_VALUE_BYTES_COUNT": 75, "WIRE_HEADERS_COUNT": 5}})
        self.assertEqual(len(analyze.wire_failures("x", [runs])), 1)


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""Checks the generated configs against PLAN.md's rules, for every cell of
every plan, without running anything:

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

ALL = cells.plan_cells("all") + cells.smoke_cells(cells.build_cells())


def load(path):
    with open(path) as f:
        return json.load(f)


def blob(d):
    out = ""
    for p in sorted(glob.glob(os.path.join(d, "**", "*"), recursive=True)):
        if os.path.isfile(p):
            with open(p) as f:
                out += f.read() + "\n"
    return out


class Generated(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.mkdtemp(prefix="matrix-test-")
        cls.dirs = {}
        for c in {c.id: c for c in cells.plan_cells("all")}.values():
            d = os.path.join(cls.tmp, c.id)
            cells.gen(c, d)
            cls.dirs[c.id] = d

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.tmp)

    def arm_dir(self, c, a):
        return os.path.join(self.dirs[c.id], "arms", a)

    def tool(self, c, a):
        return load(os.path.join(self.arm_dir(c, a), "tools", "tool.yml"))["services"]["sut"]

    def cfg(self, c, a):
        """The arm's own configs (pipelines, connectors, server properties) and its tool service."""
        d = self.arm_dir(c, a)
        s = json.dumps(self.tool(c, a))
        for sub in ("pipelines", "connect", "dbzserver"):
            s += blob(os.path.join(d, sub))
        return s

    def cells(self):
        return cells.plan_cells("all")

    def test_every_yaml_is_json(self):
        for c in self.cells():
            for p in glob.glob(os.path.join(self.dirs[c.id], "**", "*.yml"), recursive=True):
                load(p)

    def test_resource_parity(self):
        for c in self.cells():
            for a in c.arms:
                s = self.tool(c, a)
                self.assertEqual((s["cpuset"], s["mem_limit"], s["memswap_limit"], s["container_name"]),
                                 ("${SUT_CPUSET}", cells.SUT_MEM, cells.SUT_MEM, cells.SUT), (c.id, a))
                self.assertEqual(s["logging"], cells.LOGGING)
            infra = load(os.path.join(self.arm_dir(c, c.arms[0]), "infra.yml"))["services"]
            for name, s in infra.items():
                self.assertEqual(s["cpuset"], "${INFRA_CPUSET}", (c.id, name))

    def test_shipped_tier_sets_no_tuning_knob(self):
        tuning = re.compile(r"sdk\.batch|compression|max\.batch\.size|max\.queue\.size|poll\.interval|HEAP_OPTS|Xmx|"
                            r"BATCH_SIZE|LINGER|MAX_POLL|FETCH_MIN|PARTITION_FETCH|linger\.ms|\"batch\.size|jdbc\.batch\.size|"
                            r"rotate\.interval|reduction\.buffer|cache_statement|reWriteBatched|snapshot\.fetch|OFFSET_FLUSH|offset\.flush")
        for c in self.cells():
            for a in c.arms:
                if cells.arm_tier(a) != "s":
                    continue
                s = self.cfg(c, a)
                if cells.arm_side(a) == "dbzserver-avro-xn":
                    # N JVMs in one 6 GiB container need a per-process heap:
                    # the default share divided by N, not tuning.
                    s = re.sub(r"-Xmx\d+m", "", s)
                m = tuning.search(s)
                self.assertIsNone(m, f"{c.id} {a}: shipped tier sets {m and m.group(0)}")

    def test_tuned_tier_uses_the_same_values_on_every_side(self):
        """Every tuned arm carries the TUNED concept values that apply to it,
        and no tuned arm carries a batch, wait or compression value that is
        not in TUNED (so no side is tuned harder)."""
        T = cells.TUNED
        for c in self.cells():
            for a in c.arms:
                if cells.arm_tier(a) != "t":
                    continue
                s, side = self.cfg(c, a), cells.arm_side(a)
                if c.source in ("pg", "kafka", "file") and side.startswith(("conduit", "connect", "dbzserver")):
                    if side.startswith("conduit"):
                        self.assertIn(f'"sdk.batch.size": "{T["batch_records"]}"', s, (c.id, a))
                        self.assertIn(f'"sdk.batch.delay": "{T["batch_wait_ms"]}ms"', s, (c.id, a))
                    elif c.source == "pg":
                        self.assertIn(str(T["batch_records"]), s)
                        self.assertIn(f'poll.interval.ms', s)
                if "kafka" in c.dests:
                    self.assertIn(T["compression"], s, (c.id, a))
                if not side.startswith("conduit") and side != "dbzserver-native":
                    self.assertIn(f"-Xmx{T['heap']}", s, (c.id, a))
                for m in re.finditer(r"(?:linger\.ms|LINGER_MS)\D+(\d+)", s):
                    self.assertEqual(int(m.group(1)), T["producer_linger_ms"])
                for m in re.finditer(r"sdk\.batch\.delay\": \"(\d+)ms", s):
                    self.assertIn(int(m.group(1)), (T["batch_wait_ms"], T["object_wait_ms"]))

    def test_every_cell_tunes_every_primary_side_or_none(self):
        """If a cell has a tuned arm, every side that has a shipped arm in the
        cell's primary serialization has a tuned arm too. Exploratory columns
        (the JSON arms of the Debezium sides next to their Avro arms, classic,
        native, the -xn scale-out row) run shipped only, by design."""
        exploratory = {"conduit-classic", "dbzserver-native", "dbzserver-avro-xn"}
        for c in self.cells():
            sides_t = {cells.arm_side(a) for a in c.arms if cells.arm_tier(a) == "t"}
            if not sides_t:
                continue
            sides_s = {cells.arm_side(a) for a in c.arms if cells.arm_tier(a) == "s"} - exploratory
            if any(s.endswith("avro") for s in sides_s):
                sides_s = {s for s in sides_s if s.endswith("avro") or (s == "conduit" and "pg" in c.dests)}
            self.assertEqual(sides_s - sides_t, set(), c.id)

    def test_delivery_semantics(self):
        for c in self.cells():
            for a in c.arms:
                s, side = self.cfg(c, a), cells.arm_side(a)
                self.assertNotIn("exactly.once", s, (c.id, a))
                self.assertNotRegex(s, r"acks\W+[01]\b", (c.id, a))
                if side.startswith("connect"):
                    self.assertIn('"CONNECT_PRODUCER_ACKS": "all"', s)
                if "kafka" in c.dests and side.startswith("conduit"):
                    self.assertIn('"acks": "all"', s)
                if "kafka" in c.dests and side.startswith("dbzserver"):
                    self.assertIn("debezium.sink.kafka.producer.acks=all", s)

    def test_recovery_tiers(self):
        for c in self.cells():
            if not c.mode.startswith("recover"):
                continue
            for a in c.arms:
                s, tier = self.cfg(c, a), cells.arm_tier(a)
                if tier == "e":
                    self.assertTrue("CONNECT_OFFSET_FLUSH_INTERVAL_MS\": \"1000" in s or "offset.flush.interval.ms=1000" in s, (c.id, a))
                if tier == "s":
                    self.assertNotIn("offset.flush.interval.ms", s.lower().replace("_", "."))

    def test_parallelism(self):
        """Shipped: one pipeline / connector / process with a table list (one
        slot). Tuned: one per table for Conduit and Connect. Debezium Server:
        one process, or the -xn arm with one process per table."""
        for c in self.cells():
            if c.source != "pg":
                continue
            n = cells.n_tables(c)
            for a in c.arms:
                side, tier = cells.arm_side(a), cells.arm_tier(a)
                want = n if (tier == "t" and side.startswith(("conduit", "connect")) and n > 1) or side == "dbzserver-avro-xn" else 1
                self.assertEqual(len(cells.slot_names(c, a)), want, (c.id, a))
                if side.startswith("conduit"):
                    p = load(os.path.join(self.arm_dir(c, a), "pipelines", "pipelines.yml"))["pipelines"]
                    self.assertEqual(len(p), want, (c.id, a))
                elif side.startswith("connect"):
                    srcs = [f for f in glob.glob(os.path.join(self.arm_dir(c, a), "connect", "*.json")) if '"src-' in open(f).read()]
                    self.assertEqual(len(srcs), want, (c.id, a))
                for s in cells.slot_names(c, a):
                    self.assertLessEqual(len(s), 63)
                    self.assertRegex(s, r"^[a-z0-9_]+$")

    def test_slots_unique_per_arm(self):
        for c in self.cells():
            seen = set()
            for a in c.arms:
                for s in cells.slot_names(c, a):
                    self.assertNotIn(s, seen, (c.id, a))
                    seen.add(s)

    def test_kafka_source_tasks_match_by_tier(self):
        for c in self.cells():
            if c.source != "kafka":
                continue
            for a in c.arms:
                side, tier = cells.arm_side(a), cells.arm_tier(a)
                want = c.par if tier == "t" else 1
                if side == "conduit":
                    self.assertEqual(len(load(os.path.join(self.arm_dir(c, a), "pipelines", "pipelines.yml"))["pipelines"]), want)
                elif side == "connect":
                    for f in glob.glob(os.path.join(self.arm_dir(c, a), "connect", "*.json")):
                        self.assertEqual(load(f)["config"]["tasks.max"], str(want), (c.id, a))

    def test_connect_file_sink_writes_json_not_a_java_map(self):
        for c in self.cells():
            if "file" not in c.dests:
                continue
            for a in c.arms:
                if not cells.arm_side(a).startswith("connect"):
                    continue
                for f in glob.glob(os.path.join(self.arm_dir(c, a), "connect", "*.json")):
                    cfg = load(f)["config"]
                    if cfg["connector.class"].endswith("FileStreamSinkConnector"):
                        self.assertEqual(cfg["value.converter"], "org.apache.kafka.connect.storage.StringConverter", (c.id, a))

    def test_jdbc_sinks_get_schemas(self):
        for c in self.cells():
            if "pg" not in c.dests:
                continue
            for a in c.arms:
                s, side = self.cfg(c, a), cells.arm_side(a)
                if side == "dbzserver":
                    self.assertIn("debezium.format.value.schemas.enable=true", s, (c.id, a))
                if cells.serialization(a) == "avro" and side.startswith(("connect", "dbzserver")):
                    self.assertIn("apicurio.registry.url", s, (c.id, a))

    def test_avro_arms_use_the_registry(self):
        for c in self.cells():
            for a in c.arms:
                if cells.serialization(a) != "avro":
                    continue
                s = self.cfg(c, a)
                self.assertIn(cells.REGISTRY, s, (c.id, a))
                infra = load(os.path.join(self.arm_dir(c, a), "infra.yml"))["services"]
                self.assertIn("registry", infra)

    def test_classic_runs_in_one_cell_only(self):
        with_classic = [c.id for c in self.cells() if any(cells.arm_side(a) == "conduit-classic" for a in c.arms)]
        self.assertEqual(with_classic, ["A2-pg-kafka-p1-b64"])

    def test_postgres_is_on_disk_and_counted_cheaply(self):
        for c in self.cells():
            infra = load(os.path.join(self.arm_dir(c, c.arms[0]), "infra.yml"))["services"]
            if "postgres" in infra:
                self.assertNotIn("tmpfs", infra["postgres"])
                self.assertIn("cdcparity-pg:/var/lib/postgresql/data", infra["postgres"]["volumes"])
            sinks = cells.sink_specs(c)
            self.assertFalse(any(s.startswith("pgcount") for s in sinks))
            if "pg" in c.dests:
                sql = open(os.path.join(self.arm_dir(c, c.arms[0]), "init.sql")).read()
                self.assertIn("seq bigint GENERATED ALWAYS", sql)
                self.assertIn("_seq ON bench_dest_0 (seq)", sql)

    def test_hooks_reference_containers_that_exist(self):
        for c in self.cells():
            for a in c.arms:
                d = self.arm_dir(c, a)
                names = {s["container_name"] for s in load(os.path.join(d, "infra.yml"))["services"].values()}
                b = load(os.path.join(d, "bench.yml"))
                for step, hs in b["tests"][0]["steps"].items():
                    for h in hs:
                        self.assertIn(h["container"], names, (c.id, a, step, h["name"]))

    def test_round_order_is_a_permutation_with_the_primary_block_adjacent(self):
        for c in ALL:
            block = cells.primary_block(c)
            positions = {a: set() for a in c.arms}
            for r in range(1, c.rounds + 1):
                o = cells.round_order(c, r)
                self.assertEqual(sorted(o), sorted(c.arms), (c.id, r))
                if block:
                    idx = sorted(o.index(a) for a in block)
                    self.assertEqual(idx, list(range(idx[0], idx[0] + len(block))), (c.id, r))
                for i, a in enumerate(o):
                    positions[a].add(i)
            if c.rounds >= 5 and len(c.arms) >= 5:
                # randomized: not every arm stuck in one position
                self.assertGreater(sum(len(p) > 1 for p in positions.values()), len(c.arms) // 2, c.id)

    def test_shards_cover_every_cell_and_repeat_on_distinct_instances(self):
        for plan in ("flagship", "core", "holdout", "all"):
            for k in (1, 2, 4, 8):
                sh = cells.shard(k, plan)
                placed = sorted(cid for _, p in sh for cid, _ in p)
                want = sorted(c.id for c in cells.plan_cells(plan) for _ in range(c.instances))
                self.assertEqual(placed, want)
                if k >= 3:
                    for _, p in sh:
                        ids = [cid for cid, _ in p]
                        self.assertEqual(len(ids), len(set(ids)), (plan, k))

    def test_smoke_contains_every_correctness_and_recovery_cell(self):
        smoke = {c.id for c in cells.smoke_cells(cells.build_cells())}
        for c in cells.build_cells():
            if c.family in ("H", "G"):
                self.assertIn(c.id, smoke)

    def test_primary_comparisons_exist(self):
        self.assertLessEqual(len(cells.PRIMARY), 8)
        for cid, a, b in cells.PRIMARY:
            c = cells.BY_ID[cid]
            self.assertIn(a, c.arms)
            for x in (b[len("best-of:"):].split(",") if b.startswith("best-of:") else [b]):
                self.assertIn(x, c.arms, (cid, x))
            self.assertGreaterEqual(c.rounds, 5)

    def test_avro_arms_carry_equal_content(self):
        """Review R2-B1: every Avro arm into Kafka writes the flattened row plus
        __op, deletes as tombstones (Debezium: ExtractNewRecordState with
        add.fields=op; Conduit: field.set __op + avro.encode of the row), one
        registry subject per table on every side."""
        for c in self.cells():
            if "kafka" not in c.dests or c.source != "pg":
                continue
            for a in c.arms:
                if cells.serialization(a) != "avro":
                    continue
                s, side = self.cfg(c, a), cells.arm_side(a)
                if side == "conduit-avro":
                    p = load(os.path.join(self.arm_dir(c, a), "pipelines", "pipelines.yml"))["pipelines"]
                    for pipe in p:
                        procs = pipe["processors"]
                        self.assertTrue(any(x["plugin"] == "field.set" and x["settings"]["field"] == ".Payload.After.__op" for x in procs), (c.id, a))
                        subj = [x["settings"]["schema.autoRegister.subject"] for x in procs if x["plugin"] == "avro.encode"]
                        tables = pipe["connectors"][0]["settings"]["tables"].split(",")
                        self.assertEqual(sorted(subj), sorted(f"cdcparity.public.{t}-value" for t in tables), (c.id, a))
                else:
                    self.assertIn("ExtractNewRecordState", s, (c.id, a))
                    self.assertRegex(s, r"unwrap\.add\.fields\W+op", (c.id, a))
                    self.assertRegex(s, r"delete\.tombstone\.handling\.mode\W+tombstone", (c.id, a))

    def test_noseq_arms_drop_the_seq_column_and_count_from_stats(self):
        c = cells.BY_ID["P-pg-pg-p1-b64"]
        for a in c.arms:
            sql = open(os.path.join(self.arm_dir(c, a), "init.sql")).read()
            infra = load(os.path.join(self.arm_dir(c, a), "infra.yml"))["services"]
            sinks = infra["sampler"]["command"][infra["sampler"]["command"].index("-sinks") + 1]
            if cells.parse_arm(a)[3] == "noseq":
                self.assertNotIn("seq bigint", sql)
                self.assertTrue(sinks.startswith("pgops:"), sinks)
            else:
                self.assertIn("seq bigint", sql)
                self.assertTrue(sinks.startswith("pg:"), sinks)

    def test_smoke_and_shakedown_cover_the_multi_table_paths(self):
        smoke = {c.id for c in cells.smoke_cells(cells.build_cells())}
        for cid in ("A2-pg-pg-p4-b64", "A2-pg-kafka-p4-b64", "H-pg-pg-p4-verify", "H-pg-kafka-p4-verify"):
            self.assertIn(cid, smoke)
        self.assertEqual(sorted(c.id for c in cells.plan_cells("shakedown")), sorted(cells.SHAKEDOWN_CELLS))

    def test_kafka_broker_uses_append_time(self):
        for c in self.cells():
            infra = load(os.path.join(self.arm_dir(c, c.arms[0]), "infra.yml"))["services"]
            if "kafka" in infra:
                self.assertEqual(infra["kafka"]["environment"]["KAFKA_LOG_MESSAGE_TIMESTAMP_TYPE"], "LogAppendTime")

    def test_harness_hash_is_stable(self):
        self.assertEqual(cells.harness_hash(), cells.harness_hash())


if __name__ == "__main__":
    unittest.main()

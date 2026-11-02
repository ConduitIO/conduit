#!/usr/bin/env python3
"""The parity matrix: every pre-registered cell, and the generator that turns a
cell into a runnable benchi config.

    cells.py list [--plan P]           cells and their arms
    cells.py table [--plan P]          the PLAN.md cell table (Markdown)
    cells.py gen CELL OUTDIR           write the cell's benchi config, compose files,
                                       Conduit pipelines, Connect connector configs,
                                       Debezium Server properties
    cells.py order CELL ROUND          the arm order of a round (blocked, seeded)
    cells.py estimate [--plan P] [--price USD]
                                       runs, instance-hours, dollars at 2, 4, 8 instances
    cells.py shard K I [--plan P]      the cells instance I of K runs
    cells.py hash                      the harness hash (code the results depend on)
    cells.py primary                   the pre-registered primary comparisons

Plans (PLAN.md section 12, the spend order): smoke, pilot, flagship, core,
holdout. A plan after smoke may launch only with a passing smoke gate for the
same harness hash (aws/launch.sh enforces it).

Stdlib only. Every YAML file it writes is JSON, which is valid YAML.

A CELL is one comparison; an ARM is one side, one serialization, one tier in
it: `<side>-<tier>[-aa][@variant]`, for example `connect-avro-s` (Debezium on
Kafka Connect, Avro through the registry, shipped defaults), `conduit-avro-s-aa`
(the A/A twin of conduit-avro-s), `connect-s@f10k` (shipped, S3 flush.size
10000). Arms of a cell are compared only with each other, in the same session
on the same instance.

The cell list below IS the pre-registration (PLAN.md is generated from it).
Changing it after results exist must be recorded in PLAN.md's change log.
"""
from __future__ import annotations

import hashlib
import json
import os
import random
import sys
from dataclasses import dataclass, replace

HERE = os.path.dirname(os.path.abspath(__file__))

# ---------------------------------------------------------------- pinned images
# Digests resolved 2026-10-27. quay.io's Debezium tags are rebuilt daily on a
# refreshed base image, so the digest, not the tag, is the pin.
IMG = {
    "postgres": "postgres:17@sha256:b8faeff7712663c2773a190181e1246e37e364b887611c04658c28b44e2408fe",
    "kafka": "apache/kafka:4.0.0@sha256:3f7b939115cd4872e9cee9369d80bd69712fde55f9902f46d793f64848dedc75",
    "alpine": "alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc",
    "minio": "pgsty/minio:RELEASE.2026-08-04T00-00-00Z@sha256:b6bfe7239bfc83fb90d31612d9704d86039dd714f7904b3f1ad68f211e602372",
    "registry": "quay.io/apicurio/apicurio-registry:3.3.3@sha256:c9cae4c90ce46538abf673c68eb591345f23bcc6c2aa6bfd47189adcf609d8f1",
    # Built by run-shard.sh: FROM quay.io/debezium/connect:3.7.0.Final@sha256:8dc50bab...
    # plus the file connectors and the Confluent JDBC and S3 sinks (images/connect.Dockerfile).
    "connect": "matrix-connect:3.7.0",
    # Built by run-shard.sh: FROM quay.io/debezium/server:3.7.0.Final@sha256:9117f3fa...
    # plus the Apicurio Avro converter (images/dbzserver.Dockerfile).
    "dbzserver": "matrix-dbzserver:3.7.0",
    # Digest resolved per run (images/pin.py -> images/PINS.json): quay.io
    # deletes superseded 3.7.0.Final manifests, so no repository pin survives.
    "dbzserver-native": "quay.io/debezium/server-native:3.7.0.Final",
    # Built by run-shard.sh from the repository Dockerfile at CONDUIT_SHA; the
    # -http image adds the conduitio-labs HTTP connector, the -diag image the
    # pprof build tag (diag runs only).
    "conduit": "matrix-conduit:bench",
    "conduit-http": "matrix-conduit:bench-http",
    "conduit-diag": "matrix-conduit:bench-diag",
}
_PINS = os.path.join(HERE, "images", "PINS.json")
if os.path.exists(_PINS):
    with open(_PINS) as _f:
        IMG["dbzserver-native"] = json.load(_f).get("dbzserver-native", IMG["dbzserver-native"])

JOLOKIA = ("https://repo1.maven.org/maven2/org/jolokia/jolokia-agent-jvm/2.2.9/jolokia-agent-jvm-2.2.9-javaagent.jar",
           "b15dc43047fc476d4ec6e12e769812bc71de1016")  # sha1, as Maven Central publishes it

PG_URL = "postgres://bench:bench@cdcparity-postgres:5432/bench?sslmode=disable"
BROKER = "cdcparity-kafka:9092"
RECEIVER = "http://cdcparity-receiver:8090"
REGISTRY = "http://cdcparity-registry:8080"

# One container per system under test, named cdcparity-sut, on the SUT cpuset
# with the same memory limit; everything else on the INFRA cpuset (cpusets.py).
SUT_MEM = "6g"
SUT = "cdcparity-sut"
LOGGING = {"driver": "json-file", "options": {"max-size": "50m", "max-file": "2"}}

SIDES = {
    "conduit": "Conduit, arch-v2 engine (--preview.pipeline-arch-v2), OpenCDC JSON on Kafka",
    "conduit-avro": "Conduit, arch-v2, Avro value through the schema registry (avro.encode)",
    "conduit-classic": "Conduit, classic engine; ONE continuity cell only (perf work is arch-v2 only)",
    "connect": "Debezium 3.7 on Apache Kafka Connect 4.x, JSON converter",
    "connect-avro": "Debezium 3.7 on Apache Kafka Connect 4.x, Apicurio Avro converter",
    "dbzserver": "Debezium Server 3.7.0.Final (JVM), JSON format",
    "dbzserver-avro": "Debezium Server 3.7.0.Final (JVM), Avro format through Apicurio",
    "dbzserver-avro-xn": "Debezium Server, one process PER TABLE in one container (its scale-out model)",
    "dbzserver-native": "Debezium Server 3.7.0.Final native (Mandrel) build, HTTP only",
}
TIERS = {
    "s": "shipped defaults",
    "t": "tuned with each vendor's documented knobs, at equal values (TUNED)",
    "e": "recovery cells: shipped, with every side's position flush interval set to 1 s",
}

# The tuned tier, by CONCEPT. Each side's tuned config is derived from this
# table, and test_cells.py checks every tuned arm against it, so no side is
# tuned more aggressively than another on the same concept.
TUNED = {
    "batch_records": 8192,     # source batch / poll size
    "batch_wait_ms": 100,      # how long a partial batch may wait
    "compression": "lz4",      # Kafka producer compression
    "producer_batch_bytes": 200000,
    "producer_linger_ms": 100,
    "consumer_fetch_min_bytes": 100000,
    "consumer_partition_fetch_bytes": 10485760,
    "object_records": 10000,   # S3 object size
    "object_wait_ms": 1000,
    "sink_batch_rows": 8192,   # JDBC/Postgres destination batch
    "heap": "3g",              # JVM heap, -Xms = -Xmx; Go: not tuned (GOGC/GOMEMLIMIT unset on every tier)
    "snapshot_fetch_rows": 50000,
}

RAMP_STEPS = [20000, 40000, 50000, 70000, 100000, 140000, 200000]
RAMP_WARM = 180     # held at the first step, excluded: JIT and GC reach steady state
RAMP_STEP = 120     # per step: 60 s to settle, then a 60 s tracking window
RAMP_SETTLE = 60
RAMP_BATCH = 100    # loader statements per round trip in ramp cells: a 5 ms marker floor at 20k ops/s
CAP_WARM = 180
CAP_WINDOW = 300    # >= 5 min measured at saturation
SNAP_ROWS = 10_000_000
SNAP_TIMEOUT = 2400
CORRECT_OPS = 3_000_000
RECOVER_RATE = 20000           # paced, below every side's capacity (2026-10-26: all tracked 20k)
RECOVER_OPS = 6_000_000        # 300 s of load
RECOVER_KILL_FRACTION = 0.30   # kill when the sink has 30% of the expected events
ROUNDS = 5
PILOT_ROUNDS = 3
SETUP_MIN = 25

# Smoke (MATRIX_SMOKE=1): every correctness and recovery cell plus one cell per
# family, short, one round. Smoke numbers are never results; smoke is the gate.
SMOKE = os.environ.get("MATRIX_SMOKE") == "1"
if SMOKE:
    RAMP_STEPS = [20000, 50000]
    RAMP_WARM, RAMP_STEP, RAMP_SETTLE = 30, 60, 30
    CAP_WARM, CAP_WINDOW = 30, 60
    SNAP_ROWS = 500_000
    CORRECT_OPS = 300_000
    RECOVER_OPS = 600_000


@dataclass(frozen=True)
class Cell:
    id: str
    family: str
    source: str               # pg | kafka | file
    dests: tuple
    mode: str                 # ramp | capacity | drain | snapshot | correct | recover | recover-snapshot | idle
    arms: tuple
    par: int = 1              # tables (pg source) or partitions (kafka source)
    payload: int = 64
    rounds: int = ROUNDS
    diag: bool = False        # one unmeasured profiling run per arm
    diag_arms: tuple = ()     # restrict the diag runs to these arms (empty = every arm)
    instances: int = 1        # >1: the cell repeats on that many DIFFERENT instances
    transform: bool = False
    backlog_bytes: int = 0    # drain cells
    plan: str = "core"        # smoke | pilot | flagship | core | holdout
    reference: bool = False   # compares an example connector (FileStream*): appendix only
    plugin: bool = False      # compares a labs plugin (HTTP): appendix only
    # workload
    txn: int = 0              # statements per transaction (0 = one per round trip)
    insert_frac: float = 0.70
    update_frac: float = 0.20
    shape: str = "narrow"
    skew: float = 0.0
    alter_at: int = 0         # seconds into the workload: ALTER TABLE ADD COLUMN
    sink_delay_ms: int = 0    # HTTP receiver delay (backpressure)
    note: str = ""


def parse_arm(arm: str):
    """-> (side, tier, aa, variant)"""
    base, _, variant = arm.partition("@")
    aa = base.endswith("-aa")
    if aa:
        base = base[:-3]
    side, _, tier = base.rpartition("-")
    return side, tier, aa, variant


def arm_side(arm):
    return parse_arm(arm)[0]


def arm_tier(arm):
    return parse_arm(arm)[1]


def is_jvm(arm):
    s = arm_side(arm)
    return s.startswith("connect") or (s.startswith("dbzserver") and s != "dbzserver-native")


def serialization(arm):
    s = arm_side(arm)
    return "avro" if "avro" in s else "json"


# ---------------------------------------------------------------- the matrix
def pg_arms(dest: str, flagship: bool, tiers=("s", "t")) -> tuple:
    """Arms of a Postgres-source cell by destination. Primary serialization for
    Kafka and Postgres destinations is Avro through the registry for the
    Debezium sides (their recommended production setup); their JSON arms run
    shipped only, as exploratory columns."""
    arms = []
    if dest == "kafka":
        for t in tiers:
            arms += [f"conduit-avro-{t}", f"connect-avro-{t}", f"dbzserver-avro-{t}"]
        arms += ["conduit-s", "connect-s", "dbzserver-s"]
        if flagship:
            arms += ["conduit-avro-s-aa", "connect-avro-s-aa", "dbzserver-avro-s-aa"]
        else:
            arms += ["conduit-avro-s-aa"]
    elif dest == "pg":
        for t in tiers:
            arms += [f"conduit-{t}", f"connect-avro-{t}", f"dbzserver-avro-{t}"]
        arms += ["connect-s", "dbzserver-s"]
        arms += ["conduit-s-aa", "connect-avro-s-aa", "dbzserver-avro-s-aa"] if flagship else ["conduit-s-aa"]
    elif dest in ("file", "s3"):
        for t in tiers:
            arms += [f"conduit-{t}"]
            if dest == "s3" and t == "s":
                # flush.size has no default: the shipped Connect S3 sink is
                # reported at three sizes; the primary comparator is the best.
                arms += ["connect-s@f1k", "connect-s@f10k", "connect-s@f100k"]
            else:
                arms += [f"connect-{t}"]
        arms += ["conduit-s-aa"]
    elif dest == "http":
        for t in tiers:
            arms += [f"conduit-{t}", f"dbzserver-{t}"]
        arms += ["dbzserver-native-s", "conduit-s-aa"]
    return tuple(arms)


def build_cells() -> list:
    cells = []
    # A1: offered-rate ramp, shipped tier, base shape.
    for d in ("kafka", "pg", "s3", "file", "http"):
        arms = pg_arms(d, flagship=d == "kafka", tiers=("s",))
        cells.append(Cell(f"A1-pg-{d}-ramp", "A", "pg", (d,), "ramp", arms,
                          plan="flagship" if d == "kafka" else "core",
                          reference=d == "file", plugin=d == "http"))
    # A2: capacity, base shape (phase 1) and shapes (hold-out).
    for d in ("kafka", "pg", "s3", "file", "http"):
        flag = d in ("kafka", "pg")
        arms = pg_arms(d, flagship=flag)
        cells.append(Cell(f"A2-pg-{d}-p1-b64", "A", "pg", (d,), "capacity", arms, diag=True,
                          instances=3 if d == "kafka" else (2 if d == "pg" else 1),
                          plan="flagship" if flag else "core", reference=d == "file", plugin=d == "http"))
        if d == "http":
            continue
        for par, pl in ((4, 64), (16, 64), (1, 1024), (1, 16384)):
            arms = pg_arms(d, flagship=False)
            if par > 1 and d in ("kafka", "pg"):
                arms += ("dbzserver-avro-xn-s",)
            cells.append(Cell(f"A2-pg-{d}-p{par}-b{pl}", "A", "pg", (d,), "capacity", arms, par=par, payload=pl,
                              plan="holdout", reference=d == "file",
                              note="16 KiB payloads TOAST on Postgres" if pl == 16384 else ""))
    # B: Kafka topic backlog -> sink (Kafka Connect's core use case).
    for d in ("pg", "s3", "file"):
        shapes = [(1, 64), (4, 64), (16, 64), (1, 1024), (1, 16384)] if d != "file" else [(1, 64), (1, 1024), (1, 16384)]
        for par, pl in shapes:
            base = (par, pl) == (1, 64)
            if d == "s3":
                arms = ("conduit-s", "connect-s@f1k", "connect-s@f10k", "connect-s@f100k", "conduit-t", "connect-t", "conduit-s-aa")
            else:
                arms = ("conduit-s", "connect-s", "conduit-t", "connect-t", "conduit-s-aa")
            if base and d in ("pg", "s3"):
                arms += ("connect-s-aa",) if d == "pg" else ("connect-s-aa@f10k",)
            cells.append(Cell(f"B-kafka-{d}-p{par}-b{pl}", "B", "kafka", (d,), "drain", arms, par=par, payload=pl,
                              diag=base, backlog_bytes=12 * 2**30,
                              plan=("flagship" if d in ("pg", "s3") else "core") if base else "holdout",
                              reference=d == "file"))
    # C: file -> Kafka (FileStreamSource, an example connector).
    for pl in (64, 1024, 16384):
        cells.append(Cell(f"C-file-kafka-b{pl}", "C", "file", ("kafka",), "drain",
                          ("conduit-s", "connect-s", "conduit-t", "connect-t", "conduit-s-aa"), payload=pl,
                          diag=pl == 64, backlog_bytes=8 * 2**30, plan="core" if pl == 64 else "holdout", reference=True))
    # D: transform (flatten + rename) to Kafka.
    cells.append(Cell("D-pg-kafka-transform", "D", "pg", ("kafka",), "capacity",
                      ("conduit-avro-s", "connect-avro-s", "dbzserver-avro-s", "conduit-avro-t", "connect-avro-t",
                       "dbzserver-avro-t", "conduit-avro-s-aa"), transform=True, diag=True))
    # E: fan-out to file and S3: an ARCHITECTURE comparison (Conduit's two
    # destinations ack as a unit; Connect's two sinks are independent consumers).
    cells.append(Cell("E-pg-fanout-file-s3", "E", "pg", ("file", "s3"), "capacity",
                      ("conduit-s", "connect-s@f10k", "conduit-t", "connect-t", "conduit-s-aa"), diag=True,
                      note="architecture comparison; no speed ratio is headlined"))
    # F: initial snapshot, time to land every row.
    for d in ("kafka", "pg"):
        cells.append(Cell(f"F-pg-{d}-snapshot", "F", "pg", (d,), "snapshot",
                          pg_arms(d, flagship=False), plan="flagship" if d == "kafka" else "core"))
    # G: kill -9 mid-stream (paced load, kill on delivered count) and mid-snapshot.
    for d in ("kafka", "pg"):
        c = "conduit-avro" if d == "kafka" else "conduit"
        arms = (f"{c}-s", "connect-avro-s", "dbzserver-avro-s", "connect-avro-e", "dbzserver-avro-e")
        cells.append(Cell(f"G-pg-{d}-kill9", "G", "pg", (d,), "recover", arms,
                          plan="flagship" if d == "kafka" else "core"))
    cells.append(Cell("G-pg-kafka-kill9-snapshot", "G", "pg", ("kafka",), "recover-snapshot",
                      ("conduit-avro-s", "connect-avro-s", "dbzserver-avro-s", "connect-avro-e", "dbzserver-avro-e")))
    # H: record-by-record correctness at saturation, every source/destination.
    for d in ("kafka", "pg", "s3", "file", "http"):
        arms = tuple(a for a in pg_arms(d, flagship=False) if not parse_arm(a)[2])
        cells.append(Cell(f"H-pg-{d}-verify", "H", "pg", (d,), "correct", arms, rounds=1))
    for d in ("pg", "s3", "file"):
        cells.append(Cell(f"H-kafka-{d}-verify", "H", "kafka", (d,), "correct",
                          ("conduit-s", "conduit-t", "connect-s" if d != "s3" else "connect-s@f10k", "connect-t"),
                          rounds=1, backlog_bytes=0))
    cells.append(Cell("H-file-kafka-verify", "H", "file", ("kafka",), "correct",
                      ("conduit-s", "conduit-t", "connect-s", "connect-t"), rounds=1))
    cells.append(Cell("H-pg-kafka-transform-verify", "H", "pg", ("kafka",), "correct",
                      ("conduit-avro-s", "connect-avro-s", "dbzserver-avro-s"), transform=True, rounds=1))
    cells.append(Cell("H-pg-fanout-file-s3-verify", "H", "pg", ("file", "s3"), "correct",
                      ("conduit-s", "connect-s@f10k"), rounds=1))
    # Multi-table correctness through the shipped one-connector path (table
    # templates, topics.regex + RegexRouter, per-table subjects, tombstones
    # attributed by topic): review R2-M2.
    cells.append(Cell("H-pg-pg-p4-verify", "H", "pg", ("pg",), "correct",
                      ("conduit-s", "connect-avro-s", "dbzserver-avro-s", "conduit-t", "connect-avro-t", "dbzserver-avro-t"),
                      par=4, rounds=1))
    cells.append(Cell("H-pg-kafka-p4-verify", "H", "pg", ("kafka",), "correct",
                      ("conduit-avro-s", "connect-avro-s", "dbzserver-avro-s", "conduit-avro-t", "connect-avro-t", "dbzserver-avro-t"),
                      par=4, rounds=1))
    cells.append(Cell("H-pg-kafka-wide-txn1-verify", "H", "pg", ("kafka",), "correct",
                      ("conduit-avro-s", "connect-avro-s", "dbzserver-avro-s"), rounds=1, shape="wide", txn=1))
    # W: workload hold-out (mandatory before any public claim), pg -> kafka and pg -> pg.
    for d in ("kafka", "pg"):
        base = pg_arms(d, flagship=False, tiers=("s",))
        # Amendment 3 (2026-10-28): small transactions, update-heavy, wide
        # typed rows (and the slow sink below). Deferred, not dropped from
        # the design: txn100, txn10000, delete-heavy, skew16, the mid-run
        # schema change and idle (the loader and generator keep them).
        for name, kw in (("txn1", dict(txn=1, par=4)), ("txn10", dict(txn=10, par=4)),
                         ("updheavy", dict(insert_frac=0.2, update_frac=0.7)), ("wide", dict(shape="wide"))):
            cells.append(Cell(f"W-pg-{d}-{name}", "W", "pg", (d,), "capacity", base, plan="holdout", **kw))
    cells.append(Cell("W-pg-http-slowsink", "W", "pg", ("http",), "capacity", pg_arms("http", False, ("s",)),
                      sink_delay_ms=5, plan="holdout", plugin=True, note="5 ms per request at the receiver: backpressure"))
    # P: the pilot (one instance): per-arm noise, loader ceiling, WAL growth,
    # fill and drain rates, and the counting-overhead control.
    cells.append(Cell("P-pg-kafka-p1-b64", "P", "pg", ("kafka",), "capacity",
                      ("conduit-avro-s", "connect-avro-s", "dbzserver-avro-s",
                       "conduit-avro-s@i30", "connect-avro-s@i30", "dbzserver-avro-s@i30"),
                      rounds=PILOT_ROUNDS, plan="pilot",
                      note="@i30: sampler every 30 s instead of 5 s; rates must agree within the floor"))
    cells.append(Cell("P-pg-pg-p1-b64", "P", "pg", ("pg",), "capacity",
                      ("conduit-s", "connect-avro-s", "dbzserver-avro-s", "conduit-s@noseq", "connect-avro-s@noseq",
                       "dbzserver-avro-s@noseq"),
                      rounds=PILOT_ROUNDS, plan="pilot",
                      note="@noseq: destination tables without the generated seq column and index, counted from "
                           "pg_stat_user_tables: the seq column's cost, and whether the destination saturates"))
    cells.append(Cell("P-kafka-pg-p1-b64", "P", "kafka", ("pg",), "drain", ("conduit-s", "connect-s"),
                      rounds=2, backlog_bytes=12 * 2**30, plan="pilot", note="measures fill time and drain rates"))
    return cells


# The pre-registered PRIMARY comparisons (PLAN.md section 9). Every other pair
# is exploratory: reported, never headlined. Comparator "best-of:" means the
# best of those arms per round (the S3 flush sweep).
PRIMARY = [
    ("A2-pg-kafka-p1-b64", "conduit-avro-s", "connect-avro-s"),
    ("A2-pg-kafka-p1-b64", "conduit-avro-s", "dbzserver-avro-s"),
    ("A2-pg-pg-p1-b64", "conduit-s", "connect-avro-s"),
    ("A2-pg-pg-p1-b64", "conduit-s", "dbzserver-avro-s"),
    ("B-kafka-pg-p1-b64", "conduit-s", "connect-s"),
    ("B-kafka-s3-p1-b64", "conduit-s", "best-of:connect-s@f1k,connect-s@f10k,connect-s@f100k"),
    ("A1-pg-kafka-ramp", "conduit-avro-s", "connect-avro-s"),
    ("F-pg-kafka-snapshot", "conduit-avro-s", "connect-avro-s"),
]


def trim(cs: list) -> list:
    """Amendment 3 (PLAN.md section 15): 5 rounds and a second instance only
    for the cells of the 8 primary comparisons; every other measured cell 3
    rounds on one instance. Correctness (H) and pilot (P) cells keep theirs."""
    primary_cells = {cid for cid, _, _ in PRIMARY}
    out = []
    for c in cs:
        if c.id in primary_cells:
            out.append(replace(c, rounds=ROUNDS, instances=2))
        elif c.family in ("H", "P"):
            out.append(c)
        else:
            out.append(replace(c, rounds=3, instances=1))
    return out


def smoke_cells(cs: list) -> list:
    keep = {c.id for c in cs if c.family in ("H", "G")} | {
        "A2-pg-kafka-p1-b64", "A2-pg-pg-p1-b64", "A2-pg-s3-p1-b64", "A2-pg-file-p1-b64", "A2-pg-http-p1-b64",
        "A2-pg-kafka-p4-b64", "A2-pg-pg-p4-b64", "A1-pg-kafka-ramp", "B-kafka-pg-p1-b64", "B-kafka-s3-p1-b64", "B-kafka-file-p1-b64",
        "C-file-kafka-b64", "D-pg-kafka-transform", "E-pg-fanout-file-s3", "F-pg-kafka-snapshot", "F-pg-pg-snapshot",
        "W-pg-kafka-txn1", "W-pg-kafka-wide", "W-pg-http-slowsink", "P-pg-kafka-p1-b64"}
    out = []
    for c in cs:
        if c.id in keep:
            out.append(replace(c, rounds=1, instances=1, diag=c.id == "A2-pg-kafka-p1-b64", plan="smoke",
                               backlog_bytes=min(c.backlog_bytes, 256 * 2**20) if c.backlog_bytes else 0))
    return out


# The shakedown (review round 2): four cells, smoke-length, on one instance,
# to flush wiring bugs before the gated smoke. Never results, never a gate.
# Pass 3: only the paths not yet proven (B-kafka-pg and Conduit's
# multi-table paths passed in pass 2).
SHAKEDOWN_CELLS = ["A2-pg-kafka-p1-b64"]
# One arm per wiring path (each side, JSON and Avro, the multi-table and
# scale-out paths, one tuned Conduit arm), to stay under the $5 cap; the
# other tuned arms are first exercised by the smoke run. Pass 3 keeps only
# the paths passes 1 and 2 did not prove (connect-avro-s stays on
# A2-pg-kafka because its diag run is the async-profiler check). Pass 4
# (wire check) runs every side once on A2-pg-kafka to sample the records
# each one puts on the broker.
SHAKEDOWN_ARMS = {
    "A2-pg-kafka-p1-b64": ("conduit-avro-s", "connect-avro-s", "dbzserver-avro-s"),
    "A2-pg-pg-p4-b64": ("connect-avro-s", "dbzserver-avro-s", "dbzserver-avro-xn-s"),
    "B-kafka-pg-p1-b64": ("conduit-s", "connect-s"),
    "G-pg-kafka-kill9": ("conduit-avro-s", "connect-avro-s", "dbzserver-avro-s"),
}
SHAKEDOWN_DIAG = {}


def shakedown_cells(cs: list) -> list:
    return [replace(c, plan="shakedown", arms=SHAKEDOWN_ARMS[c.id], diag=c.id in SHAKEDOWN_DIAG,
                    diag_arms=SHAKEDOWN_DIAG.get(c.id, ()))
            for c in smoke_cells(cs) if c.id in SHAKEDOWN_CELLS]


# The cells this process generates and runs: the plan's own variants
# (MATRIX_PLAN, set by run-shard.sh), so a shakedown runs its own arm lists.
if os.environ.get("MATRIX_PLAN") == "shakedown":
    CELLS = shakedown_cells(build_cells())
else:
    CELLS = smoke_cells(build_cells()) if SMOKE else trim(build_cells())
BY_ID = {c.id: c for c in CELLS}



def plan_cells(plan: str) -> list:
    if plan == "smoke":
        return smoke_cells(build_cells())
    if plan == "shakedown":
        return shakedown_cells(build_cells())
    order = ["pilot", "flagship", "core", "holdout"]
    if plan == "all":
        return [c for c in trim(build_cells()) if c.plan in order]
    return [c for c in trim(build_cells()) if c.plan == plan]


# ---------------------------------------------------------------- run order
def primary_block(c: Cell) -> list:
    """The arms of the cell's primary comparisons, run back to back."""
    arms = []
    for cid, a, b in PRIMARY:
        if cid != c.id:
            continue
        for x in [a] + (b[len("best-of:"):].split(",") if b.startswith("best-of:") else [b]):
            if x in c.arms and x not in arms:
                arms.append(x)
    return arms


def round_order(c: Cell, r: int) -> list:
    """Round r's arm order. The primary arms form one block, run adjacently,
    rotated each round and reversed on even rounds (AB / BA); the block and
    the other arms are shuffled with a seed derived from the cell and round.
    A fixed rotation is NOT used: with more arms than rounds it does not give
    every arm every position (review B4)."""
    block = primary_block(c)
    rest = [a for a in c.arms if a not in block]
    if block:
        k = (r - 1) % len(block)
        block = block[k:] + block[:k]
        if r % 2 == 0:
            block = block[::-1]
    units = [block] if block else []
    units += [[a] for a in rest]
    seed = int(hashlib.sha256(f"{c.id}/{r}".encode()).hexdigest()[:12], 16)
    random.Random(seed).shuffle(units)
    return [a for u in units for a in u]


# ---------------------------------------------------------------- sizing
def record_bytes(c: Cell) -> int:
    """Estimated bytes of one backlog record (key + value): the Kafka Connect
    JSON envelope with its schema is ~330 B around the payload (review B7)."""
    if c.source == "kafka":
        return 330 + c.payload + 120
    return c.payload + 30


def backlog_records(c: Cell) -> int:
    if c.mode == "correct" and c.source != "pg":
        return CORRECT_OPS
    if not c.backlog_bytes:
        return 0
    return max(100_000, c.backlog_bytes // record_bytes(c))


def run_seconds(c: Cell) -> int:
    if c.mode == "ramp":
        return RAMP_WARM + RAMP_STEP * len(RAMP_STEPS)
    if c.mode in ("capacity", "drain", "idle"):
        return CAP_WARM + CAP_WINDOW
    return 10  # snapshot/correct wait in pre-test hooks; recover is not a benchi run


def est_minutes(c: Cell, arm: str) -> float:
    """Wall minutes for ONE run (estimate; the pilot replaces the guesses)."""
    overhead = 3.0  # compose up, JVM start, registration, collectors, compose down
    if c.mode in ("ramp", "capacity", "idle"):
        m = overhead + run_seconds(c) / 60
        if "kafka" in c.dests:
            m += 1.0  # post-run kafka-hist and broker batch dump
        return m
    if c.mode == "drain":
        return overhead + run_seconds(c) / 60 + 1.0  # fill is once per cell (cell_minutes)
    if c.mode == "snapshot":
        return overhead + 3.0 + 7.0
    if c.mode == "correct":
        return overhead + 9.0
    if c.mode == "recover":
        return overhead + RECOVER_OPS / RECOVER_RATE / 60 + 6.0
    if c.mode == "recover-snapshot":
        return overhead + 3.0 + 14.0
    raise ValueError(c.mode)


def runs_per_arm(c: Cell) -> int:
    warm = 1 if c.mode in ("ramp", "capacity", "drain", "snapshot", "idle") and not SMOKE else 0
    return c.rounds + warm + (1 if c.diag and (not c.diag_arms) else 0)


def cell_minutes(c: Cell) -> float:
    m = sum(est_minutes(c, a) * runs_per_arm(c) for a in c.arms)
    m += sum(est_minutes(c, a) for a in c.diag_arms)
    if c.mode == "drain":
        m += c.backlog_bytes / (150 * 2**20) / 60  # one fill per cell at ~150 MB/s (pilot measures it)
    return m


# ---------------------------------------------------------------- generation helpers
def dump(path: str, obj) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        json.dump(obj, f, indent=2)
        f.write("\n")


def write(path: str, text: str) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        f.write(text)


def n_tables(c: Cell) -> int:
    return c.par if c.source == "pg" and c.mode not in ("snapshot", "recover-snapshot") else (1 if c.source == "pg" else 0)


def dest_tables(c: Cell) -> int:
    return c.par if c.source == "pg" and c.mode not in ("snapshot", "recover-snapshot") else 1


def topic_pattern(c: Cell) -> str:
    return r"^cdcparity\.public\.bench_source_[0-9]+$" if c.source == "pg" else r"^cdcparity\.out$"


def sink_specs(c: Cell, purpose: str = "count", arm: str = "") -> list:
    out = []
    noseq = parse_arm(arm)[3] == "noseq" if arm else False
    for d in c.dests:
        if d == "file":
            out.append("file:/sink/out_*.txt")
        elif d == "pg":
            snap = c.mode in ("snapshot", "recover-snapshot")
            kind = "pgins:" if snap and purpose == "count" else ("pgops:" if noseq and purpose == "count" else "pg:")
            out.append(kind + "bench_dest_%d")
        elif d == "s3":
            out.append("s3:bench")
        elif d == "kafka":
            out.append("kafka:" + topic_pattern(c))
        elif d == "http":
            out.append("http:" + RECEIVER if purpose == "count" else "file:/sink/http.jsonl")
    return out


def needs_broker(c: Cell) -> bool:
    return c.source == "kafka" or "kafka" in c.dests or any(arm_side(a).startswith("connect") for a in c.arms)


def needs_registry(c: Cell) -> bool:
    return any(serialization(a) == "avro" for a in c.arms)


def needs_pg(c: Cell) -> bool:
    return c.source == "pg" or "pg" in c.dests


def multi(c: Cell, arm: str) -> bool:
    """One pipeline/connector PER TABLE? Shipped: no (one connector with a
    table list, the idiom on every side). Tuned: yes, for Conduit and
    Connect. Debezium Server is one process either way; its per-table
    scale-out is its own arm (dbzserver-avro-xn)."""
    side, tier, _, _ = parse_arm(arm)
    return tier == "t" and side.startswith(("conduit", "connect")) and n_tables(c) > 1


def slot_base(arm: str) -> str:
    return arm.replace("-", "_").replace("@", "_")


def slot_names(c: Cell, arm: str) -> list:
    if c.source != "pg":
        return []
    b = slot_base(arm)
    side = arm_side(arm)
    if side == "dbzserver-avro-xn":
        return [f"{b}_slot_{i}" for i in range(n_tables(c))]
    if multi(c, arm):
        return [f"{b}_slot_{i}" for i in range(n_tables(c))]
    return [f"{b}_slot"]


def tables_list(c: Cell) -> list:
    return [f"bench_source_{i}" for i in range(n_tables(c))]


# ---------------------------------------------------------------- infrastructure
def svc(**kw) -> dict:
    kw.setdefault("cpuset", "${INFRA_CPUSET}")
    kw.setdefault("networks", ["benchi"])
    kw.setdefault("logging", LOGGING)
    return kw


def loader_command(c: Cell) -> list:
    L = "/loader/loader"
    common = ["-payload", str(c.payload), "-shape", c.shape]
    if c.source == "pg":
        cmd = [L, "load", "-url", PG_URL, "-tables", str(n_tables(c)), "-slots", "${LOADER_SLOTS:-}",
               "-slot-timeout", "600s", "-conns", str(min(n_tables(c), 4)), "-txn", str(c.txn),
               "-insert-frac", str(c.insert_frac), "-update-frac", str(c.update_frac)] + common
        if c.skew:
            cmd += ["-skew", str(c.skew)]
        if c.alter_at:
            cmd += ["-alter-at", f"{c.alter_at}s"]
        if c.mode == "ramp":
            sched = [f"{RAMP_STEPS[0]}:{RAMP_WARM}s"] + [f"{r}:{RAMP_STEP}s" for r in RAMP_STEPS]
            cmd += ["-schedule", ",".join(sched), "-batch", str(RAMP_BATCH)]
        elif c.mode == "capacity":
            cmd += ["-rate", "0"]
        elif c.mode == "idle":
            cmd += ["-rate", "1", "-batch", "1"]
        elif c.mode in ("snapshot", "recover-snapshot"):
            cmd = [L, "load", "-url", PG_URL, "-tables", "1", "-window", str(SNAP_ROWS), "-fill-only",
                   "-done-file", "/sink/.fill_done"] + common
        elif c.mode == "correct":
            cmd += ["-rate", "0", "-total", str(CORRECT_OPS)]
        elif c.mode == "recover":
            cmd += ["-rate", str(RECOVER_RATE), "-total", str(RECOVER_OPS)]
        return cmd
    if c.source == "kafka":
        return [L, "fill-kafka", "-brokers", BROKER, "-topic", "bench.source", "-partitions", str(c.par),
                "-count", str(backlog_records(c)), "-payload", str(c.payload), "-done-file", "/sink/.fill_done"]
    return [L, "fill-file", "-path", "/sink/in_0.txt", "-count", str(backlog_records(c)),
            "-payload", str(c.payload), "-done-file", "/sink/.fill_done"]


def infra_compose(c: Cell, arm: str = "") -> dict:
    s = {}
    if needs_pg(c):
        s["postgres"] = svc(
            image=IMG["postgres"], container_name="cdcparity-postgres",
            environment={"POSTGRES_USER": "bench", "POSTGRES_PASSWORD": "bench", "POSTGRES_DB": "bench"},
            # On EBS (gp3), fsync on: destination commits cost what they cost
            # (review B8). The settings are infrastructure, identical for every
            # arm: slots for 16 tables x 2, a buffer pool and WAL size that
            # keep checkpoints out of the windows, a WAL budget a slow slot
            # cannot exceed, and pg_stat_statements for the destination side.
            command=["postgres", "-c", "wal_level=logical", "-c", "max_replication_slots=64",
                     "-c", "max_wal_senders=64", "-c", "shared_buffers=4GB", "-c", "max_wal_size=16GB",
                     "-c", "checkpoint_timeout=30min", "-c", "max_slot_wal_keep_size=40GB",
                     "-c", "shared_preload_libraries=pg_stat_statements", "-c", "track_io_timing=on"],
            shm_size="4g",
            volumes=["./init.sql:/docker-entrypoint-initdb.d/init.sql:ro", "cdcparity-pg:/var/lib/postgresql/data"],
            healthcheck={"test": ["CMD", "pg_isready", "-U", "bench", "-d", "bench"],
                         "interval": "1s", "timeout": "5s", "retries": 300})
    if needs_broker(c):
        s["kafka"] = svc(
            image=IMG["kafka"], container_name="cdcparity-kafka",
            environment={
                "CLUSTER_ID": "cdcparity-kafka-cluster", "KAFKA_NODE_ID": "1", "KAFKA_PROCESS_ROLES": "broker,controller",
                "KAFKA_LISTENERS": "PLAINTEXT://:9092,CONTROLLER://:9093",
                "KAFKA_ADVERTISED_LISTENERS": "PLAINTEXT://cdcparity-kafka:9092",
                "KAFKA_CONTROLLER_LISTENER_NAMES": "CONTROLLER",
                "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP": "CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT",
                "KAFKA_CONTROLLER_QUORUM_VOTERS": "1@cdcparity-kafka:9093",
                "KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR": "1", "KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR": "1",
                "KAFKA_TRANSACTION_STATE_LOG_MIN_ISR": "1", "KAFKA_GROUP_INITIAL_REBALANCE_DELAY_MS": "0",
                "KAFKA_NUM_PARTITIONS": "1", "KAFKA_AUTO_CREATE_TOPICS_ENABLE": "true",
                # Latency is measured against broker append time, never a
                # timestamp a producer under test chose.
                "KAFKA_LOG_MESSAGE_TIMESTAMP_TYPE": "LogAppendTime",
                "KAFKA_LOG_DIRS": "/var/lib/kafka/data", "KAFKA_HEAP_OPTS": "-Xms4g -Xmx4g",
                "KAFKA_MESSAGE_MAX_BYTES": "4194304"},
            volumes=["cdcparity-kafka:/var/lib/kafka/data"],
            healthcheck={"test": ["CMD-SHELL", "/opt/kafka/bin/kafka-broker-api-versions.sh --bootstrap-server localhost:9092 >/dev/null 2>&1"],
                         "interval": "2s", "timeout": "10s", "retries": 120})
    if needs_registry(c):
        s["registry"] = svc(
            image=IMG["registry"], container_name="cdcparity-registry",
            environment={"QUARKUS_HTTP_PORT": "8080", "APICURIO_CCOMPAT_LEGACY_ID_MODE_ENABLED": "true"},
            # Ready = the registry API answers (Apicurio 3.3.3 answered 404 on
            # /health/ready and never became healthy on :9000/q/health/ready).
            healthcheck={"test": ["CMD-SHELL", "curl -fs http://localhost:8080/apis/registry/v2/system/info >/dev/null || exit 1"],
                         "interval": "2s", "timeout": "5s", "retries": 120})
    if "s3" in c.dests:
        s["minio"] = svc(
            image=IMG["minio"], container_name="cdcparity-minio",
            environment={"MINIO_ROOT_USER": "conduitminio", "MINIO_ROOT_PASSWORD": "conduitminiosecret",
                         "MINIO_KMS_SECRET_KEY": "my-minio-key:OSMM+vkKUTCvQs9YL/CVMIMt43HFhkTiqZC7QkYYkYI="},
            command="server /data", volumes=["cdcparity-minio:/data"],
            healthcheck={"test": ["CMD", "curl", "-f", "http://localhost:9000/minio/health/live"],
                         "interval": "1s", "timeout": "5s", "retries": 60})
    if "http" in c.dests:
        s["receiver"] = svc(
            image=IMG["alpine"], container_name="cdcparity-receiver",
            command=["/loader/loader", "receive", "-addr", ":8090", "-dump", "/sink/http.jsonl",
                     "-delay", f"{c.sink_delay_ms}ms"],
            volumes=["${LOADER_BIN}:/loader/loader:ro", "cdcparity-sink:/sink"])
    deps = {}
    if c.source == "pg":
        deps = {"postgres": {"condition": "service_healthy"}}
    elif c.source == "kafka":
        deps = {"kafka": {"condition": "service_healthy"}}
    s["loader"] = svc(image=IMG["alpine"], container_name="cdcparity-loader", command=loader_command(c),
                      volumes=["${LOADER_BIN}:/loader/loader:ro", "cdcparity-sink:/sink"], depends_on=deps)
    sample = ["/loader/loader", "sample", "-out", "/out/samples.jsonl", "-interval", "${SAMPLE_INTERVAL:-5s}",
              "-sinks", ",".join(sink_specs(c, arm=arm)), "-tables", str(dest_tables(c))]
    if c.source != "pg" or c.mode in ("snapshot", "recover-snapshot"):
        sample += ["-progress", ""]
    else:
        sample += ["-slots"]
    s["sampler"] = svc(image=IMG["alpine"], container_name="cdcparity-sampler", command=sample,
                       volumes=["${LOADER_BIN}:/loader/loader:ro", "cdcparity-sink:/sink", "${RUN_DIR}:/out"])
    s["counter"] = svc(image=IMG["alpine"], container_name="cdcparity-counter", command=["sleep", "infinity"],
                       volumes=["${LOADER_BIN}:/loader/loader:ro", "cdcparity-sink:/sink", "./:/cell:ro", "${RUN_DIR}:/out"])
    vols = {"cdcparity-sink": {"external": True}}
    if needs_pg(c):
        vols["cdcparity-pg"] = {"external": True}
    if needs_broker(c):
        vols["cdcparity-kafka"] = {"external": True}
    if "s3" in c.dests:
        vols["cdcparity-minio"] = {"external": True}
    return {"services": s, "volumes": vols, "networks": {"benchi": {"external": True}}}


def init_sql(c: Cell, arm: str = "") -> str:
    noseq = parse_arm(arm)[3] == "noseq" if arm else False
    wide = WIDE_DDL if c.shape == "wide" else ""
    cols = "id bigint PRIMARY KEY, payload text NOT NULL" + (", " + wide if wide else "")
    lines = ["-- GENERATED by matrix/cells.py.",
             "CREATE EXTENSION IF NOT EXISTS pg_stat_statements;"]
    for i in range(max(n_tables(c), 1) if c.source == "pg" else 0):
        lines.append(f"CREATE TABLE bench_source_{i} ({cols});")
    if "pg" in c.dests:
        lines += ["-- Destinations: same shape, plus seq (the source op index, decoded from the",
                  "-- payload marker, indexed) so progress is one index probe, and landed_at,",
                  "-- set by a trigger in the ramp cells only (latency).",
                  "CREATE FUNCTION bench_landed() RETURNS trigger LANGUAGE plpgsql AS $$",
                  "BEGIN NEW.landed_at := clock_timestamp(); RETURN NEW; END $$;"]
        for i in range(dest_tables(c)):
            if noseq:
                lines.append(f"CREATE TABLE bench_dest_{i} ({cols}, landed_at timestamptz);")
                continue
            lines.append(f"CREATE TABLE bench_dest_{i} ({cols}, landed_at timestamptz, "
                         f"seq bigint GENERATED ALWAYS AS ((substring(payload from '^S[0-9]+[.]([0-9]+)[|]'))::bigint) STORED);")
            lines.append(f"CREATE INDEX bench_dest_{i}_seq ON bench_dest_{i} (seq);")
            if c.mode == "ramp":
                lines.append(f"CREATE TRIGGER landed BEFORE INSERT OR UPDATE ON bench_dest_{i} FOR EACH ROW EXECUTE FUNCTION bench_landed();")
    return "\n".join(lines) + "\n"


# The wide shape's 28 typed columns (loader/write.go wideCols, same order).
def _wide_ddl():
    types = ["int", "bigint", "numeric", "text", "varchar", "timestamptz", "date", "bool", "jsonb", "uuid", "float8", "int"]
    sql = {"int": "integer", "bigint": "bigint", "numeric": "numeric(14,2)", "text": "text", "varchar": "varchar(64)",
           "timestamptz": "timestamptz", "date": "date", "bool": "boolean", "jsonb": "jsonb", "uuid": "uuid",
           "float8": "double precision"}
    return ", ".join(f"c{i:02d}_{types[i % len(types)]} {sql[types[i % len(types)]]}" for i in range(28))


WIDE_DDL = _wide_ddl()


# ---------------------------------------------------------------- Conduit
def conduit_source_batch(tier: str) -> dict:
    if tier != "t":
        return {}
    return {"sdk.batch.size": str(TUNED["batch_records"]), "sdk.batch.delay": f"{TUNED['batch_wait_ms']}ms"}


def conduit_pipelines(c: Cell, arm: str) -> list:
    side, tier, _, _ = parse_arm(arm)
    slots = slot_names(c, arm)
    pipes = []
    if c.source == "pg":
        groups = [[t] for t in tables_list(c)] if multi(c, arm) else [tables_list(c)]
    elif c.source == "kafka":
        groups = [None] * (c.par if tier == "t" else 1)
    else:
        groups = [None]
    for i, tbls in enumerate(groups):
        conns, procs = [], []
        if c.source == "pg":
            snap = c.mode in ("snapshot", "recover-snapshot")
            src = {"url": PG_URL, "tables": ",".join(tbls), "cdcMode": "logrepl",
                   "snapshotMode": "initial" if snap else "never",
                   "logrepl.slotName": slots[i], "logrepl.publicationName": slots[i].replace("_slot", "_pub"),
                   "logrepl.autoCleanup": "false"}
            src.update(conduit_source_batch(tier))
            conns.append({"id": f"src{i}", "type": "source", "plugin": "builtin:postgres", "settings": src})
        elif c.source == "kafka":
            # Tuned: one pipeline per partition in one consumer group, the
            # Conduit equivalent of tasks.max = partitions. The group is per
            # RUN (@RUN@), so every run drains the same backlog from the start.
            src = {"servers": BROKER, "topics": "bench.source", "groupID": "conduit-@RUN@", "readFromBeginning": "true"}
            src.update(conduit_source_batch(tier))
            conns.append({"id": f"src{i}", "type": "source", "plugin": "builtin:kafka", "settings": src})
            if "pg" in c.dests:
                procs.append({"id": "unwrap", "plugin": "unwrap.kafkaconnect", "settings": {"field": ".Payload.After"}})
        else:
            src = {"path": "/sink/in_0.txt"}
            src.update(conduit_source_batch(tier))
            conns.append({"id": "src0", "type": "source", "plugin": "builtin:file", "settings": src})
        if c.transform:
            procs.append({"id": "rename", "plugin": "field.rename", "condition": '{{ ne (printf "%v" .Operation) "delete" }}',
                          "settings": {"mapping": ".Payload.After.payload:body"}})
        if side == "conduit-avro":
            # EQUAL AVRO CONTENT (PLAN.md section 2): the row plus Debezium's
            # __op field (c, u, d, r), as ExtractNewRecordState with
            # add.fields=op writes it; deletes become an empty value, as the
            # Debezium arms' tombstones. One subject per table
            # ("<topic>-value"), like the Apicurio converter's default
            # TopicIdStrategy: the subject is a static setting, so a
            # multi-table pipeline gets one conditional encoder per table.
            procs.append({"id": "op", "plugin": "field.set", "condition": '{{ ne (printf "%v" .Operation) "delete" }}',
                          "settings": {"field": ".Payload.After.__op", "value": OP_TEMPLATE}})
            for t in (tbls if c.source == "pg" and tbls else ["bench_source_0"]):
                cond = '{{ and (ne (printf "%v" .Operation) "delete") (eq (index .Metadata "opencdc.collection") "' + t + '") }}'
                procs.append({"id": f"avro-{t}", "plugin": "avro.encode", "condition": cond,
                              "settings": {"field": ".Payload.After", "schema.strategy": "autoRegister",
                                           "schema.autoRegister.subject": f"cdcparity.public.{t}-value"}})
        for d in c.dests:
            conns.append(conduit_dest(c, d, i, tier, side, single=len(groups) == 1))
        p = {"id": f"p{i}", "status": "running", "connectors": conns}
        if procs:
            p["processors"] = procs
        pipes.append(p)
    return pipes


# OpenCDC operation -> Debezium's op code.
# .Operation is a typed enum: templates compare it as text (printf "%v"),
# a direct comparison with a string fails at runtime. No template variables:
# Conduit expands $name in pipeline files as an environment variable, and its
# hardening refuses names outside the allowlist (shakedown, 2026-10-28).
_OP = '(printf "%v" .Operation)'
OP_TEMPLATE = ('{{ if eq ' + _OP + ' "create" }}c{{ else if eq ' + _OP + ' "update" }}u'
               '{{ else if eq ' + _OP + ' "delete" }}d{{ else }}r{{ end }}')


def conduit_dest(c: Cell, d: str, i: int, tier: str, side: str, single: bool) -> dict:
    raw = c.source != "pg"
    if d == "file":
        s = {"path": f"/sink/out_{i}.txt"}
        if raw:
            s.update({"sdk.record.format": "template", "sdk.record.format.options": '{{ printf "%s" .Payload.After }}'})
        return {"id": f"file{i}", "type": "destination", "plugin": "builtin:file", "settings": s}
    if d == "pg":
        if c.source != "pg":
            table = "bench_dest_0"
        elif single and n_tables(c) > 1:
            table = '{{ index .Metadata "opencdc.collection" | replace "bench_source_" "bench_dest_" }}'
        else:
            table = f"bench_dest_{i}" if c.mode not in ("snapshot", "recover-snapshot") else "bench_dest_0"
        url = PG_URL
        if tier == "t":
            # pgx's documented exec mode. Pre-registered openly: the v0.21
            # results found it matters for this destination (results.md).
            url += "&default_query_exec_mode=cache_statement"
        return {"id": f"pg{i}", "type": "destination", "plugin": "builtin:postgres", "settings": {"url": url, "table": table}}
    if d == "s3":
        s = {"aws.accessKeyId": "conduitminio", "aws.secretAccessKey": "conduitminiosecret", "aws.region": "us-east-1",
             "aws.bucket": "bench", "aws.endpoint": "http://cdcparity-minio:9000", "aws.pathStyle": "true",
             "format": "bytearray" if raw else "json"}
        if tier == "t":
            s.update({"sdk.batch.size": str(TUNED["object_records"]), "sdk.batch.delay": f"{TUNED['object_wait_ms']}ms"})
        return {"id": f"s3{i}", "type": "destination", "plugin": "builtin:s3", "settings": s}
    if d == "kafka":
        if c.source != "pg":
            topic = "cdcparity.out"
        elif single:
            topic = 'cdcparity.public.{{ index .Metadata "opencdc.collection" }}'
        else:
            topic = f"cdcparity.public.bench_source_{i}"
        s = {"servers": BROKER, "topic": topic, "acks": "all"}
        if raw:
            s.update({"sdk.record.format": "template", "sdk.record.format.options": '{{ printf "%s" .Payload.After }}'})
        if side == "conduit-avro":
            s.update({"sdk.record.format": "template",
                      "sdk.record.format.options": '{{ if .Payload.After }}{{ printf "%s" .Payload.After }}{{ end }}',
                      # The payload is already Avro: the destination must not
                      # decode it again. avro.encode leaves the source's
                      # schema metadata on the record, so the SDK's default
                      # extraction decodes with the wrong schema and fails
                      # every record (a Conduit defect, found by the shakedown).
                      "sdk.schema.extract.payload.enabled": "false"})
        elif c.transform:
            s.update({"sdk.record.format": "template", "sdk.record.format.options": "{{ toJson .Payload.After }}"})
        if tier == "t":
            s["compression"] = TUNED["compression"]
        return {"id": f"kafka{i}", "type": "destination", "plugin": "builtin:kafka", "settings": s}
    if d == "http":
        return {"id": f"http{i}", "type": "destination", "plugin": "standalone:http",
                "settings": {"url": RECEIVER + "/", "method": "POST", "validateConnection": "false"}}
    raise ValueError(d)


def conduit_service(c: Cell, arm: str) -> dict:
    side = arm_side(arm)
    args = ["/app/conduit", "run", "--pipelines.path", "/app/pipelines"]
    if side != "conduit-classic":
        args.append("--preview.pipeline-arch-v2")
    if side == "conduit-avro":
        args += ["--schema-registry.type", "confluent",
                 "--schema-registry.confluent.connection-string", REGISTRY + "/apis/ccompat/v7"]
    env = [
        # The image binds the API to 0.0.0.0 and refuses to start without a
        # token (GHSA-v79j-j88p-m4ff). Measured runs: loopback, nothing calls
        # it. Diag runs override (run-cell.sh) to scrape metrics with a token.
        "CONDUIT_API_HTTP_ADDRESS=${CONDUIT_API_ADDR:-127.0.0.1:8080}",
        "CONDUIT_API_GRPC_ADDRESS=127.0.0.1:8084",
        "CONDUIT_API_TOKEN_FILE=${CONDUIT_TOKEN_FILE:-}",
        "CONDUIT_CONNECTORS_FILE_ALLOWED_DIRS=/sink",
        # GC trace on stderr, every run, every Go side (observation only; the
        # JVM sides log GC to a file in every run too).
        "GODEBUG=gctrace=1",
    ]
    if "http" in c.dests:
        env.append("CONDUIT_CONNECTORS_PATH=/app/connectors")
    image = IMG["conduit-http"] if "http" in c.dests else "${CONDUIT_IMAGE:-" + IMG["conduit"] + "}"
    return {
        "image": image, "container_name": SUT, "pull_policy": "never",
        "cpuset": "${SUT_CPUSET}", "mem_limit": SUT_MEM, "memswap_limit": SUT_MEM, "logging": LOGGING,
        "command": ["sh", "-c", "exec " + " ".join(args)], "stop_grace_period": "60s",
        "environment": env,
        "volumes": ["../pipelines:/app/pipelines:ro", "cdcparity-sink:/sink", "${PROF_DIR}:/prof"],
        "networks": ["benchi"],
    }


# ---------------------------------------------------------------- Debezium / Kafka Connect
def apicurio(prefix: str, key: bool) -> dict:
    p = prefix
    conv = {f"{p}converter": "io.apicurio.registry.utils.converter.AvroConverter",
            f"{p}converter.apicurio.registry.url": REGISTRY + "/apis/registry/v2",
            f"{p}converter.apicurio.registry.auto-register": "true",
            f"{p}converter.apicurio.registry.find-latest": "true"}
    return conv


def converters(arm: str, dest: str, role: str) -> dict:
    """Connector converters. role: 'source' (Debezium) or 'sink'."""
    ser = serialization(arm)
    js = lambda p, schemas: {f"{p}converter": "org.apache.kafka.connect.json.JsonConverter",
                             f"{p}converter.schemas.enable": "true" if schemas else "false"}
    if ser == "avro":
        if dest == "pg":
            return {**apicurio("key.", True), **apicurio("value.", False)}
        return {**js("key.", False), **apicurio("value.", False)}
    if dest == "pg":
        return {**js("key.", True), **js("value.", True)}  # the JDBC sinks need the key schema
    return {**js("key.", False), **js("value.", False)}


def debezium_source(c: Cell, tier: str, slot: str, tables: list, arm: str = "") -> dict:
    snap = c.mode in ("snapshot", "recover-snapshot")
    s = {
        "connector.class": "io.debezium.connector.postgresql.PostgresConnector",
        "database.hostname": "cdcparity-postgres", "database.port": "5432",
        "database.user": "bench", "database.password": "bench", "database.dbname": "bench",
        "topic.prefix": "cdcparity", "table.include.list": ",".join(f"public.{t}" for t in tables),
        "plugin.name": "pgoutput", "slot.name": slot, "publication.name": slot.replace("_slot", "_pub"),
        "publication.autocreate.mode": "filtered",
        # Debezium 3.7 removed "never"; "no_data" streams changes with no
        # snapshot (Connect rejected "never" in the shakedown).
        "snapshot.mode": "initial" if snap else "no_data",
        "tombstones.on.delete": "false",
    }
    if tier == "t":
        # https://debezium.io/documentation/reference/3.7/connectors/postgresql.html
        # documents the knobs and max.queue.size > max.batch.size; it gives no
        # throughput values, so the values are OURS (TUNED).
        s.update({"max.batch.size": str(TUNED["batch_records"]), "max.queue.size": str(TUNED["batch_records"] * 4),
                  "poll.interval.ms": str(TUNED["batch_wait_ms"]), "snapshot.fetch.size": str(TUNED["snapshot_fetch_rows"])})
    return s


def unwrap_rename(c: Cell, pg_dest: bool, avro_kafka: bool = False) -> dict:
    """pg_dest: the JDBC sinks need the flattened row. avro_kafka: the
    EQUAL-CONTENT Avro definition (PLAN.md section 2): the flattened row plus
    __op, deletes as tombstones, the same content Conduit's Avro arm writes."""
    t = {}
    names = []
    if pg_dest or c.transform or avro_kafka:
        names.append("unwrap")
        t.update({"transforms.unwrap.type": "io.debezium.transforms.ExtractNewRecordState",
                  "transforms.unwrap.delete.tombstone.handling.mode": "tombstone"})
        if avro_kafka:
            t["transforms.unwrap.add.fields"] = "op"
    if c.transform:
        names.append("rename")
        t.update({"transforms.rename.type": "org.apache.kafka.connect.transforms.ReplaceField$Value",
                  "transforms.rename.renames": "payload:body"})
    if names:
        t["transforms"] = ",".join(names)
    return t


def connect_service(c: Cell, arm: str) -> dict:
    tier = arm_tier(arm)
    env = {
        "BOOTSTRAP_SERVERS": BROKER,
        # Per-run group and storage topics (@RUN@): a drain cell keeps its
        # broker volume across runs, and no run may see another's connectors.
        "GROUP_ID": "cdcparity-connect-@RUN@",
        "CONFIG_STORAGE_TOPIC": "cdcparity-connect-config-@RUN@",
        "OFFSET_STORAGE_TOPIC": "cdcparity-connect-offsets-@RUN@",
        "STATUS_STORAGE_TOPIC": "cdcparity-connect-status-@RUN@",
        "CONFIG_STORAGE_REPLICATION_FACTOR": "1", "OFFSET_STORAGE_REPLICATION_FACTOR": "1",
        "STATUS_STORAGE_REPLICATION_FACTOR": "1",
        "KEY_CONVERTER": "org.apache.kafka.connect.json.JsonConverter",
        "VALUE_CONVERTER": "org.apache.kafka.connect.json.JsonConverter",
        "ENABLE_APICURIO_CONVERTERS": "true",
        "CONNECT_PRODUCER_ACKS": "all",
        "KAFKA_OPTS": "-Xlog:gc*:file=/prof/gc.log:time,uptime,level,tags ${JVM_DIAG_OPTS:-}",
    }
    if tier == "t":
        # https://docs.confluent.io/platform/current/kafka/deployment.html#jvm (Xms = Xmx)
        env["HEAP_OPTS"] = f"-Xms{TUNED['heap']} -Xmx{TUNED['heap']}"
        # https://docs.confluent.io/cloud/current/best-practices/optimizing/throughput.html
        env.update({"CONNECT_PRODUCER_BATCH_SIZE": str(TUNED["producer_batch_bytes"]),
                    "CONNECT_PRODUCER_LINGER_MS": str(TUNED["producer_linger_ms"]),
                    "CONNECT_PRODUCER_COMPRESSION_TYPE": TUNED["compression"],
                    "CONNECT_CONSUMER_MAX_POLL_RECORDS": str(TUNED["batch_records"]),
                    "CONNECT_CONSUMER_FETCH_MIN_BYTES": str(TUNED["consumer_fetch_min_bytes"]),
                    "CONNECT_CONSUMER_MAX_PARTITION_FETCH_BYTES": str(TUNED["consumer_partition_fetch_bytes"])})
    if tier == "e":
        # Recovery tier: position flush every 1 s, like Conduit's persister
        # (pkg/connector/persister.go: 1 s or 10,000 records), and no
        # scheduled-rebalance hold when the only worker comes back.
        env.update({"CONNECT_OFFSET_FLUSH_INTERVAL_MS": "1000", "CONNECT_SCHEDULED_REBALANCE_MAX_DELAY_MS": "0"})
    return {
        "image": IMG["connect"], "container_name": SUT, "pull_policy": "never",
        "cpuset": "${SUT_CPUSET}", "mem_limit": SUT_MEM, "memswap_limit": SUT_MEM, "logging": LOGGING,
        "environment": env,
        "volumes": ["cdcparity-sink:/sink", "${PROF_DIR}:/prof", "${ASPROF_DIR}:/opt/async-profiler:ro",
                    "${JOLOKIA_JAR}:/opt/jolokia.jar:ro"],
        "networks": ["benchi"],
    }


def connect_connectors(c: Cell, arm: str) -> list:
    side, tier, _, variant = parse_arm(arm)
    out = []
    slots = slot_names(c, arm)
    string_conv = {"key.converter": "org.apache.kafka.connect.storage.StringConverter",
                   "value.converter": "org.apache.kafka.connect.storage.StringConverter"}
    bytes_conv = {"key.converter": "org.apache.kafka.connect.converters.ByteArrayConverter",
                  "value.converter": "org.apache.kafka.connect.converters.ByteArrayConverter"}
    if c.source == "pg":
        dest = c.dests[0] if c.dests[0] in ("pg", "kafka") else "kafka"
        groups = [[t] for t in tables_list(c)] if multi(c, arm) else [tables_list(c)]
        for i, tbls in enumerate(groups):
            src = debezium_source(c, tier, slots[i], tbls, arm)
            src.update(converters(arm, "pg" if "pg" in c.dests else "kafka", "source"))
            src.update(unwrap_rename(c, "pg" in c.dests, avro_kafka=serialization(arm) == "avro" and "kafka" in c.dests))
            out.append({"name": f"src-{i}-@RUN@", "config": src})
            topics = (f"cdcparity.public.{tbls[0]}" if len(tbls) == 1 else None)
            for d in c.dests:
                if d == "kafka":
                    continue  # the source IS the producer
                sk = connect_sink(c, d, tier, variant, topics, i, arm)
                out.append({"name": f"{d}-sink-{i}-@RUN@", "config": sk})
    else:
        topic = "bench.source" if c.source == "kafka" else "cdcparity.out"
        tasks = str(c.par) if (c.source == "kafka" and tier == "t") else "1"
        if c.source == "file":
            src = {"connector.class": "org.apache.kafka.connect.file.FileStreamSourceConnector", "file": "/sink/in_0.txt",
                   "topic": topic, "tasks.max": "1", **string_conv}
            if tier == "t":
                src["batch.size"] = str(TUNED["batch_records"])  # FileStreamSource's batch.size (default 2000)
            out.append({"name": "file-src-@RUN@", "config": src})
        for d in c.dests:
            if d == "kafka":
                continue
            sk = connect_sink(c, d, tier, variant, topic, 0, arm)
            sk["tasks.max"] = tasks
            sk["consumer.override.auto.offset.reset"] = "earliest"
            if d == "pg":
                sk.update({"key.converter": "org.apache.kafka.connect.json.JsonConverter", "key.converter.schemas.enable": "true",
                           "value.converter": "org.apache.kafka.connect.json.JsonConverter", "value.converter.schemas.enable": "true"})
            elif d == "s3":
                sk.update(bytes_conv)
            else:
                sk.update(string_conv)
            out.append({"name": f"{d}-sink-@RUN@", "config": sk})
    return out


def connect_sink(c: Cell, d: str, tier: str, variant: str, topic, i: int, arm: str) -> dict:
    """topic None: one sink over every table's topic (topics.regex)."""
    sel = {"topics": topic} if topic else {"topics.regex": r"cdcparity\.public\.bench_source_[0-9]+"}
    if d == "file":
        # The source wrote JSON bytes; StringConverter passes them through, so
        # the line is that JSON (a JSON converter here would print a Java Map).
        return {"connector.class": "org.apache.kafka.connect.file.FileStreamSinkConnector", **sel,
                "file": f"/sink/out_{i}.txt", "tasks.max": "1",
                "key.converter": "org.apache.kafka.connect.storage.StringConverter",
                "value.converter": "org.apache.kafka.connect.storage.StringConverter"}
    if d == "pg":
        s = {"connector.class": "io.confluent.connect.jdbc.JdbcSinkConnector", **sel, "tasks.max": "1",
             "connection.url": "jdbc:postgresql://cdcparity-postgres:5432/bench", "connection.user": "bench",
             "connection.password": "bench", "insert.mode": "upsert", "pk.mode": "record_key", "pk.fields": "id",
             "delete.enabled": "true", "auto.create": "false", "auto.evolve": "false"}
        if c.source == "pg":
            # cdcparity.public.bench_source_i -> bench_dest_i, whatever the topic set.
            s.update({"transforms": "route", "transforms.route.type": "org.apache.kafka.connect.transforms.RegexRouter",
                      "transforms.route.regex": r"cdcparity\.public\.bench_source_(.*)",
                      "transforms.route.replacement": "bench_dest_$1", "table.name.format": "${topic}"})
            s.update(converters(arm, "pg", "sink"))
        else:
            s["table.name.format"] = "bench_dest_0"
        if tier == "t":
            # https://docs.confluent.io/kafka-connectors/jdbc/current/sink-connector/sink_config_options.html
            # batch.size; reWriteBatchedInserts is pgjdbc's documented batching
            # flag. Pre-registered fallback: if it fails on duplicate keys in a
            # batch (ON CONFLICT DO UPDATE affecting a row twice), the smoke
            # run records it and the tuned arm drops it, in the change log.
            s["batch.size"] = str(TUNED["sink_batch_rows"])
            s["connection.url"] += "?reWriteBatchedInserts=true"
        return s
    if d == "s3":
        flush = {"f1k": "1000", "f10k": "10000", "f100k": "100000"}.get(variant, str(TUNED["object_records"]))
        s = {"connector.class": "io.confluent.connect.s3.S3SinkConnector", **sel, "tasks.max": "1",
             "format.class": ("io.confluent.connect.s3.format.json.JsonFormat" if c.source == "pg"
                              else "io.confluent.connect.s3.format.bytearray.ByteArrayFormat"),
             # flush.size is required (no default): the shipped tier runs 1k,
             # 10k and 100k (@f1k/@f10k/@f100k). rotate.interval.ms only tuned.
             "flush.size": flush,
             "storage.class": "io.confluent.connect.s3.storage.S3Storage",
             "partitioner.class": "io.confluent.connect.storage.partitioner.DefaultPartitioner",
             "s3.bucket.name": "bench", "s3.region": "us-east-1", "store.url": "http://cdcparity-minio:9000",
             "path.style.access.enabled": "true", "aws.access.key.id": "conduitminio",
             "aws.secret.access.key": "conduitminiosecret", "schema.compatibility": "NONE"}
        if tier == "t":
            s["rotate.interval.ms"] = str(TUNED["object_wait_ms"])
        if c.source == "pg":
            s.update(converters(arm, "s3", "sink"))
        return s
    raise ValueError(d)


# ---------------------------------------------------------------- Debezium Server
def dbzserver_props(c: Cell, arm: str, tables=None, slot=None, offsets="data/offsets.dat") -> str:
    side, tier, _, _ = parse_arm(arm)
    tables = tables or (tables_list(c) if c.mode not in ("snapshot", "recover-snapshot") else ["bench_source_0"])
    slot = slot or slot_names(c, arm)[0]
    eff_tier = "t" if tier == "t" else "s"
    p = {f"debezium.source.{k}": v for k, v in debezium_source(c, eff_tier, slot, tables, arm).items()}
    p.update({
        "debezium.source.offset.storage": "org.apache.kafka.connect.storage.FileOffsetBackingStore",
        "debezium.source.offset.storage.file.filename": offsets,
        "quarkus.log.level": "INFO",
    })
    if tier == "e":
        p["debezium.source.offset.flush.interval.ms"] = "1000"
    d = c.dests[0]
    avro = serialization(arm) == "avro"
    if avro:
        reg = {"apicurio.registry.url": REGISTRY + "/apis/registry/v2", "apicurio.registry.auto-register": "true",
               "apicurio.registry.find-latest": "true"}
        p["debezium.format.value"] = "avro"
        p.update({f"debezium.format.value.{k}": v for k, v in reg.items()})
        if d == "pg":
            p["debezium.format.key"] = "avro"
            p.update({f"debezium.format.key.{k}": v for k, v in reg.items()})
        else:
            p.update({"debezium.format.key": "json", "debezium.format.key.schemas.enable": "false"})
    else:
        schemas = "true" if d == "pg" else "false"  # the JDBC sink needs schema'd records (review B2)
        p.update({"debezium.format.key": "json", "debezium.format.value": "json",
                  "debezium.format.key.schemas.enable": schemas, "debezium.format.value.schemas.enable": schemas})
    if d == "kafka":
        p.update({"debezium.sink.type": "kafka", "debezium.sink.kafka.producer.bootstrap.servers": BROKER,
                  "debezium.sink.kafka.producer.key.serializer": "org.apache.kafka.common.serialization." + ("StringSerializer"),
                  "debezium.sink.kafka.producer.value.serializer": "org.apache.kafka.common.serialization." +
                  ("ByteArraySerializer" if avro else "StringSerializer"),
                  "debezium.sink.kafka.producer.acks": "all"})
        if tier == "t":
            p.update({"debezium.sink.kafka.producer.batch.size": str(TUNED["producer_batch_bytes"]),
                      "debezium.sink.kafka.producer.linger.ms": str(TUNED["producer_linger_ms"]),
                      "debezium.sink.kafka.producer.compression.type": TUNED["compression"]})
    elif d == "pg":
        p.update({"debezium.sink.type": "jdbc",
                  "debezium.sink.jdbc.connection.url": "jdbc:postgresql://cdcparity-postgres:5432/bench",
                  "debezium.sink.jdbc.connection.username": "bench", "debezium.sink.jdbc.connection.password": "bench",
                  "debezium.sink.jdbc.insert.mode": "upsert", "debezium.sink.jdbc.primary.key.mode": "record_key",
                  "debezium.sink.jdbc.delete.enabled": "true", "debezium.sink.jdbc.schema.evolution": "none"})
        # One process captures every table, so the table comes from the topic:
        # RegexRouter renames cdcparity.public.bench_source_i to bench_dest_i,
        # the same routing step the Connect JDBC sink runs.
        p.update({"debezium.transforms": "route",
                  "debezium.transforms.route.type": "org.apache.kafka.connect.transforms.RegexRouter",
                  "debezium.transforms.route.regex": "cdcparity\\\\.public\\\\.bench_source_(.*)",
                  "debezium.transforms.route.replacement": "bench_dest_$1"})
        # \${...}: Quarkus config expands ${...} itself; the JDBC sink needs the
        # literal ${topic} (shakedown: "collection.name.format is required").
        p["debezium.sink.jdbc.collection.name.format"] = r"\\${topic}"
        if tier == "t":
            # Debezium JDBC sink: batch.size (default 500), use.reduction.buffer
            # (collapses changes to one key within a batch). Both documented.
            p.update({"debezium.sink.jdbc.batch.size": str(TUNED["sink_batch_rows"]),
                      "debezium.sink.jdbc.use.reduction.buffer": "true"})
    elif d == "http":
        p.update({"debezium.sink.type": "http", "debezium.sink.http.url": RECEIVER + "/"})
    if d == "kafka" and (avro or c.transform):
        # Equal-content Avro (flattened row + __op, deletes as tombstones), and
        # the transform cell's flatten + rename.
        names = ["unwrap"] + (["rename"] if c.transform else [])
        p.update({"debezium.transforms": ",".join(names),
                  "debezium.transforms.unwrap.type": "io.debezium.transforms.ExtractNewRecordState",
                  "debezium.transforms.unwrap.delete.tombstone.handling.mode": "tombstone"})
        if avro:
            p["debezium.transforms.unwrap.add.fields"] = "op"
        if c.transform:
            p.update({"debezium.transforms.rename.type": "org.apache.kafka.connect.transforms.ReplaceField$Value",
                      "debezium.transforms.rename.renames": "payload:body"})
    return "".join(f"{k}={v}\n" for k, v in p.items())


def dbzserver_service(c: Cell, arm: str) -> dict:
    side, tier, _, _ = parse_arm(arm)
    opts = "-Xlog:gc*:file=/prof/gc${DBZ_PROC:-}.log:time,uptime,level,tags"
    if tier == "t":
        opts = f"-Xms{TUNED['heap']} -Xmx{TUNED['heap']} " + opts
    img = IMG["dbzserver-native"] if side == "dbzserver-native" else IMG["dbzserver"]
    s = {
        "image": img, "container_name": SUT,
        "cpuset": "${SUT_CPUSET}", "mem_limit": SUT_MEM, "memswap_limit": SUT_MEM, "logging": LOGGING,
        # Only application.properties: the image's config directory holds its
        # own files (debezium-jmx-config.yaml), which a directory mount hides.
        "volumes": ["../dbzserver/application.properties:/debezium/config/application.properties:ro",
                    "${PROF_DIR}:/prof", "${ASPROF_DIR}:/opt/async-profiler:ro",
                    "${JOLOKIA_JAR}:/opt/jolokia.jar:ro"],
        "networks": ["benchi"],
    }
    if side != "dbzserver-native":
        s["environment"] = {"JAVA_OPTS": opts + " ${JVM_DIAG_OPTS:-}"}
        s["pull_policy"] = "never"  # built locally (images/dbzserver.Dockerfile)
    if side == "dbzserver-avro-xn":
        # One Debezium Server process per table, all in this one container
        # (same cpuset and memory limit as every other side). Heap per process:
        # the JVM default share of the container limit divided by N. Each
        # process gets its own working copy, config and offsets file.
        n = n_tables(c)
        s["volumes"][0] = "../dbzserver:/cfg:ro"
        # $$: compose interpolates $name in the file; the shell gets $.
        script = ("for i in $(seq 0 %d); do d=/tmp/dbz$i; mkdir -p $d && cp -r /debezium/. $d/ && "
                  "cp /cfg/p$i/application.properties $d/config/application.properties && "
                  "(cd $d && JAVA_OPTS=\"-Xmx%dm $JAVA_OPTS\" DBZ_PROC=$i ./run.sh > /prof/dbz$i.log 2>&1 &); done; wait"
                  % (n - 1, max(96, 1536 // n)))
        s["entrypoint"] = ["sh", "-c", script.replace("$", "$$")]
    return s


# ---------------------------------------------------------------- benchi config
# run-cell.sh writes the arm being run to /out/arm before each benchi run.
REGISTER_SH = r"""set -e
url=http://cdcparity-sut:8083
n=0
until wget -q -O /dev/null "$url/connectors" 2>/dev/null; do
  n=$((n+1)); [ "$n" -ge 300 ] && { echo "connect REST API never came up"; exit 1; }; sleep 1
done
for f in $(ls /cell/connect/*.json | sort); do
  /loader/loader post -url "$url/connectors" -file "$f"
done
/loader/loader now CONNECT_REGISTERED_MS
"""


def verify_args(c: Cell) -> str:
    if c.source == "pg":
        total = CORRECT_OPS if c.mode == "correct" else (RECOVER_OPS if c.mode == "recover" else 0)
        window = f" -window {SNAP_ROWS} -tables 1" if c.mode == "recover-snapshot" else f" -tables {n_tables(c)}"
        return (f"{window} -payload {c.payload} -total {total} -insert-frac {c.insert_frac} -update-frac {c.update_frac}"
                + (f" -skew {c.skew}" if c.skew else ""))
    return f" -tables 1 -window 0 -insert-frac 1 -update-frac 0 -batch 1 -seed 1 -payload {c.payload} -total {backlog_records(c)}"


def verify_cmd(c: Cell, spec: str) -> str:
    ordered = "false" if spec.startswith("s3:") or (c.source == "kafka" and c.par > 1) else "true"
    name = spec.split(":")[0]
    return f"/loader/loader verify -sink '{spec}'{verify_args(c)} -ordered={ordered} -out /out/verify-{name}.json > /dev/null"


def collectors(c: Cell) -> list:
    L = "/loader/loader collect"
    out = []
    if "kafka" in c.dests and c.mode in ("ramp", "capacity", "idle"):
        out.append({"name": "kafka-hist", "container": "cdcparity-counter",
                    "run": f"{L} kafka-hist -topics '{topic_pattern(c)}' -out /out/kafka-hist.jsonl"})
    if needs_broker(c) and c.mode in ("ramp", "capacity", "drain", "idle"):
        # What each producer actually sent: batches, records per batch, codec.
        out.append({"name": "broker-batches", "container": "cdcparity-kafka", "run":
                    "for f in $(ls -d /var/lib/kafka/data/cdcparity.* 2>/dev/null); do "
                    "/opt/kafka/bin/kafka-dump-log.sh --files $(ls $f/*.log | head -1) 2>/dev/null | head -20000; done | "
                    "awk '/baseOffset/{n++; for(i=1;i<=NF;i++){if($i==\"count:\")r+=$(i+1); if($i==\"compresscodec:\")k[$(i+1)]++; "
                    "if($i==\"size:\")b+=$(i+1)}} END{printf \"BROKER_BATCH_COUNT=%d\\nBROKER_RECORD_COUNT=%d\\nBROKER_BYTES_COUNT=%d\\n\", n, r, b; "
                    "for(x in k) printf \"BROKER_CODEC_%s_COUNT=%d\\n\", toupper(x), k[x]}'"})
        # Equal content is checked on the wire, not assumed: the first records
        # of one topic with their key, value and header sizes and header keys.
        out.append({"name": "broker-sample", "container": "cdcparity-kafka", "run":
                    "f=$(ls -d /var/lib/kafka/data/cdcparity.* 2>/dev/null | head -1); "
                    "[ -n \"$f\" ] && /opt/kafka/bin/kafka-dump-log.sh --files $(ls $f/*.log | head -1) "
                    "--deep-iteration --print-data-log 2>/dev/null | head -40 | sed 's/^/BROKER_SAMPLE /' | cut -c1-2000; true"})
    if "s3" in c.dests and c.mode in ("ramp", "capacity", "drain"):
        out.append({"name": "s3-index", "container": "cdcparity-counter", "run": f"{L} s3-index -bucket bench -out /out/s3-index.jsonl"})
    if "pg" in c.dests and c.mode == "ramp":
        out.append({"name": "pg-hist", "container": "cdcparity-counter",
                    "run": f"{L} pg-hist -tables bench_dest_%d -n {dest_tables(c)} -out /out/pg-hist.jsonl"})
    if "http" in c.dests and c.mode in ("ramp", "capacity"):
        out.append({"name": "http-hist", "container": "cdcparity-counter", "run": f"{L} http-hist -out /out/http-hist.json"})
    if needs_pg(c):
        out.append({"name": "pgstat", "container": "cdcparity-counter", "run": f"{L} pgstat -out /out/pgstat.json"})
    return out


def hooks(c: Cell, arm: str = "") -> dict:
    steps = {"post-infrastructure": [], "pre-tool": [], "post-tool": [], "pre-test": [], "post-test": []}
    L = "/loader/loader"
    if "s3" in c.dests:
        steps["post-infrastructure"].append({"name": "mkbucket", "container": "cdcparity-counter", "run": f"{L} mkbucket -bucket bench"})
    if c.source in ("kafka", "file") or c.mode in ("snapshot", "recover-snapshot"):
        steps["post-infrastructure"].append({"name": "wait-fill", "container": "cdcparity-counter",
                                             "run": f"{L} wait-file -path /sink/.fill_done -timeout 60m"})
    steps["pre-tool"].append({"name": "tool-start", "container": "cdcparity-counter", "run": f"{L} now TOOL_START_MS"})
    connect_arms = [a for a in c.arms if arm_side(a).startswith("connect")]
    if connect_arms:
        steps["post-tool"].append({"name": "register-connect", "container": "cdcparity-counter", "tools": connect_arms,
                                   "run": REGISTER_SH})
    specs = ",".join(sink_specs(c, arm=arm))
    tbl = dest_tables(c)
    steps["pre-test"].append({"name": "first-record", "container": "cdcparity-counter",
                              "run": f"{L} first -sinks '{specs}' -tables {tbl} -timeout {'5m' if SMOKE else '15m'}"})
    if c.mode == "snapshot":
        steps["pre-test"].append({"name": "snapshot-landed", "container": "cdcparity-counter",
                                  "run": f"{L} first -sinks '{specs}' -tables {tbl} -until {SNAP_ROWS} -timeout {SNAP_TIMEOUT}s"})
    if c.mode == "correct":
        prog = " -progress http://cdcparity-loader:9100/progress" if c.source == "pg" else ""
        steps["pre-test"].append({"name": "drained", "container": "cdcparity-counter",
                                  "run": f"{L} first -sinks '{specs}' -tables {tbl} -settle -stable 60s -timeout 45m{prog}"})
        for spec in sink_specs(c, "verify"):
            steps["pre-test"].append({"name": "verify-" + spec.split(":")[0], "container": "cdcparity-counter",
                                      "run": verify_cmd(c, spec)})
    steps["post-test"] += collectors(c)
    steps["post-test"].append({"name": "end", "container": "cdcparity-counter", "run": f"{L} now END_MS"})
    return {k: v for k, v in steps.items() if v}


def bench_yml(c: Cell, arm: str) -> dict:
    return {
        "infrastructure": {"infra": {"compose": "./infra.yml"}},
        "tools": {arm: {"compose": "./tool.yml"}},
        "metrics": {"docker": {"collector": "docker", "settings": {"containers": [SUT]}}},
        "tests": [{"name": c.id, "duration": f"{run_seconds(c)}s", "steps": hooks(c, arm)}],
    }


def gen_arm(c: Cell, arm: str, out: str) -> None:
    """One arm's runnable directory: bench.yml, infra.yml, tool.yml, init.sql
    and the arm's own pipeline / connector / server configs. run-cell.sh copies
    it per run and replaces @RUN@ with the run id."""
    side = arm_side(arm)
    dump(os.path.join(out, "bench.yml"), bench_yml(c, arm))
    dump(os.path.join(out, "infra.yml"), infra_compose(c, arm))
    write(os.path.join(out, "init.sql"), init_sql(c, arm))
    if side.startswith("conduit"):
        svc_ = conduit_service(c, arm)
        dump(os.path.join(out, "pipelines", "pipelines.yml"), {"version": "2.2", "pipelines": conduit_pipelines(c, arm)})
    elif side.startswith("connect"):
        svc_ = connect_service(c, arm)
        for k, conn in enumerate(connect_connectors(c, arm)):
            dump(os.path.join(out, "connect", f"{k:02d}.json"), conn)
    else:
        svc_ = dbzserver_service(c, arm)
        if side == "dbzserver-avro-xn":
            for i, t in enumerate(tables_list(c)):
                write(os.path.join(out, "dbzserver", f"p{i}", "application.properties"),
                      dbzserver_props(c, arm, tables=[t], slot=slot_names(c, arm)[i]))
        else:
            write(os.path.join(out, "dbzserver", "application.properties"), dbzserver_props(c, arm))
    dump(os.path.join(out, "tools", "tool.yml"), {"services": {"sut": svc_}, "volumes": {"cdcparity-sink": {"external": True}},
                                                 "networks": {"benchi": {"external": True}}})
    # bench.yml names ./tool.yml; compose resolves relative paths from the
    # file's directory, so the tool file lives in tools/ next to its configs.
    b = bench_yml(c, arm)
    b["tools"][arm]["compose"] = "./tools/tool.yml"
    dump(os.path.join(out, "bench.yml"), b)


def gen(c: Cell, out: str) -> None:
    dump(os.path.join(out, "cell.json"), cell_meta(c))
    for a in c.arms:
        gen_arm(c, a, os.path.join(out, "arms", a))


def cell_meta(c: Cell) -> dict:
    return {
        "id": c.id, "family": c.family, "source": c.source, "dests": list(c.dests), "mode": c.mode,
        "par": c.par, "payload": c.payload, "rounds": c.rounds, "diag": c.diag, "instances": c.instances,
        "diag_arms": list(c.diag_arms or (c.arms if c.diag else ())),
        "plan": c.plan, "reference": c.reference, "plugin": c.plugin, "transform": c.transform,
        "workload": {"txn": c.txn, "insert_frac": c.insert_frac, "update_frac": c.update_frac, "shape": c.shape,
                     "skew": c.skew, "alter_at": c.alter_at, "sink_delay_ms": c.sink_delay_ms},
        "arms": list(c.arms),
        "order": {r: round_order(c, r) for r in range(1, c.rounds + 1)},
        "slots": {a: slot_names(c, a) for a in c.arms},
        "jvm": {a: is_jvm(a) for a in c.arms},
        "sample_interval": {a: ("30s" if parse_arm(a)[3] == "i30" else "5s") for a in c.arms},
        "sinks": sink_specs(c), "verify_sinks": sink_specs(c, "verify"), "verify_args": verify_args(c),
        "dest_tables": dest_tables(c),
        "needs_broker": {a: (arm_side(a).startswith("connect") or c.source == "kafka" or "kafka" in c.dests) for a in c.arms},
        "run_seconds": run_seconds(c),
        "keep_broker_volume": c.mode == "drain",  # fill once per cell
        "keep_input_file": c.source == "file",
        "ramp": {"steps": RAMP_STEPS, "warm_s": RAMP_WARM, "step_s": RAMP_STEP, "settle_s": RAMP_SETTLE} if c.mode == "ramp" else None,
        "window": {"warm_s": CAP_WARM, "window_s": CAP_WINDOW} if c.mode in ("capacity", "drain", "idle") else None,
        "recover": {"ops": RECOVER_OPS, "rate": RECOVER_RATE, "kill_fraction": RECOVER_KILL_FRACTION,
                    "snapshot_rows": SNAP_ROWS} if c.mode.startswith("recover") else None,
        "snapshot_rows": SNAP_ROWS if c.mode in ("snapshot", "recover-snapshot") else None,
        "backlog_records": backlog_records(c),
        "primary": [(a, b) for cid, a, b in PRIMARY if cid == c.id],
        "note": c.note,
        "harness_hash": harness_hash(),
    }


# ---------------------------------------------------------------- planning
def shard(k: int, plan: str = "all") -> list:
    """Longest-processing-time-first assignment of cells to k instances. A
    cell with instances = n is placed n times, on n DIFFERENT instances. The
    shakedown runs on one instance in SHAKEDOWN_CELLS order (least proven
    paths first)."""
    if plan == "shakedown" and k == 1:
        cs = plan_cells(plan)
        return [(sum(cell_minutes(c) for c in cs), [(cid, 0) for cid in SHAKEDOWN_CELLS])]
    units = []
    for c in plan_cells(plan):
        for rep in range(c.instances):
            units.append((cell_minutes(c), c.id, rep))
    units.sort(key=lambda u: -u[0])
    loads = [0.0] * k
    plan_ = [[] for _ in range(k)]
    for m, cid, rep in units:
        order = sorted(range(k), key=lambda i: loads[i])
        # A repeat goes to an instance that does not hold the cell yet; with
        # fewer instances than repeats it doubles up (and the cross-instance
        # rule then has fewer instances to compare: analyze.py says so).
        free = [i for i in order if not any(x[0] == cid for x in plan_[i])]
        i = (free or order)[0]
        plan_[i].append((cid, rep))
        loads[i] += m
    return [(loads[i], plan_[i]) for i in range(k)]


def estimate(price: float, plan: str) -> str:
    sel = plan_cells(plan)
    runs = sum(len(c.arms) * runs_per_arm(c) * c.instances for c in sel)
    total = sum(cell_minutes(c) * c.instances for c in sel)
    lines = [f"plan {plan}: cells {len(sel)}, arm-cells {sum(len(c.arms) for c in sel)}, runs {runs}, "
             f"run time {total/60:.1f} h", "",
             "| instances | wall time (longest shard) | instance-hours | cost at ${:.3f}/h |".format(price),
             "| --- | --- | --- | --- |"]
    for k in (1, 2, 4, 8):
        sh = shard(k, plan)
        wall = max(l for l, _ in sh) / 60 + SETUP_MIN / 60
        ih = sum(l for l, _ in sh) / 60 + k * SETUP_MIN / 60
        lines.append(f"| {k} | {wall:.1f} h | {ih:.1f} | ${ih*price:,.0f} |")
    return "\n".join(lines)


HASHED = (".py", ".sh", ".tf", ".tftpl", ".Dockerfile", ".go", ".mod", ".sum")


def harness_files() -> list:
    roots = [HERE, os.path.join(HERE, "..", "loader")]
    files = []
    for r in roots:
        for d, _, fs in os.walk(r):
            if "__pycache__" in d or ".terraform" in d or "terraform.tfstate" in d:
                continue
            for f in fs:
                # ._* are macOS AppleDouble metadata files, never code.
                if f.endswith(HASHED) and not f.startswith(("test_", "._")):
                    files.append(os.path.normpath(os.path.join(d, f)))
    return sorted(files)


def harness_hash() -> str:
    """SHA-256 over the code the results depend on: matrix/ (not its .md
    files) and loader/. Recorded in every run's environment and cell.json;
    analyze.py refuses results whose hash differs from its own tree's."""
    h = hashlib.sha256()
    base = os.path.normpath(os.path.join(HERE, ".."))
    for p in harness_files():
        h.update(os.path.relpath(p, base).encode() + b"\0")
        with open(p, "rb") as fh:
            h.update(fh.read())
    return h.hexdigest()[:16]


def table(plan: str) -> str:
    rows = ["| cell | plan | mode | source -> dest | shape / workload | arms | rounds | instances | diag | label |",
            "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |"]
    for c in plan_cells(plan):
        w = []
        if c.mode in ("snapshot", "recover-snapshot"):
            w.append(f"{SNAP_ROWS:,} rows")
        else:
            w.append(f"{c.par} {'tables' if c.source == 'pg' else 'partitions'}")
        w.append(f"{c.payload} B")
        if c.shape != "narrow":
            w.append(c.shape)
        if c.txn:
            w.append(f"txn {c.txn}")
        if (c.insert_frac, c.update_frac) != (0.70, 0.20):
            w.append(f"mix {int(c.insert_frac*100)}/{int(c.update_frac*100)}/{int(round((1-c.insert_frac-c.update_frac)*100))}")
        if c.skew:
            w.append(f"zipf {c.skew}")
        if c.alter_at:
            w.append(f"ALTER at {c.alter_at}s")
        if c.sink_delay_ms:
            w.append(f"sink delay {c.sink_delay_ms} ms")
        if c.backlog_bytes:
            w.append(f"backlog {c.backlog_bytes // 2**30} GiB ({backlog_records(c):,} records)")
        label = "reference" if c.reference else ("plugin" if c.plugin else ("primary" if primary_block(c) else ""))
        rows.append(f"| {c.id} | {c.plan} | {c.mode} | {c.source} -> {'+'.join(c.dests)}{' (transform)' if c.transform else ''} | "
                    f"{', '.join(w)} | {', '.join(c.arms)} | {c.rounds} | {c.instances} | {'yes' if c.diag else '-'} | {label} |")
    return "\n".join(rows)


def main(argv: list) -> int:
    if len(argv) < 2:
        print(__doc__)
        return 2
    cmd = argv[1]
    plan = argv[argv.index("--plan") + 1] if "--plan" in argv else "all"
    if cmd == "list":
        for c in plan_cells(plan):
            print(f"{c.id:30s} {c.plan:8s} {c.mode:16s} {len(c.arms):2d} arms: {','.join(c.arms)}")
        return 0
    if cmd == "table":
        print(table(plan))
        return 0
    if cmd == "gen":
        gen(BY_ID[argv[2]], argv[3])
        return 0
    if cmd == "order":
        print(" ".join(round_order(BY_ID[argv[2]], int(argv[3]))))
        return 0
    if cmd == "estimate":
        price = float(argv[argv.index("--price") + 1]) if "--price" in argv else 0.848
        print(estimate(price, plan))
        return 0
    if cmd == "shard":
        k, i = int(argv[2]), int(argv[3])
        for cid, rep in shard(k, plan)[i][1]:
            print(f"{cid} {rep}")
        return 0
    if cmd == "hash":
        if "--list" in argv:
            base = os.path.normpath(os.path.join(HERE, ".."))
            for p in harness_files():
                with open(p, "rb") as fh:
                    print(hashlib.sha256(fh.read()).hexdigest()[:12], os.path.relpath(p, base))
        print(harness_hash())
        return 0
    if cmd == "primary":
        for p in PRIMARY:
            print(" | ".join(p))
        return 0
    print(__doc__)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))

#!/usr/bin/env python3
"""The parity matrix: every pre-registered cell, and the generator that turns a
cell into a runnable benchi config.

    cells.py list [--json]             every cell and its arms
    cells.py table                     the PLAN.md cell table (Markdown)
    cells.py gen CELL OUTDIR           write the cell's bench.yml, compose files,
                                       pipelines and connector configs
    cells.py estimate [--price P] [--phase 1|2|1,2]
                                       run count, instance-hours and dollars at
                                       2, 4 and 8 parallel instances
    cells.py shard K I [--phase ...]   the cells instance I of K runs (balanced)

Stdlib only: it runs on the bench instance's stock python3. Every YAML file it
writes is JSON, which is valid YAML, so there is no YAML dependency either.

A CELL is one comparison: one source, one or two destinations, one shape
(parallelism, payload), one load mode. An ARM is one side at one tier in that
cell, for example conduit-s (Conduit, shipped defaults) or connect-t (Kafka
Connect, tuned). Arms of a cell are only ever compared with each other, within
the same session on the same instance, interleaved; that is what makes cells
independent and lets them run on different instances in parallel.

The cell list below IS the pre-registration (PLAN.md is generated from it and
committed before any data exists). Changing it after results exist must be
recorded in PLAN.md's change log with the reason.
"""
from __future__ import annotations

import json
import math
import os
import sys
from dataclasses import dataclass, field, replace
from typing import Optional

# ---------------------------------------------------------------- pinned images
# Digests resolved 2026-10-27. The quay.io Debezium tags are rebuilt daily on a
# refreshed base image, so the digest, not the tag, is the pin.
IMG = {
    "postgres": "postgres:17@sha256:b8faeff7712663c2773a190181e1246e37e364b887611c04658c28b44e2408fe",
    "kafka": "apache/kafka:4.0.0@sha256:3f7b939115cd4872e9cee9369d80bd69712fde55f9902f46d793f64848dedc75",
    "alpine": "alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc",
    "minio": "pgsty/minio:RELEASE.2026-08-04T00-00-00Z@sha256:b6bfe7239bfc83fb90d31612d9704d86039dd714f7904b3f1ad68f211e602372",
    # Built by run-shard.sh: FROM quay.io/debezium/connect:3.7.0.Final@sha256:8dc50bab...
    # plus the file connectors, the Confluent JDBC and S3 sinks (images/connect.Dockerfile).
    "connect": "matrix-connect:3.7.0",
    "dbzserver": "quay.io/debezium/server:3.7.0.Final@sha256:9117f3fac0f0ebcc76b364b81d4b914243c65836bc1391a41d23a81d28a15a65",
    "dbzserver-native": "quay.io/debezium/server-native:3.7.0.Final@sha256:6654a571bbb28a120f3d5f4fd8140620c730231f995d2b46e3d0570e4bd9de17",
    # Built by run-shard.sh from the repository Dockerfile at CONDUIT_SHA, and
    # for the HTTP cells with the conduitio-labs http connector added
    # (images/conduit-http.Dockerfile).
    "conduit": "matrix-conduit:bench",
    "conduit-http": "matrix-conduit:bench-http",
}

PG_URL = "postgres://bench:bench@cdcparity-postgres:5432/bench?sslmode=disable"
BROKER = "cdcparity-kafka:9092"
RECEIVER = "http://cdcparity-receiver:8090"

# Every system under test runs in ONE container named cdcparity-sut, on the
# SUT cpuset with the same memory limit; everything else (Postgres, broker,
# MinIO, loader, sampler, receiver, counter) runs on the INFRA cpuset. The
# cpusets are computed per instance by run-shard.sh from the CPU topology.
SUT_MEM = "6g"
SUT = "cdcparity-sut"

# ---------------------------------------------------------------- sides, tiers
SIDES = {
    "conduit": "Conduit (arch-v2 engine, --preview.pipeline-arch-v2)",
    "conduit-classic": "Conduit (classic engine), continuity with v0.21 results",
    "connect": "Debezium 3.7 / Confluent sinks on Apache Kafka Connect 4.x (one distributed worker)",
    "dbzserver": "Debezium Server 3.7.0.Final, JVM",
    "dbzserver-native": "Debezium Server 3.7.0.Final, native (Mandrel) build",
}
TIERS = {"s": "shipped defaults", "t": "tuned per the vendor's documented knobs"}

# Load modes and their run lengths (seconds). The windows are what the
# analysis measures; everything before a window is excluded from it.
RAMP_STEPS = [20000, 40000, 50000, 70000, 100000, 140000, 200000]
RAMP_WARM = 180     # held at the first step, excluded: JIT and GC reach steady state
RAMP_STEP = 120     # per step: 60 s to settle, then a 60 s tracking window
RAMP_SETTLE = 60
CAP_WARM = 180      # capacity/drain: warm-up excluded from the window
CAP_WINDOW = 300    # >= 5 min measured at saturation (JVM steady state)
SNAP_TIMEOUT = 1800
CORRECT_OPS = 3_000_000
RECOVER_OPS = 3_000_000
RECOVER_KILL_AFTER = 120  # seconds after the first record

ROUNDS = 5

# Smoke mode (MATRIX_SMOKE=1, launch.sh --smoke): one short run of one cell per
# family, every side, so a wiring failure costs minutes, not a matrix. Smoke
# numbers are never results.
SMOKE = os.environ.get("MATRIX_SMOKE") == "1"
SMOKE_CELLS = ["A2-pg-file-p1-b64", "A2-pg-pg-p1-b64", "A2-pg-s3-p1-b64", "A2-pg-kafka-p1-b64", "A2-pg-http-p1-b64",
               "A1-pg-kafka-ramp", "B-kafka-pg-p1-b64", "B-kafka-s3-p1-b64", "B-kafka-file-p1-b64", "C-file-kafka-b64",
               "D-pg-kafka-transform", "E-pg-fanout-file-s3", "F-pg-kafka-snapshot", "G-pg-kafka-kill9",
               "H-pg-pg-verify", "H-pg-kafka-verify"]
if SMOKE:
    RAMP_STEPS = [20000, 50000]
    RAMP_WARM, RAMP_STEP, RAMP_SETTLE = 30, 60, 30
    CAP_WARM, CAP_WINDOW = 30, 60
    CORRECT_OPS = RECOVER_OPS = 300_000
    RECOVER_KILL_AFTER = 20


@dataclass(frozen=True)
class Cell:
    id: str
    family: str
    source: str               # pg | kafka | file
    dests: tuple              # ("kafka",) or ("file", "s3") for fan-out
    mode: str                 # ramp | capacity | drain | snapshot | correct | recover
    par: int = 1              # tables (pg source) or partitions (kafka source)
    payload: int = 64
    sides: tuple = ("conduit", "connect")
    tiers: tuple = ("s", "t")
    aa: bool = False          # add conduit-s-aa, the A/A twin of conduit-s
    classic: bool = False     # add conduit-classic-s (continuity)
    transform: bool = False   # unwrap + rename (family D)
    rounds: int = ROUNDS
    diag: bool = False        # one extra unmeasured profiling run per arm
    repeat: bool = False      # repeat the whole cell on a second instance
    snapshot_rows: int = 0
    backlog: int = 0          # drain cells: records pre-filled
    phase: int = 1            # 1 = core; 2 = shape sweeps, run only on a budget decision taken before phase 1 data
    note: str = ""

    def arms(self) -> list:
        out = []
        for side in self.sides:
            for tier in self.tiers:
                if side == "dbzserver-native" and tier == "t":
                    continue  # the native build has no JVM to tune; same knobs as dbzserver-t would apply
                out.append(f"{side}-{tier}")
        if self.aa:
            out.append("conduit-s-aa")
        if self.classic:
            out.append("conduit-classic-s")
        return out


def arm_side(arm: str) -> str:
    base = arm[:-3] if arm.endswith("-aa") else arm
    return base.rsplit("-", 1)[0]


def arm_tier(arm: str) -> str:
    base = arm[:-3] if arm.endswith("-aa") else arm
    return base.rsplit("-", 1)[1]


# ---------------------------------------------------------------- the matrix
def build_cells() -> list:
    cells = []
    sides_for = {
        "file": ("conduit", "connect"),
        "pg": ("conduit", "connect", "dbzserver"),
        "s3": ("conduit", "connect"),
        "kafka": ("conduit", "connect", "dbzserver"),
        # Apache Kafka has no HTTP sink; Confluent's is commercial-licensed.
        "http": ("conduit", "dbzserver", "dbzserver-native"),
    }
    # A: Postgres CDC -> each destination.
    for d in ("file", "pg", "s3", "kafka", "http"):
        sd = sides_for[d]
        # A1: offered-rate ramp, base shape. Where each side stops tracking,
        # and latency at every step it still tracks.
        # Shipped tier only: the tuned tier's capacity is measured in A2.
        cells.append(Cell(f"A1-pg-{d}-ramp", "A", "pg", (d,), "ramp", sides=sd, tiers=("s",),
                          aa=True, classic=d != "http"))
        # A2: capacity (offered load above every side's capacity), shapes.
        shapes = [(1, 64), (4, 64), (16, 64), (1, 1024), (1, 16384)]
        if d == "http":
            shapes = [(1, 64), (4, 64)]
        for par, pl in shapes:
            base = (par, pl) == (1, 64)
            cells.append(Cell(f"A2-pg-{d}-p{par}-b{pl}", "A", "pg", (d,), "capacity", par=par, payload=pl,
                              sides=sd, aa=base, classic=base and d != "http", diag=base,
                              repeat=base and d in ("pg", "kafka"), phase=1 if base else 2))
    # B: Kafka topic (pre-filled backlog) -> sink. Kafka Connect's core use case.
    for d in ("pg", "s3", "file"):
        shapes = [(1, 64), (4, 64), (16, 64), (1, 1024), (1, 16384)]
        if d == "file":
            # FileStreamSink writes one file from every task; more than one
            # task interleaves partial lines. Single-partition shapes only.
            shapes = [(1, 64), (1, 1024), (1, 16384)]
        for par, pl in shapes:
            base = (par, pl) == (1, 64)
            cells.append(Cell(f"B-kafka-{d}-p{par}-b{pl}", "B", "kafka", (d,), "drain", par=par, payload=pl,
                              aa=base, diag=base, backlog=backlog_for(pl), phase=1 if base else 2))
    # C: file (pre-filled) -> Kafka. FileStreamSource is single-task.
    for pl in (64, 1024, 16384):
        cells.append(Cell(f"C-file-kafka-b{pl}", "C", "file", ("kafka",), "drain", payload=pl,
                          aa=pl == 64, diag=pl == 64, backlog=backlog_for(pl), phase=1 if pl == 64 else 2))
    # D: transform. Postgres CDC -> Kafka, flattened (Debezium unwrap) with
    # one field renamed, vs Conduit's builtin processors.
    cells.append(Cell("D-pg-kafka-transform", "D", "pg", ("kafka",), "capacity", transform=True,
                      sides=("conduit", "connect", "dbzserver"), aa=True, diag=True))
    # E: fan-out. One source, two destinations.
    cells.append(Cell("E-pg-fanout-file-s3", "E", "pg", ("file", "s3"), "capacity", aa=True, diag=True,
                      note="Conduit: one pipeline, two destinations. Connect: one Debezium source, one topic, two sink connectors."))
    # F: initial snapshot, time to land every row.
    for d in ("kafka", "pg"):
        cells.append(Cell(f"F-pg-{d}-snapshot", "F", "pg", (d,), "snapshot", sides=("conduit", "connect", "dbzserver"),
                          aa=True, snapshot_rows=10_000_000))
    # G: recovery. kill -9 the system under test at saturation, restart it.
    for d in ("kafka", "pg"):
        cells.append(Cell(f"G-pg-{d}-kill9", "G", "pg", (d,), "recover", sides=("conduit", "connect", "dbzserver"),
                          tiers=("s",)))
    # H: correctness under saturation, every destination, both tiers.
    for d in ("file", "pg", "s3", "kafka", "http"):
        cells.append(Cell(f"H-pg-{d}-verify", "H", "pg", (d,), "correct", sides=sides_for[d], rounds=1))
    for d in ("pg", "s3", "file"):
        cells.append(Cell(f"H-kafka-{d}-verify", "H", "kafka", (d,), "correct", rounds=1, backlog=CORRECT_OPS))
    cells.append(Cell("H-file-kafka-verify", "H", "file", ("kafka",), "correct", rounds=1, backlog=CORRECT_OPS))
    cells.append(Cell("H-pg-kafka-transform-verify", "H", "pg", ("kafka",), "correct", transform=True,
                      sides=("conduit", "connect", "dbzserver"), rounds=1))
    cells.append(Cell("H-pg-fanout-file-s3-verify", "H", "pg", ("file", "s3"), "correct", rounds=1))
    return cells


def backlog_for(payload: int) -> int:
    # Sized by bytes, so every drain cell has a backlog of about 6-10 GB: far
    # more than any side drains in warm-up plus window at the rates seen so
    # far. analyze.py flags a run whose backlog ran out inside the window.
    return {64: 50_000_000, 1024: 8_000_000, 16384: 600_000}[payload]


def smoke_cells(cs: list) -> list:
    out = []
    for c in cs:
        if c.id not in SMOKE_CELLS:
            continue
        out.append(replace(c, rounds=1, repeat=False, diag=c.id == "A2-pg-kafka-p1-b64", phase=1,
                           backlog=min(c.backlog, 2_000_000) if c.backlog else 0,
                           snapshot_rows=500_000 if c.snapshot_rows else 0))
    return out


CELLS = smoke_cells(build_cells()) if SMOKE else build_cells()
BY_ID = {c.id: c for c in CELLS}


# ---------------------------------------------------------------- run lengths
def run_seconds(c: Cell) -> int:
    """Benchi test duration (after the pre-test hooks return)."""
    if c.mode == "ramp":
        return RAMP_WARM + RAMP_STEP * len(RAMP_STEPS)
    if c.mode in ("capacity", "drain"):
        return CAP_WARM + CAP_WINDOW
    return 10  # snapshot/correct wait in their pre-test hook; recover is not a benchi run


def est_minutes(c: Cell, arm: str) -> float:
    """Wall minutes for ONE run of one arm, for the estimate. Startup and
    teardown are about 2.5 min (compose up, JVM start, registration, compose
    down); fills and snapshot loads are estimated from rates seen in the v0.21
    runs and marked as estimates in PLAN.md."""
    overhead = 2.5
    if c.mode == "ramp":
        return overhead + run_seconds(c) / 60
    if c.mode == "capacity":
        return overhead + run_seconds(c) / 60
    if c.mode == "drain":
        fill = {64: 2.0, 1024: 2.0, 16384: 2.5}[c.payload]
        return overhead + fill + run_seconds(c) / 60
    if c.mode == "snapshot":
        return overhead + 2.0 + 6.0
    if c.mode == "correct":
        return overhead + 8.0
    if c.mode == "recover":
        return overhead + 9.0
    raise ValueError(c.mode)


def cell_minutes(c: Cell) -> float:
    arms = c.arms()
    rounds = c.rounds + (1 if c.mode in ("ramp", "capacity", "drain", "snapshot") else 0)  # + warmup round
    m = sum(est_minutes(c, a) for a in arms) * rounds
    if c.diag:
        m += sum(est_minutes(c, a) for a in arms)
    return m


def run_count(c: Cell) -> int:
    rounds = c.rounds + (1 if c.mode in ("ramp", "capacity", "drain", "snapshot") else 0)
    return len(c.arms()) * (rounds + (1 if c.diag else 0))


# ---------------------------------------------------------------- generation
def dump(path: str, obj) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        json.dump(obj, f, indent=2, sort_keys=False)
        f.write("\n")


def write(path: str, text: str) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        f.write(text)


def topic_pattern(c: Cell) -> str:
    if c.source == "pg":
        return r"^cdcparity\.public\.bench_source_[0-9]+$"
    return r"^cdcparity\.out$"


def sink_specs(c: Cell, purpose: str = "count") -> list:
    """Sink specs for the sampler/first (count) and for verify (one per dest)."""
    out = []
    for d in c.dests:
        if d == "file":
            out.append("file:/sink/out_*.txt")
        elif d == "pg":
            out.append("pg:bench_dest_%d")
        elif d == "s3":
            out.append("s3:bench")
        elif d == "kafka":
            out.append("kafka:" + topic_pattern(c))
        elif d == "http":
            out.append("http:" + RECEIVER if purpose == "count" else "file:/sink/http.jsonl")
    return out


def dest_tables(c: Cell) -> int:
    return c.par if c.source == "pg" else 1


def needs_broker(c: Cell) -> bool:
    return c.source == "kafka" or "kafka" in c.dests or "connect" in c.sides


def needs_pg(c: Cell) -> bool:
    return c.source == "pg" or "pg" in c.dests


def slot_names(c: Cell, arm: str) -> list:
    side = arm_side(arm)
    if c.source != "pg":
        return []
    base = arm.replace("-", "_")
    if side == "dbzserver" or side == "dbzserver-native":
        return [f"{base}_slot"]  # one process, one slot, every table
    return [f"{base}_slot_{i}" for i in range(c.par)]


def loader_service(c: Cell) -> dict:
    """The workload container. Its flags are fixed per cell; only LOADER_SLOTS
    (the arm's replication slots) is set per run by run-cell.sh."""
    base = {
        "image": IMG["alpine"],
        "container_name": "cdcparity-loader",
        "cpuset": "${INFRA_CPUSET}",
        "volumes": ["${LOADER_BIN}:/loader/loader:ro", "cdcparity-sink:/sink"],
        "networks": ["benchi"],
    }
    if c.source == "pg":
        cmd = ["/loader/loader", "load", "-url", PG_URL, "-tables", str(c.par), "-payload", str(c.payload),
               "-slots", "${LOADER_SLOTS:-}", "-slot-timeout", "600s", "-conns", str(min(c.par, 4))]
        if c.mode == "ramp":
            sched = [f"{RAMP_STEPS[0]}:{RAMP_WARM}s"] + [f"{r}:{RAMP_STEP}s" for r in RAMP_STEPS]
            cmd += ["-schedule", ",".join(sched)]
        elif c.mode in ("capacity",):
            cmd += ["-rate", "0"]
        elif c.mode == "snapshot":
            # The table is filled BEFORE the pipeline starts (no slot wait);
            # the post-infrastructure hook waits for the done-file.
            cmd = ["/loader/loader", "load", "-url", PG_URL, "-tables", "1", "-payload", str(c.payload),
                   "-window", str(c.snapshot_rows), "-fill-only", "-done-file", "/sink/.fill_done"]
        elif c.mode in ("correct", "recover"):
            cmd += ["-rate", "0", "-total", str(CORRECT_OPS if c.mode == "correct" else RECOVER_OPS)]
        base["command"] = cmd
        base["depends_on"] = {"postgres": {"condition": "service_healthy"}}
    elif c.source == "kafka":
        n = c.backlog
        base["command"] = ["/loader/loader", "fill-kafka", "-brokers", BROKER, "-topic", "bench.source",
                           "-partitions", str(c.par), "-count", str(n), "-payload", str(c.payload),
                           "-done-file", "/sink/.fill_done"]
        base["depends_on"] = {"kafka": {"condition": "service_healthy"}}
    elif c.source == "file":
        base["command"] = ["/loader/loader", "fill-file", "-path", "/sink/in_0.txt", "-count", str(c.backlog),
                           "-payload", str(c.payload), "-done-file", "/sink/.fill_done"]
    return base


def infra_compose(c: Cell) -> dict:
    svcs = {}
    if needs_pg(c):
        svcs["postgres"] = {
            "image": IMG["postgres"],
            "container_name": "cdcparity-postgres",
            "cpuset": "${INFRA_CPUSET}",
            "environment": {"POSTGRES_USER": "bench", "POSTGRES_PASSWORD": "bench", "POSTGRES_DB": "bench"},
            # Workload infrastructure settings, the same for every arm: enough
            # slots for 16 tables x 2 pipelines, and a buffer pool and WAL size
            # that keep checkpoints out of the windows. Not SUT tuning.
            "command": ["postgres", "-c", "wal_level=logical", "-c", "max_replication_slots=64",
                        "-c", "max_wal_senders=64", "-c", "shared_buffers=2GB", "-c", "max_wal_size=16GB",
                        "-c", "checkpoint_timeout=30min"],
            "shm_size": "2g",
            "volumes": ["./init.sql:/docker-entrypoint-initdb.d/init.sql:ro"],
            "tmpfs": ["/var/lib/postgresql/data"],
            "healthcheck": {"test": ["CMD", "pg_isready", "-U", "bench", "-d", "bench"],
                            "interval": "1s", "timeout": "5s", "retries": 120},
            "networks": ["benchi"],
        }
    if needs_broker(c):
        svcs["kafka"] = {
            "image": IMG["kafka"],
            "container_name": "cdcparity-kafka",
            "cpuset": "${INFRA_CPUSET}",
            "environment": {
                "CLUSTER_ID": "cdcparity-kafka-cluster",
                "KAFKA_NODE_ID": "1",
                "KAFKA_PROCESS_ROLES": "broker,controller",
                "KAFKA_LISTENERS": "PLAINTEXT://:9092,CONTROLLER://:9093",
                "KAFKA_ADVERTISED_LISTENERS": "PLAINTEXT://cdcparity-kafka:9092",
                "KAFKA_CONTROLLER_LISTENER_NAMES": "CONTROLLER",
                "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP": "CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT",
                "KAFKA_CONTROLLER_QUORUM_VOTERS": "1@cdcparity-kafka:9093",
                "KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR": "1",
                "KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR": "1",
                "KAFKA_TRANSACTION_STATE_LOG_MIN_ISR": "1",
                "KAFKA_GROUP_INITIAL_REBALANCE_DELAY_MS": "0",
                "KAFKA_NUM_PARTITIONS": "1",
                "KAFKA_AUTO_CREATE_TOPICS_ENABLE": "true",
                # Latency is measured against broker append time, never against
                # a timestamp a producer under test chose.
                "KAFKA_LOG_MESSAGE_TIMESTAMP_TYPE": "LogAppendTime",
                "KAFKA_LOG_DIRS": "/var/lib/kafka/data",
                "KAFKA_HEAP_OPTS": "-Xms2g -Xmx2g",
                "KAFKA_MESSAGE_MAX_BYTES": "4194304",
            },
            "volumes": ["cdcparity-kafka:/var/lib/kafka/data"],
            "healthcheck": {"test": ["CMD-SHELL", "/opt/kafka/bin/kafka-broker-api-versions.sh --bootstrap-server localhost:9092 >/dev/null 2>&1"],
                            "interval": "2s", "timeout": "10s", "retries": 90},
            "networks": ["benchi"],
        }
    if "s3" in c.dests:
        svcs["minio"] = {
            "image": IMG["minio"],
            "container_name": "cdcparity-minio",
            "cpuset": "${INFRA_CPUSET}",
            "environment": {"MINIO_ROOT_USER": "conduitminio", "MINIO_ROOT_PASSWORD": "conduitminiosecret",
                            "MINIO_KMS_SECRET_KEY": "my-minio-key:OSMM+vkKUTCvQs9YL/CVMIMt43HFhkTiqZC7QkYYkYI="},
            "command": "server /data",
            "volumes": ["cdcparity-minio:/data"],
            "healthcheck": {"test": ["CMD", "curl", "-f", "http://localhost:9000/minio/health/live"],
                            "interval": "1s", "timeout": "5s", "retries": 60},
            "networks": ["benchi"],
        }
    if "http" in c.dests:
        svcs["receiver"] = {
            "image": IMG["alpine"],
            "container_name": "cdcparity-receiver",
            "cpuset": "${INFRA_CPUSET}",
            "command": ["/loader/loader", "receive", "-addr", ":8090", "-dump", "/sink/http.jsonl"],
            "volumes": ["${LOADER_BIN}:/loader/loader:ro", "cdcparity-sink:/sink"],
            "networks": ["benchi"],
        }
    svcs["loader"] = loader_service(c)
    sample = ["/loader/loader", "sample", "-out", "/out/samples.jsonl", "-interval", "5s",
              "-sinks", ",".join(sink_specs(c)), "-tables", str(dest_tables(c))]
    if c.source != "pg" or c.mode == "snapshot":
        sample += ["-progress", ""]
    if c.mode == "ramp" and "pg" in c.dests:
        sample += ["-pg-latency"]
    if c.mode not in ("ramp", "capacity"):
        sample += ["-latency-every", "0"]
    svcs["sampler"] = {
        "image": IMG["alpine"],
        "container_name": "cdcparity-sampler",
        "cpuset": "${INFRA_CPUSET}",
        "command": sample,
        "volumes": ["${LOADER_BIN}:/loader/loader:ro", "cdcparity-sink:/sink", "${RUN_DIR}:/out"],
        "networks": ["benchi"],
    }
    svcs["counter"] = {
        "image": IMG["alpine"],
        "container_name": "cdcparity-counter",
        "cpuset": "${INFRA_CPUSET}",
        "command": ["sleep", "infinity"],
        "volumes": ["${LOADER_BIN}:/loader/loader:ro", "cdcparity-sink:/sink", "./:/cell:ro", "${RUN_DIR}:/out"],
        "networks": ["benchi"],
    }
    vols = {"cdcparity-sink": {"external": True}}
    if needs_broker(c):
        vols["cdcparity-kafka"] = {"external": True}
    if "s3" in c.dests:
        vols["cdcparity-minio"] = {"external": True}
    return {"services": svcs, "volumes": vols, "networks": {"benchi": {"external": True}}}


def init_sql(c: Cell) -> str:
    lines = ["-- GENERATED by matrix/cells.py. Source and destination tables, same shape.",
             "-- landed_at is set by a trigger on every destination write: the Postgres",
             "-- sinks' arrival time for latency (cells that measure it). Both sides",
             "-- write through the same trigger, so its cost is the same for both."]
    n = c.par if c.source == "pg" else 0
    for i in range(n if c.mode != "snapshot" else 1):
        lines += [f"CREATE TABLE bench_source_{i} (id bigint PRIMARY KEY, payload text NOT NULL);"]
    if "pg" in c.dests:
        lines += ["CREATE FUNCTION bench_landed() RETURNS trigger LANGUAGE plpgsql AS $$",
                  "BEGIN NEW.landed_at := clock_timestamp(); RETURN NEW; END $$;"]
        for i in range(dest_tables(c)):
            lines += [f"CREATE TABLE bench_dest_{i} (id bigint PRIMARY KEY, payload text NOT NULL, landed_at timestamptz);"]
            if c.mode == "ramp":
                lines += [f"CREATE TRIGGER landed BEFORE INSERT OR UPDATE ON bench_dest_{i} FOR EACH ROW EXECUTE FUNCTION bench_landed();",
                          f"CREATE INDEX bench_dest_{i}_landed ON bench_dest_{i} USING brin (landed_at);"]
    return "\n".join(lines) + "\n"


# ---------------------------------------------------------------- Conduit
def conduit_batch(tier: str) -> dict:
    # Tuned: the SDK's documented source batching (sdk.batch.size /
    # sdk.batch.delay), set to the same size and wait as the tuned Debezium
    # (max.batch.size 8192, poll.interval.ms 100) so neither side batches more.
    return {"sdk.batch.size": "8192", "sdk.batch.delay": "100ms"} if tier == "t" else {}


def conduit_pipelines(c: Cell, arm: str) -> list:
    tier = arm_tier(arm)
    slots = slot_names(c, arm)
    pipes = []
    n_pipes = c.par if c.source == "pg" else (c.par if (c.source == "kafka" and tier == "t") else 1)
    for i in range(n_pipes):
        conns = []
        procs = []
        if c.source == "pg":
            src = {"url": PG_URL, "tables": f"bench_source_{i}", "cdcMode": "logrepl",
                   "snapshotMode": "initial" if c.mode == "snapshot" else "never",
                   "logrepl.slotName": slots[i], "logrepl.publicationName": slots[i].replace("_slot_", "_pub_"),
                   "logrepl.autoCleanup": "false"}
            if c.mode == "snapshot":
                src["tables"] = "bench_source_0"
            src.update(conduit_batch(tier))
            conns.append({"id": f"src{i}", "type": "source", "plugin": "builtin:postgres", "settings": src})
        elif c.source == "kafka":
            # Tuned: one pipeline per partition in one consumer group, the
            # Conduit equivalent of tasks.max = partitions (groupID is the
            # connector's documented consumer-group setting).
            src = {"servers": BROKER, "topics": "bench.source", "groupID": "conduit-bench", "readFromBeginning": "true"}
            src.update(conduit_batch(tier))
            conns.append({"id": f"src{i}", "type": "source", "plugin": "builtin:kafka", "settings": src})
            if "pg" in c.dests:
                # The records are Kafka Connect JSON envelopes; the JDBC sink
                # reads the same envelopes through its JsonConverter.
                procs.append({"id": "unwrap", "plugin": "unwrap.kafkaconnect", "settings": {"field": ".Payload.After"}})
        else:
            src = {"path": "/sink/in_0.txt"}
            src.update(conduit_batch(tier))
            conns.append({"id": "src0", "type": "source", "plugin": "builtin:file", "settings": src})
        if c.transform:
            # Deletes have no after image to rename; Debezium's ReplaceField
            # likewise passes tombstones through untouched.
            procs.append({"id": "rename", "plugin": "field.rename", "condition": '{{ ne .Operation "delete" }}',
                          "settings": {"mapping": ".Payload.After.payload:body"}})
        for d in c.dests:
            conns.append(conduit_dest(c, d, i, tier))
        p = {"id": f"p{i}", "status": "running", "connectors": conns}
        if procs:
            p["processors"] = procs
        pipes.append(p)
    return pipes


def conduit_dest(c: Cell, d: str, i: int, tier: str) -> dict:
    raw = c.source != "pg"  # Kafka/file sources carry opaque bytes
    if d == "file":
        s = {"path": f"/sink/out_{i}.txt"}
        if raw:
            # Write the payload bytes as the line, like FileStreamSink with
            # StringConverter, instead of the OpenCDC JSON (which base64s raw data).
            s.update({"sdk.record.format": "template", "sdk.record.format.options": '{{ printf "%s" .Payload.After }}'})
        return {"id": f"file{i}", "type": "destination", "plugin": "builtin:file", "settings": s}
    if d == "pg":
        table = f"bench_dest_{i}" if c.source == "pg" else "bench_dest_0"
        return {"id": f"pg{i}", "type": "destination", "plugin": "builtin:postgres", "settings": {"url": PG_URL, "table": table}}
    if d == "s3":
        s = {"aws.accessKeyId": "conduitminio", "aws.secretAccessKey": "conduitminiosecret", "aws.region": "us-east-1",
             "aws.bucket": "bench", "aws.endpoint": "http://cdcparity-minio:9000", "aws.pathStyle": "true",
             "format": "bytearray" if raw else "json"}
        if tier == "t":
            # Same object size and wait as the tuned S3 sink (flush.size 10000, rotate 1 s).
            s.update({"sdk.batch.size": "10000", "sdk.batch.delay": "1s"})
        return {"id": f"s3{i}", "type": "destination", "plugin": "builtin:s3", "settings": s}
    if d == "kafka":
        topic = "cdcparity.out" if c.source != "pg" else f"cdcparity.public.bench_source_{i}"
        s = {"servers": BROKER, "topic": topic, "acks": "all"}
        if raw:
            s.update({"sdk.record.format": "template", "sdk.record.format.options": '{{ printf "%s" .Payload.After }}'})
        if c.transform:
            # Emit the flattened row, as Debezium's unwrap does.
            s.update({"sdk.record.format": "template", "sdk.record.format.options": "{{ toJson .Payload.After }}"})
        if tier == "t":
            s.update({"compression": "lz4"})
        return {"id": f"kafka{i}", "type": "destination", "plugin": "builtin:kafka", "settings": s}
    if d == "http":
        return {"id": f"http{i}", "type": "destination", "plugin": "standalone:http",
                "settings": {"url": RECEIVER + "/", "method": "POST", "validateConnection": "false"}}
    raise ValueError(d)


def conduit_service(c: Cell, arm: str) -> dict:
    side = arm_side(arm)
    args = ["/app/conduit", "run", "--pipelines.path", "/app/pipelines"]
    if side == "conduit":
        args.append("--preview.pipeline-arch-v2")
    args.append("${CONDUIT_DIAG_ARGS:-}")
    env = [
        # The image binds the API to 0.0.0.0 and refuses to start without a
        # token (GHSA-v79j-j88p-m4ff); nothing here calls the API from outside,
        # so it listens on the container's loopback only.
        "CONDUIT_API_HTTP_ADDRESS=127.0.0.1:8080",
        "CONDUIT_API_GRPC_ADDRESS=127.0.0.1:8084",
        # The file connector may only touch connectors.file.allowed-dirs.
        "CONDUIT_CONNECTORS_FILE_ALLOWED_DIRS=/sink",
        # GC trace on stderr in every run (observation, not tuning): GC pauses
        # are reported for every side.
        "GODEBUG=gctrace=1",
    ]
    if "http" in c.dests:
        env.append("CONDUIT_CONNECTORS_PATH=/app/connectors")
    return {
        "image": IMG["conduit-http"] if "http" in c.dests else IMG["conduit"],
        "container_name": SUT,
        "pull_policy": "never",
        "cpuset": "${SUT_CPUSET}",
        "mem_limit": SUT_MEM,
        "memswap_limit": SUT_MEM,
        "command": ["sh", "-c", "exec " + " ".join(args)],
        "stop_grace_period": "60s",
        "environment": env,
        # Compose resolves a file's relative paths from the file's directory
        # (tools/), so the generated config is one level up.
        "volumes": [f"../pipelines/{arm}:/app/pipelines:ro", "cdcparity-sink:/sink", "${PROF_DIR}:/prof"],
        "networks": ["benchi"],
    }


# ---------------------------------------------------------------- Kafka Connect
def producer_tuned() -> dict:
    # https://docs.confluent.io/cloud/current/best-practices/optimizing/throughput.html
    # batch.size 100000-200000, linger.ms 10-100, compression lz4. Its acks=1
    # advice is NOT applied: every side runs acks=all (delivery parity).
    return {"CONNECT_PRODUCER_BATCH_SIZE": "200000", "CONNECT_PRODUCER_LINGER_MS": "100",
            "CONNECT_PRODUCER_COMPRESSION_TYPE": "lz4"}


def connect_service(c: Cell, arm: str) -> dict:
    tier = arm_tier(arm)
    env = {
        "BOOTSTRAP_SERVERS": BROKER,
        "GROUP_ID": "cdcparity-connect",
        "CONFIG_STORAGE_TOPIC": "cdcparity-connect-config",
        "OFFSET_STORAGE_TOPIC": "cdcparity-connect-offsets",
        "STATUS_STORAGE_TOPIC": "cdcparity-connect-status",
        "CONFIG_STORAGE_REPLICATION_FACTOR": "1",
        "OFFSET_STORAGE_REPLICATION_FACTOR": "1",
        "STATUS_STORAGE_REPLICATION_FACTOR": "1",
        "KEY_CONVERTER": "org.apache.kafka.connect.json.JsonConverter",
        "VALUE_CONVERTER": "org.apache.kafka.connect.json.JsonConverter",
        "CONNECT_KEY_CONVERTER_SCHEMAS_ENABLE": "false",
        "CONNECT_VALUE_CONVERTER_SCHEMAS_ENABLE": "false",
        # Delivery parity: acks=all is Kafka's default since 3.0; set
        # explicitly so a reader does not have to know that.
        "CONNECT_PRODUCER_ACKS": "all",
        # GC log in every run (observation, not tuning).
        "KAFKA_OPTS": "-Xlog:gc*:file=/prof/gc.log:time,uptime,level,tags ${JVM_DIAG_OPTS:-}",
    }
    if tier == "t":
        # Heap -Xms = -Xmx, half the container limit:
        # https://docs.confluent.io/platform/current/kafka/deployment.html#jvm
        env["HEAP_OPTS"] = "-Xms3g -Xmx3g"
        env.update(producer_tuned())
        # The consumer fetches as many records per poll as the sink batches.
        env["CONNECT_CONSUMER_MAX_POLL_RECORDS"] = "8192"
    return {
        "image": IMG["connect"],
        "container_name": SUT,
        "pull_policy": "never",
        "cpuset": "${SUT_CPUSET}",
        "mem_limit": SUT_MEM,
        "memswap_limit": SUT_MEM,
        "environment": env,
        "volumes": ["cdcparity-sink:/sink", "${PROF_DIR}:/prof", "${ASPROF_DIR}:/opt/async-profiler:ro"],
        "networks": ["benchi"],
    }


def debezium_source(c: Cell, tier: str, slot: str, tables: list, prefix: str = "cdcparity") -> dict:
    s = {
        "connector.class": "io.debezium.connector.postgresql.PostgresConnector",
        "database.hostname": "cdcparity-postgres", "database.port": "5432",
        "database.user": "bench", "database.password": "bench", "database.dbname": "bench",
        "topic.prefix": prefix, "table.include.list": ",".join(f"public.{t}" for t in tables),
        "plugin.name": "pgoutput", "slot.name": slot, "publication.name": slot.replace("_slot", "_pub"),
        "publication.autocreate.mode": "filtered",
        "snapshot.mode": "initial" if c.mode == "snapshot" else "never",
        "tombstones.on.delete": "false",
    }
    if tier == "t":
        # https://debezium.io/documentation/reference/3.7/connectors/postgresql.html
        # (max.batch.size, max.queue.size, poll.interval.ms). Debezium documents
        # the knobs and the constraint max.queue.size > max.batch.size but gives
        # no throughput values; 8192 / 32768 / 100 ms are OUR values, matched by
        # Conduit's tuned sdk.batch.size 8192 / sdk.batch.delay 100ms.
        s.update({"max.batch.size": "8192", "max.queue.size": "32768", "poll.interval.ms": "100"})
    return s


def connect_connectors(c: Cell, arm: str) -> list:
    tier = arm_tier(arm)
    out = []
    json_noschema = {"key.converter": "org.apache.kafka.connect.json.JsonConverter", "key.converter.schemas.enable": "false",
                     "value.converter": "org.apache.kafka.connect.json.JsonConverter", "value.converter.schemas.enable": "false"}
    json_schema = {"key.converter": "org.apache.kafka.connect.json.JsonConverter", "key.converter.schemas.enable": "true",
                   "value.converter": "org.apache.kafka.connect.json.JsonConverter", "value.converter.schemas.enable": "true"}
    string_conv = {"key.converter": "org.apache.kafka.connect.storage.StringConverter",
                   "value.converter": "org.apache.kafka.connect.storage.StringConverter"}
    bytes_conv = {"key.converter": "org.apache.kafka.connect.converters.ByteArrayConverter",
                  "value.converter": "org.apache.kafka.connect.converters.ByteArrayConverter"}
    slots = slot_names(c, arm)
    if c.source == "pg":
        for i in range(c.par):
            t = f"bench_source_{i}" if c.mode != "snapshot" else "bench_source_0"
            src = debezium_source(c, tier, slots[i], [t])
            src.update(json_schema if "pg" in c.dests else json_noschema)
            if "pg" in c.dests or c.transform:
                src.update({"transforms": "unwrap", "transforms.unwrap.type": "io.debezium.transforms.ExtractNewRecordState",
                            "transforms.unwrap.delete.tombstone.handling.mode": "tombstone"})
            if c.transform:
                src["transforms"] = "unwrap,rename"
                src.update({"transforms.rename.type": "org.apache.kafka.connect.transforms.ReplaceField$Value",
                            "transforms.rename.renames": "payload:body"})
            out.append({"name": f"src-{i}", "config": src})
            topic = f"cdcparity.public.{t}"
            for d in c.dests:
                if d == "kafka":
                    continue  # the source IS the producer
                out.append({"name": f"{d}-sink-{i}", "config": connect_sink(c, d, tier, topic, i, json_schema, json_noschema)})
    else:
        if c.source == "kafka":
            topic = "bench.source"
            tasks = str(c.par) if tier == "t" else "1"
        else:
            topic = "cdcparity.out"
            src = {"connector.class": "org.apache.kafka.connect.file.FileStreamSourceConnector", "file": "/sink/in_0.txt",
                   "topic": topic, "tasks.max": "1"}
            src.update(string_conv)
            if tier == "t":
                src["batch.size"] = "8192"  # FileStreamSource's documented batch.size (default 2000)
            out.append({"name": "file-src", "config": src})
            tasks = "1"
        for d in c.dests:
            if d == "kafka":
                continue
            conv = json_schema if d == "pg" else (bytes_conv if d == "s3" else string_conv)
            sk = connect_sink(c, d, tier, topic, 0, conv, conv)
            sk["tasks.max"] = tasks
            sk["consumer.override.auto.offset.reset"] = "earliest"
            out.append({"name": f"{d}-sink", "config": sk})
    return out


def connect_sink(c: Cell, d: str, tier: str, topic: str, i: int, schema_conv: dict, plain_conv: dict) -> dict:
    if d == "file":
        s = {"connector.class": "org.apache.kafka.connect.file.FileStreamSinkConnector", "topics": topic,
             "file": f"/sink/out_{i}.txt", "tasks.max": "1"}
        s.update(plain_conv)
        return s
    if d == "pg":
        table = f"bench_dest_{i}" if c.source == "pg" else "bench_dest_0"
        s = {"connector.class": "io.confluent.connect.jdbc.JdbcSinkConnector", "topics": topic, "tasks.max": "1",
             "connection.url": "jdbc:postgresql://cdcparity-postgres:5432/bench", "connection.user": "bench",
             "connection.password": "bench", "table.name.format": table, "insert.mode": "upsert",
             "pk.mode": "record_key", "pk.fields": "id", "delete.enabled": "true", "auto.create": "false",
             "auto.evolve": "false"}
        if tier == "t":
            # https://docs.confluent.io/kafka-connectors/jdbc/current/sink-connector/sink_config_options.html
            # batch.size (default 3000) raised with the consumer's max.poll.records.
            s["batch.size"] = "8192"
        s.update(schema_conv)
        return s
    if d == "s3":
        s = {"connector.class": "io.confluent.connect.s3.S3SinkConnector", "topics": topic, "tasks.max": "1",
             "format.class": ("io.confluent.connect.s3.format.json.JsonFormat" if c.source == "pg"
                              else "io.confluent.connect.s3.format.bytearray.ByteArrayFormat"),
             # flush.size has no default (required); 1000 at shipped matches the
             # v0.21 kit. Tuned: 10000 (both sides' tuned object size).
             # https://docs.confluent.io/kafka-connectors/s3-sink/current/configuration_options.html
             "flush.size": "10000" if tier == "t" else "1000", "rotate.interval.ms": "1000",
             "storage.class": "io.confluent.connect.s3.storage.S3Storage",
             "partitioner.class": "io.confluent.connect.storage.partitioner.DefaultPartitioner",
             "s3.bucket.name": "bench", "s3.region": "us-east-1", "store.url": "http://cdcparity-minio:9000",
             "path.style.access.enabled": "true", "aws.access.key.id": "conduitminio",
             "aws.secret.access.key": "conduitminiosecret", "schema.compatibility": "NONE"}
        s.update(plain_conv)
        return s
    raise ValueError(d)


# ---------------------------------------------------------------- Debezium Server
def dbzserver_props(c: Cell, arm: str) -> str:
    tier = arm_tier(arm)
    slot = slot_names(c, arm)[0]
    tables = [f"bench_source_{i}" for i in range(c.par)] if c.mode != "snapshot" else ["bench_source_0"]
    p = {}
    for k, v in debezium_source(c, tier, slot, tables).items():
        p[f"debezium.source.{k}"] = v
    p.update({
        # File offset storage (Debezium Server's documented default store),
        # flushed on its default interval: at-least-once, like the others.
        "debezium.source.offset.storage": "org.apache.kafka.connect.storage.FileOffsetBackingStore",
        "debezium.source.offset.storage.file.filename": "data/offsets.dat",
        "debezium.format.key": "json", "debezium.format.value": "json",
        "debezium.format.key.schemas.enable": "false", "debezium.format.value.schemas.enable": "false",
        "quarkus.log.level": "INFO",
    })
    d = c.dests[0]
    if d == "kafka":
        p.update({"debezium.sink.type": "kafka",
                  "debezium.sink.kafka.producer.bootstrap.servers": BROKER,
                  "debezium.sink.kafka.producer.key.serializer": "org.apache.kafka.common.serialization.StringSerializer",
                  "debezium.sink.kafka.producer.value.serializer": "org.apache.kafka.common.serialization.StringSerializer",
                  "debezium.sink.kafka.producer.acks": "all"})
        if tier == "t":
            p.update({"debezium.sink.kafka.producer.batch.size": "200000", "debezium.sink.kafka.producer.linger.ms": "100",
                      "debezium.sink.kafka.producer.compression.type": "lz4"})
    elif d == "pg":
        p.update({"debezium.sink.type": "jdbc",
                  "debezium.sink.jdbc.connection.url": "jdbc:postgresql://cdcparity-postgres:5432/bench",
                  "debezium.sink.jdbc.connection.username": "bench", "debezium.sink.jdbc.connection.password": "bench",
                  "debezium.sink.jdbc.insert.mode": "upsert", "debezium.sink.jdbc.primary.key.mode": "record_key",
                  "debezium.sink.jdbc.delete.enabled": "true", "debezium.sink.jdbc.schema.evolution": "none",
                  "debezium.sink.jdbc.collection.name.format": "${topic}",
                  # One process captures every table, so the destination table
                  # comes from the topic: RegexRouter renames
                  # cdcparity.public.bench_source_i to bench_dest_i, the table
                  # every other side writes. A routing SMT, no payload work.
                  "debezium.transforms": "route",
                  "debezium.transforms.route.type": "org.apache.kafka.connect.transforms.RegexRouter",
                  "debezium.transforms.route.regex": "cdcparity\\.public\\.bench_source_(.*)",
                  "debezium.transforms.route.replacement": "bench_dest_$1"})
        if tier == "t":
            p["debezium.sink.jdbc.batch.size"] = "8192"
    elif d == "http":
        p.update({"debezium.sink.type": "http", "debezium.sink.http.url": RECEIVER + "/"})
    if c.transform:
        p.update({"debezium.transforms": "unwrap,rename",
                  "debezium.transforms.unwrap.type": "io.debezium.transforms.ExtractNewRecordState",
                  "debezium.transforms.unwrap.delete.tombstone.handling.mode": "tombstone",
                  "debezium.transforms.rename.type": "org.apache.kafka.connect.transforms.ReplaceField$Value",
                  "debezium.transforms.rename.renames": "payload:body"})
    return "".join(f"{k}={v}\n" for k, v in p.items())


def dbzserver_service(c: Cell, arm: str) -> dict:
    side, tier = arm_side(arm), arm_tier(arm)
    opts = "-Xlog:gc*:file=/prof/gc.log:time,uptime,level,tags"
    if tier == "t":
        opts = "-Xms3g -Xmx3g " + opts
    svc = {
        "image": IMG[side],
        "container_name": SUT,
        "cpuset": "${SUT_CPUSET}",
        "mem_limit": SUT_MEM,
        "memswap_limit": SUT_MEM,
        "volumes": [f"../dbzserver/{arm}:/debezium/config:ro", "${PROF_DIR}:/prof", "${ASPROF_DIR}:/opt/async-profiler:ro"],
        "networks": ["benchi"],
    }
    if side == "dbzserver":
        svc["environment"] = {"JAVA_OPTS": opts + " ${JVM_DIAG_OPTS:-}"}
    return svc


# ---------------------------------------------------------------- benchi config
def hooks(c: Cell, arms: list) -> dict:
    steps = {"post-infrastructure": [], "pre-tool": [], "post-tool": [], "pre-test": [], "post-test": []}
    L = "/loader/loader"
    if "s3" in c.dests:
        steps["post-infrastructure"].append({"name": "mkbucket", "container": "cdcparity-counter",
                                             "run": f"{L} mkbucket -bucket bench"})
    if c.source in ("kafka", "file") or c.mode == "snapshot":
        steps["post-infrastructure"].append({"name": "wait-fill", "container": "cdcparity-counter",
                                             "run": f"{L} wait-file -path /sink/.fill_done -timeout 40m"})
    steps["pre-tool"].append({"name": "tool-start", "container": "cdcparity-counter", "run": f"{L} now TOOL_START_MS"})
    connect_arms = [a for a in arms if arm_side(a) == "connect"]
    if connect_arms:
        steps["post-tool"].append({"name": "register-connect", "container": "cdcparity-counter", "tools": connect_arms,
                                   "run": REGISTER_SH})
    specs = ",".join(sink_specs(c))
    tbl = dest_tables(c)
    steps["pre-test"].append({"name": "first-record", "container": "cdcparity-counter",
                              "run": f"{L} first -sinks '{specs}' -tables {tbl} -timeout 15m"})
    if c.mode == "snapshot":
        steps["pre-test"].append({"name": "snapshot-landed", "container": "cdcparity-counter",
                                  "run": f"{L} first -sinks '{specs}' -tables {tbl} -until {c.snapshot_rows} -timeout {SNAP_TIMEOUT}s"})
    if c.mode == "correct":
        prog = "http://cdcparity-loader:9100/progress" if c.source == "pg" else ""
        steps["pre-test"].append({"name": "drained", "container": "cdcparity-counter",
                                  "run": f"{L} first -sinks '{specs}' -tables {tbl} -settle -stable 60s -timeout 40m"
                                         + (f" -progress {prog}" if prog else "")})
        for spec in sink_specs(c, "verify"):
            steps["pre-test"].append({"name": "verify-" + spec.split(":")[0], "container": "cdcparity-counter",
                                      "run": verify_cmd(c, spec)})
    steps["post-test"].append({"name": "end", "container": "cdcparity-counter", "run": f"{L} now END_MS"})
    return {k: v for k, v in steps.items() if v}


# Registers /cell/connect/<arm>/*.json (the tool name is the arm) in order and
# waits until every connector's tasks are RUNNING.
# run-cell.sh writes the arm being run to /out/arm before each benchi run.
REGISTER_SH = r"""set -e
url=http://cdcparity-sut:8083
n=0
until wget -q -O /dev/null "$url/connectors" 2>/dev/null; do
  n=$((n+1)); [ "$n" -ge 300 ] && { echo "connect REST API never came up"; exit 1; }; sleep 1
done
arm=$(cat /out/arm)
for f in $(ls /cell/connect/"$arm"/*.json | sort); do
  wget -q -O- --header='Content-Type: application/json' --post-file="$f" "$url/connectors" >/dev/null
done
/loader/loader now CONNECT_REGISTERED_MS
"""


def verify_cmd(c: Cell, spec: str) -> str:
    if c.source == "pg":
        g = f"-tables {c.par} -payload {c.payload} -total {CORRECT_OPS}"
    else:
        # Kafka/file backlogs: inserts of table 0, id == seq, no fill.
        g = f"-tables 1 -window 0 -insert-frac 1 -update-frac 0 -batch 1 -seed 1 -payload {c.payload} -total {c.backlog}"
    ordered = "true"
    if spec.startswith("s3:") or (c.source == "kafka" and c.par > 1):
        ordered = "false"
    extra = " -no-deletes" if c.transform else ""
    name = spec.split(":")[0]
    return f"/loader/loader verify -sink '{spec}' {g} -ordered={ordered}{extra} -out /out/verify-{name}.json > /dev/null"


def bench_yml(c: Cell, arms: list) -> dict:
    return {
        "infrastructure": {"infra": {"compose": "./infra.yml"}},
        "tools": {a: {"compose": f"./tools/{a}.yml"} for a in arms},
        # benchi's own docker collector, SUT only (it is diagnostic; the
        # resource numbers come from res_sampler.py's cgroup reads).
        "metrics": {"docker": {"collector": "docker", "settings": {"containers": [SUT]}}},
        "tests": [{"name": c.id, "duration": f"{run_seconds(c)}s", "steps": hooks(c, arms)}],
    }


def gen(c: Cell, out: str) -> None:
    arms = c.arms()
    dump(os.path.join(out, "cell.json"), cell_meta(c))
    dump(os.path.join(out, "bench.yml"), bench_yml(c, arms))
    dump(os.path.join(out, "infra.yml"), infra_compose(c))
    write(os.path.join(out, "init.sql"), init_sql(c))
    for a in arms:
        side = arm_side(a)
        if side.startswith("conduit"):
            svc = conduit_service(c, a)
            dump(os.path.join(out, "pipelines", a, "pipelines.yml"), {"version": "2.2", "pipelines": conduit_pipelines(c, a)})
        elif side == "connect":
            svc = connect_service(c, a)
            for k, conn in enumerate(connect_connectors(c, a)):
                dump(os.path.join(out, "connect", a, f"{k:02d}-{conn['name']}.json"), conn)
        else:
            svc = dbzserver_service(c, a)
            write(os.path.join(out, "dbzserver", a, "application.properties"), dbzserver_props(c, a))
        dump(os.path.join(out, "tools", f"{a}.yml"),
             {"services": {"sut": svc}, "volumes": {"cdcparity-sink": {"external": True}},
              "networks": {"benchi": {"external": True}}})


def cell_meta(c: Cell) -> dict:
    arms = c.arms()
    return {
        "id": c.id, "family": c.family, "source": c.source, "dests": list(c.dests), "mode": c.mode,
        "par": c.par, "payload": c.payload, "rounds": c.rounds, "diag": c.diag, "repeat": c.repeat, "phase": c.phase,
        "transform": c.transform, "snapshot_rows": c.snapshot_rows, "backlog": c.backlog,
        "arms": arms,
        "slots": {a: slot_names(c, a) for a in arms},
        "sinks": sink_specs(c), "verify_sinks": sink_specs(c, "verify"),
        "dest_tables": dest_tables(c),
        "needs_broker": {a: (arm_side(a) == "connect" or c.source == "kafka" or "kafka" in c.dests) for a in arms},
        "run_seconds": run_seconds(c),
        "ramp": {"steps": RAMP_STEPS, "warm_s": RAMP_WARM, "step_s": RAMP_STEP, "settle_s": RAMP_SETTLE} if c.mode == "ramp" else None,
        "window": {"warm_s": CAP_WARM, "window_s": CAP_WINDOW} if c.mode in ("capacity", "drain") else None,
        "recover": {"ops": RECOVER_OPS, "kill_after_s": RECOVER_KILL_AFTER} if c.mode == "recover" else None,
        "correct_ops": CORRECT_OPS if c.mode == "correct" and c.source == "pg" else None,
        "note": c.note,
    }


# ---------------------------------------------------------------- planning
def shard(k: int, phases=(1, 2)) -> list:
    """Longest-processing-time-first assignment of cells to k instances. A
    cell marked repeat is placed twice, on two DIFFERENT instances."""
    units = []
    for c in CELLS:
        if c.phase not in phases:
            continue
        units.append((cell_minutes(c), c.id, 0))
        if c.repeat:
            units.append((cell_minutes(c), c.id, 1))
    units.sort(key=lambda u: -u[0])
    loads = [0.0] * k
    plan = [[] for _ in range(k)]
    for m, cid, rep in units:
        order = sorted(range(k), key=lambda i: loads[i])
        for i in order:
            if rep == 1 and any(x[0] == cid for x in plan[i]) and k > 1:
                continue
            plan[i].append((cid, rep))
            loads[i] += m
            break
    return [(loads[i], plan[i]) for i in range(k)]


SETUP_MIN = 20  # instance boot, image builds, pulls


def estimate(price: float, phases=(1, 2)) -> str:
    sel = [c for c in CELLS if c.phase in phases]
    total_runs = sum(run_count(c) * (2 if c.repeat else 1) for c in sel)
    total_min = sum(cell_minutes(c) * (2 if c.repeat else 1) for c in sel)
    lines = [f"phases {','.join(map(str, phases))}: cells {len(sel)}, arms {sum(len(c.arms()) for c in sel)}, runs {total_runs}, "
             f"run time {total_min/60:.1f} h (incl. warmup rounds, diag runs, repeats)", "",
             "| instances | wall time (longest shard) | instance-hours | cost at ${:.3f}/h |".format(price),
             "| --- | --- | --- | --- |"]
    for k in (2, 4, 8):
        sh = shard(k, phases)
        wall = max(l for l, _ in sh) / 60 + SETUP_MIN / 60
        ih = sum(l for l, _ in sh) / 60 + k * SETUP_MIN / 60
        lines.append(f"| {k} | {wall:.1f} h | {ih:.1f} | ${ih*price:,.0f} |")
    return "\n".join(lines)


def main(argv: list) -> int:
    if len(argv) < 2:
        print(__doc__)
        return 2
    cmd = argv[1]
    if cmd == "list":
        if "--json" in argv:
            print(json.dumps([cell_meta(c) for c in CELLS], indent=2))
        else:
            for c in CELLS:
                print(f"{c.id:34s} p{c.phase} {c.mode:9s} arms={','.join(c.arms())}")
        return 0
    if cmd == "table":
        print("| cell | phase | mode | source -> dest | shape | arms | rounds | diag | x-instance |")
        print("| --- | --- | --- | --- | --- | --- | --- | --- | --- |")
        for c in CELLS:
            shape = (f"{c.par} {'tables' if c.source == 'pg' else 'partitions'}, {c.payload} B" if c.mode != "snapshot"
                     else f"{c.snapshot_rows:,} rows, {c.payload} B")
            if c.mode == "drain":
                shape += f", backlog {c.backlog:,}"
            print(f"| {c.id} | {c.phase} | {c.mode} | {c.source} -> {'+'.join(c.dests)}{' (unwrap+rename)' if c.transform else ''} | "
                  f"{shape} | {', '.join(c.arms())} | {c.rounds} | {'yes' if c.diag else '-'} | {'yes' if c.repeat else '-'} |")
        return 0
    if cmd == "gen":
        c = BY_ID[argv[2]]
        gen(c, argv[3])
        return 0
    phases = (1, 2)
    if "--phase" in argv:
        phases = tuple(int(x) for x in argv[argv.index("--phase") + 1].split(","))
    if cmd == "estimate":
        price = float(argv[argv.index("--price") + 1]) if "--price" in argv else 0.424
        print(estimate(price, phases))
        return 0
    if cmd == "shard":
        k, i = int(argv[2]), int(argv[3])
        for cid, rep in shard(k, phases)[i][1]:
            print(f"{cid} {rep}")
        return 0
    print(__doc__)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))

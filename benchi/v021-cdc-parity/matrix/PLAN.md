# Parity matrix: pre-registration

**Status: pre-registered, no data. Amended three times before any data: twice
after independent adversarial review, once to trim the plan and move to spot
instances (section 15, amendment 3). Tag: matrix-plan-v3.**

**Sequencing (amendment 3).** The smoke run validates the harness only
(correctness, recovery, Avro parity, every wiring path); its numbers are never
results and it may run on any Conduit version. The pilot, flagship, core and
hold-out run only after Conduit `main` ships the connectors on SDK v0.18.1 with
default source batching: the matrix measures shipped defaults, and running it
on the pre-batching release would publish numbers for a version no user will
run. Every cell below will be
reported, including cells Conduit loses. A cell dropped, added or re-run after
data exists is recorded in the change log, with the reason, before its
results are used. The plan's commit is tagged; every run records the harness
hash (`cells.py hash`: the code the results depend on), and `analyze.py`
refuses results from any other harness.

The point is to find where and why Conduit is slower, so it can be fixed,
with numbers that hold up when Debezium or Kafka Connect maintainers read
them. `GAP-ANALYSIS.md` (written after the runs) turns every cell Conduit
loses into a profile-backed cause, a candidate arch-v2 fix and an estimated
gain.

Source of truth: `cells.py` (cell registry, config generator, run order,
plans). The tables in section 7 are generated from it, and `test_cells.py`
checks the rules below against every generated config.

## 1. Hypotheses (before data), with what would falsify each

Informed by the v0.21 results and the 2026-10-26 runs (`../results-aws/v022-*`).
"Resolved" means the section 10 label, not a significance test.

| id | comparison | prediction | falsified if |
| --- | --- | --- | --- |
| H1 | A2-pg-kafka, conduit-classic-s vs connect-avro-s (continuity) | classic well behind: ratio <= 0.5 | median ratio > 0.8 |
| H2 | P1, P2: A2-pg-kafka, conduit-avro-s vs connect-avro-s / dbzserver-avro-s | within 10% of both | RESOLVED with \|ratio - 1\| > 0.10 either way |
| H3 | P3, P4: A2-pg-pg, conduit-s vs connect-avro-s / dbzserver-avro-s | Conduit behind: 0.6-0.9 (per-record upserts vs batched JDBC) | median ratio >= 1.0, or < 0.4 |
| H4 | P5: B-kafka-pg, conduit-s vs connect-s | Conduit behind: 0.5-0.9 (one consumer per pipeline, per-record upserts) | median ratio >= 1.0 |
| H5 | P6: B-kafka-s3, conduit-s vs the best Connect flush size | within 10% | RESOLVED with \|ratio - 1\| > 0.10 |
| H6 | P7: A1-pg-kafka ramp, highest tracked step | Conduit >= Connect | Conduit lower in >= 4 of 5 rounds |
| H7 | P8: F-pg-kafka snapshot, land time | Conduit <= 1.2x Connect's time | Conduit > 1.2x in >= 4 of 5 rounds |
| H8 | peak RSS and startup, every base cell | Debezium Server <= 0.5x the Connect worker on both; Conduit <= Debezium Server | either Server ratio > 0.8, or Conduit > Server |
| H9 | every H and G cell | no side loses, corrupts or (on ordered sinks) reorders a record | any such record |
| H10 | G, shipped tier | duplicates after kill -9: Connect and Debezium Server >= 10x Conduit (60 s vs 1 s position flush) | ratio < 3; and in the `e` tier (all at 1 s) within 3x of each other, falsified if not |

## 2. Sides and versions

| side | what | pinned |
| --- | --- | --- |
| Conduit arch-v2 | `--preview.pipeline-arch-v2`, OpenCDC JSON on Kafka, or (`conduit-avro`) the `avro.encode` processor through the registry | the SHA in `environment.txt`, built from the repository Dockerfile |
| Conduit classic | ONE continuity cell (A2-pg-kafka-p1-b64); perf work is arch-v2 only | same build |
| Debezium on Kafka Connect | Debezium 3.7.0.Final Postgres source, Kafka Connect 4.x (one distributed worker); Confluent JDBC sink 10.9.6 and S3 sink 12.1.11 (Confluent Community License), Apache FileStream connectors; JSON converter, or (`connect-avro`) the Apicurio Avro converter | `quay.io/debezium/connect:3.7.0.Final@sha256:8dc50bab...` + `images/connect.Dockerfile` |
| Debezium Server | 3.7.0.Final JVM, one connector per process; JSON, or (`dbzserver-avro`) Avro through Apicurio; JDBC, Kafka and HTTP sinks; `dbzserver-avro-xn`: one process per table in one container | `quay.io/debezium/server:3.7.0.Final@sha256:9117f3fa...` |
| Debezium Server native | Mandrel build, HTTP cells only | `quay.io/debezium/server-native:3.7.0.Final@sha256:6654a571...` |
| schema registry | Apicurio Registry 3.3.3 (Apache-2.0), in-memory; Debezium sides use its native API, Conduit its Confluent-compatible API | `quay.io/apicurio/apicurio-registry:3.3.3@sha256:c9cae4c9...` |

Both Debezium runtimes run the same Debezium (3.7.0.Final). quay.io tags are
rebuilt daily; the digest is the pin.

**Primary serialization.** For Postgres sources into Kafka or Postgres, the
Debezium sides' primary arms use Avro through the registry: their documented
production setup. Their JSON arms run shipped only, as exploratory columns.
Into Kafka, Conduit is compared like for like (`conduit-avro` against the Avro
arms, `conduit` against the JSON arms). Into Postgres, Conduit serializes
nothing (no broker), so `conduit` is compared with the Avro arms. Key
encoding into Kafka is schemaless JSON on every side (value Avro); into
Postgres the Debezium sides use an Avro key (the JDBC sinks need the key
schema).

**Equal Avro content (every Avro arm into Kafka: P1, P2, P7, P8, A1, A2, D,
F, G, H, W).** Every side writes the same record: the flattened row plus the
operation code `__op` (c, u, d, r), and a delete as a tombstone (empty
value) keyed by the row's id. Debezium (both runtimes): `ExtractNewRecordState`
with `add.fields=op` and `delete.tombstone.handling.mode=tombstone`, the
common flattened-Avro setup. Conduit: `field.set .Payload.After.__op` from
the operation, then `avro.encode` of `.Payload.After`; deletes are written
as an empty value (Kafka has no Conduit-side tombstone; the record count and
key are the same). One registry subject per table on every side:
`cdcparity.public.bench_source_N-value` (the Apicurio converter's default
TopicIdStrategy; Conduit's subject is a static setting, so a multi-table
pipeline runs one conditional encoder per table). Conduit's `autoRegister`
strategy is documented as "for development purposes" (it infers a schema per
record); its cost is part of the measurement, which is conservative against
Conduit. The full-envelope comparison (Debezium envelope vs OpenCDC record)
remains, as the exploratory JSON columns. Test: `test_avro_arms_carry_equal_content`.

**Equal content on the wire (amendment 4).** The shakedown wire check
(2026-11-02, every side once on A2-pg-kafka) dumped the first broker
records. Keys are equal (`{"id":N}`, 8 B); values are the same Apicurio
Avro row, 74 B for Conduit and 75 B for both Debezium runtimes (their
schema carries `__op` as an optional union, Conduit's as a required string:
one byte). Headers were not equal: both Debezium runtimes add four
`__debezium.context.*` headers to every record and Debezium Server adds an
OpenTelemetry `traceparent`; Conduit adds none. Every Debezium Avro arm into
Kafka therefore drops those five headers with Kafka's `DropHeaders` SMT, and
Debezium Server runs with its bundled OpenTelemetry Java agent off (`OTEL_JAVAAGENT_ENABLED=false`, the source of `traceparent`). The equal-content
definition now includes the headers: none on any side. The smoke gate checks
it on every Avro Kafka cell (the first record's key size, value size within
2 B, and header count must match across arms; `test_gate_fails_when_avro_arms_differ_on_the_wire`).
Compression is not part of content: in the shipped tier each side writes its
default (Conduit's Kafka destination snappy, both Debezium producers none,
seen on the broker); the tuned tier sets lz4 on every side.

**Destinations without an equivalent are left out:**

- Debezium Server 3.7.0.Final has no file or S3 sink. Its JDBC sink is in the
  JVM distribution (`debezium-server-dist/pom.xml`, scope compile), not native.
- Apache Kafka ships no HTTP sink; Confluent's is commercial-licensed. The
  HTTP cells compare Conduit and Debezium Server only.
- Conduit ships no HTTP destination. The only one in the ConduitIO and
  conduitio-labs inventory is `conduitio-labs/conduit-connector-http` v0.4.0
  (Apache-2.0, SDK v0.14, commit 9bc6ada3), run as a standalone plugin. HTTP
  cells are an appendix (plugin quality), never in a Conduit summary.
- S3 as a SOURCE: Confluent's S3 Source is proprietary (Enterprise License);
  Apache-licensed Connect S3 sources exist (Aiven, Lenses). A follow-up family,
  not in this matrix.
- FileStreamSource and FileStreamSink are Apache Kafka's example connectors:
  every file cell is an appendix ("reference connectors"), never in a summary.

## 3. Tiers

- **s, shipped defaults.** Nothing is set except what a pipeline needs to exist
  and to produce equivalent output (connections, tables, topics, snapshot mode,
  converters and record formats, the hardening settings Conduit requires).
  `test_cells.py` fails if a shipped arm sets any tuning knob. The one
  exception is the Confluent S3 sink's `flush.size`, which has no default: the
  shipped tier runs it at 1,000, 10,000 and 100,000 (`@f1k`, `@f10k`,
  `@f100k`), and the primary comparison takes the best of the three per round.
- **t, tuned with each vendor's documented knobs, at EQUAL values.** Every
  side's tuned config is derived from one concept table (`cells.TUNED`), and
  `test_cells.py` checks each tuned arm against it:

| concept | value | Conduit | Kafka Connect | Debezium (both runtimes) | source |
| --- | --- | --- | --- | --- | --- |
| source batch, wait | 8192, 100 ms | source `sdk.batch.size`, `sdk.batch.delay` | consumer `max.poll.records` (sinks), FileStreamSource `batch.size` | `max.batch.size`, `poll.interval.ms`; `max.queue.size` 4x | SDK parameter docs; https://kafka.apache.org/documentation/#consumerconfigs ; https://debezium.io/documentation/reference/3.7/connectors/postgresql.html (knobs documented, **no values published: ours**) |
| producer | lz4; 200000 B, 100 ms | Kafka destination `compression` (no linger or batch-bytes knob of the same meaning) | `producer.batch.size`, `linger.ms`, `compression.type` | Server Kafka sink producer, same | https://docs.confluent.io/cloud/current/best-practices/optimizing/throughput.html (its `acks=1` advice is NOT applied: delivery parity) |
| consumer fetch | 100000 B min, 10 MiB per partition | none (no knob: an asymmetry, reported) | `fetch.min.bytes`, `max.partition.fetch.bytes` | - | same Confluent page; per-partition value ours |
| S3 object | 10000 records, 1 s | S3 `sdk.batch.size`, `sdk.batch.delay` | `flush.size`, `rotate.interval.ms` | - | https://docs.confluent.io/kafka-connectors/s3-sink/current/configuration_options.html |
| Postgres sink batch | 8192 rows | arch-v2 funnel batches at 3000 by default (#3203; no per-pipeline knob); pgx `default_query_exec_mode=cache_statement` | JDBC `batch.size`, pgjdbc `reWriteBatchedInserts=true` | Debezium JDBC `batch.size`, `use.reduction.buffer` | Confluent JDBC sink docs; pgjdbc docs; Debezium JDBC docs. cache_statement is included because the v0.21 runs showed it matters (results.md): stated, not hidden. reWriteBatchedInserts: if it fails on duplicate keys within a batch (ON CONFLICT DO UPDATE affecting a row twice), smoke records it and the tuned arm drops it, in the change log |
| snapshot fetch | 50000 rows | `snapshot.fetchSize` is 50000 by default | - | `snapshot.fetch.size` | connector docs |
| heap | -Xms = -Xmx = 3 GiB | Go: GOGC and GOMEMLIMIT unset on every tier (no Conduit-documented knob) | worker heap | Server heap | https://docs.confluent.io/platform/current/kafka/deployment.html#jvm |

  Tuned arms run only for the primary serialization; parallelism follows
  section 6.
- **e, recovery cells only**: shipped, but every side flushes positions every
  1 s (Connect `offset.flush.interval.ms=1000` and
  `scheduled.rebalance.max.delay.ms=0`; Debezium Server
  `offset.flush.interval.ms=1000`; Conduit's persister is 1 s / 10,000 records
  at shipped defaults, `pkg/connector/persister.go`).

## 4. Delivery and durability (stated per side, not asserted equal)

At-least-once everywhere; no side runs exactly-once (`test_cells.py`).

| side | Kafka produce | source position advance | position flush cadence (shipped) |
| --- | --- | --- | --- |
| Conduit | `acks=all` | after the destination acks a record, end to end; the Postgres LSN is confirmed from acked positions | persister: every 1 s or 10,000 records (`pkg/connector/persister.go`) |
| Kafka Connect | `acks=all` (explicit), idempotent producer default | sink: offsets committed after `put`/`flush`; Debezium source: confirmed LSN advances at offset commit | `offset.flush.interval.ms` 60 s |
| Debezium Server | `acks=all` (explicit) | after the sink's `handle` returns, at offset commit | `offset.flush.interval.ms` 60 s (file store) |

What "written" means here, for every side alike: Kafka is one broker, RF 1,
no per-batch fsync (the leader's page cache). The file sinks do not fsync
(Conduit's file destination and FileStreamSink alike). MinIO writes to a
Docker volume. Postgres runs on EBS gp3 with fsync on, so destination commits
are durable there. None of this matches a production durability bar; it is
the same for every side. Fan-out (E) is an architecture comparison: Conduit's
two destinations ack as a unit (the slower one gates the pipeline), Connect's
two sinks are independent consumers; no speed ratio is headlined from it.

## 5. Resource parity

- Every system under test is one container, `cdcparity-sut`, with an identical
  cpuset (the last two physical cores, both hardware threads: 4 vCPUs), an
  identical memory limit (6 GiB, no swap) and the same bounded log driver.
- Postgres, the broker, MinIO, the registry, the loader, the receiver, the
  sampler and the counter run on the other cores. So do `dockerd` and
  `containerd` (systemd `CPUAffinity`, set before Docker starts), the harness
  (`taskset`) and the samplers. Every veth receive queue's RPS mask points at
  the infrastructure cores. Residual: the send side of container networking
  still runs on the sender's CPU (section 14).
- Resources are reported three ways per arm: SUT only; footprint (SUT plus the
  broker, for arms that cannot run without one, which is the headline for
  cross-architecture comparisons); and destination CPU (Postgres, MinIO,
  registry, receiver) per million events, so CPU a side shifts onto its
  destination is charged. Per-run steal and SUT CPU frequency are recorded.
  No dollar figure is reported (the earlier $/billion was rank-equivalent to
  throughput and misleading).
- Go sees 4 CPUs because the cpuset limits the process's CPU affinity mask,
  which Go reads for GOMAXPROCS (not because of the cgroup quota); the JVMs
  read the same mask. Neither is overridden.
- Instance: c6i.4xlarge (16 vCPUs: 4 for the SUT, 12 for everything else).
- **Disks** (review R2-B2): Postgres data and WAL, the Kafka log and MinIO each
  have their own EBS gp3 volume (6000 IOPS, 500 MB/s each, XFS, noatime),
  bind-mounted into their containers; Docker's root (images, containers, the
  sink volume, logs) is the root volume. So one arm's broker or object-store
  traffic cannot slow another component's fsyncs. Every run records per-device
  utilization, await and write MB/s (`/proc/diskstats` deltas and
  `iostat -dxm`), and the bottleneck label reads "disk-bound (<volume>)" when a
  volume is busy for 90% of the window.

## 6. Workload

- **Rows**: `(id bigint, payload text)`; the `wide` shape adds 28 typed
  columns (integer, bigint, numeric, text, varchar, timestamptz, date, boolean,
  jsonb, uuid, double).
- **Payload**: a marker naming the change and its commit-queue time, then
  random alphanumerics written in pairs: about 2.3x compressible
  (`TestPayloadCompressibility`, deflate level 1), so compression is a real
  trade-off and not free. 16 KiB payloads TOAST.
- **Transactions**: `-txn N` statements per transaction (BEGIN/COMMIT in the
  pipelined batch); the base cells keep v0.21's one transaction per 1000-
  statement round trip, the hold-out cells run 1, 10, 100 and 10,000.
- **Mixes**: 70/20/10 INSERT/UPDATE/DELETE (base), 20/70/10 (update-heavy),
  45/10/45 (delete-heavy). **Skew**: Zipf 1.2 over 16 tables. **Schema
  evolution**: `ALTER TABLE ... ADD COLUMN` mid-window. **Idle**: 1 op/s.
  **Slow sink**: 5 ms per request at the HTTP receiver.
- **Parallelism**: with N tables, the shipped tier is ONE pipeline / connector /
  Server process with a table list and one slot (the idiom on every side);
  the tuned tier is one per table (N slots) for Conduit and Connect; Debezium
  Server's scale-out is its own row, one process per table in one container.
- **Postgres on EBS** (gp3, fsync on), never tmpfs. `max_slot_wal_keep_size`
  40 GB bounds WAL a slow slot can retain; `shared_buffers` 4 GB.
- **Hold-out**: the base cells are the shape Conduit's defaults were tuned on
  (#3203, #3199). The hold-out plan (shapes, transaction sizes, mixes, wide
  rows, skew, evolution, idle, backpressure) is mandatory before ANY public
  claim; phase-1 results are internal until it has run.

## 7. Cells

Labels: **primary** (a pre-registered primary comparison, section 10),
**reference** (example connector, appendix), **plugin** (labs plugin,
appendix). Arm names: `<side>-<tier>[-aa][@variant]`; `-aa` is the A/A twin.
Flagship cells have an A/A twin for EVERY side of their primary comparisons.

Order: a cell's rounds are run back to back on one instance after one
discarded warmup round per arm. In each round the primary arms run as one
adjacent block, rotated and reversed on alternate rounds (AB/BA); the block
and the other arms are shuffled with a seed derived from the cell and the
round (`cells.py order`, recorded in `session.txt`). A fixed rotation is not
used: with more arms than rounds it does not give every arm every position.

### Every cell

| cell | plan | mode | source -> dest | shape / workload | arms | rounds | instances | diag | label |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| A1-pg-kafka-ramp | flagship | ramp | pg -> kafka | 1 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-s, connect-s, dbzserver-s, conduit-avro-s-aa, connect-avro-s-aa, dbzserver-avro-s-aa | 5 | 2 | - | primary |
| A1-pg-pg-ramp | core | ramp | pg -> pg | 1 tables, 64 B | conduit-s, connect-avro-s, dbzserver-avro-s, connect-s, dbzserver-s, conduit-s-aa | 3 | 1 | - |  |
| A1-pg-s3-ramp | core | ramp | pg -> s3 | 1 tables, 64 B | conduit-s, connect-s@f1k, connect-s@f10k, connect-s@f100k, conduit-s-aa | 3 | 1 | - |  |
| A1-pg-file-ramp | core | ramp | pg -> file | 1 tables, 64 B | conduit-s, connect-s, conduit-s-aa | 3 | 1 | - | reference |
| A1-pg-http-ramp | core | ramp | pg -> http | 1 tables, 64 B | conduit-s, dbzserver-s, dbzserver-native-s, conduit-s-aa | 3 | 1 | - | plugin |
| A2-pg-kafka-p1-b64 | flagship | capacity | pg -> kafka | 1 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-avro-t, connect-avro-t, dbzserver-avro-t, conduit-s, connect-s, dbzserver-s, conduit-avro-s-aa, connect-avro-s-aa, dbzserver-avro-s-aa | 5 | 2 | yes | primary |
| A2-pg-kafka-p4-b64 | holdout | capacity | pg -> kafka | 4 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-avro-t, connect-avro-t, dbzserver-avro-t, conduit-s, connect-s, dbzserver-s, conduit-avro-s-aa, dbzserver-avro-xn-s | 3 | 1 | - |  |
| A2-pg-kafka-p16-b64 | holdout | capacity | pg -> kafka | 16 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-avro-t, connect-avro-t, dbzserver-avro-t, conduit-s, connect-s, dbzserver-s, conduit-avro-s-aa, dbzserver-avro-xn-s | 3 | 1 | - |  |
| A2-pg-kafka-p1-b1024 | holdout | capacity | pg -> kafka | 1 tables, 1024 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-avro-t, connect-avro-t, dbzserver-avro-t, conduit-s, connect-s, dbzserver-s, conduit-avro-s-aa | 3 | 1 | - |  |
| A2-pg-kafka-p1-b16384 | holdout | capacity | pg -> kafka | 1 tables, 16384 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-avro-t, connect-avro-t, dbzserver-avro-t, conduit-s, connect-s, dbzserver-s, conduit-avro-s-aa | 3 | 1 | - |  |
| A2-pg-pg-p1-b64 | flagship | capacity | pg -> pg | 1 tables, 64 B | conduit-s, connect-avro-s, dbzserver-avro-s, conduit-t, connect-avro-t, dbzserver-avro-t, connect-s, dbzserver-s, conduit-s-aa, connect-avro-s-aa, dbzserver-avro-s-aa | 5 | 2 | yes | primary |
| A2-pg-pg-p4-b64 | holdout | capacity | pg -> pg | 4 tables, 64 B | conduit-s, connect-avro-s, dbzserver-avro-s, conduit-t, connect-avro-t, dbzserver-avro-t, connect-s, dbzserver-s, conduit-s-aa, dbzserver-avro-xn-s | 3 | 1 | - |  |
| A2-pg-pg-p16-b64 | holdout | capacity | pg -> pg | 16 tables, 64 B | conduit-s, connect-avro-s, dbzserver-avro-s, conduit-t, connect-avro-t, dbzserver-avro-t, connect-s, dbzserver-s, conduit-s-aa, dbzserver-avro-xn-s | 3 | 1 | - |  |
| A2-pg-pg-p1-b1024 | holdout | capacity | pg -> pg | 1 tables, 1024 B | conduit-s, connect-avro-s, dbzserver-avro-s, conduit-t, connect-avro-t, dbzserver-avro-t, connect-s, dbzserver-s, conduit-s-aa | 3 | 1 | - |  |
| A2-pg-pg-p1-b16384 | holdout | capacity | pg -> pg | 1 tables, 16384 B | conduit-s, connect-avro-s, dbzserver-avro-s, conduit-t, connect-avro-t, dbzserver-avro-t, connect-s, dbzserver-s, conduit-s-aa | 3 | 1 | - |  |
| A2-pg-s3-p1-b64 | core | capacity | pg -> s3 | 1 tables, 64 B | conduit-s, connect-s@f1k, connect-s@f10k, connect-s@f100k, conduit-t, connect-t, conduit-s-aa | 3 | 1 | yes |  |
| A2-pg-s3-p4-b64 | holdout | capacity | pg -> s3 | 4 tables, 64 B | conduit-s, connect-s@f1k, connect-s@f10k, connect-s@f100k, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - |  |
| A2-pg-s3-p16-b64 | holdout | capacity | pg -> s3 | 16 tables, 64 B | conduit-s, connect-s@f1k, connect-s@f10k, connect-s@f100k, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - |  |
| A2-pg-s3-p1-b1024 | holdout | capacity | pg -> s3 | 1 tables, 1024 B | conduit-s, connect-s@f1k, connect-s@f10k, connect-s@f100k, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - |  |
| A2-pg-s3-p1-b16384 | holdout | capacity | pg -> s3 | 1 tables, 16384 B | conduit-s, connect-s@f1k, connect-s@f10k, connect-s@f100k, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - |  |
| A2-pg-file-p1-b64 | core | capacity | pg -> file | 1 tables, 64 B | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa | 3 | 1 | yes | reference |
| A2-pg-file-p4-b64 | holdout | capacity | pg -> file | 4 tables, 64 B | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - | reference |
| A2-pg-file-p16-b64 | holdout | capacity | pg -> file | 16 tables, 64 B | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - | reference |
| A2-pg-file-p1-b1024 | holdout | capacity | pg -> file | 1 tables, 1024 B | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - | reference |
| A2-pg-file-p1-b16384 | holdout | capacity | pg -> file | 1 tables, 16384 B | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - | reference |
| A2-pg-http-p1-b64 | core | capacity | pg -> http | 1 tables, 64 B | conduit-s, dbzserver-s, conduit-t, dbzserver-t, dbzserver-native-s, conduit-s-aa | 3 | 1 | yes | plugin |
| B-kafka-pg-p1-b64 | flagship | drain | kafka -> pg | 1 partitions, 64 B, backlog 12 GiB (25,067,902 records) | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa, connect-s-aa | 5 | 2 | yes | primary |
| B-kafka-pg-p4-b64 | holdout | drain | kafka -> pg | 4 partitions, 64 B, backlog 12 GiB (25,067,902 records) | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - |  |
| B-kafka-pg-p16-b64 | holdout | drain | kafka -> pg | 16 partitions, 64 B, backlog 12 GiB (25,067,902 records) | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - |  |
| B-kafka-pg-p1-b1024 | holdout | drain | kafka -> pg | 1 partitions, 1024 B, backlog 12 GiB (8,741,453 records) | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - |  |
| B-kafka-pg-p1-b16384 | holdout | drain | kafka -> pg | 1 partitions, 16384 B, backlog 12 GiB (765,409 records) | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - |  |
| B-kafka-s3-p1-b64 | flagship | drain | kafka -> s3 | 1 partitions, 64 B, backlog 12 GiB (25,067,902 records) | conduit-s, connect-s@f1k, connect-s@f10k, connect-s@f100k, conduit-t, connect-t, conduit-s-aa, connect-s-aa@f10k | 5 | 2 | yes | primary |
| B-kafka-s3-p4-b64 | holdout | drain | kafka -> s3 | 4 partitions, 64 B, backlog 12 GiB (25,067,902 records) | conduit-s, connect-s@f1k, connect-s@f10k, connect-s@f100k, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - |  |
| B-kafka-s3-p16-b64 | holdout | drain | kafka -> s3 | 16 partitions, 64 B, backlog 12 GiB (25,067,902 records) | conduit-s, connect-s@f1k, connect-s@f10k, connect-s@f100k, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - |  |
| B-kafka-s3-p1-b1024 | holdout | drain | kafka -> s3 | 1 partitions, 1024 B, backlog 12 GiB (8,741,453 records) | conduit-s, connect-s@f1k, connect-s@f10k, connect-s@f100k, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - |  |
| B-kafka-s3-p1-b16384 | holdout | drain | kafka -> s3 | 1 partitions, 16384 B, backlog 12 GiB (765,409 records) | conduit-s, connect-s@f1k, connect-s@f10k, connect-s@f100k, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - |  |
| B-kafka-file-p1-b64 | core | drain | kafka -> file | 1 partitions, 64 B, backlog 12 GiB (25,067,902 records) | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa | 3 | 1 | yes | reference |
| B-kafka-file-p1-b1024 | holdout | drain | kafka -> file | 1 partitions, 1024 B, backlog 12 GiB (8,741,453 records) | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - | reference |
| B-kafka-file-p1-b16384 | holdout | drain | kafka -> file | 1 partitions, 16384 B, backlog 12 GiB (765,409 records) | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - | reference |
| C-file-kafka-b64 | core | drain | file -> kafka | 1 partitions, 64 B, backlog 8 GiB (91,382,282 records) | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa | 3 | 1 | yes | reference |
| C-file-kafka-b1024 | holdout | drain | file -> kafka | 1 partitions, 1024 B, backlog 8 GiB (8,149,843 records) | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - | reference |
| C-file-kafka-b16384 | holdout | drain | file -> kafka | 1 partitions, 16384 B, backlog 8 GiB (523,329 records) | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa | 3 | 1 | - | reference |
| D-pg-kafka-transform | core | capacity | pg -> kafka (transform) | 1 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-avro-t, connect-avro-t, dbzserver-avro-t, conduit-avro-s-aa | 3 | 1 | yes |  |
| E-pg-fanout-file-s3 | core | capacity | pg -> file+s3 | 1 tables, 64 B | conduit-s, connect-s@f10k, conduit-t, connect-t, conduit-s-aa | 3 | 1 | yes |  |
| F-pg-kafka-snapshot | flagship | snapshot | pg -> kafka | 10,000,000 rows, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-avro-t, connect-avro-t, dbzserver-avro-t, conduit-s, connect-s, dbzserver-s, conduit-avro-s-aa | 5 | 2 | - | primary |
| F-pg-pg-snapshot | core | snapshot | pg -> pg | 10,000,000 rows, 64 B | conduit-s, connect-avro-s, dbzserver-avro-s, conduit-t, connect-avro-t, dbzserver-avro-t, connect-s, dbzserver-s, conduit-s-aa | 3 | 1 | - |  |
| G-pg-kafka-kill9 | flagship | recover | pg -> kafka | 1 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, connect-avro-e, dbzserver-avro-e | 3 | 1 | - |  |
| G-pg-pg-kill9 | core | recover | pg -> pg | 1 tables, 64 B | conduit-s, connect-avro-s, dbzserver-avro-s, connect-avro-e, dbzserver-avro-e | 3 | 1 | - |  |
| G-pg-kafka-kill9-snapshot | core | recover-snapshot | pg -> kafka | 10,000,000 rows, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, connect-avro-e, dbzserver-avro-e | 3 | 1 | - |  |
| H-pg-kafka-verify | core | correct | pg -> kafka | 1 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-avro-t, connect-avro-t, dbzserver-avro-t, conduit-s, connect-s, dbzserver-s | 1 | 1 | - |  |
| H-pg-pg-verify | core | correct | pg -> pg | 1 tables, 64 B | conduit-s, connect-avro-s, dbzserver-avro-s, conduit-t, connect-avro-t, dbzserver-avro-t, connect-s, dbzserver-s | 1 | 1 | - |  |
| H-pg-s3-verify | core | correct | pg -> s3 | 1 tables, 64 B | conduit-s, connect-s@f1k, connect-s@f10k, connect-s@f100k, conduit-t, connect-t | 1 | 1 | - |  |
| H-pg-file-verify | core | correct | pg -> file | 1 tables, 64 B | conduit-s, connect-s, conduit-t, connect-t | 1 | 1 | - |  |
| H-pg-http-verify | core | correct | pg -> http | 1 tables, 64 B | conduit-s, dbzserver-s, conduit-t, dbzserver-t, dbzserver-native-s | 1 | 1 | - |  |
| H-kafka-pg-verify | core | correct | kafka -> pg | 1 partitions, 64 B | conduit-s, conduit-t, connect-s, connect-t | 1 | 1 | - |  |
| H-kafka-s3-verify | core | correct | kafka -> s3 | 1 partitions, 64 B | conduit-s, conduit-t, connect-s@f10k, connect-t | 1 | 1 | - |  |
| H-kafka-file-verify | core | correct | kafka -> file | 1 partitions, 64 B | conduit-s, conduit-t, connect-s, connect-t | 1 | 1 | - |  |
| H-file-kafka-verify | core | correct | file -> kafka | 1 partitions, 64 B | conduit-s, conduit-t, connect-s, connect-t | 1 | 1 | - |  |
| H-pg-kafka-transform-verify | core | correct | pg -> kafka (transform) | 1 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s | 1 | 1 | - |  |
| H-pg-fanout-file-s3-verify | core | correct | pg -> file+s3 | 1 tables, 64 B | conduit-s, connect-s@f10k | 1 | 1 | - |  |
| H-pg-pg-p4-verify | core | correct | pg -> pg | 4 tables, 64 B | conduit-s, connect-avro-s, dbzserver-avro-s, conduit-t, connect-avro-t, dbzserver-avro-t | 1 | 1 | - |  |
| H-pg-kafka-p4-verify | core | correct | pg -> kafka | 4 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-avro-t, connect-avro-t, dbzserver-avro-t | 1 | 1 | - |  |
| H-pg-kafka-wide-txn1-verify | core | correct | pg -> kafka | 1 tables, 64 B, wide, txn 1 | conduit-avro-s, connect-avro-s, dbzserver-avro-s | 1 | 1 | - |  |
| W-pg-kafka-txn1 | holdout | capacity | pg -> kafka | 4 tables, 64 B, txn 1 | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-s, connect-s, dbzserver-s, conduit-avro-s-aa | 3 | 1 | - |  |
| W-pg-kafka-txn10 | holdout | capacity | pg -> kafka | 4 tables, 64 B, txn 10 | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-s, connect-s, dbzserver-s, conduit-avro-s-aa | 3 | 1 | - |  |
| W-pg-kafka-updheavy | holdout | capacity | pg -> kafka | 1 tables, 64 B, mix 20/70/10 | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-s, connect-s, dbzserver-s, conduit-avro-s-aa | 3 | 1 | - |  |
| W-pg-kafka-wide | holdout | capacity | pg -> kafka | 1 tables, 64 B, wide | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-s, connect-s, dbzserver-s, conduit-avro-s-aa | 3 | 1 | - |  |
| W-pg-pg-txn1 | holdout | capacity | pg -> pg | 4 tables, 64 B, txn 1 | conduit-s, connect-avro-s, dbzserver-avro-s, connect-s, dbzserver-s, conduit-s-aa | 3 | 1 | - |  |
| W-pg-pg-txn10 | holdout | capacity | pg -> pg | 4 tables, 64 B, txn 10 | conduit-s, connect-avro-s, dbzserver-avro-s, connect-s, dbzserver-s, conduit-s-aa | 3 | 1 | - |  |
| W-pg-pg-updheavy | holdout | capacity | pg -> pg | 1 tables, 64 B, mix 20/70/10 | conduit-s, connect-avro-s, dbzserver-avro-s, connect-s, dbzserver-s, conduit-s-aa | 3 | 1 | - |  |
| W-pg-pg-wide | holdout | capacity | pg -> pg | 1 tables, 64 B, wide | conduit-s, connect-avro-s, dbzserver-avro-s, connect-s, dbzserver-s, conduit-s-aa | 3 | 1 | - |  |
| W-pg-http-slowsink | holdout | capacity | pg -> http | 1 tables, 64 B, sink delay 5 ms | conduit-s, dbzserver-s, dbzserver-native-s, conduit-s-aa | 3 | 1 | - | plugin |
| P-pg-kafka-p1-b64 | pilot | capacity | pg -> kafka | 1 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-avro-s@i30, connect-avro-s@i30, dbzserver-avro-s@i30 | 3 | 1 | - |  |
| P-pg-pg-p1-b64 | pilot | capacity | pg -> pg | 1 tables, 64 B | conduit-s, connect-avro-s, dbzserver-avro-s, conduit-s@noseq, connect-avro-s@noseq, dbzserver-avro-s@noseq | 3 | 1 | - |  |
| P-kafka-pg-p1-b64 | pilot | drain | kafka -> pg | 1 partitions, 64 B, backlog 12 GiB (25,067,902 records) | conduit-s, connect-s | 2 | 1 | - |  |

### Smoke plan (short variants, one round)

| cell | plan | mode | source -> dest | shape / workload | arms | rounds | instances | diag | label |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| A1-pg-kafka-ramp | smoke | ramp | pg -> kafka | 1 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-s, connect-s, dbzserver-s, conduit-avro-s-aa, connect-avro-s-aa, dbzserver-avro-s-aa | 1 | 1 | - | primary |
| A2-pg-kafka-p1-b64 | smoke | capacity | pg -> kafka | 1 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-avro-t, connect-avro-t, dbzserver-avro-t, conduit-s, connect-s, dbzserver-s, conduit-avro-s-aa, connect-avro-s-aa, dbzserver-avro-s-aa | 1 | 1 | yes | primary |
| A2-pg-kafka-p4-b64 | smoke | capacity | pg -> kafka | 4 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-avro-t, connect-avro-t, dbzserver-avro-t, conduit-s, connect-s, dbzserver-s, conduit-avro-s-aa, dbzserver-avro-xn-s | 1 | 1 | - |  |
| A2-pg-pg-p1-b64 | smoke | capacity | pg -> pg | 1 tables, 64 B | conduit-s, connect-avro-s, dbzserver-avro-s, conduit-t, connect-avro-t, dbzserver-avro-t, connect-s, dbzserver-s, conduit-s-aa, connect-avro-s-aa, dbzserver-avro-s-aa | 1 | 1 | - | primary |
| A2-pg-pg-p4-b64 | smoke | capacity | pg -> pg | 4 tables, 64 B | conduit-s, connect-avro-s, dbzserver-avro-s, conduit-t, connect-avro-t, dbzserver-avro-t, connect-s, dbzserver-s, conduit-s-aa, dbzserver-avro-xn-s | 1 | 1 | - |  |
| A2-pg-s3-p1-b64 | smoke | capacity | pg -> s3 | 1 tables, 64 B | conduit-s, connect-s@f1k, connect-s@f10k, connect-s@f100k, conduit-t, connect-t, conduit-s-aa | 1 | 1 | - |  |
| A2-pg-file-p1-b64 | smoke | capacity | pg -> file | 1 tables, 64 B | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa | 1 | 1 | - | reference |
| A2-pg-http-p1-b64 | smoke | capacity | pg -> http | 1 tables, 64 B | conduit-s, dbzserver-s, conduit-t, dbzserver-t, dbzserver-native-s, conduit-s-aa | 1 | 1 | - | plugin |
| B-kafka-pg-p1-b64 | smoke | drain | kafka -> pg | 1 partitions, 64 B, backlog 0 GiB (522,247 records) | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa, connect-s-aa | 1 | 1 | - | primary |
| B-kafka-s3-p1-b64 | smoke | drain | kafka -> s3 | 1 partitions, 64 B, backlog 0 GiB (522,247 records) | conduit-s, connect-s@f1k, connect-s@f10k, connect-s@f100k, conduit-t, connect-t, conduit-s-aa, connect-s-aa@f10k | 1 | 1 | - | primary |
| B-kafka-file-p1-b64 | smoke | drain | kafka -> file | 1 partitions, 64 B, backlog 0 GiB (522,247 records) | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa | 1 | 1 | - | reference |
| C-file-kafka-b64 | smoke | drain | file -> kafka | 1 partitions, 64 B, backlog 0 GiB (2,855,696 records) | conduit-s, connect-s, conduit-t, connect-t, conduit-s-aa | 1 | 1 | - | reference |
| D-pg-kafka-transform | smoke | capacity | pg -> kafka (transform) | 1 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-avro-t, connect-avro-t, dbzserver-avro-t, conduit-avro-s-aa | 1 | 1 | - |  |
| E-pg-fanout-file-s3 | smoke | capacity | pg -> file+s3 | 1 tables, 64 B | conduit-s, connect-s@f10k, conduit-t, connect-t, conduit-s-aa | 1 | 1 | - |  |
| F-pg-kafka-snapshot | smoke | snapshot | pg -> kafka | 500,000 rows, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-avro-t, connect-avro-t, dbzserver-avro-t, conduit-s, connect-s, dbzserver-s, conduit-avro-s-aa | 1 | 1 | - | primary |
| F-pg-pg-snapshot | smoke | snapshot | pg -> pg | 500,000 rows, 64 B | conduit-s, connect-avro-s, dbzserver-avro-s, conduit-t, connect-avro-t, dbzserver-avro-t, connect-s, dbzserver-s, conduit-s-aa | 1 | 1 | - |  |
| G-pg-kafka-kill9 | smoke | recover | pg -> kafka | 1 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, connect-avro-e, dbzserver-avro-e | 1 | 1 | - |  |
| G-pg-pg-kill9 | smoke | recover | pg -> pg | 1 tables, 64 B | conduit-s, connect-avro-s, dbzserver-avro-s, connect-avro-e, dbzserver-avro-e | 1 | 1 | - |  |
| G-pg-kafka-kill9-snapshot | smoke | recover-snapshot | pg -> kafka | 500,000 rows, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, connect-avro-e, dbzserver-avro-e | 1 | 1 | - |  |
| H-pg-kafka-verify | smoke | correct | pg -> kafka | 1 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-avro-t, connect-avro-t, dbzserver-avro-t, conduit-s, connect-s, dbzserver-s | 1 | 1 | - |  |
| H-pg-pg-verify | smoke | correct | pg -> pg | 1 tables, 64 B | conduit-s, connect-avro-s, dbzserver-avro-s, conduit-t, connect-avro-t, dbzserver-avro-t, connect-s, dbzserver-s | 1 | 1 | - |  |
| H-pg-s3-verify | smoke | correct | pg -> s3 | 1 tables, 64 B | conduit-s, connect-s@f1k, connect-s@f10k, connect-s@f100k, conduit-t, connect-t | 1 | 1 | - |  |
| H-pg-file-verify | smoke | correct | pg -> file | 1 tables, 64 B | conduit-s, connect-s, conduit-t, connect-t | 1 | 1 | - |  |
| H-pg-http-verify | smoke | correct | pg -> http | 1 tables, 64 B | conduit-s, dbzserver-s, conduit-t, dbzserver-t, dbzserver-native-s | 1 | 1 | - |  |
| H-kafka-pg-verify | smoke | correct | kafka -> pg | 1 partitions, 64 B | conduit-s, conduit-t, connect-s, connect-t | 1 | 1 | - |  |
| H-kafka-s3-verify | smoke | correct | kafka -> s3 | 1 partitions, 64 B | conduit-s, conduit-t, connect-s@f10k, connect-t | 1 | 1 | - |  |
| H-kafka-file-verify | smoke | correct | kafka -> file | 1 partitions, 64 B | conduit-s, conduit-t, connect-s, connect-t | 1 | 1 | - |  |
| H-file-kafka-verify | smoke | correct | file -> kafka | 1 partitions, 64 B | conduit-s, conduit-t, connect-s, connect-t | 1 | 1 | - |  |
| H-pg-kafka-transform-verify | smoke | correct | pg -> kafka (transform) | 1 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s | 1 | 1 | - |  |
| H-pg-fanout-file-s3-verify | smoke | correct | pg -> file+s3 | 1 tables, 64 B | conduit-s, connect-s@f10k | 1 | 1 | - |  |
| H-pg-pg-p4-verify | smoke | correct | pg -> pg | 4 tables, 64 B | conduit-s, connect-avro-s, dbzserver-avro-s, conduit-t, connect-avro-t, dbzserver-avro-t | 1 | 1 | - |  |
| H-pg-kafka-p4-verify | smoke | correct | pg -> kafka | 4 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-avro-t, connect-avro-t, dbzserver-avro-t | 1 | 1 | - |  |
| H-pg-kafka-wide-txn1-verify | smoke | correct | pg -> kafka | 1 tables, 64 B, wide, txn 1 | conduit-avro-s, connect-avro-s, dbzserver-avro-s | 1 | 1 | - |  |
| W-pg-kafka-txn1 | smoke | capacity | pg -> kafka | 4 tables, 64 B, txn 1 | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-s, connect-s, dbzserver-s, conduit-avro-s-aa | 1 | 1 | - |  |
| W-pg-kafka-wide | smoke | capacity | pg -> kafka | 1 tables, 64 B, wide | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-s, connect-s, dbzserver-s, conduit-avro-s-aa | 1 | 1 | - |  |
| W-pg-http-slowsink | smoke | capacity | pg -> http | 1 tables, 64 B, sink delay 5 ms | conduit-s, dbzserver-s, dbzserver-native-s, conduit-s-aa | 1 | 1 | - | plugin |
| P-pg-kafka-p1-b64 | smoke | capacity | pg -> kafka | 1 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s, conduit-avro-s@i30, connect-avro-s@i30, dbzserver-avro-s@i30 | 1 | 1 | - |  |

### Shakedown (wiring only)

| cell | plan | mode | source -> dest | shape / workload | arms | rounds | instances | diag | label |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| A2-pg-kafka-p1-b64 | shakedown | capacity | pg -> kafka | 1 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s | 1 | 1 | yes | primary |
| A2-pg-pg-p4-b64 | shakedown | capacity | pg -> pg | 4 tables, 64 B | connect-avro-s, dbzserver-avro-s, dbzserver-avro-xn-s | 1 | 1 | - |  |
| G-pg-kafka-kill9 | shakedown | recover | pg -> kafka | 1 tables, 64 B | conduit-avro-s, connect-avro-s, dbzserver-avro-s | 1 | 1 | - |  |

## 8. Load modes

- **ramp** (A1): held at 20,000 ops/s for 180 s (warm-up, excluded), then
  20,000, 40,000, 50,000, 70,000, 100,000, 140,000 and 200,000 ops/s, 120 s
  each: 60 s to settle, 60 s measured, ending one sample before the sampler
  first sees the next step. A step is TRACKED when the sink rate is at least
  97% of the rate the loader achieved, the lag grew by at most 2% of the
  window's offered events, AND the lag at the window's end is under 5 s of
  offered load. A step where the loader achieved under 97% of its target is
  LOADER-BOUND (no verdict). Loader round trips are 100 statements here (a
  5 ms floor on marker-to-arrival latency at 20,000 ops/s).
- **capacity** (A2, D, E, W): the loader writes as fast as Postgres accepts.
  180 s warm-up, 300 s window. Valid only if the arm's lag grew in the window
  (saturated). An arm that kept up is a LOWER BOUND (`>= loader rate`),
  reported as such, and enters the verdict as one (section 10). The loader
  feeds one table through one connection (per-table order), so a one-table
  cell has a ceiling; the ramp and the multi-table cells show where it is.
- **drain** (B, C): a backlog written before the pipeline starts, sized in
  BYTES (12 GiB for Kafka sources, 8 GiB for file), filled ONCE per cell and
  read by every run with its own consumer group / connector name. 180 s
  warm-up, 300 s window; if the backlog runs out first, the run counts with at
  least 120 s of window (flagged short), else it is invalid. The pilot
  measures fill and drain rates; the sizes are revised from it (change log)
  before any flagship run.
- **snapshot** (F): 10 M rows loaded before the pipeline starts; time from
  first record to the last landed.
- **correct** (H): 3 M ops at the loader's maximum rate (Postgres sources) or
  a 3 M-record backlog; wait until the sink is still for 60 s and the loader is
  done; verify every record.
- **recover** (G): the loader is PACED at 20,000 ops/s (every side tracked it
  on 2026-10-26) for 6 M ops; when the sink has 30% of the expected events,
  `docker kill -s KILL` the SUT and start it at once; the loader keeps
  running; at the end verify every record. **recover-snapshot**: the same
  kill at 30% of a 10 M-row snapshot. Shipped and `e` tiers. G kills
  mid-stream and mid-snapshot only; kill mid-batch and mid-checkpoint are
  covered by `tests/chaos` for Conduit, not compared here.
- **idle** (W): 1 op/s for 300 s: CPU and memory of a pipeline with no traffic.

## 9. Metrics and counting

All ground truth at the sink or the host; nothing from an engine's metrics.
Nothing the sampler does during a window depends on how much an arm wrote:

| metric | how |
| --- | --- |
| throughput | delta of the sink count over the window, each reading stamped at its own read midpoint (interpolated). File: new bytes since the last read. Postgres: per destination table `max(seq)+1`, `seq` being the source op index (a generated column decoded from the payload marker, indexed): one index probe per table, in source ops like every other sink. Kafka: end offsets. S3: NOT read during the window; after the run every object is indexed and the series rebuilt from LastModified. HTTP: the receiver's count |
| offered | the loader's committed ops from `/progress` |
| lag | offered minus delivered at the window's edges; replication slot lag in bytes every sample |
| latency p50 / p99 / p99.9 | tracked ramp steps only (a saturated system's latency is its queue). Exact, from per-second histograms (2% buckets) built after the run: every Kafka record (broker LogAppendTime minus marker), every Postgres row's `landed_at` trigger (ramp cells only; a final-state sample), every HTTP event at the receiver, every S3 line (1 s resolution). File: not measured |
| CPU-s per million events | cgroup CPU over the window / events: SUT, footprint, destination |
| peak RSS | max cgroup anonymous memory (page cache excluded) |
| startup to first record | first sink record minus the tool-start hook |
| bytes per record, producer batches | bytes over records of every Kafka record or S3 line; per topic, the broker's log: batches, records per batch, codec (`kafka-dump-log` after the run) |
| GC pauses | JVM `-Xlog:gc*`, Go `gctrace`, every run, every side |
| bottleneck | per run: SUT CPU-bound (>= 3.6 of 4 cores or throttled), SUT waiting (with the busiest infrastructure container's cores), loader-bound, or not saturated |
| stationarity | per run: first vs second half of the window within 5%, and the CV of 30 s sub-window rates; runs that fail are reported, not dropped |
| correctness | `loader verify`: missing, duplicate, malformed, reordered, unattributed, per kind; Kafka read as key + value (tombstones attributed by key); Postgres: final state |
| recovery | time from restart to the next delivered record; loss and duplicates from verify; delivered count at the kill |

The pilot runs the counting-overhead control: the same arms with the sampler
at 5 s and at 30 s; if their rates differ by more than the effective floor,
the counter is not free and the change log records what replaces it.

## 10. Analysis rules (replaces the first version's rule)

`analyze.py`, fixed now:

- **Effective floor** for a pair = max(5% pre-registered minimum; the A/A
  floor of EACH side that has a twin in the cell, max per-round
  `|arm / arm-aa - 1|`; half the run-to-run range of either arm). A cell with
  no twin borrows its family's floor, doubled (at least 10%).
- **Label**, per pair, from the per-round ratios (same round, adjacent runs):
  RESOLVED if `|median ratio - 1|` > floor and ALL rounds have the same sign;
  SUGGESTIVE if above the floor and all but one round agree; else
  UNRESOLVED. Reported with the median ratio, its range and the exact sign
  count. **This is a consistency label, not a significance test**: with 5
  rounds the smallest two-sided sign-test p is 0.0625. No bootstrap is used
  (on 5 values its 95% CI is the range).
- **Lower bounds**: if one arm kept up with the unlimited loader (valid capacity
  unknown, at least the loader's rate) and the other saturated below that, in
  at least 4 rounds, the pair is LOWER-BOUND (`a >= kx b`), not UNRESOLVED.
- **Ramp**: ordinal. Each round's highest tracked step; RESOLVED (ordinal) if
  one arm is higher in every round, SUGGESTIVE if in all but one. No ratio.
  The delivered rate at the first untracked step is reported beside it.
- **Cross-instance**: a cell run on several instances keeps a label only if
  every instance gives the same label and the same sign; otherwise UNRESOLVED
  (instances disagree). Flagship cells run on 3 instances (A2-pg-kafka) or 2
  (A2-pg-pg).
- **Correctness gates speed**: an arm with any lost, malformed, unattributed or
  (on an ordered sink) reordered record fails the cell, whatever its speed.
  Duplicates are allowed (at-least-once) and reported per million.
- **Primary comparisons** (at most 8, `cells.PRIMARY`):

| id | cell | Conduit | vs |
| --- | --- | --- | --- |
| P1 | A2-pg-kafka-p1-b64 | conduit-avro-s | connect-avro-s |
| P2 | A2-pg-kafka-p1-b64 | conduit-avro-s | dbzserver-avro-s |
| P3 | A2-pg-pg-p1-b64 | conduit-s | connect-avro-s |
| P4 | A2-pg-pg-p1-b64 | conduit-s | dbzserver-avro-s |
| P5 | B-kafka-pg-p1-b64 | conduit-s | connect-s |
| P6 | B-kafka-s3-p1-b64 | conduit-s | the best of connect-s@f1k / @f10k / @f100k, per round |
| P7 | A1-pg-kafka-ramp | conduit-avro-s | connect-avro-s (ordinal) |
| P8 | F-pg-kafka-snapshot | conduit-avro-s | connect-avro-s (land time) |

  **Multiplicity**: about 150 other pairs exist across the matrix; they are
  exploratory. No headline, summary or claim is drawn from an exploratory
  pair, a reference cell or a plugin cell. At an expected false-label rate of a
  few percent per pair, several exploratory RESOLVED labels are expected by
  chance.

## 11. Diagnosis

Every base capacity and drain cell has one extra, unmeasured diag run per arm.
Measured runs take no profile and no scrape on any side.

- Conduit (a `-diag` build of the same commit, never the measured binary,
  built from the SAME repository Dockerfile, base image, Go version, make
  target, ldflags and -trimpath, adding only the pprof file and
  `-tags benchdiag` (`images/diag-dockerfile.py`, which fails if the build
  line changes); pprof served on the bench network, mutex sampling 1/10,
  block 100 us; profiles from it are indicative of the measured binary, the
  same code differing only by the sampling it enables): a
  60 s CPU profile from the window's start, mutex and block profiles at the
  window's edges (their difference is the window), a 10 s execution trace
  and a goroutine dump mid-window (off-CPU), the engine's metrics polled every
  10 s (per-stage histograms as a series, GC CPU fraction, allocation rate).
- JVM sides: async-profiler 4.6 itimer then wall clock, each for half the
  window; Jolokia polled every 10 s (Debezium streaming metrics: queue
  remaining capacity, ms behind source; Connect source and sink task metrics;
  producer batch-size-avg, records-per-request-avg, request-latency-avg;
  consumer fetch metrics; GC, heap).
- Every run: pg_stat_statements and pg_stat_user_tables (statements, rows per
  call, time), WAL stats, broker producer batches, the bottleneck label.
- `GAP-ANALYSIS.md`: per losing cell, the bottleneck label, the stage from
  the metrics series, the profile frames for the window, a candidate arch-v2
  fix, an estimated gain, and an A/B experiment id that will confirm it.
  Guesses are marked as guesses.

## 12. Spend order, gates and cost

`cells.py estimate --plan P` (c6i.4xlarge, $0.848/h; run lengths are
estimates until the pilot):

Prices (amendment 3): spot c6i.4xlarge in us-west-1, $0.28-0.29/h over
2026-10-27/28 (`aws ec2 describe-spot-price-history`, Linux/UNIX), estimated at
$0.29/h, plus about $0.23/h of EBS per instance (three gp3 data volumes at 6000
IOPS / 500 MB/s, root at baseline): **$0.52 per instance-hour**. On-demand
fallback: $0.848 + $0.23 = $1.08/h. The table below is at $0.52/h.

| step | plan | what | runs | instance-h | cost | wall time |
| --- | --- | --- | --- | --- | --- | --- |
| 0 | shakedown | the least-proven wiring paths, smoke length, 1 instance: wiring only, never results or a gate | - | - | < $5 per pass | 1-2 h |
| 1 | smoke | every H and G cell, plus one cell per family and side, 1 round, short (harness validation only) | 245 | 34 | $18 per pass | 17 h on 2, 9 h on 4 |
| 2 | pilot | after the SDK v0.18.1 connectors ship | 54 | 11 | $6 | 6 h on 2 |
| 3 | flagship | the 6 cells of the 8 primary comparisons, 5 rounds, each on 2 instances | 761 | 169 | $88 | 44 h on 4 |
| 4 | core (trimmed) | the other base cells, 3 rounds | 406 | 93 | $48 | 24 h on 4 |
| 5 | holdout (trimmed) | shape sweeps 3 rounds; workload variants trimmed (amendment 3) | 1,008 | 195 | $101 | 51 h on 4 |
| 2 | pilot | 1 instance: per-arm noise, loader ceiling, WAL growth, fill and drain rates, counting-overhead control | 30 | 6 | $5 | 6 h |
| 3 | flagship | A1-pg-kafka-ramp, A2-pg-kafka (3 instances), A2-pg-pg (2), B-kafka-pg, B-kafka-s3, F-pg-kafka, G-pg-kafka | 664 | 141 | $120 | 71 h on 2, 39 h on 4 |
| 4 | core | the rest of the base cells | 548 | 127 | $108 | 64 h on 2, 32 h on 4 |
| 5 | holdout | shapes and workload variants: mandatory before any public claim | 1,908 | 369 | $313 | 187 h on 2, 94 h on 4 |

Gates, in code: `aws/launch.sh` AND the Terraform configuration itself (a
`precondition` on the instances) refuse any plan after smoke without the
`gate.json` of a PASSING smoke run for the same harness hash, and each
instance refuses a kit whose hash is not the declared one (every H and G
run verified clean, every run exited 0 and delivered a record, every arm ran;
this is where the Connect file-sink format, the Debezium Server JDBC sink,
the Avro paths and the reWriteBatchedInserts fallback are proven). In
judgment, recorded in the change log: the flagship starts only after the
pilot's numbers revise the backlog sizes, the loader ceiling and the minimum
floor if they need it, and the pilot's `P-pg-pg-p1-b64` shows the destination
is not saturated (Postgres CPU and the pg volume, from the bottleneck label)
for the primary Postgres-destination arms, with and without the `seq` column
(`@noseq` arms count from `pg_stat_user_tables` instead): if it is, the
primary pg-to-pg label reads "destination-bound" and the change log says what
replaces the column. The 32-vCPU quota allows 2 x c6i.4xlarge at a time.

## 13. Known asymmetries (by design, printed with the results)

- Connect writes through a broker for file, Postgres and S3 destinations;
  Conduit and Debezium Server do not. Architecture, measured; the footprint
  column carries the broker, and "includes a durable log" is printed next to
  every Connect number for those destinations.
- Record bytes differ on the exploratory JSON arms (OpenCDC record vs the
  Debezium envelope). On the primary Avro arms the content is equal (section
  2); the encodings still differ in schema ids and field order. Bytes per
  record and producer batch statistics are reported for every arm.
- The Kafka-source cells are Connect's home turf and use Connect-format
  envelopes as input; Conduit needs `unwrap.kafkaconnect` for Postgres.
- Conduit's Kafka source has no fetch-size knob; the tuned Connect consumer
  does.
- The Postgres destinations write differently: Conduit's builtin:postgres, the
  Confluent JDBC sink, Debezium's JDBC sink (with its reduction buffer in the
  tuned tier: fewer writes for the same final state).
- The transform cell: Debezium's unwrap emits a tombstone for a delete,
  Conduit an empty value; both are one record per event and are verified by
  key.

## 14. Limitations

- **No engine-level stage gauges.** Busy ratio and queue depth per funnel stage
  would need an engine change; this matrix uses the per-stage histograms (as a
  series), the execution trace and block/mutex profiles instead. Adding the
  gauges is a separate Conduit PR, not part of the measured binary.
- **async-profiler in itimer/wall mode**, without perf_events: no kernel
  frames, no hardware counters (no extra container privileges).
- **Network**: the send side of container networking runs on the sender's
  cores; host networking was not used. Bytes on the wire are reported.
- **File-sink latency** is not measured. S3 latency has 1 s resolution.
  Postgres latency comes from the last write of each row (a sample biased
  against frequently updated rows) and only in ramp cells.
- **Postgres final-state check**: a lost INTERMEDIATE update is invisible to it.
- **The `seq` column** is a frontier, not an event count: trailing deletes do
  not advance it, and the tuned Debezium JDBC reduction buffer skips
  intermediate updates yet reaches the same frontier. It adds a generated
  column and an index write to every destination write on every side; the
  pilot measures that cost (`@noseq`).
- **Spot instances**: a spot interruption terminates the instance; the cell
  that was running is discarded and rerun from the start on a new instance
  (`aws/resume.sh`; cells already DONE in S3 are kept), so a cell's rounds
  always come from one instance. Which market each launch used is recorded in
  the bucket. Spot hosts may differ in noise from on-demand ones; steal is
  recorded per run.
- **Per-run image pins**: quay.io rebuilds the Debezium 3.7.0.Final images on
  refreshed bases and deletes superseded manifests, so the digests are
  resolved once per run (`images/pin.py`) and recorded, not fixed in the
  repository; two runs a day apart may use different base-image builds of the
  same Debezium version.
- **Debezium Server image additions**: the stock 3.7.0.Final Server image
  lacks the Apicurio Avro converter and Debezium's own SMT module
  (`debezium-connect-plugins`, where ExtractNewRecordState moved in 3.7); the
  matrix image adds both, at the versions Debezium's own 3.7 images use.
- **Gates are local files and Terraform preconditions**: someone with the
  repository can bypass them; they stop accidents, not intent. Pilot to
  flagship is a recorded judgment, not a machine gate.
- **One broker, one Postgres, one instance per run**: no replication, no
  network between components. No TLS. No multi-node Connect. No Conduit
  multi-instance. No Protobuf.
- **5 rounds**: labels are consistency labels (section 10), not tests.
- **Shared tenancy**: c6i is virtualized; steal is recorded per run, not
  prevented. `.metal` and dedicated tenancy were not chosen (cost); the
  flagship's cross-instance repeats carry the host-variance question.
- **Untested end to end before smoke**: compiled and unit-tested only (no
  Docker run anywhere outside AWS). The smoke run is the first execution; its
  fixes go in the change log before the pilot.


## 15. Change log

| date | change | reason | data seen before the change? |
| --- | --- | --- | --- |
| 2026-10-27 | plan written (commit c49b1986) | | no |
| 2026-10-27 | amended after the adversarial methodology review (`bench-methodology-review.md`); every item and its resolution below | review | no |
| 2026-10-27 | amended after review round 2 (conditional no-go); tag matrix-plan-v3; items below | review | no |
| 2026-10-28 | amendment 3: trim, spot, sequencing (below) | budget and Conduit release sequencing, decided by DeVaris | no (shakedown runs only: wiring, no results) |
| 2026-11-02 | amendment 4: headers are part of equal content; the Debezium sides drop their default headers; the smoke gate checks the wire (section 2) | shakedown wire check | no (shakedown runs only: wiring, no results) |

### Review items and resolutions

Blockers:

| item | resolution |
| --- | --- |
| B1 recovery confounded with arm speed; timers compared at defaults only | Fixed. The loader is paced at 20,000 ops/s for 6 M ops; the kill fires when the SINK has 30% of the expected events, with the loader still running (`run-cell.sh run_recover`). A recovery tier `e` flushes positions every 1 s on every side and sets `scheduled.rebalance.max.delay.ms=0` for Connect; both tiers are reported (H10). A mid-snapshot kill cell was added (G-pg-kafka-kill9-snapshot). Mid-batch and mid-checkpoint kills are not compared across systems; stated in section 8 (covered for Conduit by `tests/chaos`) |
| B2 Connect pg-to-file writes a Java Map; correctness cells not in smoke; Server JDBC schemas | Fixed. Connect file sinks use StringConverter on the sink side (the source's JSON passes through), asserted by `test_connect_file_sink_writes_json_not_a_java_map`. Debezium Server's JSON arm into Postgres runs with `schemas.enable=true`; its Avro arm uses the registry (`test_jdbc_sinks_get_schemas`). Every H and G cell is in smoke (`test_smoke_contains_every_correctness_and_recovery_cell`), and a passing smoke gate is enforced by `launch.sh` before any later plan. Conduit's S3 `json` format is one OpenCDC object per line (connector source, `destination/format/format.go`) |
| B3 JSON-with-schema strawman; no Avro + registry | Fixed. Avro through Apicurio Registry 3.3.3 is the PRIMARY serialization for the Debezium sides into Kafka and Postgres (Connect: Apicurio AvroConverter; Server: Avro format), with Conduit's `avro.encode` against them into Kafka; the JSON arms remain as exploratory columns. Bytes per record and producer batch statistics are reported for every arm |
| B4 rotation is not a Latin square | Fixed. Per round, a seeded shuffle with the primary arms in one adjacent block, rotated and reversed on alternate rounds (AB/BA) (`cells.round_order`, `test_round_order_...`). Steal and SUT CPU frequency are recorded per run. `.metal` / dedicated tenancy declined (cost); the flagship's 3-instance repeat addresses host variance (section 14). conduit-classic dropped to one cell (fewer arms per round) |
| B5 RESOLVED weaker than it reads; floor not valid | Fixed (section 10 rewritten). Effective floor = max(5% minimum, every side's A/A twin, half the pooled range); borrowed floors doubled; JVM-side twins in every flagship cell; bootstrap dropped, exact sign counts reported, the label called a consistency label; ramp ordinal; 8 primary comparisons named, the rest exploratory, multiplicity stated |
| B6 the counter loads the box and skews timestamps | Fixed. Every reading carries its own read-midpoint time and rates interpolate on it. Postgres progress is `max(seq)` through an index (no count(*), and it counts source ops, so Postgres destinations are comparable with the other sinks; the old net-rows metric is gone). S3 is not read during windows (post-run index). Latency moved to post-run exact histograms. The 5 s vs 30 s counting-overhead control is a pilot cell |
| B7 backlog sizing, per-run refill, fast arms invalidated | Fixed. Backlogs are sized in bytes with the measured envelope overhead (~450 B + payload per record), filled ONCE per cell (`fill-kafka` is idempotent; the broker volume persists across a drain cell's runs; every run uses its own consumer group and connector names). A backlog that runs out inside the window still counts with >= 120 s of window (flagged). Broker CPU and disk IO are reported; the pilot measures fill and drain rates before the flagship |
| B8 workload realism | Fixed. Payload padding is random alphanumerics written in pairs (about 2.3x compressible, pinned by a test). Transaction size is a loader parameter with hold-out cells at 1, 10, 100 and 10,000 statements; update-heavy and delete-heavy mixes; a wide 30-column typed shape; 16 KiB rows TOAST; Zipf skew over 16 tables; schema evolution mid-run; idle; slow sink. Postgres runs on EBS gp3 with fsync on for EVERY cell (not one); `max_slot_wal_keep_size` 40 GB. The hold-out plan is mandatory before any public claim. The wrong comment about autocommit in the loader was corrected (a pgx Batch is one implicit transaction) |

Majors:

| item | resolution |
| --- | --- |
| M1 tuned tiers asymmetric, knobs missing, shipped not knob-free | Fixed: one concept table (`cells.TUNED`) for every side, checked by `test_tuned_tier_uses_the_same_values_on_every_side`. Added consumer fetch sizes (Connect), JDBC `reWriteBatchedInserts` (with the pre-registered fallback), Debezium JDBC `batch.size` and `use.reduction.buffer`, Debezium `snapshot.fetch.size`, Conduit pgx `cache_statement` (pre-registered openly, with why). `rotate.interval.ms` removed from the shipped S3 sink; `flush.size` (no default) swept at 1k/10k/100k. GOGC/GOMEMLIMIT stated as not tuned. Declined: Conduit has no Kafka fetch knob and no per-pipeline arch-v2 destination batch knob; reported as asymmetries |
| M2 parity leaks, destination cost invisible, $/billion | Fixed: dockerd/containerd pinned to infra cores by systemd before start, the harness by taskset, RPS on veths; destination CPU charged per arm; footprint is the cross-architecture headline; $/billion removed. Host networking declined (would change every connection setting); the send-side residual is a stated limitation |
| M3 delivery/durability asserted | Fixed: section 4 is a per-side table with sources, and states what "written" means for every sink (no fsync on file sinks, RF 1, MinIO on a volume, Postgres fsync on). Fan-out reframed as architecture. Transform: deletes are now verified (Kafka read as key + value; `-no-deletes` removed); `ok` now fails on any unattributed record |
| M4 comparators that cannot carry a headline | Fixed: file cells are "reference connector" appendix cells, HTTP cells "labs plugin" appendix cells, both excluded from summaries. Debezium Server's scale-out is its own row (`dbzserver-avro-xn`: one process per table in one container) in the multi-table cells |
| M5 coverage and the one-connector-per-table idiom | Fixed: shipped tier = one pipeline / connector / process with a table list; one-per-table is tuned. Added shapes listed under B8. Declined: TLS (out of scope, stated), multi-node Connect, slow-sink backpressure beyond HTTP |
| M6 measurement validity | Fixed: stationarity check per run (halves within 5%, 30 s CV), reported not dropped; a 5 s absolute lag cap on "tracked"; latency only from tracked ramp steps and exact from post-run histograms; the ramp loader batch is 100 (5 ms floor, stated); no scrapes in measured runs on any side (diag runs only) |
| M7 statistics and forking paths | Fixed: flagship cells on 3 (pg-kafka) and 2 (pg-pg) instances with an all-instances-agree rule; the plan commit is tagged and the harness hash recorded per run and checked by `analyze.py`; hypotheses carry predictions and falsification thresholds (section 1); lower bounds are reported instead of hidden |
| M8 diagnostic value | Fixed with a diag-only build: window-scoped CPU, mutex, block, trace and goroutine profiles; metrics as a 10 s series; async-profiler wall clock as well as CPU; Jolokia for Debezium, Connect task, producer and consumer metrics; pg_stat_statements, WAL stats and broker batch statistics every run; a per-run bottleneck label; the GAP-ANALYSIS template requires an A/B experiment id. Declined here: engine stage busy-ratio and queue-depth gauges (an engine change; a separate PR, section 14) |
| M9 spend order | Adopted: smoke (all H, all G, one per family) -> one-instance pilot -> flagship -> core -> hold-out, with the smoke gate enforced in code (section 12) |

Minors: conduit-classic in one continuity cell only (`test_classic_runs_in_one_cell_only`);
dbzserver-native kept in the HTTP appendix cells only; the "same bytes" claim
removed and bytes per record reported; the price default and $/billion
removed; the GOMAXPROCS reason corrected; broker version and its 4 GiB heap
stated; Docker pinned to an exact apt version, and THP, swappiness,
`vm.dirty_*`, clocksource and dockerd's affinity recorded; the stuck-run cap
cut from 60 to 20 minutes past plan; `ok` includes unattributed records and the
verify summary line carries duplicates per million; the Postgres final-state
limit stated; bounded log driver on every container; the loader's SHA-256, the
kit commit, the harness hash and the analysis version are recorded in
`environment.txt` and `analysis.json`.

### Review round 2 items and resolutions

| item | resolution |
| --- | --- |
| R2-B1 Avro arms do not carry the same content | Fixed: one equal-content definition for every Avro arm into Kafka, the flattened row plus `__op`, deletes as tombstones (section 2); Debezium arms run ExtractNewRecordState with `add.fields=op`, Conduit sets `__op` and encodes the row; one subject per table on every side. The full-envelope comparison stays exploratory (JSON arms). `test_avro_arms_carry_equal_content` |
| R2-B2 one shared gp3 root volume | Fixed: separate provisioned gp3 volumes for Postgres (data + WAL), the Kafka log and MinIO, Docker root on the root volume; per-device utilization, await and MB/s per run (`/proc/diskstats`, `iostat -dxm`); "disk-bound" in the bottleneck label (section 5) |
| R2-M1 the `seq` column's per-write cost | Pilot cell `P-pg-pg-p1-b64` runs the primary pg-to-pg arms with and without it (`@noseq`, counted from `pg_stat_user_tables`); the bottleneck label reads "destination-bound" when Postgres is saturated; the frontier semantics are stated (section 14) |
| R2-M2 multi-table paths untested by smoke; tombstones attributed by key only | Fixed: smoke adds A2-pg-pg-p4-b64, H-pg-pg-p4-verify and H-pg-kafka-p4-verify; verify attributes a tombstone's table from its topic (`TestVerifyMultiTableTombstonesUseTheTopic`) |
| R2-M3 the diag binary is built differently | Fixed: generated from the repository Dockerfile with only the pprof file and `-tags benchdiag` added to the same `make build` (`images/diag-dockerfile.py`); profiles labelled indicative |
| R2-M4 gate bypass, smoke cost, registry unproven | The gate is also a Terraform precondition, and each instance checks its kit's hash against the declared one; the smoke budget is $60-90 for the expected 2-3 passes, preceded by a < $5 shakedown; the registry is named by the bottleneck label when it is the busiest infrastructure container; Conduit uses one subject per table like the Apicurio converter |
| minors | Diag runs expose the API and Jolokia on 0.0.0.0 inside an egress-only security group (stated); the empty `CONDUIT_API_TOKEN_FILE` in measured runs is checked by the shakedown |

### Amendment 3 (2026-10-28, before any result)

| change | detail |
| --- | --- |
| sequencing | smoke validates the harness only; pilot, flagship, core and hold-out wait until Conduit main ships the SDK v0.18.1 connectors with default source batching |
| rounds and instances | 5 rounds and a second-instance repeat only for the cells of the 8 primary comparisons (A1-pg-kafka-ramp, A2-pg-kafka-p1-b64, A2-pg-pg-p1-b64, B-kafka-pg-p1-b64, B-kafka-s3-p1-b64, F-pg-kafka-snapshot); every other measured cell 3 rounds on one instance (labels in those cells need all 3 rounds to agree; they are exploratory). Correctness cells keep 1 round per arm, pilot cells their own counts. A2-pg-kafka-p1-b64 goes from 3 instances to 2 |
| conduit-classic | dropped everywhere, including the A2-pg-kafka continuity arm (not a primary comparison; perf work is arch-v2 only). H1 is withdrawn untested |
| hold-out | the workload variants are trimmed to small transactions (1 and 10 statements per transaction), update-heavy, wide typed rows and the slow sink. Deferred (designed, not scheduled): 100- and 10,000-statement transactions, delete-heavy, Zipf skew over 16 tables, the mid-run schema change, idle. The shape sweeps (parallelism 4 and 16, payloads 1 KiB and 16 KiB) stay, at 3 rounds |
| market | spot instances, on-demand only after two spot capacity failures; an interrupted cell reruns from the start (section 14) |
| estimate | section 12, at $0.52 per instance-hour (spot plus EBS) |

### Shakedown findings (wiring, before any result)

| finding | fix |
| --- | --- |
| macOS tar added `._*` files to the kit; the instance's hash refused it | tar without macOS metadata; the hash ignores `._*` |
| results synced per cell: a hung run would lose a cell | every run is published as it ends; live view every 60 s |
| `timeout` put benchi outside the pty's foreground group: its UI stopped on SIGTTIN and every run hung | `timeout --foreground -k 60` |
| the instance ran the smoke arm list for the shakedown | runs use the plan's own cells (`MATRIX_PLAN`) |
| Apicurio 3.3.3 never became healthy on its health paths | readiness = the registry API answers |
| Connect: plugin directories owned by root; the Apicurio converter setting could not link into them | owned by the kafka user |
| Conduit templates compared the operation enum with a string; and a template variable `$op` was expanded by Conduit as an environment variable | `printf "%v" .Operation`, no template variables |
| Debezium Server: a directory mount hid the image's config files | mount only application.properties |
| Debezium Server image: no Apicurio converter, no `debezium-connect-plugins` (ExtractNewRecordState) | added to the matrix image (section 14) |
| quay.io deleted the pinned Debezium manifests within a day | per-run pins (section 14) |
| Debezium 3.7 removed `snapshot.mode=never` | `no_data` |
| Debezium Server JDBC: `${topic}` expanded by Quarkus config | escaped; and Debezium Server copies the already-expanded sink values into its offset-storage and schema-history namespaces, where `${topic}` is expanded again and fails startup: one inert key in each namespace stops the copy |
| Debezium Server scale-out arm: each process started in a subshell, so `wait` had nothing to wait for and the container exited at once | each process is a direct background child; one GC log per JVM |
| Connect JDBC sink on a `topics.regex`: it saw the CDC topics only at its 5-minute metadata refresh, after every run's first-record timeout | the topics are named |
| the scale-out arm's entrypoint `$` interpolated by compose | `$$` |
| Conduit: `avro.encode` leaves the source schema metadata; the destination re-decodes the Avro bytes and fails every record | `sdk.schema.extract.payload.enabled=false` on that destination; reported as a Conduit defect |
| recovery runs without a first record waited out every timeout | aborted at once, recorded as failed |
| per-run diag API tokens were synced to the bucket | every sync excludes them |
| async-profiler was given `jps`, which the JVM images do not ship | the JVM's PID from /proc |
| EC2 rejects `instanceInitiatedShutdownBehavior` on spot instances | set only on on-demand; a one-time spot instance terminates on shutdown |
| the broker codec counts used lowercase keys the run parser drops: no codec was ever recorded | upper-cased |
| Debezium sides put 4-5 headers on every record; Conduit none (Debezium Server stored 580 B per record on the broker, Connect 276 B, Conduit 41 B snappy; key and value bytes are equal, so the difference is headers, batch overhead and compression) | dropped on the Avro arms; checked by the smoke gate (amendment 4) |

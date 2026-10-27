# Parity matrix: pre-registration

**Status: pre-registered, no data.** Committed before any matrix run exists.
Every cell below will be reported, including cells Conduit loses. Any cell
dropped, added or re-run after data exists is recorded in the change log at
the end, with the reason, before its results are used.

The point is to find where and why Conduit is slower, so it can be fixed,
with numbers that hold up when Debezium or Kafka Connect maintainers read
them. `GAP-ANALYSIS.md` (written after the runs) turns every cell Conduit
loses into a profile-backed cause, a candidate arch-v2 fix and an estimated
gain.

Source of truth: `cells.py` (the cell registry and the config generator).
The table in section 6 is generated from it (`cells.py table`), and
`test_cells.py` checks the fairness rules below against every generated
config.

## 1. Hypotheses (before data)

From the v0.21 results and the 2026-10-26 runs (`../results-aws/v022-*`):

- H1. Classic engine, shipped defaults: Postgres and Kafka destinations
  stay 3.5-3.7x behind Kafka Connect (0.287x and 0.269x on 2026-10-26),
  because the classic engine writes one record per destination round trip.
- H2. arch-v2, shipped defaults: at 20,000 ops/s every destination tracks
  on every side (2026-10-26: all four ratios 0.985-1.002). At saturation:
  no prior data for arch-v2 against Connect above 40,000 ops/s; we
  expect Conduit to saturate first on the Postgres destination (per-record
  upsert statements against the JDBC sink's multi-row batches) and on S3
  (both sides were at about 17,400/s on 2026-10-26, Conduit about 1.5%
  behind).
- H3. Debezium Server uses less memory and starts faster than Debezium on
  Connect (no worker, no internal topics); Conduit is lower than both on
  RSS and startup.
- H4. Kafka-source drains (the Connect sink use case): no prior data.
  Conduit's Kafka source is a single consumer per pipeline; with more than
  one partition at shipped defaults both sides run one consumer (Connect's
  tasks.max defaults to 1).
- H5. FileStreamSource (an example connector) is slower than Conduit's file
  source; this cell says little about production Connect.
- H6. Correctness: no side loses a record; duplicates appear only around
  the kill -9 restart (at-least-once).

## 2. Sides and versions

| side | what | pinned |
| --- | --- | --- |
| Conduit (arch-v2) | `--preview.pipeline-arch-v2` at shipped defaults | the commit in `environment.txt` (launch takes a full SHA); built from the repository Dockerfile |
| Conduit (classic) | classic engine, base cells only, continuity with v0.21 | same build |
| Kafka Connect | Debezium 3.7.0.Final Postgres source on the Debezium connect image (Kafka Connect 4.x, distributed mode, one worker); Confluent JDBC sink 10.9.6, Confluent S3 sink 12.1.11, Apache Kafka FileStream connectors | `quay.io/debezium/connect:3.7.0.Final@sha256:8dc50bab...` + `images/connect.Dockerfile` |
| Debezium Server (JVM) | standalone Quarkus runtime, one Postgres connector per process | `quay.io/debezium/server:3.7.0.Final@sha256:9117f3fa...` |
| Debezium Server (native) | Mandrel native build, HTTP cells only (native supports the Redis and HTTP sinks) | `quay.io/debezium/server-native:3.7.0.Final@sha256:6654a571...` |

Debezium moves from 3.2 (v0.21 kit) to 3.7.0.Final on Connect, so both
Debezium runtimes run the same Debezium. The quay.io tags are rebuilt daily on
refreshed base images, so the digest is the pin.

**Destinations without an equivalent are left out:**

- Debezium Server has no file or S3 sink in 3.7.0.Final. Its sink modules are
  kinesis, sqs, sns, http, pubsub, pulsar, eventhubs, redis, kafka, pravega,
  nats-streaming, nats-jetstream, infinispan, rabbitmq, rocketmq, milvus,
  qdrant, instructlab, fluss, databricks-zerobus and jdbc. The JDBC sink is
  compiled into the default JVM distribution (`debezium-server-dist/pom.xml`
  at v3.7.0.Final, scope compile) and is used for the Postgres cells; native
  mode does not support it.
- Apache Kafka ships no HTTP sink, and Confluent's is commercial-licensed, so
  the HTTP cells compare Conduit and Debezium Server only.
- Conduit ships no HTTP destination. The only one in the ConduitIO and
  conduitio-labs inventory is `conduitio-labs/conduit-connector-http` v0.4.0
  (Apache-2.0, built on SDK v0.14, pinned to commit 9bc6ada3). The HTTP cells
  measure that labs plugin as a standalone connector, not a built-in.
- S3 as a SOURCE: Confluent's Amazon S3 Source connector is proprietary
  (Confluent Enterprise License, 30-day trial without a key), so it is not a
  fair open-source equivalent. Apache-licensed Kafka Connect S3 sources exist
  (Aiven's cloud-storage connectors, Lenses' stream-reactor). An S3-source
  family against one of them is a follow-up; it is not in this matrix.

## 3. Tiers

Two tiers per side, in every cell that has a tuned arm:

- **s, shipped defaults.** Nothing is set except what the pipeline needs to
  exist and to produce equivalent output: connection settings, table and
  topic names, the snapshot mode the workload requires, converters and
  record formats that make both sides write the same bytes, and the
  operator settings Conduit's hardening requires (file allowlist `/sink`, API
  on loopback). `test_cells.py` fails if a shipped arm sets any tuning knob.
- **t, tuned per the vendor's own documented knobs.** Every knob, its value
  and where it is documented:

| side | knob | shipped | tuned | source |
| --- | --- | --- | --- | --- |
| Connect worker | producer `batch.size`, `linger.ms`, `compression.type` | 16384, 5 ms, none | 200000, 100 ms, lz4 | https://docs.confluent.io/cloud/current/best-practices/optimizing/throughput.html (batch.size 100000-200000, linger.ms 10-100, lz4). Its `acks=1` advice is NOT applied: delivery parity |
| Connect worker | heap | image default (recorded in environment.txt) | `-Xms3g -Xmx3g` | https://docs.confluent.io/platform/current/kafka/deployment.html#jvm (Xms = Xmx); 3g is half the 6g container limit, our choice |
| Connect worker | consumer `max.poll.records` | 500 | 8192 | https://kafka.apache.org/documentation/#consumerconfigs_max.poll.records; value is ours, matched to the sink batch |
| Connect | `tasks.max` (Kafka-source sinks) | 1 | partitions | https://kafka.apache.org/documentation/#connect_configuring |
| Confluent JDBC sink | `batch.size` | 3000 | 8192 | https://docs.confluent.io/kafka-connectors/jdbc/current/sink-connector/sink_config_options.html; value is ours |
| Confluent S3 sink | `flush.size` (required, no default) | 1000 (v0.21 kit) | 10000 | https://docs.confluent.io/kafka-connectors/s3-sink/current/configuration_options.html; value is ours |
| Debezium (Connect and Server) | `max.batch.size`, `max.queue.size`, `poll.interval.ms` | 2048, 8192, 500 ms | 8192, 32768, 100 ms | https://debezium.io/documentation/reference/3.7/connectors/postgresql.html: the knobs and the max.queue.size > max.batch.size constraint are documented; **Debezium publishes no throughput values, these are ours** |
| Debezium Server | heap | JVM default (1/4 of the container limit) | `-Xms3g -Xmx3g` | as for Connect |
| Debezium Server | Kafka producer as Connect, JDBC `batch.size` as the JDBC sink | | | as above |
| FileStreamSource | `batch.size` | 2000 | 8192 | Apache Kafka connect-file; value is ours |
| Conduit | source `sdk.batch.size`, `sdk.batch.delay` | 0, 0 | 8192, 100 ms | the SDK's documented batching parameters (every connector's reference, e.g. https://github.com/ConduitIO/conduit-connector-kafka/blob/main/connector.yaml). Matched to Debezium's tuned batch and poll interval |
| Conduit | Kafka destination `compression` | snappy | lz4 | conduit-connector-kafka `compression` parameter; matched to the tuned Connect producer |
| Conduit | S3 destination `sdk.batch.size`, `sdk.batch.delay` | connector default | 10000, 1 s | SDK batching; matched to the tuned S3 sink's flush.size and rotate.interval.ms |
| Conduit | Kafka source pipelines per partition | 1 | partitions, one consumer group | the connector's documented `groupID` (consumer group); the Conduit equivalent of tasks.max |

Rules: the tuned values of one side are never more aggressive than the
other side's equivalent (same batch size, same wait, same compression). Where
a vendor gives no number, the value is ours and says so. Conduit has no
published production-tuning guide; only its documented parameters are used.
Shipped differences between the sides (Conduit's Kafka destination defaults
to snappy, the Connect producer to none) are part of the shipped tier.

## 4. Delivery semantics (equal on every side, every cell)

At-least-once everywhere; no side runs exactly-once (`test_cells.py` checks
that no config mentions it).

- Kafka producers: `acks=all` on Conduit's Kafka destination, the Connect
  worker producer and Debezium Server's Kafka sink, set explicitly.
  Idempotent producers stay at their defaults (on for the Java producer since
  Kafka 3.0); idempotence prevents duplicates from producer retries, which
  is not exactly-once.
- Source positions advance only after the write is acknowledged: Conduit
  acks end to end, then flushes the Postgres LSN or commits the Kafka
  offset; Connect commits sink offsets after `put`/`flush` and source offsets
  on its offset flush interval; Debezium Server flushes its file offset store
  on its interval after the sink's `handle` returns.
- Postgres destinations upsert on the key and delete on delete events on
  every side, so their row counts converge to the same state.

## 5. Resource parity

- One container per system under test, named `cdcparity-sut`, with an
  identical cpuset (the last two physical cores, both hardware threads: 4
  vCPUs) and an identical memory limit (6 GiB, no swap) on every side.
  `test_cells.py` checks every generated tool compose for it.
- Postgres, the Kafka broker, MinIO, the loader, the HTTP receiver, the
  sampler and the counter run on the other cores (`INFRA_CPUSET`), so they
  never compete with the SUT for CPU. Instance: **c6i.4xlarge recommended**
  (12 infrastructure vCPUs); c6i.2xlarge leaves the infrastructure 4 vCPUs,
  which may make the loader or Postgres the ceiling at the higher steps (the
  analysis then reports LOADER-BOUND instead of a verdict).
- Kafka Connect cannot run without a broker; Conduit and Debezium Server need
  one only for Kafka sources or destinations. Resources are reported twice:
  SUT only, and footprint (SUT plus the broker, for the arms that need it).
- Go: GOMAXPROCS is not set; Go is cgroup-aware and uses the 4-vCPU cpuset.
  JVMs: the heap per tier (section 3); `ActiveProcessorCount` is not set, the
  JVM reads the cpuset.

## 6. Cells

A cell is one comparison; its arms are compared only with each other, in the
same session on the same instance, rotated (Latin square), after one
discarded warmup round per arm. Phase 1 is the core; phase 2 (the shape
sweeps) is pre-registered here and runs only on a budget decision recorded
in the change log **before** any phase 1 result is read.

Arm names: `<side>-<tier>`; `conduit-s-aa` is the A/A twin of `conduit-s`
(byte-for-byte the same config, its own slots).

| cell | phase | mode | source -> dest | shape | arms | rounds | diag | x-instance |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| A1-pg-file-ramp | 1 | ramp | pg -> file | 1 tables, 64 B | conduit-s, connect-s, conduit-s-aa, conduit-classic-s | 5 | - | - |
| A2-pg-file-p1-b64 | 1 | capacity | pg -> file | 1 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t, conduit-s-aa, conduit-classic-s | 5 | yes | - |
| A2-pg-file-p4-b64 | 2 | capacity | pg -> file | 4 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| A2-pg-file-p16-b64 | 2 | capacity | pg -> file | 16 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| A2-pg-file-p1-b1024 | 2 | capacity | pg -> file | 1 tables, 1024 B | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| A2-pg-file-p1-b16384 | 2 | capacity | pg -> file | 1 tables, 16384 B | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| A1-pg-pg-ramp | 1 | ramp | pg -> pg | 1 tables, 64 B | conduit-s, connect-s, dbzserver-s, conduit-s-aa, conduit-classic-s | 5 | - | - |
| A2-pg-pg-p1-b64 | 1 | capacity | pg -> pg | 1 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t, dbzserver-s, dbzserver-t, conduit-s-aa, conduit-classic-s | 5 | yes | yes |
| A2-pg-pg-p4-b64 | 2 | capacity | pg -> pg | 4 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t, dbzserver-s, dbzserver-t | 5 | - | - |
| A2-pg-pg-p16-b64 | 2 | capacity | pg -> pg | 16 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t, dbzserver-s, dbzserver-t | 5 | - | - |
| A2-pg-pg-p1-b1024 | 2 | capacity | pg -> pg | 1 tables, 1024 B | conduit-s, conduit-t, connect-s, connect-t, dbzserver-s, dbzserver-t | 5 | - | - |
| A2-pg-pg-p1-b16384 | 2 | capacity | pg -> pg | 1 tables, 16384 B | conduit-s, conduit-t, connect-s, connect-t, dbzserver-s, dbzserver-t | 5 | - | - |
| A1-pg-s3-ramp | 1 | ramp | pg -> s3 | 1 tables, 64 B | conduit-s, connect-s, conduit-s-aa, conduit-classic-s | 5 | - | - |
| A2-pg-s3-p1-b64 | 1 | capacity | pg -> s3 | 1 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t, conduit-s-aa, conduit-classic-s | 5 | yes | - |
| A2-pg-s3-p4-b64 | 2 | capacity | pg -> s3 | 4 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| A2-pg-s3-p16-b64 | 2 | capacity | pg -> s3 | 16 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| A2-pg-s3-p1-b1024 | 2 | capacity | pg -> s3 | 1 tables, 1024 B | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| A2-pg-s3-p1-b16384 | 2 | capacity | pg -> s3 | 1 tables, 16384 B | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| A1-pg-kafka-ramp | 1 | ramp | pg -> kafka | 1 tables, 64 B | conduit-s, connect-s, dbzserver-s, conduit-s-aa, conduit-classic-s | 5 | - | - |
| A2-pg-kafka-p1-b64 | 1 | capacity | pg -> kafka | 1 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t, dbzserver-s, dbzserver-t, conduit-s-aa, conduit-classic-s | 5 | yes | yes |
| A2-pg-kafka-p4-b64 | 2 | capacity | pg -> kafka | 4 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t, dbzserver-s, dbzserver-t | 5 | - | - |
| A2-pg-kafka-p16-b64 | 2 | capacity | pg -> kafka | 16 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t, dbzserver-s, dbzserver-t | 5 | - | - |
| A2-pg-kafka-p1-b1024 | 2 | capacity | pg -> kafka | 1 tables, 1024 B | conduit-s, conduit-t, connect-s, connect-t, dbzserver-s, dbzserver-t | 5 | - | - |
| A2-pg-kafka-p1-b16384 | 2 | capacity | pg -> kafka | 1 tables, 16384 B | conduit-s, conduit-t, connect-s, connect-t, dbzserver-s, dbzserver-t | 5 | - | - |
| A1-pg-http-ramp | 1 | ramp | pg -> http | 1 tables, 64 B | conduit-s, dbzserver-s, dbzserver-native-s, conduit-s-aa | 5 | - | - |
| A2-pg-http-p1-b64 | 1 | capacity | pg -> http | 1 tables, 64 B | conduit-s, conduit-t, dbzserver-s, dbzserver-t, dbzserver-native-s, conduit-s-aa | 5 | yes | - |
| A2-pg-http-p4-b64 | 2 | capacity | pg -> http | 4 tables, 64 B | conduit-s, conduit-t, dbzserver-s, dbzserver-t, dbzserver-native-s | 5 | - | - |
| B-kafka-pg-p1-b64 | 1 | drain | kafka -> pg | 1 partitions, 64 B, backlog 50,000,000 | conduit-s, conduit-t, connect-s, connect-t, conduit-s-aa | 5 | yes | - |
| B-kafka-pg-p4-b64 | 2 | drain | kafka -> pg | 4 partitions, 64 B, backlog 50,000,000 | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| B-kafka-pg-p16-b64 | 2 | drain | kafka -> pg | 16 partitions, 64 B, backlog 50,000,000 | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| B-kafka-pg-p1-b1024 | 2 | drain | kafka -> pg | 1 partitions, 1024 B, backlog 8,000,000 | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| B-kafka-pg-p1-b16384 | 2 | drain | kafka -> pg | 1 partitions, 16384 B, backlog 600,000 | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| B-kafka-s3-p1-b64 | 1 | drain | kafka -> s3 | 1 partitions, 64 B, backlog 50,000,000 | conduit-s, conduit-t, connect-s, connect-t, conduit-s-aa | 5 | yes | - |
| B-kafka-s3-p4-b64 | 2 | drain | kafka -> s3 | 4 partitions, 64 B, backlog 50,000,000 | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| B-kafka-s3-p16-b64 | 2 | drain | kafka -> s3 | 16 partitions, 64 B, backlog 50,000,000 | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| B-kafka-s3-p1-b1024 | 2 | drain | kafka -> s3 | 1 partitions, 1024 B, backlog 8,000,000 | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| B-kafka-s3-p1-b16384 | 2 | drain | kafka -> s3 | 1 partitions, 16384 B, backlog 600,000 | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| B-kafka-file-p1-b64 | 1 | drain | kafka -> file | 1 partitions, 64 B, backlog 50,000,000 | conduit-s, conduit-t, connect-s, connect-t, conduit-s-aa | 5 | yes | - |
| B-kafka-file-p1-b1024 | 2 | drain | kafka -> file | 1 partitions, 1024 B, backlog 8,000,000 | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| B-kafka-file-p1-b16384 | 2 | drain | kafka -> file | 1 partitions, 16384 B, backlog 600,000 | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| C-file-kafka-b64 | 1 | drain | file -> kafka | 1 partitions, 64 B, backlog 50,000,000 | conduit-s, conduit-t, connect-s, connect-t, conduit-s-aa | 5 | yes | - |
| C-file-kafka-b1024 | 2 | drain | file -> kafka | 1 partitions, 1024 B, backlog 8,000,000 | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| C-file-kafka-b16384 | 2 | drain | file -> kafka | 1 partitions, 16384 B, backlog 600,000 | conduit-s, conduit-t, connect-s, connect-t | 5 | - | - |
| D-pg-kafka-transform | 1 | capacity | pg -> kafka (unwrap+rename) | 1 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t, dbzserver-s, dbzserver-t, conduit-s-aa | 5 | yes | - |
| E-pg-fanout-file-s3 | 1 | capacity | pg -> file+s3 | 1 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t, conduit-s-aa | 5 | yes | - |
| F-pg-kafka-snapshot | 1 | snapshot | pg -> kafka | 10,000,000 rows, 64 B | conduit-s, conduit-t, connect-s, connect-t, dbzserver-s, dbzserver-t, conduit-s-aa | 5 | - | - |
| F-pg-pg-snapshot | 1 | snapshot | pg -> pg | 10,000,000 rows, 64 B | conduit-s, conduit-t, connect-s, connect-t, dbzserver-s, dbzserver-t, conduit-s-aa | 5 | - | - |
| G-pg-kafka-kill9 | 1 | recover | pg -> kafka | 1 tables, 64 B | conduit-s, connect-s, dbzserver-s | 5 | - | - |
| G-pg-pg-kill9 | 1 | recover | pg -> pg | 1 tables, 64 B | conduit-s, connect-s, dbzserver-s | 5 | - | - |
| H-pg-file-verify | 1 | correct | pg -> file | 1 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t | 1 | - | - |
| H-pg-pg-verify | 1 | correct | pg -> pg | 1 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t, dbzserver-s, dbzserver-t | 1 | - | - |
| H-pg-s3-verify | 1 | correct | pg -> s3 | 1 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t | 1 | - | - |
| H-pg-kafka-verify | 1 | correct | pg -> kafka | 1 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t, dbzserver-s, dbzserver-t | 1 | - | - |
| H-pg-http-verify | 1 | correct | pg -> http | 1 tables, 64 B | conduit-s, conduit-t, dbzserver-s, dbzserver-t, dbzserver-native-s | 1 | - | - |
| H-kafka-pg-verify | 1 | correct | kafka -> pg | 1 partitions, 64 B | conduit-s, conduit-t, connect-s, connect-t | 1 | - | - |
| H-kafka-s3-verify | 1 | correct | kafka -> s3 | 1 partitions, 64 B | conduit-s, conduit-t, connect-s, connect-t | 1 | - | - |
| H-kafka-file-verify | 1 | correct | kafka -> file | 1 partitions, 64 B | conduit-s, conduit-t, connect-s, connect-t | 1 | - | - |
| H-file-kafka-verify | 1 | correct | file -> kafka | 1 partitions, 64 B | conduit-s, conduit-t, connect-s, connect-t | 1 | - | - |
| H-pg-kafka-transform-verify | 1 | correct | pg -> kafka (unwrap+rename) | 1 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t, dbzserver-s, dbzserver-t | 1 | - | - |
| H-pg-fanout-file-s3-verify | 1 | correct | pg -> file+s3 | 1 tables, 64 B | conduit-s, conduit-t, connect-s, connect-t | 1 | - | - |

Families: **A** Postgres CDC to file, Postgres, S3, Kafka, HTTP (A1 ramp,
A2 capacity and shapes); **B** Kafka topic backlog to Postgres (JDBC sink),
S3 and file: Kafka Connect's core use case, no Debezium; **C** file to Kafka
(FileStreamSource); **D** transform (Debezium unwrap plus a ReplaceField
rename vs Conduit's `field.rename` and a flattening record format); **E**
fan-out (Conduit: one pipeline, two destinations; Connect: one topic, two
sink connectors); **F** initial snapshot of 10 million rows; **G** kill -9
recovery; **H** record-by-record correctness at saturation.

Kafka-to-file runs single-partition only: FileStreamSink writes one file from
every task, and more than one task interleaves partial lines.

## 7. Load

- **ramp** (A1): offered rate held at 20,000 ops/s for 180 s (warm-up,
  excluded), then 20,000, 40,000, 50,000, 70,000, 100,000, 140,000 and 200,000
  ops/s, 120 s each: 60 s to settle, 60 s measured. Every step is reported
  for every arm. This fixes the v0.21 50,000 ops/s problem (the window
  counted startup backlog drain): a step counts only as TRACKED if the lag
  did not grow over its window, and every step records the rate the loader
  actually achieved.
- **capacity** (A2, D, E): the loader writes as fast as Postgres accepts
  (`-rate 0`), so every side is offered more than it can take. 180 s
  warm-up (JIT and GC steady state), then a **300 s** window.
- **drain** (B, C): a backlog written before the pipeline starts (about
  6-10 GB: 50 M x 64 B, 8 M x 1 KiB, 600 k x 16 KiB records); 180 s warm-up,
  300 s window. A run whose backlog ran out inside the window is invalid.
- **snapshot** (F): 10 M rows loaded before the pipeline starts; time from
  the first record to the last.
- **correct** (H): 3 M ops at the loader's maximum rate (pg sources) or the
  3 M-record backlog; wait until the sink has not moved for 60 s and the
  loader is done; verify every record.
- **recover** (G): 3 M ops at maximum rate; 120 s after the first record,
  `docker kill -s KILL` the SUT, start it again at once; measure time to the
  next delivered record; verify every record.

Rounds: 5 measured rounds per arm (the correctness cells run once per arm;
recovery 5 kills per arm). The base A2 cells for Postgres and Kafka run on two
different instances (cross-instance variance).

## 8. Metrics

All from ground truth at the sink or from the host, never from an engine's
own metrics.

| metric | how |
| --- | --- |
| throughput | sink count delta / real elapsed time between the two samples bounding the window; counts: lines in the sink files, rows in the destination tables, lines in every S3 object, end-minus-start offsets of the topics, events at the HTTP receiver |
| offered (achieved) | the loader's committed ops (by kind) from its `/progress`, sampled with every sink count |
| lag | offered minus delivered, at the window's start and end |
| latency p50/p99/p99.9 | from the marker's timestamp (written into every payload when the statement is queued) to: broker append time (Kafka, `LogAppendTime`), the destination row's `landed_at` trigger (Postgres, ramp cells), arrival at the receiver (HTTP), object LastModified (S3, 1 s resolution). Interval percentiles are combined as the median p50, median p99 and worst p99.9. File sinks: not measured |
| CPU-seconds per million events | cgroup `usage_usec` delta over the window / events delivered; SUT only and footprint |
| peak RSS | max cgroup `memory.stat` anon (page cache excluded) over the run |
| startup to first record | first sink record minus the tool-start hook (includes Connect's REST registration and slot creation, which are part of starting) |
| $ per billion events | instance price per hour / (rate x 3600) x 1e9 |
| GC pauses | JVM `-Xlog:gc*` and Go `GODEBUG=gctrace=1`, every run (observation only) |
| correctness | `loader verify`: missing, duplicate, malformed and reordered records per kind |
| recovery | time from restart to the next delivered record; loss and duplicates from verify |

## 9. Analysis rules

`analyze.py`, fixed now. `benchi/METHODOLOGY.md` sets the method (ground
truth, A/A, medians with spread, interleaving) but contains no
resolved/suggestive rule, so this plan defines one:

- **A/A floor** per family, destination and mode: the maximum per-round
  `|conduit-s / conduit-s-aa - 1|` in that family's base cell.
- **RESOLVED**: `|median per-round ratio - 1| > 2 x floor`, the bootstrap 95%
  CI of the median ratio excludes 1, and at least 4 of 5 rounds have the same
  sign. **SUGGESTIVE**: the median ratio is outside the floor but not all of
  that holds. **UNRESOLVED**: otherwise, or with no floor.
- A capacity run is valid only if the arm's lag grew in the window (it was
  saturated); an arm that kept up with the unlimited loader is reported as
  `>= loader rate`, not as a capacity. A drain run is valid only if its
  backlog lasted the whole window. A ramp step where the loader achieved less
  than 97% of its target is LOADER-BOUND: no verdict there.
- **Correctness gates speed**: an arm with any lost, malformed or (on an
  ordered sink) reordered record fails the cell, whatever its throughput.
  Duplicates are allowed (at-least-once) and reported per million.
- Bootstrap: 10,000 percentile resamples, seeded. With 5 rounds the CI is
  coarse; it is reported as computed.

## 10. Diagnosis

Every base capacity and drain cell has one extra, unmeasured diag run per
arm: Conduit with `--dev.cpuprofile`, `--dev.memprofile` and
`--dev.blockprofile` (block rate 1); the JVM sides with async-profiler 4.6
(itimer) for the 300 s window. Conduit's Prometheus metrics (per-stage source,
processor, destination and ack histograms) are scraped at every window's start
and end, in measured runs too. GC logs, cgroup CPU, memory and IO, and
docker's network counters are kept for every run. `GAP-ANALYSIS.md`: per cell
Conduit loses, the profile-backed cause, a candidate arch-v2 fix, an
estimated gain; guesses marked as guesses.

## 11. Reproducibility

One command per cell on a bench instance (`matrix/run-cell.sh CELL OUT`), or
the whole matrix with `matrix/aws/launch.sh RUN_ID CONDUIT_SHA N PHASES TYPE`.
The kit tarball is `git archive` of a commit (launch refuses uncommitted
changes; `KIT_COMMIT` is in it). Every image pinned by digest or built from a
pinned base; the instance's AMI, kernel, type, CPU, Docker version, Conduit
SHA and connector versions are in `environment.txt`. Raw per-run data
(samples, resources, hooks, verify reports, profiles) is committed with the
results.

## 12. Cost

`cells.py estimate`. Run lengths are estimates (startup, fill and snapshot
times are guessed from the v0.21 runs); the cap per instance is its
estimate plus 25%.

| plan | runs | instance-hours | c6i.2xlarge ($0.424/h) | c6i.4xlarge ($0.848/h) | wall time on 2 / 4 / 8 instances |
| --- | --- | --- | --- | --- | --- |
| smoke | 16 cells, 1 run per arm | about 18 (upper bound) | $8 | $15 | 9 / 5 / 3 h |
| phase 1 | 871 | 178 | $75 | $152 | 89 / 45 / 25 h |
| phase 2 | 798 | 151 | $64 | $128 | 77 / 41 / 21 h |
| both | 1,669 | 329 | $139 | $280 | 164 / 83 / 43 h |

Parallelism costs no extra instance-hours (about 20 min of setup per
instance). The account's on-demand standard vCPU quota is 32: 2 x
c6i.4xlarge or 4 x c6i.2xlarge at most, and only with no other instance
running. EBS gp3 at 500 MB/s and 6000 IOPS adds about 3%.

## 13. Known asymmetries (by design, reported with the results)

- Connect writes through a broker; for file, Postgres, S3 and HTTP
  destinations Conduit and Debezium Server do not. That is architecture and
  is measured; the footprint column carries the broker.
- Debezium Server runs one connector per process, so with 4 or 16 tables it
  captures through one slot; Conduit and Connect run one pipeline or
  connector, with its own slot, per table.
- Record bytes differ: Conduit writes OpenCDC JSON, Debezium the Debezium
  envelope; on raw-payload paths (Kafka and file sources) both write the
  payload bytes. Counts and latency are insensitive to this, CPU and IO are
  not.
- The Postgres destinations write differently: Conduit's builtin:postgres
  vs the Confluent JDBC sink (Connect) vs Debezium's JDBC sink (Debezium
  Server).
- The HTTP cells measure a conduitio-labs plugin, not a built-in.

## 14. Limitations (what this matrix does not do)

- **No mutex profile for Conduit**: the binary has no flag to enable mutex
  profiling (`--dev.cpuprofile`, `--dev.memprofile` and `--dev.blockprofile`
  only). Adding `--dev.mutexprofile` is a small change, not part of this run.
- **async-profiler in itimer mode**, not perf_events: no kernel frames, no
  hardware counters, because the JVM containers run without extra
  privileges.
- **File-sink latency is not measured**: there is no arrival timestamp
  without polling the file. S3 latency has 1 s resolution.
- **Postgres-destination latency only in the ramp cells**: the `landed_at`
  trigger and its BRIN index cost writes on every side, so the capacity cells
  run without them.
- **One broker, one Postgres, one instance per run**: no network between
  components, no replication; real deployments have both.
- **5 rounds**: bootstrap CIs on 5 values are coarse; the A/A floor and the
  same-sign rule carry most of the weight.
- **Debezium Server JDBC table routing** uses a RegexRouter SMT (topic to
  `bench_dest_i`), a routing step the other sides do not need.
- **Correctness of Debezium Server native** is checked only on HTTP.
- **Not measured**: S3 sources (section 2), multi-node Connect, Conduit
  multi-instance, schema registry formats (Avro, Protobuf), TLS.
- **Loader ceiling**: one source table is fed by one Postgres connection
  (per-table order must match the replayed sequence), so in a one-table
  capacity cell a side faster than the loader is reported as
  `>= loader rate` (unresolved), not as a capacity. The ramp cells show
  where that ceiling is.
- **Untested end to end before the smoke run**: the harness was compiled and
  unit-tested only (no Docker run anywhere outside AWS). The smoke run is the
  first execution; its failures are fixed and recorded here before phase 1.

## 15. Change log

| date | change | reason | data seen before the change? |
| --- | --- | --- | --- |
| 2026-10-27 | plan written | | no |

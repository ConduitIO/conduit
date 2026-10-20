# v0.21 close-out: Conduit vs Kafka Connect (Debezium) vs Redpanda Connect on Postgres CDC

A reproducible, ground-truth comparison of CDC pipelines on one equivalent
workload: one or more Postgres tables (`TABLES`, default 1) receiving a steady,
fixed-rate stream of INSERT/UPDATE/DELETE, captured by each system and delivered
to a sink whose accumulated count is the measured quantity. Four destination
kinds are measured:

- **file sink** (`conduit`, `conduit-aa`, `connect`, `redpanda-connect`): one
  sink file per table, whose summed line count (`wc -l`) is the metric.
- **Postgres destination** (`conduit-pg`, `connect-pg`): one destination table
  (`bench_dest_i`) per source table, whose summed row count (`SELECT count(*)`)
  is the metric.
- **S3 destination** (`conduit-s3`, `connect-s3`): CDC events written as JSON
  objects to a local MinIO bucket, whose summed line count (`wc -l` across every
  object) is the metric.
- **Kafka destination** (`conduit-kafka`, `connect-kafka`): one Kafka message
  per CDC event into one topic per table, whose summed latest offset is the
  metric.

The method follows `benchi/METHODOLOGY.md`: the metric is **ground truth**
(what the sink actually wrote), the **A/A control** (`conduit-aa`, file arm
only) establishes the noise floor, and results are **medians with variance**,
never a single best run.

## The pipelines

All arms capture the SAME set of tables (`bench_source_0` ..
`bench_source_{TABLES-1}`, each `(id bigint PRIMARY KEY, payload text)`,
`REPLICA IDENTITY DEFAULT`) over the SAME pgoutput logical-replication wire
protocol, with the snapshot disabled so only the loader's steady-state stream
is captured.

| arm | pipeline | config |
| --- | --- | --- |
| `conduit` | `TABLES` pipelines, each a `builtin:postgres` source (`cdcMode: logrepl`, `snapshotMode: never`, `withAvroSchema: false`, `sdk.batch.size: CONDUIT_BATCH_SIZE`, only when set) → `builtin:file` sink | `pipelines/conduit.yml` (generated), `tools/compose-conduit.yml` |
| `conduit-aa` | byte-for-byte the same as `conduit` (the A/A control; only the container name differs) | `tools/compose-conduit-aa.yml` |
| `connect` | `TABLES` Debezium Postgres source connectors (`plugin.name: pgoutput`, `snapshot.mode: never`, `tombstones.on.delete: false`) → Kafka broker → `TABLES` `FileStreamSinkConnector`s | `tools/compose-connect.yml`, `tools/connect.Dockerfile`, registration loop in `bench.yml` |
| `redpanda-connect` | `postgres_cdc` input (`stream_snapshot: false`) → `file` output (`codec: lines`); single-table, blocked by licensing | `config/redpanda.yaml`, `tools/compose-redpanda.yml` |
| `conduit-pg` | `TABLES` pipelines, each a `builtin:postgres` source (same config as `conduit`) → `builtin:postgres` destination (`table: bench_dest_i`; shipped-default exec mode and batching unless `PG_DEST_URL_EXTRA` / `CONDUIT_BATCH_SIZE` are set) | `pipelines/conduit-pg.yml` (generated), `tools/compose-conduit-pg.yml` |
| `connect-pg` | `TABLES` Debezium Postgres source connectors (`pgoutput`, `snapshot.mode: never`, `unwrap` transform) → Kafka broker → `TABLES` Confluent JDBC sink connectors (`insert.mode: upsert`, `pk.mode: record_key`, `delete.enabled`) writing `bench_dest_i` | `tools/compose-connect-pg.yml`, `tools/connect.Dockerfile`, registration loop in `bench.yml` |
| `conduit-s3` | `TABLES` pipelines, each a `builtin:postgres` source (same config as `conduit`) → `builtin:s3` destination (`format: json`, `aws.endpoint`/`aws.pathStyle` pointing at the local MinIO; shipped-default batching unless `CONDUIT_BATCH_SIZE` is set) | `pipelines/conduit-s3.yml` (generated), `tools/compose-conduit-s3.yml` |
| `connect-s3` | `TABLES` Debezium Postgres source connectors (byte-for-byte the `connect` source, `pgoutput`, `snapshot.mode: never`, no unwrap) → Kafka broker → `TABLES` Confluent S3 sink connectors (`format.class: JsonFormat`, `flush.size: 1000`, `store.url`/`path.style.access.enabled` pointing at the local MinIO) writing the bucket `bench` | `tools/compose-connect-s3.yml`, `tools/connect.Dockerfile`, registration loop in `bench.yml` |
| `conduit-kafka` | `TABLES` pipelines, each a `builtin:postgres` source (same config as `conduit`) → `builtin:kafka` destination (`acks: all`, one topic `cdcparitykafka.public.bench_source_i`; shipped-default batching unless `CONDUIT_BATCH_SIZE` is set) | `pipelines/conduit-kafka.yml` (generated), `tools/compose-conduit-kafka.yml` |
| `connect-kafka` | `TABLES` Debezium Postgres source connectors (`pgoutput`, `snapshot.mode: never`, no unwrap) producing straight to Kafka → one topic `cdcparitykafka.public.bench_source_i` (no sink connector) | `tools/compose-connect-kafka.yml`, `tools/connect.Dockerfile`, registration loop in `bench.yml` |

The workload generator (`loader/`) is the SAME container for every arm: a Go
program writing a 70/20/10 INSERT/UPDATE/DELETE mix at a fixed `-rate` (default
20 000 ops/s) through `pgx.Batch`, split round-robin across `TABLES` tables
(total rate unchanged), so it is never the bottleneck. It writes only the
source tables; the destination tables are populated by the pipeline under test.

## What is equivalent (by construction)

- **Source tables + row shape**: `bench_source_0` .. `bench_source_{TABLES-1}`,
  each `(id bigint PRIMARY KEY, payload text)`, fixed 64-byte payload, same for
  every arm. The destination tables `bench_dest_0` .. `bench_dest_{TABLES-1}`
  have the same shape (`id PRIMARY KEY` included, so both sinks can upsert on
  the key).
- **Rate**: the same loader, same `-rate`, same 70/20/10 mix, split round-robin
  across the tables, for every arm. Total offered load is identical regardless
  of `TABLES`; only the parallelism differs.
- **N-way split**: for a given `TABLES`, the Conduit arms run `TABLES`
  independent pipelines and the Connect arms run `TABLES` sources + `TABLES`
  sinks (one per table → topic → sink), so both systems are measured at the
  same fan-out.
- **Wire protocol**: all arms use Postgres `pgoutput` logical replication.
- **Snapshot**: disabled in all arms (`snapshotMode: never` /
  `snapshot.mode: never` / `stream_snapshot: false`), so the number reflects
  steady-state CDC, not a one-time table dump.
- **Postgres-destination write semantics**: `conduit-pg` and `connect-pg` both
  mirror the source's LIVE rows — the builtin:postgres destination upserts on
  the record key and deletes on delete events; the JDBC sink is configured the
  same way (`insert.mode: upsert`, `pk.mode: record_key`, `delete.enabled`).
  So both destination tables converge to the same row count for the same
  captured stream.
- **S3-destination write semantics**: `conduit-s3` and `connect-s3` both write
  one JSON line per CDC event — the builtin:s3 `format: json` writes one
  OpenCDC-shaped object per line, the S3 sink's `JsonFormat` writes one
  bare-value Debezium envelope per line — so the line count across the bucket
  is the event count for both. The Connect side batches into objects
  (`flush.size`); Conduit batches only if `sdk.batch.*` is set (a tuned run).
  At shipped defaults both S3 arms saturate near 18k events/s, so the S3
  sink is compared at saturation (see `results.md`).
- **Kafka-destination write semantics**: `conduit-kafka` and `connect-kafka`
  both produce exactly one Kafka message per CDC event (INSERT/UPDATE/DELETE)
  into the same topic per table (`cdcparitykafka.public.bench_source_i`). The
  builtin:kafka destination writes the OpenCDC record (one message per record);
  Debezium's source writes one message per event with `tombstones.on.delete:
  false`, so deletes are real messages, not tombstones. Both use `acks: all`
  and one partition per topic, so the topic's latest offset equals the event
  count.

## What is NOT equivalent

- **Kafka arms require a broker.** The `connect`, `connect-pg`, `connect-s3`
  and `connect-kafka` arms have an extra hop (Postgres → Debezium → Kafka →
  sink) and a JVM Connect worker; `conduit-kafka` also needs a broker, because
  its destination is Kafka. The file/Postgres Conduit arms and Redpanda Connect
  are broker-neutral single processes. The extra hop is part of what "Kafka
  Connect + Debezium" is, so it is measured, but a reader should not attribute
  the whole difference to CDC capture efficiency. For the Kafka-destination
  parity pair, both arms write to the same broker, so the broker is common
  infrastructure, not a differentiator.
- **The ground-truth metric differs between destination kinds.** The file-sink
  arms count every CDC event (one line per INSERT/UPDATE/DELETE). The
  Postgres-destination arms count **live rows**, not events: the destination
  upserts on the key, so INSERTs add a row, UPDATEs rewrite in place, and
  DELETEs remove a row. `rows_delta` for a Postgres-destination arm is
  therefore the _net_ rows captured (≈ inserts − deletes, ~60% of the raw event
  count at the default 70/20/10 mix) — a LOWER number than the file arms'
  event count, and **not directly comparable to it**. The two Postgres-
  destination arms are comparable to _each other_; the two file-sink arms to
  _each other_; and the S3- and Kafka-destination arms (both event counts) to
  _each other_ and to the file-sink arms' event count, though the S3/Kafka arms
  wait for their source's slot before writing and the file arms do not.
- **S3 objects differ in layout and line content.** The Conduit `format: json`
  writes one OpenCDC-shaped object per line to flat keys
  (`<UnixNano>.json`); the Connect `JsonFormat` writes the bare Debezium
  envelope to `topics/<topic>/partition=0/<name>.json`. The metrics are line
  counts, which are insensitive to both, but a consumer of the exact object
  keys or line shape would see a difference.
- **Postgres sinks write differently under the hood.** The builtin:postgres
  destination builds per-record `INSERT … ON CONFLICT (id) DO UPDATE` /
  `DELETE` statements. At shipped defaults (`sdk.batch.size` unset) it writes one
  record per round trip, and that destination write IS the bottleneck
  (0.30x of Connect in `results.md`); with `sdk.batch.size` set it batches. The
  Confluent JDBC sink batches inserts (`batch.size: 3000`) and issues its own
  upsert/delete SQL.
- **Record encoding differs on the Connect Postgres arm.** The JDBC sink needs
  the record key schema to extract the primary key (`pk.mode: record_key`), so
  `connect-pg` runs `key.converter`/`value.converter` with
  `schemas.enable: true` and flattens the Debezium envelope with
  `ExtractNewRecordState` (`unwrap`). That embeds a JSON schema per Kafka
  message — overhead Conduit's structured record path doesn't carry. It is a
  write-path/serialization difference, not a source-capture difference.
- **Batching defaults differ.** The benchmark runs Conduit with NO `sdk.batch.*`
  settings (the shipped defaults: `CONDUIT_BATCH_SIZE=default`); Debezium's
  `max.batch.size` defaults to 2048 and the JDBC sink's `batch.size` to 3000.
  This is a shipped-default difference, not a workload difference, and it is the
  cause of the Postgres- and Kafka-destination gaps in `results.md`. Setting
  `CONDUIT_BATCH_SIZE=1000` (plus `sdk.batch.delay`) is a TUNED run and must be
  labelled as such, as must `PG_DEST_URL_EXTRA` (for example
  `&default_query_exec_mode=cache_statement`).
- **Record bytes differ.** Conduit writes the OpenCDC record, Debezium the
  Debezium envelope, Redpanda its message. On the Kafka arms the two producers
  therefore emit different payloads (OpenCDC JSON vs the Debezium envelope) —
  but both emit exactly one message per event, and the metric (latest offset) is
  insensitive to byte size, so this is not a count difference.
- **Delivery semantics differ.** Conduit is at-least-once with end-to-end ack
  propagation; Debezium/Kafka Connect and Redpanda have their own at-least-once
  paths and (in the case of Kafka) an acknowledged broker write between capture
  and sink. This benchmark measures throughput, not delivery guarantees or
  crash recovery.
- **Resource limits are not normalized.** Each system gets the machine's
  resources with its own defaults (JVM heap for Connect, GOMAXPROCS for
  Conduit/Redpanda). No cgroup limits are applied.

## The A/A floor rule

`conduit-aa` is the same image, pipeline and compose as `conduit`, built the
same way. Any measured difference between them is run-to-run noise — the
harness's floor. **A Conduit-vs-competitor delta smaller than the A/A spread
means nothing.** The A/A control runs interleaved with the file-sink arms (rotated order), not
as a separate block. It exists **only on the file arm**; the Postgres, S3 and
Kafka pairs rely on their within-arm min-max and the size of the effect.

## The Postgres-destination arms are a parity check

The two Postgres-destination arms exist to measure the real "CDC→Postgres"
workload people index on: Conduit `builtin:postgres → builtin:postgres` vs
Debezium Postgres → Kafka Connect JDBC sink. Both sources read the same pgoutput
stream. The JDBC sink batches (`batch.size: 3000`); the Conduit destination
batches only when `sdk.batch.size` is set. At shipped defaults it does not, so
the destination write is the limiter and the arm measures the sink, not the
source: the result is 0.30x of Connect (`results.md`). With batching set
(a tuned run) the pair reaches parity.

Because the destination mirrors the source's LIVE rows, the loader must not
write before the `snapshotMode: never` source has created its replication slot:
a row written before the slot exists would never be captured, and updates to
those phantom rows would upsert-INSERT extra rows (while deletes of them would
be no-ops), inflating `rows_delta` by however long the tool took to start.
Instead of a blind delay, `run.sh` gives the loader the arm's replication slot
name(s) and the loader polls `pg_replication_slots` until they exist, bounded
by `LOADER_SLOT_TIMEOUT` (default `600s`). The file-sink arms count events, not
net rows, have no slot to wait for, and write immediately.

## The S3-destination arms are a parity check

The two S3-destination arms measure the "CDC→object storage" workload: Conduit
`builtin:postgres → builtin:s3` vs Debezium Postgres → Kafka Connect S3 sink.
Both sources read the same pgoutput stream. Both arms saturate near 18k
events/s at the 20k offered load, so the comparison is at saturation; read any
gap against the within-arm spread, since the A/A control is on the file arm
only.

The S3-destination arms also wait for their source's replication slot before
the loader writes, but for a different reason than the Postgres-destination
arms: they count _events_, so a pre-slot write cannot create a phantom-row
artifact — it is simply never captured. The wait is applied anyway for
methodological consistency with the other parity arms, so the loader's initial
fill happens _after_ the slot exists (a captured, clean steady-state stream)
and both `conduit-s3` and `connect-s3` start their measured window from the
same point. This is a documented non-equivalence only against the file-sink
arms, which write immediately; it does not affect the `conduit-s3` vs
`connect-s3` parity (they both wait for their slot).

## The Kafka-destination arms are a parity check

The two Kafka-destination arms (`conduit-kafka` vs `connect-kafka`) measure the
"CDC → Kafka" workload: Conduit `builtin:postgres → builtin:kafka` vs Debezium
Postgres producing straight to Kafka (Debezium's native producer via Kafka
Connect, with no sink connector). Debezium's producer batches. The
builtin:kafka destination produces one record per request at shipped defaults
(`sdk.batch.size` unset), which makes the destination the limiter (0.30x of
Connect, `results.md`); with `sdk.batch.*` set (a tuned run) it reaches parity.
Both emit exactly one message per event into the same topic.

The slot-aware wait is still applied to these arms, but for a different reason
than the Postgres-destination arms. A Kafka arm's ground truth is an **event
count** (one message per event), not a net-row count, so the phantom-insert
artifact that inflates `conduit-pg`/`connect-pg` cannot inflate it: events
written before the slot exists are simply never captured, and the pre-test
warmup (wait for the first record, then 15 s) keeps startup out of the window.
The wait is kept for symmetry and so the loader's initial 10 000-row `fill`
lands after the `snapshotMode: never` source has created its slot — without it,
that fill is silently dropped on the floor and the first records the warmup sees
arrive only during steady state. Either way the measured window is clean; the
wait just makes the loader's first writes captured rather than wasted.

## Redpanda Connect: blocked by licensing (honest status)

`redpanda-connect` is a real arm with a complete, committed config — but it
cannot run on the free edition. `postgres_cdc` is a Redpanda Connect
**Enterprise** component; at startup the stock image fails with:

```text
failed to init input <no label> path root.input: this feature requires a valid
Redpanda Enterprise Edition license that includes the Connect product.
```

The only free Postgres inputs are `sql_select` (a snapshot SELECT) and
`sql_raw` (a query) — neither is CDC, so using either would break the
equivalence rule. The config is kept so anyone with an Enterprise license can
run the arm; on the free edition it is documented as blocked, not faked.

## Metric

Ground truth is what the sink wrote, not any system's metrics. Per destination
kind:

- **file-sink arms**: `ROWS_BEFORE` / `ROWS_AFTER` are the SUM of `wc -l`
  across `/sink/out*.txt` — the total CDC events delivered.
- **Postgres-destination arms**: `ROWS_BEFORE` / `ROWS_AFTER` are the SUM of
  `SELECT count(*)` across `bench_dest_0` .. `bench_dest_{TABLES-1}` — the live
  rows the destination holds, i.e. the _net_ rows captured.
- **S3-destination arms**: `ROWS_BEFORE` / `ROWS_AFTER` are the SUM of `wc -l`
  across every object in the MinIO bucket `bench` (the counter container
  downloads the bucket with `aws s3 cp --recursive` and cats the objects) — the
  total CDC events delivered.
- **Kafka-destination arms**: `ROWS_BEFORE` / `ROWS_AFTER` are the SUM of the
  latest offsets across `cdcparitykafka.public.bench_source_0` ..
  `cdcparitykafka.public.bench_source_{TABLES-1}` (GetOffsetShell, one partition
  per topic) — the total CDC events delivered to the broker.

```text
throughput = (ROWS_AFTER - ROWS_BEFORE) / test_duration
```

`bench.yml` runs a 60 s steady-state window per arm. The `pre-test` hook waits
for the sink to start accumulating (pipeline startup falls outside the window),
warms up 15 s, then records `ROWS_BEFORE`; the `post-test` hook records
`ROWS_AFTER`. `run.sh` computes the per-run rate and reports medians with
variance over the rounds. The file, Postgres and S3 count hooks run in the
`cdcparity-counter` container (`psql` and the AWS CLI are installed there),
against the same Postgres / MinIO the arm writes; the Kafka count hooks run in
the `cdcparity-kafka` broker container, which has `kafka-get-offsets.sh` on its
classpath.

## Reproducing

```bash
# local
cd benchi/v021-cdc-parity
./run.sh 3 conduit conduit-aa connect                    # 3 rounds, A/A + file arms
./run.sh 3 conduit-pg connect-pg                         # 3 rounds, Postgres-destination parity pair
./run.sh 3 conduit-s3 connect-s3                         # 3 rounds, S3-destination parity pair
./run.sh 3 conduit-kafka connect-kafka                   # 3 rounds, Kafka-destination parity pair
./run.sh 3 conduit conduit-aa connect conduit-pg connect-pg conduit-s3 connect-s3 conduit-kafka connect-kafka   # all destination kinds

# N-way (2 tables): 2 sources + 2 sinks per arm, summed ground truth
TABLES=2 ./run.sh 3 conduit-pg connect-pg

# TUNED (not defaults): set sdk.batch.* on every Conduit source and destination
CONDUIT_BATCH_SIZE=1000 RUN_LABEL=TUNED-batch1000 ./run.sh 5 conduit-pg connect-pg
# TUNED: postgres destination cache_statement exec mode
PG_DEST_URL_EXTRA='&default_query_exec_mode=cache_statement' RUN_LABEL=TUNED-cache_statement ./run.sh 5 conduit-pg connect-pg
# diagnostic only: write Conduit CPU/heap profiles to <run>/<arm>/prof/
PROFILE=1 ./run.sh 0 conduit-pg

# dedicated AWS instance (see run-aws.sh)
LOADER_RATE=20000 SSH_KEY=/path/outside/repo.pem CONDUIT_REF=<sha> TABLES=1 ./run-aws.sh 5 conduit conduit-aa connect conduit-pg connect-pg conduit-s3 connect-s3 conduit-kafka connect-kafka
```

`TABLES` (default 1; sweep 1, 2, 4), `CONDUIT_BATCH_SIZE` (default `default` = unset),
`CONDUIT_BATCH_DELAY` (default 100ms) and `LOADER_SLOT_TIMEOUT` (default 600s,
how long the loader waits for an arm's replication slot(s) before failing) are
read by `run.sh`, which runs `scripts/generate.sh` to regenerate
`pipelines/conduit.yml`, `pipelines/conduit-pg.yml`, `pipelines/conduit-s3.yml`,
`pipelines/conduit-kafka.yml` and `infra/init.sql` for the given `TABLES`, then
exports `TABLES` to docker compose (the loader's `--tables` flag, and the
counter container's env for the connect registration loops in `bench.yml`).

Prerequisites: Docker, `go` (for the loader), `benchi` (v0.4.1+), and the
images `postgres:17`, `alpine:3.20`, `apache/kafka:4.0.0`,
`quay.io/debezium/connect:3.2`,
`docker.redpanda.com/redpandadata/connect` (pinned by digest in
`tools/compose-redpanda.yml`; that arm is blocked and was not run), and the MinIO build
`pgsty/minio:RELEASE.2026-08-04T00-00-00Z@sha256:b6bfe7239bfc83fb90d31612d9704d86039dd714f7904b3f1ad68f211e602372`
(the S3-destination arms' object store; `minio/minio` was removed from Docker
Hub, and `pgsty/minio` is the maintained community build the rest of the
ecosystem pins). `run.sh` builds the Conduit image from the working tree and the
Connect image from `tools/connect.Dockerfile` (which also downloads the Confluent
JDBC sink connector + Postgres driver and the Confluent S3 sink connector from
Confluent).

## Results

See [`results.md`](results.md) for the verdict, tables (median, min-max, A/A
spread), bottleneck analysis and caveats. Raw data (runs.csv, environment.txt,
per-run CSVs, logs gzipped, CPU profiles) is in `results-aws/`; regenerate any
table with `python3 scripts/summarize.py results-aws/<run dir>`.

Headline (AWS c6i.2xlarge, shipped defaults, 20,000 ops/s fixed): file sink and
S3 destination at parity with Kafka Connect + Debezium; Postgres and Kafka
destinations at 0.30x because the destinations write one record per round trip
until `sdk.batch.size` is set. Tuned configurations are reported separately and
labelled in `results.md`; they are not defaults.

# v0.21 close-out: Conduit vs Kafka Connect (Debezium) vs Redpanda Connect on Postgres CDC

A reproducible, ground-truth comparison of CDC pipelines on one equivalent
workload: one or more Postgres tables (`TABLES`, default 1) receiving a steady,
fixed-rate stream of INSERT/UPDATE/DELETE, captured by each system and delivered
to a sink whose accumulated count is the measured quantity. Two destination
kinds are measured:

- **file sink** (`conduit`, `conduit-aa`, `connect`, `redpanda-connect`): one
  sink file per table, whose summed line count (`wc -l`) is the metric.
- **Postgres destination** (`conduit-pg`, `connect-pg`): one destination table
  (`bench_dest_i`) per source table, whose summed row count (`SELECT count(*)`)
  is the metric.

The method follows `benchi/METHODOLOGY.md`: the metric is **ground truth**
(what the sink actually wrote), every comparison carries an **A/A control** to
establish the noise floor, and results are **medians with variance**, never a
single best run.

## The pipelines

All arms capture the SAME set of tables (`bench_source_0` ..
`bench_source_{TABLES-1}`, each `(id bigint PRIMARY KEY, payload text)`,
`REPLICA IDENTITY DEFAULT`) over the SAME pgoutput logical-replication wire
protocol, with the snapshot disabled so only the loader's steady-state stream
is captured.

| arm | pipeline | config |
| --- | --- | --- |
| `conduit` | `TABLES` pipelines, each a `builtin:postgres` source (`cdcMode: logrepl`, `snapshotMode: never`, `withAvroSchema: false`, `sdk.batch.size: CONDUIT_BATCH_SIZE`) → `builtin:file` sink | `pipelines/conduit.yml` (generated), `tools/compose-conduit.yml` |
| `conduit-aa` | byte-for-byte the same as `conduit` (the A/A control; only the container name differs) | `tools/compose-conduit-aa.yml` |
| `connect` | `TABLES` Debezium Postgres source connectors (`plugin.name: pgoutput`, `snapshot.mode: never`, `tombstones.on.delete: false`) → Kafka broker → `TABLES` `FileStreamSinkConnector`s | `tools/compose-connect.yml`, `tools/connect.Dockerfile`, registration loop in `bench.yml` |
| `redpanda-connect` | `postgres_cdc` input (`stream_snapshot: false`) → `file` output (`codec: lines`); single-table, blocked by licensing | `config/redpanda.yaml`, `tools/compose-redpanda.yml` |
| `conduit-pg` | `TABLES` pipelines, each a `builtin:postgres` source (same config as `conduit`) → `builtin:postgres` destination (`table: bench_dest_i`, `sdk.batch.size`/`sdk.batch.delay` on both source and destination) | `pipelines/conduit-pg.yml` (generated), `tools/compose-conduit-pg.yml` |
| `connect-pg` | `TABLES` Debezium Postgres source connectors (`pgoutput`, `snapshot.mode: never`, `unwrap` transform) → Kafka broker → `TABLES` Confluent JDBC sink connectors (`insert.mode: upsert`, `pk.mode: record_key`, `delete.enabled`) writing `bench_dest_i` | `tools/compose-connect-pg.yml`, `tools/connect.Dockerfile`, registration loop in `bench.yml` |

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

## What is NOT equivalent

- **Kafka Connect requires a broker.** The `connect` and `connect-pg` arms have
  an extra hop (Postgres → Debezium → Kafka → sink) and a JVM Connect worker.
  Conduit and Redpanda Connect are broker-neutral single processes. The extra
  hop is part of what "Kafka Connect + Debezium" is, so it is measured, but a
  reader should not attribute the whole difference to CDC capture efficiency.
- **The ground-truth metric differs between destination kinds.** The file-sink
  arms count every CDC event (one line per INSERT/UPDATE/DELETE). The
  Postgres-destination arms count **live rows**, not events: the destination
  upserts on the key, so INSERTs add a row, UPDATEs rewrite in place, and
  DELETEs remove a row. `rows_delta` for a Postgres-destination arm is
  therefore the *net* rows captured (≈ inserts − deletes, ~60% of the raw event
  count at the default 70/20/10 mix) — a LOWER number than the file arms'
  event count, and **not directly comparable to it**. The two Postgres-
  destination arms are comparable to *each other*; the two file-sink arms to
  *each other*.
- **Postgres sinks write differently under the hood.** The builtin:postgres
  destination builds per-record `INSERT … ON CONFLICT (id) DO UPDATE` /
  `DELETE` statements and batches via the engine's funnel batching
  (`sdk.batch.size`/`sdk.batch.delay`). The Confluent JDBC sink batches
  inserts (`batch.size: 3000`) and issues its own upsert/delete SQL. Both
  batch, which is why the sink is not the bottleneck and the SOURCE is what is
  measured — this is a parity check, not expected to flip throughput.
- **Record encoding differs on the Connect Postgres arm.** The JDBC sink needs
  the record key schema to extract the primary key (`pk.mode: record_key`), so
  `connect-pg` runs `key.converter`/`value.converter` with
  `schemas.enable: true` and flattens the Debezium envelope with
  `ExtractNewRecordState` (`unwrap`). That embeds a JSON schema per Kafka
  message — overhead Conduit's structured record path doesn't carry. It is a
  write-path/serialization difference, not a source-capture difference.
- **Batching defaults differ.** Conduit runs `sdk.batch.size: 1000`
  (tunable via `CONDUIT_BATCH_SIZE`, plus `sdk.batch.delay: 100ms`); Debezium's
  `max.batch.size` defaults to 2048 and the JDBC sink's `batch.size` to 3000.
  This is a shipped-default difference, not a workload difference.
- **Record bytes differ.** Conduit writes the OpenCDC record, Debezium the
  Debezium envelope, Redpanda its message. The metrics (line count / row count)
  are insensitive to byte size, but the sinks do not emit identical content.
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
means nothing.** Every result below is reported next to the A/A spread, and the
A/A control runs interleaved with the A/B arms (rotated order), not as a
separate block.

## The Postgres-destination arms are a parity check

The two Postgres-destination arms exist to measure the real "CDC→Postgres"
workload people index on: Conduit `builtin:postgres → builtin:postgres` vs
Debezium Postgres → Kafka Connect JDBC sink. Because both sinks batch writes
internally, the sink is not the limiter; the measured quantity is how fast the
SOURCE captures CDC events. Both sources read the same pgoutput stream, so the
expected result is **parity** — equal `rows_delta` per window — not a throughput
flip. A systematic gap between `conduit-pg` and `connect-pg` would point at the
source (logrepl vs Debezium) or the Kafka hop, and would need to be read
against the A/A floor before it means anything.

Because the destination mirrors the source's LIVE rows, the loader must not
write before the `snapshotMode: never` source has created its replication slot:
a row written before the slot exists would never be captured, and updates to
those phantom rows would upsert-INSERT extra rows (while deletes of them would
be no-ops), inflating `rows_delta` by however long the tool took to start.
`run.sh` therefore passes `LOADER_DELAY` (default `120s`) to the loader on the
two Postgres-destination arms only, so the loader waits for the slot before its
first write. The file-sink arms count events, not net rows, so they need no
delay and pass `0`.

## Redpanda Connect: blocked by licensing (honest status)

`redpanda-connect` is a real arm with a complete, committed config — but it
cannot run on the free edition. `postgres_cdc` is a Redpanda Connect
**Enterprise** component; at startup the stock image fails with:

```
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
  rows the destination holds, i.e. the *net* rows captured.

```
throughput = (ROWS_AFTER - ROWS_BEFORE) / test_duration
```

`bench.yml` runs a 60 s steady-state window per arm. The `pre-test` hook waits
for the sink to start accumulating (pipeline startup falls outside the window),
warms up 15 s, then records `ROWS_BEFORE`; the `post-test` hook records
`ROWS_AFTER`. `run.sh` computes the per-run rate and reports medians with
variance over the rounds. The count hooks run in the `cdcparity-counter`
container (`psql` is installed there), against the same Postgres the arm writes.

## Reproducing

```bash
# local
cd benchi/v021-cdc-parity
./run.sh 3 conduit conduit-aa connect                    # 3 rounds, A/A + file arms
./run.sh 3 conduit-pg connect-pg                         # 3 rounds, Postgres-destination parity pair
./run.sh 3 conduit conduit-aa connect conduit-pg connect-pg   # both destination kinds

# N-way (2 tables): 2 sources + 2 sinks per arm, summed ground truth
TABLES=2 ./run.sh 3 conduit-pg connect-pg

# sweep Conduit source batching
CONDUIT_BATCH_SIZE=2048 ./run.sh 3 conduit-pg

# dedicated AWS instance (see run-aws.sh)
TABLES=2 ./run-aws.sh 3 conduit conduit-aa connect conduit-pg connect-pg
```

`TABLES` (default 1; sweep 1, 2, 4), `CONDUIT_BATCH_SIZE` (default 1000),
`CONDUIT_BATCH_DELAY` (default 100ms) and `LOADER_DELAY` (default 120s,
Postgres-destination arms only) are read by `run.sh`, which runs
`scripts/generate.sh` to regenerate `pipelines/conduit.yml`,
`pipelines/conduit-pg.yml` and `infra/init.sql` for the given `TABLES`, then
exports `TABLES` to docker compose (the loader's `--tables` flag, and the
counter container's env for the connect registration loops in `bench.yml`).

Prerequisites: Docker, `go` (for the loader), `benchi` (v0.4.1+), and the
images `postgres:17`, `alpine:3.20`, `apache/kafka:4.0.0`,
`quay.io/debezium/connect:3.2`, and
`docker.redpanda.com/redpandadata/connect:latest`. `run.sh` builds the Conduit
image from the working tree and the Connect image from `tools/connect.Dockerfile`
(which also downloads the Confluent JDBC sink connector and Postgres driver from
Maven).

## Results

To be recorded here from the dedicated-hardware run. A number without its A/A
floor is not evidence, so this section will report the A/A spread next to every
arm's median, with variance, and will state only what the floor allows.

# v0.21 close-out: Conduit vs Kafka Connect (Debezium) vs Redpanda Connect on Postgres CDC

A reproducible, ground-truth comparison of three CDC pipelines on one
equivalent workload: one or more Postgres tables (`TABLES`, default 1)
receiving a steady, fixed-rate stream of INSERT/UPDATE/DELETE, captured by each
system and delivered to one sink file per table, whose summed row count is the
measured quantity.

The method follows `benchi/METHODOLOGY.md`: the metric is **ground truth**
(records the sinks actually wrote), every comparison carries an **A/A control**
to establish the noise floor, and results are **medians with variance**, never
a single best run.

## The three pipelines

All three capture the SAME set of tables (`bench_source_0` ..
`bench_source_{TABLES-1}`, each `(id bigint PRIMARY KEY, payload text)`,
`REPLICA IDENTITY DEFAULT`) over the SAME pgoutput logical-replication wire
protocol, with the snapshot disabled so only the loader's steady-state stream
is captured. Each arm writes one line per CDC event to one sink file per table
on a shared volume. Ground truth = the SUM of `wc -l` across the sink files,
measured before and after each 60 s window by the same hook in the same counter
container (the Conduit and Connect arms write `/sink/out_0.txt` ..
`/sink/out_{TABLES-1}.txt`; Redpanda is single-table and writes `/sink/out.txt`).

| arm | pipeline | config |
| --- | --- | --- |
| `conduit` | `TABLES` pipelines, each a `builtin:postgres` source (`cdcMode: logrepl`, `snapshotMode: never`, `withAvroSchema: false`, `sdk.batch.size: CONDUIT_BATCH_SIZE`) → `builtin:file` sink | `pipelines/conduit.yml` (generated), `tools/compose-conduit.yml` |
| `conduit-aa` | byte-for-byte the same as `conduit` (the A/A control; only the container name differs) | `tools/compose-conduit-aa.yml` |
| `connect` | `TABLES` Debezium Postgres source connectors (`plugin.name: pgoutput`, `snapshot.mode: never`, `tombstones.on.delete: false`) → Kafka broker → `TABLES` `FileStreamSinkConnector`s | `tools/compose-connect.yml`, `tools/connect.Dockerfile`, registration loop in `bench.yml` |
| `redpanda-connect` | `postgres_cdc` input (`stream_snapshot: false`) → `file` output (`codec: lines`); single-table, blocked by licensing | `config/redpanda.yaml`, `tools/compose-redpanda.yml` |

The workload generator (`loader/`) is the SAME container for every arm: a Go
program writing a 70/20/10 INSERT/UPDATE/DELETE mix at a fixed `-rate` (default
20 000 ops/s) through `pgx.Batch`, split round-robin across `TABLES` tables
(total rate unchanged), so it is never the bottleneck.

## What is equivalent (by construction)

- **Source tables + row shape**: `bench_source_0` .. `bench_source_{TABLES-1}`,
  each `(id bigint PRIMARY KEY, payload text)`, fixed 64-byte payload, same for
  every arm.
- **Rate**: the same loader, same `-rate`, same 70/20/10 mix, split round-robin
  across the tables, for every arm. Total offered load is identical regardless
  of `TABLES`; only the parallelism differs.
- **N-way split**: for a given `TABLES`, the Conduit arm runs `TABLES`
  independent pipelines (one source + one file sink each) and the Connect arm
  runs `TABLES` Debezium sources + `TABLES` FileStreamSinkConnectors (one per
  table → topic → sink), so both systems are measured at the same fan-out.
- **Wire protocol**: all three use Postgres `pgoutput` logical replication.
- **Snapshot**: disabled in all three (`snapshotMode: never` /
  `snapshot.mode: never` / `stream_snapshot: false`), so the number reflects
  steady-state CDC, not a one-time table dump.
- **Sink + metric**: each arm writes one line per event to its sink file(s),
  and the SUM of `wc -l` across them is the metric. No engine metric is read
  anywhere (`msg-rate-per-second` is the retracted metric; it is not used).

## What is NOT equivalent

- **Kafka Connect requires a broker.** The `connect` arm has an extra hop
  (Postgres → Debezium → Kafka → FileStreamSink) and a JVM Connect worker.
  Conduit and Redpanda Connect are broker-neutral single processes. The extra
  hop is part of what "Kafka Connect + Debezium" is, so it is measured, but a
  reader should not attribute the whole difference to CDC capture efficiency.
- **Batching defaults differ.** Conduit's source now runs with
  `sdk.batch.size: 1000` (tunable via `CONDUIT_BATCH_SIZE`, plus a
  `sdk.batch.delay` of `100ms` so a partial batch still flushes), where the
  default `sdk.batch.size: 0` is a batch of ONE record per read. Debezium's
  `max.batch.size` defaults to 2048 and Redpanda streams with
  `checkpoint_limit: 1024`. This is a shipped-default difference, not a
  workload difference; it is exactly the kind of thing the benchmark exists to
  surface.
- **Record bytes differ.** Conduit writes the OpenCDC record, Debezium the
  Debezium envelope, Redpanda its message. `wc -l` counts lines, so byte size
  is irrelevant to the metric — but the three sinks do not emit identical
  content.
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

Ground truth is lines written to the sink files, not any system's metrics:
`ROWS_BEFORE` and `ROWS_AFTER` are the SUM of `wc -l` across `/sink/out*.txt`
(the Conduit and Connect arms write one file per table, Redpanda one file), so
the number is the total CDC events delivered regardless of `TABLES`.

```
throughput = (ROWS_AFTER - ROWS_BEFORE) / test_duration
```

`bench.yml` runs a 60 s steady-state window per arm. The `pre-test` hook waits
for the sinks to start accumulating (pipeline startup falls outside the
window), warms up 15 s, then records `ROWS_BEFORE`; the `post-test` hook
records `ROWS_AFTER`. `run.sh` computes the per-run rate and reports medians
with variance over the rounds.

## Reproducing

```bash
# local
cd benchi/v021-cdc-parity
./run.sh 3 conduit conduit-aa connect           # 3 rounds, A/A + 2 real arms

# N-way (2 tables): 2 sources + 2 sinks per arm, summed ground truth
TABLES=2 ./run.sh 3 conduit conduit-aa connect

# sweep Conduit source batching
CONDUIT_BATCH_SIZE=2048 ./run.sh 3 conduit

# dedicated AWS instance (see run-aws.sh)
TABLES=2 ./run-aws.sh 3 conduit conduit-aa connect
```

`TABLES` (default 1; sweep 1, 2, 4), `CONDUIT_BATCH_SIZE` (default 1000) and
`CONDUIT_BATCH_DELAY` (default 100ms) are read by `run.sh`, which runs
`scripts/generate.sh` to regenerate `pipelines/conduit.yml` and
`infra/init.sql` for the given `TABLES`, then exports `TABLES` to docker
compose (the loader's `--tables` flag, and the counter container's env for the
connect registration loop in `bench.yml`).

Prerequisites: Docker, `go` (for the loader), `benchi` (v0.4.1+), and the
images `postgres:17`, `alpine:3.20`, `apache/kafka:4.0.0`,
`quay.io/debezium/connect:3.2`, and
`docker.redpanda.com/redpandadata/connect:latest`. `run.sh` builds the Conduit
image from the working tree and the Connect image from `tools/connect.Dockerfile`.

## Results

To be recorded here from the dedicated-hardware run. A number without its A/A
floor is not evidence, so this section will report the A/A spread next to every
arm's median, with variance, and will state only what the floor allows.

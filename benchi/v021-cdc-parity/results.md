# v0.21 CDC parity: Conduit vs Kafka Connect + Debezium, AWS c6i.2xlarge

Run date 2026-10-19/20 (UTC). Every number below comes from `results-aws/`; regenerate the
tables with `python3 scripts/summarize.py results-aws/<run dir>`.

> **Defaults-audit note (read first).** This run set `logrepl.withAvroSchema:
> "false"` on the Conduit source and ran Conduit with `--log.level error
> --log.format json`. Both ship differently (Avro `true`, info logging), so the
> numbers below are shipped defaults **except those two overrides**, which
> favour Conduit. `snapshotMode: never` is a workload choice, not a default.
> The A/A control exists only on the file arm. A rerun with the audited kit
> (both engines, an A/A twin on every Conduit arm, overrides removed) is
> planned; treat this run as the pre-audit baseline. The Postgres and Kafka
> gaps are about destination batching and are not explained by those two
> overrides, but their size at true defaults is unmeasured.

## Verdict

At **shipped defaults**, offered load 20,000 ops/s:

| destination | Conduit vs Connect | verdict |
| --- | --- | --- |
| file | 0.997 (A/A floor 0.07% median, 0.13% max) | parity |
| S3 (MinIO) | 1.020 | parity or better; both arms saturate near 18k ev/s |
| Postgres | **0.302** | **below parity, 3.3x slower** |
| Kafka | **0.301** | **below parity, 3.3x slower** |

Both gaps have one cause: at shipped defaults the Conduit Postgres and Kafka
destinations write one record per round trip (`sdk.batch.size` unset). Setting
`sdk.batch.size=1000` / `sdk.batch.delay=100ms` closes both gaps (ratios 0.999
and 1.002). That is a **tuned** configuration and is reported separately below.
It is not a claim about defaults.

## Run directories

`results-aws/` holds one directory per invocation: `phase1-shipped-defaults`,
`phase2a-tuned-batch1000`, `phase2b-tuned-cache_statement`,
`phase2c-tuned-batch1000-cache_statement` and `profile-shipped-defaults`. They
were renamed after the run from the timestamped names `run.sh` produced on the
instance; `run.sh` now appends the sanitized `RUN_LABEL` to the directory name.
Each directory's `environment.txt` carries its `label` line.

## What was run

- **Instance:** one dedicated AWS `c6i.2xlarge` (8 vCPU, 16 GiB), us-west-1,
  Ubuntu 24.04, Docker 29.9.1, benchi v0.4.1, terraform in `terraform/`. Postgres
  data, MinIO data on tmpfs; Kafka, Connect, Conduit and the loader share the box.
- **Conduit:** `ConduitIO/conduit` main at `9f11b4668fe7` (v0.21.0-nightly,
  includes #3217). Built from that commit's `Dockerfile`. Bundled connectors:
  postgres v0.17.2, s3 v0.11.1 (#3220, s3 v0.11.2, was still open, so not
  used), kafka v0.14.1, file v0.10.10, sdk v0.17.0. **Classic (v1) engine**:
  no `--preview.pipeline-arch-v2`, which is the shipped default.
- **Kafka Connect side:** image `quay.io/debezium/connect:3.2`. The tag is the
  **Debezium** version: Debezium Postgres connector **3.2.4.Final**. The
  **Kafka Connect runtime** in that image is **4.0.0** (`connect-runtime-4.0.0.jar`),
  against an `apache/kafka:4.0.0` KRaft broker. Sinks: Confluent JDBC
  10.9.6 (`batch.size=3000`), Confluent S3 12.1.11 (`flush.size=1000`),
  FileStreamSink (Kafka 4.0.0). Image digests are in `environment.txt`.
- **Postgres:** 17.11. **Workload:** the committed loader, 70/20/10
  INSERT/UPDATE/DELETE, one table, 64-byte payload, `snapshotMode: never`.
- **Rate:** **20,000 ops/s fixed.** At 50,000 (and the 35,000 of the earlier
  runs) the window measured backlog drain: the file arm "delivered" 52-60k
  rec/s against 35-50k offered because the counter window included a backlog
  accumulated during startup. At 30,000 (probe, not committed as a result) the
  S3 arms of both systems already fell behind. 20,000 is sustained by Connect on
  file, Postgres and Kafka, and is near the S3 ceiling of both. The Postgres
  arms count net live rows, so their offered rate is about 12,000 net rows/s.
- **Method (benchi/METHODOLOGY.md):** ground-truth count at the destination
  (`wc -l`, `SELECT count(*)`, summed MinIO object lines, Kafka end offsets), a
  60 s window after a 15 s post-startup warmup, one discarded warmup round, **5
  measured rounds per arm**, arms interleaved in rotated (Latin square) order, an
  A/A control (`conduit-aa`) in the same rotation, medians with min-max. Rates
  divide by the **measured** window (Postgres-clock timestamps at the two counts),
  not a nominal 60 s; the S3 count downloads the whole bucket and stretches the
  window to 70.0-71.8 s (Conduit 70.0-70.1, Connect 71.4-71.8).

## Shipped defaults (headline)

No `sdk.batch.*` settings, no connection-URL parameters: the pipeline files in
`pipelines/` as generated with `CONDUIT_BATCH_SIZE=default`. Source and
destination are exactly what Conduit ships.
Run: `results-aws/phase1-shipped-defaults/` (5 rounds + warmup, 9 arms).

| arm | unit | median /s | min-max /s | spread | median CPU (tool / broker / pg), % of 1 core |
| --- | --- | --- | --- | --- | --- |
| `conduit` (file) | events | 19,766 | 19,756 - 19,778 | 0.1% | 90 / - / 33 |
| `conduit-aa` (A/A) | events | 19,771 | 19,752 - 19,779 | 0.1% | 89 / - / 33 |
| connect (Debezium -> FileStreamSink) | events | 19,844 | 19,737 - 19,886 | 0.8% | 98 / 15 / 31 |
| `conduit-pg` | net rows | 3,570 | 3,555 - 3,580 | 0.7% | 107 / - / 97 |
| connect-pg (Debezium -> JDBC) | net rows | 11,834 | 11,806 - 11,843 | 0.3% | 102 / 16 / 68 |
| `conduit-s3` | events | 17,964 | 17,955 - 18,003 | 0.3% | 92 / - / 33 |
| connect-s3 (Debezium -> Confluent S3) | events | 17,604 | 17,553 - 17,703 | 0.9% | 121 / 17 / 33 |
| `conduit-kafka` | events | 5,943 | 5,926 - 5,968 | 0.7% | 113 / 149 / 27 |
| connect-kafka (Debezium -> Kafka) | events | 19,728 | 19,706 - 19,845 | 0.7% | 51 / 12 / 31 |

Paired per-round ratios (Conduit / Connect, same round):

| pair | median | rounds 1-5 |
| --- | --- | --- |
| `conduit` vs `connect` (file) | 0.997 | 1.001, 0.995, 0.997, 0.997, 0.995 |
| `conduit-aa` vs connect (file) | 0.997 | 1.002, 0.995, 0.997, 0.998, 0.993 |
| `conduit-pg` vs connect-pg | 0.302 | 0.302, 0.302, 0.301, 0.303, 0.300 |
| `conduit-s3` vs connect-s3 | 1.020 | 1.023, 1.023, 1.020, 1.017, 1.017 |
| `conduit-kafka` vs connect-kafka | 0.301 | 0.302, 0.302, 0.301, 0.301, 0.299 |

**A/A:** `conduit` vs `conduit-aa` per-round |delta| median 0.07%, max 0.13%. The
file-sink parity (-0.3%) is 4x the A/A median and inside the within-arm spread
of Connect (0.8%): call it parity, not a win. The A/A control exists only on the
file arm; for the other pairs the floor is the within-arm min-max in the table
(0.1-0.9%). The Postgres and Kafka gaps (-70%) and the S3 edge (+2.0%, five
rounds all at or above 1.017, outside the 0.3% and 0.9% spreads) are far outside it.

**Constant lag in Connect's file arm.** Connect shows a flat lag of about 337k
events (roughly 17 s at 20k/s): the extra Postgres -> Kafka -> sink hop keeps
that much in flight. It is steady, so the arm is sustained; it is latency, not
lost throughput.

**Sustained or behind?** Lag is the source change counter minus the destination
count, at window start and end. File, Connect Postgres, Connect Kafka stay flat
(sustained at 20k). `conduit-pg` lag grows 129k -> 622k and `conduit-kafka`
287k -> 1.14M over one window: they fall further behind every second, so their
rate is their **capacity** at this load, not the offered rate. Both S3 arms
also grow (Conduit 22k -> 161k, Connect 36k -> 192k): 20k is above both
S3 ceilings, so the S3 numbers are saturation throughput for both, compared
like for like.

## Bottleneck: Postgres and Kafka destinations at defaults

Evidence from a CPU profile of each arm at shipped defaults
(`results-aws/profile-shipped-defaults/*/prof/`, one run each, 76-82 s, Conduit
`--dev.cpuprofile`; top tables in `cpu-top-cum.txt`).

**`conduit-pg`.** 40.7% of CPU time is under
`sdk.(*writeStrategySingle).Write` -> `postgres.(*Destination).Write` ->
`execBatch` -> `pgx.(*Conn).sendBatchQueryExecModeDescribeExec` (23.8%) ->
`pgconn.(*Pipeline).Sync/Flush` (17.7%). `writeStrategySingle` is the SDK path
for `sdk.batch.size` unset: one `Write` per record, and in `describe_exec` mode
each write is a Parse/Describe/Bind/Execute/Sync round trip. Syscalls are 24.6%
of samples and the scheduler idle/steal path (`findRunnable`) another 20%, the signature of a
latency-bound loop, not a compute-bound one. Conduit uses about one core
and the Postgres container is at 97% of a core (68% for the same table under
JDBC at 3.3x the throughput). The pipeline is serialised on the destination
round trip, about 170 us per record.

**`conduit-kafka`.** 11.4% of CPU is under `sdk.(*writeStrategySingle).Write` ->
`kafka.(*Destination).Write` -> `FranzProducer.Produce`, with the `kgo` request and
response handlers at 18% and 10%, and `Source.Ack` at 5%. One produce request per
record with `acks=all` also costs the broker: **149% CPU at 5.9k msg/s versus
12% at 19.7k msg/s for Debezium**, which batches in its producer. Conduit
spends 113% of a core to deliver 30% of the messages.

**Evidence the cause is batching, not a code path regression:** the same two
pipelines with `sdk.batch.size=1000`, `sdk.batch.delay=100ms` sustain the full
offered load (below). For the Postgres arm, `cache_statement` alone does help
(3,570 -> 4,923 net rows/s, +38%) but does not close the gap (0.416); batching
does.

**Candidate fixes (not applied in this lane):**

1. Ship a destination write batch by default in the classic engine for the
   built-in postgres and kafka destinations (batch size and delay set by the
   connector, not left to each pipeline author). The arch-v2 funnel already
   defaults to 3000 rows (#3203) when delay-only batching is on; the classic
   engine and the SDK `writeStrategySingle` default do not.
2. For the Postgres destination, keep the exec mode default and make batching
   the default; `cache_statement` is only worth +38% alone and carries
   schema-drift risk (`conduit-connector-postgres`#437), so it must not be the
   shipped fix.
3. If the default stays unbatched, document the gap prominently in the
   Kafka Connect migration guide: a migrated pipeline gets 30% of Connect's
   throughput on these two destinations until `sdk.batch.size` is set.

## Tuned configurations (NOT defaults)

Offered load 20,000 ops/s, 5 rounds + warmup, interleaved. Each block is its own
invocation (the source of tuning is the label); within a block the Conduit and
Connect arms are interleaved. The Connect arms are the same unchanged Connect
config as at defaults.

| run | label | arm | median /s | min-max /s | ratio vs connect (median) | lag start -> end |
| --- | --- | --- | --- | --- | --- | --- |
| `phase2a-tuned-batch1000` | **TUNED**: `sdk.batch.size=1000`, `sdk.batch.delay=100ms` on source and destination | `conduit-pg` | 11,836 | 11,829 - 11,846 | 0.999 | 631 -> 1,179 |
| | | connect-pg | 11,841 | 11,805 - 11,877 | | -975 -> -1,147 |
| | | `conduit-kafka` | 19,754 | 19,747 - 19,776 | 1.002 | -8,000 -> -2,000 |
| | | connect-kafka | 19,725 | 19,634 - 19,821 | | 9,710 -> 4,389 |
| `phase2b-tuned-cache_statement` | **TUNED**: postgres destination `default_query_exec_mode=cache_statement` only | `conduit-pg` | 4,923 | 4,918 - 4,926 | 0.416 | 106k -> 527k (behind) |
| | | connect-pg | 11,820 | 11,815 - 11,848 | | 2,897 -> 3,896 |
| `phase2c-tuned-batch1000-cache_statement` | **TUNED**: batch 1000/100ms + `cache_statement` | `conduit-pg` | 11,837 | 11,823 - 11,847 | 1.000 | -7,121 -> -7,133 |
| | | connect-pg | 11,833 | 11,794 - 11,877 | | 536 -> 907 |

Reading this: once Conduit sustains 20k the delivered rate equals the offered
rate, so "parity" means **Conduit sustains the offered load with no growing
lag**. It says nothing about headroom above 20k. Headroom was not measured in
this run, and no "faster than Connect" claim is made for the tuned arms.
`cache_statement` adds nothing once batching is on (11,837 vs 11,836), so it is
not needed to reach parity here.

## Caveats

- **One instance, one region, one run date.** The instance stayed quiet (CPU
  74% idle in vmstat) but nothing here is a multi-instance replication.
- **The A/A control is on the file arm only.** Other pairs rely on within-arm
  min-max and the size of the effect.
- **Lag is approximate.** The source counters are `pg_stat_user_tables`,
  which Postgres flushes about once a second (about 20k events at this rate).
  Use lag for direction
  (flat, growing, shrinking), not for exact backlog.
- **Metric differs by destination.** Postgres arms count net live rows
  (about 60% of events), S3 and Kafka and file arms count events. Compare a
  Conduit arm with its own Connect pair only.
- **Classic engine only.** arch-v2 is not the shipped default yet and was not
  measured.
- **Profiles are single diagnostic runs**, taken after the measured runs, never
  used for a rate.
- **Earlier results are not carried over.** The 35k-rate results previously
  on the branch used a branch build with unmerged funnel changes and
  `cache_statement` forced on, and are dropped from this PR.

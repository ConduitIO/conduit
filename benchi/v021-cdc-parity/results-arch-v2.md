# v0.21 arch-v2 parity: Conduit vs Kafka Connect + Debezium, AWS c6i.2xlarge

Run date 2026-10-20/21 (UTC). Data in `results-aws/arch-v2-*`; regenerate any
table with `python3 scripts/summarize.py results-aws/<run dir>`. This replaces
the classic-engine run in [`results.md`](results.md) as the performance
reference: the classic engine is being retired, and that run also overrode
Avro and logging (see its audit note).

## Verdict

Arch-v2 at **shipped defaults**, offered load 20,000 ops/s, every Conduit arm
with its own A/A twin: **parity on all four destinations** (ratios 0.998,
0.999, 1.003, 0.998). The classic-engine gaps on Postgres and Kafka are gone.

20k hides headroom, so a labelled saturation phase at 40,000 ops/s found one
gap: the **Postgres destination saturates at 14.8k net rows/s against 19.1k for
Kafka Connect (0.77x)**. The cause is the destination's default
`describe_exec` query mode. With `default_query_exec_mode=cache_statement`
(tuned, not a default) the same arm reaches 23.4k net rows/s, 1.23x Connect.
File and Kafka stay at parity at 40k (the loader tops out at about 39.1k
ops/s, so those two show headroom of at least 39k, not their ceiling).

## What was run

- **Instance:** one dedicated `c6i.2xlarge` (8 vCPU, 16 GiB), us-west-1, Ubuntu
  24.04, Docker 29.9.2, benchi v0.4.1. SSH limited to the runner's /32.
- **Conduit:** `ConduitIO/conduit` main at `9e6be91baba7` (v0.21.0-nightly,
  includes #3245), built from its `Dockerfile`, **arch-v2**
  (`--preview.pipeline-arch-v2`, the funnel batches destination writes at its
  3000-row default). Bundled connectors: kafka v0.14.2 (producer-linger fix),
  s3 v0.11.1, file v0.10.10, sdk v0.17.2, commons v0.12.0. **Postgres
  connector v0.17.2.** v0.17.3 only changes an Open-time slot check, not the hot
  path, so v0.17.2 is the version recorded in `environment.txt`.
- **Shipped defaults, nothing overridden:** default info logging, Avro on
  (`logrepl.withAvroSchema` not set), no `sdk.batch.*`, default postgres
  destination exec mode. No DLQ and no `sdk.batch.*` appear in any pipeline
  (arch-v2 refuses a DLQ with `sdk.batch.size` above 1 and the funnel strips
  `sdk.batch.*` from destinations). `snapshotMode: never` is a **workload
  choice** (steady-state CDC only), not a default.
- **Connect side:** `quay.io/debezium/connect:3.2` is the **Debezium** tag:
  Debezium Postgres connector 3.2.4.Final on **Kafka Connect runtime 4.0.0**,
  `apache/kafka:4.0.0` broker, JDBC sink 10.9.6 (`batch.size=3000`), Confluent
  S3 sink 12.1.11, FileStreamSink. Digests in `environment.txt`. Debezium runs
  schemaless JSON; Conduit encodes Avro and registers schemas at defaults, so
  this is a non-equivalence that costs Conduit work Connect does not do.
- **Method:** ground-truth destination counts (`wc -l`, `SELECT count(*)`,
  MinIO object lines, Kafka end offsets), 60 s window after a 15 s startup
  warmup, 1 discarded warmup round, **5 measured rounds**, rotated (Latin
  square) order, an `-aa` twin of every Conduit arm in the same rotation,
  medians with min-max. Rates divide by the measured window (S3's bucket
  download stretches it to 70-72 s).
- **Workload:** committed loader, 70/20/10 INSERT/UPDATE/DELETE, one table,
  64-byte payload. Postgres arms count net live rows (about 60% of events).

## Shipped defaults, 20,000 ops/s (`arch-v2-shipped-defaults`)

| arm | unit | median /s | min-max /s | spread | CPU % of one core: tool / broker / postgres |
| --- | --- | --- | --- | --- | --- |
| `conduit` (file) | events | 19,780 | 19,754 - 19,791 | 0.2% | 57 / - / 31 |
| `conduit-aa` | events | 19,782 | 19,766 - 19,799 | 0.2% | 56 / - / 31 |
| `connect` | events | 19,811 | 19,682 - 19,870 | 0.9% | 97 / 14 / 31 |
| `conduit-pg` | net rows | 11,835 | 11,831 - 11,845 | 0.1% | 58 / - / 92 |
| `conduit-pg-aa` | net rows | 11,832 | 11,814 - 11,834 | 0.2% | 58 / - / 91 |
| `connect-pg` | net rows | 11,845 | 11,800 - 11,863 | 0.5% | 110 / 15 / 69 |
| `conduit-s3` | events | 17,646 | 17,613 - 17,739 | 0.7% | 66 / - / 32 |
| `conduit-s3-aa` | events | 17,597 | 17,431 - 18,245 | 4.6% | 67 / - / 32 |
| `connect-s3` | events | 17,591 | 17,291 - 17,645 | 2.0% | 130 / 18 / 33 |
| `conduit-kafka` | events | 19,769 | 19,758 - 19,775 | 0.1% | 61 / 11 / 32 |
| `conduit-kafka-aa` | events | 19,772 | 19,762 - 19,783 | 0.1% | 61 / 12 / 32 |
| `connect-kafka` | events | 19,798 | 19,752 - 19,845 | 0.5% | 54 / 13 / 31 |

| pair | Conduit / Connect (median) | rounds 1-5 | A/A per-round \|delta\| median / max |
| --- | --- | --- | --- |
| file | 0.998 | 0.998, 1.004, 0.997, 0.995, 1.005 | 0.10% / 0.11% |
| Postgres | 0.999 | 0.998, 1.000, 0.999, 1.003, 0.999 | 0.02% / 0.24% |
| S3 | 1.003 | 0.999, 1.008, 1.021, 1.000, 1.003 | 1.03% / 3.37% |
| Kafka | 0.998 | 0.998, 0.998, 1.000, 1.001, 0.996 | 0.03% / 0.12% |

- **Parity means "sustains the offered load".** File, Postgres and Kafka lag
  stay flat for both systems. At 20k both S3 arms are saturated near 17.6k
  ev/s (lag grows 40k -> 194k for Conduit, 37k -> 195k for Connect), so S3 is
  compared at saturation. S3 is the noisy pair: its A/A floor is 1.0% median and
  3.4% max, so the +0.3% is not a difference.
- **Connect file arm lag:** a flat lag of about 335k events (17 s) from the
  extra Kafka hop; steady, so sustained.
- **Efficiency:** at the same delivered rate Conduit uses less CPU on file (57
  vs 97), Postgres (58 vs 110) and S3 (66 vs 130), and about the same on Kafka
  (61 vs 54), with Conduit's Avro work included.

## Saturation, 40,000 ops/s offered (`arch-v2-saturation-40k`)

Same shipped defaults, 5 rounds, no `-aa` twins except on the file arm (the
phase's purpose is the capacity of the Postgres and Kafka destinations; S3 was
already saturated at 20k).

| arm | unit | median /s | min-max /s | ratio vs Connect | lag start -> end | CPU: tool / postgres |
| --- | --- | --- | --- | --- | --- | --- |
| `conduit` (file) | events | 39,096 | 39,091 - 39,113 | 1.002 (A/A 0.04%, max 0.07%) | 33k -> 31k | 117 / 60 |
| `conduit-aa` | events | 39,111 | 39,105 - 39,125 | | | |
| `connect` | events | 39,035 | 38,925 - 39,137 | | 695k -> 693k | 172 / 62 |
| `conduit-kafka` | events | 39,096 | 39,090 - 39,112 | 1.000 | 4k -> -10k | 142 / 66 |
| `connect-kafka` | events | 39,106 | 39,065 - 39,367 | | 17k -> 6k | 96 / 58 |
| **`conduit-pg`** | net rows | **14,798** | 14,713 - 14,808 | **0.769** (0.765-0.778) | 122k -> 655k (behind) | 91 / 125 |
| `connect-pg` | net rows | 19,145 | 19,031 - 19,349 | | 162k -> 414k (behind) | 155 / 132 |

File and Kafka deliver the full 39.1k the loader produces, so they sustain the
offered load and their ceiling is above it. Both Postgres arms fall behind at
40k (24k net rows/s offered); Conduit's capacity is 77% of Connect's.

## Bottleneck: Postgres destination at saturation

Diagnostic run of `conduit-pg` at 40k with `--dev.cpuprofile`,
`--dev.blockprofile` and `pg_stat_activity` sampling every 2 s
(`arch-v2-profile-saturation-40k`, `arch-v2-diag-saturation-40k`).

- **Conduit is not CPU-bound:** 69 s of CPU samples in a 76 s run, about 0.9
  core of 8; docker stats median 91%. The Postgres container is at 125%.
- **The destination spends little CPU but waits:** `Destination.Write` is 5.7%
  of CPU samples. On the Postgres side the destination's upsert backend is
  `active` in 23 of 36 samples and `idle` on `ClientRead` (waiting for Conduit)
  in 13, while the replication walsender sits in `WalSenderWriteData` (waiting
  for Conduit to read) in 27 of 36. The destination backend idles in 36% of
  samples and the walsender waits on Conduit in 75%: a round-trip-bound loop,
  not a compute limit.
- **Exec mode is the lever:** the destination uses pgx `describe_exec`, which
  issues a Parse/Describe round trip per statement. Changing only that to
  `cache_statement` (tuned phase below) lifts capacity from 14.8k to 23.4k net
  rows/s (+58%), past Connect's 19.1k.
- **Source-side cost, not the limiter here but real:** in the same profile
  41% of CPU is the logrepl source goroutine, of which
  `CDCHandler.updateAvroSchema` (called from `handleInsert`) is 21% and the
  SDK encode path 11%, and 12% is `ParseSDKPosition` in the ack path. The file
  arm shows this source sustains 39k ops/s, so it does not cap the Postgres
  arm, but it is the largest CPU item and the Avro default cost every user pays.

**Candidate fixes (not applied in this lane):**

1. Cut the per-statement Describe round trip in the postgres destination
   without trading correctness: cache prepared statements per table shape and
   re-describe on a schema-change error (`conduit-connector-postgres#437` is the
   `cache_statement` work). The tuned number below is evidence of the ceiling,
   not a claim that `cache_statement` is safe as a default.
2. Reduce statements per flush: build one multi-row `INSERT ... ON CONFLICT`
   (or `unnest` array upsert) per batch instead of one statement per record.
3. Source CPU: memoize the Avro schema in `CDCHandler.handleInsert` instead of
   re-extracting it per insert, and avoid re-parsing the position on every ack.

## Tuned phase, labelled (`arch-v2-TUNED-cache_statement-saturation-40k`)

**TUNED**: postgres destination `default_query_exec_mode=cache_statement`,
everything else at shipped defaults, 40,000 ops/s offered, 5 rounds, interleaved.

| arm | median net rows/s | min-max | ratio vs Connect | lag start -> end |
| --- | --- | --- | --- | --- |
| `conduit-pg` (**TUNED**) | 23,401 | 23,398 - 23,413 | 1.227 (1.207-1.267) | -18k -> -7k (sustained) |
| `connect-pg` | 19,084 | 18,473 - 19,388 | | 168k -> 414k |

Conduit now sustains essentially the full 24k net rows/s offered. This phase has
no A/A twin and its Connect spread is 4.8%, but the effect (+23%, every round
above 1.2) is far outside it. It is not a default and must not be quoted as one.

## Caveats

- **One instance, one region, one day.** CPU stayed well below saturation on the
  8 vCPUs.
- **Parity at 20k and 40k means "sustains the offered load".** The loader
  caps near 39.1k ops/s, so file and Kafka headroom above that is unmeasured.
- **S3 is saturated at 20k for both** and has the noisiest A/A (max 3.4%).
- **Lag is approximate** (`pg_stat_user_tables` flushes about once a second,
  about 20-40k events here): read it for direction only.
- **The saturation phase has an A/A twin only on the file arm;** its Postgres
  verdict rests on a 23% effect against sub-2% spreads.
- **Avro asymmetry:** Conduit encodes Avro at defaults, Debezium sends
  schemaless JSON. It favours Connect.
- **Profiles are single diagnostic runs** after the measured runs.

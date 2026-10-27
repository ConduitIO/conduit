# v0.22 start: Conduit vs Kafka Connect (Debezium), shipped defaults, classic engine

**For DeVaris's review. Not a published claim.** One dedicated cloud VM, one night, one build.

- Conduit: `5975af5f` (branch `fix/revert-3131-main-build`: `main` at the v0.21.1 tag plus the #3131
  revert and the default-DLQ fix), classic engine, shipped defaults (no `sdk.batch.*`, shipped
  `withAvroSchema`, shipped Postgres exec mode). Connectors: file v0.11.0, kafka v0.16.0, postgres
  v0.18.0, s3 v0.12.0, SDK v0.17.2.
- Kafka Connect: `quay.io/debezium/connect:3.2` with FileStreamSink, Confluent JDBC sink and S3 sink
  (`../../tools/connect.Dockerfile`).
- One on-demand c6i.2xlarge (8 vCPU, 16 GiB), us-west-1, Ubuntu 24.04, Docker 29.9.2, benchi v0.4.1.
  `environment.txt` has image digests and the full setup.
- Workload: one table, 20,000 ops/s offered (70/20/10 INSERT/UPDATE/DELETE), snapshot disabled. One
  warmup round discarded, then 5 rounds, each arm in the same order per round, fresh infrastructure
  per run, about 60 s measured window per run. Metric is ground truth at the sink (see `../../README.md`).

Two kit changes were needed for this Conduit, both operator settings rather than tuning: the file arms
allow `/sink` (`CONDUIT_CONNECTORS_FILE_ALLOWED_DIRS`, since the 2026-10 settings hardening) and every
Conduit arm binds its API to the container loopback (the image refuses to start on `0.0.0.0` without
`CONDUIT_API_TOKEN`, GHSA-v79j-j88p-m4ff). Nothing in the kit calls the API.

## Results

Median events/s per arm (rows/s for the Postgres pair), with the spread over 5 rounds. `summary.md` has
every arm, the lag columns and the per-round ratios.

| destination | Conduit | Kafka Connect | Conduit / Connect | A/A floor (max) | read |
| --- | --- | --- | --- | --- | --- |
| file | 19,779 (0.1%) | 19,792 (0.7%) | 0.999 | 0.07% | both carry the full offered 20k/s; a tie at this load, not a capacity number |
| S3 (MinIO) | 17,256 (0.4%) | 17,595 (1.0%) | 0.984 | 0.54% | both saturated (lag grows on both); Conduit about 2% slower, every round below 1 |
| Postgres | 3,390 rows/s (0.8%) | 11,856 rows/s (0.6%) | 0.287 | 0.59% | Conduit falls behind (lag grows to about 640k); Connect keeps up |
| Kafka | 5,318 (1.4%) | 19,772 (0.3%) | 0.269 | 0.76% | Conduit falls behind (lag grows to about 1.2M); Connect keeps up |

## What this says

- **File and S3 are at parity at shipped defaults.** File is limited by the offered load on both sides.
  S3 saturates both at about 17k/s, with Conduit 1.6% behind, outside the A/A floor in every round.
- **Postgres and Kafka destinations are 3.5x to 3.7x behind Kafka Connect at shipped defaults, unchanged
  from v0.21** (`../../results.md`: 0.30x for Postgres). The cause recorded there still holds: the classic
  engine writes one record per destination round trip unless `sdk.batch.size` is set, while the JDBC sink
  batches 3000 and Debezium's producer batches by default.
- **Not measured here:** arch-v2. Its funnel now batches destination writes at 3000 by default (#3203), which
  targets exactly the Postgres and Kafka gap. A `CONDUIT_ENGINE=v2` run of the same kit is the next
  measurement.

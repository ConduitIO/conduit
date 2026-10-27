# v0.22 start: Conduit vs Kafka Connect (Debezium), shipped defaults, arch-v2 engine

**For DeVaris's review. Not a published claim.** Same kit, build, instance type and workload as
`../v022-shipped-defaults-classic/`, on a fresh c6i.2xlarge, with `CONDUIT_ENGINE=v2`
(`--preview.pipeline-arch-v2`, confirmed by `using lifecycle service v2` in every Conduit arm's log).
Everything else is shipped defaults. The two kit changes described there (file allowlist `/sink`, API on
the container loopback) apply here too.

## Results

Median events/s (rows/s for the Postgres pair), spread over 5 rounds; `summary.md` has every arm, lag and
per-round ratio.

| destination | Conduit (arch-v2) | Kafka Connect | Conduit / Connect | A/A floor (max) | classic engine, same night |
| --- | --- | --- | --- | --- | --- |
| file | 19,778 (0.1%) | 19,736 (0.8%) | 1.002 | 0.10% | 0.999 |
| S3 (MinIO) | 17,358 (7.6%) | 17,605 (0.5%) | 0.985 | 7.00% (typical 0.71%) | 0.984 |
| Postgres | 11,835 rows/s (0.1%) | 11,825 rows/s (0.8%) | 1.001 | 0.11% | 0.287 |
| Kafka | 19,772 (0.2%) | 19,785 (0.9%) | 1.000 | 0.11% | 0.269 |

## What this says

- **On arch-v2, Conduit keeps up with Kafka Connect on all four destinations at shipped defaults.** File,
  Postgres and Kafka carry the full offered load on both sides (lag stays bounded), so those three are ties
  at 20,000 ops/s, not capacity numbers. The Postgres and Kafka gaps of the classic engine (0.29x and
  0.27x) are gone: arch-v2's funnel batches destination writes by default (#3203).
- **S3 saturates both at about 17,400/s; Conduit is about 1.5% behind**, inside its own typical A/A
  difference but consistent in 4 of 5 rounds. One disturbed `conduit-s3` round (18,544/s) sets the 7% max
  floor.
- **Not measured:** capacity. The loader offers 20,000 ops/s, which file, Postgres and Kafka absorb on both
  engines' fast paths. A saturation run (`LOADER_RATE=40000`, as `../arch-v2-saturation-40k/` did for
  v0.21) is the one that would separate the two systems there.

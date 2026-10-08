# archv2-gate: cross-engine throughput harness for the arch-v2 graduation gate

This harness produces the throughput evidence the
[arch-v2 graduation gate](../../docs/architecture-decision-records/20261006-archv2-graduation-gate.md)
asks for: v1 (`pkg/lifecycle`) against arch-v2 (`pkg/lifecycle-poc`,
`--preview.pipeline-arch-v2`) on the same image and the same pipeline file, with
an A/A control beside every A/B result.

It exists because every earlier v1-vs-v2 number was retracted
([`benchi/METHODOLOGY.md`](../METHODOLOGY.md), #2748). The method below is that
document's "what actually works" list, made into a program.

**No v1-vs-v2 result has been produced with this harness yet.** The gate is
decided at the v0.21 midpoint, on quiesced hardware. A laptop run is useful for
checking the harness and for seeing how wide the A/A floor is on that machine,
nothing more.

## Method

- **Ground truth at the sink.** Every destination is `builtin:file` writing to a
  Docker volume; it writes one JSON record and a newline per record. The harness
  reads each sink's byte size at the start and end of the window, removes the
  container, then counts the newlines in that byte range. No engine metric is
  read. (benchi's own collector reads Conduit's metrics, which is defect 1 in
  METHODOLOGY.md, so this harness drives Docker directly instead of going
  through benchi.)
- **60s window, warmup discarded.** Each run waits for every sink to hold data,
  discards 20s, then measures 60s. At 30s the A/A floor was about ±13%; at 60s
  it was about ±3% on the machine #2748 used.
- **Single runs, alternated.** One container per run, fresh sink volume each
  time. An A/A session runs `a, b` then `b, a` in alternating rounds; an A/B
  session runs `v1, v2, v1` every round. Drift over a session lands on both
  arms instead of one.
- **A/A control in every session.** In an A/B session the two v1 runs of each
  round are the A/A control. The summary prints the A/A floor next to the A/B
  delta.
- **Medians and spread, never a best run.** The summary reports per-arm median,
  sd, min and max, every per-round delta, and the floor.

## Shapes

| shape | sources x sinks | batching | in the gate |
| --- | --- | --- | --- |
| `1x1` | 1 x 1 | default (`sdk.batch.size` unset, batches of one) | yes |
| `2x2` | 2 x 2 | default | yes |
| `2x2-batched` | 2 x 2 | `sdk.batch.size=100`, `sdk.batch.delay=10ms` | no, context only |

Sources are `builtin:generator` (structured, `id` int and `name` string, no rate
limit). In 2x2 every record reaches both sinks; the reported rate is records per
second per sink, the mean over the two sinks.

## Running it

Requirements: Docker (Docker Desktop is fine for checking the harness), Go, and
free space in Docker's disk for one run's sink files. Records are about 390
bytes each, so at the roughly 40,000 to 70,000 records/s seen on a laptop that is
2 to 3 GB per run. Faster hardware needs proportionally more. The files are
deleted after every run.

```bash
# 1. Build the image once, from the commit you want to measure. The label is
#    how env.json records which source the numbers came from.
docker build --label org.opencontainers.image.revision=$(git rev-parse HEAD) \
  -t conduit-bench:archv2-gate .

# 2. A/A control for each engine first. This tells you how wide the floor is
#    on this machine before any A/B number exists.
go run ./benchi/archv2-gate -shape 1x1 -session aa-v1
go run ./benchi/archv2-gate -shape 1x1 -session aa-v2

# 3. The A/B session, which carries its own A/A control.
go run ./benchi/archv2-gate -shape 1x1 -session ab
go run ./benchi/archv2-gate -shape 2x2 -session ab
```

Flags: `-rounds` (default 5), `-warmup` (20s), `-window` (60s), `-cpus` (an
optional `docker --cpus` limit), `-image`, `-out`. One run takes about 90
seconds, so a 5-round A/A session is about 15 minutes and a 5-round A/B session
about 23.

Each session writes to `benchi/archv2-gate/results/<UTC time>-<shape>-<session>/`:

- `runs.csv`: one row per run, with per-sink record counts and bytes, the window
  length, the rate, and the host and Docker VM load averages around the window.
  Written after every run, so an interrupted session keeps its completed runs.
- `env.json`: the machine the session ran on (see Environment).
- `summary.md`: the tables described below.

## Reading the summary

- **A/A floor** is the largest per-round |delta| between the two replicates of
  the same engine. It is deliberately the conservative reading: with five
  rounds a median of the deltas can land near zero by luck.
- **A/B delta** is reported two ways: per round, v2 against the mean of the two
  v1 runs bracketing it; and median-to-median across the session.
- An A/B delta inside the A/A floor is a difference this session cannot
  resolve. Under the gate, that is not a pass.
- Rates from different sessions are not comparable with each other: they ran
  at different times on a machine in a different state. Only compare arms
  within one session.

## Environment

What a gate run needs, from the ADR: quiesced hardware with nothing else on the
machine. In practice:

- a dedicated host (bare metal or a dedicated cloud instance, not a shared
  laptop), no other workloads, no concurrent builds or test runs;
- Docker Engine on Linux rather than Docker Desktop, so there is no VM between
  the container and the CPU;
- CPU frequency scaling pinned (performance governor) where the platform allows
  it;
- the same image for every arm, built once, by revision.

`env.json` records what the session actually had: host OS, CPU model and count,
memory, load average at start, Docker version, the Docker VM's CPUs and memory,
the image ID and its source revision. `runs.csv` records host and VM load around
every window, so a disturbed run is visible rather than silently averaged in.
Commit the whole results directory with any result that is quoted.

## Results in this directory

`results/` holds the sessions run so far. See each `summary.md`, and
`results/README.md` for what they are and are not evidence of.

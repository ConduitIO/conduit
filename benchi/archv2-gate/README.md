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
  time. A `matrix` session runs every arm twice per round in a palindrome
  (`a b c c b a`) with the leading arm rotated each round, so drift over a
  session lands on every arm's first and last positions. (The older `aa-*` and
  `ab` sessions are kept; see "Sessions" below for why `ab` is not enough.)
- **A/A control for every arm.** The two runs of an arm in a round are its A/A
  pair, so every arm has its own noise floor, measured in the same session as
  the comparison. The summary prints each arm's floor and reads every
  comparison against the larger of the two floors involved.
- **The engine is checked, not assumed.** Conduit runs at `info` log level and
  the harness checks its log for `using lifecycle service v2` after every
  window: an arch-v2 arm must have it, a v1 arm must not. A flag that is
  silently ignored would otherwise turn an A/B comparison into an A/A one that
  looks fine. A run that fails the check is invalid and aborts the session.
- **An arm that cannot run is a result, not a gap.** Before a matrix session
  every arm is started once (the probe). An arm whose sinks stay empty, such
  as arch-v2 with a destination that batches (refused at start since #2987,
  `pipeline.archv2_destination_batching_unsupported`), is skipped and listed
  in `summary.md` and `env.json` with the error lines Conduit logged.
- **Medians and spread, never a best run.** The summary reports per-arm median,
  sd, min and max, every per-round delta, and the floor.

## Sessions

| session | runs per round | what it is for |
| --- | --- | --- |
| `matrix` | every `-arm`, twice | the gate session: any number of engines, binaries and shapes interleaved, each with its own A/A floor |
| `aa-v1`, `aa-v2` | two replicates of one engine | the noise floor of one engine on this machine |
| `ab` | `v1, v2, v1` | superseded by `matrix`. It gives v1 an A/A pair and v2 none, so v2's own noise is never measured, and METHODOLOGY.md recorded v2 with twice v1's spread. It prints a warning |

A matrix session takes `-arm label=engine[:/abs/path/to/linux/conduit][@shape]`
once per arm and `-env CONDUIT_BENCH_<NAME>=<value>` for bench knobs. Without a
binary an arm runs the binary in `-image`; with one, the binary is mounted into
`-base-image` (default `alpine:3`, which also counts the sinks). `@shape`
gives the arm its own shape, so one session can compare a configuration (source
batching on or off) as well as engines.

```bash
# the gate: v1 against arch-v2, same image, each arm with its own A/A floor
go run ./benchi/archv2-gate -session matrix -shape 1x1 \
  -image conduit-bench:archv2-gate -arm v1=v1 -arm v2=v2
```

`matrix/` holds the engine matrix (v1, arch-v2 and the staged-batch prototype
across shapes, ack delivery and destination batching): see
[matrix/README.md](matrix/README.md).

## Shapes

| shape | sources x sinks | batching | in the gate |
| --- | --- | --- | --- |
| `1x1` | 1 x 1 | default (`sdk.batch.size` unset, batches of one) | yes |
| `2x2` | 2 x 2 | default | yes |
| `4x4` | 4 x 4 | default | no, context only |
| `2x2-batched` | 2 x 2 | source `sdk.batch.size=100`, `sdk.batch.delay=10ms` | no, context only |
| `1x1-srcbatch100` | 1 x 1 | source `sdk.batch.size=100`, `sdk.batch.delay=10ms` | no, context only |
| `1x1-destbatch`, `2x2-destbatch`, `4x4-destbatch` | n x n | destination `sdk.batch.size=100`, `sdk.batch.delay=10ms` | no, context only |

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

# 3. The A/B session. Every arm runs twice per round, so each carries its own
#    A/A floor.
go run ./benchi/archv2-gate -shape 1x1 -session matrix -arm v1=v1 -arm v2=v2
go run ./benchi/archv2-gate -shape 2x2 -session matrix -arm v1=v1 -arm v2=v2
```

Flags: `-rounds` (default 5), `-warmup` (20s), `-window` (60s), `-cpus` (an
optional `docker --cpus` limit), `-image`, `-base-image`, `-probe-timeout`,
`-out`. One run takes about 90 seconds at the default window and a matrix
session runs each arm twice per round, so a 5-round session costs about 15
minutes per arm.

Each session writes to `benchi/archv2-gate/results/<UTC time>-<shape>-<session>/`:

- `runs.csv`: one row per run, with per-sink record counts and bytes, the window
  length, the rate, and the host and Docker VM load averages around the window.
  Written after every run, so an interrupted session keeps its completed runs.
- `env.json`: the machine the session ran on (see Environment).
- `summary.md`: the tables described below.

## Running on a dedicated EC2 instance

`aws/` is a separate Go module that runs every session on one on-demand
instance and leaves the results in a private S3 bucket
(`cd benchi/archv2-gate/aws && go run . launch|status|cleanup`). The package doc in
`aws/main.go` lists what it creates. The instance halts and terminates itself
at a hard cap, has no ingress, and gets no credentials in user data. A cloud VM
is virtualized and shares hardware, so its results are an early read, not gate
evidence, unless the gate's environment rules are met.

## Reading the summary

- **A/A floor** is the largest per-round |delta| between the two replicates of
  the same arm. It is deliberately the conservative reading: with five
  rounds a median of the deltas can land near zero by luck. One disturbed run
  sets it, and that is the point: it shows how much a busy machine can move a
  number.
- **A comparison** (matrix) is the median-to-median delta over all runs of the
  two arms, plus the per-round deltas of the replicate means. It is _resolved_
  only if the median delta is larger than the larger of the two arms' floors
  and every per-round delta has the same sign. Otherwise the session cannot
  tell the arms apart, and under the gate that is not a pass.
- A comparison that is not resolved is called _suggestive_ if every per-round
  delta has the median delta's sign and the median delta exceeds twice the
  larger arm's **typical** A/A difference (the median per-round |difference|,
  next to the max). One disturbed run sets the max floor and can hide a
  consistent effect on a busy machine; "suggestive" marks that case. It is a
  reason to rerun on quiet hardware, never a result.
- `summary.json` carries the same numbers for aggregation.
- `archv2-gate -rerender <session dir>` rebuilds `summary.md` and
  `summary.json` from a matrix session's `runs.csv` and `env.json` with the
  current reading rules. The runs are not touched.
- `archv2-gate -rerender <matrix dir>` does that for every session under a
  directory laid out by `matrix/run.sh`, and writes `matrix.md` and
  `matrix.json` there: every session's arms and comparisons in one table, in
  run order. It is safe to run while the matrix is still going:
  - a session counts as complete only if `env.json` records a finish and
    `runs.csv` holds every planned run, and only complete sessions get a
    `summary.md` (which `run.sh` reads as "done");
  - an incomplete session is listed, marked incomplete, with what it has;
  - a session that cannot be read is listed as failed, with the reason;
  - a short or unterminated last row of `runs.csv` is a write in progress and
    is dropped and counted. A malformed row anywhere else is an error.
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

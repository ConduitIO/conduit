# The engine matrix

v1, arch-v2 and the staged-batch prototype (#2978), across pipeline shape, ack
delivery and destination batching, measured with the archv2-gate harness
(`../README.md`): records counted at the sink, never from engine metrics, every
arm run twice per round so each arm carries its own A/A floor.

This directory is the input to the single-engine design (#2971). It is evidence
for a decision, not the decision. Laptop sessions are indicative; the gate is
decided on quiesced hardware (see "What this does not show").

## What varies

| axis | values | how |
| --- | --- | --- |
| engine | `v1`, `v2` (arch-v2), `proto` (staged-batch prototype) | `--preview.pipeline-arch-v2` for the last two; `proto` is a different binary |
| shape | 1x1, 2x2, 4x4 | `../shapes/<n>x<n>`; generator sources, `builtin:file` sinks, every record reaches every sink |
| ack delivery | unbuffered, buffered | buffered sets `CONDUIT_BENCH_DEST_ACK_LATENCY=1us` |
| destination batching | off, on | on is `../shapes/<n>x<n>-destbatch`: destinations at `sdk.batch.size=100`, `delay=10ms` |
| source batching ("Alternative A") | off, on | session `alt-a-1x1`: an extra arm on `../shapes/1x1-srcbatch100` |

**Buffered acks** are a bench-only hook, `patches/bench-ack-latency.patch`
(taken from #2978's `latency-injection.patch`). With the latency variable set,
the engine-side `connector.Destination` receives acks on a pump goroutine and
hands them to the engine through a 64k-slot channel, which is the shape of a
destination behind gRPC. The latency itself is 1 microsecond, below the hook's
100 microsecond sleep floor, so no ack is delayed: what changes is _how_ the
engine receives acks (one more goroutine and a channel hop), not how late. The
hook is compiled into every binary and does nothing unless the variable is set.
It models a gRPC destination; it is not one. A real gRPC connector run would be
the check.

## Reproducing it

```bash
# 1. Binaries. base-ref is the prototype's merge base, so the engines differ
#    only by the prototype's change; proto-ref is the prototype's head.
benchi/archv2-gate/matrix/build.sh /tmp/engine-bins c60003bb pr2978 origin/main

# 2. Interleaved sessions, one directory each (about 5 hours on a laptop).
go build -o /tmp/archv2-gate ./benchi/archv2-gate
GATE=/tmp/archv2-gate benchi/archv2-gate/matrix/run.sh /tmp/engine-bins out/
```

`run.sh` defaults to 5 rounds, 8 s warmup and a 25 s window per run, which is
shorter than the 60 s window the gate requires (`ROUNDS`, `WARMUP` and `WINDOW`
override it). Binary hashes, harness flags and the machine are in each
session's `env.json`; `BUILDS.txt` lists the build inputs.

Arch-v2 on `main` refuses a destination with `sdk.batch.size` above 1
(#2987), so in the `destbatch` sessions the arm built from `main` would be
skipped; the matrix builds `v1` and `v2` from the prototype's merge base, where
that refusal does not exist yet, so the arm runs and shows the slowdown the
refusal was added for.

## Reading the results

`results/<date>-<machine>-matrix/<session>/summary.md` has, per session, each
arm's median, sd, min, max and A/A floor, and every pairwise comparison with
its per-round deltas. A comparison is _resolved_ only if the median delta
exceeds the larger floor of the two arms and all per-round deltas have the same
sign. A cell that is not resolved is a tie as far as this session can tell.
`archv2-gate -rerender results/<...>` re-reads every session with the current
rules and writes `matrix.md` (and `matrix.json`), which collects the sessions
into one table; `results/<...>/README.md` is the written read of it.

## What this does not show

- Any number from a laptop is indicative. The laptop runs other work (build,
  tests, other sessions) and the Docker VM adds scheduling noise; the A/A floors
  say how much.
- The prototype is built from its own base commit and compared with `v1` and
  `v2` built from the same base. Current `main` has moved on (the v1 lifecycle
  service changed; `worker.go` conflicts with the prototype). `drift.sh`
  runs both commits in one session; it is the only measure of what that gap does.
- One build of each binary, one machine, one day, fresh containers.

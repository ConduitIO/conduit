# Staged batch engine prototype: measurement kit

Prototype only. Not for merge as-is (see the PR). This directory holds what is needed to reproduce the profile and
the local numbers quoted in the PR, and to rerun them on AWS. Nothing here is a Conduit feature.

**Every number produced here locally is indicative, not gate evidence.** It ran in a Docker Desktop VM on a shared
laptop (Apple M3 Max, load average 5 to 15 from other work). The graduation gate needs quiesced hardware; see
`docs/architecture-decision-records/20261006-archv2-graduation-gate.md`.

## Method

Same as the archv2-gate harness (`benchi/archv2-gate`, PR #2956), which these scripts follow:

- one fresh container per run; the sink is a docker volume; records are counted at the sink (newlines inside the byte
  range written between the window start and end), never from engine metrics;
- warmup discarded, then a fixed window; medians, standard deviation, min and max, every per-round delta;
- an A/A pair for every arm in every round (the two runs of one arm), whose largest per-round difference is that
  arm's floor.

What differs, because the harness takes one image and two engines and this compares three arms:

- `scripts/staged_session.py` runs `v1` (main binary), `v2-main` (main binary, `--preview.pipeline-arch-v2`) and
  `v2-proto` (prototype binary, same flag), each twice per round in a palindrome (a b c c b a) with the leading arm
  rotated per round, so drift lands on both runs of every arm;
- `scripts/run_docker.sh` runs the Conduit process in a plain `alpine` container with the binary mounted;
- window and warmup are shorter than the harness's 60 s and 20 s (25 s and 8 s for the default shapes, less for the
  diagnostic ones), which widens the floors. Each session's `summary.md` states what it used.

## Layout

- `shapes/`: pipeline files in the harness's format. `1x1`, `2x2` are the harness's; `4x4` is new;
  `1x1-srcbatch100` (source `sdk.batch.size=100`, `delay=10ms`), `1x1-destbatch` (destination batching),
  `1x1-noschema` and `4x4-noschema` (SDK schema extraction off on every connector) are diagnostics.
- `patches/latency-injection.patch`: bench-only hook applied to every arm's source tree. Injects destination
  latency via environment variables. **Where**: `CONDUIT_BENCH_DEST_ACK_LATENCY=L` holds each ack until its write is
  `L` old, on the ack path of the engine-side `connector.Destination`, through a pump goroutine so the plugin's serial
  write loop is never blocked (models a destination whose commit takes `L` while its Write call is fast; writes can be
  in flight together). `CONDUIT_BENCH_DEST_WRITE_LATENCY=L` makes each `Write` call take `L` (the SDK destination loop
  is serial, so this caps any engine at `1/L`).
- `patches/aws-userdata-staged.patch`: changes `benchi/archv2-gate/aws/userdata.sh.tmpl` (from PR #2956) so the
  EC2 run builds the hook into both trees and the latency images, copies `shapes/` into the harness checkout, and runs
  the sessions listed below. Not applied anywhere; it is the "extend the launcher sessions" step.
- `patches/stream-buffer.patch`: bench-only diagnostic. Gives the builtin in-memory stream channels a capacity from
  `CONDUIT_BENCH_STREAM_BUFFER` (default 0, the production value). Not a proposal; it isolates how much of the 1x1
  difference between engines is the synchronous handoff.
- `patches/credits-env.patch`: bench-only environment override for the read-ahead window and the unacked cap.
- `patches/profile-instrumentation.patch`: the profiling-only instrumentation used for `profile-report.md` (stage
  timers, trace/mutex/block hooks, canned-source and skip-destination switches). Never part of any PR.
- `scripts/`: `run_docker.sh`, `staged_session.py`, `aggregate.py`, `mkshape.py`, `traceanalyze/` (goroutine state
  per role from a Go execution trace).
- `profile-report.md`, `profile-data/`: the Phase 1 profile of arch-v2 against v1 and its raw tables.
- `results/`: local session outputs (`runs.csv`, `summary.md`, `env.json` per session).

## Running a local session

```bash
# binaries: cross-compile main (v1 and v2-main) and the prototype, each with the bench hook applied
git worktree add /tmp/bench-main  <main sha>   && (cd /tmp/bench-main  && git apply <this dir>/patches/latency-injection.patch)
git worktree add /tmp/bench-proto <proto sha>  && (cd /tmp/bench-proto && git apply <this dir>/patches/latency-injection.patch)
(cd /tmp/bench-main  && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/conduit-main  ./cmd/conduit)
(cd /tmp/bench-proto && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/conduit-proto ./cmd/conduit)

python3 scripts/staged_session.py --shape 1x1 --main /tmp/conduit-main --proto /tmp/conduit-proto \
    --out out/1x1 --rounds 5 --warmup 8 --window 25
python3 scripts/staged_session.py --shape 1x1 --main ... --proto ... --out out/lat1ms \
    --env CONDUIT_BENCH_DEST_ACK_LATENCY=1ms
```

(`run_docker.sh` finds `shapes/` in this directory. Use `GOARCH=amd64` on an Intel host.)

## AWS sessions

**Early, non-gating read on a virtualized instance; not published.** Results of the run, when it exists, are in
`results-aws/`.

`patches/aws-userdata-staged.patch` changes the #2956 launcher's `userdata.sh.tmpl` (apply it to `c6fb3d3e`). The
instance, role, security group and bucket handling are the launcher's, unchanged. What changes is the session part:

- no Docker image builds and no `archv2-gate` harness run. The instance builds two static binaries (main, and the
  prototype) with the two bench-only hooks applied to both trees (`patches/latency-injection.patch`,
  `patches/stream-buffer.patch`; both are off unless an environment variable is set), records their SHA-256, and
  runs `scripts/staged_session.py` (the local method, so AWS and local numbers are comparable in kind) through
  `scripts/aws_sessions.sh`;
- one session carries every arm, so every comparison has a same-session A/A pair: `v1` (main, classic engine),
  `v2-main` (main, `--preview.pipeline-arch-v2`), `v2-proto` (prototype, same flag) and, where marked, `v2-altA`
  (arch-v2 on main with the source `sdk.batch.size=100`, `delay=10ms`: Alternative A), each twice per round in a
  palindrome with the leading arm rotated;
- the launcher's `-harness-pr/-harness-sha` are reused to name the commit that holds this directory's scripts (the
  prototype engine is built from `-fanout-sha`; the two differ by `benchi/` files only);
- sessions run in priority order and a session that cannot finish inside the budget (`BUDGET_MIN`, default 345 of the
  360 minute cap) is skipped, not cut off. A hung arm is bounded: `run_docker.sh` SIGKILLs an engine that ignores
  SIGINT after 30 s and the run is recorded with a zero rate, and `staged_session.py` flags it as HANG in the summary.

The exact command is in the PR description. The shapes and env for each session are in `scripts/aws_sessions.sh`.

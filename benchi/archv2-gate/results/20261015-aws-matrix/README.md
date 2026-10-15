# Engine matrix, 2026-10-15, AWS c7i.4xlarge

**For DeVaris's review. Not a published claim** until he has reviewed it, and not the graduation gate's
quiesced bare-metal run: this is one dedicated cloud VM, one night, one build of each binary.

The 13 sessions of `../../matrix/run.sh`, run by `../../aws` with `-plan matrix`: v1, arch-v2 (`v2`) and
the staged-batch prototype (`proto`, #2978) across shape (1x1, 2x2, 4x4), ack delivery and destination
batching, plus Alternative A with both engines on the source batch at 100. Every arm ran twice per round,
so each has its own A/A floor from the same session. `results/matrix/matrix.md` has every arm and
comparison with per-round deltas (rendered on the instance; re-rendering here gives the same file). The
six destination-batching `summary.md` files were re-rendered after the run with `-rerender` so the
skipped arm's error sits in a code block; their numbers and `runs.csv` are unchanged.

## Setup

- One on-demand c7i.4xlarge in us-west-1a (Intel Xeon Platinum 8488C, 16 vCPU), Amazon Linux 2023,
  Docker 25.0.16, nothing else on the instance. 1-minute load average 0.8 to 8.4 for the whole run
  (`loadavg.log`; `vmstat.log` carries CPU steal). No cpufreq governor is exposed on this VM.
- 5 rounds, **20 s warmup, 60 s window** (the gate's window). 23:26Z to 08:52Z.
- `v1` and `v2` are one binary built from main `44add695` (SHA-256 `7a8d509b...`); `proto` is #2978 at
  `0c92b617` (SHA-256 `6f9bb186...`). Both carry `../../matrix/patches/bench-ack-latency.patch`, inert
  unless the buffered knob is set. Full hashes are in each session's `env.json`. `BUILDS.txt` lacks hashes
  because `build.sh` used `shasum`, which Amazon Linux lacks (fixed in `53d82566`, after this run built).
- Harness `a61d79f8` (#3067). `environment.txt` is the instance spec; `bootstrap.log` and `status.txt`
  the run log.

## Results

Medians in records/s per sink, each with the arm's A/A floor (largest per-round replicate difference). A
comparison is **resolved** only if its median delta exceeds the larger of the two floors and every
per-round delta has the same sign. Compare only within a row: each row is one session.

| session | v1 | v2 | proto | proto vs v1 | proto vs v2 | v2 vs v1 |
| --- | --- | --- | --- | --- | --- | --- |
| 1x1 | 44329 ±2.3% | 64292 ±0.8% | 51013 ±0.6% | +15.1% resolved | -20.7% resolved | +45.0% resolved |
| 2x2 | 27440 ±1.2% | 28052 ±0.2% | 50304 ±1.7% | +83.3% resolved | +79.3% resolved | +2.2% resolved |
| 4x4 | 23143 ±2.8% | 36639 ±1.5% | 47897 ±1.8% | +107.0% resolved | +30.7% resolved | +58.3% resolved |
| 1x1 buffered | 64777 ±1.3% | 61803 ±0.3% | 42008 ±0.8% | -35.1% resolved | -32.0% resolved | -4.6% resolved |
| 2x2 buffered | 46456 ±2.6% | 26611 ±0.8% | 60710 ±2.1% | +30.7% resolved | +128.1% resolved | -42.7% resolved |
| 4x4 buffered | 33716 ±19.6% | 35610 ±1.1% | 55321 ±3.0% | +64.1% resolved | +55.4% resolved | +5.6% tie |
| 1x1 destbatch | 57907 ±2.1% | skipped | 49351 ±0.5% | -14.8% resolved | | |
| 2x2 destbatch | 39800 ±1.9% | skipped | 90126 ±1.5% | +126.4% resolved | | |
| 4x4 destbatch | 26649 ±6.0% | skipped | 89880 ±1.5% | +237.3% resolved | | |
| 1x1 destbatch buffered | 58030 ±1.8% | skipped | 49191 ±0.9% | -15.2% resolved | | |
| 2x2 destbatch buffered | 39060 ±5.0% | skipped | 90085 ±1.8% | +130.6% resolved | | |
| 4x4 destbatch buffered | 26858 ±5.4% | skipped | 89295 ±1.7% | +232.5% resolved | | |

Sample standard deviation per arm is 0.2% to 2.1%, except v1 in the 4x4 sessions (1.4% to 5.9%). The one
wide floor, v1 at 4x4 buffered (±19.6%, typical ±8.5%), comes from one disturbed v1 round and leaves only
v2 vs v1 unresolved there.

`v2` was skipped by the pre-session probe in every destination-batching session. Arch-v2 on main refuses
to start a destination with `sdk.batch.size` above 1 (#2987): "can't start pipeline archv2-gate on
pipeline architecture v2: destination conn...". That is the shipped behaviour, not a harness fault.

Alternative A, 1x1 (`alt-a-1x1`; `-alta` arms run `../../shapes/1x1-srcbatch100`):

| v1 | v2 | proto | v2-alta | proto-alta |
| --- | --- | --- | --- | --- |
| 44413 ±1.4% | 64186 ±1.1% | 50692 ±1.4% | 231653 ±2.8% | 83726 ±0.8% |

All ten comparisons are resolved. Source batch 100 lifts v2 by +260.9% and the prototype by +65.2%;
`v2-alta` is +176.7% over `proto-alta` (`proto-alta` is -63.9% against `v2-alta`). This session's
default-config arms reproduce the 1x1 row above to within 1%.

## What this says

- **At 1x1 the prototype is mostly slower, resolved.** It is behind v2 at 1x1 in
  every session that ran v2 (-21%, -21%, -32%). It is behind v1 at 1x1 with buffered acks (-35%) and with
  destination batching (-15% in both ack modes), and ahead of v1 only at 1x1 unbuffered, without
  batching (+14% to +15%).
- **With fan-in and fan-out the prototype is ahead of both, resolved, in every session.** At 2x2 and 4x4
  it beats v1 by +31% to +237% and v2 by +31% to +128%. With destination batching, which arch-v2 on main
  refuses, it holds about 90,000 records/s at 2x2 and 4x4, where v1 falls to 27,000 to 40,000.
- **Alternative A favours arch-v2 by a wide margin at 1x1.** With the source batch at 100, arch-v2 is
  2.8x the prototype on the same setting. Whatever arch-v2's funnel does with large source batches, the
  prototype does not yet get the same gain from them.
- **arch-v2 vs v1 depends on the shape and the ack path.** It leads v1 unbuffered (+45% at 1x1, +58% at
  4x4), but with buffered acks it trails at 1x1 (-4.6%) and 2x2 (-42.7%).
- **This contradicts the laptop read** (`../20261014-laptop-matrix/`), which found the prototype never
  resolved below v1 or v2. The laptop's floors (±18% to ±262%) hid the 1x1 regressions, and its v1 and
  v2 were the older merge base (`../20261014-laptop-drift/` shows main has moved). The laptop's lead for
  the prototype at fan-in and fan-out holds here, on 60 s windows with floors of a few percent.

## What this does not say

- One cloud VM, one night, one build. A dedicated VM is not the gate's quiesced bare-metal host, though
  load and the A/A floors were low throughout.
- No real gRPC connector ran. Buffered acks model how a gRPC destination delivers acks; they are not one.
- Generator sources and file sinks only. Records are counted at the sink.
- The prototype is a draft (#2978). The 1x1 regressions are measurements of that draft, not of the
  design in #2971.

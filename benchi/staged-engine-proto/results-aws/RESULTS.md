# Staged-engine prototype: AWS confirmation run (2026-10-12)

**Early, non-gating read on a virtualized instance; not published.** Not graduation-gate evidence: the instance is
virtualized and shared hardware, windows are 40 s (the harness uses 60 s), and the arms and shapes are not the gate's.
Do not quote these numbers outside the PR.

## What ran

- One on-demand `c7i.4xlarge` (16 vCPU = 8 cores with hyperthreading, Intel Xeon Platinum 8488C), us-west-1a, AL2023
  kernel 6.18, Docker 25.0.16, Go 1.26.9. AMI `ami-0298380f140c1c3ca`. Launched 16:46:21Z, sessions finished 22:03Z,
  terminated by cleanup. Same launcher (#2956, `c6fb3d3e`) with `patches/aws-userdata-staged.patch`; scoped
  put-results-only role, SSM-only, no ingress, no credential in user data.
- Arms (rates are records per second per sink, counted at the sink; per-sink so 2x2 and 4x4 write 2x and 4x as many):
  `v1` main `c60003bb` classic engine; `v2-main` the same binary with `--preview.pipeline-arch-v2`; `v2-altA` the same
  binary and flag with the **source** `sdk.batch.size=100`, `delay=10ms` (Alternative A); `v2-proto` the prototype
  (#2978 engine commit `98f8411e`) with the same flag. All binaries static, built on the instance with the two
  bench-only hooks (ack latency, stream buffer; off unless an env var is set) applied to both trees.
- Binary SHA-256: main (used by `v1`, `v2-main`, `v2-altA`)
  `ac9f1aa8e4094ab5a80015363862bbbfd76bbdb4f19f1e3749994eb60ced3ddd`; prototype (`v2-proto`)
  `1e5d5091e1502afa8b41f06de02cd95a83852fa2ebfacdc90d96a77e86323a9e`.
- Method: 5 rounds, every arm twice per round in a palindrome with the leading arm rotated (so each arm's two runs
  are its same-session A/A pair, and every comparison shares a session), 10 s warmup, 40 s window, fresh container per
  run, sink on a Docker volume. Floors below are the largest per-round |difference| between an arm's two runs. Session
  driver: `scripts/staged_session.py`; session list: `scripts/aws_sessions.sh`.
- Deviation from the request: the harness's own `ab` session has only two engine slots, so it cannot give three or four
  arms with an A/A pair each in one session. I used the prototype's staged-session driver (the method of the local
  results) on the instance instead; the launcher, instance, role and cleanup are unchanged. Windows are 40 s, not 60 s.
- Not run: the `delay=0` destination batch (hangs on arch-v2 main, #2979). No arm hung and no run failed or needed
  the shutdown kill: all 10 sessions completed, 320 runs, none with a zero rate.

## Host health

Steal was 0 in all 3,758 five-second vmstat samples taken inside run windows; iowait mean 0.09% (max 3%). 1-minute
load average stayed between 1.3 and 4.2 on average per session (max 5.9 at 4x4), against 16 vCPU. Nothing else ran on
the instance. This does not make the instance quiesced hardware: the hypervisor can still add noise that steal does
not report.

| session | runs | vmstat samples | steal % mean / p95 / max | iowait % mean / p95 / max | loadavg(1m) mean / max |
| --- | --- | --- | --- | --- | --- |
| 01-1x1-bufferedacks | 40 | 455 | 0.00 / 0 / 0 | 0.05 / 1 / 2 | 2.19 / 4.10 |
| 02-2x2 | 30 | 353 | 0.00 / 0 / 0 | 0.11 / 1 / 2 | 2.63 / 3.32 |
| 03-4x4 | 30 | 401 | 0.00 / 0 / 0 | 0.16 / 1 / 3 | 4.23 / 5.94 |
| 04-1x1 | 40 | 456 | 0.00 / 0 / 0 | 0.07 / 1 / 2 | 2.06 / 3.87 |
| 05-1x1-streambuf64 | 30 | 355 | 0.00 / 0 / 0 | 0.09 / 1 / 2 | 2.75 / 3.63 |
| 06-2x2-streambuf64 | 30 | 386 | 0.00 / 0 / 0 | 0.16 / 1 / 3 | 4.02 / 5.88 |
| 07-1x1-noschema | 30 | 342 | 0.00 / 0 / 0 | 0.07 / 1 / 2 | 1.81 / 2.89 |
| 08-1x1-lat1ms | 30 | 329 | 0.00 / 0 / 0 | 0.04 / 0 / 2 | 1.65 / 2.66 |
| 09-1x1-destbatch100 | 30 | 328 | 0.00 / 0 / 0 | 0.04 / 0 / 1 | 1.30 / 2.27 |
| 10-1x1-srcbatch100 | 30 | 353 | 0.00 / 0 / 0 | 0.10 / 1 / 2 | 2.02 / 3.06 |
| **all sessions** | | 3758 | 0.00 / 0 / 0 | 0.09 / 1 / 3 | |


## Medians and A/A floors, per shape

Median rec/s per sink over the 10 runs of an arm (5 rounds x 2), floor in brackets. `-` = arm not in that session.

| session | shape and env | rounds x window | v1 (A/A max) | v2-main (A/A max) | v2-proto (A/A max) | v2-altA (A/A max) | proto vs v1 | proto vs v2-main | v2-main vs v1 | altA vs v2-main | proto vs altA |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 01-1x1-bufferedacks | 1x1 DEST_ACK_LATENCY=1us | 5 x 40s | 48558 (+/-1.3%) | 32533 (+/-2.1%) | 38608 (+/-0.7%) | 76558 (+/-2.0%) | -20.5% | +18.7% | -33.0% | +135.3% | -49.6% |
| 02-2x2 | 2x2 | 5 x 40s | 25402 (+/-5.0%) | 24094 (+/-4.1%) | 46782 (+/-3.5%) | - | +84.2% | +94.2% | -5.1% | - | - |
| 03-4x4 | 4x4 | 5 x 40s | 22114 (+/-2.8%) | 31638 (+/-1.1%) | 44600 (+/-2.8%) | - | +101.7% | +41.0% | +43.1% | - | - |
| 04-1x1 | 1x1 | 5 x 40s | 46722 (+/-1.0%) | 35157 (+/-0.5%) | 48525 (+/-1.6%) | 80453 (+/-1.6%) | +3.9% | +38.0% | -24.8% | +128.8% | -39.7% |
| 05-1x1-streambuf64 | 1x1 STREAM_BUFFER=64 | 5 x 40s | 73801 (+/-0.5%) | 77770 (+/-1.0%) | 72928 (+/-0.6%) | - | -1.2% | -6.2% | +5.4% | - | - |
| 06-2x2-streambuf64 | 2x2 STREAM_BUFFER=64 | 5 x 40s | 51414 (+/-5.6%) | 21756 (+/-1.2%) | 103692 (+/-2.2%) | - | +101.7% | +376.6% | -57.7% | - | - |
| 07-1x1-noschema | 1x1-noschema | 5 x 40s | 49008 (+/-1.9%) | 79851 (+/-1.2%) | 74666 (+/-1.3%) | - | +52.4% | -6.5% | +62.9% | - | - |
| 08-1x1-lat1ms | 1x1 DEST_ACK_LATENCY=1ms | 5 x 40s | 49084 (+/-1.5%) | 838 (+/-0.5%) | 37416 (+/-1.3%) | - | -23.8% | +4367.6% | -98.3% | - | - |
| 09-1x1-destbatch100 | 1x1-destbatch | 5 x 40s | 37994 (+/-1.6%) | 95 (+/-1.1%) | 47757 (+/-0.7%) | - | +25.7% | +50170.5% | -99.7% | - | - |
| 10-1x1-srcbatch100 | 1x1-srcbatch100 | 5 x 40s | 51428 (+/-2.8%) | 80338 (+/-0.8%) | 79710 (+/-1.8%) | - | +55.0% | -0.8% | +56.2% | - | - |


## Which differences exceed the floor

A difference is called resolved only if it exceeds the **larger** of the two arms' A/A floors in the same session.
"Per-round range" is the spread of the five per-round deltas (each round compares the means of an arm's two runs).

| session | comparison | delta (median to median) | per-round range | A/A floors (x / y) | exceeds both floors |
|---|---|---|---|---|---|
| 01-1x1-bufferedacks | v2-main vs v1 | -33.0% | -33.6% to -32.3% | 2.1% / 1.3% | yes |
| 01-1x1-bufferedacks | v2-proto vs v1 | -20.5% | -21.0% to -20.1% | 0.7% / 1.3% | yes |
| 01-1x1-bufferedacks | v2-proto vs v2-main | +18.7% | +17.9% to +19.2% | 0.7% / 2.1% | yes |
| 01-1x1-bufferedacks | v2-altA vs v1 | +57.7% | +56.2% to +58.5% | 2.0% / 1.3% | yes |
| 01-1x1-bufferedacks | v2-altA vs v2-main | +135.3% | +132.7% to +136.9% | 2.0% / 2.1% | yes |
| 01-1x1-bufferedacks | v2-proto vs v2-altA | -49.6% | -50.2% to -49.3% | 0.7% / 2.0% | yes |
| 02-2x2 | v2-main vs v1 | -5.1% | -5.7% to -5.4% | 4.1% / 5.0% | yes |
| 02-2x2 | v2-proto vs v1 | +84.2% | +81.2% to +85.8% | 3.5% / 5.0% | yes |
| 02-2x2 | v2-proto vs v2-main | +94.2% | +92.1% to +96.9% | 3.5% / 4.1% | yes |
| 03-4x4 | v2-main vs v1 | +43.1% | +41.2% to +44.8% | 1.1% / 2.8% | yes |
| 03-4x4 | v2-proto vs v1 | +101.7% | +98.8% to +104.9% | 2.8% / 2.8% | yes |
| 03-4x4 | v2-proto vs v2-main | +41.0% | +39.0% to +41.5% | 2.8% / 1.1% | yes |
| 04-1x1 | v2-main vs v1 | -24.8% | -25.4% to -23.4% | 0.5% / 1.0% | yes |
| 04-1x1 | v2-proto vs v1 | +3.9% | +2.8% to +5.4% | 1.6% / 1.0% | yes |
| 04-1x1 | v2-proto vs v2-main | +38.0% | +36.7% to +38.1% | 1.6% / 0.5% | yes |
| 04-1x1 | v2-altA vs v1 | +72.2% | +70.3% to +75.2% | 1.6% / 1.0% | yes |
| 04-1x1 | v2-altA vs v2-main | +128.8% | +127.4% to +131.1% | 1.6% / 0.5% | yes |
| 04-1x1 | v2-proto vs v2-altA | -39.7% | -40.3% to -39.6% | 1.6% / 1.6% | yes |
| 05-1x1-streambuf64 | v2-main vs v1 | +5.4% | +5.3% to +5.9% | 1.0% / 0.5% | yes |
| 05-1x1-streambuf64 | v2-proto vs v1 | -1.2% | -1.7% to -1.0% | 0.6% / 0.5% | yes |
| 05-1x1-streambuf64 | v2-proto vs v2-main | -6.2% | -7.2% to -6.0% | 0.6% / 1.0% | yes |
| 06-2x2-streambuf64 | v2-main vs v1 | -57.7% | -58.8% to -57.2% | 1.2% / 5.6% | yes |
| 06-2x2-streambuf64 | v2-proto vs v1 | +101.7% | +95.7% to +106.9% | 2.2% / 5.6% | yes |
| 06-2x2-streambuf64 | v2-proto vs v2-main | +376.6% | +373.6% to +386.3% | 2.2% / 1.2% | yes |
| 07-1x1-noschema | v2-main vs v1 | +62.9% | +61.5% to +64.1% | 1.2% / 1.9% | yes |
| 07-1x1-noschema | v2-proto vs v1 | +52.4% | +51.8% to +53.4% | 1.3% / 1.9% | yes |
| 07-1x1-noschema | v2-proto vs v2-main | -6.5% | -6.5% to -5.6% | 1.3% / 1.2% | yes |
| 08-1x1-lat1ms | v2-main vs v1 | -98.3% | -98.3% to -98.3% | 0.5% / 1.5% | yes |
| 08-1x1-lat1ms | v2-proto vs v1 | -23.8% | -23.8% to -23.3% | 1.3% / 1.5% | yes |
| 08-1x1-lat1ms | v2-proto vs v2-main | +4367.6% | +4341.2% to +4401.1% | 1.3% / 0.5% | yes |
| 09-1x1-destbatch100 | v2-main vs v1 | -99.7% | -99.8% to -99.7% | 1.1% / 1.6% | yes |
| 09-1x1-destbatch100 | v2-proto vs v1 | +25.7% | +25.0% to +26.6% | 0.7% / 1.6% | yes |
| 09-1x1-destbatch100 | v2-proto vs v2-main | +50170.5% | +50146.8% to +50542.3% | 0.7% / 1.1% | yes |
| 10-1x1-srcbatch100 | v2-main vs v1 | +56.2% | +55.4% to +56.3% | 0.8% / 2.8% | yes |
| 10-1x1-srcbatch100 | v2-proto vs v1 | +55.0% | +54.0% to +55.9% | 1.8% / 2.8% | yes |
| 10-1x1-srcbatch100 | v2-proto vs v2-main | -0.8% | -1.0% to +0.0% | 1.8% / 0.8% | NO (per-round sign flips) |

Every delta except one exceeds both floors, and almost all are sign-consistent in all five rounds. The exception is
`10-1x1-srcbatch100`, prototype vs v2-main (-0.8%): not resolved, a tie. `02-2x2` v2-main vs v1 (-5.1%) clears the
floors (4.1% and 5.0%) only marginally: the 5.0% floor is a single round-1 outlier (median A/A difference 1.0%), and
all five per-round deltas are -5.4% to -5.7%, so I read it as a small real loss, but it is the weakest "yes" here.

## Questions asked

**Does the buffered-ack regression reproduce on real hardware?** Yes. With acks delivered through a buffered pump
(`CONDUIT_BENCH_DEST_ACK_LATENCY=1us`, the shape of a gRPC destination), at 1x1 the prototype is **-20.5% against v1**
(38,608 vs 48,558 rec/s; floors 0.7% and 1.3%; per-round -21.0% to -20.1%). Locally it was -17.9%. arch-v2 main is
-33.0% against v1 in the same session (local -25.2%), so the prototype is still +18.7% above arch-v2 main. The
no-pump 1x1 default (session 04) has the prototype at only +3.9% over v1 (local +8.7% to +10.4%), also reproducing
in direction, smaller in size. At this shape the prototype's 1x1 advantage over v1 is gone or negative.

**Alternative A versus the prototype.** Alternative A (arch-v2 main with a source batch of 100) is far ahead of the
prototype at 1x1 with the default destination: **+72.2% over v1 and +128.8% over arch-v2 main, and the prototype is
-39.7% against it** (session 04, 80,453 vs 48,525 rec/s; floors at most 1.6%). With the buffered ack pump the gap is
larger: Alternative A is +57.7% over v1 and the prototype is -49.6% against it. When both are batched
(session 10, source batch 100 for every arm) arch-v2 main and the prototype are tied (-0.8%, inside the floors),
both +55% to +56% over v1: the prototype adds nothing on top of batching at 1x1, as the local result found. In that session batched arch-v2 main is +56.2% over batched v1 (local +30.9%). Alt A changes the source
connector's setting, not the engine, and I ran no Alt A arm at 2x2 or 4x4, so nothing here says whether batching closes the
N>1 gap that the prototype closes.

**Where the prototype does win** (all above the floors): 2x2 +84.2% over v1 and +94.2% over arch-v2 main (local
+42%/+40%); 4x4 +101.7% over v1 and +41.0% over arch-v2 main (local +73%/+14%); 2x2 with a 64-slot stream buffer
+101.7% over v1 and 4.8x arch-v2 main (arch-v2 main collapses to 21.8k there); destination batching with
`delay=10ms`, where arch-v2 main delivers 95 rec/s and v1 and the prototype 38.0k and 47.8k; and
destination ack latency of 1 ms, where arch-v2 main is 838 rec/s against 37,416 for the prototype and 49,084 for
v1 (the prototype is still -23.8% against v1 there).

**Where it does not:** 1x1 with the stream buffered at 64 slots: v1 73.8k, arch-v2 main 77.8k (+5.4%), prototype 72.9k
(-1.2% vs v1, -6.2% vs main): no prototype advantage, and arch-v2 main is the fastest of the three. With SDK schema
extraction off: arch-v2 main 79.9k, prototype 74.7k (-6.5% vs main, outside the floors), both +52% to +63% over v1. So the
prototype is slightly slower than arch-v2 main at 1x1 once the connector cost is out of the way; locally these tied.

**Differences from the local numbers worth knowing.** AWS rates are lower than the laptop's at 1x1 (v1 46.7k vs 65.8k)
and higher in the N>1 relative gains. The 2x2 v1-vs-arch-v2-main gap (-5.1%) matches the AWS read from 2026-10-08
(-6%) rather than the local +1.9%. The 4x4 prototype lead over arch-v2 main (+41%) is larger than local (+14%).

## Cost

About 5.28 instance hours (16:46:21Z launch to termination after 22:03Z), well under the 6 hour limit. I did not query
the price list. At an assumed on-demand rate of roughly 0.75 to 0.85 USD/h for a c7i.4xlarge in us-west-1, that is
about 4.0 to 4.5 USD, plus the 80 GB gp3 root volume (1000 MB/s provisioned) at a few tens of cents for the period and
negligible S3. Treat it as an estimate of about 5 USD all in, not a billed figure.

## Cleanup

Manifest: `aws2/manifest-2.json` (scratchpad). Created: bucket
`conduit-archv2-gate-early-132836343092-20261012t164605z` (kept, as instructed), IAM role and instance profile
`conduit-archv2-gate-early-20261012t164605z`, security group `sg-0442f9631071d50b6`, instance `i-0e92d5d754e3f1768`,
volume `vol-043e9206c3e8f277e`. Deleted and then verified by separate describe/get calls: instance `terminated`; volume,
security group, IAM role and instance profile all return NotFound; no other running or stopped instance and no other
volume exists in us-west-1.

## Limits of this read

Single instance, single AZ, single day; 40 s windows; one build of each binary; 5 rounds. The floors are from five A/A
pairs per arm, so a floor is itself a small-sample estimate. Records are counted per sink at the file sink, a
connector path dominated by SDK schema extraction (the noschema session shows it). The latency and stream-buffer
shapes use bench-only hooks that change the plugin transport, not production code paths. Nothing here has been
reviewed independently. Rates from different sessions are not comparable with each other.

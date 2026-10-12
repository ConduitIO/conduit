# Profile of arch-v2 (stop-and-wait) against v1: does the hypothesis hold?

Status: Phase 1 of the staged-engine prototype. Local, single machine, indicative. Not gate evidence.
Profiles and raw outputs are saved next to this file under `profile/`.

## Verdict

**Confirmed, with two corrections to the brief.**

1. arch-v2's `Worker.Do` is stop-and-wait, and its throughput is the _sum_ of the stage costs. Measured stage ceilings
   (engine alone, one stage at a time) are 12.7 us/record for the source side and 7.3 us/record for the destination
   side; their sum (20.0 us) predicts 50k rec/s, and arch-v2 at 1x1 runs at 21.3 us/record (46.7k rec/s). A pipelined
   engine is bounded by the _larger_ stage, 12.7 us, i.e. up to ~78k rec/s at 1x1 on this machine: +68% over arch-v2
   at best.
2. **The source is not mostly idle during write and ack.** In the trace the source plugin's read goroutine is
   running or runnable 72% of the time and blocked in `Send` only 27%. The stop-and-wait loop does not overlap
   source production with the destination stage (source-only 12.7 us + destination-only 7.3 us = 20.0 us, so overlap is
   about zero); the likely mechanism is Go scheduling of the unbuffered handoff (see "What the trace shows"), not an
   idle source.
3. **The gap to v1 is not a constant.** It depends on how expensive the connector side is per record. With the SDK's
   default schema extraction (avro serde built per record, ~27% of CPU in the connector SDK, see below) arch-v2 is
   below v1 at 1x1. With schema extraction off on both connectors, **arch-v2 main is above v1 at 1x1** (94k vs 70k) and
   the prototype is above both (108k). The 1x1 gap the AWS run measured is the stage-sum effect on a heavy connector
   path, not a fixed engine tax.

## Environment

- Machine: Apple M3 Max, 16 cores, macOS 26 (Darwin 25.5.0), Docker Desktop 29.6.2 (VM: 16 CPUs, 7.6 GiB).
- Not quiet. The host load average was 5 to 15 (browser, other agent sessions, builds) during the whole session and
  spiked above 100 during part of the SDK-batching runs. The Linux profiling runs ran inside the Docker VM, whose own
  load average stayed at 0.3 to 2.8 (`vmload` in the files), which is why they are the ones quoted. macOS-native
  runs were also done (`profile/1x1-v1`, `profile/1x1-v2`); macOS CPU profiles are unreliable (80% of samples in
  `pthread_cond_wait`/`syscall`), so they are only used for the stage timers.
- Code under test: `ce758f96` (main at the time of the AWS runs) plus profiling-only instrumentation
  (`profile-instrumentation.patch`, never part of any PR). Instrumentation cost: could not be separated from
  machine noise (macOS native arch-v2 1x1, two clean runs 34.6k and 49.6k rec/s, two instrumented runs 48.3k and 47.7k);
  each timer is two `time.Now` calls, ~100 ns against a 21 us pass.
- Harness: shapes from PR #2956 (`benchi/archv2-gate/shapes`), default config, `builtin:generator` to `builtin:file`.
  The harness itself needs one image and two engines, so profiling used `run_docker.sh`, which runs the same shape in
  a plain Linux container with the binary mounted and counts newlines at the sink like the harness.
- Rates quoted in this report come from 15 to 20 s windows with profiling on; they are for relating stage time to
  throughput, not for comparing engines. Engine comparisons are in the Phase 3 sessions.

## 1. Where one arch-v2 pass spends its time (1x1, Linux VM)

Stage timers inside `Worker.doTaskAttempt`, `SourceTask.Do`, `DestinationTask.Do`, `Worker.Ack`
(`profile/docker/1x1-v2/stages.txt`; 1.27M passes; mean per pass):

| Stage | us | share of pass |
| --- | --- | --- |
| `Source.Read` (waiting in `stream.Recv` for the plugin) | 11.86 | 55.7% |
| `ConnectorMetrics.Observe` on read | 0.26 | 1.2% |
| processors | 0 (none configured) | |
| `Destination.Write` (send into the in-memory stream) | 0.69 | 3.2% |
| wait for acks (`Destination.Ack` -> `stream.Recv`) | 5.64 | 26.5% |
| `ConnectorMetrics.Observe` on ack | 0.26 | 1.2% |
| `Source.Ack` (persist enqueue) | 0.52 | 2.4% |
| `updateTimer` | 0.23 | 1.1% |
| `DLQ.Ack` | 0.04 | 0.2% |
| `processingLock` acquire | 0.15 | 0.7% |
| `sharedMu` wait (1x1: no contention) | 0.03 | 0.1% |
| **whole pass** | **21.30** | 100% |

The engine's own work (everything except the two plugin waits) is about 2.2 us, 10% of the pass. 90% of the pass is
the engine waiting for the plugins, one after the other.

### The sum-versus-max test

To check the "throughput = 1 / sum of stages" claim directly, each side was run alone with the engine otherwise
unchanged (`CONDUIT_PROF_SKIP_DEST=1`: the destination task returns at once; `CONDUIT_PROF_CANNED_SOURCE=1`: the source
task returns a prebuilt record without calling the plugin). Three runs each, 15 s windows:

| Variant | rec/s (3 runs) | us/record |
| --- | --- | --- |
| source side only (read + source ack) | 78,557 / 78,456 / 78,634 | 12.7 |
| destination side only (write + ack wait) | 137,681 / 136,115 / 136,449 | 7.3 |
| sum of the two | | 20.0 |
| full arch-v2 pass (same build, profiled) | 46,682 | 21.3 |
| pipelined bound (1 / max) | 78.5k | 12.7 |

The measured pass is 6% above the sum. That is what "one batch in flight, throughput = 1 / (sum of stages)" predicts.
Files: `profile/ceil/*`.

## 2. Does the source sit idle while the destination writes and acks?

Not for long. Goroutine states from a 2 s execution trace taken after warmup (`go tool trace` data, analysed with
`traceanalyze`; `profile/docker/1x1-v2/trace-roles.txt`):

| goroutine | running | runnable (waiting for a P) | blocked / waiting | syscall |
| --- | --- | --- | --- | --- |
| engine `Worker.Do` loop | 23.3% | **26.1%** | 50.5% (select) | |
| source plugin `runRead` | 51.4% | 21.2% | **27.4%** (blocked in `Send`) | |
| destination plugin `Run` | 20.1% | 2.6% | 73.1% (waiting for next write) | 4.2% |

- The source plugin is busy (running or runnable) 72% of the time. It produces a record in about 12 us of CPU
  (generator record + SDK middleware: schema extraction, encoding) and is blocked in `Send` for about 27% of the cycle.
- The engine loop is _runnable but not running_ 26% of the time, about 5.5 us per pass.
- Source production does not overlap the destination stage: the engine waits 11.9 us in `Read` although it did
  8.7 us of other work after the previous `Read`. If production overlapped, `Read` would wait about 3 us.

A plausible mechanism (consistent with the data, not proven by it): when the plugin goroutine completes the
unbuffered handoff, the receiving engine goroutine is put in the sender's `runnext` slot and does not run until the
sender blocks again or another P steals it; the sender goes straight on to produce the next record (12 us), so the
engine runs the destination stage after production, not beside it. This is why the profile shows `runtime.futex`,
`usleep` and `pthread_cond_signal` weight (thread wake-ups) rather than engine code.

What a pipelined engine changes is that each stage has queued work, so stages run on different Ps at the same time.
That is the prototype (Phase 2); its numbers are in the Phase 3 summaries.

For comparison, v1 at 1x1 (`profile/docker/1x1-v1/trace-roles.txt`): every v1 node is mostly parked (SourceNode 87%,
SourceAckerNode 90%, DestinationAckerNode 42% chan receive), the source plugin runs 52.7% and is blocked in `Send`
44%. v1 is also not saturating anything: it is a chain of handoffs, and on this machine it runs at 52k to 65k rec/s,
below the 78k source-side ceiling.

## 3. The ceiling question: is ~50k destination writes/s real?

The design document noted that total destination writes were about 50k/s in 3 of the 4 AWS cells. **It is real, and it
sits in the connector SDK, not in the engine or the harness.**

Evidence:

- CPU profile of the prototype at 1x1 (`profile/proto-1x1/cpu.pprof`, where the engine is not the bottleneck): the source
  plugin's `runRead` is 38.6% of all samples, of which `sourceWithSchemaExtraction.extractAttachPayloadSchema` is 27.4%;
  the destination plugin's `Run` is 20.6%, of which `destinationWithSchemaExtraction` is 18%. The cost is
  `schema.avro.SerdeForType` -> `hamba/avro.NewTypeResolver.Register`, building an avro type resolver per record
  (`mallocgc` is 27% of all samples). Engine code (`funnel.*`) is about 5%.
- Switching schema extraction off on both connectors (`sdk.schema.extract.payload.enabled=false`,
  `sdk.schema.extract.key.enabled=false`; shapes `*-noschema`) lifts every cell, including past 50k:

  | shape | v1 | arch-v2 main | prototype |
  | --- | --- | --- | --- |
  | 1x1, default | 52k to 65k | 46k | 66k to 72k |
  | 1x1, schema extraction off | 70.4k | 94.5k | 108.0k |
  | 4x4, schema extraction off (per sink) | 32.4k | 53.4k | 67.0k |

  (Single 12 s runs; indicative; source: `profile/smoke`.) Total destination writes at 4x4 with extraction off are
  213k to 268k/s, so there is no fixed 50k/s wall in the engine, the file connector or Docker.
- On this machine the total destination write rate across cells ranged from 46k to 160k/s (arch-v2 4x4 default is
  160k/s total), so "three cells near 50k" does not repeat here. The AWS cells sit near 50k because that machine's
  per-record SDK cost, not a shared lock, sets the per-source rate (a 1x1 source is a single plugin goroutine; at 2x2
  total reads are bounded by the same plugin-side cost and by the shared-tail lock, below).

**Consequence for the graduation bar.** At default config, a result "engine X >= v1" on this harness is bounded by
the connector SDK's per-record cost. An engine that removes its own overhead cannot go above the source-side ceiling
(78k/s here), and an engine already near it shows no difference from v1 once the SDK cost dominates. The latency-injected
destination shapes and a schema-extraction-off shape are the ones that separate engines; the default shapes alone do not.
(Phase 3 reports both.)

## 4. 2x2 and 4x4: `sharedMu` contention

`profile/docker/{2x2,4x4}-v2/stages.txt`, `mutex.pprof`, `trace-roles.txt`:

| | 1x1 | 2x2 | 4x4 |
| --- | --- | --- | --- |
| rec/s per sink (profiled) | 46.7k | 38.2k | 40.1k |
| mean `Worker.Do` pass | 21.3 us | 52.4 us | 99.8 us |
| `Source.Read` per pass | 11.9 us | 1.7 us | 1.5 us |
| `sharedMu` wait per destination branch | 0.03 us | **4.38 us** | **13.11 us** |
| `sharedMu` held per branch | 8.2 us | 11.8 us | 12.7 us |
| wait as share of hold | 0.4% | 37% | 103% |
| worker goroutine time in `waiting:sync` | | 89.0% | **91.7%** |
| mutex profile total | 0.24 s | 3.5 s | **55.6 s**, 98% `sync.(*Mutex).Unlock` |

- At 2x2 and 4x4 the read is no longer on the critical path (1.5 to 1.7 us); the pass time is lock wait plus the
  destination stage plus goroutine hand-offs of `pool.Go` per branch. The workers spend 89% to 92% of their time blocked
  on `sharedMu`.
- The lock is held for the whole destination stage including the ack wait (8 to 13 us), so each destination admits
  one batch at a time across all sources. The ceiling this sets per destination is `1 / held` = 79k to 85k rec/s at
  2x2 and 4x4, about twice the observed 38k to 40k, so at these shapes the lock explains the _shape_ of the curve
  (4x4 per-sink rate does not grow with sources) but not all of the gap; the rest is the per-pass goroutine
  fan-out and the SDK cost.
- v1 4x4 is 25.6k per sink (profiled) against arch-v2's 40.1k: v1's fan-in/fan-out nodes are the serial section there
  (cores used 2.98 vs 3.47). arch-v2 beats v1 at 4x4 in this environment, and loses at 1x1.

## 5. SDK batching against the stop-and-wait loop

How the pieces interact (code reading, sdk v0.14.2, then measured):

- There is no `ReadN` in the plugin protocol: the stream is `SourceRunRequest{AckPositions}` /
  `SourceRunResponse{Records}`. The SDK's read loop calls `ReadN(ctx, 1)` by default and sends each response. With
  `sdk.batch.size`/`sdk.batch.delay` set, a middleware (`sourceWithBatch`) collects records into one response
  inside the plugin. The engine's batch is "whatever one response carried".
- The in-memory stream (built-in connectors) is unbuffered and clones the request/response on every `Send`.
- The SDK destination `Run` loop is serial: `Recv` -> `Write` -> ack `Send`. With `sdk.batch.size > 1` the write
  strategy buffers: `Write` returns after enqueueing, and acks are sent when the batch flushes (size reached, or
  `sdk.batch.delay` after the first buffered record; with `delay == 0` never by time).
- Stop-and-wait waits for every ack of a write before the next write. With a destination batcher and one-record
  batches, each write waits for the timer.

Measured (instrumented `ce758f96` build, 15 s windows; host load was high during these, so ratios, not absolutes):

| shape | v1 | arch-v2 main |
| --- | --- | --- |
| 1x1 default | 52k | 46.7k |
| destination `sdk.batch.size=100`, `delay=10ms`, source default | **56.7k** | **83 rec/s** |
| source `sdk.batch.size=100`, `delay=10ms` | 36.8k | 51.5k |
| source batch 10 / 100 / 1000 (delay 10ms) | 65.8k / 74.4k / 75.7k | 86.7k / 100.0k / 103.8k |
| source and destination batch 100, delay 10ms | 91.3k | 100.0k |
| source batch 100 + **destination** batch 100, `delay=0` | 99.5k | **hangs** (no output for 20+ min) |

- arch-v2 main with a destination batcher and default one-record reads runs at 83 rec/s: one 10 ms flush per record,
  a 99.8% loss. v1 is unaffected because its acker reads acks on another goroutine and the destination keeps receiving.
  This is the interaction the brief asked about, and it is large.
- With `delay=0` and batch size 100, arch-v2 main stops delivering entirely when a write does not align with the batch
  size (the SDK holds the partial batch and never flushes by time, and the engine will not send the next write until
  the ack for this one arrives). Not a performance problem: a liveness failure of arch-v2 main with that config.
- **Alternative A (larger source batch on arch-v2 main) closes the gap and goes past v1**: source batch 10 already puts
  arch-v2 main at 1.32x v1, batch 100 at 1.34x. See the Phase 3 sessions for the controlled version (same 3-arm,
  A/A-controlled method). This is the key control for the design: the default batch of one is what makes arch-v2 slow
  at 1x1, and a batch of 10 or more removes the problem on arch-v2 main without any new engine.
- A delay timer inside the plugin is not part of the default-config gap (delay is 0), but it is the entire gap in the
  destination-batcher shape above.

## 6. Other findings that cost time per record

- `ConnectorMetricsImpl.Observe` starts a goroutine per call, two per record (one on read, one on ack): 90k goroutine
  creations/s at 45k rec/s (trace creation counts). Together they cost 0.5 us per pass (2.4% of the pass) directly,
  plus the GC and scheduler work of 90k short-lived goroutines per second. It is orthogonal to the staged engine
  and a cheap separate fix (batch the observation, no goroutine).
- Destination file connector: one `write(2)` per record (`os.File.Write`): 37% of CPU samples in the macOS profile,
  ~4 to 5 us of the 7.3 us destination stage on Linux. That is a property of the benchmark sink, not of Conduit.
- Persister/`Source.Ack`: 0.5 us per pass at batch 1, per `Source.Ack` call; the prototype coalesces acks across
  batches into one call, which removes most of that.

## 7. What this means for the prototype

- Expected ceiling for any engine at 1x1 default on this machine: ~78k rec/s (source side). Stop-and-wait is at 46k to
  47k, v1 at 52k to 65k. A prototype that reaches 65k to 78k is "at the ceiling".
- The benefit at default config is bounded and attributable: remove the stage-sum (21.3 -> ~13 us), remove `sharedMu`
  at the destination, and make destination batching work (the 83 rec/s case).
- A prototype result at default shapes is not sufficient evidence of an engine win by itself, because the SDK cost
  bounds every engine; the schema-extraction-off and latency shapes are needed beside it.

## Files

- `profile-instrumentation.patch`: the profiling-only patch (stage timers, trace/mutex/block hooks, canned source,
  skip-destination switches).
- `profile/docker/<shape>-<v1|v2>/`: `cpu.pprof`, `mutex.pprof`, `block.pprof`, `trace.out` (2 s), `stages.txt`,
  `trace-roles.txt`, `rate.txt`; `profile/docker/rates.txt` is the summary.
- `profile/ceil/`: source-only and destination-only ceiling runs. `profile/altA/`, `profile/sdkbatch/`: batching
  runs. `profile/proto-1x1/`: CPU profile of the prototype. `profile/1x1-v1`, `profile/1x1-v2`: macOS native runs.
- `traceanalyze/`: the trace state analyser; `run_docker.sh`, `run_native.sh`, `run_profiles.sh`: runners.

## 8. Cross-check with the prototype and the in-memory stream (added after Phase 3)

Two results from the Phase 3 sessions (`results/README.md`) bear on this profile:

- **The 1x1 stage-sum is not only the engine.** With the builtin in-memory stream given a 64-slot buffer
  (`patches/stream-buffer.patch`, an environment variable on identical binaries), v1, arch-v2 main and the prototype all
  run at 97k to 100k rec/s at 1x1 (session 15); unbuffered they run at 65.9k, 53.6k and 72.7k (control, session 16). The
  source-side ceiling of 78k measured in section 1 applies to the unbuffered stream; with a buffer the source plugin
  produces ahead in parallel and the ceiling moves to the ~100k SDK cost. So the "source does not overlap the
  destination stage" observation in section 2 is a property of the synchronous handoff (the `runnext` mechanism
  proposed there), and it is removed for every engine by buffering. Section 7's "78k bound" should be read as "bound for
  an engine that keeps the unbuffered stream".
- **Hypothesis scorecard.** "v2 is stop-and-wait and throughput is the sum of the stages": confirmed (section 1).
  "The source sits idle during write and ack": not confirmed (section 2). "N-source shared tail is serialized and a
  destination never has more than one batch in flight": confirmed and large at N>1 (section 4; the prototype's 2x2 gain
  is +40% over arch-v2 main, 133.5k against 37.6k with the stream buffered). "Fixing it gets arch-v2 to v1 at 1x1":
  true only partly: prototype +8% to +10% over v1 with the unbuffered stream, -18% below v1 with buffered acks, level
  with v1 when the stream is buffered.

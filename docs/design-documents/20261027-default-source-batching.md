# Default source batching

## Summary

Make source batching the default in the Go connector SDK. Today a source that leaves `sdk.batch.*`
unset reads one record at a time. The SDK would instead hand the engine up to 100 records per read.
There are two candidate semantics:

- **Adaptive drain (proposed):** `sdk.batch.size=100`, `sdk.batch.delay=0`. The collector blocks for
  the first record, takes whatever else the SDK's read-ahead has already queued (up to 100), and
  returns. There is no timer. Batches grow when the engine is the bottleneck and shrink to one record
  when the source is sparse. This needs an SDK change: `delay=0` with `size>0` currently means "wait
  until the batch is full, with no deadline".
- **Fixed window (fallback):** `sdk.batch.size=100`, `sdk.batch.delay=10ms`, with today's SDK
  semantics. This is the only variant measured so far.

The default lives in `conduit-connector-sdk` (the `SourceWithBatch` struct tags, emitted into each
connector's generated `connector.yaml`). It reaches users only when a connector is re-released
against the new SDK. The engine injects nothing, and destination batching defaults do not change.

Arch-v2 is stop-and-wait: read, write, wait for the destination ack, ack the source, read again. At
one record per read, each record pays that whole round trip. On the archv2-gate harness, a fixed
100/10ms source batch took arch-v2 1x1 generator→file from 52.6k to 214.9k rec/s (+308.7%, resolved
against a ±2.5% A/A floor). That is a design input, not a performance claim. The benchi plan below
must confirm a gain at shipped defaults, on real connectors and on both engines, before anyone
quotes a number. It must also decide between the two variants.

This is Tier 1. It changes the default ack and position granularity, the crash-replay window, the
graceful-stop drain, and the size of each destination `Write` for every pipeline whose source does
not set `sdk.batch.*`.

Roadmap: v0.21 Engine, "Adaptive batching by default. No throughput claim without a committed benchi
run"; and the v0.22 engine flip, which is gated on arch-v2 being "parity-or-faster at shipped
defaults".

## Context and problem

### What a default pipeline does today

With `sdk.batch.size` and `sdk.batch.delay` at 0, `SourceWithBatch.Open` (SDK v0.17.2) starts no
collector; `ReadN` forwards to the connector, and the SDK's run loop calls `ReadN(ctx, 1)`
(`source.go`, `runRead`). Connectors that do not implement `ReadN` (kafka, file, s3, generator) fall
back to `Read` and send exactly one record per stream message. Arch-v2 then runs one funnel pass per
record: `Source.Read` → processors → `Destination.Write` of one record → wait for its ack →
`Source.Ack` of one position → `Persister.Persist`.

The 1x1 profiling notes (main a23efb64, generator→file, AWS c7i.4xlarge; a working file, not
committed, referred to below as _the notes_) show the cost is the handoffs, not CPU: the worker
goroutine spends 21.5s of ~50s blocked in `Source.Read` and 20.3s in the destination ack `Recv`, the
process uses 1.36 of 16 cores, and the engine executes its own code for ~16% of wall time. Each hop
is a goroutine wakeup; batching amortizes all of them.

### Postgres is the exception

Postgres v0.17.1 (#3213) introduced an internal CDC batch of 1000 when `sdk.batch.size` is 0
(`source/logrepl/combined.go`, `effectiveCDCBatchSize`). Everything below was read in v0.18.0, the
version Conduit's `go.mod` pins. `CDCIterator.NextN` returns the first handler batch unclamped, even
when asked for one record. So at shipped defaults, Postgres CDC already reaches the engine in batches
of up to 1000, flushed by the handler at 1000 records or by a 1s ticker. Two consequences:

- A non-zero SDK size would _lower_ Postgres CDC granularity: `effectiveCDCBatchSize(100)` is 100.
- The snapshot fetch size is `sdk.batch.size` too (`combined.go`: `FetchSize: c.conf.BatchSize`,
  with 0 meaning 50000 in `snapshot/fetch_worker.go`). The documented `snapshot.fetchSize` parameter
  (default 50000) is parsed but never read. A non-zero SDK default would make every snapshot `FETCH
  100`.

So Postgres must pin today's settings before it adopts the new SDK (see Decision).

### Evidence

| Run | Shape | Result | Control |
| --- | --- | --- | --- |
| archv2-gate session `matrix`, 2026-10-27T04:03Z, AWS (Xeon 8488C, 16 vCPU), 5 rounds, 60s window | 1x1 generator→file, arm `v2` vs `v2-alta` (`1x1-srcbatch100`: 100/10ms set explicitly) | 52,575 → 214,861 rec/s, +308.7%, every round +306.7% to +310.6% | A/A floor ±2.5%, resolved |
| Same session | `v1` default | 40,046 rec/s | ±2.0% |
| The notes, §5 sweep, main a23efb64, c7i.4xlarge, 3 interleaved rounds | 1x1 generator→file, source batch 1 / 10 / 100 / 1000 / 2048 | 54k / 137–150k / 221–235k / 227–256k / 225–247k rec/s | none (spread ≤3.5%, not A/A) |

The curve plateaus at about 100: 100 records gets ~91% of the gain of 1000. Past 100, the
connectors' per-record SDK work dominates. Neither run is committed. The gate image has no
source-revision label (binary sha256 `d5a91df6…`). Both runs use the two cheapest connectors there
are, and neither measures v1 with batching or the adaptive variant. Real sources will gain less.

## Constraints

1. **Invariant 1: ack only after a durable downstream write.** Batching happens in the connector
   process, before the engine sees a record. The engine's ack path is unchanged: a batch is acked
   only after every destination confirmed it, and `Source.Ack` defers the plugin ack until the
   persister flushed the position (`pkg/connector/source.go`).
2. **Invariant 2: positions monotonic and crash-safe.** No position format changes.
3. **Invariant 3: at-least-once, including error paths.** Records the SDK read but did not send
   (read-ahead, a partly collected batch) are never acked, so they replay from the last persisted
   position. The replay window must be bounded and stated, and an error must not discard good
   records that today would have been delivered.
4. **Invariant 4: per-partition order.** The collector appends in read order and sends one ordered
   slice.
5. **Invariant 7: graceful stop.** `Stop` stays bounded. The SDK cancels the read context at once and
   discards what it has not sent, and the engine drains what is in flight.
6. **The engine cannot set the default for standalone connectors.** Older SDKs reject unknown
   parameters, and a connector's spec is its contract.
7. **Bounded memory and latency**, stated per connector, including read-ahead.
8. **No protocol change.**

## How a source default reaches a pipeline

1. `SourceWithBatch` declares `BatchSize *int` with `default:"0"` and `BatchDelay *time.Duration`
   with `default:"0"` (`source_middleware.go`).
2. `conn-sdk-cli specgen` writes those defaults into the connector's `connector.yaml`. A connector
   that sets a non-nil initial value in `NewSource()` overrides the tag default there
   (`overwriteDefaults` checks `!value.IsZero()` on the pointer, so a pointer to 0 counts).
3. At runtime the SDK applies defaults from the **embedded spec**, not the struct tags:
   `NewSourcePlugin(..., spec.SourceParams)` → `Util.ParseConfig` → `ApplyDefaults(params)`. The
   built-in registry does the same (`pkg/plugin/connector/builtin/registry.go`).

So bumping the SDK in Conduit's `go.mod` alone changes nothing for built-ins. A connector picks up
the new default when it bumps the SDK and regenerates its spec, which `validate-generated-files`
forces in the same PR. A standalone connector built with an older SDK keeps its old default.

## Alternatives considered

### A. SDK default, fixed window (100/10ms)

Change the two tags to 100 and 10ms, with today's collector semantics.

- **Pros:** measured (+308.7% on 1x1 generator→file). The default is in the spec, so docs, `describe`,
  the UI and `llms.txt` show the real value. Connectors pin their own values through the existing
  initial-config mechanism.
- **Cons:** a sparse source (under `S` records per `D`, here 10k rec/s) pays up to 10ms per batch,
  and it is a fixed constant rather than the "adaptive" batching the roadmap names. It also keeps
  `delay=0` with `size>0` meaning "wait until full, no deadline". That is the stall-prone
  combination, and a user who sets only `sdk.batch.delay: 0` would get it (see Failure modes).

### B. SDK default, adaptive drain (proposed)

Defaults `size=100`, `delay=0`. `delay=0` with `size>0` changes meaning: block for the first record,
then take whatever is already queued without blocking, up to `size`, and return. A non-zero delay
keeps today's fixed-window meaning for users who set it.

The SDK already reads ahead: the `Read` fallback goroutine fills `readCh` (capacity `size`), and
`runReadN` keeps one `ReadN` result in `readNCh`, while the engine is busy with the previous batch.
In a stop-and-wait engine this is self-tuning. When the engine is the bottleneck, the queue is full
when the engine comes back, so it gets full batches. When the source is the bottleneck, it gets what
arrived and nothing waits for a timer. A sparse source gets one-record batches with no added
latency.

- **Pros:** no added collection latency and no constant to tune. It also removes the stall case, as
  delay 0 no longer waits for a full batch, and it matches the roadmap item.
- **Cons:** unmeasured. Batch size depends on relative stage speeds, so behavior is harder to predict
  in a benchmark. Redefining `delay=0` changes behavior for users who set `sdk.batch.size` without a
  delay today: they get smaller batches under light load instead of a possibly unbounded wait.
- **Decision rule:** proposed, provided the benchmark shows it within the A/A floor of A at
  saturation. If it falls short, ship A.

### C. Engine injects `sdk.batch.*` for built-in sources

- **Pros:** no SDK or connector release; can be limited to arch-v2.
- **Cons:** the effective default contradicts the published spec, so the same connector behaves
  differently built-in and standalone. Standalone connectors cannot be covered, because older SDKs
  reject unknown parameters. The engine gains knowledge of one connector family's middleware.
  Rejected.

### D. Engine-side coalescing with no added latency

The `protorc` prototype: the destination writer takes whatever is already queued and writes it in
one `Write`. The notes, §4: +66% at 1x1 default (54.5k → 90.8k), +412% per sink at 4x4.

- **Pros:** zero added latency; works for every SDK and language.
- **Cons:** at 1x1 it reaches 90.8k against ~215k for source batching. It depends on #2978's
  async-ack machinery (four close and failure bugs, the notes §6) and is a far larger data-path
  change. Complementary, not a substitute; it belongs to `20261009-staged-batch-engine.md`. B is
  the same idea applied at the source, where the read-ahead queue already exists.

### E. Per-connector defaults only, SDK unchanged

- **Pros:** third-party connectors are unaffected.
- **Cons:** the one-record trap remains for every other Go connector and every new scaffold.
  Rejected as the mechanism. We still use it where a connector needs a different value.

### F. A larger default (1000)

- **Pros:** at most ~10% more throughput in the uncontrolled sweep.
- **Cons:** 10x the memory, replay and drain work. Rejected.

## Decision

### SDK (`conduit-connector-sdk` v0.18.0)

- **Defaults.** `SourceWithBatch` tags become `size=100`, `delay=0`, with the adaptive drain
  semantics (B). If the benchmark rules B out, the tags become 100 and 10ms with today's semantics
  (A), and the `Configure` rules below still apply. Destination tags stay at 0.
- **Handle inconsistent combinations at `Configure`.** Each rule has a stable code, the config path,
  and a fix. Nothing that works today becomes a start failure in the release that introduces the
  rule (see Deprecation below). The SDK can tell explicit settings from defaulted ones because
  `parseConfig` receives the raw config before `ApplyDefaults`.
  - `size=0`, `delay>0` (delay-only). Today `runReadN` calls `ReadN(ctx, 0)`. Postgres's `NextN`
    errors on that ("n must be greater than 0"), and a connector that accepts 0 hits the collector's
    `len(resp) >= 0` early return, so it does not batch at all. Only `Read` connectors get real
    delay-only batching. Normalize: use the SDK's destination-side cap
    (`defaultMaxBatchSize = 1000`, #428) as `n` and as the record bound, and log a WARN with code
    `sdk.batch.size_defaulted`. For `Read` connectors this caps delay-only batches that were
    uncapped, which the release notes state.
  - Under A, a user who sets only `sdk.batch.delay: 0` would inherit size 100 with no deadline, a
    stall the upgrade itself creates. The SDK keeps that user on today's passthrough (size treated
    as 0) and logs a WARN with code `sdk.batch.unbounded_wait`.
  - Exception, decided during implementation (conduit-connector-sdk#433): when the connector's spec
    ships a non-zero delay default (for example MySQL's 10000 / 1s) and the user sets
    `sdk.batch.delay: 0`, with or without a size, today's behavior is wait-until-full at the spec
    size. The SDK keeps that behavior and logs `sdk.batch.unbounded_wait`; it does not drop to
    passthrough. Dropping to one record per read would be a throughput cliff for an existing
    config, which the deprecation policy rules out without an announce-and-warn cycle.
  - Under A, an explicit `size>0` with `delay=0` (wait until full, no deadline) works today and
    already logs an uncoded warning. It gets a WARN with code `sdk.batch.unbounded_wait`, and is
    rejected no earlier than two minors later.
  - Under B, an explicit `size>0` with `delay=0` changes meaning from wait-until-full to drain. That
    is a config-meaning change and follows the deprecation policy below.
- **Deprecation (B).** Announce, warn, then switch, over two minor releases:
  - SDK v0.18.0 (N). The defaults change. A user-set `size>0` with no user-set delay keeps today's
    wait-until-full meaning and logs a WARN with code `sdk.batch.drain_semantics_pending`. The
    release notes show the old and new meanings and the setting that keeps full batches
    (`sdk.batch.delay` above 0).
  - SDK N+1. The same, plus the notice in the connector docs.
  - SDK N+2. A user-set `size>0` with `delay=0` means drain.

  Only explicit settings follow this schedule. Pipelines that rely on the defaults get drain from N,
  since they had no batching before.
- **How the codes reach users.** The SDK has no error-code mechanism, and a `Configure` error reaches
  Conduit as a plain string. The SDK will:
  - export the codes as string constants in a new `sdk/codes.go`;
  - log WARNs with a structured `code` field, the convention the S3 connector already uses (`code`,
    `s3.source.lookback_short`);
  - when a rule becomes a rejection, start the error message with the code and a colon.

  Conduit registers the same codes with `conduiterr.Register` and maps a connector `Configure`
  error whose message starts with a registered SDK code to that code, keeping the original message.
  Agents and `--json` output then match the code, not the text. Old SDKs emit no codes, so the
  mapping is a no-op for them.
- **Bound the batch at `size` with a carry-over buffer.** `collectWithReadN` currently appends whole
  `ReadN` results. Worse, its `len(batch)==0 && len(resp) >= size` early return passes a connector
  that ignores `n` through unclamped; Postgres's first blocking batch can be 1000 whatever `n` is.
  The collector returns at most `size` records and keeps the excess in a carry slice, which the next
  collect consumes first. On `Stop` or an error, the carry is dropped unsent and unacked, and it
  replays from the persisted position.
- **Flush then fail.** On a `Read`/`ReadN` error mid-collection, both collectors currently return
  `nil, err` and drop the records already collected (`collectWithRead`, `collectWithReadN`). A poison
  record would then discard up to `size−1` good records that today are delivered before the error.
  The collector returns the collected records and stashes the error. Each following call returns
  the carry first, in order, at most `size` per call. Once the carry is empty, the next call returns
  the stashed error at once, without reading the channel. The read goroutine has exited and closed
  it, and the closed-channel path (`return batch, ctx.Err()`) would otherwise return an empty slice
  with a nil error, which `runRead` treats as "no records" and retries forever. The closed-channel
  path itself must never return an empty slice with a nil error.

  This applies only to errors from the connector. Context cancellation (`Stop`, `Teardown`) is not
  an error to flush around: the collector returns `nil, ctx.Err()`, discarding the partial batch and
  the carry, as described under Graceful stop. At-least-once holds in both cases; flush-then-fail
  keeps the delivered set the same as at batch 1.
- **Log the effective batching at `Open`**: one INFO line with size, delay, mode (adaptive or
  window), and the `ReadN` or `Read` path.
- **Opt-out:** `sdk.batch.size: 0` together with `sdk.batch.delay: 0` restores today's passthrough
  path exactly.

### Engine (Conduit)

- **Persister threshold counts positions, with `>=`.** API change: `Persist(ctx, conn, callback)`
  becomes `Persist(ctx, conn, count, callback)`, where `count` is the number of positions the call
  covers. The package is internal and all three callers break at compile time, so none can be missed:
  - `source.go:652` (`Source.Ack`) passes `len(p)`;
  - `source.go:278` (source lifecycle-event persist) passes 1;
  - `destination.go:139` (destination lifecycle-event persist) passes 1.

  These keep counting 1, as today. `Persist` triggers on
  `p.bundleCount == p.bundleCountThreshold` (`persister.go:190`) and counts calls; arch-v2 makes one
  call per batch. Counting `len(p)` alone would make the counter step past 10000 with unaligned
  batches (100, 37, a Postgres 1000) and never equal it, so the flush would quietly fall back to the
  1s timer. The change: `bundleCount += count`, trigger on `>=`, and keep the reset in
  `triggerFlush`. This needs a test with unaligned batch sizes (for example 37, 100, 1000, repeated)
  that asserts a flush once the threshold is crossed. That test fails both before the change and
  with `==`. v1 acks one position per call and is unaffected.
- **Destination message size (prerequisite, separate PR).** See Failure modes. The byte cap on
  destination `Write` is being added in a separate fix PR, as a byte cap in the funnel accumulator.
  This design depends on that cap also covering the non-buffered `DestinationTask.Do` path: at
  defaults the source batch goes straight to `Write`, so an oversized batch must be split across
  several `Write` calls. The default must not ship before that.
- **Metrics** (see Observability). No other engine change; both engines accept multi-record reads.

### Connectors

Every built-in source re-releases on SDK v0.18.0 with a regenerated `connector.yaml`. Conduit then
bumps each one in its own PR, and that PR is the switch for that connector.

| Connector | Source read path | Decision |
| --- | --- | --- |
| postgres | `ReadN` (CDC handler batches, snapshot `FETCH`) | Pin `sdk.batch.size: 0` and `sdk.batch.delay: 0` with non-nil pointers in `NewSource()`. That keeps today's path exactly: CDC at its internal 1000 with a 1s handler flush, snapshot `FETCH 50000`. A test asserts the regenerated `connector.yaml` shows `default: "0"` for both, so the pin cannot silently regress to 100. Separately, wire `snapshot.fetchSize` into the snapshot iterator with a regression test. |
| kafka | `Read` (franz-go fetches, FIFO `batchAcker`) | Inherit. |
| file | `Read` (line follower) | Inherit. |
| generator | `Read` (optional rate limiter) | Inherit. |
| s3 | `Read` (one record per object, held whole via `io.ReadAll`) | Pin 0/0 (today's behavior) until there is a byte bound on source batches. |
| log | destination only | SDK bump only. |

## Costs and bounds

All bounds are per source. `S` is `sdk.batch.size` (100), `D` is `sdk.batch.delay` (0 under B, 10ms
under A), and `R` is the largest `ReadN` return of a connector that ignores `n` (`R ≤ S` for `Read`
connectors).

### Latency

- **B:** no collection wait. A record waits only behind records already queued ahead of it. That is
  bounded by the read-ahead (`S` in `readCh` or one `ReadN` result) plus one batch in the engine,
  about `2S` records at the pipeline's throughput, and nothing for a sparse source. We will not
  publish a time figure for this, in release notes or anywhere else, until the benchmark measures
  it.
- **A:** the same queueing, plus up to `D` for the first record of a batch when the source produces
  fewer than `S` records per `D`.
- **Postgres** is unchanged because it pins today's settings. Its 1s handler flush is a pre-existing
  floor.

**Dashboards shift on upgrade.** `conduit_pipeline_execution_duration_seconds` already measures from
the record's `opencdc.readAt`, in both engines (`funnel/worker.go` `updateTimer`;
`lifecycle/stream/source.go`). The SDK stamps `readAt` when the connector builds the record, so the
metric includes collector wait and read-ahead queueing. After the upgrade, latency panels and alerts
will move: for dense sources by the queueing term (records now wait behind up to ~`2S` others,
instead of one), and under A also by up to `D` for sparse sources. The release notes must say this
and give the magnitude per variant, taken only from the committed benchmark results.

### Memory

The SDK holds the read-ahead (up to `S` records in `readCh`, or one `ReadN` result of up to `R`), the
batch being collected (at most `S` with the cap), the carry (up to `R−1`), and one record in the
connector's `Read`. At defaults, arch-v2 also holds one batch in flight. That totals about `3S` for
`Read` connectors and about `2S + 2R` for `ReadN` connectors that overshoot. At 64-byte payloads,
about 2 KB per record with positions and metadata, `3S` is about 600 KB.

With destination batching on, the funnel adds up to `(maxInFlightFlushes + 2) × effectiveBatchSize`
records, which is 4 × 3000 = 12,000 at its defaults (`destination_batch.go`), whatever `S` is. Large
payloads scale linearly, which is why S3 pins 0/0.

### Crash replay (duplicates)

A crash replays everything after the last durably persisted position. Today the persister flushes
after 1s or 10000 `Persist` calls. At batch 1 and 54k rec/s, that is a flush every ~185ms, with up
to ~10k records replayed. At batch 100 and ~215k rec/s without the persister change, the 1s timer
decides and up to ~215k records replay. With positions counted and `>=`, the bound is:

- 10,000 positions (the threshold), plus
- one in-flight batch (`S`), plus
- whatever the funnel destination holds when destination batching is on (up to 12,000).

Read-ahead, carry and partly collected records are re-read, not duplicated: they never reached a
destination.

### Graceful stop

`Stop` cancels the SDK's read context at once. Read-ahead, carry and partly collected records are
dropped unsent and unacked, and they replay. `LastPosition` is the last position actually sent,
which the engine already waits for. The engine drains what is in flight:

- At defaults, one batch: one `Write` and its ack instead of one record's. For a destination that
  handles records one at a time with real latency (the matrix's `W-pg-http-slowsink`, a 5ms sink
  delay), up to ~500ms.
- With destination batching on, the funnel flushes its buffer (up to 12,000 records) before
  teardown, as it does today.

The bounded-stop work (#2990) must budget for both cases.

### Ack and position granularity

Arch-v2 calls `Source.Ack` once per batch with all positions. The state position becomes the last
one, `Persist` is called once, and the plugin receives one `AckPositions` message. The SDK then calls
the connector's `Ack` once per position, in order (`runAck`), so connectors see the same per-record
acks as today. Postgres and S3 are pinned. Kafka's `batchAcker` commits in FIFO order on its own
`commit.offsets.*` settings. Read-ahead adds up to ~`2S` consumed-but-unacked records, which a
consumer-group rebalance could redeliver to the new owner. That last point is by reasoning, not
traced. File and generator `Ack` are no-ops. A nack inside a batch goes through the existing arch-v2
sub-batch path (`worker_nack_position_test.go`, `multi_ack_nacker_test.go`,
`worker_ack_order_test.go`), which becomes the default path. v1 acks each record separately
(`pkg/lifecycle/stream/source_acker.go`).

## Failure modes

- **Standalone destination receives a batch over 4 MiB.** The engine's gRPC client is not the limit:
  go-plugin v1.8.0 sets `MaxCallRecvMsgSize(math.MaxInt32)` (`grpc_client.go:40`), so a standalone
  _source_ batch is received whatever its size. The limit is the plugin's gRPC server receive size,
  `connectors.max-receive-record-size` (default 4 MiB, SDK `serve.go`), which applies to
  engine→plugin messages. `pkg/connector/destination.go` sends one `DestinationRunRequest` per
  `Write`. At defaults, arch-v2 passes the source batch straight to `Write`, so a destination that
  received one record per message today would receive 100. Any batch averaging over ~40 KiB per
  record exceeds 4 MiB, fails on every retry, and cannot clear by itself. v1 writes one record per
  call and is not exposed. Mitigation: the byte cap in the separate fix PR, covering the
  non-buffered path (a prerequisite, above). Test: a standalone destination with records just under
  and just over the cap per batch.
- **SIGKILL while collecting.** Nothing collected was sent or acked, so restart resumes from the last
  persisted position. New chaos scenario.
- **SIGKILL after a batch was written but before its position persisted.** The batch replays within
  the bound above. New chaos scenario asserting the bound, not only absence of loss.
- **Persister counter steps past the threshold.** Prevented by `>=`, and the unaligned-batch test
  pins it.
- **Read error mid-batch.** Flush-then-fail delivers the good records first. SDK tests:
  - An error after `k < S` records delivers those `k`, then the error.
  - With a carry larger than `S`, calls return the carry in order (at most `S` each), then the
    stashed error, and never an empty slice with a nil error. A spinning `runRead` fails the test
    on a deadline.
  - Cancellation mid-collection returns `nil, ctx.Err()` and nothing collected is sent.
- **SIGTERM with a partial batch.** Dropped unsent, then replayed. The existing `sigterm_test.go` and
  drain tests must run with a batched source.
- **Single-setting configurations.** Handled at `Configure`, WARN first (Decision). Without that:
  `delay: 0` alone under A stalls a sparse source with records unacked, and `size: 0` alone on a
  `ReadN` connector errors (Postgres) or silently does not batch.
- **Postgres adopts the SDK without pinning.** CDC batches drop to 100 and snapshots to `FETCH 100`.
  The `connector.yaml` golden check and a unit test on the effective sizes catch it.
- **Memory with large records.** Bounded as above. S3 pins 0/0. The `b16384` cells measure RSS.

## Upgrade, rollback and versioning

- **SDK:** v0.17.x → v0.18.0, a minor bump for a default change. Under B, it also starts the
  two-minor deprecation of explicit `size>0, delay=0` meaning wait-until-full (see Decision). The
  changelog states the new defaults, the opt-out, the `Configure` rules, their codes, and the
  deprecation schedule.
- **Connectors:** each built-in source gets a minor release on SDK v0.18.0. Conduit bumps each in a
  separate PR, which is that connector's switch.
- **Standalone connectors built with older SDKs:** unaffected. They adopt on their own SDK bump.
- **Other SDKs:** the Python SDK mirrors the Go defaults (`_batch.py`, 0 and 0). Whether to match is
  an open question, under `20261008-sdk-parity-and-open-plugin-protocol.md`.
- **Stored config:** unchanged. Defaults apply at `Configure`, so an existing pipeline without
  `sdk.batch.*` switches when Conduit upgrades to a release with the re-released connector.
- **Serialized state:** no format changes. An upgrade test is still required: run N at batch 1, stop
  mid-stream, upgrade, resume batched, and verify no loss and replay within the bound. The upgrade
  gate is not live ("by Phase 2"), so this test is part of the work, not a gate check.
- **Rollback:** per pipeline, set both to 0. Per release, revert the connector bump. No migration.

## Observability

- **Existing:** `conduit_pipeline_execution_duration_seconds` is end-to-end from `readAt` and will
  shift as described under Latency.
- **New:** `conduit_source_read_batch_size` (histogram; labels pipeline, plugin, component ID),
  recorded by the engine per `Source.Read`.
- **New:** `conduit_source_batch_read_span_seconds`: latest minus earliest `readAt` within a batch.
  This is how long the batch took to read. Under A it approximates the collection window, bounded by
  `D` plus read time. It does not include waiting after the last read.
- **New:** `conduit_source_batch_handoff_seconds`: engine receipt minus the latest `readAt` in the
  batch. It covers the wait after the batch's last record was read: queueing behind a busy engine
  (backpressure) plus, under A, any remaining timer.
- **What these cannot isolate:** neither metric separates "batching cost" from "engine slow". The
  SDK does not export the collector's own timer across the plugin boundary. The benchmark's latency
  criterion therefore compares end-to-end p99 between arms at the same offered load, set below
  capacity, where no backpressure queue forms.
- **Runbook** (`docs/operations/source-batching.md`): symptom → diagnosis → remediation for higher
  latency after upgrade, memory growth with large records, the destination message-size error, and
  the `Configure` codes.

## Test plan

- **SDK:** new defaults. Adaptive drain: a full queue yields `S`, an empty queue yields 1, and
  nothing waits on a timer. The size cap with carry, including a connector that ignores `n`.
  Flush-then-fail, ordered carry then stashed error, and discard on cancellation (see Failure
  modes). The `Configure` rules: WARN with a `code` field, defaulted versus explicit handling, and
  the deprecation-window behavior. Opt-out is byte-for-byte today's
  passthrough.
- **Postgres:** the `connector.yaml` golden shows 0/0; effective CDC and snapshot sizes at shipped
  defaults; `snapshot.fetchSize` is honored (fails before the fix).
- **Persister:** unaligned batch sizes trigger the count flush (fails with `==`).
- **Chaos (`tests/chaos`, required):** batched-source variants of the SIGKILL recovery, SIGTERM drain
  and N-source scenarios, on arch-v2 and v1. Each kill test asserts no loss, ordering, and replay
  within the bound.
- **Destination size:** the standalone destination test from Failure modes (owned by the byte-cap
  fix PR; this design depends on it).
- **Upgrade:** the resume test above.
- **Acceptance:** run each connector's suite at shipped defaults, so SRC-02 goes through the batched
  path.

## Benchmark plan

No performance number goes into release notes, docs or the README until these results are committed
in `benchi/` with configs and environment. Report medians with variance and the A/A floor, on
quiesced AWS hosts.

1. **archv2-gate harness** (`benchi/archv2-gate`, PR #3067, not on main yet).
   - Arms: `v1`, `v2`, `v1-fixed`, `v2-fixed` (100/10ms), `v1-adaptive`, `v2-adaptive` (100/0 on the
     new SDK), and a **shipped-default** arm per engine. The shipped-default arm is built with the
     re-released generator and has no `sdk.batch.*` in the pipeline file, which proves the default
     reaches the engine.
   - Shapes: 1x1, 2x2 and 4x4.
   - Sweeps on 1x1: size {10, 100, 1000} for each variant, and delay {1ms, 5ms, 10ms} for A.
   - First, fix the missing source-revision label on the image.
2. **Competitor matrix** (`benchi/v021-cdc-parity` and the matrix plan, whose cell definitions are
   not committed yet; commit them with the results). Run the `conduit-s` (shipped-default) arm and
   the `conduit-classic-s` (v1) arm in every cell listed:
   - Sources that change: `B-kafka-file-p1-b64`, `B-kafka-pg-p1-b64`, `B-kafka-s3-p1-b64`,
     `C-file-kafka-b64`, with the `p4`/`p16` holdouts.
   - Postgres as control (pinned, so expected within the floor): `A2-pg-file-p1-b64`,
     `A2-pg-kafka-p1-b64`, `A2-pg-pg-p1-b64`.
   - Snapshot: `F-pg-kafka-snapshot`, `F-pg-pg-snapshot`.
   - Latency: `W-pg-kafka-idle`, the `A1-*-ramp` cells, and a sparse kafka→file run (under 1k rec/s)
     at fixed offered load.
   - Large records: the `b16384` holdouts, with RSS. Add a standalone-destination arm on one of them
     to exercise the destination byte cap.
   - Recovery: `G-pg-kafka-kill9`, `G-pg-pg-kill9`, and the `H-*-verify` cells with kafka or file
     sources, comparing replay counts against the bound.
3. **Pass criteria:**
   - The B and C cells improve beyond the A/A floor at shipped defaults on arch-v2.
   - No cell regresses beyond its floor on either engine.
   - Choosing B requires it to land within the floor of A at saturation.
   - Sparse-cell end-to-end p99 at fixed offered load: within the floor of today's default for B, and
     at most today's plus `D` for A.
   - Kill9 replay counts stay within the bound, and the snapshot cells do not move.

`make bench-engine` uses fake connectors that bypass SDK specs, so this change does not move it.

## Docs

These ship in the same PRs as the code:

- The SDK `SourceWithBatch` godoc and README.
- The regenerated connector references.
- The batching page in the `conduit.io` docs source: defaults, adaptive drain, the opt-out, the
  `Configure` codes, the per-connector table, and the dashboard shift.
- An `llms.txt` entry.
- The runbook.
- Changelog entries in the SDK, in each connector, and in Conduit's release notes.

## Open questions for DeVaris

1. **Variant.** Adaptive drain (B, proposed, unmeasured) or fixed 100/10ms (A, measured)? The
   proposal is to decide by the benchmark rule above. Redefining `delay=0` is the cost of B.
2. **Postgres.** Pin 0/0 (proposed), or inherit and measure? The `snapshot.fetchSize` fix is wanted
   either way.
3. **S3.** Pin 0/0 until there is a source-side byte bound (proposed), or add one now? A new
   `sdk.batch.bytes` parameter would be a public contract.
4. **Release vehicle and v1.** v1 is the default engine and gets the change too. Ship in v0.21.x
   with the v1 arms and v1 chaos coverage as prerequisites, or hold for v0.22 with the flip?
5. **Python SDK.** Match the default in the same cycle?
6. **Destination message size.** Is the funnel byte cap (separate PR) enough, or should the default
   `connectors.max-receive-record-size` also rise? A higher default helps standalone destinations
   built with old SDKs only if the engine passes the setting to them, which it does through
   `EnvConduitConnectorMaxReceiveRecordSize`.

## Related

- `docs/design-documents/20261017-funnel-destination-batching.md`
- `docs/design-documents/20261019-funnel-read-ahead-pipelining.md`
- `docs/design-documents/20261009-staged-batch-engine.md` (alternative D)
- `docs/design-documents/20260723-source-ack-persist-ordering-fix.md`
- `benchi/METHODOLOGY.md`; archv2-gate harness (#2956, #3067)
- Conduit #3213 (Postgres CDC default batch 1000); `conduit-connector-sdk` #428 (destination
  delay-only batching cap)

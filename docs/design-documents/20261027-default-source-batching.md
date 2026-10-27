# Default source batching (`sdk.batch.size=100`, `sdk.batch.delay=10ms`)

## Summary

Change the Go connector SDK's source batching defaults from `sdk.batch.size=0`, `sdk.batch.delay=0`
(one record per read) to `sdk.batch.size=100`, `sdk.batch.delay=10ms`, so a source that does not
configure batching hands the engine up to 100 records per read instead of one. The default lives in
`conduit-connector-sdk` (`SourceWithBatch` struct tags, emitted into each connector's generated
`connector.yaml`) and reaches users only when a connector is re-released against the new SDK. The
engine does not inject anything. Destination batching defaults do not change.

Arch-v2 is stop-and-wait: read, write, wait for the destination ack, ack the source, read again. At
one record per read, each record pays that whole round trip. On the archv2-gate harness, enabling
source batching at 100/10ms took arch-v2 1x1 generator→file from 52.6k to 214.9k rec/s (+308.7%,
resolved against a ±2.5% A/A floor). That number is a design input, not a performance claim; the
benchi plan below must confirm it at shipped defaults, on real connectors, before anyone quotes it.

This is Tier 1: it changes the default ack and position granularity, the crash-replay window and the
graceful-stop drain for every pipeline whose source does not set `sdk.batch.*`.

Roadmap: v0.21 Engine, "Adaptive batching by default. No throughput claim without a committed benchi
run"; and the v0.22 engine flip, which is gated on arch-v2 being "parity-or-faster at shipped
defaults".

## Context and problem

### What a default pipeline does today

With `sdk.batch.size` and `sdk.batch.delay` at 0, `SourceWithBatch.Open` (SDK v0.17.2) does not
start a collector; `ReadN` forwards to the connector, and the SDK's run loop calls `ReadN(ctx, 1)`
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

`conduit-connector-postgres` v0.17.1 (#3213) and later substitute an internal CDC batch of 1000 when
`sdk.batch.size` is 0 (`source/logrepl/combined.go`, `effectiveCDCBatchSize`). Its `NextN` returns
the whole handler batch even when asked for one record, so at shipped defaults Postgres CDC already
reaches the engine in batches of up to 1000, flushed by the handler at 1000 records or by a 1s
ticker. Two consequences matter here:

- An SDK default of 100 would _lower_ Postgres CDC from 1000 to 100 records per read, because
  `effectiveCDCBatchSize(100)` is 100.
- The snapshot fetch size is `sdk.batch.size` too (`combined.go`: `FetchSize: c.conf.BatchSize`,
  with 0 meaning 50000 in `snapshot/fetch_worker.go`). The documented `snapshot.fetchSize` parameter
  (default 50000) is parsed but never read. An SDK default of 100 would make every snapshot `FETCH
  100` instead of `FETCH 50000`.

So Postgres must pin its own defaults before it adopts the new SDK (see Decision).

### Evidence

| Run | Shape | Result | Control |
| --- | --- | --- | --- |
| archv2-gate session `matrix`, 2026-10-27T04:03Z, AWS (Xeon 8488C, 16 vCPU), 5 rounds, 60s window | 1x1 generator→file, arm `v2` vs `v2-alta` (`1x1-srcbatch100`: 100/10ms set explicitly) | 52,575 → 214,861 rec/s, +308.7%, every round +306.7% to +310.6% | A/A floor ±2.5%, resolved |
| Same session | `v1` default | 40,046 rec/s | ±2.0% |
| The notes, §5 sweep, main a23efb64, c7i.4xlarge, 3 interleaved rounds | 1x1 generator→file, source batch 1 / 10 / 100 / 1000 / 2048 | 54k / 137–150k / 221–235k / 227–256k / 225–247k rec/s | none (spread ≤3.5%, not A/A) |

The curve plateaus at about 100: 100 records gets ~91% of the gain of 1000. Past 100, the
connectors' per-record SDK work dominates. Neither run is committed to the repo, the gate image has
no source-revision label (binary sha256 `d5a91df6…`), and both use the two cheapest connectors there
are. Real sources (Kafka, S3) will gain less. We need committed results before claiming anything.

## Constraints

1. **Invariant 1: ack only after a durable downstream write.** Batching happens in the connector
   process, before the engine sees a record. The engine's ack path is unchanged: a batch is acked
   only after every destination confirmed it, and `Source.Ack` defers the plugin ack until the
   persister flushed the position (`pkg/connector/source.go`).
2. **Invariant 2: positions monotonic and crash-safe.** No position format changes. A batch carries
   its records' positions in source order and the persisted state is the last acked one.
3. **Invariant 3: at-least-once.** Records the SDK has read but not sent (read-ahead, a partly
   collected batch) are never acked, so a stop or crash replays them from the last persisted
   position. The cost is a larger replay window, which must be bounded and documented.
4. **Invariant 4: per-partition order.** The SDK collector appends in read order and sends one
   ordered slice. Nothing reorders.
5. **Invariant 7: graceful stop.** `Stop` must stay bounded: the SDK cancels the read context at once
   and discards what it has not sent, and the engine drains at most what is in flight.
6. **The engine cannot set the default for standalone connectors.** Older SDKs reject unknown
   parameters, and a connector's spec is its contract. A default the spec does not show is a
   default nobody can see in `conduit connectors describe`, the docs or the UI.
7. **Bounded memory and latency.** Both must have a hard bound stated per connector.
8. **No protocol change.** `conduit-connector-protocol` is untouched.

## How a source default reaches a pipeline

1. `SourceWithBatch` declares `BatchSize *int` with `default:"0"` and `BatchDelay *time.Duration`
   with `default:"0"` (`source_middleware.go`).
2. `conn-sdk-cli specgen` writes those defaults into the connector's `connector.yaml`. A connector
   that sets an initial value in `NewSource()` (for example
   `SourceWithBatch{BatchSize: lang.Ptr(1000)}`) overrides the tag default there (`overwriteDefaults`
   in `specgen/specgen.go`). Postgres already does this for schema extraction.
3. At runtime the SDK applies defaults from the **embedded spec**, not from the struct tags:
   `NewSourcePlugin(..., spec.SourceParams)` → `Util.ParseConfig` → `ApplyDefaults(params)`. The
   built-in registry does the same (`pkg/plugin/connector/builtin/registry.go`).

So bumping the SDK in Conduit's `go.mod` alone changes nothing for built-ins: their embedded
`connector.yaml` still says 0. A connector picks up the new default when it bumps the SDK and
regenerates its spec, which each connector's `validate-generated-files` CI forces in the same PR. A
standalone connector built with an older SDK keeps its old default forever.

## Alternatives considered

### A. Change the SDK's `SourceWithBatch` defaults (chosen)

Change the two struct tags in `conduit-connector-sdk`, release a minor version, and re-release the
connectors.

- **Pros:** the default is in the spec, so docs, `describe`, the UI and `llms.txt` all show the real
  value. It works for every Go connector that embeds `DefaultSourceMiddleware`, built-in or
  standalone. Connectors with different needs pin their own value through the existing
  initial-config mechanism. No engine or protocol change. Rollout is per connector release, so each
  connector's switch is a separate, revertible PR.
- **Cons:** it reaches users only through connector re-releases. It applies to both engines, v1
  included, since the SDK cannot know which engine runs it. Third-party connectors inherit it on
  their next SDK bump, possibly without reading the changelog.

### B. Engine injects `sdk.batch.*` for built-in sources

When a built-in source's settings omit `sdk.batch.size` and `sdk.batch.delay`, the arch-v2 service
adds 100/10ms before configuring the plugin.

- **Pros:** no SDK or connector release; can be limited to arch-v2; ships in one Conduit PR.
- **Cons:** the effective default contradicts the connector's published spec (it says 0, the engine
  runs 100), so the same connector behaves differently built-in and standalone. Standalone
  connectors cannot be covered at all, because older SDKs reject unknown parameters. It also gives
  the engine knowledge of one connector family's middleware, which is a privileged integration in
  all but name. Rejected: a default users cannot see is a support problem and an agent-legibility
  problem.

### C. Engine-side coalescing with no added latency

The `protorc` prototype: the destination writer takes whatever is already queued, writes it in one
`Write`, and waits for nothing. The notes, §4: +66% at 1x1 default (54.5k → 90.8k), +412% per sink
at 4x4, no added latency when the destination keeps up.

- **Pros:** zero added latency; works for every connector in every SDK and language.
- **Cons:** at 1x1 it reaches 90.8k, well short of the ~215k that source batching reaches. It depends
  on the async-ack machinery from #2978, which carries four close and failure bugs (the notes, §6)
  and needs a byte cap for standalone gRPC before it can land on main. It is a much larger data-path
  change. Not rejected, just not a substitute: it is complementary and belongs to the staged-engine
  work (`20261009-staged-batch-engine.md`).

### D. Per-connector defaults only, SDK unchanged

Each connector we own pins 100/10ms in `NewSource()`.

- **Pros:** no SDK default change; third-party connectors are unaffected until they choose.
- **Cons:** the one-record default remains a trap for every other Go connector and for anyone who
  scaffolds a new one. Each connector carries its own copy of the same number. Rejected as the
  primary mechanism; we still use it where a connector needs a different value (Postgres, S3).

### E. A larger default (1000)

- **Pros:** slightly more throughput (227–256k vs 221–235k in the uncontrolled sweep).
- **Cons:** 10x the memory, replay window and drain work for under 10% more throughput. Rejected.

## Decision

### SDK (`conduit-connector-sdk` v0.18.0)

- `SourceWithBatch`: `default:"100"` for `sdk.batch.size`, `default:"10ms"` for `sdk.batch.delay`.
  Destination tags stay at 0; arch-v2 handles destination batching in the funnel and strips these
  settings from destinations (`pkg/connector/destination.go`).
- **Bound the batch at `sdk.batch.size`.** Today `collectWithReadN` appends a whole `ReadN` result,
  so one batch can reach `2×size−1` records. With the limit as a default rather than an opt-in, it
  should mean what it says: the collector keeps the excess for the next batch.
- **Split the stream message by bytes.** For standalone connectors, `runRead` sends a whole batch in
  one `SourceRunResponse`. From code reading, the engine's gRPC client sets no
  `MaxCallRecvMsgSize`, so gRPC's 4 MiB default applies to that direction. Today only a single record
  over 4 MiB fails; at 100 records per message, any batch averaging over ~40 KiB per record would
  fail. `runRead` splits a batch into several messages under a byte budget, in order. The order
  holds and `lastPosition` is still the last record sent. This needs a test that confirms the limit
  before and after the change, with no new parameter (see Open questions).
- **Log the effective batching at `Open`** (one INFO line: size, delay, `ReadN` or `Read` path). The
  existing "size set but delay not" warnings stay.
- Opt-out is `sdk.batch.size: 0` **and** `sdk.batch.delay: 0`, which restores today's code path
  exactly. Setting only one of them to 0 keeps batching on: size 0 with delay 10ms is delay-only
  batching with no record cap. The docs must say this plainly.

### Engine (Conduit)

- **Persister threshold counts positions, not `Ack` calls.** `DefaultPersisterBundleCountThreshold`
  (10000) counts `Persist` calls, and arch-v2 makes one per batch. At 100 records per batch, the
  count-based flush effectively stops firing and the 1s delay threshold takes over, which raises the
  crash-replay window from ~10k records to up to one second of throughput (see Costs). Counting
  `len(p)` in `Persister.Persist` keeps today's record-count bound. v1 acks one position per call,
  so it is unaffected.
- **Metrics** (see Observability).
- No other engine change. The engine already accepts multi-record reads on both engines.

### Connectors

Every built-in source re-releases on SDK v0.18.0 with a regenerated `connector.yaml`. Then Conduit
bumps each one in its own PR, and that PR is the switch for that connector.

| Connector | Source read path | Decision |
| --- | --- | --- |
| postgres | `ReadN` (CDC handler batches, snapshot `FETCH`) | Pin `sdk.batch.size: 0` and `sdk.batch.delay: 0` in `NewSource()` (non-nil pointer initial values, which specgen writes into `connector.yaml`). That keeps today's path exactly: CDC at its internal 1000 with the 1s handler flush, snapshot `FETCH 50000`. Separately, wire `snapshot.fetchSize` into the snapshot iterator (a dead parameter today) with a regression test. Pinning 1000/10ms instead would add up to 10ms to partial handler batches with no measured gain. |
| kafka | `Read` (franz-go fetches, FIFO `batchAcker`) | Inherit 100/10ms. |
| file | `Read` (line follower) | Inherit 100/10ms. |
| generator | `Read` (optional rate limiter) | Inherit 100/10ms. |
| s3 | `Read` (one record per object, whole object in memory via `io.ReadAll`) | Do not inherit until there is a byte bound. Pin both to 0, today's behavior (see Open questions). |
| log | destination only | Nothing to do beyond the SDK bump. |

## Costs and bounds

All bounds are per source. `S` is `sdk.batch.size` (100) and `D` is `sdk.batch.delay` (10ms).

### Latency

The collector starts its timer at the first record of a batch and returns at `S` records or after
`D`, whichever comes first. A source producing fewer than `S` records per `D` (under 10k rec/s)
adds up to `D` (10ms) to the first record of each batch, and less to later records. A dense source
fills the batch first and adds nothing measurable. Postgres CDC is unchanged because it pins today's
settings; its handler already holds sparse changes for up to 1s, a pre-existing floor this design
does not touch.

`conduit_pipeline_execution_duration_seconds` starts when the engine receives a record, so it will
**not** show this delay. See Observability.

### Memory

For a `Read` connector the SDK holds up to `S` records in `readCh`, up to `S` in the batch being
collected, and one in the connector's `Read`. For a `ReadN` connector it holds one finished batch in
`readNCh` and the batch being collected. With the cap fix above, that is at most about `2S+1`
records buffered in the connector process, plus the batch the engine is processing. Arch-v2 without
destination batching has one batch in flight, so the total is about `3S` records. At 64-byte
payloads, about 2 KB per record with positions and metadata, that is about 600 KB per source.
Records with large payloads scale linearly. That is why S3, where a record is a whole object, does
not inherit the default.

### Crash replay (duplicates)

At-least-once replays everything after the last durably persisted position. Today the persister
flushes after 1s or 10000 `Persist` calls. At batch 1 and 54k rec/s, that means a flush every
~185ms and up to ~10k replayed records. At batch 100 and ~215k rec/s, 10000 calls is 1M records, so
the 1s timer decides and up to ~215k records replay. **With the persister counting positions**
(Decision), the bound is again ~10k records, plus one in-flight batch (`S`) and anything the funnel
destination buffers when destination batching is on. Records the SDK read ahead but never sent are
re-read, not duplicated: they never reached a destination.

### Graceful stop

`Stop` cancels the SDK's read context at once. The collector returns `ctx.Err()`, and read-ahead and
partly collected records are dropped unsent and unacked, so they replay on restart. `LastPosition`
is the last position actually sent, which the engine already waits for. The engine then drains what
is in flight: one batch for arch-v2 stop-and-wait. Drain time grows from one record's `Write` and
ack to one batch's. For a destination that writes records one at a time with real latency (the
matrix's `W-pg-http-slowsink`, 5ms per request), that is about 500ms. The bounded-stop work (#2990)
must budget for it.

### Ack and position granularity

Arch-v2 calls `Source.Ack` once per batch with all positions. The state position becomes the last
one, `Persist` is called once, and the plugin receives one `AckPositions` message. The SDK then calls
the connector's `Ack` once per position, in order (`runAck`), so connectors see the same per-record
acks as today:

- **postgres:** acks per position, advancing `confirmed_flush_lsn`. A batch can split a transaction.
  That is already true at today's 1000, and exact resume uses the position's change key.
- **kafka:** `batchAcker` commits in FIFO order. Read-ahead adds up to ~`2S` consumed-but-unacked
  records, so a consumer-group rebalance can redeliver up to that many more to the new owner
  (at-least-once).
- **file, s3, generator:** `Ack` is a no-op; the engine's persisted position is the resume point.

A nack inside a batch (a processor or destination error routed to the DLQ) goes through the existing
arch-v2 sub-batch and `multiAckNacker` path. That path exists and has tests
(`worker_nack_position_test.go`, `multi_ack_nacker_test.go`, `worker_ack_order_test.go`), but after
this change it is the path every default pipeline takes, not an opt-in one. v1 acks each record
separately (`pkg/lifecycle/stream/source_acker.go`), so its ack and persist pattern does not change.

## Failure modes

- **SIGKILL while the SDK is collecting a batch.** Nothing in the batch was sent, so nothing was
  acked. Restart resumes from the last persisted position. No loss. New chaos scenario required.
- **SIGKILL after the destination wrote a batch but before the position persisted.** The batch
  replays, along with everything else since the last flush. The bound is ~10k records with the
  persister fix and up to 1s of throughput without it. New chaos scenario required, asserting the
  bound and not only absence of loss.
- **SIGTERM with a partial batch.** Dropped unsent, then replayed on restart. The engine drains one
  in-flight batch. The existing `sigterm_test.go` and drain tests must run with a batched source.
- **Standalone source batch over the gRPC message limit.** Without the byte split, `Send` fails and
  the source errors on every restart, a stall that cannot clear by itself. The SDK byte split
  prevents it. A test with records just under and over 4 MiB per batch pins it. A runbook entry
  covers the symptom.
- **Large records exhaust memory.** Bounded at about `3S` records per source. S3 does not inherit
  the default. Large-payload matrix cells (`b16384`) measure RSS.
- **Postgres adopts the SDK without pinning.** Its CDC batch drops to 100 and its snapshot to `FETCH
  100`. A Postgres unit test asserts the effective CDC batch size and snapshot fetch size at shipped
  defaults, and the `F-*-snapshot` cells would show the regression.
- **A connector bumps the SDK but not its spec.** The embedded spec still says 0 and nothing changes.
  That is safe, and `validate-generated-files` catches it.
- **A user set only one of the two settings.** Today, `sdk.batch.size: 1000` alone waits until the
  batch is full, with no deadline. After this change the 10ms delay default applies, which is a
  fix. Today, `sdk.batch.delay: 1s` alone means no record cap. After this change the cap is 100.
  The release notes must call this out.
- **A user explicitly set `sdk.batch.size: 0` to get one record per read.** They now get delay-only
  batching. The release notes and docs must give the full opt-out (both 0).

## Upgrade, rollback and versioning

- **SDK:** v0.17.x → v0.18.0, a minor bump for a behavior change in a default. The changelog states
  the new values, the opt-out, and the one-setting cases above.
- **Connectors:** each built-in connector gets a minor release on SDK v0.18.0. Conduit bumps each one
  in a separate PR, which is that connector's switch.
- **Standalone connectors built with older SDKs:** unaffected; their embedded spec keeps 0.
  Standalone connectors adopt it on their own next SDK bump.
- **Other SDKs:** the Python SDK mirrors the Go defaults (`_batch.py`, 0 and 0). Matching it is an
  open question, under the parity policy (`20261008-sdk-parity-and-open-plugin-protocol.md`).
- **Stored config:** unchanged. Pipelines store only the settings the user set; defaults apply at
  `Configure`. An existing pipeline without `sdk.batch.*` switches when Conduit upgrades to a
  release with the re-released connector.
- **Serialized state:** no position, state or checkpoint format changes, so upgrade and downgrade
  read the same positions. An upgrade test (run N at batch 1, stop mid-stream, upgrade, resume at
  batch 100, verify no loss and duplicates within the bound) is still required. The upgrade gate is
  not live yet ("by Phase 2"), so the test is part of this work, not a gate check.
- **Rollback:** per pipeline, set both settings to 0. Per release, revert the connector bump PR in
  Conduit. No migration either way.

## Observability

- **New:** `conduit_source_read_batch_size` (histogram, labels: pipeline, plugin, component ID),
  recorded by the engine per `Source.Read`. It shows whether batching is on and what size batches
  actually arrive. It is engine-side, so it works for any connector and SDK.
- **New:** `conduit_source_batch_assembly_seconds` (histogram, same labels): at receipt, now minus
  the earliest `opencdc.readAt` in the batch. The SDK stamps `readAt` when the connector builds the
  record, before collection, so this is the latency that batching adds. Existing pipeline latency
  metrics cannot see it.
- **Existing:** `conduit_connector_execution_duration_seconds` for the source times the engine's
  `Read`, which should include the collector wait; the assembly metric is the direct measure. The
  SDK `Open` log line above shows the effective settings.
- **Runbook:** `docs/operations/source-batching.md`: symptom → diagnosis → remediation for higher
  sparse-source latency (assembly metric, then lower `sdk.batch.delay` or opt out), memory growth
  with large records, and the standalone gRPC limit.

## Test plan

- **SDK:** unit tests for the new defaults, the size cap (a `ReadN` result larger than the remaining
  room is carried over, in order), the byte split (order, `lastPosition`, a single record over the
  budget is still sent alone), and the opt-out path being byte-for-byte today's passthrough.
- **Postgres:** effective CDC batch size and snapshot fetch size at shipped defaults;
  `snapshot.fetchSize` honored (fails before the fix).
- **Conduit chaos (`tests/chaos`, required check):** batched-source variants of the SIGKILL recovery,
  SIGTERM drain and N-source scenarios on arch-v2 and on v1, since v1 is still the default engine
  until the v0.22 flip. Each kill test asserts no loss, ordering, and duplicates within the computed
  bound.
- **Persister:** a unit test that 10000 positions in 100 calls trigger the count flush (fails before
  the fix).
- **Upgrade:** the resume test above.
- **Acceptance:** run each connector's acceptance suite at shipped defaults, so SRC-02 (1, 100 and
  `MaxBatchSize` records written and read back) goes through the batched read path.

## Benchmark plan

No performance number goes into release notes, docs or the README until this plan's results are
committed in `benchi/` with configs and environment. Report medians with variance and the A/A floor,
on quiesced AWS hosts.

1. **archv2-gate harness** (`benchi/archv2-gate`, PR #3067, not on main yet). Arms `v1`, `v2`,
   `v1-srcbatch`, `v2-srcbatch` at 1x1, 2x2 and 4x4, plus a **shipped-default** arm built with the
   re-released generator and no `sdk.batch.*` in the pipeline file. That arm must land within the
   A/A floor of the explicit `1x1-srcbatch100` arm, or the default is not reaching the engine. On
   1x1, sweep size {10, 100, 1000} at 10ms and delay {1ms, 5ms, 10ms} at 100. Fix the missing
   source-revision label on the image first.
2. **Competitor matrix** (`benchi/v021-cdc-parity` and the matrix plan, whose cell definitions are
   not committed yet; commit them with the results). The `conduit-s` (shipped-default) arm is the
   one under test:
   - Sources that change: `B-kafka-file-p1-b64`, `B-kafka-pg-p1-b64`, `B-kafka-s3-p1-b64`,
     `C-file-kafka-b64`, and their `p4`/`p16` holdouts.
   - Postgres as control (pinned to today's settings, so it should land within the floor):
     `A2-pg-file-p1-b64`, `A2-pg-kafka-p1-b64`, `A2-pg-pg-p1-b64`.
   - Snapshot regression check: `F-pg-kafka-snapshot`, `F-pg-pg-snapshot`.
   - Latency: `W-pg-kafka-idle` and the `A1-*-ramp` cells, end-to-end p50/p99, plus a sparse
     kafka→file run (under 1k rec/s), where the 10ms cost actually shows.
   - Large records and memory: the `b16384` holdouts, recording RSS.
   - Recovery and correctness: `G-pg-kafka-kill9`, `G-pg-pg-kill9`, and every `H-*-verify` cell with
     a kafka or file source, checking replayed records against the computed bound.
   - A standalone-kafka arm on one B cell to exercise the gRPC byte split.
3. **Pass criteria:** the B and C cells improve beyond the A/A floor at shipped defaults. No cell
   regresses by more than its floor. Added p99 latency on sparse cells is at most `D`. Kill9 replay
   counts are within the bound. The snapshot cells do not move.

`make bench-engine` (the CI regression gate) uses fake connectors that bypass SDK specs, so this
change does not move it. Its `1x1-srcbatch100` shape keeps tracking the engine's batched path.

## Docs

These ship in the same PRs as the code:

- The SDK `SourceWithBatch` godoc and the SDK README.
- The regenerated connector references.
- The batching page in the `conduit.io` docs source: defaults, opt-out, the one-setting cases and
  the per-connector table.
- `llms.txt`: one entry on the source batching default and the opt-out.
- The runbook above.
- Changelog entries in the SDK, in each connector, and in Conduit's release notes for the release
  that bumps the connectors.

## Open questions for DeVaris

1. **Values.** Is 100/10ms right, or should the delay be lower (1–5ms) to keep sparse sources
   closer to today's latency? The delay sweep in step 1 should decide. This design defaults to
   10ms until the sweep says otherwise.
2. **Postgres.** Pin 0/0 to keep today's CDC and snapshot behavior exactly (proposed), pin
   1000/10ms, or inherit 100/10ms and measure? The `snapshot.fetchSize` wiring fix is wanted either
   way, and is required before Postgres ever inherits a non-zero size.
3. **S3.** Pin 0/0 until there is a byte bound (proposed), or add a byte bound now? A new
   `sdk.batch.bytes` parameter would be a public contract on every connector.
4. **Persister.** Count positions instead of `Ack` calls in this change (proposed), or accept a 1s
   replay window at high throughput?
5. **Release vehicle and v1.** Under Alternative A, v1 users get the default as soon as the
   connectors are bumped. Ship it in a v0.21.x patch (v1 chaos coverage becomes a prerequisite), or
   hold it for v0.22 with the engine flip?
6. **Python and other SDKs.** Match the default in the Python SDK in the same release cycle, or treat
   it as a separate decision?
7. **Standalone byte split.** Split inside the SDK with no new parameter (proposed), or raise the
   engine's gRPC receive limit as well? The second is a Conduit-side change that old SDKs would
   benefit from too.

## Related

- `docs/design-documents/20261017-funnel-destination-batching.md`: destination batching in the funnel;
  destination defaults stay as they are.
- `docs/design-documents/20261019-funnel-read-ahead-pipelining.md`
- `docs/design-documents/20261009-staged-batch-engine.md`: the staged engine and the coalescing
  alternative (C).
- `docs/design-documents/20260723-source-ack-persist-ordering-fix.md`: why `Source.Ack` defers the
  plugin ack until the persist lands.
- `benchi/METHODOLOGY.md`; archv2-gate harness (#2956, #3067).
- Conduit #3213 (Postgres CDC default batch 1000); `conduit-connector-sdk` #428 (destination
  delay-only batching).

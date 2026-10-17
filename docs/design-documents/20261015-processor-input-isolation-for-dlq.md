# Processor input isolation: what the DLQ receives when a processor fails after editing its input

## Summary

When a processor edits the record it was given and then errors, the record that is nacked and written
to the DLQ is the half-edited input, not the record as it arrived (#3153). The confirmed case is
`custom.javascript`; reading shows the same hazard class in other built-ins that edit shared maps in
place (`field.exclude`, `field.rename`). Replaying the DLQ through the same pipeline applies the
transform to already-transformed data.

The fix is **input isolation**: hand every processor a deep copy of its input record, so a processor
that mutates-then-errors can never corrupt the record the engine holds — the record other branches and
the DLQ see. The copy is taken at the **processor invocation boundary** (`RunnableProcessor.Process`,
`pkg/processor/runnable_processor.go`), **unconditionally**, using `opencdc.Record.Clone()`.

Nothing here is implemented. This doc settles the design; the implementation PR follows.

## Context

`JavascriptProcessor.toJSRecord` (`pkg/plugin/processor/builtin/impl/custom/javascript.go`) wraps the
record's live `map[string]any` payload and `Metadata` with goja. goja wraps Go maps and slices without
copying, so a script that writes `r.Payload.After.nested.x = ...` writes into the engine's record. This
has been the case since v0.9.0 (#1378).

On error the processor returns an `sdk.ErrorRecord`, which carries only the error, not a record. The
engine nacks the record it already holds, which is the same object the script just mutated:

- v1: `stream/processor.go` (`msg.Nack`) then `stream/dlq.go:197` writes `msg.Record.Map()`.
- arch v2: `funnel/processor.go` (`b.Nack`) then `funnel/dlq.go:150` writes `r.Map()` from the batch.

Repro: `TestAudit_JSErrorLeavesMutatedInputForDLQ` (attached to #3153). After the failure the input has
`nested.x = "half-written"` and `Metadata.touched = "yes"`, and that record is what the DLQ stores.

When this matters. The default DLQ is `builtin:log` with `window-size: 1` and `nack-threshold: 0`, so
by default the first nack halts the pipeline. Halt does not corrupt anything durable: the source
position was never acked, so the source redelivers the original record after restart. The hazard needs
a DLQ with `nack-threshold > 0` (the operator chose to keep going) and then a replay of the DLQ. In
that configuration the DLQ is the only durable copy of the failed record's content, so its fidelity is
the point.

Invariants touched: 6 (schema handling never silently mangles data; a half-transformed record in the
DLQ is mangled data nobody can tell from the original) and 3 (the DLQ is the delivery path for records
we could not deliver; it has to hold the record we were given). Invariants 1, 2, 4, 5, 7 are not
affected: no ack, position, ordering, state or shutdown logic changes under any option.

Same hazard class elsewhere. Any built-in that edits shared maps in place and can fail midway has it.
Confirmed by reading, not yet by test: `field.exclude` and `field.rename` loop over several references
and return an `ErrorRecord` on the first failure, after earlier references were already deleted or
renamed in the shared map. The boundary clone fixes the whole class at once, for every built-in, now
and for any future one. Standalone (WASM) processors never had the bug: records cross their boundary
serialized, which is a copy by construction.

### Related and out of scope

- conduit-commons#338 landed in released v0.9.x: `opencdc.Record.Clone` / `StructuredData.Clone` is now
  a correct, type-preserving deep copy (maps, `[]any`, typed slices, `RawData`, `[]byte`). This is the
  isolation mechanism; no new copier is written in this repo.
- `custom.javascript` runtime pooling and global state (`TestAudit_JSGlobalsPerPooledRuntime`) is a
  separate finding.

## Constraints

- Hot path. A copy per record costs allocations and GC. Claims need benchi runs (see _Benchmark plan_);
  the benchi regression gate is not currently a blocking CI check, so the cost is measured, not
  auto-gated.
- Correctness must not depend on a DLQ being configured: a pipeline's DLQ is edited independently of
  its processors, and the aliasing also corrupts successful sibling records after fan-out (the
  `clone` -> JS case in the audit).
- No change to the DLQ record format, the processor SDK, or the plugin protocol.
- The copy must be deep and type-preserving so downstream code that switches on concrete types
  (`StructuredData`, `[]any`, `map[string]any`) keeps working. That is what commons v0.9.x `Clone`
  provides; the v0.8.x `StructuredData.Clone` retyped nested maps and is not usable.
- The fix must not make the failure path able to lose a record. `Clone` has no error return and cannot
  drop a record; its only failure mode is a panic on a cyclic payload, which decoded record data
  (protobuf/JSON/Avro) cannot be.

## Options

### A. Deep copy before each processor call, only when a DLQ is configured

The processor node (v1) and `ProcessorTask` (arch v2) snapshot the input records before calling
`Process`, and nack with the snapshot instead of the live record.

- Fixes the whole class, but only when the operator turned the DLQ on.
- Behaviour and performance now depend on a DLQ knob the operator changes independently of the
  processors. A pipeline moving from halt to DLQ changes its correctness and its profile. Hard to
  reason about and hard to test.
- Does not fix the success-path aliasing (JS output sharing maps with sibling clones after fan-out).
- Touches both engines' data path plus both DLQ writers, so it has the biggest blast radius.
- Lost for the DLQ-dependence alone; that violates the "correctness must not depend on a DLQ being
  configured" constraint.

### B. Copy into JS: `custom.javascript` passes records by copy

`toJSRecord` builds the goja values from a private deep copy of the structured data and metadata.

- Fixes JS only. Leaves the already-known `field.exclude`/`field.rename` offenders open, and reopens the
  class every time a built-in is added that edits before failing.
- Costs one deep copy per record for JS pipelines — the same copy the boundary clone pays, but scoped to
  one processor, so there is no perf win over the chosen option.
- Needs a deep, type-preserving copier, either the commons `Clone` (v0.9.x, but not on a v0.20.x patch
  branch) or a local copier.
- Lost because it fixes one instance of a class the boundary clone fixes once, for the same cost.

### C. Lazy copy-on-first-write, JS path only

Hand goja the original through a `DynamicObject`/`DynamicArray` layer that reads through to the original
and copies the touched container on first write.

- Best cost profile for read-mostly scripts on big records, which is the common shape of filter-like and
  routing scripts.
- Dynamic objects are slower on every access than the reflection wrappers goja uses today, and they
  change observable behaviour: `instanceof`, `JSON.stringify`, `Object.keys` ordering, array methods and
  `typeof` on wrapped values all have to be re-verified. This is a semantics project, not a patch.
- Lost: not a patch, and only worth it if the chosen option's measured cost on large records forces it.

### D. Unconditional deep copy at the processor invocation boundary (chosen)

Clone each input record in `RunnableProcessor.Process` before invoking the plugin, and pass the clones
to the plugin. The engine's record is never handed to a processor.

`RunnableProcessor` is the single choke point: both engines wrap every plugin — built-in and standalone
— in a `RunnableProcessor` (`pkg/lifecycle/service.go` and `pkg/lifecycle-poc/service.go` both call
`MakeRunnableProcessor`). The engine nacks, and the DLQ writes, the record it already holds
(`msg.Record` / `b.records[i]`), which is never touched, so the DLQ sees the original by construction
with no change to either DLQ writer.

- Fixes the whole class — built-in and future built-in alike — success and failure paths, with or
  without a DLQ, in both engines, with one function.
- No engine, SDK, protocol or DLQ-format change.
- Cost: one deep copy per record per processor invocation, for every processor. Raw payloads are
  `[]byte` (a memcpy); see _Record shapes_ for the cost on each shape.
- One known waste: WASM processors already serialize (an implicit copy), so they pay a second copy. It
  is accepted for uniformity now and can be removed later (skip the clone for processors that cross a
  serialization boundary) if a benchi run shows it matters.

### Also considered

- **Undo journal in the JS processor** (record writes, roll back on error): same wrapping work as C
  with a rollback step on top and no read-side savings. Rejected.
- **Make `ErrorRecord` carry the original** and have the engine nack that: the processor SDK would have
  to change, and a processor holding the original has the same copy problem. Rejected.
- **Document it and tell users to write idempotent scripts**: does not fix mangled DLQ content and puts
  the burden where nobody sees it. Rejected.
- **Gate the copy behind a "this processor mutates" flag**: the set of mutating processors is already
  known to be wrong (JS plus `field.exclude`/`rename`), and maintaining it to uphold invariant 6 is a
  latent bug. Rejected for the same reason A is.

### Comparison

| | A: snapshot if DLQ | B: JS copy-in | C: JS lazy COW | D: boundary clone |
| --- | --- | --- | --- | --- |
| Fixes DLQ fidelity for JS | only with DLQ | yes | yes | yes |
| Fixes JS success-path aliasing | no | yes | yes | yes |
| Fixes other built-ins | yes, only with DLQ | no | no | yes, always |
| Cost for read-only / non-JS pipelines | copy if DLQ set | none | none | copy always |
| Cost for JS, read-only script | copy | copy | none | copy |
| Implementation points | 2 engines + 2 DLQ writers | 1 file | 1 file | 1 file |
| Depends on knowing which processors mutate | no | n/a (only JS) | n/a | no |
| JS semantics risk | none | none | high | none |
| v0.20.x patch viable | no | yes (local copier) | no | yes (local copier) |

## Decision

Option D: clone the input records unconditionally at the processor invocation boundary
(`RunnableProcessor.Process`), and pass the clones to the plugin. The engine's record is never handed to
a processor.

The open question this doc settles is **where** to clone. The answer is the single choke point through
which every processor in both engines is invoked, not inside any one processor (B), not gated on DLQ
configuration (A), and not a lazy COW layer (C). The mechanism is `record.Clone()` from conduit-commons
v0.9.x — a correct deep, type-preserving copy — so no copier is written in this repo.

The built-ins with partial-edit paths (`field.exclude`, `field.rename`) are fixed by the same change and
need no per-processor work. The property test below locks that in.

### What the DLQ receives

Failing script, DLQ with threshold above 0, record `{nested: {x: 1}}`, script sets `nested.x = "half-written"` then throws.

| | DLQ `After` payload | Metadata edits visible | Replay safe |
| --- | --- | --- | --- |
| Today | `{nested: {x: "half-written"}}` | yes | no |
| D (and A/B/C) | `{nested: {x: 1}}` (input never touched) | no | yes |

The DLQ record shape (`After` holds `record.Map()`, plus `conduit.dlq.nack.*` metadata) is unchanged.

Batches in arch v2: `custom.javascript` returns on the first failing record, with outputs for the
records before it and nothing after; the funnel retries the rest. Under D none of the inputs, before or
after the failure, is modified, so a retried record starts from its original content.

## Record shapes

- **Error records.** `sdk.ErrorRecord` is a result, not an input; the input is always an
  `opencdc.Record`. The engine nacks its own (now-isolated) record, so the DLQ gets the original. The
  error path itself is unchanged.
- **Tombstones.** `Payload.Before`/`After` are nil; `Clone` returns nil for nil `Data`, so the copy is
  trivial (metadata and position only).
- **Raw-data payloads.** `RawData` is `[]byte`; `Clone` is a `bytes.Clone`, a memcpy. This is the cheap
  case.
- **Large structured payloads.** `StructuredData.Clone` deep-copies every container. Cost is
  proportional to record size, transient (the clone lives only for the duration of the call), and
  bounded by batch size / in-flight records. Acceptable for now: the benchi gate is not a blocking CI
  check, raw payloads are cheap, and the one redundant case (WASM) is documented above and removable.
  The benchmark plan measures this before any claim is made.

## Failure modes

| Failure | Today | D |
| --- | --- | --- |
| Processor throws after editing | DLQ gets half-edited record | DLQ gets the original |
| Processor returns a wrong type | same as above | same fix: input untouched |
| Processor keeps a reference to its record in a global (`var last = r`) | engine record can be edited later, by a later call | processor holds its own copy; the engine record is not reachable |
| Large record (MBs) | no extra memory | transient 2x for the record; peak memory = in-flight records x record size more, bounded by batch size |
| `Clone` panics on a cyclic payload | n/a | decoded record data (protobuf/JSON/Avro) cannot be cyclic; a processor that builds a cyclic output and feeds it back is already outside the supported shape |
| Copy shares a nested container with the original | n/a | commons v0.9.x `Clone` is deep; property test below locks the "mutate every reachable container, original unchanged" guarantee |
| Crash mid-processor | no effect | no effect: the copy is in memory only, the source position is acked only after the destination acked (invariant 1) |
| `kill -9` | no effect | no effect, same reason |
| Output record aliases input | yes | no: output is built from the copy; downstream sees a record that shares nothing with the input |
| Duplicate delivery on restart | source redelivers originals | same |
| Disk full / network | n/a | n/a, no I/O added |

## Upgrade, rollback, compatibility

- No serialized format, protocol, config schema or DLQ shape changes. No migration. No error code or
  CLI surface changes.
- Dependency: `conduit-commons` is bumped to v0.9.x to get the deep `Clone`. Its `Record.Map()` field
  names are unchanged (`key`/`payload`/`before`/`after`), so DLQ and inspector output is byte-identical.
- Behaviour change, called out in release notes: after the fix, a failing processor's DLQ entry holds
  the original record. DLQ entries written by earlier versions are not marked and can still hold
  half-edited content; operators who replay old DLQ entries from JS pipelines should compare against the
  source. There is no reliable way to detect affected entries after the fact.
- Scripts that relied on mutating the input and returning the mutated record keep working: the returned
  record is read from the script's copy. Scripts that relied on an in-place edit being visible to the
  engine without being returned stop working — that was never the contract (`custom.javascript` returns
  the record; a script that mutates and returns `undefined`/`null` is a filter), so we expect none;
  verify against `examples/` and the docs before merging.
- Rollback: revert the one change and the commons bump. Performance returns to today's, DLQ fidelity too.
- v0.20.x: backport as a patch (v0.20.3 or later). That branch does not have commons v0.9.x, so the
  backport carries a local deep copier with the same shape as the split fix in #3154; the boundary is
  the same one function.

## Observability

- The existing per-processor execution timers show the added cost; compare before and after on the same
  pipeline.
- No new metric for a copy. If a benchi run shows a problem, add a trace log with a byte estimate then.
- Runbook entry under `docs/operations/`: "DLQ entry from a processor error, how to tell what it
  contains" with the version split above.

## Benchmark plan (not run; no AWS)

Follows `benchi/METHODOLOGY.md`: ground-truth counts, 60s runs, A/A control, alternating runs, no claims
from engine metrics.

- **Pipeline:** `builtin:generator` (structured) to `custom.javascript` to `builtin:file`, all local, no
  external infrastructure, compose project name unique per run. Ground truth: count lines in the file
  destination.
- **Record sizes:** small (10 fields), medium (about 2 KB, nested objects and arrays), large (about 100
  KB, deep nesting). Report each separately; size is what scales the clone.
- **Scripts:** (1) read-only passthrough `return r`; (2) one-field write; (3) many writes. D pays the
  same copy for all three; keep them as a control.
- **Arms:** base (`main`), D. A/A: base against base for the noise floor. Alternate A and D runs, 5 pairs
  each size, report medians and spread. One WASM-arm run as a control for the redundant-copy note.
- **Gate:** 10% throughput regression bar. If D fails on large records, skip the clone for processors
  that cross a serialization boundary (WASM) before reaching for C.
- Committed under `benchi/js-input-isolation/` with the config, env spec, and raw CSVs when it is run.

## Tests

- Regression: `TestAudit_JSErrorLeavesMutatedInputForDLQ` promoted to a real test (fails on base, passes
  with the fix), plus the same through the real v1 and arch-v2 DLQ path with an in-memory DLQ
  destination: assert the DLQ record equals the input as it arrived.
- Boundary unit test: a `RunnableProcessor` whose plugin mutates a nested field then returns an
  `ErrorRecord`; assert the input slice handed to `Process` is unchanged.
- Property/fuzz: random nested records; plugin mutates every reachable container then throws; input is
  `DeepEqual` to a pre-call twin. Same for the success path (output shares nothing with input).
- Clone-then-JS test from the audit (`TestAudit_CloneThenJS_CrossRecordCorruption`) passes without
  depending on commons.
- Chaos: no new case needed (no ack or position change); state in the PR that the existing suite is
  unaffected and why.

## Risk tier and review

Tier 1: it changes what the DLQ receives, and so what an operator replays. Needs this doc approved,
human sign-off, and a fresh-context review of the implementation PR. This PR is docs only.

## Related

- #3153 (this issue), #3152 / #3154 (split nested fields, the sibling finding)
- conduit-commons#338 (`Clone` deep, type-preserving; released v0.9.x), #336 / #339 (Clone semantics)
- `docs/design-documents/20260723-source-ack-persist-ordering-fix.md` (ack-correctness context)
- `benchi/METHODOLOGY.md`

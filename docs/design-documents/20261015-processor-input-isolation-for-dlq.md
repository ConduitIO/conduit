# Processor input isolation: what the DLQ receives when a processor fails after editing its input

## Summary

When `custom.javascript` edits the record it was given and then throws, the record that is nacked and
written to the DLQ is the half-edited input, not the record as it arrived (#3153). Replaying the DLQ
through the same pipeline applies the transform to already-transformed data.

This doc compares three ways to fix it and recommends one for v0.21:

- **Pass records into JS by copy**, inside `custom.javascript` only. The input is never touched, so the
  nack path needs no change.
- Not now: copy-on-first-write inside the JS processor, as a follow-up if benchi shows the copy costs
  too much on large records.
- Not chosen: a deep copy before every processor call when a DLQ is configured.

Nothing here is implemented. A decision is needed from DeVaris on the points in _Open questions_.

## Context

`JavascriptProcessor.toJSRecord` (`pkg/plugin/processor/builtin/impl/custom/javascript.go`) wraps the
record's live `map[string]any` payload and `Metadata` with goja. goja wraps Go maps and slices without
copying, so a script that writes `r.Payload.After.nested.x = ...` writes into the engine's record. This
has been the case since v0.9.0 (#1378).

On error the processor returns an `sdk.ErrorRecord`. The engine does not carry the input in the error;
it nacks the record it already holds, which is the same object:

- v1: `stream/processor.go` (`msg.Nack`) then `stream/dlq.go:197` writes `msg.Record.Map()`.
- arch v2: `funnel/processor.go` (`b.Nack`) then `funnel/dlq.go:150` writes `r.Map()` from the batch.

Repro: `TestAudit_JSErrorLeavesMutatedInputForDLQ` (attached to #3153). After the failure the input has
`nested.x = "half-written"` and `Metadata.touched = "yes"`, and that record is what the DLQ stores.

When this matters. The default DLQ is `builtin:log` with `window-size: 1` and `nack-threshold: 0`, so
by default the first nack halts the pipeline. Halt does not corrupt anything durable: the source
position was never acked, so the source redelivers the original record after restart. The hazard needs
a DLQ with `nack-threshold > 0` (the operator chose to keep going) and then a replay of the DLQ. In
that configuration the DLQ is the only durable copy of the failed record's content, so its fidelity is
the point. (What the default `log` DLQ prints before the halt is a detail to check during
implementation; it does not change the recommendation.)

Invariants touched: 6 (schema handling never silently mangles data; a half-transformed record in the
DLQ is mangled data nobody can tell from the original) and 3 (the DLQ is the delivery path for records
we could not deliver; it has to hold the record we were given). Invariants 1, 2, 4, 5, 7 are not
affected: no ack, position, ordering, state or shutdown logic changes under any option.

Same hazard class elsewhere. Any built-in that edits shared maps in place and can fail midway has it.
Confirmed by reading, not yet by test: `field.exclude` and `field.rename` loop over several references
and return an `ErrorRecord` on the first failure, after earlier references were already deleted or
renamed in the shared map. Standalone (WASM) processors are not affected, because records cross the
boundary serialized, which is a copy by construction. See _Beyond JS_.

### Related and out of scope

- conduit-commons#336 / #338 / #339 (`Record.Clone` aliasing and retyping). Option B below needs a
  deep, type-preserving copy. Whether we get it from `Clone` depends on which of those has landed on
  the branch in question (main: #338 still open when this was written; v0.20.x: retyping variant).
- `custom.javascript` runtime pooling and global state (`TestAudit_JSGlobalsPerPooledRuntime`) is a
  separate finding.

## Constraints

- Hot path. A copy per record costs allocations and GC. Claims need benchi runs (see _Benchmark plan_).
- Correctness must not depend on a DLQ being configured: a pipeline's DLQ is edited independently of
  its processors, and the JS aliasing also corrupts successful sibling records after fan-out (the
  `clone` -> JS case in the audit).
- No change to the DLQ record format, the processor SDK, or the plugin protocol.
- Must work on release/v0.20.x for a patch release, where the commons `Clone` is not a deep copy.
- The fix must not make the failure path able to lose a record: any new copy step that fails has to
  surface as an `ErrorRecord`, never a panic and never a drop.

## Options

### A. Engine-level: deep copy before each processor call when a DLQ is configured

The processor node (v1) and `ProcessorTask` (arch v2) snapshot the input records before calling
`Process`, and nack with the snapshot instead of the live record.

- Fixes the whole class for every processor, built-in or not, without touching them.
- Cost lands on every processor in every pipeline with a DLQ threshold above 0, including processors
  that never mutate (`filter`, `error`, `unwrap` on a different record) and WASM processors that
  already got a serialized copy. Roughly one extra deep copy per record per processor, multiplied by
  chain length.
- Behaviour now depends on DLQ settings. A pipeline moving from halt to DLQ changes its performance
  profile and (without the copy) its correctness. Hard to reason about and hard to test.
- Does not fix the success-path aliasing (JS output sharing maps with sibling clones).
- Touches both engines' data path, so it is the biggest blast radius and needs two implementations.

### B. Copy into JS: `custom.javascript` passes records by copy

`toJSRecord` builds the goja values from a private deep copy of the structured data and metadata. The
engine's record is never visible to the script, so there is nothing to roll back and the nack path
stays exactly as it is.

- Fixes JS entirely, success and failure paths, with or without a DLQ.
- Scope is one file. No engine change, no SDK change, no change for non-JS pipelines.
- Cost: one deep copy of key/before/after/metadata per record, only for JS pipelines. Raw payloads are
  `[]byte`, copy is a memcpy. Passing a script a record it can mutate cheaply is the thing we are
  paying for.
- Needs a deep, type-preserving copier that is correct on both main and v0.20.x. Two ways: depend on
  the fixed commons `Clone`, or add a small copier local to the processor (the split fix in #3154
  already has the shape: maps, `StructuredData` and `[]any` rebuilt, everything else by value).
- Does not help other built-ins (see _Beyond JS_).

### C. Lazy copy-on-first-write, JS path only

Hand goja the original through a goja `DynamicObject`/`DynamicArray` layer that reads through to the
original and copies the touched container (and its parents) on the first write. Reads of untouched
fields cost nothing; a script that only reads pays no copy.

- Best cost profile for read-mostly scripts on big records, which is the common shape of filter-like
  and routing scripts.
- Dynamic objects are slower on every access than the reflection wrappers goja uses today, and they
  change observable behaviour: `instanceof`, `JSON.stringify`, `Object.keys` ordering, array methods
  and `typeof` on wrapped values all have to be re-verified. This is a semantics project, not a patch.
- Not suitable for a v0.20.x patch.
- Worth doing only if B's measured cost on large records fails the 10% gate.

### Also considered

- **Undo journal in the JS processor** (record writes, roll back on error): same wrapping work as C
  with a rollback step on top and no read-side savings. Rejected.
- **Make `ErrorRecord` carry the original** and have the engine nack that: the processor SDK would have
  to change, and a processor holding the original has the same copy problem. Rejected.
- **Document it and tell users to write idempotent scripts**: does not fix mangled DLQ content and
  puts the burden where nobody sees it. Rejected.

### Comparison

| | A: engine snapshot | B: JS copy-in | C: JS lazy COW |
| --- | --- | --- | --- |
| Fixes DLQ fidelity for JS | yes (only with DLQ) | yes | yes |
| Fixes JS success-path aliasing | no | yes | yes |
| Fixes other built-ins | yes | no | no |
| Cost for pipelines without JS | copy per processor call if DLQ is set | none | none |
| Cost for JS, read-only script | copy | copy | none |
| Engines to change | 2 (v1, arch v2) | 0 | 0 |
| JS semantics risk | none | none | high |
| v0.20.x patch viable | no | yes | no |

## Decision (proposed)

Option B for v0.21 and as the v0.20.x patch (backport), with its cost measured before release. C stays
on the shelf until a benchmark justifies it. A is not adopted.

The built-ins with partial-edit paths are fixed separately and individually (see below), because each
knows exactly what it mutates and can apply all-or-nothing without a generic copy.

### What the DLQ receives

Failing script, DLQ with threshold above 0, record `{nested: {x: 1}}`, script sets `nested.x = "half-written"` then throws.

| | DLQ `After` payload | Metadata edits visible | Replay safe |
| --- | --- | --- | --- |
| Today | `{nested: {x: "half-written"}}` | yes | no |
| A | `{nested: {x: 1}}` (snapshot) | no | yes |
| B | `{nested: {x: 1}}` (input never touched) | no | yes |
| C | `{nested: {x: 1}}` (input never touched) | no | yes |

The DLQ record shape (`After` holds `record.Map()`, plus `conduit.dlq.nack.*` metadata) is unchanged in
all options.

Batches in arch v2: `custom.javascript` returns on the first failing record, with outputs for the
records before it and nothing after; the funnel retries the rest. Under B none of the inputs, before or
after the failure, is modified, so a retried record starts from its original content.

## Failure modes

| Failure | Today | B |
| --- | --- | --- |
| Script throws after editing | DLQ gets half-edited record | DLQ gets the original |
| Script returns a wrong type | same as above | same fix: input untouched |
| Script keeps a reference to its record in a global (`var last = r`) | engine record can be edited later, by a later call | script holds its own copy; the engine record is not reachable |
| Large record (MBs) | no extra memory | transient 2x for the record; peak memory = in-flight records x record size more. Bounded by batch size |
| Copier panics on an unexpected value type (pointer, cyclic) | n/a | the copier must not panic: unknown types are copied by value, and a defensive recover turns any panic into an `ErrorRecord`. Test with fuzz on record shapes |
| Copier shares a nested container with the original (for example typed slices) | n/a | script can still mutate shared data. Property test: mutate every reachable container in the copy, original must be `DeepEqual` to a twin |
| Crash mid-processor | no effect | no effect: the copy is in memory only, the source position is acked only after the destination acked (invariant 1) |
| `kill -9` | no effect | no effect, same reason |
| Output record aliases input | yes | no: output is built from the copy. Downstream sees a record that shares nothing with the input |
| Duplicate delivery on restart | source redelivers originals | same |
| Disk full / network | n/a | n/a, no I/O added |

## Beyond JS: the same class in built-ins

Not part of the JS fix, listed so it is not lost:

1. Add a property test across built-in processors: for any input record, if `Process` returns an
   `ErrorRecord` the input record (compared to a deep copy taken before the call) is unchanged. This
   finds the offenders mechanically.
2. Fix offenders case by case. Expected: `field.exclude`, `field.rename` (multi-field loops). Each can
   resolve and validate all references first and mutate after, which costs no copy.
3. File what the property test finds; do not fold it into the JS change.

## Upgrade, rollback, compatibility

- No serialized format, protocol, config schema or DLQ shape changes. No migration.
- Behaviour change, called out in release notes: after the fix, a failing JS script's DLQ entry holds
  the original record. DLQ entries written by earlier versions are not marked and can still hold
  half-edited content; operators who replay old DLQ entries from JS pipelines should compare against
  the source. There is no reliable way to detect affected entries after the fact.
- Scripts that relied on mutating the input and returning something else keep working: the returned
  record is read from the script's copy. Scripts that relied on an in-place edit being visible to the
  engine without being returned would stop working. The `custom.javascript` contract is to return the
  record, and a script that mutates and returns `undefined`/`null` is a filter, so we expect none;
  verify against `examples/` and the docs before merging.
- Rollback: revert the one change. Performance returns to today's, DLQ fidelity too.
- v0.20.x: backport as a patch (v0.20.3 or later) once the copier is local to the processor rather than
  dependent on commons.

## Observability

- The existing per-processor execution timers already show the added cost; compare it
  before and after on the same pipeline.
- Add a trace log line with the copied byte estimate only if benchi shows a problem; a new metric is
  not warranted for a copy.
- Runbook entry under `docs/operations/`: "DLQ entry from a JS processor error, how to tell what it
  contains" with the version split above.

## Benchmark plan (not run; no AWS)

Follows `benchi/METHODOLOGY.md`: ground-truth counts, 60s runs, A/A control, alternating runs, no claims
from engine metrics.

- **Pipeline:** `builtin:generator` (structured) to `custom.javascript` to `builtin:file`, all local, no
  external infrastructure, compose project name unique per run. Ground truth: count lines in the file
  destination.
- **Record sizes:** small (10 fields), medium (about 2 KB, nested objects and arrays), large (about 100
  KB, deep nesting). Size matters most for B, so report each separately.
- **Scripts:** (1) read-only passthrough `return r`; (2) one-field write; (3) many writes. B pays the
  same copy for all three; C would differ, so keep them.
- **Arms:** base (`main`), B. A/A: base against base, to get the noise floor. Alternate A and B single
  runs, 5 pairs each size, report medians and spread.
- **Gate:** CLAUDE.md regression bar of 10% throughput. If B fails on large records, prototype C on
  that arm before deciding. If B passes, C is shelved.
- **Not claimed:** anything about arch v1 vs v2, or about non-JS pipelines (unchanged code path;
  one run only as a control that they did not move).
- Committed under `benchi/js-input-isolation/` with the config, env spec, and raw CSVs when it is run.

## Tests

- Regression: `TestAudit_JSErrorLeavesMutatedInputForDLQ` promoted to a real test (fails on base, passes
  with the fix), plus the same through the real v1 and arch-v2 DLQ path with an in-memory DLQ
  destination: assert the DLQ record equals the input as it arrived.
- Property/fuzz: random nested records; script mutates every reachable container then throws; input
  is `DeepEqual` to a pre-call twin. Same for the success path (output shares nothing with input).
- Clone-then-JS test from the audit (`TestAudit_CloneThenJS_CrossRecordCorruption`) passes without
  depending on commons.
- Chaos: no new case needed (no ack or position change); state in the PR that the existing suite is
  unaffected and why.

## Risk tier and review

Tier 1: it changes what the DLQ receives, and so what an operator replays. Needs this doc approved,
human sign-off, and a fresh-context review of the implementation PR. This PR is docs only.

## Open questions for DeVaris

1. Accept B (copy-in for JS only) as the v0.21 fix and the v0.20.x backport, with C only if benchi
   forces it?
2. Is it acceptable that DLQ entries written before the fix cannot be identified, or do we want a
   one-time warning in the release notes only?
3. Should the cross-built-in property test (and the `field.exclude`/`rename` fixes) ship in the same
   release, or be filed for v0.22?

## Related

- #3153 (this issue), #3152 / #3154 (split nested fields, the sibling finding)
- conduit-commons#336, #338, #339 (`Clone` semantics)
- `docs/design-documents/20260723-source-ack-persist-ordering-fix.md` (ack-correctness context)
- `benchi/METHODOLOGY.md`

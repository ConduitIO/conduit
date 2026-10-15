# Integer precision of structured data at the plugin boundary

## Summary

`opencdc.StructuredData` crosses every plugin boundary as a `google.protobuf.Struct`, and a `Struct`
number is a double. An integer that a double cannot hold exactly is rounded without an error. For
example, `int64(9007199254740993)` arrives in a standalone destination as `9007199254740992`, and a SQL
destination then deletes or overwrites the wrong row (#3100). This is invariant 6 (silent mangling) at
the protocol boundary.

This document covers two steps:

1. **v0.21: fail loudly.** `StructuredData.ToProto` in `conduit-commons` returns a typed error for any
   integer that a double cannot represent exactly. It no longer rounds. The wire format does not change.
   In Conduit the failed record is nacked with the stable code `plugin.integer_not_representable`, so the
   pipeline's DLQ policy applies: it halts by default, or the record goes to a configured DLQ. Plugins
   built against older commons still round on the plugin-to-Conduit leg, and this design says so instead
   of hiding it.
2. **Later (v0.22 or after): a lossless encoding.** The recommendation is a new structured value message
   with real integer kinds. It is used only when both sides negotiate it: through a new
   `conduit-connector-protocol` version for connectors, and through a capability flag for WASM
   processors. **This changes the connector protocol, so it is breaking-change territory.** It needs its
   own ADR and the explicit versioning discussion that CLAUDE.md requires. This document recommends a
   direction. It does not decide the wire format.

Step 1 is what DeVaris accepted for v0.21. Step 2 is a recommendation for a later decision.

## Context

### Where the rounding happens

`StructuredData.ToProto` (`conduit-commons/opencdc/proto.go`) calls `structpb.NewStruct(d)`.
`structpb.NewValue` (protobuf-go v1.36.12) converts `int`, `int8`–`int64`, `uint`, `uint8`–`uint64` and
`float32` with `float64(v)`, and it converts `json.Number` with `v.Float64()`. Every one of these
conversions rounds to the nearest double without reporting it. `FromProto` (`dataFromProto`, through
`Struct.AsMap`) returns every number as `float64`. A receiver cannot tell that a value was rounded,
because after rounding it is a valid double.

`structpb.NewValue` already rejects types it does not know (for example `time.Time`, `*big.Int`, typed
slices, and a nested `opencdc.StructuredData`) with `invalid type: %T`. A loud failure in this function
is therefore not new. Rounding is the gap.

### Every `ToProto` call site

A search of ConduitIO and conduitio-labs for `opencdcv1.Record` and `ToProto(`, excluding generated
code:

| Leg | Who calls `ToProto` | Which process, which commons version |
| --- | --- | --- |
| Conduit → standalone destination | `pconnector/v2/client.DestinationRunStream.Send` → `toproto.DestinationRunRequest` (v1 has the same shape) | Conduit; Conduit's commons |
| Standalone source → Conduit | `pconnector/v2/server.SourceRunStream.Send` → `toproto.SourceRunResponse`, called from the SDK's `runRead` | plugin; the plugin's commons |
| Conduit → WASM processor | `pkg/plugin/processor/standalone/proto.go` `protoConverter.records`, from `wasmProcessor.Process` | Conduit; Conduit's commons |
| WASM processor → Conduit | `conduit-processor-sdk/run.go` `protoConverter.singleRecord` and `multiRecord` | guest; the commons compiled into the `.wasm` |
| API inspector | `pkg/http/api/connector_v1.go` `InspectConnector`, `processor_v1.go` `InspectProcessorIn` and `InspectProcessorOut` | Conduit; Conduit's commons |
| Conduit-to-Conduit over gRPC | conduitio-labs `conduit-connector-grpc-client` `toproto/record.go` | plugin; the plugin's commons |

The built-in path does not use protobuf. Built-in connectors run in-process
(`pkg/plugin/connector/builtin`) and pass `opencdc.Record` values through `Clone()`. Built-in processors
also run in-process. A built-in-only pipeline is not affected by this issue, and step 1 does not change
its behavior.

### When the bug bites

The SDK's source middleware encodes structured keys and payloads as Avro by default
(`sdk.schema.extract.key.enabled` and `sdk.schema.extract.payload.enabled` both default to `true`). An
Avro-encoded record crosses the boundary as `RawData`, which is opaque bytes, so it is not rounded.
The destination middleware decodes it in the plugin process into an exact `int64`. The bug therefore
bites when a record crosses as `StructuredData`:

- the source has schema extraction disabled, or it emits a key or payload that the middleware does not
  encode;
- a processor leaves the data structured without a schema to re-encode it with;
- a record reaches a standalone DLQ destination: the DLQ record wraps the failed record as
  `StructuredData(msg.Record.Map())` (`pkg/lifecycle/stream/dlq.go`), so even an Avro record's
  structured fields cross as a `Struct` there.

### Precision loss that `ToProto` cannot see

Some paths round before any `ToProto` call. The value is already a `float64`, so step 1 cannot catch
them. They are out of scope here. They are listed so that nobody reads step 1 as a complete fix:

- The built-in `json.decode` processor (`impl/json/decode.go`) unmarshals into `any` without
  `UseNumber`, so a JSON integer above 2^53 becomes a rounded `float64` in-process.
- A standalone source built against older commons rounds inside the plugin, before Conduit receives
  the record.

Two neighbouring cases of `Struct` mangling are also out of scope here, but the lossless design in step 2
fixes them together: `[]byte` crosses as a base64 string and comes back as a `string`, and NaN and ±Inf
come back as the strings `"NaN"`, `"Infinity"` and `"-Infinity"`.

## Constraints

- Invariant 6: no silent coercion or truncation. Rounding an integer key is the worst case, because the
  wrong row changes.
- Invariants 1 and 3: a record that cannot be encoded must not be acked upstream. It must be delivered to
  a DLQ, or the pipeline must stop.
- Invariant 4: refusing one record must not reorder the records that are delivered.
- `conduit-connector-protocol` is a public contract. The v0.21 step must not change the wire format.
- `conduit-commons` is a shared library. Each plugin binary carries the commons version it was built with,
  and Conduit cannot change it.
- Error codes are API (`conduiterr`). Commons cannot import Conduit's `conduiterr`, so the code is
  attached at Conduit's boundary.
- Error messages must not carry record values. A wide integer can be an account or card number.
- `build (linux/386)` is a required check. `int` is 32 bits there and always fits in a double.

## Decision, step 1 (v0.21): refuse to round

### The rule

An integer is accepted if `float64(v)` represents it exactly. Otherwise `ToProto` fails. Equivalently,
the integer's binary representation has at most 53 significant bits, from the highest set bit down to the
lowest set bit:

```go
// u is |v| as uint64 (for int64, u := uint64(v); if v < 0 { u = -u }; this also handles MinInt64).
exact := u == 0 || bits.Len64(u)-bits.TrailingZeros64(u) <= 53
```

So every integer in [-2^53, 2^53] passes, and so do larger integers that a double holds exactly, such as
2^53+2, 1<<62, 10^18, `math.MinInt64` and `uint64(1)<<63`. 2^53+1, `math.MaxInt64` and
`math.MaxUint64` fail. The check uses integer bit counts, not `int64(float64(v)) == v`: converting an
out-of-range float back to an integer is implementation-defined in Go, and `float64(math.MaxInt64)` is
2^63, which is out of range.

Why exactness and not the simpler "|v| > 2^53 fails" rule: exactness fails only the values that would
actually be corrupted. A range rule would also halt pipelines that are correct today, for example `uint64`
bit masks with the high bit set or round values like 10^18, and it would do so with nothing to fix. The
cost is predictability: in a column whose values are near 2^60, some values pass and most fail. In
practice such a column fails on its first record or soon after, so the difference does not matter for
real data. See _Alternatives_.

### Types checked

| Go type in `StructuredData` | Behavior |
| --- | --- |
| `int8`, `int16`, `int32`, `uint8`, `uint16`, `uint32` | always exact; unchanged |
| `int`, `uint` | checked (64 bits on 64-bit platforms, including `wasip1/wasm`; always exact on `386`) |
| `int64`, `uint64` | checked |
| `json.Number` written as an integer literal (`-?[0-9]+`) | parsed with `big.Int`, then the same significant-bits rule; integers longer than 1024 bits fail |
| `json.Number` in float notation (`1.5`, `1e20`) | unchanged: still `Float64()`. Decimal precision is out of scope (see below) |
| `float32`, `float64` | unchanged; they are already doubles |
| `*big.Int`, `time.Time`, other types | unchanged: `structpb` already rejects them with `invalid type` |

Decimals (`json.Number("0.1")`, or a decimal with more than 17 significant digits) also lose precision in
a double. They are not covered here, because a decimal source value is often approximate already and
rejecting it would halt many pipelines that work today. The lossless design in step 2 should carry
decimals as strings with a decimal kind. This is recorded as a known gap in the docs.

### Where the code goes

All of it is in `conduit-commons` `opencdc/proto.go`. `StructuredData.ToProto` stops calling
`structpb.NewStruct` and calls a local `newStructValue` that mirrors `structpb.NewValue` case by case and
adds the integer check. It walks nested `map[string]any` and `[]any`, keeps track of the field path, and
does it in a single pass, so nothing is traversed twice. Keys, `Payload.Before` and `Payload.After` all go
through `StructuredData.ToProto`, so all three are covered. `RawData` and metadata (`map<string,string>`)
do not change.

The check is only on the **encode** side. `FromProto` receives a double, and an integral double above
2^53 is a valid value, so decode cannot tell a rounded value from a genuine one. "Both directions" means
that each leg is checked by the side that encodes it: Conduit for the legs going to plugins, and the
plugin for the legs coming back.

New exported API in `opencdc`:

```go
// ErrIntegerNotRepresentable is returned (wrapped) by ToProto when structured
// data contains an integer that a protobuf number (an IEEE-754 double) cannot
// hold exactly. ToProto refuses rather than rounding: see invariant 6.
var ErrIntegerNotRepresentable = errors.New("integer not representable as a protobuf number")

// IntegerNotRepresentableError carries where the value was and its Go type.
// It deliberately does not carry the value.
type IntegerNotRepresentableError struct {
	Path   string // e.g. "id", "items[2].n"; Record.ToProto prefixes "key." or "payload.after."
	GoType string // e.g. "int64", "json.Number"
}

func (e *IntegerNotRepresentableError) Error() string
func (e *IntegerNotRepresentableError) Is(target error) bool // matches ErrIntegerNotRepresentable

// ValidateProto reports the first error ToProto would return for r, without
// allocating proto messages. Used by callers that must attribute a failure to
// one record of a batch before encoding the batch.
func (r Record) ValidateProto() error
```

`ValidateProto` must earn its place. Batched callers need it, and there are two of them (arch-v2's
destination and processor tasks, below). Without it, they would have to encode every record twice to find
the bad one. It shares the walker with `ToProto`, so the two cannot disagree.

Error text, for a payload field: `error converting payload: error converting after: field "id" (int64)
is an integer a protobuf number cannot hold exactly (more than 53 significant bits); it would be rounded`.

### How Conduit surfaces it

Conduit registers `plugin.integer_not_representable` (`codes.InvalidArgument`) in `pkg/plugin`. Each
Conduit-side call site wraps a matching error (`errors.Is(err, opencdc.ErrIntegerNotRepresentable)`) with
`conduiterr.Wrap`. The wrapped error keeps the field path and adds this suggested fix: "encode the
key/payload with a schema (`sdk.schema.extract.*.enabled: true`, the default) or as raw JSON so the value
crosses the plugin boundary as bytes; standalone plugins built with this Conduit's SDK refuse the same
value; see #3100".

The record is **nacked**, so the pipeline's DLQ policy decides what happens next:

- **Default DLQ** (`builtin:log`, window 1, threshold 0): the nack exceeds the threshold, and the
  pipeline stops and becomes degraded, with the code in its status. The record is not acked, so after a
  restart it is read again and halts the pipeline again until the operator acts. That is loud and loses
  nothing (invariants 1 and 3).
- **Configured, tolerant DLQ**: the record is written to the DLQ, its position is acked once that write
  succeeds, and the pipeline continues. That is the operator's declared policy (invariant 6: "follow the
  configured policy"). The DLQ record's `conduit.dlq.nack.error` metadata carries the code and the path.

Per call site:

| Call site | v0.21 behavior |
| --- | --- |
| arch-v1 `DestinationNode.Run` (`pkg/lifecycle/stream/destination.go`) | Today any `Write` error nacks and returns, which stops the pipeline even when the DLQ accepted the record. For this error class only, the node nacks and **continues**, without adding the message to `openMsgTracker`, because nothing was sent and the stream is healthy (conversion fails before `client.Send`). If the nack fails, the node returns the error as today. Other `Write` errors are unchanged. |
| arch-v2 `DestinationTask.Do` (`pkg/lifecycle-poc/funnel/destination.go`) | One bad record would fail `Write` for the whole batch. Before `Write`, the task calls `ValidateProto` on each active record, `Nack`s the failing indexes, and writes the remaining active records. If nothing is left, it skips `Write`. |
| `wasmProcessor.Process` (`pkg/plugin/processor/standalone/processor.go`) | Today a conversion error returns one `ErrorRecord` for the whole batch. In arch-v2 a single error with a short output nacks **index 0**, which is the wrong record when the bad one is later in the batch (`funnel/processor.go` treats the missing tail as records to retry). This is a latent bug that step 1 makes reachable. The fix: validate each record; if index `i` fails, send `records[:i]` to the guest, append `ErrorRecord` for `i`, and return. The rest are retried, as the funnel already does for a short output. arch-v1 sends one record per call, so it is correct either way. |
| WASM guest (`conduit-processor-sdk/run.go`) | With new commons, a guest that emits a wide integer fails `processedRecords`. The host receives `CommandResponse_Error` and returns `ErrorRecord`s, so the input is nacked. The code is attached on the host side by matching the text of the sentinel, as is already done for destination ack errors, which cross the protocol as strings. |
| Standalone source (SDK `runRead`) | With new commons, `stream.Send` fails for the batch, `Run` returns the error, and the source stops. There is no DLQ for a record that never entered Conduit, so this always halts. Nothing was acked, so a restart reads the record again (invariant 1). Conduit matches the sentinel text in the source's stream error to attach the code. The SDK should add the record position to the error. That is an SDK change shipped with the commons bump. |
| Standalone DLQ destination | The DLQ write fails the same way, so the nack fails and the pipeline stops (fatal). Nothing is acked. The status shows the original error and the DLQ error. Documented. |
| API inspector | Today a `ToProto` error ends the inspector stream. It must not stop an observer. Before `ToProto`, the inspector replaces each non-representable integer with its decimal string. This is a Conduit-local helper in `pkg/http/api` that uses the same walker through `ValidateProto` and an exported path. The inspector then shows the exact value, which is better than today's rounded one. The value becomes a JSON string in the inspector output, which is documented. The inspector is display-only, and nothing acts on its output. |

Invariant comments go at each enforcement site, for example
`// Invariant 6: refuse to round an integer at the plugin boundary; nack, never deliver a rounded key.`

### Versioning (commons is shared)

1. **`conduit-commons` v0.9.0.** This is a minor bump, because `ToProto` now returns errors where it used to
   succeed. That is a behavior change for every caller, including conduitio-labs' grpc-client connector.
   The new API is additive. The wire format does not change.
2. **Conduit v0.21** bumps commons. This covers every Conduit-side leg: standalone destinations,
   WASM processor input, DLQ to a standalone destination, and the inspector. It works with **any**
   plugin build, because Conduit encodes these legs.
3. **`conduit-connector-sdk` and `conduit-processor-sdk`** bump commons in their next releases.
   `conduit-connector-protocol` does not need a release: its `toproto` packages call commons, and Go's
   minimal version selection uses the highest commons version in the final binary.
4. **What still rounds.** A standalone source, or a WASM processor's output, built against commons
   older than v0.9.0, keeps rounding inside the plugin. Conduit receives a valid double and cannot detect
   the rounding. Those plugins are fixed by rebuilding them against the new SDKs. Certified connectors
   in the v0.21 train are rebuilt as part of certification. The release notes say this plainly.

## Alternatives considered (step 1)

- **Range rule (fail when |v| > 2^53).** It is predictable and one comparison. It lost because it halts
  pipelines whose wide values round-trip exactly today, such as high-bit masks and round numbers, so the
  upgrade turns a correct pipeline into a stopped one with nothing to fix. Exactness rejects exactly the
  set of values that would be corrupted.
- **Check in Conduit only (pre-validate in the destination node).** No commons release would be needed.
  It lost because it misses the plugin-side legs, the inspector, the DLQ and the conduitio-labs callers,
  and it duplicates logic that the one shared conversion function already has in reach. Commons is the
  single place every leg goes through.
- **Encode wide integers as strings on the wire now, without negotiation.** No halts. It lost because an
  old receiver silently gets a `string` where it expected a number. For MySQL that is the same bug again:
  MySQL compares a `BIGINT` column with a string argument as doubles. It is a type mangle at the
  boundary, and invariant 6 forbids it.
- **Round, and log a warning.** It lost because it is the bug with a log line added.
- **Halt unconditionally (ignore the DLQ).** It is simpler in the destination nodes. It lost because the
  nack path is the declared policy mechanism, and processors already report bad records through it. An
  operator who configured a tolerant DLQ asked for exactly this record to go there.

## Step 2: a lossless encoding (recommendation; needs an ADR and a versioning discussion)

### Options

**A. String-encoded integers with a schema hint.** Integers that need it are sent as `string_value`, and
record metadata (for example `opencdc.structured.intPaths`) lists their paths, so new commons can decode
them back to `int64`. This loses:

- Without negotiation, old receivers see strings. That is option 3 above, with the same MySQL failure.
- With negotiation, it pays the full cost of a protocol change and still leaves a fragile encoding. The
  hint paths go stale when a processor renames, moves or deletes fields. A user string that looks like a
  number is ambiguous wherever the hint is missing. And the hint takes space in user-visible metadata.

**B. A new structured value message with integer kinds, negotiated.** (Recommended.)

- commons gets a new proto package (for example `opencdc.v2`) with `Record`, `Data` and `Value`. `Value`
  has the kinds `null`, `bool`, `double`, `string`, `int64`, `uint64`, `bytes`, `list`, `struct`, and
  room for `decimal` (string-carried) and `timestamp`. `opencdc.v1` is not edited. An old peer must
  never receive a new oneof field: an unknown field in `Data`'s oneof decodes as "no data", and
  `dataFromProto` returns `nil`. **That would drop the whole payload silently**, which is the strongest
  reason never to add the field to `opencdc.v1.Data` in place.
- Connectors: `conduit-connector-protocol` v3, whose messages carry `opencdc.v2.Record`. It is negotiated
  through go-plugin's existing `VersionedPlugins` (today `{1, 2}`; Conduit and the plugin agree on the
  highest common version). An old plugin negotiates v2 and gets step 1's behavior, so nothing silent
  happens in any combination of versions.
- WASM processors have no version handshake. Add a capability field to `Specify.Response` (proto3
  defaults it to false, so old guests are unaffected), plus v2 record fields in `Process`. The host sends
  v2 only to a guest that declared it.
- Decoding yields `int64` and `uint64`, the same Go types that built-in plugins see in-process today.
  That is a parity gain, and it is also the main cost: a standalone plugin that type-asserts
  `float64` on integer fields breaks when it is rebuilt against the SDK that enables v3. It must be in
  the SDK release notes and covered by an acceptance-test case for integer fields. That code already
  breaks when the same connector runs built-in, so the bug was there already, but it will still surprise
  people.
- The same message also fixes `[]byte` crossing as base64 and NaN/Inf decoding as strings.

**C. `RawData` plus a schema on the wire.** Conduit Avro-encodes structured data, with an inferred and
registered schema, before every plugin leg. This is what the SDK source middleware already does. It reuses
existing machinery, and the protocol does not change. It lost for these reasons:

- A receiver without decode middleware (old SDKs, or decode disabled) silently gets bytes where it
  expected a struct.
- It adds schema inference and registration to every structured record in the engine.
- An Avro `long` cannot hold a `uint64` above `math.MaxInt64`.
- It ties protocol correctness to `hamba/avro`, which was archived upstream in 2026-01 (#2817).

It stays the **recommended workaround** in v0.21 docs: keep `sdk.schema.extract.*.enabled` on (the
default), so wide integers cross as Avro bytes.

### Recommendation

B, in v0.22 or later, after an ADR that covers the message definitions, the protocol v3 contents, the
processor capability flag, the deprecation timeline for protocol v2 (announce → warn → remove, at least
two minor versions), and an upgrade test for each pair of {old, new} Conduit and {old, new} plugin.
**This is a `conduit-connector-protocol` change. CLAUDE.md requires an explicit versioning discussion
before it, and this document does not replace that discussion.** Step 1 stays in place afterwards: when a
peer negotiates only v2, refusing is still the correct behavior.

## Failure modes (step 1)

| Failure | Effect | Shown by | Mitigation |
| --- | --- | --- | --- |
| A pipeline that rounded silently before the upgrade now halts | intended; the pipeline is degraded and the same record halts it again after each restart | pipeline status with `plugin.integer_not_representable` and the field path | release note; the fix text points to schema encoding or raw JSON. A string conversion (`field.convert`) is safe only if the destination parses strings into its integer type exactly; MySQL does not |
| Tolerant DLQ: a delete for key K goes to the DLQ, then later writes for K are delivered | the destination differs from the source for K until the record is replayed | DLQ record and DLQ metrics | the operator chose this policy; documented |
| The DLQ destination is standalone | the DLQ write fails, and the pipeline stops (fatal) | status shows both errors | nothing acked; documented; use a built-in DLQ or Avro encoding |
| arch-v2 batch with one bad record | without the design above, the whole batch fails (destination), or index 0 is nacked instead of the bad record (processor) | test failures, see _Testing plan_ | `ValidateProto` before `Write`; processor sends `records[:i]` and returns the error at `i` |
| Plugin built against older commons | it still rounds on the plugin-to-Conduit leg | not detectable by Conduit | rebuild against the new SDKs; release note. An optional counter of integral doubles ≥ 2^53 received from plugins was considered and dropped: every double ≥ 2^53 is integral, so the counter cannot tell rounding from real float data |
| Standalone source halts | the record never enters Conduit, so no DLQ is possible and the pipeline stays halted until the source is reconfigured | source error with the code (matched by sentinel text) | same fix text; the SDK adds the record position |
| A record value leaks into logs or status | none: the error carries only the path and the Go type | review of the error type | the type has no value field by design |
| Inspector stream | before: it ends on the first wide integer. After: the value is shown exactly, as a string | inspector output | the display helper; documented |
| CPU cost of the walk | one extra type switch per scalar, folded into the same pass that builds the `Struct` | benchi | no number is claimed. The benchi regression gate is not live, so the code PR runs benchi on a structured-payload pipeline with a standalone destination and reports medians with variance |
| Crash during any of this | nothing new is persisted; no position, state or wire format changes | n/a | n/a |

Concurrency: the check is a pure function of its input, with no shared state.

## Upgrade and rollback

- Upgrade: no serialized format changes. Positions, state, checkpoints and the wire format are the same,
  so an N → N+1 upgrade test has nothing new to cover. (The upgrade gate is not live anyway; see the
  process maturity table.) The behavior change is the halt described above, and the release notes list
  it under breaking behavior.
- Rollback: downgrade Conduit, or pin commons below v0.9.0, and the old rounding comes back. Records
  already in a DLQ stay there, with exact values if the DLQ is built-in. Nothing needs migrating.
- Step 2 has its own upgrade and rollback plan in its ADR. Negotiation makes each pair of versions fall
  back to step 1.

## Observability

- Stable code `plugin.integer_not_representable`, with the field path and the Go type, in: the pipeline
  status error, the `conduit.dlq.nack.error` DLQ metadata, and the nack log line (connector or processor
  ID, path, type).
- The existing nack and DLQ metrics count these records. No new metric, because the code already
  separates this cause in logs and DLQ metadata. Add a labelled counter if operators ask for one.
- The runbook entry `docs/operations/` (symptom → diagnosis → remediation) ships with the code PR.

## Testing plan

Each regression test is shown to fail on current `main` before it is shown to pass.

1. **commons unit tests**, a table over `ToProto`: `int64` 2^53 (ok), 2^53+1 (error), 2^53+2 (ok),
   -(2^53+1) (error), `MaxInt64` (error), `MinInt64` (ok); `uint64` 1<<63 (ok), `MaxUint64` (error);
   `int` 2^53+1 (error on 64-bit, not reachable on 386); `json.Number("9007199254740993")` (error),
   `json.Number("1e300")` (unchanged); a nested map, a `[]any` element, and a key versus
   `payload.before`/`payload.after` (path prefixes); `errors.Is` with the sentinel, `errors.As` for the
   path, and an assertion that the error text does not contain the value. Regression:
   `{id: int64(1<<53+1)}` through `ToProto` and `FromProto` today returns `9007199254740992` with a nil
   error. The test asserts an error.
2. **commons fuzz target** `FuzzStructuredDataIntegerRoundTrip(int64, uint64)`. The property: either
   `ToProto` fails, or `FromProto(ToProto(v))` is a `float64` equal to `v`. Never a silent difference.
   The seed corpus (2^53±1, extremes) runs in the normal `test` job, which is how fuzz seeds are gated
   today. There is no property framework in the tree, and this test does not add one.
3. **Standalone processor reproduction in Conduit**, the reproduction for #3100.
   - Add a `passthrough` test guest under `pkg/plugin/processor/standalone/test/wasm_processors/`. It
     returns `SingleRecord(rec)` unchanged, and with `emit: wide-int` it sets `id = int64(1<<53+1)`. It is
     built by `build-test-processors.sh` against the commons version in the `conduit` module.
   - Host leg: `wasmProcessor.Process` on `{key: {id: int64(1<<53+1)}, payload.after: {id: ...}}`. On
     `main` it returns a `SingleRecord` with `id == float64(9007199254740992)`, which demonstrates the bug.
     After the fix it returns an `ErrorRecord` that matches the code and `ErrIntegerNotRepresentable`,
     with path `key.id`. Control: `int64(1<<53)` passes as `float64(1<<53)`.
   - Guest leg: `emit: wide-int` on a small input. After the fix it returns an `ErrorRecord`, and the
     host does not round.
   - Batch attribution: three records with index 1 bad. Result: `[processed, ErrorRecord, <retried>]`,
     with index 0 not nacked.
4. **Destination leg**: extend `pkg/plugin/connector/standalone/test/testplugin` so that its destination
   records what it received. A record with `id = int64(1<<53+1)` must not reach the plugin. `Write`
   returns an error with the code, and the plugin sees zero records.
5. **Pipeline level, both engines**: a mock source with a wide-int record, then a standalone passthrough
   processor, then a built-in destination.
   - Default DLQ: the pipeline is degraded with the code, the record is not acked, and restarting with the
     same source halts again.
   - DLQ window 2, threshold 1: the record is in the DLQ with the code in its metadata, the pipeline keeps
     running, and the next record (`id = 1<<53`) reaches the destination exactly.
   - arch-v2: a batch of three with the middle record bad. Only that record is nacked, and the other two
     are delivered in order.
6. **Inspector**: `InspectProcessorIn` on a wide-int record does not end the stream, and it shows
   `"9007199254740993"`.
7. **Chaos**: these changes touch nack paths in the destination nodes, which are data-path trees, so the
   required `tests/chaos (race, x3)` job runs. No new SIGKILL scenario is needed: nothing is persisted
   between the nack decision and the existing DLQ and ack machinery, which the chaos suite already
   covers.

## Consequences

- Silent corruption at the plugin boundary becomes a loud, coded failure. That is the trade DeVaris
  accepted for v0.21.
- Some pipelines that worked before, while silently rounding, will halt after the upgrade. That is the
  point of the change, but it needs a clear release note and runbook.
- The plugin-to-Conduit legs stay unprotected until plugins are rebuilt. The certified v0.21 connectors
  are rebuilt as part of the train.
- commons gains one sentinel, one error type and one validation method.
- Step 2 is left as a recommendation for an ADR. It is not a commitment.

## Decisions needed (DeVaris)

1. Exactness rule (recommended) or range rule.
2. Destination nodes nack and continue under the DLQ policy (recommended), or halt unconditionally.
3. Version number for the commons release: v0.9.0 (recommended) or v0.8.3.
4. Whether to open the step 2 ADR and the protocol v3 versioning discussion now, or after v0.21 ships.

## Related

- #3100: the issue. #3006: the JDBC sink lane where it was found.
- `ConduitIO/conduit-connector-mysql#193`: exact delete keys in the MySQL destination.
- `ConduitIO/conduit-connector-postgres#402`: the same root cause on the Postgres JSON path.
- #2817: archived `hamba/avro`, relevant to option C.
- `docs/design-documents/20261012-standalone-plugin-env.md`: the precedent for adding protocol behavior
  through client options without a wire change.

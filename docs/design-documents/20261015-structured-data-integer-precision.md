# Integer precision of structured data at the plugin boundary

## Summary

`opencdc.StructuredData` crosses every plugin boundary as a `google.protobuf.Struct`, and a `Struct`
number is a double. An integer outside the range a double holds exactly is rounded without an error. For
example, `int64(9007199254740993)` arrives in a standalone destination as `9007199254740992`, and a SQL
destination then deletes or overwrites the wrong row (#3100). This is invariant 6 (silent mangling) at
the protocol boundary.

This document covers two steps:

1. **v0.21: fail loudly.** `StructuredData.ToProto` in `conduit-commons` returns a typed error for any
   integer outside the safe-integer range [-(2^53−1), 2^53−1]. It no longer rounds. The wire format does
   not change. In Conduit the failed record is nacked with the stable code
   `plugin.integer_not_representable`, so the pipeline's DLQ policy applies: by default it halts, and
   with a configured DLQ the record goes to the DLQ. Nack-under-DLQ on the destination side depends on
   two engine changes designed below (the arch-v1 ordered nack and the arch-v2 batch split). If they
   cannot land in v0.21, the destination side halts unconditionally instead. Processor-side
   attribution depends on a fix in the processor SDK, which must ship in the same release as its
   commons bump. Plugins built against older commons still round on the plugin-to-Conduit
   leg, and so do two callers that do not use commons' `ToProto`. This design names them instead of
   hiding them.
2. **Later (after v0.21): a lossless encoding.** The recommendation is a new structured value message
   with real integer kinds. It is used only when both sides negotiate it: through a new
   `conduit-connector-protocol` version for connectors, and through a capability flag for WASM
   processors. **This changes the connector protocol, so it is breaking-change territory.** It needs its
   own ADR and the explicit versioning discussion that CLAUDE.md requires. This document recommends a
   direction. It does not decide the wire format.

Step 1 is what DeVaris accepted for v0.21. The four decisions below were decided by DeVaris on 2026-10-15
(see _Decisions_). Step 2 is recorded as a direction; its wire format is decided in its own ADR.

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
is therefore not new. Rounding is the gap. The existing loud failures already reach the batch
attribution bug described in #3127 (see _How Conduit surfaces it_).

### Every place a record becomes a `Struct`

A search of ConduitIO and conduitio-labs for `opencdcv1.Record`, `ToProto(` and `structpb.NewStruct`,
excluding generated code and tests:

| Leg | Who builds the `Struct` | Which process, which code | Fixed by the commons change? |
| --- | --- | --- | --- |
| Conduit → standalone destination | `pconnector/v2/client.DestinationRunStream.Send` → `toproto.DestinationRunRequest` (v1 has the same shape) | Conduit; Conduit's commons | yes |
| Standalone source → Conduit | `pconnector/v2/server.SourceRunStream.Send` → `toproto.SourceRunResponse`, called from the SDK's `runRead` | plugin; the plugin's commons | only once the plugin is rebuilt |
| Conduit → WASM processor | `pkg/plugin/processor/standalone/proto.go` `protoConverter.records`, from `wasmProcessor.Process` | Conduit; Conduit's commons | yes |
| WASM processor → Conduit | `conduit-processor-sdk/run.go` `protoConverter.singleRecord` (:300) and `multiRecord` (:333) | guest; the commons compiled into the `.wasm` | only once the guest is rebuilt |
| API inspector | `pkg/http/api/connector_v1.go` `InspectConnector`, `processor_v1.go` `InspectProcessorIn` and `InspectProcessorOut` | Conduit; Conduit's commons | yes (with the display helper below) |
| Conduit-to-Conduit over gRPC | conduitio-labs `conduit-connector-grpc-client` `toproto/record.go:86` | plugin; **calls `structpb.NewStruct` directly**, pins commons v0.6.0 | **no**: needs its own code change |
| Pinecone vector metadata | conduitio-labs `conduit-connector-pinecone` `destination.go:204` | plugin; `structpb.NewStruct` for Pinecone's own metadata API, not the Conduit protocol | **no**: separate issue for that connector |

The last two rows do not go through commons' `ToProto`, so bumping commons does not change them. The
grpc-client connector carries records between Conduit instances, so it has the same rounding bug as the
plugin protocol. It needs a code change that calls `opencdc.StructuredData.ToProto` (or the same check),
and a commons bump from v0.6.0. Until that ships it is **unprotected**, and the docs say so. Pinecone
metadata values are numbers on Pinecone's side too, so that is a connector-level policy question. It is
listed here so it is not mistaken for something step 1 covers.

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
- the record holds a `uint` or `uint64` field: the Avro extractor (`conduit-commons`
  `schema/avro/extractor.go`) does not support `uint`/`uint64`, so such records cannot use the Avro path
  at all;
- a processor leaves the data structured without a schema to re-encode it with;
- a record reaches a standalone DLQ destination: both engines wrap the failed record as
  `StructuredData(record.Map())` (`pkg/lifecycle/stream/dlq.go` and `pkg/lifecycle-poc/funnel/dlq.go:157`),
  so even an Avro record's structured fields cross as a `Struct` there.

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

### The rule: the safe-integer range

`ToProto` accepts an integer `v` only if `-(2^53−1) <= v <= 2^53−1`. Outside that range it fails. This is
JavaScript's `Number.MAX_SAFE_INTEGER` and the I-JSON interoperability limit (RFC 7493, section 2.2).

The reason is what the receiver gets. Before and after the change, a value that passes still reaches the
plugin as a `float64`. The question is whether that `float64` identifies exactly one integer. Inside the
safe range it does: each safe integer is the only integer that maps to its double. At 2^53 and above it
does not:

- 2^53 itself is exact, but 2^53+1 rounds to the same double, so a receiver that sees 9007199254740992
  cannot know which of the two was sent. An older plugin build sends the rounded 2^53+1 as exactly that
  double.
- A value such as 10^18 is exactly representable, but the spacing between doubles near 10^18 is 128. A
  destination that binds the key as a double (MySQL compares a `BIGINT` column with a double argument as
  doubles) matches every id within ±64 of it. Passing 10^18 through would bring the wrong-row delete
  back.

So "exactly representable" is not the right test. The right test is "unambiguous at the receiver", and
that is the safe range. The check is two integer comparisons. It needs no float conversion, so it avoids
Go's implementation-defined float-to-integer conversion for out-of-range values.

This rule halts some pipelines that look correct today, for example `uint64` bit masks with the high bit
set, or round numbers like 10^18. That is accepted. Those values are correct only as long as no receiver
compares them as doubles, and the boundary cannot know that. The fix text tells the operator how to move
them off the `Struct` path. The exactness rule is recorded under _Alternatives_.

### Types checked

| Go type in `StructuredData` | Behavior |
| --- | --- |
| `int8`, `int16`, `int32`, `uint8`, `uint16`, `uint32` | always in range; unchanged |
| `int`, `uint` | checked (64 bits on 64-bit platforms, including `wasip1/wasm`; always in range on `386`) |
| `int64`, `uint64` | checked |
| `json.Number` | **validated against the JSON number grammar first** (RFC 8259: `-?(0\|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?`). A literal outside it is an error (`ErrInvalidNumber`, path and type only), and it never falls back to `Float64()`. Today `strconv.ParseFloat` accepts hex floats, a leading `+`, and `Inf`/`Infinity`/`NaN`, so `json.Number("0x20000000000001p0")` silently becomes 9007199254740992, and so do `"+9007199254740993"` and `"Infinity"` (which becomes +Inf). After validation, the literal is classified with string arithmetic, without `big.Rat`. Strip the sign, leading zeros and the decimal point to get the significant digits `d` and the effective decimal exponent `e`, then drop trailing zeros from `d` (raising `e`). **Zero mantissa:** if `d` is empty (`0`, `-0`, `0.000`, `0e99999999999999999999`) the value is the integer 0 and passes. This case is handled before any `ParseInt("")`. **Exponent overflow:** the written exponent is parsed with a saturating parser, not `strconv.Atoi`, and saturates at ±2^61. The shift from the decimal point and from stripped zeros is bounded by the literal's length, which is below 2^61 for any literal that fits in memory. So `e` and `len(d)+e`, computed in `int64`, cannot overflow and keep their sign and their relation to 16, and no input size can cause a false rejection. Only two facts are used, the sign of `e` and whether `len(d)+e > 16`, and saturation preserves both: a huge positive exponent with a nonzero mantissa is out of range, and a huge negative one makes the value a non-integer. If `e < 0` the value is not an integer. Otherwise it is an integer with `len(d)+e` digits: more than 16 digits is out of range and is rejected at once, and 16 or fewer are compared exactly through `strconv.ParseInt`. So the range rule applies to integers in any notation (`9007199254740993`, `9007199254740993e0`, `1e20`), and a huge literal such as `1e999999` costs one scan of the string, not a `big.Rat` expansion. A non-integer value (`1.5`) is unchanged: it still goes through `Float64()`. Decimal precision is out of scope (see below) |
| `float32`, `float64` | unchanged; they are already doubles |
| `*big.Int`, `time.Time`, other types | unchanged: `structpb` already rejects them with `invalid type` |

`json.Number` in exponent form is included because `"9007199254740993e0"` is an integer, and today it
rounds silently just like the plain literal. The classification is exact and linear in the literal's
length, and it runs only for `json.Number`, which few sources produce.

Decimals (`json.Number("0.1")`, or a decimal with more than 17 significant digits) also lose precision in
a double. They are not covered here, because a decimal source value is often approximate already and
rejecting it would halt many pipelines that work today. The lossless design in step 2 should carry
decimals as strings with a decimal kind. This is recorded as a known gap in the docs.

### Where the code goes

All of it is in `conduit-commons` `opencdc/proto.go`. `StructuredData.ToProto` stops calling
`structpb.NewStruct` and calls a local `newStructValue` that mirrors `structpb.NewValue` case by case and
adds the range check. It walks nested `map[string]any` and `[]any`, keeps track of the field path, and
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
// data contains an integer outside [-(2^53-1), 2^53-1], the range in which a
// protobuf number (an IEEE-754 double) identifies exactly one integer.
// ToProto refuses rather than rounding: see invariant 6.
var ErrIntegerNotRepresentable = errors.New("integer not representable as a protobuf number")

// ErrInvalidNumber is returned (wrapped) by ToProto for a json.Number whose
// literal is not a JSON number (RFC 8259), e.g. a hex float or "Infinity".
// It is never parsed leniently.
var ErrInvalidNumber = errors.New("invalid number")

// IntegerNotRepresentableError carries where the value was and its Go type.
// It deliberately does not carry the value.
type IntegerNotRepresentableError struct {
	// Path is the field path from the record root, e.g. "payload.after.id",
	// "key.id", "payload.after.items[2].n". StructuredData.ToProto alone
	// reports the path relative to the data ("id"); Record.ToProto prefixes it.
	Path   string
	GoType string // e.g. "int64", "json.Number"
}

func (e *IntegerNotRepresentableError) Error() string
func (e *IntegerNotRepresentableError) Is(target error) bool // matches ErrIntegerNotRepresentable

// ValidateProto reports the first error ToProto would return for r, without
// allocating proto messages. Used by callers that must attribute a failure to
// one record of a batch before encoding the batch.
func (r Record) ValidateProto() error
```

`ValidateProto` has to justify itself as new API, and it does: four batched callers need it (the arch-v2
destination task, the WASM host, the guest SDK, and the inspector helper, below). Without it, they would
have to encode every record twice to find the bad one. It shares the walker with `ToProto`, so the two
cannot disagree.

Error text, for a payload field (from `Record.ToProto`, so the path is from the record root):
`field "payload.after.id" (int64) is outside the safe integer range ±(2^53-1) of a protobuf number; it
would be rounded`.

### How Conduit surfaces it

Conduit registers `plugin.integer_not_representable` and `plugin.number_invalid` (both
`codes.InvalidArgument`) in `pkg/plugin`. `plugin.number_invalid` covers `ErrInvalidNumber` and takes the
same path as the rest of this section. Each
Conduit-side call site wraps a matching error (`errors.Is(err, opencdc.ErrIntegerNotRepresentable)`) with
`conduiterr.Wrap`. The wrapped error keeps the field path and adds this suggested fix:

> Encode the key/payload with a schema (`sdk.schema.extract.*.enabled: true`, the default; not available
> for `uint`/`uint64` fields) or as raw JSON, so the value crosses the plugin boundary as bytes.
> Standalone plugins built with this Conduit's SDK refuse the same value. See #3100.

The record is **nacked**, so the pipeline's DLQ policy decides what happens next:

- **Default DLQ** (`builtin:log`, window 1, threshold 0): the pipeline stops and becomes **degraded**,
  with the code in its status. **This needs one more piece:** with threshold 0, both DLQ handlers return
  the nack reason as a plain error (`pkg/lifecycle/stream/dlq.go:155-163`,
  `pkg/lifecycle-poc/funnel/dlq.go:108-118`). They wrap it in `cerrors.FatalError` only when the
  threshold is above 0. A plain error goes to pipeline recovery, which by default retries forever
  (`pkg/conduit/config.go:393`, `InfiniteRetriesErrRecovery`). The result would be an endless restart
  loop that re-reads the same record and never shows Degraded. That loses nothing, but it is not loud.
  **Fix:** Conduit makes the coded refusal fatal, but only at the sites that return an error to the
  pipeline: the threshold-0 branch of both DLQ handlers returns `cerrors.FatalError(reason)` when the
  reason matches the refusal sentinels, and so do the arch-v1 node's fallback return and the arch-v2
  `Do` return. The nack reason itself, the host-side `ErrorRecord` and the text stored in the DLQ
  record's `conduit.dlq.nack.error` stay **unwrapped**. `fatalError.Error()` adds a "fatal error"
  prefix, and the DLQ metadata must show the coded message, not that prefix. The refusal is
  deterministic, so a restart cannot cure it, which is the same reasoning as the existing fatal
  DLQ-write failure (`funnel/dlq.go:99`). The record is not acked, so after a manual restart it is
  read again and halts again until the operator acts (invariants 1 and 3).
  In arch-v1 the node's error and the acker's DLQ error can race. Both are wrapped as fatal and both
  carry the code, so either one wins with the same outcome. In fan-out pipelines, sibling source ackers
  report a plain `another message failed to be acked/nacked` (`source_acker.go`). That error can reach
  the pipeline first, as non-fatal. This already happens with threshold > 0. The code PR either makes
  that error fatal when its cause is fatal, or records it as a known gap with an issue.
- **Configured, tolerant DLQ**: the record is written to the DLQ, its position is acked once that write
  succeeds, and the pipeline continues. That is the operator's declared policy (invariant 6: "follow the
  configured policy"). The DLQ record's `conduit.dlq.nack.error` metadata carries the code and the path.
  The fatal wrap does not change this case: a tolerant DLQ absorbs the nack and stores only the
  reason's text, so nothing fatal propagates.

On the destination side, nack-under-DLQ is safe only with the two engine changes below (the arch-v1
ordered nack and the arch-v2 validate-then-split). **If either cannot land in v0.21, the fallback is to
halt unconditionally on the destination side.** The fallback is specified precisely, because the
obvious version hangs:

- **arch-v1:** the node passes the refused message to `DestinationAckerNode` **exactly as in the
  primary design** (marked refused, not sent, `lastPosition` not advanced), and then returns the coded
  error. The acker nacks it in queue order while it drains during teardown. Returning without nacking
  or passing the message on would leave it open, and `SourceNode`'s `openMsgTracker.Wait()` on stop
  (`pkg/lifecycle/stream/source.go:123`) would hang. Nacking it synchronously before returning would
  hit the S2 deadlock. With the default DLQ nothing is acked, and the fatal wrap makes the pipeline
  degraded. With a tolerant DLQ, the record either reaches the DLQ before the pipeline stops or is
  redelivered on restart, depending on whether the acker drains it before teardown. Either way it is
  not lost.
- **arch-v2:** `Do` runs `ValidateProto` as in the primary design and, on a refusal, returns the coded
  error before `Write`. A task error makes `Worker.doTask` return at once (`worker.go`, the
  `task %s: %w` return), without acting on any batch flags. Nothing in the batch is acked or nacked, so
  it is all redelivered on restart (invariants 1 and 3), and no DLQ is involved. The error is
  fatal-wrapped, as above. If the destination is shared, the error also marks it poisoned
  (`worker.go:501`). That is conservative, because the refusal happened before `Write` and the ack
  stream is still in sync. It is harmless, because the pipeline is stopping anyway and a restart builds
  a fresh destination.

The halt fallback is less useful, but it cannot misattribute, deadlock or hang on stop.

The processor side has no fallback switch. The host-side per-index fix lives in Conduit. The guest-side
fix lives in `conduit-processor-sdk`, and the constraint is a release rule: **the processor SDK must not
ship its commons bump without the per-index fix.** Otherwise a rebuilt guest would refuse wide integers
with a whole-command error, and the funnel would nack index 0.

Per call site:

| Call site | v0.21 behavior |
| --- | --- |
| arch-v1 `DestinationNode.Run` (`pkg/lifecycle/stream/destination.go`) | **Precedent:** filtered messages already take this route. The node sends them straight on, and the acker acks them without a plugin ack (the `msg.filtered` branch in `destination_acker.go`). The refused branch sits beside it, and like it, **before** the `len(acks) == 0` fetch from the plugin, so a refused message at the head of the queue never waits for a plugin ack that will not come. The node **never advances `lastPosition`** for a refused message, the same as on the filtered path. The SDK destination's `Stop` waits until it has seen `req.LastPosition` (`conduit-connector-sdk/destination.go:257-264`). A position the plugin never received would make it wait out `stopTimeout` and then flush with a warning on every stop. Today any `Write` error calls `msg.Nack` on the node goroutine and returns. For this error class, a synchronous nack on the node goroutine can **deadlock** with destination batching, as follows. `SourceAckerNode`'s nack handler acquires the ordering semaphore (`source_acker.go:137-156`) and waits until every earlier message is acked. Those earlier messages sit in the plugin's batch, which flushes on `sdk.batch.size` when `sdk.batch.delay` is 0. That flush needs more records, but the node is the goroutine that would write them. **Design:** the node does not nack. It marks the message refused (with the coded error) and passes it to `DestinationAckerNode` like a written message, without sending it to the plugin. When the acker worker reaches a refused message at the head of its queue, it nacks it instead of reading a plugin ack. Ordering is kept (invariant 4): it is nacked after every earlier message is acked, and the node never blocks, so writes continue and batches fill. Other `Write` errors are unchanged. |
| arch-v2 `DestinationTask.Do` (`pkg/lifecycle-poc/funnel/destination.go`) | Not "nack and write the rest". `Batch.Nack` does not remove a record from `ActiveRecords()` (`batch.go:603-617`). A sub-slice with a hole in it would also break the index mapping in `markBatchRecords` (`destination.go:115`, `:163-168`): an ack error for record 2 would nack index 1 and treat record 2 as acked, which breaks invariants 1 and 3. **Design:** before `Write`, call `ValidateProto` on every active record. If any fail, `Nack` the failing indexes, mark every other active record `Retry`, skip `Write`, and return. The worker's existing tainted-batch split (`worker.go:636-660`) then sends the nacked runs to the DLQ and re-runs each contiguous `Retry` run as its own sub-batch. Those sub-batches contain no refused record and have no holes, so `markBatchRecords` sees contiguous indexes. The retried runs make progress, so the retry bounds (`maxRetryAttempts`, `maxRetryStall`) are not consumed. A test pins that. This is the first destination-side use of `RecordFlagRetry`. The `CodeRetryNotConverging` doc comment (`funnel/codes.go:93`) and the message text in `worker.go` (`:740`, `:763`) say "processor". The code PR makes both task-neutral, so a stuck destination retry would not be reported as a processor fault. |
| `wasmProcessor.Process` (`pkg/plugin/processor/standalone/processor.go`) | Today a host-side conversion error returns one `ErrorRecord` for the whole batch. In arch-v2 the funnel then nacks **index 0** and retries the tail (`funnel/processor.go:111-115`), which is the wrong record when the bad one is later in the batch. This is **live today** for types `structpb` already rejects, for example a `time.Time` (#3127); step 1 makes it reachable for integers too. **Fix:** validate each record. If index `i` fails, send `records[:i]` to the guest (or nothing, when `i` is 0), append an `ErrorRecord` for `i`, and return. The tail is retried, as the funnel already does for a short output. arch-v1 sends one record per call, so it is correct either way. This fix also closes #3127. |
| WASM guest output (`conduit-processor-sdk/run.go:300`, `:333`) | Today one output that fails `ToProto` fails the whole command. `processedRecords` returns an error, the guest replies with `CommandResponse_Error`, and the host's `executeCommand` path (`processor.go:354-356`) makes **one** `ErrorRecord`. In arch-v2 the funnel then nacks index 0, which is the same misattribution as above. **Fix in the processor SDK:** when output `i` (a `SingleRecord`, or any record of a `MultiRecord`) fails conversion, `processedRecords` emits `Process_ErrorRecord` at index `i` with that error, and converts the others normally. The command no longer fails. The host maps `ErrorRecord` per index as it already does. The code is attached on the host side by matching the sentinel text, as is already done for destination ack errors, which cross the protocol as strings. The fix takes effect only in guests rebuilt with the new processor SDK. Old guests keep the one-error behavior and, until rebuilt, the rounding. |
| Standalone source (SDK `runRead`) | With new commons, `stream.Send` fails for the batch, `Run` returns the error, and the source stops. There is no DLQ for a record that never entered Conduit, so this always halts. Nothing was acked, so a restart reads the record again (invariant 1). Conduit matches the sentinel text in the source's stream error to attach the code. The SDK should add the record position to the error. That is an SDK change shipped with the commons bump. |
| Standalone DLQ destination | The DLQ write fails the same way, so the nack fails and the pipeline stops (fatal). Nothing is acked. The status shows the original error and the DLQ error. Documented. |
| API inspector | Today a `ToProto` error ends the inspector stream. It must not stop an observer. Before `ToProto`, the inspector renders each out-of-range integer as its decimal string. The helper builds a **new** record and never mutates its input: `Instance.inspector.Send` runs before the record is written, and the inspector hands one clone to all sessions (`pkg/inspector/inspector.go:114`), so a mutation would leak into other sessions. The helper is local to Conduit, in `pkg/http/api`. The value becomes a JSON string in the inspector output, which is documented. The inspector is display-only, and nothing acts on its output. Because `inspector.Send` runs before `stream.Send`, the inspector also shows records that are then refused. That is useful for diagnosis and is documented. |

Invariant comments go at each enforcement site, for example
`// Invariant 6: refuse to round an integer at the plugin boundary; nack, never deliver a rounded key.`

### Versioning (commons is shared)

1. **`conduit-commons` v0.9.0.** A minor bump is required regardless of this change: `main` already holds
   the breaking `feat(schema)!: resolve Protobuf schema references (PB-3)` (#290) and eight other commits
   since v0.8.2. This change adds a behavior change (`ToProto` now returns errors where it used to
   succeed) and additive API. The wire format does not change. **Conduit v0.21 takes #290 with this
   bump**, so the Conduit PR that bumps commons must also pass the #290 migration (schema reference
   resolution) and say so in its description.
2. **Conduit v0.21** bumps commons. This covers every Conduit-side leg: standalone destinations,
   WASM processor input, DLQ to a standalone destination, and the inspector. It works with **any**
   plugin build, because Conduit encodes these legs.
3. **`conduit-connector-sdk` and `conduit-processor-sdk`** bump commons in their next releases. The
   processor SDK release also carries the per-index guest fix. `conduit-connector-protocol` does not need
   a release: its `toproto` packages call commons, and Go's minimal version selection uses the highest
   commons version in the final binary.
4. **What still rounds.** A standalone source, or a WASM processor's output, built against commons older
   than v0.9.0, keeps rounding inside the plugin. Conduit receives a valid double and cannot detect the
   rounding. Those plugins are fixed by rebuilding them against the new SDKs. Certified connectors in the
   v0.21 train are rebuilt as part of certification. `conduit-connector-grpc-client` still rounds after
   a rebuild, until its own code change lands. The release notes say all of this plainly.

## Alternatives considered (step 1)

- **Exactness rule (accept any integer that `float64(v)` represents exactly).** It rejects only the
  values that change on conversion, so it would not halt pipelines carrying high-bit masks or round
  numbers. It lost because "exact on encode" does not mean "unambiguous at the receiver". 2^53 passes but
  collides with a rounded 2^53+1. 10^18 passes, but a destination that binds it as a double matches every
  id within ±64. It would also make failures data-dependent: in a column near 2^60, a few values pass and
  most fail.
- **Check in Conduit only (pre-validate in the destination node).** No commons release would be needed.
  It lost because it misses the plugin-side legs, the inspector and the DLQ, and it duplicates logic that
  the one shared conversion function already has in reach. Commons is the single place almost every leg
  goes through. The two that bypass it are listed and handled separately.
- **Encode wide integers as strings on the wire now, without negotiation.** No halts. It lost because an
  old receiver silently gets a `string` where it expected a number. For MySQL that is the same bug again:
  MySQL compares a `BIGINT` column with a string argument as doubles. It is a type mangle at the
  boundary, and invariant 6 forbids it.
- **Round, and log a warning.** It lost because it is the bug with a log line added.
- **Halt unconditionally on the destination side.** It is simpler in both engines and cannot deadlock or
  misattribute. It is not the recommendation, because the nack path is the declared policy mechanism,
  processors already report bad records through it, and an operator who configured a tolerant DLQ asked
  for exactly this record to go there. **It is the fallback** if the arch-v1 ordered nack or the arch-v2
  batch split cannot land in v0.21. It still hands the refused message to the acker (see _How Conduit
  surfaces it_).

## Step 2: a lossless encoding (recommendation; needs an ADR and a versioning discussion)

### Options

**A. String-encoded integers with a schema hint.** Integers that need it are sent as `string_value`, and
record metadata (for example `opencdc.structured.intPaths`) lists their paths, so new commons can decode
them back to `int64`. This loses:

- Without negotiation, old receivers see strings. That is the string alternative in step 1, with the
  same MySQL failure.
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
- The Avro extractor rejects `uint` and `uint64` outright, and an Avro `long` cannot hold a `uint64`
  above `math.MaxInt64` in any case.
- It ties protocol correctness to `hamba/avro`, which was archived upstream in 2026-01 (#2817).

It stays the **recommended workaround** in v0.21 docs for signed integers: keep `sdk.schema.extract.*.enabled`
on (the default), so wide integers cross as Avro bytes. For `uint`/`uint64` fields the only workaround is
a raw JSON payload, or a string field that the destination parses exactly.

### Recommendation

B, after an ADR that covers the message definitions, the protocol v3 contents, the processor capability
flag, the deprecation timeline for protocol v2 (announce → warn → remove, at least two minor versions),
and an upgrade test for each pair of {old, new} Conduit and {old, new} plugin. The ADR and a protocol
versioning issue should be opened now, so the discussion runs in parallel. Implementation should start
after v0.21 ships. **This is a `conduit-connector-protocol` change. CLAUDE.md requires an explicit
versioning discussion before it, and this document does not replace that discussion.** Step 1 stays in
place afterwards: when a peer negotiates only v2, refusing is still the correct behavior.

## Failure modes (step 1)

| Failure | Effect | Shown by | Mitigation |
| --- | --- | --- | --- |
| A pipeline that rounded silently before the upgrade now halts, including ones whose values happened to be exact (high-bit masks, round numbers) | intended; the pipeline is degraded and the same record halts it again after each restart | pipeline status with `plugin.integer_not_representable` and the field path | release note; the fix text points to schema encoding or raw JSON. A string conversion (`field.convert`) is safe only if the destination parses strings into its integer type exactly; MySQL does not |
| arch-v1: refused record nacked synchronously while earlier records wait in a plugin batch (`sdk.batch.delay` 0) | deadlock: the nack waits on the ordering semaphore for acks that need more writes | stuck pipeline, no progress, no error | the refused message goes through the destination acker and is nacked in queue order; the node never blocks. The S2 test (below) pins this for both nack-and-continue and the halt fallback |
| Refusal returned as a plain (non-fatal) error under the default DLQ | infinite recovery loop that re-reads the same record; never Degraded (no loss) | restart counter climbing; repeated identical errors | wrap the coded refusal in `cerrors.FatalError` at the Conduit boundary; pinned by test 7 |
| `json.Number` outside the JSON grammar (hex float, `+`, `Infinity`) | today: silently rounded or turned into ±Inf | n/a today | grammar validation first; `ErrInvalidNumber`, never `Float64()` |
| arch-v1: `lastPosition` advanced to a refused record's position | the SDK destination's `Stop` waits out `stopTimeout` for a position it never received, then flushes with a warning, on every stop | slow stops; the "did not encounter expected last position" warning | the node never advances `lastPosition` for a refused message, as on the filtered path; pinned by a test |
| Halt fallback returns without handing the refused message on | the message stays open, and `openMsgTracker.Wait()` in the source node hangs on stop | a pipeline that never finishes stopping | the fallback passes the message to the acker exactly as the primary design does, then returns; pinned by a test |
| arch-v2: refused record nacked in place and the rest written as one slice | `markBatchRecords` maps acks onto the wrong indexes, so a failed record is acked upstream (invariants 1 and 3) | the ack-error-after-hole test (below) | never write a slice with holes: nack the refused records, `Retry` the rest, and let the tainted-batch split re-run contiguous sub-batches |
| WASM host or guest conversion fails for one record of a batch | arch-v2 nacks index 0 instead of the bad record (#3127, live today for `time.Time`) | the batch attribution tests (below) | host sends `records[:i]` and returns `ErrorRecord` at `i`; guest SDK emits `ErrorRecord` at the failing output index |
| Tolerant DLQ: a delete for key K goes to the DLQ, then later writes for K are delivered | the destination differs from the source for K until the record is replayed | DLQ record and DLQ metrics | the operator chose this policy; documented |
| The DLQ destination is standalone | the DLQ write fails, and the pipeline stops (fatal) | status shows both errors | nothing acked; documented; use a built-in DLQ or Avro encoding |
| Plugin built against older commons, or a guest built with the old processor SDK | it still rounds on the plugin-to-Conduit leg | not detectable by Conduit | rebuild against the new SDKs; release note. An optional counter of integral doubles ≥ 2^53 received from plugins was considered and dropped: every double ≥ 2^53 is integral, so the counter cannot tell rounding from real float data |
| `conduit-connector-grpc-client` (direct `structpb.NewStruct`) | still rounds after the commons bump | not detectable | separate code change in that connector; marked unprotected in docs until it lands |
| Standalone source halts | the record never enters Conduit, so no DLQ is possible and the pipeline stays halted until the source is reconfigured | source error with the code (matched by sentinel text) | same fix text; the SDK adds the record position |
| A record value leaks into logs or status | none: the error carries only the path and the Go type | review of the error type | the type has no value field by design |
| Inspector stream | before: it ends on the first wide integer. After: the value is shown exactly, as a string, including for records the destination then refuses | inspector output | the display helper copies, never mutates; documented |
| CPU cost of the walk | two comparisons per integer scalar, folded into the same pass that builds the `Struct`; one linear scan per `json.Number` literal | benchi | no number is claimed. The benchi regression gate is not live, so the code PR runs benchi on a structured-payload pipeline with a standalone destination and reports medians with variance |
| Crash during any of this | nothing new is persisted; no position, state or wire format changes | n/a | n/a |

Concurrency: the check is a pure function of its input, with no shared state. The engine changes touch
the arch-v1 acker queue and the arch-v2 batch flags. The S2 and hole tests below cover them, and those
tests run under the race detector.

## Upgrade and rollback

- Upgrade: no serialized format changes. Positions, state, checkpoints and the wire format are the same,
  so an N → N+1 upgrade test has nothing new to cover. (The upgrade gate is not live anyway; see the
  process maturity table.) The behavior change is the halt described above, and the release notes list
  it under breaking behavior, together with #290's schema change, which the same commons bump brings in.
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

1. **commons unit tests**, a table over `ToProto`.
   - `int64`: 2^53−1 and -(2^53−1) are ok. 2^53, 2^53+1, -(2^53), 10^18, `MaxInt64` and `MinInt64` fail.
   - `uint64`: 2^53−1 is ok. 2^53 and 1<<63 fail.
   - `int`: 2^53 fails on 64-bit (not reachable on 386).
   - `json.Number`: `"9007199254740993"`, `"9007199254740993e0"`, `"1e20"` and `"1e999999"` fail, the
     last one under a tight time and allocation budget. `"1.5"`, `"12345678901234567.5"` (non-integer,
     unchanged), `"90071992547409.91e2"` and `"9007199254740991"` are ok.
   - `json.Number` grammar: `"0x20000000000001p0"`, `"+9007199254740993"`, `"Infinity"`, `"NaN"`,
     `"1_000"`, `"01"`, `"1."`, `".5"` and `""` fail with `ErrInvalidNumber`, never through `Float64()`.
   - `json.Number` edge cases: `"0"`, `"-0"`, `"0.000"` and `"0e99999999999999999999"` are ok (zero).
     `"1e99999999999999999999"` is out of range. `"1e-99999999999999999999"` is a non-integer and is
     unchanged. None of them panics or overflows.
   - Structure: a nested map, a `[]any` element, and a key versus `payload.before`/`payload.after`. The
     path is relative from `StructuredData.ToProto` and from the record root from `Record.ToProto`.
   - Errors: `errors.Is` with the sentinel, `errors.As` for the path, and an assertion that the error
     text does not contain the value.
   - Regression: `{id: int64(1<<53+1)}` through `ToProto` and `FromProto` today returns
     `9007199254740992` with a nil error. The test asserts an error.
2. **commons fuzz target** `FuzzStructuredDataIntegerRange(int64, uint64)`. The property: `ToProto`
   succeeds if and only if the value is in the safe range, and on success `FromProto(ToProto(v))` is a
   `float64` equal to `v`. Never a silent difference. The seed corpus (2^53±1, extremes) runs in the
   normal `test` job, which is how fuzz seeds are gated today. There is no property framework in the tree,
   and this test does not add one.
3. **Standalone processor reproduction in Conduit**, the reproduction for #3100.
   - Add a `passthrough` test guest under `pkg/plugin/processor/standalone/test/wasm_processors/`. It
     returns `SingleRecord(rec)` unchanged, and with `emit: wide-int` it sets `id = int64(1<<53+1)` on
     the records whose index is listed in its config. It is built by `build-test-processors.sh` against
     the commons version in the `conduit` module.
   - Host leg: `wasmProcessor.Process` on `{key: {id: int64(1<<53+1)}, payload.after: {id: ...}}`. On
     `main` it returns a `SingleRecord` with `id == float64(9007199254740992)`, which demonstrates the bug.
     After the fix it returns an `ErrorRecord` that matches the code and `ErrIntegerNotRepresentable`,
     with path `key.id`. Control: `int64(1<<53-1)` passes as `float64(1<<53-1)`.
   - Host batch attribution: three records with index 1 bad. Result: `[processed, ErrorRecord]`, and the
     funnel retries index 2. Index 0 is not nacked. A variant with a `time.Time` in index 1 covers #3127.
   - Guest leg: `emit: wide-int` on output 1 of 3. After the fix the result is
     `[processed, ErrorRecord, processed]`: no command error, and the host does not round.
4. **Destination leg, plugin level**: extend `pkg/plugin/connector/standalone/test/testplugin` so that its
   destination records what it received. A record with `id = int64(1<<53+1)` must not reach the plugin.
   `Write` returns an error with the code, and the plugin sees zero records.
5. **Destination node, arch-v1** (`pkg/lifecycle/stream`, standalone test plugin or the mock
   destination):
   - The refused message is not sent and is not in `openMsgTracker`.
   - The next record is written.
   - With a tolerant DLQ, the refused record is nacked in order after the earlier acks.
   - With the default DLQ, the pipeline halts with the code.
   - **S2 deadlock test:** the destination batches with `sdk.batch.size: 3` and `sdk.batch.delay: 0`.
     Write two good records, then a refused one, then one more good record. The pipeline must neither hang
     nor reorder, under a deadline and the race detector. Run it for nack-and-continue and for the halt
     fallback.
   - **Halt fallback stops cleanly:** a refused record with the fallback on. The pipeline reaches a
     stopped or degraded state within a deadline. `openMsgTracker.Wait()` in the source node returns, and
     the refused record is nacked, not acked.
   - **`lastPosition` is not advanced:** a refused record is the last one before stop. `Stop` receives
     the last _written_ position, and the SDK destination's `Stop` returns without waiting out
     `stopTimeout` (assert on elapsed time and on the absence of the "did not encounter expected last
     position" warning).
6. **Destination task, arch-v2** (`pkg/lifecycle-poc/funnel`):
   - A batch of five with index 1 refused: index 1 is nacked, and 0 and 2–4 are delivered in order as
     contiguous sub-batches.
   - **Ack error after a hole:** in the same batch, the destination returns an ack error for index 3.
     Index 3 must be nacked and index 2 must be acked. On a slice with a hole, this test fails.
   - The retry-bound counters are not exhausted by the retried runs.
7. **Pipeline level, both engines**: a mock source with a wide-int record, then a standalone passthrough
   processor, then a built-in destination.
   - Default DLQ: the pipeline reaches **Degraded** (not a recovery loop) with the code, within a
     deadline. The source reads the record once, not repeatedly, and the record is not acked. A manual
     restart with the same source halts again. This test fails without the `FatalError` wrap.
   - DLQ window 2, threshold 1: the record is in the DLQ with the code in its metadata, the pipeline keeps
     running, and the next record (`id = 1<<53-1`) reaches the destination exactly.
8. **Inspector**: `InspectProcessorIn` on a wide-int record does not end the stream, and it shows
   `"9007199254740993"`. A second concurrent session sees the original record unmodified.
9. **Chaos**: these changes touch nack paths in the destination nodes, which are data-path trees, so the
   required `tests/chaos (race, x3)` job runs. No new SIGKILL scenario is needed: nothing is persisted
   between the nack decision and the existing DLQ and ack machinery, which the chaos suite already
   covers.

## Consequences

- Silent corruption at the plugin boundary becomes a loud, coded failure. That is the trade DeVaris
  accepted for v0.21.
- Some pipelines that worked before will halt after the upgrade. That includes some whose values were
  exact but ambiguous at a double-binding receiver. This is the point of the change, but it needs a clear
  release note and runbook.
- The plugin-to-Conduit legs stay unprotected until plugins are rebuilt. The grpc-client connector stays
  unprotected until its own fix. The certified v0.21 connectors are rebuilt as part of the train.
- commons gains one sentinel, one error type and one validation method. The engines gain a refused-message
  path in the arch-v1 acker and a validate-then-split step in the arch-v2 destination task.
- The processor-side fixes also close #3127.
- Step 2 is left as a recommendation for an ADR. It is not a commitment.

## Decisions

Decided by DeVaris on 2026-10-15. Each one is the recommendation this document made.

1. **Rule:** the safe-integer range ±(2^53−1). Exactness is rejected.
2. **Destination side:** nack under the DLQ policy, together with the arch-v1 ordered nack (via
   `DestinationAckerNode`, never advancing `lastPosition`) and the arch-v2 validate-then-split. If
   either cannot land in v0.21, use the halt fallback. Both options require Conduit to wrap the coded
   refusal in `cerrors.FatalError` at its error-return sites. Release rule: `conduit-processor-sdk`
   does not ship its commons bump without the guest per-index fix.
3. **Versioning:** release `conduit-commons` as v0.9.0, and Conduit v0.21 takes #290 in the same bump.
4. **Step 2:** open the ADR and a `conduit-connector-protocol` versioning issue now. Implementation
   comes after v0.21.

## Related

- #3100: the issue. #3006: the JDBC sink lane where it was found.
- #3127: arch-v2 nacks record 0 instead of the bad record when standalone processor conversion fails.
- `ConduitIO/conduit-commons#290`: breaking schema change already on commons `main`.
- `ConduitIO/conduit-connector-mysql#193`: exact delete keys in the MySQL destination.
- `ConduitIO/conduit-connector-postgres#402`: the same root cause on the Postgres JSON path.
- #2817: archived `hamba/avro`, relevant to option C.
- RFC 7493 (I-JSON), section 2.2: the interoperable integer range.
- `docs/design-documents/20261012-standalone-plugin-env.md`: the precedent for adding protocol behavior
  through client options without a wire change.

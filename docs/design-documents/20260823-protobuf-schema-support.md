# Protobuf schema support: decoding Confluent Schema Registry Protobuf topics

## Summary

Conduit cannot read Protobuf-encoded Kafka topics today. `pkg/schemaregistry/toschema/sr.go`
maps a Confluent Protobuf schema type to `0` with the comment `// not supported yet`, and
`conduit-commons`'s `schema.Type` has exactly one member (`TypeAvro`). The failure is loud, not
silent: `Schema.Serde()` fails to find a factory and returns a wrapped `ErrUnsupportedType`, so a
Protobuf topic is unreadable rather than corrupted. This is a capability gap, not a
data-integrity bug — the urgency is "we lose deals/users who have Protobuf topics," not
"pipelines are silently wrong."

**Recommendation: add read-only Protobuf decoding**, mirroring the existing Avro architecture —
a new `TYPE_PROTOBUF = 2` wire value in `conduit-commons`'s `schema.proto`, a new
`conduit-commons/schema/protobuf` `Serde` implementation that compiles the Confluent-stored
`.proto` source at runtime via `github.com/bufbuild/protocompile`, and a new built-in
`protobuf.decode` processor in `conduit` mirroring `avro.decode`. Encoding to Protobuf (the
write side) and JSON Schema (the third Confluent format, stubbed identically) are **explicitly
out of scope** — see "Scope split" and "Does this generalize to JSON Schema?" below for why, and
why that's the right cut.

Rough estimate: **3–5 weeks of solo-maintainer-plus-Claude engineering**, split into six
independently reviewable slices (PB-0 through PB-6 below). That is the basis for checking the
maintainer judgement that this is higher-value and cheaper than the Arrow/columnar record
re-platform under discussion on `docs/adr-columnar-record-archv2` — see "How it was found."

## How it was found / why this doc exists

This gap surfaced while evaluating a much larger proposal: re-platforming the record model onto
Arrow (ADR draft on branch `docs/adr-columnar-record-archv2`). The maintainer's judgement was
that closing the Protobuf gap is higher value and cheaper than the re-platform, and asked for
this design doc specifically so that judgement can be checked against a real scoped plan rather
than taken on faith.

The roadmap and strategy grounding for "higher value" is concrete, not vibes:

- `STRATEGY.md`'s "two bets that decide everything" name the Kafka Connect migration path +
  Debezium-class CDC as bet #2. A large share of real Kafka Connect deployments use Confluent
  Schema Registry with Protobuf (Debezium's `ProtobufConverter`, and Protobuf is a common
  first-class choice for hand-written producers). A Conduit that can migrate a pipeline's config
  but then cannot read the topic's actual bytes is not a Kafka Connect replacement for that
  deployment.
- `ROADMAP.md`'s Phase 2 "Enterprise correctness" section already lists, verbatim: "Confluent
  Schema Registry wire compatibility (Avro, Protobuf, JSON Schema)." This doc is the design work
  behind the Protobuf third of that line item.

## Problem

Verified directly against the code, not from memory:

- `pkg/schemaregistry/toschema/sr.go`'s `SrSchemaType` (converts a schema-registry-reported
  type into `conduit-commons`'s `schema.Type`):

  ```go
  case sr.TypeProtobuf:
      return 0 // not supported yet
  case sr.TypeJSON:
      return 0 // not supported yet
  ```

- `pkg/schemaregistry/fromschema/sr.go`'s `SrSchemaType` (the reverse direction) has no
  Protobuf case at all — anything other than `schema.TypeAvro` falls through to
  `return sr.SchemaType(-1) // unknown`.
- `conduit-commons@v0.6.0`'s `schema/schema.go` declares `type Type int32` with exactly one
  constant, `TypeAvro Type = iota + 1` (so `TypeAvro == 1`, not `0` — the `return 0` above is a
  genuinely invalid/unknown `Type`, not an accidental alias for Avro), and `KnownSerdeFactories`
  is a `map[Type]SerdeFactory` with exactly one entry, keyed on `TypeAvro`.
- `conduit-commons@v0.6.0`'s `proto/schema/v1/schema.proto` declares the wire enum with room
  already reserved for this: `enum Type { TYPE_UNSPECIFIED = 0; TYPE_AVRO = 1; }`.
- The failure mode is loud, confirmed by reading `Schema.Serde()`:

  ```go
  factory, ok := KnownSerdeFactories[s.Type]
  if !ok {
      return nil, fmt.Errorf("failed to get serde for schema type %s: %w", s.Type, ErrUnsupportedType)
  }
  ```

  A record encoded against a Protobuf (or JSON Schema) schema fails with a clear,
  already-wrapped `ErrUnsupportedType` error naming the schema type. It does not silently
  misdecode, truncate, or coerce. This matters for how this doc is framed: it is not a
  data-integrity fix under the invariants in `CLAUDE.md` (nothing is being corrupted today,
  because nothing is being decoded at all), it is closing a capability gap that blocks adoption
  for any deployment that has Protobuf topics.

## Goals / non-goals

**Goals:**

- Decode Confluent-wire Protobuf-encoded records (magic byte, schema ID, message-index prefix,
  Protobuf payload) into `opencdc.StructuredData`, through the existing schema-registry client
  and the existing `schema.Schema` / `Serde` abstraction, with a new built-in `protobuf.decode`
  processor mirroring `avro.decode`.
- Resolve Confluent schema references (`.proto` `import`s of other registered subjects).
- Define an explicit, documented policy for the lossy edges of the Protobuf → structured-data
  mapping (unknown fields, unset-vs-zero, `Any`), per invariant 6 ("schema handling never
  silently mangles data").

**Non-goals (this doc, explicitly):**

- **Encoding to Protobuf** (the `protobuf.encode` mirror of `avro.encode`). See "Scope split"
  below.
- **JSON Schema** (`sr.TypeJSON`, stubbed identically to Protobuf today). See "Does this
  generalize to JSON Schema?" below.
- **Confluent Platform 8.x GUID-based schema IDs** (`sr.SubjectSchema.GUID`, carried in a Kafka
  record header rather than the payload prefix, per the field's doc comment in
  `franz-go/pkg/sr@v1.8.0`'s `api.go`). Not investigated in this doc; flagged as a follow-up
  question, not assumed in or out of scope.
- Any change to `conduit-connector-protocol`. Nothing here touches that repo.

## Constraints

- **Solo maintainer.** DeVaris + Claude. Any option that means owning a Protobuf parser/compiler
  ourselves competes against everything else on the roadmap, indefinitely — exactly the reasoning
  that shaped the recent Avro-codec-replacement decision
  (`docs/design-documents/20260823-avro-codec-archived-decoder-advisories.md`).
- **Multi-repo blast radius.** This spans three repos with different version-bump implications:
  - `conduit-commons`: the wire enum (`schema.proto`), the `schema.Type` Go constant, the new
    `schema/protobuf` `Serde` implementation, and `KnownSerdeFactories`.
  - `conduit`: the new built-in `protobuf.decode` processor, wiring the existing
    `schemaregistry.Registry` into the new package's reference resolver.
  - `conduit-processor-sdk`: its own `pprocutils/v1` wire protocol (host ⇄ standalone WASM
    processor) carries `schema.Type` as a raw `int32` across the process boundary — verified
    below, in "Compatibility." This is a **separate protocol from
    `conduit-connector-protocol`**; nothing here touches connector protocol.
- **We were just burned by an archived codec** (`conduit-commons` issue #2817, `hamba/avro`).
  Library longevity for whatever compiles `.proto` source at runtime is a first-class selection
  criterion in this doc, not an afterthought — see "Options considered."
- **Invariant 6 applies**: unknown fields, type mismatches, and drift follow a configured policy,
  never silent coercion or truncation. The mapping-to-`StructuredData` section below exists to
  make that policy explicit for Protobuf's specific lossy edges.
- **No connector-protocol change.** `schema.proto` lives in `conduit-commons`, consumed by the
  schema-registry client and (per the constraint above) the processor-SDK's own host-function
  protocol — neither is `conduit-connector-protocol`.

## Options considered — runtime `.proto` compilation library

Confluent stores Protobuf schemas as `.proto` **source text**, not compiled descriptors (the
same `sr.Schema.Schema string` field franz-go documents as "the actual unescaped text of a
schema," used uniformly for Avro, Protobuf, and JSON Schema — verified in
`franz-go/pkg/sr@v1.8.0`'s `api.go`). Decoding therefore requires compiling `.proto` text into a
descriptor at runtime, against which a wire payload can be parsed reflectively. This is the
single highest-risk piece of this design, per the same category of risk the Avro-codec doc just
worked through for a decoder library: it runs on untrusted, network-supplied input.

### A. `github.com/bufbuild/protocompile` — recommended

Verified directly (GitHub API, not from memory, checked 2026-08-23):

- **Actively developed, not archived.** 343 stars, pushed as recently as 2026-08-21 (two days
  before this doc). Commit history shows multiple commits/week sustained over more than a year,
  from Buf Technologies staff (a funded company, not a solo maintainer) plus routine Dependabot
  bumps. Apache-2.0.
- **The officially designated successor to the previous de facto standard.**
  `github.com/jhump/protoreflect`'s `desc/protoparse` package — the library that used to be the
  answer to "compile `.proto` source at runtime in Go" — now carries its own godoc deprecation
  notice: "This protoparse package is now just a thin veneer around a newer replacement
  parser/compiler: `github.com/bufbuild/protocompile`. Users are highly encouraged to directly
  use protocompile instead of this package." protocompile's own README confirms the same
  relationship from the other side, describing itself as the "spiritual successor" to
  `protoparse`. This is not a close call between peers.
- **Structured, position-aware errors — not a bag of strings.** `reporter.ErrorWithPos` is a
  real interface (`reporter/errors.go`) carrying file/line/column via `ast.SourceSpan`, plus a
  sentinel `ErrInvalidSource`. A malformed schema produces an actionable error, not an opaque
  failure.
- **Panics from compilation are recovered, not fatal.** `compiler.go` recovers panics per-file
  during `Compile` and wraps them in a typed `PanicError{File, Value, Stack}` rather than letting
  a malformed or adversarial schema crash the process. This is a materially better
  malformed-input story than the unrecovered decoder panics that motivated the `hamba/avro`
  replacement — exactly the property this doc's constraints say to check for, checked.
- **Context-aware, so compilation is boundable.** `Compiler.Compile(ctx context.Context, files
  ...string) (linker.Files, error)` takes a `context.Context`, so a compile can be given a
  deadline — relevant for bounding CPU spent on a pathological schema during a cache-miss
  compile (see "Design — decode path" below).
- **Cyclic imports are detected, not infinite-looped.** The repo carries an
  `internal/toposort` package and explicit test fixtures for import cycles
  (`cycle.proto`/`cycle_dependency.proto`/`cycle_long.proto` with recorded expected stderr),
  confirming cyclic `.proto` imports are surfaced as compile errors.
- **Real caveat, checked rather than glossed over:** the last tagged release is `v0.14.1`
  (2024-08-30) — nearly two years stale by tag — despite the active commit history above. This
  looked, at first glance, like exactly the kind of "quietly stopped maintaining it" signal that
  the Avro doc was burned by. Checked further: Buf's own flagship CLI (`bufbuild/buf`) pins
  `github.com/bufbuild/protocompile v0.14.2-0.20260811170554-36b92ff45e08` — a **pseudo-version
  off a commit from 2026-08-11**, not the 2024 tag. That means the org that owns this library
  runs its own product against live `main`, not the stale tag. This reads as a deliberate
  Buf-wide versioning style (tag rarely, consumers pin pseudo-versions), not neglect — but it is
  a real, atypical governance quirk worth stating plainly: adopting protocompile means pinning a
  pseudo-version the same way Buf itself does, and re-checking that pin periodically rather than
  watching for tagged releases the normal way.
- **No new heavy dependency for the decode-to-struct half.** `google.golang.org/protobuf`
  (`dynamicpb`, `protoreflect`) is already a direct dependency of `conduit` at `v1.36.12`
  (verified in `go.mod`) — it's what the connector protocol itself is built on. protocompile's
  output (`protoreflect.FileDescriptor`) plugs directly into `dynamicpb.NewMessage` +
  `proto.Unmarshal` from that existing dependency. protocompile itself, plus its own modest
  transitive deps, is the only genuinely new addition.

### B. `github.com/jhump/protoreflect`'s `desc/protoparse` — rejected

Rejected on the same evidence that recommends A: its own author has deprecated it in favor of
protocompile, in-package, in exactly these words. Picking the deprecated wrapper over the thing
it wraps has no upside and reintroduces the exact "watch a smaller, less-maintained shim forever"
burden the Avro-codec decision just spent a whole doc getting out of, for zero benefit — `jhump`
(the same author) is also one of protocompile's top contributors (183 commits, #2 by count,
verified via the GitHub contributors API), so this isn't even two independently maintained
projects to hedge across; it's one project and its own author's deprecated wrapper around it.

### C. Hand-roll a minimal `.proto` source parser scoped to Confluent's needs — rejected

Reinventing even a subset of the `.proto` grammar — imports, options, proto2 vs. proto3
presence semantics, well-known-type recognition — is a correctness-critical, ongoing commitment
of exactly the kind the Avro-codec doc rejected for forking a _decoder_ of a format simpler than
Protobuf's IDL. A subtly wrong hand-rolled parser is a direct invariant-6 risk (silent
misinterpretation of a schema, not just a crash), which is a worse failure mode than the
"unsupported type" error this doc is trying to replace. Reject.

### D. Require pre-compiled `FileDescriptorSet`s only; no runtime source compilation — rejected as not matching reality

This would sidestep the whole runtime-compilation risk surface, but it doesn't solve the actual
problem: Confluent Schema Registry stores and returns `.proto` **source text** for registered
Protobuf schemas (verified above), not compiled descriptors. A design that only accepts
pre-compiled descriptors would not decode real Confluent-registered Protobuf topics — the exact
target of this doc. Rejected as the primary design, but worth keeping in mind as an emergency
descoping lever: if protocompile integration turns out to be materially harder than estimated,
"decode only when the operator supplies a pre-compiled descriptor out of band" is a smaller,
still-useful fallback rather than shipping nothing.

## Design — decode path

### Wire format: the message-index prefix (the detail that most often breaks Protobuf/Confluent interop)

Avro's Confluent wire format is `[magic byte][4-byte big-endian schema ID][Avro payload]`.
Protobuf's is `[magic byte][4-byte big-endian schema ID][varint message-index array][Protobuf
payload]` — an extra field with no Avro analog, because a single `.proto` file can declare
multiple top-level messages and the index says which one this payload is.

The existing Avro decode path does not generalize to this naively. `internal/decoder.go` (the
built-in Avro processor's decoder) does:

```go
id, data, err := (&sr.ConfluentHeader{}).DecodeID(b.Bytes())
```

`DecodeID` strips only the magic byte and the 4-byte ID; everything after is treated as payload.
That is correct for Avro (no message-index field exists) and would be **wrong** for Protobuf if
reused unmodified — the first bytes of `data` would be the varint index, not payload, and
`proto.Unmarshal` against them would either fail confusingly or, worse, occasionally succeed
against garbage. A Protobuf decoder needs a second step before unmarshaling.

The good news, verified in `franz-go/pkg/sr@v1.8.0`'s `serde.go`: this parsing already exists
and doesn't need to be hand-rolled. `ConfluentHeader.DecodeIndex(b []byte, maxLength int)
([]int, []byte, error)` implements exactly this varint-array format, including the documented
"length 0 is a shortcut for length 1, index 0" case and returning `ErrBadHeader`/`ErrNotRegistered`
on malformed input, matching `AppendEncode`'s encoder-side counterpart. `franz-go/pkg/sr` is
already a direct dependency (`v1.8.0`, pinned in `go.mod`).

What isn't free: **resolving the index into "which message descriptor"** is Protobuf-specific
logic that has to be written and tested. Per Confluent's documented convention, the index array
descends into nested message declarations — the first int selects a top-level message in the
compiled file, and each subsequent int descends one level into that message's nested message
types. This needs an explicit walk over the linked `protoreflect.FileDescriptor`
(`.Messages().Get(idx)`, then repeatedly `.Messages().Get(idx)` on the resulting nested message
type) with its own tests, including a **nested** case — flagged explicitly because most
real-world single-message Confluent Protobuf schemas only exercise the flat, single-level index
and a nested-descent bug could ship unnoticed without a fixture that forces it.

### Schema compilation & caching

`toschema.SrSchema` already flows the raw schema bytes through unmodified
(`Bytes: []byte(s.Schema.Schema)`); only the `Type` switch is missing. The new
`schema/protobuf.SerdeFactory.Parse` receives that raw `.proto` source and compiles it via
`protocompile.Compiler{Resolver: ...}.Compile(ctx, filename)`.

Compilation is not free, so it must go through the existing per-fingerprint cache in
`conduit-commons/schema/schema.go`'s `Schema.Serde()` (`globalSerdeCache`, keyed on the Rabin
fingerprint of the schema bytes) — no changes needed there; a Protobuf `SerdeFactory.Parse` plugs
into the same generic caching path Avro's does today.

Because compilation runs on network-supplied schema text, a **bounded compile timeout** via
`context.Context` is recommended as a non-optional default (unlike `conduit-commons`#278's
opt-in stance on Avro input-size bounding) — a compile-time DoS would poison the shared,
process-global `Serde` cache for every pipeline using that schema, not just one call. A
conservative starting value (e.g. 5s) is proposed; this is flagged as an open question for
DeVaris in "Rollout" rather than settled unilaterally here, since it trades off against real but
unmeasured large-schema compile times.

### Schema references

Confluent Protobuf schemas can `import` other registered subjects. `sr.Schema.References
[]SchemaReference{Name, Subject, Version}` already exists in `franz-go/pkg/sr@v1.8.0` (verified
in `api.go`) and already flows through `toschema.SrSchema`/`fromschema.SrSchema` today —
unused, since only Avro (which doesn't use registry-level references this way) is supported.

Resolution needs no new schema-registry client capability: `schemaregistry.Registry`'s existing
`SchemaBySubjectVersion(ctx, subject, version)` (verified in `pkg/schemaregistry/registry.go` and
`client.go`) is exactly what each `SchemaReference{Subject, Version}` needs to fetch the
referenced schema, recursively.

The architectural wrinkle: `SerdeFactory.Parse func([]byte) (Serde, error)` has no registry or
context parameter today — Avro's `Parse` is a pure function of bytes because Avro schemas in this
codebase carry no registry-level references. Two ways to close that gap:

- **(a) Extend `SerdeFactory.Parse`'s signature** to accept a resolver callback (or `ctx` plus a
  small resolver interface). This is a Go API break for an exported func-typed struct field in
  `conduit-commons`, but a narrow one: `Schema.Serde()` is its only caller inside `schema.go`
  itself (not independently verified across every `ConduitIO/*` repo — flagged as a migration-PR
  check, mirroring the Avro doc's `go mod why` sweep). **Recommended** — keeps `Serde` the single
  place that knows how to turn schema bytes plus whatever context it needs into a working codec,
  consistent with how the package already centralizes Avro's `Parse`.
- **(b) Resolve references eagerly before calling `Parse`**, flattening everything into a
  self-contained `FileDescriptorSet` the factory can parse context-free. Pushes the
  resolution walk (and its cycle/depth-guard responsibility — see below) up into the caller
  instead of the `schema` package, splitting "how to fetch a schema" from "how to compile
  it" less cleanly.

Recommendation: (a), flagged in "Rollout" as needing explicit sign-off since it's a
`conduit-commons` Go API change, not a pure addition.

**Reference cycles are a new failure mode, not covered by protocompile's own cycle detection.**
protocompile's cyclic-import detection (see Option A above) only sees whatever a single `Compile`
call's `Resolver` returns — it has no visibility into a caller-driven loop of registry fetches
across `A imports B imports A` at the _subject_ level, which is exactly what this resolver does.
The resolution walk needs its own explicit visited-set-plus-max-depth guard and a dedicated
adversarial test (a fixture with `A → B → A`), independent of protocompile's internal protection.

### Message decoding: Protobuf → `opencdc.StructuredData`

Given a linked `protoreflect.MessageDescriptor` (from the compiled file + resolved message
index) and the raw payload bytes: `dynamicpb.NewMessage(descriptor)` + `proto.Unmarshal(payload,
msg)` (both from the already-a-dependency `google.golang.org/protobuf`) produces a
`protoreflect.Message` to walk into `map[string]any`. Per-construct policy, worked through
because invariant 6 requires it be explicit rather than implied by whatever the reflection API
happens to do:

- **Scalars** — direct Go-type mapping (`int32`/`int64`/`uint32`/`uint64`/`float32`/`float64`/
  `bool`/`string`). No ambiguity.
- **`bytes`** — `[]byte`, matching `opencdc`'s own native representation.
- **Enums** — recommend the **name** (string), matching `protojson`'s canonical mapping (the
  officially documented Google convention) and matching what's legible to a human or an agent
  reading the structured data. Trade-off worth stating explicitly: the enum's _number_ is the
  wire-stable identity — a schema evolution that renames a value keeps the number but changes the
  name a downstream consumer sees. Recommend defaulting to name but treating "name vs. number" as
  a documented, and eventually configurable, processor option rather than a silent, unstated
  choice — this is exactly the kind of edge this doc exists to pin down rather than leave to
  whatever `protoreflect` happens to expose first.
- **`oneof`s** — flatten: emit only the set field, under its own field name, no synthetic
  wrapper key. This matches `protojson`'s canonical behavior and, closer to home, matches how
  this codebase's own Avro `unionResolver` already resolves a union to "just the concrete value"
  rather than a tagged wrapper (`conduit-commons/schema/avro/union.go`) — consistent prior art,
  not a new convention invented for Protobuf.
- **Maps** — native `map[string]any`. Proto map keys are constrained to string/int/bool types by
  the spec itself, all valid `StructuredData` keys — no ambiguity comparable to Avro's looser map
  key typing.
- **Well-known types** — recommend matching the existing Avro codec's convention of native Go
  types over `protojson`'s canonical JSON-string encodings, for consistency across the two
  formats' output shape (verified precedent: `schema/avro/extractor.go`'s
  `timeType = reflect.TypeFor[time.Time]()`):
  - `google.protobuf.Timestamp` → `time.Time`
  - `google.protobuf.Duration` → `time.Duration`
  - `google.protobuf.Struct`/`Value`/`ListValue` → `map[string]any`/`any`/`[]any` — structurally
    already exactly what `StructuredData` wants; the closest thing Protobuf has to a native JSON
    blob.
  - Wrapper types (`Int32Value`, `StringValue`, `BoolValue`, ...) → the unwrapped scalar, or
    `nil` if unset — these types exist specifically to give a scalar explicit presence, so
    unwrapping (rather than leaving a nested `{value: x}` shape) is the useful behavior.
  - `google.protobuf.Any` — the genuinely hard one. Generic resolution requires a descriptor pool
    containing _every possible_ packed type, not just the topic's own schema and its declared
    references — out of reach for a per-topic decoder. **Recommend shipping `Any` as an explicit
    raw form** (`{"type_url": ..., "value": []byte(...)}`, undecoded) rather than attempting
    partial resolution that silently succeeds for known types and silently fails for unknown
    ones. This is a documented limitation, not a silent drop — the invariant-6-relevant
    distinction.
- **Unset optional vs. zero value** — only distinguishable where the field has explicit
  presence: proto3 `optional` (synthetic oneof), proto2 fields, or singular message-type fields
  (which always have presence in proto3). Detectable via `Message.Has(fieldDescriptor)`. For a
  plain, non-`optional` proto3 scalar field, the wire format itself cannot distinguish "not sent"
  from "sent as zero" — this is an inherent Protobuf limitation, not a Conduit gap, and needs to
  be stated plainly in the processor's docs so it isn't filed as a bug later.
- **Unknown fields** — fields present on the wire but absent from the resolved descriptor (e.g.
  the producer is on a newer schema version than what got resolved). `dynamicpb` preserves these
  rather than silently dropping them (`Message.GetUnknown()`). Per invariant 6, recommend a
  configurable policy mirroring the halt/DLQ/evolve language `CLAUDE.md` already uses for schema
  drift generally, **defaulting to reject** (decode error) rather than silently omitting or
  guessing a mapping. Flagged as a policy decision for explicit sign-off in "Rollout," not a
  purely technical call this doc can make unilaterally.
- **Type mismatches** (payload doesn't parse cleanly against the resolved descriptor — wrong ID
  mapped, corrupted payload) — `proto.Unmarshal` returns a wire-format error in the normal case,
  which becomes the same loud, wrapped error every other decode failure produces. Protobuf's
  wire format is largely self-describing per field tag, so cross-schema decodes tend to fail
  loudly rather than silently coerce — but this is a "tends to," not a proof for every possible
  byte sequence, so the built-in processor's test suite needs a fixture that actually asserts
  this rather than assuming it.

## Compatibility: `schema.Type` wire enum (`TYPE_PROTOBUF = 2`)

proto3 enums are wire-compatible by design — an unrecognized numeric value round-trips through
the wire without the protobuf layer itself erroring. The question is what _application_ code on
either side of a version skew does with it. Checked in both directions, not assumed:

- **Old code, new value** (an older `conduit-processor-sdk`/`conduit-connector-sdk` build,
  pinned to a `conduit-commons` version that only knows `TypeAvro`, receiving a schema of type
  `2` from a newer engine). Verified in `conduit-processor-sdk`'s
  `pprocutils/v1/fromproto/schema.go`:

  ```go
  Type: schema.Type(req.Type),
  ```

  a raw `int32`-to-`Type` cast with **no validating switch** at this wire boundary. Old-vintage
  code simply carries the value forward as `schema.Type(2)` — out of range from its own
  `conduit-commons` pin's perspective. When that value reaches `Schema.Serde()`, the
  `KnownSerdeFactories` map lookup misses and returns today's existing, already-shipped
  `ErrUnsupportedType` — the _same_ loud failure this doc is fixing for up-to-date builds, not a
  new failure mode, and not silent corruption. This is a verified code path, not an assumption.
- **New code, old value** — trivial. `TYPE_AVRO = 1` / `TYPE_UNSPECIFIED = 0` keep their existing
  meaning; this is a purely additive change to the enum, not a renumbering.
- `Type.String()`'s stringer-generated bounds check and `Type.UnmarshalText`'s explicit
  `Type(int)` fallback (both verified in `conduit-commons@v0.6.0`) already handle unrecognized
  values without panicking. This pattern reads as having been designed with future values in
  mind; it needs no change beyond regenerating the stringer table when `TypeProtobuf` is added.
- **Rollout policy**: additive-only wire changes don't need `CLAUDE.md`'s two-minor-version
  deprecation window — that policy governs removing or changing existing contract surface, not
  growing an enum. Consuming repos (`conduit`, `conduit-processor-sdk`, `conduit-connector-sdk`,
  and any out-of-tree connector/processor vendoring an older `conduit-commons`) simply won't gain
  the new decode _capability_ until they bump their dependency — the interim failure mode is
  today's status quo (`ErrUnsupportedType`), not a regression.
- **What I could not verify**: whether any out-of-tree connector or processor outside
  `ConduitIO/*` does unchecked arithmetic on the raw `schema.Type` int (e.g. assumes only values
  `0`/`1` exist and indexes a fixed-size array) rather than going through the map lookup or the
  stringer's bounds-checked path. A repo-wide sweep — the same `go mod why`-style check the
  Avro-codec doc's migration PR used — belongs in this change's migration PR, not this doc.

## Scope split: read now, write deferred

Decoding is self-contained per record: fetch the schema by ID (already works), compile/resolve
it, decode the bytes against it. Encoding needs a schema to encode _against_ — and that's a
structurally different problem for Protobuf than for Avro.

For Avro, the built-in `avro.encode` processor calls
`KnownSerdeFactories[schema.TypeAvro].SerdeForType(sd)` (verified in
`pkg/plugin/processor/builtin/impl/avro/internal/encoder.go`), which reflects over a Go value
(`schema/avro/extractor.go`) to _infer_ an Avro schema, then registers it on the fly via
`CreateSchema`. That inference story doesn't carry over to Protobuf: a Go struct has no `.proto`
message identity, no stable field numbers, and none of the wire-compatibility guarantees a real
`.proto` schema encodes. Producing Confluent-wire Protobuf that anything else can read requires a
real, pre-authored `.proto` schema with deliberately assigned field numbers — a
schema-authoring/registration workflow, not a decode-mirror-image of this doc's work. The task
that scoped this doc already names this correctly: "registering new Protobuf schemas is its own
workflow."

**Recommendation: ship decode-only.** Treat Protobuf encoding as a separate, future design doc
once there's a concrete driving use case (a destination that specifically needs to _produce_
Confluent-wire Protobuf), rather than building write support for symmetry's own sake. This also
matches what `ROADMAP.md`'s Phase 2 line is really about for the Kafka-Connect-migration bet:
_reading_ what existing Debezium/Kafka-Connect-with-Protobuf-converter deployments already
produce, not authoring new Protobuf schemas from Conduit.

## Does this generalize to JSON Schema (the third stub)?

Partially, and it's worth being precise about which half.

**What generalizes cleanly:** the `schema.Type` enum / `KnownSerdeFactories` map /
`SerdeFactory{Parse, SerdeForType}` seam. JSON Schema would be `TYPE_JSON = 3` in the same wire
enum, another `KnownSerdeFactories` entry, another `Serde` implementation package
(`conduit-commons/schema/jsonschema`, alongside the new `schema/protobuf`), plugged into the same
decode entry point. This design doesn't foreclose that — if anything, adding a second real
`SerdeFactory` implementation (Protobuf, alongside Avro) is what retroactively earns the seam's
existence under `CLAUDE.md`'s "no speculative generality — interfaces earn their existence with
two real implementations" rule, and sets up JSON Schema as a reasonable third.

**What does not generalize, and shouldn't be forced to:**

- JSON Schema's Confluent wire format carries the same magic-byte-plus-ID prefix but **no
  message-index** — a JSON Schema document normally describes exactly one document shape, with
  no analog to "which message in the file." The message-index handling this doc builds for
  Protobuf is specific to Protobuf and should not be reused or generalized for JSON Schema.
- JSON Schema's own mapping problem is a different shape of hard. There's no
  runtime-compilation-of-an-IDL risk analogous to protocompile — JSON Schema documents are
  themselves JSON, no separate grammar to compile — but `$ref`/`$dynamicRef` resolution,
  `additionalProperties`, and `oneOf`/`anyOf` discriminated-union handling are their own
  nontrivial, JSON-Schema-specific mapping questions that this doc's Protobuf-specific analysis
  (oneofs, enums, WKTs) doesn't answer.

Net: this design's architecture is compatible with JSON Schema arriving later as a third
`Serde`, but JSON Schema needs its own design doc for its own mapping questions — it is not
in scope here and this doc should not be read as having quietly resolved it.

## Failure-mode analysis

1. **Malicious/malformed `.proto` source.** Mitigated by protocompile's `ErrorWithPos` /
   `PanicError` handling (verified above — panics are recovered per-file, not fatal). Add a fuzz
   target over the schema-bytes → `Serde.Parse` path once this ships, following the existing
   `conduit-commons` `Fuzz*` convention (9 packages already carry fuzz targets per `CLAUDE.md`'s
   process-maturity table; this becomes the 10th).
2. **Pathological compile cost** (huge or deeply nested schema; CPU exhaustion via a slow
   compile poisoning the shared cache path). Mitigated by a bounded compile timeout via
   `context.Context` (see "Design"). Concrete default needs DeVaris sign-off — flagged in
   "Rollout," not assumed.
3. **Reference-resolution cycles across registry subjects** (`A` imports `B` imports `A`). Not
   covered by protocompile's own cycle detection (see "Schema references" above — its detection
   only sees one `Compile` call's resolver universe, not a caller-driven cross-subject fetch
   loop). Mitigated by an explicit visited-set-plus-max-depth guard in the reference resolver,
   with a dedicated adversarial test fixture.
4. **Message-index parsing bugs** — explicitly the detail the task that scoped this doc called
   out as "the detail that most often breaks Protobuf/Confluent interop." Mitigated by relying on
   franz-go's already-implemented, already-tested `ConfluentHeader.DecodeIndex` rather than
   hand-rolling varint parsing, plus fixtures covering the zero-length shortcut, a flat index, and
   a **nested** index (descending two or more levels) — called out specifically because most
   real-world Confluent Protobuf schemas are single-message/flat and would not exercise a nested
   decode bug by accident.
5. **Unknown-field / unset-vs-zero-value ambiguity reaching a downstream consumer as if it were
   meaningful data.** Mitigated by the explicit policy in "Message decoding" (reject-by-default
   for unknown fields; a documented, inherent limitation for zero-vs-unset on non-explicit-presence
   fields). Squarely invariant-6 territory; the migration PR's failure-mode analysis should
   restate this against the actual diff, per `CLAUDE.md`.
6. **`google.protobuf.Any` payloads silently losing fidelity.** Mitigated by shipping `Any` as an
   explicit raw `{type_url, value}` form rather than attempting (and getting subtly wrong)
   generic resolution against an incomplete descriptor pool. Documented limitation, not a silent
   drop.
7. **Schema-type wire-enum skew across engine / processor-SDK / connector-SDK versions.**
   Mitigated as analyzed in "Compatibility" — the verified raw-cast pass-through degrades
   gracefully to today's existing `ErrUnsupportedType`, not corruption.
8. **`conduit-commons`'s `SerdeFactory.Parse` signature change** (to thread reference-resolution
   context through, per recommended option (a) above) is a Go API break for that exported type,
   even though it is not a wire-format break. Mitigated by it being a narrow, effectively
   single-consumer type today (`Schema.Serde()` is `schema.go`'s only internal caller; not
   exhaustively verified across every `ConduitIO/*` repo — flagged as a migration-PR check). Needs
   a `conduit-commons` minor version bump and a compatibility note in that repo's changelog.

## Backward compatibility & rollback

- **No existing serialized data is affected.** Protobuf topics are unreadable today, so there is
  no previously-working decode behavior this change alters — this is strictly additive
  capability, unlike, say, a decoder-library swap behind an existing format.
- **`schema.proto`'s enum addition is append-only** (`TYPE_PROTOBUF = 2`, the next available
  number) — `TYPE_AVRO = 1` / `TYPE_UNSPECIFIED = 0` are untouched.
- **Rollback is a dependency revert** — bump `conduit-commons` back down (or remove the new
  processor package) restores exactly today's behavior. No durable state changes shape; the only
  "state" this design touches is the in-memory `Serde` cache, which is process-lifetime only, so
  none of `CLAUDE.md`'s position/checkpoint versioning machinery applies here.
- **The one piece that isn't purely additive**: `SerdeFactory.Parse`'s signature change (if
  recommended option (a) is taken). Any `conduit-commons` consumer that directly references the
  `SerdeFactory` type — as opposed to just calling `Schema.Serde()` — would need updating. Not
  exhaustively checked across every `ConduitIO/*` repo in this doc; the one item genuinely worth
  double-checking (mirroring the Avro doc's `go mod why` sweep) before merge.

## Observability

- The Avro-codec doc found that the built-in Avro processor wraps `Serde.Unmarshal` errors in a
  generic `cerrors.Errorf` with no stable `conduiterr.Code` (tracked separately as
  `ConduitIO/conduit#2824`, out of scope there). **Don't repeat that gap here.** Ship the new
  `protobuf.decode` processor and the underlying `schema/protobuf` package with the
  stable-error-code discipline `CLAUDE.md`'s "Errors are API" section already requires for new
  surfaces: distinct sentinel/wrapped errors — and distinct `conduiterr.Code`s — for (a)
  schema-compile failure, (b) reference-resolution failure or cycle, (c) message-index decode
  failure, (d) unknown-field rejection, and (e) generic wire-format mismatch. An operator or
  agent should see _why_ a Protobuf record failed to decode, not just that it did.
- No new runtime metric beyond the existing processor/pipeline error-count metrics — this is a
  decode-path addition behind the existing `Serde` interface, not a new subsystem with its own
  health surface.
- Compile-cache behavior (hit/miss, timeout trips) is worth a debug-level log line at minimum,
  mirroring `pkg/schemaregistry/client.go`'s existing `logger.Trace` cache-hit/miss pattern —
  cheap to add, useful for diagnosing "why is my first record after a schema change slow."

## Rollout

1. This doc → DeVaris sign-off, with two open questions flagged for an explicit decision rather
   than settled unilaterally: the `SerdeFactory.Parse` signature change (option (a) above), and
   the unknown-field default policy (reject vs. attach-under-reserved-key).
2. `conduit-commons`: `schema.proto` enum addition + regenerate; new `schema/protobuf` package
   (`Serde`, `Parse` via protocompile, reference resolution behind the agreed resolver seam);
   `KnownSerdeFactories` entry; `SerdeFactory.Parse` signature change if option (a) is taken.
3. `conduit`: bump the `conduit-commons` dependency; new built-in `protobuf.decode` processor
   under `pkg/plugin/processor/builtin/impl/protobuf/`, mirroring `avro.decode`'s shape; wire the
   existing `schemaregistry.Registry.SchemaBySubjectVersion` into the new package's reference
   resolver.
4. `conduit-processor-sdk` / `conduit-connector-sdk`: bump `conduit-commons` once released, so
   newly built WASM processors/connectors can request Protobuf schemas. Not strictly required for
   old-vintage safety (verified in "Compatibility"), but should ship promptly so the capability
   is actually reachable end-to-end.
5. Fixtures required before merge: a flat single-message schema, a nested-message schema
   (index depth ≥ 2), a schema with a reference, one fixture per well-known type, a `oneof`, an
   enum, a map field, a deliberately malformed `.proto` source, a deliberately cyclic reference
   chain (`A → B → A`), and an unknown-field payload.
6. No feature flag needed for the decode capability itself — additive, opt-in by pipeline config
   choosing the new processor. The wire-enum addition ships as part of the same `conduit-commons`
   bump and is gated by nothing, since it's purely additive.

## Effort estimate & slice breakdown

Each slice is sized to be independently shippable and independently reviewable, following this
repo's existing convention (see the Debezium-compete roadmap doc's workstream table). Estimates
are solo-maintainer-plus-Claude working days; ranges reflect real uncertainty (the Avro-codec
migration found a real edge case despite looking "purely mechanical" going in — expect the same
here, especially in PB-3/PB-4).

| ID | Slice | Est. | Depends on |
| --- | --- | --- | --- |
| PB-0 | This design doc | done | — |
| PB-1 | `conduit-commons`: wire enum, `schema/protobuf` package skeleton, `Serde.Parse` via protocompile (flat messages, no references), `KnownSerdeFactories` entry, unit tests, first fuzz seed | 4–6 days | PB-0 |
| PB-2 | Message-index handling (flat + nested), wired into the decode entry point, using franz-go's existing `DecodeIndex` | 1–2 days | PB-1 |
| PB-3 | Schema-reference resolution: `SerdeFactory.Parse` signature change, resolver walk against `schemaregistry.Registry`, cycle/depth guard, adversarial cyclic fixture | 3–4 days | PB-1 |
| PB-4 | Field mapping: scalars, oneofs, enums, maps, bytes, well-known types, unknown-field policy, unset-vs-zero documentation, full fixture suite | 4–6 days | PB-2 |
| PB-5 | `conduit`: built-in `protobuf.decode` processor, registry wiring, acceptance tests, stable error codes, docs (README, `conduit.io` source, `llms.txt`) | 3–4 days | PB-1–PB-4 |
| PB-6 | `conduit-processor-sdk` / `conduit-connector-sdk` bumps, repo-wide sweep for direct `SerdeFactory`/raw-`schema.Type`-cast consumers | 1–2 days | PB-5 |

**Total: roughly 16–24 working days (3–5 weeks), across six independently reviewable PRs.** That
is the concrete basis for checking the maintainer's judgement that this is cheaper than the
Arrow/columnar record re-platform (`docs/adr-columnar-record-archv2`) — this is a bounded,
single-subsystem addition behind an existing seam, not a record-model change touching every code
path that reads or writes a record.

## Risk tier & related

**Tier 2 (Feature)**, not Tier 1. This touches a public contract (`schema.proto`'s enum, an
exported `conduit-commons` Go API) and is genuinely new capability, but it does not touch
ack/position/checkpoint/state — invariants 1–5 and 7 are untouched. Invariant 6 (schema handling)
is engaged, and is exactly why this doc works through the unknown-field and unset-vs-zero policy
explicitly rather than leaving it implicit. `CLAUDE.md`'s Tier 1 language ("connector protocol...
serialization formats") is read here as pointing at `conduit-connector-protocol` and `opencdc`'s
own wire format — not at the schema-registry client's own type enum — but this classification is
arguable and should be confirmed by DeVaris rather than assumed, given `schema.proto` is
genuinely a public, cross-repo contract even though it isn't _the_ connector protocol.
Recommended review bar: one reviewer approval (DeVaris, solo-maintainer reality), acceptance/
integration tests green, `--json` + stable error codes on the new processor, docs updated in the
same PR — the standard Tier 2 bar, not the full Tier 1 bar.

**Related:**

- `docs/design-documents/20260823-avro-codec-archived-decoder-advisories.md` — sibling precedent
  for the library-longevity diligence this doc applies to protocompile.
- `ROADMAP.md`, Phase 2, "Enterprise correctness": "Confluent Schema Registry wire compatibility
  (Avro, Protobuf, JSON Schema)."
- `STRATEGY.md`: the Kafka Connect migration bet this closes a real gap for.
- `docs/adr-columnar-record-archv2` (branch) — the larger re-platform proposal this doc's scope
  and estimate were triaged against.
- `ConduitIO/conduit-commons#278` / issue `#2817` — the Avro codec replacement this doc's
  "library longevity" criterion is directly modeled on.

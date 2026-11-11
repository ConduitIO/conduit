# Kafka Connect SMT compatibility pack (v0.22)

## Summary

`conduit migrate kafka-connect` blocks every connector that has a `transforms=` chain today. This pack gives it
a translator for the most-used Kafka Connect (KC) and Debezium single message transforms (SMTs), plus the
processor changes those translations need: a typed, opt-in field-copy mode for `field.set`, a `flatten`
processor, and processors for the Debezium SMTs that have no equivalent yet.

The rule from the base design stays: **emit-if-exact.** A translator writes processors only when the
configuration falls inside the subset where the processors behave exactly as the SMT does. Anything else is
reported `unsupported` and blocking, with a reason and a fix. An SMT is never silently dropped and never
approximated: transforms change data (invariant 6). Data that falls outside the exact subset at run time
fails the record with a coded error. It is never reshaped differently.

Roadmap item: v0.22 "SMT compatibility pack" (ROADMAP.md), issue #2960. This PR is the design only (Tier 3).
The implementation PRs (B1 to B10 below) are Tier 2: processors and CLI. The deferred PartitionRouting work
touches Kafka partition assignment and is Tier 1 when it lands in v0.23.

## Context

- **kcmigrate has no translator framework.** `rules/data/shared.yaml` claims `transforms`,
  `transforms.*`, `predicates` and `predicates.*` and makes all of them `unsupported` and blocking.
  `report.Transform` (alias, class, status, processor) exists in the report and the JSON Schema, but no code
  fills it. The generated pipeline model (`emit.connectorDef`) has no `processors` field.
- **The SMT parity table** (`docs/catalog-inventory.md`, "Kafka Connect SMT parity") maps every standard
  SMT to a processor. Five SMTs have no processor (Flatten, ExtractChangedRecordState, EventRouter,
  PartitionRouting, TimezoneConverter). Four are partial only because `field.set` renders a template and
  so always produces a string (ExtractField, HoistField, InsertField, ValueToKey).
- **WIP commit 6887b977** (local branch `wip/v022-smt-pack`, unreviewed) has a typed `field.set` and a
  `flatten` processor. It is a starting point, not something to merge. It has five defects:
  - `field.set` switches to copy mode whenever `value` starts with `.` and has no `{{`. A pipeline that
    sets the literal string `.Payload.After.id` today would silently start copying that field instead,
    which is a breaking change. The WIP also drops the `tplerr.Exec` wrapping of template errors.
  - `flatten` overwrites one value with another when two paths flatten to the same key. Which value wins
    depends on Go map iteration order.
  - `flatten` leaves the input's schema subject and version on the record.
  - `flatten` is not registered as a built-in.
  - `flatten` returns `flatten.not_structured` for a nil side, where KC passes a null value through. No
    test covers a nil key or payload.
- **#3147.** A processor that references a field under a nil key or payload writes `StructuredData{}`
  into that side. That turns a delete's after-image or a tombstone into an empty row. The fix belongs in
  `conduit-processor-sdk` and is not part of this pack. Translations must not depend on it.
- **Schema middleware.** Built-in processors run inside the processor SDK's schema encode middleware
  (`conduit-processor-sdk` v0.9.2). When a processor reshapes a record that carries
  `opencdc.<side>.schema.subject`/`.version`, the middleware extracts a schema from the values and
  registers it as a new version of the source's subject. That schema widens int to long and drops logical
  types and enums. For KC parity that output is lossy, and it pollutes the source subject.

## Constraints

- Every input key ends up in the report exactly once (the completeness invariant of the base design). This
  covers every `transforms.<alias>.*` and `predicates.<alias>.*` key.
- `conduit migrate` always emits a compatibility report. A refused SMT is a report entry with code, reason
  and fix, never an omission.
- No new configuration language (ADR `20260704-no-bespoke-dsl`). SMTs map to processors, conditions are the
  existing Go-template `condition`, and topic expressions use sprig functions that already ship.
- The default behaviour of every existing processor stays byte-identical. New behaviour is opt-in through
  new parameters or new processors.
- No report schema change. `reportVersion` stays 1, and the schema sets `additionalProperties: false`, so
  adding a field would break strict v1 validators.
- MCP `kc_migrate` and the CLI run the same `pkg/kcmigrate` code, with no divergent logic.
- Output is deterministic: the same input produces byte-identical pipeline YAML and report, whatever the
  key order in the input.

## Maintainer decisions (2026-11-10)

Recorded as decided:

1. **Scope for v0.22:** `Filter` with the predicates `TopicNameMatches`, `HasHeaderKey` and
   `RecordIsTombstone`; `RegexRouter`, `Cast`, `ReplaceField`, `InsertField`, `ExtractField`, `HoistField`,
   `ValueToKey`, `TimestampConverter` and `Flatten`; the Debezium `ExtractNewRecordState`,
   `ExtractChangedRecordState` and `TimezoneConverter` (narrow form). `EventRouter` is a stretch goal.
   `PartitionRouting` moves to v0.23, because it needs the Kafka destination to write an explicit
   partition (invariant 4). Until then migrate reports it as blocking, with that reason.
2. **Typed `field.set`:** copying a field by reference is an explicit opt-in. The default behaviour stays
   byte-identical, and a test pins it.
3. **Emit-if-exact:** a translator emits processors only when the result is exactly equivalent. Otherwise
   the SMT is reported unsupported with a reason. It is never silently dropped.
4. **Headers and partitions:** an SMT that reads or writes headers or partitions translates only when the
   target plugin actually reads or writes them. Otherwise it is blocking.
5. **Debezium output mode:** SMTs that reshape the envelope stay blocking when the pipeline uses
   Debezium-compatible output mode. That mode is v0.23.
6. **`HoistField`** gets a small `field.hoist` processor in v0.22. Wrapping a value in a new map cannot be
   expressed as a field copy.
7. **`ReplaceField include`** is v0.23. Until then migrate reports it as blocking, with a fix that names
   v0.23.
8. **SMTs that reshape a schema-carrying side** (Avro, Protobuf, JSON Schema, `JsonConverter` with
   `schemas.enable=true`) are refused in v0.22 with an honest reason. Type-preserving schema derivation
   is v0.23.
9. **KC oracle:** the JVM capture program `tests/migrate/smt-oracle` is accepted. It runs Kafka Connect
   4.2, manually or nightly, and its outputs are committed. Each translator's corpus case is diffed
   against the captured output.
10. **`DropHeaders`, `InsertHeader` and `HeaderFrom`** are v0.23. Until then they are blocking, with that
    reason.

## Decision

### Translator architecture

The translators live in a new package, `pkg/kcmigrate/smt`. It has no I/O and depends only on `kcmigrate`
and `kcmigrate/rules`.

**Claiming keys.** `shared.yaml` changes so that `transforms`, `predicates` and the `transforms.`/`predicates.`
prefix rules emit kind `smt-chain` instead of blocking. That kind records the key for the chain finisher
and sets no outcome. The finisher sets the outcome of every claimed key. If it leaves a key without one,
the run fails as an internal error. A test asserts this, so a claimed key cannot fall off the report.

**Grouping.** The `smt-chain` finisher runs after every other rule and finisher of the connector, because it
needs their results: the topic expression, the converter shape and the target plugins. It parses
`transforms=` and `predicates=` into ordered alias lists and groups the sub-keys by alias. These cases are
handled before any translator runs:

| Input | Outcome |
| --- | --- |
| Alias listed in `transforms=` with no `.type` | Every key of the alias unsupported and blocking (KC rejects the config) |
| `transforms.<a>.*` keys for an alias not listed | Translated as no-op ("Kafka Connect ignores transforms not in `transforms=`"), not blocking |
| Alias listed twice | The second occurrence is blocking (KC rejects duplicates) |
| `transforms.<a>.predicate` names an undefined predicate | Blocking |
| Predicate defined but used by no transform | Translated as no-op |
| A config-provider reference as any SMT value | Blocking (it cannot be evaluated offline, as in the base rules) |

**Per-class translators.** A registry maps the SMT class, after its `$Key`/`$Value` suffix is stripped, to
a translator:

```go
// Translate returns the exact processors for one SMT, or a refusal. The
// error is for bugs only (bad rule data, impossible state); it fails the run.
type Translator func(c *Chain, t *SMT) (Result, error)

type Result struct {
	Processors []Processor // in order; empty with Refusal set
	Refusal    *Refusal    // code, reason, fix, and the key the reason is about
	Claimed    []string    // the sub-keys this translator consumed
}
```

A translator receives the SMT's sub-keys and the chain state: which side it operates on, the shape of that
side, the current topic expression, the target plugins and their capabilities, and whether the pipeline
uses Debezium output mode. If the translator does not claim a sub-key, that key is `kc.key_unknown` and
blocking. This is how an option added to a later KC release surfaces instead of being ignored. A class with
no translator gets `kc.smt_unsupported` on every one of its keys, and the fix names the release that maps
it, or says to write a processor (processor SDK).

**Placement.** The processors of a source connector's SMTs go on the source connector's `processors`, and
those of a sink connector's SMTs on the destination's. Either way they keep `transforms=` order. A new emit
helper, `Plan.AppendProcessor(side, def)`, appends a processor, and `connectorDef` gains
`processors: [{id, plugin, condition, settings}]`. On a sink, KC runs the converter before the SMTs, so the
SMT processors go after any decode processor the converter rules emit. On a source they go before any
encode processor. Processor IDs are `smt-<alias>` (sanitized), with `-<n>` added when one SMT emits more
than one processor. A sanitized ID that collides gets a numeric suffix in alias order.

**All or nothing per chain.** If any SMT in a chain is refused, no processor of that chain is written. A
partial chain would hand the later links different data from what they had in KC. Every link still gets
its own entries. A link that was translatable on its own is reported `unsupported` (`kc.smt_unsupported`,
blocking), and its reason names the refused alias. With `--accept-gaps` the pipeline therefore runs with no
SMT processors at all, and `acceptedGaps` lists every link.

**Predicates and `negate`** become the processor's `condition`. `negate=true` wraps the predicate in
`not`. When a nil guard is needed (see _Nil keys and payloads_), it is combined with the predicate using
`and`. A `Filter` becomes the `filter` processor carrying that condition. A `Filter` without a predicate
drops every record, and so does `filter` without a condition.

**The topic is threaded through the chain, not written into records.** The chain starts from a topic
expression, a Go-template pipeline the connector's topic rules already produce. It is the destination
topic template on a source chain, and `(index .Metadata "opencdc.collection")` on a sink chain that reads
from the Kafka source. `RegexRouter` rewrites the expression. `TopicNameMatches` and
`InsertField topic.field` read it. At the end of the chain the expression is written to the plugin's topic
setting: the Kafka destination's `topic` on a source chain, and the destination's naming setting on a sink
chain, listed in the capability data. If the connector rules left the topic unset, or blocked it, every
topic-reading SMT is refused ("the topic this SMT reads is not translated"). It is also refused if a later
processor in the chain writes a metadata key the expression reads.

**Capability data.** A new data file, `rules/data/smt.yaml`, holds the per-class known sub-key sets and a
per-plugin capability table: whether the plugin reads headers into metadata, whether it writes metadata as
headers, whether it writes an explicit partition, and which setting names its target topic or table. The
capabilities are stated for the connector version pinned in `go.mod`. A test re-checks them against that
connector's spec, so a connector bump that changes a capability fails the build instead of changing what
migrate emits.

**Report.** Each SMT fills one `report.Transform`: `alias`, `class`, `status` (worst status among its keys),
and `processor` (the plugin of the first processor it emits). The other processors of a multi-processor
translation are located by each entry's `target.configPath`, for example
`/pipelines/0/connectors/0/processors/2`. That needs no schema change.

**MCP parity.** `kc_migrate` calls the same `migrate.Run`. The harness's `TestMigrate_MCPParity` runs every
SMT corpus case through both paths and compares the outputs byte for byte.

### Mapping and exactness

KC's `$Key` variants operate on `.Key` and `$Value` variants on `.Payload.After`. "Structured" means the
translator knows the side holds `StructuredData` at that position. That is the case after a native CDC
source, or after a decode processor emitted from the converter rules. If the shape is unknown, any SMT that
reshapes the side is refused.

On a native Conduit CDC source, an SMT placed before `ExtractNewRecordState` sees the Debezium envelope in
KC. The translator maps `after.*` to `.Payload.After.*` and `before.*` to `.Payload.Before.*`, and refuses
any other envelope path (`source.*`, `op`, `ts_ms`) unless the row below lists it. With Debezium output
mode on, every SMT that reshapes the value or key is refused (decision 5). `Filter`, the predicates and
`RegexRouter` do not reshape, and still translate.

| SMT | Emits | Exact when | Refused otherwise (reason) |
| --- | --- | --- | --- |
| `Filter` | `filter` + `condition` | always, given a translatable predicate or none | - |
| `TopicNameMatches` | condition `regexMatch "^(?:P)$" T` | `pattern` is in the RE2-safe Java subset (below) and T is known | Java construct named; topic not translated |
| `HasHeaderKey` | condition testing that key `N` is present in `.Metadata` (template fixed in B3: `index` alone cannot tell an empty header value from a missing one) | the side that carries headers at this position maps headers to metadata (sink chain: Kafka source reads them; source chain: an earlier SMT in the same chain wrote them), and `N` does not start with a reserved metadata prefix (`opencdc.`, `conduit.`, `kafka.`) | header capability missing; reserved name |
| `RecordIsTombstone` | sink chain: condition testing for a null Kafka value; native CDC source chain: constant false, evaluated at translate time | sink chain: the Kafka source's null-value representation is pinned by a test; source chain: native CDC emits no tombstones (already in the output-shape note) | other sources |
| `RegexRouter` | rewrites T to `regexReplaceAll "^(?:P)$" T "R"` | `regex` is in the RE2-safe subset; `$n` in `replacement` is rewritten to `${n}`, and `\$` handled; KC full-match plus `replaceFirst` equals an anchored replace-all | construct named |
| `Cast` | one `field.convert` per `field:type` pair, in spec order | target is `int64`, `float64`, `boolean` or `string`, and B4's conformance table shows that `field.convert` equals KC's `Cast` for every input type KC accepts for that target | `int8`/`int16`/`int32`/`float32`, `bytes`, the whole-value form, any pair that differs in the conformance table |
| `ReplaceField` | `field.exclude`, then `field.rename` | structured side; `exclude` and `renames` only; a rename of an excluded field is dropped at translate time, as KC drops it | `include` (fix: v0.23, decision 7), `replace.null.with.default` other than the default |
| `InsertField` | typed `field.set`, one per field | `static.field`/`static.value` (string, as in KC); `topic.field` from T; `partition.field`, `offset.field`, `timestamp.field` on sink chains only, from the Kafka source metadata, with `type` set to KC's type; `!`/`?` suffixes stripped | partition/offset/timestamp on source chains (KC gives null or connector-specific values there) |
| `ExtractField` | `field.set` with `from: <side>.<f>` into the side | top-level `field`, or a `field.syntax.version=V2` dotted path; runtime: the extracted value must be representable on that side (structured, string or bytes), otherwise the record fails with a coded error | nested path under V1 |
| `HoistField` | `field.hoist` (new, B2) | structured or nil side | - |
| `ValueToKey` | `field.exclude .Key`, then one `field.set` with `from` per field | structured value; the old key replaced as a whole, as in KC | unstructured value |
| `TimestampConverter` | `field.convert` (`time`, new `format`/`unit` params, B4) | `target.type` `Timestamp`, `unix` or `string`; `format` uses only the pattern letters B4 maps to a Go layout (`yyyy MM dd HH mm ss SSS XXX`, quoted literals), parsed and rendered in UTC as KC does; `unix.precision` any | `Date`/`Time` targets, other pattern letters, locale-dependent patterns |
| `Flatten` | `flatten` (B2) | structured or nil side; `delimiter` any | unstructured side |
| `ExtractNewRecordState` | native CDC source: no processor, or `field.set` steps for options; sink chain: `unwrap.debezium` | defaults; `add.fields` and `add.headers` for `op`, `table`, `schema`/`db`, `source.ts_ms` and `lsn` (headers only with header capability); `add.fields.prefix`; `delete.handling.mode`/`delete.tombstone.handling.mode` values whose per-operation output B5 tabulates and pins with goldens; on a sink chain, only for destinations whose write of the unwrapped record B5 shows equal | other options; Debezium output mode |
| `ExtractChangedRecordState` | new processor (B6) writing metadata | header capability on the target; the update carries a full before-image; header value bytes equal KC's `SimpleHeaderConverter` rendering of the name list | no header capability; source without full before-images |
| `TimezoneConverter` | new processor (B7) | narrow form: `converted.timezone` is an IANA name or a fixed offset; only columns Debezium types as `ZonedTimestamp`/`ZonedTime` (`timestamptz`/`timetz`), which Conduit holds as `time.Time`; the instant is unchanged; `include.list`/`exclude.list` only in `<table>:<column>` form | any other temporal type, other list forms |
| `EventRouter` (stretch) | `filter` (non-create ops), `field.set` with `from` for key and value, T rewritten from `aggregatetype` | default column names or explicit `table.field.*`, no `expand.json.payload`, no additional placement to headers without header capability | everything else; the whole class refused if B8 does not land |
| `PartitionRouting` | none | - | always in v0.22: `kc.target_capability_missing`, "the Kafka destination partitions by key only; explicit partition writing is v0.23 (per-partition ordering, invariant 4)" |
| `DropHeaders`, `InsertHeader`, `HeaderFrom` | none | - | always in v0.22: `kc.smt_unsupported`, blocking, "header transforms are mapped in v0.23" (decision 10) |
| Others (`MaskField`, `TimestampRouter`, `SetSchemaMetadata`, `HeaderToValue`, `ContentBasedRouter`, `ByLogicalTableRouter`, Debezium `Filter`, custom) | none | - | `kc.smt_unsupported`; fix names v0.23 or "write a processor" |

**RE2-safe Java regex subset.** The pattern is parsed with `regexp/syntax`. It is accepted only if every
node belongs to a list of constructs whose Java and RE2 meanings are the same: literals, `.`, classes
without POSIX bracket names, `\d \w \s` with their ASCII meaning, the anchors `^ $ \b`, greedy and lazy
quantifiers, and capturing and non-capturing groups. Flags other than `(?i)` are refused. Lookaround,
backreferences, possessive and atomic forms, `\h`, `\Z`, `\p{...}` and `[[:alpha:]]` are refused; the last
one means different things in Java and RE2. Being accepted by RE2 is not enough on its own.

### Typed `field.set`

`field.set` gains two optional parameters. With neither set, nothing changes.

- `from`: a field reference such as `.Payload.After.id`. The referenced value is deep-copied, with its type
  kept, into `field`. `from` and `value` are mutually exclusive. `Configure` fails with
  `field.set.value_and_from` if both are set, and with `field.set.value_required` if neither is. `from` is
  a reference, never a template: it is parsed with the reference resolver at configure time.
- `type`: `string` (default), `int`, `float`, `bool` or `time`. It converts the rendered template output
  using `field.convert`'s conversion code, so the two processors share their semantics. `type` together
  with `from` is a configure error.

We chose a separate `from` parameter over `type: reference`. In every other mode, `value` is a template.
With `type: reference`, the same string would sometimes be a template and sometimes a path, depending on
another parameter, and a YAML reader could not tell which without reading both. `type` would also mix "what
to convert to" with "where the value comes from". With `from`, the YAML says what it does, a template in
`from` is rejected at configure time, and `type` stays a closed list of target types. The cost is that
`value` loses its `required` tag and "exactly one of `value` and `from`" moves into `Configure`, covered
by a test.

**Byte-identical default.** B1 adds a test that keeps a frozen copy of the current `Process` as a
reference. Over generated records (raw, structured, nil sides, templates that error), it asserts that the
new processor with only `field` and `value` set returns identical records and identical errors, including
the `tplerr.Exec` wrapping. The test that the WIP's auto-detect would have failed is part of it: `value:
".Payload.After.id"` still sets that literal string. The processor version moves to v0.2.0.

### `flatten`

The WIP processor, with these changes:

- **Collisions refuse and never overwrite.** If two input paths flatten to the same key (`{"a.b": 1, "a":
  {"b": 2}}` with `.`), the record fails with `flatten.key_collision`. The error names both source paths
  and the key, never the values. KC overwrites silently in schemaless mode and throws in schema mode;
  refusing matches the throw, and the difference is in the report's reason for every `Flatten`
  translation. The check does not depend on iteration order, so the output is deterministic.
- **Nil passes through.** A nil side is returned unchanged with no error, matching KC's handling of null
  values. Raw data still fails with `flatten.not_structured`.
- **Schema metadata is dropped** for the side it flattens (see below).
- Registered in the built-in registry, with a doc page and examples.
- Arrays stay as they are. An empty nested map produces no key, as in KC.

### `field.hoist`

A small processor with two parameters: `field` (`.Key` or `.Payload.After`, default `.Payload.After`) and
`name`. It replaces the side with a structured value `{<name>: <old side>}`, deep-copied, and it is the
equivalent of `HoistField$Key`/`$Value` (decision 6). How it treats a nil side (wrap or pass through)
follows the captured KC 4.2 output and is pinned by a test. Raw data is wrapped as bytes, as KC wraps a
byte array. It drops the side's schema metadata (below). It lands in B2 with `flatten`.

### Schema metadata after reshaping

A processor in this pack that changes a side's shape removes that side's `opencdc.<side>.schema.subject`
and `opencdc.<side>.schema.version`: `flatten`, `field.set` with `from` or a non-string `type` on a path
under `.Key` or `.Payload`, and `field.hoist`. The encode middleware then leaves the data structured. It
does not register a lossy extracted schema as a new version of the source's subject. We chose dropping over
updating. Updating means deriving a type-preserving schema from the input schema and registering it under
a new subject, which is v0.23 work.

Existing processors (`field.exclude`, `field.rename`, `field.convert`) keep their current behaviour,
because changing it would be a silent change for hand-written pipelines. When the translator emits one of
them to reshape a side that carries schema metadata, it appends a `field.exclude` of those two keys. This
is visible in the YAML and exact.

When the KC converter for the reshaped side carries a schema (Avro, Protobuf, JSON Schema, or
`JsonConverter` with `schemas.enable=true`), KC derives a type-preserving schema for the new shape. Conduit
cannot reproduce that in v0.22, so every reshaping SMT on such a side is refused (decision 8). The reason
reads: "a schema-carrying converter needs a type-preserving schema for the reshaped data; Conduit would
register a schema with widened types, so this is refused until v0.23". Non-reshaping SMTs (`Filter`, the
predicates, `RegexRouter`) are unaffected.

### Nil keys and payloads (#3147)

KC passes a null key or value through most SMTs unchanged: `Cast`, `ReplaceField`, `InsertField`,
`Flatten`, `ExtractField`, `ValueToKey` and `TimestampConverter`. Conduit's reference resolver turns a nil
side into `StructuredData{}` (#3147). A delete has a nil `.Payload.After`, and a tombstone has a nil key or
value, so after such a processor the record would carry `{}`.

- The translator adds a nil guard to the `condition` of every processor it emits for such an SMT. The guard
  is ANDed with any predicate, so a nil side skips the processor. The guard's exact template is fixed in B3,
  and a test covers a nil interface, `RawData(nil)` and empty `StructuredData`. Only a nil interface
  counts as nil, so an empty struct is still processed, as in KC.
- For each SMT, the captured oracle output (Tests) records how KC treats a null side. An SMT that acts on
  null gets no guard. `HoistField` may be one of them, and the captured output decides.
- `flatten` passes a nil side through itself. `field.hoist` handles nil the way the captured output shows.
  Both have tests for it. The SDK fix for #3147 is tracked there. When it lands, the guards become redundant but stay correct.

## Alternatives considered

**Run the SMTs themselves in a JVM (the v0.23 connector JAR host).** This would give exact behaviour for
every SMT, custom classes included. It lost because it brings a JVM into a v0.22 migration path for
configurations that existing processors already express exactly. It also leaves no Conduit-native pipeline
to own afterwards. It stays the answer for custom SMTs in v0.23.

**Approximate, then let the user accept the difference.** Translate every SMT as closely as possible, mark
it `approximated`, and let `--accept-gaps` or review cover the difference. It lost because an approximated
transform reshapes data differently, which is invariant 6. A reviewer reading "approximated" cannot see
which records come out different. The base design already ruled this out for SMTs.

**Auto-detect copy mode in `field.set` (the WIP).** Zero new parameters, but it silently changes existing
pipelines whose value is a literal starting with `.`. Rejected by decision 2.

**`type: reference` instead of `from`.** One parameter fewer, but the meaning of `value` would depend on
another parameter. See _Typed `field.set`_.

**Emit the exact links of a chain that contains a refused link.** This is more useful with
`--accept-gaps`, but the links after the refused one would see different data than in KC. Rejected for
all-or-nothing.

**Write the topic into `opencdc.collection` with `field.set` at each `RegexRouter`.** This would be simple,
but it mutates every record, changes what destinations that read the collection see, and does not cover
`TopicNameMatches` before the router. Threading the expression through the chain costs nothing at run time.

## Failure modes

| Failure | Effect | Prevention or detection |
| --- | --- | --- |
| A translator claims exactness for a config that is not exact | Pipeline runs and writes different data | Conservative per-SMT subsets; oracle goldens from real KC per SMT (Tests); all-or-nothing chains; Tier 2 review reads the exactness column against KC source |
| Java regex construct means something else in RE2 | Different topic, or wrong predicate | Allow-list over `regexp/syntax` nodes, not "compiles"; fuzz test on the classifier |
| Topic expression diverges from KC's topic at that chain position | Wrong routing | Expression only from connector topic rules already covered by goldens; refused when unknown or overwritten later in the chain |
| `flatten` key collision | Without the rule, silent overwrite, order-dependent | `flatten.key_collision`; record to DLQ or halt per `errors.tolerance`, never dropped (invariant 3) |
| Nil side becomes `{}` (#3147) | Delete or tombstone turned into an upsert of an empty row | Nil guard on emitted conditions; nil passthrough in new processors; property tests over nil sides |
| Stale or lossy schema after reshaping | New subject version with widened types; consumers break | New processors drop schema metadata; translator appends an exclude for existing ones; schema-carrying converters refuse reshaping SMTs |
| Runtime value outside the exact subset (e.g. `ExtractField` yields an int into `.Payload.After`) | Without a check, a coerced or mangled record | Coded processor error; never coerced (invariant 6) |
| `field.set` default changes | Existing pipelines change output | Frozen-reference byte-identical test (B1) |
| Connector bump changes header or partition behaviour | Header SMTs translate against a capability that no longer holds | Capability data checked against the pinned connector's spec in a test |
| KC adds an SMT option | Option ignored | Unclaimed sub-key is `kc.key_unknown`, blocking |
| Missing tz database (`TimezoneConverter`) | Configure fails on minimal images | Embed `time/tzdata` in the processor; unknown zone is a configure-time coded error |
| Nondeterministic output (map order, IDs) | Flaky goldens; diffs between runs and between CLI and MCP | Ordered alias lists, deterministic IDs; determinism property test |

## Tests

- **Corpus.** A new `smt` family in `tests/migrate/corpus` holds one case per SMT in the exact subset, one
  per refusal reason, chains that mix both, and the Debezium-output-mode and schema-carrying-converter
  refusals. Each case commits its pipeline YAML and report goldens. The existing `dbz-mysql-regexrouter`
  case turns from pending to ready. Following the harness rule, the family turns required when
  `rules/data/smt.yaml` lands (B3).
- **KC oracle goldens.** For each translated SMT, fixture records (structured, nested, nil key, nil value,
  edge values) are run through Apache Kafka Connect 4.2's own transform classes. A small JVM capture
  program under `tests/migrate/smt-oracle` does this. It runs manually or nightly, and its outputs are
  committed with provenance (decision 9). Each translator's corpus case runs the emitted processors over
  the same fixtures and diffs the result against the captured output. A translator PR cannot merge without
  its captured output. PR CI runs only the Go side, so it needs no JVM.
- **Property tests** (`rapid`, already used by `field.exclude`'s property test):
  - Completeness, extended to `transforms`/`predicates` keys.
  - Determinism under permuted input key order and renamed aliases.
  - All-or-nothing: a refused link means no processors for that chain.
  - `flatten`: either every leaf appears exactly once in the output, or the record fails with a collision,
    and a nil side passes through.
  - `field.set` with `from` never aliases its source.
  - The `field.set` default is byte-identical.
- **Fuzz** targets on the Java regex classifier and the `SimpleDateFormat`-to-Go-layout translator. Both
  run their seed corpora in the `test` job.
- **Processor unit tests** over raw, structured, nil and error records, per the processor workflow in
  CLAUDE.md, with `Example*` functions for the doc pages.

## Docs

The docs land in the same PRs as the code, collected in B10 for the cross-cutting pages:

- `docs/catalog-inventory.md`: move the SMT parity rows to their v0.22 status.
- The Kafka Connect migration guide in the `conduit.io` docs source: an SMT section with the exactness table
  and the refusal reasons.
- `llms.txt`: the new processors and parameters, and the SMT mapping.
- Processor doc pages for `flatten`, `field.hoist` and the B6 to B8 processors, plus the new `field.set`
  and `field.convert` parameters.
- Changelog entries.

## Upgrade, rollback and compatibility

- Existing pipelines are unaffected. `field.set` and `field.convert` defaults are unchanged and pinned by
  tests, and all new behaviour is opt-in.
- migrate output changes only for inputs with transforms. Those inputs went from blocking to translated or
  refused-with-reason, and goldens are updated in the same PRs. The report schema is unchanged.
- Rollback: a pipeline generated by v0.22 that uses `flatten`, `field.hoist`, `from` or `type` does not
  validate on v0.21 (unknown processor or parameter), so it fails at start. It never runs differently.
  Rolling back means regenerating with the older migrate, which blocks the transforms again.

## Observability

- The report is the primary signal. `transforms` lists every alias with its status and first processor, and
  every SMT key has an entry whose `configPath` points at the processor it became.
- The emitted processors use the standard processor metrics. Runtime refusals (`flatten.key_collision`,
  `flatten.not_structured`, the `field.set` type errors) are coded errors on the record. They reach the
  DLQ or halt the pipeline per `errors.tolerance`, and they carry the field path, never the value.
- `conduit migrate` runs offline, so there are no new metrics.

## PR breakdown

| PR | Content | Depends on | Tier |
| --- | --- | --- | --- |
| B1 | Typed `field.set`: `from`, `type`, configure errors, byte-identical default test, schema-metadata drop, v0.2.0 | - | 2 |
| B2 | `flatten`: collision error, nil passthrough, schema drop, registration, doc page. `field.hoist`: wraps a structured or nil side under one field name, schema drop, doc page | - | 2 |
| B3 | Chain translator: `smt-chain` kind and finisher, `processors` in the pipeline model, grouping and edge cases, predicates and `negate`, topic expression, `rules/data/smt.yaml` with capability data, all-or-nothing, `report.Transform`, MCP parity, the `Filter`/predicate translators, the `PartitionRouting` refusal, the `smt` corpus family | - | 2 |
| B4 | Translators over existing processors: `RegexRouter` (regex classifier), `Cast` (conformance table), `ReplaceField` (`include` refused), `TimestampConverter` (`field.convert` `format`/`unit`), `Flatten` | B2, B3, B11 | 2 |
| B5 | `field.set`-based translators: `InsertField`, `ExtractField`, `ValueToKey`, `ExtractNewRecordState` options, `HoistField` | B1, B2, B3, B11 | 2 |
| B6 | `ExtractChangedRecordState`: processor and translator | B3, B11 | 2 |
| B7 | `TimezoneConverter`, narrow form: processor and translator | B3, B11 | 2 |
| B8 | `EventRouter` (stretch) | B1, B3, B11 | 2 |
| B9 | `PartitionRouting`: Kafka destination explicit partition and translator. **v0.23**, with its own design doc | v0.23 | 1 |
| B10 | Cross-cutting docs: catalog inventory, KC migration guide, `llms.txt`, changelog | B4-B7 | 3 |
| B11 | KC oracle: `tests/migrate/smt-oracle` capture program (Kafka Connect 4.2 transform classes over the fixture records), manual and nightly workflow, first captured outputs for the B4 SMTs, and the Go diff helper the corpus cases use | - | 3 |

The critical path is B3 and B11 in parallel, then B4 and B5 in parallel. B11 needs no translator code and
starts on day one. It must land before the first translator PR (B4) merges, so it is on the critical path
only if it takes longer than B3. Each later translator PR adds its own captured outputs to B11's program.
B1 and B2 run alongside B3.

## Open questions

None. The questions raised in the first draft were decided on 2026-11-10 (decisions 6 to 10).

## Related

- `docs/design-documents/20261012-migrate-kafka-connect.md` ("Single message transforms and
  predicates"), which this extends.
- `docs/design-documents/20261014-migrate-kafka-connect-mapping-and-harness.md`: rule data, corpus,
  pending rule, and MCP parity.
- `docs/catalog-inventory.md`, "Kafka Connect SMT parity".
- ADR `20260704-no-bespoke-dsl`.
- Issues #2960 (SMT pack), #3147 (nil side becomes `{}`), #3139 (processor property tests).
- WIP commit 6887b977 on `wip/v022-smt-pack` (typed `field.set`, `flatten`; not for merge).
- Roadmap: v0.22 "SMT compatibility pack".

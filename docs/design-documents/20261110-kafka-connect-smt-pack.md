# Kafka Connect SMT compatibility pack (v0.22)

## Summary

Today `conduit migrate kafka-connect` blocks every connector that has a `transforms=` chain. This pack adds
a translator for Kafka Connect (KC) and Debezium single message transforms (SMTs). It also makes the
processor changes those translations need: a typed, opt-in field-copy mode for `field.set`, and two new
processors, `flatten` and `field.hoist`.

The rule from the base design still holds: **emit-if-exact.** A translator writes processors only when the
configuration falls inside the subset where those processors behave exactly as the SMT does, including for
null and missing fields. Anything else is reported `unsupported` and blocking, with a reason and a fix. An
SMT is never silently dropped and never approximated, because transforms change data (invariant 6). Where
KC itself fails a record, the emitted processors fail it too, with a coded error.

v0.22 ships a deliberately narrow set. An SMT is in v0.22 only if its corpus cases diff clean against output
captured from real Kafka Connect 4.2. Everything else reports blocking with a fix that names v0.23.

Roadmap item: v0.22 "SMT compatibility pack" (ROADMAP.md), issue #2960. This PR is the design only (Tier 3).
The v0.22 implementation PRs are Tier 2 (processors, CLI) or Tier 3 (docs, the oracle tool). PartitionRouting
touches Kafka partition assignment, so it is Tier 1 when it lands in v0.23.

## Context

Checked against `main` (9de15d31), Apache Kafka 4.2.0 and the Debezium source.

- **kcmigrate.**
  - `rules/data/shared.yaml` claims `transforms`, `transforms.*`, `predicates` and `predicates.*`, and makes
    every non-empty value `unsupported` and blocking.
  - Entries take their outcome from rule data when keys are classified. `emit.ApplyAll` refines entries in
    place. Finishers (`emit.Finisher`, which returns notes only) come from each class file's `finish:` list.
  - `migrate/run.go` builds entries from the decisions after `Finish`, then runs `DropUnprotectedSASL`, which
    edits entries in place.
  - Unknown classes, and classes that produce no pipeline, never reach `Finish`.
  - The report already has `report.Transform`, but nothing fills it. `emit.connectorDef` has no
    `processors` field.
- **Converters.** For `key.converter` and `value.converter`, only `ByteArrayConverter` is translated. No
  converter rule emits a decode processor.
- **Topics.** `debezium-postgres.yaml` blocks `topic.prefix` and writes no Kafka `topic` setting. Debezium
  Postgres, S3 sink and MirrorMaker source all carry the blocking note `kc.output_shape_differs`.
- **WIP commit 6887b977** (local branch `wip/v022-smt-pack`, unreviewed) has a typed `field.set` and a
  `flatten` processor. It is a starting point, not something to merge.
  - `field.set` auto-detects copy mode whenever the value starts with `.` and contains no `{{`. That is a
    silent breaking change, and the commit also drops the `tplerr.Exec` wrapping.
  - `flatten` silently overwrites keys that collide.
  - `flatten` leaves stale schema metadata on the record, fails on a nil side, and is not registered.
- **Existing processors at field level.**
  - `field.convert` renders a nil or missing field as `"<nil>"`. It formats `*big.Rat` as `617/50` and an
    integral `float64` as `3`, and it writes bytes as the raw string.
  - `field.rename` of a missing field writes `new: nil`. Renames are sequential and fail with
    `ErrFieldExists`.
  - The reference resolver creates intermediate maps on nested paths. It turns a nil side into
    `StructuredData{}` (#3147, open). Setting a string into `.Key` or `.Payload.*` stores `RawData`.
- **Schema middleware** (`conduit-processor-sdk` v0.9.2). Every built-in processor runs the encode
  middleware. Suppose a processor reshapes a side whose `opencdc.<side>.schema.subject`/`.version` did not
  change. The middleware then extracts a schema from the values, which widens int to long and drops
  logical types. It registers that schema as a new version of the source's subject and encodes the data
  with it. `builtin:postgres` always attaches a subject.
- **Kafka connector v0.17.0.**
  - The source writes header `N` as metadata `kafka.header.N`. With `emitMirrorMetadata`, it omits headers
    that are not UTF-8. It sets `kafka.partition`/`kafka.offset` only with `emitMirrorMetadata`.
  - The source always sets `opencdc.collection` (the topic) and `opencdc.createdAt` (nanoseconds). Keys and
    values arrive as `RawData`.
  - The destination (not mirror mode) writes no headers, partitions by key hash, and writes `r.Bytes()`,
    the whole OpenCDC record.
- **Postgres connector v0.20.0.** `opencdc.collection` is the relation name. `numeric` is `*big.Rat`.
  Temporal columns are `time.Time`.
- **KC semantics that matter here.**
  - Debezium produces `Struct` values, so SMTs on a source connector always run in schema mode, whatever
    converter follows.
  - `replace.null.with.default` defaults to `true` on Cast, ReplaceField, InsertField, ExtractField,
    ValueToKey and TimestampConverter, and on Debezium's ExtractNewRecordState
    (`ExtractNewRecordStateConfigDefinition`). With it on, a null field is replaced by the column's schema default.
  - HoistField and ValueToKey have no null check: HoistField wraps a null, and ValueToKey throws on one.
  - RegexRouter runs `matches()`, then `replaceFirst`.
  - ExtractNewRecordState defaults to `delete.tombstone.handling.mode=tombstone`.

## Constraints

- **Completeness.** Every input key ends up in the report exactly once, including every
  `transforms.<alias>.*` and `predicates.<alias>.*` key.
- **Always a report.** `conduit migrate` always emits a compatibility report. A refused SMT is an entry with
  a code, a reason and a fix.
- **No DSL.** There is no new configuration language (ADR `20260704-no-bespoke-dsl`). Conditions use the
  existing Go-template `condition`, and topic expressions use sprig functions that already ship.
- **No default changes.** Every existing processor's default behaviour stays byte-identical. New behaviour
  is opt-in.
- **Report schema unchanged.** `reportVersion` stays 1, and the schema sets `additionalProperties: false`.
- **One code path.** MCP `kc_migrate` and the CLI run the same `pkg/kcmigrate` code.
- **Deterministic output.** The same input gives byte-identical YAML and report, whatever the key order.

## Maintainer decisions (2026-11-10)

Recorded as decided. Decisions 17 to 21 came out of the second review round:

1. **The v0.22 cut line.**
   - The base work: the processors from B1 and B2 (including `field.hoist`), the B3 framework and the B11
     oracle.
   - The SMTs: `Filter` with the predicates, `RegexRouter` (with the anchoring fix below), `ReplaceField`
     exclude/rename, `ExtractField`, `HoistField`, `InsertField` static/topic, `ValueToKey`, `Flatten` and
     `ExtractNewRecordState`.
   - Each of those ships only when its corpus cases diff clean against captured KC output on
     debezium-postgres with a schemaless JsonConverter.
   - Everything else reports blocking, with a v0.23 fix.
2. **Typed `field.set`.** Copying by reference is an explicit opt-in. The choice is the `from` parameter. The
   default stays byte-identical, pinned by a test.
3. **Emit-if-exact.** Processors are emitted only for an exact equivalent. Otherwise the SMT is unsupported
   with a reason, never silently dropped.
4. **Headers and partitions.** SMTs that depend on headers or partitions translate only when the target
   plugin actually reads or writes them. Otherwise they are blocking.
5. **Debezium output mode.** In Debezium-compatible output mode, SMTs that reshape the envelope stay
   blocking. That mode is v0.23.
6. **`HoistField`** gets a small `field.hoist` processor in v0.22.
7. **`ReplaceField include`** is v0.23. It is blocking, with a fix that names v0.23.
8. **Schema-carrying sides.** SMTs that reshape a side whose KC converter carries a schema (Avro, Protobuf,
   JSON Schema, or `JsonConverter` with `schemas.enable=true`) are refused in v0.22. Type-preserving schema
   derivation is v0.23.
9. **KC oracle.** `tests/migrate/smt-oracle` (Kafka Connect 4.2, run manually or nightly, outputs
   committed) is accepted. Each translator's corpus case diffs against the captured output.
10. **`DropHeaders`, `InsertHeader`, `HeaderFrom`** are v0.23, blocking with that reason.
11. **`Cast`, `TimestampConverter`, `TimezoneConverter`** are refused until v0.23. The same column reaches
    KC and Conduit as different types, and column types are unknown offline. v0.23 adds runtime type
    gates, proven by the oracle.
12. **Before-image.** Translators apply the same processors to `.Payload.Before` as to `.Payload.After`.
13. **`replace.null.with.default`.** SMT chains on Debezium sources translate only when it is set
    explicitly to `false` on every SMT that has the option. Otherwise they are blocking, with that fix.
14. **`ExtractChangedRecordState`** is v0.23, blocking with a reason. At the pinned connector versions, no
    target writes headers.
15. **`EventRouter`** is out of v0.22 and moves to v0.23. It was a stretch goal.
16. **`PartitionRouting`** is v0.23. It needs the Kafka destination to write an explicit partition
    (invariant 4). Until then it is blocking, with that reason.
17. **Before copies are always nil-guarded.** This holds for every value-side processor, `HoistField` and
    `Flatten` included. A nil `.Payload.Before` means "no image", not a KC null. Only the After copy follows
    KC's null-side behaviour.
18. **`ExtractField` on `after` is an unwrap point.** It follows the same position rules as
    `ExtractNewRecordState`.
19. **Decision 13 covers `ExtractNewRecordState`.** That SMT also needs `replace.null.with.default=false`.
    Otherwise it is blocking, with that fix.
20. **mirror-source `TopicNameMatches`** is refused in v0.22, blocking with a v0.23 fix.
21. **Oracle enforcement point.** The oracle goldens are committed: the input records and KC's output.
    - Each translator PR adds a Go test to the required `test` job, with no Docker. It feeds the committed
      input records through the emitted Conduit processor chain and diffs the result against the committed
      KC output, using the B11 diff helper.
    - The end-to-end comparison against real Postgres in Docker stays in the B11 workflow (manual and
      nightly).
    - If B11 slips, B4 and B5 do not merge, and their SMTs keep the v0.23 refusal.

## Decision

### Where the translator runs

The translators live in a new package, `pkg/kcmigrate/smt`. It has no I/O.

- **Placeholder outcome in rule data.** In `shared.yaml`, every non-empty `transforms`/`predicates` value and
  every `transforms.`/`predicates.` prefix key gets a placeholder outcome:
  - status `unsupported`, `blocking: true`, code `kc.smt_unsupported`;
  - reason "Transforms are translated only for connector classes that produce a pipeline";
  - fix "Keep this connector on Kafka Connect";
  - emit kind `smt`, a registered no-op kind like `none`.

  The placeholder is never seen in normal output.
  - Unknown classes get `kc.connector_class_unsupported`, and classes that produce no pipeline get their
    class-level `every` outcome. Neither consults the shared rules, so their `transforms` index stays
    empty.
  - For a class that does produce a pipeline, the SMT pass replaces the placeholder on every claimed key.
    The placeholder is a fail-safe: if the pass ever skips a key, the result is blocking, never translated.
- **The SMT pass.** `smt.Translate(plan, cl.Decisions, ctx)` is called from `migrateConnector` after
  `emit.Finish` and before `rules.Entries`, for every class that produces a pipeline. It is not listed per
  class file.
  - It rewrites `Decision.Entry` of each key it claims. That follows the existing practice of editing entries
    in place, as `emit.ApplyAll` and `DropUnprotectedSASL` do.
  - It appends processors to the plan and returns notes, which go into `rc.Notes`.
  - A test asserts that no entry of a class that produces a pipeline keeps the placeholder reason.
- **Data lives in Go, not `rules/data`.** The per-class known sub-key sets and the plugin capability table
  are Go tables in `pkg/kcmigrate/smt`, not YAML in `rules/data`. This leaves `rules.Builtin` and
  `harness.LoadRuleSet` unchanged; both would reject a file with a new scope.
  - The capability table records, for the connector versions pinned in `go.mod`: whether a plugin reads
    headers into metadata, and under which key; whether it writes headers or an explicit partition; and
    which setting names its target.
  - A test checks the table against the pinned connectors' specs.
- **Context.** `ctx` carries:
  - the chain side: source or sink;
  - the reference mode: schema mode on Debezium source chains; on sink chains, decided by the converter;
  - the shape of each record side, taken from that side's effective converter (the worker's converter
    with the connector's override applied), plus a per-side schema-carrying flag that enforces decision 8;
  - the initial topic expression;
  - the target plugins and their capabilities;
  - whether the pipeline uses Debezium output mode.

### Grouping and per-key outcomes

The pass parses `transforms=` and `predicates=` into ordered alias lists and groups the sub-keys by alias.

| Input | Outcome |
| --- | --- |
| Alias listed with no `.type` | Every key of that alias is blocking (KC rejects the config) |
| `transforms.<a>.*` for an alias that is not listed | Translated as a no-op ("Kafka Connect ignores it"), not blocking |
| Alias listed twice | The second occurrence is blocking |
| One alias or predicate name is a dotted prefix of another (`a` and `a.b`) | Both are refused, because Kafka Connect hands the longer alias's keys to both |
| `.predicate` names an undefined predicate | Blocking |
| Predicate used by no transform | Translated as a no-op |
| Predicate whose only users are refused or not emitted | Unsupported and blocking: "used only by transform(s) `<a>`, which are not translated" |
| Config-provider reference as any SMT value | Blocking (cannot be evaluated offline) |
| Sub-key no translator claims | `kc.key_unknown`, blocking (so an option added in a later KC release shows up) |
| Class with no v0.22 translator | `kc.smt_unsupported` on every key of the alias; fix names v0.23, or "write a processor" |
| Key of a refused SMT | The key the refusal is about gets the specific reason. The SMT's other sub-keys get "part of transform `<a>`, which is not translated: `<reason>`". All are blocking |

Translators have this shape:

```go
// Translate returns the exact processors for one SMT, or a refusal. The
// error is for bugs only; it fails the run.
type Translator func(c *Chain, t *SMT) (Result, error)

type Result struct {
	Processors []Processor // in order; empty with Refusal set
	Reads      []Path      // fields and metadata the SMT's emitted logic reads
	Writes     []Path      // what its processors write; a whole side when unknown
	Refusal    *Refusal    // code, reason, fix, and the key the reason is about
	Claimed    []string    // sub-keys consumed
}
```

**All or nothing per chain.** If one SMT in a chain is refused, no processor of that chain is written. Every
link still gets its own entries, and a link that was translatable on its own is reported as blocking, with a
reason that names the refused alias. With `--accept-gaps`, the pipeline runs with no SMT processors, and
`acceptedGaps` lists every link.

**Placement.** On a source chain, processors go on the source connector's `processors`. On a sink chain they
go on the destination's. Both keep `transforms=` order. Emitting them needs:

- `Plan.AppendProcessor(side, def)`;
- `processors: [{id, plugin, condition, settings}]` added to `connectorDef`.

Processor IDs are `smt-<alias>` (sanitized), with `-<n>` when one SMT emits several processors. A collision
gets a numeric suffix in alias order. The report's `transforms` entry for an SMT gives its alias, class and
worst status, plus the plugin of its first processor. Each key's `target.configPath` points at the
processor it became.

### Reference semantics

- **Source chains.**
  - The reference is KC's schema-mode code path over the Debezium `Struct`.
  - An **unwrap point** is `ExtractNewRecordState`, or `ExtractField$Value` with `field=after` (decision 18).
    After either one, KC's value is the row, and a delete's value is null.
  - Before the first unwrap point, the value is the envelope (`before`, `after`, `source`, `op`, `ts_ms`).
    Value-side reshaping SMTs and whole-value SMTs placed there are refused. The exception is the unwrap
    point itself: in Conduit it is a no-op, because native CDC records are already flat.
  - A chain with no unwrap point keeps the envelope throughout.
  - Key-side SMTs are not affected, because the key is not enveloped.
  - A second unwrap point after the first is refused.
  - `replace.null.with.default` must be set explicitly to `false` on every SMT that has the option,
    `ExtractNewRecordState` included (decisions 13 and 19).
  - Flatten and HoistField have no such option. They read through `Struct.get`, which applies defaults. If
    the oracle shows that a defaulted nullable column comes out with its default, they are refused on
    Debezium sources. One thing is not verified: whether Debezium Postgres propagates column defaults into
    the Connect schema. The oracle confirms it.
- **Sink chains.**
  - The converter decides. Only `ByteArrayConverter` is translated, so the value is bytes.
  - SMTs that need a map or a struct fail on bytes in KC itself, and are refused (see the reachability
    matrix).
- **Before-image** (decision 12). Every value-side processor is emitted twice: once for `.Payload.After`
  and once for `.Payload.Before`. The Before copy always carries a nil guard, whatever the SMT
(decision 17), so a nil Before stays nil. The null-side column of the nulls table applies only to the
After copy. KC's value after the unwrap has no
  before-image, so this keeps the two images consistent for CDC consumers rather than matching a KC byte
  stream. The output-shape note already covers the record envelope.

### Nulls and missing fields

KC distinguishes a null side (tombstone or delete), a null field, and a field that is absent. The table gives
the KC behaviour and what is emitted, for each SMT in v0.22. Every row is pinned by an oracle fixture and a
`rapid` property test over null and missing fields.

| SMT | Null side (KC) | Null field (KC) | Missing field (KC) | Emitted |
| --- | --- | --- | --- | --- |
| `ReplaceField` exclude | passthrough | removed | no-op | `field.exclude` with a side nil guard; a missing field is already a no-op |
| `ReplaceField` renames | passthrough | renamed, value null (default replacement off) | schema mode: never absent | one `field.rename` per mapping, so each mapping has its own condition: a side nil guard plus a key-presence guard (tests that the key is present, not that it is non-nil, because a present null field is renamed), so an absent field does not become `new: nil` |
| `InsertField` static/topic | passthrough | n/a | n/a | typed `field.set` with a side nil guard; an existing field of the same name fails (KC schema mode throws on the duplicate), emitted as an `error` processor conditioned on presence |
| `ExtractField` `after` (envelope) | null value | null value | throws | no processor (native CDC is already flat) |
| `HoistField` | wraps null: `{name: null}` | n/a | n/a | `field.hoist`. The After copy has no nil guard and wraps nil; the Before copy is nil-guarded (decision 17) |
| `ValueToKey` | throws | key field null | throws | `error` processor when `.Payload.After` is nil; then `field.set from` per field, where a missing source field fails with `field.set.from_missing` |
| `Flatten` | passthrough | leaf kept as null | n/a | `flatten`, which keeps null leaves and passes a nil side through |
| `ExtractNewRecordState` | see row in the mapping table | | | |

**Nil guards.** A nil guard is ANDed with any predicate. The condition is true only for a non-nil interface,
so `StructuredData{}` is still processed, as in KC. Its exact template is fixed in B3 and tested over a nil
interface, `RawData(nil)` and an empty struct. The guards make translations independent of #3147. When the
SDK fix lands, they become redundant but stay correct.

**Present-field guards and the `error` processor.** These use the existing built-in `error` processor with a
`condition`. KC fails such a record, and the emitted chain fails it too: to the DLQ, or halting, per
`errors.tolerance` (invariant 3). Nothing is coerced.

### Reachability at the pinned versions

What the translators can reach on `main` today. Even where an SMT translates, the pipeline stays `stopped`
in v0.22 because of blockers outside the SMT keys:

- on debezium-postgres: `topic.prefix`, the JsonConverter entry and `kc.output_shape_differs`, until
  Debezium output mode lands in v0.23;
- on s3-sink and mirror-source: `kc.output_shape_differs`.

So in v0.22 an SMT translation is a correct report entry and a processor chain that is ready to run. It does
not by itself make a pipeline runnable.

| Class | Converter | Reachable SMTs in v0.22 | Refused |
| --- | --- | --- | --- |
| debezium-postgres (source, schema mode) | any; JsonConverter schemaless is the oracle target | `Filter` + `TopicNameMatches`, `RecordIsTombstone`; `RegexRouter` and `TopicNameMatches` once T is defined (B4, below); `ReplaceField` exclude/rename, `InsertField` static/topic, `ValueToKey`, `HoistField`, `Flatten`, `ExtractNewRecordState`, `ExtractField after` | `HasHeaderKey` (Debezium headers are v0.23); everything else (v0.23) |
| s3-sink (sink) | `ByteArrayConverter` | `Filter` + `TopicNameMatches`, `HasHeaderKey`; `RegexRouter` if the S3 destination names objects from `opencdc.collection` (checked in B3); `HoistField` on bytes | `RecordIsTombstone` unless B3 shows that the Kafka source distinguishes a null value from an empty one; every SMT that needs a map or struct (KC fails it on bytes) |
| s3-sink | any other | none (the converter is blocking) | all |
| mirror-source (source of records from Kafka) | `ByteArrayConverter` | `Filter` without a topic predicate; `HoistField` on bytes | `TopicNameMatches` (T is not defined for mirror-source: its SMTs see the remote topic name after the replication policy, not `opencdc.collection`; v0.23, decision 20); `HasHeaderKey` (mirror mode omits headers that are not UTF-8); `RegexRouter` (MirrorMaker topic naming is a finisher; combining the two is v0.23) |
| Unknown or no-pipeline classes | any | none: placeholder outcome | all |

`debezium-mysql` has no class file on `main`, so its corpus cases stay pending whatever the SMT work does.

### Mapping and exactness (v0.22)

| SMT | Emits | Exact when | Refused otherwise |
| --- | --- | --- | --- |
| `Filter` | `filter` + `condition` | predicate translatable or absent (no predicate drops everything in both) | - |
| `TopicNameMatches` | condition: anchored match on T (below) | `pattern` is in the RE2-safe ASCII subset and T is known | Java construct named; topic unknown |
| `HasHeaderKey` | condition testing key `kafka.header.N` in `.Metadata` with a `range` and a flag variable (`index` cannot tell an empty value from a missing key; sprig `hasKey` does not take `opencdc.Metadata`) | sink chain from the Kafka source in non-mirror mode | source chains; mirror mode |
| `RecordIsTombstone` | depends on position | source chain before the unwrap: constant false, evaluated at translate time (no processor). Native CDC emits no tombstones, so the records KC's predicate matches do not exist in Conduit; the output-shape note covers that; after `ExtractNewRecordState` in `tombstone` mode, or after `ExtractField after` (no delete mode applies): condition `.Operation` is `delete`; after `drop` mode: constant false | sink chains unless verified (above); other delete modes |
| `RegexRouter` | rewrites T (below) | `regex` and `replacement` translate (below), and no later processor writes what T or the predicate reads | construct named; topic unknown; later write |
| `ReplaceField` | `field.exclude`, then `field.rename`, on After and Before | structured side after the unwrap; `exclude` and `renames`; `replace.null.with.default=false` on Debezium sources; no rename target is also a rename source (KC renames simultaneously from the original names, Conduit renames in sequence) | `include` (v0.23); swap or chain renames; envelope position; default replacement on |
| `InsertField` | typed `field.set` (`type: string`), on After and Before | `static.field`/`static.value`; `topic.field` from T; `!`/`?` suffixes stripped; `replace.null.with.default=false` | `partition.field`, `offset.field` (Kafka source emits them only in mirror mode, which changes what destinations write), `timestamp.field` (v0.23), default replacement on |
| `ExtractField` | none for `field=after` before any unwrap point (it is itself one) | KC's result equals the already-flat native CDC record | every other field: on debezium-postgres every column is a scalar, and a scalar cannot become an OpenCDC side without changing its bytes; on sink chains the value is bytes |
| `HoistField` | `field.hoist`, on After and Before (or on `.Key`) | structured, nil or raw side; on Debezium sources, only if the oracle shows that no default is substituted | as above |
| `ValueToKey` | `error` on a nil value, `field.exclude .Key`, then `field.set` with `from` per field | structured value after the unwrap; `replace.null.with.default=false` on Debezium sources. `field.exclude` accepts `.Key` and sets it to nil, and the later `field.set .Key.<f>` creates the structured key | envelope position; default replacement on |
| `Flatten` | `flatten`, on After and Before (or `.Key`) | structured or nil side after the unwrap; `delimiter` any; the oracle diffs clean on the jsonb and array fixtures (Debezium emits jsonb as a string, so if the Conduit source emits a map, Flatten is refused on debezium-postgres) | envelope position; any fixture that differs |
| `ExtractNewRecordState` | none (`tombstone`, the default); `filter` on `.Operation` delete (`drop`) | native CDC source; `replace.null.with.default=false` (decision 19); no other options set | `replace.null.with.default` unset or `true`; `add.fields`, `add.headers`, `rewrite*` modes and other options (v0.23); a predicate on it (it emits no processor, so it cannot be made conditional); sink chains (`unwrap.debezium` needs the `payload` wrapper of `schemas.enable=true`, which decision 8 refuses, and its `json.Unmarshal` loses int64 precision); Debezium output mode |
| `Cast`, `TimestampConverter`, `TimezoneConverter` | none | - | v0.23 (decision 11) |
| `ExtractChangedRecordState`, `EventRouter`, `DropHeaders`, `InsertHeader`, `HeaderFrom` | none | - | v0.23 (decisions 10, 14, 15) |
| `PartitionRouting` | none | - | `kc.target_capability_missing`: "the Kafka destination partitions by key only; explicit partition writing is v0.23 (per-partition ordering, invariant 4)" |
| Others (`MaskField`, `TimestampRouter`, `SetSchemaMetadata`, `HeaderToValue`, `ContentBasedRouter`, `ByLogicalTableRouter`, Debezium `Filter`, custom) | none | - | `kc.smt_unsupported`; fix names v0.23, or "write a processor" |

Every refusal on a side that carries a schema in KC (decision 8) or in Debezium output mode (decision 5)
comes before any of the rows above.

### Topic expression

The topic is a Go-template pipeline expression, T, carried through the chain. It is never written into
records.

- **Initial T.**
  - debezium-postgres: undefined until the Postgres connector emits the same `opencdc.collection` in
    snapshot and in CDC. A fix for that is in progress. Once it lands, B4 defines T as
    `<topic.prefix>.<schema>.<collection>`, and only when every captured table is in one schema named by
    `table.include.list` or `schema.include.list` and `topic.delimiter` is the default. Until then, the
    topic-reading and topic-writing SMTs on debezium-postgres are refused.
  - Sink chains: `(index .Metadata "opencdc.collection")`.
  - mirror-source: undefined in v0.22. Its SMTs see the remote topic name after the replication policy, so
    the sink-chain T would match the wrong name. Topic-reading and topic-writing SMTs there are refused
    (decision 20).
- **RegexRouter** (fixes the replace-all anchoring):

  ```text
  {{ if regexMatch `^(?:P)$` (T) }}{{ regexReplaceAll `^(?:P)` (T) `R` }}{{ else }}{{ T }}{{ end }}
  ```

  - KC runs `matches()`, then `replaceFirst`, which finds from index 0 with leftmost-first preference. So a
    proper prefix can be replaced: `a|ab` on `ab` gives `Xb`. The pattern anchored only at the start
    reproduces that in RE2, which also prefers the leftmost-first match.
  - T is a pipeline, such as `index .Metadata "opencdc.collection"`, so it is always parenthesised when it
    is an argument. The same applies in `TopicNameMatches` conditions and inside nested rewrites.
  - A predicate on a RegexRouter becomes `{{ if PRED }}…{{ else }}T{{ end }}`.
  - T is evaluated against the final record, so the pass refuses a RegexRouter, or a predicate, when a
    later processor in the chain writes any path, field or metadata key that it reads. A processor whose
    writes are a whole side conflicts with any read of that side.
- **At the end of the chain**, T is written to the plugin's topic or naming setting from the capability
  table. On debezium-postgres that is the Kafka destination's `topic`. `topic.prefix` stays blocking on its
  own entry, for the output-mode reason.

**Regex translation.**

- The pattern is parsed with `regexp/syntax`, and every node must be on an allow-list of constructs with the
  same meaning in Java and RE2:
  - literals and `.`;
  - classes without POSIX bracket names;
  - `\d \w \s` with their ASCII meaning;
  - the anchors `^ $ \b`;
  - greedy and lazy quantifiers;
  - capturing and non-capturing groups.
- Pattern literals must be ASCII. RE2's `(?i)` folds Unicode, so a Kelvin sign would match `k`, while Java
  folds ASCII only by default. `(?i)` is the only flag allowed.
- Refused: lookaround, backreferences, possessive and atomic forms, `\h`, `\Z`, `\p{…}`, `[[:alpha:]]`.
- Replacement translation, following Java's rules:
  - `$n` takes the longest digit run that is still a valid group number, and becomes `${n}`;
  - `${name}` stays a named group;
  - `\x` becomes the literal `x`;
  - a literal `$` becomes `$$`.
- Patterns and replacements are written as backtick raw strings inside templates, because `"\d"` is a
  template parse error. One that contains a backtick is refused.

**Quoting.** Field references whose names contain `.` or other special characters use bracket form
(`.Payload.After["a.b"]`). Static values are written as quoted Go template string literals.

### Schema metadata after reshaping

When a chain reshapes a side that carries `opencdc.<side>.schema.subject`/`.version`, a `field.exclude` of
those two keys is emitted **once per distinct condition, immediately before the first reshaping processor
under that condition**. It covers only the reshaped side, not the whole chain. A record that skips every
reshaping processor because of its condition keeps its schema metadata, as it keeps its shape. Placed after
the reshaping processor, the exclude would come too
late: each built-in processor's encode middleware would already have registered a widened schema as a new
version of the source subject and encoded the data with it.

The new processors (`flatten`, `field.hoist`, and `field.set` with `from` or a non-string `type` under
`.Key`/`.Payload`) also drop the two keys themselves, so hand-written pipelines are safe. A test asserts that
the schema registry holds no new version after the chain runs.

Decision 8 refuses reshaping SMTs on sides whose KC converter carries a schema, with the reason "a
schema-carrying converter needs a type-preserving schema for the reshaped data; Conduit would register one
with widened types, so this is refused until v0.23". The SMTs that do not reshape (`Filter`, the
predicates, `RegexRouter`) are unaffected.

### Processor requirements (B1, B2)

**`field.set` (B1).**

- New `from` parameter: a field reference whose value is deep-copied into `field`, type kept.
  - `from` and `value` are mutually exclusive. Configure-time errors: `field.set.value_and_from` when both
    are set, `field.set.value_required` when neither is, and an error for a template in `from`.
  - We chose `from` over `type: reference`, because then the meaning of `value` would depend on another
    parameter.
- `from` at run time:
  - a nil value sets the target to nil; if the target is a side, the side becomes nil, never `{}`;
  - a missing path fails with `field.set.from_missing`;
  - a value going into a side (`.Key`, `.Payload.*`) must be `StructuredData` or nil; anything else fails
    with `field.set.side_not_structured`.
- `type` (`string` default, `int`, `float`, `bool`, `time`) converts the template output using
  `field.convert`'s code. Setting `type` together with `from` is a configure error.
- When the value written under `.Key`/`.Payload` comes from `from` or a non-string `type`, the processor
  drops that side's schema keys.
- The default is byte-identical to today, `tplerr.Exec` wrapping included. This is checked against a frozen
  copy of today's `Process` over generated records, and `value: ".Payload.After.id"` still sets that
  literal string. The version moves to v0.2.0.

**`flatten` (B2).**

- `field` is `.Key`, `.Payload.Before` or `.Payload.After`; `delimiter` defaults to `.`.
- A nil side passes through. Raw data fails with `flatten.not_structured`.
- Null leaves are kept as nil, and a nil nested value `a: nil` is kept as `a: nil`, as in KC's schemaless
  mode. In schema mode KC expands a null nested struct into per-field nulls, which Conduit cannot do without
  a schema. Postgres rows have no nested structs, so this does not arise on debezium-postgres, and the
  oracle fixture records it.
- An empty nested map produces no key. Arrays are unchanged. `time.Time` and other leaves are unchanged.
  KC's schemaless mode throws on non-Connect Java types; that is recorded as an oracle fixture.
- A collision (two paths flattening to one key) fails with `flatten.key_collision`, naming both source paths
  and never the values. This does not depend on map iteration order.
- Drops the side's schema keys. Registered as a built-in, with a doc page and examples.

**`field.hoist` (B2).** Parameters: `field` (`.Key`, `.Payload.Before` or `.Payload.After`) and `name`. It
replaces the side with `{<name>: <old side>}`:

- a structured side is deep-copied;
- raw data becomes `[]byte`;
- nil becomes `{<name>: nil}`, as KC wraps a null.

It drops the side's schema keys.

## Alternatives considered

**Run the SMTs in a JVM (the v0.23 connector JAR host).** This would be exact for every SMT, custom classes
included. It lost because it puts a JVM into a v0.22 migration path and leaves no Conduit-native pipeline to
own afterwards. It remains the answer for custom SMTs.

**Approximate, and let the user accept the difference.** It lost because an approximated transform reshapes
data differently (invariant 6), and "approximated" does not tell a reviewer which records change.

**Auto-detect copy mode in `field.set` (the WIP).** It lost because it silently changes existing pipelines.

**`type: reference` instead of `from`.** It lost because the meaning of `value` would then depend on another
parameter.

**Emit the exact links of a partly refused chain.** It lost because later links would see different data
than in KC.

**Write the topic into `opencdc.collection` at each RegexRouter.** It lost because it mutates every record,
changes what destinations see, and does not cover a `TopicNameMatches` that comes before the router.

**Ship Cast, TimestampConverter and TimezoneConverter with a conformance table keyed by target type.** It
lost because the same column reaches KC and Conduit as different types: `numeric` as `*big.Rat` against
`Decimal`, and `timestamp` as `time.Time` against a `MicroTimestamp` INT64. Column types are not known
offline either.

**Clear `.Payload.Before` on chains that reshape the value.** This is closer to KC's value after the unwrap.
It lost to decision 12, which keeps the before-image consistent for CDC consumers.

## Failure modes

| Failure | Effect | Prevention or detection |
| --- | --- | --- |
| A translator claims exactness for a config that is not exact | Different data downstream | Narrow cut line; every SMT gated on a clean oracle diff; all-or-nothing chains; review against KC source |
| Null or missing field handled differently | `"<nil>"` strings, invented `new: nil`, lost nulls | Per-SMT null/missing table; side and field guards; `error` where KC throws; property tests over null and missing fields |
| Schema default substituted by KC, not by Conduit | A null where KC wrote a default | `replace.null.with.default=false` required on Debezium sources; oracle fixture with a defaulted nullable column |
| Java and RE2 regexes differ | Wrong topic or predicate | Allow-list over syntax nodes, ASCII literals, start-only anchoring, Java replacement translation; fuzz and oracle seeds (`a\|ab`, lazy quantifiers) |
| T diverges from KC's topic | Wrong routing | T defined only where pinned; refused when unknown or when a later processor writes something it reads |
| Delete handled differently from KC's tombstone after the unwrap | Records KC dropped are written | `RecordIsTombstone` depends on chain position and delete mode |
| Before-image left untransformed | Excluded column survives in `.Payload.Before` | Every value-side processor also emitted on Before |
| `flatten` key collision | Silent overwrite (without the rule) | `flatten.key_collision`, to the DLQ or halting (invariant 3) |
| Nil side becomes `{}` (#3147) | A delete or tombstone becomes an empty upsert | Nil guards; nil handling in the new processors |
| Lossy schema registered under the source subject | Widened types, a polluted subject | Schema-key exclude before the first reshaping processor; registry test; decision 8 |
| `field.set` default changes | Existing pipelines change | Byte-identical test against a frozen reference |
| Connector bump changes header or partition behaviour | Translations made against stale capabilities | Capability table checked against the pinned specs |
| KC adds an SMT option | Option ignored | `kc.key_unknown`, blocking |
| Nondeterministic output | Flaky goldens; CLI and MCP outputs differ | Ordered aliases, deterministic IDs; property test |
| Oracle image disappears (as Debezium 3.7.0 did on quay) | B11 cannot recapture | Pin the image by digest and mirror it; committed inputs and outputs keep the `test` gate working |
| Before copy invents an image | Inserts get `Before: {name: nil}` | Before copies always nil-guarded (decision 17); property test |

## Tests

- **Corpus.**
  - SMT cases go in the existing `debezium-postgres` and `s3-sink` families. Those class files exist, so
    readiness needs no harness change. Each case commits its pipeline YAML and report goldens and lands
    with its translator PR.
  - The cases cover: each translated SMT, each refusal reason, mixed chains, Debezium output mode, the
    schema-carrying converters, `replace.null.with.default` left unset, and predicated non-emitting SMTs.
- **KC oracle (B11).**
  - `tests/migrate/smt-oracle` runs Debezium Postgres (the corpus's pinned version, image mirrored) and
    Kafka Connect 4.2 with the case's SMT chain and a schemaless JsonConverter, against a fixture database.
  - The capture writes, per case, two files that are committed with provenance:
    - the **input records**: the OpenCDC records the Conduit Postgres source produced from the same
      database, captured in the same run;
    - the **KC output**: key, value, topic and headers per record.
  - **Enforcement point (decision 21).** Each translator PR adds a Go test to the required `test` job. It
    needs no Docker and no JVM.
    - The test generates the case's pipeline with migrate, feeds the committed input records through the
      emitted processor chain in-process, and diffs the result against the committed KC output with the B11
      diff helper.
    - That makes the "diff clean" gate blocking on every PR.
    - The diff helper applies a fixed set of normalizations. They cover representation differences that
      come from the source connector, not from the SMT, and each one has its own test.
  - The end-to-end run (Docker Postgres, Debezium, Kafka Connect 4.2 and the Conduit source) stays in the
    B11 workflow, manual and nightly. It recaptures both files and fails if either changes unexpectedly.
  - "Diff clean" means no difference after normalization. An SMT whose case does not diff clean is not in
    v0.22. If B11 slips, B4 and B5 do not merge, and every SMT keeps its v0.23 refusal.
  - Fixtures:
    - nullable columns, with and without a default;
    - TOAST-unchanged columns;
    - jsonb and array columns;
    - deletes and tombstones;
    - names containing `.`;
    - a Kelvin sign;
    - `a|ab` and lazy-quantifier regexes;
    - rename swaps;
    - an existing field named like an InsertField target;
    - a chain with `ExtractField after` followed by `Filter` + `RecordIsTombstone`;
    - an insert, which has no before-image, through `HoistField` and `Flatten`.
- **Property tests** (`rapid`, already used by `field.exclude`'s property test):
  - completeness, including `transforms` and `predicates` keys;
  - determinism under permuted input key order, identical modulo processor IDs when aliases are renamed;
  - all-or-nothing chains;
  - per translator, null and missing fields on each side;
  - a nil `.Payload.Before` stays nil through every emitted chain;
  - `flatten`: every leaf appears exactly once, or the record fails with a collision;
  - `field.set from` never aliases its source;
  - the byte-identical `field.set` default.
- **Fuzz** targets on the regex classifier and the replacement translator, in B4. Their seed corpora run in `test`.
- **Processor unit tests** over raw, structured, nil and error records, with `Example*` functions.

## Docs

Each PR updates its own docs. B10 covers the cross-cutting pages:

- `docs/catalog-inventory.md` (SMT parity rows);
- the Kafka Connect migration guide in the `conduit.io` docs source (an SMT section with the reachability
  matrix, the exactness table and the refusal reasons);
- `llms.txt`;
- processor pages for `flatten`, `field.hoist` and the new `field.set` parameters;
- changelog entries.

## Upgrade, rollback and compatibility

- **Existing pipelines are unaffected.** Every new behaviour is opt-in, and the `field.set` default is
  pinned.
- **migrate output changes** only for inputs with transforms, and goldens move in the same PRs. The report
  schema is unchanged.
- **Rollback.** A pipeline that uses `flatten`, `field.hoist`, `from` or `type` fails validation on v0.21:
  `conduit-commons` rejects unknown processors and parameters, so it fails at start and never runs
  differently. Rolling back means regenerating with the older migrate.

## Observability

- **The report is the primary signal.** The `transforms` index lists every alias with its status, and each
  key's `configPath` points at its processor.
- **Runtime refusals are coded errors on the record**, routed to the DLQ or halting per `errors.tolerance`.
  The codes are `flatten.key_collision`, `flatten.not_structured`, `field.set.from_missing`,
  `field.set.side_not_structured`, and `error`-processor records whose messages name the SMT alias and the
  field path. None of them carries a value.
- **Metrics.** The emitted processors use the standard processor metrics. migrate runs offline and adds
  none.

## PR breakdown and cut line

v0.22 cuts on 2026-11-20.

| PR | Content | Depends on | Tier | Release |
| --- | --- | --- | --- | --- |
| B1 | `field.set`: `from`, `type`, the runtime rules above, schema-key drop, byte-identical test, v0.2.0 | - | 2 | v0.22 (required) |
| B2 | `flatten` and `field.hoist` per the requirements above; registration; doc pages | - | 2 | v0.22 (required) |
| B3 | Framework only: SMT pass in `migrateConnector`; placeholder rule data and the `smt` kind; `processors` in the pipeline model; grouping and per-key outcomes, including dotted-prefix aliases; per-side converter shape and the schema-carrying flag; capability and sub-key tables in Go; nil, field and `error` guards; schema-key exclude placement per condition; all-or-nothing; `report.Transform`; MCP parity; the `PartitionRouting` and v0.23 refusals | - | 2 | v0.22 (required) |
| B11 | KC oracle: capture program, pinned and mirrored images, manual and nightly workflow; committed input records and KC outputs; the Go diff helper and normalizations; the offline replay harness the `test` job uses | - | 3 | v0.22 (required; if it slips, B4 and B5 do not merge) |
| B4 | `Filter` and the predicates; the regex translator and `RegexRouter`; the debezium-postgres T (after the Postgres `opencdc.collection` fix); the regex fuzz targets; `ReplaceField` exclude/rename, `ExtractNewRecordState`, `ExtractField after` | B3, B11 | 2 | v0.22, per SMT, if its case diffs clean in `test` |
| B5 | Translators over new processors: `InsertField` static/topic, `ValueToKey`, `HoistField`, `Flatten` | B1, B2, B3, B11 | 2 | v0.22, per SMT, if its case diffs clean in `test` |
| B10 | Cross-cutting docs | B4, B5 | 3 | v0.22 |
| B6 | `ExtractChangedRecordState` | header-writing destination | 2 | v0.23 |
| B7 | `Cast`, `TimestampConverter`, `TimezoneConverter` with runtime type gates proven by the oracle | B11 | 2 | v0.23 |
| B8 | `EventRouter` | B1, B3 | 2 | v0.23 |
| B9 | `PartitionRouting`: explicit Kafka partition, with its own design doc | v0.23 | 1 | v0.23 |
| B12 | `ReplaceField include`, `DropHeaders`/`InsertHeader`/`HeaderFrom`, `ExtractNewRecordState` options, type-preserving schema derivation | B3 | 2 | v0.23 |

**Critical path.** B3 and B11 run in parallel, then B4 and B5 in parallel; B1 and B2 run alongside B3. B11
needs no translator code and starts on day one. The minimum v0.22 is B1, B2, B3 and B11.

Landing partially is safe. An SMT whose translator misses the date, or whose case does not diff clean,
keeps its refusal with a v0.23 fix, so no report gets worse.

## Open questions

None. The decisions above (2026-11-10) close every question from the first draft and its review.

## Related

- `docs/design-documents/20261012-migrate-kafka-connect.md` ("Single message transforms and predicates"),
  which this extends.
- `docs/design-documents/20261014-migrate-kafka-connect-mapping-and-harness.md`: rule data, corpus,
  readiness and MCP parity.
- `docs/catalog-inventory.md`, "Kafka Connect SMT parity".
- ADR `20260704-no-bespoke-dsl`.
- Issues #2960 (SMT pack), #3147 (nil side becomes `{}`), #3139 (processor property tests).
- WIP commit 6887b977 on `wip/v022-smt-pack` (not for merge).
- Roadmap: v0.22 "SMT compatibility pack".

# Field references without side effects

## Summary

When a processor resolves a field reference, the processor SDK creates every missing parent on
the way to the field. It does this even when the processor only reads the field or deletes it. As
a result:

- a nil key or payload side becomes `{}`, so deletes and tombstones lose their nil after-image or
  key;
- a missing intermediate object becomes `{}`, so `field.exclude` of `.Payload.After.a.b` adds
  `"a": {}` to every record that has no `a`;
- nil metadata becomes an empty map.

The records are acked with the added data and no error is raised: invariant 6. This note
proposes that references create parents only when a value is written, and that a read or delete of
a missing field leave the record unchanged. Tracked in
[#3147](https://github.com/ConduitIO/conduit/issues/3147). No fix is made until this is approved.

## Context

### Where it happens

`conduit-processor-sdk`, `internal/reference/reference.go`. `Resolver.Resolve` walks the path one
segment at a time, and each `walk` creates what is missing:

| `walk` on | When the value is | It writes |
| --- | --- | --- |
| `keyReference`, `payloadBeforeReference`, `payloadAfterReference` | nil | `opencdc.StructuredData{}` into the record |
| `metadataReference` | nil | `make(map[string]string)` into the record |
| `dataFieldReference` (any nested object) | nil or absent | `make(map[string]any)` into the parent |

This happens in `Resolve`, before the processor calls `Get`, `Set`, `Delete` or `Rename`. A
reference one segment deep (`.Key`, `.Payload.After`, `.Metadata`) does not walk, so it creates
nothing. Any reference two or more segments deep does.

There is a related quirk in `dataFieldReference.Rename` on a field that is not there. It copies the
absent value under the new name and then deletes the old name, which leaves `newName: nil`. This
follows from the code; I have not run it.

### Since when

- The SDK has behaved this way since referencing was added: `c84a242`, "Referencing fields (#14)",
  2024-02-08, released in processor-sdk v0.1.0. It is unchanged on the SDK's `main`, `c8b0705`
  (v0.8.1). Conduit uses v0.8.0.
- The built-in processors moved to SDK references in Conduit `00dbcba7`, "Better processors
  (#1378)", 2024-03-07, first released in v0.9.0. Every Conduit release since v0.9.0 is affected.
- Standalone (WASM) processors built with the SDK carry the same code. They keep the behavior
  until they are rebuilt against a fixed SDK.

### Which processors

Every built-in that resolves a configurable field is affected when that field is two or more
segments deep:

- `field.exclude`, `field.rename`, `field.set`, `field.convert`;
- `json.encode`, `json.decode`, `base64.encode`, `base64.decode`;
- `avro.encode`, `avro.decode`;
- `unwrap.debezium`, `unwrap.kafkaconnect`, `unwrap.opencdc`;
- `split`;
- `webhook.http`, for its request and response body fields;
- `openai.embeddings`, `openai.textgen`, `cohere.embed`, `cohere.command`, `cohere.rerank`,
  `ollama`.

Reproduced on `main`, from the property tests in #3139 and direct probes:

| Processor and field | Input | Output |
| --- | --- | --- |
| `field.exclude` `.Payload.After.a`, `.Key.a`, `.Metadata.a` | `After`, `Key`, `Metadata` nil | `{}`, `{}`, `{}` |
| `field.exclude` `.Payload.After.a.b` | `After: {"x": 1}` | `After: {"a": {}, "x": 1}` |
| `json.decode` `.Payload.After.a` | a delete: `Before: {...}`, `After: nil` | `After: {}` |
| `json.decode` `.Key.a` | `Key: nil` | `Key: {}` |
| `json.encode` `.Payload.After.n` | `After: nil` | `After: {}` |

The read-only processors (the AI processors' input fields, the webhook request body) do the same
whenever their field is nested. Their defaults (`.Payload.After`, `.`) are one segment deep and do
not trigger it.

### Why it matters

- A nil `Payload.After` is how a delete is represented, and a nil value is a tombstone for
  Kafka-style destinations. `{}` is an upsert of an empty row, or a non-null message. A destination
  that branches on nil then does something different. I have not traced this through a specific
  destination; this is the case to check first.
- An added `"a": {}` changes the record's shape. That can fail schema validation downstream, or
  create a column or field the source never had.

## Constraints

- Records that a processor did not change must not change. A field that is not there is read as
  nil, and deleting it does nothing.
- `field.set` and any other `Set` on a missing path must keep creating the parents. That is the
  documented use: `field.set` on `.Payload.After.a.b` with a nil `After` creates both levels.
- The SDK's public interfaces (`sdk.ReferenceResolver`, `sdk.Reference`) keep their method sets.
  Standalone processors compiled against them must still build.
- No change to the connector protocol or any serialized format.

## Decision (proposed)

**Resolve without writing, and create parents on `Set` only.**

1. `Resolve` walks without modifying the record. When a segment is missing, it returns a reference
   that records the path it reached and the segments still to create.
2. On that reference:
   - `Get` returns nil.
   - `Delete` is a no-op.
   - `Rename` of a missing field is a no-op. This also removes the `newName: nil` quirk. Renaming
     onto an existing name stays an error, as today.
   - `Set` creates the missing parents, then sets the value: exactly what `Resolve` does today, but
     only on write.
3. Where a segment is present but has the wrong type, `Resolve` still fails as today: for example
   raw data where a field is needed, or a string where an object is needed.
4. The fix lands in `conduit-processor-sdk` first, as a minor version. Conduit then bumps the SDK,
   with a changelog entry under "Behavior changes". Standalone processors pick it up when rebuilt.

Two sentences: today, reaching a field builds the road to it, even when you only wanted to look.
After this change the road is built only when you write something at the end of it.

## Alternatives

1. **Two resolve modes.** Add `ResolveExisting` next to `Resolve` and move the read-only and delete
   paths of the built-ins to it. It lost because it fixes only the call sites that are changed.
   Every custom and standalone processor keeps the side effect, and nothing stops a new built-in
   from using the wrong mode. The bug is in what `Resolve` does, so fix it there.
2. **Undo it in each processor.** Snapshot the nil sides before processing and restore them when
   the processor left `{}`. It lost because it cannot tell an intended `{}` (a `field.set` of `{}`)
   from an accidental one, it does nothing about added intermediate objects, and it has to be
   repeated in every processor.
3. **Undo it in the engine** after each processor, the same way. It lost for the same reasons, and
   because it would change what processors may write, which is a contract change.
4. **Document it and keep it.** It lost on invariant 6: a processor that removes a field should not
   add one, and nothing in the docs warns operators that a delete can become an upsert.

## Failure modes

- **A processor relied on the side effect.** Code that calls `Resolve` and then writes into the
  parent by another route, without `Set`, would now find the parent missing. I found no built-in
  that does this. Standalone processors might. The SDK's changelog must say so, and the SDK's
  reference tests must cover `Get`, `Delete` and `Rename` on missing paths.
- **A destination relied on `{}`.** A pipeline whose processor turned deletes into `{}` will start
  delivering nil after-images again. Nil after-images are the normal shape of deletes from every
  CDC source, so destinations already handle them. The release note names the change so operators
  can check.
- **Sequences of references.** A processor that resolves several references in a row, for example
  `field.exclude` with several fields, previously saw parents created by earlier references. With
  lazy creation, each reference sees the record as it is. Excluding `.a.b` and then `.a` deletes
  nothing on a record without `a`, which is correct.
- **Concurrency:** none added. References are used on one record by one goroutine, as now.

## Upgrade and rollback

- **Upgrade:** a minor SDK version, then Conduit bumps the SDK. Pipelines see output that matches
  their input wherever a processor did not change a field. No state, position or protocol change.
- **Rollback:** pin the previous SDK, or revert the Conduit bump. The old behavior returns. Nothing
  persisted depends on the new behavior.
- **Mixed versions:** built-in and standalone processors can be on different SDK versions in one
  pipeline. Each behaves according to the SDK it was built with. Document which version fixes it.

## Observability

No new metric. The change removes a silent mutation; it does not detect one. The tests carry the
guarantee:

- the `field.exclude` property in #3139 drops its nil→`{}` tolerance;
- a property is added across every built-in that takes a field: for records where the processor
  changes nothing (a missing field, an excluded field that is not there), the output equals the
  input, nil sides included;
- the SDK's reference tests cover `Get`, `Delete`, `Rename` and `Set` on missing paths at every
  level.

## Open questions for DeVaris

1. Ship the SDK fix as a minor version with a "Behavior changes" note, or treat it as a bug fix in
   a patch? I recommend minor, because output changes for pipelines that hit it.
2. Backport to `release/v0.20.x`? It is invariant 6 and has been live since v0.9.0, but it changes
   output for running pipelines. I recommend v0.21 only, with the release note, unless a
   destination is found that mishandles `{}` for deletes.

## Related

- [#3147](https://github.com/ConduitIO/conduit/issues/3147): the issue.
- [#3139](https://github.com/ConduitIO/conduit/pull/3139): the property tests that found it.
- [`ConduitIO/conduit-commons#336`](https://github.com/ConduitIO/conduit-commons/issues/336):
  `Record.Clone` turns nested `map[string]any` into `StructuredData`. `dataFieldReference.walk`
  accepts only `map[string]any` for a nested object, so a nested field of a cloned record may not
  resolve. That needs checking alongside this change.
- `docs/design-documents/20231117-better-processors.md`: where field references were introduced.

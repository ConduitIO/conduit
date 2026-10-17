# Structured data crosses plugin boundaries as typed values, negotiated per peer

## Summary

**Status: Proposed. Implementation comes after v0.21.** This ADR takes effect only after the
`conduit-connector-protocol` versioning discussion
(`ConduitIO/conduit-connector-protocol#274`) concludes and DeVaris accepts it.
Until then, nothing here changes any wire format.

Structured record data will cross plugin boundaries as a new `opencdc.v2` value message with real
integer, unsigned, bytes and decimal kinds, instead of `google.protobuf.Struct`. Each pair of peers
negotiates it. Connectors use a new `conduit-connector-protocol` version 3, through go-plugin's existing
`VersionedPlugins` negotiation. WASM processors use a capability flag in `Specify.Response`. A peer that
does not negotiate it keeps protocol v2 and `opencdc.v1`, together with the v0.21 refusal of integers
outside ±(2^53−1). **No field is ever added to `opencdc.v1.Data` in place.**

## Context

`opencdc.StructuredData` crosses the plugin protocol as `google.protobuf.Struct`, where every number is
a double. Integers beyond 2^53 are rounded, which causes wrong-row deletes in SQL destinations (#3100).
`[]byte` comes back as a base64 string, and NaN and ±Inf come back as strings. v0.21 makes the integer
case loud: `ToProto` refuses integers outside ±(2^53−1). See
`docs/design-documents/20261015-structured-data-integer-precision.md`, which decided step 1 and
recommended this direction for step 2. Refusing is a floor, not a fix. A pipeline with 64-bit keys can
still only cross a standalone boundary as Avro (`RawData`), which cannot carry `uint64`, or as raw JSON.

The constraints that shape the decision:

- **An in-place oneof field silently drops data.** If `opencdc.v1.Data` gained a third oneof field, an old
  peer would decode a message that sets it as "no data", because `dataFromProto` returns `nil` for an
  unset oneof. The whole payload would disappear without an error. So the new shape must be
  unreachable for an old peer, not merely ignorable by one.
- `conduit-connector-protocol` is a public contract. A change needs a versioning plan and a deprecation
  policy of announce, then warn, then remove, over at least two minor versions.
- Connectors already have version negotiation. Today `pconnector/client` and `pconnector/server` offer
  `VersionedPlugins` `{1, 2}`, and both sides pick the highest version they share. WASM processors have
  no handshake. The processor protocol (`processor/v1`) is a fixed command set.
- Built-in plugins already see `int64` and `uint64` in-process. Standalone plugins see `float64`. That is
  a parity gap.

The alternatives considered in the design doc lost for these reasons:

- String-encoded integers with a metadata hint: old receivers get strings, and MySQL compares a `BIGINT`
  with a string as doubles. The hints also go stale when processors rename fields.
- Avro `RawData` on every leg: receivers without decode middleware get bytes. It also adds schema
  inference per record in the engine, cannot carry `uint64`, and depends on the archived `hamba/avro`
  (#2817).

## Decision

Proposed, for the versioning discussion to confirm or amend:

1. **`opencdc.v2` messages in `conduit-commons`:** `Record`, `Change`, `Data` and a recursive `Value`.
   `Data` is a oneof of `raw_data` (bytes) and `structured_data` (a map from string to `Value`). The
   kinds of `Value` are `null`, `bool`, `double`, `string`, `int64`, `uint64`, `bytes`, `list`, `struct`,
   and `decimal` (canonical decimal string). Room is reserved for `timestamp`. `opencdc.v1` is frozen.
2. **Go mapping:** the encoder keeps the Go type. `int*` becomes `int64`, `uint*` becomes `uint64`, and
   `[]byte` becomes `bytes`. The decoder returns `int64`, `uint64` and `[]byte`, which match what
   built-in plugins see. `json.Number` integers in range map to `int64`, and other `json.Number` values
   map to `decimal`. NaN and ±Inf stay as doubles.
3. **Connectors: `conduit-connector-protocol` v3.** Its messages are the v2 ones with `opencdc.v2.Record`
   in place of `opencdc.v1.Record`. Conduit and the SDK offer `{1, 2, 3}`, so go-plugin selects v3 only
   when both sides have it. An old plugin, or an old Conduit, negotiates v2, and the v0.21 refusal
   applies.
4. **WASM processors:** a capability field in `Specify.Response` (`structured_values_v2`, which proto3
   defaults to false) and v2 record fields in `Process`. The host sends v2 only to a guest that declared
   the capability. An old guest is never sent a v2 record.
5. **Deprecation of protocol v2:** announce it in the release that ships v3, warn on v2 negotiation in
   the next minor release, and remove it no earlier than two minor releases after the announcement.
6. **Upgrade testing:** every combination of {old, new} Conduit and {old, new} plugin, for both
   connectors and processors. The test asserts that a value outside ±(2^53−1) either crosses exactly
   (v3 on both sides) or is refused loudly. It must never be rounded and never disappear.

## Consequences

- 64-bit integers, unsigned integers, bytes and decimals cross standalone boundaries exactly, and
  standalone plugins see the same Go types as built-in ones.
- A plugin that type-asserts `float64` on integer fields breaks when it is rebuilt against the SDK that
  enables v3. Such code already breaks when the plugin runs built-in. The SDK release notes and an
  acceptance-test case for integer fields must call this out.
- The protocol carries two record shapes for the length of the deprecation window, which means more
  conversion code and more tests.
- The v0.21 refusal stays permanently as the behavior for v2 peers.
- Out of scope: a timestamp kind (reserved only), and changes to `Metadata` (still `map<string,string>`).

## Related

- #3100: the integer precision issue.
- `docs/design-documents/20261015-structured-data-integer-precision.md`: step 1, decided 2026-10-15.
- `ConduitIO/conduit-connector-protocol#274`: the versioning discussion for this change.
- #2817: archived `hamba/avro`.

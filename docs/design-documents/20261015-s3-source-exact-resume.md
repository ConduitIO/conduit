# S3 source: exact resume across restarts

## Summary

The S3 source (`ConduitIO/conduit-connector-s3`, below `s3#N`) must be Certified as a source in v0.21, which
means passing acceptance suite v2 case SRC-03 with `ExactResume`: after a restart from the last acked position,
no record that was already acked may be delivered again, and nothing may be skipped. Today a restart redelivers
everything, in two ways:

- a restart during the snapshot starts the snapshot over (s3#1041);
- a restart during change capture (CDC) re-lists the lookback window (`lookbackPeriod`, default 5m) with an
  empty dedupe set, so every object modified in the last five minutes comes again.

This document replaces the snapshot and CDC position model so that the position itself says exactly what was
delivered. The CDC keeps its tolerance for objects that become visible late (the reason the lookback exists),
and gives up some latency only when the window is busier than a configured bound.

The change in one paragraph. The position carries a **settled cut** `S`: every object version at or below it
(in a total order of LastModified, key and version id) has been delivered, and by the lookback assumption no new
version can still appear below it. It also carries a **bounded set `E`** of the versions above `S` that were
already delivered early, as 16-byte hashes. A restart lists from `S` and delivers every version above it that is
not in `E`. The set is bounded by `cdc.maxEarlyObjects`. When the window holds more objects than the bound,
the source does not fail and does not shrink the window. It delivers the excess only once those objects have
settled (they drop below `S`), so latency for them rises to at most `lookbackPeriod`. The snapshot delivers only
objects that were settled when it started, resumes after the last acked key (s3#1041), and leaves the rest to
the CDC. That makes the handoff exact with an empty `E`.

Roadmap item: v0.21: S3 sink parity (#3009), certification of `builtin:s3` as source and destination. Risk
tier: **Tier 1**: position format, the source data path, invariants 2 and 3. Implementation waits for
DeVaris's sign-off on this document.

## Context

### How the source works today (v0.9.5, `main` at 0a47c12)

- **Snapshot**: `ListObjectsV2` in key order, one record per object. Position `<key>_s<unix seconds>`.
  `NewCombinedIterator` throws snapshot positions away and starts over ("for consistency"), because nothing
  in the position says where CDC must start for objects written during the snapshot.
- **Handoff**: an anchor is the newest LastModified at snapshot start (one extra listing pass). CDC starts at
  `anchor - lookback`. #1052 (in review) seeds the CDC dedupe set with what the snapshot delivered inside that
  window, so a run where nothing fails no longer delivers duplicates (SRC-02).
- **CDC**: every poll runs `ListObjectVersions` over the prefix and selects the versions at or after
  `floor = min(newest seen, host clock) - lookback` that are not in an in-memory dedupe set (key, version id,
  ETag, LastModified). Position `<key>_c<unix seconds of the record's LastModified>`. On restart the dedupe set
  is empty.

### Why the lookback exists, and the assumption everything rests on

S3 lists in key order, stamps an object with the time its upload **started** (a multipart upload with the time
it was initiated), and reports LastModified at one-second resolution on AWS (milliseconds on MinIO). An object can
become visible after newer objects were already listed. The lookback re-examines a window so that such an object
is still delivered. The **lookback assumption** is: no object version becomes visible in listings more than
`lookbackPeriod` after its LastModified. The connector documentation already states that objects beyond it can be
missed (listing-based CDC is best effort; lossless S3 CDC needs bucket notifications). This design keeps exactly
that assumption and adds no new one.

### What SRC-03 requires

Seven kills: three during the snapshot, one exactly at the snapshot/CDC boundary, two during CDC with writes
after `Open`, then a final drain. With `ExactResume`, no acked record may come again on any restart, and no
written record may be missing at the end. It scales the data set with any declared `MaxRedelivery`, so a large
bound cannot hide a full re-read of the window. The in-process kill does not run `Teardown`.

### Constraints

- A position is an opaque byte string per record. Conduit persists the position of the last acked record. A
  position must therefore be self-contained: resuming from **any** record's position must be exact.
- Every in-flight record carries its position, so position size costs memory times records in flight, and
  bytes in every gRPC message between the plugin and Conduit.
- Connectors have no durable state other than the position. Writing state to the bucket or to a local file is not
  atomic with Conduit's position persistence.
- No new dependencies. No change to the connector protocol or to Conduit.

## Decision

### Total order and the settled cut

Versions are ordered by the tuple `(LastModified, key, version id)`. The version id is `"null"` in an
unversioned bucket. LastModified is taken from the listing, at the resolution the store provides.

The **settled cut** at a poll is `C = storeNow - lookbackPeriod`. `storeNow` is the `Date` header of the
`ListObjectVersions` response, which is the store's own clock, capped at the host clock. It falls back to the
host clock with a warning when the header is missing or unparseable. By the lookback assumption, every
version with `LastModified <= C` is visible in this listing.

The newest LastModified seen, which today's floor uses, is not usable here. It does not advance while the bucket
is idle, so objects waiting to settle would wait forever.

### CDC position state

A CDC position holds:

- `S`, the settled cut as a tuple. Invariant: **every version with tuple `<= S` that will ever be visible has
  been delivered** (at or before this record).
- `E`, the set of versions with tuple `> S` delivered at or before this record. Each entry is the first 16
  bytes of SHA-256 over `key || 0x00 || version id || 0x00 || ETag || 0x00 || LastModified (unix ms)`.
  `|E| <= cdc.maxEarlyObjects` always.

Resume rule: list from `S` (all versions; the listing cannot filter by time, so it is the same listing as
today) and deliver every visible version with tuple `> S` whose hash is not in `E`. Then continue with the
poll rules below.

### Each poll

1. List versions. Candidates are versions with tuple `> S` and hash not in `E`. Delete markers count as
   versions, as today.
2. **Settled candidates** (`LastModified <= C`), in ascending tuple order. Each one is delivered with
   `S := its tuple`, and entries of `E` that are now `<= S` are dropped from `E`. These never enter `E`, so a
   catch-up over any number of old objects (a long downtime, a large backfill) never touches the bound.
3. After the settled candidates, `S := (C, +inf)` in memory. That is correct even if no record was emitted:
   everything at or below `C` that is visible was just delivered. It is persisted with the next record's
   position. Entries of `E` at or below the new `S` are dropped.
4. **Early candidates** (`LastModified > C`), in ascending tuple order, while `|E| < cdc.maxEarlyObjects`.
   Each one is delivered and its hash added to `E`.
5. Early candidates left over wait. They are delivered by a later poll, early if `E` has room by then,
   otherwise in step 2 once they settle. A WARN with code `s3.source.early_window_full` (rate limited) and a
   counter say how many objects are waiting, how many fit, and that latency for them rises to at most
   `lookbackPeriod`.

Each emitted record's position is `(S, E)` as of that record. So any position is exact for itself.

**Why this is exact.** Claim: after emitting record r with position `(S, E)`, the versions already delivered
are exactly the versions with tuple `<= S` (step 2 and 3 invariant, using the lookback assumption) plus `E`.
Resuming delivers the versions `> S` that are not in `E`: precisely the ones not delivered yet. A version that
became visible late, but within the lookback, has `LastModified > C` at the poll that missed it (otherwise it
would have been visible), so it is `> S` and not in `E`, and is delivered when it appears.

**Overflow.** When the window holds more than `cdc.maxEarlyObjects` versions, the source neither fails nor
shrinks the window, and it does not lose late objects. The excess waits until it settles. The cost is latency
for those objects, at most `lookbackPeriod` plus one polling period, never correctness. A bucket that
constantly writes more than `maxEarlyObjects / lookbackPeriod` objects per second runs, in effect, with
"settled" latency. That is documented with the two knobs (raise the bound, or lower `lookbackPeriod` if late
objects are not a concern).

### Snapshot

The snapshot position holds the snapshot cut `C0` and the last key delivered `k`. `C0` is computed once when the
snapshot starts, as `storeNow - lookbackPeriod` from the first listing response.

- The snapshot lists with `ListObjectsV2` in key order and delivers **only objects with
  `LastModified <= C0`**. Newer objects (written in the last `lookbackPeriod` before the snapshot, or during it)
  are left to the CDC, which delivers them as changes.
- **Resume (s3#1041)**: a snapshot position resumes the listing with `StartAfter = k` and the same `C0`. Nothing
  before `k` is delivered again. Nothing is skipped: every object with `LastModified <= C0` was visible at the
  snapshot start (lookback assumption) and is listed in key order after `k` unless it was deleted, and a deletion
  is a CDC event.
- **Handoff**: the last snapshot record's position, or the CDC start state for an empty snapshot, is the CDC
  position `S = (C0, +inf)`, `E = {}`. Exact by construction: the snapshot delivered only versions `<= C0`,
  and all of them. A version the snapshot passed over (newer than `C0`, or a newer version of an object whose
  older version it delivered) is `> S` and is delivered by the CDC.
- An object overwritten during the snapshot after its old version was delivered: the new version is `> C0`,
  so it is delivered by the CDC as an update. The snapshot never delivers a version newer than `C0`, so it
  cannot deliver the same version twice.
- The extra anchor listing pass of today's snapshot goes away.

This also removes the need for the handoff seed of #1052. Nothing the snapshot delivers is inside the CDC's
window any more.

### Position format v2

```text
byte 0      0x00            marker; no v1 position can start with it (v1 starts with an S3 key, and listings
                            cannot carry NUL: XML 1.0)
byte 1      0x02            format version
byte 2      's' or 'c'      type
then        varint-length-prefixed fields:
  snapshot: C0 (unix ms, int64), k (key bytes)
  cdc:      S.time (unix ms, int64; +inf encoded as a flag), S.key, S.versionID, n = |E| (uvarint),
            n x 16-byte hashes, sorted
```

A CDC position is about 40 bytes plus the key at `S` plus `16 * |E|`. With the default bound of 512 it is at
most about 8 KiB plus the key. Memory: in-flight records times their position size. At 8 KiB that is 80 MiB per
10,000 in-flight records in the worst case (a full set on every record), and nothing while the window is quiet.
This is stated in the parameter's description. The bound is capped at 10,000 (160 KiB positions) so a position
can never get near the 4 MiB gRPC limit.

The parser is strict: unknown version, unknown type, trailing bytes, an unsorted or oversized `E`, or `S.time`
out of range fail `Open` with the error code `s3.source.position_invalid`, the position's length, and the fix
(reset the pipeline's position). It never silently starts over.

### Upgrade from v1 positions

| Persisted position | v2 behavior on `Open` | Effect |
| --- | --- | --- |
| none | new snapshot | as today |
| v1 snapshot `<key>_s<sec>` | restart the snapshot from the beginning; WARN `s3.source.position_upgraded` | the same redelivery v1 itself does on every snapshot restart, once |
| v1 CDC `<key>_c<sec>` | CDC with `S = (sec - lookbackPeriod, +inf)`, `E = {}` | redelivers the lookback window once, which v1 does on every restart; exact from then on |

The first record after the upgrade carries a v2 position. Nothing is skipped by the upgrade: the v1 CDC position
promised nothing above `sec - lookback`, and that is where v2 starts.

**Rollback**: a v0.9.x connector cannot read a v2 position. It fails `Open` with "invalid position format"
(loud, nothing skipped). Rolling back needs the pipeline's position reset, which means a re-snapshot. That is
documented in the release notes. A compatibility shim that writes v1 positions is rejected: it would have to
throw away `E`, which defeats the purpose, and it would keep two formats alive.

### Settings

| Setting | Default | Meaning |
| --- | --- | --- |
| `lookbackPeriod` (existing) | `5m` | the late-visibility tolerance, now also the maximum extra latency on overflow |
| `cdc.maxEarlyObjects` (new) | `512` | the bound of `E`; `0` means settled-only delivery (every object waits `lookbackPeriod`; smallest positions) |

Removed: the in-memory dedupe budget and its shrink and overflow paths (`s3.source.dedupe_window_shrunk`,
`s3.source.dedupe_window_overflow`). The window is no longer tracked in memory beyond `E`, so they have nothing to
do. The listing per poll is unchanged.

## Alternatives considered

1. **Lookback re-list plus the full delivered set of the window in the position.** This is the first proposal:
   keep today's floor (`newest seen - lookback`) and persist all versions delivered inside it. Rejected as the
   main mechanism, though it is the same idea as `E`:
   - The set has no natural bound. It is every object of the last `lookbackPeriod` (30,000 at 100 objects/s with
     5m), and it rides on every record.
   - The snapshot-to-CDC handoff would have to carry every snapshot object newer than `anchor - lookback`
     (#1052's seed) in the boundary position.
   - Its overflow choices are both bad: failing stops a busy pipeline, and shrinking the window gives up late
     objects, which is a silent loss path.

   The settled cut is what bounds the set. Everything below the cut needs no tracking.
2. **Settled-only delivery** (deliver a version only after it settles; the position is just the tuple `S`).
   Exact, tiny positions, simplest code. Rejected as the default because every object waits `lookbackPeriod`,
   five minutes by default, which is a large regression for users who want low latency. Kept as
   `cdc.maxEarlyObjects: 0`.
3. **Shrink the lookback on overflow** (today's in-memory behavior, carried into the position). Rejected: it
   trades the late-visibility guarantee for throughput without telling the user per object, which CLAUDE.md
   forbids without explicit approval.
4. **State outside the position** (an object in the bucket, a local file). Rejected: not atomic with Conduit's
   position persistence, so a crash between the two produces either a skip or a full redelivery. It also needs
   write permission on the source bucket.
5. **S3 event notifications (SQS or EventBridge)** for lossless CDC. That is the real fix for late visibility,
   but it needs infrastructure and permissions most users of the listing source do not have. It is out of scope
   for v0.21 and worth a separate design as a second CDC mode.
6. **8-byte hashes** to halve position size. Rejected: with 10,000 entries the per-lookup collision
   probability is about 5e-16, which over a long-running pipeline's lifetime is not negligible, and a collision
   is a skip. With 16 bytes it is about 3e-35.

## Failure modes

| Failure | Behavior | Signal |
| --- | --- | --- |
| SIGKILL during the snapshot | resume after the last acked key, same `C0`; acked objects not redelivered | SRC-03 snapshot cycles; chaos test |
| SIGKILL at the snapshot/CDC boundary | CDC from `(C0, +inf)`, empty `E`; nothing the snapshot delivered comes again | SRC-03 boundary cycle |
| SIGKILL during CDC | resume from `(S, E)` of the last acked record; exact | SRC-03 CDC cycles; chaos test |
| Window busier than the bound | excess waits until settled; latency up to `lookbackPeriod` | WARN `s3.source.early_window_full`, waiting count |
| Object visible later than `lookbackPeriod` after its stamp | missed (as today; outside the assumption) | documented limit; bucket notifications are the fix |
| Store `Date` ahead of the host | `storeNow` capped at the host: the cut is later than the store thinks, which is conservative | none needed |
| Store `Date` far behind or garbage | the host clock is used if `Date` is unusable; if `Date` is behind by more than `lookbackPeriod`, a WARN, and the earlier of the two is used | WARN with both times |
| Host clock jumps back | `C` would move back; `S` never moves back (it is `max(S, ...)`), so no redelivery and no skip; settling pauses until the clock catches up | latency, WARN |
| Hash collision in `E` | a version is skipped (probability about `n / 2^128` per lookup, with n entries in `E`) | stated; not detectable |
| Corrupt or foreign position | `Open` fails with `s3.source.position_invalid` | error |
| Old connector given a v2 position (rollback) | `Open` fails loudly | error; release notes |
| Delete during the snapshot | not listed; the delete marker (versioned bucket) is a CDC event; unversioned buckets do not report deletes (as today) | documented |
| Bucket with millions of versions | every poll lists all versions (as today); `S` does not reduce listing cost | listing cost documented as today |

Invariant 2 (monotonic, crash-safe positions): `S` only moves forward in the tuple order, and `E` only holds
versions above `S`. A position is written only when its record is acked, by Conduit, which is atomic per
position. Invariant 3: every visible version within the lookback assumption is delivered at least once; the
resume rule never drops a version that is not provably delivered. Invariant 4 (per-key ordering) is unchanged:
no ordering claim is made.

## Testing

- Unit and property tests (`rapid` is not in the tree; table tests plus a seeded random model are used instead,
  per the gate table, which marks the property framework as not live):
  - a model store with visibility delays below and above the lookback, overwrites, deletes and same-millisecond
    versions;
  - kill at a random record, resume from that record's position, repeat;
  - check exactly once within the assumption, and missed only beyond it.
- Position codec: round trip, strict rejection of every malformed shape, and a fuzz target (CI seeds; the
  scheduled long run is not live).
- Upgrade test: v1 snapshot and CDC positions, both constructed and taken from a real v0.9.5 run, resume with the
  documented behavior. In the chaos harness: run the released v0.9.5 plugin, SIGKILL, swap in the new build,
  resume, verify no loss. Also a rollback check: the old plugin fails `Open` on a v2 position.
- Chaos (`test/chaos`, extends s3#1055): S3 source to `builtin:file` destination in a real Conduit process. SIGKILL
  during the snapshot (more objects than one page), at the boundary, and during CDC with concurrent writes and
  injected late-visible objects. After each restart verify invariants 1 to 3 and count redeliveries. Conduit's
  own at-least-once can still duplicate records end to end; source-side exactness is verified by SRC-03.
- Acceptance suite v2 with `Source.ExactResume: true`.
- MinIO no-skip suite (existing `TestMinIO_*`): kept, and adjusted where it asserts today's redelivery bounds.

### Open question for sign-off: the certification run's configuration

SRC-02 writes 10,000 objects and then expects them to arrive while the source runs. With the default bound of
512, about 9,500 of them wait to settle, which takes `lookbackPeriod` (5 minutes) and probably exceeds the case's
read timeout. Options:

1. The acceptance driver sets `lookbackPeriod: 30s`. It is a real, documented setting; the report records the
   driver config.
2. A larger default bound (10,000): every busy-window record then carries a position up to 160 KiB.
3. Accept a long SRC-02 run with a raised read timeout in the driver.

Recommendation: option 1, stated in the README and the PR, because it tests the same code paths (early set,
overflow, settling) in less time, and does not change what users get by default.

## Observability

- WARN `s3.source.early_window_full` (rate limited): waiting count, bound, lookback.
- WARN `s3.source.position_upgraded` once per pipeline after a v1 position.
- Debug per poll: `S`, `|E|`, settled and early counts, waiting count.
- No new metrics in this change. The SDK has no per-connector custom metrics hook, and the counts above are in
  the logs.

## Implementation plan

One Tier 1 PR in `conduit-connector-s3`, after sign-off, superseding #1052, which closes:

1. `source/position`: v2 codec, v1 parsing kept for upgrade only, fuzz target.
2. Snapshot iterator: cut `C0`, `StartAfter` resume, skip of objects newer than `C0`; drop the anchor pass
   (s3#1041).
3. CDC iterator: settled cut, `S`/`E` state, early bound, waiting objects; remove the dedupe budget and shrink
   code.
4. Tests as above. Then the acceptance driver claims `ExactResume`, and #1050 turns green.

Docs: the connector README (delivery semantics, the two settings, the upgrade and rollback notes) and a changelog
entry. The `conduit.io` connector page and `llms.txt` are updated with the release that bumps the built-in.

## Related

- #3009 (lane), #3002 (v0.21), #3034 (certification gaps)
- s3#1041 (snapshot restart), s3#1040 and PR s3#1052 (handoff duplicate; superseded), s3#1035 and PR s3#1050
  (acceptance suite v2), PR s3#1055 (chaos harness)
- `20261013-acceptance-suite-v2.md` (SRC-03)

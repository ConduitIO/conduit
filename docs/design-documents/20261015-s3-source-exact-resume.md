# S3 source: exact resume across restarts

## Summary

The S3 source (`ConduitIO/conduit-connector-s3`, below `s3#N`) must be Certified as a source in v0.21: it has
to pass acceptance suite v2 case SRC-03 with `ExactResume`. After a restart from the last acked position, no
already acked record may be delivered again, and nothing may be skipped. Today a restart redelivers everything:

- a restart during the snapshot starts the snapshot over (s3#1041);
- a restart during change capture (CDC) re-lists the lookback window (`lookbackPeriod`, default 5m) with an
  empty dedupe set.

This document replaces the snapshot and CDC position model so that the position says exactly what was
delivered.

The CDC position carries:

- a **settled cut `S`**: every object version at or below it, in the order (LastModified, key, version id), has
  been delivered. Under the assumptions below, no new version can still appear below it. The cut follows the
  store's clock, minus the lookback, and is **held back by every multipart upload still in progress**, because
  S3 stamps such an object with its initiation time;
- a **bounded set `E`** of the versions above `S` already delivered early, each stored as a LastModified delta
  plus a 16-byte hash, so it can be pruned by time after a restart.

A restart lists from `S` and delivers every version above it that is not in `E`. When the window holds more
objects than the bound, the excess waits until it settles: latency, never loss.

The snapshot delivers only objects settled at its start, resumes after the last acked key (s3#1041), and hands
over to CDC with an empty `E`.

Roadmap item: v0.21: S3 sink parity (#3009), certification of `builtin:s3` as source and destination. Risk
tier: **Tier 1** (position format, source data path, invariants 2 and 3). Implementation waits for DeVaris's
sign-off. Revision 2 addresses the adversarial review of revision 1: B1 to B4, S1 to S7 and N1 to N5. The
changes are listed under _Revision history_.

## Context

### How the source works today (v0.9.5, `main` at 0a47c12)

- **Snapshot**: `ListObjectsV2` in key order, one record per object, position `<key>_s<unix seconds>`. A
  restart from a snapshot position starts over.
- **Handoff**: anchor = newest LastModified at snapshot start (an extra listing pass). CDC starts at
  `anchor - lookback`. #1052 (in review) seeds the CDC dedupe set with what the snapshot delivered inside that
  window.
- **CDC**: each poll lists all versions under the prefix (`ListObjectVersions`) and selects those at or after
  `floor = min(newest seen, host clock) - lookback` that are not in an in-memory dedupe set. The floor only
  advances when newer writes are seen. Position `<key>_c<unix seconds>`. On restart the dedupe set is empty.

### Assumptions (stated, not new-free)

Revision 1 claimed it added no assumption. That was wrong: a cut that advances with the clock depends on things
that today's floor (which advances only with newer writes) does not. This design rests on these:

- **A1 (non-multipart visibility).** An object version that is not written by a multipart upload is visible in
  `ListObjectVersions` within `lookbackPeriod` of its LastModified. This covers:
  - single `PutObject`: S3's stamp is close to the request; a PUT that takes longer than the lookback (a very
    large object over a slow link) breaks it;
  - server-side `CopyObject` of a large object;
  - **replication**: on a replication destination, replicated objects keep the source's LastModified, so
    replication lag must stay below `lookbackPeriod`;
  - **clock skew between store nodes** (distributed MinIO stamps with the receiving node's clock), which must
    stay below `lookbackPeriod`.
- **A2 (multipart).** An object written by a multipart upload, which AWS stamps with the upload's **initiation**
  time, is listed by `ListMultipartUploads` from initiation until completion. MinIO (pgsty
  RELEASE.2026-08-04, checked 2026-10-15) stamps a multipart object with its **completion** time instead, so
  for MinIO A1 applies. MinIO also returns nothing for a prefix that is a directory (`dir/`), only for a key or
  no prefix. The source therefore lists uploads bucket-wide and filters by prefix itself.
- **A3 (clock).** The `Date` header of a listing response is the store's clock. It has one-second resolution
  and is truncated, so it is never later than the true time.

Objects outside these assumptions can be missed, as today. The connector documentation lists them; bucket event
notifications are the lossless alternative (see _Alternatives_).

### What SRC-03 requires

Seven kills: three during the snapshot, one exactly at the snapshot/CDC boundary, two during CDC with writes
after `Open`, then a final drain. With `ExactResume`, no acked record may come again, and nothing written may be
missing at the end. The in-process kill does not run `Teardown`.

### Constraints

- A position is an opaque byte string per record, and Conduit persists the last acked one. Resuming from **any**
  record's position must be exact, so a position must be self-contained.
- Every in-flight record carries its position: position size times records in flight is memory, and it is bytes
  in every gRPC message.
- Connectors have no durable state but the position.
- No new dependencies. No change to the connector protocol or to Conduit.

## Decision

### Order and the settled cut

Versions are ordered by the tuple `(LastModified, key, version id)`. The version id is `"null"` in an
unversioned bucket. LastModified is taken from the listing at the store's resolution.

At each poll, in this order:

1. `ListMultipartUploads` over the whole bucket (all pages), keeping uploads whose key is under the prefix.
   Uploads initiated more than `cdc.maxPendingUploadAge` ago (default 24h) are ignored with a rate-limited WARN
   `s3.source.stale_upload_ignored` (key, upload id, initiated); such an object, if it ever completes, can be
   missed. The fix is an `AbortIncompleteMultipartUpload` lifecycle rule shorter than that age. If the call is
   denied (`s3:ListBucketMultipartUploads` missing), the source WARNs `s3.source.multipart_listing_denied` once
   per `Open`, continues without the hold, and documents that multipart uploads longer than the lookback can then
   be missed.
2. `ListObjectVersions`, all pages. `T` = the `Date` header of the **first** page (B4), capped at the host
   clock. A missing or unparseable header falls back to the host clock with a WARN. A version that becomes
   visible after the first page was requested is stamped no earlier than `T - lookback` (A1) or is an upload
   from step 1 (A2), so the cut below stays correct for a listing that takes many pages.
3. The cut: `C = min(T - lookbackPeriod, (oldest relevant upload's Initiated) - 1s)`.

The cut never moves backwards in the persisted state: `S` only advances (below).

Why not today's floor (newest seen minus lookback)? It does not advance while the bucket is idle, so an object
waiting to settle would wait for the next write. The clock-based cut advances on its own; the multipart hold
(B1) covers the case where the clock alone would overtake an initiation-stamped upload.

### CDC position state

- `S`: a tuple. Invariant: **every version with tuple `<= S` that the assumptions say will ever be visible has
  been delivered** (at or before this record).
- `E`: the versions with tuple `> S` that were delivered at or before this record. Each entry is
  `(LastModified - S.time in ms, as a uvarint; 16-byte hash)`. The hash is the first 16 bytes of SHA-256 over
  the uvarint-length-prefixed fields `key`, `version id`, `ETag` and `LastModified (unix ms)` (N1: no field
  separator can be ambiguous). `|E| <= cdc.maxEarlyObjects`.

**Pruning (B3).** Entries with `LastModified < S.time` are dropped whenever `S` advances, and all entries with
`LastModified <= t` when `S` becomes `(t, +inf)`. Because every entry carries its time, a position written before
a restart is pruned like any other. Revision 1 stored bare hashes, which could not be pruned after a restart and
filled `E` permanently.

**Resume rule.** List from `S` and deliver every visible version with tuple `> S` whose hash is not in `E`.

### Each poll

The candidates are the visible versions with tuple `> S` and hash not in `E`, including delete markers (as
today). Only the latest version of each key is a candidate (see _Collapsing_).

1. **Settled candidates** (`LastModified <= C`), smallest tuples first. The poll keeps at most
   `M = cdc.maxSettledPerPoll` of them (default 10,000) in a bounded heap that holds the M smallest tuples (B2).
   Each is delivered with `S := its tuple`, and pruning follows. Settled versions never enter `E`, so a catch-up
   over any number of old objects never touches the early bound.
2. **Only if no settled candidate was left out of the heap**, `S := (C, +inf)` and `E` is pruned. If some were
   left out, `S` stays at the last delivered tuple, and the next poll continues from there. Revision 1 jumped to
   `C` regardless, which skipped the deferred ones (B2).
3. **Early candidates** (`LastModified > C`), smallest tuples first, while `|E| < cdc.maxEarlyObjects`. Each is
   delivered and added to `E`.
4. Early candidates that did not fit wait for a later poll: early if `E` has room by then, otherwise in step 1
   once they settle. A rate-limited WARN `s3.source.early_window_full` gives the waiting count, the bound and the
   resulting latency.

Each emitted record's position is `(S, E)` as of that record.

**Why this is exact.** After record r with position `(S, E)`, the delivered versions are exactly the versions
`<= S` plus `E`:

- Step 1 delivers in tuple order and moves `S` with each record.
- Step 2 moves `S` to `C` only when every candidate `<= C` was delivered, and every version that will be visible
  with `LastModified <= C` was already visible when the first page was requested (A1, A2, and the multipart hold).
- `E` holds exactly the early deliveries above `S`.

The resume rule delivers the visible versions `> S` that are not in `E`, which are precisely the ones not yet
delivered.

**Fetching the listed version (S3).** `GetObject` is called with the listed `VersionId` (versioned buckets) or
with `If-Match: <listed ETag>` (unversioned). A 412 or 404 means the version was overwritten or deleted after the
listing. It is skipped without being recorded in `E`, and the newer version or delete marker is a later
candidate (its tuple is larger). Today `GetObject` fetches whatever is current, which can deliver a body under
another version's identity.

### Collapsing (S7)

Only the latest version of each key at poll time is delivered. Versions written and overwritten between two
polls, or while a key waits to settle, are collapsed into the latest one. A versioned bucket's history is not
replayed. This is today's behavior, now stated.

### Overflow and latency (S1)

`E` limits early delivery to about `cdc.maxEarlyObjects / lookbackPeriod` objects per second sustained. Beyond
that, the excess is delivered when it settles, up to `lookbackPeriod` plus one poll later, and longer while a
multipart upload holds the cut (bounded by `cdc.maxPendingUploadAge`). Nothing is lost, and the window is not
shrunk.

| `cdc.maxEarlyObjects` | sustained early rate at 5m | worst-case position size |
| --- | --- | --- |
| 512 (revision 1) | 1.7 objects/s | about 10 KiB |
| **4096 (default)** | 13.7 objects/s | about 78 KiB |
| 10000 (maximum) | 33 objects/s | about 190 KiB |

The default is raised to **4096**. Memory is the position size times the records in flight, but only while the
window is full. At 4096 entries and 1,000 records in flight that is about 78 MiB; with a quiet window, a few
bytes. A bucket written faster than the rate above is better served by a smaller lookback (with its
loss trade-off, see S5) or by bucket notifications. The README says this plainly.

### Snapshot

The snapshot position holds the snapshot cut `C0` and the last delivered key `k`. `C0` is computed once, before
the first page, with the same rule as `C`: uploads first, then
`C0 = min(Date(first page) - lookback, oldest relevant upload - 1s)`.

- Keys are listed in order (`ListObjectsV2`). Only objects with `LastModified <= C0` are delivered; newer ones
  are left to the CDC. `GetObject` uses `If-Match` with the listed ETag; on 412 or 404 the object is skipped,
  and its newer version or delete marker is a CDC event (> `C0`).
- **Resume (s3#1041)**: `StartAfter = k`, same `C0`. Nothing before `k` comes again. Nothing is skipped: every
  object `<= C0` was visible at the snapshot start and is listed after `k` unless deleted.
- **Handoff without look-ahead (N5)**: every snapshot record carries a snapshot position. When the listing after
  `k` is exhausted, the source switches to CDC with `S = (C0, +inf)`, `E = {}`. The same happens on resume from a
  snapshot position whose listing is already exhausted, which is the SRC-03 boundary case. Revision 1, like
  today's code, gave the last snapshot record a CDC position. That needs a look-ahead after every record and is
  where today's handoff error paths come from.
- Exact by construction: the snapshot delivers exactly the visible versions `<= C0` (latest per key), and the
  CDC starts strictly above. #1052's seed is no longer needed.

### Position format v2 (S4)

```text
byte 0          0x00            marker; v1 positions start with an S3 key, which cannot contain NUL
byte 1          0x02            format version
byte 2          's' or 'c'
payload         uvarint-length-prefixed fields
  snapshot:     C0 (unix ms, zigzag varint), k
  cdc:          S.time (unix ms), S.inf flag, S.key, S.versionID, n (uvarint),
                n x (uvarint delta ms, 16-byte hash), sorted by (delta, hash)
4 bytes         CRC-32C over bytes 0 .. end of payload
1 byte          0x00            fixed terminator
```

The fixed `0x00` at the end matters for rollback. v0.9.x parses a position by taking the last `_` and reading
the byte after it. A binary position that happens to end in `_` (about 1 in 256 without the terminator) would
make the old parser index past the end and panic. With the terminator it fails with "invalid position format".
The CRC rejects truncated or bit-flipped positions.

The parser is strict: bad marker, version, type, CRC, terminator, trailing bytes, an unsorted or oversized `E`,
or an out-of-range time fails `Open` with `s3.source.position_invalid`, the length and the fix (reset the
position). It never silently starts over. It is fuzzed.

### Upgrade from v1 positions

| Persisted position | v2 behavior on `Open` | Effect |
| --- | --- | --- |
| none | new snapshot | as today |
| v1 snapshot `<key>_s<sec>` | restart the snapshot; WARN `s3.source.position_upgraded` | the redelivery v1 does on every snapshot restart, once |
| v1 CDC `<key>_c<sec>` | CDC with `S = (sec - lookbackPeriod, +inf)`, `E = {}` | redelivers the lookback window once, as v1 does on every restart; exact afterwards |

Nothing is skipped by the upgrade: v1 promised nothing above `sec - lookback`.

**Rollback**: v0.9.x fails `Open` on a v2 position with "invalid position format": loud, no panic (see the
terminator), nothing skipped. Rolling back needs a position reset, which is a re-snapshot. This goes in the
release notes. There is no v1-writing compatibility mode.

### Unsupported stores (N3)

`Open` rejects stores whose listing breaks the model, with `s3.source.store_unsupported` and the reason:

- S3 Express One Zone directory buckets (name suffix `--x-s3`): no versions listing, and listings are not in
  key order;
- stores that answer `ListObjectVersions` with `NotImplemented` (some Cloudflare R2 and GCS interop setups),
  detected by one probe call.

Stores that implement the APIs with different semantics cannot be detected; the README lists AWS S3 and MinIO as
the tested stores.

### `lookbackPeriod` (S5)

- `0` stays accepted but is documented as lossy: no tolerance for late visibility beyond the multipart hold. An
  `Open` WARN says so. The acceptance driver never uses it, and `ExactResume` is claimed only for values `>= 30s`.
- Values between `0` and `30s` are rejected at configuration time. They give almost no tolerance while looking
  safe.
- The README's old advice ("lower lookbackPeriod to avoid this" in the dedupe-shrink warning) goes away with the
  shrink path. The new advice states the trade-off: a smaller lookback lowers latency on overflow and raises the
  chance of missing late objects.

### Listing cost and scale (S6)

Each poll lists **every version under the prefix** (S3 has no time filter), plus the multipart uploads. That is
about 1 request per 1,000 versions per poll: 10 million versions means about 10,000 requests per poll, which with
the default 1s polling period is neither cheap nor fast. This is today's cost, unchanged, and is now stated in the
README with guidance: narrow prefixes, expire noncurrent versions, raise `pollingPeriod`, or use bucket
notifications. A poll that takes longer than `lookbackPeriod` logs WARN `s3.source.poll_slow` (duration, pages).
Correctness holds because the cut uses the first page's `Date` (B4), but latency suffers.

### Settings

| Setting | Default | Meaning |
| --- | --- | --- |
| `lookbackPeriod` (existing) | `5m` | late-visibility tolerance; `0` (lossy) or `>= 30s` |
| `cdc.maxEarlyObjects` (new) | `4096` | bound of `E`; `0` = settled-only delivery |
| `cdc.maxPendingUploadAge` (new) | `24h` | multipart uploads older than this do not hold the cut |
| `cdc.maxSettledPerPoll` (new) | `10000` | settled deliveries per poll (memory bound of the heap) |

Removed: the in-memory dedupe budget and its shrink and overflow paths (`s3.source.dedupe_window_shrunk`,
`s3.source.dedupe_window_overflow`).

## Alternatives considered

1. **Re-list the lookback window and store the full delivered set of the window in the position** (the first
   proposal). Rejected:
   - The set is unbounded: every object of the last `lookbackPeriod`, on every record.
   - The snapshot handoff would carry every recent snapshot object.
   - Overflow can only fail (a stopped pipeline) or shrink the window (silent loss).

   The settled cut is what bounds the set.
2. **Settled-only delivery** (position = `S` only). Exact and small, but every object waits `lookbackPeriod`.
   Kept as `cdc.maxEarlyObjects: 0`.
3. **Shrink the lookback on overflow.** Rejected: it trades the late-visibility guarantee for throughput, which
   CLAUDE.md forbids without explicit approval.
4. **State outside the position** (an object in the bucket, a local file). Rejected: not atomic with Conduit's
   position persistence, and it needs write access to the source bucket.
5. **S3 event notifications (SQS or EventBridge).** The lossless answer to late visibility, slow uploads and
   replication lag, and the right mode for very large buckets. Rejected for v0.21 because it needs infrastructure
   and permissions; a separate design as a second CDC mode.
6. **Today's floor (newest seen minus lookback) as the cut.** It is safe against initiation-stamped multipart
   uploads by accident: it does not move on an idle bucket. But for the same reason waiting objects never settle
   while the bucket is idle. Rejected in favor of the clock cut with the explicit multipart hold.
7. **8-byte hashes.** Rejected: the per-lookup collision probability (about `n / 2^64`) over a long-running
   pipeline is not negligible, and a collision is a skip.

## Failure modes

| Failure | Behavior | Signal |
| --- | --- | --- |
| SIGKILL during the snapshot | resume after the last acked key, same `C0` | SRC-03 snapshot cycles; chaos test |
| SIGKILL at the boundary | resume from the last snapshot position, listing exhausted, CDC from `(C0, +inf)` | SRC-03 boundary cycle |
| SIGKILL during CDC | resume from `(S, E)` | SRC-03 CDC cycles; chaos test |
| Multipart upload longer than the lookback (AWS stamps initiation) | cut held below its initiation; delivered when it completes | debug log of the hold; latency |
| Abandoned upload older than `maxPendingUploadAge` | ignored; if it completes later it can be missed | WARN `s3.source.stale_upload_ignored` |
| `ListMultipartUploads` denied | no hold; long multipart uploads can be missed | WARN `s3.source.multipart_listing_denied` |
| Replication lag, slow single PUT, or store node skew above the lookback | can be missed (A1) | documented requirement |
| More settled candidates than the per-poll heap | `S` advances only through what was delivered; the rest come next poll | debug log |
| Window busier than `E` | the excess waits until it settles | WARN `s3.source.early_window_full` |
| Version overwritten or deleted between listing and `GetObject` | 412/404, skipped, the newer version or marker comes later | debug log |
| Metadata-only overwrite in the same second, unversioned bucket (N2) | same ETag, LastModified and version `"null"`, so it is identical to the delivered one and not redelivered | documented; versioned buckets distinguish it |
| Poll slower than the lookback | correct (first-page `Date`); latency | WARN `s3.source.poll_slow` |
| Store `Date` ahead of the host | capped at the host: conservative | none |
| `Date` missing or garbage | host clock, WARN | WARN |
| Host clock jumps back | the cut moves back in memory, `S` does not; settling pauses | latency |
| Hash collision in `E` | a version is skipped (about `n / 2^128` per lookup) | stated |
| Corrupt or foreign position | `Open` fails `s3.source.position_invalid` | error |
| Rollback with a v2 position | old connector fails `Open` (no panic) | error; release notes |
| Unsupported store | `Open` fails `s3.source.store_unsupported` | error |

Invariant 2: `S` only moves forward and `E` only holds versions above it. Conduit persists a position only when
its record is acked. Invariant 3: under A1 to A3, every version is delivered at least once; the resume rule never
drops a version that is not provably delivered. Invariant 4: unchanged, no ordering claim.

## Testing

- **Model store** (in-process, behaves like AWS): initiation-stamped multipart uploads listed by
  `ListMultipartUploads` while in progress, a `Date` header, visibility delays below and above the lookback,
  overwrites, delete markers, same-millisecond versions, 412 on a stale `If-Match`, and paging. A MinIO mode stamps
  at completion and does not match directory prefixes in `ListMultipartUploads`.
- **Seeded random property tests** (`rapid` is not in the tree, so table tests plus a seeded generator): kill at
  a random record, resume from its position, repeat; exactly once within the assumptions, and missed only where an
  assumption is broken on purpose. Run with `cdc.maxSettledPerPoll = 1` and `cdc.maxEarlyObjects = 1` (B2), with
  a long multipart upload on an idle bucket (B1), with restarts across pruning (B3), and with a version written
  while a multi-page listing runs (B4).
- **Position codec**: round trip; strict rejection; a fuzz target; the v0.9.5 parser fed v2 positions must not
  panic.
- **Upgrade and rollback**: v1 snapshot and CDC positions, constructed and taken from a real v0.9.5 run. In the
  chaos harness, run released v0.9.5, SIGKILL, swap in the new build, resume, check no loss; then the reverse,
  where the old build must fail `Open` cleanly.
- **Chaos** (`test/chaos`, extends s3#1055): S3 source → `builtin:file` in a real Conduit. SIGKILL during the
  snapshot (more than one page), at the boundary, and during CDC with concurrent writes and a multipart upload
  spanning a kill. Invariants 1 to 3 checked after each restart, redeliveries counted. Conduit's end-to-end
  at-least-once can still duplicate; source exactness is SRC-03's job.
- **MinIO**: the existing `TestMinIO_*` suite, adjusted where it asserts today's redelivery bounds; a multipart
  test that records the stamp semantics, so a MinIO change is noticed.
- **Acceptance suite v2, certified on defaults (S2).** The driver uses default source settings with
  `ReadTimeout` raised to 40s, so that SRC-02's 10,000 objects fit: about 4,096 are delivered early and the rest
  settle within 5 minutes plus a poll. The suite's limit is `10 x ReadTimeout + 2ms x n` = 420s. The CI job
  timeout rises to 60m. A separate fast lane runs every PR with `lookbackPeriod: 30s`; it is not the certified
  configuration and says so in its job name. If the defaults cannot pass SRC-02, the default bound is raised
  rather than the certified config changed.

## Observability

WARN codes: `s3.source.early_window_full`, `s3.source.stale_upload_ignored`,
`s3.source.multipart_listing_denied`, `s3.source.poll_slow`, `s3.source.position_upgraded`. Error codes:
`s3.source.position_invalid`, `s3.source.store_unsupported`. Debug per poll: `S`, `|E|`, `C`, the hold (oldest
upload), settled, early and waiting counts, pages, duration. No new metrics: the SDK has no per-connector custom
metrics hook.

## Implementation plan

One Tier 1 PR in `conduit-connector-s3` after sign-off. It supersedes #1052, which closes.

1. `source/position`: v2 codec, CRC, terminator; v1 parsing kept for upgrade only; fuzz target.
2. Snapshot: `C0` with the multipart hold, `If-Match` fetch, `StartAfter` resume, no look-ahead, no anchor pass
   (s3#1041).
3. CDC: uploads listing, first-page `Date`, cut, bounded settled heap, `S`/`E` with time-delta entries,
   versioned/`If-Match` fetch, removal of the dedupe budget and shrink code.
4. `Open` checks for unsupported stores; `lookbackPeriod` validation.
5. Tests as above; the driver claims `ExactResume` on defaults; #1050 turns green.

Docs: the connector README (assumptions A1 to A3, permissions including `s3:ListBucketMultipartUploads`, the
settings, listing cost, upgrade and rollback) and a changelog entry. The `conduit.io` page and `llms.txt` are
updated with the release that bumps the built-in.

## Revision history

Revision 2 (2026-10-15), after the adversarial review of revision 1:

- **B1**: multipart uploads are stamped with their initiation time. Added the `ListMultipartUploads` hold,
  `cdc.maxPendingUploadAge`, the denied-permission fallback, the verified MinIO semantics, and assumptions A1 to
  A3 (including replication lag). The "no new assumption" claim is withdrawn.
- **B2**: bounded settled heap; `S` jumps to `C` only when nothing settled was deferred.
- **B3**: `E` entries carry a time delta and are pruned by time after a restart.
- **B4**: `C` comes from the first page's `Date`.
- **S1**: default bound raised from 512 to 4096, with the rate and size table.
- **S2**: certification on defaults with a raised read timeout, plus a fast lane that is not the certified
  configuration.
- **S3**: `GetObject` fetches the listed version.
- **S4**: CRC-32C and a fixed terminator.
- **S5**: `lookbackPeriod` validation, and `0` documented as lossy.
- **S6**: listing cost stated, plus a slow-poll WARN.
- **S7**: collapsing stated.
- **N1** length-prefixed hash input; **N2** same-second metadata overwrite; **N3** unsupported stores rejected;
  **N4** store node skew in A1; **N5** handoff without look-ahead.

## Related

- #3009 (lane), #3002 (v0.21), #3034 (certification gaps)
- s3#1041 (snapshot restart), s3#1040 and PR s3#1052 (handoff duplicate; superseded), s3#1035 and PR s3#1050
  (acceptance suite v2), PR s3#1055 (chaos harness)
- `20261013-acceptance-suite-v2.md` (SRC-03)

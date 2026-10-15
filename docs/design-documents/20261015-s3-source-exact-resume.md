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
  been delivered, except the tracked uploads below. Under the assumptions below, no new version can still appear
  below it. The cut follows the store's clock, minus the lookback;
- a **bounded set `E`** of the versions above `S` already delivered early, each stored as a LastModified delta
  plus a 16-byte hash, so it can be pruned by time after a restart;
- a **bounded set `R`** of multipart uploads still in progress whose initiation time is below the cut (AWS stamps
  the completed object with that time). When one completes, its version below `S` is delivered because of `R`.
  When `R` is full, the oldest untracked upload holds the cut instead (proposal; DeVaris to decide, see
  _Tracked uploads_).

A restart lists from `S` and delivers every version above it that is not in `E`, plus the versions below it
that match `R`. When the window holds more
objects than the bound, the excess waits until it settles: latency, never loss.

The snapshot delivers only objects settled at its start, resumes after the last acked key (s3#1041), and hands
over to CDC with an empty `E`.

Roadmap item: v0.21: S3 sink parity (#3009), certification of `builtin:s3` as source and destination. Risk
tier: **Tier 1** (position format, source data path, invariants 2 and 3). Implementation waits for DeVaris's
sign-off. Revision 4 addresses the review of revision 3 and puts R3 (tracked uploads) up for decision. Revision 3
addressed the re-review of revision 2 (R1 to R8), and revision 2 the first review (B1 to B4, S1 to S7, N1 to N5).
The changes are listed under _Revision history_.

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
  time, is listed by `ListMultipartUploads` (with the source's prefix) from initiation until completion.
  MinIO (pgsty RELEASE.2026-08-04, checked 2026-10-15) differs in two ways:
  - it stamps a multipart object with its **completion** time, so for MinIO A1 applies and the hold is not
    needed;
  - it returns no uploads for a prefix that is a directory (`dir/`), only for an exact key or no prefix.

  The source still always sends its prefix (R4: least privilege, and cost on buckets with many unrelated
  uploads). On MinIO with a directory prefix the hold therefore sees nothing, which is harmless given the
  completion stamp. The README states both MinIO differences.
- **A3 (clock).** The `Date` header of a listing response is the clock of the store node that produced the
  response, at one-second resolution and rounded down, so `Date` is never later than that node's time when it
  answered. Skew between store nodes is part of A1.

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

1. **Uploads.** `ListMultipartUploads` with `Prefix` = the source prefix, all pages. `Tu` is the `Date` header
   of its **first** page. The uploads are classified in step 3, once `T` is known.

   **Permission denied (R4).** `Open` probes `ListMultipartUploads` (with the prefix, `MaxUploads=1`). An
   AccessDenied fails `Open` with `s3.source.multipart_listing_denied`, and the error names both fixes: grant
   `s3:ListBucketMultipartUploads`, or set `cdc.multipartHold: false`. An AccessDenied **while running** stops
   the iterator with the same error code. There is no silent fallback in the middle of a pipeline. The opt-out
   runs without uploads tracking. It is documented as lossy for multipart uploads longer than the lookback, logs
   a WARN at `Open`, and is never the configuration `ExactResume` is claimed for.
2. **Versions.** `ListObjectVersions`, all pages. `Tv` is the `Date` header of its **first** page (B4).
3. **The cut (R1) and the uploads (R3).**
   - `T = min(Tu, Tv)`, capped at the host clock. A missing or unparseable header falls back to the host clock
     with a WARN.
   - Uploads with `Initiated >= T - lookbackPeriod` do not matter yet: they are above the cut.
   - **Old uploads** (`Initiated < T - lookbackPeriod`) would complete below the cut on AWS, which stamps the
     initiation time. Each one is handled from the oldest:
     - more than `cdc.maxPendingUploadAge` (default 24h, measured against `Tu`, the store clock) before `Tu`: it
       is **released**, with a rate-limited WARN `s3.source.stale_upload_ignored`. The README requires an
       `AbortIncompleteMultipartUpload` lifecycle rule with `DaysAfterInitiation` at most this age, so a
       released upload cannot complete;
     - otherwise it is **tracked** in the set `R` of the position (see below), if `R` has room;
     - otherwise (`R` is full) the oldest untracked upload **holds the cut**:
       `C = min(T - lookbackPeriod, Initiated - 1s)`, with a rate-limited WARN `s3.source.cut_held`.
   - Without a hold, `C = T - lookbackPeriod`.

**Why `Tu` and not only `Tv` (R1).** Revision 2 took `T` from the versions listing alone. An upload initiated
**during** a slow step-1 listing, under a key step 1 had already paged past, is in neither listing. If step 1
took longer than the lookback, `Tv - lookback` is already later than that upload's initiation. The cut then
passes it, and when it completes (AWS stamps the initiation time) it lands below `S` and is skipped. With `Tu`,
every upload not seen by step 1 was initiated at or after `Tu` (rounded down). That is above `Tu - lookback` and
so above `C`, and the next poll's step 1 sees it if it is still in progress. A non-multipart version missing
from the versions listing became visible after `Tv` was produced, so by A1 its LastModified is later than
`Tv - lookback`, which is at least `C`. Revision 2's claim that the gap between the two calls is "trivially below
the lookback" was wrong for slow listings and is withdrawn.

**Tracked uploads (R3, proposal; DeVaris to decide).** A multipart upload that runs longer than the lookback
must neither be lost nor stop the pipeline. `R` is a bounded set, carried in the CDC position, of entries
`(16-byte hash of the key, Initiated in unix ms)` for old uploads still in progress. With `R` the cut advances
normally past a tracked upload. When the upload completes, its version lands below `S` (AWS stamps it with
`Initiated`), and the source delivers it because of `R`:

- **Matching.** A version **at or below `S`** is delivered if its key hash and LastModified (rounded down to the
  second, as AWS reports it) match an entry of `R`, and its ETag has the multipart form (`"<hex>-<parts>"`). The
  ETag check keeps a plain `PutObject` of the same key in the same second from consuming the entry.
- **Delivery removes the entry.** The record's position no longer contains it, so a resume from that record does
  not deliver the version again, and a resume from an earlier record still has the entry and does (exact).
- **An entry whose upload is gone** from this poll's uploads listing (completed or aborted before `Tu`) is
  resolved in the same poll. If its completed version is in the versions listing (which runs after `Tu`), it is
  delivered and the entry removed. Otherwise the entry is dropped: the upload was aborted, or its object was
  overwritten or deleted, and the latest version is a normal candidate.
- **Bound.** `|R| <= cdc.maxTrackedUploads` (default 256, about 6 KiB in a position). When `R` is full, the
  oldest untracked upload holds the cut, as in revision 3. That only delays: latency until an entry frees up, and
  never loss. MinIO stamps completion time, so its completed uploads are above `S` and simply never match.
- **Cost.** No extra requests: `R` is resolved from the two listings every poll already makes.

The S invariant becomes: every visible version `<= S` has been delivered, **except** the versions matching an
entry of `R`, which have not.

**Rejected alternative (a): idle release via `ListParts` (revision 3).** An upload with no new part for
`cdc.maxUploadIdle` (1h) stopped holding the cut. That **can lose a live upload**: one slow part, a 5 GB part at
1 MB/s for instance, uploads for about 85 minutes without producing a new part. The upload is released while it
is still running, completes below `S`, and is skipped. It also costs one `ListParts` request per old upload per
poll and needs a second permission. If (a) were chosen anyway, `Open` would probe `ListParts` with a fake upload
id: `NoSuchUpload` means permitted, AccessDenied means denied, and denied fails `Open` as in R4.

The cut never moves backwards in the persisted state: `S` only advances (below).

Why not today's floor (newest seen minus lookback)? It does not advance while the bucket is idle, so an object
waiting to settle would wait for the next write. The clock-based cut advances on its own; the multipart hold
(B1) covers the case where the clock alone would overtake an initiation-stamped upload.

### CDC position state

- `S`: a tuple. Invariant: **every version with tuple `<= S` that the assumptions say will ever be visible has
  been delivered** (at or before this record), **except versions matching an entry of `R`**.
- `E`: the versions with tuple `> S` that were delivered at or before this record. Each entry is
  `(LastModified - S.time in ms, as a uvarint; 16-byte hash)`. The hash is the first 16 bytes of SHA-256 over
  the uvarint-length-prefixed fields `key`, `version id`, `ETag` and `LastModified (unix ms)` (N1: no field
  separator can be ambiguous). `|E| <= cdc.maxEarlyObjects`.
- `R`: tracked uploads, `(key hash, Initiated)` per entry, `|R| <= cdc.maxTrackedUploads` (see _Tracked
  uploads_).

**Pruning (B3).** Entries with `LastModified < S.time` are dropped whenever `S` advances, and all entries with
`LastModified <= t` when `S` becomes `(t, +inf)`. Because every entry carries its time, a position written before
a restart is pruned like any other. Revision 1 stored bare hashes, which could not be pruned after a restart and
filled `E` permanently.

**Resume rule.** List from `S` and deliver every visible version with tuple `> S` whose hash is not in `E`,
plus every version `<= S` that matches an entry of `R`.

### Each poll

The candidates are the visible versions with tuple `> S` and hash not in `E`, including delete markers (as
today), plus the **tracked candidates**: versions `<= S` that match an entry of `R`. Only the latest version of
each key is a candidate (see _Collapsing_). Tracked candidates are delivered first, in tuple order; each delivery
removes its entry from `R`, and `S` does not move for them.

1. **Settled candidates** (`LastModified <= C`), smallest tuples first. The poll keeps the smallest tuples in a
   heap bounded by **bytes**, `cdc.maxSettledBytesPerPoll` (default 64 MiB, R7), not by count, because keys
   can be up to 1 KiB. At about 200 bytes per candidate (a typical key, version id and ETag) that is about
   330,000 settled deliveries per poll. That is the catch-up rate ceiling: about 330,000 objects per
   `pollingPeriod` plus listing time. The budget only limits memory; a catch-up never loses anything (B2).
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

Each emitted record's position is `(S, E, R)` as of that record.

**Why this is exact.** After record r with position `(S, E)`, the delivered versions are exactly the versions
`<= S` plus `E`:

- Step 1 delivers in tuple order and moves `S` with each record.
- Step 2 moves `S` to `C` only when every candidate `<= C` was delivered, and every version that will be visible
  with `LastModified <= C` was already visible when the first page was requested (A1, A2, and the multipart hold).
- `E` holds exactly the early deliveries above `S`.
- `R` holds exactly the tracked uploads below `S` not delivered yet. An old upload that is not tracked holds the
  cut, so it stays above `S`.

The resume rule delivers the visible versions `> S` that are not in `E`, which are precisely the ones not yet
delivered.

**Fetching the listed version (S3, R8).** In a versioned bucket `GetObject` is called with the listed
`VersionId`, which is exact. In an unversioned bucket it is called with `If-Match: <listed ETag>`, and the
response's `Last-Modified` is compared with the listed LastModified, both rounded down to the second. The
`Last-Modified` check catches an overwrite with the same content, which keeps the ETag but not the time. A 412,
a 404 or a `Last-Modified` mismatch means the version was overwritten or deleted after the listing. It is
skipped without being recorded in `E` or `S`. The newer version or delete marker is a later candidate:
usually because its LastModified is later and its tuple therefore above `S`. For a multipart upload initiated
before the listed version and completed after it, the stamp is earlier, and the version is still delivered,
because the upload was tracked in `R` or held the cut until it completed. A same-content overwrite within the
same second remains indistinguishable (N2). Today `GetObject` fetches whatever is current, which can deliver a
body under another version's identity (s3#1056).

### Collapsing (S7)

Only the latest version of each key at poll time is delivered. Versions written and overwritten between two
polls, or while a key waits to settle, are collapsed into the latest one. A versioned bucket's history is not
replayed. This is today's behavior, now stated.

### Overflow, latency and memory (S1, R6)

`E` limits early delivery to about `cdc.maxEarlyObjects / lookbackPeriod` objects per second sustained. Beyond
that, the excess is delivered once it settles: at most `lookbackPeriod` plus one poll later, and longer while a
multipart upload holds the cut (see _Hold older than the lookback_). Nothing is lost, and the window is not
shrunk.

| `cdc.maxEarlyObjects` | sustained early rate at 5m | position size with `E` full |
| --- | --- | --- |
| 512 | 1.7 objects/s | about 11 KiB |
| **2048 (default)** | 6.8 objects/s | about 41 KiB |
| 4096 (revision 2) | 13.7 objects/s | about 82 KiB |
| 10000 (maximum) | 33 objects/s | about 200 KiB |

The entry size is about 20 bytes (a uvarint delta plus a 16-byte hash), plus about 80 bytes of header and the
key at `S` (up to 1 KiB).

**Memory bound.** Let `N` be the number of records the source has emitted that Conduit has not acked yet. With
`K = cdc.maxEarlyObjects`, the positions take at most `N x (80 + |S.key| + 20 x K)` bytes. `N` is not set by
one connector setting. It is the SDK's read batch (`sdk.batch.size`, default 1 when unset) times the number of
batches the engine holds between source and destination, plus the destination's batch (`sdk.batch.size` of the
destination). The README states the formula with that breakdown. With `K = 2048` and the worst case of a full
`E` on every in-flight record, 10,000 records in flight is about 410 MiB, and 1,000 is about 41 MiB.

The default comes down from revision 2's 4096 to **2048**, which halves the worst case. The rate a busy bucket
gets early delivery for also halves (6.8 objects/s at 5m). Above that rate latency rises, and correctness is
unaffected.

**Encoding (R6).** `E` is kept in insertion order, and the encoded entries are kept as a byte slice that grows
by one entry per early delivery and is compacted when `S` advances. A position is that slice plus a fresh
header and trailer, a copy of `O(|E|)` bytes per record with no re-sorting or re-hashing. The parser accepts any
order and rejects duplicates.

A memory test emits 10,000 records with `E` full and none acked, and checks the heap against the formula
(within 10%). It runs in CI as a test, not as a benchmark, and makes no performance claim. It measures the
connector side only. Every record crosses gRPC to Conduit, which keeps its own copy of the position, and the SDK
and engine clone records in places. The end-to-end footprint is therefore a small multiple of the formula. It is
measured in the chaos harness (process RSS with `E` full), not assumed.

### Snapshot

The snapshot position holds the snapshot cut `C0`, the last delivered key `k`, and the tracked uploads `R0`.
They are computed once at snapshot start with the same rule as the CDC cut:

- `ListMultipartUploads` first, with `Tu` the `Date` of its first page;
- `Tl` is the `Date` of the first `ListObjectsV2` page;
- `T0 = min(Tu, Tl)`, capped at the host clock. Using `Tl` alone would reopen R1 for the snapshot;
- the old uploads are tracked in `R0` or hold the cut, so `C0 = min(T0 - lookback, hold - 1s)`.

`R0` stays fixed for the snapshot's lifetime. The snapshot itself:

- lists keys in order (`ListObjectsV2`) and delivers only objects with `LastModified <= C0`, except versions
  matching `R0`, which are left to the CDC. Newer objects are left to the CDC as well;
- fetches with `If-Match` on the listed ETag **and** the same `Last-Modified` check as the CDC (R8). On 412,
  404 or a `Last-Modified` mismatch the object is skipped; its newer version or delete marker is a CDC event
  above `C0`. Without the `Last-Modified` check, a same-content overwrite during the snapshot would be delivered
  by the snapshot under the old identity and again by the CDC.
- **Resume (s3#1041)**: `StartAfter = k`, same `C0`. Nothing before `k` comes again. Nothing is skipped: every
  object `<= C0` was visible at the snapshot start and is listed after `k` unless deleted.
- **Handoff without look-ahead (N5)**: every snapshot record carries a snapshot position. When the listing after
  `k` is exhausted, the source switches to CDC with `S = (C0, +inf)`, `E = {}`, `R = R0`. The same happens on
  resume from a snapshot position whose listing is already exhausted, which is the SRC-03 boundary case. Revision 1, like
  today's code, gave the last snapshot record a CDC position. That needs a look-ahead after every record and is
  where today's handoff error paths come from.
- Exact by construction: the snapshot delivers exactly the visible versions `<= C0` (latest per key), and the
  CDC starts strictly above. #1052's seed is no longer needed.
- **Behavior change for recent objects.** Objects modified within `lookbackPeriod` before the snapshot starts
  (or held above `C0` by a pending upload) are no longer snapshot records. They arrive as `create` records from
  the CDC after the snapshot ends. A consumer that treats `snapshot` and `create` differently sees the
  difference. The README and the release notes say so.

### Position format v2 (S4)

```text
byte 0          0x00            marker; v1 positions start with an S3 key, which cannot contain NUL
byte 1          0x02            format version
byte 2          's' or 'c'
payload         uvarint-length-prefixed fields
  snapshot:     C0 (unix ms, zigzag varint), k, r (uvarint), r x (16-byte key hash, Initiated unix ms)
  cdc:          S.time (unix ms), S.inf flag, S.key, S.versionID, n (uvarint),
                n x (uvarint delta ms, 16-byte hash), in insertion order,
                r (uvarint), r x (16-byte key hash, Initiated unix ms)
4 bytes         CRC-32C over bytes 0 .. end of payload
1 byte          0x00            fixed terminator
```

The fixed `0x00` at the end matters for rollback. v0.9.x parses a position by taking the last `_` and reading
the byte after it. A binary position that happens to end in `_` (about 1 in 256 without the terminator) would
make the old parser index past the end and panic. With the terminator it fails with "invalid position format".
The CRC rejects truncated or bit-flipped positions.

The parser is strict: bad marker, version, type, CRC, terminator, trailing bytes, a duplicate entry or oversized `E`,
or an out-of-range time fails `Open` with `s3.source.position_invalid`, the length and the fix (reset the
position). It never silently starts over. It is fuzzed.

### Upgrade from v1 positions

| Persisted position | v2 behavior on `Open` | Effect |
| --- | --- | --- |
| none | new snapshot | as today |
| v1 snapshot `<key>_s<sec>` | restart the snapshot; WARN `s3.source.position_upgraded` | the redelivery v1 does on every snapshot restart, once |
| v1 CDC `<key>_c<sec>` | CDC with `S = (min(sec - lookbackPeriod, hold - 1s), +inf)`, `E = {}`, where hold is the `Initiated` of the oldest upload that holds the cut at the first poll | redelivers the lookback window once, as v1 does on every restart; exact afterwards |

Nothing is skipped by the upgrade: v1 promised nothing above `sec - lookback`. Taking the minimum with the
oldest held upload also covers an upload that v1 had already passed with its newest-seen floor.

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

### `lookbackPeriod` (S5, R5)

- **`>= 30s`**: the supported range. `ExactResume` is claimed only here.
- **`1s` to `29s`**: accepted, with an `Open` WARN `s3.source.lookback_short`. At these values the cut's own
  imprecision is a large part of the window: `Date` is rounded down by up to a second, and the first page's
  latency (seconds under load) adds to it. It gives almost no tolerance for
  late visibility while looking safe, and it is not a configuration `ExactResume` is claimed for. Following the
  deprecation policy (announce, warn, remove; at least two minor versions), it is announced in this release and
  rejected two minor releases later. Rejecting it now would break pipelines that run with such a value today.
- **`0`**: accepted, documented as lossy (no tolerance for late visibility beyond the multipart hold), with an
  `Open` WARN. Never used by the acceptance driver.
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
| `lookbackPeriod` (existing) | `5m` | late-visibility tolerance; `>= 30s` supported, `1s`-`29s` deprecated, `0` lossy |
| `cdc.maxEarlyObjects` (new) | `2048` | bound of `E`; `0` = settled-only delivery |
| `cdc.maxSettledBytesPerPoll` (new) | `64MiB` | memory bound of the settled heap per poll |
| `cdc.multipartHold` (new) | `true` | track old multipart uploads (or hold the cut); `false` is lossy for long uploads |
| `cdc.maxTrackedUploads` (new) | `256` | bound of `R`; when full, the oldest untracked upload holds the cut |
| `cdc.maxPendingUploadAge` (new) | `24h` | an upload older than this is released; must be `>= lookbackPeriod` (validated) |

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
7. **Idle release of multipart uploads via `ListParts`** (revision 3's R3). Rejected in revision 4 as
   alternative (a); see _Tracked uploads_: a single slow part makes a live upload look idle, which loses it.
8. **8-byte hashes.** Rejected: the per-lookup collision probability (about `n / 2^64`) over a long-running
   pipeline is not negligible, and a collision is a skip.

## Failure modes

| Failure | Behavior | Signal |
| --- | --- | --- |
| SIGKILL during the snapshot | resume after the last acked key, same `C0` | SRC-03 snapshot cycles; chaos test |
| SIGKILL at the boundary | resume from the last snapshot position, listing exhausted, CDC from `(C0, +inf)` | SRC-03 boundary cycle |
| SIGKILL during CDC | resume from `(S, E)` | SRC-03 CDC cycles; chaos test |
| Multipart upload longer than the lookback (AWS stamps initiation) | tracked in `R`; the cut advances; the completed version (below `S`) is delivered by the `R` match | debug log; entry visible in the position |
| More long-running uploads than `cdc.maxTrackedUploads` | the oldest untracked upload holds the cut; latency until an entry frees up, no loss | WARN `s3.source.cut_held` |
| Upload completes and is overwritten before the next poll | entry dropped; the latest version is a normal candidate (collapsing) | debug log |
| Plain PUT of a tracked key in the same second as the upload's initiation | not a multipart ETag, so it does not consume the entry | covered by a test |
| Upload initiated during a slow uploads listing, under a key already paged past (R1) | above `Tu - lookback`, so above `C`; seen by the next poll | property test |
| Upload older than `maxPendingUploadAge` | released; if it completes later it can be missed | WARN `s3.source.stale_upload_ignored`; README lifecycle rule |
| `ListMultipartUploads` denied | at `Open`: fails with the fix; while running: the iterator stops with the same code (no fallback); opt-out `cdc.multipartHold: false` is lossy for long uploads | error `s3.source.multipart_listing_denied` |
| Same-content overwrite between listing and `GetObject` (R8) | `If-Match` passes, the `Last-Modified` check fails; skipped, the new version comes later | debug log |
| Replication lag, slow single PUT, or store node skew above the lookback | can be missed (A1) | documented requirement |
| More settled candidates than the per-poll heap | `S` advances only through what was delivered; the rest come next poll | debug log |
| Window busier than `E` | the excess waits until it settles | WARN `s3.source.early_window_full` |
| Version overwritten or deleted between listing and `GetObject` | 412/404, skipped, the newer version or marker comes later | debug log |
| Metadata-only or same-content overwrite in the same second, unversioned bucket (N2) | same ETag, LastModified (to the second on AWS) and version `"null"`: identical to the delivered one, not redelivered, and the `Last-Modified` check cannot tell either | documented; versioned buckets distinguish it |
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

- **Model store** (in-process, behaves like AWS):
  - initiation-stamped multipart uploads with multipart-form ETags, listed by `ListMultipartUploads` (prefix
    honored, paged) while in progress;
  - a `Date` header per response that advances while a listing pages;
  - visibility delays below and above the lookback;
  - overwrites (including same-content overwrites), delete markers and same-millisecond versions;
  - 412 on a stale `If-Match`, and paging.

  A MinIO mode stamps at completion and returns no uploads for directory prefixes.
- **Seeded random property tests** (`rapid` is not in the tree, so table tests plus a seeded generator): kill at
  a random record, resume from its position, repeat. Check exactly once within the assumptions, and missing only
  where an assumption is broken on purpose. Named cases:
  - settled heap budget of one entry, and `cdc.maxEarlyObjects = 1` (B2);
  - a long multipart upload on an idle bucket (B1);
  - **an upload initiated during a slow uploads listing, under a key already paged past (R1)**;
  - uploads that outlive many polls and kills, complete below `S`, and are delivered exactly once via `R`; more
    such uploads than `cdc.maxTrackedUploads` (the cut holds); one completed and overwritten before the next poll;
    a plain PUT of the same key in the same second; one slow part longer than an hour (the case alternative (a)
    loses) (R3);
  - `AccessDenied` on `ListMultipartUploads` while running: the iterator stops (R4);
  - a same-content overwrite during the snapshot (R8 for the snapshot);
  - an upload initiated during the snapshot's uploads listing (R1 for the snapshot, `C0 = min(Tu, Tl)` rule);
  - restarts across pruning (B3);
  - a version written while a multi-page versions listing runs (B4);
  - a same-content overwrite between listing and fetch (R8).
- **Position codec**: round trip, strict rejection, a fuzz target, and the v0.9.5 parser fed v2 positions must not
  panic.
- **Memory** (R6): 10,000 records in flight with `E` full; the heap stays within the formula plus 10%.
- **Upgrade and rollback**: v1 snapshot and CDC positions, constructed and taken from a real v0.9.5 run, including
  a v1 CDC position with an upload pending (NIT: the min with the hold). In the chaos harness, run released
  v0.9.5, SIGKILL, swap in the new build, resume, and check nothing is lost. Then the reverse: the old build must
  fail `Open` cleanly.
- **Chaos** (`test/chaos`, extends s3#1055): S3 source → `builtin:file` in a real Conduit.
  - The snapshot data set is **pre-settled** before the first start (R2): the harness waits until the store's
    `Date` minus the lookback is past the newest object's LastModified, so the snapshot delivers it as
    snapshot records and the snapshot kills hit a real snapshot. Otherwise every object would sit above `C0` and
    the "snapshot" kills would test the CDC.
  - SIGKILL during the snapshot (more than one page), at the boundary, and during CDC with concurrent writes and
    a multipart upload spanning a kill.
  - Invariants 1 to 3 are checked after each restart, and redeliveries are counted. Conduit's end-to-end
    at-least-once can still duplicate; source exactness is SRC-03's job.
- **MinIO**: the existing `TestMinIO_*` suite, adjusted where it asserts today's redelivery bounds, plus a
  multipart test that records the stamp and prefix semantics, so a MinIO change is noticed.
- **Acceptance suite v2, certified on defaults (S2, R2).**
  - **Pre-settled snapshot batches.** For records the suite writes as `snapshot` operations, the driver's
    `WriteToSource` returns only once the store's `Date` minus `lookbackPeriod` is past the newest written
    object's LastModified (it polls a cheap `HeadBucket` or `ListObjectsV2` with `MaxKeys=1` for the `Date`).
    Without this, objects written just before `Open` sit above `C0`. The snapshot would deliver none of them,
    and SRC-03's "snapshot cycles" would exercise CDC resume three times, as revision 2 did by mistake. With
    the default 5m lookback each snapshot batch waits about 5 minutes. The suite writes about eight snapshot
    batches, so the certified run takes about an hour, and its CI job runs nightly and on demand with a 120m
    timeout. `ReadTimeout` is 40s, so SRC-02's 10,000 CDC objects fit the suite's limit
    (`10 x ReadTimeout + 2ms x n` = 420s): about 2,048 are delivered early, and the rest settle within 5 minutes
    plus a poll.
  - **Settling run.** A second, uncertified test (`TestAcceptanceV2_Settling`, `lookbackPeriod: 30s`) also
    makes `WriteToSource` for `create` batches wait until they have settled. In SRC-03, records then settle
    between kills, so restarts exercise `S` advancing, `E` pruning and positions with an empty `E`, which the
    certified run, where CDC records are still early at each kill, does not.
  - **Fast lane.** Every PR runs the suite with `lookbackPeriod: 30s` and pre-settling (about 5 minutes). It is
    not the certified configuration, and its job name says so.
  - If the defaults cannot pass SRC-02, the default bound is raised rather than the certified config changed.
  - The suite itself does not check that pre-`Open` records arrive as snapshot operations. That is filed as
    `ConduitIO/conduit-connector-sdk#415`, a capability-gated assertion for a minor revision.

## Observability

WARN codes: `s3.source.early_window_full`, `s3.source.cut_held`,
`s3.source.stale_upload_ignored`, `s3.source.poll_slow`, `s3.source.lookback_short`,
`s3.source.position_upgraded`. Error codes: `s3.source.position_invalid`, `s3.source.store_unsupported`,
`s3.source.multipart_listing_denied`. Debug per poll: `S`, `|E|`, `C`, the hold (oldest
upload), settled, early and waiting counts, pages, duration. No new metrics: the SDK has no per-connector custom
metrics hook.

## Implementation plan

One Tier 1 PR in `conduit-connector-s3` after sign-off. It supersedes #1052, which closes.

1. `source/position`: v2 codec, CRC, terminator; v1 parsing kept for upgrade only; fuzz target.
2. Snapshot: `C0` with the multipart hold, `If-Match` fetch, `StartAfter` resume, no look-ahead, no anchor pass
   (s3#1041).
3. CDC: uploads listing with prefix and the active/idle/stale rules, `T = min(Tu, Tv)`, cut, settled heap
   bounded by bytes, `S`/`E` with time-delta entries and incremental encoding, versioned or `If-Match` plus
   `Last-Modified` fetch, removal of the dedupe budget and shrink code.
4. `Open` checks for unsupported stores and the multipart permissions; validation of `lookbackPeriod` and
   `maxPendingUploadAge`.
5. Tests as above; the driver claims `ExactResume` on defaults; #1050 turns green.

Docs: the connector README and a changelog entry. The README covers:

- assumptions A1 to A3;
- the permissions, including `s3:ListBucketMultipartUploads` and `s3:ListMultipartUploadParts`;
- a setup section with the `AbortIncompleteMultipartUpload` lifecycle rule;
- the settings and the memory formula;
- the listing cost;
- the behavior change for recent objects;
- upgrade and rollback. The `conduit.io` page and `llms.txt` are
updated with the release that bumps the built-in.

## Revision history

Revision 4 (2026-10-15), after the review of revision 3 (no blockers):

- The snapshot cut uses `C0 = min(Tu, Tl) - lookback` with `Tl` the first `ListObjectsV2` page's `Date`, so the
  snapshot does not reopen R1. The snapshot `GetObject` gets the same `Last-Modified` check as the CDC (R8).
- R4: AccessDenied while running stops the iterator; the `Open` probe is specified. Under the proposal no
  `ListParts` is needed; the `ListParts` probe is specified for alternative (a).
- The fetch paragraph no longer claims "its tuple is larger" for an early-initiated, late-completed upload; it is
  delivered because of `R` or the hold.
- The memory test is stated as connector-side only; the gRPC copy and SDK/engine clones are measured in the chaos
  harness.
- `Date` rounding and first-page latency are named as the reason the 1s to 29s lookbacks are deprecated.
- **R3, for DeVaris's decision**: proposal (b), the bounded tracked-upload set `R` in the position, replaces
  revision 3's idle release (a), which can lose a live upload with one slow part. `cdc.maxUploadIdle` and the
  `ListParts` calls are gone; `cdc.maxTrackedUploads` (256) is new.

Revision 3 (2026-10-15), after the re-review of revision 2 (B2, B3 and S4 held):

- **R1 (blocker)**: `T = min(Date of the first uploads page, Date of the first versions page)`, so an upload
  initiated during a slow uploads listing cannot fall below the cut. The property test has this case. The "gap is
  trivially below the lookback" reasoning is withdrawn.
- **R2 (blocker)**: the acceptance driver and the chaos test pre-settle snapshot data, so the snapshot cycles
  exercise the snapshot. There is a settling SRC-03 run in which records settle between kills, and SDK issue
  `ConduitIO/conduit-connector-sdk#415`.
- **R3**: WARN `s3.source.cut_held`; the active-upload rule (`ListParts`, `cdc.maxUploadIdle`, 1h); a lifecycle
  rule in the README setup.
- **R4**: always send `Prefix`. A denied listing fails `Open` by default; the `cdc.multipartHold: false` opt-out
  never claims `ExactResume`.
- **R5**: `lookbackPeriod` 1s to 29s accepted with a WARN, deprecated, removed two minors later.
- **R6**: memory bound stated as a function of in-flight records and batch settings; a memory test at 10,000 in
  flight; incremental encoding; default bound 2048.
- **R7**: settled heap bounded by bytes (64 MiB) with the rate ceiling stated.
- **R8**: `Last-Modified` compared as well as `If-Match`.
- **NITs**: A3 wording; `maxPendingUploadAge` measured on the store clock and validated `>= lookbackPeriod`; v1
  upgrade takes the min with the oldest held upload; the line-167 reasoning; the behavior change for recent
  pre-existing objects.

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

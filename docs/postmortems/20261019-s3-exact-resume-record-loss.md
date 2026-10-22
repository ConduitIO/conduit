# Postmortem: S3 exact resume shipped with the review's two record-loss blockers

This is a blameless postmortem. It records what happened, why the fixes the review asked for never landed,
and what changes. Changes: s3#1057, s3#1059, s3#1060, s3#1061. Fix: s3#1070. Advisory: GHSA-gxm3-5pgq-2p8v
(published). Here `s3#N` is an issue or PR in `ConduitIO/conduit-connector-s3`, and a bare `#N` is one in this
repository.

## Summary

The exact-resume stack (s3#1057 position codec, s3#1059 snapshot, s3#1060 change capture, s3#1061 settings and
Open probes) gives every S3 source record a position that says which objects are settled, which were delivered
early, and which multipart uploads are tracked. Its fresh-context Tier 1 review returned **changes needed** with
two blockers, both record loss:

1. **Settled-heap eviction skips a candidate.** The per-poll settled heap is bounded in bytes. Evicting a large
   candidate could make room for a smaller one that sorts above it; that one was delivered, the settled point
   moved past the evicted one, and the evicted object was never read again. 8 of 300 property-test seeds lost a
   key's final version.
2. **Tracked uploads matched by key and second.** A tracked multipart upload was recognised by its key and the
   second it started. When another multipart version or upload of the key had the same second, the source took
   the wrong object: on AWS the key's final version was lost, on MinIO a version was redelivered on every poll.

A fix lane was started. It stopped when the coordinating session hit its usage limit an hour later. The next
day all four PRs merged at exactly the heads the review had rejected, and v0.11.0 and v0.11.1 shipped with
both blockers.

**Severity: sev-0** (records never delivered, never routed to a DLQ; invariant 3). **Status:** fixed. s3#1070 (merged
2026-10-20) fixes both blockers and a related duplicate, released in v0.11.2. Affected: `conduit-connector-s3` v0.11.0
and v0.11.1. No
Conduit release bundles them. No production report is known.

## Impact

- **Blocker 1:** CDC with more settled candidates in one poll than `cdc.maxSettledBytesPerPoll` holds (default
  64 MiB, roughly 300,000 objects), with entries of different sizes. Catch-up after downtime or a restart and the
  first poll after a large snapshot are the likely cases.
- **Blocker 2:** CDC with `cdc.multipartHold: true` (the default) on keys that get two multipart writes, or a
  multipart write and another version, in the same second. AWS: lost final version. MinIO: duplicates.
- **What is lost:** a version of an object, permanently, with no log line. If the key is written again later,
  the newer version arrives, so only the skipped version is missing from the destination.
- **Blast radius in Conduit:** none released. Conduit v0.20.x bundles s3 v0.9.5. Conduit `main` has bundled
  v0.11.x since 2026-10-19 (#3209), unreleased.

## How it was detected

The review found both blockers before merge (2026-10-15). After the catch-up on 2026-10-19, the release captain
compared what had merged with the review log, saw that the stack had merged without the fixes, and re-ran the
review's property test on s3 `main` with `S3_PROP_SEEDS=300`. The same 8 seeds failed. `matchesTracked` was
unchanged, so blocker 2 was still there too.

## Timeline (UTC)

- **2026-10-15 20:02:** Tier 1 review of the stack: changes needed. s3#1057 ready but held ("no partial stack on
  main"). Blocker 1 on s3#1060, blocker 2 on s3#1059/s3#1060 (with a real MinIO repro), two SHOULDs. A fix lane is
  started. The exact-resume design doc, #3099, is still open and unreviewed.
- **2026-10-15 21:03:** the coordinating session hits its weekly usage limit. The fix lane stops with it; nothing
  is pushed. The decision log records nothing until 2026-10-19 21:00.
- **2026-10-16 20:49 to 22:27:** s3#1057, s3#1060, s3#1059 and s3#1061 merge from local-model sessions, at the
  reviewed heads, with no review on any of them and no comment linking the review.
- **2026-10-19 02:58:** v0.11.0 is published.
- **2026-10-19 19:22:** v0.11.1 is published.
- **2026-10-19 21:00:** catch-up survey: the 8 seeds fail on s3 `main`, blocker 2 unchanged.
- **2026-10-20 01:24:** s3#1070 opens with both fixes, the duplicate fix, AWS- and MinIO-style stamping in the
  model, 13 pinned seeds and a 2000-seed nightly. Sent to fresh-context review by the original reviewer.
- **2026-10-20 01:37:** DeVaris accepts the hold-latency trade-off (with a WARN on long holds), approves the
  position-format change as a separate PR after v0.11.2, and asks for v0.11.0 and v0.11.1 to be marked
  affected.

## Why it wasn't caught

It was caught. It was not stopped.

- **Review findings lived only in the decision log.** The review's verdict was not on the PRs as a GitHub
  review or comment. A session that opened the PRs later saw green CI, a Tier 1 label in the body and no
  objection.
- **Nothing tied the merge to the review.** No check required an approving review on a Tier 1 PR, and nothing
  compared the merged head with the head the review had passed or failed. The rule "never merge a Tier 1 change
  on automated review alone" was not enforced by anything a different session would run into.
- **A lane's death was silent.** When the session stopped, its lanes stopped without a marker on the PRs.
  Nothing said "fixes in progress, do not merge".
- **The property test that found blocker 1 ran 20 seeds per PR.** The first failing seed is 82, so the PR
  runs passed. The 300-seed run was a reviewer's manual run, and there was no nightly long run.
- **The model could not see blocker 2.** It stamped every version and upload in milliseconds, so the AWS
  same-second case never occurred in the per-PR run. The review found it by reading the code and confirming on
  real MinIO.
- **The release cut did not ask.** v0.11.0 and v0.11.1 were cut without anyone checking open review findings
  against the release's contents.

## What went well

- The review found both bugs before merge, with seeds, a MinIO repro and a proposed patch for blocker 1.
- Because the review's evidence was written down (seed list, patch), the catch-up confirmed the problem in
  minutes.
- s3#1070 kept the position format, so v0.11.0/v0.11.1 positions resume on v0.11.2 without a migration.

## Fix

s3#1070, for v0.11.2:

- The planner remembers the smallest evicted candidate and admits nothing at or above it in that poll (the
  review's patch, with a comment).
- An upload that shares its second with another upload or version of its key is not tracked. It holds back
  settling until it completes, or until `cdc.maxPendingUploadAge` (24 hours), and its object is then delivered
  as an ordinary version. A WARN `s3.source.cut_held_same_second` with `key` and `hold_age` fires when a hold
  lasts more than 15 minutes.
- A delivered object removes the tracked entry it matches (the duplicate when an upload completes between the
  two listings).
- Positions are unchanged. Uploads already tracked by v0.11.0/v0.11.1 keep the old matching until they resolve;
  closing that needs a per-upload identity in the position, which is a separate Tier 1 PR.

## New automated checks

- **Model stamps like AWS and like MinIO**, chosen per seed, with same-second uploads and sub-second steps
  (s3#1070). The per-PR run has 40 random seeds plus 13 pinned seeds, each verified to fail with its fix
  reverted.
- **Nightly 2000-seed property run** (`.github/workflows/property-nightly.yml`, s3#1070).
- **Real MinIO kill test** `TestMinIO_KillAnywhereVersionedBucket` in the existing MinIO CI job.
- **Tier 1 review recorded** and a **release gate on open findings** (proposed, not built yet; see action items 9 and
  11 in `20261021-data-loss-and-process-failures.md`).

## Follow-ups

- Prepend the affected notice to the v0.11.0 and v0.11.1 release notes if that is not done yet.
- Conduit `main` pins s3 v0.11.2 as of 2026-10-21.
- Per-upload identity in the position (separate Tier 1 PR, stacked on s3#1070, needs an upgrade test).
- Review and merge the exact-resume design doc #3099, which the code shipped ahead of.
- Hold time has no metric yet, only the WARN.

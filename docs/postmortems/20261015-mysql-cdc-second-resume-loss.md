# Postmortem: MySQL CDC loses rows on the second restart inside a binlog event

This is a blameless postmortem. It records what happened, why nothing caught it for eighteen months, and what
changes. Issue: mysql#227. Fix: mysql#228. Here `mysql#N` is an issue or PR in the
`ConduitIO/conduit-connector-mysql` repository, and a bare `#N` is one there too unless it says otherwise.

## Summary

The MySQL source stores, on every CDC record, the binlog event it came from (`pos`, the end offset of the rows
event), the row inside it (`idx`) and the end of the previous captured rows event (`prev`). A restart ran
binlog replication from `prev` and dropped the first `idx+1` rows of the first event it received.

After a restart in the middle of event E, the records of the rest of E were built with `prev` set to E's own end
offset, and their `idx` counted from 0 instead of from their place in E. A second restart from one of those
records started reading after E, so the rest of E was never read, and dropped `idx+1` rows of whatever event came
next. When the next event had fewer rows than that, the restart logged "some records could be lost" and panicked
on a slice bound, so the plugin crash-looped.

Two defects sat next to it in the same path. A restart inside a later rows event of one large statement (MySQL
logs it as one `Table_map` event and several rows events of 8 KiB each by default) started reading after the
`Table_map`, so the event could not be decoded and every restart halted. And all records of one event shared
one metadata map, which the SDK's batching goroutine writes while earlier records are already with the caller.

**Severity: sev-0** (records never delivered, invariant 3). **Status: fixed in #228, not yet released.** The
loss shipped in v0.1.4, v0.2.0 and v0.3.0 of `conduit-connector-mysql`. No production report is known.

## Impact

- **What it takes to trigger the loss:** two restarts, both landing inside the same multi-row binlog event,
  with the second one after at least one acked record of the rest of that event. A multi-row event is any
  statement that changes more than one row (a bulk INSERT, an UPDATE or DELETE with a range predicate). A
  restart is any stop: a deploy, a crash, a `kill -9`, a pipeline stop.
- **What is lost:** the rows of E after the second restart point, and the first `idx+1` rows of the event
  after E (of any captured table). Observed: of 8 rows to deliver, 6 were lost. Nothing is logged unless the
  next event is smaller than `idx+1`, in which case the plugin panics.
- **The halt on large statements** (defect 2) loses nothing but leaves the pipeline down until it is
  re-snapshotted.
- **Detectability after the fact:** a stored position written by the defect has `prev` equal to its own
  `name`/`pos`. A pipeline whose stored position looks like that was one restart away from the loss.

## How it was detected

While adopting the SDK acceptance suite v2 for the v0.21 MySQL certification (#3005 in this repository), the
suite passed, including its seven-kill resume case (SRC-03), whose kill points land in a fresh event after each
restart. The lane then wrote a process-level SIGKILL harness (`test/chaos`, #229) that kills the source twice
inside one 500-row statement. Its first runs hit the metadata data race under `-race`; once that was fixed, the
restart after the second kill panicked with "some records could be lost". Reading the resume code from there
gave the loss, which a targeted integration test then reproduced on `main`.

## Timeline

- 2025-04-03: #120 ("some records could be lost during CDC-iterator read") adds row-index resume inside an
  event. It seeds the first event's `prev` with the saved position's own offset and numbers its rows from 0.
  Ships in v0.1.4.
- 2025-04 to 2026-10: v0.2.0, v0.3.0 released with it. The existing restart tests resume once.
- 2026-10-15: the SIGKILL harness fails on `main`; the loss is reproduced and fixed (#228), the harness lands in
  CI (#229).

## Why no test caught it

- Every resume test restarted once. The defect needs a second restart inside the remainder of an event that was
  itself resumed.
- The acceptance suite's resume case writes events of up to 40 rows and kills after acking part of the next read,
  which after a restart usually lands in a fresh event. It passed, as it should for most kill points. A
  certification suite samples behavior; it does not prove it.
- Nothing checked that positions only move forward. With `idx` restarting at 0, they visibly went backwards.
- Large statements were never written as one event in tests, so the `Table_map` defect never showed.

## Fix

The fix (#228) adds `replay_from` to every CDC position: the point canal itself would resume from (after a commit, BEGIN,
DDL or rotation), from which the event's `Table_map` is read again. A restart reads from there and drops, by
binlog offset, every event that ends before the saved one, then the saved event's rows up to `idx`, which is now
the row's index in the whole event. Positions from older versions read their binlog file from its start, once;
one with the defect's signature delivers its event again in full (duplicates, never a gap). Each record gets its
own metadata. The field is additive, so older builds still read new positions.

## New automated checks

- `test/chaos` (#229), in CI on every PR: SIGKILLs the source as a process mid-snapshot, mid-checkpoint, at the
  snapshot/CDC boundary, twice inside one large statement and at a random instant, then checks no gap, no
  redelivery of stored records, and monotonic positions.
- Integration tests in #228 for the second resume, the row index, a resume inside a later rows event, shared
  metadata, and both kinds of older position. Each fails on the code before the fix.

## Follow-ups

- Release a connector version with #228 and say in its notes that a pipeline whose stored position has `prev`
  equal to its own position re-delivers one event after the upgrade.
- The acceptance suite could add a resume case with two kills inside one large source write (SDK), as a
  proposal for the next suite minor (not filed yet).
- Compressed transactions still carry offset 0 in positions (mysql#213); #228
  never drops such events, which can only redeliver.

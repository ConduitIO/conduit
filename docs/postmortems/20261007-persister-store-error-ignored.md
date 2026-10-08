# Postmortem: Persister commits and reports success when a connector's state write fails

This is a blameless postmortem. It records what happened, why nothing caught it for more than three
years, and what changes. Issue: #2925.

## Summary

`Persister.flushNow` (`pkg/connector/persister.go`) writes a batch of connector states in one
transaction. Inside the loop over the batch, the result of each write was assigned with `err :=`,
which declared a new `err` scoped to the loop body and shadowed the function's outer `err`. A
failed write was logged and then forgotten: the outer `err` stayed nil, the transaction committed
whatever else it held, and every connector in the batch was told its write had landed.

For a source, that callback is `Source.onPersistFlushed`. Since #2680 it is the gate that releases
the deferred plugin ack, so a nil there tells the plugin to commit upstream (advance a Postgres
replication slot, commit a Kafka offset). On this path the position that would make that ack safe
was never stored. After a restart the source resumes from the last stored position, behind what the
upstream already acked and, for a pruning upstream, already discarded. That violates invariant 1
(ack only after durable handling) and invariant 2 (positions are crash-safe).

A second defect sat next to it. If `NewTransaction` failed, `flushNow` returned without calling any
callback, so `callbacksDone` never closed. Every unbounded `WaitPendingWrites` caller
(`lifecycle.Service.StopAndWait`, `Persister.Wait` at runtime shutdown) would then hang forever,
and the source's queued acks were never resolved either way.

**Severity: sev-0 class** (an ack released for an undurable position). **Status: fixed in the PR
for #2925, shipping in v0.20.0.** No production occurrence is known.

## Impact

- **What it takes to trigger:** a connector state write that fails inside a transaction that can
  still commit. The rest of the batch is written and the failed connector is skipped. A failure that
  poisons the transaction also fails `Commit`, and that error was always propagated, so those
  failures were not affected.
- **Store by store:**
  - **badger (default):** reachable, and reproduced against the real store. Badger caps a
    transaction at 15% of its 64 MiB memtable (about 9.6 MB). Values under its 1 MiB value-log
    threshold count in full. Once a batch crosses the cap, `txn.Set` returns `ErrTxnTooBig` and
    leaves the transaction committable. Sixteen connectors with about 800 KB of encoded state each,
    flushed together, hit it (`TestPersister_Badger_TxnTooBigFailsWholeBatch`). Before the fix the
    writes up to the cap committed, the rest were dropped, and all sixteen callbacks got nil. That
    needs unusually large connector state (a large position, or large settings, which are stored
    twice: `Config` and `LastActiveConfig`) across many connectors flushing in the same debounce
    window, so it is rare. A single value of 1 MiB or more costs only a pointer in the batch and
    does not trigger it. A single value over badger's 1 GiB value-log file size fails `Set` with
    "Value with size ... exceeded" and was also silently dropped.
  - **Postgres:** not reachable in practice. A failed statement aborts the transaction, so
    `Commit` returns an error, which was propagated.
  - **SQLite:** plausible, not reproduced. A statement-level error such as `SQLITE_BUSY` does not
    necessarily roll back the transaction, so a later `Commit` could succeed without the failed row.
  - **In-memory:** writes never fail.
  - **Encoding:** `Store.PrepareSet`'s JSON encoding of an `Instance` cannot fail for the types it
    holds today.
- **Which acks:** sources only. Destinations and the lifecycle-event persist in `Open` lost the
  error the same way, but no ack depends on those callbacks.
- **Blast radius when it fires:** every source in the failed batch that acked in that debounce
  window. On restart, a retention-based upstream (Kafka) redelivers from the older position, so
  you get duplicates. A pruning upstream (Postgres slot) hits a structural gap: the records
  between the stored position and the acked one are gone.
- **Visibility:** one error log line, `error while saving connector`, and nothing else. The
  pipeline kept running and reported healthy.

## How it was detected

By reading. While writing the status-write-failure design (#2922), the author traced every place a
store error can go and found that the per-connector error in `flushNow` went nowhere. Reproduction
came afterwards, with the regression tests below.

## Timeline

- **2023-01-23** — #789 ("Fix connector initialization + refactoring") changes the loop from
  `err = p.store.Set(...)` followed by `break` to `err := p.flushSingle(...)` with no `break`. The
  shadowing starts here. First release containing it: v0.5.0.
- **2023-02-17** — #857 replaces `flushSingle` with a prepared `storeFunc`. The `err :=` survives.
- **2023 to 2026-07** — present in every release. At this point the bug loses a position write
  silently (invariant 2), but the plugin ack was sent before persisting anyway (the separate
  ack-before-persist sev-0, docs/postmortems/20260723-source-ack-persist-ordering.md), so this bug
  did not decide whether the ack went out.
- **2026-07-23** — #2680 moves the plugin ack behind the persist callback. From here the bug defeats
  that new gate on this path: a failed write releases the ack. Present in v0.19.0.
- **2026-10-07** — found while writing #2922, filed as #2925, fixed with regression tests and a
  chaos gate. DeVaris decides it ships in v0.20.0.

## Why no test caught it

- Every persister test used the in-memory store, whose writes never fail, and none injected a
  failure. The callback's error argument was only ever checked for being nil.
- The #2680 tests proved the ack waits for a successful flush. None asked what happens when the
  flush fails, because the code looked like it already handled that (`cb(err)`, and
  `onPersistFlushed` has an `err != nil` branch). That branch was unreachable for this failure.
- The chaos suite runs against a real badger DB, but nothing in it could make a write fail.
- `golangci-lint` runs `govet` without its `shadow` analyzer, which would have flagged `err :=`
  shadowing a variable read later in the function. Turning it on repo-wide is a separate
  decision (it is noisy on idiomatic code), tracked in #2930.

## Fix

- `flushNow` is all-or-nothing. Any write error discards the transaction, and every callback in the
  batch gets the same error, which names the failing connector(s). A `NewTransaction` error reaches
  every callback too. Every path closes `callbacksDone`.
- Connectors that share a batch with the failing one get the error too, possibly in other
  pipelines. The persister cannot tell whether a store left the transaction committable (badger
  does after `ErrTxnTooBig`, Postgres does not), so it does not commit around a failure. The other
  option, retrying the remainder in a second transaction, is tracked in #2930.
- `Source.onPersistFlushed` with an error keeps the acks queued and never releases them. A later
  successful flush stores a later cumulative position that covers them. The error goes to the
  pipeline through `Errors()` while the plugin runs. Once teardown has started nobody reads that
  channel, so the error is kept and returned from `Teardown` instead of blocking the persister
  callback (which would have hung `WaitPendingWrites` for everyone).

## New automated checks

- `pkg/connector/persister_store_error_test.go`:
  - batch abort with two connectors: nothing committed, both callbacks get the error;
  - `NewTransaction` failure: every callback gets the error and `WaitPendingWrites` returns;
  - recovery after a failed batch;
  - source end to end: an innocent source in a failed batch gets the error on `Errors()` and its
    plugin never receives the ack;
  - source teardown: a failed final flush withholds the final ack and `Teardown` returns the error;
  - real badger `ErrTxnTooBig`: the whole oversized batch fails and nothing is committed.
- `tests/chaos/storefault_test.go` (`TestSIGKILL_StoreFault_PruningUpstream_NoGap`): a child
  process with a real badger store whose position writes start failing, against a pruning upstream,
  SIGKILLed and restarted. Before the fix the restart reports `OPEN_GAP_ERROR`. It runs in the
  required `tests/chaos (race, x3)` check.

All of these fail on the pre-fix code and pass with the fix. The PR has the output.

## Follow-ups

- Arch-v2 (`pkg/lifecycle-poc`) never reads `Source.Errors()` or `Destination.Errors()` (see the
  TODO on `funnel.Source`). With this fix a failed flush under arch-v2 still withholds the ack, so
  there is no data loss, but the error is never surfaced. A source whose writes keep failing would
  keep running and never ack upstream. Until teardown, that is: then `Teardown` returns the error.
  This has to be fixed before the arch-v2 flip in v0.21. Tracked in #2929.
- `Destination`'s lifecycle-event persist callback still sends on its unbuffered `errs` without a
  teardown escape. Tracked in #2930.
- Consider committing the healthy part of a failed batch in a second transaction, to stop one
  connector's failure from failing connectors in other pipelines. Tracked in #2930.
- Decide whether to enable `govet`'s `shadow` analyzer, at least for `pkg/connector`,
  `pkg/lifecycle` and `pkg/lifecycle-poc`. Tracked in #2930.
- Operator runbook: `docs/operations/connector-state-write-failures.md`.

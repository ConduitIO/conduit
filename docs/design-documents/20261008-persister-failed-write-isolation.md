# Isolating a failed connector write in the persister batch

## Summary

Since #2932, `connector.Persister` fails a whole batch when one connector's state write fails:
nothing is committed and every connector in the batch gets the error. The batch is process-wide,
so one connector with unwritable state fails connectors in other pipelines too. This change keeps
the failure with the connector whose write failed. On a failed write the persister discards the
transaction, records the error for that connector, and writes the remaining connectors again in a
fresh transaction. Each callback receives the outcome of its own write. Tracked in #2930.

## Context

The persister collects changed connectors for up to one second (or 10,000 changes) and writes them
in one transaction (`flushNow` → `writeBatch`). A source's persist callback releases its deferred
upstream ack (invariant 1), so a callback may only get nil if that connector's state is committed.

Issue #2925 found that a per-connector write error was ignored and the batch committed anyway. #2932
fixed it by failing the whole batch, because whether a transaction is still usable after a failed
write depends on the store:

| Store | After a failed write in a transaction | `Discard` |
| --- | --- | --- |
| badger | Txn stays usable after `ErrTxnTooBig`; the failed `Set` is not applied | drops pending writes |
| Postgres | Txn is aborted; every later statement fails with "current transaction is aborted"; `Commit` fails | `ROLLBACK` |
| SQLite | `RAISE(ABORT)`/constraint errors: statement undone, txn continues. `SQLITE_FULL`, `IOERR`, `RAISE(ROLLBACK)`: whole txn rolled back, and later statements on the connection run in autocommit | `ROLLBACK` (an error if SQLite already rolled back; logged) |
| in-memory | Txn stays usable | no-op (changes are only applied on `Commit`) |

The cost of whole-batch failure: a connector that cannot be written (oversized state, a bad value)
fails every other connector that flushes in the same second, in any pipeline, every time.

## Decision

`writeBatch` writes the connectors in sorted ID order in one transaction and stops at the first
failed write. It then:

1. discards the transaction (never reuses it, whatever the store);
2. records that write's error for that connector only;
3. writes the remaining connectors again in a new transaction.

It repeats until an attempt commits. A failure that is not one connector's write (opening the
transaction, or `Commit`) cannot be attributed, so every connector still in that attempt gets that
error and nothing is retried.

Stopping at the first failure matters. Continuing to write in the same transaction, as the code did
before, makes Postgres fail every later write with "transaction is aborted", which blames
connectors whose writes were fine. On SQLite after an internal rollback, later writes in the "same"
transaction would even run in autocommit mode and land while the batch is reported failed.

Retrying a connector's write is safe because `Store.PrepareSet` captures a copy of the instance at
`Persist` time; the store function encodes and sets the same bytes every time it runs.

## Alternatives

**Keep whole-batch failure (status quo after #2932).** Simple and correct, but one unwritable
connector fails unrelated pipelines on every flush. Rejected: the blast radius is the problem
issue #2930 asks to fix.

**Continue past a failed write and commit the rest in the same transaction.** One transaction, no
retry. Correct on badger and in-memory only. On Postgres the commit fails anyway, and on SQLite it
can commit writes outside the transaction. Rejected: it depends on store behavior the persister
cannot see.

**On any failure, write every connector in its own transaction.** Perfect isolation, simple to
state. It costs one transaction per connector on every failed flush, even when one retry would do,
and it changes the badger `ErrTxnTooBig` case from "the overflow connectors fail" to "nothing
fails, but the batch is written in N transactions". Rejected for the common case cost; the chosen
design reaches the same per-connector outcome when every write fails, and needs two transactions
when one connector fails.

**Split the batch on `ErrTxnTooBig` instead of dropping the overflow connector.** Would commit
every connector in the oversized-batch case. Rejected for now: it is badger-specific and needs the
persister to recognize a store error. The overflow connector's next flush is a smaller batch and
normally succeeds.

## Consequences

- A connector whose write fails gets its own error; the rest of the batch commits and gets nil.
- A flush makes at most one transaction per connector in the batch. The worst case, every write
  failing fast (e.g. a store rejecting all writes), is N short failed transactions instead of one.
  A store that hangs hangs the flush either way, as before.
- For badger `ErrTxnTooBig`, the connectors whose writes cross the size limit fail, sorted last by
  ID. If the batch is oversized on every flush, those same connectors keep failing while the rest
  make progress. Before, every connector failed.
- Error messages change from `failed to store connector batch, transaction discarded: connector
  "<id>": ...` to `failed to store connector "<id>", its state was not committed: ...`. The error
  code `connector.state_persist_failed` and suggestion are unchanged.

## Failure modes

| Failure | Behavior | Invariants |
| --- | --- | --- |
| One connector's write fails | That connector gets its error; others committed in a retry and get nil | 1: nil only after commit. 3: the error reaches its own callback |
| Several writes fail | Each failing connector is left out in turn and gets its own error | same |
| `NewTransaction` fails (first attempt or a retry) | Every connector still in that attempt gets the error; earlier-failed connectors keep their own | 1, 3 |
| `Commit` fails | Every connector in that attempt gets the error; not retried (not attributable) | 1, 3 |
| Crash during a retry | Nothing from an uncommitted attempt is durable; the source never released those acks; restart resumes from the last committed position | 1, 2 |
| Out-of-order state | Retries happen inside one flush, which `triggerFlush` serializes against the next. A retried write carries the state captured at `Persist`, never older than what is stored | 2 |
| Callback count | Results are computed first, then each connector's callback runs exactly once | 3 |

## Upgrade and rollback

No serialized format, config, or protocol change. Rolling back restores whole-batch failure.

## Observability

Each failed connector write logs one error line naming the connector and the attempt number
(`failed to persist connector state; retrying the rest of the batch without it in a new
transaction`). A non-attributable failure logs `failed to persist connector batch; nothing in this
attempt was committed and every connector in it is notified`. The flush debug line reports
`count` and `failed`. The runbook `docs/operations/connector-state-write-failures.md` is updated.

## Related

- #2930, #2925, #2932
- `docs/postmortems/20261007-persister-store-error-ignored.md`
- `docs/design-documents/20260723-source-ack-persist-ordering-fix.md`

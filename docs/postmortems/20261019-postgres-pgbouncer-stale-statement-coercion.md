# Postmortem: Postgres destination converts values silently behind PgBouncer

This is a blameless postmortem. It records what happened, why the review gate did not stop it, and what
changes. Change: postgres#433. Fix: postgres#436. Advisory: GHSA-rcwq-pcpp-gx9v (published). Here `postgres#N` is
an issue or PR in `ConduitIO/conduit-connector-postgres`, and a bare `#N` is one in this repository.

## Summary

postgres#367 found that the destination's cached prepared statements keep their pre-ALTER parameter types, so
after `ALTER TABLE ... ALTER COLUMN ... TYPE` Postgres converted values by assignment cast without an error
(5/2 stored as 3). postgres#422 fixed it by describing every statement (`describe_exec`), at a cost of one round
trip per batch. It shipped in v0.16.0.

Three days later postgres#433 made cached statements (`cache_statement`) the default again, with a guard: every
batch ends with a query that compares a fingerprint of the table's columns with the one the destination
learned, and fails the batch (rolling it back) if they differ. The guard is correct for the statements of the
destination's own connection. It does not know about statements it did not prepare.

PgBouncer with `max_prepared_statements` above 0 shares prepared statements between its clients, and pgx names
a statement after a hash of its SQL, so two destinations writing one table ask for the same name. A destination
that connects after an ALTER can be handed a statement that another client prepared before it, with the old
parameter types. Its fingerprint is learned fresh, from the new schema, so the guard passes. The result is
postgres#367 again, through the pooler: 5/2 stored as 3, 5.5 stored as 5, in 40 of 40 new connections. The records were
acknowledged.

**Severity: sev-0** (silent value changes, acked; invariant 6). **Status:** fixed. postgres#436 (merged 2026-10-20)
makes `describe_exec` the default again, released in v0.17.2; the guard stays for anyone who opts into the cached
modes. Affected:
`conduit-connector-postgres` v0.17.0 and v0.17.1. No Conduit release bundles them. No production report is
known.

## Impact

- **Who:** destinations on v0.17.0 or v0.17.1 with the default query mode (or `cache_statement` /
  `cache_describe` set explicitly), connected through PgBouncer with `max_prepared_statements > 0` or another
  pooler that shares prepared statements between clients. Only PgBouncer 1.26 was tested.
- **What it takes:** a column type change on a table that the pool's server connections had already prepared an
  INSERT for, and a destination that connects or reconnects afterwards.
- **What happens:** values are converted to the new type by assignment cast (rounded, truncated), stored and
  acknowledged. The destination logs nothing.
- **Direct connections** are not affected; the guard covers them.
- **`max_prepared_statements = 0`** does not convert values; batches fail with SQLSTATE 26000 or 42P05 instead
  (a separate availability bug in v0.17.0/v0.17.1, retried in v0.17.2).
- **Blast radius in Conduit:** none released. Conduit v0.20.x bundles postgres v0.14.4. Conduit `main` has bundled
  v0.17.x since 2026-10-19 (#3199, then #3213), unreleased.
- **Detection after the fact:** none from the connector. Users have to match DDL history against rows written
  after it (see the advisory).

## How it was detected

After the catch-up on 2026-10-20, the release captain found postgres#433 merged with no review recorded and sent
it to a fresh-context post-merge review. The review confirmed the guard for one connection (same transaction,
race probes refused, fingerprints match `describe_exec`) and raised four SHOULDs, among them that PgBouncer with
`max_prepared_statements=0` failed batches. postgres#436 was opened for those. While reviewing #436, the reviewer
asked what happens when a statement crosses clients in the pooler, and wrote `TestP436_CrossClientStale`: old
clients prepare through the pool, the column changes type, new clients write. With `max_prepared_statements=200`
every new client stored the wrong value.

## Timeline (UTC)

- **2026-10-13:** postgres#367 is filed (cached statements keep pre-ALTER types).
- **2026-10-16 21:27:** postgres#422 merges: `describe_exec` by default. Ships in v0.16.0 (2026-10-17 14:46).
- **2026-10-15 to 2026-10-19:** the coordinating session is at its usage limit; work continues in local-model
  sessions that do not write to the decision log.
- **2026-10-19 05:39:** postgres#433 opens: `cache_statement` by default, plus the fingerprint guard. Its body
  declares Tier 1 and has a design doc, a failure-mode analysis and a self-review. It does not say that a
  fresh-context review and sign-off are needed, and its failure-mode analysis has no entry for poolers.
- **2026-10-19 05:49:** postgres#433 merges, 10 minutes after opening, with no review and no comment.
- **2026-10-19 05:58:** v0.17.0 is published with it.
- **2026-10-19 23:16:** v0.17.1 is published (postgres#434, an unrelated source change), still with the default.
- **2026-10-20 00:45:** post-merge review of postgres#433: guard holds, four SHOULDs, "no review recorded".
- **2026-10-20 01:22:** postgres#436 opens with the SHOULD fixes. Its body names the cross-client case as an
  untested open question.
- **2026-10-20 01:33:** the cross-client probe confirms silent conversion behind PgBouncer with
  `max_prepared_statements > 0`. Declared sev-0.
- **2026-10-20 01:37:** DeVaris decides: revert the default to `describe_exec` in v0.17.2, keep the guard and
  the #436 fixes, draft an advisory, write this postmortem.

## Why it wasn't caught

- **The Tier 1 gate did not run.** CLAUDE.md requires a fresh-context review and DeVaris's sign-off for every
  Tier 1 change. postgres#433 declared Tier 1 and merged in 10 minutes with neither. The post-merge review, done
  later, is the one that found the pooler question. Nothing in CI or branch protection checks that a review
  happened; the rule lived only in the PR template and in the sessions that follow it.
- **The guard was reasoned about per connection.** Its proof (the INSERT's `RowExclusiveLock` against ALTER's
  `AccessExclusiveLock`) is sound for the statements that connection prepared. The design did not ask who else
  can prepare a statement with the same name. The failure-mode analysis listed crash mid-ALTER, concurrent
  ALTER, missing privileges, guard failure and churn, but not "the statement came from somewhere else".
- **The connector's tests have no pooler.** Every integration test connects straight to Postgres. The
  #367 regression test passes with the guard, as it should: it uses one connection.
- **Throughput pressure set the default.** The motivation was the 15% to 35% cost of `describe_exec`. A
  correctness fix was traded back for speed three days after it shipped. CLAUDE.md asks for a design doc and
  explicit approval before trading a correctness guarantee for throughput. There was a design doc, but no
  approval was recorded.
- **The performance claim behind it was not what it said.** The README presented a benchi run of unguarded
  cached statements as the cost the guarded default avoids. The guarded default itself was measured only with a
  go-bench microbenchmark.

## What went well

- The post-merge review was started because the review was missing, not because something broke.
- The reviewer stated the cross-client case as an open question in the #436 body instead of assuming it away,
  then tested it the same hour.
- The workaround is one URL parameter, and the pre-#433 behavior (`describe_exec`) was already proven.

## Fix

postgres#436, for v0.17.2:

- `describe_exec` is the default again. The guard stays for `cache_statement` and `cache_describe`, and the docs
  say those modes are safe only on a direct connection or behind a pooler that does not share prepared
  statements.
- A batch that failed mid-send (an encode error after the connection was closed) now reconnects and retries
  instead of failing every later write.
- SQLSTATE 26000 and 42P05 (lost or duplicate prepared statements) are retried within the existing bound.
- A mode set in a service file is respected.
- Guard fires and retries are logged at Info with counters.

## New automated checks

- **PgBouncer in the connector's test stack** (postgres#436, merged): a
  transaction-mode PgBouncer with prepared-statement tracking and two server connections, so a statement
  prepared by one client is handed to another.
- **Cross-client regression test** (`destination_pooler_integration_test.go`, postgres#436): old clients
  prepare, the column changes type, new clients write; every stored value must be exact or refused. It fails on
  v0.17.1 and passes on v0.17.2.
- **Tier 1 review recorded** (proposed, not built yet; see action item 9 in
  `20261021-data-loss-and-process-failures.md`):
  a required check that fails a PR labelled or declared Tier 1 until a review from a different session or a
  sign-off from DeVaris is on the PR.

## Follow-ups

- Mark v0.17.0 and v0.17.1 as affected in their release notes if that is not done yet.
- Bump Conduit `main` (it pins v0.17.2 as of 2026-10-21; take the latest fixed version) before v0.21.0.
- Run the `pg-guarded` benchi arm before anyone proposes the cached default again, and require a design doc that
  covers poolers. A fingerprint embedded in the statement's SQL (so the statement name changes with the schema)
  is being prototyped as a separate draft.
- postgres#435: a float written to an integer column is truncated in the binary query modes, `describe_exec`
  included. Separate bug, still open.
- Conduit v0.20.x bundles postgres v0.14.4, which has postgres#367 (no pooler needed). Decide whether to bump
  the built-in connector on the release branch or document it as a known issue.

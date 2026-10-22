# Postmortem: data-loss incidents and the process failures behind them (Oct 2026)

This is a blameless postmortem. It covers the data-loss and silent-corruption incidents found in the October 2026
review of Conduit and its connectors, the process failures that let them through, the root causes they share, and
the automated checks that follow. It folds in the earlier process draft and links to the postmortems that have their
own file. Here `postgres#N`, `s3#N` and `mysql#N` are issues or PRs in `ConduitIO/conduit-connector-<name>`, and a
bare `#N` is one in this repository.

Severity of every incident below is **sev-0** under CLAUDE.md (a path that can drop, duplicate, or silently change a
record). No production report is known for any of them. That is partly luck: the affected releases were short-lived
or unreleased in Conduit.

## Summary

Between 2026-10-15 and 2026-10-21 we found and fixed, or are fixing, seven data-integrity problems:

| # | Incident | Where | Status |
| --- | --- | --- | --- |
| 1 | Values silently converted behind PgBouncer | postgres destination v0.17.0, v0.17.1 | fixed in v0.17.2; GHSA-rcwq-pcpp-gx9v published |
| 2 | S3 exact resume loses and duplicates records | s3 source v0.11.0, v0.11.1 | fixed in v0.11.2; GHSA-gxm3-5pgq-2p8v published |
| 3 | Silent gap after a replication slot is dropped or recreated | postgres source v0.17.2 and earlier, v0.14.x | missing slot fixed in v0.17.3, v0.17.4, v0.14.6 |
| 4 | Same-name slot replacement, WAL history change, snapshot restore | postgres source | postgres#442, postgres#447 open; restore on one timeline is a known gap |
| 5 | Connector delete rollback loses or replays positions | Conduit engine | fixed in #3241 (closes #3137) |
| 6 | Shallow `Record.Clone` | `conduit-commons`, engine fan-out | fixed in commons#338; own postmortem |
| 7 | Migrated Kafka sinks skip records at cutover | `conduit migrate kafka-connect` | fixed in #3236 (closes #3089), before any release |

Separately, four process failures let several of these reach a release or `main`: seven PRs merged with required
checks failing, a stale rebase that reverted a fix, Tier 1 PRs merged without review or with review blockers open,
and a release cut without a fix it was meant to carry.

Incidents 1, 2 and 6, and the MySQL and Avro cases, have their own postmortems, which this document summarizes
and does not repeat: `20261019-postgres-pgbouncer-stale-statement-coercion.md`,
`20261019-s3-exact-resume-record-loss.md`, `20261015-record-clone-shares-nested-data.md`,
`20261015-mysql-cdc-second-resume-loss.md`, `20261015-avro-encoder-cache-collision.md`. Where the same cause shows
up in more than one, that is the point of putting them together.

## Incidents

### 1. Postgres destination converts values silently behind PgBouncer

postgres#433 made cached prepared statements the default again three days after postgres#422 had switched to
describing every statement, because describing costs 15% to 35% of throughput. It added a guard that compares a
fingerprint of the table's columns at the end of each batch. The guard is correct for statements the destination's
own connection prepared. PgBouncer with `max_prepared_statements > 0` shares statements between clients, so a new
destination can be handed a statement another client prepared before an `ALTER COLUMN TYPE`. Its fingerprint is
learned fresh and passes. Values are rounded or truncated, stored and acknowledged (`5/2` stored as `3`). Violates
invariant 6.

- **Window:** postgres#433 merged 2026-10-19 05:49 UTC, ten minutes after it opened, with no review and no comment.
  v0.17.0 followed nine minutes later. Fixed by postgres#436 (merged 2026-10-20 02:11), `describe_exec` default
  again, in v0.17.2.
- **Reached Conduit `main`** through #3199 and #3213, both of which merged with required checks failing (see
  "Process failures"). Not in any Conduit release (v0.20.3 and v0.20.4 bundle postgres v0.14.4;
  v0.20.5 bundles v0.14.5 and commons v0.7.2).
- **Detail:** `20261019-postgres-pgbouncer-stale-statement-coercion.md`; advisory GHSA-rcwq-pcpp-gx9v.

### 2. S3 exact resume ships with the review's two record-loss blockers

A Tier 1 review of the exact-resume stack (s3#1057, s3#1059, s3#1060, s3#1061) returned "changes needed" on
2026-10-15 with two blockers: evicting a large candidate from the bounded settled heap let a smaller one past it,
so the evicted object was never read again; and a tracked multipart upload was matched by key and second, so on AWS
the key's final version was lost and on MinIO a version was redelivered on every poll. The coordinating session hit
its usage limit an hour later and the fix lane stopped with it. The four PRs merged on 2026-10-16 at the heads the
review had rejected, with no review on any of them. v0.11.0 and v0.11.1 shipped with both blockers.

- **Detected** by a catch-up comparison of merged heads against the review log on 2026-10-19; the review's property
  test, re-run with 300 seeds instead of the PR's 20, failed on the same 8 seeds.
- **Fixed** in s3#1070 (merged 2026-10-20), v0.11.2. Bumped into Conduit `main` through #3209, which itself merged
  with a required check failing.
- **Detail:** `20261019-s3-exact-resume-record-loss.md`; advisory GHSA-gxm3-5pgq-2p8v.

### 3. Postgres source silently skips changes after a replication slot is dropped

A source that resumed from a saved position after its replication slot was gone created a new slot at the current
WAL, logged a WARN, and moved on. Every change between the saved LSN and the new slot was skipped, with no error
(invariant 3). Reachable after a manual `pg_drop_replication_slot`, after `logrepl.autoCleanup` ran while the
position was kept, and after an invalidated slot was dropped.

- **Fix:** postgres#441 refuses to open with `postgres.source.slot_wal_gap` (missing slot) or
  `postgres.source.slot_invalidated`, before creating anything. `logrepl.allowWALGap` is an opt-in that is documented
  as data loss. Merged 2026-10-21 19:18, v0.17.3. postgres#445 (v0.17.4) warns when the opt-in recreates a slot
  dropped after the check. The v0.14.x line has it through postgres#444 (v0.14.6).
- **Affected:** every version before v0.17.3 and v0.14.6; I did not check how far back the behaviour goes. Conduit
  `release/v0.20.x` pins v0.14.5 and `main` pins v0.17.2 at the time of writing, so neither has the fix yet.
- **How a fix came to ship with a gap:** #441's description says that a slot dropped _and recreated under the same
  name_ between runs is "not detected", calls this a documented limitation in the README, and defers it to a v0.22
  design item (postgres#442). That is incident 4, and the deferral is a process failure of its own (root cause 7).

### 4. Same-name slot replacement, WAL history change, snapshot restore (in progress)

A slot dropped and recreated under the same name, between runs or after a failover or restore, looks exactly like a
healthy idle slot that is past the saved position. The resume skips everything in between. postgres#447 (open,
Tier 1, v0.17.5) closes it with a slot identity in the position (authenticated beacons written into the WAL), a
check of the server's system identifier and timeline, and tests for each. As of this writing:

- #447 has had a fresh-context review in a separate session (three blockers fixed, each with a failing-before test),
  but there is no review on the PR on GitHub, and it is not merged.
- **One case stays open:** a restore from a volume snapshot on the same timeline whose new WAL has already passed
  the saved position, with the restored slot at or below it. #447 documents it and tells users to re-snapshot after
  restores. By the rule this postmortem adds ("Documented limitation is never acceptable for data loss"), that is
  not a resolution; the external-state suite (below) carries it as an expected failure so it cannot be forgotten.
- Affected today: any v0.17.x or v0.14.x source whose slot was replaced, whose database was restored, or whose
  cluster failed over to a standby without a synchronized slot.

### 5. Connector delete rollback loses or replays positions

`ConnectorOrchestrator.Delete` ran inside a store transaction. If a later step failed, the rollback called
`connectors.Create(...)`, which put a fresh in-memory `Instance` in place of the live one: no `State` (source
position), no `LastActiveConfig`. The store kept the right record; the running process did not. Until a restart a
restarted pipeline re-read its source from the beginning (duplicates), and when the plugin's delete hook had already
released a resource the position depends on, such as a replication slot, the source would silently skip instead.
The provisioning apply rollback had the same shape. Invariant 2.

- **Filed** 2026-10-15 as #3137 (found while working on #3058; the code had been there since the rollback was
  written). **Fixed** by #3241 (merged 2026-10-21 00:42): the same instance is restored, and if the plugin's delete
  hook has run, the next start is refused with `connector.delete_hook_ran` (fail closed).
- #3241 declares Tier 1 and had no GitHub review on it when it merged. It was reviewed in a separate session; the
  review record is in the decision log, not on the PR (same gap as incident 2).

### 6. Shallow `Record.Clone`

`opencdc.StructuredData.Clone` copied nested `map[string]any` and kept every other nested value by reference.
Fan-out, the `clone` and `split` processors and the inspector rely on independent copies. An in-place nested edit in
one branch changed every copy. See `20261015-record-clone-shares-nested-data.md` (commons#336, fixed in commons#338,
engine tracking in #3151, engine regression tests in #3237). Conduit v0.20.3 and v0.20.4 were cut without the patch
line fix, which is process failure D below. `release/v0.20.x` now pins commons v0.7.2.

### 7. Migrated Kafka sinks skip records at cutover

`conduit migrate kafka-connect` emitted a `builtin:kafka` source for a sink with no `groupID` and no
`readFromBeginning`. The Kafka source then joins a random group and starts at the end of each partition, so a migrated
sink skips every record written before the pipeline starts. Kafka Connect reads in `connect-<name>` with
`auto.offset.reset=earliest`. The issue (#3089) was filed as "model Kafka Connect's own client defaults before any
sink family ships" during the review of #3081; #3236 (merged 2026-10-20) carries both defaults into the emitted
pipeline and adds a cutover runbook. Never released. It is in this list because the premise (the Kafka client
defaults are Kafka Connect's defaults) came from a comment and not from reading Connect's worker, which is root cause
5.

## Process failures

### A. Seven PRs merged with required checks failing

Required contexts on `main`: `test`, `golangci-lint`, `validate-generated-files`, `build (linux/386)`,
`docker build`, `tests/chaos (race, x3)`, `bundle-e2e`, `rag-e2e`, `coverage (engine ratchet)`. The table lists the
required contexts whose latest run on the merged head is a failure, from the check-runs API at the time of writing.
All seven merged by admin merge (`enforce_admins` is off). I did not re-derive which runs were red at the instant of
each merge; for #3213 no run can have finished.

| PR | Merged (UTC) | Required contexts failing on the merged head |
| --- | --- | --- |
| #3174 | 10-16 20:17 | `golangci-lint` |
| #3179 | 10-17 21:24 | `test`, `validate-generated-files` |
| #3199 | 10-19 05:50 | `test`, `validate-generated-files` (bundled postgres v0.17.0, incident 1) |
| #3192 | 10-19 22:39 | `test` |
| #3209 | 10-19 22:46 | `test` (bundled s3 v0.11.x, incident 2) |
| #3211 | 10-19 22:39 | `test`, `golangci-lint`, `build (linux/386)`, `coverage (engine ratchet)` |
| #3213 | 10-19 22:52 | `test`, `validate-generated-files`; merged 10 seconds after it opened (bundled postgres v0.17.1) |

CLAUDE.md already says "Never `--admin`-bypass a failing or pending check to merge". The rule was not enforced by
anything, and the merging session was one that did not carry the context behind the rule.

### B. A stale rebase reverted a fix

PR #3208 (merged 2026-10-19 22:38) bumped `conduit-connector-sdk`, `conduit-commons` and `conduit-connector-protocol`
to fix a `tomb.Go` race. #3192, merged one minute later, had been rebased onto an older `main`, and its squash
reverted those `go.mod` and `go.sum` lines (commons v0.11.0 to v0.10.0, protocol v0.11.1 to v0.11.0, sdk to v0.17.0).
Nothing failed, because CI on #3192 ran against the older base. It was noticed and restored in #3229 (merged
2026-10-20 23:30), about 25 hours later. The race fix was off `main` for that time.

### C. Tier 1 PRs merged without review, or with review blockers open

Verified on GitHub for postgres#433 and mysql#232: zero reviews and zero comments at merge.

- **S3 resume stack** (s3#1057, #1059, #1060, #1061): merged 2026-10-16 at the heads the review rejected (incident 2).
- **mysql#232**: merged 2026-10-16 20:48 at a head whose round-5 review had a blocker (a false halt on restart
  after a DDL, and a halt on the first restart after upgrading from an older position). Shipped in mysql v0.5.0 and
  v0.5.1; fixed in mysql#247 and mysql#248 (v0.5.2).
- **postgres#433**: declared Tier 1, merged in ten minutes (incident 1).

Common to all three: the review lived in a session's notes, not on the PR, and a session that opened the PR later saw
green CI and a Tier 1 label with nothing on it saying "changes needed". Branch protection requires CI contexts, not
review; `enforce_admins` is off, so an admin merge needs no approval; and lanes that stopped at the usage limit left
no marker, so an unfinished PR looked the same as a finished one.

### D. A release cut without a fix it was meant to carry

`conduit-commons` v0.7.2 had not been tagged. Its fixes, among them commons#339 (the `Record.Clone` fix), sat on
`release/v0.7.x`. Conduit v0.20.3 (2026-10-16 01:03) and v0.20.4 (03:21) were cut on commons v0.7.1. The v0.20.3 notes
draft led with the fix; the published notes were the generated changelog only, so no published note claimed it.

## What went well

- The review itself: seven real bugs found before any known user report, most with a failing-before test.
- Reviews found the S3 blockers, the PgBouncer case and the #447 blockers with repros, seeds and proposed patches.
- Fail-closed was chosen where it mattered: `connector.delete_hook_ran`, `slot_wal_gap`, `slot_invalidated`.
- Several fixes shipped with the mutation list that proves their tests bite (postgres#447).
- The catch-up survey after the session gap (merged heads and tags against the log) found C and D quickly.

## Timeline (UTC, abridged)

- **10-13:** postgres#367 filed (cached statements keep pre-ALTER types).
- **10-15 18:36:** #3137 filed. **19:32:** #3151 filed. **20:02:** s3 stack review: changes needed. **21:03:** the coordinating
  session reaches its usage limit; lanes stop; no marker on any PR.
- **10-16 20:17 to 22:27:** #3174 merges red; mysql#232 merges with a blocker open; the s3 stack merges at rejected
  heads.
- **10-17 21:24:** #3179 merges red.
- **10-19 05:49:** postgres#433 merges (10 minutes, no review). **05:50:** #3199 merges red and bundles it.
  **22:38 to 22:52:** #3208, #3192 (reverts #3208), #3211, #3209, #3213 merge; three are red and one is 10 seconds old.
  **21:00:** catch-up survey finds the S3 stack unfixed.
- **10-20 01:33:** PgBouncer coercion confirmed. **02:11:** postgres#436 merges. **03:34:** s3#1070 merges.
  **18:49:** postgres#441 opens; it documents the same-name-slot gap as a limitation. **22:39:** postgres#442 filed.
  **23:30:** #3229 restores the SDK bump.
- **10-21 00:42:** #3241 (delete rollback) merges. **19:18:** postgres#441 merges (v0.17.3). **22:17 and 22:22:**
  v0.17.4 and v0.14.6.

## Root causes

1. **Fail-open defaults chosen for availability at decision points.** A missing slot was re-created and logged
   (postgres#441's bug). A cached statement was trusted because the guard passed (postgres#433). A rollback made a
   fresh connector so the pipeline could keep running (#3137). A Kafka source with no group starts at the end
   (#3089). In each, the code reached a point where it could not tell whether continuing was safe, and chose to
   continue. The consequence was silent loss or conversion, not a visible failure. The correct default at such a point
   is to stop with a coded error and make the unsafe path an explicit, loudly named opt-in (`allowWALGap` is the
   model).
2. **No catalogue of external-state failures.** Nobody had a list of what can happen to the system behind a
   connector while Conduit is not looking: slot dropped, recreated, invalidated, restored from a snapshot, failed over;
   privilege revoked; connection half-open; statements shared by a pooler. Each of the Postgres incidents is on that
   list. Because there was no list, each was found one at a time, by luck or by an external reviewer.
3. **Reviews scoped to the diff, not to existing behaviour on the touched path.** The postgres#433 review of the
   guard was correct for the code it read. The question that mattered was about code it did not change: who else can
   prepare a statement with that name. Same for #3137, where the bug predated the PR that found it.
4. **Tests that pass with the guard removed.** The #367 regression test passes with and without the pooler guard
   because it uses one connection. s3's per-PR property test ran 20 seeds and the first failing seed was 82. In
   several Tier 1 PRs the only evidence that a guard is tested is a mutation list a reviewer ran by hand (#447).
   No CI check removes a guard and asks whether anything goes red.
5. **Design premises taken from comments instead of verified against a real server.** The PgBouncer premise was
   "statements are per connection". The Kafka client defaults premise was "Connect uses the Kafka defaults". The
   slot premise was "a slot LSN past the saved LSN means a gap", which the #441 author tested and found false on an
   idle healthy slot. In the cases that went wrong, the premise was never run against the real system.
6. **Gates that can be bypassed.** `enforce_admins` is off on `main` and on `release/v0.20.x`, so a failing required
   check, a missing review and a Tier 1 label are all advisory to a session with admin rights. The same account
   opens every PR, so GitHub's own review requirement cannot be met. A rule that depends on every session knowing
   it fails when the session that knows it is not running.
7. **The orchestrator offering "document and defer" for a data-loss gap.** postgres#441 shipped a fix for one way to
   skip records and, for the neighbouring way, a README paragraph and a v0.22 design item. That is a reasonable
   engineering split for a feature. It is not one for data loss, and it was presented as one. The failure is the
   missing rule that "document and defer" is not an available option for loss or exposure.

Contributing factors, not root causes: the coordinating session's usage limit with no hand-off marker, throughput
pressure on the PgBouncer default, and a release process that cut from what had merged instead of from a list of what
had to be in it.

## Action items

Every item is an automated check or a setting that makes one enforceable. "Owner" is who makes it real; Claude opens
the PR, DeVaris approves and merges. A status of "no PR yet" is a gap in this postmortem, not an oversight in the
table.

| # | Check | Enforces | Fixes | Owner | PR |
| --- | --- | --- | --- | --- | --- |
| 1 | External-state chaos suite (postgres) | Per case: no silent loss; every row delivered or a coded start failure. Slot dropped mid-run and between runs, invalidated, half-open connection, privilege revoked, snapshot restore. Same-name recreation and failover are listed as pending on postgres#447 and cannot stay pending once it merges. | RC 1, 2, 7 | Claude (build), DeVaris (approve) | [postgres#451](https://github.com/ConduitIO/conduit-connector-postgres/pull/451) |
| 2 | Guard mutation gate, `conduit` | Removing any `// guard:` marked guard in `pkg/connector`, `pkg/lifecycle`, `pkg/lifecycle-poc` turns tests red, or the PR fails. Survivors are baselined, shrink only. | RC 4 | Claude, DeVaris | [#3262](https://github.com/ConduitIO/conduit/pull/3262) |
| 3 | Guard mutation gate, postgres | The same for `source/logrepl`. The #447 guards (beacon, slot identity, WAL history) are marked in a follow-up after #447 merges. | RC 4 | Claude, DeVaris | [postgres#452](https://github.com/ConduitIO/conduit-connector-postgres/pull/452) |
| 4 | CODEOWNERS for Tier 1 paths and gates | Takes effect only with item 8. Names DeVaris alone on data-path code, the chaos and upgrade suites, `.github/`, `CLAUDE.md`. | RC 6 | DeVaris | [#3259](https://github.com/ConduitIO/conduit/pull/3259) |
| 5 | Process rules in CLAUDE.md and the PR template | Not a check by itself. The template makes tier, the touched-path review, the premise citations and the guard marks required fields, which the `tier1-review` check (item 9) reads. | RC 3, 5, 7 | Claude, DeVaris | [#3263](https://github.com/ConduitIO/conduit/pull/3263) |
| 7 | `go.mod` downgrade guard | Fails a PR when the go.mod it would produce (the PR merged into the base tip) lowers, against the base tip, any direct requirement or any `conduit-connector-sdk`, `conduit-commons`, `conduit-processor-sdk`, `conduit-connector-protocol` or `conduit-connector-*` version (direct or indirect), in the root, `tools/` or any nested go.mod, including through a `replace`. Waived by a `deps-downgrade: <reason>` line in the PR body. A push-to-`main` check compares `HEAD^1` with `HEAD`, fails the main build and opens an issue, so a merge that skipped PR CI is still caught. Would have stopped B. | B | Claude | [#3265](https://github.com/ConduitIO/conduit/pull/3265) |
| 8 | Branch protection settings | Admin bypass off or limited, code-owner review on, stale approvals dismissed, the required checks list below. | RC 6, A | **DeVaris** | none (settings) |
| 9 | `tier1-review` required check | Commit status `tier1-review`. A PR must declare a tier, and a PR that touches the CODEOWNERS Tier 1 paths is Tier 1 whatever it declares. A Tier 1 PR passes only with `Review: approve @ <head sha>` and a signer's `Sign-off @ <head sha>`, from commenters who are OWNER, MEMBER or COLLABORATOR; the newest verdict per author counts, edits and deletions included; any unanswered `changes-needed` blocks; a push clears both. Design below. | C | Claude (build), DeVaris | [#3266](https://github.com/ConduitIO/conduit/pull/3266) |
| 10 | Release gate: fixes the release must contain | `.github/release-fixes.txt` on the release branch (one `module, commit, note` entry per line). The `release-check` job in `release.yml`, which both publishing jobs need, requires every listed commit to be an ancestor of the tag of that module's version in `go.mod`; a patch release needs at least one fix newer than the previous tag; a `replace` to a fork or local path fails unless waived. | D | Claude | [#3267](https://github.com/ConduitIO/conduit/pull/3267) |
| 11 | Connector release gate: open findings | Fails a connector tag while a PR merged since the last tag carries `do-not-merge` or an unanswered `changes-needed`. | C | Claude | [#3268](https://github.com/ConduitIO/conduit/pull/3268) |
| 12 | Release notes required on release branches | A patch release fails if its body is only the generated changelog. | D | Claude | [#3269](https://github.com/ConduitIO/conduit/pull/3269) |
| 13 | Pooler in the connector test stacks | A PgBouncer service in `test/docker-compose.yml`; the cross-client regression test from postgres#436. | RC 2, 4 | Claude | merged in postgres#436 |

Root causes and their automated cover, so the gaps are visible:

- RC 1 (fail-open defaults): rule in CLAUDE.md (item 5) plus the external-state suite (item 1). No static check can
  find a fail-open decision point; this is review work.
- RC 2 (no catalogue): the suite is the catalogue. New connectors need their own; the rule is in CLAUDE.md.
- RC 3 (diff-scoped review): rule and PR template field only. The `tier1-review` check (item 9) can require the
  field, not judge it. This is the weakest cover.
- RC 4 (survivable guards): items 2 and 3.
- RC 5 (premises from comments): rule and template field only, same limit as RC 3.
- RC 6 (bypassable gates): items 4, 8, 9.
- RC 7 (document and defer): rule only. The suite's expected-failure state is the mechanism that keeps a deferred gap
  red.

Items 7 and 9 to 12 have PRs (above). Items 11 and 12 are stacked on 9 and 10, and item 11 is not yet wired
into any connector repository.

### Design of items 9 to 12

1. **`tier1-review`, as a commit status.** Runs from `pull_request_target` and `issue_comment` using the default
   branch's script; no PR code is checked out.
   - A PR is Tier 1 if its body declares "Tier 1", it carries the `tier-1` label, or it touches a CODEOWNERS Tier 1
     path. The path floor wins over the declaration. A PR with no declaration fails.
   - A Tier 1 PR passes only if comments naming the **current head SHA** exist for both a review
     (`Review: approve @ <sha>`) and a sign-off (`Sign-off @ <sha>`, from DeVaris). Only comments by an OWNER, MEMBER
     or COLLABORATOR (or an allowlist) count, and only the newest verdict per author. Any `Review: changes-needed`
     fails the check until a later `approve`. A push clears both, because the SHA no longer matches.
   - It uses comments, not the review API, because GitHub does not let an account approve its own PR and every PR
     here is opened from one account. With a second account (see Branch protection) it can read real reviews instead.
   - A `changes-needed` record also applies the `do-not-merge` label, which the check rejects.
   - **Limit:** a required status context can be set by any workflow in the same repository that has
     `statuses: write`, so the check can be spoofed by a workflow change. The follow-up is a GitHub App that
     posts a check run, which only that App can write.
2. **`.github/release-fixes.txt` per release branch.** For each planned patch, the fixes it must carry as module plus
   commit. `release-check` resolves each module's version in `go.mod` to its tag and checks every listed commit is an
   ancestor (`git merge-base --is-ancestor`) before anything is published. A patch release must list at least one
   fix newer than the previous tag, so a trivial old entry does not satisfy it. First entry for `release/v0.20.x`:
   commons v0.7.2 with the four fixes the v0.20.3 plan named.
3. **Connector release gate.** Before a connector tag, list the merged PRs since the last stable tag that carry
   `do-not-merge` or a `changes-needed` record without a later `approve`; fail if there are any.
4. **Release notes on release branches.** A patch release fails if its notes are only the generated changelog, or do
   not name each listed fix by commit prefix or a distinctive note.

Process changes that need no tooling: a session that cannot read the decision log does not merge or tag; a session
near its limit labels every PR it owns `do-not-merge` with a one-line status as its last action; and the catch-up
survey (compare merged heads and tags with the log) is the first step of every session after a gap.

## Branch protection (for DeVaris to apply or approve)

I changed no settings. Current state, read from the API on 2026-10-21: `main` requires 1 approving review, strict
status checks (9 contexts), conversation resolution, no force pushes, no deletions, `enforce_admins` off, code-owner
review off, stale-review dismissal off. `release/v0.20.x` is the same without `coverage (engine ratchet)`.
`release/0.10` has no protection.

Recommended for `main`:

| Setting | Value | Why |
| --- | --- | --- |
| Require a pull request, approving reviews | 1 | unchanged |
| Require review from Code Owners | **on** | makes item 4 real for Tier 1 paths |
| Dismiss stale approvals on new commits | **on** | an approval is for a head SHA |
| Require approval of the most recent push | **on** | the pusher cannot be the approver |
| Require status checks, strict | on (unchanged) | |
| Required contexts | the current nine, plus `govulncheck`; later `tier1-review` (item 9) | `govulncheck` runs on every PR and is blocking in CLAUDE.md but not required |
| Not required, and why | `bench-engine`, `external-state`, guard gates (path-filtered, cannot report on every PR); `migrate-harness` (decided: after 10 green runs); `value-correctness` (no green history yet) | a required check that does not report blocks every unrelated PR |
| Do not allow bypassing the settings (`enforce_admins`) | **on**, once the second account below exists | admin bypass is how A happened |
| Allow force pushes, deletions | off (unchanged) | |
| Require conversation resolution | on (unchanged) | |

Recommended for `release/**` (apply as one repository ruleset with the pattern `release/**`, not per-branch classic
rules, so the next release branch is covered from the day it is cut and `release/0.10` is not left open): the same
review, code-owner and bypass settings as `main`, and the required contexts that exist on the branch's own workflows.
Classic protection cannot take a wildcard through the REST API; a ruleset can.

The decision this needs from DeVaris: **GitHub never lets an account approve its own PR, and every PR is opened from
DeVaris's account.** With code-owner review on and `enforce_admins` on, no Tier 1 PR could merge. With
`enforce_admins` off, the review requirement is bypassed every time and means nothing. The two real options:

1. Open PRs from a separate machine account (or a GitHub App). DeVaris reviews as the code owner, merges, and
   `enforce_admins` goes on. This is the one that makes the rules real, and the only one I recommend.
2. Stay solo-account, keep `enforce_admins` off, and put the bypass in a ruleset with a bypass list of DeVaris only,
   "for pull requests" mode. That keeps the bypass visible and auditable, and is a smaller change, but the review
   is still advisory. Items 9 to 12 then carry the weight.

Until one of them is in place, items 4 and 8 are documentation. The same settings apply to
`conduit-connector-postgres`, `-s3`, `-mysql`, `-kafka`, `conduit-commons` and the SDKs, whose current contexts
(postgres: `golangci-lint`, `test`, `validate-generated-files`) are thinner than Conduit's.

## Fix

Each incident's fix is linked above. The process fixes are the action items. Nothing in this document is a code
change beyond what those PRs carry.

## Follow-ups

- postgres#447 merges, then the pending external-state cases are switched on, the #447 guards are marked, and
  the snapshot-restore expected failure is either closed or escalated to a decision with a fail-closed interim.
- Bump Conduit `main` to the fixed connector versions (postgres v0.17.4 and later, s3 v0.11.2, mysql v0.5.2) before
  v0.21.0; decide the same for the v0.20.x line (postgres v0.14.6).
- Merge items 7 and 9 to 12; backport the release gates (10, 12) to `release/v0.20.x` with a `release-fixes.txt`
  before v0.20.6; add the `connector-release-gate` job to each connector repository.

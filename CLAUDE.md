# CLAUDE.md — Conduit

Operational context for Claude Code sessions working on the ConduitIO org. Read this before
touching anything. This file is what a session needs to **act**. The strategy behind the
direction — competition, positioning, commercial line, SDK rationale — lives in `STRATEGY.md`
(gitignored, internal); read it only when doing strategy or positioning work.

## What we're doing

Reviving Conduit (<https://github.com/ConduitIO/conduit>) as the de facto Kafka Connect replacement:
broker-neutral, any-language plugins via gRPC + WASM, embeddable, agent-legible, with a
first-class Kafka Connect migration path and a lightweight state layer. The public plan is in
`ROADMAP.md` — check the current phase at the start of every session and ask which phase we're
executing if it's ambiguous.

## Project reality (read once, keep in mind)

- The project went dormant (~15 months, last v0.14.x nightly 2025-03, restarted 2026-06). The
  v0.15.0 nightly train is **already running**. Phase 0's release task is "cut v0.15.0 stable,"
  not "start v0.15.0."
- **Maintainer reality: solo (DeVaris) + Claude.** The PR process below is written for that,
  not for a large org. Where a gate assumes a "second human maintainer," the second human is
  DeVaris. Gates that require a team switch on by phase — see _Process maturity_ below. Do not
  pretend a review bar is met when it structurally can't be; say what was actually verified.

## Repo conventions (match what exists — do not invent parallel ones)

- **ADRs** live in `docs/architecture-decision-records/`, filename `YYYYMMDD-slug.md`, sections
  `# Title` → `## Summary` → `## Context` → `## Decision` → `## Consequences` → `## Related`.
  Immutable once merged (supersede with a new ADR; never edit). **Not** `docs/adr/`, **not**
  numbered `ADR-001`.
- **Design docs** live in `docs/design-documents/`, same `YYYYMMDD-slug.md` naming. **Not**
  `docs/design/`.
- **Contributing** guide is `CONTRIBUTING.md` (exists) — extend it, don't replace it.
- Existing package layout is documented in `docs/package_structure.md`; code guidelines in
  `docs/code_guidelines.md`. Read those before proposing structural changes.

## Repo map

| Repo | Purpose |
| --- | --- |
| `ConduitIO/conduit` | Core engine: pipeline orchestration, plugin runtime, HTTP/gRPC API, built-in UI |
| `ConduitIO/conduit-connector-protocol` | gRPC protocol between Conduit and connector plugins. **Breaking-change territory — flag loudly, version carefully** |
| `ConduitIO/conduit-connector-sdk` | Go SDK for building connectors (source + destination), acceptance test harness |
| `ConduitIO/conduit-processor-sdk` | Go SDK for processors; standalone processors compile to WASM (wazero runtime) |
| `ConduitIO/conduit-commons` | Shared types: records, schema, config |
| `ConduitIO/benchi` | Benchmarking framework — use for all performance claims |
| `ConduitIO/conduit-connector-*` | Individual connectors (Postgres, Kafka, S3, generator, file, log) |

New surfaces come online across Phases 1–3: connector/template registry, MCP server,
`libconduit` + language bindings, state layer (in-engine), fleet console, K8s operator,
Terraform provider.

## Data integrity invariants (non-negotiable)

Bugs here corrupt people's pipelines. Corruption-class bugs are sev-0: drop everything, fix,
postmortem, regression test.

1. **Never acknowledge a record upstream before it is durably handled downstream.** Ack
   propagation is end-to-end; no intermediate component may ack early for throughput. Any
   batching/buffering change must prove ack correctness under crash.
2. **Positions/offsets are monotonic and crash-safe.** A restart must never skip records
   (at-least-once) or corrupt position state. Position serialization changes require a versioned
   migration path and an upgrade test.
3. **At-least-once is the floor.** Any path that could drop a record without delivering it or
   routing it to a DLQ is a data-loss bug — including error, shutdown, and rebalance paths.
4. **Ordering guarantees are per-source-partition and documented.** Changes that could reorder
   records within a partition key require a design doc and explicit sign-off.
5. **State and checkpoint writes are atomic.** Torn writes on crash must be impossible
   (write-ahead + rename, or the store's transactional API). Every state feature ships with a
   kill-mid-write recovery test.
6. **Schema handling never silently mangles data.** Unknown fields, type mismatches, and drift
   follow the configured policy (halt/DLQ/evolve) — never silent coercion or truncation.
7. **Shutdown is graceful by default.** SIGTERM drains in-flight records and checkpoints before
   exit. `kill -9` at any instant must be recoverable without loss — and we test exactly that.

Where code upholds one of these, say so at the enforcement site:
`// Invariant 1: ack only after destination confirms durable write`.

## Architecture & design discipline (staff/principal bar)

We're building infrastructure people bet their data on. Design like it.

- **Design doc before code for anything non-trivial.** Non-trivial = touches the data path,
  changes a public contract (protocol, config schema, CLI, error codes, state format), adds a
  subsystem, or exceeds ~2 days. The doc goes in `docs/design-documents/` and must cover:
  problem, constraints, alternatives (≥2, with why they lost), failure modes, upgrade/rollback,
  observability. A design doc that doesn't enumerate failure modes is not done.
- **ADRs for decisions.** Every significant architectural decision gets an immutable ADR in
  `docs/architecture-decision-records/`. Future contributors should reconstruct _why_ without
  archaeology.
- **Invariants are documented and enforced** — in package docs, with assertions/tests, not
  comments alone. If you can't state the invariant, the design isn't finished.
- **Think in failure modes first.** For any data-path change, answer before writing code: crash
  mid-operation? network partition? duplicate delivery? out-of-order? malformed input? disk
  full? The happy path is the easy 20%.
- **Backward compatibility is a design input.** Serialized state, pipeline configs, connector
  protocol, and registry format must be readable by N+1 versions with an explicit deprecation
  policy (announce → warn → remove, minimum two minor versions).
- **No speculative generality.** Interfaces earn their existence with two real implementations
  or a design doc explaining why the seam matters. YAGNI violations are review blockers.
- **Simplicity is the review criterion.** If a reviewer can't explain the approach back in two
  sentences, it's too clever. Prefer boring, obvious code.

## Engineering conventions

- **Language:** Go (latest stable). Match existing code style; run golangci-lint (config is in
  `.golangci.yml`) before proposing a PR as done. Use the `Makefile` targets.
- **Errors are API:** every user-facing error gets a stable error code, the failing config path,
  and a suggested fix. Machine-actionable errors are a product feature (agents consume them).
- **CLI output:** every command supports `--json`. New commands ship with it, not after.
- **Commits/PRs:** conventional commits (`feat:`, `fix:`, `docs:`, `refactor:`). Small,
  reviewable PRs — split large work. PR descriptions state the roadmap item they advance.
- **Breaking changes:** connector protocol, pipeline config schema, and error codes are public
  contracts. Breaking changes require a versioning plan and migration notes in the PR.
- **Releases:** monthly release train. Changelogs from conventional commits; release notes a
  human would want to read. See `docs/releases.md`.
- **Performance:** claims require benchi runs, committed configs, reproducible results. Never
  assert numbers without a benchmark in the repo.
- **State layer discipline:** scope is set by ADR
  `20261008-state-layer-scope-windows-and-bounded-lateness.md`. Allowed: dedup with TTL,
  lookup/enrichment tables and stream-table joins, keyed upsert/merge, tumbling/sliding/session
  windows, standard aggregates, and event time with **bounded lateness only** (per partition, a
  window closes when max observed event time minus a fixed configured allowance passes its end;
  later records go to the DLQ/late-data output per policy, never silently dropped — invariant 6).
  State is partition-scoped (moves with partition claims), lives in the local embedded KV, and every
  state write commits atomically with the pipeline checkpoint (invariant 5). Read-only key lookups
  over the API/MCP are in scope; queries, scans and SQL are not. Excluded: global/distributed
  watermarks, a triggers framework, stream-stream joins, distributed snapshots, pluggable state
  backends, any expression/config DSL. If a design doc starts growing any excluded item, stop and
  flag it.
- **Docs move with code:** a feature PR without docs is incomplete. Update `conduit.io` docs
  source AND `llms.txt` when behavior changes — maintained together, always.

## Testing standards

The test suite is the product's warranty. **Baseline (live now):** unit + integration, race
detector on in CI, lint clean, conventional-commit check.

Data-path code additionally requires (see _Process maturity_ for when each becomes a hard gate):

- **Property-based tests** (rapid or gopter) for serialization, position handling, record
  transforms — round-trip, ordering, idempotency properties.
- **Chaos/fault-injection:** SIGKILL (not SIGTERM) mid-snapshot, mid-batch, mid-checkpoint;
  inject connector errors, slow destinations, network failures. Verify invariants 1–7 on
  recovery. Lives in `tests/chaos`.
- **Upgrade/downgrade:** run N, create pipelines with state, upgrade to N+1, verify resume.
  Serialized-state compat breaks are release blockers.
- **Soak:** long-running pipeline suite (24h) with memory/goroutine-leak detection.
- **Fuzzing** on every parser and protocol boundary: pipeline config, record payloads, protocol
  messages, registry manifests. Native Go fuzzing, CI (short) + scheduled (long).
- **Coverage floor** 80% on engine packages — a signal, not a goal; review _what's_ untested,
  especially error paths.
- **Acceptance tests define "connector":** the SDK acceptance suite is the compatibility
  contract. Expanding it is high-leverage; version it so authors know the bar they passed.
- **Benchmarks as regression gates:** benchi on reference pipelines; >10% throughput/latency
  regression fails the build unless waived in the PR with justification.
- **Every bug fix ships with the test that would have caught it.** No exceptions. Link the issue.

## Documentation & commenting standards

- **Godoc on every exported symbol** — say something the signature doesn't. Document behavior,
  invariants, error semantics, concurrency safety.
- **Package-level `doc.go`** for every package: purpose, core types, invariants, relationships
  to neighbors. A new contributor should navigate the engine by reading `doc.go` files alone.
- **Comment the why, not the what.** Non-obvious decisions, workarounds (with issue links),
  performance-motivated shapes, protocol quirks.
- **Invariant comments at enforcement sites** (see the invariant list above).
- **Examples that compile:** exported APIs get `Example*` functions run by `go test`.
- **Architecture docs stay current:** pipeline lifecycle, ack propagation, plugin runtime,
  state/checkpoint model, with in-repo mermaid diagrams. A PR that changes architectural
  behavior updates the doc in the same PR.
- **Runbooks for operators:** every alertable failure mode gets a symptom → diagnosis →
  remediation entry under `docs/operations/`.

## PR review process

Every PR is risk-tiered; the tier sets the bar and is declared in the PR description.

**Tier 1 — Data path** (record flow, ack/position/checkpoint logic, state layer, connector
protocol, serialization formats):

- Design doc or ADR linked (or explicit waiver for small fixes).
- **Human sign-off always required** — AI-authored or not, no Tier 1 change merges on automated
  review alone. Solo reality: that human is DeVaris, reviewing with fresh context in a separate
  session from the author. Author session never approves its own PR.
- Chaos + upgrade tests updated or explicitly justified as unaffected.
- PR description includes a **failure-mode analysis**: what could this break, which metric/alert
  would show it, how do we roll back.
- **The review examines existing behaviour on the touched path, not only the diff.** The reviewer
  lists the callers, defaults and fail-open branches already on the path and says what each does
  with the change. A review that read only the changed lines has not reviewed the change.
- **Premises about an external system are verified against a real server and cited** in the design
  doc or PR (version, command, output). A comment, a README or the author's memory is not evidence.
- **Data-path guards are marked `// guard: <name>`** next to the invariant comment, and the guard
  mutation gate must kill the mutant that removes each one (see _Process maturity_).
- **The review verdict is on the PR, at the head SHA** (`Review: approve|changes-needed @ <sha>`).
  A verdict that lives only in a session's notes does not exist for the next session. A
  `changes-needed` verdict means the PR is not merged until a later `approve`, even if CI is green.
- Merges freeze 48h before a release cut.

**Tier 2 — Features** (connectors, processors, CLI, UI, registry, docs-adjacent):

- One reviewer approval; acceptance/integration tests green; new surfaces have `--json` + error
  codes; docs updated in the same PR.

**Tier 3 — Chore** (deps, CI, typos, comments): one approval, green CI.

**Automated gates:** lint, race detector, unit + integration, conventional-commit check — all live
and blocking. The engine coverage floor is **live as a ratchet** (see Process maturity below); the
benchi regression gate is **live and blocking** (see Process maturity below). Dependency vulnerability scanning
(govulncheck) is **live and blocking as a ratchet**: it fails a PR that makes a NEW vulnerability
reachable, while the 4 remaining pre-existing findings are held in a justified baseline — see Process
maturity below.

**Review conduct:** review the tests first — if they wouldn't catch the bug this PR could
introduce, request changes before reading the implementation. Check error paths line by line
(that's where data loss lives). Verify invariant comments at touched enforcement sites still
hold. "Looks good" is not a review; state what you verified.

**AI-authored code (most of ours):** Claude does an adversarial self-review pass before opening
any PR — re-read the diff hunting for the bug, specifically error paths, concurrency, resource
cleanup, and the data-integrity invariants. Findings and resolutions go in the PR description.
Self-review is not review; a separate session (or human) reviews with fresh context. Any
uncertainty about behavior under failure is stated, not assumed away.

**Regression response:** any regression that reaches a release triggers a blameless postmortem
in `docs/postmortems/` and produces at least one new automated check.

### Rules from the October 2026 data-loss incidents

From `docs/postmortems/20261021-data-loss-and-process-failures.md`. These hold for every repo in the
org.

- **"Documented limitation" is never an acceptable resolution for data loss or exposure.** A gap
  that can drop, duplicate or silently change a record, or expose data, is fixed, or the path is
  made to fail closed with a coded error, or the release that carries it is blocked. A README
  paragraph, a design item for a later version and an opt-in flag that is off by default are not
  resolutions. A session may not offer "document and defer" for such a gap. Only DeVaris can accept
  a deferral, in writing on the PR, and it still needs a fail-closed interim.
- **Fail closed at decision points.** Where code cannot tell whether continuing is safe (a missing or
  replaced external resource, a state it did not create, a guard it cannot evaluate), the default is
  to stop with a coded error, never to recreate, assume or log a warning and go on. The unsafe path
  is an explicit, loudly named opt-in (`logrepl.allowWALGap` is the model). Choosing availability
  over safety at such a point needs a design doc and DeVaris's approval.
- **Catalogue external-state failures.** A connector design doc lists what can happen to the system
  behind it while Conduit is not looking (resource dropped, recreated, invalidated, restored from a
  snapshot, failed over; privilege revoked; connection half-open; shared by a pooler) and points to
  a test for each. A case that cannot be fixed yet stays in the suite as an expected failure.
- **A guard that no test needs is not a guard.** Mark data-path guards `// guard: <name> tests=<regexp>`,
  naming the test that must fail without them. If removing one leaves that test green, write the
  test. The guardmut baseline is empty and the workflow fails any PR that adds a line to it, so a
  baseline entry can never be how a gate passes.
- **Rebases and bumps.** Before merging a rebased PR, look at the diff against `main` for lines the PR
  did not intend to touch, especially `go.mod` and `go.sum`; a version that goes down is a revert.
- **Stopping mid-task.** A session that stops with PRs unfinished labels each `do-not-merge` and
  leaves a one-line status comment. A session that cannot read the decision log opens PRs and does not
  merge or tag.

### Process maturity (be honest about which gates are actually live)

Do not claim a bar is met when it structurally isn't. Mark PRs against what's real today.

| Gate | Status |
| --- | --- |
| Lint, race detector, unit + integration, conventional commits | **live now** |
| Design doc / ADR before non-trivial work | **live now** |
| Adversarial self-review on AI PRs | **live now** |
| Human (DeVaris) sign-off on Tier 1 | **live now** |
| Coverage floor enforcement | **live now, blocking as a ratchet** (`.github/workflows/coverage.yml` + `.github/coverage-baseline.txt` + `.github/scripts/coverage_ratchet.py`). Runs on every PR and on push to `main`. It measures per-package unit-test statement coverage (`make coverage`: `go test ./pkg/...`, no Docker, no integration tag) and compares the engine packages (`pkg/connector`, `pipeline`, `lifecycle`, `orchestrator`, `processor`, `provisioning`, `plugin`, `schemaregistry`, `conduit`, `inspector`) to a committed baseline. **It fails when an engine package drops more than 1.0 point below its baseline, when a baseline package has no coverage data, or when a NEW engine package is under 80%.** 19 of 45 baselined packages are below the 80% floor today (for example `pkg/pipeline` 57.8%, `pkg/plugin/connector` 5.6%, `pkg/plugin/processor` 37.5%), so the floor itself is NOT met: a hard 80% would fail every PR, and report-only would be silent. The baseline is the work list; raise entries as coverage improves (the job summary says which). **REQUIRED**: `coverage (engine ratchet)` is in `main`'s required contexts (blocking), added 2026-10-14 after its first push run on `main` was green. Integration-tag coverage is not measured |
| benchi regression gate | **live now, blocking** (`.github/workflows/bench-engine.yml` + `.github/workflows/bench-engine-comment.yml` + `.github/scripts/bench_engine_compare.py`; was "by Phase 1", targeted at v0.21 where the arch-v2 flip makes it load-bearing). Runs on every engine PR (path-filtered to `pkg/lifecycle*`, `pkg/connector`, `pkg/plugin`, `pkg/internal/enginetest`, `go.mod`/`go.sum`). It builds the base and head in-process benchmark binaries (`make bench-engine`: `pkg/internal/enginetest/engines/bench_test.go`, fake connectors, no Docker) and runs base, head and base again (an A/A control) interleaved, 5 rounds, rotating order each round. The metric is ground truth — the fake source produces `records*sources` records and reports records/s only after the destination acked every one — not engine instrumentation (`benchi/METHODOLOGY.md`). **It fails when any benchmark is more than 10% slower than base AND that slowdown exceeds twice the A/A noise floor measured on unchanged code in the same job** (so a 10% delta only fails when the floor is under 5%; the floor is cited next to every gate decision in the step summary and the PR comment). A run that cannot resolve the gate (base did not build, or no floor is measurable) reports "cannot resolve" and does not fail — no comparison is not a regression; a crashing benchmark binary fails the job (fail-closed). **NOT in `main`'s required contexts**: it is path-filtered, and a path-filtered workflow cannot become a required context without the always-report/fail-closed restructure (same caveat as `rag-template-e2e`); same-repo PRs only (a fork's token cannot comment and a fork's benchmark code must not run with trust) |
| Dependency vulnerability scan (govulncheck) | **live now, blocking as a ratchet** (`.github/workflows/govulncheck.yml` + `.github/govulncheck-allowlist.txt`, added 2026-08-21). Runs on every PR, on push to `main`, and nightly (05:15) via `govulncheck -format json ./...` — reachability-aware, so it reports only vulnerabilities this codebase's call graph can actually reach, not every CVE in the dependency tree. **It fails when a called-level vulnerability appears that is not in the baseline.** The baseline held 24 findings reachable on `main` at introduction; the 20 Go standard library entries were cleared 2026-10-07 by the go 1.25.14 bump (#2914). It now holds 4, each with a written justification beside it: 1 `golang.org/x/crypto` openpgp (unmaintained upstream) and 3 `github.com/hamba/avro/v2` (upstream archived 2026-01-18, #2817; cleared by the v0.21 connector sweep to `iskorotkov/avro`). Failing on ANY finding would have failed every PR for problems unrelated to its diff; report-only would have been green and therefore silent. To tighten further: clear baseline entries (the job reports which are already fixed) and add `govulncheck` to `main`'s required contexts |
| Property + fuzz tests on data-path/boundaries | **partially live** (was "by Phase 1"). 9 packages carry `Fuzz*` targets (egress policy/IP guard, registry envelope/trust/bounded-fetch, conduiterr status, fuzzymatch, diff) and their seed corpora run inside the normal `test` job — so a seed regression IS caught. What is missing is most of the testing-standards split: the only short-fuzz CI run is `migrate-harness`'s 30-second `FuzzParseInput` over the migration-corpus parsers (`tests/migrate/harness`); there is no repo-wide short-fuzz job, no scheduled long run, and no property-based framework (rapid/gopter) in the tree at all |
| Chaos suite (`tests/chaos`) nightly | **live now + REQUIRED** (`.github/workflows/chaos.yml`) — nightly + every PR via the always-report/fail-closed detector (the job runs on all PRs; the SIGKILL/FIFO-ack no-infra suite executes only when a data-path tree changed, fail-closed on any diff ambiguity). `tests/chaos (race, x3)` **is** in `main`'s required branch-protection contexts (blocking) — the earlier "not yet required" note was stale. `bundle-e2e` **is now required too**. `rag-e2e` **is now required too** (verified against the live branch-protection API 2026-08-21; the earlier "one toggle away, held for a de-flake soak" note was stale). `rag-template-e2e` IS path-filtered, so it cannot become required until it is restructured to the always-report/fail-closed pattern; a path-filtered workflow never reports on unrelated PRs and would block every merge permanently. Required contexts on `main` today: `test`, `golangci-lint`, `validate-generated-files`, `build (linux/386)`, `docker build`, `tests/chaos (race, x3)`, `bundle-e2e`, `rag-e2e`, `coverage (engine ratchet)` |
| Migration harness (`tests/migrate`, #3010) | **live (pending), NOT required** (`.github/workflows/migrate-harness.yml`, job `migrate-harness`, added 2026-10-15). Runs on every PR (always-report/fail-closed detector over the migrate surfaces), on push to `main` and nightly. The corpus checks (manifests, provenance and licenses, scrub-v1, certification entries) also run in `test` and are blocking there. Every check that runs `conduit migrate kafka-connect` reports **pending** until the rule data covers its case; pending is counted in the job summary and never reported as passing. Pending ends per family: a case becomes required when `pkg/kcmigrate/rules/data` has its family's class file (the shared file for unsupported-class cases, the worker file for `--worker` cases), so the command landing with every class unsupported (#3074) stays green and pending, and each family's rule-file PR turns its cases on and carries their goldens. MCP parity becomes required for ready cases once the MCP adapter's source registers `kc_migrate`. The certification gate runs over the plugins the ready cases actually emit. Becomes required (decided by DeVaris, 2026-10-15) once the `migrate kafka-connect` command has landed, goldens are committed for every case, and it has 10 consecutive green nightly and `main` runs; then add `migrate-harness` to `main`'s required contexts and move this row to REQUIRED. Also decided: the `pending` tier in `tests/migrate/corpus/certification.yaml` (the release gate requires `certified`) and the additive `case.yaml` fields (`provenance.license`/`copyright`/`modifications`, `expect.entries`/`error`/`pipelines`) |
| Multi-pipeline value correctness (`tests/valuecorrectness`) | **live now, not yet required** (`.github/workflows/value-correctness.yml`, added after `conduit-commons#305`) — nightly + every PR via the same always-report/fail-closed detector as chaos.yml (the suite runs when a data-path tree, `go.mod`/`go.sum`, or the suite itself changed). Several pipelines in one process with registered Avro schemas that share type names with different definitions (record field order, enum symbol order, decimal scale), through `avro.encode` and `avro.decode`/`avro.encode` round trips on the built-in registry; every record's wire header, bytes and decoded value are compared with an isolated codec. Verified to fail on `conduit-commons` v0.7.0 and v0.8.0 and pass with the `conduit-commons#309`/`#310` fix. Skipped in the `test` job unless `CONDUIT_VALUE_CORRECTNESS=1`. Covers Avro only; Protobuf and JSON Schema are not exercised yet. Make it required once it has a green nightly history |
| External-state chaos suite (`conduit-connector-postgres`, `test/externalstate`) | **live, not required once merged** (`.github/workflows/external-state.yml`, `conduit-connector-postgres#451`) on PRs touching `source/**`, `test/**`, `internal/**`, `source.go`, `go.mod`, `go.sum`, the Makefile or the workflow, on push to `main` and nightly. Runs in its own compose projects on ports 56150-56154 and never touches the `make test` (5433) or chaos (5434) stacks. Cases: slot dropped between runs and mid-run, slot invalidated, half-open replication connection, privilege revoked, snapshot restore. Each case pins the refusal codes it accepts; a case that lists none must deliver. Same-name slot recreation, failover and the `pg_logical_emit_message` privilege case are **pending** on postgres#447 and cannot stay pending: the runner fails when the test they wait for exists. Snapshot restore on one timeline is carried as an **expected failure** (an open data-loss gap): the runner requires `SILENT` loss output, and fails when it passes or fails for another reason. Path-filtered, so it cannot become required without the always-report restructure. Becomes required after a green nightly history |
| Guard mutation gate, `conduit` (`tools/guardmut`) | **live, not required once merged** (`.github/workflows/guardmut.yml`, #3262). Removes each `// guard:` marked guard in `pkg/connector`, `pkg/lifecycle`, `pkg/lifecycle-poc` and fails when the test named by `tests=` stays green. 17 guards, baseline empty. The workflow fails any added baseline line and any guard removed from `.github/guardmut-guards.txt` without a `guardmut-removed:` line in the PR body. **Not required**: becomes required after a green history |
| Guard mutation gate, `conduit-connector-postgres` | **live, not required once merged** (`conduit-connector-postgres#452`). The same tool and ratchets for `source/logrepl`, in its own compose project on ports 56170-56171; the guard that needs slot-invalidation mode runs alone, last. The guards added by postgres#447 are marked in a follow-up after it merges. **Not required** |
| CODEOWNERS for Tier 1 paths | **file live, rule not enforced** (#3259). Enforced only when branch protection turns on "Require review from Code Owners" (DeVaris's decision, see the postmortem) |
| `go.mod` downgrade guard (#3265), `tier1-review` status (#3266), release fixes gate (#3267), release notes gate (#3269), connector release gate (#3268) | **live, not required once merged**. `tier1-review` is a commit status that becomes meaningful only when it is in `main`'s required contexts (DeVaris's decision) and review verdicts come from a second account; with one account it checks the ritual, not the identity. The release gates run at tag time inside `release.yml` and exist on a release branch only after backport. The connector release gate is a reusable workflow not yet called by any connector repository |
| Upgrade/downgrade tests gating releases | by Phase 2 |
| 24h soak gating every release | by Phase 2 |

When you add a gate to CI, move its row to **live now** in the same PR. The table is the source
of truth for "is this rule real yet."

### Merging (solo-maintainer workflow)

`enforce_admins` is off on `main`, so Claude may merge DeVaris-owned PRs with
`gh pr merge --admin` — but _only_ after a thorough review, never as a rubber stamp. All of the
following must hold before merging:

- The tier-appropriate review is complete and its findings are documented in the PR (for
  AI-authored code, the adversarial self-review pass; for Tier 1, the failure-mode analysis).
- _Every required CI status check is actually green._ Never `--admin`-bypass a failing or
  pending check to merge — that violates the "no suppressing checks" rule and defeats the point
  of the gate. Wait for green.
- Bug fixes include the regression test that would have caught the bug, verified to fail without
  the fix and pass with it.
- For a Tier 1 (data-path) change, any _unresolved_ uncertainty about behavior under failure is
  surfaced to DeVaris for an explicit decision before merging — the standing merge authorization
  covers reviewed-and-understood changes, not open questions.

This replaces the second-human-approval gate while the project is solo. It re-tightens to real
peer review at the first co-maintainer; at that point admin-merge goes back to
exceptional-only. Community (non-maintainer) PRs still require the normal review — the
admin-merge path is for maintainer-authored work that has cleared the bar above.

**Release branches.** PRs to `release/vX.Y.x` follow the same rules: same required checks (strict), same review tier
as the original change, same `--admin` conditions. A backport is a `git cherry-pick -x` of a commit already on `main`
(fix on `main` first, unless release-only), labelled with the original PR; see `docs/releases.md`, "Release branches".
The release quiet window is measured on the release branch, not on `main`.

## Working style (how DeVaris operates)

- Direct, critical feedback. If a design is weak, say so and propose the alternative — don't
  hedge.
- Deployable output over frameworks. Working code, complete files, ready-to-merge PRs,
  ready-to-publish docs.
- Concise, human writing in all public-facing text. No AI-polished filler, no "delve," no
  exclamation-point enthusiasm.
- When scope is ambiguous, bias toward the two strategic bets (see `STRATEGY.md`) and ask one
  sharp question rather than five vague ones.

## Definition of done (any task)

- [ ] Risk tier declared; tier-appropriate review completed (Tier 1 = human sign-off +
      failure-mode analysis)
- [ ] Compiles, lints clean, race detector clean; all _currently-live_ tier-required suites pass
- [ ] Every bug fix includes the test that would have caught it
- [ ] Data-path guards are marked `// guard:` and the mutation gate kills their removal
- [ ] Tier 1: existing behaviour on the touched path reviewed, external-system premises cited, review
      verdict posted on the PR at the head SHA
- [ ] Godoc on new exported symbols; invariant comments at touched enforcement sites;
      architecture docs updated if behavior changed
- [ ] Docs updated (README, `conduit.io` source, llms.txt, changelog entry)
- [ ] `--json` output and stable error codes on any new CLI surface
- [ ] Adversarial self-review findings documented in the PR (AI-authored code)
- [ ] Roadmap item referenced in the PR
- [ ] No new dependencies without justification in the PR description
- [ ] Breaking changes flagged with a migration note and deprecation plan

## Session workflows

### Issue triage

1. Pull open issues; group by bug / feature / question / stale.
2. Per item: reproduce if a bug, label, link to a roadmap item or close with a respectful
   explanation.
3. Output a triage report: closed, labeled, needs-maintainer-decision.

### New connector

1. Scaffold from `conduit-connector-sdk` template (or `conduit connector new` once it exists).
2. Implement source and/or destination; wire config validation and schema support.
3. Pass acceptance tests; add integration tests with docker-compose for the target system.
4. README with config reference, delivery-semantics notes, runnable example pipeline.
5. Add to the connector list in docs and (once live) the registry.

### New processor

1. Scaffold from `conduit-processor-sdk`; prefer standalone (WASM) unless perf demands built-in.
2. Unit tests covering record shapes: raw, structured, tombstones, errors.
3. Add a cookbook recipe to docs.

### AI-pipeline components (embedding/chunking processors, vector sinks)

1. Follow the connector/processor workflows.
2. Embedding processors: pluggable provider (OpenAI, Voyage, local), batching, rate-limit
   handling, cost-relevant docs (tokens per record).
3. Vector destinations: upsert semantics, metadata mapping, dimension validation at pipeline
   start (fail fast, actionable error).
4. Every AI component ships with a working RAG-sync template update.

### Kafka Connect migration

- Concept mapping: KC worker → Conduit instance · connector+tasks → pipeline · SMT → processor ·
  converter → schema/format config.
- `conduit migrate kafka-connect` must always emit a compatibility report — never silently drop
  config it can't translate.

### MCP server

- Tools mirror CLI verbs: scaffold, validate, deploy, inspect, repair. Same code paths as the
  CLI — no divergent logic.
- Every tool returns structured results with error codes; test with a real agent session (zero →
  running pipeline using only MCP + llms.txt) before calling it done. That's a north-star metric.

### State layer

- Scope check first against ADR `20261008-state-layer-scope-windows-and-bounded-lateness.md`:
  dedup with TTL, lookup tables and stream-table joins, keyed upsert/merge, tumbling/sliding/session
  windows, standard aggregates, bounded-lateness event time, read-only key lookups over API/MCP.
  Anything on its excluded list needs a superseding ADR, not a design-doc footnote.
- Prerequisites before state work beyond design: arch-v2 is the default engine, the
  partition-claims protocol has shipped, replay/backfill verbs exist.
- State is partition-scoped and committed atomically with the checkpoint (invariant 5); late
  records follow the configured policy, never silently dropped (invariant 6).
- Checkpoint/recovery tests are mandatory for every state feature (kill mid-write and mid-window,
  verify state and positions).

### Benchmarks

- Use benchi. Compare against Kafka Connect first. Commit configs and environment specs. Report
  medians with variance, not best runs.

## Things to never do

- Never introduce a privileged integration for one broker vendor.
- Never add license restrictions or enterprise-gated features to open-source repos.
- Never claim Conduit replaces Flink; position it as operational stream processing for
  integration and AI, with streaming SQL partner engines for heavy SQL and stream-stream joins.
- Never add global/distributed watermarks, a triggers framework, stream-stream joins, distributed
  snapshots, or pluggable state backends to the state layer without a superseding ADR.
- Never introduce a bespoke transformation DSL — transformations are real-language code compiled
  to WASM or run as out-of-process gRPC processors. Established standard query languages (jq,
  JMESPath, SQL) are allowed as processor parameters, never as a pipeline configuration language.
- Never change `conduit-connector-protocol` without an explicit versioning discussion.
- Never publish performance claims without reproducible benchi results.
- Never mark a connector "supported" without acceptance tests passing in CI.
- Never start a speculative SDK for a community-tier language (Ruby, etc.) without documented
  demand. Official languages are Go, Python, TypeScript, Rust (full tier) and Java, C# (enterprise
  tier) — see `ROADMAP.md`.
- Never merge a Tier 1 (data-path) change on automated review alone — human sign-off is
  mandatory.
- Never trade an ack-correctness, ordering, or crash-safety guarantee for throughput without a
  design doc and explicit approval.
- Never change a serialized format (positions, state, checkpoints, protocol) without a versioned
  migration path and an upgrade test.
- Never ship a bug fix without the regression test that would have caught it.
- Never suppress, downgrade, or waive a failing chaos, upgrade, or race-detector check to make a
  release date.
- Never add clustering primitives (membership, leader election, gossip, consensus) to the
  engine — distribution lives in the scheduling layer, per the ADR.
- Never move anything shipped as open source behind a paywall, and never gate the k8s operator,
  Helm chart, or fleet console core — the one-way ratchet is absolute.
- Never claim a review or test gate was satisfied when the _Process maturity_ table marks it not
  yet live — say what was actually run.
- Never resolve a data-loss or exposure gap by documenting it, deferring it to a later version, or
  hiding it behind an opt-in. Fix it, fail closed, or block the release.
- Never recreate, assume or "continue with a warning" at a point where an external resource is
  missing, replaced or unverifiable and continuing could skip, duplicate or change records.
- Never merge a Tier 1 PR that has a `changes-needed` verdict at any head with no later `approve`, or
  one that has no `approve` and sign-off at its current head.
- Never take a design premise about an external system from a comment or documentation alone; run it
  against a real server and cite the result.

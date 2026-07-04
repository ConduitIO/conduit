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
- **State layer discipline:** local embedded KV state, checkpointed with the pipeline. No
  distributed snapshots, no pluggable state backends, no event-time watermark machinery. If a
  design doc starts growing Flink features, stop and flag it.
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
- Merges freeze 48h before a release cut.

**Tier 2 — Features** (connectors, processors, CLI, UI, registry, docs-adjacent):

- One reviewer approval; acceptance/integration tests green; new surfaces have `--json` + error
  codes; docs updated in the same PR.

**Tier 3 — Chore** (deps, CI, typos, comments): one approval, green CI.

**Automated gates:** lint, race detector, unit + integration, coverage floor, benchmark
regression check, dependency vulnerability scan, conventional-commit check.

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

### Process maturity (be honest about which gates are actually live)

Do not claim a bar is met when it structurally isn't. Mark PRs against what's real today.

| Gate | Status |
| --- | --- |
| Lint, race detector, unit + integration, conventional commits | **live now** |
| Design doc / ADR before non-trivial work | **live now** |
| Adversarial self-review on AI PRs | **live now** |
| Human (DeVaris) sign-off on Tier 1 | **live now** |
| Coverage floor enforcement, benchi regression gate | by Phase 1 |
| Property + fuzz tests on data-path/boundaries | by Phase 1 |
| Chaos suite (`tests/chaos`) nightly | by Phase 2 (needs the state layer + a second maintainer) |
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

- Scope check first: dedup, lookup tables, simple windows — nothing else without explicit
  maintainer sign-off. Checkpoint/recovery tests are mandatory (kill mid-window, verify state).

### Benchmarks

- Use benchi. Compare against Kafka Connect first. Commit configs and environment specs. Report
  medians with variance, not best runs.

## Things to never do

- Never introduce a privileged integration for one broker vendor.
- Never add license restrictions or enterprise-gated features to open-source repos.
- Never claim Conduit replaces Flink; use the state-layer + streaming-SQL-partner framing.
- Never introduce a bespoke transformation DSL — transformations are real-language code compiled
  to WASM.
- Never change `conduit-connector-protocol` without an explicit versioning discussion.
- Never publish performance claims without reproducible benchi results.
- Never mark a connector "supported" without acceptance tests passing in CI.
- Never start a speculative C#/Ruby/Java-native SDK without documented demand.
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

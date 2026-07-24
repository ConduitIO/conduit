# Workstream 3 — Templates gallery (vendored-first, permanently)

Status: implemented by this doc's companion PR (`conduit pipelines init --template`). Originated
as a cross-repo workstream planning note (`v019-execution-plan-v2.md` §"Workstream 3"); committed
here, in `docs/design-documents/`, alongside the implementing PR so the section-number citations
in the implementation's code comments and tests (`cmd/conduit/root/pipelines/template_gallery.go`,
`init.go`, and their test files) resolve to a real, in-repo file rather than an external planning
document. Content below is unchanged from the pre-implementation planning version except this
status line and the note at the end of §12: the implementation followed §4's chosen design and
§5's rejected alternatives as written, and §12's open questions are carried forward for explicit
maintainer confirmation post-hoc (this PR was implemented directly per its assigning task, not
after an interactive sign-off round-trip on this doc — see the PR description).

Supersedes nothing; elaborates `v019-execution-plan-v2.md` §"Workstream 3" (lines 263–299) with
repo-verified specifics.

## 1. Problem & goal

Today, getting a first pipeline running requires either `conduit pipelines init` with zero
flags (a hardcoded `generator→log` demo) or hand-authoring a YAML file from the docs. There is
no curated set of runnable, realistic pipelines a new user or agent can drop in and run
immediately — no `postgres→s3`, no CDC example, nothing that demonstrates what Conduit is
actually for beyond the toy demo.

Goal: ship a small, permanently-maintained, embedded set of named pipeline templates —
`conduit pipelines init --template <name>` — that scaffold a runnable pipeline using only
already-built-in connectors, each CI-tested end-to-end (infra up, records asserted at the
destination), each documented. This is infrastructure the project keeps forever (cargo-new /
rustup-init style), not a stopgap until the connector registry ships template distribution.

## 2. Scope

**In v0.19:**

- An embedded (`go:embed`), versioned set of named pipeline templates, pinned to specific
  built-in-connector config shapes.
- `conduit pipelines init --template <name>` scaffolds a pipeline YAML from a named template
  (extends the existing `pipelines init` verb — see §4 for why, and §5 for the alternative
  considered and rejected).
- `conduit pipelines init --template list --json` enumerates the embedded set with a one-line
  description each.
- MVP set (four templates, all reusing existing built-in connectors):
  `generator→log`, `generator→file`, `postgres→s3`, `postgres CDC→kafka`.
- End-to-end CI for every template: infra spun up (docker-compose, matching existing connector
  integration-test patterns), pipeline run, records asserted landed at the destination.
- A README per template: config reference, delivery-semantics notes, runnable example.
- A documented community contribution path: a plain PR to the core repo's template directory,
  reviewed Tier 2.
- `--json` conformance on both `--template list` and the scaffold outcome, per Workstream 8's
  envelope (this doc's §10 is normative for that; Workstream 8's schema-golden test is the
  enforcement point).

**Out of v0.19 (explicitly, not silently dropped):**

- Registry-backed / community-published templates as a distribution mechanism. Gated on the
  registry MVP's R-2 (install core) **and** its web UI shipping — not on R-1 alone (only R-1 is
  merged; see §4). This is an additive Phase-2 breadth layer, never a replacement for vendoring.
- Any template requiring a connector that isn't already built in (WASM connectors, non-builtin
  plugins). Structurally impossible in the MVP set (§4), but the refusal behavior for a
  hypothetical future template that does need one is specified in §7 so authors don't have to
  invent it later.
- Rust/TS connector templates (no SDK exists yet — Workstream 4).
- A `conduit templates publish`/signing flow (that's the registry's R-3, unbuilt).

## 3. Invariants & tier

**Tier 2** (Features), per `v019-execution-plan-v2.md` line 265 ("No design doc gate... it's
Tier 2, not a new subsystem") and CLAUDE.md's tiering: this reuses existing scaffolding
infrastructure (`pipelines init`) and existing, already-shipped connectors (postgres, s3, kafka,
generator, log, file) — no new subsystem, no protocol change, no state-layer touch. This doc is
committed anyway (see the status line above) so the implementation's in-code citations resolve.

Data-integrity invariants that still apply even though this is "just a generator of config
files":

- **Invariant 6** (schema handling never silently mangles data) is relevant to what the
  `postgres CDC→kafka` template's README documents about type mapping — the template itself
  doesn't do any coercion, it configures existing connectors, but the README must not
  misrepresent their guarantees.
- **Invariant 3** (at-least-once floor) — the CDC template's delivery-semantics note must
  accurately state what the underlying connectors already guarantee, not invent a new claim.

No new data-path code is written by this workstream — it assembles existing, already-tested
connector configs into named presets. The chaos/upgrade/property-test gates (CLAUDE.md's
Process Maturity table) do not apply to the templates themselves; they apply to the connectors
being templated, and those already exist and are already covered independently.

## 4. Chosen design

**Extend `conduit pipelines init`, not the top-level `conduit init`.**

Verified repo reality, and why this matters for where `--template` lands:

- `cmd/conduit/root/initialize/init.go` is `conduit init` — it sets up a **workspace**
  (`conduit.yaml` + `processors`/`connectors`/`pipelines` directories, `init.go:66-110`). It does
  not touch pipeline config and has no source/destination concept. `plan-v2`'s literal phrasing
  ("`conduit init --template <name>`", line 271) collides with this existing, unrelated verb.
- `cmd/conduit/root/pipelines/init.go` is `conduit pipelines init [PIPELINE_NAME]` — it **already
  scaffolds a single pipeline YAML** from `--source`/`--destination` flags (defaulting to
  `generator`/`log`, `init.go:44-46,53-57`), using a `go:embed`'d `text/template`
  (`pipeline.tmpl`, embedded at `init.go:39-40`) and introspecting
  `builtin.DefaultBuiltinConnectors` (`pkg/plugin/connector/builtin/registry.go:42-48`) for
  parameter docs (`init.go:105-148`). This is the load-bearing infra a template gallery extends:
  same output shape, same connector-introspection mechanism, same destination path
  (`--pipelines.path`).
- **Decision:** add a `--template <name>` flag to `conduit pipelines init`, mutually exclusive
  with `--source`/`--destination` (a named template _is_ a specific source+destination+settings
  triple; mixing them is ambiguous and rejected with a coded error). `--template list --json`
  (the literal value `"list"`) triggers enumeration mode instead of scaffolding — matching
  plan-v2's specified UX (line 271) rather than inventing a separate subcommand, at the cost of
  a slightly unconventional "flag value as verb" pattern (see §5 for the alternative and why it
  lost).
- **Symbol collision to avoid:** `cmd/conduit/root/pipelines/template.go` already defines
  `pipelineTemplate` and `connectorSpec` structs (`template.go:24-29`) used by the _generic_
  source/destination path. The named-template gallery is a different concept (a curated,
  versioned, embedded catalog) and must not reuse that type name. Proposed: a new file
  `cmd/conduit/root/pipelines/template_gallery.go` (or a `pkg/scaffold/pipelinetemplate`
  subpackage if the catalog logic is non-trivial enough to unit test independently — see task
  breakdown) defining a distinctly-named `GalleryTemplate`/`Catalog` type, reusing
  `pipelineTemplate`'s render path (the same `pipeline.tmpl` + `funcMap`) rather than
  duplicating it.
- **Not `pkg/scaffold`.** That package (`pkg/scaffold/scaffold.go`, `pkg/scaffold/template/`)
  scaffolds **connector/processor Go source code** (a full repo: `setup.sh`, Go module, SDK
  wiring — see `pkg/scaffold/request.go:44-79`), not pipeline config. It is a different kind of
  "template" (code generation vs. config generation) that happens to share the English word.
  Naming this out explicitly because plan-v2's phrasing ("reuses existing scaffolding
  infrastructure") could be misread as pointing at `pkg/scaffold`; it should be read as pointing
  at `pipelines init`'s existing generic-template mechanism instead.

**Corrected precedent for the non-empty-destination / overwrite behavior** (plan-v2 line
294–296 claims templates reuse "the same confirm/--dry-run/--force convention as `conduit init`
today" — **verified false**, see §7): neither `conduit init` (`init.go:88-110`, plain
`os.WriteFile` with no existence check — silently overwrites `conduit.yaml`) nor today's
`pipelines init` (`init.go:217-224`, `os.OpenFile` with `O_TRUNC` — silently overwrites an
existing pipeline YAML with no flag to prevent it) actually implement any confirm/force/dry-run
convention. The real, working precedent is connector/processor scaffolding's `--force`:
`pkg/scaffold/request.go:77-78` (`Force bool`, doc: "allows overwriting an existing destination
directory"), enforced at `request.go:182-185` (refuses without `--force`, coded error suggesting
`--force` or a different `--path`), with `cmd/conduit/internal/scaffoldcmd/newcmd.go:48-49`
wiring `--yes`/`-y` (currently a no-op stub, "forward compatibility" per its own usage string)
and `--force` as CLI flags. Templates' overwrite handling should copy _this_ pattern
(`--force` refuses-by-default, coded error, no working `--dry-run` or interactive prompt to
imitate because none exists yet for scaffolding) rather than a nonexistent `conduit init`
convention. Adding a genuine `--dry-run` to `pipelines init --template` (print the resolved YAML
without writing) is new, small, and worth doing here since nothing to copy exists — call this
out plainly rather than pretending it's reused.

**Connector coverage (verified, confirms zero manual-download cliff):**
`pkg/plugin/connector/builtin/registry.go:42-48` — `DefaultBuiltinConnectors` = `file`,
`generator`, `kafka`, `log`, `postgres`, `s3`. Every MVP template
(`generator→log`, `generator→file`, `postgres→s3`, `postgres CDC→kafka`) uses only these six.
Confirmed structurally impossible to hit the manual-download cliff with this set.

**Registry reservation claim corrected:** plan-v2 (line 278) states the registry index schema
"reserves room for this [registry-backed templates]; no rework needed when it arrives." Verified
against `pkg/registry/index/schema_v1.go` and `pkg/registry/index/doc.go`: **no mention of
"template" in either file.** This is not yet true — flagged as an open question in §12, not
silently corrected, since it's the registry team's (i.e., this same maintainer's, later) call
whether to add the reservation now or treat it as Phase-2 schema-v2 work.

## 5. Alternatives considered

1. **Top-level `conduit init --template <name>`** (plan-v2's literal wording). **Rejected:**
   `conduit init` is workspace setup (config file + directories), a different lifecycle stage
   than "scaffold a runnable pipeline." Overloading it would mean `conduit init --template x`
   sometimes creates a workspace and sometimes also drops a pipeline file, which is exactly the
   kind of two-things-in-one-flag design CLAUDE.md's simplicity bar rejects. `pipelines init`
   already owns "produce a pipeline YAML"; extending it keeps one verb per lifecycle stage.
2. **New subcommand group: `conduit pipelines templates list --json` / `conduit pipelines new
   --template <name>`.** More conventional CLI shape (a real subcommand instead of a flag value
   doubling as a sentinel for "list mode"), and avoids the slightly awkward
   `--template list --json` overload. **Lost** because plan-v2 already commits to the
   `--template list --json` UX explicitly (line 271) and DeVaris has not asked to revisit that
   surface; changing it here would be scope creep on a Tier-2, no-design-doc item. Noted as the
   cleaner alternative if the flag-value-as-sentinel pattern draws review pushback.
3. **Registry-backed templates first, vendored later ("thin now, thick later").** **Rejected**
   per plan-v2's explicit framing (line 268–270): vendoring is the permanent model, not a
   stopgap. Building registry-dependent template distribution first would also be blocked today
   — R-2 through R-6 are unbuilt (only R-1 merged, `v019-execution-plan-v2.md` line 118) — so
   this alternative isn't just worse, it's currently impossible to ship in v0.19 at all.

## 6. Acceptance criteria

1. **Every template in the embedded set scaffolds a working pipeline.** Test: a table-driven
   integration test in the new template-gallery test file that runs
   `pipelines init --template <name>` for all four names against a temp `--pipelines.path` and
   asserts the output YAML parses via the existing pipeline-config parser
   (`pkg/provisioning/config/yaml`). Metric: 4/4 templates produce parseable, schema-valid YAML.
2. **`--template list --json` enumerates all templates with a one-line description, conforming
   to Workstream 8's envelope.** Test: `template_gallery_test.go` asserts the `--json` output's
   top-level shape against Workstream 8's committed schema (cross-referenced, not duplicated —
   see §10) and asserts `len(result.templates) == 4` with non-empty `description` on each.
3. **Every template is CI-tested end-to-end.** Test: one CI job per template (or a matrix job)
   that (a) spins up the template's required infra via docker-compose (postgres+kafka for the
   CDC template, postgres+minio/s3-compatible for `postgres→s3`, nothing external for the two
   `generator`-sourced templates), (b) runs `conduit pipelines init --template <name>` then
   `conduit run` against the result, (c) asserts records land at the destination (row count / a
   specific record's content, not just process exit code). Metric: all four templates green in
   CI, asserting on destination-side data, not YAML parseability alone.
4. **Each template ships a README matching what CI actually asserts.** Test: a doc-lint or
   simple test asserts a `README.md` exists alongside each embedded template directory and
   contains the required sections (config reference, delivery-semantics note, runnable example)
   — grep-based section-heading check, not prose-quality review (that's human review).
5. **Zero MVP templates require a non-built-in connector.** Verified structurally at write time
   (§4); test: a unit test asserts every template's declared source/destination plugin name is a
   key in `builtin.DefaultBuiltinConnectors` (`registry.go:42`), failing the build if a future
   template addition violates this without updating this doc's scope.
6. **Version-pinned mismatch refuses cleanly.** See §7's row — test asserts a coded error, not a
   scaffold of a broken pipeline.
7. **Non-empty/conflicting destination file refuses without `--force`, per §4's corrected
   precedent.** Test: `pipelines init --template generator-log` twice into the same
   `--pipelines.path` without `--force` between runs asserts a coded refusal
   (`conduiterr` code, exit 2 via `pkg/conduit/exitcode`), and asserts success with `--force`.

## 7. Edge cases & mitigations

| Case | Failure/risk | Mitigation | Test |
| --- | --- | --- | --- |
| Unknown `--template` name | User typo scaffolds nothing, or worse, silently falls back to the generic demo | Coded error (`conduiterr`) listing valid names, exit 2 | Unit test: `--template postgre-s3` (typo) asserts error message enumerates the 4 valid names |
| `--template` combined with `--source`/`--destination` | Ambiguous: which wins? | Reject at flag-parse/validate time with a coded error explaining they're mutually exclusive | Unit test: both flags set asserts `CodeInvalidArgument`-class error, exit 2 |
| `--template list` combined with a real scaffold (e.g. `--template list --source generator`) | Same ambiguity as above, plus "list" could theoretically collide with a future template literally named "list" | Reserve `list` as a sentinel value (documented); a template must never be named `list` — enforced by a catalog-loading assertion, not just convention | Unit test: catalog loader panics/fails fast if any embedded template's name is `"list"` |
| Version-pinned template's connector version mismatch (e.g. the template pins a `postgres` connector param shape that changed) | `init` scaffolds a file that fails at `conduit run`, confusing failure far from the cause | `init` refuses up front with a clear message naming the mismatch, rather than scaffolding something that won't run — validated against the _build-time_ `builtin.DefaultBuiltinConnectors` version, so mismatch can only occur if a template's pinned expectation drifts from the binary it ships in, which CI's end-to-end test (AC-3) catches before release | Regression test: intentionally stale a template fixture's expected param set and assert `init` refuses rather than emitting a broken YAML |
| Non-empty/pre-existing destination file | Silent overwrite (today's actual behavior in both `init` and `pipelines init` — see §4) loses a user's edited pipeline config | `--force` required to overwrite, same shape as `pkg/scaffold/request.go:77-78,182-185`; add `--dry-run` to print resolved YAML without writing (new, since no existing `pipelines init` precedent has it) | AC-7's test above; a second test asserts `--dry-run` writes nothing and prints the would-be YAML to stdout |
| CI infra spin-up flake (docker-compose postgres/kafka/minio doesn't come up in time) | Flaky CI blocks unrelated PRs; a real regression gets lost in flake noise | Reuse the existing connector integration-test harness's retry/health-check conventions (same docker-compose + wait-for-healthy pattern already used by `conduit-connector-postgres`/`-kafka`/`-s3` integration tests) rather than inventing a new one; templates' E2E tests are consumers of that harness, not a new flake surface | CI job retries transient infra-startup failures per the existing connector CI convention; a genuine assertion failure (wrong record count) never retries |
| A future template author proposes one needing a non-built-in connector | Reintroduces the manual-download cliff the MVP set structurally avoids | Documented refusal behavior: template-gallery contribution docs state new templates must use only `builtin.DefaultBuiltinConnectors` entries; a CI check on the templates directory enforces this the same way AC-5's test does today, so it can't silently regress via a future PR | Same test as AC-5, run against the full catalog including any future additions |
| Community PR adds a template with a broken/misleading README | Docs promise something CI doesn't verify | Tier-2 review requires README sections to match what the added CI job actually asserts (AC-4); reviewer checklist item, not automated beyond the section-presence check | AC-4's test (structural) + human review (content accuracy) |

## 8. Failure-mode analysis

Applicable (per CLAUDE.md, Tier 2 does not require the full Tier-1 chaos/failure-mode writeup,
but the review's ask for "as applicable" is met here for the workstream's actual risk surface):

- **Infra spin-up flake in CI:** covered in §7's table. Mitigation is reuse of existing
  connector-repo CI patterns, not new infrastructure — this workstream is not the first to spin
  up postgres/kafka/s3 in CI.
- **Missing connector at template-author time:** structurally can't happen in the MVP set (§4,
  §6 AC-5); the refusal path for a hypothetical future violation is a build-time test failure
  (fails PR CI), not a runtime failure a user hits.
- **Dirty destination directory:** covered in §7; the fix (require `--force`) closes an actual,
  verified-real silent-overwrite bug in both existing `init` commands, so this workstream also
  improves — not just replicates — the status quo. Flag this to DeVaris as a possible companion
  fix to backport `--force`-gating onto the existing generic `pipelines init` path in the same
  PR (see §12).
- **No data-path invariant is at risk:** templates are config generation, not runtime pipeline
  execution logic; the underlying connectors' own invariant coverage is unchanged and untouched.

## 9. Backward-compat

- `conduit pipelines init` with `--source`/`--destination` (or no flags — the demo pipeline)
  continues to work exactly as today; `--template` is strictly additive.
- Adding `--force`/`--dry-run` gating to `pipelines init`'s existing generic path (if taken as a
  companion fix per §8) changes today's silent-overwrite behavior. This is a **behavior change,
  not a breaking config/protocol change** — no serialized format changes, but it does mean a
  script that today calls `pipelines init` twice into the same path and expects a silent
  overwrite will start failing without `--force`. Worth an explicit call-out in the v0.19
  changelog even though it's a bug fix, per CLAUDE.md's backward-compatibility discipline.
- The embedded template catalog is versioned independently of the Conduit binary version only
  in the sense that it's pinned per-release (go:embed'd at build time); no runtime
  version-negotiation exists or is needed, since there's no server/client split here.

## 10. Observability & errors

- All new/changed CLI surfaces conform to Workstream 8's envelope
  (`command`/`ok`/`summary`/`result`/`error`) via `cecdysis.CommandWithResult` — **this requires
  first bringing `pipelines init` onto that interface, since it currently implements only
  `ecdysis.CommandWithExecute` with no `--json` support at all**
  (`cmd/conduit/root/pipelines/init.go:34-37`, no `cecdysis` import). This is not optional
  scope creep — Workstream 8 explicitly names `init` as a surface requiring conformance
  (`v019-execution-plan-v2.md` line 486), and it structurally cannot happen without this change,
  since there's no envelope to conform to today.
- Coded errors (via `conduiterr`) for: unknown template name, mutually-exclusive flags, version
  mismatch, destination-exists-without-`--force` — each with `code`, `message`, `suggestion`
  populated per the CLI output conventions doc's error shape (`20260707-cli-output-conventions.md`
  §1).
- `--template list --json`'s `result` payload: `{"templates": [{"name": "...", "description":
  "...", "source": "...", "destination": "...", "deliverySemantics": "..."}]}` — schema
  registered with Workstream 8's schema-golden test as `pipelines.init` command family (see
  cross-workstream note in cli-contract.md §11).

## 11. Task breakdown + parallelization map

Ordered; `(size)` is rough effort, `[agent]` is suggested delegate type if executed by Claude.

1. **Bring `pipelines init` onto `cecdysis.CommandWithResult`** (size: M) `[golang-pro]`.
   Prerequisite for everything else — no dependents can conform to Workstream 8's envelope
   before this lands. Includes the `--force`/`--dry-run` gating fix (§7, §8) since it touches
   the same `Execute`/`getOutput` path being rewritten anyway.
2. **Design the embedded template catalog format + loader** (size: S) `[golang-pro]`. Depends on
   (1) only for the command wiring, not the catalog itself — can start in parallel. Decide: flat
   Go structs + `go:embed`'d YAML fixtures per template, vs. pure Go literals. Recommendation:
   embed the actual rendered-YAML fixture per template (so CI's "does it parse" check and the
   README's "runnable example" are the literal same bytes, not hand-copied prose that drifts) —
   revisit against alternatives in §5 discussion if reviewer disagrees.
3. **Author the four MVP templates** (size: M, parallelizable across templates once (2)'s format
   is fixed) `[golang-pro]` — `generator→log`, `generator→file`, `postgres→s3`,
   `postgres CDC→kafka`. Each is independent once the catalog format is fixed; can run as up to
   4 concurrent subagent tasks.
4. **`--template <name>` and `--template list --json` flag wiring** (size: S) `[golang-pro]`.
   Depends on (1) and (2).
5. **CI docker-compose harness per template** (size: M, parallelizable per template similarly to
   (3)) `[golang-pro]` or `[devops-engineer]` for the docker-compose/CI-yaml side specifically.
   Depends on (3).
6. **READMEs per template** (size: S each, parallelizable) — can be written alongside (3) by the
   same task, not a separate pass.
7. **Contribution-path docs** (size: S) — update `CONTRIBUTING.md` (verified: currently has zero
   mention of templates) stating the Tier-2 PR path explicitly, and that it is _not_ the
   registry's signed-publish flow. Can run in parallel with everything above; no code
   dependency.
8. **Workstream 8 schema-golden test registers `pipelines.init`'s envelope shape** — owned by
   the CLI-contract workstream, not this one, but **cannot complete until task (1) lands** —
   flagged as the cross-workstream dependency in cli-contract.md.

Parallelization: (2) and (7) can start immediately/day one. (1) blocks (4) and Workstream 8's
registration of this command; (3) blocks (5) and (6) but templates are independent of each
other. Suggested single-week-1 lane: (1) → (2) → (3)×4 parallel → (4)/(5)/(6) parallel → (7)
anytime.

## 12. Risk, confidence & open questions for DeVaris

**Risk: LOW-MEDIUM** (upgraded slightly from plan-v2's flat LOW, per findings above — the actual
work now includes retrofitting `--json`/`--force` onto an existing command with a real, if
minor, silent-overwrite bug, not purely additive new-file work).

**Confidence: HIGH** on the template content and CI approach (reuses proven connector
integration-test patterns); **MEDIUM** on the exact catalog-format decision (task 2) and the
`--template list` sentinel-value UX (flagged as alternative in §5).

**Open questions for DeVaris:**

1. Confirm the command-surface decision in §4 (`pipelines init --template`, not top-level
   `conduit init --template`) — this is a real deviation from plan-v2's literal text and should
   be an explicit sign-off, not an assumed correction.
2. Should the `--force`/`--dry-run` fix to `pipelines init`'s existing silent-overwrite behavior
   (§8, §9) ride in this same PR (bundled, since it touches the same code), or ship as a
   separate, earlier bug-fix PR so the behavior-change changelog note isn't buried inside a
   feature PR?
3. Registry index schema's "reserved room for templates" (plan-v2 line 278) is verified absent
   today (§4). Worth adding a placeholder field now (cheap, avoids a schema-v2 bump later) or
   deferred entirely to whenever registry-backed templates actually get designed?
4. Catalog format (task 2, §11): embedded rendered-YAML fixtures vs. Go-literal specs — any
   preference, given it affects how "runnable example in the README" and "CI's parse-check"
   stay in sync?

**Post-implementation note:** questions 1 and 4 above were resolved as follows by the
implementing PR, pending explicit confirmation rather than as an assumed correction: (1)
`pipelines init --template` (not top-level `conduit init --template`), per §4's reasoning; (4)
embedded, fully-rendered YAML fixtures (not re-templated Go literals), per §11 task 2's own
recommendation. Question 2 (`--force`/`--dry-run` bundled vs. separate PR) was already resolved
by the prerequisite `pipelines init` cecdysis-conformance PR landing first (task 1 above, merged
ahead of this doc's companion PR) — so it is moot. Question 3 (registry schema reservation) is
still open and deferred to whenever registry-backed templates are actually designed, per the
option this doc itself offers.

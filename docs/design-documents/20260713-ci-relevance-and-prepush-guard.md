# CI relevance audit and pre-push guard plan

## Summary

Audits every workflow in `.github/workflows/` against the current product (v0.17,
"CLI-as-product + agent-native": new CLI verbs, an MCP server over stdio and HTTP, Go connector
and processor scaffolding, planned Python scaffolding and `llms.txt`) and finds the pipeline
mostly healthy: no workflow builds or tests a surface that no longer exists, and every current
code surface is exercised by `go test ./...` because `test.yml` and `lint.yml` carry no path
filters. The two real problems are narrower than "stale CI": a couple of fossilized references to
the retired Ember UI (a dead exclusion glob, a dead GoReleaser build tag), and one concrete,
already-hit drift bug — `make markdown-lint` and the CI `markdown-lint.yml` job resolve to
different `markdownlint` versions, so "passes locally" and "passes in CI" are not the same claim.

This document also designs the fix: pin one `markdownlint-cli2` version shared by the Makefile and
CI (`0.18.1`, which bundles `markdownlint 0.38.0` — the version `markdownlint-cli2-action@v20`
actually resolves to today), assesses the same drift risk for `golangci-lint`, `buf`, and the
generated-files toolchain, and proposes a single `make verify` entry point plus an optional
pre-push hook so a contributor (or an agent) can answer "will this pass CI" before pushing,
without adding a second toolchain to maintain.

## Context

### Product surfaces as of v0.17 (per `CLAUDE.md`, `ROADMAP.md`)

- New CLI verbs under `cmd/conduit/root/*`: `validate`/`lint`/`dry-run`/`inspect`/`deploy`/
  `apply`/`doctor`/`init`/`quickstart`, plus scaffolding (`cmd/conduit/internal/scaffoldcmd`).
- An MCP server, `conduit mcp` (`cmd/conduit/root/mcp`, engine in `cmd/conduit/internal/mcp`),
  serving stdio by default and an optional streamable-HTTP transport behind bearer auth + TLS.
- Go connector/processor scaffolding (`pkg/scaffold`), generating from vendored golden templates.
- Planned, not yet in the tree: Python connector/processor scaffolding (design doc only:
  `docs/design-documents/20260707-python-connector-sdk.md`), `llms.txt` generation (documented as
  deferred in `docs/operations/mcp-server.md:98-105`), and true in-place pipeline hot-reload
  (`docs/design-documents/20260712-pipeline-dev-hot-reload.md`; `pkg/provisioning/plan.go:316,
  349, 412` confirm `apply` still does a full restart today, not a hot-swap).
- Gone: the external Ember UI. No `pkg/web` directory exists (confirmed by search); the OpenAPI
  spec now lives at `pkg/http/openapi/`. Two fossils remain in the tree and are called out below.

### Scope and method

Every file in `.github/workflows/` was read in full and cross-checked against what it claims to
protect: does the code path it builds/tests/lints still exist, does it cover the CLI/MCP/scaffold
surfaces above, and where a local `make` target claims to be CI-equivalent, does it actually use
the same tool version CI uses. Findings are cited `file:line`. No workflow YAML was changed in
this pass — this is the audit and plan only.

## Part 1 — Workflow relevance audit

### Inventory

| Workflow | Trigger | Protects | Verdict |
| --- | --- | --- | --- |
| `lint.yml` | push(main), PR | all Go source, `golangci-lint` | relevant, no gaps |
| `test.yml` | push(main), PR | unit+integration tests, linux/386 cross-build, Docker build | relevant, one coverage gap (MCP e2e) |
| `markdown-lint.yml` | PR touching `**.md` | all docs | relevant, drift bug (Part 2) + one dead exclusion |
| `buf-push.yaml` | push(main), `proto/**` | publish `proto/` to BSR | relevant |
| `buf-validate.yaml` | PR, `proto/**` | breaking-change detection | relevant |
| `buf-update.yaml` | weekly cron, dispatch | `buf` dependency freshness | relevant |
| `validate-generated-files.yml` | push(main), PR | generated code staying in sync | relevant, minor efficiency note |
| `dependabot-auto-merge-go.yml` | PR labeled by dependabot | Go dependency hygiene | relevant |
| `flake-hunt.yml` | nightly cron, dispatch | flaky-test detection (non-blocking) | relevant |
| `milestone-automation.yml` | hourly-Saturday cron, dispatch | project-board milestones | relevant (process, not code) |
| `roadmap-automation.yml` | issue labeled/unlabeled | project-board roadmap sync | relevant (process, not code) |
| `release.yml` | tag push `v*` | GoReleaser + Docker image release | relevant, ships one dead build tag |
| `trigger-nightly.yml` | weekday cron, dispatch | nightly tag/release/cleanup | relevant |

### `lint.yml`

Runs `golangci-lint` on every push to `main` and every PR, no path filter (`lint.yml:3-6`), so it
covers the whole module including `cmd/conduit/root/*` and `cmd/conduit/internal/mcp` — the new
CLI/MCP surfaces are ordinary Go packages and get linted like everything else. The version is
resolved at run time from `tools/go.mod` (`lint.yml:20-24`:
`go list -modfile=tools/go.mod -m -f '{{.Version}}' github.com/golangci/golangci-lint/v2`) and
handed explicitly to `golangci-lint-action@v8` (`lint.yml:26-29`). This is the right pattern:
`tools/go.mod` is the single source of truth and CI reads it live rather than hardcoding a
version. No dead surface, no coverage gap.

### `test.yml`

The `test` job (`test.yml:9-44`) runs `make test-integration GOTEST_FLAGS="-v -count=1
-shuffle=on"`, which does `go test -race --tags=integration ./...` against Postgres and
schema-registry containers (`Makefile:22-27`). `-tags=integration` only _adds_ files with that
build constraint; it does not exclude untagged packages, so this single `go test ./...` also runs
every unit test in `cmd/conduit/...` and `pkg/scaffold/...` — the CLI verbs, the MCP tool catalog,
and the Go scaffolding all execute here. `test.yml:33-44` adds a second, narrowly-scoped run of
the error-code contract tests specifically so a regression there is its own named, unmissable
check rather than buried inside the general `go test` output — a good pattern.

`build-cross` (`test.yml:46-63`) cross-compiles `linux/386`. This still matches what ships:
`.goreleaser.yml:6-12` builds `darwin`/`linux`/`windows` and only ignores `windows/386`, so
`linux/386` is a real release artifact and this guard is not stale.

`docker-build` (`test.yml:65-76`) builds the release image from the repo `Dockerfile`, which is
still `golang:1.25-bookworm` / `alpine:3.22` — matches `go.mod`'s `go 1.25.0`. Not stale.

**Gap — MCP transports are unit-tested, not exercised end-to-end.** The MCP tool catalog is
tested over an in-memory transport pair (`cmd/conduit/internal/mcp/testing_test.go:26-33`,
`sdkmcp.NewInMemoryTransports()`), and HTTP auth/TLS wiring is tested via an in-process handler
(`cmd/conduit/root/mcp/http_test.go:159`, `TestNewMCPHTTPHandler_EndToEndAuth`). Both are solid
unit coverage for tool logic and auth gating, but no CI step builds the actual `conduit` binary,
spawns `conduit mcp` as a real subprocess, and speaks JSON-RPC over its real stdin/stdout, or
spawns `conduit mcp --http` with a real token file and TLS cert and curls it. `docs/operations/
mcp-server.md:98-105` already documents this as a known Wave 3 limitation — "North-star E2E...
deferred pending the `llms.txt` generator" — so this is not a new discovery, but it does mean the
narrower claim ("does the binary actually speak the protocol over a real transport") is untested
today and is worth closing independently of the full north-star scenario. See recommendation R4.

**Non-gap — Python scaffolding, `llms.txt`.** Neither has code in the tree (confirmed by search:
only the design docs exist), so there is nothing for CI to be missing yet. Flagged for
follow-up once each lands (R5).

### `markdown-lint.yml`

Path-filtered to PRs touching `**.md` (`markdown-lint.yml:4-6`); it does not run on a direct push
to `main`. Given `CLAUDE.md`'s admin-merge path is maintainer-only and still requires the
tier-appropriate review, this is low risk but worth naming: a markdown change landed via
`--admin` merge without ever opening a PR would not be linted. Not a recommended change (PRs are
the norm), just a documented edge.

Uses `DavidAnson/markdownlint-cli2-action@v20` (`markdown-lint.yml:13`), which GitHub's release
notes confirm resolves to `markdownlint-cli2 v0.18.1` / `markdownlint v0.38.0`. The drift between
this and the local `make markdown-lint` target is the subject of Part 2.

**Dead exclusion.** `markdown-lint.yml:18` and `Makefile:86` both exclude `pkg/web/openapi/**`.
That directory does not exist — the OpenAPI assets moved to `pkg/http/openapi/` (which contains
`pkg/http/openapi/README.md`, confirmed to lint clean today, so this is not currently hiding a
failure). The exclusion is a fossil from the pre-move package layout, most likely dating to when
`pkg/web` held the UI-adjacent OpenAPI/Ember assets. It should be updated to `pkg/http/openapi/**`
or dropped; either way it currently does nothing, which is worse than either alternative because
it reads as intentional. `pkg/scaffold/template/**` (`markdown-lint.yml:20`, added since) is the
correct, current exclusion — it covers the vendored golden connector/processor template fixtures
under `pkg/scaffold/template/testdata/golden/**`, which intentionally violate several rules
(`MD026`, `MD033`, `MD041`, `MD046`, `MD047`) as part of being realistic scaffold output, not
project docs.

### `buf-push.yaml`, `buf-validate.yaml`, `buf-update.yaml`

All three still protect a live, actively-used surface: `proto/api/v1` defines the gRPC/HTTP
gateway API that `pkg/http`, `conduit doctor`'s `engine.reachable` check, and the MCP `inspect`
tool all dial into. Both `buf-push.yaml:16-24` and `buf-validate.yaml:18-26` resolve the `buf`
version from `tools/go.mod` the same way `lint.yml` resolves `golangci-lint` — the right pattern,
no drift risk on the CI side. `buf-update.yaml:16-19` runs `make install-tools proto-update`,
which is the pattern Part 2 recommends generalizing: install the pinned tool from `tools/go.mod`
immediately before using it, rather than assuming it is already on `PATH` at the right version.

### `validate-generated-files.yml`

Runs `make install-tools generate proto-generate` then fails on any resulting diff
(`validate-generated-files.yml:19-26`). This is the correct belt-and-suspenders check for
mocks, `stringer` output, `paramgen` config structs, and generated `.pb.go` files, and — like
`buf-update.yaml` — installs pinned tools from `tools/go.mod` before using them rather than
trusting `PATH`. No path filter means it runs on every push and PR including docs-only ones,
which costs a few minutes of CI time it doesn't need to; low priority given the size of the team,
but a cheap future win would be scoping it to Go source, `.proto`, and `go:generate` directive
changes.

### `dependabot-auto-merge-go.yml`

Still active and firing — the five most recent commits on `main` (`Go tools: Bump chi`, `Bump
quic-go`, `bump klauspost/compress`, `bump semver`, `bump go-plugin`) are exactly the kind of
patch/minor dependency bump this workflow auto-approves and auto-merges
(`dependabot-auto-merge-go.yml:17,27,35`). No relevance concern.

### `flake-hunt.yml`

Nightly (`flake-hunt.yml:16`, offset from `trigger-nightly.yml`'s schedule per the comment at
line 15) repeat of the test suite at `-count=3 -shuffle=on`, deliberately excluded from branch
protection (`flake-hunt.yml:9-10`) so a red run is a signal, not a merge blocker. This is a
sound pattern for a solo maintainer and directly answers the process-maturity gap in `CLAUDE.md`
("Chaos suite... by Phase 2") by giving cheap, present-day flake detection without pretending it's
the chaos suite. No changes recommended.

### `milestone-automation.yml`, `roadmap-automation.yml`

Both call reusable workflows in `ConduitIO/automation` to keep the GitHub project board in sync
with issue labels and milestones — project management, not code correctness, so they're outside
this audit's "does it test a surface that still exists" question by design. One oddity worth a
footnote: `milestone-automation.yml:5`'s cron `0 * * * 6` runs hourly, every Saturday, not weekly
as the workflow's apparent intent suggests — plausibly a deliberate retry-safety net rather than a
mistake, but worth a maintainer glance since it's outside this audit's code-relevance scope.

### `release.yml`

The workflow itself is sound: tag-triggered, runs GoReleaser then builds/pushes a multi-arch
Docker image (`release.yml:34-43,87-94`). But the GoReleaser config it drives asks for a Go build
tag that nothing in the tree consumes:

- `.goreleaser.yml:14-15` passes `tags: [ui]` to every release build. A repo-wide search for
  `//go:build ui` (and `+build ui`) returns zero matches — no file in the current tree is gated by
  that constraint. This is very likely a leftover from when the tag gated `go:embed`-ing compiled
  Ember UI assets into the binary; the UI and whatever it embedded are gone, but the release build
  still asks for a tag with no effect. Harmless (an unmatched build tag is a no-op), but it
  misleads a reader into thinking a `ui`-gated surface still exists in the shipped binary.
- Related fossil, same origin, not gated by any tag so it is _not_ harmless in the same way:
  `pkg/conduit/runtime.go:774,802-804` hardcodes `allowCORS(gwmux, "http://localhost:4200")` —
  port 4200 is Ember CLI's default dev-server port. It compiles into every build today, whether or
  not `tags: ui` is set, and permits CORS from an origin nothing in the org serves from anymore.

Neither breaks a build or a test today, so neither blocks this audit's recommendations, but both
are exactly the "tests/builds a removed... surface" pattern this audit was asked to flag. Filed as
a follow-up (R6), not fixed in this PR — cleanup of `runtime.go`'s CORS policy is a product/design
decision (what origin should the _future_ v0.18 UI be allowed from?), not a CI change.

### `trigger-nightly.yml`

Matches the documented state in `CLAUDE.md` ("the v0.15.0 nightly train is already running") and
`docs/releases.md:27-28`. Weekday-only cron, feeds `release.yml` via tag push, cleans up old
nightly tags/releases/containers. No stale-surface concerns.

### Redundancy and gap summary

- No workflow builds, tests, or lints a removed surface as its primary job. The only dead
  references found are the `pkg/web/openapi/**` exclusion glob (inert, not incorrect-in-effect
  today) and the GoReleaser `ui` build tag / hardcoded `:4200` CORS origin (inert-but-present,
  flagged above) — none of these cause a false pass or false fail today.
- The one real coverage gap against the current product is the MCP real-transport smoke test
  (R4) — everything else new (CLI verbs, Go scaffolding) is caught by `test.yml`'s unfiltered
  `go test ./...`.
- No matrix entries are stale (`build-cross` targets a real release artifact; Docker base images
  match `go.mod`).
- Minor efficiency-only redundancy: `validate-generated-files.yml` and (to a lesser extent)
  `lint.yml`/`test.yml` run unconditionally on doc-only PRs. Not worth path-filtering for a
  13-workflow, solo-maintained repo today — noted, not actioned.

## Part 2 — Local-vs-CI drift and a pre-push guard

### The concrete failure: markdownlint

CI runs `DavidAnson/markdownlint-cli2-action@v20` (`markdown-lint.yml:13`), which GitHub's release
notes for that tag confirm is `markdownlint-cli2 v0.18.1` bundling `markdownlint v0.38.0`. The
local target, `Makefile:84-86`, invokes whatever `markdownlint-cli2` binary is already on `PATH`
with no version pin at all:

```makefile
.PHONY: markdown-lint
markdown-lint:
    markdownlint-cli2 "**/*.md" "#LICENSE.md" "#pkg/web/openapi/**" "#.github/*.md"
```

A contributor (or agent) who installed `markdownlint-cli2` globally today gets `v0.23.0` /
`markdownlint v0.41.0` — three minor versions ahead, with rules CI doesn't have (for example
`MD060`). That produces drift in both directions: local failures on files that are actually
CI-clean (a newer rule fires locally that CI never checks), and — the more dangerous direction —
local passes on files that will fail in CI, if a contributor's global install happens to be
_older_ than CI's pin, or if a newer local rule masks something CI's older ruleset would flag
differently. Either way, "passed locally" stops meaning "will pass CI," which defeats the point of
running the linter locally at all.

### Recommended pin

Pin `markdownlint-cli2` to **`0.18.1`** (bundling `markdownlint 0.38.0`) in both places, via
`npx` so no new committed dependency file (`package.json`/lockfile) is needed for a single dev
tool:

```makefile
.PHONY: markdown-lint
markdown-lint:
    npx --yes markdownlint-cli2@0.18.1 "**/*.md" "#LICENSE.md" "#pkg/http/openapi/**" "#.github/*.md" "#pkg/scaffold/template/**"
```

and pin the CI action to the same release by SHA (belt-and-suspenders — `@v20` already resolves to
this version today, but a moving major tag can be repointed upstream; pinning the SHA makes the
version an explicit, reviewable diff instead of a silent upstream change):

```yaml
- uses: DavidAnson/markdownlint-cli2-action@<sha-for-v20.0.0>  # markdownlint-cli2 0.18.1 / markdownlint 0.38.0
  with:
    globs: |
      **/*.md
      !LICENSE.md
      !pkg/http/openapi/**
      !.github/*.md
      !pkg/scaffold/template/**
```

This also happens to fix the dead-exclusion finding from Part 1 (`pkg/web/openapi/**` →
`pkg/http/openapi/**`) in the same change, since both files need to be touched to add the pin —
worth doing together in the implementation PR rather than as two PRs.

`tools/go.mod` was considered as the single source of truth instead (mirroring how
`golangci-lint`/`buf` versions are resolved), but `markdownlint-cli2` is an npm package with no Go
module wrapper, so forcing it through `tools/go.mod` would mean vendoring a fake Go tool shim for
no benefit over a plain version-pinned `npx` invocation. `npx --yes <pkg>@<version>` already gives
exact-version reproducibility without a lockfile; the version string in the Makefile and the
version string in the CI step are the two things that must match, and both are now one `grep`
away from each other.

### Verification

Confirmed against the actual git-tracked doc set (57 files matching the corrected globs):
`npx --yes markdownlint-cli2@0.18.1` reports 0 errors on the current `main` tree, so pinning to
this version is not expected to newly break any existing, merged documentation. This document
itself was checked the same way before being committed (see PR description).

### Drift risk across other local-vs-CI checks

| Tool | CI version source | Local (`make`) version source | Drift risk |
| --- | --- | --- | --- |
| `golangci-lint` | `tools/go.mod`, read live (`lint.yml:20-24`) | `make lint` (`Makefile:44-46`) runs whatever `golangci-lint` is on `PATH` | **Safe if** `make install-tools` (`Makefile:65-68`, also reads `tools/go.mod`) ran first and `$(go env GOPATH)/bin` is early on `PATH`. **Drifts if** a different `golangci-lint` (e.g. a Homebrew install) shadows it on `PATH` — `make lint` does not verify the binary it finds matches `tools/go.mod`. |
| `buf` | `tools/go.mod`, read live (`buf-validate.yaml:20-26`, `buf-push.yaml:16-24`) | `make proto-lint`/`proto-generate` (`Makefile:53-58`) run `buf` directly, same "assumes `install-tools` already ran" caveat | Same shape and same fix as `golangci-lint` above. |
| Go toolchain | `actions/setup-go@v5` with `go-version-file: 'go.mod'` in every workflow that needs Go | `go.mod`'s `go 1.25.0` directive, checked by `scripts/check-go-version.sh` (wired into `make build` via `check-go-version`, `Makefile:8-9`) | **Safe.** Single source of truth (`go.mod`) on both sides, and the local check actively warns on a too-old toolchain rather than silently drifting — the strongest pattern in the repo. Note it is not wired into `make lint`/`make markdown-lint`, only `make build`. |
| Generate/proto-generate toolchain (`mockgen`, `stringer`, `paramgen`, `buf generate`) | `tools/go.mod`, installed live in `validate-generated-files.yml:19-24` via `make install-tools generate proto-generate` | `make generate`/`make proto-generate` (`Makefile:60-62,50-52`) — same commands, but only version-correct if `install-tools` ran first | **Safe by construction if run via the sequence CI uses** (`install-tools` then `generate`/`proto-generate`); the risk is the same "did you run `install-tools` first" gap as `golangci-lint`/`buf`, not a version-pin gap — `tools/go.mod` is already the single source of truth for all of these. |
| `markdownlint-cli2` | `DavidAnson/markdownlint-cli2-action@v20` → `0.18.1` (pinned by the action's tag, not by this repo) | `make markdown-lint` (`Makefile:84-86`) — no pin at all today | **Unsafe today** (this section's subject); fixed by the R1 pin above. |

The pattern that emerges: every Go-toolchain tool (`golangci-lint`, `buf`, `mockgen`, `stringer`,
`paramgen`) already has a correct single source of truth in `tools/go.mod`, and CI always installs
from it. The only thing missing on the local side is a habit/discipline problem, not a
version-pin problem: a contributor must run `make install-tools` before `make lint`/`make
proto-lint`/`make generate`, and nothing today enforces or even checks that they did. `npx`-based
`markdownlint-cli2` is the outlier because it has no `tools/go.mod` entry to anchor to, hence the
explicit version string recommended above.

### Pre-push guard design

Add one new `make` target, `make verify`, that runs the CI-equivalent checks in the same order CI
runs them, using the same tool-resolution mechanism CI uses (`tools/go.mod` for Go tools, the
pinned `npx` invocation for `markdownlint-cli2`) rather than trusting ambient `PATH` state:

```makefile
.PHONY: verify
verify: install-tools
    @echo "==> lint"
    @$(GOPATH_BIN)/golangci-lint run
    @echo "==> markdown-lint"
    @npx --yes markdownlint-cli2@0.18.1 "**/*.md" "#LICENSE.md" "#pkg/http/openapi/**" "#.github/*.md" "#pkg/scaffold/template/**"
    @echo "==> validate-generated-files"
    @$(MAKE) generate proto-generate
    @git diff --exit-code --numstat
    @echo "==> test (unit subset, no docker)"
    @go test -race -short ./...
    @echo "verify: all checks passed"
```

Design choices, and why:

- **`install-tools` as a prerequisite, not a separate step the contributor must remember** — this
  closes the exact gap identified above: today `make lint` silently trusts `PATH`; `make verify`
  does not.
- **`$(GOPATH_BIN)/golangci-lint` explicitly, not bare `golangci-lint`** — forces resolution of the
  binary `install-tools` just installed, sidestepping the `PATH`-shadowing risk called out in the
  drift table, rather than hoping `PATH` ordering happens to be right. (`GOPATH_BIN` would be
  defined once near the top of the `Makefile` as `$(shell go env GOPATH)/bin`, reused by
  `install-tools` too.)
- **`go test -race -short ./...` instead of full `make test-integration`** — the full integration
  suite needs Docker Compose (Postgres, schema registry) up, which is a reasonable ask in CI but
  friction-heavy for a fast pre-push check; `-short` gives quick unit-level feedback on the same
  packages `lint`/`markdown-lint` just touched. `verify` is explicitly a fast local gate, not a
  CI replacement — a contributor still gets full-fidelity results from CI on the actual PR, and
  `flake-hunt.yml` continues to own repeated/shuffled runs.
- **No new tool, no new config file** — everything `verify` calls already exists in the
  `Makefile`; it is a composition, not new machinery, which matters for a solo maintainer's
  ongoing maintenance burden.
- **Optional git pre-push hook**, not a mandatory one — add a documented, opt-in
  `scripts/hooks/pre-push` that runs `make verify` and a `make setup-hooks` target
  (`git config core.hooksPath scripts/hooks`) a contributor can enable once. Making it mandatory
  (auto-installed by `make build` or similar) would silently slow down every push for a solo
  maintainer who sometimes wants to push a WIP branch; opt-in respects that while still making the
  CI-equivalent check a one-command action (`make verify`) any agent session can run before
  proposing a PR is done, which is the actual ask in `CLAUDE.md`'s Definition of Done ("Compiles,
  lints clean, race detector clean... all currently-live tier-required suites pass").

This design is realistic for the stated solo-maintainer reality: one target, reusing
already-pinned tool versions, no CI changes required to benefit locally (`make verify` works
today against the _current_ mismatched `markdownlint-cli2`; it only becomes fully accurate once
R1's pin lands), and no new day-to-day ceremony beyond running `make verify` before opening a PR —
exactly what the adversarial-self-review step in `CLAUDE.md`'s PR process already asks an
AI-authored change to do.

## Decision — recommended action items

1. **R1 (do first, small, safe).** Pin `markdownlint-cli2` to `0.18.1` in `Makefile:84-86` (via
   `npx --yes markdownlint-cli2@0.18.1 ...`) and pin `markdown-lint.yml:13` to the SHA behind
   `v20.0.0`, updating the stale `pkg/web/openapi/**` exclusion to `pkg/http/openapi/**` in both
   places in the same PR. Verified against `main`: 0 new lint errors.
2. **R2.** Add `make verify` (design above) as the CI-equivalent local entry point; document it in
   `CONTRIBUTING.md` as the pre-PR check.
3. **R3.** Add the optional `make setup-hooks` / `scripts/hooks/pre-push`, opt-in, running
   `make verify`.
4. **R4 (follow-up, separate PR).** Add a smoke-level MCP transport test to `test.yml` (or a new
   workflow): build the `conduit` binary, spawn `conduit mcp` and do a real stdio JSON-RPC
   `initialize` + `tools/list` round trip; separately spawn `conduit mcp --http` with a throwaway
   token file and self-signed cert and confirm a bearer-authenticated request succeeds and an
   unauthenticated one is rejected over the wire (not just via the in-process handler test that
   exists today). This closes the "binary actually speaks the protocol" gap independent of the
   already-tracked, `llms.txt`-gated north-star E2E in `docs/operations/mcp-server.md:98-105`.
5. **R5 (deferred, no action now).** When Python scaffolding and `llms.txt` generation land, give
   each its own CI coverage: a Python test matrix analogous to `pkg/scaffold`'s golden-file tests
   for the former; a `validate-generated-files.yml`-style staleness check for the latter.
6. **R6 (deferred, product decision not a CI change).** File a cleanup issue for the two Ember-UI
   fossils found in Part 1: drop the inert `tags: [ui]` from `.goreleaser.yml:14-15`, and revisit
   the hardcoded `allowCORS(gwmux, "http://localhost:4200")` in `pkg/conduit/runtime.go:774` —
   the latter needs a maintainer decision about what origin (if any) should be allowed before the
   v0.18 greenfield UI exists to consume it, so it is out of scope for a CI-only change.

## Consequences

- **Positive:** a contributor or agent can run one command (`make verify`) and get a result that
  actually predicts CI's markdown-lint/lint/generated-files outcome, closing the exact failure
  mode that motivated this audit. The `markdownlint-cli2` pin is a two-file, low-risk change
  verified against the current tree before being proposed.
- **Neutral:** `make verify`'s `-short` test pass is intentionally narrower than full CI (no
  Docker-backed integration tests), so a green `make verify` is necessary but not sufficient for
  "this PR will pass CI" — it is a fast local signal, not a CI replacement. This is called out in
  `CONTRIBUTING.md` when R2 lands so the gap is not overclaimed.
- **Cost:** pinning `markdownlint-cli2` by exact version means a deliberate, reviewed bump (edit
  two files) whenever the team wants newer markdownlint rules, instead of silently picking up
  whatever's globally installed — that's the intended trade, not a regression.
- **No workflow YAML changes ship in this PR.** R1–R4 are recommendations with verified diffs
  sketched inline; implementing them is follow-up work once this audit is reviewed, per the task
  scope (review/plan only).

## Related

- `CLAUDE.md` — process-maturity table, PR review tiers, Definition of Done.
- `docs/operations/mcp-server.md` — existing, authoritative statement of the MCP north-star E2E
  limitation referenced in R4.
- `docs/design-documents/20260708-mcp-server.md` — MCP server design this audit's R4 extends.
- `docs/design-documents/20260707-connector-processor-scaffolding.md`,
  `docs/design-documents/20260707-python-connector-sdk.md` — scaffolding surfaces referenced in
  Part 1 and R5.
- `docs/design-documents/20260712-pipeline-dev-hot-reload.md` — confirms hot-reload is design-only
  today, referenced in Context.
- `docs/releases.md` — nightly/release process `trigger-nightly.yml`/`release.yml` implement.

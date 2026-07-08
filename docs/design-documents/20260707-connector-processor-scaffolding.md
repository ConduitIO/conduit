# CLI: `conduit connector new` / `conduit processor new` (plugin scaffolding)

## Summary

Scaffold a full connector/processor repo (SDK wiring, tests, CI, release workflow,
acceptance harness) from one command, targeting **first working connector < 30 min**
on a clean machine (a CI-measured gate, not a hope). v0.17 §5. **Go-first**: the Go
connector + processor paths are ready now; Python is blocked on a nonexistent SDK
and ships as an honest "not yet" error. Tier low–medium (Go).

## Feasibility verdict (blunt, per language)

| Target | SDK | Template | Verdict |
| --- | --- | --- | --- |
| Go connector | `conduit-connector-sdk` (mature) | `conduit-connector-template` (updated 2026-07-05) | **READY NOW** |
| Go processor | `conduit-processor-sdk` (mature, WASM/wazero) | `conduit-processor-template` (2026-07-05) | **READY NOW** |
| Python connector | **does not exist** | none | **BLOCKED** — SDK-authoring task, not scaffolding |
| Python processor | `conduit-processor-sdk-python` (experimental, WASM, year-stale) | none | not viable this phase |
| Rust / TS | — | — | v0.19 (correct) |

Python connectors would generate code against an SDK that does not exist. Keep it a
separate gated workstream (the Python connector SDK design doc); expose `--lang
python` today only as an explicit error naming the blocker + a tracking issue.

## Decision — orchestrate the upstream template, do NOT reimplement boilerplate

Option C: the command runs the canonical template's own setup flow.
- Rejected A (embedded/duplicated skeleton — guarantees drift) and B (`gh repo
  create --template` as primary — breaks offline/local-first; keep as `--remote`).
- The template `setup.sh` already encodes the recipe: module/token rename, tool
  install, `make generate`. Port that logic to **Go** (not shell — Windows +
  determinism).

**Template sourcing (the core tension: reproducible vs catch-drift):**
- **Vendored snapshot, `go:embed`'d, pinned per Conduit release** — default
  generation is offline, deterministic, reproducible. A release-time `//go:generate`
  script machine-syncs the snapshot from upstream `main` and records the SHA (a
  lockfile, not a hand-maintained fork).
- `--template-ref` / `--template-repo` override to fetch live (power users + the
  nightly drift job).
- **Nightly CI** regenerates from upstream `main` + latest SDK, builds/tests →
  drift goes red before users hit it.

## CLI UX & ergonomics (the <30-min experience)

```
conduit connector new [name] [flags]
conduit processor new [name] [flags]
```

Flags: `--lang` (go; python→gated error), `--module`
(`github.com/<gh-user>/conduit-connector-<name>`), `--path`, `--sdk-version`,
`--template-ref/-repo`, `--git/--no-git`, `--remote <owner/repo>`,
`--skip-generate`, `--yes/-y`, `--json`, `--force`.

TTY + no `--yes` → guided prompts for only what's missing (name → module →
destination) then a confirm summary. `--yes`/`--json`/non-TTY → fully
flag-driven; missing input → deterministic error + non-zero exit.

**Preflight runs before any write** (shared with `conduit doctor`'s toolchain
checks): Go on PATH + min version, `GOPATH/bin` writable, `git`, `docker`
(warn-only). On failure, fail fast with remediation + a distinct exit code, leaving
no partial directory.

**Felt experience** — stream progress, verify a real `go build` in-flow, show a
wall-clock timer + copy-paste next steps:

```
conduit connector new s3
✓ Toolchain OK (go 1.23.4, git 2.44)
✓ Wrote 24 files → ./conduit-connector-s3
✓ Rewrote module path → github.com/devaris/conduit-connector-s3
✓ Generated connector.yaml + README
✓ go build OK (12s)
✓ Initialized git, created first commit
Your connector is ready at ./conduit-connector-s3  (48s)
Next steps:
  cd conduit-connector-s3
  make test     # unit + SDK acceptance suite
  make build    # standalone plugin binary
  conduit run   # wired into a pipeline
```

`--json`: `{language, name, module, path, template_ref, sdk_version,
steps:[{name,ok,duration_ms}], elapsed_ms, next_steps[]}`; errors →
`{error:{code,message}}` + matching non-zero exit.

## File-level plan

- New commands: `cmd/conduit/root/{connectors,processors}/new.go` (+ tests),
  registered in each group's `SubCommands()`.
- New reusable engine `pkg/scaffold/` (out of `cmd/` so the future MCP `scaffold`
  tool shares it): `scaffold.go` (orchestrator), `request.go`, `doctor/` (toolchain
  preflight — consume `pkg/conduit/doctor` toolchain checks, don't fork),
  `template/` (`source.go`, `rewrite.go` = the setup.sh→Go port, `embed.go` +
  `templates/` machine-synced snapshots), `steps/` (generate, verified build,
  gitinit), `sync/` (release-time refresh).
- CI/release/acceptance are inherited verbatim from the template — the command adds
  ZERO CI authoring, only rewrites the module token.

## Acceptance criteria

1. `connector new --lang go s3 --yes` → exit 0, `./conduit-connector-s3` that
   `go build ./...` compiles with no edits.
2. generated `make test` passes incl. the SDK acceptance suite.
3. `make build` → standalone plugin binary Conduit **loads and runs** in a pipeline
   (lifecycle methods invoked) — validates protocol wiring, not just compilation.
4. generated CI green on first commit (test/lint/validate-generated-files).
5. processor parity: AC-1/2/4 for `processor new --lang go`; `make build` → a
   wazero-loadable WASM module Conduit runs.
6. preflight catches missing Go → exits non-zero BEFORE writing, remediation
   printed, no partial dir.
7. `--json` schema honored; failure → `{error}` + deterministic non-zero exit.
8. `--lang python` → honest non-zero "not yet (no Python connector SDK)" + issue link.
9. Windows: once the upstream matrix PR lands (§ below), generated `test.yml` green
   on `windows-latest` (unit).
10. **<30 min, measured in CI**: a matrix job (ubuntu/macos/windows × connector/
    processor) on a runner with NO preinstalled dev tooling times
    `new → make test → make build → smoke-run in a pipeline`; assert `< 1800`s
    (hard) + `< 600`s (warning, to catch creep). Publish the duration per OS.

## Failure modes / dependencies

- Missing toolchain → preflight fail before write.
- tool-install fails → clear error + `--skip-generate` escape hatch.
- destination exists → refuse; `--force` to overwrite (never silent clobber).
- partial write then crash → write to temp dir, atomic rename, cleanup on error.
- Windows path/EOL → Go port of setup.sh (no bash/sed) + the win-CI leg proves it.
- **Upstream gap:** both templates' `test.yml` are `ubuntu-latest` only. "Windows in
  the matrix" needs coordinated PRs to `conduit-connector-template` +
  `conduit-processor-template` (add a `windows-latest` unit-test leg) — NOT an
  in-repo edit (would diverge the snapshot). Track as a dependency of §5 acceptance.

## Optimization

The command orchestrates; SDK wiring, CI, release, goreleaser, dependabot, and the
acceptance harness all come from the upstream templates + SDKs unmodified. Original
code = the two `new` subcommands, the language-agnostic engine, the setup.sh→Go
port, the preflight, and the drift/measurement CI. The embedded snapshot is
machine-synced, never hand-maintained.

## Related

- Execution plan §5. Shares toolchain preflight with the `doctor` design doc.
- Python connector SDK design doc (the blocker for `--lang python`).
- Feeds the MCP `scaffold` tool (§2, shared engine).

## Review outcome (2026-07-07) — SOUND-WITH-CONCERNS

Must-fix during implementation:
- **connector ≠ processor symmetry**: the PROCESSOR template's `setup.sh` does ONLY
  the rename — it does NOT `install-tools`/`generate` like the connector one, and
  `make generate` uses different tools (`conn-sdk-cli specgen` vs `paramgen`). The
  Go port must synthesize the processor install+generate steps itself.
- Error JSON must carry `suggestion`/`configPath`/`fix` (per output conventions),
  not a bare `{code,message}`.
- Route all exits through `pkg/conduit/exitcode` (missing toolchain → 3; bad
  `--module`/`--lang python`/dest-exists → 2; `go build` fail → 1).
- Toolchain preflight uses the shared `pkg/conduit/check` engine (see ADR).
- Windows-CI-green (AC-9) is an EXTERNAL dependency (upstream template PRs), tracked
  separately — not this repo's own deliverable. Declare risk tier **Tier 2**.

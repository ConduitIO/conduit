# Generate llms.txt and llms-full.txt from source with a CI drift-guard

## Summary

Conduit ships no `llms.txt` today. This design specifies a **deterministic Go generator** that
emits repo-root `llms.txt` (a concise, spec-conformant index) and `llms-full.txt` (the same index
with the full detail inlined) from four in-repo sources of truth: the pipeline config schema, the
built-in connector registry, the `ConduitError` code registry, and the MCP tool catalog. The
generated files are **committed to the repo**; CI regenerates them and fails on any `git diff`,
reusing the existing `validate-generated-files` job and the `make generate` (`go generate ./...`)
mechanism verbatim. Nothing about the catalog is ever hand-maintained.

This is a **design/plan only** — no generator code, no `llms.txt`, no CI change is implemented in
this PR. It advances the v0.17 agent-native line, execution plan §2:

> `llms.txt` + `/llms-full.txt` regenerated in CI from source (config schema, connector list, error
> registry, MCP tool catalog) — never hand-maintained.
> — `docs/design-documents/20260704-phase-1-execution-plan.md:191`

Two hooks for this feature already exist in the code and are called out by name:

- `cmd/conduit/internal/mcp/server.go:24-28` — the tool-name `const` block is exported specifically
  so "a future llms.txt tool-catalog generator, design doc §6" has one source of truth.
- `pkg/foundation/cerrors/conduiterr/conduiterr.go:83-84` — `Codes()` is documented as the hook
  "used to generate the error-code reference for docs, llms.txt, and the UI."

## Context

### Problem / use-case

Agents (and the `conduit generate "<nl>"` command slated for v0.19, which needs llms.txt as
grounding — execution plan §195) consume `llms.txt`/`llms-full.txt` to answer: what config schema
does a pipeline use, which connectors are built in, what error codes can I get and how do I fix
them, and what MCP tools exist. If these files are hand-written they rot the moment a connector is
added, a config field is introduced, an error code is registered, or an MCP tool is renamed —
producing an agent that confidently fabricates a plugin name or misreads an error. The only durable
fix is to derive the files from the same declarations the engine already uses, and to fail CI when
the committed files drift from those declarations.

### Confirmed current state (grounding)

- **No `llms.txt` / `llms-full.txt` exist** anywhere in the tree (verified: `find . -iname
  'llms*.txt'` returns nothing). **No generator exists** for them.
- **No existing schema/doc emitter** for the config structs (searched: no `jsonschema`/`invopop`
  schema generation over the config packages).

### Source #1 — Pipeline config schema

- Parser + version dispatch: `pkg/provisioning/config/yaml/parser.go`. `const LatestVersion =
  v2.LatestVersion` (`parser.go:36`); version-major switch in `Parser.parseConfiguration`
  (`parser.go:253`) dispatches to `v1.Configuration` or `v2.Configuration`; changelog list
  `var changelogs = []internal.Changelog{v1.Changelog, v2.Changelog}` (`parser.go:39`).
- Latest schema structs (source of truth for YAML shape), `pkg/provisioning/config/yaml/v2/model.go`:
  `LatestVersion = "2.2"`, `MajorVersion = "2"` (`model.go:22-25`); `Configuration` (`:67`),
  `Pipeline` (`:72`, DLQ field tagged `dead-letter-queue` at `:79`), `Connector` (`:82`),
  `Processor` (`:91`), `DLQ` (`:100`, fields `window-size`, `window-nack-threshold`). Fields carry
  both `yaml:` and `json:` tags. `v2.Changelog` (`model.go:29-65`) keys `"2.0"`, `"2.1"`, `"2.2"`
  drive introduced/deprecated field annotations.
- Legacy schema retained: `pkg/provisioning/config/yaml/v1/model.go` (`LatestVersion = "1.1"`,
  `MajorVersion = "1"`, `model.go:23-24`).

### Source #2 — Built-in connector list

- Registry: `pkg/plugin/connector/builtin/registry.go:39-49`,
  `var DefaultBuiltinConnectors = map[string]sdk.Connector{...}` — **6 connectors today**: `file`,
  `generator`, `kafka`, `log`, `postgres`, `s3` (map key = Go module import path).
- Programmatic spec: each `sdk.Connector` value exposes `NewSpecification()`; the registry also
  offers `Registry.List() map[plugin.FullName]pconnector.Specification` (`registry.go:223`).
  `pconnector.Specification` (protocol `v0.9.5`, `pconnector/specifier.go:39`) fields: `Name`,
  `Summary`, `Description`, `Version`, `Author`, `SourceParams`, `DestinationParams`. Params are
  `config.Parameters map[string]Parameter` (commons `v0.6.0`, `config/parameter.go:20`) with
  `Parameter{Default, Description, Type, Validations}` and `ParameterType` ∈ {String, Int, Float,
  Bool, File, Duration}.
- **Version caveat (determinism-critical):** the registry overwrites `Specification.Version` from
  `debug.BuildInfo` via `getModuleVersion` (`registry.go:168-176`). `debug.BuildInfo.Deps` is
  populated for built binaries but can be **empty under `go generate`/`go run`/`go test`**, which
  would make the emitted connector version non-deterministic across contexts. See Determinism and
  Adversarial review — the generator must not depend on `debug.BuildInfo`.

### Source #3 — ConduitError code registry

- Single source of truth: `var registry = map[string]Code{}`
  (`pkg/foundation/cerrors/conduiterr/conduiterr.go:58`), populated only via
  `Register(reason, grpcCode)` (`:69`). `Code` is `struct{reason string; grpcCode codes.Code}`
  (`:41`). `func Codes() []Code` (`:86`) returns **every registered code, sorted by reason** — the
  generator hook. The user-facing `ConduitError{Code, Message, Suggestion, Fix}` (`:124`) is where
  suggestions/fixes live.
- **40 codes today**, registered across 12 files (6 in `conduiterr.go`, plus `codes.go` in
  `pkg/pipeline`, `pkg/processor`, `pkg/connector`, `pkg/orchestrator`, `pkg/provisioning`,
  `pkg/provisioning/config`, `pkg/scaffold`, `pkg/lifecycle-poc`, `conduiterr/environment.go`, and
  two in `cmd/conduit/internal/deploy`).
- **Init-order caveat (completeness-critical):** `Codes()` only returns codes whose declaring
  package has been imported (init ran). The generator must import every code-declaring package or it
  silently under-reports. See Adversarial review for the guard against this.
- Existing CI **error-code guard** (a precedent, not this drift-guard): `.github/workflows/test.yml`
  step `Error-code guard (execution plan §1.1)` runs
  `go test ./pkg/http/api/status/... -run 'Codes|Registry' -v -race` (`test.yml:33,44`). It checks
  that boundary errors carry a registered reason — it does **not** check llms.txt.

### Source #4 — MCP tool catalog

- `cmd/conduit/internal/mcp/server.go:29-39` — tool-name `const` block; **9 tools**: `validate`,
  `lint`, `dry_run`, `doctor`, `deploy`, `inspect` (always registered, read tools) and `apply`,
  `scaffold_connector`, `scaffold_processor` (registered only when `cfg.AllowMutations`, write
  tools). Registration is `sdkmcp.AddTool(srv, &sdkmcp.Tool{Name, Description}, handler)` inside
  `NewServer` (`:115`, `:125-171`); read/write split mirrored in `server_test.go:29,31`.
- **Description caveat:** each tool's `Description` string lives inline in the `AddTool` literal and
  is **not exported**. The generator needs name + description + read/write class. See the proposed
  `mcp.Catalog()` refactor below so registration and generation read one list.

### CI drift-guard to mirror (exactly)

- Job: `.github/workflows/validate-generated-files.yml`, job `validate-generated-files`, step
  `Check generated files`:

  ```console
  export PATH=$PATH:$(go env GOPATH)/bin
  make install-tools generate proto-generate
  git diff
  git diff --exit-code --numstat
  ```

  The final `git diff --exit-code --numstat` is the guard: non-zero exit if regeneration changed any
  tracked file.
- `make generate` = `go generate -x ./...` (`Makefile:73-75`). Generated Go files sit beside their
  source, carry a `// Code generated ...; DO NOT EDIT.` header (e.g.
  `pkg/pipeline/status_string.go:1`), and are produced by `//go:generate` directives in-package.

## Decision

### D1 — Deterministic Go generator, run by `go generate ./...`

Add a generator package `cmd/conduit/internal/llmsgen/` (an internal `main` package). A single
`//go:generate` directive drives it so it is picked up by the **existing** `make generate` target
with **zero new CI wiring**:

```go
// cmd/conduit/internal/llmsgen/generate.go
//go:generate go run github.com/conduitio/conduit/cmd/conduit/internal/llmsgen
package main
```

The generator writes two files to the repo root: `llms.txt` and `llms-full.txt`. Both begin with a
`<!-- Code generated by cmd/conduit/internal/llmsgen; DO NOT EDIT. -->` header line so the "do not
edit by hand" convention is explicit and matches the repo's generated-file marker style.

Why this location: the generator must import internal packages from three trees — the MCP catalog
(`cmd/conduit/internal/mcp`), the connector registry (`pkg/plugin/connector/builtin`), the config
models (`pkg/provisioning/config/yaml/v2` + `v1`), and every code-declaring package
(`pkg/.../codes.go`). Only a package inside the main module can import all of these. Placing it
under `cmd/conduit/internal/` matches where MCP already lives and keeps it unexported.

### D2 — Read each source through its declared API, never a copy

- **Connectors:** iterate `builtin.DefaultBuiltinConnectors`, and for each call
  `conn.NewSpecification()` directly. Do **not** build dispensers and do **not** call the
  registry's `getSpecification`/`getModuleVersion` path — that path reads `debug.BuildInfo` (empty
  under `go generate`) and would introduce nondeterminism. Connector version comes from the
  connector's own spec (`Specification.Version`); if that is unset, emit the pinned version parsed
  from `go.mod`'s `require` line for the connector module (deterministic, file-derived), never from
  build info.
- **Error codes:** call `conduiterr.Codes()` (already sorted by reason). Blank-import every
  code-declaring package (see D5 guard).
- **MCP tools:** consume a new exported `mcp.Catalog() []mcp.ToolInfo` (D4).
- **Config schema:** reflect over the `v2` structs (`Configuration`, `Pipeline`, `Connector`,
  `Processor`, `DLQ`), reading `yaml` tags for field names, Go types for field types, and the
  `v2.Changelog` map for the version list. Emit a "legacy v1" note from `v1.model.go`. Reflection (vs
  a hand-list) is required so a new field can't be added without appearing in the output.

### D3 — Determinism (the diff-guard's correctness depends on this)

The output must be byte-identical on every run in every environment. Rules, enforced by tests:

- **Sort everything that comes from a map.** `DefaultBuiltinConnectors` is a `map` → sort connectors
  by `Name`. Per-connector `SourceParams`/`DestinationParams` are `map`s → sort params by key. The
  error `registry` is a `map` → `Codes()` already sorts by reason; re-sort defensively. Config struct
  fields are emitted in declaration order via reflection (stable). MCP tools sorted by name.
- **Zero timestamps, zero randomness, zero host/user/path data.** No `time.Now`, no build date, no
  absolute paths, no hostname, no version of Go, no `debug.BuildInfo`. A negative grep test asserts
  none of these substrings appear.
- **Fixed formatting:** LF line endings, UTF-8 without BOM, exactly one trailing newline, spaces not
  tabs inside the markdown, `%v`-free deterministic rendering of types/defaults. Durations and
  numbers rendered by explicit formatters, not locale-dependent ones.
- **Stable derived values:** connector versions from `go.mod` (a committed file), not build info;
  gRPC codes rendered by their stable string name.
- Add a `.gitattributes` entry `llms*.txt text eol=lf` so a CRLF contributor checkout cannot cause a
  false-positive diff.

### D4 — `mcp.Catalog()` — one list feeding both registration and generation

Refactor `cmd/conduit/internal/mcp/server.go` so the nine `AddTool` calls and the generator read the
same slice. Introduce:

```go
type ToolInfo struct {
    Name        string // == existing Tool* const
    Description string
    Mutates     bool   // true => registered only under AllowMutations
    // InputSchemaRef: a stable, human-readable description of the argument struct
}

func Catalog() []ToolInfo // sorted by Name; the single source of truth
```

`NewServer` iterates `Catalog()` to register (respecting `Mutates` vs `AllowMutations`), so a new
tool is added in exactly one place and cannot exist in the server but be missing from llms.txt (or
vice-versa). This is a small Tier-2 refactor local to the MCP package; behavior is unchanged and is
covered by the existing `server_test.go` read/write split assertions.

### D5 — Import-completeness guard for the error registry

Because `conduiterr.Codes()` only sees imported packages, add a test (in the generator package or
`pkg/http/api/status`) that builds the **full** set of registered codes by importing the whole
engine and asserts the generator's own import set yields the same count/reasons. Concretely: the
generator imports an internal aggregation package (e.g. `pkg/foundation/cerrors/conduiterr/allcodes`
— a blank-import barrel of every `codes.go`), and a test asserts that barrel's `Codes()` equals the
count produced by a broad `./...` build. If someone adds a new `pkg/foo/codes.go` and forgets to add
it to the barrel, the test fails. This makes source-completeness enforceable, not aspirational.

### File structure

#### `llms.txt` (concise index — llmstxt.org conformant)

```text
<!-- Code generated by cmd/conduit/internal/llmsgen; DO NOT EDIT. -->
# Conduit

> Broker-neutral data streaming engine and Kafka Connect replacement. Pipelines move records from
> source connectors to destination connectors with processors in between; any-language plugins over
> gRPC/WASM; embeddable; agent-legible with stable error codes and an MCP server.

## Pipeline configuration
- [Config schema (v2, latest 2.2)](llms-full.txt): version, pipelines, connectors, processors,
  dead-letter-queue. Legacy v1 (1.1) still parsed.

## Built-in connectors
- file — <summary>
- generator — <summary>
- kafka — <summary>
- log — <summary>
- postgres — <summary>
- s3 — <summary>
(6 built-in connectors; full parameters in llms-full.txt)

## Error codes
- 40 stable error codes, dotted `domain.name` reasons, each with a gRPC status class. Full list with
  descriptions in llms-full.txt.

## MCP tools
- Read: validate, lint, dry_run, doctor, deploy, inspect
- Write (require --allow-mutations): apply, scaffold_connector, scaffold_processor
(9 tools; full schemas in llms-full.txt)

## Full detail
- [llms-full.txt](llms-full.txt)
```

The blockquote summary text is a committed constant in the generator (the one piece of curated
prose); every list is derived. One `#` H1, blockquote summary, `##` sections of links/notes — the
spec shape.

#### `llms-full.txt` (expanded — everything inlined)

Same header + H1 + blockquote, then:

1. **Pipeline configuration schema** — version list from `v2.Changelog` (2.0/2.1/2.2) + latest
   marker; every field of `Configuration`/`Pipeline`/`Connector`/`Processor`/`DLQ` with its YAML key,
   Go type, and whether required; the `dead-letter-queue` / `window-size` / `window-nack-threshold`
   keys spelled exactly; a "legacy v1 (1.1)" subsection noting differences.
2. **Built-in connectors** — for each of the 6 (sorted): name, version, summary, description,
   author, then **source parameters** and **destination parameters** as sorted tables of
   `key · type · default · required · description · validations`.
3. **Error codes** — for each of the 40 (sorted by reason): reason, gRPC status class, and the
   human description (from the code's doc/registration context).
4. **MCP tool catalog** — for each of the 9 (sorted): name, read/write class + mutation-gate note,
   description, and input-schema summary.

### Makefile + CI wiring

- **Makefile:** no new required target — the `//go:generate` directive makes `make generate` (which
  runs `go generate -x ./...`, `Makefile:73-75`) produce both files. Optionally add a convenience
  alias `make generate-llms` (`.PHONY`) that runs
  `go run ./cmd/conduit/internal/llmsgen` for local iteration; it is not on the CI path.
- **CI:** **no change to `validate-generated-files.yml`.** Its existing
  `make install-tools generate proto-generate` already runs the new directive, and the existing
  `git diff --exit-code --numstat` already fails on drift for the two new committed files. This is
  the "mirror the mechanism exactly" requirement met by reuse, not duplication.
- **Process-maturity honesty:** this rides an already-live gate (`validate-generated-files`), so no
  new gate row is added to the CLAUDE.md maturity table — the drift-guard is live the moment the
  files are committed and the directive lands.

### Detailed acceptance criteria (testable checklist)

Determinism / drift-guard:

- [ ] **AC-1 (idempotent):** running `make generate` twice with no source change leaves
      `git diff --exit-code` clean — `llms.txt` and `llms-full.txt` are byte-identical on the second
      run.
- [ ] **AC-2 (guard catches new connector):** adding an entry to
      `builtin.DefaultBuiltinConnectors` without running `make generate` makes the
      `validate-generated-files` job fail (`git diff --exit-code --numstat` non-zero). Verified by a
      test that stages a fake connector and asserts the regenerated output differs from committed.
- [ ] **AC-3 (guard catches new error code):** registering a new `conduiterr.Register(...)` without
      regenerating fails the guard.
- [ ] **AC-4 (guard catches MCP tool change):** adding/renaming a tool in `mcp.Catalog()` without
      regenerating fails the guard.
- [ ] **AC-5 (guard catches config field):** adding a field to a `v2` config struct without
      regenerating fails the guard.
- [ ] **AC-6 (guard catches hand edits):** a hand edit to committed `llms.txt` that the generator
      would not reproduce is caught, because CI regenerates and diffs.
- [ ] **AC-7 (no nondeterministic tokens):** a grep test asserts neither file contains a timestamp,
      date, absolute path, hostname, Go version, or `debug.BuildInfo`-derived string.
- [ ] **AC-8 (encoding):** both files are UTF-8, no BOM, LF endings, single trailing newline.

Completeness of the four sources:

- [ ] **AC-9 (all connectors):** `llms.txt` lists exactly `len(DefaultBuiltinConnectors)` connectors
      (6 today) and `llms-full.txt` inlines each; a test compares the emitted set to
      `DefaultBuiltinConnectors` keys.
- [ ] **AC-10 (all params):** for each connector, every key in `SourceParams` and
      `DestinationParams` appears in `llms-full.txt` with type + default.
- [ ] **AC-11 (all error codes):** every reason in `conduiterr.Codes()` appears in `llms-full.txt`
      (set equality; 40 today) and the count line in `llms.txt` equals `len(Codes())`.
- [ ] **AC-12 (registry init completeness):** the D5 barrel guard asserts the generator's imported
      code set equals the whole-engine code set — no `codes.go` can be silently omitted.
- [ ] **AC-13 (all MCP tools):** every name in `mcp.Catalog()` (9 today) appears in both files with
      the correct read/write classification; read set == `server_test.go` `readToolNames`, write set
      == `writeToolNames`.
- [ ] **AC-14 (config schema):** every exported field of the five `v2` structs appears in
      `llms-full.txt` with its exact YAML key (including `dead-letter-queue`, `window-size`,
      `window-nack-threshold`); the version string `2.2` and the v1 `1.1` note are present.

Spec conformance:

- [ ] **AC-15:** `llms.txt` has exactly one `#` H1, a single blockquote summary immediately after,
      and `##` sections thereafter (llmstxt.org shape); a structural test parses and asserts this.
- [ ] **AC-16:** both files start with the `DO NOT EDIT` generated-file header.

### Golden-file test plan

The committed `llms.txt`/`llms-full.txt` **are** the golden files (they live at repo root, not under
`testdata/`). Tests:

1. **Round-trip against committed output** (`llmsgen_test.go`): run the generator into an in-memory
   buffer and assert byte-equality with the committed file. This is the same drift-guard, runnable as
   a unit test for fast local feedback (fails before you even push).
2. **Twice-is-identical**: generate into two buffers, assert equal (AC-1).
3. **Formatter fixtures** under `cmd/conduit/internal/llmsgen/testdata/`: feed the rendering
   functions synthetic inputs (a fake `Specification` with two params of different types; a fake
   `ToolInfo`; a fake config struct) and assert exact-string golden output for each renderer, so
   formatting regressions are localized and readable. These small goldens are updated via a
   `-update` flag guarded by `testing.Short()`.
4. **Completeness assertions** (AC-9…AC-14) as table tests comparing emitted sets to the live source
   APIs, so they can never pass while under-reporting.
5. **Negative determinism grep** (AC-7) over both committed files.

### Failure modes

- **Nondeterministic connector version** (build-info empty under `go generate`) → false-positive CI
  diffs. Mitigated by D2/D3: read versions from `go.mod`, never `debug.BuildInfo`.
- **Map iteration order** → nondeterministic ordering. Mitigated by D3 sorting + AC-1/AC-7 tests.
- **Under-reported error codes** (unimported `codes.go`) → silently incomplete llms.txt. Mitigated by
  the D5 barrel and AC-12.
- **MCP description drift** (server has a tool the generator can't see) → Mitigated by D4's single
  `Catalog()` and AC-13.
- **Generator panics at generate time** (e.g. `NewSpecification` on a misconfigured connector) →
  breaks `make generate` for everyone. Mitigated by calling `NewSpecification()` directly (no
  dispenser/runtime) and by a smoke test that runs the full generator in CI unit tests.
- **CRLF checkout** → false-positive diff. Mitigated by `.gitattributes eol=lf` (D3).

### Upgrade / rollback

- Additive: introduces two generated files, one internal generator package, and a small `mcp.Catalog`
  refactor. No public contract changes (config schema, protocol, error codes are only _read_).
- Rollback: delete the two files, the generator package, the `//go:generate` directive, and revert
  the `mcp.Catalog` refactor; CI reverts automatically since the directive is gone. No migration, no
  serialized-state impact.
- Forward compat: when connectors/codes/tools/config grow, the files regenerate; the drift-guard is
  the forcing function. If llms.txt structure itself changes, that's a generator change with its own
  golden update in the same PR.

### Observability

Generation is a build-time step, not a runtime path — the observable signal is the CI job status.
The `validate-generated-files` job going red **is** the alert ("someone changed a source of truth
without regenerating"); the job log prints `git diff` before failing, so the drift is visible inline.

## Consequences

### Adversarial self-review

- **Determinism — is the output really stable?** The only non-obvious nondeterminism sources are (a)
  Go map iteration over connectors/params/registry — addressed by mandatory sorting (D3) and AC-1;
  (b) connector version from `debug.BuildInfo`, which is empty under `go generate` but populated in a
  built binary — the single most likely cause of "green locally, red in CI" — explicitly designed out
  by sourcing versions from `go.mod` (D2). AC-7 greps for the classic offenders. Residual risk:
  locale-dependent number/duration formatting; mitigated by explicit formatters, and AC-1 would catch
  any leak.
- **Completeness of the four sources — can a source silently under-report?** Yes, twice, and both are
  designed against: error codes require every `codes.go` to be imported (D5 barrel + AC-12), and MCP
  descriptions aren't exported (D4 `Catalog()` + AC-13). Connectors and config fields are read
  reflectively from the live declarations, so they can't drift without changing the output. The
  count-line assertions (AC-9, AC-11, AC-13) turn "did we get them all" into a numeric test, not a
  reviewer's eyeball.
- **Does the guard actually catch drift?** The guard is the _already-live_
  `validate-generated-files` `git diff --exit-code --numstat`. It catches: a new connector (AC-2), a
  new error code (AC-3), a renamed/added MCP tool (AC-4), a new config field (AC-5), and even a raw
  hand edit to the committed file (AC-6, because CI regenerates from source and diffs). The one thing
  a diff-guard cannot catch is a change that is _invisible to the generator_ — e.g. a code registered
  in a package the generator doesn't import (D5 closes this) or an MCP tool the generator can't
  enumerate (D4 closes this). With those two closed, every source-of-truth change is either reflected
  in the output or fails a test.
- **Scope creep check:** this stays a read-only generator over existing declarations plus one small
  MCP refactor. It adds no new config surface, no runtime behavior, no protocol change, no new CI job.
  The `--json`/error-code/CLI conventions don't apply (no CLI surface added). The only new public Go
  symbol is `mcp.Catalog()`, justified by removing description duplication.

### Risk tier

**Tier 3 (chore/docs-tooling)** for the generator + CI wiring: no data path, no public contract, no
serialized format. The `mcp.Catalog()` refactor is **Tier 2** (feature-adjacent, one reviewer,
existing tests green). Neither is Tier 1 — nothing here touches ack/position/checkpoint/protocol.

## Related

- `docs/design-documents/20260704-phase-1-execution-plan.md` §2 (agent-native), AC at line 191.
- `docs/design-documents/20260708-mcp-server.md` (MCP tool surface this catalogs).
- `docs/design-documents/20260705-conduit-error-and-structured-output.md` (the `ConduitError` /
  code-registry model this reads).
- Code hooks: `cmd/conduit/internal/mcp/server.go:24-39`,
  `pkg/foundation/cerrors/conduiterr/conduiterr.go:58,86`,
  `pkg/plugin/connector/builtin/registry.go:39-49`,
  `pkg/provisioning/config/yaml/v2/model.go:22-105`.
- CI mechanism mirrored: `.github/workflows/validate-generated-files.yml`, `Makefile:73-75`.

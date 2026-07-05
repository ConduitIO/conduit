# ConduitError and structured output

_Status: draft. This is the v0.16 foundation linchpin — the MCP server, `--json`
on every command, the React UI's error rendering, and `conduit generate` all
consume what this defines. Build it once, correctly, first._

## Problem

Conduit has no uniform, machine-actionable error model, and no uniform structured
output. Concretely:

- **Errors carry no stable identity.** `pkg/foundation/cerrors` is a thin
  forwarding layer (`New`, `Errorf`, `Is`, …) over `xerrors`/stdlib — no code, no
  type. Identity today is ~expressed as Go sentinel errors (`pipeline.ErrInstanceNotFound`,
  `connector.ErrInvalidConnectorType`, …) matched with `cerrors.Is`. There are
  ~720 `cerrors.Errorf`/`New` and ~94 `fmt.Errorf` construction sites.
- **The only "codes" are gRPC codes, mapped by hand.** `pkg/http/api/status/status.go`
  switches on sentinel errors to a `google.golang.org/grpc/codes.Code`
  (`InvalidArgument`, `NotFound`, …), defaulting to `codeFromError`. The HTTP
  gateway renders the standard `google.rpc.Status` JSON. So an API consumer gets a
  numeric gRPC code + a prose string — no stable string code, no failing config
  path, no suggested fix.
- **CLI output interleaves logic and formatting.** Commands (on the `ecdysis`
  framework) fetch, then format-and-print inline — e.g. `list.go` calls
  `c.output.Stdout(getPipelinesTable(resp.Pipelines))`. There is no `Result` value
  to render two ways, so `--json` cannot be added uniformly.
- **Agents and the UI are first-class consumers now.** An MCP tool or the React UI
  needs `{ code, configPath, suggestion }` to self-correct or render an actionable
  message. Prose strings force re-parsing and guesswork.

This is the CLAUDE.md promise "every user-facing error gets a stable error code,
the failing config path, and a suggested fix" — currently unmet.

## Constraints

- **The HTTP/gRPC error shape is a public contract.** Changing it requires an
  additive, versioned migration (announce → add → migrate), not a break.
- **Dual representation is mandatory.** Library/embedder callers (the `Import`
  path works on Go types) must receive a native Go `error`; CLI-via-client, MCP,
  and UI receive it over gRPC/HTTP as structured data. One model, two encodings —
  designed together, or it silently breaks at the embedding boundary.
- **~800 construction sites cannot change at once.** The rollout must be
  incremental and must not require touching every `Errorf` to ship value.
- **No new heavy dependency.** Prefer the standard `google.rpc` error-details
  types already in the gRPC stack.
- **Match existing seams.** `cerrors` is mandatory for construction; `ecdysis`
  already owns CLI output. Extend them, don't fork parallel systems.

## Decision (proposed)

### 1. `ConduitError` — one type, dual encoding

A typed error in `cerrors` (or a new `pkg/foundation/cerrors/…` subpackage):

```go
type ConduitError struct {
    Code       Code              // stable string enum, e.g. "connector.plugin_not_found"
    Message    string            // human-readable, no secrets
    ConfigPath string            // JSON-pointer into the offending pipeline config, optional
    Suggestion string            // human-readable fix hint, optional
    Fix        *Fix              // STRUCTURED fix, optional — shared by CLI `repair` and MCP `repair`
    DocsURL    string            // optional
    err        error             // wrapped cause, always non-nil (see constructor)
}

type Fix struct {
    ConfigPath string  // where to change
    Op         string  // "set" | "remove" | "add"
    Value      string  // new value (for set/add)
}

func (e *ConduitError) Error() string { return e.Message }
func (e *ConduitError) Unwrap() error { return e.err }

// NewConduitError is the ONLY sanctioned way to construct one. It guarantees a
// stack frame even for a leaf error with no wrapped cause — a bare struct literal
// would carry no trace, because *ConduitError is not itself an xerrors type and
// cerrors' stack-trace walk only finds frames on wrapped xerrors values.
func NewConduitError(code Code, msg string, cause error) *ConduitError {
    if cause == nil {
        cause = xerrors.New(msg) // capture a frame at the origination site
    }
    return &ConduitError{Code: code, Message: msg, err: cause}
}
```

- Implements `error` and `Unwrap`, so it composes with existing `cerrors.Is`/`As`
  and the stack-trace machinery. Library callers use
  `var ce *ConduitError; errors.As(err, &ce)`.
- **Constructing via `NewConduitError` is mandatory** (a lint/guard forbids raw
  `&ConduitError{...}` literals outside the constructor). This is what makes the
  Observability section's "every error carries a stack trace" claim true — verified:
  a leaf `ConduitError` with no cause captures **zero** frames otherwise.
- `Fix` is **structured** (not just prose) so `repair` is one deterministic applier
  shared by the CLI and MCP tool — the UX review's requirement that a human not be
  a second-class citizen to an agent.

### 2. Error-code registry + CI guard

- A single registry file enumerates every code. Each entry is not a bare string but
  a record carrying its **explicit gRPC category** — deriving a `codes.Code` by
  string-matching a dotted name (is `pipeline.name_already_exists` an
  `InvalidArgument` or an `AlreadyExists`? substring-matching "not_found" is not
  naming discipline) is fragile, so the category is set once at registration:

  ```go
  type Code struct {
      Reason   string     // stable dotted id, e.g. "connector.plugin_not_found"
      GRPCCode codes.Code // explicit, not inferred
  }
  var CodeConnectorPluginNotFound = register("connector.plugin_not_found", codes.NotFound)
  ```

  The registry is the source of truth for docs, `llms.txt`, and the UI.
- **CI guard (concrete mechanism):** a `grpc.UnaryServerInterceptor` checks
  `errors.As(err, &ce)` on every non-nil response error and, in CI/integration-test
  mode, fails fatally instead of silently downgrading to `internal.unknown` (the
  runtime fallback in Failure modes). This is cheap, ships in v0.16, and reuses the
  fallback path as the enforcement point. It does NOT force a `*ConduitError` return
  type on handlers — those satisfy protobuf-generated `(resp, error)` server
  interfaces (`pkg/http/api/pipeline_v1.go`) whose signatures are fixed by codegen.
  Coverage is bounded by the test suite's error-path coverage. Stronger enforcement
  (an SSA/dataflow analyzer over the generated server interfaces, or a two-function
  thin-adapter handler pattern typed to return `(*Resp, *ConduitError)`) is **later
  hardening, not v0.16** — there is no static-analysis guard in the repo today to
  model from. The guard scopes to boundaries, NOT all ~800 internal sites — internal
  errors bubble up and get a code at the boundary.

### 3. The gRPC/HTTP boundary — additive, versioned

- At the API boundary, `ConduitError` serializes into the existing
  `google.rpc.Status` as a **detail** (`google.rpc.ErrorInfo`: `reason` = the code's
  `Reason`, `domain` = `conduit`, `metadata` = `{configPath, suggestion, docsUrl}`),
  with the top-level gRPC `Code` taken from the registry's explicit `GRPCCode`. The
  existing top-level `code`/`message` fields are unchanged — **purely additive**.
- **Verified compat (the load-bearing claim):** grpc-gateway's
  `DefaultHTTPErrorHandler` marshals `status.Convert(err).Proto()` — including
  `Details` — and Conduit's gateway wrapper (`grpcutil/gateway.go`) calls it
  unmodified; the default `JSONPb` marshaler expands the `Any`-typed detail via
  `protojson` automatically once the `errdetails` package is imported (it is not
  imported today — a new, trivial import). No error-handler or marshaler change is
  required. The one caveat: a strict JSON client with `additionalProperties:false`
  would reject the new `details` field — additive for essentially all real
  consumers, not literally all possible ones.
- `status.go`'s hand-written sentinel→`codes.Code` switches become a single
  mapping keyed on the registry; the sentinel switches remain as a fallback during
  migration.
- A `schemaVersion` string is stamped on structured output (see §4) and bumped by a
  deliberate, reviewed change. This is a **minimal, standalone mechanism** — a
  version field + a documented bump policy + contract-test snapshots — NOT the
  config-YAML parser's version machinery. That machinery
  (`config/yaml/parser.go`, `v1`/`v2` model packages, the rename changelog) solves a
  different problem: decoding a versioned pipeline-config _input file_ into one of
  several Go struct trees. It produces `config.Pipeline`, not API/CLI responses, and
  borrowing its multi-version-decode complexity for an output version tag would be
  over-engineering.

### 4. Structured output & `--json` — one `Result`, two renderings

- Introduce a `Result` type per command (or a small generic `Result[T]`) that
  holds the data; human and JSON output both render from it. No command calls
  `Stdout(table)` directly anymore.
- **Do the framework wiring first — but it is not a silver bullet.** `ecdysis`'s
  `Output` is just `{Stdout(any), Stderr(any)}` over `fmt.Fprint`; there is no
  `Result`/marshaler/format-negotiation concept, and the "dead `json` flag hook"
  (`decorators.go`) is only a deprecation-banner suppression check that _presumes_
  a command already defines its own `--json` flag — it is not output
  infrastructure. There is zero `--json` in `cmd/conduit` today. So the work splits:
  - **Framework wiring (O(1), do first):** register a shared `--json` bool + a
    decorator that dispatches to a JSON marshaler vs. the human formatter. Real and
    small — this part of the original claim holds. `ecdysis` is Conduit-controlled.
  - **Per-command Result extraction (O(n), the actual bulk):** each of the ~16–23
    commands still must stop building a `simpletable`/string inside `Execute`,
    define a `Result` value (or a generic `Result[T]`) with exactly the fields to
    serialize, and provide a `Human()` renderer (largely lifting the existing table
    code) alongside the JSON shape. The dispatch mechanism is shared; the per-command
    touch is not eliminated. Estimate accordingly — `--json` is not "free after the
    spike."
- Every JSON payload carries `schemaVersion`. Contract-test snapshots pin the
  schema so agent/UI consumers don't silently break.

### 5. `configPath` and secrets

- `configPath` has **two sources, and only one already exists.** The YAML parser's
  line/column tracking (`config/yaml/linter.go:93-141`, currently log-only) is real
  but narrow: it fires only while decoding a `pipelines.yaml` provisioning file, and
  only for fields the rename changelog already knows about. It is gone by the time
  the config reaches semantic validation. So it covers exactly one boundary:
  provisioning-file parse errors. Surface it there.
- For **every other boundary** — the primary API/CLI path
  (`POST /v1/pipelines`, `conduit pipelines create`; no YAML file exists) and
  downstream semantic-validation errors (invalid plugin name, DAG cycle, missing
  required config, deep in `orchestrator`/`connector`) — there is no position data
  to surface. `configPath` there is **net-new work**: generating a JSON-pointer
  (`/connectors/1/plugin`) by walking the config struct as validation errors
  propagate. The doc's own example (`connector.plugin_not_found`) is in this second
  category. Scope it as new work, not "surface existing data." It can be deferred:
  `ConfigPath` is optional, so early codes ship without it and gain it as the
  pointer-generation lands.
- A redaction pass strips secret values from `Message`/`ConfigPath`/metadata,
  keyed on connector-parameter sensitivity. Tested with a known-secret fixture.

## Alternatives considered

1. **gRPC codes only (status quo, formalized).** Keep numeric gRPC codes as the
   contract, add nothing. Rejected: numeric codes are coarse (no
   `connector.plugin_not_found` granularity), carry no configPath/suggestion/fix,
   and fail the machine-actionable-errors product goal. Agents can't self-correct
   from `InvalidArgument`.
2. **A parallel error package, not `cerrors`.** Rejected: `cerrors` is mandatory
   for construction and owns stack traces; forking creates two error systems and a
   migration nightmare. `ConduitError` lives with/beside `cerrors` and wraps.
3. **Encode the whole error as JSON in the gRPC message string.** Rejected: abuses
   the message field, isn't discoverable, and breaks the moment a proxy or the
   gateway reformats the message. `google.rpc.ErrorInfo` details are the standard,
   gateway-preserved mechanism.
4. **Big-bang migration of all ~800 sites.** Rejected: unshippable in one release
   and unnecessary — value comes from the user-facing boundaries. Incremental,
   boundary-first, guard-enforced.

## Failure modes

- **A boundary returns a bare `error` (no code).** The CI guard catches it at
  build time; at runtime, an un-coded error maps to a generic
  `internal.unknown` code + `codes.Internal` so the shape is never malformed.
- **Secret leaks into an error/log.** Redaction pass + a fixture test; the guard
  can also flag interpolating a known-sensitive param into a message.
- **Schema drift breaks an agent/UI consumer.** `schemaVersion` + contract-test
  snapshots; a bump is a reviewed change with a migration note.
- **Dual-representation divergence.** The Go→proto mapping is defined in one place
  with a round-trip test (`ConduitError` → proto detail → reconstructed
  `ConduitError`), so the embedding boundary and the wire boundary can't drift.
- **Wrapped cause loses the code.** `ConduitError` wraps via `Unwrap`; `errors.As`
  finds the nearest `ConduitError`. Guard encourages coding at the boundary where
  context is richest.
- **Nested `ConduitError`s shadow inner codes (stated invariant).** `errors.As`
  returns the _outermost_ `*ConduitError`, so wrapping one `ConduitError` in another
  hides the inner code. **Invariant: a boundary must not re-wrap an existing
  `*ConduitError` — if one is already in the chain, pass its code through
  unchanged.** The `NewConduitError` constructor enforces this: if `cause` already
  contains a `*ConduitError`, it returns that one (optionally enriching empty
  fields) rather than nesting a second.

## Upgrade / rollback (public-contract compat plan)

- **Additive only.** New `ErrorInfo` details + a `schemaVersion` field; the
  existing `google.rpc.Status` `code`/`message` and current CLI human output are
  unchanged. Existing consumers are unaffected → no breaking change, no deprecation
  window needed for the additions themselves.
- **`--json` is new surface**, not a change to existing output; default (human)
  output is untouched.
- **Deprecating the old sentinel→code switches** (once `ConduitError` coverage is
  complete) follows the announce → warn → remove policy across ≥2 minor versions.
- **Rollback:** the additions are inert to old consumers; reverting removes the
  details and the `--json` flag with no state/format migration.
- Fuzz the JSON error encode/decode and the proto detail round-trip (CLAUDE.md:
  fuzz every parser/protocol boundary).

## Observability

- Errors already carry stack traces via `cerrors`; `Code` becomes a structured log
  field, enabling per-code error-rate metrics (an operator can alert on
  `code="connector.plugin_not_found"` spiking). No new subsystem — a label on the
  existing metrics/logging path.

## Rollout (incremental, boundary-first)

1. Land `ConduitError` + the mandatory `NewConduitError` constructor (with the
   leaf-stack-trace guarantee and the no-nested-error enforcement) + `Fix` + the
   code registry (with explicit `GRPCCode`) + the Go↔proto mapping + the
   round-trip/fuzz tests. No behavior change yet. This safe construction path must
   exist before step 3, or boundary code will bake in raw struct literals with the
   trace gap.
2. The `ecdysis` `--json`/`Result` spike; wire the framework decorator.
3. Migrate the highest-value user-facing surfaces first: config validation,
   pipeline provisioning, and **Conduit-side** connector/plugin errors — those
   Conduit itself raises about a plugin (missing binary, bad handshake, unknown
   plugin name). These need no protocol change and are safe to code now. Each
   migrated boundary gets its code(s) + `--json`.
   - **Explicit gap — plugin-originated errors need a protocol change.** Errors a
     _running_ plugin raises about its own operation (bad credentials, missing
     table, rate limits) are the most valuable for agent self-correction, but they
     cross the connector protocol as a numeric gRPC status or a bare
     `string error` field (`conduit-connector-protocol` v0.9.4,
     `destination.proto`). Giving them a `ConduitError.Code`/`Fix` requires adding
     fields to `conduit-connector-protocol` — CLAUDE.md "breaking-change territory,
     version carefully." The same likely applies to the processor (WASM) protocol.
     This gets its **own ADR + versioned protocol change** before it is touched; it
     is explicitly out of this design's initial rollout.
4. Turn on the CI guard for migrated boundaries; expand its scope as coverage grows.
5. Document codes in `llms.txt` + the docs; contract-test the JSON schema.

## Related

- Phase 1 execution plan §1.1 (`docs/design-documents/20260704-phase-1-execution-plan.md`)
- `pkg/foundation/cerrors`, `pkg/http/api/status/status.go`,
  `pkg/foundation/grpcutil` (gateway), `pkg/provisioning/config/yaml` (line/column,
  version mechanism), `ecdysis` (CLI framework, `--json` hook)
- Downstream consumers: MCP server (§2 of the plan), React UI (§6), `conduit generate`

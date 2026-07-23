# `embed` / embedded v1: a stable Go embedding API for Conduit

## Summary

Conduit has no committed embedding contract today. Driving Conduit as a library means importing
`pkg/conduit` directly and calling `NewRuntime`/`Entrypoint.Serve` — a CLI-shaped surface that
prints a splash to stdout, calls `os.Exit` on error, writes logs unconditionally to `os.Stdout`,
and registers metrics into the global `prometheus.DefaultRegisterer` behind a process-wide
`sync.Once`. None of it is semver-committed; `pkg/conduit` is documented (`docs/package_structure.md`)
as an **internal** library, so anything built against it today can break on any refactor.

This doc commits to a concrete v1 slice, shippable in v0.19 (Phase 1, "Embedded v1" per
`ROADMAP.md`):

- **A new root-level Go package, `github.com/conduitio/conduit`** (package name `conduit`), as the
  one stable, semver-committed embedding surface — `conduit.New(ctx, opts)`, `Engine.Import`,
  `Engine.Run`, `Handle.Stop`. Root, not a `pkg/conduit`-reuse and not an `embed` subpackage (both
  considered and rejected below).
- **Host-injected observability**: `Options.Logger *slog.Logger` and `Options.MetricsRegisterer
  prometheus.Registerer`, wired so an embedded engine writes nothing to `os.Stdout`/`os.Stderr`,
  mutates no zerolog package global, and registers into the host's registry — never the
  Prometheus default registerer (single engine per process — see Non-goals).
- **Invariant 7 in embedded mode**: the host owns process signals; `Engine.Run` reuses the
  existing ctx-cancellation-driven tomb drain path unchanged; `Handle.Stop(ctx)` cancels and waits,
  bounded by the caller's context, and never calls `os.Exit`.
- **A pipelines-in-code builder (B2)** producing the exact same `config.Pipeline` the YAML
  provisioner parses, re-exported as `conduit.PipelineConfig` so embedders never need to reach into
  `pkg/provisioning/config` directly.
- **The C-ABI seam is designed, not built.** `libconduit` (C shared library) + Python/Node bindings
  are Phase 2 (B3), explicitly out of scope here; this doc sketches the shape so B1's Go API stays
  representable across a future `cgo` boundary without over-building for it now.

Risk tier: **Tier 2** (embedding surface / CLI-adjacent feature) for the new package and the
`Options`-driven observability injection. Any implementation PR that touches `Runtime.Run`'s
tomb/cleanup mechanics itself (as opposed to calling it unchanged) should be tiered **Tier 1** out
of caution, since that is the code invariant 7 lives in — see _Related_ for how this doc's
follow-up PRs should self-declare tier.

## Context

### What exists today

`pkg/conduit` (`pkg/conduit/runtime.go`) is the internal wiring package: `Runtime` holds the
orchestrator, provisioning service, DB, and lifecycle service; `NewRuntime(cfg Config)` builds all
of it; `(*Runtime) Run(ctx)` blocks until `ctx` is canceled or a service fails, using a
`gopkg.in/tomb.v2` tomb so that canceling `ctx` and receiving SIGTERM go through the _same_ drain
path (`registerCleanupV1`/`registerCleanupV2`, `runtime.go:508-594`) — this is the invariant-7
machinery, and it already treats "someone canceled my context" and "someone sent SIGTERM" as the
same event, because `Entrypoint.CancelOnInterrupt` (`pkg/conduit/entrypoint.go:68-85`) does nothing
but translate a signal into a canceled `context.Context`. That symmetry is exactly what makes
embedding tractable: the host doesn't need new drain logic, it needs a `context.Context` it
controls.

`pkg/provisioning` (`pkg/provisioning/import.go:33`) already has the programmatic entry point the
2026-07-05 vision doc pointed at: `(*provisioning.Service) Import(ctx, config.Pipeline) error`
takes a parsed pipeline — not YAML — diffs it against any existing pipeline with the same ID
(`Export` → `actionsBuilder.Build` → `executeActions`/rollback-on-failure), and tags it
`ProvisionTypeAPI` so a restart's config-file reconciliation never deletes it (`import.go:28-31`).
This is real, hardened, ordered-rollback-on-failure code — B1 does not need to invent a new import
path, only expose the existing one.

### The concrete gaps (grounded in code, not the vision doc's sketch)

Three things `pkg/conduit` does today that are incompatible with "runs inside someone else's
process":

1. **stdout/stderr.** `log.GetWriter` (`pkg/foundation/log/format.go:46-54`) hardcodes
   `os.Stdout` for both the JSON and CLI console writers — there is no writer-injection seam at
   all. `Entrypoint.Serve` additionally prints a splash to `os.Stdout` and errors to `os.Stderr`
   (`entrypoint.go:41-56`), and `exitWithError` calls `os.Exit` (`entrypoint.go:136-139`). The dev
   hot-reload watcher (`runtime.go:1163`, only when `Config.Dev.Enabled`) also writes to
   `os.Stdout` directly.
2. **Global metrics registration.** `configureMetrics()` (`runtime.go:364-377`) is guarded by a
   **package-level** `sync.Once` and calls `promclient.MustRegister(reg)` — registering Conduit's
   metrics into `promclient`'s **default, process-global registerer**. Two consequences: (a) a
   second `NewRuntime` call in the same process is a silent no-op for metrics (the `Once` never
   fires again), and (b) any host that already uses `prometheus.DefaultRegisterer` for its own
   metrics is now sharing a namespace with Conduit's, with `MustRegister` ready to panic on any
   name collision. Layered under this, `pkg/foundation/metrics.Register` (`metrics.go:109-123`)
   itself keeps **process-global** `metrics` and `registries` slices — every metric object ever
   created via `metrics.NewCounter`/`NewGauge`/etc. anywhere in the process accumulates a reference
   to _every_ registry ever passed to `Register`, and `Inc`/`Set`/`Observe` fan out to all of them
   (`metrics.go:243-257` etc.). A second engine in the same process does not get an isolated metric
   set; it gets **cross-talk** with the first.
3. **Global logger context.** `NewRuntime` sets `zerolog.DefaultContextLogger = &logger.Logger`
   unconditionally (`runtime.go:144`) — a mutation of a package-level variable in a _third-party_
   package. A second `NewRuntime` call clobbers the first engine's default; a host that also uses
   zerolog's own `zerolog.Ctx()` fallback anywhere gets silently repointed.

None of this is a design flaw in the CLI — `conduit run` is a whole, one-Runtime-per-process
program, so global state is fine there. It is precisely what makes `pkg/conduit` unsafe to embed
as-is, and precisely what B1 has to route around.

### Why this needs its own design doc (not an extension of the 2026-07-05 vision doc)

The v0.19 technical review flagged that `20260705-sdk-and-embedding-dx.md` is the right strategic
document for the _DX through-line_ across both the plugin-author and embedder personas, but it is
explicitly not the CLAUDE.md-mandated shape for shipping code: it has one alternative path
implied, not ≥2 compared and rejected, and no enumerated failure-mode list. `embed` is also
greenfield (no code exists at the paths it sketches) and, per the invariant list, "adds a
subsystem" that touches invariant 7 (shutdown) — both trigger the "design doc before code" bar on
their own. This doc is that artifact for the B1 (Go embedding API) + B2 (builder) slice only.

## Goals / Non-goals

**Goals (v0.19, this doc):**

- A stable, discoverable, semver-committed Go import path and constructor for embedding Conduit.
- Host-supplied `*slog.Logger` and `prometheus.Registerer` wired all the way through — zero writes
  to `os.Stdout`/`os.Stderr`, zero mutation of `zerolog.DefaultContextLogger`, zero registration
  into `prometheus.DefaultRegisterer`, for a single engine per process.
- `Engine.Run`/`Handle.Stop` reusing the existing ctx-driven drain path, with `os.Exit` banned from
  the entire call graph reachable from the embed package.
- A fluent pipeline builder that round-trips to the exact `config.Pipeline` YAML produces.
- A versioning/deprecation policy for the new package, matching the discipline CLAUDE.md already
  requires of the connector protocol and config schema.
- A designed (not built) C-ABI seam that the future `libconduit` can layer on without forcing a
  breaking change to the Go API defined here.

**Non-goals (explicitly out of scope, tracked as follow-ups):**

- **True multi-engine-per-process isolation.** The `pkg/foundation/metrics` global-slice design
  (gap 2 above) means two engines in one process do not get isolated metrics today. Fixing that
  requires refactoring `metrics.Register`/`addMetric` off their package-level globals — a separate,
  larger change. This doc fixes the _default-registerer pollution_ (real, fixable, high-value) and
  explicitly does **not** claim multi-engine isolation; see Failure modes.
- **Restricting `pkg/conduit`/`pkg/provisioning`/etc. from being imported directly.** Go's
  `internal/` visibility could enforce "only the root package is a committed contract," but moving
  `pkg/*` under `internal/` is a large, separate mechanical change (import-path rewrite across the
  whole tree) — flagged as recommended follow-up hardening, not done here.
- **Type-safe, per-connector config in the builder.** B2 ships a fluent builder over the existing
  loosely-typed `Settings map[string]string` shape (same shape config files already have) — not a
  generated, per-plugin-typed config API. That needs generated bindings tied to the connector
  registry and is its own future design doc.
- **C-ABI implementation, Python/Node bindings, ASAN gate (B3).** Designed at the shape level only;
  no `cgo`, no shared-library build target, no bindings ship from this doc.
- **Per-pipeline typed metrics/tracing hooks for the host.** The host gets Conduit's existing
  metric set through its own registerer; richer per-pipeline observability hooks are future work.

## Decision

### AC-1 — Import path: a new root package, not `pkg/conduit` reuse, not `embed`

**`import "github.com/conduitio/conduit"`, package name `conduit`, constructor `conduit.New(ctx,
opts) (*Engine, error)`.**

This is the literal first line every embedder copies, so it is pinned first. The module's root
import path has no existing package today (`find . -maxdepth 1 -name '*.go'` is empty) — this is
genuinely greenfield. `package conduit` at the module root is also the Go-conventional name (it
matches the import path's last component), so `go doc github.com/conduitio/conduit` becomes the
canonical embedding reference by construction, not a separate discoverability effort.

The new root package internally constructs a `pkg/conduit.Runtime` and delegates to it; it does
**not** reuse `pkg/conduit.Config` verbatim (which is CLI-flag-tagged and growing every release —
UI, dev-watcher, live-apply have all added fields to it in the last month of history). `Options` is
a small, curated, hand-committed type distinct from `Config`.

### AC-2 — Host observability injection (the biggest real-world-adoption gap)

`Options` carries:

```text
type Options struct {
    // ... DB/API/pipeline-path fields, curated subset of pkg/conduit.Config ...
    Logger             *slog.Logger           // required; slog.Default() is a valid, explicit choice
    MetricsRegisterer  prometheus.Registerer  // required; nil means "do not expose metrics"
}
```

Internally, `conduit.New`:

- Builds a `zerolog.Logger` whose writer is a small adapter that turns each zerolog event into an
  `slog.Record` (level-mapped, fields preserved as attrs) and calls `Logger.Handler().Handle(ctx,
  record)` — replacing the `log.GetWriter(format)`/`os.Stdout` path `NewRuntime` uses today. The
  CLI path is untouched: `Entrypoint.Serve` keeps calling `log.InitLogger` exactly as it does now;
  only the new embed constructor takes this different, injected path.
- Does **not** set `zerolog.DefaultContextLogger`. That global is a CLI-only convenience fallback
  for code paths that call `zerolog.Ctx(ctx)` without a `log.CtxLogger` attached; Conduit's own
  internal call chains already thread `log.CtxLogger` explicitly, so the embed path has no need
  for it and must not mutate it.
- Registers Conduit's metrics into `MetricsRegisterer` (via the existing
  `pkg/foundation/metrics/prometheus.Registry`, which already implements `prometheus.Collector`)
  using `Registerer.Register` — not `MustRegister` — surfacing an `AlreadyRegisteredError` as a
  structured, coded error from `New` instead of panicking. It never touches
  `promclient.DefaultRegisterer`.
- Skips `promclient.MustRegister` and the package-level `sync.Once` in `configureMetrics()`
  entirely on the embed path (that function stays as the CLI's own configuration, called only from
  `NewRuntime` when invoked via `Entrypoint`, not when invoked via `conduit.New`).

**Acceptance criteria (testable):** constructing an `Engine` with a captured `slog.Handler` and an
isolated `prometheus.NewRegistry()`, then running `New` → `Run` → `Import` → `Stop`, must show (a)
the captured handler receiving Conduit's log lines, (b) the isolated registerer containing
Conduit's metric families, (c) zero bytes written to the process's real stdout/stderr for the
duration, and (d) `prometheus.DefaultRegisterer` containing nothing Conduit-derived.

**`Options` deliberately excludes `Dev.*`.** The CLI's hot-reload dev watcher (`startDevWatcher`,
`runtime.go` ~L1163, only reachable when `Config.Dev.Enabled`) hardcodes `Out: os.Stdout`, entirely
bypassing the logger seam above. The "(c) zero bytes to real stdout/stderr" guarantee holds today
only because `Options` exposes no `Dev.*` field — so the underlying `Config.Dev.Enabled` stays at
its `false` default and the watcher never runs on the embed path. That is a mitigation by omission,
not a designed seam, and it is intentional: do not add a `Dev` passthrough to `Options` without
first giving the dev watcher the same writer-injection treatment AC-5 gives the rest of the logger.
Adding one silently reopens the stdout gap this doc closes.

### AC-3 — Package boundary: what's exported, what's internal

Exported from `github.com/conduitio/conduit` (the committed v1 surface):

| Symbol | Purpose |
| --- | --- |
| `Options` | Curated construction config: DB, API (optional — off by default for embeds that don't want the HTTP/gRPC surface), pipelines path, `Logger`, `MetricsRegisterer` |
| `New(ctx, Options) (*Engine, error)` | Validates config, opens the store (honoring `ctx`, see AC-5), wires services — no side effects on process globals |
| `PipelineConfig` | `= provisioning/config.Pipeline` (type alias) — the one artifact both YAML and the builder produce |
| `(*Engine) Import(ctx, PipelineConfig) error` | Delegates to `ProvisionService.Import` unchanged |
| `(*Engine) Run(ctx) (*Handle, error)` | Starts the engine in the background, returns once ready (mirrors `Runtime.Ready`) |
| `(*Engine) StartPipeline(ctx, id string) error` / `StopPipeline(ctx, id string, force bool) error` | Thin delegating wrappers over the existing per-pipeline lifecycle verbs — no new logic |
| `(*Handle) Stop(ctx) error` | Cancels the run context, blocks for drain-or-deadline (AC-4) |
| `NewPipeline(id string) *PipelineBuilder` | B2 fluent builder, terminal method returns `PipelineConfig` |

**`PipelineConfig`'s hidden surface (closing the type-alias semver leak).** Because `PipelineConfig`
is a structural alias — not a wrapper — `provisioning/config.Pipeline`'s field set, and its
`Connector`/`Processor`/`DLQ` sibling structs' field sets, are now part of the committed public
contract by construction, whether or not `pkg/provisioning/config` intended that. That package was
never told it is anything but internal — its own maintainers would reasonably refactor it believing
everything under `pkg/` is internal, per `docs/package_structure.md`. This doc closes that gap
explicitly rather than silently: as of this alias, `config.Pipeline`, `config.Connector`,
`config.Processor`, and `config.DLQ`'s shapes are under the same AC-7 announce → warn → remove
discipline as every other symbol in this table — by name, not by wrapping. This is the cheaper of
two options considered: the alternative, giving `PipelineConfig` its own struct with an internal
conversion to `config.Pipeline`, was rejected as disproportionate here — the alias already
round-trips YAML today (AC-6), and a wrapper only pays for itself if `provisioning/config` needs
freedom to diverge from the embed contract, which nothing today requires. A comment to this effect
belongs at the top of `pkg/provisioning/config/config.go` once this ships, so a future refactor
there sees the constraint locally, not just in this doc.

Internal (not part of the committed contract, even though Go cannot fully enforce this without the
`internal/` follow-up in Non-goals): `pkg/conduit`, `pkg/provisioning`, `pkg/lifecycle`,
`pkg/orchestrator`, and everything else under `pkg/`, per the existing convention in
`docs/package_structure.md` ("`pkg` — the internal libraries and services that Conduit runs").
This doc's package doc (`doc.go` at the module root) states explicitly: this package is the stable
embedding API; `pkg/conduit` is Conduit's internal engine wiring and is not a committed contract,
even though it happens to be importable.

### AC-4 — Invariant 7 in embedded mode: the host owns the process, Conduit owns the drain

`Runtime.Run(ctx)` already treats context cancellation and SIGTERM identically (both flow through
the same tomb; see Context above) — `Engine.Run`/`Handle.Stop` add no new drain logic, they add a
non-CLI _calling convention_ onto the existing one:

```text
func (e *Engine) Run(ctx context.Context) (*Handle, error) {
    runCtx, cancel := context.WithCancel(ctx)
    done := make(chan struct{})
    h := &Handle{cancel: cancel, done: done}
    go func() {
        defer close(done)
        h.runErr = e.runtime.Run(runCtx)   // unchanged Runtime.Run
    }()
    select {
    case <-e.runtime.Ready:
        return h, nil
    case <-done:
        // Runtime.Run returned — successfully or not — before signaling Ready.
        // Ready is closed only at the end of a *successful* Run (runtime.go ~L435);
        // an early failure (initServices, runtime.go ~L407-410; gRPC/HTTP serve,
        // ~L415-421) never closes it. Without this branch, Engine.Run blocks forever.
        return nil, h.runErr
    }
}

func (h *Handle) Stop(ctx context.Context) error {
    h.stopOnce.Do(h.cancel)
    select {
    case <-h.done:
        return h.runErr
    case <-ctx.Done():
        return fmt.Errorf("stop deadline exceeded, drain continues in background: %w", ctx.Err())
    }
}
```

**Acceptance criteria (testable):** `Engine.Run` must always resolve — either successfully once
`Ready` closes, or with `h.runErr` (non-nil) once `done` closes first — whichever happens first;
there is no third outcome where `Run` blocks indefinitely. Exercised directly by the Failure-mode-8
test (see Failure modes / Testing): `Run` against a config guaranteed to fail before `close(Ready)`
(e.g. a bad connector-utils/gRPC/HTTP bind address) must return an error promptly, not hang.

Two absolutes drive this shape: **the embed package never calls `os.Exit`** (a library killing its
host's process out from under it is never correct — the host may have its own subsystems that also
need clean shutdown), and **`Stop` blocks until drain-complete-or-deadline**, never fire-and-forget
(a `Stop` that returns before the drain finishes teaches the host the wrong mental model — that
calling it is sufficient — exactly reproducing the double-SIGTERM data-loss risk invariant 7 exists
to prevent, just via a different call site). `Entrypoint`'s CLI-only mechanics — the splash,
`CancelOnInterrupt`'s second-signal `os.Exit(hardExitCode(sig))`, `exitWithError` — are not part of
the embed call graph at all; `conduit.New` constructs a `Runtime` directly and never touches
`Entrypoint`.

### AC-5 — Implementation seam required in `pkg/conduit` (what B1 needs, precisely)

This doc is the spec for the following surgical, non-behavior-changing-for-the-CLI changes.
Mechanically, these seams thread through as functional options on a lower-level constructor (e.g.
`NewRuntime(cfg Config, opts ...RuntimeOption)`, or an equivalent unexported builder the embed
package alone calls) — deliberately **not** by growing `pkg/conduit.Config` with embed-only fields,
since AC-1 already rejected exposing `Config` to embedders for exactly that reason (CLI-flag-tagged,
growing every release). `conduit.New` is the only caller that passes these options; the CLI's
`Entrypoint` path keeps constructing `Runtime` exactly as it does today, options unset, defaults
unchanged.

1. `log`/`Runtime` need a writer-injection seam (or `NewRuntime` needs to accept a pre-built
   `log.CtxLogger`) so the embed layer can hand it the slog-backed writer from AC-2, instead of
   `log.GetWriter` unconditionally returning `os.Stdout`.
2. `configureMetrics()`'s process-wide `sync.Once` + `promclient.MustRegister` needs a per-Runtime
   path that accepts a `prometheus.Registerer` parameter, used only by the embed constructor (the
   CLI keeps calling the existing default-registerer path unchanged).
3. `zerolog.DefaultContextLogger = &logger.Logger` (`runtime.go:144`) needs to become conditional —
   set by the CLI entrypoint's construction path, skipped by the embed path.
4. `OpenStore(cfg Config, logger log.CtxLogger) (database.DB, error)` hardcodes
   `context.Background()` for the Postgres/SQLite dial (`runtime.go:206,211`) instead of accepting
   a caller `context.Context` — `conduit.New(ctx, opts)` needs the dial to honor `ctx` so a
   host-imposed startup timeout/cancellation actually bounds construction.
5. `Runtime.Run(ctx)`'s tomb-based cleanup (`registerCleanupV1`/`V2`) needs **no change** — it is
   already ctx-cancellation-driven and is reused as-is by `Handle.Stop`.
6. `Runtime.Run(ctx)`'s entry gains one line: `if err := ctx.Err(); err != nil { return nil, err }`.
   There is no such check today (`runtime.go` ~L382), so "fail fast on an already-canceled context"
   (Failure mode 8) is currently aspirational, not actual behavior. This is a guard clause, not a
   change to the drain mechanics in point 5, and it pairs with the `Engine.Run` `select` fix in
   AC-4: an already-canceled context and a fails-during-startup context now resolve through the
   same shape — a prompt error, never a hang.

### AC-6 — Pipelines-in-code builder (B2)

`conduit.NewPipeline(id string) *PipelineBuilder` with chained `.WithConnector(...)`,
`.WithProcessor(...)`, `.WithDLQ(...)` methods, terminal `.Build() (PipelineConfig, error)`
(validating the same way `conduit pipeline validate` does, returning a `ConduitError`-coded error).
`PipelineConfig` is the type alias from AC-3 — the builder cannot diverge from the YAML shape by
construction, because it produces the identical struct. Verified by a round-trip test: build →
`yaml.Marshal` → `yaml.Parse` → assert equal to the pre-marshal value (and vice versa, parse a
fixture YAML pipeline and assert the builder can reproduce it field-for-field).

### AC-7 — Versioning and deprecation policy

Conduit itself is pre-1.0 (v0.19 today). This package is nonetheless held to the **same**
announce → warn → remove discipline CLAUDE.md already requires of the connector protocol, pipeline
config schema, and error codes — not the looser "anything can break on a 0.x bump" norm. Concretely:
a breaking change to any exported symbol in AC-3's table is announced (CHANGELOG + godoc
`Deprecated:` comment) in one monthly release, kept working with a warning for at least one more
minor release, and removed no earlier than the third minor release after the announcement. This
package does not get its own independent version number or a separate Go module — it ships at the
same tag as the rest of Conduit, so "v0.19.x has embed API X" is always answerable from the
existing release train, not a second one to track.

This discipline extends **by name** to `provisioning/config.Pipeline`, `.Connector`, `.Processor`,
and `.DLQ` via the `PipelineConfig` type alias (AC-3): the alias makes their shape part of this
committed contract regardless of what `pkg/provisioning/config`'s own documentation claims about
its internal status.

### AC-8 — The C-ABI seam (designed, not built)

A future `libconduit` (Go `-buildmode=c-shared`/`c-archive`) would expose a small C surface
mirroring `Engine`'s lifecycle, with data crossing the boundary as JSON envelopes (not raw structs
— cgo cannot safely pass Go pointers/slices/interfaces across the ABI):

```text
conduit_new(options_json: *const char)                    -> handle*
conduit_import(h: handle*, pipeline_json: *const char)     -> error*
conduit_run(h: handle*)                                    -> error*
conduit_stop(h: handle*, timeout_ms: int64)                -> error*
conduit_free(h: handle*)
conduit_free_error(e: error*)
```

Ownership/threading sketch: `handle*` wraps a `runtime/cgo.Handle` referencing the Go `*Engine`,
keeping it alive against the GC while a C caller holds the opaque pointer. Errors cross as the same
`ConduitError` shape (code, config path, suggestion) JSON-encoded, matching the "same errors cross
every boundary" principle already used for the CLI/API/MCP. Single-writer-per-handle: a caller must
not invoke `conduit_run`/`conduit_stop`/`conduit_free` concurrently on the same handle — language
bindings (Python/Node) are responsible for serializing access from their own multi-threaded
runtimes; this is not automatically safe just because the Go side has its own internal locking.
`conduit_stop` must be called before `conduit_free`, mirroring the Go-level "Stop before drop."

This is scoped out of v0.19 implementation on purpose (no `cgo`, no shared-library build target, no
bindings, no ASAN gate): per CLAUDE.md's "no speculative generality," building the ABI, bindings,
and an ASAN gate before a single production Go embedder has exercised B1 would be exactly the kind
of interface built on a hunch rather than two real implementations. Sketching the shape now,
however, keeps B1's Go API JSON-envelope-friendly (plain data in/out on the hot construction paths)
so it doesn't need a breaking redesign when B3 is picked up in Phase 2.

## Alternatives considered

**Import path / constructor (AC-1):**

1. **Reuse `pkg/conduit` directly** (add `New`/safe wrappers alongside the existing
   `NewRuntime`/`Entrypoint`). Rejected: `pkg/conduit` already has 15+ exported, CLI-shaped symbols
   with zero semver discipline, growing every release (UI, dev-watcher, live-apply all added
   surface to it in the last month), and `docs/package_structure.md` already designates everything
   under `pkg/` as internal — conflating that with "the one stable public surface" contradicts the
   repo's own stated convention and gives embedders no compiler-enforced boundary against reaching
   past the committed API into churn.
2. **A subpackage named `embed`** (`github.com/conduitio/conduit/embed`, `embed.New(...)`).
   Rejected on a concrete collision, not aesthetics: the Go standard library's `"embed"` package
   (for `go:embed`) is **already imported in this repo** (`pkg/web/ui/ui.go`, for the built-in UI's
   embedded assets). Any file needing both — plausible for an embedder that also embeds its own
   assets — forces an import alias on literally the first line every embedder copies. It also reads
   as "the go:embed feature" before it reads as "Conduit-as-a-library" to any Go developer.
3. **Name the package `libconduit`** (pre-aligning with the future C-ABI artifact name).
   Rejected for now: it collides conceptually with the actual future shared-library artifact
   (`libconduit.so`/`.dylib`) from AC-8 — having both "the Go package" and "the C shared library"
   called `libconduit` invites exactly the kind of confusion a design doc should prevent. Reserve
   that name for the artifact it will actually name in Phase 2.
4. **(Chosen) A new root-level package**, `github.com/conduitio/conduit` / `conduit.New(...)`. Costs
   one acknowledged wart — `conduit` (root) and `conduit` (`pkg/conduit`) share a base name, so prose
   must always spell out the full import path — but that is a far smaller cost than a stdlib
   collision or an internal-package leak, and it is a common, unremarkable Go pattern.

**Host observability injection (AC-2):**

1. **No API change; tell embedders to redirect `os.Stdout` themselves and accept the default
   Prometheus registry.** Rejected: `os.Stdout` is one file descriptor per process — it cannot be
   redirected per-engine, so two engines (or Conduit's logs and the host's own stdout-JSON logs)
   interleave on the same fd. It also does nothing about `MustRegister` panicking on a name
   collision with the host's own default-registry metrics, and forces every embedder to expose
   Conduit's metrics at whatever `/metrics` the host already serves, whether or not that's what
   they want.
2. **Accept a plain `io.Writer` for logs (not `*slog.Logger`) and let hosts bridge themselves.**
   Rejected as insufficient on its own: most modern Go hosts standardize on `log/slog` internally;
   requiring them to parse zerolog's own JSON output back out just to fold it into their `slog`
   pipeline is precisely the "you can't actually wire this into the host's logging" failure the
   review flagged. (An `io.Writer` bridge is still needed _internally_ — the adapter in AC-2 is
   exactly that — it's just not the thing exposed to the embedder.)
3. **(Chosen) Accept `*slog.Logger` and `prometheus.Registerer` directly in `Options`**, with the
   zerolog→slog adapter and the registerer-injection built as internal plumbing (AC-2/AC-5).

**Invariant-7 shutdown model (AC-4):**

1. **Mirror the CLI's double-signal hard-exit inside `Stop`** (a second `Stop` call, or a timeout,
   calls `os.Exit`). Rejected unconditionally: a library must never terminate its host's process;
   the host may have other subsystems needing clean shutdown, and it also makes the API untestable
   in-process (a test exercising the "second Stop" path would kill the test binary).
2. **`Stop` cancels and returns immediately, fire-and-forget.** Rejected: if `Stop` returns before
   drain+checkpoint complete, a host that follows it with its own process exit (or a short-lived
   host/test harness that returns from `main` right after) truncates the drain exactly like a raw
   double-SIGTERM — defeating the reason invariant 7 has teeth on the CLI side.
3. **(Chosen) `Stop(ctx)` blocks until the run goroutine returns or `ctx` is done, whichever
   first**, using an ordinary `context.Context` deadline — idiomatic, testable, and it puts "how
   long is long enough, and what happens if it's not" where it belongs: with the host.

## Failure modes

1. **Host never calls `Stop`** (process is `SIGKILL`ed externally, or a host bug skips it). No
   worse than the CLI's `kill -9` case: recoverability depends entirely on invariant 5 (atomic
   checkpoint writes), which the embed path does not change. Mitigation: document the `defer
   handle.Stop(ctx)` pattern prominently; this is not something the embed API can protect against
   beyond what already exists.
2. **`Stop`'s context deadline is shorter than the in-flight drain.** `Stop` returns a
   deadline-exceeded error; the drain continues best-effort in the background (nothing can force-
   kill it, per AC-4). If the host then exits its own process anyway, this degrades to failure mode
   1. Mitigation: document a recommended minimum deadline (the internal `exitTimeout = 30s` used by
   `registerCleanupV1`/`V2` today is the right floor to recommend) and make the returned error's
   message state the trade-off explicitly.
3. **Concurrent/duplicate `Stop` calls** (e.g. a host signal handler and a health-check-triggered
   shutdown both fire). Must be idempotent: `stopOnce` (a `sync.Once` around the cancel) plus
   blocking all callers on the same `done` channel — no double-cancel panic, no double `DB.Close`.
   Regression test: two goroutines calling `Stop` concurrently must both return the same result
   without racing (run under `-race`).
4. **A second engine (or a `conduit run` CLI process) already holds the store.** Already handled
   for BadgerDB/SQLite via their exclusive file lock (`OpenStore` returns a `CodeUnavailable`
   `ConduitError`, `runtime.go:228-231`) and refused outright for Postgres (see the standalone-
   apply design doc referenced from `InitProvisioningOnly`'s comment). `conduit.New` must surface
   this as the same coded error, not a panic — this is existing behavior, not new work, but the
   embed path must not swallow or wrap it into something less specific.
5. **Two engines in one process share `pkg/foundation/metrics`'s global registration slices** (gap
   2 in Context). Until that package is refactored off its package-level `global` var, a second
   engine's metrics either no-op (if the old `sync.Once` path is hit) or cross-register into the
   first engine's registerer (since `metrics.Register` attaches _every_ metric object ever created,
   process-wide, to whatever registry is passed to it). This is a real, structural limitation —
   explicitly out of scope (Non-goals) — and ships with a same-process, two-engine integration test
   that **asserts the current cross-talk behavior** (so it's documented and regression-caught, not
   silently discovered by an embedder). It is not silently claimed as "supported."
6. **The host's `slog.Handler` blocks** (e.g. an unbuffered network log shipper). Conduit's hot,
   per-record paths do not log per-record today (existing behavior, unchanged), but startup/
   shutdown/error logging could stall the drain sequence if the handler blocks synchronously.
   Mitigation: document that the supplied handler must not block indefinitely — the same
   expectation any embedder already has for a raw `io.Writer`.
7. **The host's `prometheus.Registerer` already has a same-named metric.** `Registerer.Register`
   (not `MustRegister`) returns a `prometheus.AlreadyRegisteredError`; `conduit.New` must surface
   this as a coded, actionable error at construction time — fail fast — rather than panicking. This
   is one of the concrete things AC-2 fixes relative to today's `MustRegister` call.
8. **Lifecycle misuse by the host: `Run` called twice, `Stop`/`Run` on a nil/zero `Handle`,
   duplicate builder pipeline IDs.** These are programmer errors, not runtime conditions, and the
   design deliberately **does not mint new error codes for them** — inventing a `CodeRunTwice` /
   `CodeNilHandle` would be speculative taxonomy growth for cases a correct host never hits.
   Instead:
   - **`Engine.Run` invoked twice on the same `Engine`**, and **`Handle.Stop`/method calls on a nil
     or zero-value `Handle`**, return the **existing** `conduiterr.CodeInvalidArgument` (a coded,
     actionable error naming the misuse) — never a panic, never a new code.
   - **Duplicate pipeline `id` in the B2 builder** keeps its own guard: `.Build()` rejects two
     pipelines sharing an `id` at build time, the same way `conduit pipeline validate` rejects it,
     surfaced as a `ConduitError` (again `CodeInvalidArgument`, reusing the existing code the
     validator already emits for this class — not a builder-specific new code). This is the one
     guard worth naming explicitly because a fluent builder makes an accidental duplicate easy to
     write; the other two are plain single-`Engine` contract violations.

   Regression test: each misuse returns the coded error (not a panic), asserted under `-race` for
   the concurrent-`Run` case.
9. **`Run` fails before it can signal readiness.** Two related host-visible cases, both required to
   resolve as a prompt, clear **error** — never a hang, and never a silent start-then-crash that
   looks unrelated: (a) an already-canceled `context.Context` passed to `Run` (host bug) — handled
   by the `ctx.Err()` guard added at `Runtime.Run`'s entry (AC-5 point 6); (b) a config valid enough
   to construct but that fails during `Runtime.Run` itself, before `close(Ready)` (`runtime.go`
   ~L435) — e.g. `initServices` failing (~L407-410) or the gRPC/HTTP listener failing to bind
   (~L415-421) — handled by `Engine.Run`'s `select` on `Ready` vs. `done` (AC-4). Before that
   `select` was added, case (b) hung `Engine.Run` forever: the background goroutine observes the
   failure and closes `done`, but nothing in the original `<-e.runtime.Ready`-only sketch was
   listening for it.

## Upgrade / rollback

This doc introduces **no new serialized/persisted format**: `Import` takes the same
`config.Pipeline` the YAML provisioner has always produced, and the B2 builder emits that identical
struct (verified by the AC-6 round-trip test) rather than a new representation. Invariants 2 and 5's
"versioned migration path" requirement therefore does not apply to this feature — there is nothing
on disk that changes shape.

What _does_ need a policy is the new package's own API surface, covered by AC-7 (announce → warn →
remove, same as the connector protocol/config schema/error codes). Rollback for a bad release of
the embed package is ordinary Go module rollback: an embedder pins their `go.mod` `require` to the
prior Conduit tag — no special migration step, since no state format moved.

The one thing that _does_ interact with existing invariant 7 machinery is `Handle.Stop` reusing
`Runtime.Run`'s drain path unchanged (AC-4/AC-5 point 5) — any future change to that shared
drain/cleanup code remains Tier 1 regardless of whether it's touched "for embedding" or "for the
CLI," since both callers now depend on the same invariant-7 guarantee.

## Testing

Per the live-now bar (lint, race detector, unit + integration — property/fuzz/chaos gates are not
live until Phase 1/2 per the Process maturity table, and are not claimed here):

- **Observability isolation test** (AC-2's acceptance criteria): captured `slog.Handler` + isolated
  `prometheus.Registry`, asserting zero bytes to real stdout/stderr and zero pollution of
  `prometheus.DefaultRegisterer`, for a full `New → Run → Import → Stop` cycle.
- **Stop idempotency test** (failure mode 3): concurrent `Stop` calls under `-race`, asserting a
  single consistent result and no panic.
- **Lifecycle-misuse test** (failure mode 8): `Run` called twice, `Stop` on a nil/zero `Handle`,
  and a builder with duplicate pipeline IDs each return `conduiterr.CodeInvalidArgument` (no new
  code, no panic); the concurrent-`Run` case runs under `-race`.
- **Run-fails-before-Ready test** (failure mode 9, AC-4): `Run` invoked against a config guaranteed
  to fail during startup — e.g. a connector-utils/gRPC/HTTP address already bound — must return
  `h.runErr` promptly rather than hang; a companion case passes an already-canceled
  `context.Context` and asserts the same fail-fast behavior via the AC-5 point 6 guard.
- **Stop timeout test** (failure mode 2): a `Stop` context with a deadline shorter than a
  deliberately slow shutdown path returns a distinguishable timeout error without blocking forever.
- **Two-engine-in-one-process test** (failure mode 5): documents, rather than hides, the current
  metrics cross-talk limitation — asserts the known behavior so a future fix to
  `pkg/foundation/metrics` has a test that must be consciously updated, not silently left stale.
- **Round-trip test** (AC-6): builder → YAML → parse → equal, and YAML fixture → builder-equivalent
  struct, both directions.
- **`ConduitError` propagation test** (failure modes 4, 7): store-already-open and metric-name-
  collision both surface as coded, non-panicking errors from `New`.
- **`Example` godoc function** compiled and run by `go test` (per CLAUDE.md's "examples that
  compile" standard): the canonical `New → Run → Import → Stop` flow against the in-memory DB
  driver, doubling as the copy-paste-runnable doc example.
- No upgrade/downgrade or chaos-suite test is added for this feature specifically (see Upgrade /
  rollback — no serialized format changes), consistent with the Process maturity table marking
  those gates "by Phase 2," not live now.

## Related

- `docs/design-documents/20260705-sdk-and-embedding-dx.md` — the strategic DX vision this doc
  makes concrete for the B1 (embedding API) + B2 (builder) slice; B3 (C-ABI + bindings) remains
  that doc's Phase 2 scope, sketched at the shape level in AC-8 here.
- `docs/design-documents/20260704-phase-1-execution-plan.md` — sequences "embedded v1" into v0.19.
- `docs/design-documents/20260708-cli-pipeline-deploy-apply.md` — the provisioning
  `Plan`/`ApplyPlan` preview engine `Import` shares its diff/rollback machinery with.
- `docs/architecture-decision-records/20260704-single-node-engine.md` — the engine-scope ADR this
  package's `Options` (no clustering/membership fields) stays consistent with.
- `ROADMAP.md`, "Embedded v1" (Phase 1) and "Persona B" scope in the DX vision doc.
- `pkg/conduit/runtime.go`, `pkg/conduit/entrypoint.go`, `pkg/provisioning/import.go` — the exact
  code this doc's AC-5 seam and AC-1/AC-4 decisions are grounded in.

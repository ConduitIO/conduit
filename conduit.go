// Copyright © 2026 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package conduit

import (
	"context"
	"log/slog"
	goruntime "runtime"
	"sync"
	"sync/atomic"

	"github.com/conduitio/conduit-commons/database"
	pkgconduit "github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	provisioningconfig "github.com/conduitio/conduit/pkg/provisioning/config"
	promclient "github.com/prometheus/client_golang/prometheus"
)

// PipelineConfig is Conduit's pipeline configuration shape: a type alias (not
// a copy) of provisioning/config.Pipeline, the exact struct the YAML
// provisioner both produces and consumes. Aliasing guarantees Engine.Import
// accepts precisely what a hand-built PipelineConfig value produces, with no
// conversion step to drift out of sync — see provisioning/config/parser.go's
// matching doc comment for the constraint this places on that package.
type PipelineConfig = provisioningconfig.Pipeline

// Options configures a New Engine. Logger and MetricsRegisterer have
// deliberately different zero-value semantics — read both before assuming
// symmetry: a nil Logger is a safe, explicit default; a nil
// MetricsRegisterer disables metrics.
type Options struct {
	// Logger receives every log line Conduit produces, with level and
	// structured fields preserved as slog attributes. nil defaults to
	// slog.Default() (an explicit, documented choice, not an error). Two
	// concurrent Engines constructed with distinct Loggers never cross-talk:
	// Conduit never writes to os.Stdout/os.Stderr or a process-global logger
	// on this path. The resolved logger also backs the leak-check finalizer
	// (see Engine's "Lifecycle contract" doc) if Close is never called.
	Logger *slog.Logger

	// MetricsRegisterer receives Conduit's Prometheus metric families. nil
	// means "do not expose metrics" — metrics are silently disabled, not an
	// error. It is never prometheus.DefaultRegisterer implicitly: Conduit's
	// metrics never reach the process-global default registry through New.
	//
	// Known limitation: see this package's doc.go "metrics cross-talk"
	// section — two Engines each get an isolated Registerer/Registry
	// *object*, but pkg/foundation/metrics' process-global metric
	// definitions still fan values out across every registry in the process.
	MetricsRegisterer promclient.Registerer

	// DB configures the embedded storage backend. The zero value (Type =="")
	// defaults to an in-memory store — convenient for examples and tests, but
	// every pipeline configuration is lost once the Engine stops. A
	// production embedder should set Type explicitly.
	DB DBOptions

	// PipelinesDir optionally points at a directory (or a single file) of
	// pipeline YAML configs Conduit provisions on Run. Leave empty to skip
	// file-based provisioning entirely and manage pipelines exclusively
	// through Engine.Import — New never falls back to a CLI-style
	// cwd/pipelines default in that case (B1 DX hardening fix: an earlier
	// version of this package fell back to scanning the working directory
	// anyway, and — because that directory usually doesn't exist for a
	// pure-embed caller — provisioning then failed silently, as a swallowed
	// ERROR log, while Run still returned a healthy Handle). When
	// PipelinesDir is set and provisioning genuinely fails (a missing
	// directory, malformed YAML, ...), Run returns that failure as an error
	// instead of only logging it — see Run's doc.
	PipelinesDir string

	// API optionally exposes Conduit's HTTP/gRPC API. Disabled by default: an
	// embedder that only needs in-process orchestration is not forced to bind
	// a socket.
	API APIOptions
}

// DBOptions configures Options.DB.
type DBOptions struct {
	// Driver, when set, is used directly and takes precedence over Type and
	// the type-specific fields below — the same precedence
	// pkg/conduit.Config gives a caller-supplied database.DB.
	Driver database.DB

	// Type selects the storage backend: pkgconduit.DBTypeBadger,
	// DBTypePostgres, DBTypeInMemory, or DBTypeSQLite. Defaults to
	// DBTypeInMemory when empty.
	Type string

	Badger struct {
		Path string
	}
	Postgres struct {
		ConnectionString string
		Table            string
	}
	SQLite struct {
		Path  string
		Table string
	}
}

// APIOptions configures Options.API.
type APIOptions struct {
	// Enabled turns on Conduit's HTTP and gRPC API. Default: false.
	Enabled bool
	// GRPCAddress is the bind address for the gRPC API (e.g. ":8084", or
	// "127.0.0.1:0" for an OS-assigned port). Required if Enabled.
	GRPCAddress string
	// HTTPAddress is the bind address for the HTTP API/gateway. Required if
	// Enabled.
	HTTPAddress string
}

// Engine is a constructed, not-yet-running Conduit instance. Obtain one with
// New; start it with Run.
//
// # Lifecycle contract
//
// New only validates Options — it does not open Options.DB, dial anything, or
// construct pkg/conduit's Runtime. That happens lazily, in ensureRuntime, the
// first time either Run or Import is called (B1 DX hardening fix: v1 opened
// the database eagerly in New, so a New'd-and-discarded Engine — constructed
// speculatively, or abandoned after a later precondition check failed — leaked
// a Badger file lock or a Postgres/SQLite pool for the rest of the process's
// life unless Close was called). Concretely:
//
//   - New → discard (Run and Import never called): nothing was ever opened.
//     Calling Close is still safe but no longer required to avoid a leak —
//     there is nothing to release.
//   - New → Close (Run/Import never called): same as above; Close observes
//     that ensureRuntime never ran and returns nil without touching anything.
//   - New → Run or Import (opens the database) → Close: Close releases the
//     database ensureRuntime opened.
//   - New → Run (fails before Ready, including the already-done-context
//     guard) → Close: ensureRuntime already opened the database before
//     Runtime.Run's guard trips (started still flips true on the Run call,
//     see below), and it was never handed to Runtime's own cleanup path
//     (registerCleanup) — Close is still required to release it.
//   - New → Run (succeeds) → Stop → Close: Stop's drain already closed the
//     database via Runtime's cleanup path. Close is safe to call anyway — it
//     is idempotent with that path, not merely with itself — and returns nil.
//   - Close is safe to call more than once from the same or different
//     goroutines; every call after the first observes the same result.
//   - New → Close → Run or Import (no prior Run/Import call — ensureRuntime
//     never ran before Close): ensureRuntime checks the closed flag before
//     ever opening anything, so this never silently opens a fresh runtime the
//     caller believed was already released. It returns a coded error
//     (conduiterr.CodeInvalidArgument) instead.
//
// Safety net: if an Engine that DID open resources (Run or Import actually
// ran) is garbage-collected without Close ever being called, a
// runtime.SetFinalizer hook logs a leak warning through Options.Logger (or
// slog.Default() if none was supplied) instead of leaking silently — see
// engineLeakCheckFinalizer. This is belt-and-suspenders, not a substitute for
// Close: a finalizer's timing is unpredictable (GC-dependent, and finalizers
// are not guaranteed to run at all before process exit), and it deliberately
// only logs — it never closes the database itself, since running arbitrary
// cleanup from a finalizer goroutine with no ordering guarantee relative to
// the rest of the program is its own footgun.
//
// started flips to true the moment Run is *called*, not when it succeeds: a
// failed Run permanently retires this Engine for running (a second Run
// attempt — even after a failure — returns the same
// conduiterr.CodeInvalidArgument "called more than once" error Run's own doc
// describes). Close is unaffected by started and may be called in any state.
type Engine struct {
	cfg         pkgconduit.Config
	runtimeOpts []pkgconduit.RuntimeOption
	logger      *slog.Logger // resolved, never nil; used only by the leak-check finalizer

	started atomic.Bool

	// mu guards every field below it: the lazy-open state (initDone, runtime,
	// initErr, opened) and closed. A plain Mutex — not sync.Once/atomics —
	// specifically so Close can block until any in-flight ensureRuntime call
	// (from a concurrent Run or Import) has finished, rather than racing to
	// observe "nothing opened yet" and returning early while a database open
	// completes moments later with nothing left to release it. Engine
	// construction and shutdown are not a hot path, so a mutex held across
	// NewRuntime's dial is an acceptable, simple trade for that correctness.
	mu       sync.Mutex
	initDone bool
	runtime  *pkgconduit.Runtime
	initErr  error
	// opened is true once ensureRuntime has successfully constructed runtime
	// (i.e. Options.DB was actually opened). Close and the leak-check
	// finalizer both read it to decide whether there is anything to release
	// or warn about.
	opened bool
	// closed is true once Close has been called.
	closed bool
}

// ensureRuntime lazily constructs the real pkg/conduit Runtime — opening
// Options.DB and every service built on top of it — the first time it is
// called, from whichever of Run or Import gets there first; every later or
// concurrent caller blocks until that first call finishes and then observes
// the same (*pkgconduit.Runtime, error) pair, without reopening anything.
// This is the mechanism behind Engine's lazy-DB lifecycle contract (see
// Engine's doc) — New itself never calls this.
//
// Only the very first caller's ctx bounds the Postgres/SQLite dial (via
// pkgconduit.WithDialContext); once the runtime exists (or has permanently
// failed to), a later call's ctx has no effect. ctx is not retained beyond
// this call in either case.
//
// Known limitation: only the first caller's ctx bounds the dial — a
// concurrent second caller (e.g. Import racing Run) has no independent
// timeout of its own. If that first caller's ctx is short-lived and gets
// canceled, the second caller's dial observes that same cancellation even
// though its own ctx may still have time left. This is accepted for v1: the
// alternative (bounding the dial by whichever caller's ctx is most
// permissive, or racing the dial per caller) adds real complexity for a
// narrow window that only matters when two callers race the very first
// Run/Import on an Engine.
//
// Once Close has been called, ensureRuntime never opens a runtime — even if
// Run or Import is called for the first time afterward, with no prior open —
// so a caller cannot resurrect resources it already believed were released.
// It returns the same conduiterr.CodeInvalidArgument used elsewhere in this
// package for caller-misuse states (double Run, nil-Handle Stop) rather than
// minting a new code for this one.
func (e *Engine) ensureRuntime(ctx context.Context) (*pkgconduit.Runtime, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		// Guards against Run/Import being called after Close with no prior
		// ensureRuntime call: without this check, a caller that already
		// believes its Engine's resources are released would silently open a
		// brand new runtime (and database) here instead of getting an error.
		return nil, conduiterr.New(conduiterr.CodeInvalidArgument,
			"conduit: Run/Import called on an Engine after Close")
	}

	if e.initDone {
		return e.runtime, e.initErr
	}
	e.initDone = true

	opts := make([]pkgconduit.RuntimeOption, len(e.runtimeOpts), len(e.runtimeOpts)+1)
	copy(opts, e.runtimeOpts)
	opts = append(opts, pkgconduit.WithDialContext(ctx))

	rt, err := pkgconduit.NewRuntime(e.cfg, opts...)
	if err != nil {
		e.initErr = err
		return nil, err
	}

	e.runtime = rt
	e.opened = true
	// Leak-check safety net (see engineLeakCheckFinalizer's doc): only armed
	// once resources actually exist to leak. Close clears it again.
	goruntime.SetFinalizer(e, engineLeakCheckFinalizer)

	return rt, nil
}

// engineLeakCheckFinalizer is Engine's GC safety net (see Engine's "Lifecycle
// contract" doc): if an Engine that actually opened resources is
// garbage-collected without Close ever being called, this logs a leak
// warning instead of letting the database handle leak silently for the rest
// of the process's life. Close clears the finalizer (via
// runtime.SetFinalizer(e, nil)) on every real call, so a well-behaved caller
// never sees this fire.
//
// It deliberately does not call Close itself: a finalizer runs on a
// runtime-managed goroutine with no ordering guarantee relative to the rest
// of the program (and, per the runtime.SetFinalizer doc, is not guaranteed to
// run at all before the process exits) — auto-releasing the database from
// here would trade the leak footgun for an even harder-to-debug
// use-after-close footgun. Logging is the safety net; it is not a substitute
// for calling Close.
func engineLeakCheckFinalizer(e *Engine) {
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return
	}

	logger := e.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn(
		"conduit: Engine garbage-collected without Close being called; its database resources "+
			"(a Badger file lock, or a Postgres/SQLite connection pool) were never released — "+
			"call Engine.Close once an Engine that has run or imported a pipeline is no longer needed",
		"component", "conduit.Engine",
	)
}

// New validates opts and returns a not-yet-running Engine. It does not open
// Options.DB, dial anything, or construct pkg/conduit's Runtime — that is
// deferred to the first call to Run or Import (see ensureRuntime and Engine's
// "Lifecycle contract" doc). ctx is not used beyond this call (there is
// nothing left to bound: validation does no I/O); Run's and Import's own ctx
// bounds the later database dial.
//
// Every failure is returned as an error — a *conduiterr.ConduitError where
// classifiable — never an os.Exit and never a panic on a caller-supplied
// misconfiguration.
func New(_ context.Context, opts Options) (*Engine, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	ctxLogger := newSlogCtxLogger(logger)

	cfg := pkgconduit.DefaultConfig()

	if opts.PipelinesDir == "" {
		// B1 DX hardening fix: an embed caller who leaves PipelinesDir unset
		// must never have Conduit silently scan DefaultConfig's CLI-oriented
		// cwd/pipelines default — that directory usually doesn't exist for a
		// pure-embed caller, and the resulting provisioning failure used to
		// surface only as a swallowed ERROR log while Run still returned a
		// healthy Handle. Disabled short-circuits file provisioning entirely
		// (see pkgconduit.Config.Pipelines.Disabled's doc and initServices);
		// cfg.Pipelines.Path is deliberately left at its DefaultConfig value
		// (unused while Disabled) rather than "", which would instead fail
		// cfg.Validate()'s required-field check just below.
		cfg.Pipelines.Disabled = true
	} else {
		cfg.Pipelines.Path = opts.PipelinesDir
	}

	cfg.API.Enabled = opts.API.Enabled
	if opts.API.Enabled {
		cfg.API.GRPC.Address = opts.API.GRPCAddress
		cfg.API.HTTP.Address = opts.API.HTTPAddress
	}

	cfg.DB.Driver = opts.DB.Driver
	if opts.DB.Driver == nil {
		dbType := opts.DB.Type
		if dbType == "" {
			dbType = pkgconduit.DBTypeInMemory
		}
		cfg.DB.Type = dbType
		cfg.DB.Badger.Path = opts.DB.Badger.Path
		cfg.DB.Postgres.ConnectionString = opts.DB.Postgres.ConnectionString
		cfg.DB.Postgres.Table = opts.DB.Postgres.Table
		cfg.DB.SQLite.Path = opts.DB.SQLite.Path
		cfg.DB.SQLite.Table = opts.DB.SQLite.Table
	}

	// pkgconduit.NewRuntime (deferred to ensureRuntime, on first Run/Import)
	// validates cfg again before opening anything; validating here too gives
	// New its documented fail-fast behavior without needing to open the
	// database — cfg.Validate() does no I/O of its own.
	if err := cfg.Validate(); err != nil {
		return nil, cerrors.Errorf("invalid config: %w", err)
	}

	runtimeOpts := []pkgconduit.RuntimeOption{
		pkgconduit.WithLogger(ctxLogger),
	}
	if opts.MetricsRegisterer != nil {
		runtimeOpts = append(runtimeOpts, pkgconduit.WithMetricsRegisterer(opts.MetricsRegisterer))
	}

	return &Engine{
		cfg:         cfg,
		runtimeOpts: runtimeOpts,
		logger:      logger,
	}, nil
}

// Close releases whatever ensureRuntime actually opened — currently, the
// configured database.DB — regardless of whether Run was ever called on this
// Engine. See Engine's "Lifecycle contract" doc for the states Close must
// cover.
//
// Close is a safe no-op if nothing was ever opened: an Engine that never had
// Run or Import called on it holds no resources at all (Options.DB opens
// lazily — see New's and ensureRuntime's doc), so there is nothing to release
// and Close returns nil without touching anything.
//
// Close is idempotent: it is safe to call more than once, from any goroutine,
// and safe to call after a successful Run→Stop cycle (where the database was
// already released via Runtime's own cleanup path) — every call after the
// first returns the same result. When resources were opened, it delegates to
// Runtime.CloseDB, whose sync.Once is what actually makes double-release
// across both paths safe; Close itself does not need a separate guard for
// that.
//
// Close does not stop a running Engine — an Engine with Run in flight must be
// stopped via Handle.Stop first (Invariant 7: that is the graceful-drain
// path). Calling Close while Run is running closes the database out from
// under the running pipelines, which is a caller error, not something Close
// detects or prevents. Likewise, calling Close concurrently with the very
// first Run or Import call on this Engine (i.e. while ensureRuntime is still
// opening the database for the first time) blocks until that open attempt
// finishes (Close and ensureRuntime share the same mutex) and then releases
// whatever it produced — but starting that race at all is caller-orchestrated
// concurrency this method does not otherwise guard against.
//
// ctx is accepted for interface symmetry with Stop and for future resources
// that may need a bounded release; the current database.DB.Close call is not
// itself context-aware.
func (e *Engine) Close(_ context.Context) error {
	e.mu.Lock()
	e.closed = true
	opened := e.opened
	rt := e.runtime
	e.mu.Unlock()

	// A real Close means the leak-check finalizer no longer applies, whether
	// or not it was ever armed (SetFinalizer on an object with none set is a
	// harmless no-op).
	goruntime.SetFinalizer(e, nil)

	if !opened {
		return nil
	}
	return rt.CloseDB()
}

// Handle is returned by Engine.Run once the engine has successfully started.
// Use Stop to drain and shut it down. A Handle is only ever constructed
// inside Engine.Run — there is no supported way to obtain one otherwise.
type Handle struct {
	cancel   context.CancelFunc
	done     chan error
	stopOnce sync.Once
	stopErr  error
}

// Run starts the engine: on first call, it lazily opens Options.DB and
// constructs every service (ensureRuntime — a no-op if Import already did
// this), then initializes all services and, if Options.API.Enabled, begins
// serving the HTTP/gRPC API. It returns as soon as either the engine becomes
// ready to accept work (a non-nil *Handle) or startup definitively fails (a
// non-nil error) — it never blocks indefinitely.
//
// ctx bounds the database dial if this is the first Run or Import call on
// this Engine (see ensureRuntime), and then governs the engine's entire run:
// cancelling it starts the same graceful drain Handle.Stop triggers
// (Invariant 7) — Run reuses pkg/conduit's Runtime.Run and its existing
// ctx-cancellation-driven tomb drain unchanged, the same mechanism `conduit
// run`'s SIGTERM handling already drives from an OS signal via
// Entrypoint.CancelOnInterrupt. Run does not store ctx beyond deriving a
// cancelable child from it; a caller-supplied deadline propagates normally.
//
// Run must be called at most once per Engine. A second call returns a coded
// error (conduiterr.CodeInvalidArgument) rather than racing a second tomb
// against the first — this is deliberately not a new conduiterr code: a
// double Run is caller misuse that has never been observed in practice, not
// a distinct, documented failure mode that needs its own machine-actionable
// identity.
//
// A caller-configured Options.PipelinesDir that fails to provision (a missing
// directory, malformed pipeline YAML, ...) is a startup failure Run surfaces
// as a returned error — not merely a log line, unlike `conduit run`'s own
// default behavior for the identical failure (see
// pkgconduit.Runtime.ProvisioningErr's doc, which this reads: that field is
// purely additive and `conduit run` never consults it). B1 DX hardening: the
// CLI's swallow-unless-exit-on-degraded default, reused unchanged on the
// embed path, violated New's "every failure returned as an error" promise.
// Run detects this right after Ready fires (the same point
// ProvisionService.Init has already run), gracefully stops what it just
// started (Invariant 7 — the identical drain Handle.Stop would trigger), and
// returns the provisioning error instead of a Handle. This never fires when
// Options.PipelinesDir is empty: file provisioning is Disabled entirely in
// that case (see New's doc), not attempted against a possibly-nonexistent
// path.
func (e *Engine) Run(ctx context.Context) (*Handle, error) {
	if !e.started.CompareAndSwap(false, true) {
		return nil, conduiterr.New(conduiterr.CodeInvalidArgument, "conduit: Run called more than once on this Engine")
	}

	rt, err := e.ensureRuntime(ctx)
	if err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- rt.Run(runCtx) }()

	select {
	case <-rt.Ready:
		if provErr := rt.ProvisioningErr; provErr != nil {
			// Fix (B1 DX hardening): declare this Run a failure instead of
			// handing back a Handle that looks healthy over broken file
			// provisioning. cancel+drain first (Invariant 7's graceful
			// shutdown), exactly what Handle.Stop would do, since no Handle
			// is being returned for the caller to Stop later.
			cancel()
			<-done
			wrapped := cerrors.Errorf("conduit: pipeline provisioning failed for pipelines dir %q: %w", e.cfg.Pipelines.Path, provErr)
			return nil, conduiterr.Wrap(conduiterr.CodeInvalidArgument, wrapped.Error(), wrapped)
		}
		return &Handle{cancel: cancel, done: done}, nil
	case err := <-done:
		// Run returned — almost always a failure — before Ready ever closed.
		// AC-4: this is the *other* of the two, and only two, ways Run
		// resolves; it must never block waiting for Ready once done has
		// already fired.
		cancel() // release runCtx's resources; the tomb has already exited
		if err == nil {
			err = cerrors.New("conduit: engine stopped before becoming ready")
		}
		return nil, err
	}
}

// Stop drains in-flight records and checkpoints, then shuts the engine down.
//
// Invariant 7: Stop triggers the identical ctx-cancellation-driven drain
// Runtime.Run's registerCleanup dispatches to (registerCleanupV1/V2,
// unchanged by this package) — the same path `conduit run`'s SIGTERM
// handling already exercises. Stop does not reimplement or duplicate that
// logic; it only supplies a different trigger (an explicit call instead of a
// signal).
//
// Stop is idempotent: concurrent or repeated calls return the same result,
// with no panic and no double-close. Calling Stop on a nil *Handle is a
// caller bug (see Handle's doc); Stop reports it as a coded error
// (conduiterr.CodeInvalidArgument — again deliberately not a new code, see
// Run's doc) rather than panicking.
//
// If ctx is done before the drain completes, Stop returns a coded timeout
// error without blocking forever; the underlying drain continues in the
// background (Runtime.Run's own registerCleanup timeout, exitTimeout, still
// applies) — a subsequent Stop call returns the same timeout result rather
// than re-waiting, per the idempotency guarantee above.
func (h *Handle) Stop(ctx context.Context) error {
	if h == nil {
		return conduiterr.New(conduiterr.CodeInvalidArgument, "conduit: Stop called on a nil *Handle")
	}

	h.stopOnce.Do(func() {
		h.cancel()
		select {
		case h.stopErr = <-h.done:
			if h.stopErr != nil && cerrors.Is(h.stopErr, context.Canceled) {
				// A canceled ctx is Stop's own expected shutdown trigger, not
				// a failure the caller needs to see.
				h.stopErr = nil
			}
		case <-ctx.Done():
			h.stopErr = conduiterr.Wrap(conduiterr.CodeInvalidArgument,
				"conduit: timed out waiting for the engine to stop", ctx.Err())
		}
	})
	return h.stopErr
}

// Import creates or updates a pipeline from cfg, delegating to the same
// provisioning path the HTTP/gRPC API and CLI use
// (provisioning.Service.Import) — no divergent logic. cfg is tagged as
// programmatically provisioned internally, so it is not removed by
// config-file reconciliation on a future Run (see
// pkg/provisioning/import.go's doc on Import vs. the config-file path).
//
// Import enriches cfg with the same defaults the file-based provisioning path
// applies (provisioning/config.Enrich — DLQ defaults, connector/processor
// Settings, ID namespacing) and validates the result (provisioning/config.Validate)
// before handing it to the provisioning service, exactly mirroring
// provisioning.Service's own file-based provisionPipeline. This matters for a
// hand-built PipelineConfig literal specifically: provisioning.Service.Import
// assumes an already-enriched config (e.g. it dereferences cfg.DLQ.WindowSize/
// WindowNackThreshold directly, which are nil on a zero-value DLQ) — skipping
// Enrich here would crash on the exact construction pattern Import exists for.
// A malformed cfg (after enrichment) is rejected as a coded error from
// Validate, not a later, harder-to-diagnose provisioning failure.
//
// Import may be called before Run — a pure-embed, Import-driven caller that
// never needs a Handle need not call Run at all. It is (with Run) one of the
// two calls that can trigger ensureRuntime and lazily open Options.DB, if
// neither has run yet on this Engine — see Engine's "Lifecycle contract" doc.
func (e *Engine) Import(ctx context.Context, cfg PipelineConfig) error {
	rt, err := e.ensureRuntime(ctx)
	if err != nil {
		return err
	}

	cfg = provisioningconfig.Enrich(cfg)
	if err := provisioningconfig.Validate(cfg); err != nil {
		return err
	}
	return rt.ProvisionService.Import(ctx, cfg)
}

// ImportPipeline builds b and imports the result in one call — the common
// case for a host that constructs its pipeline with NewPipeline instead of
// parsing YAML. It is exactly:
//
//	cfg, err := b.Build()
//	if err != nil {
//		return err
//	}
//	return e.Import(ctx, cfg)
//
// b.Build()'s error (a coded *conduiterr.ConduitError — missing id/plugin/
// type, a duplicate connector/processor id, a nil nested builder passed to
// one of PipelineBuilder's With... methods, ...) is returned as-is, before
// Import is ever called; Import's own enrichment/validation still runs
// against the built PipelineConfig exactly as it would for any other
// caller of Import. This is the one call an embedder needs for the common
// "define a pipeline in code, run it" path — see the package Example.
func (e *Engine) ImportPipeline(ctx context.Context, b *PipelineBuilder) error {
	cfg, err := b.Build()
	if err != nil {
		return err
	}
	return e.Import(ctx, cfg)
}

// StartPipeline starts a previously imported/provisioned pipeline by ID,
// delegating to the orchestrator unchanged. Calling it before Run or Import
// has ever run on this Engine still lazily opens Options.DB (ensureRuntime)
// so this returns the orchestrator's own not-found error rather than a
// nil-pointer panic — though no pipeline could exist yet on such an Engine.
func (e *Engine) StartPipeline(ctx context.Context, pipelineID string) error {
	rt, err := e.ensureRuntime(ctx)
	if err != nil {
		return err
	}
	return rt.Orchestrator.Pipelines.Start(ctx, pipelineID)
}

// StopPipeline stops a running pipeline by ID, delegating to the
// orchestrator unchanged. force, when true, skips the graceful drain for
// that single pipeline (mirrors the orchestrator's own Stop semantics) —
// an embedder that wants Invariant-7 graceful shutdown for everything should
// prefer Handle.Stop (which always drains) over StopPipeline(force=true) for
// individual pipelines. See StartPipeline's doc on why this also routes
// through ensureRuntime.
func (e *Engine) StopPipeline(ctx context.Context, pipelineID string, force bool) error {
	rt, err := e.ensureRuntime(ctx)
	if err != nil {
		return err
	}
	return rt.Orchestrator.Pipelines.Stop(ctx, pipelineID, force)
}

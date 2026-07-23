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
	// on this path.
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
	// pipeline YAML configs Conduit provisions when Run starts. Leave empty
	// to skip file-based provisioning entirely and manage pipelines
	// exclusively through Engine.Import.
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
// New eagerly opens Options.DB (see New's doc) — that resource exists from
// New onward, independent of whether Run is ever called or succeeds. Close
// releases it, and is the only supported release path outside of a normal
// Run→Stop cycle. Concretely:
//
//   - New → Close (Run never called): Close releases the database opened by
//     New. Required if the embedder decides not to run the engine after all
//     (e.g. it was constructed speculatively, or a later precondition check
//     failed) — otherwise the DB handle (a Badger file lock, a Postgres pool,
//     a SQLite handle) leaks for the process lifetime.
//   - New → Run (fails before Ready, including the already-done-context
//     guard) → Close: started still flips true on the Run call (see below),
//     but the database was never handed to Runtime's own cleanup path
//     (registerCleanup), so Close is still required to release it.
//   - New → Run (succeeds) → Stop → Close: Stop's drain already closed the
//     database via Runtime's cleanup path. Close is safe to call anyway — it
//     is idempotent with that path, not merely with itself — and returns nil.
//   - Close is safe to call more than once from the same or different
//     goroutines; every call after the first observes the same result.
//
// started flips to true the moment Run is *called*, not when it succeeds: a
// failed Run permanently retires this Engine for running (a second Run
// attempt — even after a failure — returns the same
// conduiterr.CodeInvalidArgument "called more than once" error Run's own doc
// describes). Close is unaffected by started and may be called in any state.
type Engine struct {
	runtime *pkgconduit.Runtime
	started atomic.Bool
}

// New constructs an Engine from opts. It opens the configured database (ctx
// bounds that dial only — see pkgconduit.WithDialContext) but starts no
// pipeline, server, or goroutine; call Engine.Run for that. ctx is not
// retained past this call: Run's context is independent and must be supplied
// fresh.
//
// Every failure is returned as an error — a *conduiterr.ConduitError where
// classifiable — never an os.Exit and never a panic on a caller-supplied
// misconfiguration.
//
// New's eagerly-opened database is a resource an embedder must release: call
// Engine.Close when the returned Engine is no longer needed, whether or not
// Run was ever called — see Engine's "Lifecycle contract" doc.
func New(ctx context.Context, opts Options) (*Engine, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	ctxLogger := newSlogCtxLogger(logger)

	cfg := pkgconduit.DefaultConfig()

	if opts.PipelinesDir != "" {
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

	runtimeOpts := []pkgconduit.RuntimeOption{
		pkgconduit.WithLogger(ctxLogger),
		pkgconduit.WithDialContext(ctx),
	}
	if opts.MetricsRegisterer != nil {
		runtimeOpts = append(runtimeOpts, pkgconduit.WithMetricsRegisterer(opts.MetricsRegisterer))
	}

	rt, err := pkgconduit.NewRuntime(cfg, runtimeOpts...)
	if err != nil {
		return nil, err
	}

	return &Engine{runtime: rt}, nil
}

// Close releases resources New acquired eagerly — currently, the configured
// database.DB — regardless of whether Run was ever called on this Engine.
// See Engine's "Lifecycle contract" doc for the states Close must cover.
//
// Close is idempotent: it is safe to call more than once, from any goroutine,
// and safe to call after a successful Run→Stop cycle (where the database was
// already released via Runtime's own cleanup path) — every call after the
// first returns the same result. It delegates to Runtime.CloseDB, whose
// sync.Once is what actually makes double-release across both paths safe;
// Close itself does not need a separate guard.
//
// Close does not stop a running Engine — an Engine with Run in flight must be
// stopped via Handle.Stop first (Invariant 7: that is the graceful-drain
// path). Calling Close while Run is running closes the database out from
// under the running pipelines, which is a caller error, not something Close
// detects or prevents.
//
// ctx is accepted for interface symmetry with Stop and for future resources
// that may need a bounded release; the current database.DB.Close call is not
// itself context-aware.
func (e *Engine) Close(_ context.Context) error {
	return e.runtime.CloseDB()
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

// Run starts the engine: it initializes all services and, if
// Options.API.Enabled, begins serving the HTTP/gRPC API. It returns as soon
// as either the engine becomes ready to accept work (a non-nil *Handle) or
// startup definitively fails (a non-nil error) — it never blocks
// indefinitely.
//
// ctx governs the engine's entire run: cancelling it starts the same
// graceful drain Handle.Stop triggers (Invariant 7) — Run reuses
// pkg/conduit's Runtime.Run and its existing ctx-cancellation-driven tomb
// drain unchanged, the same mechanism `conduit run`'s SIGTERM handling
// already drives from an OS signal via Entrypoint.CancelOnInterrupt. Run
// does not store ctx beyond deriving a cancelable child from it; a
// caller-supplied deadline propagates normally.
//
// Run must be called at most once per Engine. A second call returns a coded
// error (conduiterr.CodeInvalidArgument) rather than racing a second tomb
// against the first — this is deliberately not a new conduiterr code: a
// double Run is caller misuse that has never been observed in practice, not
// a distinct, documented failure mode that needs its own machine-actionable
// identity.
func (e *Engine) Run(ctx context.Context) (*Handle, error) {
	if !e.started.CompareAndSwap(false, true) {
		return nil, conduiterr.New(conduiterr.CodeInvalidArgument, "conduit: Run called more than once on this Engine")
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- e.runtime.Run(runCtx) }()

	select {
	case <-e.runtime.Ready:
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
func (e *Engine) Import(ctx context.Context, cfg PipelineConfig) error {
	cfg = provisioningconfig.Enrich(cfg)
	if err := provisioningconfig.Validate(cfg); err != nil {
		return err
	}
	return e.runtime.ProvisionService.Import(ctx, cfg)
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
// delegating to the orchestrator unchanged.
func (e *Engine) StartPipeline(ctx context.Context, pipelineID string) error {
	return e.runtime.Orchestrator.Pipelines.Start(ctx, pipelineID)
}

// StopPipeline stops a running pipeline by ID, delegating to the
// orchestrator unchanged. force, when true, skips the graceful drain for
// that single pipeline (mirrors the orchestrator's own Stop semantics) —
// an embedder that wants Invariant-7 graceful shutdown for everything should
// prefer Handle.Stop (which always drains) over StopPipeline(force=true) for
// individual pipelines.
func (e *Engine) StopPipeline(ctx context.Context, pipelineID string, force bool) error {
	return e.runtime.Orchestrator.Pipelines.Stop(ctx, pipelineID, force)
}

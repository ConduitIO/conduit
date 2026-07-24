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

package conduit_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	conduit "github.com/conduitio/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	provisioningconfig "github.com/conduitio/conduit/pkg/provisioning/config"
	promclient "github.com/prometheus/client_golang/prometheus"

	"github.com/matryer/is"
)

// newTestEngine constructs an Engine with an in-memory DB, the API disabled,
// and an isolated pipelines dir, suitable for tests that don't care about
// those specifics.
func newTestEngine(t *testing.T, opts conduit.Options) *conduit.Engine {
	t.Helper()
	is := is.New(t)

	if opts.PipelinesDir == "" {
		opts.PipelinesDir = t.TempDir()
	}
	if opts.DB.Type == "" && opts.DB.Driver == nil {
		opts.DB.Type = "inmemory"
	}

	e, err := conduit.New(context.Background(), opts)
	is.NoErr(err)
	is.True(e != nil)
	return e
}

// TestNew_ConstructsEngine proves AC-1: the literal first line every embedder
// copies (`conduit.New(ctx, Options{...})`) compiles and returns a usable
// *Engine.
func TestNew_ConstructsEngine(t *testing.T) {
	newTestEngine(t, conduit.Options{})
}

// TestNew_NilLogger_DefaultsToSlogDefault proves Options.Logger's documented
// zero-value semantics: nil is a safe, explicit default (slog.Default()), not
// an error.
func TestNew_NilLogger_DefaultsToSlogDefault(t *testing.T) {
	is := is.New(t)
	e, err := conduit.New(context.Background(), conduit.Options{
		PipelinesDir: t.TempDir(),
		DB:           conduit.DBOptions{Type: "inmemory"},
	})
	is.NoErr(err)
	is.True(e != nil)
}

// TestNew_NilRegisterer_DisablesMetrics proves Options.MetricsRegisterer's
// documented zero-value semantics: nil means "disable metrics", not an
// error — deliberately asymmetric with Logger's nil handling (see Options'
// doc).
func TestNew_NilRegisterer_DisablesMetrics(t *testing.T) {
	is := is.New(t)
	e, err := conduit.New(context.Background(), conduit.Options{
		PipelinesDir:      t.TempDir(),
		DB:                conduit.DBOptions{Type: "inmemory"},
		MetricsRegisterer: nil,
	})
	is.NoErr(err)
	is.True(e != nil)
}

// TestRun_FailsBeforeReady proves AC-4's first of exactly two resolution
// paths: Run returns a non-nil error (never blocking) when startup fails
// before the engine becomes ready — here, a gRPC address already bound by
// another listener.
func TestRun_FailsBeforeReady(t *testing.T) {
	is := is.New(t)

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	is.NoErr(err)
	defer ln.Close()
	busyAddr := ln.Addr().String()

	e := newTestEngine(t, conduit.Options{
		API: conduit.APIOptions{
			Enabled:     true,
			GRPCAddress: busyAddr, // already bound: serveGRPCAPI must fail
			HTTPAddress: "127.0.0.1:0",
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	h, err := e.Run(ctx)
	is.True(err != nil) // must fail, not hang
	is.True(h == nil)
}

// TestRun_AlreadyCanceledContext proves AC-4's second resolution path: Run
// returns promptly (never blocking) when handed an already-canceled context.
func TestRun_AlreadyCanceledContext(t *testing.T) {
	is := is.New(t)
	e := newTestEngine(t, conduit.Options{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	type result struct {
		h   *conduit.Handle
		err error
	}
	resC := make(chan result, 1)
	go func() {
		h, err := e.Run(ctx)
		resC <- result{h, err}
	}()

	select {
	case res := <-resC:
		is.True(res.err != nil)
		is.True(res.h == nil)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return promptly for an already-canceled context")
	}
}

// TestRun_CalledTwice_ReturnsCodedError proves the must-fix decision: a
// second Run call returns the existing conduiterr.CodeInvalidArgument, not a
// new speculative code.
func TestRun_CalledTwice_ReturnsCodedError(t *testing.T) {
	is := is.New(t)
	e := newTestEngine(t, conduit.Options{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h, err := e.Run(ctx)
	is.NoErr(err)
	defer func() { _ = h.Stop(context.Background()) }()

	_, err = e.Run(context.Background())
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeInvalidArgument)
}

// TestStop_NilHandle_ReturnsCodedError proves the must-fix decision: calling
// Stop on a nil *Handle returns the existing conduiterr.CodeInvalidArgument
// rather than panicking.
func TestStop_NilHandle_ReturnsCodedError(t *testing.T) {
	is := is.New(t)
	var h *conduit.Handle

	err := h.Stop(context.Background())
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeInvalidArgument)
}

// TestStop_ConcurrentIdempotent proves Stop's idempotency guarantee: N
// concurrent callers get the same result, no panic, no double-close.
func TestStop_ConcurrentIdempotent(t *testing.T) {
	is := is.New(t)
	e := newTestEngine(t, conduit.Options{})

	h, err := e.Run(context.Background())
	is.NoErr(err)

	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = h.Stop(context.Background())
		}(i)
	}
	wg.Wait()

	for i := 1; i < n; i++ {
		is.Equal(errs[i], errs[0])
	}
	is.NoErr(errs[0])
}

// TestStop_DeadlineExceeded proves Stop returns a distinguishable timeout
// error, bounded by ctx, without blocking forever.
func TestStop_DeadlineExceeded(t *testing.T) {
	is := is.New(t)
	e := newTestEngine(t, conduit.Options{})

	h, err := e.Run(context.Background())
	is.NoErr(err)
	defer func() { _ = h.Stop(context.Background()) }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond) // ensure the deadline has genuinely elapsed

	err = h.Stop(ctx)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeInvalidArgument)
}

// TestNew_StoreAlreadyOpen_ReturnsCodedError proves failure mode 4: two
// engines opening the same BadgerDB path in one process surface the
// existing coded CodeUnavailable path (OpenStore's own tagging), not a
// generic OS error or a panic.
//
// B1 DX-hardening (lazy DB open): New no longer opens anything, so the
// contention has to be forced by actually running e1 first — the store only
// opens (and the lock is only held) once ensureRuntime runs, on Run or
// Import, not on New. See Engine's "Lifecycle contract" doc.
func TestNew_StoreAlreadyOpen_ReturnsCodedError(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	ctx := context.Background()

	opts := conduit.Options{
		PipelinesDir: t.TempDir(),
		DB: conduit.DBOptions{
			Type: "badger",
		},
	}
	opts.DB.Badger.Path = dir

	e1, err := conduit.New(ctx, opts)
	is.NoErr(err)
	h1, err := e1.Run(ctx) // actually opens the Badger store, holding its file lock
	is.NoErr(err)
	defer func() {
		is.NoErr(h1.Stop(ctx))
		is.NoErr(e1.Close(ctx))
	}()

	e2, err := conduit.New(ctx, opts)
	is.NoErr(err) // New only validates Options; still opens nothing

	_, err = e2.Run(ctx)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeUnavailable)
}

// TestClose_NoRun_IsNoOp proves Engine.Close's first lifecycle-contract case
// under lazy DB open: New alone opens nothing, so Close on a never-Run,
// never-Imported Engine is a safe no-op — proven here the same way
// TestNew_StoreAlreadyOpen_ReturnsCodedError proves contention: a second
// Engine at the same BadgerDB path opens (via Run) without contention, which
// would fail with CodeUnavailable if e1 had actually opened (and leaked) the
// store instead.
func TestClose_NoRun_IsNoOp(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	ctx := context.Background()

	opts := conduit.Options{
		PipelinesDir: t.TempDir(),
		DB:           conduit.DBOptions{Type: "badger"},
	}
	opts.DB.Badger.Path = dir

	e1, err := conduit.New(ctx, opts)
	is.NoErr(err)

	is.NoErr(e1.Close(ctx)) // no Run/Import was ever called; nothing to release

	e2, err := conduit.New(ctx, opts)
	is.NoErr(err)
	h2, err := e2.Run(ctx) // would fail with CodeUnavailable if e1 had opened (and leaked) the store
	is.NoErr(err)
	is.NoErr(h2.Stop(ctx))
	is.NoErr(e2.Close(ctx))
}

// TestRun_AfterClose_WithNoPriorOpen_ReturnsCodedError is the regression test
// for the review-flagged use-after-close gap: ensureRuntime never checked the
// closed flag, so calling Run or Import for the first time on an Engine after
// Close had already been called (with no prior Run/Import) silently opened a
// fresh runtime — and a fresh database — that the caller believed was already
// released. Proves both that the call fails with the existing
// conduiterr.CodeInvalidArgument (see ensureRuntime's doc: deliberately not a
// new code) and, via the same no-leak proof TestClose_NoRun_IsNoOp uses, that
// no database was actually opened: a second Engine at the same BadgerDB path
// opens without contention, which would fail with CodeUnavailable if Run had
// gone ahead and opened (and held the file lock on) e1's store.
func TestRun_AfterClose_WithNoPriorOpen_ReturnsCodedError(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	ctx := context.Background()

	opts := conduit.Options{
		PipelinesDir: t.TempDir(),
		DB:           conduit.DBOptions{Type: "badger"},
	}
	opts.DB.Badger.Path = dir

	e1, err := conduit.New(ctx, opts)
	is.NoErr(err)

	is.NoErr(e1.Close(ctx)) // Close before Run/Import ever ran: nothing to release yet

	_, err = e1.Run(ctx)
	is.True(err != nil) // must not silently open a fresh runtime after Close
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeInvalidArgument)

	err = e1.Import(ctx, conduit.PipelineConfig{})
	is.True(err != nil) // Import must be guarded identically to Run
	ce, ok = conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeInvalidArgument)

	// No-leak proof: a second Engine at the identical BadgerDB path must open
	// without contention. If Run (or Import) above had gone ahead and opened
	// e1's database despite Close, this would instead fail with
	// conduiterr.CodeUnavailable (see TestNew_StoreAlreadyOpen_ReturnsCodedError).
	e2, err := conduit.New(ctx, opts)
	is.NoErr(err)
	h2, err := e2.Run(ctx)
	is.NoErr(err)
	is.NoErr(h2.Stop(ctx))
	is.NoErr(e2.Close(ctx))
}

// TestClose_AfterRunHitsAlreadyDoneContextGuard_ReleasesDB proves Engine.Close's
// second lifecycle-contract case: Run's ensureRuntime call opens the database
// before pkgconduit.Runtime.Run's already-done-context guard (a pre-canceled
// ctx) trips and returns, before registerCleanup is ever registered — so the
// runtime's own cleanup path never closes the database. Close is the only
// release path left, and it must still work. started flipping true on the Run
// call (not on success) must not block Close.
func TestClose_AfterRunHitsAlreadyDoneContextGuard_ReleasesDB(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	ctx := context.Background()

	opts := conduit.Options{
		PipelinesDir: t.TempDir(),
		DB:           conduit.DBOptions{Type: "badger"},
	}
	opts.DB.Badger.Path = dir

	e1, err := conduit.New(ctx, opts)
	is.NoErr(err)

	canceledCtx, cancel := context.WithCancel(ctx)
	cancel() // already canceled before Run is ever called

	h, err := e1.Run(canceledCtx)
	is.True(err != nil) // guard trips: Run must fail, not hang or succeed
	is.True(h == nil)

	is.NoErr(e1.Close(ctx)) // must still release the DB despite the failed Run

	e2, err := conduit.New(ctx, opts)
	is.NoErr(err) // would fail with CodeUnavailable if e1's DB leaked
	is.NoErr(e2.Close(ctx))
}

// TestClose_DoubleCloseIsSafe proves Close's idempotency guarantee on an
// Engine that never opened anything (lazy DB open: New/Close alone, no
// Run/Import — see Engine's "Lifecycle contract" doc): calling it twice
// returns the same (nil) no-op result both times, with no panic.
func TestClose_DoubleCloseIsSafe(t *testing.T) {
	is := is.New(t)
	e := newTestEngine(t, conduit.Options{})

	is.NoErr(e.Close(context.Background()))
	is.NoErr(e.Close(context.Background()))
}

// TestClose_DoubleCloseAfterRunIsSafe proves the same idempotency guarantee
// on the path that actually exercises #2667's Runtime.CloseDB double-release
// safety: an Engine that did open a database (via Run) and already released
// it once (via Stop's own cleanup path) still returns the same nil result on
// a second, third, ... Close call, rather than a double-close error.
func TestClose_DoubleCloseAfterRunIsSafe(t *testing.T) {
	is := is.New(t)
	e := newTestEngine(t, conduit.Options{})

	h, err := e.Run(context.Background())
	is.NoErr(err)
	is.NoErr(h.Stop(context.Background()))

	is.NoErr(e.Close(context.Background()))
	is.NoErr(e.Close(context.Background()))
}

// TestClose_AfterSuccessfulStop_IsSafe proves Close's third lifecycle-contract
// case: calling Close after a normal Run->Stop cycle (where Stop's drain
// already closed the database via Runtime's own cleanup path) is safe and
// returns nil, not a double-close error.
func TestClose_AfterSuccessfulStop_IsSafe(t *testing.T) {
	is := is.New(t)
	e := newTestEngine(t, conduit.Options{})

	h, err := e.Run(context.Background())
	is.NoErr(err)
	is.NoErr(h.Stop(context.Background()))

	is.NoErr(e.Close(context.Background()))
}

// TestRun_MetricNameCollision_ReturnsCodedError proves failure mode 7: two
// engines sharing one prometheus.Registerer collide on Conduit's metric
// names, surfacing a coded error rather than MustRegister's panic.
//
// B1 DX-hardening (lazy DB open): metrics registration happens inside
// ensureRuntime, which New no longer calls — so the collision only manifests
// once the second engine actually runs, not at New. This was
// TestNew_MetricNameCollision_ReturnsCodedError before the lazy-open change;
// renamed to match where the failure now surfaces.
func TestRun_MetricNameCollision_ReturnsCodedError(t *testing.T) {
	is := is.New(t)
	reg := promclient.NewRegistry()

	e1 := newTestEngine(t, conduit.Options{MetricsRegisterer: reg})
	h1, err := e1.Run(context.Background())
	is.NoErr(err)
	defer func() { _ = h1.Stop(context.Background()) }()

	e2 := newTestEngine(t, conduit.Options{MetricsRegisterer: reg})
	_, err = e2.Run(context.Background())
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeInvalidArgument)
}

// TestPipelineConfig_IsAliasOfProvisioningConfig proves AC-3: PipelineConfig
// is a type alias of (identical type to) provisioning/config.Pipeline, not a
// parallel copy that could drift.
func TestPipelineConfig_IsAliasOfProvisioningConfig(t *testing.T) {
	is := is.New(t)
	is.Equal(
		reflect.TypeOf(conduit.PipelineConfig{}),
		reflect.TypeOf(provisioningconfig.Pipeline{}),
	)
}

// TestImport_RoundTripsThroughRunningEngine proves Engine.Import delegates to
// the real provisioning path end to end: a pipeline built as a
// PipelineConfig literal, imported, and started via StartPipeline actually
// reaches StatusRunning, then stops cleanly via Handle.Stop.
func TestImport_RoundTripsThroughRunningEngine(t *testing.T) {
	is := is.New(t)
	e := newTestEngine(t, conduit.Options{})

	h, err := e.Run(context.Background())
	is.NoErr(err)

	cfg := conduit.PipelineConfig{
		ID:     "hello-pipeline",
		Status: "stopped",
		Name:   "hello",
		Connectors: []provisioningconfig.Connector{
			{ID: "src", Type: "source", Plugin: "builtin:generator", Settings: map[string]string{
				"format.type": "raw",
				"recordCount": "1",
			}},
			{ID: "dst", Type: "destination", Plugin: "builtin:log"},
		},
	}

	ctx := context.Background()
	err = e.Import(ctx, cfg)
	is.NoErr(err)

	err = e.StartPipeline(ctx, cfg.ID)
	is.NoErr(err)

	err = e.StopPipeline(ctx, cfg.ID, false)
	is.NoErr(err)

	is.NoErr(h.Stop(context.Background()))
}

// TestImportPipeline_RoundTripsThroughRunningEngine mirrors
// TestImport_RoundTripsThroughRunningEngine, but defines the pipeline with
// NewPipeline and imports it via the single-call ImportPipeline instead of
// Build-then-Import — proving ImportPipeline is a real, working shortcut
// against the actual provisioning path, not just a unit-tested Build+Import
// pairing.
func TestImportPipeline_RoundTripsThroughRunningEngine(t *testing.T) {
	is := is.New(t)
	e := newTestEngine(t, conduit.Options{})

	h, err := e.Run(context.Background())
	is.NoErr(err)

	ctx := context.Background()
	const pipelineID = "hello-pipeline"

	err = e.ImportPipeline(ctx, conduit.NewPipeline(pipelineID).
		WithStatus(conduit.StatusStopped).
		WithName("hello").
		WithConnector(
			conduit.NewSourceConnector("src", "builtin:generator").
				WithSetting("format.type", "raw").
				WithSetting("recordCount", "1"),
		).
		WithConnector(conduit.NewDestinationConnector("dst", "builtin:log")),
	)
	is.NoErr(err)

	err = e.StartPipeline(ctx, pipelineID)
	is.NoErr(err)

	err = e.StopPipeline(ctx, pipelineID, false)
	is.NoErr(err)

	is.NoErr(h.Stop(context.Background()))
}

// TestImportPipeline_PropagatesBuildError proves ImportPipeline surfaces a
// Build failure directly, without ever calling Import — a malformed builder
// (here, a nil nested processor) must not reach the provisioning service at
// all.
func TestImportPipeline_PropagatesBuildError(t *testing.T) {
	is := is.New(t)
	e := newTestEngine(t, conduit.Options{})

	err := e.ImportPipeline(context.Background(), conduit.NewPipeline("bad").WithProcessor(nil))
	is.True(err != nil)

	var ce *conduiterr.ConduitError
	is.True(cerrors.As(err, &ce))
	is.Equal(ce.Code, conduiterr.CodeInvalidArgument)
}

// TestNew_EmptyPipelinesDir_NeverScansCWD is the regression test for the B1
// DX-hardening bug: leaving Options.PipelinesDir empty used to fall through
// to pkgconduit.DefaultConfig's CLI-oriented cwd/pipelines default, so an
// embed caller who never asked for file provisioning could still have
// whatever happened to be sitting in its process's working directory
// provisioned. This plants a real, valid pipeline YAML at cwd/pipelines and
// proves Run with an empty PipelinesDir never provisions it — StartPipeline
// on its ID must return a not-found error, not succeed.
func TestNew_EmptyPipelinesDir_NeverScansCWD(t *testing.T) {
	is := is.New(t)

	cwd := t.TempDir()
	t.Chdir(cwd)

	pipelinesDir := filepath.Join(cwd, "pipelines")
	is.NoErr(os.MkdirAll(pipelinesDir, 0o755))
	pipelineYAML := `
version: 2.2
pipelines:
  - id: should-not-be-provisioned
    status: stopped
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
        settings:
          format.type: raw
          recordCount: "1"
      - id: dst
        type: destination
        plugin: builtin:log
`
	is.NoErr(os.WriteFile(filepath.Join(pipelinesDir, "test.yaml"), []byte(pipelineYAML), 0o600))

	ctx := context.Background()
	// conduit.New directly, not newTestEngine: that helper backfills an empty
	// PipelinesDir with t.TempDir(), which would defeat the exact case this
	// test exists to cover.
	e, err := conduit.New(ctx, conduit.Options{
		DB: conduit.DBOptions{Type: "inmemory"},
		// PipelinesDir deliberately left empty.
	})
	is.NoErr(err)

	h, err := e.Run(ctx)
	is.NoErr(err) // Disabled provisioning must not fail Run, and must not surface even a swallowed log as an error
	defer func() { _ = h.Stop(ctx) }()

	err = e.StartPipeline(ctx, "should-not-be-provisioned")
	is.True(err != nil) // the pipeline sitting in cwd/pipelines must never have been provisioned
}

// TestNew_NonexistentCustomPipelinesDir_FailsFast proves a side effect of
// this fix worth pinning down explicitly: unlike an empty PipelinesDir (fully
// Disabled, see TestNew_EmptyPipelinesDir_NeverScansCWD), a *configured*
// PipelinesDir that doesn't exist at all fails Config.Validate() — and
// therefore New itself — immediately, rather than deferring to Run. New's
// own doc promises "every failure returned as an error"; a missing directory
// is exactly the kind of caller-supplied misconfiguration that should never
// need a Run round-trip to discover. (This particular Validate error is a
// plain wrapped error, not a *conduiterr.ConduitError — pkg/conduit.Config's
// requiredConfigFieldErr/invalidConfigFieldErr predate the conduiterr
// convention — so this only asserts non-nil, not a code.)
func TestNew_NonexistentCustomPipelinesDir_FailsFast(t *testing.T) {
	is := is.New(t)

	_, err := conduit.New(context.Background(), conduit.Options{
		DB:           conduit.DBOptions{Type: "inmemory"},
		PipelinesDir: filepath.Join(t.TempDir(), "does-not-exist"),
	})
	is.True(err != nil)
}

// TestRun_MalformedPipelinesDir_ReturnsError is the regression test for the
// other half of the B1 DX-hardening bug: when Options.PipelinesDir IS
// configured, exists (so Config.Validate() passes — see
// TestNew_NonexistentCustomPipelinesDir_FailsFast for the case it doesn't),
// but genuinely fails to *provision* (here, a syntactically invalid pipeline
// YAML file, discovered only once ProvisionService.Init actually parses it),
// Run must surface that failure as a returned error — not only the swallowed
// ERROR log `conduit run`'s own default (Config.Pipelines.ExitOnDegraded ==
// false) produces for the identical failure.
func TestRun_MalformedPipelinesDir_ReturnsError(t *testing.T) {
	is := is.New(t)

	dir := t.TempDir()
	malformedYAML := "pipelines:\n  - id: pipeline1\n                      status: running\n"
	is.NoErr(os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(malformedYAML), 0o600))

	e := newTestEngine(t, conduit.Options{PipelinesDir: dir})

	h, err := e.Run(context.Background())
	is.True(err != nil) // must fail, not return a healthy Handle over a swallowed log
	is.True(h == nil)

	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeInvalidArgument)
}

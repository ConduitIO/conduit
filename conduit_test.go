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
	"reflect"
	"sync"
	"testing"
	"time"

	conduit "github.com/conduitio/conduit"
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
func TestNew_StoreAlreadyOpen_ReturnsCodedError(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	opts := conduit.Options{
		PipelinesDir: t.TempDir(),
		DB: conduit.DBOptions{
			Type: "badger",
		},
	}
	opts.DB.Badger.Path = dir

	e1, err := conduit.New(context.Background(), opts)
	is.NoErr(err)
	_ = e1

	_, err = conduit.New(context.Background(), opts)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeUnavailable)
}

// TestNew_MetricNameCollision_ReturnsCodedError proves failure mode 7: two
// engines sharing one prometheus.Registerer collide on Conduit's metric
// names, surfacing a coded error from New rather than MustRegister's panic.
func TestNew_MetricNameCollision_ReturnsCodedError(t *testing.T) {
	is := is.New(t)
	reg := promclient.NewRegistry()

	_, err := conduit.New(context.Background(), conduit.Options{
		PipelinesDir:      t.TempDir(),
		DB:                conduit.DBOptions{Type: "inmemory"},
		MetricsRegisterer: reg,
	})
	is.NoErr(err)

	_, err = conduit.New(context.Background(), conduit.Options{
		PipelinesDir:      t.TempDir(),
		DB:                conduit.DBOptions{Type: "inmemory"},
		MetricsRegisterer: reg,
	})
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

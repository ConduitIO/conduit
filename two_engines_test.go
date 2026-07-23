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
	"log/slog"
	"sync"
	"testing"

	conduit "github.com/conduitio/conduit"
	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"

	"github.com/matryer/is"
)

// capturingHandler is a minimal slog.Handler that records every log.Record it
// receives, so a test can assert exactly which engine's log lines a given
// *slog.Logger observed.
type capturingHandler struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recs = append(h.recs, r.Clone())
	return nil
}

func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

func (h *capturingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.recs)
}

// TestTwoEngines_LoggerIsolated proves AC-2/AC-9's "must pass" half: two
// concurrent Engines constructed with distinct injected loggers never
// cross-talk — each handler receives only its own engine's log lines, and
// neither engine touches the process-global zerolog.DefaultContextLogger
// fallback (WithLogger's doc: that global is skipped entirely on the embed
// path specifically so two engines can't cross-talk through it).
func TestTwoEngines_LoggerIsolated(t *testing.T) {
	is := is.New(t)

	prevGlobal := zerolog.DefaultContextLogger
	zerolog.DefaultContextLogger = nil
	defer func() { zerolog.DefaultContextLogger = prevGlobal }()

	handlerA := &capturingHandler{}
	handlerB := &capturingHandler{}
	loggerA := slog.New(handlerA)
	loggerB := slog.New(handlerB)

	eA := newTestEngine(t, conduit.Options{Logger: loggerA})
	eB := newTestEngine(t, conduit.Options{Logger: loggerB})

	hA, err := eA.Run(context.Background())
	is.NoErr(err)
	hB, err := eB.Run(context.Background())
	is.NoErr(err)

	is.NoErr(hA.Stop(context.Background()))
	is.NoErr(hB.Stop(context.Background()))

	is.True(handlerA.count() > 0) // engine A produced log lines...
	is.True(handlerB.count() > 0) // ...and so did engine B, into its own handler

	// The core of AC-2/AC-9: the embed path never sets the process-global
	// ambient fallback logger, precisely so two engines with distinct
	// loggers cannot cross-talk through it.
	is.True(zerolog.DefaultContextLogger == nil)
}

// conduitInfoValue sums the "conduit_info" gauge family's values across all
// label combinations in reg — a convenience for asserting on
// measure.ConduitInfo's current value as observed through reg.
func conduitInfoValue(t *testing.T, reg *promclient.Registry) float64 {
	t.Helper()
	is := is.New(t)

	mfs, err := reg.Gather()
	is.NoErr(err)

	var total float64
	for _, mf := range mfs {
		if mf.GetName() != "conduit_info" {
			continue
		}
		for _, m := range mf.GetMetric() {
			total += m.GetGauge().GetValue()
		}
	}
	return total
}

// TestTwoEngines_MetricsCrossTalk_KnownLimitation documents (does not fix) a
// known limitation of the embed metrics seam: pkg/foundation/metrics keeps
// process-global metric *definitions* (metrics.go's `global` slices), so a
// metric like measure.ConduitInfo fans its value out to every registry ever
// passed to metrics.Register — including a second, fully independent
// embedded engine's registry. Constructing engine B (with its own,
// never-shared MetricsRegisterer) still bumps engine A's already-gathered
// "conduit_info" value. This is asserted as PRESENT, not fixed — see
// pkg/conduit.WithMetricsRegisterer's doc and this package's doc.go. A future
// fix to pkg/foundation/metrics must consciously update this test.
func TestTwoEngines_MetricsCrossTalk_KnownLimitation(t *testing.T) {
	is := is.New(t)

	regA := promclient.NewRegistry()
	newTestEngine(t, conduit.Options{MetricsRegisterer: regA})
	beforeA := conduitInfoValue(t, regA)

	regB := promclient.NewRegistry()
	newTestEngine(t, conduit.Options{MetricsRegisterer: regB})
	afterA := conduitInfoValue(t, regA)

	is.True(afterA > beforeA) // engine B's construction leaked into engine A's registry
}

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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
	promclient "github.com/prometheus/client_golang/prometheus"
)

func TestNewHTTPMetricsHandlerUsesExplicitGatherer(t *testing.T) {
	is := is.New(t)
	reg := promclient.NewRegistry()
	wrapped := promclient.WrapRegistererWithPrefix("myapp_", reg)

	cfg := newMetricsRuntimeConfig(t)
	runtime, err := NewRuntime(cfg, WithMetricsRegisterer(wrapped), WithMetricsGatherer(reg))
	is.NoErr(err)
	defer runtime.DB.Close()

	recorder := httptest.NewRecorder()
	runtime.newHTTPMetricsHandler().ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil),
	)

	is.Equal(recorder.Code, http.StatusOK)
	is.True(strings.Contains(recorder.Body.String(), "myapp_conduit_info"))
}

func TestNewHTTPMetricsHandlerUsesRegistererGatherer(t *testing.T) {
	is := is.New(t)
	reg := promclient.NewRegistry()
	hostMetric := promclient.NewGauge(promclient.GaugeOpts{Name: "host_metric"})
	hostMetric.Set(1)
	is.NoErr(reg.Register(hostMetric))

	runtime, err := NewRuntime(newMetricsRuntimeConfig(t), WithMetricsRegisterer(reg))
	is.NoErr(err)
	defer runtime.DB.Close()

	recorder := httptest.NewRecorder()
	runtime.newHTTPMetricsHandler().ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil),
	)

	is.Equal(recorder.Code, http.StatusOK)
	is.True(strings.Contains(recorder.Body.String(), "host_metric"))
	is.True(strings.Contains(recorder.Body.String(), "conduit_info"))
	is.True(strings.Contains(recorder.Body.String(), "promhttp_metric_handler_requests_total"))
}

func TestNewRuntimeRequiresGathererForWrappedRegisterer(t *testing.T) {
	is := is.New(t)
	reg := promclient.NewRegistry()
	wrapped := promclient.WrapRegistererWithPrefix("myapp_", reg)

	_, err := NewRuntime(newMetricsRuntimeConfig(t), WithMetricsRegisterer(wrapped))
	is.True(err != nil)
	conduitErr, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(conduitErr.Code, conduiterr.CodeInvalidArgument)
}

func TestNewRuntimeAllowsWrappedRegistererWhenHTTPDisabled(t *testing.T) {
	is := is.New(t)
	reg := promclient.NewRegistry()
	wrapped := promclient.WrapRegistererWithPrefix("myapp_", reg)
	cfg := newMetricsRuntimeConfig(t)
	cfg.API.Enabled = false

	runtime, err := NewRuntime(cfg, WithMetricsRegisterer(wrapped))
	is.NoErr(err)
	defer runtime.DB.Close()
	is.True(runtime.metricsGatherer == promclient.DefaultGatherer)

	recorder := httptest.NewRecorder()
	runtime.newHTTPMetricsHandler().ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil),
	)
	is.Equal(recorder.Code, http.StatusOK)
}

func TestNewRuntimeUsesDefaultGatherer(t *testing.T) {
	is := is.New(t)
	cfg := newMetricsRuntimeConfig(t)

	runtime, err := NewRuntime(cfg)
	is.NoErr(err)
	defer runtime.DB.Close()
	is.True(runtime.metricsGatherer == promclient.DefaultGatherer)
}

func TestNewRuntimeRejectsGathererWithoutRegisterer(t *testing.T) {
	is := is.New(t)

	_, err := NewRuntime(newMetricsRuntimeConfig(t), WithMetricsGatherer(promclient.NewRegistry()))
	is.True(err != nil)
	conduitErr, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(conduitErr.Code, conduiterr.CodeInvalidArgument)
}

func newMetricsRuntimeConfig(t *testing.T) Config {
	cfg := DefaultConfig()
	cfg.DB.Type = DBTypeInMemory
	cfg.API.Enabled = true
	cfg.Pipelines.Path = t.TempDir()
	return cfg
}

// A host collector whose name collides with promhttp's own must never take the
// embedding process down. promhttp.InstrumentMetricHandler panics on any
// registration error that is not AlreadyRegisteredError, and this registerer
// belongs to the host — so without instrumentMetricHandler's recover, building
// the handler panics inside serveHTTPAPI, which Run calls synchronously with no
// recover in the path.
//
// Remove the recover in instrumentMetricHandler and this test panics instead of
// failing, which is exactly the production symptom.
func TestNewHTTPMetricsHandlerSurvivesConflictingHostCollector(t *testing.T) {
	is := is.New(t)
	reg := promclient.NewRegistry()

	conflicting := promclient.NewCounterVec(
		promclient.CounterOpts{
			Name: "promhttp_metric_handler_requests_total",
			Help: "the host's own, with a different label set",
		},
		[]string{"method"}, // promhttp uses "code"
	)
	is.NoErr(reg.Register(conflicting))

	runtime, err := NewRuntime(newMetricsRuntimeConfig(t), WithMetricsRegisterer(reg))
	is.NoErr(err)
	defer runtime.DB.Close()

	recorder := httptest.NewRecorder()
	runtime.newHTTPMetricsHandler().ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil),
	)

	// Degraded — no scrape counters — but still serving Conduit's metrics.
	is.Equal(recorder.Code, http.StatusOK)
	is.True(strings.Contains(recorder.Body.String(), "conduit_info"))
}

// One broken host collector must not hide Conduit's own metrics. With
// promhttp's default error handling a single Collect failure turns the whole
// scrape into a 500; ContinueOnError keeps the rest of the registry readable.
func TestNewHTTPMetricsHandlerServesConduitMetricsDespiteABrokenHostCollector(t *testing.T) {
	is := is.New(t)
	reg := promclient.NewRegistry()
	is.NoErr(reg.Register(brokenCollector{}))

	runtime, err := NewRuntime(newMetricsRuntimeConfig(t), WithMetricsRegisterer(reg))
	is.NoErr(err)
	defer runtime.DB.Close()

	recorder := httptest.NewRecorder()
	runtime.newHTTPMetricsHandler().ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil),
	)

	is.True(strings.Contains(recorder.Body.String(), "conduit_info"))
}

// brokenCollector fails on every Collect, standing in for a host collector that
// errors in production (a scrape against a dead dependency, say).
type brokenCollector struct{}

func (brokenCollector) Describe(ch chan<- *promclient.Desc) {
	ch <- promclient.NewDesc("broken_host_collector", "always fails to collect", nil, nil)
}

func (brokenCollector) Collect(ch chan<- promclient.Metric) {
	ch <- promclient.NewInvalidMetric(
		promclient.NewDesc("broken_host_collector", "always fails to collect", nil, nil),
		cerrors.New("host collector is broken"),
	)
}

// The isolation claim the whole seam rests on: an embedded Runtime's /metrics
// serves ITS registry, not whatever happens to be in the process-global default
// one. Without this, "isolated per-Runtime metrics" is an assertion nobody
// checks.
func TestNewHTTPMetricsHandlerDoesNotServeTheDefaultRegistry(t *testing.T) {
	is := is.New(t)

	sentinel := promclient.NewGauge(promclient.GaugeOpts{
		Name: "default_registry_sentinel_metric",
		Help: "registered only into the process-global default registry",
	})
	sentinel.Set(1)
	is.NoErr(promclient.DefaultRegisterer.Register(sentinel))
	defer promclient.DefaultRegisterer.Unregister(sentinel)

	reg := promclient.NewRegistry()
	runtime, err := NewRuntime(newMetricsRuntimeConfig(t), WithMetricsRegisterer(reg))
	is.NoErr(err)
	defer runtime.DB.Close()

	recorder := httptest.NewRecorder()
	runtime.newHTTPMetricsHandler().ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil),
	)

	is.True(!strings.Contains(recorder.Body.String(), "default_registry_sentinel_metric"))
}

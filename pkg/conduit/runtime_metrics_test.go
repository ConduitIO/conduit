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

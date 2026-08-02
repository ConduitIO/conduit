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

	"github.com/matryer/is"
	promclient "github.com/prometheus/client_golang/prometheus"
)

func TestNewHTTPMetricsHandlerUsesEmbeddedGatherer(t *testing.T) {
	is := is.New(t)

	reg := promclient.NewRegistry()
	gauge := promclient.NewGauge(promclient.GaugeOpts{
		Name: "conduit_embedded_http_metrics_test",
		Help: "Metric used to verify the embedded HTTP metrics gatherer.",
	})
	gauge.Set(42)
	reg.MustRegister(gauge)

	cfg := DefaultConfig()
	cfg.DB.Type = DBTypeInMemory
	cfg.API.Enabled = false
	cfg.Pipelines.Path = t.TempDir()

	runtime, err := NewRuntime(cfg, WithMetricsRegisterer(reg))
	is.NoErr(err)
	defer runtime.DB.Close()

	recorder := httptest.NewRecorder()
	runtime.newHTTPMetricsHandler().ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil),
	)

	is.Equal(recorder.Code, http.StatusOK)
	is.True(strings.Contains(recorder.Body.String(), "conduit_embedded_http_metrics_test 42"))
}

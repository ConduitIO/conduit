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
	"testing"

	"github.com/conduitio/conduit/pkg/conduit"
	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"

	"github.com/matryer/is"
)

// TestNewRuntime_CLIDefaultsUnchanged proves AC-5: the RuntimeOption seams
// (WithLogger, WithMetricsRegisterer, WithDialContext) are inert unless an
// embed option is passed. It constructs a Runtime exactly the way
// Entrypoint.Serve/`conduit run` does — NewRuntime(cfg) with zero
// RuntimeOptions — and asserts the pre-seam behavior still holds:
// zerolog.DefaultContextLogger is still set globally, and the package-level
// configureMetrics() (sync.Once + promclient.MustRegister into
// promclient.DefaultRegisterer) still fires.
func TestNewRuntime_CLIDefaultsUnchanged(t *testing.T) {
	is := is.New(t)

	cfg := conduit.DefaultConfig()
	cfg.DB.Type = conduit.DBTypeInMemory
	cfg.API.Enabled = false
	cfg.Pipelines.Path = t.TempDir()

	prevGlobal := zerolog.DefaultContextLogger
	zerolog.DefaultContextLogger = nil
	defer func() { zerolog.DefaultContextLogger = prevGlobal }()

	// The exact CLI call pattern: no RuntimeOptions.
	r, err := conduit.NewRuntime(cfg)
	is.NoErr(err)
	is.True(r != nil)
	defer r.DB.Close()

	// AC-5.3: CLI path still sets the process-global fallback logger.
	is.True(zerolog.DefaultContextLogger != nil)

	// AC-5.2: CLI path still calls configureMetrics(), which registers
	// Conduit's metrics into promclient.DefaultRegisterer.
	mfs, err := promclient.DefaultGatherer.Gather()
	is.NoErr(err)
	var foundConduitMetric bool
	for _, mf := range mfs {
		if mf.GetName() == "conduit_info" {
			foundConduitMetric = true
			break
		}
	}
	is.True(foundConduitMetric) // conduit_info must be registered in the default registry on the CLI path
}

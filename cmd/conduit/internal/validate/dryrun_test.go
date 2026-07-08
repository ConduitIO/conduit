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

package validate

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

// builtinsForTest is the injected builtin set the dry-run command would build
// from builtin.DefaultBuiltinConnectors (full path + short name).
var builtinsForTest = map[string]struct{}{
	"generator": {},
	"github.com/conduitio/conduit-connector-generator": {},
}

// AC-5: dry-run exposes the fully-enriched pipeline graph — final IDs
// (pipelineID:connectorID) and injected DLQ defaults — on FileReport.Enriched.
func TestRunWithOptions_Enriched_ExposesGraph(t *testing.T) {
	is := is.New(t)

	report, err := RunWithOptions(context.Background(), "testdata/goodplugin.yaml", Options{Enriched: true})
	is.NoErr(err)
	is.Equal(len(report.Files), 1)
	is.Equal(len(report.Files[0].Enriched), 1)

	p := report.Files[0].Enriched[0]
	is.Equal(p.ID, "good-plugin-pipeline")
	is.Equal(len(p.Connectors), 1)
	is.Equal(p.Connectors[0].ID, "good-plugin-pipeline:c1") // enriched, pipelineID-prefixed
	is.True(p.DLQ.Plugin != "")                             // DLQ default injected
}

// validate/lint leave Enriched nil (it is dry-run-only).
func TestRun_ValidateMode_NoEnriched(t *testing.T) {
	is := is.New(t)

	report, err := Run(context.Background(), "testdata/goodplugin.yaml")
	is.NoErr(err)
	is.Equal(len(report.Files[0].Enriched), 0)
}

// AC-6: --resolve-plugins flags an unknown builtin as connector.plugin_not_found
// (codes.NotFound -> exit 2 via bucketRank).
func TestRunWithOptions_ResolvePlugins_UnknownBuiltin(t *testing.T) {
	is := is.New(t)

	report, err := RunWithOptions(context.Background(), "testdata/badplugin.yaml",
		Options{ResolvePlugins: true, BuiltinPlugins: builtinsForTest})
	is.NoErr(err)
	is.True(!report.OK())
	is.Equal(report.Summary.Errors, 1)

	f := report.Files[0].Findings[0]
	is.Equal(f.Severity, SeverityError)
	is.Equal(f.Code, conduiterr.CodeConnectorPluginNotFound.Reason())
	is.Equal(bucketRank(conduiterr.CodeConnectorPluginNotFound), 2) // Validation -> exit 2
}

// AC-6: a known builtin resolves cleanly.
func TestRunWithOptions_ResolvePlugins_KnownBuiltin_OK(t *testing.T) {
	is := is.New(t)

	report, err := RunWithOptions(context.Background(), "testdata/goodplugin.yaml",
		Options{ResolvePlugins: true, BuiltinPlugins: builtinsForTest})
	is.NoErr(err)
	is.True(report.OK())
	is.Equal(report.Summary.Errors, 0)
}

// AC-6: a standalone plugin reference is advisory — not statically verifiable,
// never a false failure — so resolution produces no finding for it.
func TestRunWithOptions_ResolvePlugins_Standalone_Advisory(t *testing.T) {
	is := is.New(t)

	report, err := RunWithOptions(context.Background(), "testdata/standalone-plugin.yaml",
		Options{ResolvePlugins: true, BuiltinPlugins: builtinsForTest})
	is.NoErr(err)
	is.True(report.OK())
	is.Equal(report.Summary.Errors, 0)
}

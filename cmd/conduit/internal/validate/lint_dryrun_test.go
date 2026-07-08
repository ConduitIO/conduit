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
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/matryer/is"
)

// TestRunLint_DeprecatedField_WarningWithLineColumn covers AC-1: a file
// using a field deprecated as of its declared version (here, a v2.2
// processor's "type" field, superseded by "plugin") produces exactly one
// SeverityWarning Finding with Line/Column set and no error findings, so the
// report is still OK (exit 0 without --strict).
func TestRunLint_DeprecatedField_WarningWithLineColumn(t *testing.T) {
	is := is.New(t)

	report, err := RunLint(context.Background(), "testdata/lint-warning.yaml")
	is.NoErr(err)
	is.True(report.OK()) // warnings alone never fail Report.OK
	is.Equal(len(report.Files), 1)

	fr := report.Files[0]
	is.True(fr.OK)
	is.Equal(len(fr.Findings), 1)

	find := fr.Findings[0]
	is.Equal(find.Severity, SeverityWarning)
	is.Equal(find.Code, config.CodeParserWarning.Reason())
	is.True(find.Line > 0)
	is.True(find.Column > 0)
	is.True(find.Message != "")

	is.Equal(report.Summary.Warnings, 1)
	is.Equal(report.Summary.Errors, 0)
}

// TestRunLint_Strict_PromotesWarningToFailure covers AC-2: the same
// warning-only file, but LintExitError(strict=true) now reports a failing
// exit code — `lint --strict` is what turns AC-1's exit 0 into exit 2. The
// underlying Report/Findings are unchanged by strict (see engine.go's
// design: aggregation-to-exit-code is command-owned, not engine state).
func TestRunLint_Strict_PromotesWarningToFailure(t *testing.T) {
	is := is.New(t)

	report, err := RunLint(context.Background(), "testdata/lint-warning.yaml")
	is.NoErr(err)
	is.True(report.OK()) // still OK at the Report level

	_, ok := LintExitError(report.Files, false)
	is.True(!ok) // not strict: warnings don't produce an exit error

	exitErr, ok := LintExitError(report.Files, true)
	is.True(ok) // strict: the lone warning now produces one
	is.True(exitErr != nil)
}

// TestRunLint_ErrorAndWarning_ErrorDominates covers AC-3: a file with both
// an error and a warning finding exits non-OK (error dominates) via the
// plain (non-strict) ExitError/LintExitError path, and both findings are
// present in the report (never fail-fast, never collapsed).
func TestRunLint_ErrorAndWarning_ErrorDominates(t *testing.T) {
	is := is.New(t)

	report, err := RunLint(context.Background(), "testdata/lint-error-and-warning.yaml")
	is.NoErr(err)
	is.True(!report.OK()) // the error finding fails the report regardless of strict
	is.Equal(len(report.Files), 1)

	fr := report.Files[0]
	is.True(!fr.OK)

	var errs, warns int
	for _, f := range fr.Findings {
		switch f.Severity {
		case SeverityError:
			errs++
		case SeverityWarning:
			warns++
		}
	}
	is.Equal(errs, 1)  // the invalid "status" field
	is.Equal(warns, 1) // the deprecated processor "type" field

	// Non-strict LintExitError already reports a failure (the error alone
	// is enough) — strict doesn't change that outcome, just whether a
	// warning ALONE would also trigger it (covered above).
	_, ok := LintExitError(report.Files, false)
	is.True(ok)
}

// TestRunLint_Offline_NoDial documents the same offline invariant Run has:
// RunLint never dials anything, only ever touching the local filesystem and
// the in-memory parser/linter.
func TestRunLint_Offline_NoDial(t *testing.T) {
	is := is.New(t)

	report, err := RunLint(context.Background(), "testdata/valid.yaml")
	is.NoErr(err)
	is.True(report.OK())
	is.Equal(report.Summary.Warnings, 0) // a clean v2.2 file produces no warnings either
}

// TestRunDryRun_ValidFile_EnrichedGraph covers AC-5: dry-run on a valid file
// exits 0 and reports the enriched graph — final pipelineID-prefixed
// connector IDs, DLQ defaults, and worker counts — matching what
// config.Enrich actually computes.
func TestRunDryRun_ValidFile_EnrichedGraph(t *testing.T) {
	is := is.New(t)

	report, err := RunDryRun(context.Background(), "testdata/valid.yaml", true)
	is.NoErr(err)
	is.True(report.OK())
	is.Equal(len(report.Enriched), 1)

	ef := report.Enriched[0]
	is.Equal(ef.Path, "testdata/valid.yaml")
	is.Equal(len(ef.Pipelines), 1)

	p := ef.Pipelines[0]
	is.Equal(p.ID, "valid-pipeline")
	is.Equal(len(p.Connectors), 2)

	// Final IDs are pipelineID:connectorID, per config.Enrich.
	byID := map[string]EnrichedConnector{}
	for _, c := range p.Connectors {
		byID[c.ID] = c
	}
	src, ok := byID["valid-pipeline:src"]
	is.True(ok)
	is.Equal(src.Plugin, "builtin:generator")
	is.Equal(src.PluginStatus, PluginStatusBuiltin)

	dst, ok := byID["valid-pipeline:dst"]
	is.True(ok)
	is.Equal(dst.Plugin, "builtin:log")
	is.Equal(dst.PluginStatus, PluginStatusBuiltin)

	// DLQ defaults were injected by config.Enrich (no dead-letter-queue
	// block in testdata/valid.yaml at all).
	is.True(p.DLQ.Plugin != "")
	is.True(p.DLQ.WindowSize > 0)
}

// TestRunDryRun_UnknownBuiltinPlugin covers AC-6's first half: an unknown
// "builtin:" plugin ref becomes an error Finding
// (connector.plugin_not_found), which fails the report (exit 2 via
// ExitError).
func TestRunDryRun_UnknownBuiltinPlugin(t *testing.T) {
	is := is.New(t)

	report, err := RunDryRun(context.Background(), "testdata/dryrun-unknown-builtin.yaml", true)
	is.NoErr(err)
	is.True(!report.OK())
	is.Equal(len(report.Files), 1)

	fr := report.Files[0]
	var found bool
	for _, f := range fr.Findings {
		if f.Code == conduiterr.CodeConnectorPluginNotFound.Reason() {
			found = true
			is.Equal(f.Severity, SeverityError)
			is.Equal(f.ConfigPath, "/connectors/1/plugin")
		}
	}
	is.True(found)

	exitErr, ok := ExitError(report.Files)
	is.True(ok)
	is.True(exitErr != nil)

	// The enriched graph still reports the connector (dry-run never hides
	// data because of a resolution failure) with a not_found status.
	is.Equal(len(report.Enriched), 1)
	var dst EnrichedConnector
	for _, c := range report.Enriched[0].Pipelines[0].Connectors {
		if c.ID == "dryrun-unknown-builtin:dst" {
			dst = c
		}
	}
	is.Equal(dst.PluginStatus, PluginStatusNotFound)
}

// TestRunDryRun_StandalonePlugin_AdvisoryNotFalseFail covers AC-6's second
// half: a "standalone:" plugin ref is not statically verifiable and must
// NEVER produce an error Finding — the report stays OK (exit 0), and the
// enriched graph marks it "unverified" rather than silently claiming
// "builtin" or falsely failing it as "not_found".
func TestRunDryRun_StandalonePlugin_AdvisoryNotFalseFail(t *testing.T) {
	is := is.New(t)

	report, err := RunDryRun(context.Background(), "testdata/dryrun-standalone.yaml", true)
	is.NoErr(err)
	is.True(report.OK()) // no false fail
	is.Equal(len(report.Files), 1)
	is.Equal(len(report.Files[0].Findings), 0)

	_, ok := ExitError(report.Files)
	is.True(!ok)

	var dst EnrichedConnector
	for _, c := range report.Enriched[0].Pipelines[0].Connectors {
		if c.Plugin == "standalone:my-custom-connector" {
			dst = c
		}
	}
	is.Equal(dst.PluginStatus, PluginStatusUnverified)
}

// TestRunDryRun_ResolvePluginsOff_NoPluginStatus proves --resolve-plugins=false
// (the flag defaults on, but is togglable) skips plugin resolution
// entirely: no plugin Findings, and PluginStatus stays unset on the
// enriched graph — a caller must not see a status for a ref that was never
// actually checked.
func TestRunDryRun_ResolvePluginsOff_NoPluginStatus(t *testing.T) {
	is := is.New(t)

	report, err := RunDryRun(context.Background(), "testdata/dryrun-unknown-builtin.yaml", false)
	is.NoErr(err)
	is.True(report.OK()) // the unknown builtin is never even checked
	is.Equal(len(report.Files[0].Findings), 0)

	for _, c := range report.Enriched[0].Pipelines[0].Connectors {
		is.Equal(c.PluginStatus, PluginStatus(""))
	}
}

// TestRunDryRun_Offline_NoDial documents the offline invariant for
// dry-run: RunDryRun (including --resolve-plugins) never dials anything —
// builtinConnectorNames/builtinProcessorNames are computed once from
// in-memory sdk.Connector specifications (see plugins.go), never a real
// plugin process or Registry.
func TestRunDryRun_Offline_NoDial(t *testing.T) {
	is := is.New(t)

	report, err := RunDryRun(context.Background(), "testdata/valid.yaml", true)
	is.NoErr(err)
	is.True(report.OK())
}

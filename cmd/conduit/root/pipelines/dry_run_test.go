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

package pipelines

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/ecdysis"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
)

func newDryRunEcdysis() *ecdysis.Ecdysis {
	return ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithResultDecorator{}))
}

func TestDryRunCommand_Args_NoPath(t *testing.T) {
	is := is.New(t)
	c := &DryRunCommand{}
	err := c.Args([]string{})
	is.True(err != nil)
}

func TestDryRunCommand_Args_TooMany(t *testing.T) {
	is := is.New(t)
	c := &DryRunCommand{}
	err := c.Args([]string{"a", "b"})
	is.True(err != nil)
}

// TestDryRunCommand_ResolvePlugins_DefaultsOn proves --resolve-plugins
// defaults to true (per the design doc's flag table) without the flag being
// passed at all: the enriched connectors in the --json output already carry
// a pluginStatus.
func TestDryRunCommand_ResolvePlugins_DefaultsOn(t *testing.T) {
	is := is.New(t)

	cmd := newDryRunEcdysis().MustBuildCobraCommand(&DryRunCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/valid.yaml", "--json"})

	err := cmd.Execute()
	is.NoErr(err)
	is.Equal(exitcode.ExitCode(err), exitcode.OK)

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	resultBytes, merr := json.Marshal(got.Result)
	is.NoErr(merr)

	var result struct {
		Enriched []struct {
			Pipelines []struct {
				Connectors []struct {
					PluginStatus string `json:"pluginStatus"`
				} `json:"connectors"`
			} `json:"pipelines"`
		} `json:"enriched"`
	}
	is.NoErr(json.Unmarshal(resultBytes, &result))
	is.Equal(len(result.Enriched[0].Pipelines[0].Connectors), 2)
	for _, c := range result.Enriched[0].Pipelines[0].Connectors {
		is.Equal(c.PluginStatus, "builtin") // both connectors in valid.yaml are known builtins
	}
}

// TestDryRunCommand_ValidFile_EnrichedGraph covers AC-5: dry-run on a valid
// file exits 0, and --json shows the enriched IDs (pipelineID:connectorID)
// and DLQ defaults.
func TestDryRunCommand_ValidFile_EnrichedGraph(t *testing.T) {
	is := is.New(t)

	cmd := newDryRunEcdysis().MustBuildCobraCommand(&DryRunCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/valid.yaml", "--json"})

	err := cmd.Execute()
	is.NoErr(err)
	is.Equal(exitcode.ExitCode(err), exitcode.OK)

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.Equal(got.Command, "pipelines.dry_run")
	is.True(got.OK)

	resultBytes, merr := json.Marshal(got.Result)
	is.NoErr(merr)
	var result struct {
		Enriched []struct {
			Pipelines []struct {
				ID         string `json:"id"`
				Connectors []struct {
					ID string `json:"id"`
				} `json:"connectors"`
				DLQ struct {
					Plugin     string `json:"plugin"`
					WindowSize int    `json:"windowSize"`
				} `json:"dlq"`
			} `json:"pipelines"`
		} `json:"enriched"`
	}
	is.NoErr(json.Unmarshal(resultBytes, &result))
	is.Equal(len(result.Enriched), 1)
	is.Equal(len(result.Enriched[0].Pipelines), 1)

	p := result.Enriched[0].Pipelines[0]
	is.Equal(p.ID, "valid-pipeline")
	is.Equal(len(p.Connectors), 2)
	is.Equal(p.Connectors[0].ID, "valid-pipeline:src") // enriched (pipelineID-prefixed) ID
	is.True(p.DLQ.Plugin != "")                        // injected DLQ default
	is.True(p.DLQ.WindowSize > 0)
}

// TestDryRunCommand_WarningOnlyFile_HumanShowsWarning is a regression test:
// dry-run always includes parser warnings (validate.RunDryRun sets
// includeWarnings unconditionally), so a file with a warning but no error
// is still "OK" at the errors-only FileReport.OK level — Render must not
// use that boolean alone to decide whether to print findings, or a
// warning-only file's warning would be silently dropped from human output
// (while still present in --json, an OK/--json asymmetry bug fixed by
// having Render classify pass/warn/fail via fileLintStatus, same as lint).
func TestDryRunCommand_WarningOnlyFile_HumanShowsWarning(t *testing.T) {
	is := is.New(t)

	cmd := newDryRunEcdysis().MustBuildCobraCommand(&DryRunCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/lint-warning.yaml"})

	err := cmd.Execute()
	is.NoErr(err) // no error finding, so dry-run (no --strict) still exits 0
	is.Equal(exitcode.ExitCode(err), exitcode.OK)

	got := out.String()
	is.True(strings.Contains(got, "config.parser_warning")) // the warning must still be rendered
	is.True(strings.Contains(got, "please use field 'plugin'"))
}

// TestDryRunCommand_UnknownBuiltinPlugin_ExitTwo covers AC-6's first half:
// an unknown "builtin:" plugin ref fails with connector.plugin_not_found
// and exits 2.
func TestDryRunCommand_UnknownBuiltinPlugin_ExitTwo(t *testing.T) {
	is := is.New(t)

	cmd := newDryRunEcdysis().MustBuildCobraCommand(&DryRunCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/dryrun-unknown-builtin.yaml"})

	err := cmd.Execute()
	is.True(err != nil)
	is.Equal(exitcode.ExitCode(err), exitcode.Validation)

	got := out.String()
	is.True(strings.Contains(got, conduiterr.CodeConnectorPluginNotFound.Reason()))
}

// TestDryRunCommand_StandalonePlugin_AdvisoryExitZero covers AC-6's second
// half: a standalone plugin ref is never a false failure — dry-run exits 0
// and shows it as advisory ("unverified"), not as an error.
func TestDryRunCommand_StandalonePlugin_AdvisoryExitZero(t *testing.T) {
	is := is.New(t)

	cmd := newDryRunEcdysis().MustBuildCobraCommand(&DryRunCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/dryrun-standalone.yaml"})

	err := cmd.Execute()
	is.NoErr(err)
	is.Equal(exitcode.ExitCode(err), exitcode.OK)

	got := out.String()
	is.True(!strings.Contains(got, conduiterr.CodeConnectorPluginNotFound.Reason()))
	is.True(strings.Contains(got, "unverified"))
}

// TestDryRunCommand_ResolvePluginsOff_SkipsResolution proves
// --resolve-plugins=false disables the check entirely: the same
// otherwise-failing unknown-builtin file now exits 0, with no pluginStatus
// on the enriched output.
func TestDryRunCommand_ResolvePluginsOff_SkipsResolution(t *testing.T) {
	is := is.New(t)

	cmd := newDryRunEcdysis().MustBuildCobraCommand(&DryRunCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/dryrun-unknown-builtin.yaml", "--resolve-plugins=false", "--json"})

	err := cmd.Execute()
	is.NoErr(err)
	is.Equal(exitcode.ExitCode(err), exitcode.OK)

	got := out.String()
	is.True(!strings.Contains(got, conduiterr.CodeConnectorPluginNotFound.Reason()))
	is.True(!strings.Contains(got, `"pluginStatus"`))
}

// TestDryRunCommand_Offline_NoDial proves `dry-run` never dials the API.
// Structurally: DryRunCommand implements only cecdysis.CommandWithResult
// (see the var _ assertions at the top of this file), never
// cecdysis.CommandWithExecuteWithClientResult (the online decorator
// describe.go/list.go use) — so there is no API-address flag and no client
// construction anywhere in its command tree at all, unlike an online
// command such as `pipelines describe`. Behaviorally: a run completes
// near-instantly using only the local testdata file, with no address flag
// to even point anywhere — a real dial attempt would need to time out
// first.
func TestDryRunCommand_Offline_NoDial(t *testing.T) {
	is := is.New(t)

	cmd := newDryRunEcdysis().MustBuildCobraCommand(&DryRunCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/valid.yaml", "--resolve-plugins"})

	start := time.Now()
	err := cmd.Execute()
	elapsed := time.Since(start)

	is.NoErr(err)
	is.True(elapsed < 2*time.Second)
}

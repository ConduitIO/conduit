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
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/ecdysis"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
)

func newLintEcdysis() *ecdysis.Ecdysis {
	return ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithResultDecorator{}))
}

func TestLintCommand_Args_NoPath(t *testing.T) {
	is := is.New(t)
	c := &LintCommand{}
	err := c.Args([]string{})
	is.True(err != nil)
}

func TestLintCommand_Args_TooMany(t *testing.T) {
	is := is.New(t)
	c := &LintCommand{}
	err := c.Args([]string{"a", "b"})
	is.True(err != nil)
}

// TestLintCommand_DeprecatedField_WarningExitZero covers AC-1: a file with a
// deprecated field produces exactly one warning finding with line/column
// (severity:"warning" in --json), and exits 0.
func TestLintCommand_DeprecatedField_WarningExitZero(t *testing.T) {
	is := is.New(t)

	cmd := newLintEcdysis().MustBuildCobraCommand(&LintCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/lint-warning.yaml", "--json"})

	err := cmd.Execute()
	is.NoErr(err)
	is.Equal(exitcode.ExitCode(err), exitcode.OK)

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.Equal(got.Command, "pipelines.lint")
	is.True(got.OK)
	is.True(got.Error == nil)

	resultBytes, merr := json.Marshal(got.Result)
	is.NoErr(merr)
	var result struct {
		Files []struct {
			Findings []struct {
				Severity string `json:"severity"`
				Code     string `json:"code"`
				Line     int    `json:"line"`
				Column   int    `json:"column"`
			} `json:"findings"`
		} `json:"files"`
	}
	is.NoErr(json.Unmarshal(resultBytes, &result))
	is.Equal(len(result.Files), 1)
	is.Equal(len(result.Files[0].Findings), 1)
	f := result.Files[0].Findings[0]
	is.Equal(f.Severity, "warning")
	is.Equal(f.Code, config.CodeParserWarning.Reason())
	is.True(f.Line > 0)
	is.True(f.Column > 0)
}

// TestLintCommand_Strict_ExitsTwo covers AC-2: the same warning-only file,
// with --strict, exits 2 (the warning is promoted to a failure) even though
// the rendered envelope's `ok`/`error` fields still follow the "findings,
// not a hard failure" convention.
func TestLintCommand_Strict_ExitsTwo(t *testing.T) {
	is := is.New(t)

	cmd := newLintEcdysis().MustBuildCobraCommand(&LintCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/lint-warning.yaml", "--strict", "--json"})

	err := cmd.Execute()
	is.True(err != nil)
	is.Equal(exitcode.ExitCode(err), exitcode.Validation)

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.True(!got.OK) // strict promotes the warning-only run to not-OK
	is.True(got.Error == nil)
}

// TestLintCommand_WithoutStrict_ExitsZero is the control for
// TestLintCommand_Strict_ExitsTwo: the identical file/flags minus --strict
// exits 0, proving --strict (not some other difference) is what changes the
// outcome.
func TestLintCommand_WithoutStrict_ExitsZero(t *testing.T) {
	is := is.New(t)

	cmd := newLintEcdysis().MustBuildCobraCommand(&LintCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/lint-warning.yaml"})

	err := cmd.Execute()
	is.NoErr(err)
	is.Equal(exitcode.ExitCode(err), exitcode.OK)
}

// TestLintCommand_ErrorAndWarning_BothRendered_ExitTwo covers AC-3: a file
// with both an error and a warning finding renders both (never fail-fast)
// and exits 2 — the error dominates regardless of --strict.
func TestLintCommand_ErrorAndWarning_BothRendered_ExitTwo(t *testing.T) {
	is := is.New(t)

	cmd := newLintEcdysis().MustBuildCobraCommand(&LintCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/lint-error-and-warning.yaml"})

	err := cmd.Execute()
	is.True(err != nil)
	is.Equal(exitcode.ExitCode(err), exitcode.Validation)

	got := out.String()
	is.True(strings.Contains(got, config.CodeParserWarning.Reason())) // the warning
	is.True(strings.Contains(got, config.CodeFieldInvalid.Reason()))  // the error
	is.True(strings.Contains(got, "Summary:"))
}

// TestLintCommand_Offline_NoDial proves `lint` never dials the API.
// Structurally: LintCommand implements only cecdysis.CommandWithResult (see
// the var _ assertions at the top of this file), never
// cecdysis.CommandWithExecuteWithClientResult (the online decorator
// describe.go/list.go use) — so there is no API-address flag and no client
// construction anywhere in its command tree at all. Behaviorally: a run
// completes near-instantly using only the local testdata file.
func TestLintCommand_Offline_NoDial(t *testing.T) {
	is := is.New(t)

	cmd := newLintEcdysis().MustBuildCobraCommand(&LintCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/valid.yaml"})

	start := time.Now()
	err := cmd.Execute()
	elapsed := time.Since(start)

	is.NoErr(err)
	is.True(elapsed < 2*time.Second)
}

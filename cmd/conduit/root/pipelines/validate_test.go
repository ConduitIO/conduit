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

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/testutils"
	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/ecdysis"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
)

func newValidateEcdysis() *ecdysis.Ecdysis {
	return ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithResultDecorator{}))
}

func TestValidateCommand_Args_NoPath(t *testing.T) {
	is := is.New(t)
	c := &ValidateCommand{}
	err := c.Args([]string{})
	is.True(err != nil)
}

func TestValidateCommand_Args_TooMany(t *testing.T) {
	is := is.New(t)
	c := &ValidateCommand{}
	err := c.Args([]string{"a", "b"})
	is.True(err != nil)
}

// TestValidateCommand_ValidFile_JSON covers AC-1 and AC-6: a valid file
// exits 0 with ok:true and no findings, and the --json output round-trips
// as a single well-formed envelope object.
func TestValidateCommand_ValidFile_JSON(t *testing.T) {
	is := is.New(t)

	cmd := newValidateEcdysis().MustBuildCobraCommand(&ValidateCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/valid.yaml", "--json"})

	err := cmd.Execute()
	is.NoErr(err)
	is.Equal(exitcode.ExitCode(err), exitcode.OK)

	// Family A golden fixture (v0.19 workstream 8 — cli-contract.md §6 AC-3):
	// validate's real --json output must validate against the shared
	// envelope schema; see cmd/conduit/cli/schema_golden_test.go.
	is.NoErr(testutils.ValidateEnvelope(out.Bytes()))

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.Equal(got.Command, "pipelines.validate")
	is.True(got.OK)
	is.True(got.Error == nil)
}

// TestValidateCommand_FileWithErrors_JSON covers AC-2 and AC-6: every
// finding is present in the --json result, with a registered code, and the
// process exit code classifies as Validation (2) via
// pkg/conduit/exitcode — even though ExecuteWithResult itself returns a nil
// error (see cecdysis.Outcome.ExitErr's doc for why that still yields a
// nonzero process exit code).
func TestValidateCommand_FileWithErrors_JSON(t *testing.T) {
	is := is.New(t)

	cmd := newValidateEcdysis().MustBuildCobraCommand(&ValidateCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/errors.yaml", "--json"})

	err := cmd.Execute()
	is.True(err != nil) // RunE returns outcome.ExitErr for classification
	is.Equal(exitcode.ExitCode(err), exitcode.Validation)

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.Equal(got.Command, "pipelines.validate")
	is.True(!got.OK)
	is.True(got.Error == nil) // findings, not a hard command failure — CLI output conventions §1

	resultBytes, merr := json.Marshal(got.Result)
	is.NoErr(merr)
	var result struct {
		Files []struct {
			Path      string `json:"path"`
			OK        bool   `json:"ok"`
			Pipelines []string
			Findings  []struct {
				Severity   string `json:"severity"`
				Code       string `json:"code"`
				Message    string `json:"message"`
				ConfigPath string `json:"configPath"`
				Suggestion string `json:"suggestion"`
			} `json:"findings"`
		} `json:"files"`
	}
	is.NoErr(json.Unmarshal(resultBytes, &result))
	is.Equal(len(result.Files), 1)
	is.Equal(len(result.Files[0].Findings), 5)
	for _, f := range result.Files[0].Findings {
		is.True(f.Code != "")
		is.True(f.ConfigPath != "")
		is.True(f.Message != "")
		is.True(f.Suggestion != "")
		// code == registered reason (AC-6)
		_, ok := conduiterr.LookupCode(f.Code)
		is.True(ok)
	}
}

// TestValidateCommand_FileWithErrors_Human covers the human-rendering path:
// every finding renders via ui.RenderFinding (glyph + code + configPath,
// message, suggestion), and no bare "(exit 2)" appears in the output per
// CLI output conventions §2.
func TestValidateCommand_FileWithErrors_Human(t *testing.T) {
	is := is.New(t)

	cmd := newValidateEcdysis().MustBuildCobraCommand(&ValidateCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/errors.yaml"})

	err := cmd.Execute()
	is.True(err != nil)
	is.Equal(exitcode.ExitCode(err), exitcode.Validation)

	got := out.String()
	is.True(strings.Contains(got, config.CodeFieldInvalid.Reason()))
	is.True(strings.Contains(got, "/status"))
	is.True(!strings.Contains(got, "(exit 2)")) // never print the exit code in human output
	is.True(strings.Contains(got, "Summary:"))
}

// TestValidateCommand_Directory_ShowsPassingFiles covers the directory
// aggregation + "passing files shown as ✓ <file>" requirement.
func TestValidateCommand_Directory_ShowsPassingFiles(t *testing.T) {
	is := is.New(t)

	cmd := newValidateEcdysis().MustBuildCobraCommand(&ValidateCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/dir"})

	err := cmd.Execute()
	is.True(err != nil) // fail1.yaml has findings
	is.Equal(exitcode.ExitCode(err), exitcode.Validation)

	got := out.String()
	is.True(strings.Contains(got, "pass1.yaml"))
	is.True(strings.Contains(got, "pass2.yaml"))
	is.True(strings.Contains(got, "fail1.yaml"))
	is.True(strings.Contains(got, "Summary: 3 files"))
}

// TestValidateCommand_Quiet_SuppressesPassingLines covers the -q/--quiet
// flag: passing-file lines are suppressed, failures and the summary remain.
func TestValidateCommand_Quiet_SuppressesPassingLines(t *testing.T) {
	is := is.New(t)

	cmd := newValidateEcdysis().MustBuildCobraCommand(&ValidateCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/dir", "--quiet"})

	err := cmd.Execute()
	is.True(err != nil)

	got := out.String()
	is.True(!strings.Contains(got, "pass1.yaml"))
	is.True(!strings.Contains(got, "pass2.yaml"))
	is.True(strings.Contains(got, "fail1.yaml")) // failures still shown
	is.True(strings.Contains(got, "Summary:"))
}

// TestValidateCommand_CrossFileDuplicateID_JSON covers the cross-file
// duplicate pipeline ID acceptance case end to end through the command.
func TestValidateCommand_CrossFileDuplicateID_JSON(t *testing.T) {
	is := is.New(t)

	cmd := newValidateEcdysis().MustBuildCobraCommand(&ValidateCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/dup", "--json"})

	err := cmd.Execute()
	is.True(err != nil)
	is.Equal(exitcode.ExitCode(err), exitcode.Validation)

	is.True(strings.Contains(out.String(), `"provisioning.pipeline_id_duplicate"`))
}

// TestValidateCommand_NonexistentPath_HardFailure covers a HARD command
// failure (the given path doesn't resolve to anything) rendering through
// the envelope's error field, still classified as Validation (2) — bad
// input on the caller's side, not an environment problem.
func TestValidateCommand_NonexistentPath_HardFailure(t *testing.T) {
	is := is.New(t)

	cmd := newValidateEcdysis().MustBuildCobraCommand(&ValidateCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"testdata/does-not-exist.yaml", "--json"})

	err := cmd.Execute()
	is.True(err != nil)
	is.Equal(exitcode.ExitCode(err), exitcode.Validation)

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.True(!got.OK)
	is.True(got.Error != nil)
}

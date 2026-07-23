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

// These are full-stack tests for `conduit pipelines init`, built the same
// way cmd/conduit/root/pipelines/validate_test.go and
// cmd/conduit/root/doctor/doctor_test.go build theirs: a real cobra command
// wired through the same cecdysis.CommandWithResultDecorator cmd/conduit/cli
// uses, driven through cmd.Execute()/ExecuteC(), asserting on both rendered
// output and the process-level exit code classification.
//
// TestInitCommand_ExistingFile_RefusesWithoutForce is the regression test for
// the bug this command shipped with: writeFile used to open with
// os.O_CREATE|os.O_WRONLY|os.O_TRUNC and no existence check at all, silently
// clobbering a second `pipelines init` into the same path. It fails without
// the fix (the second init would succeed and overwrite silently) and passes
// with it (coded refusal, original content intact).
package pipelines

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/ecdysis"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
)

func newInitEcdysis() *ecdysis.Ecdysis {
	return ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithResultDecorator{}))
}

// TestInitCommand_ExistingFile_RefusesWithoutForce is the regression test:
// a second `pipelines init` into an already-populated --pipelines.path must
// refuse with a coded error, exit Validation (2), and must not touch the
// existing file's content.
func TestInitCommand_ExistingFile_RefusesWithoutForce(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	// First init: succeeds, creates demo-pipeline.yaml.
	cmd := newInitEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + dir})
	is.NoErr(cmd.Execute())

	path := filepath.Join(dir, "demo-pipeline.yaml")
	original, err := os.ReadFile(path)
	is.NoErr(err)
	is.True(len(original) > 0)

	// Second init into the same path, no --force: must refuse, not
	// overwrite.
	cmd2 := newInitEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out2 bytes.Buffer
	cmd2.SetOut(&out2)
	cmd2.SetErr(&out2)
	cmd2.SetArgs([]string{"--pipelines.path=" + dir, "--json"})

	err2 := cmd2.Execute()
	is.True(err2 != nil)
	is.Equal(exitcode.ExitCode(err2), exitcode.Validation)

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out2.Bytes(), &got))
	is.Equal(got.Command, "pipelines.init")
	is.True(!got.OK)
	is.True(got.Error != nil)
	is.Equal(got.Error.Code, CodeDestinationExists.Reason())
	is.True(strings.Contains(got.Error.Suggestion, "--force"))
	is.Equal(got.Error.ConfigPath, path)

	// The file on disk must be untouched — this is the actual bug: prove
	// content identity, not just that an error was returned.
	after, err := os.ReadFile(path)
	is.NoErr(err)
	is.Equal(string(original), string(after))
}

// TestInitCommand_ExistingFile_ForceOverwrites covers the opt-in overwrite
// path: --force must succeed and actually replace the file's content.
func TestInitCommand_ExistingFile_ForceOverwrites(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cmd := newInitEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + dir, "--source=generator", "--destination=log"})
	is.NoErr(cmd.Execute())

	path := filepath.Join(dir, "generator-to-log.yaml")
	is.NoErr(os.WriteFile(path, []byte("hand-edited: true\n"), 0o600))

	cmd2 := newInitEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out2 bytes.Buffer
	cmd2.SetOut(&out2)
	cmd2.SetArgs([]string{"--pipelines.path=" + dir, "--source=generator", "--destination=log", "--force", "--json"})
	is.NoErr(cmd2.Execute())

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out2.Bytes(), &got))
	is.Equal(got.Command, "pipelines.init")
	is.True(got.OK)
	is.True(got.Error == nil)

	after, err := os.ReadFile(path)
	is.NoErr(err)
	is.True(!strings.Contains(string(after), "hand-edited"))
	is.True(strings.Contains(string(after), "generator-source"))
}

// TestInitCommand_DryRun_WritesNothing covers --dry-run: the command must
// print the rendered config but must not create any file.
func TestInitCommand_DryRun_WritesNothing(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cmd := newInitEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + dir, "--dry-run"})
	is.NoErr(cmd.Execute())

	path := filepath.Join(dir, "demo-pipeline.yaml")
	_, statErr := os.Stat(path)
	is.True(os.IsNotExist(statErr)) // nothing written

	got := out.String()
	is.True(strings.Contains(got, "Dry run"))
	is.True(strings.Contains(got, "generator-source")) // the rendered config content
	is.True(!strings.Contains(got, "has been initialized"))
}

// TestInitCommand_DryRun_JSON covers --dry-run combined with --json: the
// envelope must report written:false in the summary and carry the rendered
// config in the result, with nothing written to disk.
func TestInitCommand_DryRun_JSON(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cmd := newInitEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + dir, "--dry-run", "--json"})
	is.NoErr(cmd.Execute())

	path := filepath.Join(dir, "demo-pipeline.yaml")
	_, statErr := os.Stat(path)
	is.True(os.IsNotExist(statErr))

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.Equal(got.Command, "pipelines.init")
	is.True(got.OK)
	is.True(got.Error == nil)

	summaryBytes, err := json.Marshal(got.Summary)
	is.NoErr(err)
	var summary InitSummary
	is.NoErr(json.Unmarshal(summaryBytes, &summary))
	is.True(!summary.Written)

	resultBytes, err := json.Marshal(got.Result)
	is.NoErr(err)
	var result InitResult
	is.NoErr(json.Unmarshal(resultBytes, &result))
	is.True(result.DryRun)
	is.Equal(result.Path, path)
	is.True(strings.Contains(result.Config, "generator-source"))
}

// TestInitCommand_ValidJSON_EnvelopeShape covers the Family A envelope
// conformance requirement (CLI output conventions §1 / Workstream 8): a
// successful --json run has command/ok/error present and error null.
func TestInitCommand_ValidJSON_EnvelopeShape(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cmd := newInitEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + dir, "--json"})
	is.NoErr(cmd.Execute())
	is.Equal(exitcode.ExitCode(nil), exitcode.OK)

	var raw map[string]any
	is.NoErr(json.Unmarshal(out.Bytes(), &raw))
	for _, key := range []string{"command", "ok", "summary", "result", "error"} {
		_, ok := raw[key]
		is.True(ok)
	}
	is.Equal(raw["command"], "pipelines.init")
	is.Equal(raw["ok"], true)
	is.True(raw["error"] == nil)
}

// TestInitCommand_UnknownConnector_HardFailure covers a HARD command failure
// (unresolvable connector) rendering through the envelope's error field with
// a registered code, exit Validation (2).
func TestInitCommand_UnknownConnector_HardFailure(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cmd := newInitEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + dir, "--source=does-not-exist", "--json"})

	err := cmd.Execute()
	is.True(err != nil)
	is.Equal(exitcode.ExitCode(err), exitcode.Validation)

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.True(!got.OK)
	is.True(got.Error != nil)
}

// TestCodeDestinationExists_Registered proves CodeDestinationExists is a
// real, registered conduiterr code (agents/docs can look it up), not just a
// local sentinel.
func TestCodeDestinationExists_Registered(t *testing.T) {
	is := is.New(t)
	_, ok := conduiterr.LookupCode(CodeDestinationExists.Reason())
	is.True(ok)
}

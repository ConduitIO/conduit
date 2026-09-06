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

// TestInitCommand_MissingPipelinesDir_CreatesIt is the regression test for
// the bug this change fixes: `conduit pipelines init` in a directory that is
// not already a Conduit workspace failed because nothing created the
// pipelines/ directory it was about to write into. It must now create the
// directory, write the pipeline, and report the creation.
//
// Without the fix this test fails at the first is.NoErr(cmd.Execute()) with
// a `could not open ".../pipelines/demo-pipeline.yaml"` error under the
// generic internal.error code.
func TestInitCommand_MissingPipelinesDir_CreatesIt(t *testing.T) {
	is := is.New(t)
	// t.TempDir() exists; the pipelines dir *under* it deliberately does
	// not — that is exactly the shape of a fresh, non-workspace directory.
	pipelinesDir := filepath.Join(t.TempDir(), "pipelines")
	_, statErr := os.Stat(pipelinesDir)
	is.True(os.IsNotExist(statErr)) // precondition: it really is missing

	cmd := newInitEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + pipelinesDir, "--json"})
	is.NoErr(cmd.Execute())

	path := filepath.Join(pipelinesDir, "demo-pipeline.yaml")
	written, err := os.ReadFile(path)
	is.NoErr(err)
	is.True(strings.Contains(string(written), "generator-source"))

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.Equal(got.Command, "pipelines.init")
	is.True(got.OK)
	is.True(got.Error == nil)

	resultBytes, err := json.Marshal(got.Result)
	is.NoErr(err)
	var result InitResult
	is.NoErr(json.Unmarshal(resultBytes, &result))
	is.Equal(result.Path, path)
	is.Equal(result.CreatedDir, pipelinesDir)
}

// TestInitCommand_DefaultPath_FreshDirectory is the end-to-end shape of the
// reported defect: the very first example in `pipelines init --help` — the
// command with no arguments at all — run from a directory that has never
// seen `conduit init`. It must succeed against the default ./pipelines path.
func TestInitCommand_DefaultPath_FreshDirectory(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	// The --pipelines.path default is resolved from the working directory
	// in InitCommand.Flags(), so the chdir has to happen before the cobra
	// command is built.
	t.Chdir(dir)

	cmd := newInitEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(nil)
	is.NoErr(cmd.Execute())

	written, err := os.ReadFile(filepath.Join(dir, "pipelines", "demo-pipeline.yaml"))
	is.NoErr(err)
	is.True(strings.Contains(string(written), "generator-source"))

	rendered := out.String()
	is.True(strings.Contains(rendered, "Created directory:"))
	is.True(strings.Contains(rendered, "has been initialized"))
}

// TestInitCommand_DryRun_DoesNotCreateDir guards the boundary the fix must
// not cross: --dry-run touches the filesystem not at all, so it must not
// create the destination directory either.
func TestInitCommand_DryRun_DoesNotCreateDir(t *testing.T) {
	is := is.New(t)
	pipelinesDir := filepath.Join(t.TempDir(), "pipelines")

	cmd := newInitEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + pipelinesDir, "--dry-run", "--json"})
	is.NoErr(cmd.Execute())

	_, statErr := os.Stat(pipelinesDir)
	is.True(os.IsNotExist(statErr)) // still missing

	resultBytes, err := json.Marshal(mustResult(is, out.Bytes()).Result)
	is.NoErr(err)
	var result InitResult
	is.NoErr(json.Unmarshal(resultBytes, &result))
	is.Equal(result.CreatedDir, "")
}

// TestInitCommand_MissingDirThenExisting_StillRefusesWithoutForce proves
// creating the directory did not weaken the overwrite protection: the first
// run creates pipelines/ and the file, the second must still refuse without
// --force (and must report createdDir empty, since it created nothing).
func TestInitCommand_MissingDirThenExisting_StillRefusesWithoutForce(t *testing.T) {
	is := is.New(t)
	pipelinesDir := filepath.Join(t.TempDir(), "pipelines")

	cmd := newInitEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + pipelinesDir})
	is.NoErr(cmd.Execute())

	path := filepath.Join(pipelinesDir, "demo-pipeline.yaml")
	original, err := os.ReadFile(path)
	is.NoErr(err)

	cmd2 := newInitEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out2 bytes.Buffer
	cmd2.SetOut(&out2)
	cmd2.SetErr(&out2)
	cmd2.SetArgs([]string{"--pipelines.path=" + pipelinesDir, "--json"})

	err2 := cmd2.Execute()
	is.True(err2 != nil)
	is.Equal(exitcode.ExitCode(err2), exitcode.Validation)

	got := mustResult(is, out2.Bytes())
	is.True(got.Error != nil)
	is.Equal(got.Error.Code, CodeDestinationExists.Reason())

	after, err := os.ReadFile(path)
	is.NoErr(err)
	is.Equal(string(original), string(after))
}

// TestInitCommand_FileWherePipelinesDirShouldBe covers the genuinely
// unwritable destination: a regular file occupies the path the pipelines
// directory would need. That must fail with the non-generic
// pipelines.init_path_unwritable code (never internal.error), exit
// Validation (2), and surface both the underlying OS error and a remedy.
func TestInitCommand_FileWherePipelinesDirShouldBe(t *testing.T) {
	is := is.New(t)
	blocked := filepath.Join(t.TempDir(), "pipelines")
	is.NoErr(os.WriteFile(blocked, []byte("not a directory\n"), 0o600))

	cmd := newInitEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + blocked, "--json"})

	err := cmd.Execute()
	is.True(err != nil)
	is.Equal(exitcode.ExitCode(err), exitcode.Validation)

	got := mustResult(is, out.Bytes())
	is.True(!got.OK)
	is.True(got.Error != nil)
	is.Equal(got.Error.Code, CodePipelinesPathUnwritable.Reason())
	is.True(got.Error.Code != conduiterr.CodeInternal.Reason())
	// The message names the underlying OS error, not a bare "could not open".
	is.True(strings.Contains(got.Error.Message, blocked))
	is.True(strings.Contains(got.Error.Message, "not a directory"))
	// And the suggestion points at the documented workspace setup command.
	is.True(strings.Contains(got.Error.Suggestion, "conduit init"))
}

// TestInitCommand_UnwritableParent covers the permissions half of the same
// failure: the parent directory exists but cannot be written to, so
// MkdirAll fails with EACCES. Skipped when running as root, for whom the
// permission bits do not apply.
func TestInitCommand_UnwritableParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions are not enforced")
	}
	is := is.New(t)

	parent := filepath.Join(t.TempDir(), "readonly")
	is.NoErr(os.Mkdir(parent, 0o755))
	is.NoErr(os.Chmod(parent, 0o555))
	// Restore write permission so t.TempDir's cleanup can remove it.
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	cmd := newInitEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + filepath.Join(parent, "pipelines"), "--json"})

	err := cmd.Execute()
	is.True(err != nil)
	is.Equal(exitcode.ExitCode(err), exitcode.Validation)

	got := mustResult(is, out.Bytes())
	is.True(got.Error != nil)
	is.Equal(got.Error.Code, CodePipelinesPathUnwritable.Reason())
	is.True(strings.Contains(got.Error.Message, "permission denied"))
}

// TestInitCommand_UnwritableExistingDir covers the other new error site: the
// destination directory already exists (so MkdirAll is a no-op) but the file
// open fails. That must be coded the same way, not as internal.error.
func TestInitCommand_UnwritableExistingDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions are not enforced")
	}
	is := is.New(t)

	dir := filepath.Join(t.TempDir(), "pipelines")
	is.NoErr(os.Mkdir(dir, 0o755))
	is.NoErr(os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	cmd := newInitEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + dir, "--json"})

	err := cmd.Execute()
	is.True(err != nil)
	is.Equal(exitcode.ExitCode(err), exitcode.Validation)

	got := mustResult(is, out.Bytes())
	is.True(got.Error != nil)
	is.Equal(got.Error.Code, CodePipelinesPathUnwritable.Reason())
	is.True(strings.Contains(got.Error.Message, "permission denied"))
	is.True(strings.Contains(got.Error.Suggestion, "--pipelines.path"))
}

// TestCodePipelinesPathUnwritable_Registered proves the new code is a real,
// registered conduiterr code (docs, llms.txt and agents can look it up), and
// that it classifies to the Validation bucket rather than the generic
// internal/runtime one.
func TestCodePipelinesPathUnwritable_Registered(t *testing.T) {
	is := is.New(t)
	code, ok := conduiterr.LookupCode(CodePipelinesPathUnwritable.Reason())
	is.True(ok)
	is.Equal(code.Reason(), "pipelines.init_path_unwritable")
	is.Equal(exitcode.ExitCode(conduiterr.New(CodePipelinesPathUnwritable, "x")), exitcode.Validation)
}

// mustResult decodes a --json envelope, failing the test if it is not valid
// JSON — the envelope must stay well-formed on the error paths too.
func mustResult(is *is.I, b []byte) cecdysis.Result {
	is.Helper()
	var got cecdysis.Result
	is.NoErr(json.Unmarshal(b, &got))
	return got
}

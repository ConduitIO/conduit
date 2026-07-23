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

package scaffoldcmd

import (
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/testutils"
	"github.com/conduitio/conduit/pkg/scaffold"
	"github.com/conduitio/ecdysis"
	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCommand_Args(t *testing.T) {
	withName := &NewCommand{Kind: scaffold.KindConnector}
	require.NoError(t, withName.Args([]string{"s3"}))
	assert.Equal(t, "s3", withName.name)

	noArgs := &NewCommand{Kind: scaffold.KindConnector}
	require.NoError(t, noArgs.Args(nil))
	assert.Empty(t, noArgs.name)

	tooMany := &NewCommand{Kind: scaffold.KindConnector}
	require.Error(t, tooMany.Args([]string{"a", "b"}))
}

func TestNewCommand_Flags_Defaults(t *testing.T) {
	c := &NewCommand{Kind: scaffold.KindProcessor}
	flags := c.Flags()

	f, ok := findFlag(flags, "git")
	require.True(t, ok)
	assert.Equal(t, true, f.Default)

	f, ok = findFlag(flags, "lang")
	require.True(t, ok)
	assert.Equal(t, "go", f.Default)

	f, ok = findFlag(flags, "module")
	require.True(t, ok)
	assert.Contains(t, f.Usage, "conduit-processor-<name>")
}

func findFlag(flags []ecdysis.Flag, long string) (ecdysis.Flag, bool) {
	for _, f := range flags {
		if f.Long == long {
			return f, true
		}
	}
	return ecdysis.Flag{}, false
}

func TestNewCommand_ResultCommand(t *testing.T) {
	assert.Equal(t, "connector.new", (&NewCommand{Kind: scaffold.KindConnector}).ResultCommand())
	assert.Equal(t, "processor.new", (&NewCommand{Kind: scaffold.KindProcessor}).ResultCommand())
}

func TestNewCommand_ExecuteWithResult_ValidationError(t *testing.T) {
	c := &NewCommand{Kind: scaffold.KindConnector}
	c.flags.Module = "bogus"
	c.name = "s3"

	_, err := c.ExecuteWithResult(context.Background())
	require.Error(t, err)
}

func TestNewCommand_Render(t *testing.T) {
	c := &NewCommand{Kind: scaffold.KindConnector}
	res := scaffold.Result{
		Kind:   scaffold.KindConnector,
		Module: "github.com/devaris/conduit-connector-s3",
		Path:   "./conduit-connector-s3",
		Steps: []scaffold.StepResult{
			{Name: scaffold.StepToolchain, OK: true, DurationMS: 14},
			{Name: scaffold.StepRewriteModule, OK: true, DurationMS: 2},
			{Name: scaffold.StepBuild, OK: true, DurationMS: 1200},
			{Name: scaffold.StepGitInit, OK: false, DurationMS: 5, Message: "git commit: exit status 1"},
		},
		ElapsedMS: 3600,
		NextSteps: []string{"cd conduit-connector-s3", "make test"},
	}

	out := c.Render(cecdysis.Outcome{Result: res})
	assert.True(t, strings.Contains(out, "Rewrote module path → github.com/devaris/conduit-connector-s3"))
	assert.True(t, strings.Contains(out, "go build OK (1.2s)"))
	assert.True(t, strings.Contains(out, "Your connector is ready at ./conduit-connector-s3  (3.6s)"))
	assert.True(t, strings.Contains(out, "Next steps:"))
	assert.True(t, strings.Contains(out, "cd conduit-connector-s3"))
}

func TestFormatDuration(t *testing.T) {
	assert.Equal(t, "594ms", formatDuration(594))
	assert.Equal(t, "1.2s", formatDuration(1200))
	assert.Equal(t, "5.6s", formatDuration(5560))
}

func TestExample(t *testing.T) {
	ex := Example(scaffold.KindConnector, "s3")
	assert.Contains(t, ex, "conduit connector new s3")
	assert.Contains(t, ex, "conduit-connector-s3")
}

// TestResultMarshalsToTheDocumentedEnvelope guards the --json shape's
// specific field spellings (templateRef, sdkVersion, durationMs, elapsedMs,
// nextSteps) — a typo in scaffold.Result's json tags wouldn't be caught by
// any type-level compile check, only by asserting the actual marshaled keys.
func TestResultMarshalsToTheDocumentedEnvelope(t *testing.T) {
	res := scaffold.Result{
		Kind:        scaffold.KindConnector,
		TemplateRef: "abc123",
		SDKVersion:  "v0.14.1",
		Steps:       []scaffold.StepResult{{Name: scaffold.StepBuild, OK: true, DurationMS: 5}},
		ElapsedMS:   10,
		NextSteps:   []string{"cd x"},
	}
	b, err := json.Marshal(res)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	for _, key := range []string{"templateRef", "sdkVersion", "elapsedMs", "nextSteps", "steps"} {
		assert.Contains(t, m, key)
	}

	steps, ok := m["steps"].([]any)
	require.True(t, ok)
	require.Len(t, steps, 1)
	step, ok := steps[0].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, step, "durationMs")
}

// TestResultMarshalsToTheDocumentedEnvelope_FamilyA is the Family A golden
// fixture for both `conduit connector new` and `conduit processor new`
// (v0.19 workstream 8 — cli-contract.md §6 AC-3): both concrete commands
// (cmd/conduit/root/connectors.NewCommand, cmd/conduit/root/processors.NewCommand)
// delegate their ExecuteWithResult/Render entirely to this shared
// scaffoldcmd.NewCommand, so proving this type's envelope conformance once,
// here, covers both leaves the completeness walk
// (cmd/conduit/cli/schema_golden_test.go) classifies as Family A. A real
// end-to-end run needs network access and a Go toolchain (template clone +
// go build) — out of scope for a unit test, per this file's own
// TestNewCommand_Render precedent (a synthetic scaffold.Result, not a real
// scaffold.Generate call).
func TestResultMarshalsToTheDocumentedEnvelope_FamilyA(t *testing.T) {
	res := scaffold.Result{
		Kind:        scaffold.KindConnector,
		Module:      "github.com/devaris/conduit-connector-s3",
		Path:        "./conduit-connector-s3",
		TemplateRef: "abc123",
		SDKVersion:  "v0.14.1",
		Steps:       []scaffold.StepResult{{Name: scaffold.StepBuild, OK: true, DurationMS: 5}},
		ElapsedMS:   10,
		NextSteps:   []string{"cd conduit-connector-s3"},
	}
	envelope := cecdysis.Result{
		Command: "connector.new",
		OK:      true,
		Summary: map[string]any{"stepsTotal": 1, "stepsFailed": 0},
		Result:  res,
	}
	b, err := json.Marshal(envelope)
	require.NoError(t, err)
	assert.NoError(t, testutils.ValidateEnvelope(b))
}

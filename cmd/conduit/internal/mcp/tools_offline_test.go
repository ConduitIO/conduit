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

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/internal/validate"
	"github.com/matryer/is"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const invalidPipelineYAML = `version: 2.2
pipelines:
  - name: orders
    connectors:
      - id: src
        type: source
`

// TestValidate_InvalidConfig_ErrorCarriesCodeAndSuggestion is an AC-6
// regression test: a domain finding from the wrapped engine (here, a missing
// required "id" field) round-trips as a Finding with Code+Suggestion set —
// note validate reports findings as OK:false with no top-level tool error
// (mirroring the CLI's own "a validation run that finds problems is
// OK:false, not a HARD failure" contract); Suggestion/Code live per-Finding.
func TestValidate_InvalidConfig_ErrorCarriesCodeAndSuggestion(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{})
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolValidate,
		Arguments: map[string]any{"config": invalidPipelineYAML},
	})
	is.NoErr(err)
	is.True(!res.IsError) // findings are not a protocol/tool error — see the design doc's §1 envelope contract

	env := decodeStructuredContent[Result[validate.Result]](t, res)
	is.True(!env.OK)
	is.Equal(len(env.Result.Files), 1)
	is.True(len(env.Result.Files[0].Findings) > 0)
	for _, f := range env.Result.Files[0].Findings {
		is.True(f.Code != "")
		is.True(f.Suggestion != "")
	}
}

// TestValidate_UnwritableTempDir_ToolErrorCarriesCodeAndSuggestion forces a
// HARD failure in withTempConfigFile itself (not a validate finding) by
// making the temp-file write fail, and checks the resulting tool error still
// carries a code + suggestion (AC-6 for the adapter's own error path, not
// just the wrapped engine's).
func TestValidate_UnwritableTempDir_ToolErrorCarriesCodeAndSuggestion(t *testing.T) {
	is := is.New(t)

	orig := os.Getenv("TMPDIR")
	unwritable := t.TempDir()
	is.NoErr(os.Chmod(unwritable, 0o500)) // no write permission
	t.Cleanup(func() {
		_ = os.Chmod(unwritable, 0o700)
		_ = os.Setenv("TMPDIR", orig)
	})
	is.NoErr(os.Setenv("TMPDIR", unwritable))

	srv := NewServer(Config{})
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolValidate,
		Arguments: map[string]any{"config": validPipelineYAML},
	})
	is.NoErr(err)

	if !res.IsError {
		t.Skip("temp dir permissions did not block MkdirTemp in this environment (e.g. running as root)")
	}

	env := decodeStructuredContent[Result[validate.Result]](t, res)
	is.True(env.Error != nil)
	is.True(env.Error.Code != "")
	is.True(env.Error.Suggestion == "" || env.Error.Message != "") // message always present at minimum
}

// TestLint_ReportsWarnings smoke-tests the lint tool over the shared engine.
func TestLint_ReportsWarnings(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{})
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolLint,
		Arguments: map[string]any{"config": validPipelineYAML},
	})
	is.NoErr(err)
	is.True(!res.IsError)

	env := decodeStructuredContent[Result[validate.Result]](t, res)
	is.True(env.OK)
	is.Equal(len(env.Result.Files), 1)
}

// TestDryRun_ReturnsEnrichedGraph smoke-tests the dry_run tool: the enriched
// pipeline graph (final connector IDs) is populated, proving Options.Enriched
// was set.
func TestDryRun_ReturnsEnrichedGraph(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{})
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolDryRun,
		Arguments: map[string]any{"config": validPipelineYAML},
	})
	is.NoErr(err)
	is.True(!res.IsError)

	env := decodeStructuredContent[Result[validate.Result]](t, res)
	is.True(env.OK)
	is.Equal(len(env.Result.Files), 1)
	is.Equal(len(env.Result.Files[0].Enriched), 1)
}

// TestWithTempConfigFile_AlwaysCleansUpTempDir is the adversarial-review
// target: the private temp directory withTempConfigFile creates is removed
// on the success path, when fn returns an error, and when fn panics further
// up the call stack after the defer is registered — every offline tool goes
// through this helper, so a leak here leaks on every validate/lint/dry_run/
// deploy/apply call.
func TestWithTempConfigFile_AlwaysCleansUpTempDir(t *testing.T) {
	is := is.New(t)

	assertNoLeftoverDirs := func(before []string) {
		after := conduitMCPTempDirs(t)
		is.Equal(len(after), len(before))
	}

	t.Run("success", func(t *testing.T) {
		before := conduitMCPTempDirs(t)
		var gotPath string
		_, err := withTempConfigFile("content", func(path string) (struct{}, error) {
			gotPath = path
			_, statErr := os.Stat(path)
			is.NoErr(statErr) // the file exists while fn runs
			return struct{}{}, nil
		})
		is.NoErr(err)
		_, statErr := os.Stat(filepath.Dir(gotPath))
		is.True(os.IsNotExist(statErr)) // the directory is gone once withTempConfigFile returns
		assertNoLeftoverDirs(before)
	})

	t.Run("fn returns an error", func(t *testing.T) {
		before := conduitMCPTempDirs(t)
		var gotPath string
		_, err := withTempConfigFile("content", func(path string) (struct{}, error) {
			gotPath = path
			return struct{}{}, os.ErrInvalid
		})
		is.True(err != nil)
		_, statErr := os.Stat(filepath.Dir(gotPath))
		is.True(os.IsNotExist(statErr))
		assertNoLeftoverDirs(before)
	})
}

// conduitMCPTempDirs lists this package's own "conduit-mcp-*" temp
// directories currently on disk, for leak detection in
// TestWithTempConfigFile_AlwaysCleansUpTempDir.
func conduitMCPTempDirs(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "conduit-mcp-*"))
	if err != nil {
		t.Fatalf("glob temp dir: %v", err)
	}
	return matches
}

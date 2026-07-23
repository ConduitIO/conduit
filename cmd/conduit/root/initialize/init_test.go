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

// Full-stack integration test for `conduit init`, mirroring
// cmd/conduit/root/doctor/doctor_test.go's pattern: build the real cobra
// command via ecdysis and drive it through cmd.ExecuteC(), proving `init`'s
// v0.19 migration onto cecdysis.CommandWithResult (workstream 8 — see
// v019-plans/workstreams/cli-contract.md §4.3, §6 AC-4) produces a real
// --json envelope that validates against the shared Family A schema.
package initialize_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/testutils"
	"github.com/conduitio/conduit/cmd/conduit/root/initialize"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/ecdysis"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
	"github.com/spf13/cobra"
)

func buildInitCmd(t *testing.T, cfg *conduit.Config) *cobra.Command {
	t.Helper()
	e := ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithResultDecorator{}))
	return e.MustBuildCobraCommand(&initialize.InitCommand{Cfg: cfg})
}

func runInit(t *testing.T, cfg *conduit.Config, args ...string) (output string, err error) {
	t.Helper()
	cmd := buildInitCmd(t, cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	_, err = cmd.ExecuteC()
	return out.String(), err
}

// TestInit_JSON_ValidatesAgainstEnvelope is the Family A golden fixture for
// `conduit init`: a fresh workspace directory succeeds, and its --json
// output is a well-formed Result envelope (cli-contract.md §6 AC-4).
func TestInit_JSON_ValidatesAgainstEnvelope(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	cfg := conduit.DefaultConfigWithBasePath(dir)

	out, err := runInit(t, &cfg, "--path="+dir, "--json")
	is.NoErr(err)
	is.NoErr(testutils.ValidateEnvelope([]byte(out)))

	var got cecdysis.Result
	is.NoErr(json.Unmarshal([]byte(out), &got))
	is.Equal(got.Command, "init")
	is.True(got.OK)
	is.True(got.Error == nil)

	// The directories/config file described in Outcome.Result must actually
	// exist on disk — the envelope isn't just decorative.
	for _, sub := range []string{"processors", "connectors", "pipelines"} {
		_, statErr := os.Stat(filepath.Join(dir, sub))
		is.NoErr(statErr)
	}
	_, statErr := os.Stat(filepath.Join(dir, "conduit.yaml"))
	is.NoErr(statErr)
}

// TestInit_Human_MatchesPreMigrationText pins the human-mode output text
// (cli-contract.md §7's backward-compat mitigation: only --json is new,
// existing plain-text output must not silently change).
func TestInit_Human_MatchesPreMigrationText(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	cfg := conduit.DefaultConfigWithBasePath(dir)

	out, err := runInit(t, &cfg, "--path="+dir)
	is.NoErr(err)

	is.True(strings.Contains(out, "Created directory: "+filepath.Join(dir, "processors")))
	is.True(strings.Contains(out, "Created directory: "+filepath.Join(dir, "connectors")))
	is.True(strings.Contains(out, "Created directory: "+filepath.Join(dir, "pipelines")))
	is.True(strings.Contains(out, "Config file written to "+filepath.Join(dir, "conduit.yaml")))
	is.True(strings.Contains(out, "Conduit workspace is ready."))
	is.True(strings.Contains(out, "conduit pipelines init"))
	is.True(strings.Contains(out, "conduit run"))
}

// TestInit_RerunSkipsExistingDirs is the --json regression check for the
// pre-migration "Directory '%s' already exists, skipping..." branch: running
// init twice must not error, and the second run's envelope must report
// created:false for every directory.
func TestInit_RerunSkipsExistingDirs(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	cfg := conduit.DefaultConfigWithBasePath(dir)

	_, err := runInit(t, &cfg, "--path="+dir, "--json")
	is.NoErr(err)

	out, err := runInit(t, &cfg, "--path="+dir, "--json")
	is.NoErr(err)
	is.NoErr(testutils.ValidateEnvelope([]byte(out)))

	var got cecdysis.Result
	is.NoErr(json.Unmarshal([]byte(out), &got))
	is.True(got.OK)

	result, ok := got.Result.(map[string]any)
	is.True(ok)
	dirs, ok := result["dirs"].([]any)
	is.True(ok)
	is.Equal(len(dirs), 3)
	for _, d := range dirs {
		entry, ok := d.(map[string]any)
		is.True(ok)
		is.Equal(entry["created"], false)
	}
}

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

package cli_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/root"
	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/ecdysis"
	json "github.com/goccy/go-json"
)

// familyALeafPaths walks the live command tree, like
// TestSchemaGolden_Completeness, and returns the command path (without the
// root) of every Family A leaf.
func familyALeafPaths() [][]string {
	var paths [][]string
	var walk func(prefix []string, cmds []ecdysis.Command)
	walk = func(prefix []string, cmds []ecdysis.Command) {
		for _, c := range cmds {
			name := strings.Fields(c.Usage())[0]
			path := append(append([]string{}, prefix...), name)
			if sub, ok := c.(ecdysis.CommandWithSubCommands); ok {
				walk(path, sub.SubCommands())
				continue
			}
			if _, ok := c.(cecdysis.CommandWithResult); ok {
				paths = append(paths, path)
			}
		}
	}
	walk(nil, (&root.RootCommand{}).SubCommands())
	return paths
}

// TestFamilyA_UnknownFlag_RendersEnvelopeAndExits2 is the tree-wide
// regression test for #3086: every Family A leaf, run with an unknown flag
// and --json, must print exactly one envelope carrying cli.invalid_usage to
// stdout and exit 2. Before the fix each one printed nothing and exited 1.
// The --json comes after the bad flag, so pflag never parses it; that is
// the order a user types and the case the raw-argument scan exists for.
func TestFamilyA_UnknownFlag_RendersEnvelopeAndExits2(t *testing.T) {
	paths := familyALeafPaths()
	if len(paths) == 0 {
		t.Fatal("found no Family A commands; the walk is broken")
	}

	for _, path := range paths {
		t.Run(strings.Join(path, "_"), func(t *testing.T) {
			args := append(append([]string{}, path...), "--bogus-flag-3086", "--json")

			// jsonRequested reads os.Args when pflag stopped before --json.
			oldArgs := os.Args
			os.Args = append([]string{"conduit"}, args...)
			defer func() { os.Args = oldArgs }()

			e := ecdysis.New(ecdysis.WithDecorators(
				cecdysis.CommandWithExecuteWithClientDecorator{},
				cecdysis.CommandWithExecuteWithClientResultDecorator{},
				cecdysis.CommandWithResultDecorator{},
			))
			cmd := e.MustBuildCobraCommand(&root.RootCommand{})
			var out, errOut bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&errOut)
			cmd.SetArgs(args)

			_, err := cmd.ExecuteC()
			if err == nil {
				t.Fatal("expected an error for an unknown flag")
			}
			if got := exitcode.ExitCode(err); got != exitcode.Validation {
				t.Errorf("exit code %d, want %d (validation)", got, exitcode.Validation)
			}

			var res cecdysis.Result
			if err := json.Unmarshal(out.Bytes(), &res); err != nil {
				t.Fatalf("stdout is not one JSON envelope: %v\nstdout: %q\nstderr: %q", err, out.String(), errOut.String())
			}
			if res.OK || res.Error == nil || res.Error.Code != "cli.invalid_usage" {
				t.Errorf("unexpected envelope: %s", out.String())
			}
			if res.Command == "" {
				t.Errorf("envelope has no command: %s", out.String())
			}
		})
	}
}

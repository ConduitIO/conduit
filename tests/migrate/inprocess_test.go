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

package migrate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	cmdmigrate "github.com/conduitio/conduit/cmd/conduit/root/migrate"
	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/tests/migrate/harness"
	"github.com/conduitio/ecdysis"
)

// inProcess is the design's per-PR offline runner (mapping design, "Harness
// corpus format"): a harness.Migrator that runs `migrate kafka-connect`
// inside the test binary, through the real command and its --json
// decorator, so the normal `test` job checks every ready case without a
// conduit binary. It is the same code path as the CLI (cmd/conduit/root/
// migrate over pkg/kcmigrate/migrate.Run); TestMigrate_CLI keeps covering
// the built binary.
type inProcess struct{}

func (inProcess) Name() string { return "in-process" }

func (inProcess) Available(context.Context) error { return nil }

// Migrate runs the command with --json and --out. Input paths in req.Args
// must be absolute: the test binary's working directory is shared by every
// test, so the runner never changes it.
func (inProcess) Migrate(ctx context.Context, req harness.Request) (harness.Output, error) {
	args := req.Args
	// Case commands start with the command path; the in-process command is
	// already the kafka-connect leaf.
	if len(args) >= 2 && args[0] == "migrate" && args[1] == "kafka-connect" {
		args = args[2:]
	}
	for _, a := range args {
		if strings.HasPrefix(a, "input/") {
			return harness.Output{}, cerrors.Errorf("in-process runs need absolute input paths, got %q", a)
		}
	}
	cmd := ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithResultDecorator{})).
		MustBuildCobraCommand(&cmdmigrate.KafkaConnectCommand{})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(append(append([]string{}, args...), "--json", "--out", req.OutDir))
	executed, err := cmd.ExecuteContextC(ctx)

	out := harness.Output{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: exitcode.ExitCode(err)}
	if code, ok := cecdysis.ResultExitCode(executed); ok && err == nil {
		out.ExitCode = code
	}
	files, rerr := readOutTree(req.OutDir)
	if rerr != nil {
		return out, rerr
	}
	out.Files = files
	return out, nil
}

// readOutTree returns every file under dir keyed by slash path, as the CLI
// migrator does; a missing dir is empty.
func readOutTree(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == dir {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		return nil, cerrors.Errorf("read %s: %w", dir, err)
	}
	return files, nil
}

// TestMigrate_InProcess runs every ready case in-process and applies the
// checks that need no binary: the report contract and completeness, the
// case's expectations, no leaked secret or host path, the goldens, and
// determinism between two runs. Unlike TestMigrate_CLI it runs in every
// `go test`, so a rule-data change that breaks a golden fails the PR that
// makes it. Cases whose rule data is missing stay pending, as in the CLI
// runner.
func TestMigrate_InProcess(t *testing.T) {
	e := harnessEnv(t)
	ctx := context.Background()
	m := inProcess{}
	for _, c := range loadCorpus(t) {
		t.Run(c.Rel, func(t *testing.T) {
			name := "inprocess/" + c.Rel
			track(t, name)
			inputs, err := harness.InputKeys(c.Dir, c.Inputs())
			if err != nil {
				t.Fatal(err)
			}
			if ready, why := harness.CaseReady(c, inputs, e.rules); !ready {
				pending(t, name, false, cerrors.Errorf("%s: %w", why, harness.ErrPending))
			}
			outDir := filepath.Join(t.TempDir(), "out")
			out, err := m.Migrate(ctx, harness.Request{Case: c, WorkDir: c.Dir, Args: absArgs(c), OutDir: outDir})
			if err != nil {
				t.Fatal(err)
			}
			res := harness.CheckOutput(harness.Check{Case: c, Inputs: inputs, Out: out, HostPaths: hostPaths(c.Dir, outDir)})
			for _, p := range res.Problems {
				t.Error(p)
			}
			if err := harness.ValidateSchema(e.repoRoot, harness.EnvelopeSchemaPath, out.Stdout); err != nil {
				t.Errorf("envelope: %v", err)
			}
			if res.Envelope != nil && res.Envelope.Error == nil {
				if err := harness.ValidateSchema(e.repoRoot, harness.ReportSchemaPath, res.Envelope.Result); err != nil {
					t.Errorf("report: %v", err)
				}
			}
			goldens, err := harness.CompareGoldens(c.Dir, out, *update)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range goldens {
				t.Error(p)
			}
			again, err := m.Migrate(ctx, harness.Request{Case: c, WorkDir: c.Dir, Args: absArgs(c), OutDir: filepath.Join(t.TempDir(), "out")})
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range harness.CompareRuns(out, again) {
				t.Error("determinism: " + p)
			}
		})
	}
}

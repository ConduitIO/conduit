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
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/tests/migrate/harness"
	"github.com/goccy/go-json"
)

var update = flag.Bool("update", false, "rewrite each case's expected/ goldens from the command's output")

var recorder harness.Recorder

func TestMain(m *testing.M) {
	code := m.Run()
	if p := os.Getenv("MIGRATE_HARNESS_SUMMARY"); p != "" {
		if err := recorder.WriteMarkdown(p); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
		}
	}
	os.Exit(code)
}

// runEnv is how the runner was configured.
//
//   - CONDUIT_BIN: absolute path of the conduit binary under test. Unset (the
//     normal `go test ./...`): every check that runs the command is pending.
//   - MIGRATE_HARNESS_REQUIRE_CLI=1: pending CLI checks fail instead. Implied
//     when CONDUIT_BIN is set and cmd/conduit/root/migrate exists (the
//     command is registered), so the harness job cannot stay green on
//     "pending" once the command is in the tree. Keyed on the command, not on
//     pkg/kcmigrate: the report types land there first (#3069), before there
//     is anything to run.
//   - MIGRATE_HARNESS_REQUIRE_MCP=1: the same for the MCP parity checks.
//     Implied when CONDUIT_BIN is set and the MCP adapter's source registers
//     kc_migrate.
//   - MIGRATE_HARNESS_RELEASE=1: the release gate. Implies both, and
//     certification entries must be certified.
type runEnv struct {
	bin        string
	repoRoot   string
	requireCLI bool
	requireMCP bool
	release    bool
}

func harnessEnv(t *testing.T) runEnv {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := harness.RepoRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	e := runEnv{bin: os.Getenv("CONDUIT_BIN"), repoRoot: root, release: releaseMode()}
	if e.bin != "" && !filepath.IsAbs(e.bin) {
		t.Fatalf("CONDUIT_BIN must be absolute, got %q", e.bin)
	}
	_, statErr := os.Stat(filepath.Join(root, "cmd", "conduit", "root", "migrate"))
	e.requireCLI = e.release || os.Getenv("MIGRATE_HARNESS_REQUIRE_CLI") == "1" || (e.bin != "" && statErr == nil)
	e.requireMCP = e.release || os.Getenv("MIGRATE_HARNESS_REQUIRE_MCP") == "1" || (e.bin != "" && sourceRegistersTool(t, root, harness.ToolKCMigrate))
	return e
}

// sourceRegistersTool reports whether a non-test Go file of the MCP adapter
// names the tool as a string literal, which is how catalog.go registers
// tools. Once it does, parity checks fail instead of reporting pending.
func sourceRegistersTool(t *testing.T, root, tool string) bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, "cmd", "conduit", "internal", "mcp", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(`"`+tool+`"`)) {
			return true
		}
	}
	return false
}

// track records the subtest's outcome for the summary.
func track(t *testing.T, name string) {
	t.Cleanup(func() {
		switch {
		case t.Skipped(): // recorded by pending
		case t.Failed():
			recorder.Record(name, harness.OutcomeFail, "see the test log")
		default:
			recorder.Record(name, harness.OutcomePass, "")
		}
	})
}

// pending skips a check whose surface does not exist yet, or fails it when
// that surface is required. A pending check is never reported as passing.
func pending(t *testing.T, name string, required bool, err error) {
	t.Helper()
	if !cerrors.Is(err, harness.ErrPending) {
		t.Fatalf("%s unavailable: %v", name, err)
	}
	if required {
		t.Fatalf("required but not available: %v", err)
	}
	recorder.Record(name, harness.OutcomePending, err.Error())
	t.Skipf("PENDING: %v", err)
}

// hostPaths returns the absolute paths, symlink-resolved too, that must not
// appear in the output.
func hostPaths(paths ...string) []string {
	var out []string
	for _, p := range paths {
		out = append(out, p)
		if r, err := filepath.EvalSymlinks(p); err == nil && r != p {
			out = append(out, r)
		}
	}
	return out
}

func absArgs(c harness.Case) []string {
	args := append([]string{}, c.Manifest.Command.Args...)
	for i, a := range args {
		if strings.HasPrefix(a, "input/") {
			args[i] = filepath.Join(c.Dir, filepath.FromSlash(a))
		}
	}
	return args
}

// TestMigrate_CLI runs every case through `conduit migrate kafka-connect`
// and checks: the envelope schema, the report schema, the report contract
// and completeness (no key silently dropped), the case's expectations, the
// absence of leaked secrets and host paths, determinism across two working
// directories, the goldens, `pipelines validate` on the generated pipelines,
// and finally the certification gate over the plugins actually emitted.
func TestMigrate_CLI(t *testing.T) {
	e := harnessEnv(t)
	ctx := context.Background()
	cli := harness.CLI{Bin: e.bin}
	avail := cli.Available(ctx)
	cases := loadCorpus(t)

	var mu sync.Mutex
	var emitted []string

	for _, c := range cases {
		t.Run(c.Rel, func(t *testing.T) {
			name := "cli/" + c.Rel
			track(t, name)
			if avail != nil {
				pending(t, name, e.requireCLI, avail)
			}
			inputs, err := harness.InputKeys(c.Dir, c.Inputs())
			if err != nil {
				t.Fatal(err)
			}

			outDirA := filepath.Join(t.TempDir(), "out")
			outA, err := cli.Migrate(ctx, harness.Request{Case: c, WorkDir: c.Dir, Args: c.Manifest.Command.Args, OutDir: outDirA})
			if err != nil {
				t.Fatal(err)
			}
			res := harness.CheckOutput(harness.Check{Case: c, Inputs: inputs, Out: outA, HostPaths: hostPaths(c.Dir, outDirA)})
			for _, p := range res.Problems {
				t.Error(p)
			}
			if err := harness.ValidateSchema(e.repoRoot, harness.EnvelopeSchemaPath, outA.Stdout); err != nil {
				t.Errorf("envelope: %v", err)
			}
			if res.Envelope != nil && res.Envelope.Error == nil {
				// The CLI exists, so the schema must too (fail closed).
				if err := harness.ValidateSchema(e.repoRoot, harness.ReportSchemaPath, res.Envelope.Result); err != nil {
					t.Errorf("report: %v", err)
				}
			}

			workB := t.TempDir()
			outDirB := filepath.Join(t.TempDir(), "out")
			outB, err := cli.Migrate(ctx, harness.Request{Case: c, WorkDir: workB, Args: absArgs(c), OutDir: outDirB})
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range harness.CompareRuns(outA, outB) {
				t.Error("determinism: " + p)
			}

			goldens, err := harness.CompareGoldens(c.Dir, outA, *update)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range goldens {
				t.Error(p)
			}

			if _, err := os.Stat(filepath.Join(outDirA, "pipelines")); err == nil {
				if err := cli.ValidatePipelines(ctx, filepath.Join(outDirA, "pipelines")); err != nil {
					t.Errorf("generated pipelines do not validate: %v", err)
				}
			}

			mu.Lock()
			emitted = append(emitted, res.Plugins...)
			mu.Unlock()
		})
	}

	t.Run("certification", func(t *testing.T) {
		track(t, "cli/certification")
		if avail != nil {
			pending(t, "cli/certification", e.requireCLI, avail)
		}
		entries, problems := harness.LoadCertification(filepath.Join(corpusRoot, "certification.yaml"))
		for _, p := range problems {
			t.Error(p)
		}
		if len(emitted) == 0 {
			t.Fatal("no plugin was emitted by any case; the certification gate would pass vacuously")
		}
		for _, p := range harness.CheckCertification(entries, emitted, e.release) {
			t.Error(p)
		}
	})
}

// TestMigrate_MCPParity runs every case through the kc_migrate MCP tool and
// requires the same result and the same files as the CLI.
func TestMigrate_MCPParity(t *testing.T) {
	e := harnessEnv(t)
	ctx := context.Background()
	cli := harness.CLI{Bin: e.bin}
	mcp := harness.MCP{Bin: e.bin}
	cliAvail := cli.Available(ctx)
	var mcpAvail error
	if cliAvail == nil {
		mcpAvail = mcp.Available(ctx)
	}

	for _, c := range loadCorpus(t) {
		t.Run(c.Rel, func(t *testing.T) {
			name := "mcp/" + c.Rel
			track(t, name)
			if cliAvail != nil {
				pending(t, name, e.requireMCP, cliAvail)
			}
			if mcpAvail != nil {
				pending(t, name, e.requireMCP, mcpAvail)
			}
			inputs, err := harness.InputKeys(c.Dir, c.Inputs())
			if err != nil {
				t.Fatal(err)
			}
			cliOut, err := cli.Migrate(ctx, harness.Request{Case: c, WorkDir: c.Dir, Args: c.Manifest.Command.Args, OutDir: filepath.Join(t.TempDir(), "out")})
			if err != nil {
				t.Fatal(err)
			}
			mcpOut, err := mcp.Migrate(ctx, harness.Request{Case: c})
			if err != nil {
				t.Fatal(err)
			}
			res := harness.CheckOutput(harness.Check{Case: c, Inputs: inputs, Out: mcpOut, HostPaths: hostPaths(c.Dir), EnvelopeOnly: true})
			for _, p := range res.Problems {
				t.Error("mcp: " + p)
			}
			for _, p := range parity(cliOut, mcpOut) {
				t.Error(p)
			}
		})
	}
}

// parity compares ok, summary, result and error of the two envelopes after
// canonical re-encoding, and every file the tool returns with the CLI's.
func parity(cliOut, mcpOut harness.Output) []string {
	var p []string
	pick := func(b []byte) (map[string]any, error) {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err //nolint:wrapcheck // reported with context below
		}
		delete(m, "command") // the MCP envelope has no command discriminator
		return m, nil
	}
	a, errA := pick(cliOut.Stdout)
	b, errB := pick(mcpOut.Stdout)
	if errA != nil || errB != nil {
		return []string{"parity: an envelope is not JSON"}
	}
	for _, k := range []string{"ok", "summary", "result", "error"} {
		ja, _ := json.Marshal(a[k])
		jb, _ := json.Marshal(b[k])
		if !bytes.Equal(ja, jb) {
			p = append(p, "parity: envelope field "+k+" differs between the CLI and kc_migrate")
		}
	}
	for name, content := range mcpOut.Files {
		if !bytes.Equal(cliOut.Files[name], content) {
			p = append(p, "parity: file "+name+" differs between the CLI and kc_migrate")
		}
	}
	for name := range cliOut.Files {
		if _, ok := mcpOut.Files[name]; !ok && name != "report.json" {
			p = append(p, "parity: kc_migrate did not return "+name)
		}
	}
	return p
}

// TestValidator_Smoke proves the `pipelines validate` step the runner uses
// can fail and pass, against the repository's own validate fixtures. It runs
// today: the validate command exists.
func TestValidator_Smoke(t *testing.T) {
	e := harnessEnv(t)
	track(t, "validate/smoke")
	cli := harness.CLI{Bin: e.bin}
	if e.bin == "" {
		pending(t, "validate/smoke", false, cerrors.Errorf("CONDUIT_BIN is not set: %w", harness.ErrPending))
	}
	ctx := context.Background()
	fixtures := filepath.Join(e.repoRoot, "cmd", "conduit", "internal", "validate", "testdata")
	if err := cli.ValidatePipelines(ctx, filepath.Join(fixtures, "valid.yaml")); err != nil {
		t.Errorf("valid fixture: %v", err)
	}
	if err := cli.ValidatePipelines(ctx, filepath.Join(fixtures, "errors.yaml")); err == nil {
		t.Error("errors fixture validated; the validate step cannot fail")
	}
}

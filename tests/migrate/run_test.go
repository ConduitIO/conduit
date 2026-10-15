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
//   - With CONDUIT_BIN set, a case is required, not pending, once the rule
//     data in pkg/kcmigrate/rules/data covers it (harness.CaseReady): its
//     family's class file, the shared file for unsupported-class cases, the
//     worker file for --worker cases. Families without a rule file stay
//     pending, so the command landing hidden with every class unsupported
//     (#3074) changes nothing; each family's rule-file PR turns its cases on.
//   - MCP parity is required for a ready case once a top-level Go file of
//     cmd/conduit/internal/mcp registers kc_migrate.
//   - MIGRATE_HARNESS_REQUIRE_CLI=1 / MIGRATE_HARNESS_REQUIRE_MCP=1: every
//     case required, ready or not.
//   - MIGRATE_HARNESS_RELEASE=1: the release gate. Implies both, and
//     certification entries must be certified.
type runEnv struct {
	bin           string
	repoRoot      string
	rules         harness.RuleSet
	forceCLI      bool
	forceMCP      bool
	mcpRegistered bool
	release       bool
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
	if e.rules, err = harness.LoadRuleSet(root); err != nil {
		t.Fatal(err)
	}
	e.forceCLI = e.release || os.Getenv("MIGRATE_HARNESS_REQUIRE_CLI") == "1"
	e.forceMCP = e.release || os.Getenv("MIGRATE_HARNESS_REQUIRE_MCP") == "1"
	e.mcpRegistered = sourceRegistersTool(t, root, harness.ToolKCMigrate)
	return e
}

// cliRequired decides whether a case's CLI checks are required. It returns
// a pending error when the case's rule data is missing.
func (e runEnv) cliRequired(c harness.Case, inputs []harness.InputFile) (bool, error) {
	if e.forceCLI {
		return true, nil
	}
	ready, why := harness.CaseReady(c, inputs, e.rules)
	if !ready {
		return false, cerrors.Errorf("%s: %w", why, harness.ErrPending)
	}
	return e.bin != "", nil
}

// sourceRegistersTool reports whether a non-test Go file of the MCP adapter
// names the tool as a string literal, which is how catalog.go registers
// tools. Once it does, parity checks of ready cases fail instead of
// reporting pending. It scans only the top level of cmd/conduit/internal/mcp
// (where catalog.go lives), not subdirectories: a registration moved into a
// subpackage would leave parity pending until this glob follows it.
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
	declared := 0 // ready cases that declare emitted plugins

	for _, c := range cases {
		t.Run(c.Rel, func(t *testing.T) {
			name := "cli/" + c.Rel
			track(t, name)
			plugins := runCLICase(ctx, t, e, cli, avail, c)
			mu.Lock()
			emitted = append(emitted, plugins...)
			if len(c.Manifest.Emits) > 0 {
				declared++
			}
			mu.Unlock()
		})
	}

	t.Run("certification", func(t *testing.T) {
		track(t, "cli/certification")
		if avail != nil {
			pending(t, "cli/certification", e.forceCLI, avail)
		}
		if declared == 0 && !e.forceCLI {
			// Unsupported-class cases emit nothing; the gate starts with the
			// first ready case that declares an emitted plugin.
			pending(t, "cli/certification", false, cerrors.Errorf("no ready case emits a plugin yet: %w", harness.ErrPending))
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

// runCLICase runs one case through the CLI and every check, and returns the
// plugins its pipelines reference. It skips (pending) or fails as runEnv says.
func runCLICase(ctx context.Context, t *testing.T, e runEnv, cli harness.CLI, avail error, c harness.Case) []string {
	t.Helper()
	inputs, err := harness.InputKeys(c.Dir, c.Inputs())
	if err != nil {
		t.Fatal(err)
	}
	required, notReady := e.cliRequired(c, inputs)
	if avail != nil {
		pending(t, "cli/"+c.Rel, required, avail)
	}
	if notReady != nil {
		pending(t, "cli/"+c.Rel, false, notReady)
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

	return res.Plugins
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
			inputs, err := harness.InputKeys(c.Dir, c.Inputs())
			if err != nil {
				t.Fatal(err)
			}
			cliReq, notReady := e.cliRequired(c, inputs)
			required := e.forceMCP || (cliReq && e.mcpRegistered)
			if cliAvail != nil {
				pending(t, name, required, cliAvail)
			}
			if mcpAvail != nil {
				pending(t, name, required, mcpAvail)
			}
			if notReady != nil && !e.forceMCP {
				pending(t, name, false, notReady)
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

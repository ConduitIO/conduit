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

package harness

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/goccy/go-json"
)

// ErrPending marks a check that cannot run yet because the surface it
// exercises does not exist (the migrate command, the MCP tools, the report
// schema file). Tests report it as pending, never as passing.
var ErrPending = cerrors.New("not available yet")

// Request is one run of the migrate command over a case.
type Request struct {
	// Case is the case being run.
	Case Case
	// WorkDir is the working directory of the run.
	WorkDir string
	// Args is the command line without the runner's flags. Input paths are
	// relative to WorkDir or absolute.
	Args []string
	// OutDir is the output directory to pass as --out. It must not exist.
	OutDir string
}

// Output is what one run produced.
type Output struct {
	ExitCode int
	// Stdout is the --json envelope. For the MCP adapter it is the tool's
	// structured content, which has the same {ok, summary, result, error}
	// shape without "command".
	Stdout []byte
	// Stderr is never compared, only scanned for leaked canaries.
	Stderr []byte
	// Files are the files written under OutDir, keyed by slash path relative
	// to it.
	Files map[string][]byte
}

// Migrator runs `migrate kafka-connect` one way: the CLI binary, the MCP
// tool, or (once pkg/kcmigrate exists) the in-process kcmigrate.Run that the
// design's per-PR offline runner calls. All three must agree (MCP parity).
type Migrator interface {
	// Name labels the migrator in failures.
	Name() string
	// Available returns nil when Migrate can run, or an error wrapping
	// ErrPending when the surface does not exist yet.
	Available(ctx context.Context) error
	Migrate(ctx context.Context, req Request) (Output, error)
}

// runTimeout bounds every child process the harness starts.
const runTimeout = 2 * time.Minute

// CLI runs the conduit binary at Bin.
type CLI struct {
	Bin string
}

func (c CLI) Name() string { return "cli" }

// Available probes `conduit migrate kafka-connect --help`.
func (c CLI) Available(ctx context.Context) error {
	if c.Bin == "" {
		return cerrors.Errorf("CONDUIT_BIN is not set, so there is no conduit binary to run: %w", ErrPending)
	}
	out, code, err := c.run(ctx, "", []string{cmdMigrate, cmdKafkaConnect, "--help"})
	if err != nil {
		return err
	}
	if code != 0 || bytes.Contains(out, []byte("unknown command")) {
		return cerrors.Errorf("the conduit binary has no `migrate kafka-connect` command (#3003): %w", ErrPending)
	}
	return nil
}

// Migrate runs the command with --json and --out added.
func (c CLI) Migrate(ctx context.Context, req Request) (Output, error) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	args := append(append([]string{}, req.Args...), flagJSON, "--out", req.OutDir)
	cmd, stdout, stderr := c.command(ctx, req.WorkDir, args)
	err := cmd.Run()
	out := Output{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case cerrors.As(err, &exitErr):
		out.ExitCode = exitErr.ExitCode()
	default:
		return out, cerrors.Errorf("run %s: %w", c.Bin, err)
	}
	files, err := readTree(req.OutDir)
	if err != nil {
		return out, err
	}
	out.Files = files
	return out, nil
}

// ValidatePipelines runs `conduit pipelines validate --json` over dir and
// returns nil when every pipeline file in it is valid.
func (c CLI) ValidatePipelines(ctx context.Context, dir string) error {
	out, code, err := c.run(ctx, "", []string{"pipelines", "validate", dir, flagJSON})
	if err != nil {
		return err
	}
	var env struct {
		OK bool `json:"ok"`
	}
	if jerr := json.Unmarshal(out, &env); jerr != nil {
		return cerrors.Errorf("pipelines validate printed no JSON envelope (exit %d): %w", code, jerr)
	}
	if code != 0 || !env.OK {
		return cerrors.Errorf("pipelines validate failed (exit %d): %s", code, truncate(out, 4000))
	}
	return nil
}

func (c CLI) run(ctx context.Context, dir string, args []string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	cmd, stdout, stderr := c.command(ctx, dir, args)
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return stdout.Bytes(), 0, nil
	case cerrors.As(err, &exitErr):
		return append(stdout.Bytes(), stderr.Bytes()...), exitErr.ExitCode(), nil
	default:
		return nil, 0, cerrors.Errorf("run %s: %w", c.Bin, err)
	}
}

// command builds the child process. ctx must carry the caller's deadline;
// cancelling it kills the process.
func (c CLI) command(ctx context.Context, dir string, args []string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	cmd := exec.CommandContext(ctx, c.Bin, args...) //nolint:gosec // the binary under test, chosen by the test runner
	cmd.Dir = dir
	cmd.Env = fixedEnv(scratchHome())
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	return cmd, &stdout, &stderr
}

// fixedEnv is the whole environment of every child process: never the
// runner's own, so a report can depend on nothing but its inputs and the
// harness never passes a credential from CI into the command under test.
func fixedEnv(home string) []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=" + home, "NO_COLOR=1", "LANG=C", "TZ=UTC"}
}

var (
	homeOnce sync.Once
	homeDir  string
)

// scratchHome is one empty HOME per test binary, so the command cannot read
// the runner's dotfiles. It lives under the OS temp dir and is left for the
// OS to clean, like the go test cache.
func scratchHome() string {
	homeOnce.Do(func() {
		d, err := os.MkdirTemp("", "conduit-migrate-harness-home-")
		if err != nil {
			d = os.TempDir()
		}
		homeDir = d
	})
	return homeDir
}

// readTree returns every regular file under dir, keyed by slash path. A
// missing dir is an empty tree: a rejected input writes nothing.
func readTree(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return files, nil
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err //nolint:wrapcheck // wrapped below
		}
		b, err := os.ReadFile(p) //nolint:gosec // p is under an output directory this run created
		if err != nil {
			return err //nolint:wrapcheck // wrapped below
		}
		files[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		return nil, cerrors.Errorf("read output tree: %w", err)
	}
	return files, nil
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

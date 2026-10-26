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

//go:build unix

package chaos

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	json "github.com/goccy/go-json"
)

// The other scenarios in this package drive the engine in-process with
// builtin plugins. This one runs the real binary (cmd/conduit) with a real
// standalone plugin (testdata/standalonelog, the log connector built against
// this module's SDK pin), because ConduitIO/conduit#3092 lived in the SDK's
// destination adapter on the far side of the plugin's Stop RPC.
//
// #3092: with SDK batching, once the pipeline had drained, SIGTERM took a
// minute and failed with "could not stop destination plugin: context deadline
// exceeded" whenever the last record filled a batch: the SDK's batch Flush
// waited for the result of a flush that never happened, until its 1 minute
// stop timeout. That breaks invariant 7 (graceful shutdown) for every
// operator whose termination grace period is under a minute.

const (
	// standaloneSigtermExit bounds the graceful exit. The bug took the SDK's
	// full 60s stop timeout; a healthy drained shutdown takes well under a
	// second. The gap leaves room for a loaded CI runner without letting the
	// bug through.
	standaloneSigtermExit = 20 * time.Second
	// standaloneDrainTimeout bounds the drain itself (a few hundred records).
	standaloneDrainTimeout = 60 * time.Second
	standaloneBatchSize    = 50
)

var (
	standaloneBuildOnce sync.Once
	standaloneConduit   string
	standaloneLogPlugin string
	errStandaloneBuild  error
)

// buildStandaloneBinaries builds cmd/conduit and the standalone log plugin
// once per test binary (so -count=N pays for it once).
func buildStandaloneBinaries(t *testing.T) (conduitBin, pluginBin string) {
	t.Helper()
	standaloneBuildOnce.Do(func() {
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			errStandaloneBuild = err
			return
		}
		dir, err := os.MkdirTemp("", "chaos-standalone-")
		if err != nil {
			errStandaloneBuild = err
			return
		}
		// Shared by every test in the binary (and every -count run), so it is
		// removed after all tests finish, not in t.Cleanup. About 139MB.
		afterTests = append(afterTests, func() { _ = os.RemoveAll(dir) })
		build := func(out, pkg string) error {
			cmd := exec.CommandContext(context.Background(), "go", "build", "-o", out, pkg)
			cmd.Dir = root
			b, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("go build %s: %w\n%s", pkg, err, b)
			}
			return nil
		}
		standaloneConduit = filepath.Join(dir, "conduit")
		standaloneLogPlugin = filepath.Join(dir, "conduit-connector-log")
		if err := build(standaloneConduit, "./cmd/conduit"); err != nil {
			errStandaloneBuild = err
			return
		}
		errStandaloneBuild = build(standaloneLogPlugin, "./tests/chaos/testdata/standalonelog")
	})
	if errStandaloneBuild != nil {
		t.Fatalf("build binaries: %v", errStandaloneBuild)
	}
	return standaloneConduit, standaloneLogPlugin
}

// conduitProcess is a real Conduit process in its own process group, so its
// plugins can be killed with it. Its output is scanned as it arrives; seen
// fires when a line contains the awaited substring.
type conduitProcess struct {
	cmd    *exec.Cmd
	exited chan struct{}
	err    error // cmd.Wait's result, set before exited closes

	mu       sync.Mutex
	lines    []string
	awaited  string
	seen     chan struct{}
	seenOnce sync.Once
}

func (p *conduitProcess) output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.lines, "\n")
}

func startConduit(t *testing.T, bin, dir, awaited string) *conduitProcess {
	t.Helper()
	cmd := exec.CommandContext(
		context.Background(), bin, "run",
		"--pipelines.path", filepath.Join(dir, "pipelines"),
		"--connectors.path", filepath.Join(dir, "connectors"),
		"--processors.path", filepath.Join(dir, "processors"),
		"--connectors.file.allowed-dirs", dir,
		"--db.type", "badger",
		"--db.badger.path", filepath.Join(dir, "db"),
		"--api.enabled=false",
		"--log.format", "json",
		"--log.level", "info",
	)
	cmd.Dir = dir
	// fixed environment: nothing from the test's environment leaks in
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	cmd.Stdout, cmd.Stderr = w, w

	p := &conduitProcess{cmd: cmd, exited: make(chan struct{}), awaited: awaited, seen: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start conduit: %v", err)
	}
	_ = w.Close() // the child holds its own copy

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := sc.Text()
			p.mu.Lock()
			p.lines = append(p.lines, line)
			p.mu.Unlock()
			if strings.Contains(line, p.awaited) {
				p.seenOnce.Do(func() { close(p.seen) })
			}
		}
	}()
	go func() {
		err := cmd.Wait()
		<-readerDone // EOF: every process holding the pipe exited
		p.err = err
		close(p.exited)
	}()

	t.Cleanup(func() {
		// whatever happened, leave no Conduit or plugin process behind
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-p.exited
		_ = r.Close()
	})
	return p
}

// writeStandalonePipeline lays out a Conduit working directory: n numbered
// lines, builtin:file -> standalone:log with SDK batching.
func writeStandalonePipeline(t *testing.T, plugin string, n int, batchDelay string) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"pipelines", "connectors", "processors"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(plugin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "connectors", "conduit-connector-log"), b, 0o755); err != nil {
		t.Fatal(err)
	}
	var in strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&in, "%d\n", i)
	}
	input := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(input, []byte(in.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline := fmt.Sprintf(`version: "2.2"
pipelines:
  - id: p3092
    status: running
    connectors:
      - id: src
        type: source
        plugin: builtin:file
        settings:
          path: %q
      - id: dst
        type: destination
        plugin: standalone:log
        settings:
          level: warn
          # conduit-connector-log v0.8.0 logs only hashes unless payload is
          # true (GHSA-5c9c-5rf5-2r5q); loggedPayloads needs the payloads to
          # check that every record was written (invariant 3).
          payload: "true"
          sdk.batch.size: "%d"
          sdk.batch.delay: %q
`, input, standaloneBatchSize, batchDelay)
	if err := os.WriteFile(filepath.Join(dir, "pipelines", "p.yaml"), []byte(pipeline), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// loggedPayloads returns how often each record payload was logged by the
// destination (the log connector logs every record it is asked to write, in
// full because the pipeline sets payload:true).
func loggedPayloads(t *testing.T, out string) map[string]int {
	t.Helper()
	got := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, `"record":{`) {
			continue
		}
		var l struct {
			Record struct {
				Payload struct {
					After string `json:"after"`
				} `json:"payload"`
			} `json:"record"`
		}
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			continue // not a record line
		}
		p, err := base64.StdEncoding.DecodeString(l.Record.Payload.After)
		if err != nil {
			t.Fatalf("payload %q is not base64: %v", l.Record.Payload.After, err)
		}
		got[string(p)]++
	}
	return got
}

// TestSIGTERM_StandaloneBatchingDestination_DrainedPipelineExits: a pipeline
// whose standalone destination uses SDK batching drains, then gets SIGTERM.
// Invariant 7: Conduit stops the pipeline cleanly and exits promptly, with no
// stop error, and every record was written (invariant 3).
func TestSIGTERM_StandaloneBatchingDestination_DrainedPipelineExits(t *testing.T) {
	conduitBin, pluginBin := buildStandaloneBinaries(t)

	for _, tc := range []struct {
		name       string
		records    int
		batchDelay string
	}{{
		// #3092: no timer, so batches are cut by size only and the last
		// record fills the last batch
		name:       "last record fills a batch",
		records:    10 * standaloneBatchSize,
		batchDelay: "0s",
	}, {
		// the delay timer writes the partial last batch during the drain
		name:       "partial last batch flushed by the timer",
		records:    10*standaloneBatchSize + 7,
		batchDelay: "20ms",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeStandalonePipeline(t, pluginBin, tc.records, tc.batchDelay)
			last := base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(tc.records)))
			p := startConduit(t, conduitBin, dir, fmt.Sprintf(`"after":%q`, last))

			// drained: the destination wrote the last record
			select {
			case <-p.seen:
			case <-p.exited:
				t.Fatalf("conduit exited before draining: %v\n%s", p.err, p.output())
			case <-time.After(standaloneDrainTimeout):
				t.Fatalf("pipeline did not drain within %s\n%s", standaloneDrainTimeout, p.output())
			}

			// Invariant 7: SIGTERM drains (nothing left here) and exits
			start := time.Now()
			if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatalf("SIGTERM: %v", err)
			}
			select {
			case <-p.exited:
			case <-time.After(standaloneSigtermExit):
				t.Fatalf("conduit did not exit within %s of SIGTERM after the drain (ConduitIO/conduit#3092)\n%s",
					standaloneSigtermExit, p.output())
			}
			out := p.output()
			if p.err != nil {
				t.Fatalf("conduit exited with %v after %s\n%s", p.err, time.Since(start), out)
			}
			if strings.Contains(out, "could not stop destination plugin") {
				t.Fatalf("destination did not stop cleanly\n%s", out)
			}

			// Invariant 3: every record was written
			got := loggedPayloads(t, out)
			for i := 1; i <= tc.records; i++ {
				if got[strconv.Itoa(i)] == 0 {
					t.Fatalf("record %d was never written (%d distinct records logged)", i, len(got))
				}
			}
			t.Logf("exited %s after SIGTERM, %d records written", time.Since(start).Round(time.Millisecond), len(got))
		})
	}
}

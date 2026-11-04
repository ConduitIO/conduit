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

package resume

import (
	"bufio"
	"bytes"
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

const (
	total         = 300000
	stopAt        = 30000
	persisterWin  = 10000 // connector.DefaultPersisterBundleCountThreshold
	replayCredit  = 2000
	waitTimeout   = 120 * time.Second
	exitTimeout   = 20 * time.Second
	outputName    = "output.txt"
	inputName     = "input.txt"
	baseRefEnvVar = "CONDUIT_UPGRADE_BASE_REF"
)

type engine struct {
	name  string
	flags []string
}

var engines = []engine{
	{name: "classic"},
	{name: "arch-v2", flags: []string{"--preview.pipeline-arch-v2"}},
}

var (
	buildOnce      sync.Once
	oldBin, newBin string
	errBuild       error
	buildDir       string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

// binaries builds the base-ref binary and this tree's binary once per test run.
func binaries(t *testing.T) (oldB, newB string) {
	t.Helper()
	ref := os.Getenv(baseRefEnvVar)
	if ref == "" {
		t.Skipf("set %s to the base ref (tag, branch or commit) to run the resume gate", baseRefEnvVar)
	}
	buildOnce.Do(func() {
		root, err := filepath.Abs(filepath.Join("..", "..", ".."))
		if err != nil {
			errBuild = err
			return
		}
		buildDir, err = os.MkdirTemp("", "upgrade-resume-")
		if err != nil {
			errBuild = err
			return
		}
		run := func(dir string, name string, args ...string) error {
			cmd := exec.CommandContext(context.Background(), name, args...)
			cmd.Dir = dir
			if b, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, b)
			}
			return nil
		}
		baseTree := filepath.Join(buildDir, "base")
		if err := run(root, "git", "worktree", "add", "--detach", baseTree, ref); err != nil {
			errBuild = err
			return
		}
		defer func() { _ = run(root, "git", "worktree", "remove", "--force", baseTree) }()
		oldBin = filepath.Join(buildDir, "conduit-old")
		newBin = filepath.Join(buildDir, "conduit-new")
		if err := run(baseTree, "go", "build", "-o", oldBin, "./cmd/conduit"); err != nil {
			errBuild = err
			return
		}
		errBuild = run(root, "go", "build", "-o", newBin, "./cmd/conduit")
	})
	if errBuild != nil {
		t.Fatalf("build binaries: %v", errBuild)
	}
	return oldBin, newBin
}

type proc struct {
	cmd    *exec.Cmd
	exited chan struct{}
	err    error
	mu     sync.Mutex
	lines  []string
}

func (p *proc) output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.lines, "\n")
}

func start(t *testing.T, bin, dir string, eng engine) *proc {
	t.Helper()
	args := append([]string{
		"run",
		"--pipelines.path", filepath.Join(dir, "pipelines"),
		"--connectors.path", filepath.Join(dir, "connectors"),
		"--processors.path", filepath.Join(dir, "processors"),
		"--connectors.file.allowed-dirs", dir,
		"--db.type", "badger",
		"--db.badger.path", filepath.Join(dir, "db"),
		"--api.enabled=false",
		"--log.format", "json",
		"--log.level", "info",
	}, eng.flags...)
	cmd := exec.CommandContext(context.Background(), bin, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = w, w
	p := &proc{cmd: cmd, exited: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	_ = w.Close()
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			p.mu.Lock()
			p.lines = append(p.lines, sc.Text())
			p.mu.Unlock()
		}
	}()
	go func() {
		err := cmd.Wait()
		<-readerDone
		p.err = err
		close(p.exited)
	}()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-p.exited
		_ = r.Close()
	})
	return p
}

func (p *proc) sigkill(t *testing.T) {
	t.Helper()
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	select {
	case <-p.exited:
	case <-time.After(exitTimeout):
		t.Fatal("process did not die after SIGKILL")
	}
}

func (p *proc) sigterm(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.exited:
	case <-time.After(exitTimeout):
		t.Fatalf("no exit within %s of SIGTERM\n%s", exitTimeout, p.output())
	}
	if p.err != nil {
		t.Fatalf("exit after SIGTERM: %v\n%s", p.err, p.output())
	}
}

// batching returns the size and mode of the SDK's "source batching configured"
// line for the file source, or ok=false if the binary logs none (an SDK that
// predates default batching does not).
func (p *proc) batching() (size int, mode string, ok bool) {
	for _, line := range strings.Split(p.output(), "\n") {
		if !strings.Contains(line, "source batching configured") {
			continue
		}
		var l struct {
			Size int    `json:"size"`
			Mode string `json:"mode"`
			Name string `json:"plugin_name"`
		}
		if json.Unmarshal([]byte(line), &l) == nil && l.Name == "builtin:file" {
			return l.Size, l.Mode, true
		}
	}
	return 0, "", false
}

func layout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"pipelines", "connectors", "processors"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var in bytes.Buffer
	for i := 1; i <= total; i++ {
		fmt.Fprintf(&in, "%d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, inputName), in.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline := fmt.Sprintf(`version: "2.2"
pipelines:
  - id: resume
    status: running
    connectors:
      - id: src
        type: source
        plugin: builtin:file
        settings:
          path: %q
      - id: dst
        type: destination
        plugin: builtin:file
        settings:
          path: %q
`, filepath.Join(dir, inputName), filepath.Join(dir, outputName))
	if err := os.WriteFile(filepath.Join(dir, "pipelines", "p.yaml"), []byte(pipeline), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func lineCount(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, outputName))
	if err != nil {
		return 0
	}
	return bytes.Count(b, []byte{'\n'})
}

func waitLines(t *testing.T, p *proc, dir string, n int) {
	t.Helper()
	deadline := time.After(waitTimeout)
	for lineCount(dir) < n {
		select {
		case <-p.exited:
			t.Fatalf("exited (%v) before %d records\n%s", p.err, n, p.output())
		case <-deadline:
			t.Fatalf("fewer than %d records within %s\n%s", n, waitTimeout, p.output())
		case <-time.After(time.Millisecond):
		}
	}
}

func delivered(t *testing.T, dir string) map[int]int {
	t.Helper()
	counts := map[int]int{}
	f, err := os.Open(filepath.Join(dir, outputName))
	if err != nil {
		return counts
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var rec struct {
			Payload struct {
				After string `json:"after"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue // torn line from a kill
		}
		b, err := base64.StdEncoding.DecodeString(rec.Payload.After)
		if err != nil {
			continue
		}
		if n, err := strconv.Atoi(string(b)); err == nil {
			counts[n]++
		}
	}
	return counts
}

func dups(counts map[int]int) int {
	d := 0
	for _, c := range counts {
		d += c - 1
	}
	return d
}

// waitComplete waits until every input record was delivered, or the timeout
// passes (a lost record never arrives; the caller reports it).
func waitComplete(t *testing.T, p *proc, dir string) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for len(delivered(t, dir)) < total && time.Now().Before(deadline) {
		select {
		case <-p.exited:
			t.Fatalf("new binary exited: %v\n%s", p.err, p.output())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TestUpgradeResume_BatchedSource: positions written by the base binary
// (one record per read) are resumed by this tree's binary (batched reads).
func TestUpgradeResume_BatchedSource(t *testing.T) {
	oldB, newB := binaries(t)
	for _, eng := range engines {
		for _, kill := range []bool{false, true} {
			stop := "SIGTERM"
			if kill {
				stop = "SIGKILL"
			}
			t.Run(eng.name+"/"+stop, func(t *testing.T) {
				dir := layout(t)

				old := start(t, oldB, dir, eng)
				waitLines(t, old, dir, stopAt)
				if kill {
					old.sigkill(t)
				} else {
					old.sigterm(t)
				}
				if size, mode, ok := old.batching(); ok && size == 100 && mode == "adaptive" {
					t.Fatalf("the base binary already batches by default; pick an earlier %s", baseRefEnvVar)
				}
				atStop := delivered(t, dir)
				if len(atStop) >= total {
					t.Fatalf("the base binary delivered everything before the stop landed")
				}

				fresh := start(t, newB, dir, eng)
				defer fresh.sigterm(t)
				waitComplete(t, fresh, dir)
				size, mode, ok := fresh.batching()
				if !ok || size != 100 || mode != "adaptive" {
					t.Fatalf("the new binary is not running the default batching (ok=%v size=%d mode=%q)", ok, size, mode)
				}

				counts := delivered(t, dir)
				for i := 1; i <= total; i++ {
					if counts[i] == 0 {
						t.Fatalf("record %d was never delivered after the upgrade (%d of %d, %d at the stop): gap", i, len(counts), total, len(atStop))
					}
				}
				d := dups(counts)
				t.Logf("%s %s: %d delivered by the base binary, %d replayed after upgrade", eng.name, stop, len(atStop), d)
				if !kill && d != 0 {
					t.Fatalf("%d records delivered twice across a graceful stop and upgrade", d)
				}
				if kill && d > persisterWin+replayCredit {
					t.Fatalf("replayed %d records, over the bound %d", d, persisterWin+replayCredit)
				}
			})
		}
	}
}

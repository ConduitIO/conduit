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

package chaos

import (
	"bufio"
	"bytes"
	"context"
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
)

// childConfig configures one child-process run. dbDir and upstreamDir are
// reused verbatim across a kill+restart pair - that persistence, on disk,
// across two separate OS processes, is the entire point of this harness: it
// is what makes "restart and check for a gap" mean something.
type childConfig struct {
	dbDir       string
	upstreamDir string
	prune       bool
	paceMS      int
	total       uint64
}

func (c childConfig) env() []string {
	return []string{
		envChild + "=1",
		envDBDir + "=" + c.dbDir,
		envUpstreamDir + "=" + c.upstreamDir,
		envPrune + "=" + strconv.FormatBool(c.prune),
		envPaceMS + "=" + strconv.Itoa(c.paceMS),
		envTotal + "=" + strconv.FormatUint(c.total, 10),
	}
}

// childProcess wraps a running (or exited) chaos child and its observed
// stdout, so a test can wait for a specific number of ACK progress lines
// before deciding when to SIGKILL - deterministic relative to the child's
// own observed progress, not a blind wall-clock guess.
type childProcess struct {
	cmd *exec.Cmd

	mu     sync.Mutex
	lines  []string
	stderr bytes.Buffer

	readerDone chan struct{}

	// reapOnce guards cmd.Wait(), which the os/exec docs require be called at
	// most once. sigkill, waitExit and the spawnChild-registered t.Cleanup
	// fallback can all end up trying to reap the same process (e.g. an
	// assertion failing between spawnChild and the test's own sigkill/
	// waitExit call would otherwise leak the process); routing all of them
	// through reap() makes that safe regardless of which one gets there
	// first.
	reapOnce sync.Once
	waitErr  error
}

// reap calls cmd.Wait() exactly once (idempotent - see reapOnce's doc
// comment) and returns its result on every call.
func (c *childProcess) reap() error {
	c.reapOnce.Do(func() {
		c.waitErr = c.cmd.Wait()
	})
	return c.waitErr
}

// spawnChild re-executes the current test binary (os.Args[0]) with
// CONDUIT_CHAOS_CHILD=1, which - per TestMain in sigkill_test.go - makes it
// behave as runChild (child.go) instead of running this package's actual Go
// tests. This is the standard "re-exec the test binary as a helper process"
// pattern.
func spawnChild(t *testing.T, cfg childConfig) *childProcess {
	t.Helper()

	exe := os.Args[0]
	if !filepath.IsAbs(exe) {
		resolved, err := os.Executable()
		if err != nil {
			t.Fatalf("resolve test binary path: %v", err)
		}
		exe = resolved
	}

	// CommandContext with context.Background() (rather than exec.Command) is
	// used only to satisfy the noctx linter - the child's lifecycle is
	// controlled explicitly via sigkill/waitExit below, not via context
	// cancellation.
	//nolint:gosec // exe is this test binary's own path (os.Args[0]/os.Executable), not external input - the standard re-exec-self pattern
	cmd := exec.CommandContext(context.Background(), exe)
	cmd.Env = append(os.Environ(), cfg.env()...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}

	cp := &childProcess{cmd: cmd, readerDone: make(chan struct{})}
	cmd.Stderr = &cp.stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}

	// Fallback safety net: if the test function returns early (e.g. a failed
	// assertion between spawnChild and the test's own sigkill/waitExit call)
	// without explicitly reaping this child, don't leave a live or zombie
	// process behind. Harmless (and a no-op beyond the Kill call) if the test
	// already reaped it - see reap()'s doc comment.
	t.Cleanup(func() {
		if cp.cmd.Process != nil {
			_ = cp.cmd.Process.Kill()
		}
		_ = cp.reap()
	})

	go func() {
		defer close(cp.readerDone)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			cp.mu.Lock()
			cp.lines = append(cp.lines, line)
			cp.mu.Unlock()
		}
	}()

	return cp
}

func (c *childProcess) linesSnapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
}

// ackCount returns how many distinct "ACK <n>" progress lines have been
// observed so far - i.e. how many positions the chaosPlugin has durably
// committed upstream.
func (c *childProcess) ackCount() int {
	n := 0
	for _, l := range c.linesSnapshot() {
		if strings.HasPrefix(l, "ACK ") {
			n++
		}
	}
	return n
}

// line returns the first observed line with the given prefix, and whether
// one was found.
func (c *childProcess) line(prefix string) (string, bool) {
	for _, l := range c.linesSnapshot() {
		if strings.HasPrefix(l, prefix) {
			return l, true
		}
	}
	return "", false
}

func (c *childProcess) diagnostics() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return fmt.Sprintf("stdout lines: %v\nstderr: %s", c.lines, c.stderr.String())
}

// waitForAckCount blocks (polling, not sleeping a fixed duration) until at
// least n ACK lines have been observed, or fails the test after a generous
// timeout. Because the child's own pacing (chaosPlugin.paceMS) enforces a
// MINIMUM real delay between acks via time.Sleep, waiting for n acks always
// means at least n*paceMS of wall-clock time has genuinely elapsed - slower
// CI scheduling can only add delay, never let this return early. That is
// what makes waitForAckCount a safe way to guarantee "at least this much
// time has passed since the first ack" without a fragile fixed sleep.
func (c *childProcess) waitForAckCount(t *testing.T, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.ackCount() >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d acks (saw %d)\n%s", n, c.ackCount(), c.diagnostics())
}

// sigkill sends SIGKILL (not SIGTERM, not context cancellation) and reaps
// the process. This is the actual chaos: no cleanup, no graceful shutdown,
// no final flush - exactly what CLAUDE.md's chaos-testing standard requires
// ("SIGKILL (not SIGTERM)").
func (c *childProcess) sigkill(t *testing.T) {
	t.Helper()
	if err := c.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL child (pid %d): %v", c.cmd.Process.Pid, err)
	}
	_ = c.reap() // a "signal: killed" wait error is expected here, not a failure
	<-c.readerDone
}

// waitExit blocks until the child exits on its own (the "let it run to
// completion" restart run), failing the test if it doesn't within timeout or
// exits with an unexpected non-zero code.
func (c *childProcess) waitExit(t *testing.T, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_ = c.reap()
		close(done)
	}()

	select {
	case <-done:
		<-c.readerDone
		if c.waitErr != nil {
			t.Fatalf("child exited unexpectedly: %v\n%s", c.waitErr, c.diagnostics())
		}
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for child to exit\n%s", c.diagnostics())
	}
}

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

	// persistDelayMS overrides connector.DefaultPersisterDelayThreshold in the
	// child. Zero means "use the default".
	//
	// This exists to make a case's PRECONDITION deterministic instead of
	// wall-clock-inferred. The mid-snapshot case needs the kill to land before
	// Conduit has persisted ANY position; it used to get that by arithmetic
	// ("30 reads x 1ms = ~30ms, well under the 1s flush"), which silently stops
	// being true on a loaded CI box where those 30 reads can take longer than a
	// second. The flush then fires, the premise the assertion rests on is void,
	// and the test fails for a reason that has nothing to do with the invariant
	// under test. Setting the threshold far beyond any plausible scheduling
	// delay removes the race rather than papering over it - the assertion is
	// unchanged and just as strict. See sigkillCases.
	persistDelayMS int

	// snapshotK/snapshotPaceMS: DBZ-2 Property 1/2's two-phase producer
	// knobs (see chaosPlugin's type doc, upstream.go). Zero values preserve
	// DBZ-1's original single-phase behavior.
	snapshotK      uint64
	snapshotPaceMS int

	// numKeys: DBZ-2 Property 3's multi-key knob (see chaosPlugin's type
	// doc, upstream.go). Zero/one preserves DBZ-1's original,
	// single-position-space behavior.
	numKeys int

	// driftAt: Property 4's synthetic drift/poison-marker knob (see
	// chaosPlugin.driftAt's field doc, upstream.go). Zero preserves every
	// existing scenario's behavior unchanged - no record in the run carries
	// the marker. Property 4 itself (property4_test.go) drives this
	// in-process via buildChild directly, not through this cross-process
	// config; this field exists so the knob is available end-to-end for
	// symmetry/parity with the rest of childConfig, and so a future
	// cross-process drift scenario doesn't need new plumbing.
	driftAt uint64

	// sigtermMode: DBZ-2's SIGTERM/invariant-7 knob (see runChildSigterm,
	// child.go, and sigterm_test.go). false preserves every existing
	// scenario's behavior unchanged - the child runs runChild, not
	// runChildSigterm.
	sigtermMode bool
}

func (c childConfig) env() []string {
	return []string{
		envChild + "=1",
		envDBDir + "=" + c.dbDir,
		envUpstreamDir + "=" + c.upstreamDir,
		envPrune + "=" + strconv.FormatBool(c.prune),
		envPaceMS + "=" + strconv.Itoa(c.paceMS),
		envTotal + "=" + strconv.FormatUint(c.total, 10),
		envSnapshotK + "=" + strconv.FormatUint(c.snapshotK, 10),
		envSnapshotPaceMS + "=" + strconv.Itoa(c.snapshotPaceMS),
		envNumKeys + "=" + strconv.Itoa(c.numKeys),
		envDriftAt + "=" + strconv.FormatUint(c.driftAt, 10),
		envSigtermMode + "=" + strconv.FormatBool(c.sigtermMode),
		envPersistDelayMS + "=" + strconv.Itoa(c.persistDelayMS),
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
	stderr syncBuffer

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
	return spawnChildWithEnv(t, cfg.env())
}

// spawnChildWithEnv is spawnChild's env-agnostic core: it re-executes the
// current test binary with the given extra environment variables appended,
// and is the actual re-exec-self-as-child-process mechanism every chaos
// scenario in this package uses to get a real, SIGKILL-able OS process (see
// spawnChild's doc for why this - not an in-process approximation - is what
// makes "kill and check for a gap" mean something for a hard crash). Factored
// out of spawnChild so a scenario whose child needs a different
// environment-variable protocol than childConfig.env() (e.g. recovery_test.go's
// pkg/lifecycle-poc.Service scenario, driven by lcChildConfig.env()) can reuse
// the identical process-spawning/stdout-capture/cleanup machinery without
// forcing every scenario's config through one shared env-var struct.
func spawnChildWithEnv(t *testing.T, env []string) *childProcess {
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
	cmd.Env = append(os.Environ(), env...)

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

// readCount returns how many distinct "READ <pos>" progress lines have been
// observed so far - i.e. how many records chaosPlugin.produceLoop has sent
// down the stream. Used for kill-timing (waitForReadCount): unlike ACK
// progress, READ progress is paced directly by chaosPlugin.paceMS and is
// unaffected by connector.Persister's debounce, so it remains a reliable
// proxy for "at least N*paceMS of wall-clock time has elapsed" under
// Approach A (see produceLoop's doc comment in upstream.go).
func (c *childProcess) readCount() int {
	return c.progressCount("READ ")
}

func (c *childProcess) progressCount(prefix string) int {
	n := 0
	for _, l := range c.linesSnapshot() {
		if strings.HasPrefix(l, prefix) {
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

// linesWithPrefix returns every observed line with the given prefix, in the
// order they were emitted by the child (i.e. arrival/delivery order — see
// Property 3's ordering_test.go, which relies on this order to reconstruct
// the per-key ack delivery ledger from ACK_ORDER lines).
func (c *childProcess) linesWithPrefix(prefix string) []string {
	var out []string
	for _, l := range c.linesSnapshot() {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

func (c *childProcess) diagnostics() string {
	// stderr is its own syncBuffer (self-synchronizing, see its doc), not
	// guarded by c.mu - c.mu only ever protected lines. Reading it while the
	// child is still alive (e.g. from waitExit's timeout branch) races
	// against os/exec's internal io.Copy goroutine, which keeps writing to
	// it for as long as the child process keeps producing stderr output.
	return fmt.Sprintf("stdout lines: %v\nstderr: %s", c.linesSnapshot(), c.stderr.String())
}

// syncBuffer is a bytes.Buffer safe for concurrent Write (from os/exec's
// internal stderr-copying goroutine, for as long as the child process is
// alive) and String (from a test goroutine building a diagnostics message,
// e.g. on a waitExit timeout while the child - and that copy goroutine - are
// still running). A plain bytes.Buffer is not safe for this: os/exec.Cmd.Wait
// only guarantees the copy goroutine has stopped once the process has
// exited, which is exactly the case a timeout means never happened.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitForReadCount blocks (polling, not sleeping a fixed duration) until at
// least n READ lines have been observed, or fails the test after a generous
// timeout. Because the child's own pacing (chaosPlugin.paceMS) enforces a
// MINIMUM real delay between reads via time.Sleep, waiting for n reads always
// means at least n*paceMS of wall-clock time has genuinely elapsed - slower
// CI scheduling can only add delay, never let this return early. That is
// what makes this a safe way to guarantee "at least this much time has
// passed since the first read" without a fragile fixed sleep. Unlike ack
// progress, read progress stays reliable for this purpose even under
// Approach A's deferred-ack timing (docs/design-documents/
// 20260723-source-ack-persist-ordering-fix.md), which is why
// sigkill_test.go's kill-timing uses this signal, not ack progress.
func (c *childProcess) waitForReadCount(t *testing.T, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.readCount() >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d reads (saw %d)\n%s", n, c.readCount(), c.diagnostics())
}

// waitForMarker blocks (polling, not sleeping a fixed duration) until a line
// with the given prefix has been observed, or fails the test after timeout.
// Used by Property 2's mid-handoff case to gate the kill on the HANDOFF
// marker itself rather than a read-count that merely happens to be close to
// it: HANDOFF is printed synchronously by chaosPlugin.produceLoop the
// instant the producer crosses the snapshot->stream boundary (upstream.go),
// so waiting on it is exactly as race-free as waitForReadCount - it is
// gated on genuine, paced producer progress, never a wall-clock guess.
func (c *childProcess) waitForMarker(t *testing.T, prefix string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, ok := c.line(prefix); ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for marker %q\n%s", prefix, c.diagnostics())
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

// sigterm sends SIGTERM (not SIGKILL) and waits (via waitExit) for the child
// to exit gracefully on its own within timeout. Used by the SIGTERM/
// invariant-7 case (sigterm_test.go): unlike sigkill (which expects and
// tolerates a "signal: killed" wait error - see its doc comment), this
// expects the child to catch SIGTERM, run Source.Teardown's
// flush-and-wait-then-stopStream ordering (source.go:249-326, runChildSigterm
// in child.go), and exit cleanly with code 0 - a graceful shutdown, not a
// crash. waitExit already fails the test on a nonzero exit code or a timeout,
// so a child that doesn't handle SIGTERM cleanly fails loudly here.
func (c *childProcess) sigterm(t *testing.T, timeout time.Duration) {
	t.Helper()
	if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM child (pid %d): %v", c.cmd.Process.Pid, err)
	}
	c.waitExit(t, timeout)
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

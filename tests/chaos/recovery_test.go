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
	"strings"
	"testing"
	"time"

	"github.com/matryer/is"
)

// This file is PR-3 of the arch-v2 recovery epic: a chaos/fault-injection
// test proving that pkg/lifecycle-poc/service.go's error-recovery loop
// (recoverPipeline / StartWithBackoff / runPipeline's recovery arm, added in
// PR-2, feat/archv2-recovery-port) upholds invariants 1 and 3 across a hard
// crash, not just across an in-process transient error.
// pkg/lifecycle-poc/service_test.go's TestServiceLifecycle_Recovery_* tests
// already prove the recovery loop works when the process itself keeps
// running; what those tests structurally cannot prove is what happens if the
// whole Conduit process is killed WHILE the recovery loop is in flight - the
// two windows this file targets:
//
//  1. Parked in the recovery backoff wait (StartWithBackoff's
//     `time.After(duration)` select, service.go) - a crash here means no
//     restart was ever attempted by the killed process; a brand new process
//     must still resume correctly from whatever was durably persisted.
//  2. Mid the recovered run - a crash after the pipeline has already
//     transitioned Recovering -> Running again and resumed producing
//     records, proving the SECOND run's ack/persist/deliver chain is just as
//     crash-safe as the first.
//
// # Crash-injection mechanism, and exactly what it does and doesn't prove
//
// Both scenarios spawn a REAL, separate OS process (recovery_child.go's
// runChildLifecycle, via spawnChildWithEnv - the identical re-exec-self
// mechanism sigkill_test.go already uses) that builds a REAL
// pkg/lifecycle-poc.Service around a real funnel.Worker, and kill it with an
// actual SIGKILL (childProcess.sigkill) once its stdout markers show it has
// reached the targeted window. This is a genuine, no-cleanup-possible hard
// crash of the process running the recovery loop - not an in-process
// approximation (an in-process test could only ever crash by voluntarily
// stopping cooperating goroutines, which is not what a `kill -9` does: it
// gives the recovery loop's own code zero further instructions, mid
// whatever it was doing, which is exactly what SIGKILL against a real
// process provides and an in-process shortcut cannot).
//
// What this DOES prove: pkg/lifecycle-poc.Service's recovery orchestration,
// layered on top of pkg/connector.Source/Persister's already-proven
// ack-before-persist ordering (sigkill_test.go), does not itself introduce a
// new invariant-1/3 violation when killed mid-recovery - specifically, that
// Worker.Close/Source.Teardown never ran (this is a hard kill, not a
// graceful stop) and yet a freshly-restarted Service, pointed at the same
// on-disk state, resumes without dropping any record the old process had
// already durably delivered downstream.
//
// What this does NOT prove on its own: this package's badger/persister
// crash-safety claims - those are TestSIGKILL_PruningUpstream_NoGap and
// TestSIGKILL_DurableUpstream_NoGap's job, exercised directly against
// pkg/connector.Source, and this test deliberately reuses that same durable
// on-disk boundary (buildLifecycleChild opens the identical kind of badger
// DB via buildChild's own loadOrCreateInstance/openUpstreamStore helpers)
// rather than re-litigating it. This test also does not exercise a REAL
// out-of-process connector plugin (gRPC/WASM) crashing independently of
// Conduit - recoverySourcePlugin/recoveryDestinationPlugin are in-process
// synthetic plugins, chosen (like chaosPlugin elsewhere in this package) so
// the test can inject a precisely-timed transient error and a durable,
// independently-inspectable delivery ledger without a real external system.
//
// # Invariant assertions
//
// Both scenarios assert, via assertRecoveryInvariants:
//   - Invariant 1 (never ack upstream before durable downstream): at the
//     exact instant of the kill, every position the source's upstream ledger
//     (upstreamStore) has committed is already present in the destination's
//     durable delivery ledger (deliveryLog) - this can only hold if
//     recoveryDestinationPlugin.loop's durable Record-before-ack ordering
//     (see its doc comment) was never bypassed by a Worker/Source race during
//     the crash.
//   - Invariant 3 (at-least-once, no gap): after the post-crash restart runs
//     to completion, the upstream ledger has committed every position
//     1..total exactly (no drop), and the destination's delivery ledger
//     covers every position 1..total at least once (duplicates - a record
//     redelivered across the crash boundary - are expected and tolerated;
//     only a genuine gap fails the test).
func spawnLCChild(t *testing.T, cfg lcChildConfig) *childProcess {
	t.Helper()
	return spawnChildWithEnv(t, cfg.env())
}

// waitForLCRecovered blocks (polling this child process's own stdout lines,
// not a fixed sleep) until a "LC_STATUS Recovering" line has been observed
// AND a LATER "LC_STATUS Running" line has also been observed - i.e. the
// pipeline actually completed a recovery restart, not just entered
// Recovering. Mirrors pkg/lifecycle-poc/service_test.go's waitForRecovered,
// adapted to read a child process's captured stdout lines instead of an
// in-process statusRecorder.
func waitForLCRecovered(t *testing.T, cp *childProcess, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		lines := cp.linesWithPrefix(markerLCStatus)
		seenRecovering := false
		for _, l := range lines {
			status := strings.TrimSpace(strings.TrimPrefix(l, markerLCStatus+" "))
			switch {
			case status == "Recovering":
				seenRecovering = true
			case status == "Running" && seenRecovering:
				return // Running after a Recovering == a completed recovery restart
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the pipeline to recover (LC_STATUS lines: %v)\n%s", lines, cp.diagnostics())
		}
		time.Sleep(time.Millisecond)
	}
}

// assertNoGapThrough fails the test unless every position 1..upTo appears in
// positions at least once. Duplicates and positions beyond upTo are fine -
// this is invariant 3's "no drop" check, not an exact-set-equality check.
func assertNoGapThrough(t *testing.T, positions []uint64, upTo uint64, label string) {
	t.Helper()
	have := make(map[uint64]bool, len(positions))
	for _, p := range positions {
		have[p] = true
	}
	var missing []uint64
	for i := uint64(1); i <= upTo; i++ {
		if !have[i] {
			missing = append(missing, i)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%s: missing position(s) (a GAP, not a tolerated duplicate) through %d: %v", label, upTo, missing)
	}
}

// assertRecoveryInvariantsAtKill reads the upstream commit ledger and the
// main destination's delivery ledger fresh from disk immediately after a
// SIGKILL, and asserts invariant 1: every position already committed
// upstream at the moment of the kill was already durably delivered
// downstream. It returns the observed committed watermark so the caller can
// fold it into diagnostics.
func assertRecoveryInvariantsAtKill(t *testing.T, cfg lcChildConfig) uint64 {
	t.Helper()
	is := is.New(t)

	upstream, err := openUpstreamStore(cfg.upstreamDir, false)
	is.NoErr(err)
	committedAtKill, err := upstream.Committed()
	is.NoErr(err)

	mainLog, err := openDeliveryLog(cfg.mainDir)
	is.NoErr(err)
	delivered, err := mainLog.Positions()
	is.NoErr(err)

	assertNoGapThrough(
		t, delivered, committedAtKill,
		"invariant 1: upstream committed a position the destination never durably received",
	)
	return committedAtKill
}

// assertRecoveryInvariantsAfterRestart reads both ledgers fresh from disk
// after the post-crash restart child has run to completion, and asserts
// invariant 3: the upstream ledger reaches exactly cfg.total (no drop, and
// the run genuinely finished), and the destination's delivery ledger covers
// every position 1..cfg.total (duplicates tolerated, gaps are not).
func assertRecoveryInvariantsAfterRestart(t *testing.T, cfg lcChildConfig) {
	t.Helper()
	is := is.New(t)

	upstream, err := openUpstreamStore(cfg.upstreamDir, false)
	is.NoErr(err)
	committed, err := upstream.Committed()
	is.NoErr(err)
	is.Equal(committed, cfg.total) // invariant 3: at-least-once delivery completed through `total` despite the kill

	mainLog, err := openDeliveryLog(cfg.mainDir)
	is.NoErr(err)
	delivered, err := mainLog.Positions()
	is.NoErr(err)
	assertNoGapThrough(t, delivered, cfg.total, "invariant 3: a position was never durably delivered downstream after the restart")
}

// TestSIGKILL_RecoveryLoop_CrashDuringBackoff drives PR-3's first crash
// window: a transient source error (at position failAt) drives the pipeline
// into pkg/lifecycle-poc.Service's recovery loop, and the child process is
// SIGKILLed the instant it reports StatusRecovering - i.e. while it is
// parked in StartWithBackoff's backoff wait, before any restart was ever
// attempted. A long backoff (minDelayMS/maxDelayMS) makes this window wide
// enough to hit reliably without a kill-hook: even generous CI scheduling
// jitter between the Recovering marker being printed and this test's
// following SIGKILL call (at most a few goroutine-scheduling instructions'
// worth, see waitForLCRecovered's doc) is many orders of magnitude smaller
// than the multi-second backoff.
func TestSIGKILL_RecoveryLoop_CrashDuringBackoff(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	cfg := lcChildConfig{
		dbDir:       dir + "/db",
		upstreamDir: dir + "/upstream",
		mainDir:     dir + "/main-delivered",
		dlqDir:      dir + "/dlq-delivered",
		total:       20,
		paceMS:      2,
		failAt:      5, // positions 1-4 are produced/acked/committed before the injected transient error
		minDelayMS:  4000,
		maxDelayMS:  4000,
		graceful:    false,
	}

	first := spawnLCChild(t, cfg)
	first.waitForMarker(t, markerLCStatus+" Recovering", 10*time.Second)
	first.sigkill(t)

	committedAtKill := assertRecoveryInvariantsAtKill(t, cfg)
	// Sanity: the injected failure actually fired before this scenario's
	// crash window (otherwise this test would trivially pass without ever
	// exercising the backoff-wait crash it claims to). committedAtKill can be
	// at most failAt-1 (the last position produced before the induced
	// failure) - a higher watermark would mean the crash landed somewhere
	// else entirely.
	is.True(committedAtKill <= cfg.failAt-1)

	restart := lcChildConfig{
		dbDir: cfg.dbDir, upstreamDir: cfg.upstreamDir, mainDir: cfg.mainDir, dlqDir: cfg.dlqDir,
		total: cfg.total, paceMS: cfg.paceMS, failAt: 0, // no further induced failure - let it finish
		minDelayMS: 10, maxDelayMS: 10, graceful: true,
	}
	second := spawnLCChild(t, restart)
	second.waitExit(t, 30*time.Second)
	_, done := second.line(markerLCDone)
	is.True(done)

	assertRecoveryInvariantsAfterRestart(t, cfg)
}

// TestSIGKILL_RecoveryLoop_CrashDuringRecoveredRun drives PR-3's second crash
// window: the same transient-error-triggered recovery as
// TestSIGKILL_RecoveryLoop_CrashDuringBackoff, but with a short backoff so
// the pipeline actually completes the restart (Recovering -> Running again)
// and resumes producing records - and the child is SIGKILLed WHILE that
// second, recovered run is mid-flight, not while parked in the backoff wait.
// waitForLCRecovered gates on the observed Recovering->Running transition
// (not a guess), and waitForReadCount then gates the kill on genuine
// production progress past the point the first run ever reached (see the
// comment at its call site) - both are polling waits on the child's own
// progress, never a blind wall-clock sleep.
func TestSIGKILL_RecoveryLoop_CrashDuringRecoveredRun(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	cfg := lcChildConfig{
		dbDir:       dir + "/db",
		upstreamDir: dir + "/upstream",
		mainDir:     dir + "/main-delivered",
		dlqDir:      dir + "/dlq-delivered",
		total:       60,
		paceMS:      3,
		failAt:      5, // fail fast so the recovered run gets most of the budget
		minDelayMS:  20,
		maxDelayMS:  20,
		graceful:    false,
	}

	first := spawnLCChild(t, cfg)
	waitForLCRecovered(t, first, 10*time.Second)
	// The first run could only ever have produced positions 1..failAt-1 (4)
	// before its induced failure - waiting for 30 total READ lines (READ
	// progress is cumulative across both dispenses within this one process,
	// see recoverySourcePlugin.produceLoop) proves we are deep into the
	// SECOND (recovered) run's production, comfortably past both the failure
	// point and the restart itself, and well short of cfg.total (60) so the
	// kill still lands mid-run rather than at the natural end.
	const killAfterReads = 30
	first.waitForReadCount(t, killAfterReads, 10*time.Second)
	first.sigkill(t)

	committedAtKill := assertRecoveryInvariantsAtKill(t, cfg)
	// Sanity: the kill landed after the recovery actually happened (more
	// progress committed than the first run alone could ever have reached)
	// and before the natural end (otherwise this wouldn't be testing a
	// mid-recovered-run crash at all).
	is.True(committedAtKill > cfg.failAt-1)
	is.True(committedAtKill < cfg.total)

	restart := lcChildConfig{
		dbDir: cfg.dbDir, upstreamDir: cfg.upstreamDir, mainDir: cfg.mainDir, dlqDir: cfg.dlqDir,
		total: cfg.total, paceMS: cfg.paceMS, failAt: 0,
		minDelayMS: 10, maxDelayMS: 10, graceful: true,
	}
	second := spawnLCChild(t, restart)
	second.waitExit(t, 30*time.Second)
	_, done := second.line(markerLCDone)
	is.True(done)

	assertRecoveryInvariantsAfterRestart(t, cfg)
}

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
	"strconv"
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

// waitForUpstreamCommittedAtLeast blocks (polling the child's durable
// upstream commit marker on disk, not sleeping a fixed duration) until that
// watermark has reached at least want, then returns it.
//
// This reads the very same file assertRecoveryInvariantsAtKill reads back
// after the kill, while the child is still running. That is safe and
// race-free by construction: upstreamStore.Commit writes to a temp file,
// fsyncs, and atomically renames it into place (upstream.go), so a concurrent
// reader in another process only ever observes a complete previous or
// complete new value - never a torn one.
//
// Why gate a kill on THIS rather than on childProcess.waitForReadCount: read
// progress is a LAGGING, parent-side observation of a child that keeps
// running while the parent is descheduled, so "the child had produced n
// records when I last looked" says nothing about where the child is now. The
// committed watermark is a durable fact, and - crucially - it is paired with
// lcChildConfig.holdAt, which caps how far the child can ever get. Lower
// bound observed, upper bound structural: neither depends on the parent being
// scheduled promptly.
func waitForUpstreamCommittedAtLeast(t *testing.T, cp *childProcess, upstreamDir string, want uint64, timeout time.Duration) uint64 {
	t.Helper()
	upstream, err := openUpstreamStore(upstreamDir, false)
	if err != nil {
		t.Fatalf("open upstream store %s: %v", upstreamDir, err)
	}
	deadline := time.Now().Add(timeout)
	var last uint64
	for time.Now().Before(deadline) {
		got, err := upstream.Committed()
		if err != nil {
			t.Fatalf("read upstream commit marker: %v\n%s", err, cp.diagnostics())
		}
		if got >= want {
			return got
		}
		last = got
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for the upstream commit watermark to reach %d (saw %d)\n%s", want, last, cp.diagnostics())
	return 0
}

// maxProgressPosition returns the highest position across every observed
// progress line with the given tag ("READ", markerLCHeld, ...). Positions,
// not line counts: a restart within one process re-produces positions it
// already produced once (see waitForLCRecovered), so counting lines and
// reading a position off the count are not the same thing - which is
// precisely the confusion the old read-count kill gate encoded.
func maxProgressPosition(cp *childProcess, tag string) uint64 {
	var highest uint64
	for _, l := range cp.linesWithPrefix(tag + " ") {
		n, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(l, tag+" ")), 10, 64)
		if err != nil {
			continue
		}
		if n > highest {
			highest = n
		}
	}
	return highest
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
	second.waitExit(t, parentWaitExit)
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
//
// # Why the kill point is bounded on both sides, and neither bound is a race
//
// The two sanity assertions below are the test's own vacuity guards: they
// refuse to let it pass unless the kill genuinely landed after the recovery
// and before the run's natural end. Both of those preconditions are
// established by construction here, not won by out-running the child:
//
//   - Lower bound (committedAtKill > failAt-1): the kill is gated on the
//     child's DURABLE upstream commit watermark reaching killAfterCommitted,
//     read off disk (waitForUpstreamCommittedAtLeast). Every position at or
//     past failAt is one the first run provably never produced - produceLoop
//     closes the stream instead of sending failAt - so a committed watermark
//     that has passed it is positive proof the recovered run is the one
//     making progress. It is a durable fact at the instant it is read, not an
//     inference from a parent-side observation that may already be stale.
//
//   - Upper bound (committedAtKill < total): cfg.holdAt caps this child's
//     producer at position 20, well below cfg.total (60). Nothing past 20 is
//     ever produced by this process, so nothing past 20 can ever be acked or
//     committed by it, no matter how long the parent is descheduled between
//     deciding to kill and SIGKILL actually landing.
//
// That second bound is the fix for #2836. This test used to gate its kill on
// childProcess.waitForReadCount(30) against an uncapped child free-running to
// total: the child kept producing during the (unbounded) window between the
// parent observing the 30th READ line and the signal landing, so on a loaded
// runner it could reach total before it died and the "committedAtKill <
// total" guard would - correctly - refuse to pass a test that had not
// actually crashed anything mid-run. A ~400ms stall in that window is enough
// to reproduce it; see TestRecoveryChild_HoldAt_CapsProductionBelowTotal,
// which pins the cap against exactly that stall. Enlarging total would only
// have widened the window that has to be lost, not closed it.
//
// Records really are in flight at the kill: the cap is a ceiling, not the
// trigger. The kill fires the moment the watermark reaches
// killAfterCommitted (10) while the producer is still on its way to holdAt
// (20), so records are typically produced-but-not-yet-committed at the
// instant of the SIGKILL - which is what gives
// assertRecoveryInvariantsAtKill's invariant-1 check something to bite on,
// over a committed range (1..10+) deep enough to be worth checking.
//
// Note what killAfterCommitted does and does not control. Any value strictly
// inside (failAt-1, holdAt] gives the identical verdict: the gate cannot
// return before it, and the child cannot pass holdAt, so both guards hold for
// every one of them. It tunes how deep the in-flight window is, never whether
// the test passes - which is exactly the difference between bounding a test
// and tuning one. (If a badly-starved runner drains all 20 before the parent
// gets to the kill, the assertions still hold: 40 records were still never
// produced, so the crash is still mid-run.)
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
		failAt:      5,  // fail fast so the recovered run gets most of the budget
		holdAt:      20, // hard ceiling on this process's production; see the doc comment above
		minDelayMS:  20,
		maxDelayMS:  20,
		graceful:    false,
	}

	// killAfterCommitted brackets the kill point together with cfg.holdAt:
	// the watermark at the kill is always in [10, 20], strictly inside
	// (failAt-1, total). See the doc comment above for why every value in
	// that interval yields the same verdict.
	const killAfterCommitted = 10

	first := spawnLCChild(t, cfg)
	waitForLCRecovered(t, first, 10*time.Second)
	// Gate the kill on durable, recovered-run progress: once the watermark is
	// past failAt, the recovered run has provably produced, delivered and
	// acked records the first run never even sent. See
	// waitForUpstreamCommittedAtLeast for why this signal, and not a
	// parent-side read count, is the sound one to kill on. This wait can
	// never overshoot, because cfg.holdAt caps the child at 20.
	waitForUpstreamCommittedAtLeast(t, first, cfg.upstreamDir, killAfterCommitted, 10*time.Second)
	first.sigkill(t)

	committedAtKill := assertRecoveryInvariantsAtKill(t, cfg)
	// Sanity: the kill landed after the recovery actually happened (more
	// progress committed than the first run alone could ever have reached)
	// and before the natural end (otherwise this wouldn't be testing a
	// mid-recovered-run crash at all). Both hold by construction - see the
	// doc comment above.
	is.True(committedAtKill > cfg.failAt-1)
	is.True(committedAtKill < cfg.total)
	is.True(committedAtKill >= killAfterCommitted) // the gate's own guarantee, restated against the ledger
	// The stronger, structural form of the guard above: the producer ceiling,
	// not the margin to total, is what makes it impossible to reach total.
	// Asserted separately so that if someone removes holdAt, this fails
	// immediately and deterministically rather than flaking back to life.
	is.True(committedAtKill <= cfg.holdAt)

	restart := lcChildConfig{
		dbDir: cfg.dbDir, upstreamDir: cfg.upstreamDir, mainDir: cfg.mainDir, dlqDir: cfg.dlqDir,
		total: cfg.total, paceMS: cfg.paceMS, failAt: 0,
		minDelayMS: 10, maxDelayMS: 10, graceful: true,
	}
	second := spawnLCChild(t, restart)
	second.waitExit(t, parentWaitExit)
	_, done := second.line(markerLCDone)
	is.True(done)

	assertRecoveryInvariantsAfterRestart(t, cfg)
}

// recoveryHoldStall is how long TestRecoveryChild_HoldAt_CapsProductionBelowTotal
// deliberately does nothing after the producer reports it has hit its ceiling.
//
// This is not a "wait long enough and hope" sleep - it is the opposite, and
// the distinction matters because this package forbids the former. An
// unsound kill gate is one whose precondition decays as the parent is
// descheduled for longer; this sleep IS that descheduling, injected on
// purpose, and the assertions after it must hold no matter how large it
// gets. Making it larger can only make an unsound cap fail harder. It is
// sized at 400ms because that is empirically enough for the uncapped
// producer this test guards against (60 records at paceMS 3, plus a 10ms
// persister debounce) to run all the way to total - i.e. enough to reproduce
// #2836's original failure, and the value the flake was diagnosed with.
const recoveryHoldStall = 400 * time.Millisecond

// TestRecoveryChild_HoldAt_CapsProductionBelowTotal is #2836's regression
// test: it pins the production ceiling that
// TestSIGKILL_RecoveryLoop_CrashDuringRecoveredRun's "committedAtKill <
// total" guard now rests on.
//
// Before the ceiling existed, that test raced its own child: it observed a
// read count and then SIGKILLed, and the child kept producing throughout the
// window in between. This test reproduces that window directly and
// adversarially - it waits for the producer to report its ceiling, then
// stalls for recoveryHoldStall (long enough that a ceiling-less child would
// have finished all 60 records several times over, which is exactly how the
// original flake was reproduced) - and asserts the child has not moved.
//
// Run against a child without the holdAt cap, the maxRead assertion below
// fails outright: production would be at total (60), not the ceiling (20).
func TestRecoveryChild_HoldAt_CapsProductionBelowTotal(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	cfg := lcChildConfig{
		dbDir:       dir + "/db",
		upstreamDir: dir + "/upstream",
		mainDir:     dir + "/main-delivered",
		dlqDir:      dir + "/dlq-delivered",
		total:       60,
		paceMS:      3,
		failAt:      5,
		holdAt:      20,
		minDelayMS:  20,
		maxDelayMS:  20,
		graceful:    false,
	}
	// The ceiling only bounds anything if it is genuinely below total; the
	// child enforces this too (parseLCChildEnv), but state it here so the
	// scenario's own numbers can't drift into vacuity unnoticed.
	is.True(cfg.holdAt > cfg.failAt)
	is.True(cfg.holdAt < cfg.total)

	child := spawnLCChild(t, cfg)
	child.waitForMarker(t, markerLCHeld+" ", 10*time.Second)
	is.Equal(maxProgressPosition(child, markerLCHeld), cfg.holdAt) // the marker reports the ceiling itself

	time.Sleep(recoveryHoldStall) // see recoveryHoldStall: adversarial, not load-bearing

	// Nothing beyond the ceiling was ever produced, however long we looked
	// away...
	is.True(maxProgressPosition(child, "READ") <= cfg.holdAt)

	// ...so nothing beyond it can ever have been acked and committed either,
	// which is the property the SIGKILL scenario's upper-bound guard needs.
	// Read while the child is still alive, exactly as the kill gate does.
	upstream, err := openUpstreamStore(cfg.upstreamDir, false)
	is.NoErr(err)
	committed, err := upstream.Committed()
	is.NoErr(err)
	is.True(committed >= cfg.failAt) // the recovered run really did make durable progress
	is.True(committed <= cfg.holdAt)
	is.True(committed < cfg.total)

	child.sigkill(t) // crashable variant: it would otherwise block forever
}

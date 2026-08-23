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
	"testing"
	"time"

	"github.com/matryer/is"
)

// TestSIGTERM_GracefulTeardown_DrainsDeferredAck is DBZ-2's SIGTERM/
// invariant-7 case (docs/design-documents/20260726-dbz2-cdc-correctness-suite.md,
// Rollout section and Resolved decision 4). It is the graceful-path
// complement to Property 2's SIGKILL cases (property2_test.go): instead of
// asking "did a crash lose the record" (answer: no, but only after a
// restart, and duplicates are fine), it asks the strictly stronger question
// a graceful stop must answer: did Source.Teardown's flush-and-wait-then-
// stopStream ordering (source.go:249-326) actually SEND the ack that was
// still deferred at the moment the signal landed, before the process ever
// exited - no restart required at all.
//
// Timing mirrors property2_test.go's mid-position-write case exactly
// (paceMS=15, killAfterReads=95): by read #95 (~1.4s in) one automatic
// debounce flush has normally already happened (~1s, around read ~66) and a
// second debounce window has already started (on the next ack after that
// flush) but not yet fired - so there is a genuine deferred ack in flight
// when SIGTERM lands, which is exactly the window invariant 7 protects.
//
// "Normally" is load-bearing, and used to be the flake (#2773): the read
// count and the flush chain are ordered only by wall clock, with a measured
// ~460ms of margin between them. The read count is still what puts this case
// in the mid-position-write window, but the flush is now WAITED FOR
// (waitForFirstUpstreamCommit) rather than assumed, and every assertion
// below is anchored on state this test actually observed at signal time.
func TestSIGTERM_GracefulTeardown_DrainsDeferredAck(t *testing.T) {
	cases := []struct {
		name string
		// persistDelayMS overrides the persister debounce; 0 = production
		// default (1s). See childConfig.persistDelayMS.
		persistDelayMS int
	}{{
		// The original case: production's own 1s debounce, so the deferred
		// window this exercises is the one real deployments have.
		name:           "default_debounce",
		persistDelayMS: 0,
	}, {
		// #2773 regression case, and a stronger invariant-7 scenario in its
		// own right.
		//
		// A debounce longer than the run's read phase means NO flush can
		// have landed by read #95 - deterministically, on any machine, with
		// no contention needed. Pre-fix that made this case fail 100% of the
		// time ("test setup assumption violated: expected 0 <
		// committedBefore(0)"), which is precisely the failure #2773 saw
		// intermittently in CI: a slow enough box is indistinguishable from
		// a slow enough debounce. Post-fix the run simply waits for the
		// flush and asserts the same invariant against it.
		//
		// It is also the worst case for invariant 7 on its own merits: the
		// entire run's worth of acks is still deferred when SIGTERM lands,
		// so Teardown has the largest possible backlog to drain. Same knob,
		// same reasoning as sigkillCases' mid-snapshot precondition (see
		// childConfig.persistDelayMS) - make the precondition deterministic
		// instead of wall-clock-inferred.
		name:           "debounce_slower_than_read_phase",
		persistDelayMS: 2000,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runSigtermDrainCase(t, tc.persistDelayMS)
		})
	}
}

func runSigtermDrainCase(t *testing.T, persistDelayMS int) {
	is := is.New(t)
	dir := t.TempDir()

	const (
		paceMS         = 15
		killAfterReads = 95
		// total is deliberately unbounded (0): this run terminates via
		// SIGTERM, never by reaching a target count - see runChildSigterm.
		total = 0
	)

	cfg := childConfig{
		dbDir:          dir + "/db",
		upstreamDir:    dir + "/upstream",
		prune:          false, // graceful path; prune-vs-durable is irrelevant here (Property 2 already covers that axis)
		paceMS:         paceMS,
		total:          total,
		sigtermMode:    true,
		persistDelayMS: persistDelayMS,
	}

	cp := spawnChild(t, cfg)
	cp.waitForReadCount(t, killAfterReads, 30*time.Second)

	// Read the upstream's committed watermark BEFORE sending SIGTERM, so we
	// know exactly how much was durable before the graceful stop forced the
	// still-pending flush.
	upstreamBefore, err := openUpstreamStore(cfg.upstreamDir, cfg.prune)
	is.NoErr(err)

	// #2773: POLL for the first flush to become visible upstream instead of
	// sampling the watermark once and asserting it is already non-zero.
	//
	// "One debounce flush has happened by read #95" is an asynchronous
	// watermark, not a fact the read count establishes. The two are only
	// ordered by wall clock, and the budget is small and fixed: measured
	// over 15 -race runs on an idle machine, the first upstream commit lands
	// at 1.10-1.19s (the 1s persister debounce, plus the durable write, plus
	// the deferred-ack send, plus the plugin's own commit) while read #95
	// lands at 1.56-1.64s - a margin of 448-473ms, ~460ms every time. Any CI
	// stall longer than that anywhere in that chain used to turn this into a
	// "test setup assumption violated" failure on a required check, with
	// nothing wrong with the code under test (that is exactly the 1.98s
	// failure in #2773).
	//
	// Waiting for the watermark itself removes the wall-clock dependency
	// without weakening anything: the property this case exists to pin is
	// about what SIGTERM does to a deferred ack, and it is asserted below
	// against the state actually observed here, not against an assumed one.
	committedBefore := waitForFirstUpstreamCommit(t, cp, upstreamBefore, 30*time.Second)

	// Sample the read count AFTER the watermark (never before: polling may
	// have taken time, and a stale-low read count would make the gap check
	// below fire spuriously). readsAtSignal is a lower bound on how many
	// records the source had read when the signal landed - reads keep coming
	// until then - which is what makes it a safe anchor for the drain
	// assertion further down.
	readsAtSignal := uint64(cp.readCount())

	// The premise this case needs, now checked against observed state rather
	// than assumed from timing: a flush has landed upstream, but it is
	// behind the reads - i.e. there genuinely IS a "deferred and not yet
	// visible upstream" gap for Teardown to close, not something that had
	// already caught up on its own. The persister's debounce makes acks lag
	// reads structurally, so this holds however slow the run was.
	if committedBefore >= readsAtSignal {
		t.Fatalf(
			"test setup assumption violated: expected committedBefore(%d) < readsAtSignal(%d) - "+
				"the upstream watermark has caught up with the reads, so there is no deferred ack "+
				"in flight for SIGTERM to land on; either the persister debounce was disabled or "+
				"this case's pacing no longer lands in the intended mid-position-write window\n%s",
			committedBefore, readsAtSignal, cp.diagnostics(),
		)
	}

	cp.sigterm(t, 30*time.Second) // graceful: child catches SIGTERM, Teardown drains the deferred ack, exits 0 (waitExit asserts this)

	_, ok := cp.line(markerSigtermDone)
	if !ok {
		t.Fatalf("child did not reach its own graceful-shutdown marker (%s) - see diagnostics\n%s", markerSigtermDone, cp.diagnostics())
	}

	// The core invariant-7 assertion: the ack that was DEFERRED (queued via
	// Source.Ack, registered with the persister, but not yet confirmed
	// durable) at the moment SIGTERM landed must have been flushed - not
	// dropped - by the time the process exited. This is a SPECIFIC,
	// non-tautological claim: it is not "no error happened", it is "the
	// upstream watermark advanced past what was already durable before the
	// signal, all the way to (approximately) the read point" - i.e. nothing
	// was left "deferred and lost".
	upstreamAfter, err := openUpstreamStore(cfg.upstreamDir, cfg.prune)
	is.NoErr(err)
	committedAfter, err := upstreamAfter.Committed()
	is.NoErr(err)

	if committedAfter <= committedBefore {
		t.Fatalf(
			"SEV-0-CLASS REGRESSION: graceful SIGTERM teardown did not drain the deferred ack - "+
				"upstream watermark before signal was %d, after graceful teardown still %d "+
				"(expected it to advance past killAfterReads-ish, %d) - Source.Teardown's "+
				"flush-and-wait-then-stopStream ordering (source.go:249-326) should make this "+
				"structurally unreachable; re-verify Teardown before assuming this is safe.\n%s",
			committedBefore, committedAfter, killAfterReads, cp.diagnostics(),
		)
	}
	// Allow a small margin below readsAtSignal: the producer's own pacing
	// means the very last read(s) before the signal may not have been acked
	// yet (an Ack call happens strictly after its Read), so the drained
	// position can be a few short without indicating any drop - what matters
	// is that it advanced well past committedBefore, not that it hit the
	// read count exactly.
	//
	// Anchored on readsAtSignal rather than the killAfterReads constant
	// (#2773): once the watermark is polled for above, a slow run can go on
	// reading past killAfterReads before the signal lands, and every one of
	// those extra reads must be drained too. Using the observed read count
	// keeps this assertion as strong as the run was long, instead of
	// silently loosening to a fixed 95 on exactly the slow runs where a
	// drain bug is most likely to show.
	const slack = 5
	if committedAfter+slack < readsAtSignal {
		t.Fatalf(
			"graceful SIGTERM teardown drained fewer acks than expected - committed only reached %d, "+
				"expected within %d of the read count at signal time (%d) - the deferred ack pending "+
				"at signal-time looks like it was NOT fully flushed before the process exited\n%s",
			committedAfter, slack, readsAtSignal, cp.diagnostics(),
		)
	}

	// Cross-check against Conduit's own persisted position too (not just the
	// plugin-side upstream commit): a fresh child pointed at the same
	// on-disk state must report RESUME_POSITION consistent with what the
	// upstream says was committed - nothing left un-persisted that the
	// plugin was already told (upstream) to consider durable. Give it a
	// small, fast-paced total just past the current watermark so it
	// completes quickly.
	third := spawnChild(t, childConfig{
		dbDir:       cfg.dbDir,
		upstreamDir: cfg.upstreamDir,
		prune:       cfg.prune,
		paceMS:      1,
		total:       committedAfter + 10,
	})
	third.waitExit(t, parentWaitExit)

	resumeLine, ok := third.line("RESUME_POSITION")
	is.True(ok)
	resumePos := parseResumePosition(t, resumeLine)
	is.True(resumePos >= committedBefore) // no gap either way
	if resumePos < committedAfter-slack {
		t.Fatalf(
			"Conduit's own persisted resume position (%d) is suspiciously behind the upstream's "+
				"committed watermark after the graceful stop (%d) - the deferred ack may have reached "+
				"the plugin without Conduit's own state write actually landing durably first, which "+
				"would itself be an invariant-1 violation\n%s",
			resumePos, committedAfter, third.diagnostics(),
		)
	}

	_, corrupt := third.line(markerCorruptPo)
	is.True(!corrupt) // invariant 2: no torn/corrupted position

	_, done := third.line(markerDone)
	is.True(done)
}

// waitForFirstUpstreamCommit blocks (polling, exactly like
// childProcess.waitForReadCount - never a fixed sleep) until the upstream
// store reports a non-zero committed watermark, and returns it.
//
// This exists because "the persister has flushed at least once, and that
// flush's deferred ack has made it all the way to the plugin's own durable
// commit" is an ASYNCHRONOUS event with no ordering relationship to the
// child's read count. Read progress is paced (waitForReadCount's doc
// explains why that makes it a sound clock); the flush chain is not paced by
// anything this test controls, so the only correct way to depend on it is to
// wait for it. Reading it once and asserting it already happened is the
// #2773 flake.
//
// The timeout is a stuck-forever guard, not a tuning knob: on the happy path
// this returns after ~0ms (the flush has normally already landed by the time
// killAfterReads reads have been paced out) and it must never be raised to
// "fix" a failure - a run that genuinely never commits upstream is a real
// invariant-1 finding, and the diagnostics dump below is how to read it.
func waitForFirstUpstreamCommit(t *testing.T, cp *childProcess, upstream *upstreamStore, timeout time.Duration) uint64 {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		committed, err := upstream.Committed()
		if err != nil {
			t.Fatalf("read upstream committed watermark: %v\n%s", err, cp.diagnostics())
		}
		if committed > 0 {
			return committed
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"upstream watermark never advanced past 0 within %s - the persister's debounce flush, "+
					"its durable write, the resulting deferred ack, or the plugin's commit of it never "+
					"completed while the source kept reading; that is an invariant-1 finding, not a "+
					"timing artifact\n%s",
				timeout, cp.diagnostics(),
			)
		}
		time.Sleep(time.Millisecond)
	}
}

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

	"github.com/conduitio/conduit/pkg/connector"
)

// The drain bound must distinguish a WEDGED ack path from a merely slow one.
//
// The implementation this replaced could not: it was a wall-clock deadline
// (first flat, then a function of total), so both cases expired the same
// timer and produced the same message. Worse, the per-total version was a
// no-op for every case in the tree — drainBudget(500) returned exactly the 5s
// floor it was meant to raise, and mid-snapshot is total=500. A test asserted
// that ("floor boundary", 500, 5s) and it read as a boundary case rather than
// as the bug. These cases are written to fail if that shape ever returns.
func TestClassifyDrain_DistinguishesWedgedFromSlow(t *testing.T) {
	const total = 500

	for _, tc := range []struct {
		name          string
		committed     uint64
		sinceProgress time.Duration
		sinceStart    time.Duration
		want          drainVerdict
	}{
		{"still working", 250, time.Second, 2 * time.Second, drainContinue},
		{"finished", total, 0, time.Second, drainDone},
		{"finished over-count", total + 1, 0, time.Second, drainDone},

		// The property the old wall clock could not express: a drain slower
		// than any fixed budget is TOLERATED for as long as it keeps moving.
		// 500 fsyncs on a contended CI filesystem is exactly this case, and
		// it is the flake this whole change exists to stop misreporting.
		{"slow but advancing is not a failure", 499, time.Second, ackDrainHardCap - time.Second, drainContinue},

		{"stopped advancing is a wedge", 100, ackDrainStallBudget, 6 * time.Second, drainStalled},
		{"advancing but past the cap", 499, time.Second, ackDrainHardCap, drainTooSlow},

		// Order matters: the more serious diagnosis must win, and a drain
		// that completes on the same poll must never be called a wedge.
		{"wedge wins over cap", 100, ackDrainStallBudget, ackDrainHardCap, drainStalled},
		{"done wins over wedge", total, ackDrainStallBudget, ackDrainHardCap, drainDone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDrain(tc.committed, total, tc.sinceProgress, tc.sinceStart, ackDrainStallBudget)
			if got != tc.want {
				t.Fatalf("classifyDrain(committed=%d, total=%d, sinceProgress=%s, sinceStart=%s) = %s, want %s",
					tc.committed, total, tc.sinceProgress, tc.sinceStart, got, tc.want)
			}
		})
	}
}

// classifyDrain must never report drainTooSlow for a stallBudget larger than
// ackDrainHardCap, even if the caller forgot to clamp it: since sinceProgress
// <= sinceStart by construction (drainTracker.observe never lets progress get
// ahead of start), an oversized stallBudget makes sinceStart reach the cap
// before sinceProgress could ever reach it, so the stalled branch - checked
// first - would never fire. A watermark frozen since drain entry (the
// clearest possible wedge) would then be reported as pacing: the
// false-exoneration mirror of a false wedge, and worse, because it dismisses
// a real defect instead of merely misnaming a benign one.
//
// drainStallBudget (the only production caller of classifyDrain, via
// drainTracker) already clamps before this - see
// TestDrainStallBudget_ClampsToTheHardCap - but that only proves the ONE
// caller is well-behaved. This pins the invariant where it actually has to
// hold, independent of any caller.
func TestClassifyDrain_UnclampedStallBudgetNeverReportsTooSlowAsAWedge(t *testing.T) {
	const total = 500
	oversized := ackDrainHardCap * 10

	got := classifyDrain(100, total, ackDrainHardCap, ackDrainHardCap, oversized)
	if got == drainTooSlow {
		t.Fatalf("classifyDrain with stallBudget=%s (> ackDrainHardCap=%s) reported drainTooSlow "+
			"for a watermark frozen since drain entry; that dismisses a real wedge as pacing",
			oversized, ackDrainHardCap)
	}
	if got != drainStalled {
		t.Fatalf("classifyDrain(...) = %s, want drainStalled", got)
	}
}

// The hard cap must stay clear of the parent's waitExit timeout, with real
// margin for everything that happens between the cap firing and the parent
// giving up.
//
// The previous version of this test modeled that margin as a bare
// subtraction (parentWaitExit - ackDrainHardCap >= 5s) and called the result
// "headroom for Teardown and process exit". That is not what the 5s
// represents: the child's OWN read loop runs to completion BEFORE the drain
// even starts, and it is not free (upstreamStore.Commit's fsync happens
// there too, once per position). property2_test.go's "mid-position-write"
// case is total=400 at paceMS=15 - a read loop worth ~6s on its own - so the
// bare subtraction was silently spending headroom the read loop had already
// used. maxReadLoop below is derived from the actual scenario tables
// (sigkillCases, property2Cases) rather than a hand-picked number, so this
// test can't drift from what the tree actually runs.
func TestAckDrainHardCap_StaysUnderTheParentWaitExitTimeout(t *testing.T) {
	maxReadLoop := maxReadLoopInTree(t)
	const teardownAllowance = 5 * time.Second // Teardown, WaitPendingWrites, process start/exit, badger open

	if total := maxReadLoop + ackDrainHardCap + teardownAllowance; total > parentWaitExit {
		t.Fatalf("maxReadLoop(%s) + ackDrainHardCap(%s) + teardownAllowance(%s) = %s exceeds the "+
			"parent's waitExit(%s); a pathologically slow run would hit the parent's blunt "+
			"\"timed out waiting for child to exit\" instead of this cap's precise diagnosis",
			maxReadLoop, ackDrainHardCap, teardownAllowance, total, parentWaitExit)
	}
	if ackDrainStallBudget >= ackDrainHardCap {
		t.Fatalf("stall budget %s must be shorter than the hard cap %s, or a wedge would be "+
			"reported as slowness", ackDrainStallBudget, ackDrainHardCap)
	}
}

// maxReadLoopInTree returns the slowest read loop (total x paceMS) among the
// scenario tables that actually run a graceful child through this drain path
// (sigkillCases, property2Cases - see their own callers' use of
// waitForUpstreamCommitted). Computed from those tables, not hand-copied,
// so a new or resized case is automatically reflected here instead of
// silently invalidating TestAckDrainHardCap_StaysUnderTheParentWaitExitTimeout's
// model.
func maxReadLoopInTree(t *testing.T) time.Duration {
	t.Helper()
	var slowest time.Duration
	for _, tc := range sigkillCases {
		if d := time.Duration(tc.total) * time.Duration(tc.paceMS) * time.Millisecond; d > slowest {
			slowest = d
		}
	}
	for _, tc := range property2Cases {
		if d := time.Duration(tc.total) * time.Duration(tc.paceMS) * time.Millisecond; d > slowest {
			slowest = d
		}
	}
	if slowest == 0 {
		t.Fatal("maxReadLoopInTree computed 0; sigkillCases/property2Cases must be non-empty with a positive total*paceMS")
	}
	return slowest
}

// The assertion that would have caught the previous fix being a no-op.
//
// The measured drain for total=500 is 6.5-9.9 ms/position on an idle local
// SSD — 3.3s to 5.0s, i.e. one observed run finished 45ms inside the old 5s
// deadline. A CI runner's filesystem is slower, which is why it fails there
// and not here. So the bound must tolerate a drain that takes materially
// LONGER than 5s while still advancing; anything that expires at 5s has not
// fixed the flake regardless of how it is expressed.
func TestDrain_ToleratesADrainSlowerThanTheOldFlatBudget(t *testing.T) {
	const (
		total       = 500 // mid-snapshot, and sigkill_test.go's mid-snapshot
		oldFlat     = 5 * time.Second
		observedMax = 5 * time.Second // 9.9ms/position x 500, measured under -race
	)

	// Twice the worst locally-observed (idle-SSD) drain, still advancing
	// throughout. NOT the tree's global worst: a separate, forced-contention
	// run recorded up to ~12.5s for the same total=500
	// (TestDrainTracker_MonotonicProgressIsNeverAWedge's comment) - this is
	// 2x the idle-machine number, not 2x that one.
	slow := 2 * observedMax
	if got := classifyDrain(total-1, total, 50*time.Millisecond, slow, ackDrainStallBudget); got != drainContinue {
		t.Fatalf("a drain still advancing after %s (2x the worst locally-observed, idle-SSD draw) "+
			"was judged %s, want drainContinue; a bound that gives up here has not fixed the flake",
			slow, got)
	}

	// And the specific regression: whatever bounds a still-advancing drain
	// must exceed the flat budget this replaced, or nothing changed.
	if ackDrainHardCap <= oldFlat {
		t.Fatalf("ackDrainHardCap %s does not exceed the old flat %s, so total=%d gets no more "+
			"room than before", ackDrainHardCap, oldFlat, total)
	}
}

// The test that would have caught the previous attempt.
//
// classifyDrain is pure and well-pinned, but the bookkeeping that feeds it -
// refreshing lastProgress when the watermark moves - lived inline in the loop
// with zero coverage. Deleting that single line collapsed sinceProgress into
// sinceStart, turning the whole thing back into a flat wall clock from drain
// entry, and all three existing tests still passed. Under contention that
// mutant reported "ack drain STALLED at 268/500" on a perfectly healthy
// child - the original flake, now with a false wedge accusation attached.
//
// Driving drainTracker with a fake clock is what closes that gap.
func TestDrainTracker_MonotonicProgressIsNeverAWedge(t *testing.T) {
	const total = 500
	base := time.Unix(0, 0)
	tracker := newDrainTracker(base, ackDrainStallBudget)

	// One position every 100ms for 12s: far slower than any measured drain
	// (worst observed is ~12.5s for the whole 500 under forced contention),
	// and comfortably past the old flat 5s budget. It advances the entire
	// time, so it must never be judged a wedge.
	now := base
	for committed := uint64(1); committed <= 120; committed++ {
		now = now.Add(100 * time.Millisecond)
		if got := tracker.observe(committed, total, now); got == drainStalled {
			t.Fatalf("steady progress at %d/%d after %s was judged a wedge; the tracker is not "+
				"refreshing lastProgress, so sinceProgress has collapsed into sinceStart",
				committed, total, now.Sub(base))
		}
	}
	if elapsed := now.Sub(base); elapsed <= 5*time.Second {
		t.Fatalf("this test only proves something past the old flat 5s budget; it ran %s", elapsed)
	}
}

// The mirror image: once the watermark genuinely stops, the tracker must
// notice within the budget rather than waiting out the hard cap.
func TestDrainTracker_StoppedProgressIsAWedge(t *testing.T) {
	const total = 500
	base := time.Unix(0, 0)
	tracker := newDrainTracker(base, ackDrainStallBudget)

	now := base.Add(time.Second)
	if got := tracker.observe(100, total, now); got != drainContinue {
		t.Fatalf("first observation judged %s, want drainContinue", got)
	}
	// Watermark frozen at 100 from here on.
	now = now.Add(ackDrainStallBudget - time.Millisecond)
	if got := tracker.observe(100, total, now); got != drainContinue {
		t.Fatalf("just inside the budget judged %s, want drainContinue", got)
	}
	now = now.Add(2 * time.Millisecond)
	if got := tracker.observe(100, total, now); got != drainStalled {
		t.Fatalf("frozen watermark past the budget judged %s, want drainStalled", got)
	}
}

// The stall budget is derived from the child's own persister debounce, not
// assumed to be the default. Deferred acks are not released until the
// persister flushes, so a healthy child commits NOTHING for one debounce
// period after its read loop ends. persistDelayMS is a per-child knob
// (sigkill_test.go already sets 600_000 for the killed child); at 6s against
// a bare 5s budget it made a healthy graceful child report a wedge.
func TestDrainTracker_StallBudgetClearsThePersisterDebounce(t *testing.T) {
	const total = 500
	longDebounce := 6 * time.Second
	base := time.Unix(0, 0)
	tracker := newDrainTracker(base, drainStallBudget(longDebounce))

	// The whole debounce elapses with nothing committed - the real shape of a
	// healthy child waiting for its first flush.
	now := base.Add(longDebounce)
	if got := tracker.observe(0, total, now); got == drainStalled {
		t.Fatalf("a child idle for its own %s debounce was judged a wedge; the budget must be "+
			"derived from the debounce, not fixed at %s", longDebounce, ackDrainStallBudget)
	}
	// And it still catches a genuine stall past the derived budget.
	now = now.Add(ackDrainStallBudget + time.Second)
	if got := tracker.observe(0, total, now); got != drainStalled {
		t.Fatalf("no progress for %s past a %s budget judged %s, want drainStalled",
			now.Sub(base), longDebounce+ackDrainStallBudget, got)
	}
}

// effectivePersistDelay had no test in either direction. A mutation making
// it ignore cfg.persistDelayMS entirely (always returning the default)
// survives the whole suite: every unit-level drain test below hands
// drainStallBudget/drainTracker/classifyDrain an already-computed duration
// rather than routing through this function. That mutation also silently
// disarms sigkill_test.go's mid-snapshot precondition - the 600_000ms
// override exists to guarantee no persister flush lands before the kill,
// and without it actually taking effect that guarantee becomes
// probabilistic, caught only by watermarkAtKill != 0, which can pass on a
// fast machine even while the override is being silently ignored.
func TestEffectivePersistDelay(t *testing.T) {
	if got, want := effectivePersistDelay(childEnv{persistDelayMS: 6000}), 6*time.Second; got != want {
		t.Fatalf("effectivePersistDelay(persistDelayMS=6000) = %s, want %s (the override)", got, want)
	}
	if got, want := effectivePersistDelay(childEnv{}), connector.DefaultPersisterDelayThreshold; got != want {
		t.Fatalf("effectivePersistDelay(persistDelayMS=0) = %s, want %s (the default)", got, want)
	}
}

// No test executed waitForUpstreamCommitted at all before this seam existed.
// TestDrainStallBudget_IncludesThePersisterDebounce tests drainStallBudget,
// the function; TestDrainTracker_StallBudgetClearsThePersisterDebounce is
// HANDED an already-computed budget rather than letting the wait compute one
// itself. A mutation at the call site this function replaces (child.go's
// `stallBudget := drainStallBudget(persistDelay)` -> `:= ackDrainStallBudget`,
// dropping the derivation entirely) left every one of those tests green,
// because none of them exercise the wiring that connects persistDelay to the
// budget actually used by a running drain.
//
// drainLoop is that wiring, with the upstream read and the clock injected.
// This test drives it with persistDelay=6s and a reader that commits nothing
// until 6s of (fake) time has passed - the real shape of a healthy child's
// tail, since deferred acks are not released until the persister flushes -
// then advances to total. Under the mutation above (budget hardcoded to the
// bare 5s ackDrainStallBudget), sinceProgress crosses that budget while the
// reader is still idle inside its own debounce window, well before it ever
// starts advancing, and the loop reports drainStalled on a perfectly healthy
// drain instead of the drainDone this test requires.
func TestDrainLoop_DerivesTheBudgetFromPersistDelayNotTheBareConstant(t *testing.T) {
	const total = 10
	persistDelay := 6 * time.Second

	base := time.Unix(0, 0)
	var elapsed time.Duration
	const step = 500 * time.Millisecond
	now := func() time.Time {
		elapsed += step
		return base.Add(elapsed)
	}

	var committed uint64
	read := func() (uint64, error) {
		if elapsed < persistDelay {
			return 0, nil
		}
		if committed < total {
			committed++
		}
		return committed, nil
	}

	verdict, tracker := drainLoop(read, now, total, persistDelay)
	if verdict != drainDone {
		t.Fatalf("drainLoop classified a healthy, debounce-delayed drain as %s (committed=%d after "+
			"%s of fake time); a stall budget that ignores persistDelay (using the bare %s "+
			"ackDrainStallBudget instead of persistDelay+ackDrainStallBudget) would report %s here, "+
			"because the %s idle window blows a bare %s budget before the reader ever advances",
			verdict, tracker.last, elapsed, ackDrainStallBudget, drainStalled, persistDelay, ackDrainStallBudget)
	}
	if tracker.last != total {
		t.Fatalf("drainLoop returned committed=%d, want %d", tracker.last, total)
	}
}

// String makes failures name the verdict instead of an integer.
func (d drainVerdict) String() string {
	switch d {
	case drainContinue:
		return "drainContinue"
	case drainDone:
		return "drainDone"
	case drainStalled:
		return "drainStalled"
	case drainTooSlow:
		return "drainTooSlow"
	default:
		return "drainVerdict(?)"
	}
}

// The budget must be DERIVED from the child's debounce, not fixed. Dropping
// the persistDelay term is a one-token mutation that leaves every
// tracker-level test green — they are handed a budget rather than computing
// one — so the derivation needs its own assertion.
//
// This used to also cover sigkill_test.go's 600_000ms (600s) override, and
// asserted drainStallBudget(600s) == 605s - i.e. it actively blessed a budget
// nearly 40x ackDrainHardCap, which would have made classifyDrain's stalled
// branch unreachable for that child (see TestClassifyDrain_
// UnclampedStallBudgetNeverReportsTooSlowAsAWedge). That override applies
// only to sigkill_test.go's KILLED first child, which never reaches this
// drain (see this PR's own description for the correction) - but the
// derivation must still be safe for any persistDelay it could ever be
// called with, not just the ones that happen to reach it today. The 600s
// case now lives in TestDrainStallBudget_ClampsToTheHardCap, asserting the
// clamp instead of the unclamped (and unsafe) value.
func TestDrainStallBudget_IncludesThePersisterDebounce(t *testing.T) {
	for _, debounce := range []time.Duration{
		connector.DefaultPersisterDelayThreshold,
		6 * time.Second, // the value that produced a false wedge
	} {
		got := drainStallBudget(debounce)
		if got <= debounce {
			t.Fatalf("drainStallBudget(%s) = %s, which does not clear the debounce itself; a "+
				"healthy child commits nothing for one debounce period after its read loop ends",
				debounce, got)
		}
		if want := debounce + ackDrainStallBudget; got != want {
			t.Fatalf("drainStallBudget(%s) = %s, want %s", debounce, got, want)
		}
	}
}

// The derived budget must never reach, let alone exceed, ackDrainHardCap:
// past that point classifyDrain's stalled branch becomes unreachable (see
// its own doc comment and TestClassifyDrain_
// UnclampedStallBudgetNeverReportsTooSlowAsAWedge) and a real wedge gets
// reported as pacing instead - the false-exoneration mirror of the false
// wedge this whole change exists to stop.
func TestDrainStallBudget_ClampsToTheHardCap(t *testing.T) {
	got := drainStallBudget(600 * time.Second) // sigkill_test.go's killed-child override
	if got >= ackDrainHardCap {
		t.Fatalf("drainStallBudget(600s) = %s, which does not clear the hard cap %s; the stalled "+
			"branch in classifyDrain becomes unreachable past that point", got, ackDrainHardCap)
	}
	if want := ackDrainHardCap - time.Second; got != want {
		t.Fatalf("drainStallBudget(600s) = %s, want the clamp value %s", got, want)
	}
}

// The safety argument for this whole change, stated as an assertion rather
// than left in a PR description: the new bound is strictly weaker than
// main's, so this change cannot fail a run main would have passed.
//
// main's drain wait was a flat drainEntry + 5s. This change's stall arm
// cannot fire before sinceProgress >= stallBudget, and stallBudget =
// persistDelay + ackDrainStallBudget always exceeds ackDrainStallBudget
// alone, because every persistDelay actually reachable in this tree is
// strictly positive (effectivePersistDelay floors at
// connector.DefaultPersisterDelayThreshold, 1s - never 0; see
// minPersistDelay below for why even a near-zero one still holds). Since
// sinceProgress <= sinceStart by construction (drainTracker.observe), every
// new stall failure implies sinceStart > ackDrainStallBudget - the exact
// point at which main's flat 5s budget had already failed.
func TestNewStallBoundIsStrictlyWeakerThanMainsFlatBudget(t *testing.T) {
	const oldFlatBudget = 5 * time.Second // main's drainEntry + 5s

	// The theoretical floor, not the practical one (1s): even a persistDelay
	// this close to zero must still leave the new bound strictly above
	// main's, or the safety argument depends on no scenario ever configuring
	// a smaller one - which is exactly the kind of coupling this PR exists
	// to remove.
	const minPersistDelay = time.Millisecond

	if got := ackDrainStallBudget + minPersistDelay; got <= oldFlatBudget {
		t.Fatalf("ackDrainStallBudget(%s) + a near-zero persistDelay(%s) = %s does not exceed "+
			"main's flat budget %s; this change could then fail a run main would have passed, "+
			"which defeats its entire safety argument", ackDrainStallBudget, minPersistDelay, got,
			oldFlatBudget)
	}
}

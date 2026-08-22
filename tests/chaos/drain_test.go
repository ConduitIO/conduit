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
		{"slow but advancing is not a failure", 499, time.Second, 19 * time.Second, drainContinue},

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

// The hard cap must stay clear of the parent's waitExit timeout. If the
// child can outlive the parent's patience, the parent reports "timed out
// waiting for child to exit" and the child's own precise diagnosis is lost —
// which is the regression this change exists to prevent, arriving by a
// different door. The previous ceiling was 25s against a 30s parent timeout
// and nothing pinned the relationship.
func TestAckDrainHardCap_StaysUnderTheParentWaitExitTimeout(t *testing.T) {
	const parentWaitExit = 30 * time.Second // property2_test.go / sigkill_test.go

	if ackDrainHardCap >= parentWaitExit {
		t.Fatalf("ackDrainHardCap %s must be less than the parent's waitExit %s",
			ackDrainHardCap, parentWaitExit)
	}
	// Teardown, WaitPendingWrites and process exit all happen after the drain
	// and before the parent gives up, so the cap needs real headroom, not a
	// bare inequality.
	if margin := parentWaitExit - ackDrainHardCap; margin < 5*time.Second {
		t.Fatalf("only %s between the drain hard cap and the parent's waitExit; too tight for "+
			"Teardown and process exit", margin)
	}
	if ackDrainStallBudget >= ackDrainHardCap {
		t.Fatalf("stall budget %s must be shorter than the hard cap %s, or a wedge would be "+
			"reported as slowness", ackDrainStallBudget, ackDrainHardCap)
	}
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

	// Twice the worst locally-observed drain, still advancing throughout.
	slow := 2 * observedMax
	if got := classifyDrain(total-1, total, 50*time.Millisecond, slow, ackDrainStallBudget); got != drainContinue {
		t.Fatalf("a drain still advancing after %s (2x the worst measured) was judged %s, want "+
			"drainContinue; a bound that gives up here has not fixed the flake", slow, got)
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
func TestDrainStallBudget_IncludesThePersisterDebounce(t *testing.T) {
	for _, debounce := range []time.Duration{
		connector.DefaultPersisterDelayThreshold,
		6 * time.Second,   // the value that produced a false wedge
		600 * time.Second, // sigkill_test.go's override for the killed child
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

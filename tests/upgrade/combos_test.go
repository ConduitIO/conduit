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

// combos_test.go covers shape combinations: split+retry (the exact #2722
// shape - see TestV2Combo_RetryThenSplit's doc comment), filter+nack (both
// engines), and split+fan-out (the exact shape TestV2Combo_SplitFanOut's
// mutation-check evidence targets - see doc.go's "Non-vacuity" section).
package upgrade

import (
	"testing"

	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
)

// TestV2Combo_RetryThenSplit is the suite's primary #2722 mutation-check
// shape: a task retries one record, then on the retry pass splits it into
// several pieces - growing the sub-batch Worker.doTaskAttempt's tainted loop
// handed it, mid-recursion. This is the exact shape
// pkg/lifecycle-poc/funnel/worker_retry_span_test.go's
// TestDoTask_RetryThatSplits_DoesNotSkipRecords reproduces against a fake
// ackNacker; this test reproduces it end-to-end through a REAL
// *connector.Source and its real persisted position, which is what actually
// matters for upgrade safety (a fake-acker unit test can't observe
// connector.Source.Ack's p[len(p)-1] persistence behavior at all).
//
// See doc.go's "Non-vacuity" section for the verified mutation evidence:
// reverting worker.go's captured-span fix makes this test fail with exactly
// the signature #2722's fix commit describes (a middle position missing
// while later positions are present and the persisted position has already
// advanced past it).
func TestV2Combo_RetryThenSplit(t *testing.T) {
	const n = 6
	sh := newSourceHarness(t, n)
	dest := newMemDestination("main")

	p := newV2Pipeline(t, sh, v2Config{
		// Retries record "2" (0-based index 1), then splits it into 3 pieces
		// on the retry pass - mirrors
		// worker_retry_span_test.go's retryThenSplitTask exactly.
		Middle: []funnel.Task{&retryThenSplitTask{id: "retry-split", retryIndex: 1, splitInto: 3}},
		Dests:  []*memDestination{dest},
	})
	// 1, 2a, 2b, 2c, 3, 4, 5, 6 = 8 active records.
	p.waitTotalDelivered(8, shapeTimeout, dest)
	p.stopGracefully(shapeTimeout)

	for _, suffix := range []string{"a", "b", "c"} {
		if !dest.hasPositionSuffix("2", suffix) {
			t.Fatalf("split piece 2%s was never delivered - a record was skipped by the retry-span overshoot", suffix)
		}
	}
	for _, i := range []int{1, 3, 4, 5, 6} {
		if !dest.hasPosition(pos(i)) {
			t.Fatalf("position %d was never delivered - a record was skipped by the retry-span overshoot", i)
		}
	}
	if got, want := dest.count(), 8; got != want {
		t.Fatalf("destination delivered %d records, want %d", got, want)
	}

	sh.waitPersistedPosition(n, shapeTimeout)
}

// TestV2Combo_FilterNack chains a filter and a nack in the same batch,
// exercising subBatchByFlag's transitions across Filter/Ack/Nack groups
// (batch.go/worker.go).
func TestV2Combo_FilterNack(t *testing.T) {
	const n = 6
	sh := newSourceHarness(t, n)
	dest := newMemDestination("main")
	dlqDest := newMemDestination("dlq")

	p := newV2Pipeline(t, sh, v2Config{
		Middle: []funnel.Task{
			&filterPosTask{id: "filter", positions: map[string]bool{"2": true}},
			&nackPosTask{id: "nack", positions: map[string]bool{"4": true}},
		},
		Dests:               []*memDestination{dest},
		DLQDest:             dlqDest,
		DLQWindowSize:       0,
		DLQWindowNackThresh: 0,
	})
	p.waitTotalDelivered(n-1, shapeTimeout, dest, dlqDest) // 6 total minus the filtered one; the nacked one lands in the DLQ, not dest
	p.stopGracefully(shapeTimeout)

	if dest.hasPosition(pos(2)) {
		t.Fatal("filtered record (position 2) was delivered to the main destination")
	}
	if dest.hasPosition(pos(4)) {
		t.Fatal("nacked record (position 4) was delivered to the main destination")
	}
	if !dlqDest.hasPosition(pos(4)) {
		t.Fatal("nacked record (position 4) never reached the DLQ")
	}
	for _, i := range []int{1, 3, 5, 6} {
		if !dest.hasPosition(pos(i)) {
			t.Fatalf("position %d was never delivered", i)
		}
	}

	sh.waitPersistedPosition(n, shapeTimeout)
}

// TestV2Combo_SplitFanOut splits a record BEFORE the destination fan-out
// point (so the whole run is present, not straddled, when doNextTask clones
// the batch per branch - see run_ledger.go's validateRunsWholeBeforeFanOut).
// Both destinations must receive every piece. This is a baseline check, not
// a mutation-check shape: the whole run resolves within a single, untainted
// Ack pass here (nothing straddles two separate Ack/Nack calls), so
// run_ledger.go's withholding has nothing to withhold in this particular
// shape - see TestV2Combo_SplitThenPartialRetry for the shape that actually
// exercises it.
func TestV2Combo_SplitFanOut(t *testing.T) {
	const n = 4
	sh := newSourceHarness(t, n)
	d1 := newMemDestination("d1")
	d2 := newMemDestination("d2")

	p := newV2Pipeline(t, sh, v2Config{
		Middle: []funnel.Task{&splitAtTask{id: "split", index: 1, into: 2}}, // splits record "2" into "2a","2b"
		Dests:  []*memDestination{d1, d2},
	})
	// 1, 2a, 2b, 3, 4 = 5 active records per branch.
	p.waitEachDelivered(5, shapeTimeout, d1, d2)
	p.stopGracefully(shapeTimeout)

	for _, d := range []*memDestination{d1, d2} {
		for _, suffix := range []string{"a", "b"} {
			if !d.hasPositionSuffix("2", suffix) {
				t.Fatalf("destination %q never received split piece 2%s", d.id, suffix)
			}
		}
		for _, i := range []int{1, 3, 4} {
			if !d.hasPosition(pos(i)) {
				t.Fatalf("destination %q never received position %d", d.id, i)
			}
		}
		if got := d.count(); got != 5 {
			t.Fatalf("destination %q delivered %d records, want 5", d.id, got)
		}
	}

	sh.waitPersistedPosition(n, shapeTimeout)
}

// TestV2Combo_SplitThenPartialRetry is the suite's second mutation-check
// shape: a record is split into two pieces, and only ONE of the pieces
// ("2a") is retried - converging cleanly on the next attempt, no further
// split. This is run_ledger.go's own canonical #2723/#2730 trigger (see its
// package doc: "a sub-batch covering only PART of a run - e.g. the head,
// while the tail is still off being retried in a separate doTask recursion
// - reaches the parent's Ack/Nack on its own"): "2a" resolves through the
// RecordFlagRetry recursion's OWN, separate Ack call, while "2b" resolves
// together with the unrelated records after it (3, 4) in a THIRD, later
// Ack call - three calls total, each touching only PART of the "2" run
// (note nacking, unlike retrying, does NOT produce this shape: Batch.Nack's
// own setFlagWithErr propagates a nack to every sibling of a split record in
// one step - see batch.go - so only retry can tear a run's members across
// separate calls this way).
//
// See doc.go's "Non-vacuity" section for the verified mutation evidence:
// bypassing runAckNacker's vote (forwarding every Ack/Nack straight to the
// parent instead of tallying run completion) makes this test fail - "2a"'s
// own Ack call releases the run's original position (2) immediately, and
// the THIRD call (["2b", 3, 4], "2b" carrying a nil position because it is
// a tail split piece) is then handed directly to Worker.Ack with a nil
// position in it, tripping validateAckPositions' CodeEmptySourcePosition
// guard and halting the pipeline - proving the run's completion tally, not
// just the final number, was actually being exercised.
func TestV2Combo_SplitThenPartialRetry(t *testing.T) {
	const n = 4
	sh := newSourceHarness(t, n)
	dest := newMemDestination("main")

	p := newV2Pipeline(t, sh, v2Config{
		Middle: []funnel.Task{
			&splitAtTask{id: "split", index: 1, into: 2},                           // splits record "2" into "2a","2b"
			&retryPosOnceTask{id: "retry", positions: map[string]bool{"2a": true}}, // retries only the first piece
		},
		Dests: []*memDestination{dest},
	})
	// 1, 2a, 2b, 3, 4 = 5 active records.
	p.waitTotalDelivered(5, shapeTimeout, dest)
	p.stopGracefully(shapeTimeout)

	for _, suffix := range []string{"a", "b"} {
		if !dest.hasPositionSuffix("2", suffix) {
			t.Fatalf("split piece 2%s was never delivered", suffix)
		}
	}
	for _, i := range []int{1, 3, 4} {
		if !dest.hasPosition(pos(i)) {
			t.Fatalf("position %d was never delivered", i)
		}
	}

	sh.waitPersistedPosition(n, shapeTimeout)
}

// TestV1Combo_FilterNack is v1's counterpart to TestV2Combo_FilterNack: a
// single processor filters one record and errors another. Both primitives
// are per-record in v1, so this is a straightforward composition, included
// for direct engine-to-engine comparison.
func TestV1Combo_FilterNack(t *testing.T) {
	const n = 6
	sh := newSourceHarness(t, n)

	p := newV1Pipeline(t, sh, v1Config{
		Proc: &filterOrErrorAtPosProcessor{
			filterPositions: map[string]bool{"2": true},
			errorPositions:  map[string]bool{"4": true},
		},
		DLQWindowSize:       0,
		DLQWindowNackThresh: 0,
	})
	errs := p.waitDone(shapeTimeout)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", fmtErrs(errs))
	}

	if p.dest.hasPosition(pos(2)) {
		t.Fatal("filtered record (position 2) was delivered to the main destination")
	}
	if p.dest.hasPosition(pos(4)) {
		t.Fatal("nacked record (position 4) was delivered to the main destination")
	}
	if p.dlq.count() != 1 {
		t.Fatalf("DLQ received %d records, want exactly 1", p.dlq.count())
	}
	for _, i := range []int{1, 3, 5, 6} {
		if !p.dest.hasPosition(pos(i)) {
			t.Fatalf("position %d was never delivered", i)
		}
	}

	sh.waitPersistedPosition(n, shapeTimeout)
}

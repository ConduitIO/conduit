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

// TestSIGKILL_NSource_FastAndSlow_GaplessIndependentResume is slice 3b of the
// arch-v2 multi-connector epic's chaos gate: SIGKILL the nsource child
// (nsource_child.go) while TWO independent sources - one fast, one
// deliberately slower - are actively reading/writing through a real
// funnel.Sink-shared destination, then restart it against the SAME on-disk
// state for BOTH sources, and assert every position 1..total from EACH
// source was durably delivered to the shared destination by the time the
// run completes - duplicates allowed (at-least-once), gaps forbidden, and
// each source's own delivery is independent of the other's (a kill timed to
// land mid-flight for one source must never affect the other's eventual
// completeness).
//
// Both sources count their OWN [1, total] position space (nsourceChildConfig
// no longer gives B a disjoint posOffset - see buildNSourceChildSource's
// doc): position VALUES are allowed, and in this test's overlapping range
// [1, min(totalA,totalB)] GUARANTEED, to collide byte-for-byte across A and
// B. That collision is deliberate, not incidental - it is the exact hazard
// behind H2 (docs/design-documents/20260801-archv2-multiconnector-
// nsource.md's H2 section): a worker erroring inside the shared destination
// subtree can leave unread acks on the shared stream, and a sibling reading
// them passes validateAcks' byte-for-byte position comparison whenever the
// bytes happen to match. Proving gapless, correctly-attributed per-source
// delivery under a REAL collision (not one avoided by construction) is this
// test's entire reason for existing - see nsourceSourceTagA/B and
// fanoutDestination.Write for the record-level salt and (sourceID, position)
// keying that make the two sources' colliding positions distinguishable in
// the shared deliveryLog without changing the collision itself.
//
// This is the load-bearing end-to-end proof for this slice's three central
// claims:
//   - Per-source resume independence: source A and source B each persist
//     and resume from their OWN position, in their OWN on-disk state -
//     nothing about one source's crash-time progress is entangled with the
//     other's.
//   - Shared-sink correctness under a real crash: the shared destination's
//     single deliveryLog ends up gapless for BOTH sources' contributions,
//     which is only possible if TaskNode.MarkSharedBoundary's serialization
//     held throughout the run (a lost/corrupted interleaving would show up
//     as a wrong-(sourceID,position) write, caught by deliveryLog's
//     per-delivery files) and if Worker.Close/funnel.Sink.Close never tore
//     the shared destination down while the other source was still writing
//     to it.
//   - No cross-source attribution under a genuine position collision: every
//     delivered record's sourceID (read back from deliveryLog, never
//     inferred) matches the source that actually produced it, even at
//     positions where A's and B's records are byte-identical apart from the
//     salt. See the strict switch below - an unattributed or
//     wrongly-tagged delivery fails the test immediately, it is never
//     silently dropped or folded into either source's count.
func TestSIGKILL_NSource_FastAndSlow_GaplessIndependentResume(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	const totalA = 150 // fast source
	const totalB = 80  // slow source

	cfg := nsourceChildConfig{
		dbDirA:       dir + "/dbA",
		dbDirB:       dir + "/dbB",
		upstreamDirA: dir + "/upstreamA",
		upstreamDirB: dir + "/upstreamB",
		destDir:      dir + "/dest",
		dlqDirA:      dir + "/dlqA",
		dlqDirB:      dir + "/dlqB",
		totalA:       totalA,
		totalB:       totalB,
		paceMSA:      1,  // fast: minimal pacing
		paceMSB:      15, // slow: deliberately behind A, so a kill timed on
		// combined read progress is very likely to land with A well ahead of
		// B - the "one fast, one slow" precondition the task calls for.
	}

	cp := spawnChildWithEnv(t, cfg.env())
	// Wait for real, combined progress across BOTH sources' chaosPlugin
	// instances (produceLoop's own READ lines - see waitForReadCount's doc
	// for why this is a genuine, race-free wall-clock guarantee) before
	// killing mid-flight. 60 combined reads, with B paced far slower than A,
	// all but guarantees A is well ahead of B at kill time.
	cp.waitForReadCount(t, 60, 15*time.Second)
	cp.sigkill(t)

	destLog, err := openDeliveryLog(cfg.destDir)
	is.NoErr(err)

	preDest, err := destLog.Positions()
	is.NoErr(err)
	t.Logf("pre-kill: shared destination delivered %d total positions (from either source)", len(preDest))

	// Restart against the SAME db/upstream/destination dirs for BOTH
	// sources: each resumed source must continue from ITS OWN durably
	// persisted position (never the other's), and the shared destination's
	// delivery log keeps accumulating across both runs.
	cp2 := spawnChildWithEnv(t, cfg.env())
	cp2.waitForMarker(t, markerNSourceDone, 45*time.Second)
	cp2.waitExit(t, 10*time.Second)

	finalDest, err := destLog.PositionsBySource()
	is.NoErr(err)

	// Source A and source B both count [1, total] independently - their
	// position VALUES overlap (and, for positions in [1, min(totalA,totalB)],
	// are byte-identical - see the test's own doc comment for why that
	// collision is deliberate). sourceID (deliveryLog's (sourceID, position)
	// key - see Record's doc) is the ONLY thing that can tell a delivery's
	// origin apart here; splitting finalDest by sourceID and checking each
	// independently for gaps is the real assertion this test exists to make:
	// a gap in EITHER source's range would mean a record from that source
	// was never durably written to the shared destination - a data-loss bug
	// - and any record whose sourceID is neither A nor B (unattributed, or
	// attributed to something else entirely) is exactly the misattribution
	// H2 was about and fails the test immediately rather than being folded
	// into either count.
	var gotA, gotB []uint64
	for _, r := range finalDest {
		switch r.SourceID {
		case nsourceSourceTagA:
			gotA = append(gotA, r.Position)
		case nsourceSourceTagB:
			gotB = append(gotB, r.Position)
		default:
			t.Fatalf("shared destination delivered position %d with unattributed/unexpected source tag %q "+
				"(want %q or %q) - this is exactly the cross-source misattribution H2 exists to prevent",
				r.Position, r.SourceID, nsourceSourceTagA, nsourceSourceTagB)
		}
	}
	assertGaplessDelivery(t, "source A (fast)", gotA, totalA)
	assertGaplessDelivery(t, "source B (slow)", gotB, totalB)

	// Both sources' own upstream watermarks (independently read straight
	// from disk, not from either process's memory) must show they reached
	// their own total - i.e. neither source's resume was silently starved
	// or truncated by the other's crash/restart.
	upstreamA, err := openUpstreamStore(cfg.upstreamDirA, false)
	is.NoErr(err)
	committedA, err := upstreamA.Committed()
	is.NoErr(err)
	is.Equal(uint64(totalA), committedA)

	upstreamB, err := openUpstreamStore(cfg.upstreamDirB, false)
	is.NoErr(err)
	committedB, err := upstreamB.Committed()
	is.NoErr(err)
	is.Equal(uint64(totalB), committedB)
}

// TestDeliveryLog_PositionsBySource_DistinguishesCollidingSources is a
// focused unit-level proof that deliveryLog's (sourceID, position) keying
// actually disambiguates a genuine collision, independent of the full
// SIGKILL scenario above - i.e. that the assertions in
// TestSIGKILL_NSource_FastAndSlow_GaplessIndependentResume would genuinely
// fail if a record's source attribution were ever lost or conflated, not
// merely that they happen to pass on the current happy path.
//
// It durably records the SAME position from two different sourceIDs (the
// exact shape TestSIGKILL_NSource_FastAndSlow_GaplessIndependentResume's
// overlapping [1, total] ranges produce) and checks two things a
// position-only ledger could not:
//  1. PositionsBySource reports BOTH deliveries, each correctly tagged - not
//     one delivery silently standing in for (or overwriting) the other.
//  2. The now-legacy Positions accessor (still used by every single-source
//     scenario, which never salts records - see its own doc comment) can no
//     longer serve as a stand-in for source-attributed correctness here: it
//     collapses both entries down to the bare position number, which is
//     precisely why the collision scenario needed PositionsBySource instead
//     of just calling Positions() and being unable to tell A's and B's
//     contributions apart - the bug this test exists to make impossible to
//     reintroduce silently.
func TestDeliveryLog_PositionsBySource_DistinguishesCollidingSources(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	log, err := openDeliveryLog(dir)
	is.NoErr(err)

	// Source A and source B both durably deliver position 5 - a genuine,
	// byte-identical-position collision, the same shape produceLoop's
	// overlapping counters produce end-to-end in the SIGKILL scenario.
	is.NoErr(log.Record(nsourceSourceTagA, 5))
	is.NoErr(log.Record(nsourceSourceTagB, 5))

	bySource, err := log.PositionsBySource()
	is.NoErr(err)
	is.Equal(len(bySource), 2) // both deliveries present - neither silently overwrote the other on disk

	var sawA, sawB bool
	for _, r := range bySource {
		is.Equal(r.Position, uint64(5))
		switch r.SourceID {
		case nsourceSourceTagA:
			sawA = true
		case nsourceSourceTagB:
			sawB = true
		default:
			t.Fatalf("unexpected sourceID %q in PositionsBySource result", r.SourceID)
		}
	}
	is.True(sawA) // source A's delivery of position 5 is attributed to A
	is.True(sawB) // source B's delivery of position 5 is attributed to B, not merged into A's

	// Positions (the plain, pre-collision-scenario accessor) legitimately
	// cannot make this distinction: both on-disk files parse to the same
	// bare position 5, so it reports position 5 TWICE with no way to tell
	// whether that means "one source delivered position 5 twice" (an
	// ordinary at-least-once duplicate, harmless) or "two DIFFERENT sources
	// each delivered their own position 5 once" (this test's actual
	// scenario). That ambiguity is exactly the gap PositionsBySource exists
	// to close - this is not a bug in Positions (every caller of it predates
	// sourceID and never collides - see its own doc comment), it is the
	// reason the collision scenario cannot use it, demonstrated directly
	// rather than merely asserted.
	plain, err := log.Positions()
	is.NoErr(err)
	is.Equal(plain, []uint64{5, 5})
}

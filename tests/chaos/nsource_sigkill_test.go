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
// This is the load-bearing end-to-end proof for this slice's two central
// claims:
//   - Per-source resume independence: source A and source B each persist
//     and resume from their OWN position, in their OWN on-disk state -
//     nothing about one source's crash-time progress is entangled with the
//     other's.
//   - Shared-sink correctness under a real crash: the shared destination's
//     single deliveryLog ends up gapless for BOTH sources' contributions,
//     which is only possible if TaskNode.MarkSharedBoundary's serialization
//     held throughout the run (a lost/corrupted interleaving would show up
//     as a wrong-position write, caught by deliveryLog's per-position files)
//     and if Worker.Close/funnel.Sink.Close never tore the shared
//     destination down while the other source was still writing to it.
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

	finalDest, err := destLog.Positions()
	is.NoErr(err)

	// Source A occupies [1, totalA]; source B occupies
	// [nsourcePosOffsetB+1, nsourcePosOffsetB+totalB] - a deliberately
	// disjoint numeric range (see buildNSourceChildSource's doc for why:
	// chaosPlugin.makeRecord is a pure function of the position number
	// alone, so without this the two sources would produce byte-for-byte
	// identical records at the same position, indistinguishable once both
	// land in the ONE shared deliveryLog). Splitting finalDest back into
	// each source's own range and checking each independently for gaps is
	// the real assertion this test exists to make: a gap in EITHER range
	// would mean a record from that source was never durably written to the
	// shared destination - a data-loss bug - and the two ranges being
	// checked separately (rather than as one combined count) is what proves
	// each source's delivery is independent of the other's.
	var gotA, gotB []uint64
	for _, p := range finalDest {
		if p > nsourcePosOffsetB {
			gotB = append(gotB, p-nsourcePosOffsetB)
		} else {
			gotA = append(gotA, p)
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
	is.Equal(uint64(nsourcePosOffsetB+totalB), committedB)
}

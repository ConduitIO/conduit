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

// TestSIGKILL_MidFanout_GaplessResume is slice 3a of the arch-v2
// multi-connector epic's chaos gate (AC-8): SIGKILL the fan-out child
// (fanout_child.go) while it is actively writing to two destinations through
// a real funnel.Worker/multiAckNacker, then restart it against the SAME
// on-disk state, and assert every source position 1..total was durably
// delivered to BOTH destinations by the time the run completes - duplicates
// allowed (at-least-once), gaps forbidden.
//
// destBDelayMS deliberately makes destB slower than destA (see
// fanoutChildConfig's doc), so waiting on read progress before killing gives
// a real chance of catching the exact scenario multiAckNacker exists to
// handle correctly: a record durably written to destA (fanoutDestination.
// Write already fsync'd it via deliveryLog.Record) but NOT YET to destB, and
// therefore - per multiAckNacker's unanimous-ack invariant (Invariant 1) -
// NOT YET acked to the source. If multiAckNacker instead acked as soon as
// ANY branch confirmed (the per-batch-counter bug this slice's design doc
// calls out), a kill at that exact instant would durably lose destB's copy
// of that record forever: the source would already believe it delivered,
// so a resumed run would never re-read it, and destB's ledger would show a
// permanent gap - exactly what this test's final assertion checks for.
func TestSIGKILL_MidFanout_GaplessResume(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	const total = 200

	cfg := fanoutChildConfig{
		dbDir:        dir + "/db",
		upstreamDir:  dir + "/upstream",
		destADir:     dir + "/destA",
		destBDir:     dir + "/destB",
		dlqDir:       dir + "/dlq",
		total:        total,
		paceMS:       2,
		destBDelayMS: 20, // destB always at least 20ms behind destA per Write call - see doc comment above
	}

	cp := spawnChildWithEnv(t, cfg.env())
	// Wait for real progress (chaosPlugin's own READ pacing - see
	// waitForReadCount's doc for why this is a genuine, race-free wall-clock
	// guarantee, not a wall-clock guess) before killing mid-flight.
	cp.waitForReadCount(t, 60, 15*time.Second)
	cp.sigkill(t)

	logA, err := openDeliveryLog(cfg.destADir)
	is.NoErr(err)
	logB, err := openDeliveryLog(cfg.destBDir)
	is.NoErr(err)

	preA, err := logA.Positions()
	is.NoErr(err)
	preB, err := logB.Positions()
	is.NoErr(err)
	// Diagnostic only (best-effort - a slow CI box could still have both
	// branches land in lockstep): confirms the destBDelayMS asymmetry is
	// actually doing its job of separating the two branches' progress.
	t.Logf("pre-kill: destA delivered %d positions, destB delivered %d positions", len(preA), len(preB))

	// Restart against the SAME db/upstream/destination dirs: the resumed run
	// must continue from Conduit's own durably persisted source position
	// (never re-derived from either destination ledger - the source has no
	// visibility into destination state, only into what multiAckNacker told
	// it was unanimously acked) and keep appending into the SAME delivery
	// logs, so the final logs reflect the union of both runs.
	cp2 := spawnChildWithEnv(t, cfg.env())
	cp2.waitForMarker(t, markerFanoutDone, 45*time.Second)
	cp2.waitExit(t, 10*time.Second)

	finalA, err := logA.Positions()
	is.NoErr(err)
	finalB, err := logB.Positions()
	is.NoErr(err)

	// AC-8's actual assertion: gapless resume. Every position from 1 to
	// total must appear in EACH destination's ledger at least once -
	// duplicates (a position appearing because it was redelivered after the
	// kill) are expected and fine, but a missing position in either ledger
	// would mean multiAckNacker acked the source for a record one of its M
	// branches never durably received - the exact data-loss bug this slice
	// exists to prevent (Invariant 1: ack only after ALL destinations
	// durably wrote).
	assertGaplessDelivery(t, "destA", finalA, total)
	assertGaplessDelivery(t, "destB", finalB, total)
}

// assertGaplessDelivery fails the test if any position in [1, total] is
// missing from positions. Duplicates are explicitly fine (positions is
// deduplicated into a set before checking) - only a genuine gap is a
// failure.
func assertGaplessDelivery(t *testing.T, label string, positions []uint64, total uint64) {
	t.Helper()

	seen := make(map[uint64]bool, len(positions))
	for _, p := range positions {
		seen[p] = true
	}

	var missing []uint64
	for p := uint64(1); p <= total; p++ {
		if !seen[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%s: gap detected - positions %v were never durably delivered (out of 1..%d); "+
			"this means the source was acked for a record %s never durably wrote, a data-loss bug",
			label, missing, total, label)
	}
}

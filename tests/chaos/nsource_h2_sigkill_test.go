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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
	"github.com/matryer/is"
)

// ackedSubsetOfDelivered checks H2's invariant-1 consequence (issue #2740)
// for a single source: every position it has committed (acked) upstream must
// have a matching durable delivery from THAT source in the shared
// destination's ledger. Returns a descriptive error naming the first missing
// position, or nil if every acked position was durably delivered by this
// source. Duplicates in delivered are fine (at-least-once); a position
// present in committed but absent from delivered is exactly the H2
// misattribution symptom (PR #2734's adversarial review): another source's
// identically-valued position got mistaken for this one's.
func ackedSubsetOfDelivered(sourceLabel, sourceTag string, committed uint64, delivered map[uint64]bool) error {
	for p := uint64(1); p <= committed; p++ {
		if !delivered[p] {
			return fmt.Errorf(
				"invariant 1 violation: source %s (tag %q) has upstream-committed (acked) position %d, "+
					"but the shared destination's ledger has NO durable delivery attributed to source %q "+
					"at that position - this source's own write was never confirmed; the ack it received "+
					"must have come from a DIFFERENT source's leftover/misattributed acknowledgment "+
					"(the exact H2 hazard, see PR #2734's adversarial review)",
				sourceLabel, sourceTag, p, sourceTag,
			)
		}
	}
	return nil
}

// TestAckedSubsetOfDelivered_CatchesUnattributedAck is a focused, isolated
// proof that ackedSubsetOfDelivered - the exact function
// TestSIGKILL_NSource_H2_AckStreamFault_InvariantHolds below uses to check
// the real, end-to-end chaos scenario - genuinely flags the H2 symptom (a
// position acked upstream that this source's own write never durably
// reached) rather than merely being asserted to. Mirrors
// nsource_sigkill_test.go's
// TestDeliveryLog_PositionsBySource_DistinguishesCollidingSources, which
// proves the ledger-keying primitive this check builds on the same way:
// independent of the full crash/resume scenario, so a regression in the
// CHECK itself (not just the engine) would be caught here too.
func TestAckedSubsetOfDelivered_CatchesUnattributedAck(t *testing.T) {
	is := is.New(t)

	// Simulates the exact H2 symptom: source B's upstream committed (acked)
	// position 1, but the shared destination's ledger has NO delivery
	// recorded under source B's tag at position 1 - only source A's
	// identically-valued position 1 exists, under A's own tag entirely, and
	// is absent from B's map here (see h2FaultDestination.Ack's doc comment,
	// nsource_h2_child.go, for how this shape arises end-to-end when
	// doTask's poison check is bypassed).
	err := ackedSubsetOfDelivered("B", nsourceSourceTagB, 1, map[uint64]bool{})
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "invariant 1 violation"))
	is.True(strings.Contains(err.Error(), "never confirmed"))

	// The healthy case: B's own delivery IS present at the acked position -
	// no violation.
	is.NoErr(ackedSubsetOfDelivered("B", nsourceSourceTagB, 1, map[uint64]bool{1: true}))

	// A violation partway through a longer run is caught too, not just the
	// obvious all-missing case - and it names the first missing position.
	err = ackedSubsetOfDelivered("B", nsourceSourceTagB, 3, map[uint64]bool{1: true, 3: true}) // 2 missing
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "position 2"))
}

// TestSIGKILL_NSource_H2_AckStreamFault_InvariantHolds is the end-to-end
// chaos coverage for H2 that issue #2740 asks for: it closes the gap between
// pkg/lifecycle-poc/funnel/worker_h2_poison_test.go's
// TestNSource_H2_AckStreamErrorPoisonsSharedDestination (a synthetic,
// in-package probe-destination unit test) and the real path - doTask's
// shared-boundary lock, DestinationTask.Do's early return on an Ack()
// error, real connector.Source/connector.Persister durability, and a real
// SIGKILL+resume - none of which that unit test, or #2739's ordinary
// (non-faulting) collision scenario, ever exercised together.
//
// See nsource_h2_child.go's file doc comment for the fault-injection
// mechanism (h2FaultDestination) and why it faithfully reproduces "a worker
// erroring inside a shared destination subtree leaves unread acks on the
// stream".
//
// # Non-vacuity
//
// This test's central assertion (ackedSubsetOfDelivered, below) was verified
// to go RED with the H2 fix's poison CHECK (not the store - see worker.go's
// doTask) temporarily neutralized to a no-op:
//
//	if taskNode.poisoned.Load() {          if false && taskNode.poisoned.Load() {
//
// Under that change, worker B is no longer refused: it reaches Write/Ack on
// the desynchronized shared stream, pops source A's leftover ack (byte-
// identical position, per this scenario's design), and acks its own source
// upstream on the strength of a write it never made. markerH2PoisonBypassed
// fires (this test's FIRST assertion below already fails at that point,
// before ever reaching ackedSubsetOfDelivered) with output of the shape:
//
//	H2 poison check was bypassed - worker B reached Write/Ack on a poisoned
//	shared destination; this must never happen with the fix intact
//
// This is deliberately the earliest possible failure: it proves the fix's
// specific mechanism (refusing entry) is what this test depends on, not
// merely a downstream symptom of it. See TestAckedSubsetOfDelivered_
// CatchesUnattributedAck above for the complementary, isolated proof that
// ackedSubsetOfDelivered ITSELF - independent of this scenario's timing and
// orchestration - correctly flags the deeper "acked but never delivered"
// shape once B's wrongly-acked position is fed to it. Between the two, this
// test's dependence on the real H2 fix is demonstrated at both the
// mechanism level (this test) and the consequence level (the focused unit
// test), not merely asserted.
func TestSIGKILL_NSource_H2_AckStreamFault_InvariantHolds(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	// Run 1 (fault): both sources attempt exactly one record by construction
	// - see nsourceH2ChildConfig's doc comment for why totalA=totalB=1 is
	// what makes this scenario race-free (source B, if it ever wrongly
	// succeeds, blocks forever on its now-exhausted source rather than
	// racing this process's own snapshot-then-block sequence with a second
	// batch). failAt=1 makes source A's very first Ack() call fail with
	// unread acks left queued; source B - gated on that same fault's
	// trigger via chaosPlugin.startGate, so it can never attempt entry
	// before the fault (and therefore the poison store, if the fix holds)
	// has happened - then contends for the same shared destination with a
	// record at the SAME position value (both sources count from 1).
	faultCfg := nsourceH2ChildConfig{
		dbDirA:       dir + "/dbA",
		dbDirB:       dir + "/dbB",
		upstreamDirA: dir + "/upstreamA",
		upstreamDirB: dir + "/upstreamB",
		destDir:      dir + "/dest",
		dlqDirA:      dir + "/dlqA",
		dlqDirB:      dir + "/dlqB",
		totalA:       1,
		totalB:       1,
		failAt:       1,
	}

	cp := spawnChildWithEnv(t, faultCfg.env())
	cp.waitForMarker(t, markerH2Confirmed, 15*time.Second)

	// The fix's mechanism, checked first and most directly (see the doc
	// comment's "Non-vacuity" section): worker B must have been REFUSED
	// entry to the poisoned shared destination, never silently reached
	// Write/Ack on it.
	if _, bypassed := cp.line(markerH2PoisonBypassed); bypassed {
		t.Fatalf("H2 poison check was bypassed - worker B reached Write/Ack on a poisoned shared "+
			"destination; this must never happen with the fix intact\n%s", cp.diagnostics())
	}

	// The poison error must surface (issue #2740's explicit requirement),
	// not a silent success.
	errBCodeLine, ok := cp.line(markerH2ErrBCode)
	is.True(ok)
	if !strings.Contains(errBCodeLine, funnel.CodeSharedDestinationPoisoned.Reason()) {
		t.Fatalf("worker B's error code line was %q, want it to contain %q (CodeSharedDestinationPoisoned)\n%s",
			errBCodeLine, funnel.CodeSharedDestinationPoisoned.Reason(), cp.diagnostics())
	}

	errALine, ok := cp.line(markerH2ErrA)
	is.True(ok)
	is.True(strings.Contains(errALine, "injected H2 ack-stream fault"))

	cp.sigkill(t)

	// RESUME: restart with the ORDINARY (non-faulting) N-source child
	// against the SAME on-disk state, using its own independently-sized
	// totals (the fault run's totals don't need to match - see
	// nsourceH2ChildConfig's doc). This proves the pipeline fails loudly
	// (never half-acks) and then recovers cleanly and completely once
	// restarted, exactly like an operator recycling a Degraded pipeline.
	const resumeTotalA = 3
	const resumeTotalB = 3
	resumeCfg := nsourceChildConfig{
		dbDirA:       faultCfg.dbDirA,
		dbDirB:       faultCfg.dbDirB,
		upstreamDirA: faultCfg.upstreamDirA,
		upstreamDirB: faultCfg.upstreamDirB,
		destDir:      faultCfg.destDir,
		dlqDirA:      faultCfg.dlqDirA,
		dlqDirB:      faultCfg.dlqDirB,
		totalA:       resumeTotalA,
		totalB:       resumeTotalB,
		paceMSA:      1,
		paceMSB:      1,
	}
	cp2 := spawnChildWithEnv(t, resumeCfg.env())
	cp2.waitForMarker(t, markerNSourceDone, 30*time.Second)
	cp2.waitExit(t, 10*time.Second)

	// The central assertion (issue #2740): for every source, the set of
	// positions acked upstream must be a subset of what that source's own
	// records actually reached the shared destination as. A position acked
	// but never delivered - not a byte mismatch, not a gap - is the failure
	// this scenario exists to catch.
	destLog, err := openDeliveryLog(faultCfg.destDir)
	is.NoErr(err)
	finalDest, err := destLog.PositionsBySource()
	is.NoErr(err)

	deliveredA := map[uint64]bool{}
	deliveredB := map[uint64]bool{}
	for _, r := range finalDest {
		switch r.SourceID {
		case nsourceSourceTagA:
			deliveredA[r.Position] = true
		case nsourceSourceTagB:
			deliveredB[r.Position] = true
		default:
			t.Fatalf("shared destination delivered position %d with unattributed/unexpected source tag %q "+
				"(want %q or %q) - exactly the cross-source misattribution H2 exists to prevent",
				r.Position, r.SourceID, nsourceSourceTagA, nsourceSourceTagB)
		}
	}

	upstreamA, err := openUpstreamStore(faultCfg.upstreamDirA, false)
	is.NoErr(err)
	committedA, err := upstreamA.Committed()
	is.NoErr(err)

	upstreamB, err := openUpstreamStore(faultCfg.upstreamDirB, false)
	is.NoErr(err)
	committedB, err := upstreamB.Committed()
	is.NoErr(err)

	if err := ackedSubsetOfDelivered("A", nsourceSourceTagA, committedA, deliveredA); err != nil {
		t.Fatal(err)
	}
	if err := ackedSubsetOfDelivered("B", nsourceSourceTagB, committedB, deliveredB); err != nil {
		t.Fatal(err)
	}

	// The pipeline must have recovered COMPLETELY, not just safely: both
	// sources reach their own (resume run's) total.
	is.Equal(uint64(resumeTotalA), committedA)
	is.Equal(uint64(resumeTotalB), committedB)

	var gotA, gotB []uint64
	for p := range deliveredA {
		gotA = append(gotA, p)
	}
	for p := range deliveredB {
		gotB = append(gotB, p)
	}
	assertGaplessDelivery(t, "source A (H2 end-to-end)", gotA, resumeTotalA)
	assertGaplessDelivery(t, "source B (H2 end-to-end)", gotB, resumeTotalB)
}

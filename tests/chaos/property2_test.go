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

// resumeShape is the two-state discriminator DBZ-2's design doc insists on
// (docs/design-documents/20260726-dbz2-cdc-correctness-suite.md, Property 2
// section: "the engine exposes exactly two post-crash resume states, not
// three"). loadOrCreateInstance (child.go) resumes from either
// ErrKeyNotExist -> fresh (empty) or the last durably flushed
// SourceState.Position (valid-stale). There is no third "boundary" shape.
type resumeShape int

const (
	// resumeEmpty: RESUME_POSITION must be the empty/nil position - no
	// state was ever durably persisted before the kill.
	resumeEmpty resumeShape = iota
	// resumeValidNonZero: RESUME_POSITION must be a valid, non-zero, stale
	// (behind the kill point) position - at least one flush landed before
	// the kill.
	resumeValidNonZero
)

// property2Case is one of DBZ-2's three Property 2 SIGKILL scenarios
// (design doc, Property 2 section). All three drive the identical engine
// code DBZ-1 already exercises (pkg/connector.Source.Ack's deferred-ack
// ordering, onPersistFlushed, pendingAcks/nextAckSeq/durableAckSeq, and
// connector.Persister's debounce/crash-safe badger write) - they differ only
// in WHEN, in producer time, the kill lands, using chaosPlugin's two-phase
// producer (snapshotK/snapshotPaceMS, upstream.go) so the mid-handoff case
// has a real HANDOFF marker to kill against.
type property2Case struct {
	name string

	// Two-phase producer knobs (chaosPlugin, upstream.go). snapshotK == 0
	// (mid-snapshot, mid-position-write) means "no distinct phase" -
	// identical single-pace behavior to DBZ-1's original sigkillCases.
	snapshotK      uint64
	snapshotPaceMS int
	paceMS         int
	total          uint64

	// Kill-timing gate: exactly one of these is set per case, both
	// READ-progress-gated (never ACK-gated, never a fixed sleep - see
	// waitForReadCount's and waitForMarker's doc comments for why that
	// matters under Approach A's deferred acks).
	killAfterReads int  // mid-snapshot, mid-position-write: gate on N observed READ lines
	killOnHandoff  bool // mid-handoff: gate on the HANDOFF marker itself

	// The two-state resume discriminator this case's kill timing is chosen
	// to land in - see resumeShape's doc.
	expectResume resumeShape
}

var property2Cases = []property2Case{
	{
		// Mid-snapshot: identical timing to DBZ-1's own "mid-snapshot" case
		// (sigkill_test.go) - an initial, fast, unpaced-ish burst (1ms/read).
		// killAfterReads=30 is ~30ms in, far short of the persister's FIRST
		// automatic flush (~1000ms), so Conduit has durably persisted
		// NOTHING at all when the kill lands. This is the highest-stakes
		// edge case named in the design doc: a crash before the snapshot
		// watermark is durably recorded at all - the engine-side reflection
		// of the conduit-connector-mysql #182 bug class.
		name:           "mid-snapshot",
		paceMS:         1,
		killAfterReads: 30,
		total:          500,
		expectResume:   resumeEmpty,
	},
	{
		// Mid-handoff (producer-pacing variant, NOT a distinct engine
		// state - see the design doc's Property 2 section and resumeShape's
		// doc above). snapshotK=40 at 1ms/read means the HANDOFF marker
		// fires at ~40ms elapsed - still far short of the first ~1000ms
		// flush, so this lands in the SAME "empty" persisted state as
		// mid-snapshot above, not a fictional third "boundary" shape.
		// Killing on the marker itself (waitForMarker), rather than a read
		// count chosen to merely be close to it, is what makes this
		// genuinely "just after HANDOFF" rather than a guess.
		name:           "mid-handoff",
		snapshotK:      40,
		snapshotPaceMS: 1,
		paceMS:         15, // stream-phase pace; never reached before the kill
		total:          300,
		killOnHandoff:  true,
		expectResume:   resumeEmpty,
	},
	{
		// Mid-position-write: identical timing to DBZ-1's own "mid-stream"
		// case (sigkill_test.go) - steady-state 15ms/read pacing.
		// killAfterReads=95 is ~1.4s in: by then one automatic flush has
		// already happened (~1s, around read ~66) and a second debounce
		// window has already started (on the next ack after that flush)
		// but not yet fired (its own 1s timer would land around read
		// ~133). So Conduit's persisted position is a valid but STALE
		// checkpoint - the other edge case named in the design doc.
		name:           "mid-position-write",
		paceMS:         15,
		killAfterReads: 95,
		total:          400,
		expectResume:   resumeValidNonZero,
	},
}

// TestSIGKILL_Property2_PruningUpstream drives all three Property 2 cases
// against a pruning (Postgres-slot-like) upstream: the harder class, where a
// gap is structurally reachable if the ack-follows-durable-flush ordering
// ever regresses (see doc.go and DBZ-1's TestSIGKILL_PruningUpstream_NoGap,
// which this generalizes to two additional kill points).
func TestSIGKILL_Property2_PruningUpstream(t *testing.T) {
	for _, tc := range property2Cases {
		t.Run(tc.name, func(t *testing.T) {
			assertProperty2Case(t, tc, true)
		})
	}
}

// TestSIGKILL_Property2_DurableUpstream is the counterfactual control: the
// IDENTICAL crash windows, against the IDENTICAL engine code, but an
// upstream that CAN redeliver behind its last commit (Kafka-like). Kept
// alongside the pruning-upstream variant so the same assertion set covers
// both upstream classes, as DBZ-1 established.
func TestSIGKILL_Property2_DurableUpstream(t *testing.T) {
	for _, tc := range property2Cases {
		t.Run(tc.name, func(t *testing.T) {
			assertProperty2Case(t, tc, false)
		})
	}
}

// assertProperty2Case drives one Property 2 SIGKILL scenario against an
// upstream of the given prune class and asserts:
//   - the shared invariants every case and every prune class must satisfy
//     (resume position at or ahead of the upstream watermark at kill time -
//     no gap; no OPEN_GAP_ERROR even against prune=true; no CORRUPT_POSITION,
//     invariant 2; DONE reached with committed == total, at-least-once); and
//   - the case-specific two-state RESUME_POSITION shape (resumeShape) that
//     makes this more than DBZ-1's generic ">=" check: it pins WHICH of the
//     engine's two post-crash resume states this kill timing actually lands
//     in, so a case silently landing in the wrong window fails loudly
//     instead of passing having tested nothing (see the design doc's
//     "timing flakiness" failure mode).
func assertProperty2Case(t *testing.T, tc property2Case, prune bool) {
	t.Helper()
	is := is.New(t)
	dir := t.TempDir()
	cfg := childConfig{
		dbDir:          dir + "/db",
		upstreamDir:    dir + "/upstream",
		prune:          prune,
		paceMS:         tc.paceMS,
		snapshotK:      tc.snapshotK,
		snapshotPaceMS: tc.snapshotPaceMS,
		total:          tc.total,
	}

	first := spawnChild(t, cfg)
	if tc.killOnHandoff {
		first.waitForMarker(t, markerHandoff, 30*time.Second)
	} else {
		first.waitForReadCount(t, tc.killAfterReads, 30*time.Second)
	}
	first.sigkill(t)

	committedAtKill, err := openUpstreamStore(cfg.upstreamDir, cfg.prune)
	is.NoErr(err)
	watermarkAtKill, err := committedAtKill.Committed()
	is.NoErr(err)

	second := spawnChild(t, cfg)
	second.waitExit(t, 30*time.Second)

	resumeLine, ok := second.line("RESUME_POSITION")
	is.True(ok)
	resumePos := parseResumePosition(t, resumeLine)

	// Shared assertions (all cases, both prune classes).
	is.True(resumePos >= watermarkAtKill) // invariant 1/3: no gap - resume is never behind what was already committed upstream

	_, foundGap := second.line(markerOpenGap)
	if foundGap {
		gapLine, _ := second.line(markerOpenGap)
		t.Fatalf(
			"Property 2 regression (%s, prune=%v): chaosPlugin.Open reported a gap "+
				"(resume position %d, upstream committed watermark at kill time %d) - "+
				"the ack-follows-durable-flush ordering in pkg/connector/source.go "+
				"(Approach A) should make this structurally unreachable.\n%s\n%s",
			tc.name, prune, resumePos, watermarkAtKill, gapLine, second.diagnostics(),
		)
	}

	_, corrupt := second.line(markerCorruptPo)
	is.True(!corrupt) // invariant 2: no torn/corrupted position on restart

	_, done := second.line(markerDone)
	is.True(done) // invariant 3: at-least-once delivery completed through total despite the kill

	finalWatermark, err := openUpstreamStore(cfg.upstreamDir, cfg.prune)
	is.NoErr(err)
	committed, err := finalWatermark.Committed()
	is.NoErr(err)
	is.Equal(committed, tc.total) // every position 1..total was durably committed exactly once by the end

	// Case-specific two-state resume shape (resumeShape's doc).
	switch tc.expectResume {
	case resumeEmpty:
		if resumePos != 0 {
			t.Fatalf(
				"Property 2 (%s, prune=%v): expected RESUME_POSITION to be empty/fresh "+
					"(kill landed before the first debounce flush), got %d - either the kill "+
					"timing no longer lands where this case's comment claims, or the persister's "+
					"debounce threshold changed underneath this test\n%s",
				tc.name, prune, resumePos, second.diagnostics(),
			)
		}
	case resumeValidNonZero:
		if resumePos == 0 {
			t.Fatalf(
				"Property 2 (%s, prune=%v): expected RESUME_POSITION to be a valid, non-zero, "+
					"stale checkpoint (kill landed after at least one debounce flush), got empty - "+
					"either the kill timing no longer lands where this case's comment claims, or the "+
					"persister's debounce threshold changed underneath this test\n%s",
				tc.name, prune, second.diagnostics(),
			)
		}
		if resumePos >= uint64(tc.killAfterReads) {
			t.Fatalf(
				"Property 2 (%s, prune=%v): expected RESUME_POSITION (%d) to be STALE - strictly "+
					"behind the kill point (read #%d) - a valid checkpoint that already caught up to "+
					"the kill point would mean this case no longer exercises a mid-flush window\n%s",
				tc.name, prune, resumePos, tc.killAfterReads, second.diagnostics(),
			)
		}
	}
}

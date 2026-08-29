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

	// persistDelayMS overrides the persister debounce for the FIRST (killed)
	// child only; 0 = default. The two resumeEmpty cases set it far beyond
	// any plausible scheduling delay so NO flush can ever land before the
	// kill - the "nothing durably persisted at kill time" precondition
	// becomes structural instead of a race against the ~1s automatic flush
	// (the #2534 pattern; see childConfig.persistDelayMS). The RESUMED child
	// always runs with the real default.
	persistDelayMS int

	// holdAt caps the FIRST (killed) child's production (the #2835 pattern;
	// see childConfig.holdAt and chaosPlugin.holdAt). Two structural roles:
	//   - the child can never produce - and therefore never ack or commit -
	//     anything past holdAt, bounding the kill's landing point above no
	//     matter how long the parent is descheduled; and
	//   - the child never reaches total, so its read loop blocks and it
	//     stays alive until SIGKILLed - the kill can never miss an exited
	//     process (an uncapped child that finishes its run and exits on its
	//     own would fail the kill instead of the test).
	// 0 = no cap. The RESUMED child always runs with it zeroed.
	holdAt uint64

	// Kill-timing gate: exactly one of these is set per case. All three are
	// LOWER-bound observations - the kill can never fire before the gate
	// returns - while the UPPER bound is structural in every case, which is
	// what makes the kill's exact landing point irrelevant to the verdict
	// (see assertProperty2Case's doc).
	killAfterReads     int    // mid-snapshot: gate on N observed READ lines
	killOnHandoff      bool   // mid-handoff: gate on the HANDOFF marker itself
	killAfterCommitted uint64 // mid-position-write: gate on the durable upstream commit watermark (waitForUpstreamCommittedAtLeast)

	// The two-state resume discriminator this case's kill timing is chosen
	// to land in - see resumeShape's doc.
	expectResume resumeShape
}

var property2Cases = []property2Case{
	{
		// Mid-snapshot: identical timing to DBZ-1's own "mid-snapshot" case
		// (sigkill_test.go) - an initial, fast, unpaced-ish burst (1ms/read).
		// The precondition is that Conduit has durably persisted NOTHING at
		// all when the kill lands - the highest-stakes edge case named in
		// the design doc: a crash before the snapshot watermark is durably
		// recorded at all - the engine-side reflection of the
		// conduit-connector-mysql #182 bug class.
		//
		// That used to be inferred from arithmetic ("30 reads x 1ms = ~30ms,
		// far short of the persister's FIRST automatic flush (~1000ms)"). On
		// a loaded CI box the parent can be descheduled for longer than that
		// window: the flush fires, a position IS persisted, and the case
		// fails (or, worse, the child finishes its whole run and the resumed
		// child starves - #2836's 41.6s hang). Both preconditions are now
		// STRUCTURAL: persistDelayMS (600s) means no flush can ever fire
		// before the kill, and holdAt caps production below total so the
		// child can never finish its run and exit before the kill lands.
		// The kill itself is still gated on observed READ progress as the
		// LOWER bound - the child has genuinely produced - but the verdict
		// no longer depends on how long the parent stalls around it.
		name:           "mid-snapshot",
		paceMS:         1,
		killAfterReads: 30,
		total:          500,
		persistDelayMS: 600_000,
		holdAt:         80,
		expectResume:   resumeEmpty,
	},
	{
		// Mid-handoff (producer-pacing variant, NOT a distinct engine
		// state - see the design doc's Property 2 section and resumeShape's
		// doc above). snapshotK=40 at 1ms/read means the HANDOFF marker
		// fires at ~40ms elapsed - killing on the marker itself
		// (waitForMarker), rather than a read count chosen to merely be
		// close to it, is what makes this genuinely "just after HANDOFF"
		// rather than a guess. The precondition is the SAME "empty"
		// persisted state as mid-snapshot above - still far short of the
		// first ~1000ms flush, and not a fictional third "boundary" shape -
		// and it is structural the same way: a 600s persister debounce
		// means no flush can ever land before the kill, and holdAt caps
		// production so the child stays alive until the kill lands.
		name:           "mid-handoff",
		snapshotK:      40,
		snapshotPaceMS: 1,
		paceMS:         15, // stream-phase pace; only ever reached up to holdAt
		total:          300,
		killOnHandoff:  true,
		persistDelayMS: 600_000,
		holdAt:         80,
		expectResume:   resumeEmpty,
	},
	{
		// Mid-position-write: identical timing to DBZ-1's own "mid-stream"
		// case (sigkill_test.go) - steady-state 15ms/read pacing. The
		// precondition is a valid, non-zero, STALE (behind the kill point)
		// checkpoint: at least one automatic flush has landed before the
		// kill, and the persisted position is from that flush, not caught
		// up to where the producer is.
		//
		// Both sides of that are now STRUCTURAL (the #2835 treatment). The
		// kill is gated on the child's DURABLE upstream commit watermark
		// reaching killAfterCommitted=70 (waitForUpstreamCommittedAtLeast) -
		// a flush has provably landed, so the resume position is provably
		// non-zero, no matter how slow the machine is. And holdAt caps the
		// producer at 100: nothing past it is ever produced or committed, so
		// the resume position can never catch up past the ceiling however
		// long the parent is descheduled - the old "resumePos <
		// killAfterReads" guard raced exactly this (a stalled parent let the
		// child blow through it, #2836). The watermark only moves when a
		// flush lands, so the kill lands at a flush boundary and the
		// checkpoint equals the watermark - which is exactly the no-gap
		// equality the shared assertions check.
		//
		// 70/100, not 40/120, is deliberate: at 15ms pace a flush covers
		// ~66 records (producer-paced, so load can only ever make it FEWER),
		// so the 70 gate deterministically fires at the SECOND flush, whose
		// uncapped coverage (~132) clears the 100 ceiling - removing the cap
		// makes the resume position jump to ~132 > 100 and this case fails
		// immediately instead of flaking back to life.
		name:               "mid-position-write",
		paceMS:             15,
		killAfterCommitted: 70,
		holdAt:             100,
		total:              400,
		expectResume:       resumeValidNonZero,
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
//
// # Why the kill point is bounded on both sides, and neither bound is a race
//
// Every case's kill timing used to be a parent-side observation of a
// FREE-RUNNING child: waitForReadCount/waitForMarker watched stdout, and the
// child kept producing while the parent was descheduled, so "I last saw N
// READs" said nothing about where the child was when SIGKILL actually
// landed. With an injected stall, mid-handoff and mid-position-write failed
// and mid-snapshot hung for ~41.6s (issue #2836) - the starvation
// sigkillCase's own doc comment describes. Each case now brackets its kill
// point with one observed LOWER bound and one STRUCTURAL upper bound, and
// every value inside the bracket yields the identical verdict:
//
//   - mid-snapshot, mid-handoff (resumeEmpty): the lower bound is the READ /
//     HANDOFF observation (the child has genuinely produced, and the
//     mid-handoff kill is genuinely post-boundary); the upper bound is
//     structural TWICE - a 600s persister debounce (persistDelayMS) means no
//     flush can ever land before the kill, so nothing is ever durably
//     persisted however long the parent stalls, and holdAt caps production so
//     the child can never finish its run and exit before the kill lands.
//
//   - mid-position-write (resumeValidNonZero): the kill is gated on the
//     child's DURABLE upstream commit watermark reaching killAfterCommitted,
//     read off the same on-disk marker the assertions read back after the
//     kill (waitForUpstreamCommittedAtLeast). The watermark only moves when
//     a persister flush lands, so a watermark at or past killAfterCommitted
//     is positive proof at least one flush has fired - the resume position
//     is provably non-zero. The upper bound is the holdAt ceiling: nothing
//     past it is ever produced, so nothing past it can ever be acked or
//     committed, and the resume position can never catch up past it however
//     long the parent is descheduled.
//
// The tunables (killAfterReads, killAfterCommitted, holdAt) therefore affect
// how deep the in-flight window is, never whether the test passes - which is
// exactly the difference between bounding a test and tuning one.
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

	// The structural knobs apply ONLY to the first (killed) child. The
	// RESUMED child must run with the real default debounce and no cap, or
	// it would never flush or run to completion either - which is not the
	// scenario under test, and would make the resume assertions vacuous.
	firstCfg := cfg
	firstCfg.persistDelayMS = tc.persistDelayMS
	firstCfg.holdAt = tc.holdAt

	first := spawnChild(t, firstCfg)
	switch {
	case tc.killOnHandoff:
		first.waitForMarker(t, markerHandoff, 30*time.Second)
	case tc.killAfterCommitted > 0:
		// Gate the kill on durable, flushed progress rather than a lagging
		// parent-side read count. This wait can never overshoot: firstCfg.holdAt
		// caps what this child can ever produce or commit, so the watermark is
		// provably in [killAfterCommitted, holdAt] when the kill lands.
		waitForUpstreamCommittedAtLeast(t, first, cfg.upstreamDir, tc.killAfterCommitted, 30*time.Second)
	default:
		first.waitForReadCount(t, tc.killAfterReads, 30*time.Second)
	}
	first.sigkill(t)

	committedAtKill, err := openUpstreamStore(cfg.upstreamDir, cfg.prune)
	is.NoErr(err)
	watermarkAtKill, err := committedAtKill.Committed()
	is.NoErr(err)

	// Make each case's PRECONDITION explicit and self-diagnosing (the
	// sigkill_test.go precedent, #2534): the structural bounds above should
	// make these unreachable, but assert them rather than trusting them, so
	// a future violation identifies itself instead of surfacing as a
	// confusing downstream mismatch. A run that lands outside its case's
	// precondition tests nothing - failing loudly here is the alternative
	// to passing vacuously.
	switch tc.expectResume {
	case resumeEmpty:
		if watermarkAtKill != 0 {
			t.Fatalf(
				"precondition violated: %s (prune=%v) requires NO persisted position at kill time, "+
					"but the upstream watermark was already %d. A persister flush beat the kill "+
					"despite a %dms debounce, so this run did not test the crash-before-first-"+
					"checkpoint scenario at all.\n%s",
				tc.name, prune, watermarkAtKill, tc.persistDelayMS, first.diagnostics(),
			)
		}
	case resumeValidNonZero:
		if watermarkAtKill < tc.killAfterCommitted {
			t.Fatalf(
				"precondition violated: %s (prune=%v) requires the kill to land after at least one "+
					"durable flush (watermark >= %d), but the watermark at kill time was %d - the "+
					"kill gate did not do what it claims.\n%s",
				tc.name, prune, tc.killAfterCommitted, watermarkAtKill, first.diagnostics(),
			)
		}
		if watermarkAtKill > tc.holdAt {
			t.Fatalf(
				"precondition violated: %s (prune=%v): the upstream watermark at kill time (%d) "+
					"exceeded the production ceiling holdAt (%d) - the child produced past its cap, "+
					"so the upper bound this case rests on is not in effect.\n%s",
				tc.name, prune, watermarkAtKill, tc.holdAt, first.diagnostics(),
			)
		}
	}

	second := spawnChild(t, cfg)
	second.waitExit(t, parentWaitExit)

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
					"(no flush could have landed before the kill: %dms debounce, cap %d), got %d - "+
					"either the persister's debounce threshold changed underneath this test, or "+
					"the structural precondition no longer holds\n%s",
				tc.name, prune, tc.persistDelayMS, tc.holdAt, resumePos, second.diagnostics(),
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
		// The structural form of the stale guard. The old check - resumePos
		// strictly behind killAfterReads - raced a free-running child: a
		// stalled parent let the child blow through it (#2836). Nothing past
		// the ceiling is ever produced, so nothing past it can ever be
		// persisted: resumePos <= holdAt holds by construction, and if
		// someone removes the cap this fails immediately and deterministically
		// (the uncapped child's watermark keeps advancing past the ceiling)
		// instead of flaking back to life.
		if resumePos > tc.holdAt {
			t.Fatalf(
				"Property 2 (%s, prune=%v): expected RESUME_POSITION (%d) to be at or behind the "+
					"production ceiling (holdAt %d) - a checkpoint that caught up past the ceiling "+
					"means the upper bound this case rests on is not in effect\n%s",
				tc.name, prune, resumePos, tc.holdAt, second.diagnostics(),
			)
		}
	}
}

// property2HoldStall is how long TestChild_HoldAt_CapsProductionBelowTotal
// deliberately does nothing after the producer reports it has hit its
// ceiling.
//
// This is not a "wait long enough and hope" sleep - it is the opposite, and
// the distinction matters because this package forbids the former. An
// unsound kill gate is one whose precondition decays as the parent is
// descheduled for longer; this sleep IS that descheduling, injected on
// purpose, and the assertions after it must hold no matter how large it
// gets - making it larger can only make an unsound cap fail harder. It is
// sized at 400ms because that is empirically enough for the uncapped
// producer this test guards against (paceMS 1) to run far past the 80
// ceiling the cap pins - i.e. enough for #2836's original failure mode to
// manifest. The sleep is adversarial, not load-bearing: an uncapped child
// fails the HELD-marker wait outright, and a child capped too high fails
// the position assertions.
const property2HoldStall = 400 * time.Millisecond

// TestChild_HoldAt_CapsProductionBelowTotal is #2836's regression test: it
// pins the production ceiling (chaosPlugin.holdAt) that
// TestSIGKILL_Property2_*'s resume-shape guards now rest on.
//
// Before the ceiling existed, those tests raced their own children: they
// observed read progress and then SIGKILLed, and the child kept producing
// throughout the window in between - a stalled parent could let the child
// blow through every kill-point guard (#2836). This test reproduces that
// window directly and adversarially: it waits for the producer to report
// its ceiling, then stalls for property2HoldStall (long enough that a
// ceiling-less child would have run far past the ceiling - exactly how the
// original flake was reproduced) and asserts the child has not moved.
//
// Run against a child without the cap, the marker wait below times out
// (the producer free-runs past the ceiling, printing READ lines instead),
// and the position assertions fail outright.
func TestChild_HoldAt_CapsProductionBelowTotal(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	cfg := childConfig{
		dbDir:       dir + "/db",
		upstreamDir: dir + "/upstream",
		paceMS:      1,
		total:       500,
		holdAt:      80,
	}
	// The ceiling only bounds anything if it is genuinely below total; the
	// child enforces this too (parseChildEnv), but state it here so the
	// scenario's own numbers can't drift into vacuity unnoticed.
	is.True(cfg.holdAt < cfg.total)

	child := spawnChild(t, cfg)
	child.waitForMarker(t, markerHeld+" ", 10*time.Second)
	is.Equal(maxProgressPosition(child, markerHeld), cfg.holdAt) // the marker reports the ceiling itself

	time.Sleep(property2HoldStall) // see property2HoldStall: adversarial, not load-bearing

	// Nothing beyond the ceiling was ever produced, however long we looked
	// away...
	is.True(maxProgressPosition(child, "READ") <= cfg.holdAt)

	// ...so nothing beyond it can ever have been acked and committed either,
	// which is the property the SIGKILL scenarios' upper-bound guards need.
	// Read while the child is still alive, exactly as the kill gate does.
	upstream, err := openUpstreamStore(cfg.upstreamDir, false)
	is.NoErr(err)
	committed, err := upstream.Committed()
	is.NoErr(err)
	is.True(committed <= cfg.holdAt)
	is.True(committed < cfg.total)

	child.sigkill(t) // crashable variant: it would otherwise block forever
}

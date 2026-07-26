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
// debounce flush has already happened (~1s, around read ~66) and a second
// debounce window has already started (on the next ack after that flush) but
// not yet fired - so there is a genuine deferred ack in flight when SIGTERM
// lands, which is exactly the window invariant 7 protects.
func TestSIGTERM_GracefulTeardown_DrainsDeferredAck(t *testing.T) {
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
		dbDir:       dir + "/db",
		upstreamDir: dir + "/upstream",
		prune:       false, // graceful path; prune-vs-durable is irrelevant here (Property 2 already covers that axis)
		paceMS:      paceMS,
		total:       total,
		sigtermMode: true,
	}

	cp := spawnChild(t, cfg)
	cp.waitForReadCount(t, killAfterReads, 30*time.Second)

	// Read the upstream's committed watermark BEFORE sending SIGTERM, so we
	// know exactly how much was durable before the graceful stop forced the
	// still-pending flush.
	upstreamBefore, err := openUpstreamStore(cfg.upstreamDir, cfg.prune)
	is.NoErr(err)
	committedBefore, err := upstreamBefore.Committed()
	is.NoErr(err)
	// The timing (see doc comment) is chosen so a flush has already
	// happened, but nowhere near killAfterReads worth of positions -
	// confirming there's genuinely a "deferred and not yet visible upstream"
	// gap for Teardown to close, not something that had already caught up
	// on its own.
	if committedBefore == 0 || committedBefore >= uint64(killAfterReads) {
		t.Fatalf(
			"test setup assumption violated: expected 0 < committedBefore(%d) < killAfterReads(%d) - "+
				"either the persister debounce timing changed, or this case's pacing no longer lands "+
				"in the intended mid-position-write window\n%s",
			committedBefore, killAfterReads, cp.diagnostics(),
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
	// Allow a small margin below killAfterReads: the producer's own pacing
	// means the very last read(s) before the signal may not have been acked
	// yet (an Ack call happens strictly after its Read), so the drained
	// position can be a few short of killAfterReads without indicating any
	// drop - what matters is that it advanced well past committedBefore,
	// not that it hit the read count exactly.
	const slack = 5
	if committedAfter+slack < uint64(killAfterReads) {
		t.Fatalf(
			"graceful SIGTERM teardown drained fewer acks than expected - committed only reached %d, "+
				"expected within %d of killAfterReads (%d) - the deferred ack pending at signal-time "+
				"looks like it was NOT fully flushed before the process exited\n%s",
			committedAfter, slack, killAfterReads, cp.diagnostics(),
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
	third.waitExit(t, 30*time.Second)

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

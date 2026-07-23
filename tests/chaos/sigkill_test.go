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
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matryer/is"
)

// TestMain intercepts the "am I a chaos child?" case before any actual Go
// test runs. See child.go/isChildInvocation and harness.go/spawnChild for the
// re-exec protocol.
func TestMain(m *testing.M) {
	if isChildInvocation() {
		runChild() // never returns; always os.Exit's
	}
	os.Exit(m.Run())
}

// sigkillCase drives a single SIGKILL scenario. paceMS and killAfterReads
// together determine where in the ack->commit->persist crash window
// (source.go's Ack, see doc.go) the kill lands: with the persister's real
// DefaultPersisterDelayThreshold (1s), waiting for killAfterReads records to
// be read off the stream at paceMS each guarantees at least
// killAfterReads*paceMS of genuine elapsed time (childProcess.
// waitForReadCount's guarantee - see its doc comment), so the numbers below
// are chosen to land reliably inside or across that 1s boundary without
// needing a deterministic kill-hook, matching the finding this workstream was
// built around.
//
// This keys off READ progress, not ACK progress (see waitForReadCount's doc
// for why): under the sev-0 fix (Approach A, docs/design-documents/
// 20260723-source-ack-persist-ordering-fix.md), the plugin's ack visibility
// is deliberately deferred behind connector.Persister's debounce, so "N acks
// observed" no longer tracks wall-clock-since-start at fine grain for a fast
// burst - a stale, ack-keyed version of this table drove the mid-snapshot
// case's entire read+ack loop to completion before the debounce's first
// flush ever fired, which starved chaosPlugin.produceLoop (nothing left to
// read after a full resume) and hung the second child indefinitely.
type sigkillCase struct {
	name           string
	paceMS         int
	killAfterReads int
	total          uint64
}

var sigkillCases = []sigkillCase{
	{
		// Mid-snapshot: an initial, fast burst (minimal pacing, mirroring
		// Debezium's initial full-table snapshot dumping many rows quickly).
		// killAfterReads=30 at 1ms/read is ~30ms in - well before the FIRST
		// automatic flush (which fires at ~1000ms), so Conduit has not
		// persisted ANY position yet when the kill lands. This is the
		// highest-stakes edge case named in the design doc: a crash before
		// the snapshot watermark is durably recorded at all.
		name:           "mid-snapshot",
		paceMS:         1,
		killAfterReads: 30,
		total:          500,
	},
	{
		// Mid-stream: steady-state pacing slow enough that, by the time we
		// reach killAfterReads=95 reads (~1.4s in), one automatic flush has
		// already happened (~1s, around read ~66) AND a second debounce
		// window has already started (on the next ack after that flush) but
		// not yet fired (its own 1s timer would land around read ~133). So
		// Conduit's persisted position is a valid but STALE checkpoint
		// (unlike the mid-snapshot case's "nothing at all"), which is the
		// other edge case named in the design doc.
		name:           "mid-stream",
		paceMS:         15,
		killAfterReads: 95,
		total:          400,
	},
}

// TestSIGKILL_PruningUpstream_NoGap is the sev-0 fix's central regression
// gate. It supersedes PR #2677's TestSIGKILL_PruningUpstream_ProducesGap,
// which demonstrated (and was designed to demonstrate) a genuine, structural
// gap in this exact crash window against a pruning upstream - see
// docs/postmortems/20260723-source-ack-persist-ordering.md for that finding.
// With the fix (docs/design-documents/20260723-source-ack-persist-ordering-fix.md,
// Approach A: the plugin ack is deferred until the resulting position is
// durably flushed, per-connector FIFO order preserved via source.go's
// pendingAcks/onPersistFlushed), the identical crash window - identical
// engine code, identical kill timing, identical pruning upstream that cannot
// redeliver behind its own last commit - must no longer be able to produce
// that gap: restarting must always resume from a position at or ahead of
// whatever the upstream has committed, never behind it.
//
// This test is verified to FAIL without the fix: temporarily reverting
// Source.Ack's ordering (moving the plugin ack back before persister.Persist)
// reproduces the exact OPEN_GAP_ERROR this test now asserts never happens -
// see the fix PR's description for that run's output.
func TestSIGKILL_PruningUpstream_NoGap(t *testing.T) {
	for _, tc := range sigkillCases {
		t.Run(tc.name, func(t *testing.T) {
			assertSigkillIsGapFree(t, tc, true)
		})
	}
}

// TestSIGKILL_DurableUpstream_NoGap is the counterfactual control for
// TestSIGKILL_PruningUpstream_NoGap: the IDENTICAL crash window, against the
// IDENTICAL engine code (pkg/connector.Source.Ack / Persister), but against
// an upstream that CAN redeliver behind its last commit (modeling a durable,
// replayable log such as Kafka). This upstream class was never able to
// produce a structural gap even before the fix (see the superseded PR
// #2677's TestSIGKILL_DurableUpstream_ProducesDuplicateNotGap) - keeping it
// here, with the fix applied, proves the fix didn't regress the
// already-safe case while closing the pruning-upstream one: both classes are
// now gap-free by the identical assertion, not by a different one per class.
func TestSIGKILL_DurableUpstream_NoGap(t *testing.T) {
	for _, tc := range sigkillCases {
		t.Run(tc.name, func(t *testing.T) {
			assertSigkillIsGapFree(t, tc, false)
		})
	}
}

// assertSigkillIsGapFree drives one SIGKILL scenario against an upstream of
// the given prune class and asserts the sev-0 invariants (1, 2, 3) all hold:
// no gap ever gets reported on resume (invariant 1: the plugin was never
// told to commit ahead of what's durable), no torn/corrupted position is
// ever read back (invariant 2), and the run still completes end-to-end
// despite the kill, committing every position exactly through total
// (invariant 3, at-least-once - duplicates are fine, gaps are not).
func assertSigkillIsGapFree(t *testing.T, tc sigkillCase, prune bool) {
	t.Helper()
	is := is.New(t)
	dir := t.TempDir()
	cfg := childConfig{
		dbDir:       dir + "/db",
		upstreamDir: dir + "/upstream",
		prune:       prune,
		paceMS:      tc.paceMS,
		total:       tc.total,
	}

	first := spawnChild(t, cfg)
	first.waitForReadCount(t, tc.killAfterReads, 30*time.Second)
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

	// The core verdict, and this workstream's whole point: Conduit's own
	// durably-persisted resume position must never be behind what it already
	// told the plugin (and, for a pruning upstream, therefore the plugin's
	// own irreversible commit) it could consider committed. Before the fix,
	// this could go either way depending on the upstream's prune behavior;
	// after it, it must hold unconditionally - see assertSigkillIsGapFree's
	// doc and the two callers for why the same assertion now covers both
	// upstream classes.
	is.True(resumePos >= watermarkAtKill)

	_, foundGap := second.line(markerOpenGap)
	if foundGap {
		gapLine, _ := second.line(markerOpenGap)
		t.Fatalf(
			"SEV-0 REGRESSION: chaosPlugin.Open reported a gap (resume position %d, "+
				"upstream committed watermark at kill time %d, prune=%v) - the "+
				"ack-follows-durable-flush ordering in pkg/connector/source.go (Approach A) "+
				"should make this structurally unreachable; re-verify Source.Ack/onPersistFlushed "+
				"before assuming this is safe.\n%s\n%s",
			resumePos, watermarkAtKill, prune, gapLine, second.diagnostics(),
		)
	}

	_, corrupt := second.line(markerCorruptPo)
	is.True(!corrupt) // invariant 2: no torn/corrupted position on restart

	_, done := second.line(markerDone)
	is.True(done) // invariant 3: at-least-once delivery completed through `total` despite the kill

	finalWatermark, err := openUpstreamStore(cfg.upstreamDir, cfg.prune)
	is.NoErr(err)
	committed, err := finalWatermark.Committed()
	is.NoErr(err)
	is.Equal(committed, tc.total) // every position 1..total was durably committed exactly once by the end
}

// parseResumePosition extracts the integer position from a "RESUME_POSITION
// <pos>" line (or 0 for "RESUME_POSITION" with an empty position, i.e. a
// genuinely fresh start with no persisted state at all).
func parseResumePosition(t *testing.T, line string) uint64 {
	t.Helper()
	fields := strings.Fields(line)
	// opencdc.Position.String() prints the literal "<nil>" for a nil
	// position (see conduit-commons/opencdc/position.go) - i.e. a genuinely
	// fresh start with nothing persisted yet.
	if len(fields) < 2 || fields[1] == "<nil>" {
		return 0
	}
	n, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		t.Fatalf("unparseable RESUME_POSITION line %q: %v", line, err)
	}
	return n
}

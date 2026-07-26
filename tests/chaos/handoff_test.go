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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matryer/is"
)

// TestSnapshotStreamHandoff_CleanRun_Smoke is DBZ-2 Property 1
// (docs/design-documents/20260726-dbz2-cdc-correctness-suite.md, Property 1
// section). Read that section before touching this test: it is deliberately
// NOT a test of "handoff atomicity". At the engine layer, snapshot and
// stream share one opaque, monotone SourceState.Position
// (pkg/connector/source.go:394) with no persisted boundary state between
// them - so a clean run producing every position 1..N exactly once, in
// order, is near-tautological (structurally unreachable to violate without
// a crash - see Property 2 for the crash cases that actually carry weight).
// This test exists as a cheap regression tripwire and as the fixture
// Property 2's mid-handoff case reuses (chaosPlugin's two-phase producer,
// upstream.go) - not as evidence that snapshot->stream handoff is
// crash-safe. Real handoff atomicity (two distinct position ENCODINGS plus a
// connector-internal mode switch) is a connector property, deferred to
// DBZ-3 and the DBZ-2 contract.
func TestSnapshotStreamHandoff_CleanRun_Smoke(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	const (
		snapshotK      = 50  // positions 1..50: fast "snapshot" burst
		snapshotPaceMS = 1   // ~50ms total for the burst
		paceMS         = 10  // positions 51..total: paced "stream" phase
		total          = 150 // 100 stream-phase records * 10ms ~= 1s total runtime
	)

	cfg := childConfig{
		dbDir:          dir + "/db",
		upstreamDir:    dir + "/upstream",
		prune:          false, // no crash in this scenario; prune-vs-durable is irrelevant here (Property 2 covers it)
		paceMS:         paceMS,
		snapshotK:      snapshotK,
		snapshotPaceMS: snapshotPaceMS,
		total:          total,
	}

	cp := spawnChild(t, cfg)
	cp.waitExit(t, 30*time.Second)

	_, done := cp.line(markerDone)
	is.True(done) // the run completed end to end without any crash

	// The HANDOFF marker exists for Property 2's kill-timing (mid-handoff),
	// not to signal a persisted engine state - see chaosPlugin's type doc
	// and produceLoop. On a clean run it must still appear exactly once,
	// at exactly the configured boundary, so this smoke test also serves as
	// a regression check on the marker itself.
	handoffLines := cp.linesWithPrefix(markerHandoff)
	is.Equal(len(handoffLines), 1)
	fields := strings.Fields(handoffLines[0])
	is.Equal(len(fields), 2)
	handoffPos, err := strconv.ParseUint(fields[1], 10, 64)
	is.NoErr(err)
	is.Equal(handoffPos, uint64(snapshotK))

	// The core smoke assertion: every position 1..total was delivered
	// exactly once, in order, with no gap and no duplicate. Since this is a
	// single, uninterrupted run of runReadAckLoop (no restart, no kill),
	// this is guaranteed by construction rather than by anything this test
	// discovers - see the doc comment above for why that's the honest
	// framing. It is asserted directly against the observed READ lines
	// (not just the final upstream watermark) so a future change that,
	// say, re-delivers or skips a position inside a single run would still
	// be caught here even though it can never reach the upstream-watermark
	// check in a way that looks wrong (a duplicate READ would still let
	// the watermark reach total).
	readLines := cp.linesWithPrefix("READ ")
	is.Equal(len(readLines), total)
	for i, l := range readLines {
		fields := strings.Fields(l)
		is.Equal(len(fields), 2)
		pos, err := strconv.ParseUint(fields[1], 10, 64)
		is.NoErr(err)
		is.Equal(pos, uint64(i+1)) // positions 1..total, strictly in order, no gap, no duplicate
	}

	// The persisted resume position advances monotonically to the end of
	// the run: the final upstream commit watermark reaches exactly total
	// (matching DBZ-1's own final-watermark check in sigkill_test.go).
	finalUpstream, err := openUpstreamStore(cfg.upstreamDir, cfg.prune)
	is.NoErr(err)
	committed, err := finalUpstream.Committed()
	is.NoErr(err)
	is.Equal(committed, uint64(total))
}

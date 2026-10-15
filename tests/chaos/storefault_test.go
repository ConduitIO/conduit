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

// TestSIGKILL_StoreFault_PruningUpstream_NoGap is the cross-process
// regression gate for #2925 (docs/postmortems/20261007-persister-store-error-ignored.md).
//
// The first child runs against a pruning (Postgres-slot-like) upstream with
// a store fault: after its first connector-state write, every position
// write fails inside an otherwise healthy badger transaction. Its producer
// is capped at holdAt, so it stays alive until the parent SIGKILLs it.
//
// Before the fix the persister logged the failure, committed the
// transaction anyway and told the source the write landed, so the deferred
// plugin acks were released: the upstream committed and pruned through
// holdAt while badger still held the old position. The restarted child
// then asked to resume from behind the pruned watermark: OPEN_GAP_ERROR.
//
// With the fix the failed flush reaches the source as an error, no ack is
// released, the upstream never commits past what badger holds, and the
// restarted child resumes with no gap and runs to total.
func TestSIGKILL_StoreFault_PruningUpstream_NoGap(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	cfg := childConfig{
		dbDir:          dir + "/db",
		upstreamDir:    dir + "/upstream",
		prune:          true,
		paceMS:         2,
		total:          60,
		persistDelayMS: 20,
	}

	first := cfg
	first.holdAt = 30
	first.storeFailAfter = 1
	child := spawnChild(t, first)
	child.waitForMarker(t, markerHeld+" ", 30*time.Second)

	// Give every flush the faulty run will attempt time to finish: many
	// debounce intervals past the producer stopping. Before the fix this is
	// when the upstream's watermark reaches holdAt. With the fix nothing may
	// move it past the stored position however long we wait, so the wait
	// only makes the pre-fix failure reliable; the passing condition does
	// not depend on it.
	time.Sleep(50 * time.Duration(cfg.persistDelayMS) * time.Millisecond)
	child.sigkill(t)

	upstream, err := openUpstreamStore(cfg.upstreamDir, cfg.prune)
	is.NoErr(err)
	watermarkAtKill, err := upstream.Committed()
	is.NoErr(err)

	second := spawnChild(t, cfg)
	second.waitExit(t, parentWaitExit)

	if gapLine, gap := second.line(markerOpenGap); gap {
		t.Fatalf("#2925 regression: acks were released for position writes that failed; the upstream "+
			"pruned through %d but the store held an older position.\n%s\n%s",
			watermarkAtKill, gapLine, second.diagnostics())
	}
	resumeLine, ok := second.line("RESUME_POSITION")
	is.True(ok)
	is.True(parseResumePosition(t, resumeLine) >= watermarkAtKill) // invariants 1/2: never resume behind the upstream

	_, done := second.line(markerDone)
	is.True(done) // invariant 3: delivery completes through total once the fault clears

	final, err := openUpstreamStore(cfg.upstreamDir, cfg.prune)
	is.NoErr(err)
	committed, err := final.Committed()
	is.NoErr(err)
	is.Equal(committed, cfg.total)
}

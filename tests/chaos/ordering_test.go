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

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/matryer/is"
)

// TestPerPartitionOrdering_AckFIFO is DBZ-2 Property 3
// (docs/design-documents/20260726-dbz2-cdc-correctness-suite.md, Property 3
// section). Unlike Property 2, this is NOT a crash-safety test - it never
// restarts and never touches upstreamStore's commit/prune/gap machinery
// (see chaosPlugin's type doc, upstream.go). It asserts the engine's
// ack-DELIVERY-order guarantee: pkg/connector/source.go's onPersistFlushed
// drains pendingAcks from the head, strictly by ascending nextAckSeq
// (source.go:450-465), so the plugin must receive acks in exactly
// Source.Ack call order, one Ack-call's positions at a time - never
// reordered, never batched out of order, even across a debounce flush
// boundary or an out-of-order flush confirmation (durableAckSeq only ever
// advances - see onPersistFlushed's doc comment).
//
// This is a real per-key delivery LEDGER (every ACK_ORDER line, not just a
// max-position watermark), per the design doc's explicit warning that
// position-only gap detection cannot see intra-batch reordering: a plugin
// that received key k0's positions 3 and 2 out of order, but whose maximum
// acked position for k0 still ends at N, would look identical to a correct
// run under a max-position check. The assertions below would catch that.
func TestPerPartitionOrdering_AckFIFO(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	const (
		numKeys = 4
		perKey  = 40
		total   = numKeys * perKey // 160
		paceMS  = 20               // total runtime ~= total*paceMS = 3.2s, crossing several
		// DefaultPersisterDelayThreshold (1s) debounce windows - chosen so
		// "no reorder across a debounce flush" is actually exercised
		// multiple times, not just trivially true of a single end-of-run
		// flush. The assertions below hold regardless of how many flushes
		// actually occur (this property must hold for 0, 1, or many), so a
		// slower CI machine can only make this MORE likely to cross
		// multiple windows, never invalidate the test.
	)

	cfg := childConfig{
		dbDir:       dir + "/db",
		upstreamDir: dir + "/upstream",
		prune:       false, // irrelevant here - Property 3 has no crash/resume path (see chaosPlugin's type doc)
		paceMS:      paceMS,
		total:       total,
		numKeys:     numKeys,
	}

	cp := spawnChild(t, cfg)
	cp.waitExit(t, 60*time.Second)

	_, done := cp.line(markerDone)
	is.True(done)

	entries := parseAckOrderLedger(t, cp.linesWithPrefix(markerAckOrder))
	is.Equal(len(entries), total) // every position was acked - no drop, no extra

	// 1. Full ledger, per key: every seq 1..perKey appears EXACTLY once.
	// This is the "real delivery ledger, not just a max-position counter"
	// property the design doc calls for - it catches both gaps AND
	// duplicates within a key, not just whether the maximum reached perKey.
	seen := make([][]bool, numKeys)
	for k := range seen {
		seen[k] = make([]bool, perKey+1) // 1-indexed
	}
	for _, e := range entries {
		if e.key < 0 || e.key >= numKeys {
			t.Fatalf("ACK_ORDER ledger: out-of-range key %d (entry %+v)", e.key, e)
		}
		if e.seq < 1 || e.seq > perKey {
			t.Fatalf("ACK_ORDER ledger: out-of-range seq %d for key %d (entry %+v)", e.seq, e.key, e)
		}
		if seen[e.key][e.seq] {
			t.Fatalf("ACK_ORDER ledger: DUPLICATE ack for key %d seq %d - the plugin was acked twice "+
				"for the same position, which a max-position-only check would never catch", e.key, e.seq)
		}
		seen[e.key][e.seq] = true
	}
	for k := 0; k < numKeys; k++ {
		for s := 1; s <= perKey; s++ {
			if !seen[k][s] {
				t.Fatalf("ACK_ORDER ledger: GAP - key %d seq %d was never acked", k, s)
			}
		}
	}

	// 2. Strict-FIFO-one-Ack-call-at-a-time (Q4's wrapper-seam contract):
	// arrival must be non-decreasing across the ledger in the order the
	// plugin actually received it (i.e. the order these lines were
	// printed - linesWithPrefix preserves emission order). A regression
	// that let onPersistFlushed send a later seq's ack before an earlier
	// one still pending would show up here as a decrease.
	var lastArrival uint64
	for i, e := range entries {
		if i > 0 && e.arrival < lastArrival {
			t.Fatalf("ACK_ORDER ledger: arrival order regression at entry %d (%+v) - arrival %d "+
				"came after %d; onPersistFlushed must drain pendingAcks strictly by ascending seq "+
				"(source.go:450-465)", i, e, e.arrival, lastArrival)
		}
		lastArrival = e.arrival
	}

	// 3. Per-key monotonicity in DELIVERY order (invariant 4 / Property 3's
	// core assertion): within each key, the seq values in the order the
	// plugin actually received them must be strictly increasing. This is
	// the assertion that would fail if onPersistFlushed ever delivered a
	// LATER Ack-call's positions before an EARLIER one for the same key -
	// exactly the "no reorder across a debounce flush" property named in
	// the design doc.
	lastSeqPerKey := make([]uint64, numKeys)
	for _, e := range entries {
		if e.seq <= lastSeqPerKey[e.key] {
			t.Fatalf("ACK_ORDER ledger: per-key ordering regression - key %d received seq %d after "+
				"already having received seq %d; per-partition ordering (invariant 4) requires acks "+
				"delivered to the plugin in exactly Source.Ack call order (source.go:396-406)",
				e.key, e.seq, lastSeqPerKey[e.key])
		}
		lastSeqPerKey[e.key] = e.seq
	}
	for k := 0; k < numKeys; k++ {
		is.Equal(lastSeqPerKey[k], uint64(perKey)) // every key's delivery ends at its final position
	}
}

// ackOrderEntry is one parsed "ACK_ORDER <arrival> <pos>" line.
type ackOrderEntry struct {
	arrival uint64
	key     int
	seq     uint64
}

// parseAckOrderLedger parses chaosPlugin.ackLoop's ACK_ORDER lines
// (upstream.go) into the ordered per-key delivery ledger this test asserts
// on. The returned slice preserves the lines' original order, i.e. the
// exact order the plugin's ackLoop goroutine received and processed them -
// see linesWithPrefix's doc for why that order is meaningful here.
func parseAckOrderLedger(t *testing.T, lines []string) []ackOrderEntry {
	t.Helper()
	entries := make([]ackOrderEntry, 0, len(lines))
	for _, l := range lines {
		fields := strings.Fields(l)
		if len(fields) != 3 {
			t.Fatalf("malformed ACK_ORDER line %q", l)
		}
		arrival, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			t.Fatalf("malformed ACK_ORDER line %q: %v", l, err)
		}
		key, seq, err := decodeKeyedPosition(opencdc.Position(fields[2]))
		if err != nil {
			t.Fatalf("malformed ACK_ORDER line %q: %v", l, err)
		}
		entries = append(entries, ackOrderEntry{arrival: arrival, key: key, seq: seq})
	}
	return entries
}

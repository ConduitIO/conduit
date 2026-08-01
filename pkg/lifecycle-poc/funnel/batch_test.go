// Copyright © 2025 Meroxa, Inc.
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

package funnel

import (
	"slices"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
)

func TestNewBatch(t *testing.T) {
	is := is.New(t)
	records := randomRecords(2)
	batch := NewBatch(slices.Clone(records))

	is.Equal(batch.records, records)
	is.Equal(batch.recordStatuses, []RecordStatus{{Flag: RecordFlagAck}, {Flag: RecordFlagAck}})
	is.Equal(batch.positions, []opencdc.Position{opencdc.Position("0"), opencdc.Position("1")})
	is.Equal(batch.splitRecords, nil)
	is.True(!batch.tainted)
	is.Equal(0, batch.filterCount)
}

func TestBatch_Ack(t *testing.T) {
	is := is.New(t)
	records := randomRecords(2)
	batch := NewBatch(slices.Clone(records))

	batch.Retry(0)
	is.Equal(batch.recordStatuses[0].Flag, RecordFlagRetry)

	batch.Ack(0)
	is.Equal(batch.recordStatuses[0].Flag, RecordFlagAck)
}

func TestBatch_Nack(t *testing.T) {
	is := is.New(t)
	records := randomRecords(2)
	batch := NewBatch(slices.Clone(records))

	is.Equal(batch.recordStatuses[0].Flag, RecordFlagAck)
	is.True(!batch.tainted)

	wantErr := cerrors.New("test error")
	batch.Nack(0, wantErr)

	is.Equal(batch.recordStatuses[0].Flag, RecordFlagNack)
	is.Equal(batch.recordStatuses[0].Error, wantErr)
	is.True(batch.tainted)
}

func TestBatch_Retry(t *testing.T) {
	is := is.New(t)
	records := randomRecords(2)
	batch := NewBatch(slices.Clone(records))

	is.Equal(batch.recordStatuses[0].Flag, RecordFlagAck)
	is.True(!batch.tainted)

	batch.Retry(0)

	is.Equal(batch.recordStatuses[0].Flag, RecordFlagRetry)
	is.True(batch.tainted)
}

func TestBatch_Filter(t *testing.T) {
	is := is.New(t)
	records := randomRecords(2)
	batch := NewBatch(slices.Clone(records))

	is.Equal(batch.recordStatuses[0].Flag, RecordFlagAck)
	is.Equal(batch.filterCount, 0)
	batch.Filter(0)
	is.Equal(batch.recordStatuses[0].Flag, RecordFlagFilter)
	is.Equal(batch.filterCount, 1)
}

func TestBatch_SetRecords_Simple(t *testing.T) {
	is := is.New(t)
	records := randomRecords(2)
	batch := NewBatch(slices.Clone(records))

	newRecords := randomRecords(2)

	is.Equal(batch.records, records)
	// Overwrite all
	batch.SetRecords(0, newRecords)
	is.Equal(batch.records, newRecords)
	// Overwrite one
	batch.SetRecords(1, records[:1])
	is.Equal(batch.records, []opencdc.Record{newRecords[0], records[0]})
}

func TestBatch_SetRecords_Filtered(t *testing.T) {
	is := is.New(t)
	records := randomRecords(3)
	batch := NewBatch(slices.Clone(records))
	batch.Filter(1)

	newRecords := randomRecords(2)

	is.Equal(batch.records, records)
	// Overwrite all
	batch.SetRecords(0, newRecords)
	is.Equal(batch.ActiveRecords(), newRecords)
	// Overwrite one
	batch.SetRecords(1, records[:1])
	is.Equal(batch.ActiveRecords(), []opencdc.Record{newRecords[0], records[0]})
}

func TestBatch_SplitRecord(t *testing.T) {
	is := is.New(t)
	records := randomRecords(2)
	batch := NewBatch(slices.Clone(records))

	splitRecords := randomRecords(2)
	batch.SplitRecord(1, splitRecords)

	is.Equal(len(batch.records), 3)
	is.Equal(batch.records[1:], splitRecords)
	is.True(batch.positions[2] == nil)
	pos := records[1].Position.String()
	is.Equal(batch.splitRecords[pos], records[1])
}

func TestBatch_Nack_SplitRecord(t *testing.T) {
	is := is.New(t)
	records := randomRecords(2)
	batch := NewBatch(slices.Clone(records))

	splitRecords := randomRecords(2)
	batch.SplitRecord(1, splitRecords)

	wantErr := cerrors.New("test error")
	batch.Nack(1, wantErr) // Nack a split record

	is.Equal(batch.recordStatuses[1].Flag, RecordFlagNack)
	is.Equal(batch.recordStatuses[1].Error, wantErr)
	is.Equal(batch.recordStatuses[2].Flag, RecordFlagNack)
	is.Equal(batch.recordStatuses[2].Error, wantErr)
}

func TestBatch_HasActiveRecords(t *testing.T) {
	is := is.New(t)
	records := []opencdc.Record{{}, {}}
	batch := NewBatch(slices.Clone(records))

	is.True(batch.HasActiveRecords())
	batch.Filter(0)
	is.True(batch.HasActiveRecords())
	batch.Filter(0)
	is.True(!batch.HasActiveRecords())
}

func TestBatch_ActiveRecords(t *testing.T) {
	is := is.New(t)
	records := []opencdc.Record{
		{Position: opencdc.Position("foo")},
		{Position: opencdc.Position("bar")},
	}
	batch := NewBatch(slices.Clone(records))

	is.Equal(batch.ActiveRecords(), records)
	batch.Filter(0)
	is.Equal(batch.ActiveRecords(), records[1:])
}

func TestBatch_ActiveRecordIndices(t *testing.T) {
	is := is.New(t)
	records := []opencdc.Record{{}, {}}
	batch := NewBatch(slices.Clone(records))

	is.Equal(batch.activeRecordIndices(), nil)
	batch.Filter(0)
	is.Equal(batch.activeRecordIndices(), []int{1})
}

func TestBatch_OriginalBatch(t *testing.T) {
	is := is.New(t)
	records := randomRecords(1)
	batch := NewBatch(slices.Clone(records))
	wantOriginal := batch.clone()

	splitRecords := []opencdc.Record{{}, {}}
	batch.SplitRecord(0, splitRecords)

	gotOriginal := batch.originalBatch()
	is.Equal(wantOriginal, gotOriginal)
}

func TestBatch_Clone(t *testing.T) {
	is := is.New(t)
	records := []opencdc.Record{
		{Position: opencdc.Position("pos1")},
		{Position: opencdc.Position("pos2")},
		{Position: opencdc.Position("pos3")},
	}
	batch := NewBatch(slices.Clone(records))

	clonedBatch := batch.clone()
	is.Equal(batch.records, clonedBatch.records)
	is.Equal(batch.recordStatuses, clonedBatch.recordStatuses)
	is.Equal(batch.positions, clonedBatch.positions)
	is.Equal(batch.filterCount, clonedBatch.filterCount)
	is.Equal(batch.tainted, clonedBatch.tainted)

	// Filtering, nacking, splitting should affect the cloned batch
	nackErr := cerrors.New("test error")
	clonedBatch.Filter(0) // NB: indices are -1 from this point on
	clonedBatch.Nack(0, nackErr)
	clonedBatch.SplitRecord(1, []opencdc.Record{{}, {}})
	clonedBatch.Retry(2)

	is.True(clonedBatch.tainted)
	is.True(clonedBatch.filterCount == 1)
	is.Equal(clonedBatch.records, []opencdc.Record{
		{Position: opencdc.Position("pos1")},
		{Position: opencdc.Position("pos2")},
		{},
		{},
	})
	// Indices 2 and 3 are the two pieces of the SplitRecord(1, ...) call
	// above (raw indices 2,3 - pos3 split into 2). clonedBatch.Retry(2)
	// (active index 2, raw index 3) marks only the SECOND piece, but Retry
	// propagates across a split run (setFlagEscalating/splitRunFlagTier), so
	// index 2 (the run's other piece, otherwise left at the default Ack) is
	// escalated to Retry too - this is the #2723 fix: a split run must never
	// carry a mix of Ack and Retry, or Worker.subBatchByFlag could later
	// partition it and ack the run's head before its tail is delivered.
	is.Equal(clonedBatch.recordStatuses, []RecordStatus{
		{Flag: RecordFlagFilter},
		{Flag: RecordFlagNack, Error: nackErr},
		{Flag: RecordFlagRetry},
		{Flag: RecordFlagRetry},
	})
	is.Equal(clonedBatch.positions, []opencdc.Position{
		opencdc.Position("pos1"),
		opencdc.Position("pos2"),
		opencdc.Position("pos3"),
		nil,
	})
	is.Equal(clonedBatch.splitRecords, map[string]opencdc.Record{
		"pos3": records[2],
	})

	// It shouldn't affect the original batch
	is.True(!batch.tainted)
	is.True(batch.filterCount == 0)
	is.Equal(batch.records, records)
	is.Equal(batch.recordStatuses, []RecordStatus{
		{Flag: RecordFlagAck},
		{Flag: RecordFlagAck},
		{Flag: RecordFlagAck},
	})
	is.Equal(batch.positions, []opencdc.Position{
		opencdc.Position("pos1"),
		opencdc.Position("pos2"),
		opencdc.Position("pos3"),
	})
	is.Equal(batch.splitRecords, nil)
}

// TestBatch_Clone_PositionsAliasing is the regression test for edge (j) named
// in the arch-v2 multi-connector slice 3a plan: clone() must never let a
// SplitRecord call on one fan-out branch corrupt a sibling branch's
// (or the original batch's) view of its own positions.
//
// Before the fix, clone() shared b.positions verbatim - same length AND same
// capacity as the original. Go's append reuses spare backing-array capacity
// whenever it's available, and slicing to a shorter length does not reduce
// capacity, so `append(b.positions[:i+1], newElems...)` (SplitRecord's own
// implementation) could silently overwrite index i+1 (and beyond) of the
// SAME backing array every branch's clone still pointed to - a data race
// with a data-loss blast radius, since a corrupted position could reach
// Source.Ack (invariant 2).
//
// The corruption needs the shared array to have SPARE capacity (cap > len)
// to reproduce: a batch fresh out of NewBatch always has cap == len (no
// headroom), so a single split on it always reallocates regardless of the
// bug - it can never trigger the very first time. Go's append growth
// strategy over-allocates once a slice DOES grow, though (confirmed
// empirically: splitting a freshly-built 5-position batch once yields
// cap=10, len=7), so the reachable scenario - and the one this test
// reproduces - is a SHARED upstream processor splitting a record before the
// destination fan-out point (giving the shared batch spare capacity), which
// is then cloned into M branches that split further, independently, on top
// of that same spare-capacity array.
func TestBatch_Clone_PositionsAliasing(t *testing.T) {
	is := is.New(t)

	newPreSplitBatch := func() *Batch {
		b := NewBatch(randomRecords(5))
		b.SplitRecord(1, randomRecords(3)) // gives b.positions spare capacity (cap > len) - see doc comment
		if cap(b.positions) == len(b.positions) {
			t.Fatalf("test precondition failed: expected spare capacity after a split, got cap==len==%d", len(b.positions))
		}
		return b
	}

	original := newPreSplitBatch()
	branchA := original.clone()
	branchB := original.clone()

	wantBPositions := slices.Clone(branchB.positions)
	wantOriginalPositions := slices.Clone(original.positions)

	// Split a record on branch A only, indexed into the shared array's spare
	// capacity. If clone() shared the backing array without capping
	// capacity, this append reuses (and corrupts) it - branchB's and
	// original's positions would silently change despite neither being
	// touched directly.
	branchA.SplitRecord(3, randomRecords(2))

	is.Equal(branchB.positions, wantBPositions)
	is.Equal(original.positions, wantOriginalPositions)

	// The concurrent version of the same scenario, run under -race: M
	// branches split records from the same pre-split source AT THE SAME
	// TIME, as doNextTask's real fan-out pool does. Without slices.Clip in
	// clone(), this is a genuine data race on the shared backing array (not
	// just a logical corruption a serial test might miss), so this loop
	// gives the race detector many chances to catch it directly.
	for i := 0; i < 50; i++ {
		base := newPreSplitBatch()
		a := base.clone()
		b := base.clone()

		done := make(chan struct{}, 2)
		go func() {
			defer func() { done <- struct{}{} }()
			a.SplitRecord(3, randomRecords(2))
		}()
		go func() {
			defer func() { done <- struct{}{} }()
			b.SplitRecord(4, randomRecords(2))
		}()
		<-done
		<-done
	}
}

// TestBatch_Clone_PreservesSplitRecords guards the second bug clone() had
// before this fix: it silently dropped splitRecords entirely (returned nil),
// losing the ability to restore a record's true original (pre-split)
// content via originalBatch() once cloned - relevant whenever a SHARED
// processor (running before a destination fan-out point) already split a
// record before the batch is cloned per branch. clone() must carry over a
// COPY of the map (not the same map reference: two branches calling
// SplitRecord concurrently on a shared map is a fatal concurrent-map-write
// crash, not just a benign race).
func TestBatch_Clone_PreservesSplitRecords(t *testing.T) {
	is := is.New(t)
	records := randomRecords(3)
	batch := NewBatch(slices.Clone(records))
	originalPositions := slices.Clone(batch.positions) // pre-split, for comparison below

	// Simulate a shared upstream processor splitting record 1 BEFORE the
	// fan-out point clones the batch.
	batch.SplitRecord(1, randomRecords(2))
	is.Equal(len(batch.splitRecords), 1)

	branchA := batch.clone()
	branchB := batch.clone()

	// Both branches see the split, and it collapses back to the true
	// original record via originalBatch() on EITHER branch independently.
	is.Equal(len(branchA.splitRecords), 1)
	is.Equal(len(branchB.splitRecords), 1)

	origFromA := branchA.originalBatch()
	origFromB := branchB.originalBatch()
	// Collapsing removes the split fragment (position nil), so the 4
	// records/positions the split introduced collapse back down to the
	// original 3.
	is.Equal(origFromA.positions, originalPositions)
	is.Equal(origFromA.records[1], records[1]) // the true pre-split original, not a split fragment
	is.Equal(origFromB.records[1], records[1])

	// The two branches' maps are independent objects: further splitting on
	// branch A must not appear in branch B's map (this is what makes the
	// "copy, don't share" fix concurrency-safe - see
	// TestBatch_Clone_PositionsAliasing for the analogous positions check).
	branchA.SplitRecord(0, randomRecords(2))
	is.Equal(len(branchA.splitRecords), 2)
	is.Equal(len(branchB.splitRecords), 1)
}

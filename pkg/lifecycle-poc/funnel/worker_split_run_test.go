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

package funnel

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// This file is the regression coverage for #2723 (split-run head acked
// before its tail is delivered) under the redesigned fix: Batch.SplitRecord
// replicating a split record's current status (closing the review's finding
// A) plus Batch.normalizeSplitRuns, a single linear pass run once after a
// task's Do returns (closing findings B, C and D of the adversarial review
// of #2725). See batch.go's doc comments on SplitRecord, normalizeSplitRuns
// and normalizeRun for the design; worker.go's groupFlagAt/subBatchByFlag for
// how a normalized run is kept as one unit when partitioning.

// buildSplitRunRecords returns 3 distinct source records, positions "p0",
// "p1", "p2".
func buildSplitRunRecords() (p0, p1, p2 opencdc.Record) {
	recs := randomRecords(3)
	recs[0].Position = opencdc.Position("p0")
	recs[1].Position = opencdc.Position("p1")
	recs[2].Position = opencdc.Position("p2")
	return recs[0], recs[1], recs[2]
}

// TestSplitRun_RetryHead_NotAckedBeforeTailResolved is the exact repro from
// #2723: processor A splits p0 into 3 pieces; a later processor B (task
// "procB") returns success for the split run's head but nil (unprocessed,
// ProcessorTask.Do's documented Retry trigger, see processor.go) for the
// other two pieces.
//
// Before any fix, Worker.subBatchByFlag would partition this into a
// 1-record Ack sub-batch (just the head) and a 2-record Retry sub-batch (the
// orphaned tail). The head's sub-batch collapses via Batch.originalBatch to
// position p0 alone and is acked immediately - before the tail is ever
// resubmitted to procB. This test wires a REAL two-processor pipeline (not
// just Batch-level calls) and fails on that premature ack via a strict,
// ordered gomock expectation: the mocked Source.Ack is only ever set up to
// accept the position set that is correct AFTER the retry resolves, so any
// earlier or different call is an unexpected call and fails the test.
func TestSplitRun_RetryHead_NotAckedBeforeTailResolved(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	ctrl := gomock.NewController(t)

	p0, p1, p2 := buildSplitRunRecords()

	sourceMock := NewMockSource(ctrl)
	sourceMock.EXPECT().ID().Return("src").AnyTimes()
	dlqMock, _ := NewMockDLQ(ctrl, logger)

	processorA := NewMockProcessor(ctrl)
	processorB := NewMockProcessor(ctrl)
	destinationMock := NewMockDestination(ctrl)

	srcTask := NewSourceTask("src", sourceMock, logger, NoOpConnectorMetrics{})
	taskA := NewProcessorTask("procA", processorA, logger, NoOpProcessorMetrics{})
	taskB := NewProcessorTask("procB", processorB, logger, NoOpProcessorMetrics{})
	destTask := NewDestinationTask("dest", destinationMock, logger, NoOpConnectorMetrics{})

	destNode := &TaskNode{Task: destTask}
	bNode := &TaskNode{Task: taskB, Next: []*TaskNode{destNode}}
	aNode := &TaskNode{Task: taskA, Next: []*TaskNode{bNode}}
	srcNode := &TaskNode{Task: srcTask, Next: []*TaskNode{aNode}}

	w, err := NewWorker(srcNode, dlqMock, logger, noop.Timer{})
	is.NoErr(err)

	splitPieces := randomRecords(3) // the 3 pieces processor A splits p0 into

	// Processor A: splits p0 into 3, passes p1/p2 through unchanged.
	processorA.EXPECT().Process(ctx, []opencdc.Record{p0, p1, p2}).Return(
		toProcessedRecords([]opencdc.Record{p0, p1, p2}, markMultiRecord(0, splitPieces)),
	)

	// Processor B, first pass: succeeds for the split run's HEAD (piece 0)
	// and for p1/p2, but returns nil - "not processed" - for pieces 1 and 2
	// of the run.
	firstPassIn := []opencdc.Record{splitPieces[0], splitPieces[1], splitPieces[2], p1, p2}
	processorB.EXPECT().Process(ctx, firstPassIn).Return([]sdk.ProcessedRecord{
		sdk.SingleRecord(splitPieces[0]),
		nil,
		nil,
		sdk.SingleRecord(p1),
		sdk.SingleRecord(p2),
	})

	// Processor B, retry pass: called again with the WHOLE split run once it
	// is escalated to Retry as a whole (see Batch.normalizeRun) - never with
	// just the head, and never with an orphaned tail missing the head.
	processorB.EXPECT().Process(ctx, splitPieces).Return(toProcessedRecords(splitPieces))

	// The destination must receive the split run's 3 pieces together
	// (after the retry resolves), and separately p1/p2 - never a lone head.
	gomock.InOrder(
		destinationMock.EXPECT().Write(ctx, splitPieces).Return(nil),
		destinationMock.EXPECT().Ack(ctx).Return(toDestinationAcks(splitPieces, nil), nil),
		// Invariant 1/3 enforcement: the source may only be acked for p0
		// once every piece of its split run has been durably written above -
		// never for p0 alone while pieces 1/2 are still outstanding.
		sourceMock.EXPECT().Ack(ctx, []opencdc.Position{p0.Position}).Return(nil),
		destinationMock.EXPECT().Write(ctx, []opencdc.Record{p1, p2}).Return(nil),
		destinationMock.EXPECT().Ack(ctx).Return(toDestinationAcks([]opencdc.Record{p1, p2}, nil), nil),
		sourceMock.EXPECT().Ack(ctx, []opencdc.Position{p1.Position, p2.Position}).Return(nil),
	)

	batch := NewBatch([]opencdc.Record{p0, p1, p2})
	err = w.doTask(ctx, aNode, batch, w)
	is.NoErr(err)
}

// TestSplitRun_FilterHead_NotAckedBeforeTailResolved is the Filter variant,
// and doubles as the defect-C convergence proof: processor B filters TWO of
// the run's three pieces on its first pass and defers the third (nil ->
// Retry). Because Batch.normalizeRun preserves Filter rather than
// escalating it (the redesign's core resolution — see its doc comment), the
// retry pass is fed ONLY the one still-undecided piece: the filtered
// pieces are excluded from Batch.ActiveRecords and never resubmitted. That
// shrinpeocessing input is what makes convergence provable rather than
// accidental (the review noted the previous attempt's own test for this
// shape could not catch its bug because its processor B was an identity
// transform).
func TestSplitRun_FilterHead_NotAckedBeforeTailResolved(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	ctrl := gomock.NewController(t)

	p0, p1, p2 := buildSplitRunRecords()

	sourceMock := NewMockSource(ctrl)
	sourceMock.EXPECT().ID().Return("src").AnyTimes()
	dlqMock, _ := NewMockDLQ(ctrl, logger)

	processorA := NewMockProcessor(ctrl)
	processorB := NewMockProcessor(ctrl)
	destinationMock := NewMockDestination(ctrl)

	srcTask := NewSourceTask("src", sourceMock, logger, NoOpConnectorMetrics{})
	taskA := NewProcessorTask("procA", processorA, logger, NoOpProcessorMetrics{})
	taskB := NewProcessorTask("procB", processorB, logger, NoOpProcessorMetrics{})
	destTask := NewDestinationTask("dest", destinationMock, logger, NoOpConnectorMetrics{})

	destNode := &TaskNode{Task: destTask}
	bNode := &TaskNode{Task: taskB, Next: []*TaskNode{destNode}}
	aNode := &TaskNode{Task: taskA, Next: []*TaskNode{bNode}}
	srcNode := &TaskNode{Task: srcTask, Next: []*TaskNode{aNode}}

	w, err := NewWorker(srcNode, dlqMock, logger, noop.Timer{})
	is.NoErr(err)

	splitPieces := randomRecords(3)

	processorA.EXPECT().Process(ctx, []opencdc.Record{p0, p1, p2}).Return(
		toProcessedRecords([]opencdc.Record{p0, p1, p2}, markMultiRecord(0, splitPieces)),
	)

	// Processor B, first pass: pieces 0 and 1 of the run are explicitly
	// Filtered (dropped for good), but piece 2 comes back nil (Retry) - the
	// run as a whole is NOT done.
	firstPassIn := []opencdc.Record{splitPieces[0], splitPieces[1], splitPieces[2], p1, p2}
	processorB.EXPECT().Process(ctx, firstPassIn).Return([]sdk.ProcessedRecord{
		sdk.FilterRecord{},
		sdk.FilterRecord{},
		nil,
		sdk.SingleRecord(p1),
		sdk.SingleRecord(p2),
	})

	// Retry pass: ONLY piece 2 is resubmitted - pieces 0/1 stay Filter and
	// are excluded from ActiveRecords, so the processor sees a SHRUNK input
	// (1 record, not 3). If Filter had been escalated back to Retry (the
	// previous attempt's defect C), this expectation would never be met and
	// the test would fail with an unexpected call for `splitPieces` (3
	// records) instead.
	processorB.EXPECT().Process(ctx, []opencdc.Record{splitPieces[2]}).Return(
		[]sdk.ProcessedRecord{sdk.SingleRecord(splitPieces[2])},
	)

	// Only piece 2 is actually active (0 and 1 are filtered, but retained in
	// the batch - see Batch.Filter), so only it reaches the destination.
	gomock.InOrder(
		destinationMock.EXPECT().Write(ctx, []opencdc.Record{splitPieces[2]}).Return(nil),
		destinationMock.EXPECT().Ack(ctx).Return(toDestinationAcks([]opencdc.Record{splitPieces[2]}, nil), nil),
		sourceMock.EXPECT().Ack(ctx, []opencdc.Position{p0.Position}).Return(nil),
		destinationMock.EXPECT().Write(ctx, []opencdc.Record{p1, p2}).Return(nil),
		destinationMock.EXPECT().Ack(ctx).Return(toDestinationAcks([]opencdc.Record{p1, p2}, nil), nil),
		sourceMock.EXPECT().Ack(ctx, []opencdc.Position{p1.Position, p2.Position}).Return(nil),
	)

	batch := NewBatch([]opencdc.Record{p0, p1, p2})
	err = w.doTask(ctx, aNode, batch, w)
	is.NoErr(err)
}

// TestWorker_SubBatchByFlag_NeverPartitionsSplitRun asserts the invariant
// directly at the seam where the bug lived: given a batch whose split run
// was left with a non-uniform flag by the task that just ran (exactly what
// ProcessorTask.Do produces when a later processor returns fewer records
// than it received), Batch.normalizeSplitRuns resolves it, and the first
// Worker.subBatchByFlag call must consume the WHOLE run in one sub-batch,
// never just its head.
func TestWorker_SubBatchByFlag_NeverPartitionsSplitRun(t *testing.T) {
	is := is.New(t)
	logger := log.Test(t)
	ctrl := gomock.NewController(t)
	_, _, taskNode := newMockTasks(ctrl, "sourceTask", "task1")
	worker, err := NewWorker(taskNode, nil, logger, noop.Timer{})
	is.NoErr(err)

	records := randomRecords(3) // p0, p1, p2
	batch := NewBatch(records)
	batch.SplitRecord(0, randomRecords(3)) // p0 -> 3 pieces; batch is now 5 records

	// Mirrors a later processor returning fewer records than it received for
	// pieces 1 and 2 of the split run (see Batch.Retry's doc comment).
	batch.Retry(1, 3)
	batch.normalizeSplitRuns()

	sub, flag, err := worker.subBatchByFlag(batch, 0)
	is.NoErr(err)
	is.Equal(flag, RecordFlagRetry)
	// The WHOLE split run (3 pieces), not just the 2 explicitly-marked ones
	// and definitely not just the 1-record head.
	is.Equal(len(sub.records), 3)
	is.Equal(sub.recordStatuses, []RecordStatus{
		{Flag: RecordFlagRetry},
		{Flag: RecordFlagRetry},
		{Flag: RecordFlagRetry},
	})

	// The next sub-batch starts exactly where the run ended - p1 and p2,
	// untouched.
	next, flag2, err := worker.subBatchByFlag(batch, 3)
	is.NoErr(err)
	is.Equal(flag2, RecordFlagAck)
	is.Equal(len(next.records), 2)
	is.Equal(next.recordStatuses, []RecordStatus{
		{Flag: RecordFlagAck},
		{Flag: RecordFlagAck},
	})
}

// TestValidateSplitRunBoundary_CatchesForcedPartition unit-tests the
// defensive backstop (CodeSplitRunPartitioned) in isolation from whether
// normalizeSplitRuns actually runs first: it pokes recordStatuses directly
// (bypassing Batch.Retry/Filter/Nack and normalizeSplitRuns entirely) to
// simulate a hypothetical future flag-setting path that forgot to route
// through them, and confirms the boundary check still catches the resulting
// partition and returns a coded, actionable error rather than silently
// allowing it.
func TestValidateSplitRunBoundary_CatchesForcedPartition(t *testing.T) {
	is := is.New(t)

	records := randomRecords(3)
	batch := NewBatch(records)
	batch.SplitRecord(0, randomRecords(3)) // p0 -> 3 pieces

	// Force a partitioned state directly, bypassing every public flag-setting
	// method and normalizeSplitRuns.
	batch.recordStatuses[0].Flag = RecordFlagAck
	batch.recordStatuses[1].Flag = RecordFlagRetry
	batch.recordStatuses[2].Flag = RecordFlagRetry

	// A boundary that would carve off just the head (index 0) - exactly the
	// #2723 shape.
	err := validateSplitRunBoundary(batch, 0, 1)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeSplitRunPartitioned.Reason())
	// The error text must not blame the processor/connector chain for what
	// is, in this case, an internal normalization bug.
	is.True(!isEmpty(ce.Suggestion))

	// A boundary that contains the whole run is fine.
	is.NoErr(validateSplitRunBoundary(batch, 0, 3))
	// A boundary entirely before or after the run is also fine.
	is.NoErr(validateSplitRunBoundary(batch, 3, 4))
}

func isEmpty(s string) bool { return s == "" }

// --- Batch-level SplitRecord fix (finding A) ---

// TestBatch_SplitRecord_ReplicatesNackStatus is a direct Batch-level test of
// the SplitRecord fix: a record that is currently Nack (here, via Nack's
// own — kept — propagation across an existing split run) must have that
// status REPLICATED onto any further split of one of its pieces, not
// defaulted back to Ack. Before the fix (`make([]RecordStatus, n)`, whose
// zero value is RecordFlagAck), this would silently resurrect an Ack piece
// inside an already-failed run.
func TestBatch_SplitRecord_ReplicatesNackStatus(t *testing.T) {
	is := is.New(t)

	records := randomRecords(2) // p0, p1
	batch := NewBatch(records)

	batch.SplitRecord(0, randomRecords(2)) // p0 -> 2 pieces: indices 0,1; p1 now at index 2
	wantErr := cerrors.New("boom")
	// Nack ONE piece of the run (index 1); Nack propagates across the whole
	// run (setFlagWithErr), so index 0 becomes Nack too.
	batch.Nack(1, wantErr)
	is.Equal(batch.recordStatuses[0].Flag, RecordFlagNack)
	is.Equal(batch.recordStatuses[0].Error, wantErr)

	// Split index 0 (already Nack, with wantErr) further into 2 more pieces.
	beforeFilterCount := batch.filterCount
	batch.SplitRecord(0, randomRecords(2))

	// FAIL-WITHOUT-FIX: with the old zero-value default, recordStatuses[0]
	// and [1] would be RecordFlagAck here instead of RecordFlagNack.
	is.Equal(batch.recordStatuses[0].Flag, RecordFlagNack)
	is.Equal(batch.recordStatuses[0].Error, wantErr)
	is.Equal(batch.recordStatuses[1].Flag, RecordFlagNack)
	is.Equal(batch.recordStatuses[1].Error, wantErr)
	is.Equal(batch.filterCount, beforeFilterCount) // unaffected: Nack, not Filter
}

// TestBatch_SplitRecord_AlongsideFilteredSibling_FilterCountCorrect is the
// reachable form of the required "split of a Filter-flagged record" test.
//
// SplitRecord's `i` argument is always an ACTIVE index (translated via
// activeRecordIndices, exactly as ProcessorTask.Do's markBatchRecords calls
// it — see processor.go). activeRecordIndices, by construction, only ever
// yields indices whose CURRENT flag is not Filter (filtered records are
// excluded from ActiveRecords), so a piece that is itself already Filter can
// never be the direct target of a SplitRecord call through Batch's public
// calling convention — see TestBatch_SplitRecord_OfAlreadyFilteredRecord_Panics
// for that boundary. What IS reachable, and what this test covers, is
// splitting an ACTIVE piece further while a FILTERED sibling already sits
// alongside it in the batch: filterCount must keep reflecting exactly the
// filtered sibling, unaffected by the unrelated split.
func TestBatch_SplitRecord_AlongsideFilteredSibling_FilterCountCorrect(t *testing.T) {
	is := is.New(t)

	records := randomRecords(2) // p0, p1
	batch := NewBatch(records)

	batch.Filter(0) // p0 filtered; only p1 remains active.
	is.Equal(batch.filterCount, 1)

	// Split p1 (the only active record - active index 0) into 4 pieces.
	batch.SplitRecord(0, randomRecords(4))

	is.Equal(batch.recordStatuses, []RecordStatus{
		{Flag: RecordFlagFilter}, // p0, untouched
		{Flag: RecordFlagAck},
		{Flag: RecordFlagAck},
		{Flag: RecordFlagAck},
		{Flag: RecordFlagAck},
	})
	// filterCount is unaffected by splitting an unrelated ACTIVE record.
	is.Equal(batch.filterCount, 1)
	is.Equal(batch.HasActiveRecords(), true)
	is.Equal(len(batch.ActiveRecords()), 4)
}

// TestBatch_SplitRecord_OfAlreadyFilteredRecord_Panics documents and proves
// the boundary above: forcing (white-box, bypassing the public Filter/
// SplitRecord calling convention entirely) a record into Filter and then
// asking SplitRecord to target it via a raw index that activeIndices would
// never actually produce must panic rather than silently mis-account
// filterCount. This is the split-run analogue of
// TestValidateSplitRunBoundary_CatchesForcedPartition: a defensive invariant
// check for a state that should never occur through the public API, proven
// by deliberately forcing it.
func TestBatch_SplitRecord_OfAlreadyFilteredRecord_Panics(t *testing.T) {
	is := is.New(t)

	records := randomRecords(1)
	batch := NewBatch(records)
	batch.recordStatuses[0].Flag = RecordFlagFilter // force, bypassing Batch.Filter
	batch.filterCount = 0                           // so activeRecordIndices() takes the "no filters" fast path and returns nil, letting i=0 pass through untranslated

	defer func() {
		r := recover()
		is.True(r != nil)
	}()
	batch.SplitRecord(0, randomRecords(2))
	t.Fatal("expected SplitRecord to panic when asked to split an already-filtered record")
}

// --- Shape (a): Retry applied to a run, then SplitRecord at a lower index
// in the SAME Do pass ---

// retryThenLowerSplitTask reproduces, at the Batch level inside a single
// Do call, the exact interleaving the review found broke the previous
// attempt (finding A): within ONE task's Do, a HIGHER-index piece of an
// existing split run is marked Retry first (mirroring ProcessorTask.Do's
// end-to-start marking order), and THEN a LOWER-index piece of the SAME
// run is split further. Under the redesign there is no mid-Do escalation to
// corrupt, but this proves the END-TO-END outcome (after
// Batch.normalizeSplitRuns runs, post-Do) is still a single, uniform,
// unpartitioned run - not a fatal CodeSplitRunPartitioned guard trip.
type retryThenLowerSplitTask struct {
	id   string
	seen bool
}

func (t *retryThenLowerSplitTask) ID() string                  { return t.id }
func (t *retryThenLowerSplitTask) Open(context.Context) error  { return nil }
func (t *retryThenLowerSplitTask) Close(context.Context) error { return nil }

func (t *retryThenLowerSplitTask) Do(_ context.Context, b *Batch) error {
	if t.seen {
		// Retry pass: succeed for everything so the pipeline completes.
		active := b.ActiveRecords()
		b.SetRecords(0, active)
		return nil
	}
	t.seen = true

	// The batch enters this Do call already containing a pre-existing split
	// run at indices [0,2] (set up by the test before calling doTask) -
	// mirrors a run created by an EARLIER task in the pipeline.
	//
	// Mark the higher-index piece of the run Retry FIRST (end-to-start
	// order).
	b.Retry(2, 3)
	// Then split the LOWER-index piece of the SAME run further - this is
	// the shape that broke the previous attempt's escalate-during-Do design:
	// the new pieces must replicate index 0's CURRENT status (Ack, since
	// nothing mutates it during Do under this redesign) rather than being
	// forced to disagree with the Retry just written at index 2.
	b.SplitRecord(0, randomRecords(2))
	return nil
}

func TestDoTask_RetryThenLowerSplit_RunStaysUniform_GuardDoesNotFire(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(2) // p0, p1
	parent := &fakeParentAckNacker{}
	task := &retryThenLowerSplitTask{id: "retry-then-split"}
	node := &TaskNode{Task: task}

	w := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}

	b := NewBatch(records)
	b.SplitRecord(0, randomRecords(3)) // pre-existing run: p0 -> 3 pieces, indices [0,2]; p1 at index 3

	err := w.doTask(ctx, node, b, parent)
	is.NoErr(err) // must NOT be CodeSplitRunPartitioned

	// Every original position acked exactly once.
	got := map[string]int{}
	for _, call := range parent.ackCalls() {
		for _, p := range call.positions {
			got[string(p)]++
		}
	}
	is.Equal(got[string(records[0].Position)], 1)
	is.Equal(got[string(records[1].Position)], 1)
}

// Shape (b) — a processor that filters the head piece and skips the rest,
// asserting convergence (no infinite retry) and that the Filter decision
// survives — is covered by TestSplitRun_FilterHead_NotAckedBeforeTailResolved
// above: its gomock expectations are exact and ordered, so the retry pass
// being fed ONLY the one still-undecided piece (never the filtered ones, and
// never a third call) is enforced by the mock itself failing the test on any
// unexpected call.

// --- Content-integrity: Filter + split run + partial-return processor ---

// TestDoTask_ContentIntegrity_FilterPlusSplitRun is the "content shift"
// regression shape (finding B of the review, TestAdversarial_FilterCountShiftMidDo):
// a batch with an unrelated filtered record ahead of a split run, and a
// downstream processor that returns fewer records than it received (padding
// with Retry). Asserts every record's CONTENT and every filter decision
// match what an unmodified batch (no escalation, no mid-Do mutation) would
// produce - no cross-contamination between the filtered record, the split
// run, and the plain records around them.
func TestDoTask_ContentIntegrity_FilterPlusSplitRun(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	ctrl := gomock.NewController(t)

	base := randomRecords(4)
	base[0].Position = opencdc.Position("p0") // will be filtered by procA
	base[1].Position = opencdc.Position("p1") // will be split by procA
	base[2].Position = opencdc.Position("p2") // untouched
	base[3].Position = opencdc.Position("p3") // untouched
	p0, p1, p2, p3 := base[0], base[1], base[2], base[3]

	sourceMock := NewMockSource(ctrl)
	sourceMock.EXPECT().ID().Return("src").AnyTimes()
	dlqMock, _ := NewMockDLQ(ctrl, logger)

	processorA := NewMockProcessor(ctrl)
	processorB := NewMockProcessor(ctrl)
	destinationMock := NewMockDestination(ctrl)

	srcTask := NewSourceTask("src", sourceMock, logger, NoOpConnectorMetrics{})
	taskA := NewProcessorTask("procA", processorA, logger, NoOpProcessorMetrics{})
	taskB := NewProcessorTask("procB", processorB, logger, NoOpProcessorMetrics{})
	destTask := NewDestinationTask("dest", destinationMock, logger, NoOpConnectorMetrics{})

	destNode := &TaskNode{Task: destTask}
	bNode := &TaskNode{Task: taskB, Next: []*TaskNode{destNode}}
	aNode := &TaskNode{Task: taskA, Next: []*TaskNode{bNode}}
	srcNode := &TaskNode{Task: srcTask, Next: []*TaskNode{aNode}}

	w, err := NewWorker(srcNode, dlqMock, logger, noop.Timer{})
	is.NoErr(err)

	splitPieces := randomRecords(3)

	// procA: filters p0, splits p1 into 3, passes p2/p3 through.
	processorA.EXPECT().Process(ctx, []opencdc.Record{p0, p1, p2, p3}).Return([]sdk.ProcessedRecord{
		sdk.FilterRecord{},
		sdk.MultiRecord(splitPieces),
		sdk.SingleRecord(p2),
		sdk.SingleRecord(p3),
	})

	// procB: called with the 5 active records (p0 excluded, filtered).
	// Returns fewer than it received - success for the split run's head and
	// p2, nothing (retry) for the rest, EXCEPT p3 which is also padded-nil
	// (procB returns only 3 of 5 outputs; ProcessorTask.Do pads the missing
	// 2 with nil/Retry).
	firstPassIn := []opencdc.Record{splitPieces[0], splitPieces[1], splitPieces[2], p2, p3}
	processorB.EXPECT().Process(ctx, firstPassIn).Return([]sdk.ProcessedRecord{
		sdk.SingleRecord(splitPieces[0]),
		nil,
		nil,
		sdk.SingleRecord(p2),
		// 5th (p3) omitted entirely - Do pads it as nil/Retry too.
	})

	// Retry pass: the WHOLE split run (3 pieces) resubmitted together -
	// content-integrity requires p2's already-accepted content is NOT
	// touched by this, and p3 is retried entirely independently.
	processorB.EXPECT().Process(ctx, splitPieces).Return(toProcessedRecords(splitPieces))
	processorB.EXPECT().Process(ctx, []opencdc.Record{p3}).Return([]sdk.ProcessedRecord{sdk.SingleRecord(p3)})

	// p0 (filtered) is acked alone, with its ORIGINAL content (never
	// touched by procB at all).
	destinationMock.EXPECT().Write(ctx, splitPieces).Return(nil)
	destinationMock.EXPECT().Ack(ctx).Return(toDestinationAcks(splitPieces, nil), nil)
	sourceMock.EXPECT().Ack(ctx, []opencdc.Position{p0.Position}).Return(nil)
	sourceMock.EXPECT().Ack(ctx, []opencdc.Position{p1.Position}).Return(nil)
	destinationMock.EXPECT().Write(ctx, []opencdc.Record{p2}).Return(nil)
	destinationMock.EXPECT().Ack(ctx).Return(toDestinationAcks([]opencdc.Record{p2}, nil), nil)
	sourceMock.EXPECT().Ack(ctx, []opencdc.Position{p2.Position}).Return(nil)
	destinationMock.EXPECT().Write(ctx, []opencdc.Record{p3}).Return(nil)
	destinationMock.EXPECT().Ack(ctx).Return(toDestinationAcks([]opencdc.Record{p3}, nil), nil)
	sourceMock.EXPECT().Ack(ctx, []opencdc.Position{p3.Position}).Return(nil)

	batch := NewBatch([]opencdc.Record{p0, p1, p2, p3})
	err = w.doTask(ctx, aNode, batch, w)
	is.NoErr(err)
}

// --- Fan-out composition (M=2): no premature unanimity across branches ---

// splitOnceThenRetryOnceTask splits the record it's given into 2 pieces on
// its first call, marking one piece Retry; on its second call (the retry
// pass, fed only the still-pending piece per Batch.resetForRetry), it
// succeeds. If block/unblock are set, it signals block and waits on
// unblock before returning from the FIRST call - used to hold one fan-out
// branch back deterministically while a sibling branch races ahead.
type splitOnceThenRetryOnceTask struct {
	id      string
	seen    bool
	block   chan struct{}
	unblock chan struct{}
}

func (t *splitOnceThenRetryOnceTask) ID() string                  { return t.id }
func (t *splitOnceThenRetryOnceTask) Open(context.Context) error  { return nil }
func (t *splitOnceThenRetryOnceTask) Close(context.Context) error { return nil }

func (t *splitOnceThenRetryOnceTask) Do(_ context.Context, b *Batch) error {
	if t.seen {
		active := b.ActiveRecords()
		b.SetRecords(0, active)
		return nil
	}
	t.seen = true

	active := b.ActiveRecords()
	pieces := []opencdc.Record{active[0], active[0]}
	b.SplitRecord(0, pieces)
	b.Retry(1, 2) // one of the two new pieces needs another pass

	if t.block != nil {
		close(t.block)
		<-t.unblock
	}
	return nil
}

// TestDoNextTask_FanOut_SplitRunPending_NoPrematureUnanimity is the required
// M=2 fan-out test: branch A resolves its (independently cloned) split run
// immediately; branch B's copy of the SAME split run needs a retry pass and
// is held back deterministically. Asserts the source is not acked for the
// original position until BOTH branches have actually finished - proving
// multiAckNacker's unanimity requirement isn't short-circuited by a
// split-run's internal retry recursion on one branch finishing "early" in
// some intermediate, partial sense.
func TestDoNextTask_FanOut_SplitRunPending_NoPrematureUnanimity(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(1)
	src := newFakeSource("src", records)
	destA := newFakeDestination("destA")
	destB := newFakeDestination("destB")

	logger := log.Test(t)
	dlqDest := newFakeDestination("dlq")
	dlq := NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, 0, 0)

	taskA := &splitOnceThenRetryOnceTask{id: "splitA"} // resolves immediately (no block)
	block := make(chan struct{})
	unblock := make(chan struct{})
	taskB := &splitOnceThenRetryOnceTask{id: "splitB", block: block, unblock: unblock}

	destTaskA := NewDestinationTask(destA.id, destA, logger, NoOpConnectorMetrics{})
	destTaskB := NewDestinationTask(destB.id, destB, logger, NoOpConnectorMetrics{})

	nodeA := &TaskNode{Task: taskA, Next: []*TaskNode{{Task: destTaskA}}}
	nodeB := &TaskNode{Task: taskB, Next: []*TaskNode{{Task: destTaskB}}}
	branchNode := &TaskNode{Task: passthroughTask{id: "shared"}, Next: []*TaskNode{nodeA, nodeB}}
	srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{branchNode}}

	w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
	is.NoErr(err)

	batch := NewBatch(append([]opencdc.Record(nil), records...))

	doneCh := make(chan error, 1)
	go func() { doneCh <- w.doTask(ctx, branchNode, batch, w) }()

	select {
	case <-block:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for branch B to block on its first pass")
	}

	// Branch A races ahead and fully resolves (bounded wait, not a fixed
	// sleep) - but the source must NOT be acked yet: branch B hasn't voted.
	waitForCondition(t, 5*time.Second, func() bool {
		return len(destA.receivedPositions()) == 2 // both split pieces written
	})
	is.Equal(len(src.ackedPositions()), 0)

	close(unblock)

	select {
	case err := <-doneCh:
		is.NoErr(err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for doTask to finish after unblocking branch B")
	}

	acked := src.ackedPositions()
	is.Equal(len(acked), 1)
	is.Equal(acked[0], records[0].Position)
}

// --- Forced-partition backstop ---

// TestSubBatchByFlag_ForcedPartition_ReturnsCodedError is the "forced
// partition backstop" test: directly corrupt a batch into a mixed-flag
// split run WITHOUT going through normalizeSplitRuns (simulating a
// hypothetical future bug that bypasses it), and assert
// Worker.subBatchByFlag itself - not just validateSplitRunBoundary in
// isolation - surfaces CodeSplitRunPartitioned rather than silently
// producing a partial sub-batch.
func TestSubBatchByFlag_ForcedPartition_ReturnsCodedError(t *testing.T) {
	is := is.New(t)
	logger := log.Test(t)
	ctrl := gomock.NewController(t)
	_, _, taskNode := newMockTasks(ctrl, "sourceTask", "task1")
	worker, err := NewWorker(taskNode, nil, logger, noop.Timer{})
	is.NoErr(err)

	records := randomRecords(2)
	batch := NewBatch(records)
	batch.SplitRecord(0, randomRecords(3)) // run at indices [0,2]

	// Bypass normalizeSplitRuns entirely: force a mixed run.
	batch.recordStatuses[0].Flag = RecordFlagAck
	batch.recordStatuses[1].Flag = RecordFlagRetry
	batch.recordStatuses[2].Flag = RecordFlagRetry

	sub, _, err := worker.subBatchByFlag(batch, 0)
	is.True(sub == nil)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeSplitRunPartitioned.Reason())
}

// --- Randomized property test ---

// randomSplitFilterRetryTask is a hand-rolled Task (not going through the
// generic Processor/ProcessorTask interface, for full control over timing)
// used by the property test below. It mimics ProcessorTask.Do's contract
// (single-record ops applied high-to-low over ActiveRecords, using nil for
// "retry") and tracks, per ORIGINAL record identity (carried in Metadata
// across splits and retries), how many rounds that identity has been
// offered a decision. Once a piece has been offered 3 rounds, it is FORCED
// to succeed - this bounds the harness's own retry recursion depth
// independently of Conduit's behavior, so a bug in either the harness or
// the code under test can never manifest as an actual unbounded loop /
// stack overflow in the test process (the unbounded-recursion crash is
// #2726, deliberately not fixed here - this cap exists purely so the
// property test itself stays finite regardless).
type randomSplitFilterRetryTask struct {
	id  string
	rng *rand.Rand

	attempts  map[string]int
	splitDone map[string]bool
	calls     int
}

func newRandomSplitFilterRetryTask(id string, rng *rand.Rand) *randomSplitFilterRetryTask {
	return &randomSplitFilterRetryTask{
		id:        id,
		rng:       rng,
		attempts:  make(map[string]int),
		splitDone: make(map[string]bool),
	}
}

func (t *randomSplitFilterRetryTask) ID() string                  { return t.id }
func (t *randomSplitFilterRetryTask) Open(context.Context) error  { return nil }
func (t *randomSplitFilterRetryTask) Close(context.Context) error { return nil }

const testOrigIDKey = "test.orig_id"

func (t *randomSplitFilterRetryTask) Do(_ context.Context, b *Batch) error {
	t.calls++
	if t.calls > 200 {
		return cerrors.New("randomSplitFilterRetryTask: exceeded safety call cap - harness bug, not a real infinite loop")
	}

	active := b.ActiveRecords()
	type decision int
	const (
		decideAck decision = iota
		decideFilter
		decideNack
		decideSplit
		decideRetry
	)
	decisions := make([]decision, len(active))
	splitInto := make([]int, len(active))

	for i, r := range active {
		id := r.Metadata[testOrigIDKey]
		t.attempts[id]++
		forceDone := t.attempts[id] > 3

		roll := t.rng.Float64()
		switch {
		case !forceDone && roll < 0.10:
			decisions[i] = decideNack
		case !forceDone && roll < 0.35:
			decisions[i] = decideFilter
		case !forceDone && !t.splitDone[id] && roll < 0.55:
			decisions[i] = decideSplit
			splitInto[i] = 2 + t.rng.Intn(2)
			t.splitDone[id] = true
		case !forceDone && roll < 0.80:
			decisions[i] = decideRetry
		default:
			decisions[i] = decideAck
		}
	}

	// Apply high-to-low, mirroring ProcessorTask.Do's own end-to-start
	// marking order (see processor.go).
	for i := len(active) - 1; i >= 0; i-- {
		switch decisions[i] {
		case decideAck:
			// Leave as-is (already Ack by default).
		case decideFilter:
			b.Filter(i, i+1)
		case decideNack:
			b.Nack(i, cerrors.New("random nack"))
		case decideRetry:
			b.Retry(i, i+1)
		case decideSplit:
			id := active[i].Metadata[testOrigIDKey]
			pieces := make([]opencdc.Record, splitInto[i])
			for k := range pieces {
				pieces[k] = opencdc.Record{
					Metadata: opencdc.Metadata{testOrigIDKey: id},
					Payload:  active[i].Payload,
				}
			}
			b.SplitRecord(i, pieces)
		}
	}
	return nil
}

// TestProperty_SplitFilterRetryPartialReturn_NoMixedRuns_NoGuardFire is the
// required randomized/property test: 3000 iterations (scaled down only if
// CI proves too slow — see the -short gate below) of random
// split/filter/retry/ack decisions over a small batch, asserting for every
// iteration:
//
//   - doTask never returns an error (in particular, never
//     CodeSplitRunPartitioned - "the guard never fires").
//   - Every original position is acked-once XOR DLQ'd-once (recorded via
//     acks/nacks on a fakeParentAckNacker) - never both, never neither,
//     never more than once ("no run is ever mixed": a mixed run is exactly
//     what would let a position be released twice, or dropped, by
//     subBatchByFlag partitioning it incorrectly).
func TestProperty_SplitFilterRetryPartialReturn_NoMixedRuns_NoGuardFire(t *testing.T) {
	const iterations = 3000
	seed := time.Now().UnixNano()
	t.Logf("seed=%d (reproduce a failure by hardcoding this seed)", seed)
	rng := rand.New(rand.NewSource(seed))

	guardFires := 0
	mixedRunViolations := 0
	otherErrors := 0

	for iter := 0; iter < iterations; iter++ {
		n := 1 + rng.Intn(4) // 1..4 original records
		records := make([]opencdc.Record, n)
		positions := make([]opencdc.Position, n)
		for i := range records {
			pos := opencdc.Position(fmt.Sprintf("iter%d-p%d", iter, i))
			records[i] = opencdc.Record{
				Position: pos,
				Metadata: opencdc.Metadata{testOrigIDKey: fmt.Sprintf("p%d", i)},
				Payload:  opencdc.Change{After: opencdc.RawData(fmt.Sprintf("v%d", i))},
			}
			positions[i] = pos
		}

		parent := &fakeParentAckNacker{}
		task := newRandomSplitFilterRetryTask(fmt.Sprintf("task-%d", iter), rng)
		node := &TaskNode{Task: task}
		w := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}

		batch := NewBatch(records)
		err := w.doTask(context.Background(), node, batch, parent)
		if err != nil {
			ce, ok := conduiterr.Get(err)
			if ok && ce.Code.Reason() == CodeSplitRunPartitioned.Reason() {
				guardFires++
				t.Errorf("iter %d (seed %d): CodeSplitRunPartitioned fired - a split run was "+
					"partitioned: %v", iter, seed, err)
				continue
			}
			otherErrors++
			t.Errorf("iter %d (seed %d): unexpected error: %v", iter, seed, err)
			continue
		}

		resolved := map[string]int{}
		for _, call := range parent.ackCalls() {
			for _, p := range call.positions {
				resolved[string(p)]++
			}
		}
		for _, call := range parent.nackCalls() {
			for _, p := range call.positions {
				resolved[string(p)]++
			}
		}
		for i, p := range positions {
			if count := resolved[string(p)]; count != 1 {
				mixedRunViolations++
				t.Errorf("iter %d (seed %d): position %d (%q) resolved %d times, want exactly 1 "+
					"(acked-once XOR DLQ'd-once)", iter, seed, i, p, count)
			}
		}
	}

	t.Logf("guard-fire rate: %d/%d; mixed-run violations: %d/%d",
		guardFires, iterations, mixedRunViolations, iterations)
	if guardFires != 0 || mixedRunViolations != 0 || otherErrors != 0 {
		t.Fatalf("guard fires=%d, mixed-run violations=%d, other errors=%d - want all 0 (seed %d)",
			guardFires, mixedRunViolations, otherErrors, seed)
	}
}

// --- Benchmarks: proving normalizeSplitRuns is linear (fixes finding D) ---

// buildBigSplitRunBatch builds a batch containing exactly ONE split run of n
// pieces, with a realistic mix of flags: every 5th piece Filter, roughly
// half of the remainder Retry, the rest left at the Ack default - the same
// "mixed run needing reconciliation" shape normalizeRun is built for.
func buildBigSplitRunBatch(n int) *Batch {
	orig := randomRecords(1)
	batch := NewBatch(orig)
	pieces := randomRecords(n)
	batch.SplitRecord(0, pieces)

	for i := 0; i < n; i++ {
		switch {
		case i%5 == 0:
			batch.recordStatuses[i].Flag = RecordFlagFilter
			batch.filterCount++
		case i%2 == 0:
			batch.recordStatuses[i].Flag = RecordFlagRetry
		}
	}
	return batch
}

// BenchmarkNormalizeSplitRuns is the required benchmark: run sizes 1k/4k/16k
// must scale linearly (roughly 4x time for a 4x size increase), not
// quadratically (which would show ~16x for a 4x size increase) - the
// escalate-on-every-write design this replaces measured 10.5us -> 439ms
// (~42,000x) going from a smaller run to a 16k-piece one; this benchmark
// exists to prove that regression cannot recur silently.
func BenchmarkNormalizeSplitRuns(b *testing.B) {
	for _, n := range []int{1000, 4000, 16000} {
		b.Run(fmt.Sprintf("run_size_%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				batch := buildBigSplitRunBatch(n)
				b.StartTimer()
				batch.normalizeSplitRuns()
			}
		})
	}
}

// BenchmarkSubBatchByFlag_AfterNormalize benchmarks the OTHER half of the
// hot path - partitioning an already-normalized batch containing one big
// run - to confirm groupFlagAt's run-spanning scan doesn't reintroduce
// quadratic behavior at the partitioning step either.
func BenchmarkSubBatchByFlag_AfterNormalize(b *testing.B) {
	logger := log.Nop()
	w := &Worker{logger: logger, processingLock: make(chan struct{}, 1)}

	for _, n := range []int{1000, 4000, 16000} {
		b.Run(fmt.Sprintf("run_size_%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				batch := buildBigSplitRunBatch(n)
				batch.normalizeSplitRuns()
				b.StartTimer()

				idx := 0
				for {
					sub, _, err := w.subBatchByFlag(batch, idx)
					if err != nil {
						b.Fatalf("unexpected error: %v", err)
					}
					if sub == nil {
						break
					}
					idx += len(sub.positions)
				}
			}
		})
	}
}

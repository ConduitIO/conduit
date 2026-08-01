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
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// This file is the regression coverage for #2723: a split run's head could be
// acked before its tail (marked for Retry or Filter by a later processor that
// returned fewer records than it received) was ever delivered anywhere,
// violating invariants 1 and 3. See splitRunFlagTier and setFlagEscalating in
// batch.go for the fix, and validateSplitRunBoundary in worker.go for the
// defensive backstop.

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
// other two pieces - the same [p0, nil, nil, p1, p2] / [ack, retry, retry,
// ack, ack] shape the issue's own reproduction used.
//
// Before the fix, Worker.subBatchByFlag would partition this into a 1-record
// Ack sub-batch (just the head) and a 2-record Retry sub-batch (the
// orphaned tail, which had already lost its splitRecords association - see
// Batch.sub). The head's sub-batch collapses via Batch.originalBatch to
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
	// of the run. This is the exact shape from the issue: only the run's
	// head looks done.
	firstPassIn := []opencdc.Record{splitPieces[0], splitPieces[1], splitPieces[2], p1, p2}
	processorB.EXPECT().Process(ctx, firstPassIn).Return([]sdk.ProcessedRecord{
		sdk.SingleRecord(splitPieces[0]),
		nil,
		nil,
		sdk.SingleRecord(p1),
		sdk.SingleRecord(p2),
	})

	// Processor B, retry pass: called again with ONLY the split run once it
	// is escalated to Retry as a whole (see setFlagEscalating) - never with
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

// TestSplitRun_FilterHead_NotAckedBeforeTailResolved is the Filter variant:
// a split run where one piece is explicitly Filtered while a SIBLING piece
// of the SAME run is marked Retry (this is what taints the batch - Filter
// alone never does, see Batch.Filter's doc comment - and is what makes the
// scenario reachable: Worker.subBatchByFlag only partitions a tainted
// batch). Before the fix, Batch.Filter never propagated, so
// Worker.subBatchByFlag's "collect Filter and Ack together" rule would group
// the filtered piece with the run's (default-Ack) head into a sub-batch that
// excludes the still-Retry-flagged sibling - the run's head, again, acked
// before the rest of its own run resolved.
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
	// run as a whole is NOT done, even though none of its pieces are still
	// waiting to be acked in the ordinary sense.
	firstPassIn := []opencdc.Record{splitPieces[0], splitPieces[1], splitPieces[2], p1, p2}
	processorB.EXPECT().Process(ctx, firstPassIn).Return([]sdk.ProcessedRecord{
		sdk.FilterRecord{},
		sdk.FilterRecord{},
		nil,
		sdk.SingleRecord(p1),
		sdk.SingleRecord(p2),
	})

	// Retry pass: the WHOLE run is resubmitted (all 3 pieces), not just
	// piece 2 - Filter must not have been able to carve pieces 0/1 out of
	// the run ahead of piece 2's Retry.
	processorB.EXPECT().Process(ctx, splitPieces).Return([]sdk.ProcessedRecord{
		sdk.FilterRecord{},
		sdk.FilterRecord{},
		sdk.SingleRecord(splitPieces[2]),
	})

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
// was left with a non-uniform flag by a naive (propagation-unaware) caller -
// exactly what Batch.Retry produces once escalation is applied - the first
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

	sub, err := worker.subBatchByFlag(batch, 0)
	is.NoErr(err)
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
	next, err := worker.subBatchByFlag(batch, 3)
	is.NoErr(err)
	is.Equal(len(next.records), 2)
	is.Equal(next.recordStatuses, []RecordStatus{
		{Flag: RecordFlagAck},
		{Flag: RecordFlagAck},
	})
}

// TestValidateSplitRunBoundary_CatchesForcedPartition unit-tests the
// defensive backstop (CodeSplitRunPartitioned) in isolation from whether the
// tier-escalation that is supposed to make it unreachable actually works: it
// pokes recordStatuses directly (bypassing Batch.Retry/Filter/Nack
// entirely) to simulate a hypothetical future flag-setting path that forgot
// to route through setFlagEscalating/setFlagWithErr, and confirms the
// boundary check still catches the resulting partition and returns a coded,
// actionable error rather than silently allowing it.
func TestValidateSplitRunBoundary_CatchesForcedPartition(t *testing.T) {
	is := is.New(t)

	records := randomRecords(3)
	batch := NewBatch(records)
	batch.SplitRecord(0, randomRecords(3)) // p0 -> 3 pieces

	// Force a partitioned state directly, bypassing every public flag-setting
	// method's propagation.
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

	// A boundary that contains the whole run is fine.
	is.NoErr(validateSplitRunBoundary(batch, 0, 3))
	// A boundary entirely before or after the run is also fine.
	is.NoErr(validateSplitRunBoundary(batch, 3, 4))
}

// splitOnceThenBlockTask splits the record at splitIdx into 3 identical
// pieces on its first call and marks 2 of them for Retry (mirroring a real
// processor returning a partial result for a split run), then - if blockCh
// and resumeCh are set - signals blockCh and waits on resumeCh before
// returning, so a test can observe a sibling fan-out branch's progress while
// this branch's split run is still unresolved. The second call (the Retry
// resubmission) resolves the run cleanly.
type splitOnceThenBlockTask struct {
	id       string
	splitIdx int
	calls    int
	blockCh  chan struct{}
	resumeCh chan struct{}
}

func (t *splitOnceThenBlockTask) ID() string                  { return t.id }
func (t *splitOnceThenBlockTask) Open(context.Context) error  { return nil }
func (t *splitOnceThenBlockTask) Close(context.Context) error { return nil }

func (t *splitOnceThenBlockTask) Do(_ context.Context, b *Batch) error {
	t.calls++
	if t.calls > 1 {
		return nil // retry pass: resolve cleanly
	}

	active := b.ActiveRecords()
	orig := active[t.splitIdx]
	b.SplitRecord(t.splitIdx, []opencdc.Record{orig, orig, orig})
	b.Retry(t.splitIdx+1, t.splitIdx+3)

	if t.blockCh != nil {
		close(t.blockCh)
	}
	if t.resumeCh != nil {
		<-t.resumeCh
	}
	return nil
}

// TestFanOut_SplitRun_NoPrematureUnanimity is the fan-out (M=2) composition
// test: branch A splits the single source record into a 3-piece run and
// blocks mid-retry (its run is NOT yet fully resolved); branch B, meanwhile,
// acks the very same original position straight through with no delay.
//
// Before the fix, branch A's first pass would let its run's head sub-batch
// (a lone, already-collapsed position) reach multiAckNacker as a premature
// ack vote - and since branch B's vote lands immediately, that would be
// unanimity (M=2) on the FIRST vote, acking the source before branch A's
// split run ever finished. This test proves that does not happen: while
// branch A is deliberately held mid-retry, the source must not be acked at
// all, even though branch B's side is fully done.
func TestFanOut_SplitRun_NoPrematureUnanimity(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(1)
	pos := records[0].Position

	src := newFakeSource("src", records)
	destA := newFakeDestination("destA")
	destB := newFakeDestination("destB")

	blockCh := make(chan struct{})
	resumeCh := make(chan struct{})
	splitTaskA := &splitOnceThenBlockTask{id: "splitA", splitIdx: 0, blockCh: blockCh, resumeCh: resumeCh}

	logger := log.Test(t)
	dlqDest := newFakeDestination("dlq")
	dlq := NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, 0, 0)

	destNodeA := &TaskNode{Task: NewDestinationTask("destA", destA, logger, NoOpConnectorMetrics{})}
	branchANode := &TaskNode{Task: splitTaskA, Next: []*TaskNode{destNodeA}}
	branchBNode := &TaskNode{Task: NewDestinationTask("destB", destB, logger, NoOpConnectorMetrics{})}

	fanoutNode := &TaskNode{Task: passthroughTask{id: "shared"}, Next: []*TaskNode{branchANode, branchBNode}}
	srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{fanoutNode}}

	w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
	is.NoErr(err)

	batch := NewBatch(append([]opencdc.Record(nil), records...))

	doneCh := make(chan error, 1)
	go func() { doneCh <- w.doTask(ctx, fanoutNode, batch, w) }()

	// Wait for branch A to reach its blocking point (split run tainted, head
	// NOT yet resolvable) ...
	select {
	case <-blockCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for branch A to reach its blocking point")
	}
	// ... and for branch B to independently finish writing (it has no
	// splits and nothing blocking it).
	waitForCondition(t, 5*time.Second, func() bool { return len(destB.receivedPositions()) == 1 })

	// Invariant 1/3: branch B's vote alone must not be unanimity. The source
	// must not have been acked yet, even though branch B is fully done and
	// branch A's split run - superficially, on its head alone - "looks"
	// acked-worthy under the pre-fix behavior.
	is.Equal(len(src.ackedPositions()), 0)

	close(resumeCh) // let branch A's retry pass resolve

	select {
	case err := <-doneCh:
		is.NoErr(err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for doTask to finish")
	}

	is.Equal(src.ackedPositions(), []opencdc.Position{pos})
	is.Equal(len(destA.receivedPositions()), 3) // the 3 split pieces
	is.Equal(len(destB.receivedPositions()), 1) // the untouched original
}

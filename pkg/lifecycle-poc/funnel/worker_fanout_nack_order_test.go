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
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/matryer/is"
)

// TestWorker_FanOutBatching_SiblingNackDoesNotAckPastBufferedBatch: one source
// fans out to destination A (batching, size 3, so batch N stays buffered) and
// destination B. Batch N (rec0) is buffered in A and not written. Batch N+1
// (rec1) is nacked by B. Nack-wins makes rec1 terminal in batch N+1's
// multiAckNacker without A's vote, so it is released to the DLQ and then
// connector.Source.Ack(rec1) runs. Source.Ack persists the last position it is
// given, so a crash at that instant resumes after rec1 and A never gets rec0
// (invariants 1 and 3).
//
// The source must not receive any ack covering a record A has not written.
func TestWorker_FanOutBatching_SiblingNackDoesNotAckPastBufferedBatch(t *testing.T) {
	for _, tc := range []struct {
		name       string
		bBatchSize int // 0 = B does not batch
	}{
		{name: "B unbatched", bBatchSize: 0},
		{name: "B batching size 2", bBatchSize: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			logger := log.Test(t)
			recs := randomRecords(2)

			src := newFakeSource("src", nil)
			destA, destB := newFakeDestination("A"), newFakeDestination("B")
			destB.setNackErr(recs[1].Position, cerrors.New("B: simulated write failure"))

			dtA := NewBatchingDestinationTask("A", destA, logger, NoOpConnectorMetrics{}, 3, 0)
			var dtB *DestinationTask
			if tc.bBatchSize > 1 {
				dtB = NewBatchingDestinationTask("B", destB, logger, NoOpConnectorMetrics{}, tc.bBatchSize, 0)
			} else {
				dtB = NewDestinationTask("B", destB, logger, NoOpConnectorMetrics{})
			}

			fanNode := &TaskNode{Task: passthroughTask{id: "shared"}, Next: []*TaskNode{{Task: dtA}, {Task: dtB}}}
			srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{fanNode}}
			dlqDest := newFakeDestination("dlq")
			dlq := NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, 10, 10)
			w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
			is.NoErr(err)

			// Batch N: buffered in A (size 3 not reached).
			is.NoErr(w.doTask(ctx, fanNode, NewBatch([]opencdc.Record{recs[0]}), newRunAckNacker(w)))
			// Batch N+1: B nacks rec1.
			is.NoErr(w.doTask(ctx, fanNode, NewBatch([]opencdc.Record{recs[1]}), newRunAckNacker(w)))

			// Crash point: A has written nothing. The source must not have
			// been acked past rec0.
			if len(destA.receivedPositions()) == 0 {
				if acked := src.ackedPositions(); len(acked) != 0 {
					t.Fatalf("invariant 1/3 violated: source acked %d position(s) (last=%s) while "+
						"destination A has not written batch N (rec0=%s); dlq got %d record(s). "+
						"A crash now resumes past rec0 and A loses it",
						len(acked), acked[len(acked)-1], recs[0].Position, len(dlqDest.receivedPositions()))
				}
			}

			// After A flushes, everything is acked in source order.
			is.NoErr(dtA.Close(ctx))
			is.NoErr(dtB.Close(ctx))
			is.Equal(destA.receivedPositions(), posOf(recs...))
			acked := src.ackedPositions()
			is.Equal(len(acked), 2)
			is.Equal(acked[len(acked)-1], recs[1].Position)
		})
	}
}

// fanOutDest describes one destination branch for the #3323 tests below.
type fanOutDest struct {
	id        string
	batchSize int  // 0 = unbatched
	nackRec1  bool // the destination fails the write of recs[1]
}

// TestWorker_FanOutBatching_NackReleasedOnlyAfterEveryBranchVoted extends the
// #3323 regression test to the other shapes of the same hole: the nack comes
// from the batching destination itself, from both destinations, or from one of
// three. In every case batch N (rec0) is still buffered in at least one
// batching destination when batch N+1 (rec1) is nacked, and the source must not
// be acked, nor the DLQ written, until every branch has voted for rec1.
func TestWorker_FanOutBatching_NackReleasedOnlyAfterEveryBranchVoted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dests []fanOutDest
		// dlqTask is the task the DLQ entry is attributed to: the first branch
		// to nack, which is the one that votes first.
		dlqTask string
	}{{
		name:    "nack in the batching destination",
		dests:   []fanOutDest{{id: "A", batchSize: 3, nackRec1: true}, {id: "B"}},
		dlqTask: "A",
	}, {
		name:    "nack in both destinations",
		dests:   []fanOutDest{{id: "A", batchSize: 3, nackRec1: true}, {id: "B", nackRec1: true}},
		dlqTask: "B", // B is unbatched, so it votes (and nacks) first
	}, {
		name:    "three destinations, unbatched sibling nacks",
		dests:   []fanOutDest{{id: "A", batchSize: 3}, {id: "B", nackRec1: true}, {id: "C", batchSize: 2}},
		dlqTask: "B",
	}, {
		name:    "three destinations, the batching one that flushes first nacks",
		dests:   []fanOutDest{{id: "A", batchSize: 3}, {id: "B"}, {id: "C", batchSize: 2, nackRec1: true}},
		dlqTask: "C",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			logger := log.Test(t)
			recs := randomRecords(2)

			src := newFakeSource("src", nil)
			fakes := make([]*fakeDestination, len(tc.dests))
			tasks := make([]*DestinationTask, len(tc.dests))
			next := make([]*TaskNode, len(tc.dests))
			for i, d := range tc.dests {
				fakes[i] = newFakeDestination(d.id)
				if d.nackRec1 {
					fakes[i].setNackErr(recs[1].Position, cerrors.New(d.id+": simulated write failure"))
				}
				if d.batchSize > 1 {
					tasks[i] = NewBatchingDestinationTask(d.id, fakes[i], logger, NoOpConnectorMetrics{}, d.batchSize, 0)
				} else {
					tasks[i] = NewDestinationTask(d.id, fakes[i], logger, NoOpConnectorMetrics{})
				}
				next[i] = &TaskNode{Task: tasks[i]}
			}
			fanNode := &TaskNode{Task: passthroughTask{id: "shared"}, Next: next}
			srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{fanNode}}
			dlqDest := newFakeDestination("dlq")
			w, err := NewWorker(srcNode, NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, 10, 10), logger, noop.Timer{})
			is.NoErr(err)

			is.NoErr(w.doTask(ctx, fanNode, NewBatch([]opencdc.Record{recs[0]}), newRunAckNacker(w)))
			is.NoErr(w.doTask(ctx, fanNode, NewBatch([]opencdc.Record{recs[1]}), newRunAckNacker(w)))

			// Crash point: A (batch size 3) still holds rec0 and rec1 unwritten.
			is.Equal(len(fakes[0].receivedPositions()), 0)
			is.Equal(len(src.ackedPositions()), 0)        // invariant 1/3: nothing acked past rec0
			is.Equal(len(dlqDest.receivedPositions()), 0) // not released before A's vote

			for _, dt := range tasks {
				is.NoErr(dt.Close(ctx))
			}
			for _, f := range fakes {
				is.Equal(f.receivedPositions(), posOf(recs...))
			}
			is.Equal(src.ackedPositions(), posOf(recs...)) // in source order, each once
			is.Equal(dlqDest.receivedPositions(), posOf(recs[1]))
			dlqDest.mu.Lock()
			nodeID, _ := dlqDest.written[0].Metadata.GetConduitDLQNackNodeID()
			nackErr, _ := dlqDest.written[0].Metadata.GetConduitDLQNackError()
			dlqDest.mu.Unlock()
			is.Equal(nodeID, tc.dlqTask) // the first nack's attribution
			is.Equal(nackErr, tc.dlqTask+": simulated write failure")
		})
	}
}

// TestWorker_FanOutBatching_StopDrainReleasesSiblingNack: a graceful Stop while
// the batching sibling holds batch N and the other branch has nacked batch N+1.
// The drain flushes A, A's votes complete both positions, and they are released
// in source order while the source is still running: rec0 acked, rec1 to the
// DLQ and then acked. Nothing is left waiting for a vote that never comes.
func TestWorker_FanOutBatching_StopDrainReleasesSiblingNack(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	recs := randomRecords(2)

	src := &teardownSource{fakeSource: newFakeSource("src", nil)}
	destA, destB := newFakeDestination("A"), newFakeDestination("B")
	destB.setNackErr(recs[1].Position, cerrors.New("B: simulated write failure"))
	dtA := NewBatchingDestinationTask("A", destA, logger, NoOpConnectorMetrics{}, 3, 0)
	dtB := NewDestinationTask("B", destB, logger, NoOpConnectorMetrics{})
	fanNode := &TaskNode{Task: passthroughTask{id: "shared"}, Next: []*TaskNode{{Task: dtA}, {Task: dtB}}}
	srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{fanNode}}
	dlqDest := newFakeDestination("dlq")
	w, err := NewWorker(srcNode, NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, 10, 10), logger, noop.Timer{})
	is.NoErr(err)

	is.NoErr(w.doTask(ctx, fanNode, NewBatch([]opencdc.Record{recs[0]}), newRunAckNacker(w)))
	is.NoErr(w.doTask(ctx, fanNode, NewBatch([]opencdc.Record{recs[1]}), newRunAckNacker(w)))
	accepted, _ := src.snapshot()
	is.Equal(len(accepted), 0)

	is.NoErr(w.Stop(ctx))

	accepted, refused := src.snapshot()
	is.Equal(len(refused), 0) // released before the source was torn down
	is.Equal(accepted, posOf(recs...))
	is.Equal(destA.receivedPositions(), posOf(recs...))
	is.Equal(dlqDest.receivedPositions(), posOf(recs[1]))
	is.NoErr(dtA.Close(ctx))
	is.NoErr(dtB.Close(ctx))
}

// TestWorker_FanOutBatching_BufferedDestinationErrorAfterSiblingNack: B nacks
// rec1, then A's flush of rec0 and rec1 fails. A never votes for either, so
// neither position may be released: rec1 must not go to the DLQ and the source
// must not be acked. Both replay on restart (invariant 3, at-least-once). Before
// #3323 rec1 was already in the DLQ and acked to the source by then.
func TestWorker_FanOutBatching_BufferedDestinationErrorAfterSiblingNack(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	recs := randomRecords(2)

	src := newFakeSource("src", nil)
	destA := failingWriteDestination{newFakeDestination("A")}
	destB := newFakeDestination("B")
	destB.setNackErr(recs[1].Position, cerrors.New("B: simulated write failure"))
	dtA := NewBatchingDestinationTask("A", destA, logger, NoOpConnectorMetrics{}, 2, 0)
	dtB := NewDestinationTask("B", destB, logger, NoOpConnectorMetrics{})
	fanNode := &TaskNode{Task: passthroughTask{id: "shared"}, Next: []*TaskNode{{Task: dtA}, {Task: dtB}}}
	srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{fanNode}}
	dlqDest := newFakeDestination("dlq")
	w, err := NewWorker(srcNode, NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, 10, 10), logger, noop.Timer{})
	is.NoErr(err)

	is.NoErr(w.doTask(ctx, fanNode, NewBatch([]opencdc.Record{recs[0]}), newRunAckNacker(w)))
	// Batch N+1 fills A (size 2): its flush fails while B nacks rec1.
	err = w.doTask(ctx, fanNode, NewBatch([]opencdc.Record{recs[1]}), newRunAckNacker(w))
	is.True(err != nil)

	is.Equal(len(src.ackedPositions()), 0)
	is.Equal(len(dlqDest.receivedPositions()), 0)
	_ = dtA.Close(ctx) // fails closed: the flush error is sticky
	is.NoErr(dtB.Close(ctx))
	is.Equal(len(src.ackedPositions()), 0)
	is.Equal(len(dlqDest.receivedPositions()), 0)
}

// TestWorker_FanOutBatching_DefaultDLQ_SiblingNackStillFailsPipeline: with the
// default DLQ (window 1, threshold 0) a nack fails the pipeline and moves no
// position past it. Since #3323 the failure surfaces when the batching sibling
// votes (here, when its buffer fills on batch N+2), not when B nacks; until then
// nothing is acked past rec0. After it, exactly rec0 is acked: rec1 and rec2
// replay.
func TestWorker_FanOutBatching_DefaultDLQ_SiblingNackStillFailsPipeline(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	recs := randomRecords(3)

	src := newFakeSource("src", nil)
	destA, destB := newFakeDestination("A"), newFakeDestination("B")
	destB.setNackErr(recs[1].Position, cerrors.New("B: simulated write failure"))
	dtA := NewBatchingDestinationTask("A", destA, logger, NoOpConnectorMetrics{}, 3, 0)
	dtB := NewDestinationTask("B", destB, logger, NoOpConnectorMetrics{})
	fanNode := &TaskNode{Task: passthroughTask{id: "shared"}, Next: []*TaskNode{{Task: dtA}, {Task: dtB}}}
	srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{fanNode}}
	dlqDest := newFakeDestination("dlq")
	w, err := NewWorker(srcNode, NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, 1, 0), logger, noop.Timer{})
	is.NoErr(err)

	is.NoErr(w.doTask(ctx, fanNode, NewBatch([]opencdc.Record{recs[0]}), newRunAckNacker(w)))
	is.NoErr(w.doTask(ctx, fanNode, NewBatch([]opencdc.Record{recs[1]}), newRunAckNacker(w)))
	is.Equal(len(src.ackedPositions()), 0)

	err = w.doTask(ctx, fanNode, NewBatch([]opencdc.Record{recs[2]}), newRunAckNacker(w))
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "B: simulated write failure"))
	is.Equal(src.ackedPositions(), posOf(recs[0]))
	is.Equal(len(dlqDest.receivedPositions()), 0)
}

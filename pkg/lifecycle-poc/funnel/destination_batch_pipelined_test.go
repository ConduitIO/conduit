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
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// newPipelinedTask builds a batching destination task with read-ahead
// pipelining enabled and the flusher goroutine started (Open). The caller owns
// the task and must Close it exactly once to drain the flusher and tear down
// the destination.
func newPipelinedTask(t *testing.T, dest Destination, batchSize int, batchDelay time.Duration) *DestinationTask {
	t.Helper()
	task := NewBatchingDestinationTask("test", dest, log.Test(t), NoOpConnectorMetrics{}, batchSize, batchDelay)
	task.SetPipelined()
	if err := task.Open(context.Background()); err != nil {
		t.Fatalf("open: %v", err)
	}
	return task
}

// TestDestinationTask_DoBuffered_Pipelined_OverlapsReadAndWrite proves the core
// win: doBuffered hands a full buffer to the flusher goroutine and returns
// WITHOUT blocking on the destination's Write/Ack, while the flusher is
// concurrently blocked inside Write. The source ack happens only after the
// blocked Write is released (Invariant 1) — so the worker can keep reading the
// next batch while the destination is still writing this one.
func TestDestinationTask_DoBuffered_Pipelined_OverlapsReadAndWrite(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	log := &eventLog{}
	dest := newFakeDestination("test")

	recs := randomRecords(2)
	blocked, unblock := dest.blockWrites(recs[0].Position)

	task := newPipelinedTask(t, dest, 2, 0)

	// First record buffers; the second fills the batch and hands it to the
	// flusher. Neither call blocks on the destination.
	is.True(isBuffered(task.doBuffered(ctx, NewBatch([]opencdc.Record{recs[0]}), newRecordingAckNacker(log))))
	is.True(isBuffered(task.doBuffered(ctx, NewBatch([]opencdc.Record{recs[1]}), newRecordingAckNacker(log))))

	// The flusher has picked up the flush and is now blocked inside Write. The
	// source must not be acked while the write is in flight.
	<-blocked
	is.Equal(len(log.snapshot()), 0)

	unblock()

	// Once the write completes and acks drain, the source is acked.
	waitForCondition(t, time.Second, func() bool { return len(log.snapshot()) == 2 })

	is.NoErr(task.Close(ctx))
	is.Equal(len(dest.receivedPositions()), 2)
}

// TestDestinationTask_DoBuffered_Pipelined_AckAfterDurableWrite proves the
// event ordering on the pipelined path: destination write, then destination
// ack, and only then the source ack (Invariant 1) — identical to the
// synchronous path, just deferred onto the flusher goroutine.
func TestDestinationTask_DoBuffered_Pipelined_AckAfterDurableWrite(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	log := &eventLog{}
	ctrl := gomock.NewController(t)
	dest := NewMockDestination(ctrl)

	recs := randomRecords(3)
	dest.EXPECT().Open(gomock.Any()).Return(nil)
	task := newPipelinedTask(t, dest, 3, 0)

	for _, rec := range recs[:2] {
		is.True(isBuffered(task.doBuffered(ctx, NewBatch([]opencdc.Record{rec}), newRecordingAckNacker(log))))
	}
	is.Equal(len(log.snapshot()), 0) // nothing written or acked while buffering

	dest.EXPECT().Write(ctx, recs).DoAndReturn(func(context.Context, []opencdc.Record) error {
		log.append("dest.write")
		return nil
	})
	dest.EXPECT().Ack(ctx).DoAndReturn(func(context.Context) ([]connector.DestinationAck, error) {
		log.append("dest.ack")
		return toDestinationAcks(recs, nil), nil
	})
	dest.EXPECT().Teardown(ctx).Return(nil)

	// The third record fills the batch; on the pipelined path this defers
	// (errBuffered) rather than blocking on the write+ack.
	is.True(isBuffered(task.doBuffered(ctx, NewBatch([]opencdc.Record{recs[2]}), newRecordingAckNacker(log))))

	// Close drains the flusher and waits for the flush to complete.
	is.NoErr(task.Close(ctx))

	// Invariant 1: every source ack is strictly after the destination write+ack.
	events := log.snapshot()
	is.Equal(events[:2], []string{"dest.write", "dest.ack"})
	for _, ev := range events[2:] {
		is.Equal(ev, "acker.ack")
	}
	is.Equal(len(events), 2+len(recs))
}

// TestDestinationTask_DoBuffered_Pipelined_PreservesOrder proves multiple
// flushes reach the destination in source order (Invariant 4): the single
// flusher processes the FIFO channel in enqueue order.
func TestDestinationTask_DoBuffered_Pipelined_PreservesOrder(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	dest := newFakeDestination("test")

	recs := randomRecords(6)
	task := newPipelinedTask(t, dest, 2, 0)

	// Three flushes of two records each.
	for i := 0; i < len(recs); i += 2 {
		is.True(isBuffered(task.doBuffered(ctx, NewBatch([]opencdc.Record{recs[i]}), newRecordingAckNacker(&eventLog{}))))
		is.True(isBuffered(task.doBuffered(ctx, NewBatch([]opencdc.Record{recs[i+1]}), newRecordingAckNacker(&eventLog{}))))
	}

	is.NoErr(task.Close(ctx))

	got := dest.receivedPositions()
	is.Equal(len(got), len(recs))
	for i := range got {
		is.Equal(got[i], recs[i].Position)
	}
}

// TestDestinationTask_DoBuffered_Pipelined_FlusherErrorPropagates proves a
// destination Write error on the flusher goroutine is surfaced by the next
// doBuffered (rather than silently dropped), so the worker fails the run
// exactly like a synchronous flush error would.
func TestDestinationTask_DoBuffered_Pipelined_FlusherErrorPropagates(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	dest := NewMockDestination(ctrl)

	recs := randomRecords(2)
	dest.EXPECT().Open(gomock.Any()).Return(nil)
	task := newPipelinedTask(t, dest, 2, 0)

	wantErr := cerrors.New("write failed")
	dest.EXPECT().Write(ctx, recs).Return(wantErr)
	dest.EXPECT().Teardown(ctx).Return(nil)

	// The second record triggers the flush (handed to the flusher).
	is.True(isBuffered(task.doBuffered(ctx, NewBatch([]opencdc.Record{recs[0]}), newRecordingAckNacker(&eventLog{}))))
	is.True(isBuffered(task.doBuffered(ctx, NewBatch([]opencdc.Record{recs[1]}), newRecordingAckNacker(&eventLog{}))))

	// Wait for the flusher to record the Write error, then the next doBuffered
	// returns it.
	waitForCondition(t, time.Second, func() bool { return task.flushError() != nil })
	err := task.doBuffered(ctx, NewBatch([]opencdc.Record{recs[0]}), newRecordingAckNacker(&eventLog{}))
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr))

	// Close still tears down, and reports the flush failure with a stable code so
	// the stop is not mistaken for a clean one.
	ce, ok := conduiterr.Get(task.Close(ctx))
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeFlushOnCloseFailed.Reason())
}

// TestDestinationTask_DoBuffered_Pipelined_CloseFlushesTrailing proves Close
// flushes a trailing partial batch (below the size threshold) through the
// flusher before teardown (Invariant 7).
func TestDestinationTask_DoBuffered_Pipelined_CloseFlushesTrailing(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	dest := NewMockDestination(ctrl)

	recs := randomRecords(2)
	dest.EXPECT().Open(gomock.Any()).Return(nil)
	task := newPipelinedTask(t, dest, 100, 0) // won't fill from two records

	for _, rec := range recs {
		is.True(isBuffered(task.doBuffered(ctx, NewBatch([]opencdc.Record{rec}), newRecordingAckNacker(&eventLog{}))))
	}

	dest.EXPECT().Write(ctx, recs).Return(nil)
	dest.EXPECT().Ack(ctx).Return(toDestinationAcks(recs, nil), nil)
	dest.EXPECT().Teardown(ctx).Return(nil)

	is.NoErr(task.Close(ctx))
}

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
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// recordingAckNacker is a fake ackNacker that records, in order, whether it was
// asked to Ack or Nack. It shares an event log with the test so ack ordering
// relative to destination Write/Ack can be asserted (Invariant 1).
type recordingAckNacker struct {
	log *eventLog
}

func newRecordingAckNacker(log *eventLog) *recordingAckNacker {
	return &recordingAckNacker{log: log}
}

func (r *recordingAckNacker) Ack(context.Context, *Batch) error {
	r.log.append("acker.ack")
	return nil
}

func (r *recordingAckNacker) Nack(context.Context, *Batch, string) error {
	r.log.append("acker.nack")
	return nil
}

// eventLog is a mutex-guarded ordered list of events, used to prove that the
// source is acknowledged only after the destination confirms the write.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) append(ev string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func newBatchingTask(
	t *testing.T,
	destination *MockDestination,
	batchSize int,
	batchDelay time.Duration,
) *DestinationTask {
	t.Helper()
	return NewBatchingDestinationTask("test", destination, log.Test(t), NoOpConnectorMetrics{}, batchSize, batchDelay)
}

// TestDestinationTask_DoBuffered_FlushesOnSize buffers single-record batches
// and flushes them together once the size threshold is reached: one multi-record
// Write, one Ack, and only then the source acks.
func TestDestinationTask_DoBuffered_FlushesOnSize(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	log := &eventLog{}
	ctrl := gomock.NewController(t)
	dest := NewMockDestination(ctrl)

	recs := randomRecords(3)
	task := newBatchingTask(t, dest, 3, 0)

	// Two single-record buffers return errBuffered and must not write or ack.
	for _, rec := range recs[:2] {
		batch := NewBatch([]opencdc.Record{rec})
		err := task.doBuffered(ctx, batch, newRecordingAckNacker(log))
		is.True(isBuffered(err))
	}
	is.Equal(len(log.snapshot()), 0) // no write, no ack yet

	// The third record fills the batch and flushes.
	dest.EXPECT().Write(ctx, recs).DoAndReturn(func(context.Context, []opencdc.Record) error {
		log.append("dest.write")
		return nil
	})
	dest.EXPECT().Ack(ctx).DoAndReturn(func(context.Context) ([]connector.DestinationAck, error) {
		log.append("dest.ack")
		return toDestinationAcks(recs, nil), nil
	})

	batch := NewBatch([]opencdc.Record{recs[2]})
	err := task.doBuffered(ctx, batch, newRecordingAckNacker(log))
	is.NoErr(err)

	// The source ack must come strictly after the destination write and ack.
	is.Equal(log.snapshot(), []string{"dest.write", "dest.ack", "acker.ack", "acker.ack", "acker.ack"})
}

// TestDestinationTask_DoBuffered_AckOnlyAfterFlush proves a buffered record is
// never acknowledged before its flush: Close is what finally flushes and acks.
func TestDestinationTask_DoBuffered_AckOnlyAfterFlush(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	log := &eventLog{}
	ctrl := gomock.NewController(t)
	dest := NewMockDestination(ctrl)

	recs := randomRecords(1)
	task := newBatchingTask(t, dest, 100, 0) // won't fill from one record

	err := task.doBuffered(ctx, NewBatch(recs), newRecordingAckNacker(log))
	is.True(isBuffered(err))
	is.Equal(len(log.snapshot()), 0) // buffered: nothing written, nothing acked

	// Close flushes the trailing batch before teardown.
	dest.EXPECT().Write(ctx, recs).DoAndReturn(func(context.Context, []opencdc.Record) error {
		log.append("dest.write")
		return nil
	})
	dest.EXPECT().Ack(ctx).DoAndReturn(func(context.Context) ([]connector.DestinationAck, error) {
		log.append("dest.ack")
		return toDestinationAcks(recs, nil), nil
	})
	dest.EXPECT().Teardown(ctx).Return(nil)

	is.NoErr(task.Close(ctx))
	is.Equal(log.snapshot(), []string{"dest.write", "dest.ack", "acker.ack"})
}

// TestDestinationTask_DoBuffered_PreservesOrder flushes several single-record
// batches in source order inside one Write.
func TestDestinationTask_DoBuffered_PreservesOrder(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	dest := NewMockDestination(ctrl)

	recs := randomRecords(4)
	task := newBatchingTask(t, dest, 4, 0)

	for _, rec := range recs[:3] {
		is.True(isBuffered(task.doBuffered(ctx, NewBatch([]opencdc.Record{rec}), newRecordingAckNacker(&eventLog{}))))
	}

	dest.EXPECT().Write(ctx, gomock.Any()).DoAndReturn(func(_ context.Context, got []opencdc.Record) error {
		for i, rec := range got {
			is.Equal(rec.Position, recs[i].Position) // order preserved
		}
		return nil
	})
	dest.EXPECT().Ack(ctx).Return(toDestinationAcks(recs, nil), nil)

	is.NoErr(task.doBuffered(ctx, NewBatch([]opencdc.Record{recs[3]}), newRecordingAckNacker(&eventLog{})))
}

// TestDestinationTask_DoBuffered_FlushesOnDeadline flushes a partial batch once
// the flush deadline has passed, checked at the next record arrival.
func TestDestinationTask_DoBuffered_FlushesOnDeadline(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	dest := NewMockDestination(ctrl)

	recs := randomRecords(2)
	task := newBatchingTask(t, dest, 100, 5*time.Millisecond)

	is.True(isBuffered(task.doBuffered(ctx, NewBatch([]opencdc.Record{recs[0]}), newRecordingAckNacker(&eventLog{}))))

	time.Sleep(10 * time.Millisecond) // let the deadline pass

	dest.EXPECT().Write(ctx, recs).Return(nil)
	dest.EXPECT().Ack(ctx).Return(toDestinationAcks(recs, nil), nil)

	// The next arrival observes the passed deadline and flushes both records.
	is.NoErr(task.doBuffered(ctx, NewBatch([]opencdc.Record{recs[1]}), newRecordingAckNacker(&eventLog{})))
}

// TestDestinationTask_DoBuffered_KillMidBatchLeavesRecordsUnacked simulates a
// crash while records are buffered but not yet flushed: the source is never
// acknowledged, so a restart replays them (at-least-once, no loss).
func TestDestinationTask_DoBuffered_KillMidBatchLeavesRecordsUnacked(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	log := &eventLog{}
	ctrl := gomock.NewController(t)
	dest := NewMockDestination(ctrl) // no expectations: Write/Ack must not be called

	recs := randomRecords(5)
	task := newBatchingTask(t, dest, 100, 0) // never fills from 5 records

	for _, rec := range recs {
		is.True(isBuffered(task.doBuffered(ctx, NewBatch([]opencdc.Record{rec}), newRecordingAckNacker(log))))
	}

	// "Crash": the task is discarded without Close. Nothing was written to the
	// destination and nothing was acknowledged to the source, so restart
	// replays all five records.
	is.Equal(len(log.snapshot()), 0)
}

// TestDestinationTask_DoBuffered_AllFilteredAcksImmediately: a batch with only
// filtered records writes nothing and acks the source at once.
func TestDestinationTask_DoBuffered_AllFilteredAcksImmediately(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	log := &eventLog{}
	ctrl := gomock.NewController(t)
	dest := NewMockDestination(ctrl)

	recs := randomRecords(1)
	task := newBatchingTask(t, dest, 2, 0)
	batch := NewBatch(recs)
	batch.Filter(0)

	is.NoErr(task.doBuffered(ctx, batch, newRecordingAckNacker(log)))
	is.Equal(log.snapshot(), []string{"acker.ack"}) // acked, nothing written
}

// TestDestinationTask_DoBuffered_NackMarksBatch: a destination ack error nacks
// the owning source batch after the write.
func TestDestinationTask_DoBuffered_NackMarksBatch(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	dest := NewMockDestination(ctrl)

	recs := randomRecords(2)
	task := newBatchingTask(t, dest, 2, 0)
	wantErr := cerrors.New("write failed")

	dest.EXPECT().Write(ctx, recs).Return(nil)
	dest.EXPECT().Ack(ctx).Return(toDestinationAcks(recs, []error{wantErr, nil}), nil)

	acker := newRecordingAckNacker(&eventLog{})
	batch := NewBatch(recs)
	is.NoErr(task.doBuffered(ctx, batch, acker))

	// Record 0 is nacked, record 1 is acked (the tainted batch splits).
	is.Equal(batch.recordStatuses[0].Flag, RecordFlagNack)
	is.Equal(batch.recordStatuses[1].Flag, RecordFlagAck)
}

// TestDestinationTask_BatchingDisabledFallsBackToDo: a task built with no
// batching settings reports batchingEnabled() == false and uses the immediate
// Do path.
func TestDestinationTask_BatchingDisabled(t *testing.T) {
	is := is.New(t)
	task := NewDestinationTask("test", nil, log.Test(t), NoOpConnectorMetrics{})
	is.True(!task.batchingEnabled())
}

func BenchmarkDestinationTask_DoBuffered(b *testing.B) {
	ctx := context.Background()
	const batchSize = 100
	const records = 10_000

	recs := randomRecords(records)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		dest := newFakeDestination("bench")
		task := NewBatchingDestinationTask("test", dest, log.Nop(), NoOpConnectorMetrics{}, batchSize, 0)
		for i := 0; i < records; i++ {
			batch := NewBatch([]opencdc.Record{recs[i]})
			if err := task.doBuffered(ctx, batch, newRecordingAckNacker(&eventLog{})); err != nil && !isBuffered(err) {
				b.Fatal(err)
			}
		}
		if err := task.Close(ctx); err != nil {
			b.Fatal(err)
		}
		if got := len(dest.receivedPositions()); got != records {
			b.Fatalf("delivered %d of %d records", got, records)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(records)/b.Elapsed().Seconds(), "records/s")
}

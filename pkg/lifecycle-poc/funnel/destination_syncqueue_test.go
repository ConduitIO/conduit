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

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// TestDestinationTask_Sync_QueuedAcksCountTowardFlush: on the synchronous path
// (fan-out) a queued source ack used to wait for the next unfiltered record or
// Close. Behind one buffered record, a run of fully filtered batches would pile
// up *Batch values and pin the source position (on Postgres, WAL retention).
// Queued acks now count toward the flush trigger, so the run flushes the buffered
// record and releases them.
func TestDestinationTask_Sync_QueuedAcksCountTowardFlush(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	dest := newFakeDestination("d")
	task := NewBatchingDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{}, 3, 0)
	recs := randomRecords(1)
	alog := &eventLog{}

	is.True(isBuffered(task.doBuffered(ctx, NewBatch(recs), newRecordingAckNacker(alog))))
	for i := 0; i < 3; i++ {
		is.NoErr(task.deferOrdered(ctx, func(context.Context) error {
			alog.append("action")
			return nil
		}))
	}

	is.Equal(len(dest.receivedPositions()), 1) // the buffered record was flushed
	is.Equal(alog.snapshot(), []string{"acker.ack", "action", "action", "action"})
}

// TestDestinationTask_Sync_AckQueuedDuringFlushIsReleased: a source ack queued
// while another worker's synchronous flush is in flight must be released once
// that flush completes, without waiting for another record or Close.
func TestDestinationTask_Sync_AckQueuedDuringFlushIsReleased(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	dest := newFakeDestination("d")
	task := NewBatchingDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{}, 2, 0)
	recs := randomRecords(2)
	alog := &eventLog{}

	blocked, release := dest.blockWrites(recs[0].Position)
	var once sync.Once
	unblock := func() { once.Do(release) }
	defer unblock()

	is.True(isBuffered(task.doBuffered(ctx, NewBatch(recs[:1]), newRecordingAckNacker(alog))))
	flushed := make(chan error, 1)
	go func() { flushed <- task.doBuffered(ctx, NewBatch(recs[1:]), newRecordingAckNacker(alog)) }()
	<-blocked // the synchronous flush is in flight

	queued := make(chan error, 1)
	go func() {
		queued <- task.deferOrdered(ctx, func(context.Context) error {
			alog.append("action")
			return nil
		})
	}()
	time.Sleep(30 * time.Millisecond)
	is.Equal(len(alog.snapshot()), 0) // not run ahead of the write

	unblock()
	is.NoErr(<-flushed)
	is.NoErr(<-queued)
	waitForCondition(t, time.Second, func() bool { return len(alog.snapshot()) == 3 })
	is.Equal(alog.snapshot(), []string{"acker.ack", "acker.ack", "action"})
}

// TestDestinationTask_Sync_QueuedAckDoesNotOvertakeTakenBatch: on the synchronous
// path doBuffered took the pending buffer and only afterwards acquired syncMu. In
// that gap a sibling's processor-side ack, queued behind the (already counted)
// flush, could win syncMu in releaseSync and run BEFORE the batch the first
// worker had already taken: that batch may hold the sibling's own earlier
// records, so a crash then skips them. Take and flush must be one unit with
// respect to releaseSync.
func TestDestinationTask_Sync_QueuedAckDoesNotOvertakeTakenBatch(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	task := NewBatchingDestinationTask("d", newFakeDestination("d"), log.Test(t), NoOpConnectorMetrics{}, 2, 0)
	recs := randomRecords(2)
	ev := &eventLog{}

	var once sync.Once
	task.testAfterTake = func() {
		once.Do(func() {
			is.NoErr(task.deferOrdered(ctx, func(context.Context) error {
				ev.append("sibling.action")
				return nil
			}))
		})
	}
	is.True(isBuffered(task.doBuffered(ctx, NewBatch(recs[:1]), newRecordingAckNacker(ev))))
	is.NoErr(task.doBuffered(ctx, NewBatch(recs[1:]), newRecordingAckNacker(ev))) // takes pending, hook fires

	is.Equal(ev.snapshot(), []string{"acker.ack", "acker.ack", "sibling.action"})
}

// TestDestinationTask_Sync_FlushNotOvertakenByQueuedAck: same for flush() (Close
// and Drain), through the same hook point.
func TestDestinationTask_Sync_FlushNotOvertakenByQueuedAck(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	task := NewBatchingDestinationTask("d", newFakeDestination("d"), log.Test(t), NoOpConnectorMetrics{}, 5, 0)
	recs := randomRecords(1)
	ev := &eventLog{}

	var once sync.Once
	task.testAfterTake = func() {
		once.Do(func() {
			is.NoErr(task.deferOrdered(ctx, func(context.Context) error {
				ev.append("sibling.action")
				return nil
			}))
		})
	}
	is.True(isBuffered(task.doBuffered(ctx, NewBatch(recs), newRecordingAckNacker(ev))))
	is.NoErr(task.flush(ctx))

	is.Equal(ev.snapshot(), []string{"acker.ack", "sibling.action"})
}

// TestSyncAudit_QueuedAckNotWrittenAheadOfTakenFlush is the ack-path audit's
// repro: the queued ack must not run while the batch taken for the flush has
// not been written.
func TestSyncAudit_QueuedAckNotWrittenAheadOfTakenFlush(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	dest := newFakeDestination("d")
	task := NewBatchingDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{}, 2, 0)
	alog := &eventLog{}
	writtenAtAction := -1
	task.testAfterTake = func() {
		task.testAfterTake = nil
		is.NoErr(task.deferOrdered(ctx, func(context.Context) error {
			writtenAtAction = len(dest.receivedPositions())
			alog.append("action")
			return nil
		}))
	}
	is.NoErr(task.doBuffered(ctx, NewBatch(randomRecords(2)), newRecordingAckNacker(alog)))
	is.True(writtenAtAction != 0) // the action ran after the write (or not at all yet)
	is.Equal(alog.snapshot(), []string{"acker.ack", "action"})
}

// TestReleaseSync_ResetsBufferedBytes: composition of the synchronous release path
// with the byte cap. releaseSync takes pending; it must clear the byte count with
// the record count, or the next small batch flushes early.
func TestReleaseSync_ResetsBufferedBytes(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	task := NewBatchingDestinationTask("d", newFakeDestination("d"), log.Test(t), NoOpConnectorMetrics{}, 100, 0)
	task.SetMaxReceiveRecordSize(testMaxReceive)
	is.True(isBuffered(task.doBuffered(ctx, NewBatch(bigRecords(1, 1000)), newRecordingAckNacker(&eventLog{}))))
	task.mu.Lock()
	is.True(task.bufferedBytes > 0)
	task.batchDelay = time.Millisecond
	task.flushDeadline = time.Now().Add(-time.Second) // overdue: the queued ack releases the buffer
	task.mu.Unlock()

	is.NoErr(task.deferOrdered(ctx, func(context.Context) error { return nil }))

	task.mu.Lock()
	defer task.mu.Unlock()
	is.Equal(task.bufferedCount, 0)
	is.Equal(task.bufferedBytes, 0)
}

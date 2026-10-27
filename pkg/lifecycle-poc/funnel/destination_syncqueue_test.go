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

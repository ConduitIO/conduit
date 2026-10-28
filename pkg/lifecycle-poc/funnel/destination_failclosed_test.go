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
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// TestEnqueueCtxDropFailsClosed: pipelined enqueueFlush gives up on its ctx and
// drops the batches it took. It must release the slot and record a flush error in
// one step (as drainNotSent does); otherwise a live flusher releases a LATER
// queued source ack past the dropped batch, which was never written.
func TestEnqueueCtxDropFailsClosed(t *testing.T) {
	is := is.New(t)
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	dest := newFakeDestination("d")
	task := NewBatchingDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{}, 2, 0)
	task.SetPipelined()
	is.NoErr(task.Open(runCtx))
	recs := randomRecords(8)
	alog := &eventLog{}
	blocked, unblock := dest.blockWrites(recs[0].Position)
	// batch 0 in flight (blocked), batches 1 and 2 fill flushCh (cap 2)
	for i := 0; i < 3; i++ {
		is.True(isBuffered(task.doBuffered(runCtx, NewBatch(recs[2*i:2*i+2]), newRecordingAckNacker(alog))))
		if i == 0 {
			<-blocked
		}
	}
	// batch 3: the channel is full and the worker's ctx ends while it waits.
	wctx, wcancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer wcancel()
	err := task.doBuffered(wctx, NewBatch(recs[6:8]), newRecordingAckNacker(alog))
	is.True(err != nil)
	is.True(task.flushError() != nil) // failed closed

	// a later filtered batch of a sibling source must not be released
	actionErr := make(chan error, 1)
	go func() {
		actionErr <- task.deferOrdered(runCtx, func(context.Context) error {
			alog.append(fmt.Sprintf("action(written=%d)", len(dest.receivedPositions())))
			return nil
		})
	}()
	unblock()
	is.True(<-actionErr != nil)
	time.Sleep(100 * time.Millisecond)
	for _, e := range alog.snapshot() {
		is.True(!strings.HasPrefix(e, "action")) // never released past the dropped batch
	}
}

// TestClose_ReturnsCodedErrorWhenRecordsAreLeftUnwritten: a stop that leaves
// buffered records unwritten must not be reported clean. Close returns the flush
// error with a stable code (teardown still runs).
func TestClose_ReturnsCodedErrorWhenRecordsAreLeftUnwritten(t *testing.T) {
	for name, pipelined := range map[string]bool{"synchronous": false, "pipelined": true} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			task := NewBatchingDestinationTask("d", failingWriteDestination{newFakeDestination("d")}, log.Test(t), NoOpConnectorMetrics{}, 10, 0)
			if pipelined {
				task.SetPipelined()
			}
			is.NoErr(task.Open(ctx))
			is.True(isBuffered(task.doBuffered(ctx, NewBatch(randomRecords(1)), newRecordingAckNacker(&eventLog{}))))

			err := task.Close(ctx) // flushes the trailing batch, which fails
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code.Reason(), CodeFlushOnCloseFailed.Reason())
		})
	}
}

// TestDeferOrdered_RefusesAfterClose: Close can overlap a Stop that is still
// draining, so "closed" does not prove nothing is ahead; refuse, never ack inline.
func TestDeferOrdered_RefusesAfterClose(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	task := NewBatchingDestinationTask("d", newFakeDestination("d"), log.Test(t), NoOpConnectorMetrics{}, 10, 0)
	task.SetPipelined()
	is.NoErr(task.Open(ctx))
	is.NoErr(task.Close(ctx))

	var ran bool
	err := task.deferOrdered(ctx, func(context.Context) error {
		ran = true
		return nil
	})
	is.True(err != nil)
	is.True(!ran)
}

// blockingCanceledDestination fails Write with a bare context.Canceled, as a
// plugin's own transport can, while the run itself is still alive.
type blockingCanceledDestination struct{ *fakeDestination }

func (blockingCanceledDestination) Write(context.Context, []opencdc.Record) error {
	return context.Canceled
}

// TestClose_PluginCanceledIsNotAStopCancellation: a bare context.Canceled coming
// from the destination plugin while the run is alive is a real failure; Close
// must still report the unwritten records. Only the run's own cancellation (a
// forced stop) is the expected replay case.
func TestClose_PluginCanceledIsNotAStopCancellation(t *testing.T) {
	is := is.New(t)
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	task := NewBatchingDestinationTask("d", blockingCanceledDestination{newFakeDestination("d")}, log.Test(t), NoOpConnectorMetrics{}, 10, 0)
	is.NoErr(task.Open(runCtx))
	is.True(isBuffered(task.doBuffered(runCtx, NewBatch(randomRecords(1)), newRecordingAckNacker(&eventLog{}))))

	err := task.Close(context.Background()) // run ctx still alive
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeFlushOnCloseFailed.Reason())
}

// TestClose_RunCancellationIsNotReportedAsFailure: when the run's own context was
// cancelled (force stop), the unwritten records replay and Close stays quiet.
func TestClose_RunCancellationIsNotReportedAsFailure(t *testing.T) {
	is := is.New(t)
	runCtx, cancelRun := context.WithCancel(context.Background())
	task := NewBatchingDestinationTask("d", blockingCanceledDestination{newFakeDestination("d")}, log.Test(t), NoOpConnectorMetrics{}, 10, 0)
	is.NoErr(task.Open(runCtx))
	is.True(isBuffered(task.doBuffered(runCtx, NewBatch(randomRecords(1)), newRecordingAckNacker(&eventLog{}))))

	cancelRun() // the run is force-stopped
	is.NoErr(task.Close(context.Background()))
}

// TestEnqueueFlush_SendRacingFlusherExitLeavesNothingStranded: a sender can win the
// send select just as the flusher exits (both cases ready). The batch must not be
// left in flushCh with nobody to complete or count it: the sender must notice the
// flusher is gone, reclaim it, and fail closed. Looped because the select is random.
func TestEnqueueFlush_SendRacingFlusherExitLeavesNothingStranded(t *testing.T) {
	for i := 0; i < 40; i++ {
		is := is.New(t)
		runCtx, cancelRun := context.WithCancel(context.Background())
		task := NewBatchingDestinationTask("d", newFakeDestination("d"), log.Test(t), NoOpConnectorMetrics{}, 2, 0)
		task.SetPipelined()
		is.NoErr(task.Open(runCtx))

		task.testBeforeEnqueueSend = func() {
			task.testBeforeEnqueueSend = nil
			cancelRun() // the flusher exits (ctx.Done) ...
			<-task.flusherDone
			time.Sleep(time.Millisecond) // ... and has finished its exit drain
		}
		is.True(isBuffered(task.doBuffered(context.Background(), NewBatch(randomRecords(1)), newRecordingAckNacker(&eventLog{}))))
		err := task.doBuffered(context.Background(), NewBatch(randomRecords(1)), newRecordingAckNacker(&eventLog{})) // triggers the flush
		is.True(err != nil)                                                                                          // fails closed, never "buffered" into a dead flusher
		is.Equal(len(task.flushCh), 0)
		task.mu.Lock()
		is.Equal(task.outstanding, 0)
		task.mu.Unlock()
		cancelRun()
	}
}

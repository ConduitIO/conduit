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
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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
	blocked, release := dest.blockWrites(recs[0].Position)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(release) }
	// The flusher and helper goroutines log through t: wait for them before the
	// test returns.
	t.Cleanup(func() {
		unblock()
		cancelRun()
		<-task.flusherExited
	})
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
			cancelRun()          // the flusher exits (ctx.Done) ...
			<-task.flusherExited // ... and has finished its exit drain
		}
		is.True(isBuffered(task.doBuffered(context.Background(), NewBatch(randomRecords(1)), newRecordingAckNacker(&eventLog{}))))
		err := task.doBuffered(context.Background(), NewBatch(randomRecords(1)), newRecordingAckNacker(&eventLog{})) // triggers the flush
		is.True(err != nil)                                                                                          // fails closed, never "buffered" into a dead flusher
		is.Equal(len(task.flushCh), 0)
		task.mu.Lock()
		is.Equal(task.outstanding, 0)
		task.mu.Unlock()
		cancelRun()
		<-task.flusherExited
	}
}

// TestRunFlusher_ClosesDoneBeforeDrainingQueue: the order is load-bearing. A
// sender that wins its send select just as the flusher exits must be able to see
// the flusher is gone; that only works if flusherDone is closed before the exit
// drain runs (a send landing between a drain and a late close would return
// "buffered" into a dead channel).
func TestRunFlusher_ClosesDoneBeforeDrainingQueue(t *testing.T) {
	is := is.New(t)
	runCtx, cancelRun := context.WithCancel(context.Background())
	task := NewBatchingDestinationTask("d", newFakeDestination("d"), log.Test(t), NoOpConnectorMetrics{}, 2, 0)
	task.SetPipelined()
	is.NoErr(task.Open(runCtx))

	closedAtMidExit := make(chan bool, 1)
	task.testMidFlusherExit = func() {
		select {
		case <-task.flusherDone:
			closedAtMidExit <- true
		default:
			closedAtMidExit <- false
		}
	}
	cancelRun()
	select {
	case closed := <-closedAtMidExit:
		is.True(closed)
	case <-time.After(time.Second):
		t.Fatal("flusher did not exit")
	}
	<-task.flusherExited
}

// TestCtxMutex covers the stop-path lock: LockCtx gives up with the context's
// error without taking the lock, and takes it when it is free.
func TestCtxMutex(t *testing.T) {
	is := is.New(t)
	var m ctxMutex

	is.NoErr(m.LockCtx(context.Background()))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	is.True(m.LockCtx(ctx) != nil) // held: gives up at the deadline

	m.Unlock()
	is.NoErr(m.LockCtx(context.Background())) // the failed attempt did not leave it locked
	m.Unlock()

	m.Lock()
	acquired := make(chan struct{})
	go func() { m.Lock(); close(acquired); m.Unlock() }()
	select {
	case <-acquired:
		t.Fatal("second Lock acquired a held mutex")
	case <-time.After(20 * time.Millisecond):
	}
	m.Unlock()
	<-acquired
}

// chunkHookDestination runs onWrite(n) at the start of its n-th Write (1-based)
// and fails every Write from failFrom on.
type chunkHookDestination struct {
	*fakeDestination
	mu       sync.Mutex
	n        int
	failFrom int
	onWrite  func(n int)
}

func (d *chunkHookDestination) Write(ctx context.Context, recs []opencdc.Record) error {
	d.mu.Lock()
	d.n++
	n := d.n
	d.mu.Unlock()
	if d.onWrite != nil {
		d.onWrite(n)
	}
	if d.failFrom > 0 && n >= d.failFrom {
		return fmt.Errorf("chunk %d rejected", n)
	}
	return d.fakeDestination.Write(ctx, recs)
}

// TestChunkedFlush_SecondChunkFailsWhileSiblingAckQueued: wire-size chunking
// (several Writes per flush) composed with the ordered-ack machinery. A sibling's
// filtered batch is queued while chunk 1 is in flight; chunk 2 then fails. The
// slot stays outstanding for the whole chunked flush (so the ack cannot run
// inline), the failure is recorded in the same step that releases it, and the
// queued ack must never run: the records of the failed chunk are unwritten and a
// later flush (Close) must not release anything past them.
func TestChunkedFlush_SecondChunkFailsWhileSiblingAckQueued(t *testing.T) {
	for name, pipelined := range map[string]bool{"synchronous": false, "pipelined": true} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			var ran atomic.Bool
			var task *DestinationTask
			dest := &chunkHookDestination{fakeDestination: newFakeDestination("d"), failFrom: 2}
			dest.onWrite = func(n int) {
				if n == 1 {
					// A sibling's whole-batch filter lands while chunk 1 is in flight.
					err := task.deferOrdered(ctx, func(context.Context) error {
						ran.Store(true)
						return nil
					})
					if err != nil {
						t.Errorf("deferOrdered while chunk 1 in flight: %v", err)
					}
				}
			}
			task = NewBatchingDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{}, 100, 0)
			task.SetMaxReceiveRecordSize(testMaxReceive) // ~2 records of 1 KB per chunk
			if pipelined {
				task.SetPipelined()
			}
			runCtx, cancelRun := context.WithCancel(ctx)
			defer cancelRun()
			is.NoErr(task.Open(runCtx))

			recs := bigRecords(5, 1000) // 3 chunks
			alog := &eventLog{}
			err := task.doBuffered(ctx, NewBatch(recs), newRecordingAckNacker(alog))
			if pipelined {
				is.True(isBuffered(err))
			} else {
				is.True(err != nil) // the synchronous flush surfaces chunk 2's failure
			}

			if pipelined {
				waitForCondition(t, 2*time.Second, func() bool { return task.flushError() != nil })
			} else {
				is.True(task.flushError() != nil) // the failed flush recorded it before returning
			}
			is.True(!ran.Load()) // the queued ack did not run
			task.mu.Lock()
			is.Equal(task.outstanding, 0) // the slot was released exactly once
			task.mu.Unlock()

			_ = task.Close(ctx) // must not flush the queued ack either
			is.True(!ran.Load())
			is.Equal(len(alog.snapshot()), 0)          // and the failed batch was never acked
			is.Equal(len(dest.receivedPositions()), 2) // only chunk 1 reached the destination
		})
	}
}

// TestDeferOrdered_ReleaseResetsBufferedBytes: composition of the byte cap with
// the ordered-ack queue. When deferOrdered hands the buffer to the flusher it must
// clear the buffered byte count too; a stale count makes the next small batch
// flush early.
func TestDeferOrdered_ReleaseResetsBufferedBytes(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	dest := newFakeDestination("d")
	task := NewBatchingDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{}, 100, 0)
	task.SetMaxReceiveRecordSize(testMaxReceive)
	task.SetPipelined()
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	is.NoErr(task.Open(runCtx))
	t.Cleanup(func() { cancelRun(); <-task.flusherExited })

	recs := bigRecords(2, 1000)
	is.True(isBuffered(task.doBuffered(ctx, NewBatch(recs[:1]), newRecordingAckNacker(&eventLog{}))))
	task.mu.Lock()
	is.True(task.bufferedBytes > 0)
	task.mu.Unlock()

	// A queued ack with a deadline-free full queue: force the release path.
	task.mu.Lock()
	task.batchDelay = time.Millisecond
	task.flushDeadline = time.Now().Add(-time.Second)
	task.mu.Unlock()
	is.NoErr(task.deferOrdered(ctx, func(context.Context) error { return nil }))

	task.mu.Lock()
	defer task.mu.Unlock()
	is.Equal(task.bufferedCount, 0)
	is.Equal(task.bufferedBytes, 0)
}

// TestCtxMutex_LockCtxAbandonedWaiterReleases: a LockCtx that gave up must not
// leave the mutex held when its helper finally acquires it.
func TestCtxMutex_LockCtxAbandonedWaiterReleases(t *testing.T) {
	is := is.New(t)
	var m ctxMutex
	m.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	is.True(m.LockCtx(ctx) != nil) // gave up while the lock was held
	m.Unlock()                     // the abandoned helper now acquires and must release
	done := make(chan struct{})
	go func() { m.Lock(); close(done); m.Unlock() }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("mutex left held by an abandoned LockCtx")
	}
}

// TestCtxMutex_AbandonedHelperExitsOnRelease: LockCtx whose context won while the
// holder is wedged leaves one helper goroutine parked in Lock(). It cannot outlive
// the holder: when the holder releases (teardown, a force-stop unwedging it), the
// helper acquires, sees it was abandoned, releases and exits. No goroutine and no
// lock is left behind.
func TestCtxMutex_AbandonedHelperExitsOnRelease(t *testing.T) {
	is := is.New(t)
	var m ctxMutex
	before := runtime.NumGoroutine()

	m.Lock() // a sibling mid-pass behind a wedged destination
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		is.True(m.LockCtx(ctx) != nil) // each stop attempt times out
		cancel()
	}
	is.True(runtime.NumGoroutine() >= before+10) // the parked helpers

	m.Unlock() // teardown: the holder finally lets go
	waitForCondition(t, 2*time.Second, func() bool { return runtime.NumGoroutine() <= before })

	done := make(chan struct{})
	go func() { m.Lock(); close(done); m.Unlock() }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("mutex left held after the helpers exited")
	}
}

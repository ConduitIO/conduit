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
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/matryer/is"
)

// failWriteDestination is a fakeDestination whose every Write fails with err.
type failWriteDestination struct {
	*fakeDestination
	err    error
	writes atomic.Int64
}

func (d *failWriteDestination) Write(context.Context, []opencdc.Record) error {
	d.writes.Add(1)
	return d.err
}

// TestWorker_FlusherErrorWhileIdle_FailsRun is the regression test for the idle
// flusher error (docs/design-documents/20261103-archv2-destination-batch-timing.md,
// Rollout step 1). The worker reads one full batch, hands it to the flusher and
// goes back to the source, which then has nothing more and blocks in Read. The
// flusher's Write fails. Nothing used to tell the worker: the error was read only
// by the next doBuffered, Drain or Close, so the run stayed blocked in Read and
// the pipeline reported running. The run's fail func must be called, with the
// error's fatal classification intact.
func TestWorker_FlusherErrorWhileIdle_FailsRun(t *testing.T) {
	for name, writeErr := range map[string]error{
		"non-fatal": cerrors.New("destination write failed"),
		"fatal":     cerrors.FatalError(cerrors.New("destination write failed for good")),
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			logger := log.Test(t)
			runCtx, cancel := context.WithCancel(context.Background())
			defer cancel()

			recs := randomRecords(3)
			src := newFakeSource("src", recs) // serves recs once, then blocks in Read
			dest := &failWriteDestination{fakeDestination: newFakeDestination("dest"), err: writeErr}
			dt := NewBatchingDestinationTask("dest", dest, logger, NoOpConnectorMetrics{}, len(recs), 0)
			dt.SetPipelined()
			is.NoErr(dt.Open(runCtx))
			srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{{Task: dt}}}
			dlq := NewDLQ("dlq", newFakeDestination("dlq"), logger, NoOpConnectorMetrics{}, 10, 10)
			w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
			is.NoErr(err)

			// fail does what the tomb kill does in pkg/lifecycle-poc: it ends the
			// run's context, which is what unblocks the source Read.
			got := make(chan error, 1)
			fail := func(err error) {
				got <- err
				cancel()
			}
			stop := w.WatchConnectorErrors(runCtx, fail)
			defer stop()
			doDone := make(chan error, 1)
			go func() { doDone <- w.Do(runCtx) }()
			doReturned := false
			defer func() {
				cancel()
				if !doReturned {
					<-doDone
				}
				_ = dt.Close(context.Background())
			}()

			var runErr error
			select {
			case runErr = <-got:
			case <-time.After(5 * time.Second):
				t.Fatalf("run was not failed: the flusher's Write failed (%d writes) while the source was idle, "+
					"and the error never reached the run", dest.writes.Load())
			}
			is.True(cerrors.Is(runErr, writeErr))
			is.Equal(cerrors.IsFatalError(runErr), cerrors.IsFatalError(writeErr)) // classification survives
			<-doDone
			doReturned = true
			is.Equal(len(src.ackedPositions()), 0) // invariant 1: nothing acked upstream
		})
	}
}

// gatedDestination is a fakeDestination whose Write blocks until release is
// called and then fails with err, and whose Ack can be made to wait for its
// context (ackWaitsForCtx), the way connector.Destination.Ack returns when its
// ctx ends.
type gatedDestination struct {
	*fakeDestination
	err            error
	started        chan struct{}
	gate           chan struct{}
	startOnce      sync.Once
	releaseOnce    sync.Once
	ackWaitsForCtx bool
}

func newGatedDestination(id string, err error) *gatedDestination {
	return &gatedDestination{
		fakeDestination: newFakeDestination(id),
		err:             err,
		started:         make(chan struct{}),
		gate:            make(chan struct{}),
	}
}

func (d *gatedDestination) release() { d.releaseOnce.Do(func() { close(d.gate) }) }

func (d *gatedDestination) Write(ctx context.Context, recs []opencdc.Record) error {
	if d.ackWaitsForCtx {
		return d.fakeDestination.Write(ctx, recs)
	}
	d.startOnce.Do(func() { close(d.started) })
	<-d.gate
	return d.err
}

func (d *gatedDestination) Ack(ctx context.Context) ([]connector.DestinationAck, error) {
	if d.ackWaitsForCtx {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return d.fakeDestination.Ack(ctx)
}

// assertNoFlushFailure checks that nothing was reported to the run: the task's
// flush-failure channel is empty and the watch never called fail. stop waits for
// every reader, so a reader that took an error has called fail by then.
func assertNoFlushFailure(t *testing.T, dt *DestinationTask, stop func(), calls *atomic.Int64) {
	t.Helper()
	stop()
	if n := len(dt.flushFailures); n != 0 {
		t.Fatalf("%d flush failures queued for the run, want 0: %v", n, <-dt.flushFailures)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("run failed %d times, want 0", n)
	}
}

// TestWorker_StopDrainTimeout_DoesNotFailRun: a graceful Stop whose deadline
// ends while the drain waits on a wedged flusher (its channel is full). The
// drain records its failure (drainNotSent) so siblings fail closed, and the
// in-flight flush then fails too. Neither may fail the run: the worker is
// stopping and the records replay. Nothing is acked.
func TestWorker_StopDrainTimeout_DoesNotFailRun(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	src := &teardownSource{fakeSource: newFakeSource("src", nil)}
	dest := newGatedDestination("dest", cerrors.New("write failed after the stop deadline"))
	defer dest.release()
	dt := NewBatchingDestinationTask("dest", dest, logger, NoOpConnectorMetrics{}, 2, 0) // every 2-record batch flushes
	dt.SetPipelined()
	is.NoErr(dt.Open(ctx))
	destNode := &TaskNode{Task: dt}
	srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{destNode}}
	w, err := NewWorker(srcNode, NewDLQ("dlq", newFakeDestination("dlq"), logger, NoOpConnectorMetrics{}, 10, 10), logger, noop.Timer{})
	is.NoErr(err)
	fail, calls, _ := failRecorder()
	stop := w.WatchConnectorErrors(ctx, fail)

	recs := randomRecords(7)
	is.NoErr(w.doTask(ctx, destNode, NewBatch(recs[0:2]), newRunAckNacker(w)))
	<-dest.started // the flusher is wedged in Write
	is.NoErr(w.doTask(ctx, destNode, NewBatch(recs[2:4]), newRunAckNacker(w)))
	is.NoErr(w.doTask(ctx, destNode, NewBatch(recs[4:6]), newRunAckNacker(w))) // flushCh is full now
	is.NoErr(w.doTask(ctx, destNode, NewBatch(recs[6:7]), newRunAckNacker(w))) // partial, buffered

	sctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	is.NoErr(w.Stop(sctx)) // the drain cannot reach the flusher before the deadline
	cancel()
	is.True(dt.flushError() != nil) // drainNotSent failed the task closed

	dest.release() // the in-flight flush fails now, behind the recorded drain error
	<-dt.flusherExited

	assertNoFlushFailure(t, dt, stop, calls)
	accepted, refused := src.snapshot()
	is.Equal(len(accepted), 0) // invariant 1: nothing acked, everything replays
	is.Equal(len(refused), 0)
	_ = dt.Close(ctx)
}

// TestWorker_SyncStopDrainDeadline_DoesNotFailRun is the synchronous (fan-out)
// form of the stop deadline: Drain flushes on the stopping goroutine with the
// stop context, and the destination's Ack returns when that context ends. The
// flush's own failure is recorded (finishFlush), but it is the stop deadline,
// not a destination failure, so it must not fail the run.
func TestWorker_SyncStopDrainDeadline_DoesNotFailRun(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	src := &teardownSource{fakeSource: newFakeSource("src", nil)}
	dest := newGatedDestination("dest", nil)
	dest.ackWaitsForCtx = true
	dt := NewBatchingDestinationTask("dest", dest, logger, NoOpConnectorMetrics{}, 10, 0) // not pipelined
	is.NoErr(dt.Open(ctx))
	destNode := &TaskNode{Task: dt}
	srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{destNode}}
	w, err := NewWorker(srcNode, NewDLQ("dlq", newFakeDestination("dlq"), logger, NoOpConnectorMetrics{}, 10, 10), logger, noop.Timer{})
	is.NoErr(err)
	fail, calls, _ := failRecorder()
	stop := w.WatchConnectorErrors(ctx, fail)

	is.NoErr(w.doTask(ctx, destNode, NewBatch(randomRecords(2)), newRunAckNacker(w))) // buffered
	sctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	is.NoErr(w.Stop(sctx))
	cancel()
	is.True(cerrors.Is(dt.flushError(), context.DeadlineExceeded)) // the drain's flush failed on the deadline

	assertNoFlushFailure(t, dt, stop, calls)
	accepted, _ := src.snapshot()
	is.Equal(len(accepted), 0)
	_ = dt.Close(ctx)
}

// TestDestinationTask_FlushAfterSourceTeardown_DoesNotFailRun: the stop drain did
// not start (the shared lock was held past the deadline), the source was torn
// down, and the flush that Close then runs fails its source ack with
// plugin.ErrPluginNotRunning. That is the expected replay case Close logs; it is
// not sent to the run.
func TestDestinationTask_FlushAfterSourceTeardown_DoesNotFailRun(t *testing.T) {
	for name, pipelined := range map[string]bool{"pipelined": true, "synchronous": false} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			logger := log.Test(t)

			src := &teardownSource{fakeSource: newFakeSource("src", nil)}
			dest := newFakeDestination("dest")
			dt := NewBatchingDestinationTask("dest", dest, logger, NoOpConnectorMetrics{}, 10, 0)
			if pipelined {
				dt.SetPipelined()
			}
			is.NoErr(dt.Open(ctx))
			shared := &TaskNode{Task: dt}
			shared.MarkSharedBoundary()
			srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{shared}}
			w, err := NewWorker(srcNode, NewDLQ("dlq", newFakeDestination("dlq"), logger, NoOpConnectorMetrics{}, 10, 10), logger, noop.Timer{})
			is.NoErr(err)
			fail, calls, _ := failRecorder()
			stop := w.WatchConnectorErrors(ctx, fail)

			recs := randomRecords(2)
			is.True(isBuffered(dt.doBuffered(ctx, NewBatch(recs), newRunAckNacker(w))))
			shared.sharedMu.Lock() // a sibling mid-pass: the drain does not start
			sctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			is.NoErr(w.Stop(sctx))
			cancel()
			shared.sharedMu.Unlock()

			is.NoErr(dt.Close(ctx)) // logged, not reported: ErrPluginNotRunning is the replay case
			is.True(cerrors.Is(dt.flushError(), plugin.ErrPluginNotRunning))
			is.Equal(len(dest.receivedPositions()), 2) // written, but the ack was refused: they replay

			assertNoFlushFailure(t, dt, stop, calls)
			_, refused := src.snapshot()
			is.Equal(len(refused), 2)
		})
	}
}

// TestDestinationTask_PoisonEcho_NotSent: a sibling's failed pass poisons the
// task (setFlushErr). That failure already failed the run where it happened, so
// neither the poisoning nor a flush failing behind it is sent again.
func TestDestinationTask_PoisonEcho_NotSent(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	dest := newGatedDestination("dest", cerrors.New("write failed behind the poison"))
	defer dest.release()
	dt := NewBatchingDestinationTask("dest", dest, logger, NoOpConnectorMetrics{}, 2, 0) // every 2-record batch flushes
	dt.SetPipelined()
	is.NoErr(dt.Open(ctx))
	node := &TaskNode{Task: dt}
	srcNode := &TaskNode{Task: NewSourceTask("src", newFakeSource("src", nil), logger, NoOpConnectorMetrics{}), Next: []*TaskNode{node}}
	w, err := NewWorker(srcNode, NewDLQ("dlq", newFakeDestination("dlq"), logger, NoOpConnectorMetrics{}, 10, 10), logger, noop.Timer{})
	is.NoErr(err)
	fail, calls, _ := failRecorder()
	stop := w.WatchConnectorErrors(ctx, fail)

	is.True(isBuffered(dt.doBuffered(ctx, NewBatch(randomRecords(2)), newRunAckNacker(w))))
	<-dest.started // a flush is in flight

	siblingErr := cerrors.New("sibling pass failed")
	poisonBatchingDestinations(node, siblingErr)
	is.True(cerrors.Is(dt.doBuffered(ctx, NewBatch(randomRecords(1)), newRunAckNacker(w)), siblingErr))

	dest.release() // the in-flight flush fails behind the poison
	<-dt.flusherExited
	is.True(cerrors.Is(dt.flushError(), siblingErr)) // the first error stays the poison

	assertNoFlushFailure(t, dt, stop, calls)
	_ = dt.Close(ctx)
}

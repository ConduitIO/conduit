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
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/matryer/is"
)

// The flush timer tests (#3231, docs/design-documents/20261103-archv2-destination-batch-timing.md,
// "Test plan") run in a testing/synctest bubble: time is fake, so a 1 s delay
// costs nothing and "at 999 ms nothing was written" is exact, not a race.

// timedWrite is one Write a timedDestination received.
type timedWrite struct {
	at        time.Duration // fake time since the test started
	positions []opencdc.Position
}

// timedDestination is a fakeDestination that also records when each Write
// arrived, can fail or block a Write, and fails the test on any call after
// Teardown.
type timedDestination struct {
	*fakeDestination
	t     *testing.T
	start time.Time

	mu       sync.Mutex
	writes   []timedWrite
	writeErr error
	gate     chan struct{} // if set, Write waits for it (or ctx) before recording
	entered  chan struct{} // closed when a Write first waits on gate
	enterOne sync.Once
	torn     bool
}

func newTimedDestination(t *testing.T, id string) *timedDestination {
	return &timedDestination{fakeDestination: newFakeDestination(id), t: t, start: time.Now()}
}

func (d *timedDestination) Write(ctx context.Context, recs []opencdc.Record) error {
	d.mu.Lock()
	if d.torn {
		d.mu.Unlock()
		d.t.Errorf("destination %s: Write after Teardown", d.id)
		return cerrors.New("write after teardown")
	}
	gate, entered, werr := d.gate, d.entered, d.writeErr
	d.mu.Unlock()
	if gate != nil {
		d.enterOne.Do(func() { close(entered) })
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	d.mu.Lock()
	w := timedWrite{at: time.Since(d.start)}
	for _, r := range recs {
		w.positions = append(w.positions, r.Position)
	}
	d.writes = append(d.writes, w)
	d.mu.Unlock()
	if werr != nil {
		return werr
	}
	return d.fakeDestination.Write(ctx, recs)
}

func (d *timedDestination) Ack(ctx context.Context) ([]connector.DestinationAck, error) {
	d.mu.Lock()
	torn := d.torn
	d.mu.Unlock()
	if torn {
		d.t.Errorf("destination %s: Ack after Teardown", d.id)
	}
	return d.fakeDestination.Ack(ctx)
}

func (d *timedDestination) Teardown(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.torn = true
	return nil
}

// block makes the next Writes wait until the returned release is called.
func (d *timedDestination) block() (entered <-chan struct{}, release func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gate = make(chan struct{})
	d.entered = make(chan struct{})
	d.enterOne = sync.Once{}
	gate := d.gate
	var once sync.Once
	return d.entered, func() { once.Do(func() { close(gate) }) }
}

func (d *timedDestination) failWrites(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writeErr = err
}

func (d *timedDestination) timedWrites() []timedWrite {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]timedWrite(nil), d.writes...)
}

// writeSizes is the number of records of each Write, in order.
func (d *timedDestination) writeSizes() []int {
	var out []int
	for _, w := range d.timedWrites() {
		out = append(out, len(w.positions))
	}
	return out
}

// chanSource is a Source whose Read returns the batches the test pushes, one
// per Read, and otherwise blocks like an idle source (until Teardown or ctx).
// Ack records each call's positions and, if lockOf is set, whether that
// worker's processing lock was held at the call.
type chanSource struct {
	*fakeSource
	batches chan []opencdc.Record

	mu        sync.Mutex
	ackCalls  [][]opencdc.Position
	lockOf    *Worker
	ackLocked []bool
}

func newChanSource(id string) *chanSource {
	return &chanSource{fakeSource: newFakeSource(id, nil), batches: make(chan []opencdc.Record, 16)}
}

func (s *chanSource) push(recs ...opencdc.Record) { s.batches <- recs }

func (s *chanSource) Read(ctx context.Context) ([]opencdc.Record, error) {
	select {
	case recs := <-s.batches:
		return recs, nil
	case <-s.stopped:
		return nil, context.Canceled
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *chanSource) Ack(ctx context.Context, positions []opencdc.Position) error {
	s.mu.Lock()
	s.ackCalls = append(s.ackCalls, append([]opencdc.Position(nil), positions...))
	if s.lockOf != nil {
		s.ackLocked = append(s.ackLocked, len(s.lockOf.processingLock) == 1)
	}
	s.mu.Unlock()
	return s.fakeSource.Ack(ctx, positions)
}

func (s *chanSource) acksUnderLock() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.ackLocked...)
}

// lockCheckingDestination is a fakeDestination (used as a DLQ) that records, at
// every Write, whether w's processing lock was held.
type lockCheckingDestination struct {
	*fakeDestination
	w *Worker

	mu     sync.Mutex
	locked []bool
}

func (d *lockCheckingDestination) Write(ctx context.Context, recs []opencdc.Record) error {
	d.mu.Lock()
	d.locked = append(d.locked, len(d.w.processingLock) == 1)
	d.mu.Unlock()
	return d.fakeDestination.Write(ctx, recs)
}

func (d *lockCheckingDestination) writesUnderLock() []bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]bool(nil), d.locked...)
}

// assertMonotonicAcks fails unless every position the source was acked with is
// strictly greater than the one before (randomRecords positions are decimal
// indexes, compared numerically) and none repeats (invariant 2).
func assertMonotonicAcks(t *testing.T, acked []opencdc.Position) {
	t.Helper()
	last := -1
	for _, p := range acked {
		n := 0
		for _, c := range p {
			n = n*10 + int(c-'0')
		}
		if n <= last {
			t.Fatalf("source acks not strictly increasing: %q after %d (all: %q)", p, last, acked)
		}
		last = n
	}
}

// sleep advances fake time by d and waits until every goroutine in the bubble
// is blocked again.
func sleep(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

// goDo runs w.Do(ctx) and returns a func that waits for it and returns its
// error. A cleanup cancels ctx and waits too, so a test that fails early does
// not leave the worker running in the synctest bubble.
func goDo(t *testing.T, ctx context.Context, cancel context.CancelFunc, w *Worker) func() error {
	t.Helper()
	done := make(chan struct{})
	var err error
	go func() {
		err = w.Do(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return func() error {
		<-done
		return err
	}
}

func newTimerTestWorker(t *testing.T, src Source, first *TaskNode, dlq *DLQ) *Worker {
	t.Helper()
	return newTimerTestWorkerNext(t, src, dlq, first)
}

// sharedRoots wraps each destination task in its own shared boundary node: the
// shape buildSharedTail builds for M destinations and no pipeline processors.
// The source's tail is then the fan-out point, so a pass holds one branch's
// shared mutex at a time, and only the processing lock serialises a timer of
// one branch with a pass in another.
func sharedRoots(tasks ...Task) []*TaskNode {
	out := make([]*TaskNode, len(tasks))
	for i, task := range tasks {
		out[i] = &TaskNode{Task: task}
		out[i].MarkSharedBoundary()
	}
	return out
}

func newTimerTestWorkerNext(t *testing.T, src Source, dlq *DLQ, next ...*TaskNode) *Worker {
	t.Helper()
	logger := log.Test(t)
	if dlq == nil {
		dlq = NewDLQ("dlq", newFakeDestination("dlq"), logger, NoOpConnectorMetrics{}, 10, 10)
	}
	srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: next}
	w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
	is.New(t).NoErr(err)
	return w
}

// TestDestinationTask_TimerFlushesIdleTail is the regression test for #3231:
// one partial batch (5,088 records, size 10,000, delay 1 s) and then no more
// arrivals. On main nothing ever wrote it until a stop. Now it is written at
// the deadline, in one Write, and acked to the source once, after the write.
func TestDestinationTask_TimerFlushesIdleTail(t *testing.T) {
	for name, pipelined := range map[string]bool{"pipelined": true, "synchronous": false} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				is := is.New(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				dest := newTimedDestination(t, "dest")
				dt := NewBatchingDestinationTask("dest", dest, log.Test(t), NoOpConnectorMetrics{}, 10000, time.Second)
				if pipelined {
					dt.SetPipelined()
				}
				is.NoErr(dt.Open(ctx))
				src := newFakeSource("src", nil)
				w := newTimerTestWorker(t, src, &TaskNode{Task: dt}, nil)

				recs := randomRecords(5088)
				is.True(isBuffered(dt.doBuffered(ctx, NewBatch(recs), newRunAckNacker(w))))

				sleep(999 * time.Millisecond)
				is.Equal(len(dest.timedWrites()), 0) // not before the deadline
				is.Equal(len(src.ackedPositions()), 0)

				sleep(time.Millisecond)
				writes := dest.timedWrites()
				is.Equal(len(writes), 1)
				is.Equal(writes[0].at, time.Second)            // at the deadline, from the first record
				is.Equal(writes[0].positions, posOf(recs...))  // the whole tail, once
				is.Equal(src.ackedPositions(), posOf(recs...)) // acked after its write (invariant 1)

				is.NoErr(dt.Close(ctx))
				is.Equal(len(dest.timedWrites()), 1) // nothing left for Close
			})
		})
	}
}

// TestDestinationTask_SizeOnlyBatching_NoTimer: with sdk.batch.size and no
// delay there is no time bound, as in the SDK; the tail waits for a stop.
func TestDestinationTask_SizeOnlyBatching_NoTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		is := is.New(t)
		ctx := context.Background()
		dest := newTimedDestination(t, "dest")
		dt := NewBatchingDestinationTask("dest", dest, log.Test(t), NoOpConnectorMetrics{}, 100, 0)
		dt.SetPipelined()
		is.NoErr(dt.Open(ctx))
		w := newTimerTestWorker(t, newFakeSource("src", nil), &TaskNode{Task: dt}, nil)

		is.True(isBuffered(dt.doBuffered(ctx, NewBatch(randomRecords(3)), newRunAckNacker(w))))
		sleep(time.Hour)
		is.Equal(len(dest.timedWrites()), 0)
		dt.mu.Lock()
		is.True(dt.flushTimer == nil)
		dt.mu.Unlock()
		is.NoErr(dt.Close(ctx))
		is.Equal(dest.writeSizes(), []int{3})
	})
}

// TestWorker_FanOut_TimerFlushesIdleTail: a fan-out with two batching
// destinations whose delays differ (1 s and 2 s). Each tail is written at its
// own deadline, and the source ack for the batch is issued once, when the
// second destination has written it: the ack needs both votes.
func TestWorker_FanOut_TimerFlushesIdleTail(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		is := is.New(t)
		ctx, cancel := context.WithCancel(context.Background())
		logger := log.Test(t)

		destA, destB := newTimedDestination(t, "A"), newTimedDestination(t, "B")
		dtA := NewBatchingDestinationTask("A", destA, logger, NoOpConnectorMetrics{}, 100, time.Second)
		dtB := NewBatchingDestinationTask("B", destB, logger, NoOpConnectorMetrics{}, 100, 2*time.Second)
		fanNode := &TaskNode{Task: passthroughTask{id: "shared"}, Next: []*TaskNode{{Task: dtA}, {Task: dtB}}}
		fanNode.MarkSharedBoundary()
		is.NoErr(dtA.Open(ctx))
		is.NoErr(dtB.Open(ctx))
		src := newChanSource("src")
		w := newTimerTestWorker(t, src, fanNode, nil)
		src.lockOf = w
		waitDo := goDo(t, ctx, cancel, w)

		recs := randomRecords(5)
		src.push(recs...)
		synctest.Wait()
		is.Equal(len(destA.timedWrites())+len(destB.timedWrites()), 0)

		sleep(time.Second)
		is.Equal(destA.writeSizes(), []int{5})
		is.Equal(len(destB.timedWrites()), 0)
		is.Equal(len(src.ackedPositions()), 0) // B has not voted

		sleep(time.Second)
		is.Equal(destB.writeSizes(), []int{5})
		is.Equal(destB.timedWrites()[0].at, 2*time.Second)
		is.Equal(src.ackedPositions(), posOf(recs...))
		is.Equal(src.acksUnderLock(), []bool{true}) // issued while the timer held the processing lock

		cancel()
		is.True(cerrors.Is(waitDo(), context.Canceled))
		is.NoErr(dtA.Close(context.Background()))
		is.NoErr(dtB.Close(context.Background()))
	})
}

// TestWorker_FanOut_TimerWaitsForPass: destination A's timer fires while a
// pass is in flight (its write to the unbatched destination B is held open).
// The timer's flush must wait for the pass to release the processing lock, so
// it never runs during a pass, and every source ack stays strictly increasing.
func TestWorker_FanOut_TimerWaitsForPass(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		is := is.New(t)
		ctx, cancel := context.WithCancel(context.Background())
		logger := log.Test(t)

		destA, destB := newTimedDestination(t, "A"), newTimedDestination(t, "B")
		dtA := NewBatchingDestinationTask("A", destA, logger, NoOpConnectorMetrics{}, 100, time.Second)
		dtB := NewDestinationTask("B", destB, logger, NoOpConnectorMetrics{})
		roots := sharedRoots(dtA, dtB)
		is.NoErr(dtA.Open(ctx))
		src := newChanSource("src")
		w := newTimerTestWorkerNext(t, src, nil, roots...)
		var passDone, flushedDuringPass atomic.Bool
		dtA.testTimerHoldsLocks = func() {
			if !passDone.Load() {
				flushedDuringPass.Store(true)
			}
		}
		waitDo := goDo(t, ctx, cancel, w)

		recs := randomRecords(2)
		src.push(recs[0]) // pass 1: buffered in A, written by B
		synctest.Wait()

		sleep(500 * time.Millisecond)
		entered, release := destB.block()
		src.push(recs[1]) // pass 2: buffered in A; B's write is held open
		<-entered
		synctest.Wait()

		sleep(time.Second) // A's deadline (1 s) passes while pass 2 holds the lock
		is.Equal(len(destA.timedWrites()), 0)
		is.Equal(len(src.ackedPositions()), 0)

		passDone.Store(true)
		release()
		synctest.Wait()
		is.True(!flushedDuringPass.Load())
		is.Equal(destA.writeSizes(), []int{2}) // both batches, one Write, after the pass
		is.Equal(src.ackedPositions(), posOf(recs...))
		assertMonotonicAcks(t, src.ackedPositions())

		cancel()
		_ = waitDo()
		is.NoErr(dtA.Close(context.Background()))
	})
}

// TestWorker_FanOut_TimerReleasesSiblingNack: the timer form of #3324's
// sibling-nack tests. A batches with a delay, B is unbatched. Batch N (rec0)
// is buffered in A; B nacks rec1 of batch N+1; the source goes idle. The nacked
// record may reach the DLQ only when A's timer flush casts A's vote, not when
// B nacks, and the DLQ write and its source ack must both run while the timer
// holds the processing lock.
func TestWorker_FanOut_TimerReleasesSiblingNack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		is := is.New(t)
		ctx, cancel := context.WithCancel(context.Background())
		logger := log.Test(t)
		recs := randomRecords(2)

		destA, destB := newTimedDestination(t, "A"), newTimedDestination(t, "B")
		destB.setNackErr(recs[1].Position, cerrors.New("B: simulated write failure"))
		dtA := NewBatchingDestinationTask("A", destA, logger, NoOpConnectorMetrics{}, 100, time.Second)
		dtB := NewDestinationTask("B", destB, logger, NoOpConnectorMetrics{})
		roots := sharedRoots(dtA, dtB)
		is.NoErr(dtA.Open(ctx))
		src := newChanSource("src")
		dlqDest := &lockCheckingDestination{fakeDestination: newFakeDestination("dlq")}
		w := newTimerTestWorkerNext(t, src, NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, 10, 10), roots...)
		dlqDest.w, src.lockOf = w, w
		waitDo := goDo(t, ctx, cancel, w)

		src.push(recs[0])
		synctest.Wait()
		src.push(recs[1]) // B nacks it
		synctest.Wait()
		is.Equal(len(dlqDest.receivedPositions()), 0) // not released by B's nack
		is.Equal(len(src.ackedPositions()), 0)

		sleep(time.Second)
		is.Equal(destA.writeSizes(), []int{2})
		is.Equal(dlqDest.receivedPositions(), posOf(recs[1]))
		is.Equal(dlqDest.writesUnderLock(), []bool{true})
		is.Equal(src.ackedPositions(), posOf(recs...))
		is.Equal(src.acksUnderLock(), []bool{true, true})
		assertMonotonicAcks(t, src.ackedPositions())

		cancel()
		_ = waitDo()
		is.NoErr(dtA.Close(context.Background()))
	})
}

// TestWorker_FanOut_TimerReleasesSiblingNack_FailsIdleRun: the same with a DLQ
// that refuses the nack while the source is idle. The release fails inside A's
// timer flush, which is the only thing running, so the failure must reach the
// run through the idle error channel, within the delay, with its fatal or
// non-fatal classification intact. Only positions before the nacked one are
// acked.
func TestWorker_FanOut_TimerReleasesSiblingNack_FailsIdleRun(t *testing.T) {
	for _, tc := range []struct {
		name                string
		window, threshold   int
		nacked              []int // indexes of recs B nacks
		wantFatal           bool
		wantAcked           int // leading records acked
		wantDLQ             int
		wantRecordErrInRoot bool
	}{
		// The default DLQ (window 1, threshold 0): the record error, not fatal.
		{name: "default DLQ", window: 1, threshold: 0, nacked: []int{1}, wantAcked: 1, wantRecordErrInRoot: true},
		// Threshold exceeded: fatal, so it does not enter the restart loop.
		{name: "nack threshold exceeded", window: 2, threshold: 1, nacked: []int{1, 2}, wantFatal: true, wantAcked: 2, wantDLQ: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				is := is.New(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				logger := log.Test(t)
				recs := randomRecords(3)

				destA, destB := newTimedDestination(t, "A"), newTimedDestination(t, "B")
				recordErr := cerrors.New("B: simulated write failure")
				for _, i := range tc.nacked {
					destB.setNackErr(recs[i].Position, recordErr)
				}
				dtA := NewBatchingDestinationTask("A", destA, logger, NoOpConnectorMetrics{}, 100, time.Second)
				dtB := NewDestinationTask("B", destB, logger, NoOpConnectorMetrics{})
				roots := sharedRoots(dtA, dtB)
				is.NoErr(dtA.Open(ctx))
				src := newChanSource("src")
				dlqDest := newFakeDestination("dlq")
				w := newTimerTestWorkerNext(t, src, NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, tc.window, tc.threshold), roots...)
				got := make(chan error, 1)
				stop := w.WatchConnectorErrors(ctx, func(err error) { got <- err; cancel() })
				defer stop()
				waitDo := goDo(t, ctx, cancel, w)

				for _, r := range recs {
					src.push(r)
					synctest.Wait()
				}
				is.Equal(len(got), 0) // nothing failed yet: no vote from A

				sleep(time.Second)
				var runErr error
				select {
				case runErr = <-got:
				default:
					t.Fatal("the run was not failed by A's timer flush while the source was idle")
				}
				is.True(cerrors.Is(runErr, recordErr))
				is.Equal(cerrors.IsFatalError(runErr), tc.wantFatal)
				// Poisoned before the shared mutex was released, as a failed pass
				// or drain would: no sibling may write or ack past the failure.
				is.True(roots[0].poisoned.Load())
				is.True(cerrors.Is(dtA.flushError(), recordErr))
				is.Equal(src.ackedPositions(), posOf(recs[:tc.wantAcked]...))
				is.Equal(len(dlqDest.receivedPositions()), tc.wantDLQ)
				_ = waitDo()
				_ = dtA.Close(context.Background())
			})
		})
	}
}

// TestWorker_FanOut_TwoTimers: two batching destinations, B nacks rec0. Whatever
// the order of their deadlines, the timer flush that casts the last vote for
// rec0 is the one that releases it: the nack is routed to the DLQ exactly once
// and the source is acked once, in order, while a timer holds the processing
// lock. With equal delays both timers fire at the same instant and are
// serialised by the processing lock.
func TestWorker_FanOut_TwoTimers(t *testing.T) {
	for _, tc := range []struct {
		name           string
		delayA, delayB time.Duration
	}{
		{name: "A first, B's own flush releases its nack", delayA: time.Second, delayB: 2 * time.Second},
		{name: "B first, A's flush releases B's deferred nack", delayA: 2 * time.Second, delayB: time.Second},
		{name: "same deadline", delayA: time.Second, delayB: time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				is := is.New(t)
				ctx, cancel := context.WithCancel(context.Background())
				logger := log.Test(t)
				recs := randomRecords(3)

				destA, destB := newTimedDestination(t, "A"), newTimedDestination(t, "B")
				destB.setNackErr(recs[0].Position, cerrors.New("B: simulated write failure"))
				dtA := NewBatchingDestinationTask("A", destA, logger, NoOpConnectorMetrics{}, 100, tc.delayA)
				dtB := NewBatchingDestinationTask("B", destB, logger, NoOpConnectorMetrics{}, 100, tc.delayB)
				roots := sharedRoots(dtA, dtB)
				is.NoErr(dtA.Open(ctx))
				is.NoErr(dtB.Open(ctx))
				src := newChanSource("src")
				dlqDest := &lockCheckingDestination{fakeDestination: newFakeDestination("dlq")}
				w := newTimerTestWorkerNext(t, src, NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, 10, 10), roots...)
				dlqDest.w, src.lockOf = w, w
				waitDo := goDo(t, ctx, cancel, w)

				src.push(recs[0])
				synctest.Wait()
				src.push(recs[1], recs[2])
				synctest.Wait()

				first := min(tc.delayA, tc.delayB)
				sleep(first)
				if tc.delayA != tc.delayB {
					is.Equal(len(dlqDest.receivedPositions()), 0) // one vote still missing
					is.Equal(len(src.ackedPositions()), 0)
					sleep(max(tc.delayA, tc.delayB) - first)
				}
				is.Equal(destA.writeSizes(), []int{3})
				is.Equal(destB.writeSizes(), []int{3})
				is.Equal(dlqDest.receivedPositions(), posOf(recs[0])) // exactly once
				is.Equal(src.ackedPositions(), posOf(recs...))
				assertMonotonicAcks(t, src.ackedPositions())
				for _, held := range append(dlqDest.writesUnderLock(), src.acksUnderLock()...) {
					is.True(held)
				}

				cancel()
				_ = waitDo()
				is.NoErr(dtA.Close(context.Background()))
				is.NoErr(dtB.Close(context.Background()))
			})
		})
	}
}

// TestWorker_NSourceFanOut_TimersDoNotDeadlock: two source workers share a
// fan-out to two batching destinations, so each destination's timer takes both
// workers' processing locks. Timers that hold several locks must take them in
// the same order, or two of them firing together deadlock (synctest reports a
// deadlock as a test failure). Every record is written once per destination
// and acked once to its own source.
func TestWorker_NSourceFanOut_TimersDoNotDeadlock(t *testing.T) {
	for _, shape := range []string{"shared processor root", "one root per destination"} {
		t.Run(shape, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				is := is.New(t)
				ctx, cancel := context.WithCancel(context.Background())
				logger := log.Test(t)

				destA, destB := newTimedDestination(t, "A"), newTimedDestination(t, "B")
				dtA := NewBatchingDestinationTask("A", destA, logger, NoOpConnectorMetrics{}, 100, time.Second)
				dtB := NewBatchingDestinationTask("B", destB, logger, NoOpConnectorMetrics{}, 100, time.Second)
				roots := sharedRoots(dtA, dtB)
				if shape == "shared processor root" {
					fanNode := &TaskNode{Task: passthroughTask{id: "shared"}, Next: []*TaskNode{{Task: dtA}, {Task: dtB}}}
					fanNode.MarkSharedBoundary()
					roots = []*TaskNode{fanNode}
				}
				is.NoErr(dtA.Open(ctx))
				is.NoErr(dtB.Open(ctx))
				src1, src2 := newChanSource("src1"), newChanSource("src2")
				w1 := newTimerTestWorkerNext(t, src1, nil, roots...)
				w2 := newTimerTestWorkerNext(t, src2, nil, roots...)
				for _, dt := range []*DestinationTask{dtA, dtB} {
					dt.mu.Lock()
					is.Equal(dt.feeders, []*Worker{w1, w2}) // build order, the same on every task
					dt.mu.Unlock()
				}

				wait1, wait2 := goDo(t, ctx, cancel, w1), goDo(t, ctx, cancel, w2)
				recs := randomRecords(4)
				for round := 0; round < 3; round++ {
					src1.push(recs[0], recs[1])
					src2.push(recs[2], recs[3])
					synctest.Wait()
					sleep(time.Second)
				}
				is.Equal(len(destA.receivedPositions()), 12)
				is.Equal(len(destB.receivedPositions()), 12)
				is.Equal(len(src1.ackedPositions()), 6)
				is.Equal(len(src2.ackedPositions()), 6)

				cancel()
				_, _ = wait1(), wait2()
				is.NoErr(dtA.Close(context.Background()))
				is.NoErr(dtB.Close(context.Background()))
			})
		})
	}
}

// TestWorker_NSourceFanOut_TimersTakeLocksInOneOrder: A's timer holds the
// first worker's processing lock and is held there while B's timer (1 ms
// later) fires and tries to take the same locks. B must wait for the first lock rather than
// take the second one first; if the two timers took the locks in different
// orders they would deadlock here (synctest fails the test on a deadlock).
func TestWorker_NSourceFanOut_TimersTakeLocksInOneOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		is := is.New(t)
		ctx, cancel := context.WithCancel(context.Background())
		logger := log.Test(t)

		destA, destB := newTimedDestination(t, "A"), newTimedDestination(t, "B")
		dtA := NewBatchingDestinationTask("A", destA, logger, NoOpConnectorMetrics{}, 100, time.Second)
		dtB := NewBatchingDestinationTask("B", destB, logger, NoOpConnectorMetrics{}, 100, time.Second+time.Millisecond)
		roots := sharedRoots(dtA, dtB)
		aHolds, goA := make(chan struct{}), make(chan struct{})
		var once sync.Once
		dtA.testAfterFeederLock = func(i int) {
			if i == 0 {
				once.Do(func() { close(aHolds); <-goA })
			}
		}
		is.NoErr(dtA.Open(ctx))
		is.NoErr(dtB.Open(ctx))
		src1, src2 := newChanSource("src1"), newChanSource("src2")
		w1 := newTimerTestWorkerNext(t, src1, nil, roots...)
		w2 := newTimerTestWorkerNext(t, src2, nil, roots...)
		wait1, wait2 := goDo(t, ctx, cancel, w1), goDo(t, ctx, cancel, w2)

		recs := randomRecords(2)
		src1.push(recs[0])
		src2.push(recs[1])
		synctest.Wait()
		time.Sleep(time.Second) // A's timer fires and holds the first lock
		<-aHolds
		sleep(time.Millisecond) // B's timer fires and waits on a processing lock
		close(goA)
		synctest.Wait()

		is.Equal(len(destA.receivedPositions()), 2)
		is.Equal(len(destB.receivedPositions()), 2)
		is.Equal(src1.ackedPositions(), posOf(recs[0]))
		is.Equal(src2.ackedPositions(), posOf(recs[1]))
		cancel()
		_, _ = wait1(), wait2()
		is.NoErr(dtA.Close(context.Background()))
		is.NoErr(dtB.Close(context.Background()))
	})
}

// TestDestinationTask_TimerRacesTake: the timer fires and its callback is
// running when an arrival takes the buffer by size; the arrival is then held
// right after its take (holding enqueueMu or syncMu) while the callback goes on
// and waits on that lock. The callback finds a new generation and does
// nothing: one Write, no empty flush, acks in order.
func TestDestinationTask_TimerRacesTake(t *testing.T) {
	for name, pipelined := range map[string]bool{"pipelined": true, "synchronous": false} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				is := is.New(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				dest := newTimedDestination(t, "dest")
				dt := NewBatchingDestinationTask("dest", dest, log.Test(t), NoOpConnectorMetrics{}, 3, time.Second)
				if pipelined {
					dt.SetPipelined()
				}
				is.NoErr(dt.Open(ctx))
				src := newFakeSource("src", nil)
				w := newTimerTestWorker(t, src, &TaskNode{Task: dt}, nil)
				recs := randomRecords(3)

				// Hooks are set before anything runs; each holds only once.
				fired, holdTimer := make(chan struct{}), make(chan struct{})
				var timerOnce, takeOnce sync.Once
				dt.testBeforeTimerFlush = func() { timerOnce.Do(func() { close(fired); <-holdTimer }) }
				taken, holdTake := make(chan struct{}), make(chan struct{})
				dt.testAfterTake = func() { takeOnce.Do(func() { close(taken); <-holdTake }) }

				is.True(isBuffered(dt.doBuffered(ctx, NewBatch(recs[:1]), newRunAckNacker(w)))) // arms the timer
				time.Sleep(time.Second)
				<-fired // the callback is running: Stop can no longer cancel it

				arrival := make(chan error, 1)
				go func() { arrival <- dt.doBuffered(ctx, NewBatch(recs[1:]), newRunAckNacker(w)) }()
				<-taken // the arrival took the buffer by size and holds its lock
				close(holdTimer)
				// No synctest.Wait here: the callback may be parked on a sync.Mutex,
				// which synctest does not count as durably blocked.
				close(holdTake)
				err := <-arrival
				is.True(err == nil || isBuffered(err))
				synctest.Wait()

				is.Equal(dest.writeSizes(), []int{3}) // the arrival's flush only
				is.Equal(src.ackedPositions(), posOf(recs...))
				is.NoErr(dt.Close(ctx))
				is.Equal(dest.writeSizes(), []int{3}) // no empty flush, before or at Close
			})
		})
	}
}

// TestDestinationTask_StaleTimerDoesNotTakeNextBuffer: a timer callback that
// is already running when its buffer is taken by size must not take the next
// buffer, which has its own, later deadline. Only the generation tells the two
// buffers apart.
func TestDestinationTask_StaleTimerDoesNotTakeNextBuffer(t *testing.T) {
	for name, pipelined := range map[string]bool{"pipelined": true, "synchronous": false} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				is := is.New(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				dest := newTimedDestination(t, "dest")
				dt := NewBatchingDestinationTask("dest", dest, log.Test(t), NoOpConnectorMetrics{}, 3, time.Second)
				if pipelined {
					dt.SetPipelined()
				}
				is.NoErr(dt.Open(ctx))
				src := newFakeSource("src", nil)
				w := newTimerTestWorker(t, src, &TaskNode{Task: dt}, nil)
				fired, hold := make(chan struct{}), make(chan struct{})
				var once sync.Once
				dt.testBeforeTimerFlush = func() { once.Do(func() { close(fired); <-hold }) }
				recs := randomRecords(4)

				is.True(isBuffered(dt.doBuffered(ctx, NewBatch(recs[:1]), newRunAckNacker(w))))
				time.Sleep(time.Second)
				<-fired                                                            // the first buffer's callback is running
				err := dt.doBuffered(ctx, NewBatch(recs[1:3]), newRunAckNacker(w)) // size: takes buffer 1
				is.True(err == nil || isBuffered(err))
				is.True(isBuffered(dt.doBuffered(ctx, NewBatch(recs[3:]), newRunAckNacker(w)))) // buffer 2, due at 2 s
				close(hold)
				synctest.Wait()
				is.Equal(dest.writeSizes(), []int{3}) // the stale callback left buffer 2 alone

				sleep(999 * time.Millisecond)
				is.Equal(dest.writeSizes(), []int{3})
				sleep(time.Millisecond)
				is.Equal(dest.writeSizes(), []int{3, 1}) // buffer 2 at its own deadline
				is.Equal(dest.timedWrites()[1].at, 2*time.Second)
				is.Equal(src.ackedPositions(), posOf(recs...))
				is.NoErr(dt.Close(ctx))
			})
		})
	}
}

// TestDestinationTask_TimerBehindQueuedAck: a filtered batch's source ack is
// queued by deferOrdered behind buffered records. The timer flush writes and
// acks the records, then runs the queued ack, in that order.
func TestDestinationTask_TimerBehindQueuedAck(t *testing.T) {
	for name, pipelined := range map[string]bool{"pipelined": true, "synchronous": false} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				is := is.New(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				events := &eventLog{}
				dest := newTimedDestination(t, "dest")
				dt := NewBatchingDestinationTask("dest", dest, log.Test(t), NoOpConnectorMetrics{}, 10, time.Second)
				if pipelined {
					dt.SetPipelined()
				}
				is.NoErr(dt.Open(ctx))
				newTimerTestWorker(t, newFakeSource("src", nil), &TaskNode{Task: dt}, nil)

				is.True(isBuffered(dt.doBuffered(ctx, NewBatch(randomRecords(2)), newRecordingAckNacker(events))))
				is.NoErr(dt.deferOrdered(ctx, func(context.Context) error {
					events.append("queued.ack")
					return nil
				}))
				synctest.Wait()
				is.Equal(len(events.snapshot()), 0) // held behind the buffered records

				sleep(time.Second)
				is.Equal(dest.writeSizes(), []int{2})
				is.Equal(events.snapshot(), []string{"acker.ack", "queued.ack"})
				is.NoErr(dt.Close(ctx))
			})
		})
	}
}

// TestDestinationTask_TimerAfterDrainAndClose: a timer that would fire after
// Drain took its buffer, and one whose callback is already running when Close
// tears the destination down, make no destination call. timedDestination fails
// the test on any Write or Ack after Teardown.
func TestDestinationTask_TimerAfterDrainAndClose(t *testing.T) {
	for name, pipelined := range map[string]bool{"pipelined": true, "synchronous": false} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				is := is.New(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				dest := newTimedDestination(t, "dest")
				dt := NewBatchingDestinationTask("dest", dest, log.Test(t), NoOpConnectorMetrics{}, 10, time.Second)
				if pipelined {
					dt.SetPipelined()
				}
				is.NoErr(dt.Open(ctx))
				src := newFakeSource("src", nil)
				w := newTimerTestWorker(t, src, &TaskNode{Task: dt}, nil)
				recs := randomRecords(3)
				// The first callback that runs is held before its first lock. The
				// Drain below stops the first timer before it fires, so this is the
				// second one.
				running, hold := make(chan struct{}), make(chan struct{})
				var once sync.Once
				dt.testBeforeTimerFlush = func() { once.Do(func() { close(running); <-hold }) }

				// Drain takes the buffer before the deadline; the timer stays quiet.
				is.True(isBuffered(dt.doBuffered(ctx, NewBatch(recs[:1]), newRunAckNacker(w))))
				sleep(500 * time.Millisecond)
				is.NoErr(dt.Drain(ctx))
				sleep(time.Second)
				is.Equal(dest.writeSizes(), []int{1})

				// A timer callback is already running (held before its first lock)
				// when Close takes the buffer and tears the destination down.
				is.True(isBuffered(dt.doBuffered(ctx, NewBatch(recs[1:]), newRunAckNacker(w))))
				time.Sleep(time.Second)
				<-running
				is.NoErr(dt.Close(ctx)) // writes the tail itself, then Teardown
				close(hold)
				synctest.Wait()

				is.Equal(dest.writeSizes(), []int{1, 2})
				is.Equal(src.ackedPositions(), posOf(recs...))
			})
		})
	}
}

// TestDestinationTask_SyncDrainLeavesTaskOpen: on the synchronous path Drain is
// flush, and so is the timer's fire. Neither may close the task: a shared
// fan-out destination drained by one worker's Stop keeps serving its siblings.
// Only Close makes doBuffered and deferOrdered return "destination is closed".
func TestDestinationTask_SyncDrainLeavesTaskOpen(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	dest := newFakeDestination("dest")
	dt := NewBatchingDestinationTask("dest", dest, log.Test(t), NoOpConnectorMetrics{}, 10, time.Hour)
	is.NoErr(dt.Open(ctx))
	acker := newRecordingAckNacker(&eventLog{})

	is.True(isBuffered(dt.doBuffered(ctx, randomBatch(1), acker)))
	is.NoErr(dt.Drain(ctx))
	is.True(isBuffered(dt.doBuffered(ctx, randomBatch(1), acker))) // a sibling still buffers
	is.NoErr(dt.deferOrdered(ctx, func(context.Context) error { return nil }))

	is.NoErr(dt.Close(ctx))
	is.True(dt.doBuffered(ctx, randomBatch(1), acker) != nil)
	is.True(dt.deferOrdered(ctx, func(context.Context) error { return nil }) != nil)
}

// TestWorker_TimerDuringStopDrain: the timer fires while Worker.Stop is
// draining (the drain's write is held open). On the synchronous path the timer
// waits for the processing lock Stop holds and then finds a new generation;
// on the pipelined path the drain took the buffer first. Either way the tail is
// written once and acked once, before the source is torn down (invariant 7).
func TestWorker_TimerDuringStopDrain(t *testing.T) {
	for name, pipelined := range map[string]bool{"pipelined": true, "synchronous": false} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				is := is.New(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				dest := newTimedDestination(t, "dest")
				dt := NewBatchingDestinationTask("dest", dest, log.Test(t), NoOpConnectorMetrics{}, 10, time.Second)
				if pipelined {
					dt.SetPipelined()
				}
				is.NoErr(dt.Open(ctx))
				src := &teardownSource{fakeSource: newFakeSource("src", nil)}
				w := newTimerTestWorker(t, src, &TaskNode{Task: dt}, nil)
				recs := randomRecords(4)

				is.True(isBuffered(dt.doBuffered(ctx, NewBatch(recs), newRunAckNacker(w))))
				sleep(900 * time.Millisecond)
				entered, release := dest.block()
				stopDone := make(chan error, 1)
				go func() { stopDone <- w.Stop(ctx) }()
				<-entered
				sleep(time.Second) // the deadline passes during the drain
				release()
				is.NoErr(<-stopDone)
				synctest.Wait()

				is.Equal(dest.writeSizes(), []int{4})
				accepted, refused := src.snapshot()
				is.Equal(accepted, posOf(recs...))
				is.Equal(len(refused), 0)
				is.NoErr(dt.Close(ctx))
				is.Equal(dest.writeSizes(), []int{4})
			})
		})
	}
}

// TestDestinationTask_TimerAfterRunCancelled: the run is cancelled (fatal
// error or force-stop) before the deadline. The timer neither writes nor acks,
// and reports nothing: the records replay.
func TestDestinationTask_TimerAfterRunCancelled(t *testing.T) {
	for name, pipelined := range map[string]bool{"pipelined": true, "synchronous": false} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				is := is.New(t)
				runCtx, cancel := context.WithCancel(context.Background())
				dest := newTimedDestination(t, "dest")
				dt := NewBatchingDestinationTask("dest", dest, log.Test(t), NoOpConnectorMetrics{}, 10, time.Second)
				if pipelined {
					dt.SetPipelined()
				}
				is.NoErr(dt.Open(runCtx))
				src := newFakeSource("src", nil)
				w := newTimerTestWorker(t, src, &TaskNode{Task: dt}, nil)

				is.True(isBuffered(dt.doBuffered(runCtx, NewBatch(randomRecords(2)), newRunAckNacker(w))))
				cancel()
				sleep(2 * time.Second)

				is.Equal(len(dest.timedWrites()), 0)
				is.Equal(len(src.ackedPositions()), 0)
				is.Equal(len(dt.flushFailures), 0)
				_ = dt.Close(context.Background())
			})
		})
	}
}

// TestDestinationTask_TimerFlushFails: the timer's flush fails. Nothing is
// acked, nothing queued behind it runs, the next arrival fails closed, and the
// failure reaches the run through the idle error channel (invariant 3).
func TestDestinationTask_TimerFlushFails(t *testing.T) {
	for name, pipelined := range map[string]bool{"pipelined": true, "synchronous": false} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				is := is.New(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				dest := newTimedDestination(t, "dest")
				writeErr := cerrors.New("destination write failed")
				dest.failWrites(writeErr)
				dt := NewBatchingDestinationTask("dest", dest, log.Test(t), NoOpConnectorMetrics{}, 10, time.Second)
				if pipelined {
					dt.SetPipelined()
				}
				is.NoErr(dt.Open(ctx))
				src := newFakeSource("src", nil)
				w := newTimerTestWorker(t, src, &TaskNode{Task: dt}, nil)
				fail, calls, got := failRecorder()
				stop := w.WatchConnectorErrors(ctx, fail)
				defer stop()

				var queued bool
				is.True(isBuffered(dt.doBuffered(ctx, NewBatch(randomRecords(2)), newRunAckNacker(w))))
				is.NoErr(dt.deferOrdered(ctx, func(context.Context) error { queued = true; return nil }))
				sleep(time.Second)

				is.Equal(calls.Load(), int64(1))
				is.True(cerrors.Is(<-got, writeErr))
				is.Equal(len(src.ackedPositions()), 0) // invariant 1
				is.True(!queued)                       // invariant 3: nothing behind the failure ran
				is.True(cerrors.Is(dt.doBuffered(ctx, randomBatch(1), newRunAckNacker(w)), writeErr))
				_ = dt.Close(ctx)
			})
		})
	}
}

// TestDestinationTask_RunCancelledMidFlush_NotReported (#3331 review, L1): a
// flush that fails because the run was cancelled is not a flush failure to
// report. "pipelined" cancels the run while the flusher's Write is in flight;
// there the flush context is the run context, so the context-end filter in
// reportFlushFailure covers it as well. "live flush context" pins the
// run-cancellation exclusion in reportsFlushFailure on its own: the flush
// (a synchronous Drain with a live stop context) fails with context.Canceled
// because the run ended, and only runCancelled tells it apart from a plugin's
// transport failing while the run is alive.
func TestDestinationTask_RunCancelledMidFlush_NotReported(t *testing.T) {
	t.Run("pipelined", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			is := is.New(t)
			runCtx, cancel := context.WithCancel(context.Background())
			dest := newTimedDestination(t, "dest")
			entered, release := dest.block()
			defer release()
			dt := NewBatchingDestinationTask("dest", dest, log.Test(t), NoOpConnectorMetrics{}, 2, 0)
			dt.SetPipelined()
			is.NoErr(dt.Open(runCtx))
			w := newTimerTestWorker(t, newFakeSource("src", nil), &TaskNode{Task: dt}, nil)

			is.True(isBuffered(dt.doBuffered(runCtx, NewBatch(randomRecords(2)), newRunAckNacker(w))))
			<-entered
			cancel() // the Write returns the run's cancellation
			<-dt.flusherExited
			is.True(dt.flushError() != nil)
			is.Equal(len(dt.flushFailures), 0)
			_ = dt.Close(context.Background())
		})
	})
	t.Run("live flush context", func(t *testing.T) {
		is := is.New(t)
		runCtx, cancel := context.WithCancel(context.Background())
		dest := &canceledAfterRunEnds{fakeDestination: newFakeDestination("dest"), run: runCtx}
		dt := NewBatchingDestinationTask("dest", dest, log.Test(t), NoOpConnectorMetrics{}, 10, 0)
		is.NoErr(dt.Open(runCtx))
		acker := newRecordingAckNacker(&eventLog{})

		is.True(isBuffered(dt.doBuffered(runCtx, randomBatch(2), acker)))
		cancel()
		err := dt.Drain(context.Background()) // a stop context that is still alive
		is.True(cerrors.Is(err, context.Canceled))
		is.Equal(len(dt.flushFailures), 0)
		_ = dt.Close(context.Background())
	})
}

// canceledAfterRunEnds is a destination whose Write fails with
// context.Canceled once run has ended, the way a plugin's stream fails when
// the context it was opened with is cancelled.
type canceledAfterRunEnds struct {
	*fakeDestination
	run context.Context
}

func (d *canceledAfterRunEnds) Write(ctx context.Context, recs []opencdc.Record) error {
	if d.run.Err() != nil {
		return cerrors.Errorf("stream closed: %w", context.Canceled)
	}
	return d.fakeDestination.Write(ctx, recs)
}

// recordingFlushMetrics records every FlushMetrics observation.
type recordingFlushMetrics struct {
	mu       sync.Mutex
	triggers []FlushTrigger
	lingers  []time.Duration
}

func (m *recordingFlushMetrics) ObserveFlush(tr FlushTrigger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.triggers = append(m.triggers, tr)
}

func (m *recordingFlushMetrics) ObserveLinger(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lingers = append(m.lingers, d)
}

// TestDestinationTask_FlushMetrics: every take is counted by its trigger, and
// the linger is the age of the buffer's first record.
func TestDestinationTask_FlushMetrics(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		is := is.New(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		m := &recordingFlushMetrics{}
		dt := NewBatchingDestinationTask("dest", newTimedDestination(t, "dest"), log.Test(t), NoOpConnectorMetrics{}, 3, time.Second)
		dt.SetFlushMetrics(m)
		dt.SetPipelined()
		is.NoErr(dt.Open(ctx))
		w := newTimerTestWorker(t, newFakeSource("src", nil), &TaskNode{Task: dt}, nil)
		do := func(n int) {
			err := dt.doBuffered(ctx, randomBatch(n), newRunAckNacker(w))
			is.True(err == nil || isBuffered(err))
			synctest.Wait()
		}

		do(3) // size
		do(1)
		sleep(time.Second) // deadline_timer, linger 1 s
		do(1)
		sleep(2 * time.Second) // the timer fires at 1 s
		do(1)
		sleep(100 * time.Millisecond)
		is.NoErr(dt.Drain(ctx)) // drain, linger 100 ms
		do(1)
		is.NoErr(dt.Close(ctx)) // close, linger 0

		is.Equal(m.triggers, []FlushTrigger{
			FlushTriggerSize, FlushTriggerDeadlineTimer, FlushTriggerDeadlineTimer, FlushTriggerDrain, FlushTriggerClose,
		})
		is.Equal(m.lingers, []time.Duration{0, time.Second, time.Second, 100 * time.Millisecond, 0})
	})
}

// TestDestinationTask_TimerHandOffAtomicWithArrival pins enqueueMu in
// flushDuePipelined (review of #3335, M1): the timer has taken buffer 1 and is
// about to hand it to the flusher when an arrival fills buffer 2 by size.
// Buffer 1 must reach the flusher first; otherwise the flusher writes buffer 2
// and acks the source past buffer 1 (invariants 1 and 2), and a Close in the
// same gap closes flushCh under the timer's send.
func TestDestinationTask_TimerHandOffAtomicWithArrival(t *testing.T) {
	// Real time, not a synctest bubble: with the fix the arrival waits on
	// enqueueMu, a sync.Mutex, which synctest does not count as durably
	// blocked, so fake time would never advance past the wait below.
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dest := newTimedDestination(t, "dest")
	dt := NewBatchingDestinationTask("dest", dest, log.Test(t), NoOpConnectorMetrics{}, 2, 50*time.Millisecond)
	dt.SetPipelined()
	is.NoErr(dt.Open(ctx))
	src := newFakeSource("src", nil)
	w := newTimerTestWorker(t, src, &TaskNode{Task: dt}, nil)
	recs := randomRecords(3)

	inHook, proceed := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	// Not sync.Once: a concurrent Do would block the arrival in the hook and
	// hide the race this test is for. Only the first send (the timer's) is
	// held; the arrival's goes straight through.
	dt.testBeforeEnqueueSend = func() {
		if first.CompareAndSwap(false, true) {
			close(inHook)
			<-proceed
		}
	}

	is.True(isBuffered(dt.doBuffered(ctx, NewBatch(recs[:1]), newRunAckNacker(w)))) // arms the timer
	<-inHook                                                                        // the timer took [rec0] and is about to send it

	arrival := make(chan error, 1)
	go func() { arrival <- dt.doBuffered(ctx, NewBatch(recs[1:]), newRunAckNacker(w)) }()
	time.Sleep(100 * time.Millisecond) // the arrival waits on enqueueMu (without it, overtakes)
	close(proceed)
	err := <-arrival
	is.True(err == nil || isBuffered(err))
	is.NoErr(dt.Close(ctx))

	is.Equal(dest.writeSizes(), []int{1, 2}) // the timer's buffer first
	assertMonotonicAcks(t, src.ackedPositions())
	is.Equal(src.ackedPositions(), posOf(recs...))
}

// TestDestinationTask_TimerOnPoisonedTask pins the flushErr check in
// timerDueLocked (review of #3335, M5). A sibling poisons the task through
// setFlushErr while a partial batch is buffered; that takes nothing, so the
// generation is unchanged and the timer stays armed. At the deadline the timer
// must not write or ack the batch (invariant 3). On the synchronous path this
// check is the only one that stops it: flushPending does not consult the flush
// error.
func TestDestinationTask_TimerOnPoisonedTask(t *testing.T) {
	for name, pipelined := range map[string]bool{"pipelined": true, "synchronous": false} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				is := is.New(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				dest := newTimedDestination(t, "dest")
				dt := NewBatchingDestinationTask("dest", dest, log.Test(t), NoOpConnectorMetrics{}, 10, time.Second)
				if pipelined {
					dt.SetPipelined()
				}
				is.NoErr(dt.Open(ctx))
				src := newFakeSource("src", nil)
				w := newTimerTestWorker(t, src, &TaskNode{Task: dt}, nil)
				is.True(isBuffered(dt.doBuffered(ctx, NewBatch(randomRecords(2)), newRunAckNacker(w))))
				dt.setFlushErr(cerrors.New("sibling failed"))

				sleep(2 * time.Second) // past the deadline
				is.Equal(len(dest.timedWrites()), 0)
				is.Equal(len(src.ackedPositions()), 0)
				_ = dt.Close(ctx) // fails with the flush error; nothing written
				is.Equal(len(dest.timedWrites()), 0)
				is.Equal(len(src.ackedPositions()), 0)
			})
		})
	}
}

// TestWorker_NSourceFanOut_TimerWaitsForSecondFeederPass pins that the
// synchronous timer holds EVERY feeder's processing lock, not only the first
// (review of #3335, M2). Two workers, one shared root per destination (the
// production shape without pipeline processors). A pass of the SECOND worker
// is in flight, its write to B held open, when A's deadline passes. A's timer
// must wait for that pass: holding only worker 1's lock it would flush during
// the pass and cast the last vote for worker 2's records, so worker 2's source
// acks would run inside its own pass.
func TestWorker_NSourceFanOut_TimerWaitsForSecondFeederPass(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		is := is.New(t)
		ctx, cancel := context.WithCancel(context.Background())
		logger := log.Test(t)

		destA, destB := newTimedDestination(t, "A"), newTimedDestination(t, "B")
		dtA := NewBatchingDestinationTask("A", destA, logger, NoOpConnectorMetrics{}, 100, time.Second)
		dtB := NewDestinationTask("B", destB, logger, NoOpConnectorMetrics{})
		roots := sharedRoots(dtA, dtB)
		is.NoErr(dtA.Open(ctx))
		src1, src2 := newChanSource("src1"), newChanSource("src2")
		w1 := newTimerTestWorkerNext(t, src1, nil, roots...)
		w2 := newTimerTestWorkerNext(t, src2, nil, roots...)
		var passDone, flushedDuringPass atomic.Bool
		dtA.testTimerHoldsLocks = func() {
			if !passDone.Load() {
				flushedDuringPass.Store(true)
			}
		}
		wait1, wait2 := goDo(t, ctx, cancel, w1), goDo(t, ctx, cancel, w2)

		recs := randomRecords(2)
		src2.push(recs[0]) // worker 2, pass 1: buffered in A, written by B
		synctest.Wait()
		sleep(500 * time.Millisecond)
		entered, release := destB.block()
		src2.push(recs[1]) // worker 2, pass 2: B's write is held open
		<-entered
		synctest.Wait()

		sleep(time.Second) // A's deadline passes while worker 2's pass holds its lock
		is.Equal(len(destA.timedWrites()), 0)
		is.Equal(len(src2.ackedPositions()), 0)

		passDone.Store(true)
		release()
		synctest.Wait()
		is.True(!flushedDuringPass.Load())
		is.Equal(destA.writeSizes(), []int{2}) // both records, one Write, after the pass
		assertMonotonicAcks(t, src2.ackedPositions())
		is.Equal(src2.ackedPositions(), posOf(recs...))

		cancel()
		_, _ = wait1(), wait2()
		is.NoErr(dtA.Close(context.Background()))
	})
}

// TestDestinationTask_StaleSyncTimerParkedOnFeederLock pins the second
// generation check, in flushTimerSync (review of #3335, L1, M6). The callback
// passes the early check in flushDueSync and is then held right after taking
// its feeder's processing lock. Meanwhile buffer 1 is taken by size and buffer
// 2 starts. When the callback goes on it must find the new generation and
// leave buffer 2 to its own timer: flushing it would write a smaller batch
// before its deadline and report it as deadline_timer.
func TestDestinationTask_StaleSyncTimerParkedOnFeederLock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		is := is.New(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		dest := newTimedDestination(t, "dest")
		dt := NewBatchingDestinationTask("dest", dest, log.Test(t), NoOpConnectorMetrics{}, 3, time.Second)
		is.NoErr(dt.Open(ctx))
		src := newFakeSource("src", nil)
		w := newTimerTestWorker(t, src, &TaskNode{Task: dt}, nil)
		parked, hold := make(chan struct{}), make(chan struct{})
		var first atomic.Bool
		dt.testAfterFeederLock = func(int) {
			if first.CompareAndSwap(false, true) {
				close(parked)
				<-hold
			}
		}
		recs := randomRecords(4)

		is.True(isBuffered(dt.doBuffered(ctx, NewBatch(recs[:1]), newRunAckNacker(w))))
		time.Sleep(time.Second)
		<-parked                                                           // buffer 1's callback passed the early check and holds the feeder lock
		err := dt.doBuffered(ctx, NewBatch(recs[1:3]), newRunAckNacker(w)) // size: takes buffer 1
		is.True(err == nil || isBuffered(err))
		is.True(isBuffered(dt.doBuffered(ctx, NewBatch(recs[3:]), newRunAckNacker(w)))) // buffer 2, due at 2 s
		close(hold)
		synctest.Wait()
		is.Equal(dest.writeSizes(), []int{3}) // the stale callback left buffer 2 alone

		sleep(999 * time.Millisecond)
		is.Equal(dest.writeSizes(), []int{3})
		sleep(time.Millisecond)
		is.Equal(dest.writeSizes(), []int{3, 1}) // buffer 2 at its own deadline
		is.Equal(dest.timedWrites()[1].at, 2*time.Second)
		is.Equal(src.ackedPositions(), posOf(recs...))
		is.NoErr(dt.Close(ctx))
	})
}

// TestNewWorker_FeedersMustShareBoundary (review of #3335, L4): the
// synchronous timer flushes under one shared boundary node, the one its
// feeders reach it through. A second worker that reaches the same batching
// destination through a different node, or through none, is a build bug, and
// NewWorker refuses it instead of keeping the first node without a sign.
func TestNewWorker_FeedersMustShareBoundary(t *testing.T) {
	for name, tc := range map[string]struct {
		second  func(dt *DestinationTask) []*TaskNode
		wantMsg string
	}{
		"different shared node": {
			second:  func(dt *DestinationTask) []*TaskNode { return sharedRoots(dt) },
			wantMsg: "different shared boundary nodes",
		},
		"no shared node": {
			second:  func(dt *DestinationTask) []*TaskNode { return []*TaskNode{{Task: dt}} },
			wantMsg: "mark the shared roots (NewSink) before building workers",
		},
	} {
		second := tc.second
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			logger := log.Test(t)
			dt := NewBatchingDestinationTask("dest", newTimedDestination(t, "dest"), logger, NoOpConnectorMetrics{}, 10, time.Second)
			roots := sharedRoots(dt)
			w1 := newTimerTestWorkerNext(t, newChanSource("src1"), nil, roots...)
			// The same node again is fine.
			w2 := newTimerTestWorkerNext(t, newChanSource("src2"), nil, roots...)

			dlq := NewDLQ("dlq", newFakeDestination("dlq"), logger, NoOpConnectorMetrics{}, 10, 10)
			srcNode := &TaskNode{Task: NewSourceTask("src3", newChanSource("src3"), logger, NoOpConnectorMetrics{}), Next: second(dt)}
			w3, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
			is.True(err != nil)
			is.True(strings.Contains(err.Error(), tc.wantMsg))
			is.True(w3 == nil)

			feeders, shared := dt.FeederBoundary()
			is.Equal(feeders, []*Worker{w1, w2}) // the refused worker was not registered
			is.True(shared == roots[0])
		})
	}
}

// TestNewWorker_FeederWithoutBoundaryFirst (review of #3335, nit 2): when the
// FIRST feeder reached the task with no shared node and a later one with a
// node, the roots were still not marked when a worker was built, and the error
// must say so and name the fix rather than report "different" nodes.
func TestNewWorker_FeederWithoutBoundaryFirst(t *testing.T) {
	is := is.New(t)
	logger := log.Test(t)
	dt := NewBatchingDestinationTask("dest", newTimedDestination(t, "dest"), logger, NoOpConnectorMetrics{}, 10, time.Second)
	w1 := newTimerTestWorkerNext(t, newChanSource("src1"), nil, &TaskNode{Task: dt})

	dlq := NewDLQ("dlq", newFakeDestination("dlq"), logger, NoOpConnectorMetrics{}, 10, 10)
	srcNode := &TaskNode{Task: NewSourceTask("src2", newChanSource("src2"), logger, NoOpConnectorMetrics{}), Next: sharedRoots(dt)}
	w2, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "mark the shared roots (NewSink) before building workers"))
	is.True(w2 == nil)

	feeders, shared := dt.FeederBoundary()
	is.Equal(feeders, []*Worker{w1})
	is.True(shared == nil)
}

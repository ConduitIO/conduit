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
	"io"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/matryer/is"
)

// teardownSource is a fakeSource that, like a real connector.Source, refuses
// acks once torn down (plugin.ErrPluginNotRunning) and remembers every ack that
// was refused.
type teardownSource struct {
	*fakeSource
	mu       sync.Mutex
	down     bool
	refused  []opencdc.Position
	accepted []opencdc.Position
}

func (s *teardownSource) Teardown(ctx context.Context) error {
	s.mu.Lock()
	s.down = true
	s.mu.Unlock()
	return s.fakeSource.Teardown(ctx)
}

func (s *teardownSource) Ack(_ context.Context, p []opencdc.Position) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down {
		s.refused = append(s.refused, p...)
		return plugin.ErrPluginNotRunning
	}
	s.accepted = append(s.accepted, p...)
	return nil
}

func (s *teardownSource) snapshot() (accepted, refused []opencdc.Position) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]opencdc.Position(nil), s.accepted...), append([]opencdc.Position(nil), s.refused...)
}

func stopDrainPositions(recs ...opencdc.Record) []opencdc.Position {
	out := make([]opencdc.Position, len(recs))
	for i, r := range recs {
		out[i] = r.Position
	}
	return out
}

// TestWorker_Stop_DrainsDestinationBeforeSourceTeardown is the regression test
// for #3231. A trailing partial batch is buffered and (pipelined) a full batch
// is still in flight at the destination when Stop arrives. Stop used to tear the
// source down first; the destination was drained later (DestinationTask.Close),
// so every source ack failed with plugin.ErrPluginNotRunning and the written
// records replayed on restart as duplicates. They must be acked while the source
// is still running.
func TestWorker_Stop_DrainsDestinationBeforeSourceTeardown(t *testing.T) {
	for name, pipelined := range map[string]bool{"pipelined": true, "synchronous": false} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			logger := log.Test(t)

			src := &teardownSource{fakeSource: newFakeSource("src", nil)}
			dest := newFakeDestination("dest")
			dt := NewBatchingDestinationTask("dest", dest, logger, NoOpConnectorMetrics{}, 2, 0)
			if pipelined {
				dt.SetPipelined()
			}
			is.NoErr(dt.Open(ctx))
			destNode := &TaskNode{Task: dt}
			srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{destNode}}
			dlq := NewDLQ("dlq", newFakeDestination("dlq"), logger, NoOpConnectorMetrics{}, 10, 10)
			w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
			is.NoErr(err)
			recs := randomRecords(3)

			var once sync.Once
			unblock := func() {}
			if pipelined {
				blocked, release := dest.blockWrites(recs[0].Position)
				unblock = func() { once.Do(release) }
				defer unblock()                                                           // never leave the flusher wedged on failure
				is.NoErr(w.doTask(ctx, destNode, NewBatch(recs[:2]), newRunAckNacker(w))) // full batch, in flight
				<-blocked
			} else {
				is.NoErr(w.doTask(ctx, destNode, NewBatch(recs[:2]), newRunAckNacker(w))) // flushed inline
			}
			is.NoErr(w.doTask(ctx, destNode, NewBatch(recs[2:3]), newRunAckNacker(w))) // trailing partial, buffered

			stopped := make(chan error, 1)
			go func() { stopped <- w.Stop(ctx) }()

			if pipelined {
				// Stop must wait for the in-flight write instead of tearing the
				// source down underneath it.
				select {
				case <-stopped:
					t.Fatal("Stop returned while a flush was still in flight")
				case <-time.After(50 * time.Millisecond):
				}
				unblock()
			}
			is.NoErr(<-stopped)

			accepted, refused := src.snapshot()
			is.Equal(len(refused), 0) // no ack hit a torn-down source
			is.Equal(accepted, stopDrainPositions(recs...))
			is.Equal(len(dest.receivedPositions()), 3)
			is.NoErr(dt.Close(ctx))
		})
	}
}

// eofSource is a teardownSource whose Read serves one batch and then returns
// io.EOF, the way a source that has permanently exhausted its records does.
type eofSource struct {
	*teardownSource
	recs []opencdc.Record
	read bool
}

func (s *eofSource) Read(context.Context) ([]opencdc.Record, error) {
	if s.read {
		return nil, io.EOF
	}
	s.read = true
	return s.recs, nil
}

// TestWorker_SourceEOF_DrainsDestinationBeforeTeardown: a source that ends by
// itself (io.EOF) tears itself down just like a Stop does, so its buffered tail
// must be drained and acked first.
func TestWorker_SourceEOF_DrainsDestinationBeforeTeardown(t *testing.T) {
	for name, pipelined := range map[string]bool{"pipelined": true, "synchronous": false} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			logger := log.Test(t)
			recs := randomRecords(3)

			src := &eofSource{teardownSource: &teardownSource{fakeSource: newFakeSource("src", nil)}, recs: recs}
			dest := newFakeDestination("dest")
			dt := NewBatchingDestinationTask("dest", dest, logger, NoOpConnectorMetrics{}, 10, 0) // never fills
			if pipelined {
				dt.SetPipelined()
			}
			is.NoErr(dt.Open(ctx))
			srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{{Task: dt}}}
			dlq := NewDLQ("dlq", newFakeDestination("dlq"), logger, NoOpConnectorMetrics{}, 10, 10)
			w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
			is.NoErr(err)

			is.NoErr(w.Do(ctx)) // reads the batch (buffered), then io.EOF: drain, teardown, return

			accepted, refused := src.snapshot()
			is.Equal(len(refused), 0)
			is.Equal(accepted, stopDrainPositions(recs...))
			is.NoErr(dt.Close(ctx))
		})
	}
}

func recsWithPrefix(p string, n int) []opencdc.Record {
	r := randomRecords(n)
	for i := range r {
		r[i].Position = opencdc.Position(p + string(r[i].Position))
	}
	return r
}

// TestWorker_Stop_DrainCtxExpiry_DoesNotDropSiblingBatches: two workers share
// one pipelined destination. The flusher is wedged on B's first write and the
// flush queue is full when A is stopped with a short ctx, so A's drain cannot
// hand its take to the flusher. It takes the shared buffer, which holds B's
// partial batch. Dropping that would let B's next batch ack a position past it.
func TestWorker_Stop_DrainCtxExpiry_DoesNotDropSiblingBatches(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	srcA := &teardownSource{fakeSource: newFakeSource("a", nil)}
	srcB := &teardownSource{fakeSource: newFakeSource("b", nil)}
	dest := newFakeDestination("dest")
	dt := NewBatchingDestinationTask("dest", dest, logger, NoOpConnectorMetrics{}, 2, 0)
	dt.SetPipelined()
	is.NoErr(dt.Open(ctx))
	destNode := &TaskNode{Task: dt}
	destNode.MarkSharedBoundary()
	nA := &TaskNode{Task: NewSourceTask("a", srcA, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{destNode}}
	nB := &TaskNode{Task: NewSourceTask("b", srcB, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{destNode}}
	wA, err := NewWorker(nA, NewDLQ("dlqa", newFakeDestination("dlqa"), logger, NoOpConnectorMetrics{}, 10, 10), logger, noop.Timer{})
	is.NoErr(err)
	wB, err := NewWorker(nB, NewDLQ("dlqb", newFakeDestination("dlqb"), logger, NoOpConnectorMetrics{}, 10, 10), logger, noop.Timer{})
	is.NoErr(err)

	b := recsWithPrefix("b", 9)
	blocked, release := dest.blockWrites(b[0].Position)
	var once sync.Once
	unblock := func() { once.Do(release) }
	defer unblock()
	is.NoErr(wB.doTask(ctx, destNode, NewBatch(b[0:2]), newRunAckNacker(wB)))
	<-blocked // flusher wedged in Write
	is.NoErr(wB.doTask(ctx, destNode, NewBatch(b[2:4]), newRunAckNacker(wB)))
	is.NoErr(wB.doTask(ctx, destNode, NewBatch(b[4:6]), newRunAckNacker(wB)))
	is.NoErr(wB.doTask(ctx, destNode, NewBatch(b[6:7]), newRunAckNacker(wB))) // B's partial, buffered

	sctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_ = wA.Stop(sctx) // the drain cannot reach the flusher
	cancel()
	unblock()

	// B carries on (or fails closed); either way it must never ack past b6
	// unless b6 was written.
	_ = wB.doTask(ctx, destNode, NewBatch(b[7:9]), newRunAckNacker(wB))
	_ = dt.Close(ctx)

	written := map[string]bool{}
	for _, p := range dest.receivedPositions() {
		written[string(p)] = true
	}
	acked, _ := srcB.snapshot()
	for _, p := range acked {
		is.True(written[string(p)]) // every acked position was written
	}
	if len(acked) > 0 && string(acked[len(acked)-1]) == "b8" {
		is.True(written["b6"]) // acking through b8 requires b6 delivered
	}
}

// TestDestinationTask_Drain_WaitsForSiblingHandoff: a drain barrier must not
// overtake a batch another worker has taken from the buffer and not yet handed
// to the flusher (enqueueMu).
func TestDestinationTask_Drain_WaitsForSiblingHandoff(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	dest := newFakeDestination("d")
	task := NewBatchingDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{}, 2, 0)
	task.SetPipelined()
	is.NoErr(task.Open(ctx))
	recs := randomRecords(2)
	alog := &eventLog{}

	taken, release := make(chan struct{}), make(chan struct{})
	task.testAfterTake = func() { close(taken); <-release }

	is.True(isBuffered(task.doBuffered(ctx, NewBatch(recs[:1]), newRecordingAckNacker(alog))))
	go func() { _ = task.doBuffered(ctx, NewBatch(recs[1:]), newRecordingAckNacker(alog)) }()
	<-taken

	drained := make(chan error, 1)
	go func() { drained <- task.Drain(ctx) }()
	select {
	case <-drained:
		t.Fatal("Drain returned while a batch was taken but not yet handed to the flusher")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	is.NoErr(<-drained)
	is.Equal(len(alog.snapshot()), 2) // both batches acked before Drain returned
	is.NoErr(task.Close(ctx))
}

// TestWorker_Stop_SyncDrainFailure_PoisonsSharedDestination: on a shared
// synchronous destination a failed drain may have dropped a sibling's batches or
// left the ack stream desynchronised; the sibling must fail closed, not carry on.
func TestWorker_Stop_SyncDrainFailure_PoisonsSharedDestination(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	srcA := &teardownSource{fakeSource: newFakeSource("a", nil)}
	srcB := &teardownSource{fakeSource: newFakeSource("b", nil)}
	dt := NewBatchingDestinationTask("dest", failingWriteDestination{newFakeDestination("dest")}, logger, NoOpConnectorMetrics{}, 10, 0)
	is.NoErr(dt.Open(ctx))
	destNode := &TaskNode{Task: dt}
	destNode.MarkSharedBoundary()
	nA := &TaskNode{Task: NewSourceTask("a", srcA, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{destNode}}
	nB := &TaskNode{Task: NewSourceTask("b", srcB, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{destNode}}
	wA, err := NewWorker(nA, NewDLQ("dlqa", newFakeDestination("dlqa"), logger, NoOpConnectorMetrics{}, 10, 10), logger, noop.Timer{})
	is.NoErr(err)
	wB, err := NewWorker(nB, NewDLQ("dlqb", newFakeDestination("dlqb"), logger, NoOpConnectorMetrics{}, 10, 10), logger, noop.Timer{})
	is.NoErr(err)

	recs := randomRecords(2)
	is.NoErr(wB.doTask(ctx, destNode, NewBatch(recs[:1]), newRunAckNacker(wB))) // B's record, buffered

	_ = wA.Stop(ctx) // drain fails: Write is refused
	is.True(destNode.poisoned.Load())

	err = wB.doTask(ctx, destNode, NewBatch(recs[1:]), newRunAckNacker(wB))
	is.True(err != nil) // refused: the shared destination is poisoned
	acked, _ := srcB.snapshot()
	is.Equal(len(acked), 0)
}

// TestDestinationTask_DeferOrdered_DuringDrain: a source ack queued while a
// drain's flush is in flight must run after it, not inline ahead of it.
func TestDestinationTask_DeferOrdered_DuringDrain(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	dest := newFakeDestination("d")
	task := NewBatchingDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{}, 10, 0)
	task.SetPipelined()
	is.NoErr(task.Open(ctx))
	recs := randomRecords(1)
	alog := &eventLog{}

	blocked, release := dest.blockWrites(recs[0].Position)
	var once sync.Once
	unblock := func() { once.Do(release) }
	defer unblock()
	is.True(isBuffered(task.doBuffered(ctx, NewBatch(recs), newRecordingAckNacker(alog))))

	drained := make(chan error, 1)
	go func() { drained <- task.Drain(ctx) }()
	<-blocked // the drain's flush is in flight

	is.NoErr(task.deferOrdered(ctx, func(context.Context) error {
		alog.append("action")
		return nil
	}))
	is.Equal(len(alog.snapshot()), 0) // not run inline ahead of the write

	unblock()
	is.NoErr(<-drained)
	waitForCondition(t, time.Second, func() bool { return len(alog.snapshot()) == 2 })
	is.Equal(alog.snapshot(), []string{"acker.ack", "action"})
	is.NoErr(task.Close(ctx))
}

// TestWorker_Stop_SyncDrainFailure_SiblingOrderedAckFailsClosed: after a failed
// synchronous drain on a shared destination (B's record dropped, node poisoned) a
// sibling's whole-batch filter must not be acked past the dropped record.
func TestWorker_Stop_SyncDrainFailure_SiblingOrderedAckFailsClosed(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	srcA := &teardownSource{fakeSource: newFakeSource("a", nil)}
	srcB := &teardownSource{fakeSource: newFakeSource("b", nil)}
	dt := NewBatchingDestinationTask("dest", failingWriteDestination{newFakeDestination("dest")}, logger, NoOpConnectorMetrics{}, 10, 0)
	is.NoErr(dt.Open(ctx))
	destNode := &TaskNode{Task: dt}
	destNode.MarkSharedBoundary()
	nA := &TaskNode{Task: NewSourceTask("a", srcA, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{destNode}}
	nB := &TaskNode{Task: NewSourceTask("b", srcB, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{destNode}}
	wA, err := NewWorker(nA, NewDLQ("dlqa", newFakeDestination("dlqa"), logger, NoOpConnectorMetrics{}, 10, 10), logger, noop.Timer{})
	is.NoErr(err)
	wB, err := NewWorker(nB, NewDLQ("dlqb", newFakeDestination("dlqb"), logger, NoOpConnectorMetrics{}, 10, 10), logger, noop.Timer{})
	is.NoErr(err)

	recs := randomRecords(2)
	is.NoErr(wB.doTask(ctx, destNode, NewBatch(recs[:1]), newRunAckNacker(wB))) // B's record 0, buffered

	_ = wA.Stop(ctx) // the sync drain fails; B's record 0 is dropped; the node is poisoned
	is.True(destNode.poisoned.Load())

	// B's next batch is fully filtered by its source-side processor, i.e. above
	// the shared boundary where the poison flag is never consulted.
	err = wB.ackInOrder(ctx, nB, func(ctx context.Context) error {
		return newRunAckNacker(wB).Ack(ctx, NewBatch(recs[1:2]))
	})
	is.True(err != nil)
	acked, _ := srcB.snapshot()
	is.Equal(len(acked), 0) // record 0 was never written: nothing may be acked past it
}

// TestWorker_Stop_PipelinedDrainCtxExpiry_SiblingOrderedAckFailsClosed is the
// ordered-ack variant of the ctx-expiry test: after A's drain could not reach the
// flusher, B's fully filtered next batch must not be acked past B's buffered
// batch. It guards the drain failure handling (restore plus fail-closed flush
// error); the node poison alone does not stop an ordered ack.
func TestWorker_Stop_PipelinedDrainCtxExpiry_SiblingOrderedAckFailsClosed(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	srcA := &teardownSource{fakeSource: newFakeSource("a", nil)}
	srcB := &teardownSource{fakeSource: newFakeSource("b", nil)}
	dest := newFakeDestination("dest")
	dt := NewBatchingDestinationTask("dest", dest, logger, NoOpConnectorMetrics{}, 2, 0)
	dt.SetPipelined()
	is.NoErr(dt.Open(ctx))
	destNode := &TaskNode{Task: dt}
	destNode.MarkSharedBoundary()
	nA := &TaskNode{Task: NewSourceTask("a", srcA, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{destNode}}
	nB := &TaskNode{Task: NewSourceTask("b", srcB, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{destNode}}
	wA, err := NewWorker(nA, NewDLQ("dlqa", newFakeDestination("dlqa"), logger, NoOpConnectorMetrics{}, 10, 10), logger, noop.Timer{})
	is.NoErr(err)
	wB, err := NewWorker(nB, NewDLQ("dlqb", newFakeDestination("dlqb"), logger, NoOpConnectorMetrics{}, 10, 10), logger, noop.Timer{})
	is.NoErr(err)

	b := recsWithPrefix("b", 9)
	blocked, release := dest.blockWrites(b[0].Position)
	var once sync.Once
	unblock := func() { once.Do(release) }
	defer unblock()
	is.NoErr(wB.doTask(ctx, destNode, NewBatch(b[0:2]), newRunAckNacker(wB)))
	<-blocked
	is.NoErr(wB.doTask(ctx, destNode, NewBatch(b[2:4]), newRunAckNacker(wB)))
	is.NoErr(wB.doTask(ctx, destNode, NewBatch(b[4:6]), newRunAckNacker(wB)))
	is.NoErr(wB.doTask(ctx, destNode, NewBatch(b[6:7]), newRunAckNacker(wB))) // B's partial b6

	sctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_ = wA.Stop(sctx) // the drain cannot reach the flusher
	cancel()
	unblock()

	_ = wB.ackInOrder(ctx, nB, func(ctx context.Context) error {
		return newRunAckNacker(wB).Ack(ctx, NewBatch(b[7:8]))
	})
	time.Sleep(200 * time.Millisecond)

	written := map[string]bool{}
	for _, p := range dest.receivedPositions() {
		written[string(p)] = true
	}
	acked, _ := srcB.snapshot()
	for _, p := range acked {
		if string(p) == "b7" {
			is.True(written["b6"]) // acking b7 requires b6 delivered
		}
	}
	_ = dt.Close(ctx)
}

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
	"io"
	"math/rand"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/matryer/is"
)

// This file tests the staged engine (worker_pipeline.go, ledger.go,
// DestinationTask.Submit): read-ahead, asynchronous acks, and in-order prefix
// release. The fakes here are deliberately controllable: a destination whose
// acks the test releases, and a source that serves scripted batches.

// eventLog is a shared, ordered record of what happened, so tests can assert
// "the DLQ write happened before the source ack that covers it".
type eventLog struct {
	mu sync.Mutex
	ev []string
}

func (l *eventLog) addf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ev = append(l.ev, fmt.Sprintf(format, args...))
}

func (l *eventLog) index(ev string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Index(l.ev, ev)
}

// batchSource serves scripted batches, one per Read, then blocks until
// Teardown. It records every Source.Ack call.
type batchSource struct {
	id  string
	log *eventLog

	mu         sync.Mutex
	batches    [][]opencdc.Record
	reads      int
	ackCalls   [][]opencdc.Position
	tornDown   bool
	stopped    chan struct{}
	stoppedOne sync.Once
	// infinite, if set, makes Read generate one-record batches forever.
	infinite bool
}

func newBatchSource(id string, batches ...[]opencdc.Record) *batchSource {
	return &batchSource{id: id, batches: batches, stopped: make(chan struct{})}
}

func (s *batchSource) ID() string                 { return s.id }
func (s *batchSource) Open(context.Context) error { return nil }
func (s *batchSource) Errors() <-chan error       { return make(chan error) }

func (s *batchSource) Teardown(context.Context) error {
	s.mu.Lock()
	s.tornDown = true
	s.mu.Unlock()
	s.stoppedOne.Do(func() { close(s.stopped) })
	return nil
}

func (s *batchSource) Read(ctx context.Context) ([]opencdc.Record, error) {
	s.mu.Lock()
	if s.infinite {
		s.reads++
		n := s.reads
		s.mu.Unlock()
		return []opencdc.Record{{Position: opencdc.Position(fmt.Sprintf("inf-%06d", n)), Payload: opencdc.Change{After: opencdc.RawData("x")}}}, nil
	}
	if s.reads < len(s.batches) {
		b := slices.Clone(s.batches[s.reads])
		s.reads++
		s.mu.Unlock()
		return b, nil
	}
	s.mu.Unlock()
	select {
	case <-s.stopped:
		return nil, context.Canceled
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *batchSource) Ack(_ context.Context, p []opencdc.Position) error {
	s.mu.Lock()
	s.ackCalls = append(s.ackCalls, slices.Clone(p))
	s.mu.Unlock()
	if s.log != nil {
		for _, pos := range p {
			s.log.addf("ack:%s", pos)
		}
	}
	return nil
}

func (s *batchSource) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func (s *batchSource) isTornDown() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tornDown
}

// acked returns every position handed to Source.Ack, flattened, in call order.
func (s *batchSource) acked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.ackCalls {
		for _, p := range c {
			out = append(out, string(p))
		}
	}
	return out
}

// gatedDestination records writes immediately but only acks what the test
// released. Acks are delivered in chunks of at most chunk records (0 = all
// available), so tests can exercise acks that straddle writes.
type gatedDestination struct {
	id  string
	log *eventLog

	mu       sync.Mutex
	written  []opencdc.Record
	unacked  []connector.DestinationAck // written, not released
	released []connector.DestinationAck // released, not yet returned by Ack
	nackErr  map[string]error
	chunk    int
	closed   bool
	ready    chan struct{}
	autoAck  bool
}

func newGatedDestination(id string) *gatedDestination {
	return &gatedDestination{id: id, ready: make(chan struct{}, 1)}
}

func (d *gatedDestination) ID() string                 { return d.id }
func (d *gatedDestination) Open(context.Context) error { return nil }
func (d *gatedDestination) Errors() <-chan error       { return make(chan error) }

func (d *gatedDestination) Teardown(context.Context) error {
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
	d.notify()
	return nil
}

func (d *gatedDestination) notify() {
	select {
	case d.ready <- struct{}{}:
	default:
	}
}

func (d *gatedDestination) Write(_ context.Context, recs []opencdc.Record) error {
	d.mu.Lock()
	for _, r := range recs {
		d.written = append(d.written, r)
		a := connector.DestinationAck{Position: r.Position, Error: d.nackErr[string(r.Position)]}
		if d.autoAck {
			d.released = append(d.released, a)
		} else {
			d.unacked = append(d.unacked, a)
		}
		if d.log != nil {
			d.log.addf("write:%s", r.Position)
		}
	}
	d.mu.Unlock()
	d.notify()
	return nil
}

// release lets the next n written records be acked.
func (d *gatedDestination) release(n int) {
	d.mu.Lock()
	n = min(n, len(d.unacked))
	d.released = append(d.released, d.unacked[:n]...)
	d.unacked = d.unacked[n:]
	d.mu.Unlock()
	d.notify()
}

func (d *gatedDestination) releaseAll() {
	d.mu.Lock()
	n := len(d.unacked)
	d.mu.Unlock()
	d.release(n)
}

func (d *gatedDestination) Ack(ctx context.Context) ([]connector.DestinationAck, error) {
	for {
		d.mu.Lock()
		if len(d.released) > 0 {
			n := len(d.released)
			if d.chunk > 0 {
				n = min(n, d.chunk)
			}
			out := slices.Clone(d.released[:n])
			d.released = d.released[n:]
			d.mu.Unlock()
			return out, nil
		}
		if d.closed {
			d.mu.Unlock()
			return nil, io.EOF
		}
		d.mu.Unlock()
		select {
		case <-d.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (d *gatedDestination) writtenPositions() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.written))
	for i, r := range d.written {
		out[i] = string(r.Position)
	}
	return out
}

// dlqRecorder is a DLQ destination that records, and optionally fails.
type dlqRecorder struct {
	id      string
	log     *eventLog
	failErr error

	mu      sync.Mutex
	written []opencdc.Record
	pending []connector.DestinationAck
}

func (d *dlqRecorder) ID() string                     { return d.id }
func (d *dlqRecorder) Open(context.Context) error     { return nil }
func (d *dlqRecorder) Teardown(context.Context) error { return nil }
func (d *dlqRecorder) Errors() <-chan error           { return make(chan error) }

func (d *dlqRecorder) Write(_ context.Context, recs []opencdc.Record) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failErr != nil {
		return d.failErr
	}
	for _, r := range recs {
		d.written = append(d.written, r)
		d.pending = append(d.pending, connector.DestinationAck{Position: r.Position})
		if d.log != nil {
			d.log.addf("dlq:%s", r.Position)
		}
	}
	return nil
}

func (d *dlqRecorder) Ack(context.Context) ([]connector.DestinationAck, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	a := d.pending
	d.pending = nil
	return a, nil
}

func (d *dlqRecorder) positions() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.written))
	for i, r := range d.written {
		out[i] = string(r.Position)
	}
	return out
}

// stagedHarness is one source, optional destination-branch tasks, one
// destination, one DLQ, wired the way buildSharedTail does it (the destination
// branch is a shared root of its own Sink).
type stagedHarness struct {
	t      *testing.T
	w      *Worker
	sink   *Sink
	src    *batchSource
	dest   *gatedDestination
	dlq    *dlqRecorder
	cancel context.CancelFunc
	ctx    context.Context
	doErr  chan error
}

func newStagedHarness(t *testing.T, src *batchSource, dest *gatedDestination, preDest ...Task) *stagedHarness {
	t.Helper()
	is := is.New(t)
	logger := log.Test(t)

	destNode := &TaskNode{Task: NewDestinationTask(dest.id, dest, logger, NoOpConnectorMetrics{})}
	branch := destNode
	if len(preDest) > 0 {
		// destination-scoped processors run before the destination task
		head := &TaskNode{Task: preDest[0]}
		tail := head
		for _, p := range preDest[1:] {
			n := &TaskNode{Task: p}
			tail.Next = []*TaskNode{n}
			tail = n
		}
		tail.Next = []*TaskNode{destNode}
		branch = head
	}
	sink, err := NewSink(branch)
	is.NoErr(err)

	dlq := &dlqRecorder{id: "dlq", log: src.log}
	dlqTask := NewDLQ("dlq", dlq, logger, NoOpConnectorMetrics{}, 100, 100)

	srcNode := &TaskNode{Task: NewSourceTask(src.id, src, logger, NoOpConnectorMetrics{})}
	is.NoErr(srcNode.AppendToEnd(branch))
	w, err := NewWorker(srcNode, dlqTask, logger, noop.Timer{})
	is.NoErr(err)

	ctx, cancel := context.WithCancel(context.Background())
	h := &stagedHarness{t: t, w: w, sink: sink, src: src, dest: dest, dlq: dlq, ctx: ctx, cancel: cancel, doErr: make(chan error, 1)}
	is.NoErr(sink.Open(ctx))
	is.NoErr(w.Open(ctx))
	return h
}

func (h *stagedHarness) start() {
	go func() { h.doErr <- h.w.Do(h.ctx) }()
}

func (h *stagedHarness) finish() {
	h.t.Helper()
	h.cancel()
	_ = h.w.Close(context.Background())
	_ = h.sink.Close(context.Background())
}

// recs builds records with the given positions.
func recs(positions ...string) []opencdc.Record {
	out := make([]opencdc.Record, len(positions))
	for i, p := range positions {
		out[i] = opencdc.Record{
			Position: opencdc.Position(p),
			Key:      opencdc.RawData(p),
			Payload:  opencdc.Change{After: opencdc.RawData("v-" + p)},
		}
	}
	return out
}

func TestStaged_ReadsAheadAndWritesWhileAcksAreHeld(t *testing.T) {
	is := is.New(t)
	src := newBatchSource("src", recs("p0", "p1"), recs("p2", "p3"), recs("p4", "p5"))
	dest := newGatedDestination("dest")
	h := newStagedHarness(t, src, dest)
	defer h.finish()
	h.start()

	// Stop-and-wait would have read and written one batch and parked. The
	// staged engine reads and writes all three while no ack is released.
	waitForCondition(t, 5*time.Second, func() bool {
		return src.readCount() == 3 && len(dest.writtenPositions()) == 6
	})
	// Invariant 1: nothing was acked upstream, because nothing was acked
	// downstream.
	is.Equal(len(src.acked()), 0)

	// Release acks for p0..p2. A write completes when ALL its records were
	// acked, so only the first write (p0, p1) can vote; p2 waits for p3.
	dest.release(3)
	waitForCondition(t, 5*time.Second, func() bool { return len(src.acked()) == 2 })
	time.Sleep(20 * time.Millisecond)
	is.Equal(src.acked(), []string{"p0", "p1"})

	dest.releaseAll()
	waitForCondition(t, 5*time.Second, func() bool { return len(src.acked()) == 6 })
	is.Equal(src.acked(), []string{"p0", "p1", "p2", "p3", "p4", "p5"})
	is.Equal(dest.writtenPositions(), []string{"p0", "p1", "p2", "p3", "p4", "p5"})

	is.NoErr(h.w.Stop(h.ctx))
	is.NoErr(<-h.doErr)
	is.True(src.isTornDown())
}

// A batch that a processor fully filters completes immediately, while the
// batch before it is still waiting for its destination ack. Its positions
// must not reach Source.Ack until the earlier batch is acked (Invariants 1, 2).
func TestStaged_FilteredBatchBehindPendingBatch_IsNotAckedEarly(t *testing.T) {
	is := is.New(t)
	src := newBatchSource("src", recs("p0", "p1"), recs("q0"))
	dest := newGatedDestination("dest")
	seen := make(chan struct{})
	filter := &funcTask{id: "filter-q", do: func(b *Batch) error {
		for i, r := range b.records {
			if string(r.Position) == "q0" {
				b.Filter(i)
				close(seen)
			}
		}
		return nil
	}}
	// the filter runs source-side, before the shared destination
	h := newStagedHarnessWithSourceTask(t, src, dest, filter)
	defer h.finish()
	h.start()

	select {
	case <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("second batch never reached the filter")
	}
	// q0 is terminal (filtered) but p0, p1 are not acked downstream yet.
	time.Sleep(50 * time.Millisecond)
	is.Equal(len(src.acked()), 0)

	dest.releaseAll()
	waitForCondition(t, 5*time.Second, func() bool { return len(src.acked()) == 3 })
	is.Equal(src.acked(), []string{"p0", "p1", "q0"})
}

// funcTask is a processor-like task driven by a func.
type funcTask struct {
	id string
	do func(*Batch) error
}

func (f *funcTask) ID() string                           { return f.id }
func (f *funcTask) Open(context.Context) error           { return nil }
func (f *funcTask) Close(context.Context) error          { return nil }
func (f *funcTask) Do(_ context.Context, b *Batch) error { return f.do(b) }

func newStagedHarnessWithSourceTask(t *testing.T, src *batchSource, dest *gatedDestination, srcTask Task) *stagedHarness {
	t.Helper()
	is := is.New(t)
	logger := log.Test(t)
	destNode := &TaskNode{Task: NewDestinationTask(dest.id, dest, logger, NoOpConnectorMetrics{})}
	sink, err := NewSink(destNode)
	is.NoErr(err)
	dlq := &dlqRecorder{id: "dlq", log: src.log}
	dlqTask := NewDLQ("dlq", dlq, logger, NoOpConnectorMetrics{}, 100, 100)
	srcNode := &TaskNode{Task: NewSourceTask(src.id, src, logger, NoOpConnectorMetrics{})}
	is.NoErr(srcNode.AppendToEnd(&TaskNode{Task: srcTask}))
	is.NoErr(srcNode.AppendToEnd(destNode))
	w, err := NewWorker(srcNode, dlqTask, logger, noop.Timer{})
	is.NoErr(err)
	ctx, cancel := context.WithCancel(context.Background())
	h := &stagedHarness{t: t, w: w, sink: sink, src: src, dest: dest, dlq: dlq, ctx: ctx, cancel: cancel, doErr: make(chan error, 1)}
	is.NoErr(sink.Open(ctx))
	is.NoErr(w.Open(ctx))
	return h
}

// A destination nacks a record in a middle batch while the batch before it is
// incomplete and the one after is complete. The nack waits for the prefix; the
// DLQ is written exactly once, BEFORE the source ack that covers the position.
func TestStaged_NackInMiddleBatch_DLQBeforeSourceAck(t *testing.T) {
	is := is.New(t)
	elog := &eventLog{}
	src := newBatchSource("src", recs("p0", "p1"), recs("p2", "p3"), recs("p4", "p5"))
	src.log = elog
	dest := newGatedDestination("dest")
	dest.nackErr = map[string]error{"p3": cerrors.New("destination rejected p3")}
	dest.log = elog
	h := newStagedHarness(t, src, dest)
	defer h.finish()
	h.start()

	waitForCondition(t, 5*time.Second, func() bool { return len(dest.writtenPositions()) == 6 })

	// Complete the LAST batch and the middle one first; the first is held.
	// The destination acks FIFO, so release in order but hold p0: nothing may
	// be released past the unacked p0.
	dest.release(0)
	is.Equal(len(src.acked()), 0)

	dest.releaseAll()
	waitForCondition(t, 5*time.Second, func() bool { return len(src.acked()) == 6 })
	is.Equal(src.acked(), []string{"p0", "p1", "p2", "p3", "p4", "p5"})
	is.Equal(h.dlq.positions(), []string{"p3"})
	is.True(elog.index("dlq:p3") >= 0)
	is.True(elog.index("dlq:p3") < elog.index("ack:p3")) // Invariant 3: DLQ confirmed before the ack
}

// The DLQ write fails: the pipeline fails, and only the prefix whose handling
// was confirmed is released. Nothing past the failed record is acked.
func TestStaged_DLQFailure_ReleasesOnlyConfirmedPrefix(t *testing.T) {
	is := is.New(t)
	src := newBatchSource("src", recs("p0", "p1"), recs("p2", "p3"), recs("p4", "p5"))
	dest := newGatedDestination("dest")
	dest.nackErr = map[string]error{"p3": cerrors.New("destination rejected p3")}
	h := newStagedHarness(t, src, dest)
	defer h.finish()
	h.dlq.failErr = cerrors.New("dlq down")
	h.start()

	waitForCondition(t, 5*time.Second, func() bool { return len(dest.writtenPositions()) == 6 })
	dest.releaseAll()

	select {
	case err := <-h.doErr:
		is.True(err != nil)
		is.True(cerrors.Is(err, h.dlq.failErr))
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline did not fail on a DLQ write error")
	}
	is.Equal(src.acked(), []string{"p0", "p1", "p2"}) // p3 failed, p4 and p5 must not be acked
}

// Graceful stop with batches in flight: a Stop that cannot drain by its
// deadline rolls back (the worker keeps running, nothing torn down, nothing
// acked early); once the destination acks, Stop drains, acks the whole prefix
// and only then tears the source down.
func TestStaged_Stop_RollsBackWhenItCannotDrain_ThenDrains(t *testing.T) {
	is := is.New(t)
	src := newBatchSource("src", recs("p0", "p1"), recs("p2", "p3"))
	dest := newGatedDestination("dest")
	h := newStagedHarness(t, src, dest)
	defer h.finish()
	h.start()
	waitForCondition(t, 5*time.Second, func() bool { return len(dest.writtenPositions()) == 4 })

	// Two records acked: the completed prefix is p0, p1.
	dest.release(2)
	waitForCondition(t, 5*time.Second, func() bool { return len(src.acked()) == 2 })

	short, cancel := context.WithTimeout(h.ctx, 100*time.Millisecond)
	err := h.w.Stop(short)
	cancel()
	is.True(err != nil)
	is.True(!h.w.Stopping())                    // rolled back: not armed
	is.True(!src.isTornDown())                  // the source is still there to receive acks
	is.Equal(src.acked(), []string{"p0", "p1"}) // only the completed prefix, nothing early

	// The worker kept running and still reads: a new batch is admitted after the rollback.
	select {
	case err := <-h.doErr:
		t.Fatalf("worker exited after a rolled-back stop: %v", err)
	default:
	}

	dest.releaseAll()
	is.NoErr(h.w.Stop(h.ctx))
	is.Equal(src.acked(), []string{"p0", "p1", "p2", "p3"})
	is.True(src.isTornDown())
	is.NoErr(<-h.doErr)
}

// Force stop (context canceled) with batches in flight releases at most the
// already-completed prefix; the rest replays (Invariant 3).
func TestStaged_ForceStop_ReleasesNothingPastCompletedPrefix(t *testing.T) {
	is := is.New(t)
	src := newBatchSource("src", recs("p0", "p1"), recs("p2", "p3"))
	dest := newGatedDestination("dest")
	h := newStagedHarness(t, src, dest)
	h.start()
	waitForCondition(t, 5*time.Second, func() bool { return len(dest.writtenPositions()) == 4 })
	dest.release(2)
	waitForCondition(t, 5*time.Second, func() bool { return len(src.acked()) == 2 })

	h.cancel()
	select {
	case err := <-h.doErr:
		is.True(cerrors.Is(err, context.Canceled))
	case <-time.After(5 * time.Second):
		t.Fatal("Do did not return after the context was canceled")
	}
	dest.releaseAll() // acks arriving after the cancel must not be released
	time.Sleep(50 * time.Millisecond)
	is.Equal(src.acked(), []string{"p0", "p1"})
	h.finish()
}

// Credits bound read-ahead: with a stalled destination the reader stops at the
// window and memory stays flat; acks resume it.
func TestStaged_CreditsBoundReadAhead(t *testing.T) {
	is := is.New(t)
	old := defaultCreditRecords
	defaultCreditRecords = 4
	defer func() { defaultCreditRecords = old }()

	src := newBatchSource("src")
	src.infinite = true
	dest := newGatedDestination("dest")
	h := newStagedHarness(t, src, dest)
	defer h.finish()
	h.start()

	waitForCondition(t, 5*time.Second, func() bool { return src.readCount() == 4 })
	time.Sleep(100 * time.Millisecond)
	is.Equal(src.readCount(), 4) // stalled: no more reads than the window
	is.Equal(len(dest.writtenPositions()), 4)

	dest.release(2)
	waitForCondition(t, 5*time.Second, func() bool { return src.readCount() == 6 })
	time.Sleep(50 * time.Millisecond)
	is.Equal(src.readCount(), 6)
}

// A destination processor filters one record and nacks another; both get
// votes without any destination ack, the rest are written, and the source acks
// every position in order.
func TestStaged_DestinationProcessorFilterAndNack_VotesWithoutPluginAck(t *testing.T) {
	is := is.New(t)
	elog := &eventLog{}
	src := newBatchSource("src", recs("r0", "r1", "r2", "r3"))
	src.log = elog
	dest := newGatedDestination("dest")
	dest.log = elog
	proc := &funcTask{id: "dest-proc", do: func(b *Batch) error {
		b.Nack(2, cerrors.New("processor rejected r2")) // before Filter: indices are of active records
		b.Filter(1)
		return nil
	}}
	h := newStagedHarness(t, src, dest, proc)
	defer h.finish()
	h.start()

	waitForCondition(t, 5*time.Second, func() bool { return len(dest.writtenPositions()) == 2 })
	is.Equal(dest.writtenPositions(), []string{"r0", "r3"}) // r1 filtered, r2 nacked: never written
	dest.releaseAll()
	waitForCondition(t, 5*time.Second, func() bool { return len(src.acked()) == 4 })
	is.Equal(src.acked(), []string{"r0", "r1", "r2", "r3"})
	is.Equal(h.dlq.positions(), []string{"r2"})
	is.True(elog.index("dlq:r2") < elog.index("ack:r2"))
}

// Records reach the destination in source order across batches.
func TestStaged_PipelinedDeliveryOrder(t *testing.T) {
	is := is.New(t)
	rng := rand.New(rand.NewSource(1))
	var batches [][]opencdc.Record
	var want []string
	n := 0
	for range 40 {
		size := 1 + rng.Intn(5)
		var ps []string
		for range size {
			p := fmt.Sprintf("p%04d", n)
			n++
			ps = append(ps, p)
			want = append(want, p)
		}
		batches = append(batches, recs(ps...))
	}
	src := newBatchSource("src", batches...)
	dest := newGatedDestination("dest")
	dest.autoAck = true
	h := newStagedHarness(t, src, dest)
	defer h.finish()
	h.start()

	waitForCondition(t, 5*time.Second, func() bool { return len(src.acked()) == len(want) })
	is.Equal(dest.writtenPositions(), want)
	is.Equal(src.acked(), want)
	is.NoErr(h.w.Stop(h.ctx))
	is.NoErr(<-h.doErr)
}

// Acks that straddle writes (one response covering several writes, a write
// acked in several responses) complete each write exactly once, in order, and
// mark nacks at the right records.
func TestDestinationTask_AckChunksStraddlingWrites(t *testing.T) {
	is := is.New(t)
	dest := newGatedDestination("dest")
	dest.chunk = 3 // acks come back 3 at a time: [a0 a1 b0] [b1 c0 c1]
	dest.nackErr = map[string]error{"b1": cerrors.New("nack b1")}
	task := NewDestinationTask("dest", dest, log.Test(t), NoOpConnectorMetrics{})
	is.NoErr(task.Open(context.Background()))
	defer func() { _ = task.Close(context.Background()) }()

	var mu sync.Mutex
	var order []string
	batches := []*Batch{NewBatch(recs("a0", "a1")), NewBatch(recs("b0", "b1")), NewBatch(recs("c0", "c1"))}
	for i, b := range batches {
		name := string(rune('a' + i))
		is.NoErr(task.Submit(context.Background(), b, func(err error) {
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				order = append(order, name+":err")
				return
			}
			order = append(order, name)
		}))
	}
	waitForCondition(t, 5*time.Second, func() bool { return len(dest.writtenPositions()) == 6 })
	dest.releaseAll()
	waitForCondition(t, 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(order) == 3
	})
	mu.Lock()
	is.Equal(order, []string{"a", "b", "c"})
	mu.Unlock()
	is.True(!batches[0].tainted)
	is.True(batches[1].tainted) // b1 nacked
	is.Equal(batches[1].recordStatuses[0].Flag, RecordFlagAck)
	is.Equal(batches[1].recordStatuses[1].Flag, RecordFlagNack)
	is.True(!batches[2].tainted)
}

// A wrong, surplus or out-of-order ack is a protocol violation: every write in
// flight fails, no write completes successfully (no votes), and the failure is
// sticky.
func TestDestinationTask_AckProtocolViolations_FailWithoutCompleting(t *testing.T) {
	cases := map[string][]connector.DestinationAck{
		"wrong position": {{Position: opencdc.Position("zzz")}},
		"surplus ack":    {{Position: opencdc.Position("a0")}, {Position: opencdc.Position("a1")}},
	}
	for name, acks := range cases {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			dest := &scriptedAckDestination{acks: acks}
			task := NewDestinationTask("dest", dest, log.Test(t), NoOpConnectorMetrics{})
			is.NoErr(task.Open(context.Background()))
			defer func() { _ = task.Close(context.Background()) }()

			res := make(chan error, 2)
			is.NoErr(task.Submit(context.Background(), NewBatch(recs("a0")), func(err error) { res <- err }))
			select {
			case err := <-res:
				is.True(err != nil) // failed, never completed
			case <-time.After(5 * time.Second):
				t.Fatal("write neither completed nor failed")
			}
			// sticky
			waitForCondition(t, 5*time.Second, func() bool {
				return task.Submit(context.Background(), NewBatch(recs("b0")), func(error) {}) != nil
			})
		})
	}
}

type scriptedAckDestination struct {
	acks []connector.DestinationAck
	mu   sync.Mutex
	sent bool
}

func (d *scriptedAckDestination) ID() string                                    { return "scripted" }
func (d *scriptedAckDestination) Open(context.Context) error                    { return nil }
func (d *scriptedAckDestination) Teardown(context.Context) error                { return nil }
func (d *scriptedAckDestination) Errors() <-chan error                          { return make(chan error) }
func (d *scriptedAckDestination) Write(context.Context, []opencdc.Record) error { return nil }
func (d *scriptedAckDestination) Ack(ctx context.Context) ([]connector.DestinationAck, error) {
	d.mu.Lock()
	if !d.sent {
		d.sent = true
		d.mu.Unlock()
		return d.acks, nil
	}
	d.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

// batchingDestination imitates the SDK destination batcher: it acks only when
// size records are buffered or, if delay > 0, delay after the first buffered
// record. With delay == 0 a partial batch is never flushed except by Flush
// (the SDK flushes it on Destination.Stop).
type batchingDestination struct {
	id    string
	size  int
	delay time.Duration

	mu     sync.Mutex
	buf    []connector.DestinationAck
	out    []connector.DestinationAck
	ready  chan struct{}
	closed bool
	timer  *time.Timer
}

func newBatchingDestination(id string, size int, delay time.Duration) *batchingDestination {
	return &batchingDestination{id: id, size: size, delay: delay, ready: make(chan struct{}, 1)}
}

func (d *batchingDestination) ID() string                 { return d.id }
func (d *batchingDestination) Open(context.Context) error { return nil }
func (d *batchingDestination) Errors() <-chan error       { return make(chan error) }
func (d *batchingDestination) Teardown(context.Context) error {
	d.mu.Lock()
	d.closed = true
	if d.timer != nil {
		d.timer.Stop()
	}
	d.mu.Unlock()
	d.wake()
	return nil
}

func (d *batchingDestination) wake() {
	select {
	case d.ready <- struct{}{}:
	default:
	}
}

func (d *batchingDestination) Write(_ context.Context, recs []opencdc.Record) error {
	d.mu.Lock()
	for _, r := range recs {
		d.buf = append(d.buf, connector.DestinationAck{Position: r.Position})
	}
	if len(d.buf) >= d.size {
		d.flushLocked()
	} else if d.delay > 0 && d.timer == nil {
		d.timer = time.AfterFunc(d.delay, d.Flush)
	}
	d.mu.Unlock()
	return nil
}

func (d *batchingDestination) flushLocked() {
	d.out = append(d.out, d.buf...)
	d.buf = nil
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	d.wake()
}

func (d *batchingDestination) Flush() {
	d.mu.Lock()
	d.flushLocked()
	d.mu.Unlock()
}

func (d *batchingDestination) Ack(ctx context.Context) ([]connector.DestinationAck, error) {
	for {
		d.mu.Lock()
		if len(d.out) > 0 {
			out := d.out
			d.out = nil
			d.mu.Unlock()
			return out, nil
		}
		if d.closed {
			d.mu.Unlock()
			return nil, io.EOF
		}
		d.mu.Unlock()
		select {
		case <-d.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func newBatchingHarness(t *testing.T, src *batchSource, dest Destination) (*Worker, *Sink, context.Context, context.CancelFunc, chan error) {
	t.Helper()
	is := is.New(t)
	logger := log.Test(t)
	destNode := &TaskNode{Task: NewDestinationTask("dest", dest, logger, NoOpConnectorMetrics{})}
	sink, err := NewSink(destNode)
	is.NoErr(err)
	dlq := NewDLQ("dlq", &dlqRecorder{id: "dlq"}, logger, NoOpConnectorMetrics{}, 100, 100)
	srcNode := &TaskNode{Task: NewSourceTask(src.id, src, logger, NoOpConnectorMetrics{})}
	is.NoErr(srcNode.AppendToEnd(destNode))
	w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
	is.NoErr(err)
	ctx, cancel := context.WithCancel(context.Background())
	is.NoErr(sink.Open(ctx))
	is.NoErr(w.Open(ctx))
	done := make(chan error, 1)
	go func() { done <- w.Do(ctx) }()
	return w, sink, ctx, cancel, done
}

// Stop with an SDK-style batching destination (batch.size > 1).
//
// With a flush delay the tail batch flushes by itself, Stop drains and every
// record is acked. Without a delay (size > 1, delay 0) the SDK never flushes a
// partial batch by time: v1 flushes it with Destination.Stop(lastPosition)
// BEFORE waiting for acks, this prototype does not, so Stop times out. That
// outcome is pinned here as the known limitation: Stop rolls back, the worker
// keeps running, and nothing was acked early. Flushing the destination then
// lets a second Stop drain.
func TestStaged_Stop_WithBatchingDestination(t *testing.T) {
	t.Run("delay flushes the tail", func(t *testing.T) {
		is := is.New(t)
		src := newBatchSource("src", recs("p0", "p1", "p2"))
		dest := newBatchingDestination("dest", 100, 20*time.Millisecond)
		w, sink, ctx, cancel, done := newBatchingHarness(t, src, dest)
		defer func() { cancel(); _ = w.Close(context.Background()); _ = sink.Close(context.Background()) }()

		waitForCondition(t, 5*time.Second, func() bool { return src.readCount() == 1 })
		is.NoErr(w.Stop(ctx))
		is.NoErr(<-done)
		is.Equal(src.acked(), []string{"p0", "p1", "p2"})
	})
	t.Run("no delay: partial batch needs a flush", func(t *testing.T) {
		is := is.New(t)
		src := newBatchSource("src", recs("p0", "p1", "p2"))
		dest := newBatchingDestination("dest", 100, 0)
		w, sink, ctx, cancel, done := newBatchingHarness(t, src, dest)
		defer func() { cancel(); _ = w.Close(context.Background()); _ = sink.Close(context.Background()) }()

		waitForCondition(t, 5*time.Second, func() bool { return src.readCount() == 1 })
		short, c := context.WithTimeout(ctx, 150*time.Millisecond)
		err := w.Stop(short)
		c()
		is.True(err != nil)
		is.True(!w.Stopping())
		is.Equal(len(src.acked()), 0) // no early ack

		dest.Flush()
		is.NoErr(w.Stop(ctx))
		is.NoErr(<-done)
		is.Equal(src.acked(), []string{"p0", "p1", "p2"})
	})
}

// No goroutine of the staged engine outlives Stop + Close.
func TestStaged_NoGoroutineLeakAfterStopAndClose(t *testing.T) {
	is := is.New(t)
	before := runtime.NumGoroutine()
	for range 5 {
		src := newBatchSource("src", recs("p0", "p1"), recs("p2"))
		dest := newGatedDestination("dest")
		dest.autoAck = true
		h := newStagedHarness(t, src, dest)
		h.start()
		waitForCondition(t, 5*time.Second, func() bool { return len(src.acked()) == 3 })
		is.NoErr(h.w.Stop(h.ctx))
		is.NoErr(<-h.doErr)
		h.finish()
	}
	waitForCondition(t, 5*time.Second, func() bool { return runtime.NumGoroutine() <= before+2 })
}

// ledgerHarness drives the ledger and the coordinator's release logic without
// any goroutines, so the property test controls every interleaving.
type ledgerHarness struct {
	w   *Worker
	src *batchSource
	dlq *dlqRecorder
	log *eventLog
}

func newLedgerHarness(t *testing.T) *ledgerHarness {
	t.Helper()
	logger := log.Nop()
	elog := &eventLog{}
	src := newBatchSource("src")
	src.log = elog
	dlq := &dlqRecorder{id: "dlq", log: elog}
	w := &Worker{
		Source: src,
		DLQ:    NewDLQ("dlq", dlq, logger, NoOpConnectorMetrics{}, 1000, 1000),
		logger: logger,
		timer:  noop.Timer{},
		pl:     newPipelineState(),
	}
	return &ledgerHarness{w: w, src: src, dlq: dlq, log: elog}
}

// TestLedger_Property_InOrderPrefixRelease: for random windows of in-flight
// batches, M destinations, random nacks and a random vote order that finishes
// later batches before earlier ones, the positions released to the source are
// at every step a gapless prefix of source order, a position is only released
// once all M destinations voted for it (or one nacked), nacked positions reach
// the DLQ exactly once and before the source ack, and at the end everything is
// released.
func TestLedger_Property_InOrderPrefixRelease(t *testing.T) {
	for seed := int64(1); seed <= 300; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			is := is.New(t)
			rng := rand.New(rand.NewSource(seed))
			h := newLedgerHarness(t)
			ctx := context.Background()

			numBatches := 2 + rng.Intn(5) // at least 2 in flight
			m := 1 + rng.Intn(3)          // destinations

			type vote struct {
				entry *ledgerEntry
				multi *multiAckNacker
				batch *Batch
				nack  bool
			}
			var all []string
			var votes []vote
			nacked := map[string]bool{}
			voteCount := map[string]int{}

			for b := range numBatches {
				size := 1 + rng.Intn(4)
				var ps []string
				for i := range size {
					p := fmt.Sprintf("b%d-%d", b, i)
					ps = append(ps, p)
					all = append(all, p)
				}
				batch := NewBatch(recs(ps...))
				h.w.pl.credits.charge(int64(len(ps)), 1)
				e := h.w.pl.ledger.register(batch, 1, time.Now())
				multi, err := newMultiAckNacker(newRunAckNacker(e), m, batch.positions)
				is.NoErr(err)
				for _, p := range ps {
					nackedBy := -1
					if rng.Intn(8) == 0 {
						nackedBy = rng.Intn(m)
						nacked[p] = true
					}
					for d := range m {
						single := NewBatch(recs(p))
						v := vote{entry: e, multi: multi, batch: single}
						if d == nackedBy {
							single.Nack(0, cerrors.New("nack "+p))
							v.nack = true
						}
						votes = append(votes, v)
					}
				}
			}
			rng.Shuffle(len(votes), func(i, j int) { votes[i], votes[j] = votes[j], votes[i] })

			for _, v := range votes {
				var err error
				if v.nack {
					err = v.multi.Nack(ctx, v.batch, "dest")
				} else {
					err = v.multi.Ack(ctx, v.batch)
				}
				is.NoErr(err)
				voteCount[string(v.batch.positions[0])]++
				is.NoErr(h.w.releasePrefix(ctx))

				// Invariant 2: released positions are a gapless prefix of source order.
				released := h.src.acked()
				is.True(len(released) <= len(all))
				is.True(slices.Equal(released, all[:len(released)]))
				// Invariant 1: nothing is released before its votes are in
				// (all M, or a nack).
				for _, p := range released {
					is.True(nacked[p] || voteCount[p] == m)
				}
			}

			is.NoErr(h.w.releasePrefix(ctx))
			is.Equal(h.src.acked(), all) // everything released
			var wantDLQ []string
			for _, p := range all {
				if nacked[p] {
					wantDLQ = append(wantDLQ, p)
				}
			}
			is.True(slices.Equal(h.dlq.positions(), wantDLQ)) // exactly the nacked set, once, in source order
			for _, p := range wantDLQ {
				is.True(h.log.index("dlq:"+p) < h.log.index("ack:"+p)) // Invariant 3
			}
			is.Equal(h.w.pl.ledger.depth(), 0)
			inflight, _ := h.w.pl.credits.inFlight()
			is.Equal(inflight, int64(0)) // every record's credit came back
		})
	}
}

// Illegal vote sequences hit the fatal path: nothing is released.
func TestLedger_IllegalVotes_AreFatalAndReleaseNothing(t *testing.T) {
	is := is.New(t)
	h := newLedgerHarness(t)
	ctx := context.Background()
	batch := NewBatch(recs("a", "b"))
	e := h.w.pl.ledger.register(batch, 1, time.Now())

	is.NoErr(e.Ack(ctx, NewBatch(recs("a"))))
	err := e.Ack(ctx, NewBatch(recs("a"))) // double vote
	is.True(err != nil)
	is.True(h.w.pl.ledger.failure() != nil)
	is.True(h.w.pl.ledger.next() == nil) // nothing is released after a failure
	is.Equal(len(h.src.acked()), 0)

	h2 := newLedgerHarness(t)
	e2 := h2.w.pl.ledger.register(NewBatch(recs("a")), 1, time.Now())
	is.True(e2.Ack(ctx, NewBatch(recs("zzz"))) != nil) // unknown position
}

// A processor nack under a DLQ that tolerates none (window enabled, threshold
// 0) halts the pipeline AT the nack: read-ahead must not deliver the records
// that come after it (Invariant 6, halt means halt). The records before it are
// delivered and acked; the nacked record and the ones after are never acked.
func TestStaged_ProcessorNackWithDisabledDLQ_HaltsBeforeLaterBatches(t *testing.T) {
	is := is.New(t)
	src := newBatchSource("src", recs("p0"), recs("p1"), recs("p2"), recs("p3"))
	dest := newGatedDestination("dest")
	dest.autoAck = true
	reject := &funcTask{id: "drift-detect", do: func(b *Batch) error {
		for i, r := range b.records {
			if string(r.Position) == "p1" {
				b.Nack(i, cerrors.New("record p1 drifted"))
			}
		}
		return nil
	}}
	h := newStagedHarnessWithSourceTask(t, src, dest, reject)
	defer h.finish()
	h.w.DLQ = NewDLQ("dlq", &dlqRecorder{id: "dlq"}, log.Test(t), NoOpConnectorMetrics{}, 1, 0)
	h.start()

	select {
	case err := <-h.doErr:
		is.True(err != nil)
		is.True(cerrors.IsFatalError(err))
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline did not halt on the nack")
	}
	time.Sleep(50 * time.Millisecond)
	is.Equal(dest.writtenPositions(), []string{"p0"}) // nothing after the nack was delivered
	waitForCondition(t, 5*time.Second, func() bool { return len(src.acked()) <= 1 })
	for _, p := range src.acked() {
		is.Equal(p, "p0") // the nacked record and its successors are never acked
	}
}

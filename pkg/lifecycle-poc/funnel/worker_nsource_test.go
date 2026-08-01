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
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/matryer/is"
)

// This file tests slice 3b of the arch-v2 multi-connector epic at the
// funnel.Worker/Sink level: N independent Workers (one per source), each with
// its own per-source prefix (source + DLQ), converging on ONE shared
// destination owned by a funnel.Sink. It covers the three properties the
// design exists to guarantee:
//
//  1. Cross-source ack isolation: each Worker only ever acks its OWN source,
//     never a sibling's, even though the destination task is a single shared
//     object (TestNSource_NoCrossContamination_SourceStaysFixedWhileSiblingRuns).
//  2. Serialized entry into the shared destination: two Workers' goroutines
//     must never call Write/Ack on the shared destination concurrently -
//     destinations require single-writer, position-ordered pairing (see
//     DestinationTask.Do and TaskNode.MarkSharedBoundary's doc)
//     (TestNSource_ConcurrentWorkers_SerializeSharedDestinationWrites).
//  3. A source that finishes gracefully (io.EOF) does not affect a sibling,
//     and does not touch the shared sink at all - only the coordinating Sink
//     owns Open/Close for it
//     (TestNSource_SourceFinishesGracefully_DoesNotTouchSharedSink).

// finiteSource is a fakeSource variant that, once its fixed record set is
// exhausted, returns io.EOF instead of blocking - simulating a snapshot-only
// connector that has genuinely run out of data on its own (nobody called
// Stop). See Worker.doTask's io.EOF handling (slice 3b).
type finiteSource struct {
	id string

	mu      sync.Mutex
	records []opencdc.Record
	pos     int
	acked   []opencdc.Position
}

func newFiniteSource(id string, records []opencdc.Record) *finiteSource {
	return &finiteSource{id: id, records: records}
}

func (s *finiteSource) ID() string                 { return s.id }
func (s *finiteSource) Open(context.Context) error { return nil }
func (s *finiteSource) Errors() <-chan error       { return make(chan error) }
func (s *finiteSource) Teardown(context.Context) error {
	return nil
}

func (s *finiteSource) Read(context.Context) ([]opencdc.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pos >= len(s.records) {
		return nil, io.EOF
	}
	out := s.records[s.pos:]
	s.pos = len(s.records)
	return out, nil
}

func (s *finiteSource) Ack(_ context.Context, positions []opencdc.Position) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acked = append(s.acked, positions...)
	return nil
}

// exclusiveDestination is a funnel.Destination test double with NO internal
// synchronization protecting Write/Ack against concurrent callers - it relies
// entirely on the caller (Worker.doTask's sharedMu, acquired via
// TaskNode.MarkSharedBoundary) to serialize entry. If two goroutines ever
// call Write concurrently, the busy-channel check below deterministically
// fails the test (not a probabilistic race-detector catch): the second
// caller's non-blocking send to the capacity-1 busy channel fails immediately
// because the first caller is still holding it. An artificial sleep between
// acquiring "busy" and releasing it widens the window so two genuinely
// concurrent calls are overwhelmingly likely to overlap if the outer lock
// were ever missing or buggy.
type exclusiveDestination struct {
	id   string
	t    *testing.T
	busy chan struct{}

	mu      sync.Mutex
	written []opencdc.Record
	pending []connector.DestinationAck
}

func newExclusiveDestination(t *testing.T, id string) *exclusiveDestination {
	return &exclusiveDestination{id: id, t: t, busy: make(chan struct{}, 1)}
}

func (d *exclusiveDestination) ID() string                     { return d.id }
func (d *exclusiveDestination) Open(context.Context) error     { return nil }
func (d *exclusiveDestination) Teardown(context.Context) error { return nil }
func (d *exclusiveDestination) Errors() <-chan error           { return make(chan error) }

func (d *exclusiveDestination) Write(_ context.Context, recs []opencdc.Record) error {
	select {
	case d.busy <- struct{}{}:
	default:
		d.t.Fatalf("concurrent Write detected on shared destination %s: sharedMu failed to serialize N-source access", d.id)
	}
	defer func() { <-d.busy }()

	time.Sleep(2 * time.Millisecond) // widen the window - see type doc

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range recs {
		d.written = append(d.written, r)
		d.pending = append(d.pending, connector.DestinationAck{Position: r.Position})
	}
	return nil
}

func (d *exclusiveDestination) Ack(context.Context) ([]connector.DestinationAck, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	acks := d.pending
	d.pending = nil
	return acks, nil
}

func (d *exclusiveDestination) receivedPositions() []opencdc.Position {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]opencdc.Position, len(d.written))
	for i, r := range d.written {
		out[i] = r.Position
	}
	return out
}

// buildNSourceWorkers wires len(sources) independent Workers (one per
// source, each with its own DLQ) whose per-source prefixes all AppendToEnd
// the SAME shared destination node, wrapped in a Sink - exactly the shape
// lifecycle-poc.Service.buildRunnablePipeline produces in production for N
// sources sharing one destination.
func buildNSourceWorkers(t *testing.T, dest Destination, sources ...Source) ([]*Worker, *Sink) {
	t.Helper()
	is := is.New(t)
	logger := log.Test(t)

	destTask := NewDestinationTask("shared-dest", dest, logger, NoOpConnectorMetrics{})
	sharedRoot := &TaskNode{Task: destTask}
	sink, err := NewSink(sharedRoot)
	is.NoErr(err)

	workers := make([]*Worker, len(sources))
	for i, src := range sources {
		dlqDest := newFakeDestination(fmt.Sprintf("dlq-%d", i))
		dlq := NewDLQ(fmt.Sprintf("dlq-%d", i), dlqDest, logger, NoOpConnectorMetrics{}, 0, 0)

		srcNode := &TaskNode{Task: NewSourceTask(fmt.Sprintf("src-%d", i), src, logger, NoOpConnectorMetrics{})}
		is.NoErr(srcNode.AppendToEnd(sharedRoot))

		w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
		is.NoErr(err)
		workers[i] = w
	}
	return workers, sink
}

// TestNSource_NoCrossContamination_SourceStaysFixedWhileSiblingRuns is the
// direct test of this slice's core safety property: source A is stopped
// (idle) while source B keeps streaming through the SAME shared destination.
// A's acked position set must stay exactly as it was the instant A stopped -
// never advanced by anything B does - because each Worker is its own
// terminal acker bound to its own w.Source (see runnablePipeline.workers'
// doc): B's Ack/Nack calls can only ever reach B's own Worker, never A's.
func TestNSource_NoCrossContamination_SourceStaysFixedWhileSiblingRuns(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	recordsA := randomRecords(3)
	recordsB := randomRecords(20)
	srcA := newFakeSource("srcA", recordsA)
	srcB := newFakeSource("srcB", recordsB)
	dest := newFakeDestination("shared")

	workers, sink := buildNSourceWorkers(t, dest, srcA, srcB)
	wA, wB := workers[0], workers[1]

	is.NoErr(sink.Open(ctx))
	is.NoErr(wA.Open(ctx))
	is.NoErr(wB.Open(ctx))

	doErrA := make(chan error, 1)
	doErrB := make(chan error, 1)
	go func() { doErrA <- wA.Do(ctx) }()
	go func() { doErrB <- wB.Do(ctx) }()

	// Let A fully drain its (small, fixed) record set, then stop it - A goes
	// idle while B keeps running.
	waitForCondition(t, 5*time.Second, func() bool { return len(srcA.ackedPositions()) == len(recordsA) })
	is.NoErr(wA.Stop(ctx))
	is.NoErr(<-doErrA)

	frozenA := srcA.ackedPositions()
	is.Equal(len(frozenA), len(recordsA))

	// B keeps streaming well past A's own record count, through the SAME
	// shared destination.
	waitForCondition(t, 5*time.Second, func() bool { return len(srcB.ackedPositions()) >= len(recordsB)/2 })

	// The core assertion: A's acked positions have not moved, even though B
	// is actively acking through the identical shared destination task.
	is.Equal(frozenA, srcA.ackedPositions())

	is.NoErr(wB.Stop(ctx))
	is.NoErr(<-doErrB)
	is.Equal(len(srcB.ackedPositions()), len(recordsB))

	is.NoErr(wA.Close(ctx))
	is.NoErr(wB.Close(ctx))
	is.NoErr(sink.Close(ctx))
}

// TestNSource_ConcurrentWorkers_SerializeSharedDestinationWrites runs three
// source workers concurrently against one exclusiveDestination (no internal
// locking of its own - see its doc). If TaskNode.MarkSharedBoundary's
// sharedMu ever failed to serialize entry into the shared destination task,
// exclusiveDestination.Write would detect the overlap and fail the test
// deterministically.
func TestNSource_ConcurrentWorkers_SerializeSharedDestinationWrites(t *testing.T) {
	ctx := context.Background()
	is := is.New(t)

	const n = 3
	const recordsPerSource = 15

	sources := make([]Source, n)
	fakeSources := make([]*fakeSource, n)
	for i := range n {
		recs := randomRecords(recordsPerSource)
		fs := newFakeSource(fmt.Sprintf("src-%d", i), recs)
		fakeSources[i] = fs
		sources[i] = fs
	}
	dest := newExclusiveDestination(t, "shared")

	workers, sink := buildNSourceWorkers(t, dest, sources...)
	is.NoErr(sink.Open(ctx))

	doErrs := make([]chan error, n)
	for i, w := range workers {
		is.NoErr(w.Open(ctx))
		doErrs[i] = make(chan error, 1)
		go func() { doErrs[i] <- w.Do(ctx) }()
	}

	waitForCondition(t, 10*time.Second, func() bool {
		return len(dest.receivedPositions()) >= n*recordsPerSource
	})

	for i, w := range workers {
		is.NoErr(w.Stop(ctx))
		is.NoErr(<-doErrs[i])
		is.NoErr(w.Close(ctx))
	}
	is.NoErr(sink.Close(ctx))

	is.Equal(len(dest.receivedPositions()), n*recordsPerSource)
	for _, fs := range fakeSources {
		is.Equal(len(fs.ackedPositions()), recordsPerSource) // source fully drained and acked
	}
}

// TestNSource_SourceFinishesGracefully_DoesNotTouchSharedSink is the
// "snapshot-only source finishes while another streams" scenario: source A
// exhausts its fixed record set (io.EOF, nobody called Stop) while source B
// keeps running. A's Do() must return nil promptly (not an error), and A's
// own Worker.Close must NOT close/teardown the shared destination - only an
// explicit, separate Sink.Close call (made here only after BOTH workers have
// stopped, mirroring runPipeline's workersWg.Wait ordering) may do that. This
// is the crux assertion: the shared destination survives a sibling's early,
// clean exit.
func TestNSource_SourceFinishesGracefully_DoesNotTouchSharedSink(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	recordsA := randomRecords(4)
	srcA := newFiniteSource("srcA", recordsA)
	srcB := newFakeSource("srcB", randomRecords(50))
	dest := newFakeDestination("shared")

	workers, sink := buildNSourceWorkers(t, dest, srcA, srcB)
	wA, wB := workers[0], workers[1]

	is.NoErr(sink.Open(ctx))
	is.NoErr(wA.Open(ctx))
	is.NoErr(wB.Open(ctx))

	doErrA := make(chan error, 1)
	doErrB := make(chan error, 1)
	go func() { doErrA <- wA.Do(ctx) }()
	go func() { doErrB <- wB.Do(ctx) }()

	// A must exit on its own (nil, not an error) once it runs out of records -
	// nobody calls wA.Stop.
	select {
	case err := <-doErrA:
		is.NoErr(err) // a self-exhausted source is graceful, not a failure
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for srcA's worker to finish gracefully on io.EOF")
	}

	// Closing A's own worker must not touch the shared destination at all:
	// TaskNode.MarkSharedBoundary excludes it from wA.FirstTask.Tasks().
	is.NoErr(wA.Close(ctx))

	// B is still actively writing through the SAME shared destination task
	// object A's own Worker.Close just ran past without touching (see
	// TaskNode.MarkSharedBoundary) - prove the destination is still usable
	// (i.e. A's Close did NOT tear it down) by letting B fully drain and ack
	// every one of its records through it.
	waitForCondition(t, 5*time.Second, func() bool { return len(srcB.ackedPositions()) == 50 })

	is.NoErr(wB.Stop(ctx))
	is.NoErr(<-doErrB)
	is.NoErr(wB.Close(ctx))

	// Only now, after every worker has exited, is it safe/expected to close
	// the shared sink - mirroring runPipeline's workersWg.Wait-gated
	// rp.sink.Close call.
	is.NoErr(sink.Close(ctx))
}

// TestNSource_SharedBoundary_RetryDoesNotSelfDeadlock guards the interaction
// that rebasing slice 3b onto the bounded-retry fix (#2726, PR #2732) created,
// and which nothing else covers.
//
// #2726 split doTask into a thin wrapper plus doTaskAttempt, and made the retry
// recursion call doTaskAttempt DIRECTLY so it can thread retry state. 3b added
// a per-shared-root mutex so N source workers cannot interleave Write/Ack pairs
// on one destination's single gRPC stream.
//
// Those two combine badly if the mutex is acquired in the wrong function. Put
// it in doTaskAttempt and the FIRST retry inside a shared node re-enters and
// self-deadlocks on a non-reentrant sync.Mutex — the worker hangs forever
// holding the processing lock, so the pipeline never drains and SIGTERM never
// completes (invariant 7). It is a hang, not a failure, so no existing
// assertion would catch it: the suite would simply stop.
//
// The lock therefore lives in the doTask wrapper, acquired once at the external
// entry point and held across the whole synchronous pass — every retry
// recursion and any fan-out beneath it included. This test drives a retry
// through a shared-boundary node and requires it to complete, so a future move
// of that lock fails here (by timeout) instead of in production.
func TestNSource_SharedBoundary_RetryDoesNotSelfDeadlock(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(3)

	// A task that retries the batch once, then lets it through.
	retryOnce := &retryOnceThenPassTask{id: "shared-retry"}
	node := &TaskNode{Task: retryOnce}
	node.MarkSharedBoundary() // exactly what funnel.Sink does for the shared tail

	parent := &fakeParentAckNacker{}
	w := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}

	done := make(chan error, 1)
	go func() { done <- w.doTask(ctx, node, NewBatch(slices.Clone(records)), parent) }()

	select {
	case err := <-done:
		is.NoErr(err)
		is.True(retryOnce.calls >= 2) // the retry actually happened
	case <-time.After(10 * time.Second):
		t.Fatal("doTask deadlocked: a retry inside a shared-boundary node " +
			"re-entered the shared mutex. The lock must be acquired in the " +
			"doTask wrapper, not in doTaskAttempt (which the retry recursion " +
			"calls directly).")
	}
}

// retryOnceThenPassTask marks the whole batch Retry on its first pass and then
// passes it through, so doTask recurses exactly once.
type retryOnceThenPassTask struct {
	id    string
	calls int
}

func (t *retryOnceThenPassTask) ID() string                  { return t.id }
func (t *retryOnceThenPassTask) Open(context.Context) error  { return nil }
func (t *retryOnceThenPassTask) Close(context.Context) error { return nil }

func (t *retryOnceThenPassTask) Do(_ context.Context, b *Batch) error {
	t.calls++
	if t.calls == 1 {
		b.Retry(0, len(b.ActiveRecords()))
	}
	return nil
}

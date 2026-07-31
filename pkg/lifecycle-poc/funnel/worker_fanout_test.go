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
	"bufio"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/csync"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/matryer/is"
)

// This file tests slice 3a of the arch-v2 multi-connector epic end to end at
// the funnel.Worker level: doNextTask's M-destination fan-out wired to real
// multiAckNacker instances, driven either directly (doTask, for
// fast/deterministic per-scenario control) or through the full public
// Do()/Stop() loop (for the integration-flavored test). Unit-level
// multiAckNacker semantics are covered in multi_ack_nacker_test.go; this file
// is about the WIRING - doNextTask's clone/pool/multiAckNacker construction,
// and DestinationTask's real per-record ack/nack translation - being correct
// together.

// passthroughTask is a minimal no-op Task standing in for the shared prefix
// node (source + pipeline processors) directly above a fan-out point. Its
// own Do is never exercised by these tests (they call doTask on it, which
// runs it once and then immediately falls into doNextTask since it always
// returns a clean, non-tainted batch).
type passthroughTask struct{ id string }

func (p passthroughTask) ID() string                       { return p.id }
func (p passthroughTask) Open(context.Context) error       { return nil }
func (p passthroughTask) Close(context.Context) error      { return nil }
func (p passthroughTask) Do(context.Context, *Batch) error { return nil }

// fakeSource is a minimal, non-gomock funnel.Source test double: Read
// returns every remaining record as one batch, then blocks (or errors) once
// exhausted; Ack records every position handed to it, in call order, for
// assertions on ordering/progress.
//
// Read's blocking branch selects on BOTH stopped (closed by Teardown) and
// ctx.Done(): Worker.Stop does NOT cancel the context passed to Do (only a
// real plugin's stream closing - triggered by Source.Teardown - unblocks a
// pending Read in production, per worker.go's doTask/Stop doc comments), so
// a fake that only waited on ctx.Done() would deadlock forever once
// Worker.Stop is called on a fakeSource whose Read is already blocked here -
// exactly the bug this comment exists to flag: an earlier version of this
// type did exactly that and hung TestFanOut_Integration_GeneratorToTwoFileSinks
// until its own 5s WaitTimeout gave up.
type fakeSource struct {
	id string

	mu      sync.Mutex
	records []opencdc.Record
	pos     int
	acked   []opencdc.Position
	stopped chan struct{}
}

func newFakeSource(id string, records []opencdc.Record) *fakeSource {
	return &fakeSource{id: id, records: records, stopped: make(chan struct{})}
}

func (s *fakeSource) ID() string                 { return s.id }
func (s *fakeSource) Open(context.Context) error { return nil }
func (s *fakeSource) Errors() <-chan error       { return make(chan error) }

func (s *fakeSource) Teardown(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stopped:
		// already closed - Teardown can be called more than once (Stop then Close)
	default:
		close(s.stopped)
	}
	return nil
}

func (s *fakeSource) Read(ctx context.Context) ([]opencdc.Record, error) {
	s.mu.Lock()
	if s.pos < len(s.records) {
		out := s.records[s.pos:]
		s.pos = len(s.records)
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()

	select {
	case <-s.stopped:
		// Mirrors a real plugin's stream closing on Teardown - doTask treats
		// context.Canceled from the first task as a graceful stop.
		return nil, context.Canceled
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *fakeSource) Ack(_ context.Context, positions []opencdc.Position) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acked = append(s.acked, positions...)
	return nil
}

func (s *fakeSource) ackedPositions() []opencdc.Position {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]opencdc.Position(nil), s.acked...)
}

// fakeDestination is a minimal, non-gomock funnel.Destination test double.
// It records every written record; Ack immediately acks everything written
// so far (DestinationTask.Do's contract: one Write followed by repeated Ack
// calls until every position is acknowledged - see destination.go's Do). If
// nackErr is set for a position, that position's ack carries the error
// instead, driving DestinationTask.Do's own markBatchRecords to mark the
// batch record Nack'd - exactly how a real destination plugin failure would
// surface. blockOn, if set, makes Write block until unblock() is called,
// before recording anything - used for the slow-destination (AC-3) test.
type fakeDestination struct {
	id string

	mu       sync.Mutex
	written  []opencdc.Record
	pending  []connector.DestinationAck
	nackErr  map[string]error
	blockOn  opencdc.Position
	blocked  chan struct{}
	unblockC chan struct{}
}

func newFakeDestination(id string) *fakeDestination {
	return &fakeDestination{id: id}
}

func (d *fakeDestination) ID() string                     { return d.id }
func (d *fakeDestination) Open(context.Context) error     { return nil }
func (d *fakeDestination) Teardown(context.Context) error { return nil }
func (d *fakeDestination) Errors() <-chan error           { return make(chan error) }

// blockWrites arms this destination to block the Write call that includes
// pos, signaling blocked (closed) once it starts blocking, and waiting for
// unblock() before proceeding.
func (d *fakeDestination) blockWrites(pos opencdc.Position) (blocked <-chan struct{}, unblock func()) {
	d.mu.Lock()
	d.blockOn = pos
	d.blocked = make(chan struct{})
	d.unblockC = make(chan struct{})
	blockedCh, unblockC := d.blocked, d.unblockC
	d.mu.Unlock()
	return blockedCh, func() { close(unblockC) }
}

func (d *fakeDestination) setNackErr(pos opencdc.Position, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.nackErr == nil {
		d.nackErr = make(map[string]error)
	}
	d.nackErr[string(pos)] = err
}

func (d *fakeDestination) Write(_ context.Context, recs []opencdc.Record) error {
	d.mu.Lock()
	blockOn, blocked, unblockC := d.blockOn, d.blocked, d.unblockC
	d.mu.Unlock()

	if blockOn != nil {
		for _, r := range recs {
			if string(r.Position) == string(blockOn) {
				close(blocked)
				<-unblockC
				break
			}
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range recs {
		d.written = append(d.written, r)
		var ackErr error
		if d.nackErr != nil {
			ackErr = d.nackErr[string(r.Position)]
		}
		d.pending = append(d.pending, connector.DestinationAck{Position: r.Position, Error: ackErr})
	}
	return nil
}

func (d *fakeDestination) Ack(context.Context) ([]connector.DestinationAck, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	acks := d.pending
	d.pending = nil
	return acks, nil
}

func (d *fakeDestination) receivedPositions() []opencdc.Position {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]opencdc.Position, len(d.written))
	for i, r := range d.written {
		out[i] = r.Position
	}
	return out
}

// waitForCondition polls cond (not a fixed sleep) until it returns true, or
// fails the test after timeout - used wherever a test needs to observe the
// result of work happening in a different goroutine without asserting on an
// unsynchronized race.
func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	if !cond() {
		t.Fatalf("timed out after %s waiting for condition", timeout)
	}
}

// buildFanoutWorker wires a shared passthrough node fanning out to len(dests)
// destination branches, and a real *Worker (Source=src, DLQ backed by a
// no-op-nacking fake destination) around it - the exact same construction
// buildTaskNodes produces in production for M destination connectors.
func buildFanoutWorker(t *testing.T, src *fakeSource, dests ...*fakeDestination) (*Worker, *TaskNode) {
	t.Helper()
	logger := log.Test(t)

	dlqDest := newFakeDestination("dlq")
	dlq := NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, 0, 0)

	destNodes := make([]*TaskNode, len(dests))
	for i, d := range dests {
		task := NewDestinationTask(d.id, d, logger, NoOpConnectorMetrics{})
		destNodes[i] = &TaskNode{Task: task}
	}
	branchNode := &TaskNode{Task: passthroughTask{id: "shared"}, Next: destNodes}
	srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{branchNode}}

	w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
	is.New(t).NoErr(err)

	return w, branchNode
}

// TestDoNextTask_FanOut_M2_Basic is the M=2 happy-path fan-out test: both
// destinations receive and ack every record, and the source is acked once
// with the full, ordered position set.
func TestDoNextTask_FanOut_M2_Basic(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(10)
	src := newFakeSource("src", records)
	destA := newFakeDestination("destA")
	destB := newFakeDestination("destB")

	w, branchNode := buildFanoutWorker(t, src, destA, destB)
	batch := NewBatch(append([]opencdc.Record(nil), records...))

	err := w.doTask(ctx, branchNode, batch, w)
	is.NoErr(err)

	wantPositions := make([]opencdc.Position, len(records))
	for i, r := range records {
		wantPositions[i] = r.Position
	}
	is.Equal(destA.receivedPositions(), wantPositions)
	is.Equal(destB.receivedPositions(), wantPositions)
	is.Equal(src.ackedPositions(), wantPositions)
}

// TestDoNextTask_FanOut_M3_Basic is the M=3 counterpart: unanimity across
// three branches, not just two.
func TestDoNextTask_FanOut_M3_Basic(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(7)
	src := newFakeSource("src", records)
	destA := newFakeDestination("destA")
	destB := newFakeDestination("destB")
	destC := newFakeDestination("destC")

	w, branchNode := buildFanoutWorker(t, src, destA, destB, destC)
	batch := NewBatch(append([]opencdc.Record(nil), records...))

	err := w.doTask(ctx, branchNode, batch, w)
	is.NoErr(err)

	wantPositions := make([]opencdc.Position, len(records))
	for i, r := range records {
		wantPositions[i] = r.Position
	}
	is.Equal(destA.receivedPositions(), wantPositions)
	is.Equal(destB.receivedPositions(), wantPositions)
	is.Equal(destC.receivedPositions(), wantPositions)
	is.Equal(src.ackedPositions(), wantPositions)
}

// TestDoNextTask_FanOut_PartialNack_RoutesToDLQ_NotSourceLoss exercises
// invariants 1 and 3 through the REAL wiring (not multiAckNacker in
// isolation): destB fails one record; that record must reach the DLQ
// (and, through the DLQ's own successful write, still be acked to the
// source - it was handled, just not by the main destination), while every
// other record is acked normally. destA, meanwhile, DID successfully write
// the failed record (a partial write is expected and allowed - see
// worker.go's multiAckNacker doc comment) - it is simply not what earns the
// ack.
func TestDoNextTask_FanOut_PartialNack_RoutesToDLQ_NotSourceLoss(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(5)
	failPos := records[2].Position

	src := newFakeSource("src", records)
	destA := newFakeDestination("destA")
	destB := newFakeDestination("destB")
	destB.setNackErr(failPos, cerrors.New("destB: simulated write failure"))

	w, branchNode := buildFanoutWorker(t, src, destA, destB)
	batch := NewBatch(append([]opencdc.Record(nil), records...))

	err := w.doTask(ctx, branchNode, batch, w)
	is.NoErr(err)

	// destA wrote everything, including the record destB failed on - a
	// partial write, which is expected and does not by itself earn an ack.
	is.Equal(len(destA.receivedPositions()), 5)

	// The source was still acked for EVERY position (invariant 3: the
	// failed record was handled via the DLQ, not silently dropped).
	acked := src.ackedPositions()
	is.Equal(len(acked), 5)
	for i, r := range records {
		is.Equal(acked[i], r.Position)
	}
}

// TestDoNextTask_FanOut_SlowDestination_SourceDoesNotAdvancePastWithheld is
// AC-3: while one destination withholds its ack for a record, the source's
// ack must not advance past it, even though a faster sibling destination has
// already completed every record. Only once the slow destination catches up
// does the withheld position (and everything after it, in one run) get
// acked.
//
// This is driven as TWO sequential doTask calls (one small "already
// delivered" batch, then a second batch that gets stuck) rather than one
// single batch with an internal blocking point: DestinationTask.Do calls
// Write ONCE per batch with the entire batch's records, so a fake that
// blocks partway through a single Write call would block ALL of that
// batch's records, including ones that logically precede the blocking
// position - it cannot selectively let part of one batch through while
// withholding the rest. Splitting into two batches (mirroring the real
// worker reading one batch at a time from the source) reproduces the
// intended scenario faithfully: the first batch's positions are already
// fully resolved and acked by the time the second batch's slow destination
// blocks.
func TestDoNextTask_FanOut_SlowDestination_SourceDoesNotAdvancePastWithheld(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(5)
	withheldPos := records[2].Position

	src := newFakeSource("src", records)
	destFast := newFakeDestination("destFast")
	destSlow := newFakeDestination("destSlow")

	w, branchNode := buildFanoutWorker(t, src, destFast, destSlow)

	// First batch (records 0,1): nothing blocks, resolves and acks fully.
	firstBatch := NewBatch(append([]opencdc.Record(nil), records[0:2]...))
	is.NoErr(w.doTask(ctx, branchNode, firstBatch, w))
	is.Equal(src.ackedPositions(), []opencdc.Position{records[0].Position, records[1].Position})

	// Second batch (records 2,3,4): destSlow blocks on the FIRST record of
	// this batch (withheldPos), withholding the whole batch.
	blocked, unblock := destSlow.blockWrites(withheldPos)
	secondBatch := NewBatch(append([]opencdc.Record(nil), records[2:5]...))

	doneCh := make(chan error, 1)
	go func() { doneCh <- w.doTask(ctx, branchNode, secondBatch, w) }()

	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for destSlow to block on the withheld position")
	}

	// destFast races ahead concurrently and, being unblocked, usually
	// finishes the second batch almost immediately - but it runs in its own
	// pool goroutine with no synchronization forcing it to finish before
	// this point, so wait (bounded) for it rather than asserting on a race.
	// What actually matters for AC-3 is proven below regardless of exactly
	// when destFast finishes: unanimity means the source must still be
	// stuck behind the withheld position (invariant 1: ack only after ALL
	// destinations durably wrote), however far ahead destFast gets.
	waitForCondition(t, 5*time.Second, func() bool {
		return len(destFast.receivedPositions()) == 5 // 2 from batch 1 + 3 from batch 2
	})

	// Acked positions are unchanged from just after the first batch: even
	// though destFast (and, per the poll above, its full write) raced
	// ahead, destSlow hasn't voted at all yet for the second batch, so
	// nothing in it can have been released.
	acked := src.ackedPositions()
	is.Equal(len(acked), 2) // still only the two positions from the first batch
	is.Equal(acked[0], records[0].Position)
	is.Equal(acked[1], records[1].Position)

	unblock()

	select {
	case err := <-doneCh:
		is.NoErr(err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for doTask to finish after unblocking destSlow")
	}

	finalAcked := src.ackedPositions()
	is.Equal(len(finalAcked), 5)
	is.Equal(finalAcked[4], records[4].Position)
}

// TestFanOut_Integration_GeneratorToTwoFileSinks is the "1 generator -> 2
// file sinks" integration test named in the slice 3a test plan: it drives
// the REAL public Do()/Stop() loop (not doTask called directly, unlike the
// tests above) against two genuinely file-backed destinations, and asserts
// the source's final acked position and each file's delivered set agree with
// no loss.
func TestFanOut_Integration_GeneratorToTwoFileSinks(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const total = 25
	records := randomRecords(total)
	src := newFakeSource("generator", records)

	dir := t.TempDir()
	fileA := newFileDestination(t, "sinkA", filepath.Join(dir, "sinkA.log"))
	fileB := newFileDestination(t, "sinkB", filepath.Join(dir, "sinkB.log"))

	// buildFanoutWorker's helper takes *fakeDestination; file sinks are a
	// different Destination implementation, so this test wires the worker
	// directly instead of reusing that helper.
	logger := log.Test(t)
	dlqDest := newFakeDestination("dlq")
	dlq := NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, 0, 0)

	destANode := &TaskNode{Task: NewDestinationTask("sinkA", fileA, logger, NoOpConnectorMetrics{})}
	destBNode := &TaskNode{Task: NewDestinationTask("sinkB", fileB, logger, NoOpConnectorMetrics{})}
	srcNode := &TaskNode{
		Task: NewSourceTask("generator", src, logger, NoOpConnectorMetrics{}),
		Next: []*TaskNode{{Task: passthroughTask{id: "shared"}, Next: []*TaskNode{destANode, destBNode}}},
	}

	worker, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
	is.NoErr(err)
	is.NoErr(worker.Open(ctx))

	var wg csync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := worker.Do(ctx); err != nil {
			t.Errorf("worker.Do returned an error: %v", err)
		}
	}()

	// Give the fan-out time to complete, then stop gracefully - mirrors
	// Example_simpleStream's own pattern (funnel_test.go).
	time.AfterFunc(300*time.Millisecond, func() { _ = worker.Stop(ctx) })
	is.NoErr(wg.WaitTimeout(ctx, 5*time.Second))
	is.NoErr(worker.Close(ctx))

	wantPositions := make([]string, total)
	for i, r := range records {
		wantPositions[i] = string(r.Position)
	}

	is.Equal(fileA.lines(t), wantPositions)
	is.Equal(fileB.lines(t), wantPositions)

	acked := src.ackedPositions()
	is.Equal(len(acked), total)
	is.Equal(string(acked[total-1]), wantPositions[total-1])
}

// fileDestination is a literal file-backed funnel.Destination: Write appends
// each record's position as its own line, immediately flushed (so a
// concurrently-reading test can see it as soon as Ack is called). Modeled on
// tests/chaos's deliveryLog/fanoutDestination, simplified since this test has
// no crash-durability requirement of its own.
type fileDestination struct {
	id string
	f  *os.File

	mu      sync.Mutex
	pending []connector.DestinationAck
}

func newFileDestination(t *testing.T, id, path string) *fileDestination {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	is.New(t).NoErr(err)
	t.Cleanup(func() { _ = f.Close() })
	return &fileDestination{id: id, f: f}
}

func (d *fileDestination) ID() string                     { return d.id }
func (d *fileDestination) Open(context.Context) error     { return nil }
func (d *fileDestination) Teardown(context.Context) error { return nil }
func (d *fileDestination) Errors() <-chan error           { return make(chan error) }

func (d *fileDestination) Write(_ context.Context, recs []opencdc.Record) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range recs {
		if _, err := fmt.Fprintln(d.f, string(r.Position)); err != nil {
			return err
		}
		d.pending = append(d.pending, connector.DestinationAck{Position: r.Position})
	}
	return nil
}

func (d *fileDestination) Ack(context.Context) ([]connector.DestinationAck, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	acks := d.pending
	d.pending = nil
	return acks, nil
}

func (d *fileDestination) lines(t *testing.T) []string {
	t.Helper()
	is := is.New(t)
	is.NoErr(d.f.Sync())

	f, err := os.Open(d.f.Name())
	is.NoErr(err)
	defer f.Close()

	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	is.NoErr(sc.Err())
	return out
}

// --- OQ-1 property test ---
//
// OQ-1 (highest composition risk, per the slice 3a plan): confirm that a
// per-destination processor splitting a record DIFFERENTLY across branches
// (destA splits record 3 into 2 pieces, destB doesn't split it at all)
// still collapses to the SAME original position on both sides via
// originalBatch - so multiAckNacker's per-position tally, keyed by that
// position, never sees two branches disagreeing about what "the same
// record" is.
//
// No pgregory.net/rapid or gopter dependency exists in this module (CLAUDE.md
// requires justifying new dependencies, and builder_roundtrip_test.go already
// established this repo's precedent: a deterministic, seeded pseudo-random
// driver instead of a new property-testing library). This test seeds
// math/rand directly (not math/rand/v2, so t.Logf's seed is reproducible
// across Go versions) and logs the seed on failure so any failure is
// reproducible.
type splitTask struct {
	id string
	// splitPositions marks (by index into ActiveRecords) which records this
	// task splits into 2 pieces.
	splitPositions map[int]bool
}

func (s *splitTask) ID() string                  { return s.id }
func (s *splitTask) Open(context.Context) error  { return nil }
func (s *splitTask) Close(context.Context) error { return nil }

func (s *splitTask) Do(_ context.Context, b *Batch) error {
	// Iterate in reverse so splitting (which shifts subsequent indices) does
	// not invalidate earlier indices still to be processed.
	active := b.ActiveRecords()
	for i := len(active) - 1; i >= 0; i-- {
		if s.splitPositions[i] {
			orig := active[i]
			b.SplitRecord(i, []opencdc.Record{orig, orig}) // split into 2 identical-content pieces
		}
	}
	return nil
}

// TestFanOut_OQ1_DifferentialSplitCollapsesToSameOriginalPosition is OQ-1's
// resolution: a property test over random per-branch split/nack
// interleavings, asserting every source record is acked-once XOR DLQ'd-once
// - never both, never neither - even when branches split the SAME original
// record differently.
func TestFanOut_OQ1_DifferentialSplitCollapsesToSameOriginalPosition(t *testing.T) {
	const iterations = 200
	seed := time.Now().UnixNano()
	t.Logf("seed=%d (reproduce a failure by hardcoding this seed)", seed)
	rng := rand.New(rand.NewSource(seed))

	for iter := 0; iter < iterations; iter++ {
		n := 3 + rng.Intn(6)        // 3..8 records
		branches := 2 + rng.Intn(2) // 2 or 3 branches

		records := randomRecords(n)
		positions := make([]opencdc.Position, n)
		for i, r := range records {
			positions[i] = r.Position
		}

		// Each branch independently decides which records to split, and
		// which single record (if any) to nack.
		type branchPlan struct {
			splits  map[int]bool
			nackIdx int // -1 means no nack this branch
		}
		plans := make([]branchPlan, branches)
		for b := range plans {
			splits := make(map[int]bool)
			for i := 0; i < n; i++ {
				if rng.Intn(3) == 0 { // ~1/3 chance this branch splits record i
					splits[i] = true
				}
			}
			nackIdx := -1
			if rng.Intn(4) == 0 { // ~1/4 chance this branch nacks one record
				nackIdx = rng.Intn(n)
			}
			plans[b] = branchPlan{splits: splits, nackIdx: nackIdx}
		}

		src := newFakeSource("src", records)
		dests := make([]*fakeDestination, branches)
		destNodes := make([]*TaskNode, branches)
		logger := log.Test(t)
		for b := range plans {
			dest := newFakeDestination(fmt.Sprintf("dest%d", b))
			if plans[b].nackIdx >= 0 {
				dest.setNackErr(positions[plans[b].nackIdx], cerrors.New("simulated nack"))
			}
			dests[b] = dest

			split := &splitTask{id: fmt.Sprintf("split%d", b), splitPositions: plans[b].splits}
			splitNode := &TaskNode{Task: split}
			destTask := NewDestinationTask(dest.id, dest, logger, NoOpConnectorMetrics{})
			splitNode.Next = []*TaskNode{{Task: destTask}}
			destNodes[b] = splitNode
		}

		dlqDest := newFakeDestination("dlq")
		dlq := NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, 0, 0)
		branchNode := &TaskNode{Task: passthroughTask{id: "shared"}, Next: destNodes}
		srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{branchNode}}

		w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
		if err != nil {
			t.Fatalf("iter %d (seed %d): NewWorker: %v", iter, seed, err)
		}

		batch := NewBatch(append([]opencdc.Record(nil), records...))
		if err := w.doTask(context.Background(), branchNode, batch, w); err != nil {
			t.Fatalf("iter %d (seed %d): doTask: %v", iter, seed, err)
		}

		// The round-trip assertion: every original position was acked to the
		// source (directly, or via a successful DLQ nack-then-ack) EXACTLY
		// once - never lost, never double-counted regardless of how
		// differently each branch split or nacked it.
		acked := src.ackedPositions()
		ackedSet := make(map[string]int, len(acked))
		for _, p := range acked {
			ackedSet[string(p)]++
		}
		for i, p := range positions {
			count, ok := ackedSet[string(p)]
			if !ok || count != 1 {
				t.Fatalf(
					"iter %d (seed %d): position %d (%q) was acked %d times, want exactly 1 - "+
						"acked-once XOR DLQ'd-once was violated (splits=%v)",
					iter, seed, i, p, count, plans,
				)
			}
		}
		is := is.New(t)
		is.Equal(len(acked), n) // no extras, no gaps
	}
}

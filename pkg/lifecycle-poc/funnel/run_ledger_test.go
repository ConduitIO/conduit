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
	"math/rand"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/matryer/is"
)

// This file is the regression suite for #2723 (split-run head acked before
// its tail is delivered) and #2730 (split-run membership defined two
// different ways), fixed by run_ledger.go's runAckNacker/splitRun rather than
// by touching RecordFlag semantics - see
// docs/design-documents/20260801-archv2-split-run-ack-ledger.md for the full
// design and why the two previously-rejected approaches (#2725, #2727) don't
// work.

// --- Test tasks -------------------------------------------------------

// splitOnceTask splits the record at index 0 into 2 identical pieces, the
// first time it's called on a batch containing it, and does nothing after.
type splitOnceTask struct {
	id        string
	triggered bool
}

func (t *splitOnceTask) ID() string                  { return t.id }
func (t *splitOnceTask) Open(context.Context) error  { return nil }
func (t *splitOnceTask) Close(context.Context) error { return nil }
func (t *splitOnceTask) Do(_ context.Context, b *Batch) error {
	if t.triggered {
		return nil
	}
	active := b.ActiveRecords()
	if len(active) == 0 {
		return nil
	}
	t.triggered = true
	orig := active[0]
	b.SplitRecord(0, []opencdc.Record{orig, orig})
	return nil
}

// deferSecondTask marks index 1 (the tail of a 2-piece split) for retry the
// first time it sees at least 2 active records, then leaves it alone (so the
// retry pass, seeing just that 1 record, resolves it to the default Ack).
// This is the #2723 "Retry-head" shape: the head (index 0) reaches an Ack
// call on its own, in a separate sub-batch, while the tail is still in
// flight.
type deferSecondTask struct {
	id        string
	triggered bool
}

func (t *deferSecondTask) ID() string                  { return t.id }
func (t *deferSecondTask) Open(context.Context) error  { return nil }
func (t *deferSecondTask) Close(context.Context) error { return nil }
func (t *deferSecondTask) Do(_ context.Context, b *Batch) error {
	active := b.ActiveRecords()
	if !t.triggered && len(active) > 1 {
		t.triggered = true
		b.Retry(1, len(active))
	}
	return nil
}

// TestRunLedger_RetryHead_NotAckedUntilTailResolves is the direct regression
// test for #2723's "Retry" shape: a split run's head reaches its own Ack call
// while the tail is still being retried, and the run's original position must
// not reach the parent until the tail resolves too.
func TestRunLedger_RetryHead_NotAckedUntilTailResolves(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(1)
	origPos := records[0].Position

	splitNode := &TaskNode{Task: &splitOnceTask{id: "split"}}
	deferNode := &TaskNode{Task: &deferSecondTask{id: "defer"}}
	splitNode.Next = []*TaskNode{deferNode}

	parent := &fakeParentAckNacker{}
	w := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}

	batch := NewBatch(slices.Clone(records))
	is.NoErr(w.doTask(ctx, splitNode, batch, newRunAckNacker(parent)))

	acks := parent.ackCalls()
	is.Equal(len(acks), 1) // exactly one release, not one per piece
	is.Equal(acks[0].positions, []opencdc.Position{origPos})
	is.Equal(len(parent.nackCalls()), 0)
}

// deferHeadFilterTask, on its first call over 3 active records, filters the
// head (index 0) and retries the middle piece (index 1), leaving the last
// piece (index 2) as a plain Ack. Filter alone never taints a batch (see
// Batch.Filter), so pairing it with a Retry on a different index is what
// forces doTask's tainted loop to partition the run into three separate
// sub-batches: [Filter], [Retry->Ack], [Ack] - covering #2723's "Filter"
// shape, where the filtered head must not release the run either.
type deferHeadFilterTask struct {
	id        string
	triggered bool
}

func (t *deferHeadFilterTask) ID() string                  { return t.id }
func (t *deferHeadFilterTask) Open(context.Context) error  { return nil }
func (t *deferHeadFilterTask) Close(context.Context) error { return nil }
func (t *deferHeadFilterTask) Do(_ context.Context, b *Batch) error {
	active := b.ActiveRecords()
	if !t.triggered && len(active) == 3 {
		t.triggered = true
		b.Filter(0, 1)
		b.Retry(1, 2)
	}
	return nil
}

// TestRunLedger_FilterHead_NotAckedUntilTailResolves is the direct regression
// test for #2723's "Filter" shape.
func TestRunLedger_FilterHead_NotAckedUntilTailResolves(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(1)
	origPos := records[0].Position

	splitNode := &TaskNode{Task: &split3Task{id: "split3"}}
	deferNode := &TaskNode{Task: &deferHeadFilterTask{id: "defer"}}
	splitNode.Next = []*TaskNode{deferNode}

	parent := &fakeParentAckNacker{}
	w := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}

	batch := NewBatch(slices.Clone(records))
	is.NoErr(w.doTask(ctx, splitNode, batch, newRunAckNacker(parent)))

	acks := parent.ackCalls()
	is.Equal(len(acks), 1)
	is.Equal(acks[0].positions, []opencdc.Position{origPos})
	is.Equal(len(parent.nackCalls()), 0)
}

// split3Task splits the record at index 0 into 3 identical pieces, once.
type split3Task struct {
	id        string
	triggered bool
}

func (t *split3Task) ID() string                  { return t.id }
func (t *split3Task) Open(context.Context) error  { return nil }
func (t *split3Task) Close(context.Context) error { return nil }
func (t *split3Task) Do(_ context.Context, b *Batch) error {
	if t.triggered {
		return nil
	}
	active := b.ActiveRecords()
	if len(active) == 0 {
		return nil
	}
	t.triggered = true
	orig := active[0]
	b.SplitRecord(0, []opencdc.Record{orig, orig, orig})
	return nil
}

// --- #2730: position rewrite defeats run membership --------------------

// rewritePositionTask rewrites every active record's OWN Position field
// (what a processor would do), leaving Batch's separately-tracked original
// position (b.positions[i]) untouched. Models the #2730 shape: a processor
// upstream of a later SplitRecord call changes record.Position.
type rewritePositionTask struct {
	id string
}

func (t *rewritePositionTask) ID() string                  { return t.id }
func (t *rewritePositionTask) Open(context.Context) error  { return nil }
func (t *rewritePositionTask) Close(context.Context) error { return nil }
func (t *rewritePositionTask) Do(_ context.Context, b *Batch) error {
	active := b.ActiveRecords()
	for i, r := range active {
		r2 := r.Clone()
		r2.Position = append(opencdc.Position("rewritten-"), r2.Position...)
		b.SetRecords(i, []opencdc.Record{r2})
	}
	return nil
}

// TestRunLedger_PositionRewriteThenSplit_NoPrematureAck is the regression
// test for #2730: SplitRecord must key run membership on Batch's own tracked
// original position (b.positions[i]), not the record's own (rewritable)
// Position field - otherwise a later partial-retry on the run would be
// classified as an ordinary record and acked with zero destination writes for
// two of its three pieces.
func TestRunLedger_PositionRewriteThenSplit_NoPrematureAck(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(1)
	origPos := records[0].Position // the TRUE original source position

	rewriteNode := &TaskNode{Task: &rewritePositionTask{id: "rewrite"}}
	splitNode := &TaskNode{Task: &split3Task{id: "split3"}}
	deferNode := &TaskNode{Task: &deferHeadFilterTask{id: "defer"}}
	rewriteNode.Next = []*TaskNode{splitNode}
	splitNode.Next = []*TaskNode{deferNode}

	parent := &fakeParentAckNacker{}
	w := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}

	batch := NewBatch(slices.Clone(records))
	is.NoErr(w.doTask(ctx, rewriteNode, batch, newRunAckNacker(parent)))

	acks := parent.ackCalls()
	is.Equal(len(acks), 1)
	// The released position is the TRUE original source position, not the
	// rewritten one a processor assigned to the record's own Position field.
	is.Equal(acks[0].positions, []opencdc.Position{origPos})
}

// --- Attempt-2 killer C1: deterministic output-cap processor must still converge ---

// outputCapTask always "processes" at most cap records per Do() call and
// retries the rest - exactly the documented ProcessorTask.Do behavior for a
// processor that returns fewer records than it received (e.g. an embedding
// processor with a per-call batch limit). Retry/Filter semantics are
// completely untouched by this change, so this must converge in exactly the
// same number of rounds it would on main.
type outputCapTask struct {
	id    string
	cap   int
	calls int
}

func (t *outputCapTask) ID() string                  { return t.id }
func (t *outputCapTask) Open(context.Context) error  { return nil }
func (t *outputCapTask) Close(context.Context) error { return nil }
func (t *outputCapTask) Do(_ context.Context, b *Batch) error {
	t.calls++
	if t.calls > 20 {
		// Defensive cap for the TEST itself (not production code): if this
		// fires, the change under test made retry non-convergent.
		return cerrors.New("outputCapTask: too many rounds, non-convergent")
	}
	active := b.ActiveRecords()
	if len(active) > t.cap {
		b.Retry(t.cap, len(active))
	}
	return nil
}

// TestRunLedger_OutputCapProcessor_ConvergesAndAcksOnce is attempt-2's killer
// C1, guarded against: normalizeRun in #2727 escalated an ENTIRE split run to
// Retry, so a deterministic-output-cap processor's retry input never shrank -
// unbounded recursion into #2726's stack-overflow crash. This design never
// escalates anything; Retry/Filter behave exactly as on main, so a split run
// fed into an output-capped processor converges in the same bounded number of
// rounds main would take, and the run's original position is still released
// exactly once at the end.
func TestRunLedger_OutputCapProcessor_ConvergesAndAcksOnce(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(1)
	origPos := records[0].Position

	splitNode := &TaskNode{Task: &split5Task{id: "split5"}}
	capTask := &outputCapTask{id: "cap", cap: 2}
	capNode := &TaskNode{Task: capTask}
	splitNode.Next = []*TaskNode{capNode}

	parent := &fakeParentAckNacker{}
	w := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}

	batch := NewBatch(slices.Clone(records))
	is.NoErr(w.doTask(ctx, splitNode, batch, newRunAckNacker(parent)))

	// 5 pieces, cap 2 per round: rounds of 5->(2 ack,3 retry), 3->(2 ack,1
	// retry), 1->(1 ack) = 3 rounds. Bounded and deterministic, identical to
	// what main's unmodified Retry/subBatchByFlag logic would produce.
	is.Equal(capTask.calls, 3)

	acks := parent.ackCalls()
	is.Equal(len(acks), 1) // released exactly once, despite 3 internal rounds
	is.Equal(acks[0].positions, []opencdc.Position{origPos})
}

// split5Task splits the record at index 0 into 5 identical pieces, once.
type split5Task struct {
	id        string
	triggered bool
}

func (t *split5Task) ID() string                  { return t.id }
func (t *split5Task) Open(context.Context) error  { return nil }
func (t *split5Task) Close(context.Context) error { return nil }
func (t *split5Task) Do(_ context.Context, b *Batch) error {
	if t.triggered {
		return nil
	}
	active := b.ActiveRecords()
	if len(active) == 0 {
		return nil
	}
	t.triggered = true
	orig := active[0]
	b.SplitRecord(0, []opencdc.Record{orig, orig, orig, orig, orig})
	return nil
}

// --- Attempt-2 killer C2: no double transformation on retry ---

// markOnceTask "fully processes" (appends "|X" to the payload) only the
// first record it's handed each round, and retries the rest - so on a
// retry pass, a piece that already carries a marker from an earlier round is
// NEVER handed to this task again (it already left via Ack and proceeded
// downstream). If it were re-fed (as #2727's whole-run escalation did), it
// would pick up a second "|X".
type markOnceTask struct {
	id string
}

func (t *markOnceTask) ID() string                  { return t.id }
func (t *markOnceTask) Open(context.Context) error  { return nil }
func (t *markOnceTask) Close(context.Context) error { return nil }
func (t *markOnceTask) Do(_ context.Context, b *Batch) error {
	active := b.ActiveRecords()
	keep := len(active)
	if keep > 1 {
		keep = 1
	}
	recs := make([]opencdc.Record, keep)
	for i := 0; i < keep; i++ {
		r2 := active[i].Clone()
		r2.Payload.After = opencdc.RawData(string(active[i].Payload.After.Bytes()) + "|X")
		recs[i] = r2
	}
	if keep > 0 {
		b.SetRecords(0, recs)
	}
	if keep < len(active) {
		b.Retry(keep, len(active))
	}
	return nil
}

// TestRunLedger_NonIdempotentProcessor_TransformsExactlyOnce is attempt-2's
// killer C2: #2727's whole-run escalation re-fed already-Ack'd (already
// transformed) pieces back to the processor, silently double-encoding them
// before they reached the destination. This design never re-feeds a
// terminal piece to any task, so the destination must receive each of the 3
// split pieces transformed EXACTLY once.
func TestRunLedger_NonIdempotentProcessor_TransformsExactlyOnce(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(1)
	logger := log.Nop()
	dest := newFakeDestination("dest")

	splitNode := &TaskNode{Task: &split3Task{id: "split3"}}
	markNode := &TaskNode{Task: &markOnceTask{id: "mark"}}
	destNode := &TaskNode{Task: NewDestinationTask(dest.id, dest, logger, NoOpConnectorMetrics{})}
	splitNode.Next = []*TaskNode{markNode}
	markNode.Next = []*TaskNode{destNode}

	parent := &fakeParentAckNacker{}
	w := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}

	batch := NewBatch(slices.Clone(records))
	is.NoErr(w.doTask(ctx, splitNode, batch, newRunAckNacker(parent)))

	is.Equal(len(dest.written), 3)
	for _, r := range dest.written {
		is.Equal(string(r.Payload.After.Bytes()), "value|X") // exactly one marker, never "value|X|X"
	}

	acks := parent.ackCalls()
	is.Equal(len(acks), 1)
	is.Equal(acks[0].positions, []opencdc.Position{records[0].Position})
}

// --- In-source-order release ---

// TestRunLedger_InSourceOrderRelease_DeferredRunBlocksLaterPosition is
// invariant 4's direct single-branch test: a run whose completion is
// deferred (p0, split and retried) must release before a standalone record
// positioned after it in the original batch (p1) - proving the release order
// the parent observes always matches source order, never arrival order.
func TestRunLedger_InSourceOrderRelease_DeferredRunBlocksLaterPosition(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(2)
	p0, p1 := records[0].Position, records[1].Position

	splitNode := &TaskNode{Task: &splitOnceTask{id: "split"}}
	deferNode := &TaskNode{Task: &deferSecondTask{id: "defer"}}
	splitNode.Next = []*TaskNode{deferNode}

	parent := &fakeParentAckNacker{}
	w := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}

	batch := NewBatch(slices.Clone(records))
	is.NoErr(w.doTask(ctx, splitNode, batch, newRunAckNacker(parent)))

	acks := parent.ackCalls()
	is.Equal(len(acks), 2)
	is.Equal(acks[0].positions, []opencdc.Position{p0}) // the deferred run releases first...
	is.Equal(acks[1].positions, []opencdc.Position{p1}) // ...strictly before the record positioned after it
}

// --- Fan-out composition ---

// gatedSplitRetryTask splits the FIRST record matching targetPos into 2
// pieces and defers (retries) the tail, exactly like deferSecondTask/
// splitOnceTask combined - except the retry pass blocks on gate until the
// test closes it, letting the test observe multiAckNacker's state while this
// branch's run is still incomplete.
type gatedSplitRetryTask struct {
	id        string
	targetPos opencdc.Position
	triggered bool
	entered   chan struct{}
	gate      chan struct{}
}

func (t *gatedSplitRetryTask) ID() string                  { return t.id }
func (t *gatedSplitRetryTask) Open(context.Context) error  { return nil }
func (t *gatedSplitRetryTask) Close(context.Context) error { return nil }
func (t *gatedSplitRetryTask) Do(_ context.Context, b *Batch) error {
	active := b.ActiveRecords()
	if !t.triggered {
		for i, r := range active {
			if string(r.Position) == string(t.targetPos) {
				t.triggered = true
				orig := r
				b.SplitRecord(i, []opencdc.Record{orig, orig})
				b.Retry(i+1, i+2)
				return nil
			}
		}
		return nil
	}
	// Retry pass for the deferred tail (a single record): block until the
	// test says to proceed.
	close(t.entered)
	<-t.gate
	return nil
}

// TestRunLedger_FanOut_PendingRunOnOneBranch_NoPrematureUnanimity is the
// fan-out (M=2) composition test: branch A splits p1 and defers its tail;
// branch B is a plain pass-through. Branch B alone voting ack for p1 must not
// release it (no premature unanimity - multiAckNacker still requires BOTH
// branches, and branch A's OWN runAckNacker withholds its vote until its run
// completes) and, since p1 sits between p0 and p2 in source order, p2 must
// not release either until p1 does (invariant 4, composed across
// runAckNacker and multiAckNacker).
func TestRunLedger_FanOut_PendingRunOnOneBranch_NoPrematureUnanimity(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(3)
	p0, p1, p2 := records[0].Position, records[1].Position, records[2].Position

	src := newFakeSource("src", records)
	destA := newFakeDestination("destA")
	destB := newFakeDestination("destB")
	logger := log.Test(t)

	dlqDest := newFakeDestination("dlq")
	dlq := NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, 0, 0)

	gate := make(chan struct{})
	entered := make(chan struct{})
	branchATask := &gatedSplitRetryTask{id: "branchA-split", targetPos: p1, entered: entered, gate: gate}
	branchANode := &TaskNode{Task: branchATask}
	branchANode.Next = []*TaskNode{{Task: NewDestinationTask(destA.id, destA, logger, NoOpConnectorMetrics{})}}

	branchBNode := &TaskNode{Task: NewDestinationTask(destB.id, destB, logger, NoOpConnectorMetrics{})}

	fanoutNode := &TaskNode{Task: passthroughTask{id: "shared"}, Next: []*TaskNode{branchANode, branchBNode}}
	srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{fanoutNode}}

	w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
	is.NoErr(err)

	batch := NewBatch(append([]opencdc.Record(nil), records...))
	done := make(chan error, 1)
	go func() { done <- w.doTask(ctx, fanoutNode, batch, w) }()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for branch A to reach the gated retry pass")
	}

	// Branch B (unblocked) finishes fully.
	waitForCondition(t, 5*time.Second, func() bool {
		return len(destB.receivedPositions()) == 3
	})

	// Branch A has voted p0 (standalone in its own run-ledger) but not p1
	// (still gated) or p2 (not reached yet - doTask's loop is still
	// synchronously blocked resolving p1's retry). Only p0 can have reached
	// unanimity so far.
	is.Equal(src.ackedPositions(), []opencdc.Position{p0})

	close(gate) // let branch A's deferred tail resolve
	is.NoErr(<-done)

	is.Equal(src.ackedPositions(), []opencdc.Position{p0, p1, p2})
}

// --- Zero-value Ack status is safe under this design (decision for item 5) ---

// TestBatch_SplitRecord_ZeroValueAck_SafeUnderRunLedger proves the decision
// documented on SplitRecord: leaving the zero-value RecordFlagAck default for
// newly split pieces is safe here, even when injected into an
// already-Retry-flagged run (attempt 1's exact finding), because run
// completion is tracked independently via splitRun.total/terminalCount, never
// by requiring recordStatuses to agree across a run.
func TestBatch_SplitRecord_ZeroValueAck_SafeUnderRunLedger(t *testing.T) {
	is := is.New(t)
	records := randomRecords(1)
	batch := NewBatch(slices.Clone(records))

	batch.Retry(0)
	is.Equal(batch.recordStatuses[0].Flag, RecordFlagRetry)

	batch.SplitRecord(0, []opencdc.Record{records[0], records[0], records[0]})

	is.Equal(len(batch.recordStatuses), 3)
	is.Equal(batch.recordStatuses[0].Flag, RecordFlagRetry) // head keeps its flag
	is.Equal(batch.recordStatuses[1].Flag, RecordFlagAck)   // zero-value default
	is.Equal(batch.recordStatuses[2].Flag, RecordFlagAck)

	run := batch.runs[0]
	is.True(run != nil)
	is.Equal(run.total, 3)

	parent := &fakeParentAckNacker{}
	r := newRunAckNacker(parent)
	ctx := context.Background()

	// The Ack-flagged pieces resolve first...
	is.NoErr(r.Ack(ctx, batch.sub(1, 3)))
	is.Equal(len(parent.ackCalls()), 0) // not complete - head hasn't voted yet

	// ...then the (previously Retry-flagged) head resolves too.
	is.NoErr(r.Ack(ctx, batch.sub(0, 1)))
	acks := parent.ackCalls()
	is.Equal(len(acks), 1)
	is.Equal(acks[0].positions, []opencdc.Position{records[0].Position})
}

// --- Property test ---

type propOutcome int

const (
	propAck propOutcome = iota
	propFilter
	propNack
	propSplitBothAck
	propSplitTailRetryThenAck
	propSplitTailNack
)

// propRewriteTask rewrites the record's own Position field for every active
// record whose plan_id is in rewrite - modeling a processor that reassigns
// positions, upstream of a later split (the #2730 shape), for a random subset
// of records.
type propRewriteTask struct{ rewrite map[string]bool }

func (t *propRewriteTask) ID() string                  { return "prop-rewrite" }
func (t *propRewriteTask) Open(context.Context) error  { return nil }
func (t *propRewriteTask) Close(context.Context) error { return nil }
func (t *propRewriteTask) Do(_ context.Context, b *Batch) error {
	active := b.ActiveRecords()
	for i, r := range active {
		if !t.rewrite[r.Metadata["plan_id"]] {
			continue
		}
		r2 := r.Clone()
		r2.Position = append(opencdc.Position("rw-"), r2.Position...)
		b.SetRecords(i, []opencdc.Record{r2})
	}
	return nil
}

// propSplitTask splits every active record whose plan_id is in split into a
// head/tail pair, tagged via Metadata["role"] so later stages (which cannot
// see Batch's internal position bookkeeping the way this white-box test file
// can, by design - real processors never see Batch at all) can tell them
// apart without relying on content.
type propSplitTask struct{ split map[string]bool }

func (t *propSplitTask) ID() string                  { return "prop-split" }
func (t *propSplitTask) Open(context.Context) error  { return nil }
func (t *propSplitTask) Close(context.Context) error { return nil }
func (t *propSplitTask) Do(_ context.Context, b *Batch) error {
	active := b.ActiveRecords()
	for i := len(active) - 1; i >= 0; i-- {
		r := active[i]
		if !t.split[r.Metadata["plan_id"]] {
			continue
		}
		head := r.Clone()
		head.Metadata["role"] = "head"
		tail := r.Clone()
		tail.Metadata["role"] = "tail"
		b.SplitRecord(i, []opencdc.Record{head, tail})
	}
	return nil
}

// propDivergeTask is the last task in the property-test pipeline: for each
// active record it applies the plan's outcome. Split pairs are only ever
// resolved from the "tail" role, and only once (tailRetried), so a retry pass
// converges instead of looping.
type propDivergeTask struct {
	outcome     map[string]propOutcome
	tailRetried map[string]bool
}

func (t *propDivergeTask) ID() string                  { return "prop-diverge" }
func (t *propDivergeTask) Open(context.Context) error  { return nil }
func (t *propDivergeTask) Close(context.Context) error { return nil }
func (t *propDivergeTask) Do(_ context.Context, b *Batch) error {
	active := b.ActiveRecords()
	for i := len(active) - 1; i >= 0; i-- {
		r := active[i]
		id := r.Metadata["plan_id"]
		role := r.Metadata["role"]
		switch t.outcome[id] {
		case propFilter:
			if role == "" {
				b.Filter(i)
			}
		case propNack:
			if role == "" {
				b.Nack(i, cerrors.New("chaos nack "+id))
			}
		case propSplitBothAck:
			// leave both pieces at the default Ack.
		case propSplitTailRetryThenAck:
			if role == "tail" && !t.tailRetried[id] {
				t.tailRetried[id] = true
				b.Retry(i)
			}
		case propSplitTailNack:
			if role == "tail" {
				b.Nack(i, cerrors.New("chaos split-nack "+id))
			}
		case propAck:
			// leave at the default Ack.
		}
	}
	return nil
}

// TestRunLedger_Property_SplitFilterRetryNackRewrite is the randomised
// property test: over many random combinations of split/filter/retry/nack/
// position-rewrite, every original source position must be acked-once XOR
// DLQ'd-once - never both, never neither - and State.Position (represented
// here by src.ackedPositions()) must never skip or reorder a position.
// propPlan is one randomised iteration's full test fixture: which records
// exist, and what each one is planned to do. Split out from the test body to
// keep TestRunLedger_Property_SplitFilterRetryNackRewrite's own cyclomatic
// complexity down - the branching lives here, in a function with a single
// job (build a plan), not interleaved with the assertions.
type propPlan struct {
	records    []opencdc.Record
	positions  []opencdc.Position
	outcome    map[string]propOutcome
	rewrite    map[string]bool
	split      map[string]bool
	wantNacked map[string]bool
}

func newPropPlan(rng *rand.Rand, n int) propPlan {
	p := propPlan{
		records:    make([]opencdc.Record, n),
		positions:  make([]opencdc.Position, n),
		outcome:    make(map[string]propOutcome, n),
		rewrite:    make(map[string]bool, n),
		split:      make(map[string]bool, n),
		wantNacked: make(map[string]bool, n),
	}
	for i := 0; i < n; i++ {
		id := strconv.Itoa(i)
		p.records[i] = opencdc.Record{
			Position:  opencdc.Position(id),
			Operation: opencdc.OperationCreate,
			Metadata:  opencdc.Metadata{"plan_id": id},
			Payload:   opencdc.Change{After: opencdc.RawData("value")},
		}
		p.positions[i] = p.records[i].Position

		if rng.Intn(3) == 0 {
			p.rewrite[id] = true
		}

		outcomes := []propOutcome{
			propAck, propFilter, propNack,
			propSplitBothAck, propSplitTailRetryThenAck, propSplitTailNack,
		}
		o := outcomes[rng.Intn(len(outcomes))]
		p.outcome[id] = o
		p.split[id] = o == propSplitBothAck || o == propSplitTailRetryThenAck || o == propSplitTailNack
		p.wantNacked[id] = o == propNack || o == propSplitTailNack
	}
	return p
}

// expectedCounts returns how many records the main destination and the DLQ
// destination should have received under this plan - see the case-by-case
// reasoning in the two propSplit* comments below.
func (p propPlan) expectedCounts() (wantDestCount, wantDLQCount int) {
	for _, o := range p.outcome {
		switch o {
		case propAck:
			wantDestCount++
		case propFilter:
			// reaches neither destination
		case propNack:
			wantDLQCount++
		case propSplitBothAck, propSplitTailRetryThenAck:
			wantDestCount += 2 // head and tail both reach the destination
		case propSplitTailNack:
			// Batch.Nack's EXISTING (pre-existing, main) propagation logic
			// propagates a nack across an entire split run (setFlagWithErr,
			// see batch.go) - nacking the tail also flags the head Nack, so
			// the whole 2-member run is dispatched to acker.Nack in ONE
			// group. Neither piece reaches the destination; the run
			// collapses to exactly one DLQ entry (the same collapsing
			// originalBatch()/splitRun's ackBatch/nackBatch already do for a
			// fully-resolved run).
			wantDLQCount++
		}
	}
	return wantDestCount, wantDLQCount
}

func TestRunLedger_Property_SplitFilterRetryNackRewrite(t *testing.T) {
	const iterations = 300
	seed := time.Now().UnixNano()
	t.Logf("seed=%d (reproduce a failure by hardcoding this seed)", seed)
	rng := rand.New(rand.NewSource(seed))

	for iter := 0; iter < iterations; iter++ {
		n := 2 + rng.Intn(5) // 2..6 records
		plan := newPropPlan(rng, n)
		records, positions := plan.records, plan.positions
		outcome, rewrite, split, wantNacked := plan.outcome, plan.rewrite, plan.split, plan.wantNacked

		src := newFakeSource("src", records)
		dest := newFakeDestination("dest")
		dlqDest := newFakeDestination("dlq")
		logger := log.Test(t)
		dlq := NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, 0, 0)

		rewriteNode := &TaskNode{Task: &propRewriteTask{rewrite: rewrite}}
		splitNode := &TaskNode{Task: &propSplitTask{split: split}}
		divergeNode := &TaskNode{Task: &propDivergeTask{outcome: outcome, tailRetried: map[string]bool{}}}
		destNode := &TaskNode{Task: NewDestinationTask(dest.id, dest, logger, NoOpConnectorMetrics{})}
		srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{rewriteNode}}
		rewriteNode.Next = []*TaskNode{splitNode}
		splitNode.Next = []*TaskNode{divergeNode}
		divergeNode.Next = []*TaskNode{destNode}

		w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
		if err != nil {
			t.Fatalf("iter %d (seed %d): NewWorker: %v", iter, seed, err)
		}

		batch := NewBatch(append([]opencdc.Record(nil), records...))
		if err := w.doTask(context.Background(), rewriteNode, batch, newRunAckNacker(w)); err != nil {
			t.Fatalf("iter %d (seed %d): doTask: %v", iter, seed, err)
		}

		acked := src.ackedPositions()
		ackedSet := make(map[string]int, len(acked))
		for _, p := range acked {
			ackedSet[string(p)]++
		}
		for i, p := range positions {
			count := ackedSet[string(p)]
			if count != 1 {
				t.Fatalf(
					"iter %d (seed %d): position %d (%q) was acked %d times, want exactly 1 (nacked=%v, split=%v, rewrite=%v)",
					iter, seed, i, p, count, wantNacked, split, rewrite,
				)
			}
		}
		is := is.New(t)
		is.Equal(len(acked), n) // no extras, no gaps

		// Every original position acked-once XOR DLQ'd-once: cross-check by
		// COUNT which destination actually received it (dlqRecord builds a
		// fresh, DLQ-shaped opencdc.Record - see dlq.go - so matching by
		// plan_id would mean reaching into its generic StructuredData
		// payload; counts are simpler and just as conclusive here since
		// every plan_id's outcome is already known).
		wantDestCount, wantDLQCount := plan.expectedCounts()
		if len(dlqDest.written) != wantDLQCount {
			t.Fatalf("iter %d (seed %d): DLQ received %d records, want %d (outcomes=%v)",
				iter, seed, len(dlqDest.written), wantDLQCount, outcome)
		}
		if len(dest.written) != wantDestCount {
			t.Fatalf("iter %d (seed %d): destination received %d records, want %d (outcomes=%v)",
				iter, seed, len(dest.written), wantDestCount, outcome)
		}
	}
}

// --- Benchmark: O(n), not O(n^2) ---

// BenchmarkRunLedger_VoteLinear resolves a single N-member split run through
// runAckNacker one member at a time (the worst case for call count) and
// reports ns/op per N, to demonstrate the ledger's cost is linear in the
// number of run members - not quadratic like the per-write escalation design
// rejected in #2725 (which measured 10.5us -> 439ms, ~42000x, for a 4x
// increase in run size).
func BenchmarkRunLedger_VoteLinear(b *testing.B) {
	for _, n := range []int{1000, 4000, 16000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				batch := NewBatch(randomRecords(1))
				pieces := make([]opencdc.Record, n)
				for k := range pieces {
					pieces[k] = batch.records[0]
				}
				batch.SplitRecord(0, pieces)
				parent := &fakeParentAckNacker{}
				r := newRunAckNacker(parent)
				ctx := context.Background()
				b.StartTimer()

				for k := 0; k < n; k++ {
					if err := r.Ack(ctx, batch.sub(k, k+1)); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

// TestRunLedger_FanOut_RunCutBeforeFanOut_FailsLoud is the regression test for
// the CRITICAL defect adversarial review found in the first version of this
// ledger.
//
// The shape: a processor BEFORE the fan-out splits a record, and a later
// pre-fan-out processor resolves only part of that split run (returning fewer
// records than it received marks the remainder Retry). doTask then partitions
// the batch by flag, so the sub-batch that reaches doNextTask holds only SOME
// of the run's members.
//
// Batch.clone must give each branch its own ledger (branches diverge, and a
// shared tally would race and bleed decisions across branches), but a clone
// carries splitRun.total — the whole-run member count. So the branch needs
// `total` members to complete while only ever seeing k < total of them, and the
// parent side only ever sees the other total-k. Two disjoint tallies, neither
// able to complete, and no join point between them.
//
// The consequence was strictly worse than not supporting the shape: the run's
// original source position was withheld FOREVER while every later position
// acked normally. Source.Ack sets State.Position from the last position it is
// handed, so the withheld record was silently skipped on restart — no error, no
// DLQ entry, no replay. It also disarmed the two backstops that caught this
// shape before the ledger existed (validateAckPositions and
// newMultiAckNacker's empty-position check), because the nil-position tail was
// intercepted and withheld before ever reaching them.
//
// Now it fails loud with CodeSplitRunStraddlesFanOut. Nothing is acked, so the
// records are redelivered on restart (invariant 3).
func TestRunLedger_FanOut_RunCutBeforeFanOut_FailsLoud(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(1)
	logger := log.Nop()
	destA := newFakeDestination("destA")
	destB := newFakeDestination("destB")

	// split3 (pre-fan-out) → deferSecond (pre-fan-out, resolves only part of
	// the run) → fan-out to two destinations.
	splitNode := &TaskNode{Task: &split3Task{id: "split3"}}
	cutNode := &TaskNode{Task: &deferSecondTask{id: "cut"}}
	destANode := &TaskNode{Task: NewDestinationTask(destA.id, destA, logger, NoOpConnectorMetrics{})}
	destBNode := &TaskNode{Task: NewDestinationTask(destB.id, destB, logger, NoOpConnectorMetrics{})}
	splitNode.Next = []*TaskNode{cutNode}
	cutNode.Next = []*TaskNode{destANode, destBNode}

	parent := &fakeParentAckNacker{}
	w := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}

	batch := NewBatch(slices.Clone(records))
	err := w.doTask(ctx, splitNode, batch, newRunAckNacker(parent))

	// Must fail loud, not silently withhold.
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeSplitRunStraddlesFanOut.Reason())

	// And crucially: nothing was acked to the source. The records are
	// redelivered on restart rather than skipped.
	is.Equal(len(parent.ackCalls()), 0)
}

// --- #2729: destination nack index shift / wrong DLQ error attribution ---

// splitFilterOnceTask models the upstream processor state in #2729's
// reproduction: on its first call it splits the first active record into 3
// pieces (head, middle, tail) and immediately filters the middle piece. This
// makes the batch it hands downstream exactly the "split run [Ack, Filter,
// Ack] (untainted, so never normalized)" shape the issue describes - Filter
// alone never taints a batch, so this reaches DestinationTask.Do without ever
// going through doTask's tainted-splitting loop first.
type splitFilterOnceTask struct {
	id        string
	triggered bool
}

func (t *splitFilterOnceTask) ID() string                  { return t.id }
func (t *splitFilterOnceTask) Open(context.Context) error  { return nil }
func (t *splitFilterOnceTask) Close(context.Context) error { return nil }
func (t *splitFilterOnceTask) Do(_ context.Context, b *Batch) error {
	if t.triggered {
		return nil
	}
	t.triggered = true
	active := b.ActiveRecords()
	orig := active[0]

	// Give each piece a distinct Record.Position so the fake destination
	// (which keys writes/nacks by the record's OWN Position field, exactly
	// like a real destination plugin would) can independently target one
	// piece - Batch's own, separately-tracked original position (b.positions)
	// is untouched by this and stays "p0" for the head, nil for the tail
	// pieces, per SplitRecord's contract.
	head := orig.Clone()
	head.Position = opencdc.Position("piece0")
	mid := orig.Clone()
	mid.Position = opencdc.Position("piece1")
	tail := orig.Clone()
	tail.Position = opencdc.Position("piece2")

	b.SplitRecord(0, []opencdc.Record{head, mid, tail})
	b.Filter(1) // filter the middle piece - Filter alone does not taint.
	return nil
}

// TestDestinationTask_Nack_SplitRunWithFilterSibling_CorrectAttribution is
// the direct end-to-end regression test for #2729, reproducing the exact
// shape from the issue: a split run [Ack, Filter, Ack] (untainted) plus two
// ordinary records p1, p2. The destination fails the split run's head
// (piece0) and the unrelated ordinary record p1.
//
// Before the fix: DestinationTask.markBatchRecords nacked piece0 forward
// first, and Batch.Nack's propagation-across-a-split-run logic (removed by
// this fix) re-included the Filter'd middle piece in the "active" set,
// shifting every later index in this same pass - the SECOND Nack call (meant
// for p1) landed back on a split piece instead, attaching p1's error to the
// run's DLQ entry while p1 itself kept its default Ack status and was
// silently acked to the source as a success. See the issue for the exact
// (wrong) observed trace.
//
// After the fix: p1 is correctly Nack'd and DLQ'd with its OWN error, and the
// split run's DLQ entry carries the head's OWN error - never p1's.
func TestDestinationTask_Nack_SplitRunWithFilterSibling_CorrectAttribution(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	p0 := opencdc.Record{Position: opencdc.Position("p0"), Operation: opencdc.OperationCreate, Metadata: opencdc.Metadata{}, Payload: opencdc.Change{After: opencdc.RawData("v0")}}
	p1 := opencdc.Record{Position: opencdc.Position("p1"), Operation: opencdc.OperationCreate, Metadata: opencdc.Metadata{}, Payload: opencdc.Change{After: opencdc.RawData("v1")}}
	p2 := opencdc.Record{Position: opencdc.Position("p2"), Operation: opencdc.OperationCreate, Metadata: opencdc.Metadata{}, Payload: opencdc.Change{After: opencdc.RawData("v2")}}
	records := []opencdc.Record{p0, p1, p2}

	src := newFakeSource("src", append([]opencdc.Record(nil), records...))
	dest := newFakeDestination("dest")
	dlqDest := newFakeDestination("dlq")
	dlq := NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, 0, 0)

	errHead := cerrors.New("dest failed piece0")
	errP1 := cerrors.New("dest failed p1")
	dest.setNackErr(opencdc.Position("piece0"), errHead)
	dest.setNackErr(opencdc.Position("p1"), errP1)

	splitFilterNode := &TaskNode{Task: &splitFilterOnceTask{id: "split-filter"}}
	destNode := &TaskNode{Task: NewDestinationTask(dest.id, dest, logger, NoOpConnectorMetrics{})}
	srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{splitFilterNode}}
	splitFilterNode.Next = []*TaskNode{destNode}

	w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
	is.NoErr(err)

	batch := NewBatch(append([]opencdc.Record(nil), records...))
	is.NoErr(w.doTask(ctx, splitFilterNode, batch, newRunAckNacker(w)))

	// The destination saw all 4 non-filtered pieces (head, tail, p1, p2).
	is.Equal(len(dest.written), 4)

	// Exactly 2 DLQ entries: the split run (once, collapsed to its original
	// position) and p1 - never both AND acked, never neither.
	is.Equal(len(dlqDest.written), 2)

	byPos := make(map[string]opencdc.Record, len(dlqDest.written))
	for _, r := range dlqDest.written {
		byPos[string(r.Position)] = r
	}

	gotHeadEntry, ok := byPos["p0"]
	is.True(ok)
	headErr, err := gotHeadEntry.Metadata.GetConduitDLQNackError()
	is.NoErr(err)
	is.Equal(headErr, errHead.Error()) // NOT errP1 - the bug's misattribution

	gotP1Entry, ok := byPos["p1"]
	is.True(ok)
	p1Err, err := gotP1Entry.Metadata.GetConduitDLQNackError()
	is.NoErr(err)
	is.Equal(p1Err, errP1.Error())

	// Every original position reaches the source exactly once - Worker.Nack
	// acks a position too, once its DLQ write succeeds (that IS how a nacked
	// record is "durably handled"), so p0 and p1 are expected here as well as
	// p2. The bug was never about p1 failing to reach the source at all - in
	// the buggy trace it was ALSO acked once, just via the wrong (direct,
	// non-DLQ) path, having silently kept its default Ack status instead of
	// being nacked. The discriminator is len(dlqDest.written) and the
	// per-entry error above, not source-ack presence.
	acked := src.ackedPositions()
	ackedSet := make(map[string]int, len(acked))
	for _, p := range acked {
		ackedSet[string(p)]++
	}
	is.Equal(ackedSet["p0"], 1)
	is.Equal(ackedSet["p1"], 1)
	is.Equal(ackedSet["p2"], 1)
	is.Equal(len(acked), 3) // no extras, no double-acks
}

// TestRunLedger_AccountingConsistent_ThroughMixedMarkingPass directly asserts
// that filterCount, splitRun.total and splitRun.terminalCount all stay
// mutually consistent through the exact mixed marking pass #2729 exercises: a
// 3-piece split run (head/filter/tail) plus an unrelated ordinary record,
// nacked in the same end->start order DestinationTask.markBatchRecords now
// uses.

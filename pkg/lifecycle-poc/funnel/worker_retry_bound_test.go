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
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// This file is the regression suite for #2726: Worker.doTask's
// RecordFlagRetry branch used to recurse into itself directly with no bound,
// so a processor that never converges (returns the same short result forever)
// drove unbounded stack growth to a fatal, unrecoverable "goroutine stack
// exceeds limit" crash - killing the ENTIRE process, not just the offending
// pipeline. See doTaskAttempt/retryAttempt/maxRetryAttempts in worker.go for
// the fix: a bounded, count-comparing retry chain that fails loud with
// CodeRetryNotConverging instead of recursing forever.

// alwaysRetryTask marks its entire active input as RecordFlagRetry on every
// call, resolving (ack/nack/filter) nothing, ever - the textbook
// non-converging processor from #2726: a deterministic function of its input
// that returns nothing useful, fed the identical input every time.
type alwaysRetryTask struct {
	id    string
	calls int
}

func (t *alwaysRetryTask) ID() string                  { return t.id }
func (t *alwaysRetryTask) Open(context.Context) error  { return nil }
func (t *alwaysRetryTask) Close(context.Context) error { return nil }
func (t *alwaysRetryTask) Do(_ context.Context, b *Batch) error {
	t.calls++
	active := b.ActiveRecords()
	b.Retry(0, len(active))
	return nil
}

// TestDoTask_Retry_NonConverging_FailsWithCodedError is the core regression
// test for #2726. Without the fix, this either recurses until the goroutine's
// stack hits Go's 1GB limit (a fatal, unrecoverable crash - see the PR
// description for a verified repro) or, at best, spins forever. With the fix,
// it must fail fast with a registered, actionable error naming the offending
// task, after a small, bounded number of attempts.
func TestDoTask_Retry_NonConverging_FailsWithCodedError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	task := &alwaysRetryTask{id: "stuck-task"}
	node := &TaskNode{Task: task}
	parent := &fakeParentAckNacker{}
	w := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}

	batch := randomBatch(4)
	err := w.doTask(ctx, node, batch, newRunAckNacker(parent))

	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok) // must be a registered, machine-actionable ConduitError
	is.Equal(ce.Code.Reason(), CodeRetryNotConverging.Reason())
	is.True(strings.Contains(err.Error(), "stuck-task")) // names the offending task

	// Bounded: the initial pass, plus maxRetryStall (3) consecutive retry
	// attempts that each observe zero shrinkage - never anywhere close to
	// unbounded recursion.
	is.Equal(task.calls, 1+maxRetryStall)

	// Invariant 1/3 (enforcement site): nothing in the un-converged batch was
	// ever acked or nacked to the source. Every record is redelivered on
	// restart, not silently dropped.
	is.Equal(len(parent.ackCalls()), 0)
	is.Equal(len(parent.nackCalls()), 0)
}

// shrinkByCapTask always resolves (acks) up to `cap` records per call and
// retries the remainder - deterministically converges in ceil(n/cap) rounds.
// This mirrors TestRunLedger_OutputCapProcessor_ConvergesAndAcksOnce but
// without a split run, isolating the bounded-retry logic itself from
// run-ledger accounting.
type shrinkByCapTask struct {
	id    string
	cap   int
	calls int
}

func (t *shrinkByCapTask) ID() string                  { return t.id }
func (t *shrinkByCapTask) Open(context.Context) error  { return nil }
func (t *shrinkByCapTask) Close(context.Context) error { return nil }
func (t *shrinkByCapTask) Do(_ context.Context, b *Batch) error {
	t.calls++
	active := b.ActiveRecords()
	if len(active) > t.cap {
		b.Retry(t.cap, len(active))
	}
	return nil
}

// TestDoTask_Retry_Converging_CompletesNormally guards against the false-
// positive risk the STOP-and-surface rule calls out: a legitimately
// converging retry (each pass strictly shrinks, e.g. an output-capped
// processor) must complete exactly as it does on main today - same number of
// rounds, every record eventually acked exactly once, no error.
func TestDoTask_Retry_Converging_CompletesNormally(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	task := &shrinkByCapTask{id: "cap-task", cap: 2}
	node := &TaskNode{Task: task}
	parent := &fakeParentAckNacker{}
	w := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}

	batch := randomBatch(5)
	wantPositions := append([]opencdc.Position(nil), batch.positions...)

	err := w.doTask(ctx, node, batch, newRunAckNacker(parent))
	is.NoErr(err)

	// 5 records, cap 2 per round: 5->(2 ack,3 retry), 3->(2 ack,1 retry),
	// 1->(1 ack) = 3 calls. Identical to what main's unmodified
	// Retry/subBatchByFlag logic already produces - the fix changes nothing
	// about how a genuinely converging retry behaves.
	is.Equal(task.calls, 3)

	var gotPositions []opencdc.Position
	for _, call := range parent.ackCalls() {
		gotPositions = append(gotPositions, call.positions...)
	}
	is.Equal(gotPositions, wantPositions) // every record acked, exactly once, in order
	is.Equal(len(parent.nackCalls()), 0)
}

// recoveringRateLimiterTask retries its ENTIRE input, unresolved, for a fixed
// number of calls, then fully resolves everything on the next call - modeling
// a real, wall-clock-based rate limiter whose token bucket refills after a
// couple of rounds regardless of record content. This is the motivating
// shape for maxRetryStall (see its doc comment on worker.go): a lack of
// progress here does not depend on the records at all, so a small number of
// stalled rounds must not be conflated with the input-deterministic fixpoint
// #2726 is actually about.
type recoveringRateLimiterTask struct {
	id          string
	stallRounds int
	calls       int
}

func (t *recoveringRateLimiterTask) ID() string                  { return t.id }
func (t *recoveringRateLimiterTask) Open(context.Context) error  { return nil }
func (t *recoveringRateLimiterTask) Close(context.Context) error { return nil }
func (t *recoveringRateLimiterTask) Do(_ context.Context, b *Batch) error {
	t.calls++
	if t.calls <= t.stallRounds {
		b.Retry(0, len(b.ActiveRecords()))
		return nil
	}
	return nil // recovered: resolve everything (default RecordFlagAck)
}

// TestDoTask_Retry_TemporaryStall_RecoversAndCompletesNormally is the direct
// test for maxRetryStall's own justification: a processor that stalls for
// FEWER than maxRetryStall consecutive rounds and then fully recovers must
// complete normally, with no error - proving the tolerance added on top of
// the (exact, per-round) count comparison actually prevents the false
// positive it was added for, not just that the comment claims it does.
func TestDoTask_Retry_TemporaryStall_RecoversAndCompletesNormally(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	is.True(maxRetryStall > 2) // precondition for stallRounds=2 to stay under the tolerance

	task := &recoveringRateLimiterTask{id: "rate-limiter-task", stallRounds: 2}
	node := &TaskNode{Task: task}
	parent := &fakeParentAckNacker{}
	w := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}

	batch := randomBatch(3)
	wantPositions := append([]opencdc.Position(nil), batch.positions...)

	err := w.doTask(ctx, node, batch, newRunAckNacker(parent))
	is.NoErr(err) // must NOT be treated as non-converging

	is.Equal(task.calls, 3) // 2 stalled rounds + the recovering round

	var gotPositions []opencdc.Position
	for _, call := range parent.ackCalls() {
		gotPositions = append(gotPositions, call.positions...)
	}
	is.Equal(gotPositions, wantPositions)
	is.Equal(len(parent.nackCalls()), 0)
}

// splitThenStuckTask splits its one input record into two identical pieces on
// the first call (head keeps the original position, tail is nil-positioned -
// see Batch.SplitRecord), retries the tail, and then - on every subsequent
// call - retries that tail's entire input forever, never resolving it. This
// drives the run-ledger interaction: the head's Ack is withheld by
// runAckNacker pending the tail, and the tail never converges.
type splitThenStuckTask struct {
	id        string
	splitDone bool
	calls     int
}

func (t *splitThenStuckTask) ID() string                  { return t.id }
func (t *splitThenStuckTask) Open(context.Context) error  { return nil }
func (t *splitThenStuckTask) Close(context.Context) error { return nil }
func (t *splitThenStuckTask) Do(_ context.Context, b *Batch) error {
	t.calls++
	if !t.splitDone {
		t.splitDone = true
		active := b.ActiveRecords()
		orig := active[0]
		b.SplitRecord(0, []opencdc.Record{orig, orig})
		b.Retry(1, 2) // tail (index 1) retries; head (index 0) stays Ack
		return nil
	}
	// The tail's retry pass: never resolves, regardless of round.
	active := b.ActiveRecords()
	b.Retry(0, len(active))
	return nil
}

// TestDoTask_Retry_SplitRun_NonConverging_NoEarlyReleaseOrWithhold guards the
// interaction called out explicitly in the task: a non-converging retry must
// not corrupt the run-completion ledger (run_ledger.go) that #2723/#2731
// added. Specifically:
//   - the head must NOT be released early just because it individually voted
//     Ack (that would advance the source position past the still-live tail -
//     invariant 1);
//   - once the tail fails to converge, NOTHING for this run may reach the
//     source acker - the whole record (head + tail) is redelivered on restart
//     (invariant 3), not partially acked.
//
// Without correct withholding, a regression here would look like the head's
// position reaching the source before the pipeline halts - exactly the data
// loss #2723 fixed, reopened via a different path.
func TestDoTask_Retry_SplitRun_NonConverging_NoEarlyReleaseOrWithhold(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	task := &splitThenStuckTask{id: "split-stuck-task"}
	node := &TaskNode{Task: task}
	parent := &fakeParentAckNacker{}
	w := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}

	batch := randomBatch(1)
	err := w.doTask(ctx, node, batch, newRunAckNacker(parent))

	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeRetryNotConverging.Reason())

	// Split (1 call) + maxRetryStall (3) consecutive failed retry attempts on
	// the tail, each observing zero shrinkage.
	is.Equal(task.calls, 1+maxRetryStall)

	// Invariant 1: the head's Ack vote was withheld by runAckNacker pending
	// the tail (which never resolved) - it must never have reached the
	// parent, early or otherwise.
	is.Equal(len(parent.ackCalls()), 0)
	is.Equal(len(parent.nackCalls()), 0)
}

// shrinkByOneForeverTask acks exactly one record and retries the rest, every
// round - a genuinely, monotonically converging processor (it always makes
// progress by the exact definition doTaskAttempt's progress check uses) that
// nonetheless needs as many rounds as records in the batch. Used to drive
// Worker.doTask's iteration cap (maxRetryAttempts) on its own, independent of
// the progress check, by lowering the cap for the duration of the test rather
// than constructing a batch of thousands of records.
type shrinkByOneForeverTask struct {
	id    string
	calls int
}

func (t *shrinkByOneForeverTask) ID() string                  { return t.id }
func (t *shrinkByOneForeverTask) Open(context.Context) error  { return nil }
func (t *shrinkByOneForeverTask) Close(context.Context) error { return nil }
func (t *shrinkByOneForeverTask) Do(_ context.Context, b *Batch) error {
	t.calls++
	active := b.ActiveRecords()
	if len(active) <= 1 {
		return nil // would fully resolve - not reached in this test
	}
	b.Retry(1, len(active))
	return nil
}

// TestDoTask_Retry_IterationCap_Reached proves the iteration cap
// (maxRetryAttempts) is itself reachable and produces the same coded error -
// the backstop the task description asks for, distinct from (and a superset
// of) the progress check: this task makes real, monotonic progress every
// single round, so the progress check alone would happily let it run for all
// 10 rounds it needs. Lowering maxRetryAttempts to 3 forces the cap, not the
// progress check, to be what stops it.
func TestDoTask_Retry_IterationCap_Reached(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	oldCap := maxRetryAttempts
	maxRetryAttempts = 3
	defer func() { maxRetryAttempts = oldCap }()

	task := &shrinkByOneForeverTask{id: "slow-shrink-task"}
	node := &TaskNode{Task: task}
	parent := &fakeParentAckNacker{}
	w := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}

	batch := randomBatch(10)
	wantAckedPrefix := append([]opencdc.Position(nil), batch.positions[:4]...)
	wantStuck := append([]opencdc.Position(nil), batch.positions[4:]...)

	err := w.doTask(ctx, node, batch, newRunAckNacker(parent))

	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeRetryNotConverging.Reason())
	is.True(strings.Contains(err.Error(), "maximum of 3 attempts"))

	// One initial call, then 3 rounds of genuine shrinkage before the
	// (lowered) cap trips on the 4th recursion - never anywhere close to
	// unbounded.
	is.Equal(task.calls, 4)

	var gotAcked []opencdc.Position
	for _, call := range parent.ackCalls() {
		gotAcked = append(gotAcked, call.positions...)
	}
	// The 4 records legitimately resolved (one per round, before the cap
	// tripped) are acked exactly as they would be on main - the cap does not
	// retroactively un-ack legitimate progress.
	is.Equal(gotAcked, wantAckedPrefix)
	// The records still stuck in the retry group at the moment the cap
	// tripped must never be acked (invariant 3: redelivered on restart).
	for _, stuck := range wantStuck {
		for _, got := range gotAcked {
			is.True(string(stuck) != string(got))
		}
	}
	is.Equal(len(parent.nackCalls()), 0)
}

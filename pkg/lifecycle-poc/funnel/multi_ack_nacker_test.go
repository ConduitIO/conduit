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
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
)

// recordedAck/recordedNack capture exactly what a fakeParentAckNacker
// observed, in call order, so tests can assert both WHAT was released to the
// parent and in WHAT ORDER (invariant 4 - in-source-order release).
type recordedAck struct {
	positions []opencdc.Position
}

type recordedNack struct {
	positions []opencdc.Position
	taskID    string
}

// fakeParentAckNacker stands in for the Worker (or an outer multiAckNacker)
// that multiAckNacker.releaseLocked calls into. It's a plain recorder by
// default; ackErr/nackErr (or the *Func overrides, for per-call control) let
// a test simulate the parent failing - in particular a fatal DLQ-threshold
// error, which multiAckNacker must propagate rather than swallow
// (requirement 5 in worker.go's multiAckNacker doc comment).
type fakeParentAckNacker struct {
	mu    sync.Mutex
	acks  []recordedAck
	nacks []recordedNack

	ackErr  error
	nackErr error
}

func (p *fakeParentAckNacker) Ack(_ context.Context, b *Batch) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.acks = append(p.acks, recordedAck{positions: append([]opencdc.Position(nil), b.positions...)})
	return p.ackErr
}

func (p *fakeParentAckNacker) Nack(_ context.Context, b *Batch, taskID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nacks = append(p.nacks, recordedNack{positions: append([]opencdc.Position(nil), b.positions...), taskID: taskID})
	return p.nackErr
}

func (p *fakeParentAckNacker) ackCalls() []recordedAck {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recordedAck(nil), p.acks...)
}

func (p *fakeParentAckNacker) nackCalls() []recordedNack {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recordedNack(nil), p.nacks...)
}

// ackBatchFor builds a *Batch of RecordFlagAck records for positions,
// suitable for passing directly to multiAckNacker.Ack - mirrors what a
// branch's doTask would hand its acker at the "clean batch" shortcut
// (worker.go's doTask, !b.tainted case).
func ackBatchFor(records []opencdc.Record, positions ...opencdc.Position) *Batch {
	recs := make([]opencdc.Record, len(positions))
	for i, p := range positions {
		recs[i] = findRecordByPosition(records, p)
	}
	return &Batch{
		records:        recs,
		recordStatuses: make([]RecordStatus, len(positions)), // zero value is RecordFlagAck
		positions:      append([]opencdc.Position(nil), positions...),
	}
}

// nackBatchFor builds a single-record *Batch flagged RecordFlagNack, as a
// branch's doTask would hand its acker for a nacked sub-batch.
func nackBatchFor(records []opencdc.Record, pos opencdc.Position, nackErr error) *Batch {
	return &Batch{
		records:        []opencdc.Record{findRecordByPosition(records, pos)},
		recordStatuses: []RecordStatus{{Flag: RecordFlagNack, Error: nackErr}},
		positions:      []opencdc.Position{pos},
		tainted:        true,
	}
}

func findRecordByPosition(records []opencdc.Record, pos opencdc.Position) opencdc.Record {
	for _, r := range records {
		if string(r.Position) == string(pos) {
			return r
		}
	}
	panic("findRecordByPosition: no record with position " + string(pos))
}

// TestMultiAckNacker_UnanimousAck_ReleasesOnce is the core happy-path
// invariant-1 check: with M=2 branches, a position is released to the
// parent's Ack exactly once ack votes from BOTH branches have arrived, in
// one coalesced call covering the whole contiguous run.
func TestMultiAckNacker_UnanimousAck_ReleasesOnce(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	records := randomRecords(3)
	positions := []opencdc.Position{records[0].Position, records[1].Position, records[2].Position}

	parent := &fakeParentAckNacker{}
	m := newMultiAckNacker(parent, 2, positions)

	// Branch 1 acks all three - not yet unanimous, nothing released.
	is.NoErr(m.Ack(ctx, ackBatchFor(records, positions...)))
	is.Equal(len(parent.ackCalls()), 0)
	is.Equal(len(parent.nackCalls()), 0)

	// Branch 2 acks all three too - now unanimous for every position, and
	// they're released as ONE coalesced call (invariant 4's batching).
	is.NoErr(m.Ack(ctx, ackBatchFor(records, positions...)))

	acks := parent.ackCalls()
	is.Equal(len(acks), 1)
	is.Equal(acks[0].positions, positions)
	is.Equal(len(parent.nackCalls()), 0)
}

// TestMultiAckNacker_AnyNack_DLQsOnceRegardlessOfOrder is the nack-wins
// check (invariant 3): for a single position with M=2 branches, however the
// ack and nack votes are interleaved, the position is routed to the parent's
// DLQ (Nack) exactly once, and NEVER also acked.
func TestMultiAckNacker_AnyNack_DLQsOnceRegardlessOfOrder(t *testing.T) {
	nackErr := cerrors.New("dest B failed to write")

	t.Run("ack then nack", func(t *testing.T) {
		is := is.New(t)
		ctx := context.Background()
		records := randomRecords(1)
		positions := []opencdc.Position{records[0].Position}

		parent := &fakeParentAckNacker{}
		m := newMultiAckNacker(parent, 2, positions)

		is.NoErr(m.Ack(ctx, ackBatchFor(records, positions...)))                          // branch A acks
		is.NoErr(m.Nack(ctx, nackBatchFor(records, positions[0], nackErr), "destB-task")) // branch B nacks

		is.Equal(len(parent.ackCalls()), 0) // never acked - nack won
		nacks := parent.nackCalls()
		is.Equal(len(nacks), 1)
		is.Equal(nacks[0].positions, positions)
		is.Equal(nacks[0].taskID, "destB-task")
	})

	t.Run("nack then ack (late ack vote is a no-op)", func(t *testing.T) {
		is := is.New(t)
		ctx := context.Background()
		records := randomRecords(1)
		positions := []opencdc.Position{records[0].Position}

		parent := &fakeParentAckNacker{}
		m := newMultiAckNacker(parent, 2, positions)

		is.NoErr(m.Nack(ctx, nackBatchFor(records, positions[0], nackErr), "destB-task")) // branch B nacks first
		is.NoErr(m.Ack(ctx, ackBatchFor(records, positions...)))                          // branch A's ack arrives late

		is.Equal(len(parent.ackCalls()), 0) // still never acked
		nacks := parent.nackCalls()
		is.Equal(len(nacks), 1) // still exactly once, not twice
		is.Equal(nacks[0].positions, positions)
	})
}

// TestMultiAckNacker_PartialPerRecordDivergence is the central multi-record
// scenario the whole type exists for: destination A acks everything, but
// destination B nacks ONE record in the middle of the batch. The nacked
// record must be DLQ'd (and, since the DLQ write is itself the durable
// handling, acked to the source by the parent's own Nack implementation -
// tested separately at the Worker level), while its neighbors are acked
// normally in their own coalesced runs. A per-BATCH counter could not
// represent this at all (see worker.go's multiAckNacker doc comment) - this
// test is the direct proof a per-position tally can.
func TestMultiAckNacker_PartialPerRecordDivergence(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(5)
	positions := make([]opencdc.Position, 5)
	for i, r := range records {
		positions[i] = r.Position
	}

	parent := &fakeParentAckNacker{}
	m := newMultiAckNacker(parent, 2, positions)

	// Destination A: clean batch, acks all 5 in one call (worker.go's
	// !b.tainted shortcut path).
	is.NoErr(m.Ack(ctx, ackBatchFor(records, positions...)))

	// Destination B: records[2] failed, so its batch is tainted and split
	// into three sub-batches by doTask's subBatchByFlag - ack(0,1), nack(2),
	// ack(3,4).
	nackErr := cerrors.New("dest B: write failed for record 2")
	is.NoErr(m.Ack(ctx, ackBatchFor(records, positions[0], positions[1])))
	is.NoErr(m.Nack(ctx, nackBatchFor(records, positions[2], nackErr), "destB-task"))
	is.NoErr(m.Ack(ctx, ackBatchFor(records, positions[3], positions[4])))

	acks := parent.ackCalls()
	is.Equal(len(acks), 2)
	is.Equal(acks[0].positions, positions[0:2])
	is.Equal(acks[1].positions, positions[3:5])

	nacks := parent.nackCalls()
	is.Equal(len(nacks), 1)
	is.Equal(nacks[0].positions, positions[2:3])
	is.Equal(nacks[0].taskID, "destB-task")
}

// TestMultiAckNacker_OutOfOrderCompletion_ReleasesInSourceOrder is
// invariant 4's direct test: destB votes for a LATER position before an
// EARLIER position has even received its first vote. The later position must
// never be released to the parent until every position before it (in source
// order) has also reached a terminal decision - otherwise Source.Ack's
// monotonically-advancing position would skip over a record still in
// flight.
func TestMultiAckNacker_OutOfOrderCompletion_ReleasesInSourceOrder(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(3)
	p0, p1, p2 := records[0].Position, records[1].Position, records[2].Position

	parent := &fakeParentAckNacker{}
	m := newMultiAckNacker(parent, 2, []opencdc.Position{p0, p1, p2})

	// Branch B races ahead and votes for p2 FIRST - only 1 of 2 votes, so it
	// cannot go terminal yet regardless.
	is.NoErr(m.Ack(ctx, ackBatchFor(records, p2)))
	is.Equal(len(parent.ackCalls()), 0)

	// Branch A votes for p0 - still only 1 of 2 votes for p0.
	is.NoErr(m.Ack(ctx, ackBatchFor(records, p0)))
	is.Equal(len(parent.ackCalls()), 0)

	// Branch B now votes for p0 too - p0 is unanimous and releases...
	is.NoErr(m.Ack(ctx, ackBatchFor(records, p0)))
	acks := parent.ackCalls()
	is.Equal(len(acks), 1)
	is.Equal(acks[0].positions, []opencdc.Position{p0})

	// ...but p1 has zero votes and p2 (despite already being fully voted on
	// branch B's side) still needs branch A's vote, and even once it gets
	// it, p2 must NOT release until p1 does too.
	is.NoErr(m.Ack(ctx, ackBatchFor(records, p1, p2))) // branch A votes p1 and p2
	// p1 only just got its first (of two) votes here - still not terminal -
	// so nothing new can release yet even though p2's tally is now 2/2.
	is.Equal(len(parent.ackCalls()), 1) // unchanged

	// Branch B finally votes p1 - NOW both p1 and p2 are terminal, and they
	// release together as one contiguous run (never p2 without p1 first).
	is.NoErr(m.Ack(ctx, ackBatchFor(records, p1)))
	acks = parent.ackCalls()
	is.Equal(len(acks), 2)
	is.Equal(acks[1].positions, []opencdc.Position{p1, p2})
}

// TestMultiAckNacker_FatalDLQError_Propagates is requirement 5's direct
// test: a fatal error from the parent's Nack (e.g. DLQ nack-threshold
// exceeded, see funnel.DLQ.Nack) must be returned from multiAckNacker's own
// Nack call, so the branch pool (doNextTask's pool.New().WithErrors()) fails
// and the worker tombs with it - identical to the single-destination path.
func TestMultiAckNacker_FatalDLQError_Propagates(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	records := randomRecords(1)
	pos := records[0].Position

	fatalErr := cerrors.FatalError(cerrors.New("DLQ nack threshold exceeded"))
	parent := &fakeParentAckNacker{nackErr: fatalErr}
	m := newMultiAckNacker(parent, 2, []opencdc.Position{pos})

	err := m.Nack(ctx, nackBatchFor(records, pos, cerrors.New("write failed")), "destA-task")
	is.True(err != nil)
	is.True(cerrors.Is(err, fatalErr))
	is.True(cerrors.IsFatalError(err))

	// Exactly one Nack call was attempted (no retry loop inside
	// multiAckNacker - the caller/pool is responsible for propagating this
	// up, just like Worker.Nack's own fatal-error path).
	is.Equal(len(parent.nackCalls()), 1)
}

// TestMultiAckNacker_UnknownPosition_ReturnsBugError guards against a task
// graph bug (a branch producing a record whose position doesn't trace back,
// via originalBatch, to one of the positions captured when the fan-out
// started) being silently swallowed - it must surface as a reportable error,
// not a panic or a silent drop.
func TestMultiAckNacker_UnknownPosition_ReturnsBugError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	records := randomRecords(2)
	known := records[0].Position

	parent := &fakeParentAckNacker{}
	m := newMultiAckNacker(parent, 2, []opencdc.Position{known})

	err := m.Ack(ctx, ackBatchFor(records, records[1].Position)) // records[1].Position was never registered
	is.True(err != nil)
}

// TestMultiAckNacker_ThreeBranches_UnanimousRequiresAllThree confirms the
// unanimity gate scales past M=2: with 3 branches, 2 acks are not enough to
// release, and a 3rd branch's nack (even after 2 acks already arrived) still
// wins per invariant 3.
func TestMultiAckNacker_ThreeBranches_UnanimousRequiresAllThree(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	records := randomRecords(2)
	pAcked, pNacked := records[0].Position, records[1].Position

	parent := &fakeParentAckNacker{}
	m := newMultiAckNacker(parent, 3, []opencdc.Position{pAcked, pNacked})

	// pAcked: all 3 branches ack it.
	is.NoErr(m.Ack(ctx, ackBatchFor(records, pAcked)))
	is.NoErr(m.Ack(ctx, ackBatchFor(records, pAcked)))
	is.Equal(len(parent.ackCalls()), 0) // only 2/3 so far

	// pNacked: 2 branches ack, but the 3rd nacks - nack wins even though it
	// arrives last.
	is.NoErr(m.Ack(ctx, ackBatchFor(records, pNacked)))
	is.NoErr(m.Ack(ctx, ackBatchFor(records, pNacked)))
	is.NoErr(m.Nack(ctx, nackBatchFor(records, pNacked, cerrors.New("branch 3 failed")), "destC-task"))

	// pAcked's 3rd (final) vote arrives after pNacked was already resolved -
	// order across DIFFERENT positions doesn't matter, only per-position
	// unanimity does.
	is.NoErr(m.Ack(ctx, ackBatchFor(records, pAcked)))

	// Release order still respects source order: pAcked (index 0) before
	// pNacked (index 1).
	acks := parent.ackCalls()
	is.Equal(len(acks), 1)
	is.Equal(acks[0].positions, []opencdc.Position{pAcked})

	nacks := parent.nackCalls()
	is.Equal(len(nacks), 1)
	is.Equal(nacks[0].positions, []opencdc.Position{pNacked})
	is.Equal(nacks[0].taskID, "destC-task")
}

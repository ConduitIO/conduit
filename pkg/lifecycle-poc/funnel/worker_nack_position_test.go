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
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
)

// TestWorker_Nack_TailOnlySubBatch_RefusesToWipeSourcePosition covers the
// invariant-2 hole that Worker.Ack guards and Worker.Nack did not.
//
// Batch.SplitRecord gives every piece after the first a nil position, and
// Batch.sub deliberately drops splitRecords when its range contains none of the
// split ORIGINALS (batch.go: "if len(splitRecords) == 0 { splitRecords = nil }")
// — which is always true of a tail-only range, because a nil position stringifies
// to "" and never matches a key. originalBatch() only compacts nil positions when
// splitRecords is non-empty, so on such a sub-batch it returns the batch
// unchanged, nils and all.
//
// connector.Source.Ack then executes `State.Position = p[len(p)-1]`
// unconditionally (source.go), so acking that slice OVERWRITES the durable
// source position with nothing: on restart Postgres re-snapshots from scratch
// and file/Kafka resume at offset 0. That is invariant 2 (positions are
// monotonic and crash-safe) violated with a far worse blast radius than the
// records this nack was trying to DLQ.
//
// This test drives Worker.Nack directly, and an earlier version of it claimed
// the shape was reachable through doTask once the "defer the fan-out" run join
// lands. That claim was wrong and is withdrawn: doTask never calls Worker.Nack
// directly — it calls acker.Nack, and runAckNacker.vote credits every split-run
// member to its run rather than forwarding it, then forwards the completed run
// as a single record carrying run.origPos, which SplitRecord guarantees is
// non-nil. So the run ledger, not validateRunsWholeBeforeFanOut, is what keeps
// nils away from this path, and the join does not obviously change that.
//
// It is kept because Worker.Nack is exported within the package and its
// contract should hold for any caller, and because it pins the exact
// corruption: without the guard it fails with Ack([<nil> <nil>]). The route
// that is genuinely live today is covered by the test below, which is the one
// that makes this a bug fix rather than hardening.
func TestWorker_Nack_TailOnlySubBatch_RefusesToWipeSourcePosition(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	ctrl := gomock.NewController(t)

	sourceMock, _, taskNode := newMockTasks(ctrl, "sourceTask", "task1")
	dlq, dlqDestinationMock := NewMockDLQ(ctrl, logger)

	batch := randomBatch(3)
	// positions: [p0, p1, p2] -> split record 1 into 3 pieces ->
	// [p0, p1, nil, nil, p2]. Indices 2 and 3 are the split TAIL.
	batch.SplitRecord(1, randomRecords(3))

	tail := batch.sub(2, 4)

	// Precondition: this is the shape the bug needs. Assert it rather than
	// assume it — if Batch.sub ever starts carrying splitRecords through, this
	// test would silently stop covering anything.
	is.Equal(len(tail.splitRecords), 0)
	for _, p := range tail.originalBatch().positions {
		is.Equal(len(p), 0) // every position must be nil for this to be the tail-only case
	}

	nackErr := cerrors.New("destination rejected the record")
	tail.Nack(0, nackErr, nackErr)

	// The DLQ accepts both records, so Worker.Nack proceeds to ack them
	// upstream — the exact step that would corrupt the position.
	destinationExpectSuccessfulWrites(is, dlqDestinationMock, tail.records)

	// Source.Ack must never be reached. Without the guard it is called with
	// [nil, nil] and the source's durable position is wiped.
	sourceMock.EXPECT().Ack(gomock.Any(), gomock.Any()).Times(0)

	worker, err := NewWorker(taskNode, dlq, logger, noop.Timer{})
	is.NoErr(err)

	err = worker.Nack(ctx, tail, "taskID")
	is.True(err != nil) // must refuse rather than ack an empty position

	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeEmptySourcePosition.Reason())
	is.True(cerrors.IsFatalError(err))
}

// TestWorker_Nack_SourceEmittedEmptyPosition_RefusesAndIsFatal covers the route
// that is actually reachable today, with no split, no fan-out and no run ledger
// involvement: a source connector emits a record whose Position is empty, and
// that record is nacked.
//
// Nothing between the source and connector.Source.Ack rejects it — NewBatch
// copies r.Position verbatim with no validation, the record was never split so
// runAckNacker forwards it straight through rather than withholding it behind a
// run, and originalBatch() returns the batch unchanged because splitRecords is
// empty. Source.Ack then executes State.Position = p[len(p)-1] unconditionally,
// so the durable source position is overwritten with nothing and the source
// re-reads from scratch on restart. Worker.Ack has guarded its half of this for
// longer; the nack path was the remaining hole.
//
// The error must also be FATAL. Non-fatal would land in the recovery arm, whose
// default MaxRetries is infinite, and the condition is deterministic — the
// source would re-read the same record, nack it again, and write another DLQ
// copy on every iteration, without bound.
func TestWorker_Nack_SourceEmittedEmptyPosition_RefusesAndIsFatal(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	ctrl := gomock.NewController(t)

	sourceMock, _, taskNode := newMockTasks(ctrl, "sourceTask", "task1")
	dlq, dlqDestinationMock := NewMockDLQ(ctrl, logger)

	// Exactly what a misbehaving source connector hands us: a normal record
	// followed by one with no position at all. No processor is involved.
	batch := NewBatch([]opencdc.Record{
		{Position: opencdc.Position("p0")},
		{Position: nil},
	})
	is.Equal(len(batch.splitRecords), 0) // precondition: nothing was split

	nackErr := cerrors.New("destination rejected the record")
	batch.Nack(0, nackErr, nackErr)

	destinationExpectSuccessfulWrites(is, dlqDestinationMock, batch.records)

	// Without the guard this is called with ["p0", nil] and the nil, being
	// last, becomes State.Position.
	sourceMock.EXPECT().Ack(gomock.Any(), gomock.Any()).Times(0)

	worker, err := NewWorker(taskNode, dlq, logger, noop.Timer{})
	is.NoErr(err)

	err = worker.Nack(ctx, batch, "taskID")
	is.True(err != nil)

	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeEmptySourcePosition.Reason())
	is.True(cerrors.IsFatalError(err))
}

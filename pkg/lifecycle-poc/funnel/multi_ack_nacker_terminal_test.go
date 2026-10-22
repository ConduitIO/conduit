// Copyright © 2022 Meroxa, Inc.
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
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
)

// Both tests below were written because guardmut found that deleting the
// "already terminal" guards in multiAckNacker left every test green. The
// existing "nack then ack" test uses one vote per branch, where the mutant
// behaves the same. These put the position BEHIND a pending one, so it stays
// unreleased while the extra votes arrive, and the extra vote is observable.

// A duplicate ack vote from one branch must not turn a nacked position into an
// acked one (invariant 3: nack wins, a record is never partially acked). With
// two branches, branch A's second ack would otherwise reach the branch count
// and flip the position to acked before it is released.
func TestMultiAckNacker_DuplicateAckAfterNack_DoesNotFlipToAck(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	records := randomRecords(2)
	positions := []opencdc.Position{records[0].Position, records[1].Position}

	parent := &fakeParentAckNacker{}
	m := mustNewMultiAckNacker(t, parent, 2, positions)

	// Position 1 is nacked by branch B while position 0 is still pending, so it is not released yet.
	is.NoErr(m.Nack(ctx, nackBatchFor(records, positions[1], cerrors.New("dest B failed")), "destB"))
	// Branch A acks position 1, then (a bug in a branch) acks it again.
	is.NoErr(m.Ack(ctx, ackBatchFor(records, positions[1])))
	is.NoErr(m.Ack(ctx, ackBatchFor(records, positions[1])))
	// Position 0 completes and releases the prefix.
	is.NoErr(m.Ack(ctx, ackBatchFor(records, positions[0])))
	is.NoErr(m.Ack(ctx, ackBatchFor(records, positions[0])))

	nacks := parent.nackCalls()
	is.Equal(len(nacks), 1) // position 1 reached the DLQ path
	is.Equal(nacks[0].positions, []opencdc.Position{positions[1]})
	for _, a := range parent.ackCalls() {
		for _, p := range a.positions {
			is.True(string(p) != string(positions[1])) // never acked to the source
		}
	}
}

// The first nack for a position is terminal: a second nack from another branch
// must not replace the DLQ attribution (task id, error) of the first, and the
// position is DLQ'd once.
func TestMultiAckNacker_SecondNackForTerminalPosition_IsIgnored(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	records := randomRecords(2)
	positions := []opencdc.Position{records[0].Position, records[1].Position}

	parent := &fakeParentAckNacker{}
	m := mustNewMultiAckNacker(t, parent, 2, positions)

	is.NoErr(m.Nack(ctx, nackBatchFor(records, positions[1], cerrors.New("first")), "first-task"))
	is.NoErr(m.Nack(ctx, nackBatchFor(records, positions[1], cerrors.New("second")), "second-task"))
	is.NoErr(m.Ack(ctx, ackBatchFor(records, positions[0])))
	is.NoErr(m.Ack(ctx, ackBatchFor(records, positions[0])))

	nacks := parent.nackCalls()
	is.Equal(len(nacks), 1)
	is.Equal(nacks[0].taskID, "first-task") // the first nack's attribution stands
}

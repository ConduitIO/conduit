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
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
)

// The tests below guard multiAckNacker's refusal of a vote for a position that
// already has a vote from every branch. Before #3323 a nack was terminal on its
// own, so a sibling's later ack was the normal case and was ignored. Now a
// position is terminal only once all M branches have voted, so a further vote
// can only be a branch voting twice, and it is refused with a (bug) error. Each
// test puts the position BEHIND a pending one, so it stays unreleased while the
// extra vote arrives and a mutant that counts the vote is observable. The same
// refusal before every branch has voted is covered by
// TestMultiAckNacker_DoubleVoteBeforeAllVoted_IsRefused.

// TestMultiAckNacker_AckAfterAllBranchesVoted_IsRefused: an extra ack for a
// nacked position must be refused, and must not turn the position into an
// acked one (invariant 3: nack wins, a record is never partially acked).
func TestMultiAckNacker_AckAfterAllBranchesVoted_IsRefused(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	records := randomRecords(2)
	positions := []opencdc.Position{records[0].Position, records[1].Position}

	parent := &fakeParentAckNacker{}
	m := mustNewMultiAckNacker(t, parent, 2, positions)

	// Position 1: branch B nacks, branch A acks. Both have voted; position 0
	// is still pending, so nothing is released yet.
	is.NoErr(m.branch(1).Nack(ctx, nackBatchFor(records, positions[1], cerrors.New("dest B failed")), "destB"))
	is.NoErr(m.branch(0).Ack(ctx, ackBatchFor(records, positions[1])))
	// Branch A (a bug in a branch) acks position 1 again.
	err := m.branch(0).Ack(ctx, ackBatchFor(records, positions[1]))
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "(bug)"))
	is.Equal(len(parent.ackCalls()), 0)
	is.Equal(len(parent.nackCalls()), 0)

	// Position 0 completes and releases the prefix: position 1 is still a nack.
	is.NoErr(m.branch(0).Ack(ctx, ackBatchFor(records, positions[0])))
	is.NoErr(m.branch(1).Ack(ctx, ackBatchFor(records, positions[0])))

	nacks := parent.nackCalls()
	is.Equal(len(nacks), 1)
	is.Equal(nacks[0].positions, []opencdc.Position{positions[1]})
	for _, a := range parent.ackCalls() {
		for _, p := range a.positions {
			is.True(string(p) != string(positions[1])) // never acked to the source
		}
	}
}

// TestMultiAckNacker_NackAfterAllBranchesVoted_IsRefused: an extra nack for a
// position every branch acked must be refused and must not flip it to a nack.
// With the refusal deleted it would be routed to the DLQ instead of acked. A
// second nack from a different branch BEFORE every vote is in only counts as a
// vote: the first nack's attribution stands.
func TestMultiAckNacker_NackAfterAllBranchesVoted_IsRefused(t *testing.T) {
	t.Run("extra nack after unanimous ack", func(t *testing.T) {
		is := is.New(t)
		ctx := context.Background()
		records := randomRecords(2)
		positions := []opencdc.Position{records[0].Position, records[1].Position}

		parent := &fakeParentAckNacker{}
		m := mustNewMultiAckNacker(t, parent, 2, positions)

		is.NoErr(m.branch(0).Ack(ctx, ackBatchFor(records, positions[1])))
		is.NoErr(m.branch(1).Ack(ctx, ackBatchFor(records, positions[1])))
		err := m.branch(0).Nack(ctx, nackBatchFor(records, positions[1], cerrors.New("late")), "late-task")
		is.True(err != nil)
		is.True(strings.Contains(err.Error(), "(bug)"))

		is.NoErr(m.branch(0).Ack(ctx, ackBatchFor(records, positions[0])))
		is.NoErr(m.branch(1).Ack(ctx, ackBatchFor(records, positions[0])))

		is.Equal(len(parent.nackCalls()), 0)
		acks := parent.ackCalls()
		is.Equal(len(acks), 1)
		is.Equal(acks[0].positions, positions)
	})

	t.Run("two branches nack: first attribution stands", func(t *testing.T) {
		is := is.New(t)
		ctx := context.Background()
		records := randomRecords(2)
		positions := []opencdc.Position{records[0].Position, records[1].Position}

		parent := &fakeParentAckNacker{}
		m := mustNewMultiAckNacker(t, parent, 2, positions)

		is.NoErr(m.branch(0).Nack(ctx, nackBatchFor(records, positions[1], cerrors.New("first")), "first-task"))
		is.NoErr(m.branch(1).Nack(ctx, nackBatchFor(records, positions[1], cerrors.New("second")), "second-task"))
		is.NoErr(m.branch(0).Ack(ctx, ackBatchFor(records, positions[0])))
		is.NoErr(m.branch(1).Ack(ctx, ackBatchFor(records, positions[0])))

		nacks := parent.nackCalls()
		is.Equal(len(nacks), 1)
		is.Equal(nacks[0].taskID, "first-task")
	})
}

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
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
)

// TestMultiAckNacker_FailedRelease_IsFinal (#3324 review, M2): once a release
// to the parent fails, a later vote from a sibling branch that is still
// running must not retry it. The branch pool does not cancel siblings when
// one branch fails, so a sibling can vote on a later position and re-enter the
// release loop, which starts again at the position whose release failed.
// Retrying parent.Nack writes a second DLQ entry for a record whose first
// write may already have succeeded (DLQ.Nack succeeds, then Source.Ack fails),
// and counts the nack in the DLQ window a second time, which can turn a write
// error into a "threshold exceeded" error and change the reported cause.
func TestMultiAckNacker_FailedRelease_IsFinal(t *testing.T) {
	t.Run("failed nack release is not retried", func(t *testing.T) {
		is := is.New(t)
		ctx := context.Background()
		records := randomRecords(2)
		p0, p1 := records[0].Position, records[1].Position

		releaseErr := cerrors.FatalError(cerrors.New("task dlq failed to ack"))
		parent := &fakeParentAckNacker{nackErr: releaseErr}
		m := mustNewMultiAckNacker(t, parent, 2, []opencdc.Position{p0, p1})
		a, b := m.branch(0), m.branch(1)

		// B nacks p0, A acks p0: the last vote releases p0 and parent.Nack fails.
		is.NoErr(b.Nack(ctx, nackBatchFor(records, p0, cerrors.New("dest B failed")), "destB"))
		err := a.Ack(ctx, ackBatchFor(records, p0))
		is.True(cerrors.Is(err, releaseErr))

		// The parent would now succeed, so a retry would be a second DLQ write.
		parent.mu.Lock()
		parent.nackErr = nil
		parent.mu.Unlock()

		// B, still running, votes on p1 (a nack, so the nack path's check is
		// the one exercised). It must get the failure, not a retry of p0.
		err = b.Nack(ctx, nackBatchFor(records, p1, cerrors.New("dest B failed again")), "destB")
		is.Equal(len(parent.nackCalls()), 1) // one DLQ write, one window count
		is.True(err != nil)
		is.True(cerrors.Is(err, releaseErr))
		is.True(cerrors.IsFatalError(err)) // the classification survives
		err = a.Ack(ctx, ackBatchFor(records, p1))
		is.True(cerrors.Is(err, releaseErr))

		is.Equal(len(parent.nackCalls()), 1)
		is.Equal(len(parent.ackCalls()), 0) // nothing past the failed position
	})

	t.Run("failed ack release is not retried", func(t *testing.T) {
		is := is.New(t)
		ctx := context.Background()
		records := randomRecords(2)
		p0, p1 := records[0].Position, records[1].Position

		releaseErr := cerrors.New("source ack failed")
		parent := &fakeParentAckNacker{ackErr: releaseErr}
		m := mustNewMultiAckNacker(t, parent, 2, []opencdc.Position{p0, p1})
		a, b := m.branch(0), m.branch(1)

		is.NoErr(b.Ack(ctx, ackBatchFor(records, p0)))
		err := a.Ack(ctx, ackBatchFor(records, p0))
		is.True(cerrors.Is(err, releaseErr))

		parent.mu.Lock()
		parent.ackErr = nil
		parent.mu.Unlock()

		err = b.Ack(ctx, ackBatchFor(records, p1))
		is.Equal(len(parent.ackCalls()), 1) // not retried
		is.True(cerrors.Is(err, releaseErr))
		err = a.Ack(ctx, ackBatchFor(records, p1))
		is.True(cerrors.Is(err, releaseErr))

		is.Equal(len(parent.ackCalls()), 1)
	})
}

// TestMultiAckNacker_DoubleVoteBeforeAllVoted_IsRefused (#3324 review, L1):
// with M=2, a branch that votes twice for a position before its sibling has
// voted would otherwise make the position terminal on its own and release it
// without the sibling's vote: an early ack (invariant 1). The refusal must
// come from tracking WHICH branch voted, not from counting votes.
func TestMultiAckNacker_DoubleVoteBeforeAllVoted_IsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		second func(ctx context.Context, a ackNacker, records []opencdc.Record, p opencdc.Position) error
	}{
		{"ack twice", func(ctx context.Context, a ackNacker, records []opencdc.Record, p opencdc.Position) error {
			return a.Ack(ctx, ackBatchFor(records, p))
		}},
		{"ack then nack", func(ctx context.Context, a ackNacker, records []opencdc.Record, p opencdc.Position) error {
			return a.Nack(ctx, nackBatchFor(records, p, cerrors.New("again")), "destA")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			records := randomRecords(1)
			p0 := records[0].Position

			parent := &fakeParentAckNacker{}
			m := mustNewMultiAckNacker(t, parent, 2, []opencdc.Position{p0})
			a, b := m.branch(0), m.branch(1)

			is.NoErr(a.Ack(ctx, ackBatchFor(records, p0)))
			err := tc.second(ctx, a, records, p0)
			is.Equal(len(parent.ackCalls()), 0) // not released without B
			is.Equal(len(parent.nackCalls()), 0)
			is.True(err != nil)
			is.True(cerrors.Is(err, errDoubleVote))

			// B's real vote still completes the position normally.
			is.NoErr(b.Ack(ctx, ackBatchFor(records, p0)))
			acks := parent.ackCalls()
			is.Equal(len(acks), 1)
			is.Equal(acks[0].positions, []opencdc.Position{p0})
		})
	}
}

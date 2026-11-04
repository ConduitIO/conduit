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
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/matryer/is"
)

// TestWorker_FanOutBatching_SiblingNacksAtAnyPositionOfMultiRecordBatch (#3323): batch
// N (rec0) is buffered in batching destination A; batch N+1 has three records
// and B nacks some of them, so B's votes for N+1 arrive as several sub-batch
// votes (ack/nack runs) on one multiAckNacker. None may be released before A
// votes for N+1, and once A votes, rec0..rec3 are released strictly in source
// order with the nacked records in the DLQ. With only the middle record
// nacked, the prefix scan alone holds it (rec1 still needs A's vote); with the
// first record nacked, only the #3323 fix holds it.
//
// "A flushes at Close": A's buffer (10) never fills, so A votes only in the
// drain. "A flushes inside the pass": A's buffer (3) fills while batch N+1 is
// being fanned out, so A's flush races B's three votes on the same tally.
func TestWorker_FanOutBatching_SiblingNacksAtAnyPositionOfMultiRecordBatch(t *testing.T) {
	for _, tc := range []struct {
		name       string
		aBatchSize int
		flushInN1  bool  // A's buffer fills during batch N+1
		nacked     []int // indexes into recs that B nacks (all in batch N+1)
	}{
		{name: "middle nack, A flushes at Close", aBatchSize: 10, nacked: []int{2}},
		{name: "middle nack, A flushes inside the pass", aBatchSize: 3, flushInN1: true, nacked: []int{2}},
		{name: "first and last nacked, A flushes at Close", aBatchSize: 10, nacked: []int{1, 3}},
		{name: "first and last nacked, A flushes inside the pass", aBatchSize: 3, flushInN1: true, nacked: []int{1, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			logger := log.Test(t)
			recs := randomRecords(4)

			src := newFakeSource("src", nil)
			destA, destB := newFakeDestination("A"), newFakeDestination("B")
			var dlqWant []opencdc.Position
			for _, i := range tc.nacked {
				destB.setNackErr(recs[i].Position, cerrors.New("B: simulated write failure"))
				dlqWant = append(dlqWant, recs[i].Position)
			}

			dtA := NewBatchingDestinationTask("A", destA, logger, NoOpConnectorMetrics{}, tc.aBatchSize, 0)
			dtB := NewDestinationTask("B", destB, logger, NoOpConnectorMetrics{})
			fanNode := &TaskNode{Task: passthroughTask{id: "shared"}, Next: []*TaskNode{{Task: dtA}, {Task: dtB}}}
			srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{fanNode}}
			dlqDest := newFakeDestination("dlq")
			w, err := NewWorker(srcNode, NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, 10, 10), logger, noop.Timer{})
			is.NoErr(err)

			// Batch N: rec0, buffered in A.
			is.NoErr(w.doTask(ctx, fanNode, NewBatch([]opencdc.Record{recs[0]}), newRunAckNacker(w)))
			// Batch N+1: rec1..rec3, B nacks tc.nacked.
			is.NoErr(w.doTask(ctx, fanNode, NewBatch([]opencdc.Record{recs[1], recs[2], recs[3]}), newRunAckNacker(w)))

			if !tc.flushInN1 {
				// Crash point: A has written nothing, so nothing may be acked
				// and nothing may be in the DLQ yet.
				is.Equal(len(destA.receivedPositions()), 0)
				is.Equal(len(src.ackedPositions()), 0)
				is.Equal(len(dlqDest.receivedPositions()), 0)
			} else {
				// Every source ack must cover only records A has written.
				written := len(destA.receivedPositions())
				is.True(len(src.ackedPositions()) <= written)
			}

			is.NoErr(dtA.Close(ctx))
			is.NoErr(dtB.Close(ctx))

			is.Equal(destA.receivedPositions(), posOf(recs...))
			is.Equal(src.ackedPositions(), posOf(recs...)) // source order, each once
			is.Equal(dlqDest.receivedPositions(), dlqWant)
		})
	}
}

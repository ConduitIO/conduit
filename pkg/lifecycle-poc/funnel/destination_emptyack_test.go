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
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// scriptedAckDestination replays a fixed list of ack responses, one per Ack
// call, then fails Ack. It is how a destination that sends an empty
// DestinationRunResponse (legal in the protocol) looks to the funnel.
type scriptedAckDestination struct {
	*fakeDestination
	responses [][]connector.DestinationAck
	calls     int
}

func (d *scriptedAckDestination) Write(context.Context, []opencdc.Record) error { return nil }

func (d *scriptedAckDestination) Ack(context.Context) ([]connector.DestinationAck, error) {
	if d.calls >= len(d.responses) {
		return nil, cerrors.New("stream closed: no more acks")
	}
	r := d.responses[d.calls]
	d.calls++
	return r, nil
}

// TestDestinationTask_Do_CountsAcksNotResponses: Do bounded its ack loop by the
// number of responses (len(positions)), not the number of acked records. With
// an empty response in the mix it left the loop with records unacked and
// returned nil, so the worker acked them upstream (Invariant 1) and the
// destination's remaining acks stayed queued on the stream for the next write.
func TestDestinationTask_Do_CountsAcksNotResponses(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	recs := randomRecords(2)
	ack := func(i int) connector.DestinationAck { return connector.DestinationAck{Position: recs[i].Position} }

	dest := &scriptedAckDestination{
		fakeDestination: newFakeDestination("d"),
		responses: [][]connector.DestinationAck{
			{},       // empty response
			{ack(0)}, // first record
			{ack(1)}, // second record
		},
	}
	task := NewDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{})
	batch := NewBatch(recs)

	err := task.Do(ctx, batch)
	is.NoErr(err)
	is.Equal(dest.calls, 3) // every response consumed: nothing left queued on the stream
}

// TestDestinationTask_Do_ShortAckSetIsAnError: if the destination stops acking
// before every record is acked, Do must fail, never return nil.
func TestDestinationTask_Do_ShortAckSetIsAnError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	recs := randomRecords(3)

	dest := &scriptedAckDestination{
		fakeDestination: newFakeDestination("d"),
		responses: [][]connector.DestinationAck{
			{},
			{},
			{{Position: recs[0].Position}},
		},
	}
	task := NewDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{})

	err := task.Do(ctx, NewBatch(recs))
	is.True(err != nil)
}

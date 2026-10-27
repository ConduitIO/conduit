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
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
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

// floodDestination answers every Ack with an empty response, forever.
type floodDestination struct {
	*fakeDestination
	calls int
}

func (d *floodDestination) Write(context.Context, []opencdc.Record) error { return nil }
func (d *floodDestination) Ack(context.Context) ([]connector.DestinationAck, error) {
	d.calls++
	return nil, nil
}

// TestDestinationTask_Do_EmptyAckFlood_FailsWithCode: the protocol allows empty
// ack responses but not an endless run of them; Do must give up with a coded
// error (engine policy, maxConsecutiveEmptyAcks) and ack nothing.
func TestDestinationTask_Do_EmptyAckFlood_FailsWithCode(t *testing.T) {
	is := is.New(t)
	dest := &floodDestination{fakeDestination: newFakeDestination("d")}
	task := NewDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{})

	err := task.Do(context.Background(), NewBatch(randomRecords(2)))
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeDestinationEmptyAckFlood.Reason())
	is.Equal(dest.calls, maxConsecutiveEmptyAcks)
}

// TestDestinationTask_FlushPending_EmptyAckFlood_FailsWithCode: same bound on the
// batching path, and no source ack is issued.
func TestDestinationTask_FlushPending_EmptyAckFlood_FailsWithCode(t *testing.T) {
	is := is.New(t)
	dest := &floodDestination{fakeDestination: newFakeDestination("d")}
	task := NewBatchingDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{}, 2, 0)
	alog := &eventLog{}
	recs := randomRecords(2)

	is.True(isBuffered(task.doBuffered(context.Background(), NewBatch(recs[:1]), newRecordingAckNacker(alog))))
	err := task.doBuffered(context.Background(), NewBatch(recs[1:]), newRecordingAckNacker(alog))
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeDestinationEmptyAckFlood.Reason())
	is.Equal(len(alog.snapshot()), 0)
}

// TestDestinationTask_Do_EmptyResponsesBelowBoundAreTolerated: empty responses
// below the bound do not fail the write, and they reset on a response with acks.
func TestDestinationTask_Do_EmptyResponsesBelowBoundAreTolerated(t *testing.T) {
	is := is.New(t)
	recs := randomRecords(2)
	var responses [][]connector.DestinationAck
	for i := 0; i < maxConsecutiveEmptyAcks-1; i++ {
		responses = append(responses, nil)
	}
	responses = append(responses, []connector.DestinationAck{{Position: recs[0].Position}})
	for i := 0; i < maxConsecutiveEmptyAcks-1; i++ {
		responses = append(responses, nil)
	}
	responses = append(responses, []connector.DestinationAck{{Position: recs[1].Position}})
	dest := &scriptedAckDestination{fakeDestination: newFakeDestination("d"), responses: responses}
	task := NewDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{})

	is.NoErr(task.Do(context.Background(), NewBatch(recs)))
}

// TestDestinationTask_Do_OverLongAckSetAcrossResponsesFails: an ack set that
// overflows only on a later response is rejected by the positions[ackCount:]
// slicing, not ignored.
func TestDestinationTask_Do_OverLongAckSetAcrossResponsesFails(t *testing.T) {
	is := is.New(t)
	recs := randomRecords(2)
	extra := connector.DestinationAck{Position: opencdc.Position("extra")}
	dest := &scriptedAckDestination{
		fakeDestination: newFakeDestination("d"),
		responses: [][]connector.DestinationAck{
			{{Position: recs[0].Position}},
			{{Position: recs[1].Position}, extra},
		},
	}
	task := NewDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{})
	is.True(task.Do(context.Background(), NewBatch(recs)) != nil)
}

// ctxAckDestination blocks in Ack until its own context (standing in for the
// stream/Open context a real connector.Destination honours) ends.
type ctxAckDestination struct {
	*fakeDestination
	streamCtx context.Context
}

func (d *ctxAckDestination) Write(context.Context, []opencdc.Record) error { return nil }
func (d *ctxAckDestination) Ack(ctx context.Context) ([]connector.DestinationAck, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-d.streamCtx.Done():
		return nil, d.streamCtx.Err()
	}
}

// TestDestinationTask_Do_ReturnsWhenCancelledWhileWaitingForAcks: a destination
// that never acks must not hold Do once the context ends, and nothing is acked.
func TestDestinationTask_Do_ReturnsWhenCancelledWhileWaitingForAcks(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	dest := &ctxAckDestination{fakeDestination: newFakeDestination("d"), streamCtx: context.Background()}
	task := NewDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{})

	done := make(chan error, 1)
	go func() { done <- task.Do(ctx, NewBatch(randomRecords(2))) }()
	cancel()
	select {
	case err := <-done:
		is.True(err != nil)
	case <-time.After(2 * time.Second):
		t.Fatal("Do did not return after its context was cancelled")
	}
}

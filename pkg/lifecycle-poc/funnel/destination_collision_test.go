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

// dropAckDestination acks every write except it silently drops the ack of the
// record at index dropIdx of the flush, then closes the stream (Ack errors).
type dropAckDestination struct {
	*fakeDestination
	acks  []connector.DestinationAck
	sent  bool
	drop  int
	calls int
}

func (d *dropAckDestination) Write(_ context.Context, recs []opencdc.Record) error {
	for i, r := range recs {
		if i == d.drop {
			continue
		}
		d.acks = append(d.acks, connector.DestinationAck{Position: r.Position})
	}
	return nil
}

func (d *dropAckDestination) Ack(context.Context) ([]connector.DestinationAck, error) {
	d.calls++
	if d.sent {
		return nil, cerrors.New("stream closed: destination dropped an ack")
	}
	d.sent = true
	return d.acks, nil
}

// TestFlushPending_CollidingPositionsAcrossSources_DroppedAckAcksNothing
// characterises the shared-destination (Nx1) batching path on main. Two sources
// emit records with byte-identical positions into one coalesced flush; the
// destination drops one ack. validateAcks cannot tell the two records apart by
// position, so the ack that does arrive is accepted for the first record, but
// the flush still owes an ack for the second: it waits for it and fails when the
// stream ends. Neither source may be acked. This pins that the one-write-in-
// flight design (a single flusher) turns a dropped ack into a stall or error,
// never into an ack for an unwritten record; the design that puts several
// Writes in flight on one stream (#3292) removes that property and needs a
// per-write ack count.
func TestFlushPending_CollidingPositionsAcrossSources_DroppedAckAcksNothing(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	same := opencdc.Position("p1") // identical bytes from two different sources
	a := opencdc.Record{Position: same, Key: opencdc.RawData("a")}
	b := opencdc.Record{Position: same, Key: opencdc.RawData("b")}

	for drop := 0; drop < 2; drop++ {
		dest := &dropAckDestination{fakeDestination: newFakeDestination("d"), drop: drop}
		task := NewBatchingDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{}, 2, 0)

		logA, logB := &eventLog{}, &eventLog{}
		is.True(isBuffered(task.doBuffered(ctx, NewBatch([]opencdc.Record{a}), newRecordingAckNacker(logA))))
		err := task.doBuffered(ctx, NewBatch([]opencdc.Record{b}), newRecordingAckNacker(logB))

		is.True(err != nil)               // the flush fails instead of succeeding
		is.Equal(len(logA.snapshot()), 0) // source A not acked
		is.Equal(len(logB.snapshot()), 0) // source B not acked
	}
}

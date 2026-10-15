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

package connector

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// fakeAckStream returns one fixed DestinationRunResponse.
type fakeAckStream struct {
	resp pconnector.DestinationRunResponse
}

func (f fakeAckStream) Send(pconnector.DestinationRunRequest) error { return nil }
func (f fakeAckStream) Recv() (pconnector.DestinationRunResponse, error) {
	return f.resp, nil
}

// TestDestination_Ack_AvroLimitIsCoded: the connector-sdk destination schema
// middleware fails the whole batch when one record exceeds the Avro element
// limit, and the error crosses the plugin protocol as a string. Ack must
// surface it as schema.avro.limit_exceeded; other ack errors keep no code.
func TestDestination_Ack_AvroLimitIsCoded(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	dest, destinationMock := newTestDestination(ctx, t, gomock.NewController(t))

	const sdkErr = "failed to decode payload.after: failed to unmarshal bytes with schema: " +
		"could not unmarshal from avro: opencdc.StructuredData: avro: decode array: " +
		"size is greater than `Config.MaxSliceAllocSize`"
	dest.plugin = destinationMock
	dest.stream = fakeAckStream{resp: pconnector.DestinationRunResponse{Acks: []pconnector.DestinationRunResponseAck{
		{Position: opencdc.Position("1"), Error: sdkErr},
		{Position: opencdc.Position("2"), Error: "connection refused"},
		{Position: opencdc.Position("3")},
	}}}

	acks, err := dest.Ack(ctx)
	is.NoErr(err)
	is.Equal(len(acks), 3)

	ce, ok := conduiterr.Get(acks[0].Error)
	is.True(ok)
	is.Equal(ce.Code, schemaregistry.CodeAvroLimitExceeded)
	is.Equal(acks[0].Error.Error(), sdkErr) // message unchanged

	_, ok = conduiterr.Get(acks[1].Error)
	is.True(!ok)
	is.Equal(acks[2].Error, nil)
}

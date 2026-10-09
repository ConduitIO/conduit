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

package builtin

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/matryer/is"
)

// TestInMemoryDestinationRunStream_ClientSendClonesRequest pins the clone in
// inMemoryStreamClient.Send. Arch-v2 destination fan-out (#2910) lets sibling
// destination branches share one copy of each record, which is only safe
// because a builtin destination never receives the caller's records. A server
// that edits what it received, as the SDK's schema-extraction destination
// middleware does (it assigns to Key and Payload.Before/After of the received
// slice elements and writes into Metadata), must not reach the sender.
//
// Removing req.Clone() in Send makes this test fail; go test -race on the
// fan-out tests does not catch that, because those use mock destinations.
func TestInMemoryDestinationRunStream_ClientSendClonesRequest(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	var stream InMemoryDestinationRunStream
	stream.Init(ctx)

	sent := []opencdc.Record{{
		Position: opencdc.Position("p1"),
		Key:      opencdc.RawData("key"),
		Metadata: opencdc.Metadata{"k": "v"},
		Payload: opencdc.Change{
			Before: opencdc.RawData("before"),
			After:  opencdc.RawData("after"),
		},
	}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		req, err := stream.Server().Recv()
		if err != nil {
			t.Errorf("server recv: %v", err)
			return
		}
		// Edits a plugin is allowed to make to a record it received.
		req.Records[0].Key = opencdc.RawData("server-key")
		req.Records[0].Payload.Before = opencdc.RawData("server-before")
		req.Records[0].Payload.After = opencdc.StructuredData{"server": "after"}
		req.Records[0].Metadata["k"] = "server-edit"
		req.Records[0].Metadata["added"] = "by-server"
	}()

	err := stream.Client().Send(pconnector.DestinationRunRequest{Records: sent})
	is.NoErr(err)
	<-done

	is.Equal(sent[0].Key, opencdc.RawData("key"))
	is.Equal(sent[0].Payload.Before, opencdc.RawData("before"))
	is.Equal(sent[0].Payload.After, opencdc.RawData("after"))
	is.Equal(sent[0].Metadata, opencdc.Metadata{"k": "v"})
}

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

// TestInMemorySourceRunStream_ServerSendTransfersOwnership pins the missing
// clone in inMemoryStreamServer.Send for the SOURCE stream. A built-in source
// hands ownership of its records to the engine (the SDK Source.ReadN contract
// transfers the records to the SDK, and the SDK passes them to the stream
// without reusing them — it only reads the last record's Position afterwards).
// The engine then mutates what it received (connector.Source.sanitizeRecord
// writes Metadata and RawData placeholders), which must NOT require a per-record
// deep clone: the clone would double the Position bytes, Metadata map and
// Key/Payload StructuredData of every record on the source→engine boundary,
// the hottest path in the process.
//
// Re-adding resp.Clone() in inMemoryStreamServer.Send for the source stream
// makes this test fail.
func TestInMemorySourceRunStream_ServerSendTransfersOwnership(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	var stream InMemorySourceRunStream
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
		resp, err := stream.Client().Recv()
		if err != nil {
			t.Errorf("client recv: %v", err)
			return
		}
		// The edits the engine's connector.Source.sanitizeRecord is allowed to
		// make to a record it received from a built-in source.
		resp.Records[0].Key = opencdc.RawData("engine-key")
		resp.Records[0].Metadata["k"] = "engine-edit"
		resp.Records[0].Metadata["added"] = "by-engine"
	}()

	err := stream.Server().Send(pconnector.SourceRunResponse{Records: sent})
	is.NoErr(err)
	<-done

	// The server's record is the SAME object the client received (no clone):
	// the engine's edits are visible on it.
	is.Equal(sent[0].Key, opencdc.RawData("engine-key"))
	is.Equal(sent[0].Metadata, opencdc.Metadata{"k": "engine-edit", "added": "by-engine"})
}

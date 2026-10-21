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

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// TestProcessorTask_SchemaDecodeFailure_NacksOnlyTheFailingRecord is the
// regression test for #2968. Every built-in processor runs behind the SDK's
// schema middleware (pkg/plugin/processor/builtin/registry.go). Before
// processor-sdk v0.8.1 the middleware answered a decode failure on record i
// with a single ErrorRecord at index 0, so arch v2 nacked record 0 (the DLQ
// got a good record with someone else's error) and retried the rest until
// the bad record reached index 0. With v0.8.1 the error sits at index i:
// exactly the bad record is nacked, and the good ones reach the destination.
func TestProcessorTask_SchemaDecodeFailure_NacksOnlyTheFailingRecord(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	passthrough := sdk.NewProcessorFunc(sdk.Specification{Name: "passthrough"},
		func(_ context.Context, r opencdc.Record) (opencdc.Record, error) { return r, nil })
	proc := sdk.ProcessorWithMiddleware(passthrough, sdk.DefaultProcessorMiddleware()...)
	is.NoErr(proc.Configure(ctx, config.Config{}))

	bad := opencdc.Record{
		Position: opencdc.Position("r1"),
		Metadata: opencdc.Metadata{
			opencdc.MetadataPayloadSchemaSubject: "issue-2968-missing-subject",
			opencdc.MetadataPayloadSchemaVersion: "1",
		},
		Payload: opencdc.Change{After: opencdc.RawData("not decodable")},
	}
	records := []opencdc.Record{
		{Position: opencdc.Position("r0"), Payload: opencdc.Change{After: opencdc.RawData("ok-0")}},
		bad,
		{Position: opencdc.Position("r2"), Payload: opencdc.Change{After: opencdc.RawData("ok-2")}},
	}

	dest := newPoisonProbeDestination("dest", -1, nil) // never fails an ack
	destNode := &TaskNode{Task: NewDestinationTask("dest", dest, log.Nop(), NoOpConnectorMetrics{})}
	procNode := &TaskNode{Task: NewProcessorTask("proc", proc, log.Nop(), NoOpProcessorMetrics{})}
	procNode.Next = []*TaskNode{destNode}
	sink, err := NewSink(procNode)
	is.NoErr(err)
	is.NoErr(sink.Open(ctx))
	defer func() { is.NoErr(sink.Close(ctx)) }()

	w := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}
	parent := &fakeParentAckNacker{}
	is.NoErr(w.doTask(ctx, procNode, NewBatch(records), parent))

	var nacked, acked []string
	for _, n := range parent.nackCalls() {
		for _, p := range n.positions {
			nacked = append(nacked, string(p))
		}
	}
	for _, a := range parent.ackCalls() {
		for _, p := range a.positions {
			acked = append(acked, string(p))
		}
	}
	is.Equal(nacked, []string{"r1"})      // exactly the record that failed decode goes to the DLQ
	is.Equal(acked, []string{"r0", "r2"}) // the good records are delivered and acked
}

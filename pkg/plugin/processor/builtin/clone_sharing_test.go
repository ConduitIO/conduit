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
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle/stream"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/custom"
	"github.com/matryer/is"
)

// TestFanout_JSArrayMutationDoesNotLeakToSiblingBranch is the engine-level
// regression test for ConduitIO/conduit-commons#336 (silent cross-record
// corruption). An arch-v1 pipeline with two destinations fans each message out
// with Message.Clone in stream.FanoutNode, and each branch then runs its own
// destination-side processors on its copy. StructuredData.Clone used to share
// nested arrays between the copies, so a JavaScript processor that edited an
// array element on one branch changed the sibling branch and the original.
//
// Invariant 6: branch B must receive exactly the data the source produced.
func TestFanout_JSArrayMutationDoesNotLeakToSiblingBranch(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	newRecord := func() opencdc.Record {
		return opencdc.Record{
			Position: opencdc.Position("p"),
			Payload: opencdc.Change{After: opencdc.StructuredData{
				"tags":   []any{"a", "b"},
				"nested": map[string]any{"x": 1},
			}},
		}
	}

	// Wire a source channel through the real FanoutNode to two destination
	// branches.
	in := make(chan *stream.Message, 1)
	fanout := &stream.FanoutNode{Name: "fanout"}
	fanout.Sub(in)
	outA, outB := fanout.Pub(), fanout.Pub()
	done := make(chan error, 1)
	go func() { done <- fanout.Run(ctx) }()

	orig := &stream.Message{Ctx: ctx, Record: newRecord()}
	in <- orig
	close(in)
	branchA, branchB := <-outA, <-outB

	// Branch A's destination-side JS processor edits a nested array in place.
	js := custom.NewJavascriptProcessor(log.Nop())
	is.NoErr(js.Configure(ctx, config.Config{"script": `
function process(record) {
  record.Payload.After["tags"][0] = "MUTATED";
  return record;
}`}))
	is.NoErr(js.Open(ctx))
	defer func() { _ = js.Teardown(ctx) }()

	res := js.Process(ctx, []opencdc.Record{branchA.Record})
	is.Equal(len(res), 1)
	out, ok := res[0].(sdk.SingleRecord)
	is.True(ok)
	// control: the processor did change branch A's own result
	is.Equal(out.Payload.After.(opencdc.StructuredData)["tags"].([]any)[0], "MUTATED")

	tags := func(r opencdc.Record) any {
		return r.Payload.After.(opencdc.StructuredData)["tags"].([]any)[0]
	}
	is.Equal(tags(branchB.Record), "a") // the sibling branch must not see branch A's change
	is.Equal(tags(orig.Record), "a")    // nor the original message

	is.NoErr(<-done)
}

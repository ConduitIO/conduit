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

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle/stream"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/custom"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/field"
	"github.com/matryer/is"
)

// conduit-commons#336 repros. A v1 pipeline with two destinations fans each
// message out with Message.Clone (pkg/lifecycle/stream/fanout.go), and each
// branch then runs its own destination-side processors on its copy.

func fanoutRecord() opencdc.Record {
	return opencdc.Record{
		Position: opencdc.Position("p"),
		Payload: opencdc.Change{After: opencdc.StructuredData{
			"tags":   []any{"a", "b"},
			"nested": map[string]any{"x": 1},
		}},
	}
}

func process(t *testing.T, p sdk.Processor, r opencdc.Record) sdk.ProcessedRecord {
	t.Helper()
	out := p.Process(context.Background(), []opencdc.Record{r})
	if len(out) != 1 {
		t.Fatalf("got %d results", len(out))
	}
	return out[0]
}

// A JS processor on branch A mutates a nested array in place. Branch B's
// copy must not change.
func TestRepro336_FanoutJSArrayMutationLeaksToSiblingBranch(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	msg := &stream.Message{Ctx: ctx, Record: fanoutRecord()}
	branchA, branchB := msg.Clone(), msg.Clone()

	js := custom.NewJavascriptProcessor(log.Nop())
	is.NoErr(js.Configure(ctx, config.Config{"script": `
function process(record) {
  record.Payload.After["tags"][0] = "MUTATED";
  return record;
}`}))
	is.NoErr(js.Open(ctx))
	defer func() { _ = js.Teardown(ctx) }()

	_, ok := process(t, js, branchA.Record).(sdk.SingleRecord)
	is.True(ok)

	got := branchB.Record.Payload.After.(opencdc.StructuredData)["tags"].([]any)[0]
	is.Equal(got, "a") // branch B's record must not see branch A's change
	got = msg.Record.Payload.After.(opencdc.StructuredData)["tags"].([]any)[0]
	is.Equal(got, "a") // nor the original
}

// field.set on a nested path works on the original record but fails on a
// clone, because Clone turns nested map[string]any into StructuredData,
// which the reference resolver does not walk into.
func TestRepro336_FieldSetNestedPathFailsOnClone(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	set := field.NewSetProcessor(log.Nop())
	is.NoErr(set.Configure(ctx, config.Config{"field": ".Payload.After.nested.y", "value": "2"}))
	is.NoErr(set.Open(ctx))

	_, ok := process(t, set, fanoutRecord()).(sdk.SingleRecord)
	is.True(ok) // control: the original works

	msg := &stream.Message{Ctx: ctx, Record: fanoutRecord()}
	out := process(t, set, msg.Clone().Record)
	if e, isErr := out.(sdk.ErrorRecord); isErr {
		t.Fatalf("field.set on a fanned-out copy failed: %v", e.Error)
	}
}

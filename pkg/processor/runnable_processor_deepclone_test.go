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

package processor

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/custom"
	"github.com/matryer/is"
)

func deepProcessorRecord() opencdc.Record {
	return opencdc.Record{
		Position:  opencdc.Position("p1"),
		Operation: opencdc.OperationCreate,
		Metadata:  opencdc.Metadata{},
		Key:       opencdc.StructuredData{"ids": []any{"k1", "k2"}},
		Payload: opencdc.Change{After: opencdc.StructuredData{
			"items":  []any{map[string]any{"sku": "A", "qty": 1}},
			"nested": map[string]any{"deeper": map[string]any{"y": 2}, "tags": []any{"a", "b"}},
		}},
	}
}

// TestRunnableProcessor_Process_IsolatesNestedInput is the processor-boundary
// case of conduit#3151 (conduit-commons#336). RunnableProcessor hands the
// processor a Record.Clone of the engine's record so that a processor that
// edits its input in place cannot change what the engine holds, which is what
// sibling fan-out branches and the DLQ read. TestRunnableProcessor_Process_IsolatesInputOnError
// only edits a nested map, which even a shallow Clone copied; the arrays and
// the maps below them are what it shared.
func TestRunnableProcessor_Process_IsolatesNestedInput(t *testing.T) {
	const script = `function process(r) {
		r.Payload.After.items[0].qty = 999;
		r.Key.ids[0] = "EDITED";
		if (r.Metadata["fail"] == "yes") { throw new Error("boom"); }
		return r;
	}`
	for name, fail := range map[string]bool{"success": false, "error": true} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			js := custom.NewJavascriptProcessor(log.Nop())
			is.NoErr(js.Configure(ctx, config.Config{"script": script}))
			is.NoErr(js.Open(ctx))
			defer func() { _ = js.Teardown(ctx) }()

			underTest := newRunnableProcessor(js, nil, newTestInstance())
			defer underTest.Close()

			in := deepProcessorRecord()
			if fail {
				in.Metadata["fail"] = "yes"
			}
			want := string(in.Clone().Bytes())

			out := underTest.Process(ctx, []opencdc.Record{in})

			if fail {
				_, ok := out[0].(sdk.ErrorRecord)
				is.True(ok)
			} else {
				// The processor's own output carries its edits, so the
				// test cannot pass because the script did nothing.
				sr, ok := out[0].(sdk.SingleRecord)
				is.True(ok)
				items := sr.Payload.After.(opencdc.StructuredData)["items"].([]any)
				is.Equal(items[0].(map[string]any)["qty"], int64(999))
			}

			// Invariant 6: the record the engine holds is unchanged.
			is.Equal(string(in.Bytes()), want)
		})
	}
}

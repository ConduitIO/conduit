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

package impl

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/custom"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/field"
	"github.com/matryer/is"
)

// These tests cover conduit#3151 (conduit-commons#336) for the processors that
// produce several records from one with opencdc.Record.Clone (clone, split).
// Every output record must own its nested arrays and maps: with a shallow
// Clone, an in-place edit to one output rewrites its siblings, and a nested
// map[string]any is retyped to opencdc.StructuredData, which breaks field.*
// on nested fields.

func deepCloneRecord() opencdc.Record {
	return opencdc.Record{
		Position:  opencdc.Position("p1"),
		Operation: opencdc.OperationCreate,
		Metadata:  opencdc.Metadata{},
		Key:       opencdc.StructuredData{"ids": []any{"k1", "k2"}},
		Payload: opencdc.Change{After: opencdc.StructuredData{
			"items":  []any{map[string]any{"sku": "A", "qty": 1}, map[string]any{"sku": "B", "qty": 2}},
			"nested": map[string]any{"x": 1, "items": []any{"a", "b"}},
			"blob":   []byte("blob-bytes"),
		}},
	}
}

func newDeepJS(t *testing.T, script string) *custom.JavascriptProcessor {
	t.Helper()
	ctx := context.Background()
	p := custom.NewJavascriptProcessor(log.Nop())
	is := is.New(t)
	is.NoErr(p.Configure(ctx, config.Config{"script": script}))
	is.NoErr(p.Open(ctx))
	t.Cleanup(func() { _ = p.Teardown(ctx) })
	return p
}

func nestedMap(v any) map[string]any {
	if sd, ok := v.(opencdc.StructuredData); ok {
		return sd
	}
	return v.(map[string]any)
}

// TestCloneProcessor_CloneThenJS_OutputsAreIndependent is the clone-processor
// case of #3151: a custom.javascript processor after clone edits clone 0's
// nested array in place; clone 1 (and the record the clone processor was
// given) must not change.
func TestCloneProcessor_CloneThenJS_OutputsAreIndependent(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	cp := NewCloneProcessor(log.Nop())
	is.NoErr(cp.Configure(ctx, config.Config{"count": "1"}))
	in := deepCloneRecord()
	mr := cp.Process(ctx, []opencdc.Record{in})[0].(sdk.MultiRecord)
	is.Equal(len(mr), 2)

	js := newDeepJS(t, `function process(r) {
		if (r.Metadata["clone.index"] == "0") {
			r.Payload.After.items[0].qty = 999;
			r.Key.ids[0] = "EDITED";
		}
		return r;
	}`)
	out0 := js.Process(ctx, []opencdc.Record{mr[0]})
	_, ok := out0[0].(sdk.SingleRecord)
	is.True(ok) // the script ran without error

	want := deepCloneRecord()
	want.Metadata["clone.index"] = "1"
	is.Equal(string(mr[1].Bytes()), string(want.Bytes()))           // clone 1 changed by an edit on clone 0
	is.Equal(string(in.Bytes()), string(deepCloneRecord().Bytes())) // the input changed
	// the edit really ran on clone 0
	is.Equal(nestedMap(mr[0].Payload.After.(opencdc.StructuredData)["items"].([]any)[0])["qty"], int64(999))
}

// TestCloneProcessor_OutputsDoNotShareNestedData checks the same without a
// JavaScript runtime: edit every kind of nested value of each output in
// place with that output's own marker, then read the markers back.
func TestCloneProcessor_OutputsDoNotShareNestedData(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	cp := NewCloneProcessor(log.Nop())
	is.NoErr(cp.Configure(ctx, config.Config{"count": "3"}))
	in := deepCloneRecord()
	mr := cp.Process(ctx, []opencdc.Record{in})[0].(sdk.MultiRecord)
	is.Equal(len(mr), 4)

	markers := []string{"m0", "m1", "m2", "m3"}
	for i, r := range mr {
		after := r.Payload.After.(opencdc.StructuredData)
		nestedMap(after["items"].([]any)[0])["sku"] = markers[i]
		after["items"].([]any)[1] = markers[i]
		nestedMap(after["nested"])["x"] = markers[i]
		nestedMap(after["nested"])["items"].([]any)[0] = markers[i]
		after["blob"].([]byte)[0] = markers[i][1]
		r.Key.(opencdc.StructuredData)["ids"].([]any)[0] = markers[i]
	}
	for i, r := range mr {
		after := r.Payload.After.(opencdc.StructuredData)
		is.Equal(nestedMap(after["items"].([]any)[0])["sku"], markers[i])
		is.Equal(after["items"].([]any)[1], markers[i])
		is.Equal(nestedMap(after["nested"])["x"], markers[i])
		is.Equal(nestedMap(after["nested"])["items"].([]any)[0], markers[i])
		is.Equal(after["blob"].([]byte)[0], markers[i][1])
		is.Equal(r.Key.(opencdc.StructuredData)["ids"].([]any)[0], markers[i])
	}
	is.Equal(string(in.Bytes()), string(deepCloneRecord().Bytes())) // the input record is untouched
}

// TestCloneProcessor_CloneThenFieldSetNested is the second symptom in #3151:
// the shallow Clone retyped nested map[string]any to opencdc.StructuredData,
// and processor-sdk's field reference only walks map[string]any, so field.set
// on a nested field returned an ErrorRecord for every record that went through
// a clone (every fan-out branch and every pipeline with a clone step).
func TestCloneProcessor_CloneThenFieldSetNested(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	cp := NewCloneProcessor(log.Nop())
	is.NoErr(cp.Configure(ctx, config.Config{"count": "1"}))
	mr := cp.Process(ctx, []opencdc.Record{deepCloneRecord()})[0].(sdk.MultiRecord)

	fs := field.NewSetProcessor(log.Nop())
	is.NoErr(fs.Configure(ctx, config.Config{"field": ".Payload.After.nested.x", "value": "2"}))
	is.NoErr(fs.Open(ctx))

	for i, r := range mr {
		out := fs.Process(ctx, []opencdc.Record{r})[0]
		sr, ok := out.(sdk.SingleRecord)
		if !ok {
			t.Fatalf("clone %d: field.set on a nested field after clone: %#v", i, out)
		}
		is.Equal(nestedMap(sr.Payload.After.(opencdc.StructuredData)["nested"])["x"], "2")
	}
}

// TestSplitProcessor_OutputsDoNotShareNestedData: split copies the whole
// record for every item and then replaces one field, so the fields it does
// not touch are exactly what a shallow Clone would share between the pieces.
func TestSplitProcessor_OutputsDoNotShareNestedData(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	sp := NewSplitProcessor(log.Nop())
	is.NoErr(sp.Configure(ctx, config.Config{"field": ".Payload.After.items"}))
	in := deepCloneRecord()
	mr := sp.Process(ctx, []opencdc.Record{in})[0].(sdk.MultiRecord)
	is.Equal(len(mr), 2)

	markers := []string{"m0", "m1"}
	for i, r := range mr {
		after := r.Payload.After.(opencdc.StructuredData)
		nestedMap(after["nested"])["x"] = markers[i]
		nestedMap(after["nested"])["items"].([]any)[0] = markers[i]
		after["blob"].([]byte)[0] = markers[i][1]
		r.Key.(opencdc.StructuredData)["ids"].([]any)[0] = markers[i]
	}
	for i, r := range mr {
		after := r.Payload.After.(opencdc.StructuredData)
		is.Equal(nestedMap(after["nested"])["x"], markers[i])
		is.Equal(nestedMap(after["nested"])["items"].([]any)[0], markers[i])
		is.Equal(after["blob"].([]byte)[0], markers[i][1])
		is.Equal(r.Key.(opencdc.StructuredData)["ids"].([]any)[0], markers[i])
	}
	is.Equal(string(in.Bytes()), string(deepCloneRecord().Bytes()))
}

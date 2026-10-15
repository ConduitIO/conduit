// Copyright © 2025 Meroxa, Inc.
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
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestSplitProcessor_Process_Success(t *testing.T) {
	ctx := context.Background()
	testCases := []struct {
		name   string
		field  string
		record opencdc.Record
		want   sdk.MultiRecord
	}{
		{
			name:  "single field",
			field: ".Key.myslice",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"myslice": []int{1, 2, 3}},
			},
			want: sdk.MultiRecord{
				opencdc.Record{
					Metadata: map[string]string{"split.index": "0"},
					Key:      opencdc.StructuredData{"myslice": 1},
				},
				opencdc.Record{
					Metadata: map[string]string{"split.index": "1"},
					Key:      opencdc.StructuredData{"myslice": 2},
				},
				opencdc.Record{
					Metadata: map[string]string{"split.index": "2"},
					Key:      opencdc.StructuredData{"myslice": 3},
				},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			logger := log.Test(t)
			proc := NewSplitProcessor(logger)

			cfg := config.Config{"field": tc.field}
			err := proc.Configure(ctx, cfg)
			is.NoErr(err)

			output := proc.Process(ctx, []opencdc.Record{tc.record})
			is.True(len(output) == 1)
			is.Equal(output[0], tc.want)
		})
	}
}

func TestSplitProcessor_Process_Fail(t *testing.T) {
	ctx := context.Background()
	testCases := []struct {
		name    string
		field   string
		record  opencdc.Record
		wantErr string
	}{
		{
			name:  "not a slice",
			field: ".Key.id",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": "123"},
			},
			wantErr: `field ".Key.id" is not a slice`,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			logger := log.Test(t)
			proc := NewSplitProcessor(logger)

			cfg := config.Config{"field": tc.field}
			err := proc.Configure(ctx, cfg)
			is.NoErr(err)

			output := proc.Process(ctx, []opencdc.Record{tc.record})
			is.True(len(output) == 1)
			rec, ok := output[0].(sdk.ErrorRecord)
			is.True(ok)
			is.True(strings.Contains(rec.Error.Error(), tc.wantErr))
		})
	}
}

func TestSplitProcessor_Configure_Fail(t *testing.T) {
	proc := NewSplitProcessor(log.Nop())
	ctx := context.Background()
	testCases := []struct {
		name string
		cfg  config.Config
	}{
		{
			name: "invalid config, contains an invalid prefix for the field",
			cfg:  config.Config{"field": ".Metadata.foo"},
		}, {
			name: "invalid config, invalid prefix",
			cfg:  config.Config{"field": "aPayload.foo"},
		}, {
			name: "missing param",
			cfg:  config.Config{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			err := proc.Configure(ctx, tc.cfg)
			is.True(err != nil)
		})
	}
}

// Regression tests for #3152: split on a nested field panicked the engine
// because Record.Clone retypes nested map[string]any to opencdc.StructuredData,
// the reference resolver only walks map[string]any, and the second Resolve's
// error was discarded.

// processNoPanic runs Process and turns a panic into a test failure, so that a
// regression shows up as a failed assertion instead of killing the test binary.
func processNoPanic(t *testing.T, proc *SplitProcessor, recs []opencdc.Record) (out []sdk.ProcessedRecord) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("split.Process panicked: %v", r)
		}
	}()
	return proc.Process(context.Background(), recs)
}

// asMap views either kind of nested map (same storage) as map[string]any.
func asMap(v any) map[string]any {
	switch v := v.(type) {
	case map[string]any:
		return v
	case opencdc.StructuredData:
		return v
	}
	panic(fmt.Sprintf("not a map: %T", v))
}

func TestSplitProcessor_Process_NestedField(t *testing.T) {
	ctx := context.Background()

	items := func() []any { return []any{"a", "b"} }
	after := func(d opencdc.StructuredData) opencdc.Record {
		return opencdc.Record{Payload: opencdc.Change{After: d}}
	}
	// holder returns the map that contains the split field.
	afterNested := func(r opencdc.Record) map[string]any {
		return asMap(r.Payload.After.(opencdc.StructuredData)["nested"])
	}

	testCases := []struct {
		name   string
		field  string
		record func() opencdc.Record
		holder func(opencdc.Record) map[string]any
	}{
		{
			name:  "payload after, nested map[string]any",
			field: ".Payload.After.nested.items",
			record: func() opencdc.Record {
				return after(opencdc.StructuredData{
					"id":     1,
					"nested": map[string]any{"x": 1, "items": items()},
				})
			},
			holder: afterNested,
		},
		{
			// What a record looks like after Record.Clone() in
			// conduit-commons before the deep-copy fix.
			name:  "payload after, nested opencdc.StructuredData",
			field: ".Payload.After.nested.items",
			record: func() opencdc.Record {
				return after(opencdc.StructuredData{
					"id":     1,
					"nested": opencdc.StructuredData{"x": 1, "items": items()},
				})
			},
			holder: afterNested,
		},
		{
			name:  "payload after, three levels deep",
			field: ".Payload.After.nested.deeper.items",
			record: func() opencdc.Record {
				return after(opencdc.StructuredData{
					"id": 1,
					"nested": map[string]any{
						"x":      1,
						"deeper": map[string]any{"items": items()},
					},
				})
			},
			holder: func(r opencdc.Record) map[string]any {
				return asMap(afterNested(r)["deeper"])
			},
		},
		{
			name:  "payload before",
			field: ".Payload.Before.nested.items",
			record: func() opencdc.Record {
				return opencdc.Record{Payload: opencdc.Change{Before: opencdc.StructuredData{
					"id":     1,
					"nested": map[string]any{"x": 1, "items": items()},
				}}}
			},
			holder: func(r opencdc.Record) map[string]any {
				return asMap(r.Payload.Before.(opencdc.StructuredData)["nested"])
			},
		},
		{
			name:  "key",
			field: ".Key.nested.items",
			record: func() opencdc.Record {
				return opencdc.Record{Key: opencdc.StructuredData{
					"id":     1,
					"nested": map[string]any{"x": 1, "items": items()},
				}}
			},
			holder: func(r opencdc.Record) map[string]any {
				return asMap(r.Key.(opencdc.StructuredData)["nested"])
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			proc := NewSplitProcessor(log.Nop())
			is.NoErr(proc.Configure(ctx, config.Config{"field": tc.field}))

			in := tc.record()
			out := processNoPanic(t, proc, []opencdc.Record{in})
			is.Equal(len(out), 1)
			mr, ok := out[0].(sdk.MultiRecord)
			if !ok {
				t.Fatalf("want MultiRecord, got %T (%v)", out[0], out[0])
			}
			is.Equal(len(mr), 2)

			// The input must not have been modified.
			is.Equal(in, tc.record())

			// Each output record carries one item in place of the slice.
			for i, want := range []string{"a", "b"} {
				is.Equal(mr[i].Metadata["split.index"], strconv.Itoa(i))
				is.Equal(tc.holder(mr[i])["items"], want)
			}

			// Output records must not share nested maps: editing one leaves
			// its sibling alone.
			if _, ok := tc.holder(mr[0])["x"]; ok {
				tc.holder(mr[0])["x"] = "edited"
				is.Equal(tc.holder(mr[1])["x"], 1)
			}
		})
	}
}

func TestSplitProcessor_Process_NestedField_Fail(t *testing.T) {
	ctx := context.Background()
	testCases := []struct {
		name    string
		field   string
		record  opencdc.Record
		wantErr string
	}{
		{
			name:  "parent is not structured data",
			field: ".Payload.After.nested.items",
			record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{
				"nested": "a string",
			}}},
			wantErr: "nested",
		},
		{
			name:  "nested field is not a slice",
			field: ".Payload.After.nested.items",
			record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{
				"nested": map[string]any{"items": "nope"},
			}}},
			wantErr: "is not a slice",
		},
		{
			name:  "nested field is missing",
			field: ".Payload.After.nested.items",
			record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{
				"other": 1,
			}}},
			wantErr: "is not a slice",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			proc := NewSplitProcessor(log.Nop())
			is.NoErr(proc.Configure(ctx, config.Config{"field": tc.field}))

			out := processNoPanic(t, proc, []opencdc.Record{tc.record})
			is.Equal(len(out), 1)
			er, ok := out[0].(sdk.ErrorRecord)
			if !ok {
				t.Fatalf("want ErrorRecord, got %T", out[0])
			}
			is.True(strings.Contains(er.Error.Error(), tc.wantErr))
		})
	}
}

// Output must have exactly the types Record.Clone produces (that is what split
// emitted for top-level fields before #3152), whichever conduit-commons
// version is in use, and must not share anything with the input or between
// siblings, typed slices included.
func TestSplitProcessor_Process_MatchesClone(t *testing.T) {
	ctx := context.Background()
	newRec := func() opencdc.Record {
		return opencdc.Record{
			Position: opencdc.Position("p"),
			Metadata: opencdc.Metadata{"k": "v"},
			Key:      opencdc.StructuredData{"id": 1},
			Payload: opencdc.Change{
				Before: opencdc.StructuredData(nil),
				After: opencdc.StructuredData{
					"items":  []any{"a", map[string]any{"m": []string{"x"}}},
					"plain":  map[string]any{"deep": map[string]any{"z": 1}},
					"named":  opencdc.StructuredData{"deep": map[string]any{"z": 1}},
					"tags":   []string{"t1", "t2"},
					"list":   []map[string]any{{"q": 1}},
					"anys":   []any{map[string]any{"r": 1}},
					"nilmap": map[string]any(nil),
					"nilsd":  opencdc.StructuredData(nil),
					"nilsl":  []string(nil),
				},
			},
		}
	}

	proc := NewSplitProcessor(log.Nop())
	is := is.New(t)
	is.NoErr(proc.Configure(ctx, config.Config{"field": ".Payload.After.items"}))

	in := newRec()
	out := processNoPanic(t, proc, []opencdc.Record{in})
	is.Equal(len(out), 1)
	mr, ok := out[0].(sdk.MultiRecord)
	if !ok {
		t.Fatalf("want MultiRecord, got %T (%v)", out[0], out[0])
	}
	is.Equal(len(mr), 2)
	is.Equal(in, newRec()) // input untouched

	for i, item := range newRec().Payload.After.(opencdc.StructuredData)["items"].([]any) {
		want := newRec().Clone()
		want.Payload.After.(opencdc.StructuredData)["items"] = item
		want.Metadata["split.index"] = strconv.Itoa(i)
		if !reflect.DeepEqual(mr[i], want) {
			t.Fatalf("output %d differs from Clone (types included):\n got %#v\nwant %#v", i, mr[i], want)
		}
	}

	// No sharing: edit every container of output 0, nothing else may change.
	a0 := mr[0].Payload.After.(opencdc.StructuredData)
	a0["tags"].([]string)[0] = "edited"
	a0["list"].([]map[string]any)[0]["q"] = "edited"
	a0["anys"].([]any)[0].(map[string]any)["r"] = "edited"
	asMap(asMap(a0["plain"])["deep"])["z"] = "edited"
	asMap(asMap(a0["named"])["deep"])["z"] = "edited"
	mr[0].Metadata["k"] = "edited"
	is.Equal(in, newRec())
	// output 1 is compared with a fresh expectation
	want1 := newRec().Clone()
	want1.Payload.After.(opencdc.StructuredData)["items"] = newRec().Payload.After.(opencdc.StructuredData)["items"].([]any)[1]
	want1.Metadata["split.index"] = "1"
	if !reflect.DeepEqual(mr[1], want1) {
		t.Fatalf("output 1 changed when output 0 was edited: %#v", mr[1])
	}

	// A slice item that is itself a container is not shared with the input.
	mr[1].Payload.After.(opencdc.StructuredData)["items"].(map[string]any)["m"].([]string)[0] = "edited"
	is.Equal(in, newRec())
}

// A typed-nil structured value with a field whose parents are missing used to
// reach the resolver's nil-map assignment and panic (no recover on this path).
func TestSplitProcessor_Process_NilStructuredData(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		field string
		rec   opencdc.Record
	}{
		{".Key.a.b.items", opencdc.Record{Key: opencdc.StructuredData(nil)}},
		{".Payload.Before.a.b.items", opencdc.Record{Payload: opencdc.Change{Before: opencdc.StructuredData(nil)}}},
		{".Payload.After.a.b.items", opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData(nil)}}},
		{".Payload.After.a.b.items", opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"a": opencdc.StructuredData(nil)}}}},
		{".Payload.After.a.b.items", opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"a": map[string]any(nil)}}}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			is := is.New(t)
			proc := NewSplitProcessor(log.Nop())
			is.NoErr(proc.Configure(ctx, config.Config{"field": tc.field}))
			out := processNoPanic(t, proc, []opencdc.Record{tc.rec})
			is.Equal(len(out), 1)
			_, ok := out[0].(sdk.ErrorRecord)
			if !ok {
				t.Fatalf("want ErrorRecord, got %T", out[0])
			}
		})
	}
}

// When a record fails, split returns what it has so far; the funnel retries the
// records after it. Pins the existing behaviour.
func TestSplitProcessor_Process_FailsMidBatch(t *testing.T) {
	ctx := context.Background()
	is := is.New(t)
	proc := NewSplitProcessor(log.Nop())
	is.NoErr(proc.Configure(ctx, config.Config{"field": ".Payload.After.nested.items"}))

	good := func() opencdc.Record {
		return opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{
			"nested": map[string]any{"items": []any{1, 2}},
		}}}
	}
	bad := opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"nested": "str"}}}

	out := processNoPanic(t, proc, []opencdc.Record{good(), bad, good()})
	is.Equal(len(out), 2)
	_, ok := out[0].(sdk.MultiRecord)
	is.True(ok)
	_, ok = out[1].(sdk.ErrorRecord)
	is.True(ok)
}

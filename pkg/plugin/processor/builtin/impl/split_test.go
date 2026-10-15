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

func TestSplitProcessor_Process_NestedField(t *testing.T) {
	ctx := context.Background()

	items := func() []any { return []any{"a", "b"} }
	after := func(d opencdc.StructuredData) opencdc.Record {
		return opencdc.Record{Payload: opencdc.Change{After: d}}
	}
	// holder returns the map that contains the split field.
	afterNested := func(r opencdc.Record) map[string]any {
		return r.Payload.After.(opencdc.StructuredData)["nested"].(map[string]any)
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
				return afterNested(r)["deeper"].(map[string]any)
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
				return r.Payload.Before.(opencdc.StructuredData)["nested"].(map[string]any)
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
				return r.Key.(opencdc.StructuredData)["nested"].(map[string]any)
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

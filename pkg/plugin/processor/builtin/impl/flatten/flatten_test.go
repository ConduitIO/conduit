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

package flatten

import (
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func schemaMD() opencdc.Metadata {
	return opencdc.Metadata{
		opencdc.MetadataKeySchemaSubject:     "k",
		opencdc.MetadataKeySchemaVersion:     "1",
		opencdc.MetadataPayloadSchemaSubject: "p",
		opencdc.MetadataPayloadSchemaVersion: "2",
		"other":                              "kept",
	}
}

func TestFlatten_Process(t *testing.T) {
	ctx := context.Background()
	testCases := []struct {
		name   string
		config config.Config
		record opencdc.Record
		want   opencdc.Record
	}{{
		name:   "nested payload, default delimiter",
		config: config.Config{},
		record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{
			"id": 1,
			"content": map[string]any{
				"name": map[string]any{"first": "David", "middle": nil, "last": "Wong"},
			},
		}}},
		want: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{
			"id":                  1,
			"content.name.first":  "David",
			"content.name.middle": nil, // a null leaf is kept
			"content.name.last":   "Wong",
		}}},
	}, {
		name:   "underscore delimiter",
		config: config.Config{"delimiter": "_"},
		record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{
			"a": map[string]any{"b": map[string]any{"c": true}},
		}}},
		want: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"a_b_c": true}}},
	}, {
		name:   "multi-character delimiter",
		config: config.Config{"delimiter": "__"},
		record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{
			"a": map[string]any{"b": 1},
		}}},
		want: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"a__b": 1}}},
	}, {
		name:   "arrays are kept as they are",
		config: config.Config{},
		record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{
			"items":  []any{1, 2, 3},
			"nested": map[string]any{"arr": []any{map[string]any{"x": 1}}},
		}}},
		want: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{
			"items":      []any{1, 2, 3},
			"nested.arr": []any{map[string]any{"x": 1}},
		}}},
	}, {
		name:   "an empty nested map produces no field",
		config: config.Config{},
		record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{
			"a": map[string]any{}, "b": opencdc.StructuredData{}, "c": map[string]any{"d": map[string]any{}}, "e": 1,
		}}},
		want: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"e": 1}}},
	}, {
		name:   "nested StructuredData is flattened",
		config: config.Config{},
		record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{
			"a": opencdc.StructuredData{"b": 2},
		}}},
		want: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"a.b": 2}}},
	}, {
		name:   "an already flat payload is unchanged except for schema metadata",
		config: config.Config{},
		record: opencdc.Record{Metadata: schemaMD(), Payload: opencdc.Change{After: opencdc.StructuredData{"a": 1}}},
		want: opencdc.Record{
			Metadata: opencdc.Metadata{opencdc.MetadataKeySchemaSubject: "k", opencdc.MetadataKeySchemaVersion: "1", "other": "kept"},
			Payload:  opencdc.Change{After: opencdc.StructuredData{"a": 1}},
		},
	}, {
		name:   "the key, dropping only the key schema metadata",
		config: config.Config{"field": ".Key"},
		record: opencdc.Record{Metadata: schemaMD(), Key: opencdc.StructuredData{"a": map[string]any{"b": "c"}}},
		want: opencdc.Record{
			Metadata: opencdc.Metadata{opencdc.MetadataPayloadSchemaSubject: "p", opencdc.MetadataPayloadSchemaVersion: "2", "other": "kept"},
			Key:      opencdc.StructuredData{"a.b": "c"},
		},
	}, {
		name:   "the before-image leaves the after-image alone",
		config: config.Config{"field": ".Payload.Before"},
		record: opencdc.Record{Payload: opencdc.Change{
			Before: opencdc.StructuredData{"a": map[string]any{"b": 1}},
			After:  opencdc.StructuredData{"a": map[string]any{"b": 2}},
		}},
		want: opencdc.Record{Payload: opencdc.Change{
			Before: opencdc.StructuredData{"a.b": 1},
			After:  opencdc.StructuredData{"a": map[string]any{"b": 2}},
		}},
	}, {
		name:   "a nil after-image (delete) passes through with its schema metadata",
		config: config.Config{},
		record: opencdc.Record{Operation: opencdc.OperationDelete, Metadata: schemaMD(), Payload: opencdc.Change{Before: opencdc.StructuredData{"a": map[string]any{"b": 1}}}},
		want:   opencdc.Record{Operation: opencdc.OperationDelete, Metadata: schemaMD(), Payload: opencdc.Change{Before: opencdc.StructuredData{"a": map[string]any{"b": 1}}}},
	}, {
		name:   "a nil key (tombstone) passes through",
		config: config.Config{"field": ".Key"},
		record: opencdc.Record{},
		want:   opencdc.Record{},
	}, {
		name:   "nil metadata",
		config: config.Config{"field": ".Key"},
		record: opencdc.Record{Key: opencdc.StructuredData{"a": map[string]any{"b": 1}}},
		want:   opencdc.Record{Key: opencdc.StructuredData{"a.b": 1}},
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			p := NewProcessor(log.Nop())
			is.NoErr(p.Configure(ctx, tc.config))
			out := p.Process(ctx, []opencdc.Record{tc.record})
			is.Equal(len(out), 1)
			got, ok := out[0].(sdk.SingleRecord)
			if !ok {
				t.Fatalf("expected a SingleRecord, got %#v", out[0])
			}
			is.Equal(opencdc.Record(got), tc.want)
		})
	}
}

func TestFlatten_Errors(t *testing.T) {
	ctx := context.Background()
	testCases := []struct {
		name     string
		config   config.Config
		record   opencdc.Record
		wantCode conduiterr.Code
		wantMsg  string
	}{{
		name:     "raw payload",
		config:   config.Config{},
		record:   opencdc.Record{Payload: opencdc.Change{After: opencdc.RawData(`{"SECRET":1}`)}},
		wantCode: CodeNotStructured,
		wantMsg:  ".Payload.After does not contain structured data",
	}, {
		name:     "raw key",
		config:   config.Config{"field": ".Key"},
		record:   opencdc.Record{Key: opencdc.RawData("SECRET")},
		wantCode: CodeNotStructured,
		wantMsg:  ".Key does not contain structured data",
	}, {
		name:   "a dotted name collides with a nested path",
		config: config.Config{},
		record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{
			"a.b": "SECRET-1", "a": map[string]any{"b": "SECRET-2"},
		}}},
		wantCode: CodeKeyCollision,
		wantMsg:  `.Payload.After.a.b and .Payload.After["a.b"] both flatten to the field name "a.b"`,
	}, {
		name:   "two nested paths collide with a custom delimiter",
		config: config.Config{"field": ".Key", "delimiter": "_"},
		record: opencdc.Record{Key: opencdc.StructuredData{
			"a_b": map[string]any{"c": 1}, "a": map[string]any{"b_c": 2},
		}},
		wantCode: CodeKeyCollision,
		wantMsg:  `.Key.a.b_c and .Key.a_b.c both flatten to the field name "a_b_c"`,
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			p := NewProcessor(log.Nop())
			is.NoErr(p.Configure(ctx, tc.config))
			in := tc.record.Clone()
			// Repeat: the error must not depend on map iteration order.
			for range 50 {
				out := p.Process(ctx, []opencdc.Record{tc.record})
				is.Equal(len(out), 1)
				er, ok := out[0].(sdk.ErrorRecord)
				if !ok {
					t.Fatalf("expected an ErrorRecord, got %#v", out[0])
				}
				var ce *conduiterr.ConduitError
				is.True(cerrors.As(er.Error, &ce))
				is.Equal(ce.Code, tc.wantCode)
				is.Equal(ce.Error(), tc.wantMsg)
				is.True(ce.Suggestion != "")
				is.True(!strings.Contains(ce.Error(), "SECRET")) // names, never values
				is.Equal(tc.record, in)                          // the failed record is not changed
			}
		})
	}
}

// A batch stops at the first failing record; the records before it are
// returned flattened.
func TestFlatten_BatchStopsAtError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	p := NewProcessor(log.Nop())
	is.NoErr(p.Configure(ctx, config.Config{}))
	out := p.Process(ctx, []opencdc.Record{
		{Payload: opencdc.Change{After: opencdc.StructuredData{"a": map[string]any{"b": 1}}}},
		{Payload: opencdc.Change{After: opencdc.RawData("x")}},
		{Payload: opencdc.Change{After: opencdc.StructuredData{"c": 1}}},
	})
	is.Equal(len(out), 2)
	is.Equal(out[0], sdk.SingleRecord{Payload: opencdc.Change{After: opencdc.StructuredData{"a.b": 1}}})
	_, ok := out[1].(sdk.ErrorRecord)
	is.True(ok)
}

func TestFlatten_Configure(t *testing.T) {
	ctx := context.Background()
	is := is.New(t)
	// An empty delimiter cannot be configured: the SDK applies the default.
	p := NewProcessor(log.Nop())
	is.NoErr(p.Configure(ctx, config.Config{"delimiter": ""}))
	is.Equal(p.delimiter, ".")
	is.True(NewProcessor(log.Nop()).Configure(ctx, config.Config{"field": ".Metadata"}) != nil)
	is.True(NewProcessor(log.Nop()).Configure(ctx, config.Config{"field": ".Payload.After.x"}) != nil)
	is.NoErr(NewProcessor(log.Nop()).Configure(ctx, config.Config{"field": ".Payload.Before"}))
}

func TestReference(t *testing.T) {
	is := is.New(t)
	is.Equal(reference(".Key", []string{"a", "b c", "d.e", "_f1", "1g", ""}), `.Key.a["b c"]["d.e"]._f1["1g"][""]`)
}

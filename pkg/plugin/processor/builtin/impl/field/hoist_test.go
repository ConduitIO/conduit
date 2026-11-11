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

package field

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/recordgen"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/matryer/is"
	"pgregory.net/rapid"
)

func hoistSchemaMD() opencdc.Metadata {
	return opencdc.Metadata{
		opencdc.MetadataKeySchemaSubject:     "k",
		opencdc.MetadataKeySchemaVersion:     "1",
		opencdc.MetadataPayloadSchemaSubject: "p",
		opencdc.MetadataPayloadSchemaVersion: "2",
		"other":                              "kept",
	}
}

func TestHoist_Process(t *testing.T) {
	ctx := context.Background()
	testCases := []struct {
		name   string
		config config.Config
		record opencdc.Record
		want   opencdc.Record
	}{{
		name:   "structured payload",
		config: config.Config{"name": "row"},
		record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"id": 1, "n": map[string]any{"a": nil}}}},
		want:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"row": map[string]any{"id": 1, "n": map[string]any{"a": nil}}}}},
	}, {
		name:   "raw payload becomes bytes",
		config: config.Config{"name": "raw"},
		record: opencdc.Record{Payload: opencdc.Change{After: opencdc.RawData("abc")}},
		want:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"raw": []byte("abc")}}},
	}, {
		name:   "a nil after-image is wrapped as null, as Kafka Connect does",
		config: config.Config{"name": "v"},
		record: opencdc.Record{Operation: opencdc.OperationDelete, Payload: opencdc.Change{Before: opencdc.StructuredData{"id": 1}}},
		want:   opencdc.Record{Operation: opencdc.OperationDelete, Payload: opencdc.Change{Before: opencdc.StructuredData{"id": 1}, After: opencdc.StructuredData{"v": nil}}},
	}, {
		name:   "a nil key (tombstone) is wrapped as null",
		config: config.Config{"field": ".Key", "name": "k"},
		record: opencdc.Record{},
		want:   opencdc.Record{Key: opencdc.StructuredData{"k": nil}},
	}, {
		name:   "a typed nil StructuredData is wrapped as null",
		config: config.Config{"field": ".Key", "name": "k"},
		record: opencdc.Record{Key: opencdc.StructuredData(nil)},
		want:   opencdc.Record{Key: opencdc.StructuredData{"k": nil}},
	}, {
		name:   "a nil before-image (insert) passes through with its schema metadata",
		config: config.Config{"field": ".Payload.Before", "name": "b"},
		record: opencdc.Record{Operation: opencdc.OperationCreate, Metadata: hoistSchemaMD(), Payload: opencdc.Change{After: opencdc.StructuredData{"id": 1}}},
		want:   opencdc.Record{Operation: opencdc.OperationCreate, Metadata: hoistSchemaMD(), Payload: opencdc.Change{After: opencdc.StructuredData{"id": 1}}},
	}, {
		name:   "a typed nil before-image passes through",
		config: config.Config{"field": ".Payload.Before", "name": "b"},
		record: opencdc.Record{Payload: opencdc.Change{Before: opencdc.StructuredData(nil)}},
		want:   opencdc.Record{Payload: opencdc.Change{Before: opencdc.StructuredData(nil)}},
	}, {
		name:   "RawData(nil) is bytes, not a nil side",
		config: config.Config{"field": ".Key", "name": "k"},
		record: opencdc.Record{Key: opencdc.RawData(nil)},
		want:   opencdc.Record{Key: opencdc.StructuredData{"k": []byte(nil)}},
	}, {
		name:   "an empty structure is wrapped",
		config: config.Config{"field": ".Key", "name": "k"},
		record: opencdc.Record{Key: opencdc.StructuredData{}},
		want:   opencdc.Record{Key: opencdc.StructuredData{"k": map[string]any{}}},
	}, {
		name:   "a name with a dot is one field name",
		config: config.Config{"name": "a.b"},
		record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"x": 1}}},
		want:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"a.b": map[string]any{"x": 1}}}},
	}, {
		name:   "the key drops only the key schema metadata",
		config: config.Config{"field": ".Key", "name": "k"},
		record: opencdc.Record{Metadata: hoistSchemaMD(), Key: opencdc.StructuredData{"id": 1}},
		want: opencdc.Record{
			Metadata: opencdc.Metadata{opencdc.MetadataPayloadSchemaSubject: "p", opencdc.MetadataPayloadSchemaVersion: "2", "other": "kept"},
			Key:      opencdc.StructuredData{"k": map[string]any{"id": 1}},
		},
	}, {
		name:   "the before-image drops the payload schema metadata and leaves the after-image",
		config: config.Config{"field": ".Payload.Before", "name": "b"},
		record: opencdc.Record{Metadata: hoistSchemaMD(), Payload: opencdc.Change{Before: opencdc.StructuredData{"id": 1}, After: opencdc.StructuredData{"id": 2}}},
		want: opencdc.Record{
			Metadata: opencdc.Metadata{opencdc.MetadataKeySchemaSubject: "k", opencdc.MetadataKeySchemaVersion: "1", "other": "kept"},
			Payload:  opencdc.Change{Before: opencdc.StructuredData{"b": map[string]any{"id": 1}}, After: opencdc.StructuredData{"id": 2}},
		},
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			p := NewHoistProcessor(log.Nop())
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

func TestHoist_Configure(t *testing.T) {
	ctx := context.Background()
	for name, cfg := range map[string]config.Config{
		"name missing":          {},
		"empty name":            {"name": ""},
		"metadata field":        {"name": "x", "field": ".Metadata"},
		"a field inside a side": {"name": "x", "field": ".Payload.After.a"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := NewHoistProcessor(log.Nop()).Configure(ctx, cfg); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// TestHoist_Property: over generated batches with nil, raw and structured
// sides, every record comes out with the hoisted side holding exactly the old
// value under the name (structured data deep-copied, raw data as bytes, nil
// as null), every other part of the record unchanged, and nothing shared
// with the input.
func TestHoist_Property(t *testing.T) {
	ctx := context.Background()
	rapid.Check(t, func(t *rapid.T) {
		field := rapid.SampledFrom([]string{".Key", ".Payload.Before", ".Payload.After"}).Draw(t, "field")
		p := NewHoistProcessor(log.Nop())
		if err := p.Configure(ctx, config.Config{"field": field, "name": "h"}); err != nil {
			t.Fatal(err)
		}
		in := recordgen.Batch().Draw(t, "records")
		orig := recordgen.Clone(in)
		out := p.Process(ctx, in)
		if len(out) != len(orig) {
			t.Fatalf("got %d records, want %d", len(out), len(orig))
		}
		for i, r := range out {
			got, ok := r.(sdk.SingleRecord)
			if !ok {
				t.Fatalf("record %d: %#v", i, r)
			}
			want := recordgen.Clone([]opencdc.Record{orig[i]})[0]
			side := hoistSide(&want, field)
			if *side == nil && field == ".Payload.Before" {
				// No before-image: the record passes through unchanged.
				if diff := cmp.Diff(want, opencdc.Record(got), cmpopts.IgnoreUnexported(opencdc.Record{})); diff != "" {
					t.Fatalf("record %d (-want +got):\n%s", i, diff)
				}
				continue
			}
			var v any
			switch d := (*side).(type) {
			case nil:
			case opencdc.StructuredData:
				v = map[string]any(d)
			default:
				v = d.Bytes()
			}
			*side = opencdc.StructuredData{"h": v}
			if field == ".Key" {
				delete(want.Metadata, opencdc.MetadataKeySchemaSubject)
				delete(want.Metadata, opencdc.MetadataKeySchemaVersion)
			} else {
				delete(want.Metadata, opencdc.MetadataPayloadSchemaSubject)
				delete(want.Metadata, opencdc.MetadataPayloadSchemaVersion)
			}
			if diff := cmp.Diff(want, opencdc.Record(got), cmpopts.IgnoreUnexported(opencdc.Record{})); diff != "" {
				t.Fatalf("record %d (-want +got):\n%s", i, diff)
			}
			// The hoisted value shares nothing with the input record.
			hoisted := (*hoistSide((*opencdc.Record)(&got), field)).(opencdc.StructuredData)["h"]
			if b, ok := hoisted.([]byte); ok && len(b) > 0 {
				b[0]++
				if raw, ok := (*hoistSide(&in[i], field)).(opencdc.RawData); !ok || raw[0] == b[0] {
					t.Fatalf("record %d: hoisted bytes alias the input", i)
				}
			}
			if m, ok := hoisted.(map[string]any); ok {
				m["__mutated"] = true
				if sd, ok := (*hoistSide(&in[i], field)).(opencdc.StructuredData); ok {
					if _, shared := sd["__mutated"]; shared {
						t.Fatalf("record %d: hoisted value aliases the input", i)
					}
				}
			}
		}
	})
}

func hoistSide(rec *opencdc.Record, field string) *opencdc.Data {
	switch field {
	case ".Key":
		return &rec.Key
	case ".Payload.Before":
		return &rec.Payload.Before
	default:
		return &rec.Payload.After
	}
}

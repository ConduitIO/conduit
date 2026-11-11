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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/recordgen"
	"github.com/matryer/is"
	"pgregory.net/rapid"
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

func TestSetField_Typed(t *testing.T) {
	ctx := context.Background()
	ts := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	testCases := []struct {
		name   string
		config config.Config
		record opencdc.Record
		want   opencdc.Record
	}{{
		name:   "int from a template",
		config: config.Config{"field": ".Payload.After.count", "value": "{{ .Payload.After.n }}", "type": "int"},
		record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"n": "42"}}},
		want:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"n": "42", "count": 42}}},
	}, {
		name:   "float",
		config: config.Config{"field": ".Payload.After.f", "value": "1.5", "type": "float"},
		record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{}}},
		want:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"f": 1.5}}},
	}, {
		name:   "bool",
		config: config.Config{"field": ".Key.active", "value": "true", "type": "bool"},
		record: opencdc.Record{Key: opencdc.StructuredData{}},
		want:   opencdc.Record{Key: opencdc.StructuredData{"active": true}},
	}, {
		name:   "time",
		config: config.Config{"field": ".Payload.After.ts", "value": "2026-11-10T12:00:00Z", "type": "time"},
		record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{}}},
		want:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"ts": ts}}},
	}, {
		name:   "explicit string type is the default behaviour",
		config: config.Config{"field": ".Payload.After.s", "value": "7", "type": "string"},
		record: opencdc.Record{Metadata: schemaMD(), Payload: opencdc.Change{After: opencdc.StructuredData{}}},
		want:   opencdc.Record{Metadata: schemaMD(), Payload: opencdc.Change{After: opencdc.StructuredData{"s": "7"}}},
	}, {
		name:   "from copies a scalar with its type",
		config: config.Config{"field": ".Payload.After.copy", "from": ".Payload.After.id"},
		record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"id": int64(7)}}},
		want:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"id": int64(7), "copy": int64(7)}}},
	}, {
		name:   "from copies nested data",
		config: config.Config{"field": ".Payload.After.copy", "from": ".Payload.After.src"},
		record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{
			"src": map[string]any{"a": []any{1, map[string]any{"b": "c"}}},
		}}},
		want: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{
			"src":  map[string]any{"a": []any{1, map[string]any{"b": "c"}}},
			"copy": map[string]any{"a": []any{1, map[string]any{"b": "c"}}},
		}}},
	}, {
		name:   "from a whole side into the key (ValueToKey)",
		config: config.Config{"field": ".Key", "from": ".Payload.After"},
		record: opencdc.Record{Key: opencdc.RawData("old"), Payload: opencdc.Change{After: opencdc.StructuredData{"id": 1}}},
		want:   opencdc.Record{Key: opencdc.StructuredData{"id": 1}, Payload: opencdc.Change{After: opencdc.StructuredData{"id": 1}}},
	}, {
		name:   "from a field into the whole side (ExtractField)",
		config: config.Config{"field": ".Payload.After", "from": ".Payload.After.row"},
		record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"row": map[string]any{"id": 1}, "x": 2}}},
		want:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"id": 1}}},
	}, {
		name:   "a null field is copied as null, never as a string",
		config: config.Config{"field": ".Payload.After.copy", "from": ".Payload.After.n"},
		record: opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"n": nil}}},
		want:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"n": nil, "copy": nil}}},
	}, {
		name:   "a null field into the key gives a nil key",
		config: config.Config{"field": ".Key", "from": ".Payload.After.n"},
		record: opencdc.Record{Key: opencdc.StructuredData{"id": 1}, Payload: opencdc.Change{After: opencdc.StructuredData{"n": nil}}},
		want:   opencdc.Record{Key: nil, Payload: opencdc.Change{After: opencdc.StructuredData{"n": nil}}},
	}, {
		name:   "from metadata into a payload field",
		config: config.Config{"field": ".Payload.After.topic", "from": `.Metadata["opencdc.collection"]`},
		record: opencdc.Record{Metadata: opencdc.Metadata{"opencdc.collection": "t"}, Payload: opencdc.Change{After: opencdc.StructuredData{}}},
		want:   opencdc.Record{Metadata: opencdc.Metadata{"opencdc.collection": "t"}, Payload: opencdc.Change{After: opencdc.StructuredData{"topic": "t"}}},
	}, {
		name:   "from a payload string into metadata keeps the schema metadata",
		config: config.Config{"field": ".Metadata.id", "from": ".Payload.After.id"},
		record: opencdc.Record{Metadata: schemaMD(), Payload: opencdc.Change{After: opencdc.StructuredData{"id": "x"}}},
		want: opencdc.Record{Metadata: func() opencdc.Metadata {
			md := schemaMD()
			md["id"] = "x"
			return md
		}(), Payload: opencdc.Change{After: opencdc.StructuredData{"id": "x"}}},
	}, {
		name:   "from a nil after-image into the key gives a nil key (tombstone)",
		config: config.Config{"field": ".Key", "from": ".Payload.After"},
		record: opencdc.Record{Key: opencdc.StructuredData{"id": 1}, Payload: opencdc.Change{Before: opencdc.StructuredData{"id": 1}}},
		want:   opencdc.Record{Key: nil, Payload: opencdc.Change{Before: opencdc.StructuredData{"id": 1}}},
	}, {
		name:   "from the nil key into a nil after-image keeps both nil",
		config: config.Config{"field": ".Payload.After", "from": ".Key"},
		record: opencdc.Record{},
		want:   opencdc.Record{},
	}, {
		name:   "from drops the payload schema metadata only",
		config: config.Config{"field": ".Payload.After.copy", "from": ".Key.id"},
		record: opencdc.Record{Metadata: schemaMD(), Key: opencdc.StructuredData{"id": 1}, Payload: opencdc.Change{After: opencdc.StructuredData{}}},
		want: opencdc.Record{
			Metadata: opencdc.Metadata{opencdc.MetadataKeySchemaSubject: "k", opencdc.MetadataKeySchemaVersion: "1", "other": "kept"},
			Key:      opencdc.StructuredData{"id": 1}, Payload: opencdc.Change{After: opencdc.StructuredData{"copy": 1}},
		},
	}, {
		name:   "a typed set on the key drops the key schema metadata only",
		config: config.Config{"field": "$.Key.n", "value": "1", "type": "int"},
		record: opencdc.Record{Metadata: schemaMD(), Key: opencdc.StructuredData{}},
		want: opencdc.Record{
			Metadata: opencdc.Metadata{opencdc.MetadataPayloadSchemaSubject: "p", opencdc.MetadataPayloadSchemaVersion: "2", "other": "kept"},
			Key:      opencdc.StructuredData{"n": 1},
		},
	}, {
		name:   "a typed set on a nil metadata map",
		config: config.Config{"field": ".Payload.Before.n", "value": "1", "type": "int"},
		record: opencdc.Record{},
		want:   opencdc.Record{Payload: opencdc.Change{Before: opencdc.StructuredData{"n": 1}}},
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			p := NewSetProcessor(log.Nop())
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

// TestSetField_FromNeverAliases: the copy and its source never share data,
// over generated payloads, and a nil or raw side is never replaced by {}.
func TestSetField_FromNeverAliases(t *testing.T) {
	ctx := context.Background()
	p := NewSetProcessor(log.Nop())
	if err := p.Configure(ctx, config.Config{"field": ".Key", "from": ".Payload.After"}); err != nil {
		t.Fatal(err)
	}
	rapid.Check(t, func(t *rapid.T) {
		for i, in := range recordgen.Batch().Draw(t, "records") {
			orig := recordgen.Clone([]opencdc.Record{in})[0]
			out := p.Process(ctx, []opencdc.Record{in})
			if len(out) != 1 {
				t.Fatalf("got %d records, want 1", len(out))
			}
			if _, raw := orig.Payload.After.(opencdc.RawData); raw {
				er, ok := out[0].(sdk.ErrorRecord)
				var ce *conduiterr.ConduitError
				if !ok || !cerrors.As(er.Error, &ce) || ce.Code != CodeSetSideNotStructured {
					t.Fatalf("record %d: raw after-image into the key: got %#v", i, out[0])
				}
				continue
			}
			rec, ok := out[0].(sdk.SingleRecord)
			if !ok {
				t.Fatalf("record %d: %#v", i, out[0])
			}
			want := orig.Payload.After
			if !cmpData(rec.Key, want) || !cmpData(rec.Payload.After, want) {
				t.Fatalf("record %d: key %#v, after %#v, want both %#v", i, rec.Key, rec.Payload.After, want)
			}
			if sd, ok := rec.Key.(opencdc.StructuredData); ok {
				sd["__mutated"] = true
				if _, shared := rec.Payload.After.(opencdc.StructuredData)["__mutated"]; shared {
					t.Fatalf("record %d: key aliases the after-image", i)
				}
			}
		}
	})
}

// TestReferenceFields_MatchesResolver checks that lookup walks the path the
// SDK resolver walks: the resolver writes a marker through each reference,
// and lookup must find exactly that marker.
func TestReferenceFields_MatchesResolver(t *testing.T) {
	for _, ref := range []string{
		".Key", ".key", "$.Key", ".Key.a", ".key.a.b", `.Key["a b"]`, `.Key["a.b"].c`, `.Key.a["x\"y"]`,
		".Payload.After", ".payload.after.x", ".Payload.after.x", ".payload.before", `.Payload.Before["k"]["l"]`,
		"$.Payload.After.a_1", ".Metadata.x", `.Metadata["opencdc.collection"]`, ".metadata.y", ".Payload.After.ünï",
	} {
		t.Run(ref, func(t *testing.T) {
			resolver, err := sdk.NewReferenceResolver(ref)
			if err != nil {
				t.Fatalf("invalid test reference: %v", err)
			}
			var rec opencdc.Record
			r, err := resolver.Resolve(&rec)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Set(opencdc.StructuredData{"marker": true}); err != nil {
				if err := r.Set("marker"); err != nil {
					t.Fatal(err)
				}
			}
			got, res := lookup(rec, referenceFields(ref))
			if res != lookupFound {
				t.Fatalf("lookup(%q) = %v, fields %q", ref, res, referenceFields(ref))
			}
			if !cmpAny(got, r.Get()) {
				t.Fatalf("lookup(%q) = %#v, want %#v", ref, got, r.Get())
			}
		})
	}
}

func cmpAny(a, b any) bool {
	return fmt.Sprintf("%#v", a) == fmt.Sprintf("%#v", b)
}

func cmpData(a, b opencdc.Data) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return string(mustJSON(a)) == string(mustJSON(b)) && typeName(a) == typeName(b)
}

func mustJSON(d opencdc.Data) []byte {
	if sd, ok := d.(opencdc.StructuredData); ok {
		return sd.Bytes()
	}
	return d.Bytes()
}

func typeName(d opencdc.Data) string {
	if _, ok := d.(opencdc.StructuredData); ok {
		return "structured"
	}
	return "raw"
}

func TestSetField_TypedErrors(t *testing.T) {
	ctx := context.Background()
	testCases := []struct {
		name     string
		config   config.Config
		record   opencdc.Record
		wantCode conduiterr.Code
		secret   string
	}{{
		name:     "type conversion fails",
		config:   config.Config{"field": ".Payload.After.n", "value": "{{ .Payload.After.s }}", "type": "int"},
		record:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"s": "SECRET-4711"}}},
		wantCode: CodeSetTypeConversion,
		secret:   "SECRET",
	}, {
		name:     "time conversion fails",
		config:   config.Config{"field": ".Payload.After.n", "value": "{{ .Payload.After.s }}", "type": "time"},
		record:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"s": "SECRET-4711"}}},
		wantCode: CodeSetTypeConversion,
		secret:   "SECRET",
	}, {
		name:     "an int cannot be the key",
		config:   config.Config{"field": ".Key", "from": ".Payload.After.id"},
		record:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"id": 4711}}},
		wantCode: CodeSetSideNotStructured,
		secret:   "4711",
	}, {
		name:     "a typed int cannot be a metadata value",
		config:   config.Config{"field": ".Metadata.n", "value": "4711", "type": "int"},
		record:   opencdc.Record{},
		wantCode: CodeSetValueNotSettable,
		secret:   "4711",
	}, {
		name:     "a map cannot be a metadata value",
		config:   config.Config{"field": ".Metadata.n", "from": ".Payload.After"},
		record:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"SECRET": 1}}},
		wantCode: CodeSetValueNotSettable,
		secret:   "SECRET",
	}, {
		name:     "a string cannot be a whole side",
		config:   config.Config{"field": ".Payload.After", "from": ".Payload.After.s"},
		record:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"s": "SECRET"}}},
		wantCode: CodeSetSideNotStructured,
		secret:   "SECRET",
	}, {
		name:     "a string cannot be the key",
		config:   config.Config{"field": "$.Key", "from": `.Metadata["k"]`},
		record:   opencdc.Record{Metadata: opencdc.Metadata{"k": "SECRET"}},
		wantCode: CodeSetSideNotStructured,
		secret:   "SECRET",
	}, {
		name:     "from crosses raw data",
		config:   config.Config{"field": ".Payload.After.x", "from": ".Key.id"},
		record:   opencdc.Record{Key: opencdc.RawData("SECRET"), Payload: opencdc.Change{After: opencdc.StructuredData{}}},
		wantCode: CodeSetSideNotStructured,
		secret:   "SECRET",
	}, {
		name:     "bytes cannot be a whole side",
		config:   config.Config{"field": ".Payload.After", "from": ".Payload.After.b"},
		record:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"b": []byte("SECRET")}}},
		wantCode: CodeSetSideNotStructured,
		secret:   "SECRET",
	}, {
		name:     "a typed int cannot be a whole side",
		config:   config.Config{"field": ".Payload.Before", "value": "4711", "type": "int"},
		record:   opencdc.Record{},
		wantCode: CodeSetSideNotStructured,
		secret:   "4711",
	}, {
		name:     "a typed set into a raw side",
		config:   config.Config{"field": ".Payload.After.n", "value": "1", "type": "int"},
		record:   opencdc.Record{Payload: opencdc.Change{After: opencdc.RawData("SECRET")}},
		wantCode: CodeSetSideNotStructured,
		secret:   "SECRET",
	}, {
		name:     "a copy into a raw side",
		config:   config.Config{"field": ".Key.id", "from": ".Payload.After.id"},
		record:   opencdc.Record{Key: opencdc.RawData("SECRET"), Payload: opencdc.Change{After: opencdc.StructuredData{"id": 1}}},
		wantCode: CodeSetSideNotStructured,
		secret:   "SECRET",
	}, {
		name:     "a missing field",
		config:   config.Config{"field": ".Payload.After.copy", "from": ".Payload.After.missing"},
		record:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"id": "SECRET"}}},
		wantCode: CodeSetFromMissing,
		secret:   "SECRET",
	}, {
		name:     "a missing parent is not created (#3147)",
		config:   config.Config{"field": ".Payload.After.copy", "from": ".Payload.After.a.b.c"},
		record:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"x": "SECRET"}}},
		wantCode: CodeSetFromMissing,
		secret:   "SECRET",
	}, {
		name:     "a field under a nil key is missing, and the key stays nil (#3147)",
		config:   config.Config{"field": ".Payload.After.k", "from": ".Key.id"},
		record:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{}}},
		wantCode: CodeSetFromMissing,
	}, {
		name:     "a field under a null parent is missing",
		config:   config.Config{"field": ".Payload.After.k", "from": ".Payload.After.a.b"},
		record:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"a": nil}}},
		wantCode: CodeSetFromMissing,
	}, {
		name:     "a field under a scalar is missing",
		config:   config.Config{"field": ".Payload.After.k", "from": ".Payload.After.a.b"},
		record:   opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"a": "SECRET"}}},
		wantCode: CodeSetFromMissing,
		secret:   "SECRET",
	}, {
		name:     "a missing metadata key",
		config:   config.Config{"field": ".Payload.After.k", "from": ".Metadata.nope"},
		record:   opencdc.Record{Metadata: opencdc.Metadata{"other": "SECRET"}, Payload: opencdc.Change{After: opencdc.StructuredData{}}},
		wantCode: CodeSetFromMissing,
		secret:   "SECRET",
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			p := NewSetProcessor(log.Nop())
			is.NoErr(p.Configure(ctx, tc.config))
			in := tc.record.Clone()
			out := p.Process(ctx, []opencdc.Record{tc.record})
			is.Equal(len(out), 1)
			er, ok := out[0].(sdk.ErrorRecord)
			if !ok {
				t.Fatalf("expected an ErrorRecord, got %#v", out[0])
			}
			var ce *conduiterr.ConduitError
			is.True(cerrors.As(er.Error, &ce))
			is.Equal(ce.Code, tc.wantCode)
			is.True(ce.Suggestion != "")
			is.True(strings.HasPrefix(ce.ConfigPath, "settings."))
			if tc.secret != "" {
				is.True(!strings.Contains(er.Error.Error(), tc.secret)) // the message names paths, never values
			}
			is.Equal(tc.record, in) // the input record is not changed
		})
	}
}

// A template error in a typed set keeps the tplerr.Exec wrapping.
func TestSetField_TypedTemplateError(t *testing.T) {
	is := is.New(t)
	p := NewSetProcessor(log.Nop())
	is.NoErr(p.Configure(context.Background(), config.Config{
		"field": ".Payload.After.out",
		"value": `{{ mustToDate "2006-01-02" (index .Metadata "d") }}`,
		"type":  "time",
	}))
	out := p.Process(context.Background(), []opencdc.Record{{
		Metadata: opencdc.Metadata{"d": "customer-SECRET-4711"},
		Payload:  opencdc.Change{After: opencdc.StructuredData{}},
	}})
	er, ok := out[0].(sdk.ErrorRecord)
	is.True(ok)
	is.True(strings.Contains(er.Error.Error(), "template execution failed"))
	is.True(!strings.Contains(er.Error.Error(), "SECRET"))
}

func TestSetField_ConfigureCodes(t *testing.T) {
	ctx := context.Background()
	testCases := []struct {
		name     string
		cfg      config.Config
		wantCode conduiterr.Code
		wantPath string
	}{
		{"value and from", config.Config{"field": ".Key", "value": "x", "from": ".Payload.After"}, CodeSetValueAndFrom, "settings.from"},
		{"neither value nor from", config.Config{"field": ".Key"}, CodeSetValueRequired, "settings.value"},
		{"empty value and no from", config.Config{"field": ".Key", "value": ""}, CodeSetValueRequired, "settings.value"},
		{"type with from", config.Config{"field": ".Key", "from": ".Payload.After", "type": "int"}, CodeSetTypeWithFrom, "settings.type"},
		{"explicit string type with from", config.Config{"field": ".Key", "from": ".Payload.After", "type": "string"}, CodeSetTypeWithFrom, "settings.type"},
		{"from is a template", config.Config{"field": ".Key", "from": "{{ .Payload.After.id }}"}, CodeSetFromInvalid, "settings.from"},
		{"from is not a reference", config.Config{"field": ".Key", "from": ".Payload.Middle"}, CodeSetFromInvalid, "settings.from"},
		{"from is the whole payload", config.Config{"field": ".Key", "from": ".Payload"}, CodeSetFromInvalid, "settings.from"},
		{"from is the whole record", config.Config{"field": ".Key", "from": "."}, CodeSetFromInvalid, "settings.from"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			err := NewSetProcessor(log.Nop()).Configure(ctx, tc.cfg)
			var ce *conduiterr.ConduitError
			if !cerrors.As(err, &ce) {
				t.Fatalf("expected a coded error, got %v", err)
			}
			is.Equal(ce.Code, tc.wantCode)
			is.Equal(ce.ConfigPath, tc.wantPath)
			is.True(ce.Suggestion != "")
		})
	}

	t.Run("unknown type", func(t *testing.T) {
		err := NewSetProcessor(log.Nop()).Configure(ctx, config.Config{"field": ".Key", "value": "1", "type": "decimal"})
		if err == nil {
			t.Fatal("expected an error")
		}
	})
}

// Configure replaces the previous configuration entirely: a processor
// reconfigured from copy mode to a template no longer copies.
func TestSetField_ReconfigureResets(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	p := NewSetProcessor(log.Nop())
	is.NoErr(p.Configure(ctx, config.Config{"field": ".Payload.After.x", "from": ".Payload.After.id"}))
	is.NoErr(p.Configure(ctx, config.Config{"field": ".Payload.After.x", "value": "lit"}))
	out := p.Process(ctx, []opencdc.Record{{Metadata: schemaMD(), Payload: opencdc.Change{After: opencdc.StructuredData{"id": 1}}}})
	is.Equal(out, []sdk.ProcessedRecord{sdk.SingleRecord{
		Metadata: schemaMD(), Payload: opencdc.Change{After: opencdc.StructuredData{"id": 1, "x": "lit"}},
	}})
}

func TestSchemaSideOf(t *testing.T) {
	for ref, want := range map[string]schemaSide{
		".Key": schemaSideKey, ".key.a": schemaSideKey, "$.Key.a": schemaSideKey, `.Key["a b"]`: schemaSideKey,
		".Payload.After": schemaSidePayload, ".payload.before.x": schemaSidePayload, "$.Payload.After.x": schemaSidePayload,
		".Metadata.x": schemaSideNone, ".Operation": schemaSideNone, `.Metadata["opencdc.key.x"]`: schemaSideNone,
	} {
		if got := schemaSideOf(ref); got != want {
			t.Errorf("schemaSideOf(%q) = %v, want %v", ref, got, want)
		}
	}
}

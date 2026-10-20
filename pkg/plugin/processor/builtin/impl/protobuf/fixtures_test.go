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

package protobuf

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/sr"
)

// fixtureRegistry serves a fixed root schema for SchemaByID and a subject map
// for SchemaBySubjectVersion, so reference fixtures can resolve imports.
type fixtureRegistry struct {
	schema   sr.Schema
	subjects map[string]sr.SubjectSchema
}

func (f *fixtureRegistry) CreateSchema(context.Context, string, sr.Schema) (sr.SubjectSchema, error) {
	return sr.SubjectSchema{}, cerrors.New("not used")
}

func (f *fixtureRegistry) SchemaByID(context.Context, int) (sr.Schema, error) {
	return f.schema, nil
}

func (f *fixtureRegistry) SchemaBySubjectVersion(_ context.Context, subject string, version int) (sr.SubjectSchema, error) {
	ss, ok := f.subjects[subject]
	if !ok || ss.Version != version {
		return sr.SubjectSchema{}, cerrors.New("subject not found")
	}
	return ss, nil
}

// wire builds the full Confluent wire: magic byte + schema ID + message-index
// array + payload.
func wire(t *testing.T, schemaID int, index []int, payload []byte) []byte {
	t.Helper()
	h, err := (&sr.ConfluentHeader{}).AppendEncode(nil, schemaID, index)
	is.New(t).NoErr(err)
	return append(h, payload...)
}

// decode runs the processor over one record and returns the decoded
// StructuredData or the error.
func decode(t *testing.T, registry *fixtureRegistry, payload []byte) (opencdc.StructuredData, error) {
	t.Helper()
	is := is.New(t)
	ctx := context.Background()

	p := NewDecodeProcessor(log.Nop())
	p.SetSchemaRegistry(registry)
	err := p.Configure(ctx, config.Config{"field": ".Payload.After"})
	is.NoErr(err)
	err = p.Open(ctx)
	is.NoErr(err)
	t.Cleanup(func() { _ = p.Teardown(ctx) })

	rec := opencdc.Record{Payload: opencdc.Change{After: opencdc.RawData(payload)}}
	got := p.Process(ctx, []opencdc.Record{rec})
	is.Equal(len(got), 1)
	switch pr := got[0].(type) {
	case sdk.ErrorRecord:
		return nil, pr.Error
	case sdk.SingleRecord:
		after, ok := pr.Payload.After.(opencdc.StructuredData)
		is.True(ok)
		return after, nil
	default:
		t.Fatalf("unexpected processed record type %T", got[0])
		return nil, nil
	}
}

func TestDecode_NestedMessage(t *testing.T) {
	is := is.New(t)
	// message A { string id = 1; } message B { A a = 1; int32 n = 2; }
	// Encode B (message index 1) with a = A{id:"x"}, n = 5.
	payload := []byte{0x0A, 0x03, 0x0A, 0x01, 'x', 0x10, 0x05}
	got, err := decode(t, &fixtureRegistry{schema: sr.Schema{
		Type: sr.TypeProtobuf,
		Schema: `syntax = "proto3";
message A { string id = 1; }
message B { A a = 1; int32 n = 2; }`,
	}}, wire(t, 1, []int{1}, payload))
	is.NoErr(err)
	is.Equal(got, opencdc.StructuredData{"a": opencdc.StructuredData{"id": "x"}, "n": int32(5)})
}

func TestDecode_Map(t *testing.T) {
	is := is.New(t)
	// message M { map<string, int32> m = 1; } with m = {"a": 1}.
	// Map entry: field 1 (key, string) + field 2 (value, int32).
	payload := []byte{0x0A, 0x05, 0x0A, 0x01, 'a', 0x10, 0x01}
	got, err := decode(t, &fixtureRegistry{schema: sr.Schema{
		Type:   sr.TypeProtobuf,
		Schema: `syntax = "proto3"; message M { map<string, int32> m = 1; }`,
	}}, wire(t, 1, []int{0}, payload))
	is.NoErr(err)
	// Map fields decode to a plain map[string]any, not opencdc.StructuredData.
	is.Equal(got, opencdc.StructuredData{"m": map[string]any{"a": int32(1)}})
}

func TestDecode_EnumByName(t *testing.T) {
	is := is.New(t)
	// enum E { A = 0; B = 1; } message M { E e = 1; } with e = B (wire 1).
	payload := []byte{0x08, 0x01}
	got, err := decode(t, &fixtureRegistry{schema: sr.Schema{
		Type:   sr.TypeProtobuf,
		Schema: `syntax = "proto3"; enum E { A = 0; B = 1; } message M { E e = 1; }`,
	}}, wire(t, 1, []int{0}, payload))
	is.NoErr(err)
	// Enums decode by name, matching protojson.
	is.Equal(got, opencdc.StructuredData{"e": "B"})
}

func TestDecode_UnknownField(t *testing.T) {
	is := is.New(t)
	// message M { string s = 1; } — payload carries field 2, which the schema
	// does not declare, and the default policy is reject.
	payload := []byte{0x10, 0x01}
	_, err := decode(t, &fixtureRegistry{schema: sr.Schema{
		Type:   sr.TypeProtobuf,
		Schema: `syntax = "proto3"; message M { string s = 1; }`,
	}}, wire(t, 1, []int{0}, payload))
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeUnknownField)
}

func TestDecode_MalformedSchema(t *testing.T) {
	is := is.New(t)
	// Deliberately malformed: unclosed message.
	payload := []byte{0x0A, 0x01, 'x'}
	_, err := decode(t, &fixtureRegistry{schema: sr.Schema{
		Type:   sr.TypeProtobuf,
		Schema: `syntax = "proto3"; message M {`,
	}}, wire(t, 1, []int{0}, payload))
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeSchemaCompile)
}

func TestDecode_Reference(t *testing.T) {
	is := is.New(t)
	// Root: import "common.proto"; message M { common.C c = 1; }
	// Referenced: package common; message C { string id = 1; }
	// Encode M.c = C{id:"x"} (message index 0).
	payload := []byte{0x0A, 0x03, 0x0A, 0x01, 'x'}
	got, err := decode(t, &fixtureRegistry{
		schema: sr.Schema{
			Type:   sr.TypeProtobuf,
			Schema: `syntax = "proto3"; import "common.proto"; message M { common.C c = 1; }`,
			References: []sr.SchemaReference{
				{Name: "common.proto", Subject: "common", Version: 1},
			},
		},
		subjects: map[string]sr.SubjectSchema{
			"common": {Subject: "common", Version: 1, Schema: sr.Schema{
				Type:   sr.TypeProtobuf,
				Schema: `syntax = "proto3"; package common; message C { string id = 1; }`,
			}},
		},
	}, wire(t, 1, []int{0}, payload))
	is.NoErr(err)
	is.Equal(got, opencdc.StructuredData{"c": opencdc.StructuredData{"id": "x"}})
}

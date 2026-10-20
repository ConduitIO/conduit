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
	protobufschema "github.com/conduitio/conduit-commons/schema/protobuf"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/sr"
)

func TestMapError(t *testing.T) {
	is := is.New(t)
	for _, tc := range []struct {
		name string
		err  error
		want conduiterr.Code
	}{
		{"schema compile", protobufschema.ErrSchemaCompile, CodeSchemaCompile},
		{"compile timeout", protobufschema.ErrCompileTimeout, CodeSchemaCompile},
		{"reference resolve", protobufschema.ErrReferenceResolve, CodeReferenceResolve},
		{"reference cycle", protobufschema.ErrReferenceCycle, CodeReferenceResolve},
		{"unresolved import", protobufschema.ErrUnresolvedImport, CodeReferenceResolve},
		{"message index", protobufschema.ErrMessageIndex, CodeMessageIndex},
		{"unknown field", protobufschema.ErrUnknownField, CodeUnknownField},
		{"wire format", protobufschema.ErrDecode, CodeWireFormat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mapError(tc.err)
			ce, ok := conduiterr.Get(got)
			is.True(ok)
			is.Equal(ce.Code, tc.want)
		})
	}

	t.Run("uncoded error passes through", func(t *testing.T) {
		got := mapError(cerrors.New("boom"))
		_, ok := conduiterr.Get(got)
		is.True(!ok)
	})
}

// fakeRegistry serves a fixed flat Protobuf schema for SchemaByID and no
// references.
type fakeRegistry struct {
	schema sr.Schema
}

func (f *fakeRegistry) CreateSchema(context.Context, string, sr.Schema) (sr.SubjectSchema, error) {
	return sr.SubjectSchema{}, cerrors.New("not used")
}

func (f *fakeRegistry) SchemaByID(context.Context, int) (sr.Schema, error) {
	return f.schema, nil
}

func (f *fakeRegistry) SchemaBySubjectVersion(context.Context, string, int) (sr.SubjectSchema, error) {
	return sr.SubjectSchema{}, cerrors.New("not used")
}

func TestDecodeProcessor_DecodesFlatMessage(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	// message M { string id = 1; } with id = "foo":
	// payload = 0x0A 0x03 'f' 'o' 'o'  (field 1, wire type 2, length 3).
	// Confluent wire = magic 0x00 + schema ID 1 (4 bytes BE) + empty
	// message-index array (varint 0) + payload.
	encoded := append([]byte{0x00, 0x00, 0x00, 0x00, 0x01, 0x00}, []byte{0x0A, 0x03, 'f', 'o', 'o'}...)

	underTest := NewDecodeProcessor(log.Nop())
	underTest.SetSchemaRegistry(&fakeRegistry{schema: sr.Schema{
		Type:   sr.TypeProtobuf,
		Schema: `syntax = "proto3"; message M { string id = 1; }`,
	}})
	err := underTest.Configure(ctx, config.Config{"field": ".Payload.After"})
	is.NoErr(err)
	err = underTest.Open(ctx)
	is.NoErr(err)
	t.Cleanup(func() { _ = underTest.Teardown(ctx) })

	rec := opencdc.Record{Payload: opencdc.Change{After: opencdc.RawData(encoded)}}
	got := underTest.Process(ctx, []opencdc.Record{rec})
	is.Equal(len(got), 1)

	single, ok := got[0].(sdk.SingleRecord)
	is.True(ok)
	after, ok := single.Payload.After.(opencdc.StructuredData)
	is.True(ok)
	is.Equal(after["id"], "foo")
}

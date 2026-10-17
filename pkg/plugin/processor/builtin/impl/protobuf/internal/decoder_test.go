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

package internal

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-commons/schema"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/sr"
)

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

func wire(t *testing.T, id int, index []int, payload []byte) []byte {
	t.Helper()
	h, err := (&sr.ConfluentHeader{}).AppendEncode(nil, id, index)
	is.New(t).NoErr(err)
	return append(h, payload...)
}

func TestDecoder_DecodeFlatMessage(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	d := NewDecoder(&fixtureRegistry{schema: sr.Schema{
		Type:   sr.TypeProtobuf,
		Schema: `syntax = "proto3"; message M { string id = 1; }`,
	}}, log.Nop())

	// payload = 0x0A 0x03 'f' 'o' 'o' (field 1, string "foo")
	payload := []byte{0x0A, 0x03, 'f', 'o', 'o'}
	got, err := d.Decode(ctx, opencdc.RawData(wire(t, 1, []int{0}, payload)))
	is.NoErr(err)
	is.Equal(got, opencdc.StructuredData{"id": "foo"})
}

func TestDecoder_DecodeUnknownField(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	d := NewDecoder(&fixtureRegistry{schema: sr.Schema{
		Type:   sr.TypeProtobuf,
		Schema: `syntax = "proto3"; message M { string s = 1; }`,
	}}, log.Nop())

	// field 2 is not declared
	payload := []byte{0x10, 0x01}
	_, err := d.Decode(ctx, opencdc.RawData(wire(t, 1, []int{0}, payload)))
	is.True(err != nil)
}

func TestRegistryResolver_ResolveReference(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	r := NewRegistryResolver(&fixtureRegistry{subjects: map[string]sr.SubjectSchema{
		"common": {Subject: "common", Version: 1, Schema: sr.Schema{
			Type:   sr.TypeProtobuf,
			Schema: `syntax = "proto3"; package common; message C { string id = 1; }`,
		}},
	}})

	sch, err := r.ResolveReference(ctx, schema.Reference{Name: "common.proto", Subject: "common", Version: 1})
	is.NoErr(err)
	is.Equal(sch.Type, schema.TypeProtobuf)

	// a missing subject fails
	_, err = r.ResolveReference(ctx, schema.Reference{Name: "nope.proto", Subject: "nope", Version: 1})
	is.True(err != nil)
}

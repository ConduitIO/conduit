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

package toschema

import (
	"testing"

	"github.com/conduitio/conduit-commons/schema"
	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/sr"
)

func TestSrSchemaType(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   sr.SchemaType
		want schema.Type
	}{
		{"avro", sr.TypeAvro, schema.TypeAvro},
		{"protobuf", sr.TypeProtobuf, schema.TypeProtobuf},
		{"json", sr.TypeJSON, 0},
		{"unknown", sr.SchemaType(99), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			is.Equal(SrSchemaType(tc.in), tc.want)
		})
	}
}

func TestSrSchema_Protobuf(t *testing.T) {
	is := is.New(t)
	got := SrSchema(sr.Schema{
		Type:   sr.TypeProtobuf,
		Schema: `syntax = "proto3"; message M { string id = 1; }`,
	})
	is.Equal(got.Type, schema.TypeProtobuf)
	is.Equal(string(got.Bytes), `syntax = "proto3"; message M { string id = 1; }`)
}

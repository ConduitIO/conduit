// Copyright © 2024 Meroxa, Inc.
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

package fromschema

import (
	"testing"

	"github.com/conduitio/conduit-commons/schema"
	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/sr"
)

func TestSrSchemaType(t *testing.T) {
	unknown := sr.SchemaType(-1)
	testCases := []struct {
		name string
		have schema.Type
		want sr.SchemaType
	}{
		{name: "avro", have: schema.TypeAvro, want: sr.TypeAvro},
		// Protobuf is unsupported toward a registry for now, same as before
		// commons v0.8.0 introduced the type.
		{name: "protobuf", have: schema.TypeProtobuf, want: unknown},
		{name: "unknown type", have: schema.Type(99), want: unknown},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			is.Equal(SrSchemaType(tc.have), tc.want)
		})
	}
}

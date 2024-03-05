// Copyright © 2023 Meroxa, Inc.
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

package schemaregistry

import (
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/avro/schemaregistry/avro"
	"github.com/lovromazgon/franz-go/pkg/sr"
)

type Schema interface {
	// Marshal returns the encoded representation of v.
	Marshal(v any) ([]byte, error)
	// Unmarshal parses encoded data and stores the result in the value pointed
	// to by v. If v is nil or not a pointer, Unmarshal returns an error.
	Unmarshal(b []byte, v any) error
	// String returns the textual representation of the schema.
	String() string
}

type SchemaFactory struct {
	// Parse takes the textual representation of the schema and parses it into
	// a Schema.
	Parse func(string) (Schema, error)
	// SchemaForType returns a Schema that matches the structure of v.
	SchemaForType func(v any) (Schema, error)
}

var DefaultSchemaFactories = map[sr.SchemaType]SchemaFactory{
	avro.Type: {
		Parse:         func(s string) (Schema, error) { return avro.Parse(s) },
		SchemaForType: func(v any) (Schema, error) { return avro.SchemaForType(v) },
	},
}

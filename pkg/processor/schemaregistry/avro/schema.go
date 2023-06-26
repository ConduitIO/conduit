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

package avro

import (
	"reflect"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/hamba/avro/v2"
	"github.com/lovromazgon/franz-go/pkg/sr"
)

const Type = sr.TypeAvro

// Schema represents an Avro schema. It exposes methods for marshaling and
// unmarshaling data.
type Schema struct {
	schema avro.Schema
	mur    MapUnionResolver
}

// Marshal returns the Avro encoding of v. Note that this function may mutate v.
// Limitations:
// - Map keys need to be of type string
// - Array values need to be of type uint8 (byte)
func (s *Schema) Marshal(v any) ([]byte, error) {
	err := s.mur.BeforeMarshal(v)
	if err != nil {
		return nil, err
	}
	bytes, err := avro.Marshal(s.schema, v)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

// Unmarshal parses the Avro encoded data and stores the result in the value
// pointed to by v. If v is nil or not a pointer, Unmarshal returns an error.
func (s *Schema) Unmarshal(b []byte, v any) error {
	err := avro.Unmarshal(s.schema, b, v)
	if err != nil {
		return err
	}
	err = s.mur.AfterUnmarshal(v)
	if err != nil {
		return err
	}
	return nil
}

// String returns the canonical form of the schema.
func (s *Schema) String() string {
	return s.schema.String()
}

// Sort fields in the schema. It can be used in tests to ensure the schemas can
// be compared.
func (s *Schema) Sort() {
	traverseSchema(s.schema, sortFn)
}

// Parse parses a schema string.
func Parse(text string) (*Schema, error) {
	schema, err := avro.Parse(text)
	if err != nil {
		return nil, cerrors.Errorf("could not parse avro schema: %w", err)
	}
	return &Schema{
		schema: schema,
		mur:    NewMapUnionResolver(schema),
	}, nil
}

// SchemaForType uses reflection to extract an Avro schema from v. Maps are
// regarded as structs.
func SchemaForType(v any) (*Schema, error) {
	schema, err := reflectInternal([]string{"record"}, reflect.ValueOf(v), reflect.TypeOf(v))
	if err != nil {
		return nil, err
	}
	traverseSchema(schema, sortFn)
	return &Schema{
		schema: schema,
		mur:    NewMapUnionResolver(schema),
	}, nil
}

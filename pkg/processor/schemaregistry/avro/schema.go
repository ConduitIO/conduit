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
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/hamba/avro/v2"
	"github.com/lovromazgon/franz-go/pkg/sr"
)

const Type = sr.TypeAvro

type Schema struct {
	schema avro.Schema
}

// Marshal returns the Avro encoding of v.
// Limitations:
// - Map keys need to be of type string
// - Array values need to be of type uint8 (byte)
func (s *Schema) Marshal(v any) ([]byte, error) {
	return avro.Marshal(s.schema, v)
}

// Unmarshal parses the Avro encoded data and stores the result in the value
// pointed to by v. If v is nil or not a pointer, Unmarshal returns an error.
func (s *Schema) Unmarshal(b []byte, v any) error {
	return avro.Unmarshal(s.schema, b, v)
}

// String returns the canonical form of the schema.
func (s *Schema) String() string {
	return s.schema.String()
}

// Sort fields in the schema. It can be used in tests to ensure the schemas can
// be compared.
func (s *Schema) Sort() {
	sortSchema(s.schema)
}

// Parse parses a schema string.
func Parse(text string) (*Schema, error) {
	s, err := avro.Parse(text)
	if err != nil {
		return nil, cerrors.Errorf("could not parse avro schema: %w", err)
	}
	return &Schema{schema: s}, nil
}

// SchemaForType uses reflection to extract an Avro schema from v. Maps are
// regarded as structs.
func SchemaForType(v any) (*Schema, error) {
	schema, err := reflectInternal([]string{"record"}, reflect.ValueOf(v), reflect.TypeOf(v))
	if err != nil {
		return nil, err
	}
	return &Schema{schema: schema}, nil
}

var (
	structuredDataType = reflect.TypeOf(record.StructuredData{})
	byteType           = reflect.TypeOf(byte(0))
)

//nolint:gocyclo // reflection requires a huge switch, it's fine
func reflectInternal(path []string, v reflect.Value, t reflect.Type) (avro.Schema, error) {
	if t == nil {
		return nil, cerrors.New("can't get schema for untyped nil") // untyped nil
	}
	switch t.Kind() {
	case reflect.Bool:
		return avro.NewPrimitiveSchema(avro.Boolean, nil), nil
	case reflect.Int64, reflect.Uint32:
		return avro.NewPrimitiveSchema(avro.Long, nil), nil
	case reflect.Int, reflect.Int32, reflect.Int16, reflect.Uint16, reflect.Int8, reflect.Uint8:
		return avro.NewPrimitiveSchema(avro.Int, nil), nil
	case reflect.Float32:
		return avro.NewPrimitiveSchema(avro.Float, nil), nil
	case reflect.Float64:
		return avro.NewPrimitiveSchema(avro.Double, nil), nil
	case reflect.String:
		return avro.NewPrimitiveSchema(avro.String, nil), nil
	case reflect.Pointer:
		var vElem reflect.Value
		if v.IsValid() {
			vElem = v.Elem()
		}
		s, err := reflectInternal(path, vElem, t.Elem())
		if err != nil {
			return nil, err
		}

		var schemas avro.Schemas
		if us, ok := s.(*avro.UnionSchema); ok && us.Nullable() {
			// it's already a nullable schema
			return s, nil
		} else if ok {
			// take types from union schema
			schemas = us.Types()
		} else if s.Type() != avro.Null {
			// non-nil type
			schemas = avro.Schemas{s}
		}

		s, err = avro.NewUnionSchema(append(schemas, &avro.NullSchema{}))
		if err != nil {
			return nil, err
		}

		return s, nil
	case reflect.Interface:
		if !v.IsValid() || v.IsNil() {
			// unknown type, fall back to nullable string
			return avro.NewUnionSchema([]avro.Schema{
				avro.NewPrimitiveSchema(avro.String, nil),
				&avro.NullSchema{},
			})
		}
		return reflectInternal(path, v.Elem(), v.Elem().Type())
	case reflect.Array:
		if t.Elem() != byteType {
			return nil, cerrors.Errorf("arrays with value type %v not supported, avro only supports bytes as values", t.Elem().String())
		}
		return avro.NewFixedSchema(strings.Join(path, "."), "", t.Len(), nil)
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return avro.NewPrimitiveSchema(avro.Bytes, nil), nil
		}

		var elemValue reflect.Value
		if v.Len() > 0 {
			elemValue = v.Index(0)
		}
		items, err := reflectInternal(append(path, "item"), elemValue, t.Elem())
		if err != nil {
			return nil, err
		}
		return avro.NewArraySchema(items), nil
	case reflect.Map:
		if t == structuredDataType {
			// special case - we treat StructuredData like a struct
			var fields []*avro.Field
			valType := t.Elem()
			for _, keyValue := range v.MapKeys() {
				fs, err := reflectInternal(append(path, keyValue.String()), v.MapIndex(keyValue), valType)
				if err != nil {
					return nil, err
				}
				field, err := avro.NewField(keyValue.String(), fs)
				if err != nil {
					return nil, err
				}
				fields = append(fields, field)
			}
			rs, err := avro.NewRecordSchema(strings.Join(path, "."), "", fields)
			if err != nil {
				return nil, err
			}
			return rs, nil
		}
		if t.Key().Kind() != reflect.String {
			return nil, cerrors.Errorf("%s: maps with key type %v not supported, avro only supports strings as keys", strings.Join(path, "."), t.Key().Kind())
		}
		// try getting value type based on the map type
		if t.Elem().Kind() != reflect.Interface {
			vs, err := reflectInternal(append(path, "value"), reflect.Value{}, t.Elem())
			if err != nil {
				return nil, err
			}
			return avro.NewMapSchema(vs), nil
		}

		// this is map[string]any, loop through all values and extracting their
		// types into a union schema, null is included by default
		types := []avro.Schema{&avro.NullSchema{}}
		typesSet := make(map[[32]byte]struct{})
		var valValue reflect.Value
		for _, kv := range v.MapKeys() {
			valValue = v.MapIndex(kv)
			vs, err := reflectInternal(append(path, "value"), valValue, t.Elem())
			if err != nil {
				return nil, err
			}
			if _, ok := typesSet[vs.Fingerprint()]; ok {
				continue
			}
			types = combineSchemaTypes(types, func(schema avro.Schema) bool {
				fp := vs.Fingerprint()
				if _, ok := typesSet[fp]; ok {
					return true
				}
				typesSet[fp] = struct{}{}
				return false
			}, vs)
		}
		if len(v.MapKeys()) == 0 {
			// it's an empty map, add string to types to have a valid schema
			types = append(types, avro.NewPrimitiveSchema(avro.String, nil))
		}
		vs, err := avro.NewUnionSchema(types)
		if err != nil {
			return nil, cerrors.Errorf("%s: %w", strings.Join(path, "."), err)
		}
		return avro.NewMapSchema(vs), nil
	case reflect.Struct:
		var fields []*avro.Field
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			name, ok := getStructFieldJSONName(sf)
			if !ok {
				continue // skip this field
			}
			var vfi reflect.Value
			if v.IsValid() {
				vfi = v.Field(i)
			}
			fs, err := reflectInternal(append(path, name), vfi, t.Field(i).Type)
			if err != nil {
				return nil, err
			}

			field, err := avro.NewField(name, fs)
			if err != nil {
				return nil, err
			}
			fields = append(fields, field)
		}
		rs, err := avro.NewRecordSchema(strings.Join(path, "."), "", fields)
		if err != nil {
			return nil, err
		}
		return rs, nil
	}
	// Invalid, Uintptr, UnsafePointer, Uint64, Uint, Complex64, Complex128, Chan, Func
	return nil, fmt.Errorf("unsupported type: %v", t)
}

func combineSchemaTypes(schemas []avro.Schema, exists func(avro.Schema) bool, additionalSchemas ...avro.Schema) []avro.Schema {
	if len(additionalSchemas) == 1 {
		schema := additionalSchemas[0]
		if us, ok := schema.(*avro.UnionSchema); ok {
			return combineSchemaTypes(schemas, exists, us.Types()...)
		}
		if !exists(schema) {
			schemas = append(schemas, schema)
		}
		return schemas
	}
	for _, schema := range additionalSchemas {
		schemas = combineSchemaTypes(schemas, exists, schema)
	}
	return schemas
}

func getStructFieldJSONName(sf reflect.StructField) (string, bool) {
	jsonTag := strings.Split(sf.Tag.Get("json"), ",")[0] // ignore tag options (omitempty)
	if jsonTag == "-" {
		return "", false
	}
	if jsonTag != "" {
		return jsonTag, true
	}
	return sf.Name, true
}

// sort is a utility for tests to ensure the schemas can be compared.
func sortSchema(s avro.Schema) {
	switch s.Type() {
	case avro.Record:
		sortRecordSchema(s.(*avro.RecordSchema))
	case avro.Map:
		sortMapSchema(s.(*avro.MapSchema))
	case avro.Union:
		sortUnionSchema(s.(*avro.UnionSchema))
	case avro.Array:
		sortArraySchema(s.(*avro.ArraySchema))
	}
}

func sortRecordSchema(s *avro.RecordSchema) {
	fields := s.Fields()
	sort.Slice(fields, func(i, j int) bool {
		return fields[i].Name() < fields[j].Name()
	})
	for i := range fields {
		sortSchema(fields[i].Type())
	}
}

func sortMapSchema(s *avro.MapSchema) {
	sortSchema(s.Values())
}

func sortArraySchema(s *avro.ArraySchema) {
	sortSchema(s.Items())
}

func sortUnionSchema(s *avro.UnionSchema) {
	schemas := s.Types()
	sort.Slice(schemas, func(i, j int) bool {
		return schemas[i].String() < schemas[j].String()
	})
	for i := range schemas {
		sortSchema(schemas[i])
	}
}

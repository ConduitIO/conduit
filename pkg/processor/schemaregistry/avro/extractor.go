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
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/hamba/avro/v2"
)

var (
	structuredDataType = reflect.TypeOf(record.StructuredData{})
	byteType           = reflect.TypeOf(byte(0))
)

// extractor exposes a way to extract an Avro schema from a Go value.
type extractor struct{}

func (e extractor) Extract(v reflect.Value, t reflect.Type) (avro.Schema, error) {
	return e.extractInternal([]string{"record"}, v, t)
}

func (e extractor) extractInternal(path []string, v reflect.Value, t reflect.Type) (avro.Schema, error) {
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
		s, err := e.extractInternal(path, vElem, t.Elem())
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
		return e.extractInternal(path, v.Elem(), v.Elem().Type())
	case reflect.Array:
		if t.Elem() != byteType {
			return nil, cerrors.Errorf("arrays with value type %v not supported, avro only supports bytes as values", t.Elem().String())
		}
		return avro.NewFixedSchema(strings.Join(path, "."), "", t.Len(), nil)
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return avro.NewPrimitiveSchema(avro.Bytes, nil), nil
		}

		// try getting value type based on the slice type
		if t.Elem().Kind() != reflect.Interface {
			vs, err := e.extractInternal(append(path, "item"), reflect.Value{}, t.Elem())
			if err != nil {
				return nil, err
			}
			return avro.NewArraySchema(vs), nil
		}

		// this is []any, loop through all values and extracting their types
		// into a union schema, null is included by default
		types := []avro.Schema{&avro.NullSchema{}}
		for i := 0; i < v.Len(); i++ {
			itemSchema, err := e.extractInternal(
				append(path, fmt.Sprintf("item%d", i)),
				v.Index(i), t.Elem(),
			)
			if err != nil {
				return nil, err
			}
			types = e.appendSchema(types, itemSchema)
		}
		if v.Len() == 0 {
			// it's an empty slice, add string to types to have a valid schema
			types = append(types, avro.NewPrimitiveSchema(avro.String, nil))
		}

		itemsSchema, err := avro.NewUnionSchema(types)
		if err != nil {
			return nil, cerrors.Errorf("%s: %w", strings.Join(path, "."), err)
		}
		return avro.NewArraySchema(itemsSchema), nil
	case reflect.Map:
		if t == structuredDataType {
			// special case - we treat StructuredData like a struct
			var fields []*avro.Field
			valType := t.Elem()
			for _, keyValue := range v.MapKeys() {
				fs, err := e.extractInternal(append(path, keyValue.String()), v.MapIndex(keyValue), valType)
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
			vs, err := e.extractInternal(append(path, "value"), reflect.Value{}, t.Elem())
			if err != nil {
				return nil, err
			}
			return avro.NewMapSchema(vs), nil
		}

		// this is map[string]any, loop through all values and extracting their
		// types into a union schema, null is included by default
		types := []avro.Schema{&avro.NullSchema{}}
		typesSet := make(map[[32]byte]struct{})
		for _, kv := range v.MapKeys() {
			valValue := v.MapIndex(kv)
			vs, err := e.extractInternal(append(path, "value"), valValue, t.Elem())
			if err != nil {
				return nil, err
			}
			if _, ok := typesSet[vs.Fingerprint()]; ok {
				continue
			}
			typesSet[vs.Fingerprint()] = struct{}{}
			types = e.appendSchema(types, vs)
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
			name, ok := e.getStructFieldJSONName(sf)
			if !ok {
				continue // skip this field
			}
			var vfi reflect.Value
			if v.IsValid() {
				vfi = v.Field(i)
			}
			fs, err := e.extractInternal(append(path, name), vfi, t.Field(i).Type)
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

func (e extractor) appendSchema(schemas []avro.Schema, additionalSchemas ...avro.Schema) []avro.Schema {
	if len(additionalSchemas) == 1 {
		schema := additionalSchemas[0]
		if us, ok := schema.(*avro.UnionSchema); ok {
			return e.appendSchema(schemas, us.Types()...)
		}
		for _, s := range schemas {
			if s.Type() == schema.Type() {
				// TODO we should be smarter about combining the schemas if the
				//  type matches, it could be that the type is map or array and
				//  we need to combine the underlying types
				return schemas // schema exists, don't add it
			}
		}
		// schema does not exist yet
		schemas = append(schemas, schema)
		return schemas
	}
	for _, schema := range additionalSchemas {
		schemas = e.appendSchema(schemas, schema)
	}
	return schemas
}

func (extractor) getStructFieldJSONName(sf reflect.StructField) (string, bool) {
	jsonTag := strings.Split(sf.Tag.Get("json"), ",")[0] // ignore tag options (omitempty)
	if jsonTag == "-" {
		return "", false
	}
	if jsonTag != "" {
		return jsonTag, true
	}
	return sf.Name, true
}

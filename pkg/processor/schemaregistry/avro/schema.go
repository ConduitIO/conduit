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

	"github.com/hamba/avro/v2"
)

// ExtractSchema uses reflection to extract an Avro schema from v.
func ExtractSchema(v any) (avro.Schema, error) {
	return reflectInternal([]string{"record"}, reflect.ValueOf(v), reflect.TypeOf(v))
}

// SortFields is a utility for tests to ensure the schemas can be compared.
func SortFields(s avro.Schema) {
	rs, ok := s.(*avro.RecordSchema)
	if !ok {
		return
	}
	fields := rs.Fields()
	sort.Slice(fields, func(i, j int) bool {
		return fields[i].Name() < fields[j].Name()
	})
	for i := range fields {
		SortFields(fields[i].Type())
	}
}

//nolint:gocyclo // reflection requires a huge switch, it's fine
func reflectInternal(path []string, v reflect.Value, t reflect.Type) (avro.Schema, error) {
	if t == nil {
		return nil, nil // untyped nil
	}
	switch t.Kind() {
	case reflect.Bool:
		return avro.NewPrimitiveSchema(avro.Boolean, nil), nil
	case reflect.Int, reflect.Int64, reflect.Uint32:
		return avro.NewPrimitiveSchema(avro.Long, nil), nil
	case reflect.Int32, reflect.Int16, reflect.Uint16, reflect.Int8, reflect.Uint8:
		return avro.NewPrimitiveSchema(avro.Int, nil), nil
	case reflect.Float32:
		return avro.NewPrimitiveSchema(avro.Float, nil), nil
	case reflect.Float64:
		return avro.NewPrimitiveSchema(avro.Double, nil), nil
	case reflect.String:
		return avro.NewPrimitiveSchema(avro.String, nil), nil
	case reflect.Pointer:
		s, err := reflectInternal(path, v.Elem(), t.Elem())
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
			return nil, nil // can't get a schema for this
		}
		return reflectInternal(path, v.Elem(), v.Elem().Type())
	case reflect.Array, reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return avro.NewPrimitiveSchema(avro.Bytes, nil), nil
		}

		var elemValue reflect.Value
		if v.Len() > 0 {
			elemValue = v.Index(0)
		}
		items, err := reflectInternal(path, elemValue, t.Elem())
		if err != nil {
			return nil, err
		}
		if items == nil {
			// we fall back to a simple nullable string schema, items are either
			// of an unknown type or nil
			var err error
			items, err = avro.NewUnionSchema([]avro.Schema{
				avro.NewPrimitiveSchema(avro.String, nil),
				avro.NewPrimitiveSchema(avro.Null, nil),
			})
			if err != nil {
				return nil, err
			}
		}
		return avro.NewArraySchema(items), nil
	case reflect.Map:
		if t.Key().Kind() == reflect.String {
			// special case - we treat the map like a struct
			var fields []*avro.Field
			valType := t.Elem()
			for _, keyValue := range v.MapKeys() {
				fs, err := reflectInternal(append(path, keyValue.String()), v.MapIndex(keyValue), valType)
				if err != nil {
					return nil, err
				}
				if fs == nil {
					// we fall back to a simple nullable string schema, item is
					// either of an unknown type or nil
					var err error
					fs, err = avro.NewUnionSchema([]avro.Schema{
						avro.NewPrimitiveSchema(avro.String, nil),
						avro.NewPrimitiveSchema(avro.Null, nil),
					})
					if err != nil {
						return nil, err
					}
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

		var valValue reflect.Value
		for _, kv := range v.MapKeys() {
			valValue = v.MapIndex(kv)
			break
		}
		ks, err := reflectInternal(append(path, "key"), valValue, t.Elem())
		if err != nil {
			return nil, err
		}
		return avro.NewMapSchema(ks), nil
	case reflect.Struct:
		var fields []*avro.Field
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			name, ok := getStructFieldJSONName(sf)
			if !ok {
				continue // skip this field
			}
			fs, err := reflectInternal(append(path, name), v.Field(i), t.Field(i).Type)
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
	panic(fmt.Errorf("unsupported type: %v", t))
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

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
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/hamba/avro/v2"
)

var (
	structuredDataType = reflect.TypeFor[opencdc.StructuredData]()
	byteType           = reflect.TypeFor[byte]()
	timeType           = reflect.TypeFor[time.Time]()
)

// extractor exposes a way to extract an Avro schema from a Go value.
type extractor struct{}

// Extract uses reflection to traverse the value and type of v and extract an
// Avro schema from it. There are some limitations that will cause this function
// to return an error, here are all known cases:
//   - A fixed array of a type other than byte (e.g. [4]int).
//   - A map with a key type other than string (e.g. map[int]any).
//   - We only support built-in Avro types, which means that the following Go
//     types are NOT supported:
//     uint, uint64, complex64, complex128, chan, func, uintptr
//
// The function does its best to infer the schema, but it's working with limited
// information and has to make some assumptions:
//   - If a map does not specify the type of its values (e.g. map[string]any),
//     Extract will traverse all values in the map, extract their types and
//     combine them in a union type. If the map is empty, the extracted value
//     type will default to a nullable string (union type of string and null).
//   - If a slice does not specify the type of its values (e.g. []any), Extract
//     will traverse all values in the slice, extract their types and combine
//     them in a union type. If the slice is empty, the extracted value type
//     will default to a nullable string (union type of string and null).
//   - If Extract encounters a value with the type of opencdc.StructuredData it
//     will treat it as a record and extract a record schema, where each key in
//     the structured data is extracted into its own record field.
func (e extractor) Extract(v any) (avro.Schema, error) {
	return e.extract([]string{"record"}, reflect.ValueOf(v), reflect.TypeOf(v))
}

func (e extractor) extract(path []string, v reflect.Value, t reflect.Type) (avro.Schema, error) {
	if t == nil {
		return nil, cerrors.Errorf("%s: can't get schema for untyped nil", strings.Join(path, ".")) // untyped nil
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
		return e.extractPointer(path, v, t)
	case reflect.Interface:
		return e.extractInterface(path, v, t)
	case reflect.Array:
		if t.Elem() != byteType {
			return nil, cerrors.Errorf("%s: arrays with value type %v not supported, avro only supports bytes as values", strings.Join(path, "."), t.Elem().String())
		}
		return avro.NewFixedSchema(strings.Join(path, "."), "", t.Len(), nil)
	case reflect.Slice:
		return e.extractSlice(path, v, t)
	case reflect.Map:
		return e.extractMap(path, v, t)
	case reflect.Struct:
		switch t {
		case timeType:
			return avro.NewPrimitiveSchema(
				avro.Long,
				avro.NewPrimitiveLogicalSchema(avro.TimestampMicros),
			), nil
		}
		return e.extractStruct(path, v, t)
	}
	// Invalid, Uintptr, UnsafePointer, Uint64, Uint, Complex64, Complex128, Chan, Func
	return nil, cerrors.Errorf("%s: unsupported type: %v", strings.Join(path, "."), t)
}

// extractPointer extracts the schema behind the pointer and makes it nullable
// (if it's not already nullable).
func (e extractor) extractPointer(path []string, v reflect.Value, t reflect.Type) (avro.Schema, error) {
	var vElem reflect.Value
	if v.IsValid() {
		vElem = v.Elem()
	}
	s, err := e.extract(path, vElem, t.Elem())
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
}

// extractInterface ignores the type, since an interface doesn't say anything
// about the concrete type behind it. Instead, it looks at the value behind the
// interface and tries to extract the schema based on its actual type.
// If the value is nil we have no way of knowing the actual type, but since we
// need to be able to encode untyped nil values, we default to a nullable string.
func (e extractor) extractInterface(path []string, v reflect.Value, _ reflect.Type) (avro.Schema, error) {
	if !v.IsValid() || v.IsNil() {
		// unknown type, fall back to nullable string
		return avro.NewUnionSchema([]avro.Schema{
			avro.NewPrimitiveSchema(avro.String, nil),
			&avro.NullSchema{},
		})
	}
	return e.extract(path, v.Elem(), v.Elem().Type())
}

// extractSlice tries to extract the schema based on the slice value type. If
// that type is an interface it falls back to looping through all values,
// extracting their types and combining them into a nullable union schema.
func (e extractor) extractSlice(path []string, v reflect.Value, t reflect.Type) (avro.Schema, error) {
	if t.Elem().Kind() == reflect.Uint8 {
		return avro.NewPrimitiveSchema(avro.Bytes, nil), nil
	}

	// try getting value type based on the slice type
	if t.Elem().Kind() != reflect.Interface {
		vs, err := e.extract(append(path, "item"), reflect.Value{}, t.Elem())
		if err != nil {
			return nil, err
		}
		return avro.NewArraySchema(vs), nil
	}

	// this is []any, loop through all values and extracting their types
	// into a union schema, null is included by default
	types := []avro.Schema{&avro.NullSchema{}}
	for i := 0; i < v.Len(); i++ {
		itemSchema, err := e.extract(
			append(path, fmt.Sprintf("item%d", i)),
			v.Index(i), t.Elem(),
		)
		if err != nil {
			return nil, err
		}
		types = append(types, itemSchema)
	}
	// we could have duplicate schemas, deduplicate them
	types, err := e.deduplicate(types)
	if err != nil {
		return nil, err
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
}

// extractMap tries to extract the schema based on the map value type. If that
// type is an interface it falls back to looping through all values, extracting
// their types and combining them into a nullable union schema.
// If the key of the map is not a string, this function returns an error. If the
// type of the map is opencdc.StructuredData it will treat it as a record and
// extract a record schema, where each key in the structured data is extracted
// into its own record field.
func (e extractor) extractMap(path []string, v reflect.Value, t reflect.Type) (avro.Schema, error) {
	if t == structuredDataType {
		// special case - we treat StructuredData like a struct
		var fields []*avro.Field
		valType := t.Elem()
		for _, keyValue := range v.MapKeys() {
			fs, err := e.extract(append(path, keyValue.String()), v.MapIndex(keyValue), valType)
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
		vs, err := e.extract(append(path, "value"), reflect.Value{}, t.Elem())
		if err != nil {
			return nil, err
		}
		return avro.NewMapSchema(vs), nil
	}

	// this is map[string]any, loop through all values and extracting their
	// types into a union schema, null is included by default
	types := []avro.Schema{&avro.NullSchema{}}
	for _, kv := range v.MapKeys() {
		valValue := v.MapIndex(kv)
		vs, err := e.extract(append(path, "value"), valValue, t.Elem())
		if err != nil {
			return nil, err
		}
		types = append(types, vs)
	}
	// we could have duplicate schemas, deduplicate them
	types, err := e.deduplicate(types)
	if err != nil {
		return nil, err
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
}

// extractStruct traverses the struct fields, extracts the schema for each field
// and combines them into a record schema. If the field contains a json tag,
// that tag is used for the extracted name of the field, otherwise it is the
// name of the Go struct field. If the json tag of a field contains "-" (i.e.
// ignored field), then the field is skipped.
func (e extractor) extractStruct(path []string, v reflect.Value, t reflect.Type) (avro.Schema, error) {
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
		fs, err := e.extract(append(path, name), vfi, t.Field(i).Type)
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

func (e extractor) deduplicate(schemas []avro.Schema) ([]avro.Schema, error) {
	out := make([]avro.Schema, 0, len(schemas))
	typesSet := make(map[[32]byte]struct{})

	var appendSchema func(schema avro.Schema) error
	appendSchema = func(schema avro.Schema) error {
		if _, ok := typesSet[schema.Fingerprint()]; ok {
			return nil
		}
		if us, ok := schema.(*avro.UnionSchema); ok {
			for _, st := range us.Types() {
				if err := appendSchema(st); err != nil {
					return err
				}
			}
			return nil
		}
		for _, s := range out {
			if s.Type() == schema.Type() {
				switch s := s.(type) {
				case *avro.ArraySchema:
					// we are combining two array schemas with different item
					// schemas, combine them and create a new array schema
					schema := schema.(*avro.ArraySchema)
					itemsSchema, err := e.deduplicate([]avro.Schema{s.Items(), schema.Items()})
					if err != nil {
						return err
					}
					if len(itemsSchema) == 1 {
						*s = *avro.NewArraySchema(itemsSchema[0])
					} else {
						itemsUnionSchema, err := avro.NewUnionSchema(itemsSchema)
						if err != nil {
							return err
						}
						*s = *avro.NewArraySchema(itemsUnionSchema)
					}
				case *avro.MapSchema:
					schema := schema.(*avro.MapSchema)
					valuesSchema, err := e.deduplicate([]avro.Schema{s.Values(), schema.Values()})
					if err != nil {
						return err
					}
					if len(valuesSchema) == 1 {
						*s = *avro.NewMapSchema(valuesSchema[0])
					} else {
						valuesUnionSchema, err := avro.NewUnionSchema(valuesSchema)
						if err != nil {
							return err
						}
						*s = *avro.NewMapSchema(valuesUnionSchema)
					}
				default:
					return cerrors.Errorf("can't combine schemas of type %T", s)
				}
				return nil
			}
		}

		// schema does not exist yet
		out = append(out, schema)
		typesSet[schema.Fingerprint()] = struct{}{}
		return nil
	}

	for _, schema := range schemas {
		if err := appendSchema(schema); err != nil {
			return nil, err
		}
	}
	return out, nil
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

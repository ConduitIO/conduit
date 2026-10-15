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

package connect

import (
	"math"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

// kitchenSchema has one field of every Connect type and shape the goldens do
// not reach (maps, int8, float arrays, bytes arrays, nested structs).
func kitchenSchema() *Schema {
	return &Schema{Type: TypeStruct, Name: "k.Value", Fields: []Field{
		{Name: "i8", Schema: &Schema{Type: TypeInt8}},
		{Name: "f32", Schema: &Schema{Type: TypeFloat32}},
		{Name: "f64", Schema: &Schema{Type: TypeFloat64, Optional: true}},
		{Name: "b", Schema: &Schema{Type: TypeBytes}},
		{Name: "strs", Schema: &Schema{Type: TypeArray, Items: &Schema{Type: TypeString}}},
		{Name: "smap", Schema: &Schema{Type: TypeMap, Keys: &Schema{Type: TypeString}, Values: &Schema{Type: TypeInt64, Optional: true}}},
		{Name: "imap", Schema: &Schema{Type: TypeMap, Keys: &Schema{Type: TypeInt32}, Values: &Schema{Type: TypeBoolean}}},
		{Name: "nested", Schema: &Schema{Type: TypeStruct, Optional: true, Fields: []Field{
			{Name: "d", Schema: decimalSchema("1")},
		}}},
	}}
}

func kitchenValue() map[string]any {
	return map[string]any{
		"i8":     int8(-7),
		"f32":    float32(1.5),
		"f64":    math.Inf(-1),
		"b":      []byte{0, 1, 2},
		"strs":   []string{"a", `q"`},
		"smap":   []MapEntry{{Key: "z", Value: int64(1)}, {Key: "a", Value: nil}},
		"imap":   []MapEntry{{Key: int32(2), Value: true}},
		"nested": map[string]any{"d": "-1.5"},
	}
}

func TestKitchenSinkConverters(t *testing.T) {
	is := is.New(t)
	s := kitchenSchema()
	cv, err := FromGo(s, kitchenValue())
	is.NoErr(err)

	conv := NewJSONConverter()
	conv.SchemasEnable = false
	got, err := conv.Marshal(s, cv)
	is.NoErr(err)
	// Maps keep their input order; string-keyed maps are objects, others
	// arrays of pairs.
	is.Equal(string(got), `{"i8":-7,"f32":1.5,"f64":"-Infinity","b":"AAEC","strs":["a","q\""],"smap":{"z":1,"a":null},"imap":[[2,true]],"nested":{"d":"8Q=="}}`)

	conv.DecimalFormat = DecimalNumeric
	got, err = conv.Marshal(s, cv)
	is.NoErr(err)
	is.Equal(string(got), `{"i8":-7,"f32":1.5,"f64":"-Infinity","b":"AAEC","strs":["a","q\""],"smap":{"z":1,"a":null},"imap":[[2,true]],"nested":{"d":-1.5}}`)

	hdr, err := SimpleHeaderConverter{}.Marshal(s, cv)
	is.NoErr(err)
	is.Equal(string(hdr), `{"i8":-7,"f32":1.5,"f64":-Infinity,"b":"AAEC","strs":["a","q\""],"smap":{"z":1,"a":null},"imap":{2:true},"nested":{"d":-1.5}}`)

	schemaJSON, err := NewJSONConverter().MarshalSchema(s)
	is.NoErr(err)
	back, err := ParseSchemaJSON(schemaJSON)
	is.NoErr(err)
	again, err := NewJSONConverter().MarshalSchema(back)
	is.NoErr(err)
	is.Equal(string(again), string(schemaJSON))

	// The payload parser is the converter's inverse for everything but maps,
	// which it refuses rather than guess an order.
	_, err = ParsePayloadJSON(s, got, DecimalNumeric)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeUnsupportedType)
}

func TestFromGoIntegerKinds(t *testing.T) {
	is := is.New(t)
	s := &Schema{Type: TypeInt64}
	for _, v := range []any{int(1), int8(1), int16(1), int32(1), int64(1), uint(1), uint8(1), uint16(1), uint32(1), uint64(1)} {
		got, err := FromGo(s, v)
		is.NoErr(err)
		is.Equal(got, int64(1))
	}
	_, err := FromGo(s, uint(math.MaxUint64))
	ce, _ := conduiterr.Get(err)
	is.Equal(ce.Code, CodeValueOutOfRange)
	_, err = FromGo(&Schema{Type: TypeInt8}, 128)
	ce, _ = conduiterr.Get(err)
	is.Equal(ce.Code, CodeValueOutOfRange)
}

func TestFromGoStructPointer(t *testing.T) {
	is := is.New(t)
	s := &Schema{Type: TypeStruct, Fields: []Field{{Name: "a", Schema: &Schema{Type: TypeInt32}}}}
	st := &Struct{Schema: s, Values: []any{int64(1)}}
	got, err := FromGo(s, st)
	is.NoErr(err)
	is.Equal(got, st)

	other := &Schema{Type: TypeStruct, Fields: []Field{{Name: "a", Schema: &Schema{Type: TypeInt32}}}}
	_, err = FromGo(other, st)
	ce, _ := conduiterr.Get(err)
	is.Equal(ce.Code, CodeRecordSchemaMismatch) // only the same schema instance
}

func TestFromGoMismatches(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema *Schema
		value  any
	}{
		{"bool", &Schema{Type: TypeBoolean}, "true"},
		{"double", &Schema{Type: TypeFloat64}, 1},
		{"float", &Schema{Type: TypeFloat32}, 1},
		{"array", &Schema{Type: TypeArray, Items: &Schema{Type: TypeInt32}}, "x"},
		{"bytes is not an array", &Schema{Type: TypeArray, Items: &Schema{Type: TypeInt32}}, []byte{1}},
		{"array element", &Schema{Type: TypeArray, Items: &Schema{Type: TypeInt32}}, []any{"x"}},
		{"map key", &Schema{Type: TypeMap, Keys: &Schema{Type: TypeInt32}, Values: &Schema{Type: TypeInt32}}, []MapEntry{{Key: "x", Value: 1}}},
		{"map value", &Schema{Type: TypeMap, Keys: &Schema{Type: TypeInt32}, Values: &Schema{Type: TypeInt32}}, []MapEntry{{Key: 1, Value: "x"}}},
		{"struct", &Schema{Type: TypeStruct}, 1},
		{"vsd not a string", vsdSchema(), 1.5},
		{"vsd garbage", vsdSchema(), "x"},
		{"vsd wrong shape", &Schema{Type: TypeStruct, Name: LogicalVariableScaleDecimal}, "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := FromGo(tc.schema, tc.value)
			if ce, ok := conduiterr.Get(err); !ok || ce.Code != CodeRecordSchemaMismatch {
				t.Fatalf("want record_schema_mismatch, got %v", err)
			}
		})
	}
	_, err := FromGo(vsdSchema(), "NaN")
	if ce, ok := conduiterr.Get(err); !ok || ce.Code != CodeValueOutOfRange {
		t.Fatalf("NaN VariableScaleDecimal: want value_out_of_range, got %v", err)
	}
	_, err = FromGo(&Schema{Type: TypeFloat32}, math.NaN())
	if err != nil {
		t.Fatalf("NaN float64 converts to float32 exactly: %v", err)
	}
}

func vsdSchema() *Schema {
	return &Schema{Type: TypeStruct, Optional: true, Name: LogicalVariableScaleDecimal, Fields: []Field{
		{Name: "scale", Schema: &Schema{Type: TypeInt32}},
		{Name: "value", Schema: &Schema{Type: TypeBytes}},
	}}
}

func TestSchemaDefaults(t *testing.T) {
	is := is.New(t)
	in := `{"type":"struct","fields":[` +
		`{"type":"int16","optional":false,"default":-3,"field":"a"},` +
		`{"type":"float","optional":false,"default":1.5,"field":"b"},` +
		`{"type":"double","optional":false,"default":"NaN","field":"c"},` +
		`{"type":"boolean","optional":false,"default":false,"field":"d"},` +
		`{"type":"bytes","optional":false,"default":"AQI=","field":"e"},` +
		`{"type":"array","items":{"type":"string","optional":false},"optional":false,"default":["x"],"field":"f"},` +
		`{"type":"bytes","optional":true,"name":"org.apache.kafka.connect.data.Decimal","version":1,"parameters":{"scale":"2"},"default":1.25,"field":"g"},` +
		`{"type":"string","optional":true,"default":null,"field":"h"}` +
		`],"optional":false}`
	s, err := ParseSchemaJSON([]byte(in))
	is.NoErr(err)
	want := []any{int64(-3), float32(1.5), nil, false, []byte{1, 2}, []any{"x"}, "1.25", nil}
	for i, f := range s.Fields {
		switch i {
		case 2:
			is.True(math.IsNaN(f.Schema.Default.(float64)))
		case 6:
			is.Equal(f.Schema.Default.(Decimal).String(), want[i])
		default:
			is.Equal(f.Schema.Default, want[i])
		}
	}
	// NUMERIC defaults are written back as BASE64 by the default converter.
	out, err := NewJSONConverter().MarshalSchema(s)
	is.NoErr(err)
	is.True(len(out) > 0)

	for _, bad := range []string{
		`{"type":"int32","default":"x"}`,
		`{"type":"double","default":"Inf"}`,
		`{"type":"double","default":true}`,
		`{"type":"boolean","default":1}`,
		`{"type":"string","default":1}`,
		`{"type":"bytes","default":1}`,
		`{"type":"array","items":{"type":"int32"},"default":1}`,
		`{"type":"bytes","name":"org.apache.kafka.connect.data.Decimal","parameters":{"scale":"x"}}`,
		`{"type":"bytes","name":"org.apache.kafka.connect.data.Decimal","parameters":{"scale":"2"},"default":true}`,
		`{"type":"int32","version":"1"}`,
		`{"type":"int32","optional":"yes"}`,
		`{"type":"int32","parameters":{"a":"1","a":"2"}}`,
		`{"type":"struct","fields":[{"type":"int32","field":1}]}`,
		`{"type":"int32","items":{"type":"int32"}}`,
		`{"type":"map","keys":{"type":"string"}}`,
	} {
		_, err := ParseSchemaJSON([]byte(bad))
		if ce, ok := conduiterr.Get(err); !ok || ce.Code != CodeDescriptorMissing {
			t.Errorf("%s: want descriptor_missing, got %v", bad, err)
		}
	}
	// A map default is a schema Conduit cannot write faithfully: refused as
	// unsupported, not guessed.
	_, err = ParseSchemaJSON([]byte(`{"type":"map","keys":{"type":"string"},"values":{"type":"string"},"default":{}}`))
	if ce, ok := conduiterr.Get(err); !ok || ce.Code != CodeUnsupportedType {
		t.Errorf("map default: want unsupported_type, got %v", err)
	}
}

func TestParsePayloadJSONErrors(t *testing.T) {
	s := &Schema{Type: TypeStruct, Fields: []Field{
		{Name: "i", Schema: &Schema{Type: TypeInt32, Optional: true}},
		{Name: "f", Schema: &Schema{Type: TypeFloat64, Optional: true}},
		{Name: "b", Schema: &Schema{Type: TypeBoolean, Optional: true}},
		{Name: "s", Schema: &Schema{Type: TypeString, Optional: true}},
		{Name: "y", Schema: &Schema{Type: TypeBytes, Optional: true}},
		{Name: "a", Schema: &Schema{Type: TypeArray, Optional: true, Items: &Schema{Type: TypeInt32}}},
		{Name: "d", Schema: decimalSchema("2")},
		{Name: "v", Schema: vsdSchema()},
	}}
	for _, in := range []string{
		`{`, `{} x`, `[]`,
		`{"zz":1}`,
		`{"i":"1"}`, `{"f":"x"}`, `{"f":true}`, `{"b":1}`, `{"s":1}`, `{"y":1}`, `{"y":"%%"}`,
		`{"a":1}`, `{"a":["x"]}`,
		`{"d":1}`, `{"d":"%%"}`,
		`{"v":1}`, `{"v":{"value":"AA=="}}`, `{"v":{"scale":1}}`, `{"v":{"scale":1,"value":"%%"}}`,
	} {
		_, err := ParsePayloadJSON(s, []byte(in), DecimalBase64)
		if _, ok := conduiterr.Get(err); !ok {
			t.Errorf("%s: want a coded error, got %v", in, err)
		}
	}
	_, err := ParsePayloadJSON(s, []byte(`{"d":"x"}`), DecimalNumeric)
	if _, ok := conduiterr.Get(err); !ok {
		t.Errorf("NUMERIC decimal as string: want a coded error, got %v", err)
	}
	got, err := ParsePayloadJSON(s, []byte(`{"f":"-Infinity","v":{"scale":-2,"value":"AQ=="}}`), DecimalBase64)
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if !math.IsInf(m["f"].(float64), -1) || m["v"] != "1E2" {
		t.Fatalf("got %v", m)
	}
}

func TestMarshalErrors(t *testing.T) {
	is := is.New(t)
	conv := NewJSONConverter()
	_, err := conv.Marshal(nil, "x")
	ce, _ := conduiterr.Get(err)
	is.Equal(ce.Code, CodeUnsupportedType)

	conv.DecimalFormat = "HEX"
	_, err = conv.Marshal(&Schema{Type: TypeInt32}, int64(1))
	ce, _ = conduiterr.Get(err)
	is.Equal(ce.Code, CodeConfigInvalid)

	_, err = NewJSONConverter().Marshal(&Schema{Type: TypeString}, string([]byte{0xff}))
	ce, _ = conduiterr.Get(err)
	is.Equal(ce.Code, CodeValueOutOfRange) // invalid UTF-8 is refused, not replaced

	for _, tc := range []struct {
		s *Schema
		v any
	}{
		{&Schema{Type: TypeInt32}, 1},
		{&Schema{Type: TypeFloat32}, 1.0},
		{&Schema{Type: TypeFloat64}, float32(1)},
		{&Schema{Type: TypeBoolean}, 1},
		{&Schema{Type: TypeString}, 1},
		{&Schema{Type: TypeBytes}, "x"},
		{&Schema{Type: TypeArray, Items: &Schema{Type: TypeInt32}}, []int{1}},
		{&Schema{Type: TypeMap, Keys: &Schema{Type: TypeString}, Values: &Schema{Type: TypeInt32}}, []MapEntry{{Key: 1, Value: int64(1)}}},
		{&Schema{Type: TypeMap, Keys: &Schema{Type: TypeString}, Values: &Schema{Type: TypeInt32}}, map[string]any{}},
		{&Schema{Type: TypeStruct}, map[string]any{}},
		{&Schema{Type: TypeStruct, Fields: []Field{{Name: "a", Schema: &Schema{Type: TypeInt32}}}}, &Struct{}},
		{decimalSchema("2"), "1.00"},
		{&Schema{Type: TypeString}, nil},
	} {
		_, err := NewJSONConverter().Marshal(tc.s, tc.v)
		if ce, ok := conduiterr.Get(err); !ok || ce.Code != CodeRecordSchemaMismatch {
			t.Errorf("Marshal(%s, %T): want record_schema_mismatch, got %v", tc.s.Type, tc.v, err)
		}
	}
}

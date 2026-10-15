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
	"bytes"
	"math"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

func decimalSchema(scale string) *Schema {
	v := 1
	return &Schema{Type: TypeBytes, Optional: true, Name: LogicalDecimal, Version: &v, Parameters: []Param{{ParamDecimalScale, scale}}}
}

// Every value that would need a lossy conversion is refused with a stable
// code instead of being coerced (invariant 6).
func TestFromGoRefusesLossyValues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema *Schema
		value  any
		code   conduiterr.Code
	}{
		{"int16 overflow", &Schema{Type: TypeInt16}, 40000, CodeValueOutOfRange},
		{"int32 overflow", &Schema{Type: TypeInt32}, int64(math.MaxInt32) + 1, CodeValueOutOfRange},
		{"uint64 above int64", &Schema{Type: TypeInt64}, uint64(math.MaxUint64), CodeValueOutOfRange},
		{"float64 for int64", &Schema{Type: TypeInt64}, float64(9007199254740993), CodeRecordSchemaMismatch},
		{"integral float for int32", &Schema{Type: TypeInt32}, 1.0, CodeRecordSchemaMismatch},
		{"string for int", &Schema{Type: TypeInt32}, "1", CodeRecordSchemaMismatch},
		{"float64 not exact as float32", &Schema{Type: TypeFloat32}, 0.1, CodeValueOutOfRange},
		{"string for bytes", &Schema{Type: TypeBytes}, "AP8Q", CodeRecordSchemaMismatch},
		{"bytes for string", &Schema{Type: TypeString}, []byte("x"), CodeRecordSchemaMismatch},
		{"null for required", &Schema{Type: TypeString}, nil, CodeRecordSchemaMismatch},
		{"decimal rounding", decimalSchema("2"), "1.005", CodeValueOutOfRange},
		{"decimal NaN", decimalSchema("2"), "NaN", CodeValueOutOfRange},
		{"decimal Infinity", decimalSchema("2"), "Infinity", CodeValueOutOfRange},
		{"decimal garbage", decimalSchema("2"), "1.2.3", CodeRecordSchemaMismatch},
		{"decimal as float", decimalSchema("2"), 1.5, CodeRecordSchemaMismatch},
		{"go map for connect map", &Schema{Type: TypeMap, Keys: &Schema{Type: TypeString}, Values: &Schema{Type: TypeString}}, map[string]any{"a": "b"}, CodeRecordSchemaMismatch},
		{"unknown struct field", &Schema{Type: TypeStruct, Fields: []Field{{Name: "a", Schema: &Schema{Type: TypeInt32}}}}, map[string]any{"a": 1, "b": 2}, CodeRecordSchemaMismatch},
		{"missing required field", &Schema{Type: TypeStruct, Fields: []Field{{Name: "a", Schema: &Schema{Type: TypeInt32}}}}, map[string]any{}, CodeRecordSchemaMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			_, err := FromGo(tc.schema, tc.value)
			ce, ok := conduiterr.Get(err)
			is.True(ok) // want a ConduitError
			is.Equal(ce.Code, tc.code)
		})
	}
}

func TestFromGoDecimalRescale(t *testing.T) {
	is := is.New(t)
	for in, want := range map[string]string{
		"5":       "5.0000",
		"-0.0001": "-0.0001",
		"1.10":    "1.1000",
		"1.5E2":   "150.0000",
		"1.00000": "1.0000", // trailing zeros may be dropped, never a digit
	} {
		v, err := FromGo(decimalSchema("4"), in)
		is.NoErr(err)
		is.Equal(v.(Decimal).String(), want)
	}
}

func TestReplaceNullWithDefault(t *testing.T) {
	is := is.New(t)
	s := &Schema{Type: TypeStruct, Fields: []Field{
		{Name: "a", Schema: &Schema{Type: TypeString, Optional: true, Default: "dflt", HasDefault: true}},
	}}
	cv, err := FromGo(s, map[string]any{"a": nil})
	is.NoErr(err)

	conv := NewJSONConverter()
	conv.SchemasEnable = false
	got, err := conv.Marshal(s, cv)
	is.NoErr(err)
	is.Equal(string(got), `{"a":"dflt"}`)

	conv.ReplaceNullWithDefault = false
	got, err = conv.Marshal(s, cv)
	is.NoErr(err)
	is.Equal(string(got), `{"a":null}`)
}

func TestMarshalTombstone(t *testing.T) {
	is := is.New(t)
	got, err := NewJSONConverter().Marshal(nil, nil)
	is.NoErr(err)
	is.True(got == nil) // a tombstone is a Kafka null, not an empty value
}

func TestJavaFloatStringKnownValues(t *testing.T) {
	for _, tc := range []struct {
		f    float64
		bits int
		want string
	}{
		{1.5, 64, "1.5"},
		{100, 64, "100.0"},
		{1e7, 64, "1.0E7"},
		{9999999.999, 64, "9999999.999"},
		{0.001, 64, "0.001"},
		{0.0001, 64, "1.0E-4"},
		{1e-300, 64, "1.0E-300"},
		{4.9e-324, 64, "4.9E-324"},
		{math.MaxFloat64, 64, "1.7976931348623157E308"},
		{2e22, 64, "2.0E22"},
		{float64(float32(3.4028235e38)), 32, "3.4028235E38"},
		{float64(float32(1.17549435e-38)), 32, "1.1754944E-38"}, // golden; JDK 17 printed 1.17549435E-38
		{float64(float32(1e-45)), 32, "1.4E-45"},
		{math.Copysign(0, -1), 64, "-0.0"},
	} {
		if got := JavaFloatString(tc.f, tc.bits); got != tc.want {
			t.Errorf("JavaFloatString(%v, %d) = %q, want %q", tc.f, tc.bits, got, tc.want)
		}
	}
}

func TestParseSchemaJSONRejects(t *testing.T) {
	for _, in := range []string{
		``,
		`{}`,
		`{"type":"nope","optional":false}`,
		`{"type":"array","optional":false}`,
		`{"type":"struct","fields":[{"type":"int32","optional":false}],"optional":false}`,
		`{"type":"struct","fields":[{"type":"int32","field":"a"},{"type":"int32","field":"a"}]}`,
		`{"type":"bytes","name":"org.apache.kafka.connect.data.Decimal"}`,
		`{"type":"int32","type":"int64"}`,
		`{"type":"int32","unknown":1}`,
		`{"type":"int32"} trailing`,
		`{"type":"int8","default":300}`,
	} {
		_, err := ParseSchemaJSON([]byte(in))
		ce, ok := conduiterr.Get(err)
		if !ok || ce.Code != CodeDescriptorMissing {
			t.Errorf("%q: want descriptor_missing, got %v", in, err)
		}
	}
}

func FuzzParseSchemaJSON(f *testing.F) {
	for _, seed := range []string{
		`{"type":"struct","fields":[{"type":"int32","optional":false,"field":"id"}],"optional":false,"name":"x.Key"}`,
		`{"type":"bytes","optional":true,"name":"org.apache.kafka.connect.data.Decimal","version":1,"parameters":{"scale":"2","connect.decimal.precision":"10"},"default":"AQ=="}`,
		`{"type":"array","items":{"type":"string","optional":true},"optional":true}`,
		`{"type":"map","keys":{"type":"string","optional":false},"values":{"type":"int64","optional":true},"optional":false}`,
		`{"type":"string","optional":true,"name":"io.debezium.data.Enum","version":1,"parameters":{"allowed":"a,b"},"default":"a"}`,
	} {
		f.Add([]byte(seed))
	}
	conv := NewJSONConverter()
	f.Fuzz(func(t *testing.T, in []byte) {
		s, err := ParseSchemaJSON(in)
		if err != nil {
			if _, ok := conduiterr.Get(err); !ok {
				t.Fatalf("error without a stable code: %v", err)
			}
			return
		}
		b, err := conv.MarshalSchema(s)
		if err != nil {
			return // e.g. a default the converter cannot write
		}
		back, err := ParseSchemaJSON(b)
		if err != nil {
			t.Fatalf("converter output does not parse: %s: %v", b, err)
		}
		b2, err := conv.MarshalSchema(back)
		if err != nil || !bytes.Equal(b, b2) {
			t.Fatalf("schema not stable across a round trip:\n%s\n%s (%v)", b, b2, err)
		}
	})
}

func FuzzParseDecimal(f *testing.F) {
	for _, seed := range []string{"0", "-1.10", "1E+2", "123456789012345678901234567890.123456789", ".5", "1.", "NaN", "1e-2147483649"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		u, scale, err := parseDecimal(in)
		if err != nil {
			return
		}
		str := Decimal{Unscaled: u, Scale: scale}.String()
		u2, scale2, err := parseDecimal(str)
		if err != nil {
			t.Fatalf("%q rendered as %q, which does not parse: %v", in, str, err)
		}
		a, b := Decimal{u, scale}, Decimal{u2, scale2}
		if a.String() != b.String() {
			t.Fatalf("%q: %s != %s", in, a, b)
		}
	})
}

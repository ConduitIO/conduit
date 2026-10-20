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

package debezium

import (
	"bytes"
	"math"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
)

func TestWriteValueTypes(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	cases := []struct {
		name string
		s    *schema
		v    any
		want string
	}{
		{"array", &schema{Type: typeArray, Items: &schema{Type: typeInt32}}, []any{int32(1), int32(2)}, "[1,2]"},
		{"map", &schema{Type: typeMap}, map[string]any{"k": "v"}, `{"k":"v"}`},
		{"bytes", &schema{Type: typeBytes}, []byte("hi"), `"aGk="`},
		{"decimal_base64", &schema{Type: typeBytes, Name: nameDecimal, Params: []parameter{{Key: "scale", Value: "2"}}}, "1.23", `"ew=="`},
		{"variable_scale_decimal", &schema{Type: typeStruct, Name: nameVariableScaleDecimal}, "1.10", `{"scale":1,"value":"Cw=="}`},
		{"float", &schema{Type: typeFloat}, float32(1.5), "1.5"},
		{"double", &schema{Type: typeDouble}, float64(-2.5), "-2.5"},
		{"bool_true", &schema{Type: typeBoolean}, true, "true"},
		{"bool_false", &schema{Type: typeBoolean}, false, "false"},
		{"string", &schema{Type: typeString}, "hi", `"hi"`},
		{"int16", &schema{Type: typeInt16}, int16(3), "3"},
		{"int32", &schema{Type: typeInt32}, int32(4), "4"},
		{"int64", &schema{Type: typeInt64}, int64(5), "5"},
		{"null_optional", &schema{Type: typeString, Optional: true}, nil, "null"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b bytes.Buffer
			err := writeValue(&b, c.s, c.v, decimalBase64)
			is.NoErr(err)
			is.Equal(b.String(), c.want)
		})
	}
}

func TestWriteValueErrors(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	cases := []struct {
		name string
		s    *schema
		v    any
		code conduiterr.Code
	}{
		{"null_required", &schema{Type: typeString}, nil, CodeRecordSchemaMismatch},
		{"array_not_list", &schema{Type: typeArray, Items: &schema{Type: typeInt32}}, "x", CodeRecordSchemaMismatch},
		{"map_not_object", &schema{Type: typeMap}, 42, CodeRecordSchemaMismatch},
		{"bytes_not_bytes", &schema{Type: typeBytes}, "x", CodeRecordSchemaMismatch},
		{"float_not_float", &schema{Type: typeFloat}, "x", CodeRecordSchemaMismatch},
		{"double_not_float", &schema{Type: typeDouble}, "x", CodeRecordSchemaMismatch},
		{"bool_not_bool", &schema{Type: typeBoolean}, "x", CodeRecordSchemaMismatch},
		{"string_not_string", &schema{Type: typeString}, 42, CodeRecordSchemaMismatch},
		{"int_not_int", &schema{Type: typeInt64}, "x", CodeRecordSchemaMismatch},
		{"unsupported_type", &schema{Type: "mystery"}, "x", CodeUnsupportedType},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b bytes.Buffer
			err := writeValue(&b, c.s, c.v, decimalBase64)
			is.True(err != nil)
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, c.code)
		})
	}
}

func TestAsMap(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	m, err := asMap(map[string]any{"a": "b"})
	is.NoErr(err)
	is.Equal(m["a"], "b")

	m, err = asMap(opencdc.StructuredData{"a": "b"})
	is.NoErr(err)
	is.Equal(m["a"], "b")

	m, err = asMap(map[string]string{"a": "b"})
	is.NoErr(err)
	is.Equal(m["a"], "b")

	_, err = asMap(42)
	is.True(err != nil)
}

func TestWriteIntTypes(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	cases := []struct {
		v    any
		want string
	}{
		{int(1), "1"},
		{int8(2), "2"},
		{int16(3), "3"},
		{int32(4), "4"},
		{int64(5), "5"},
		{uint64(6), "6"},
		{json.Number("7"), "7"},
		{float64(8), "8"},
	}
	for _, c := range cases {
		var b bytes.Buffer
		err := writeInt(&b, c.v)
		is.NoErr(err)
		is.Equal(b.String(), c.want)
	}
}

func TestAsFloat(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	f, err := asFloat(float32(1.5), 32)
	is.NoErr(err)
	is.Equal(f, 1.5)

	f, err = asFloat(float64(2.5), 64)
	is.NoErr(err)
	is.Equal(f, 2.5)

	f, err = asFloat(json.Number("3.5"), 64)
	is.NoErr(err)
	is.Equal(f, 3.5)

	_, err = asFloat("x", 64)
	is.True(err != nil)
}

func TestAsDecimalString(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	s, ok := asDecimalString("1.5")
	is.True(ok)
	is.Equal(s, "1.5")

	s, ok = asDecimalString(json.Number("2.5"))
	is.True(ok)
	is.Equal(s, "2.5")

	_, ok = asDecimalString(42)
	is.Equal(ok, false)
}

func TestWriteDecimal(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	s := &schema{Type: typeBytes, Name: nameDecimal, Params: []parameter{{Key: "scale", Value: "2"}}}

	var b bytes.Buffer
	err := writeDecimal(&b, "1.23", s, decimalBase64)
	is.NoErr(err)
	is.Equal(b.String(), `"ew=="`)

	b.Reset()
	err = writeDecimal(&b, "1.23", s, decimalNumeric)
	is.NoErr(err)
	is.Equal(b.String(), "1.23")
}

func TestWriteDecimalErrors(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	noScale := &schema{Type: typeBytes, Name: nameDecimal}
	withScale := &schema{Type: typeBytes, Name: nameDecimal, Params: []parameter{{Key: "scale", Value: "2"}}}

	cases := []struct {
		name string
		s    *schema
		v    any
		df   string
		code conduiterr.Code
	}{
		{"not_string", withScale, 42, decimalBase64, CodeRecordSchemaMismatch},
		{"no_scale", noScale, "1.23", decimalBase64, CodeRecordSchemaMismatch},
		{"numeric_invalid", withScale, "abc", decimalNumeric, CodeValueOutOfRange},
		{"base64_out_of_range", withScale, "1.234", decimalBase64, CodeValueOutOfRange},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b bytes.Buffer
			err := writeDecimal(&b, c.v, c.s, c.df)
			is.True(err != nil)
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, c.code)
		})
	}
}

func TestWriteVariableScaleDecimal(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	var b bytes.Buffer
	err := writeVariableScaleDecimal(&b, "1.10")
	is.NoErr(err)
	is.Equal(b.String(), `{"scale":1,"value":"Cw=="}`)

	b.Reset()
	err = writeVariableScaleDecimal(&b, 42)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeRecordSchemaMismatch)

	b.Reset()
	err = writeVariableScaleDecimal(&b, "abc")
	is.True(err != nil)
	ce, ok = conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeValueOutOfRange)
}

func TestAppendFloatSpecials(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	cases := []struct {
		f    float64
		bits int
		want string
	}{
		{math.NaN(), 64, "NaN"},
		{math.Inf(1), 64, "Infinity"},
		{math.Inf(-1), 64, "-Infinity"},
		{1e20, 64, "1.0E20"},
	}
	for _, c := range cases {
		var b bytes.Buffer
		appendFloat(&b, c.f, c.bits)
		is.Equal(b.String(), c.want)
	}
}

func TestParseInt64(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	is.Equal(parseInt64(""), nil)
	is.Equal(parseInt64("123"), int64(123))
	is.Equal(parseInt64("abc"), nil)
}

func TestOptionalString(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	is.Equal(optionalString(""), nil)
	is.Equal(optionalString("x"), "x")
}

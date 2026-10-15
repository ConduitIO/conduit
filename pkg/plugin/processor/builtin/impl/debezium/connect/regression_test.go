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
	"math/big"
	"strconv"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

// Regression tests for the #3076 review findings. Each failed before its fix.

// BLOCKER 1: decimal.format=NUMERIC writes BigDecimal.toString, not plain
// notation. A zero in a numeric(p, s>=7) column is the common case.
func TestReviewNumericDecimalUsesBigDecimalToString(t *testing.T) {
	is := is.New(t)
	conv := NewJSONConverter()
	conv.SchemasEnable = false
	conv.DecimalFormat = DecimalNumeric
	for _, tc := range []struct {
		unscaled int64
		scale    int32
		want     string
	}{
		{1, 7, "1E-7"},
		{0, 10, "0E-10"},
		{5, -3, "5E+3"},
		{0, -2, "0E+2"},
		{0, 7, "0E-7"},
		{1, 6, "0.000001"},
		{150, 2, "1.50"},
	} {
		s := decimalSchema(itoa(tc.scale))
		got, err := conv.Marshal(s, Decimal{Unscaled: big.NewInt(tc.unscaled), Scale: tc.scale})
		is.NoErr(err)
		is.Equal(string(got), tc.want)
		// and the payload parser reads it back to the same value
		goVal, err := ParsePayloadJSON(s, got, DecimalNumeric)
		is.NoErr(err)
		cv, err := FromGo(s, goVal)
		is.NoErr(err)
		back, err := conv.Marshal(s, cv)
		is.NoErr(err)
		is.Equal(string(back), tc.want)
	}
}

func itoa(i int32) string { return big.NewInt(int64(i)).String() }

// BLOCKER 2: an unknown field must be refused even when an optional field is
// absent and the key counts happen to match.
func TestReviewUnknownFieldWithAbsentOptional(t *testing.T) {
	s, err := ParseSchemaJSON([]byte(`{"type":"struct","fields":[{"type":"string","optional":true,"field":"a"},{"type":"int32","optional":false,"field":"b"}],"optional":false}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = FromGo(s, map[string]any{"b": 1, "x_added_by_processor": "important"})
	if ce, ok := conduiterr.Get(err); !ok || ce.Code != CodeRecordSchemaMismatch {
		t.Fatalf("want record_schema_mismatch, got %v", err)
	}
}

// SHOULD 4: the header converter substitutes struct-field defaults (as
// Struct.get does) and never panics on a wrongly typed value.
func TestReviewHeaderDefaultsAndTypes(t *testing.T) {
	is := is.New(t)
	s := &Schema{Type: TypeStruct, Fields: []Field{
		{Name: "a", Schema: &Schema{Type: TypeString, Optional: true, Default: "dflt", HasDefault: true}},
		{Name: "arr", Schema: &Schema{Type: TypeArray, Optional: true, Items: &Schema{Type: TypeInt32, Optional: true, Default: int64(9), HasDefault: true}}},
	}}
	cv, err := FromGo(s, map[string]any{"a": nil, "arr": []any{1, nil}})
	is.NoErr(err)
	got, err := SimpleHeaderConverter{}.Marshal(s, cv)
	is.NoErr(err)
	is.Equal(string(got), `{"a":"dflt","arr":[1,null]}`) // array elements keep null

	for _, tc := range []struct {
		s *Schema
		v any
	}{
		{&Schema{Type: TypeInt32}, "not an int"},
		{&Schema{Type: TypeFloat64}, 1},
		{&Schema{Type: TypeStruct}, "x"},
		{&Schema{Type: TypeArray, Items: &Schema{Type: TypeInt32}}, []int{1}},
		{decimalSchema("2"), "1.00"},
	} {
		_, err := SimpleHeaderConverter{}.Marshal(tc.s, tc.v)
		ce, ok := conduiterr.Get(err)
		is.True(ok)
		is.Equal(ce.Code, CodeRecordSchemaMismatch)
	}
}

// NIT: non-UTF-8 names are refused by Validate and by every writer.
func TestReviewInvalidUTF8Names(t *testing.T) {
	is := is.New(t)
	bad := "a\xff"
	s := &Schema{Type: TypeStruct, Fields: []Field{{Name: bad, Schema: &Schema{Type: TypeString}}}}
	is.True(s.Validate() != nil)
	cv := &Struct{Schema: s, Values: []any{"x"}}
	_, err := NewJSONConverter().Marshal(s, cv)
	is.True(err != nil)
	_, err = SimpleHeaderConverter{}.Marshal(s, cv)
	is.True(err != nil)
	_, err = NewJSONConverter().MarshalSchema(&Schema{Type: TypeString, Name: bad})
	is.True(err != nil)
}

// NIT: nesting is limited exactly as Jackson limits it.
func TestReviewNestingLimit(t *testing.T) {
	nest := func(n int) (*Schema, any) {
		s, v := &Schema{Type: TypeInt32}, any(int64(1))
		for i := 0; i < n; i++ {
			s, v = &Schema{Type: TypeArray, Items: s}, []any{v}
		}
		return s, v
	}
	conv := NewJSONConverter()
	conv.SchemasEnable = false
	if _, err := conv.Marshal(nest(MaxNestingDepth)); err != nil {
		t.Fatalf("depth %d must serialize: %v", MaxNestingDepth, err)
	}
	_, err := conv.Marshal(nest(MaxNestingDepth + 1))
	if ce, ok := conduiterr.Get(err); !ok || ce.Code != CodeValueOutOfRange {
		t.Fatalf("depth %d: want value_out_of_range, got %v", MaxNestingDepth+1, err)
	}
}

// Re-review SHOULD 1: an absent optional field with a default must not be
// written as the default; the row never had that value.
func TestReviewAbsentFieldNotReplacedByDefault(t *testing.T) {
	s := &Schema{Type: TypeStruct, Fields: []Field{
		{Name: "a", Schema: &Schema{Type: TypeString, Optional: true, Default: "D", HasDefault: true}},
		{Name: "b", Schema: &Schema{Type: TypeInt32}},
	}}
	_, err := FromGo(s, map[string]any{"b": 1})
	if ce, ok := conduiterr.Get(err); !ok || ce.Code != CodeRecordSchemaMismatch {
		t.Fatalf("want record_schema_mismatch for the absent field, got %v", err)
	}
	// An explicit null is the row's value and follows replace.null.with.default.
	cv, err := FromGo(s, map[string]any{"a": nil, "b": 1})
	if err != nil {
		t.Fatal(err)
	}
	conv := NewJSONConverter()
	conv.SchemasEnable = false
	got, _ := conv.Marshal(s, cv)
	if string(got) != `{"a":"D","b":1}` {
		t.Fatalf("got %s", got)
	}
}

// Re-review NIT: a null in a required field with a default is accepted and
// encoded as Kafka encodes it: the default with replace.null.with.default,
// an error without (oracle probe C8).
func TestReviewRequiredNullWithDefault(t *testing.T) {
	is := is.New(t)
	s := &Schema{Type: TypeStruct, Fields: []Field{{Name: "r", Schema: &Schema{Type: TypeString, Default: "x", HasDefault: true}}}}
	cv, err := FromGo(s, map[string]any{"r": nil})
	is.NoErr(err)
	conv := NewJSONConverter()
	conv.SchemasEnable = false
	got, err := conv.Marshal(s, cv)
	is.NoErr(err)
	is.Equal(string(got), `{"r":"x"}`)
	h, err := SimpleHeaderConverter{}.Marshal(s, cv)
	is.NoErr(err)
	is.Equal(string(h), `{"r":"x"}`)
	conv.ReplaceNullWithDefault = false
	_, err = conv.Marshal(s, cv)
	is.True(err != nil)
}

// Re-review SHOULD 2: ParsePayloadJSON refuses input a lenient decoder would
// silently alter.
func TestReviewParsePayloadStrict(t *testing.T) {
	key := &Schema{Type: TypeStruct, Fields: []Field{
		{Name: "id", Schema: &Schema{Type: TypeString}},
		{Name: "n", Schema: &Schema{Type: TypeArray, Optional: true, Items: &Schema{Type: TypeStruct, Optional: true, Fields: []Field{{Name: "x", Schema: &Schema{Type: TypeInt32, Optional: true}}}}}},
	}}
	esc := func(hex string) string { return "\\" + "u" + hex }
	for name, in := range map[string]string{
		"invalid UTF-8":         "{\"id\":\"a\xffb\",\"n\":null}",
		"lone high surrogate":   `{"id":"` + esc("d800") + `","n":null}`,
		"high then text":        `{"id":"` + esc("d800") + `x","n":null}`,
		"lone low surrogate":    `{"id":"` + esc("dc00") + `","n":null}`,
		"high then high":        `{"id":"` + esc("d800") + esc("d800") + `","n":null}`,
		"duplicate member":      `{"id":"a","id":"b","n":null}`,
		"nested duplicate":      `{"id":"a","n":[{"x":1,"x":2}]}`,
		"duplicate after array": `{"n":[{"x":1}],"id":"a","n":null}`,
	} {
		_, err := ParsePayloadJSON(key, []byte(in), DecimalNumeric)
		if ce, ok := conduiterr.Get(err); !ok || ce.Code != CodeRecordSchemaMismatch {
			t.Errorf("%s: want record_schema_mismatch, got %v", name, err)
		}
	}
	for name, in := range map[string]string{
		"surrogate pair":           `{"id":"` + esc("d83d") + esc("de00") + `","n":null}`,
		"same name, other objects": `{"id":"a","n":[{"x":1},{"x":2}]}`,
		"escaped backslash then u": `{"id":"\\` + `ud800","n":null}`,
		"non-ASCII raw":            `{"id":"é😀","n":null}`,
	} {
		if _, err := ParsePayloadJSON(key, []byte(in), DecimalNumeric); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func FuzzParsePayloadJSONStrict(f *testing.F) {
	f.Add([]byte(`{"id":"a","n":[{"x":1}]}`))
	f.Add([]byte(`{"id":"a","id":"b"}`))
	key := &Schema{Type: TypeStruct, Fields: []Field{{Name: "id", Schema: &Schema{Type: TypeString, Optional: true}}}}
	f.Fuzz(func(t *testing.T, in []byte) {
		v, err := ParsePayloadJSON(key, in, DecimalNumeric)
		if err != nil {
			return
		}
		// Whatever is accepted round-trips through the converter unchanged
		// in meaning: re-encoding and re-parsing gives the same value.
		cv, err := FromGo(key, v)
		if err != nil {
			return
		}
		conv := NewJSONConverter()
		conv.SchemasEnable = false
		out, err := conv.Marshal(key, cv)
		if err != nil {
			t.Fatalf("accepted %q but cannot encode it: %v", in, err)
		}
		v2, err := ParsePayloadJSON(key, out, DecimalNumeric)
		if err != nil {
			t.Fatalf("encoder output %q does not parse: %v", out, err)
		}
		if m1, m2 := v.(map[string]any)["id"], v2.(map[string]any)["id"]; m1 != m2 {
			t.Fatalf("value changed: %q -> %q", m1, m2)
		}
	})
}

// Re-review NIT: a float32 default is parsed at 32 bits. 1.00000005960464477539
// is the midpoint between two float32 values just above 1; rounding it to a
// float64 first lands on the midpoint and ties to even (1.0), rounding the
// text once gives the upper neighbour.
func TestReviewFloat32DefaultParsedAt32Bits(t *testing.T) {
	s, err := ParseSchemaJSON([]byte(`{"type":"float","optional":true,"default":1.000000059604644775390625000000001}`))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := strconv.ParseFloat("1.000000059604644775390625000000001", 32)
	if got := s.Default.(float32); got != float32(want) || got == 1 {
		t.Fatalf("default %v, want %v", got, float32(want))
	}
}

// Re-review SHOULD 3: Connect temporal types in headers are formatted as
// Kafka's Values formats a java.util.Date (the oracle HT rows cover the
// boundaries; this pins the probe's case).
func TestReviewHeaderTemporal(t *testing.T) {
	one := 1
	s := &Schema{Type: TypeStruct, Fields: []Field{
		{Name: "ts", Schema: &Schema{Type: TypeInt64, Name: LogicalTimestamp, Version: &one}},
		{Name: "d", Schema: &Schema{Type: TypeInt32, Name: LogicalDate, Version: &one}},
		{Name: "t", Schema: &Schema{Type: TypeInt32, Name: LogicalTime, Version: &one}},
	}}
	cv, err := FromGo(s, map[string]any{"ts": int64(1700000000123), "d": 19000, "t": 3723004})
	if err != nil {
		t.Fatal(err)
	}
	got, err := SimpleHeaderConverter{}.Marshal(s, cv)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"ts":2023-11-14T22:13:20.123Z,"d":2022-01-08,"t":01:02:03.004Z}`; string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

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
	cv, err := FromGo(s, map[string]any{"arr": []any{1, nil}})
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

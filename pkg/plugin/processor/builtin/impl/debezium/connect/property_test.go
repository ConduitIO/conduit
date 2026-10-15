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
	"math/big"
	"strconv"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/goccy/go-json"
	"pgregory.net/rapid"
)

func genBigInt() *rapid.Generator[*big.Int] {
	return rapid.Custom(func(t *rapid.T) *big.Int {
		digits := rapid.StringMatching(`[0-9]{1,60}`).Draw(t, "digits")
		u, _ := new(big.Int).SetString(digits, 10)
		if rapid.Bool().Draw(t, "neg") {
			u.Neg(u)
		}
		return u
	})
}

// Two's complement encoding is BigInteger.toByteArray: it round-trips and is
// minimal (no redundant sign byte).
func TestPropTwosComplement(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		u := genBigInt().Draw(t, "u")
		b := twosComplement(u)
		if got := fromTwosComplement(b); got.Cmp(u) != 0 {
			t.Fatalf("round trip %s -> %x -> %s", u, b, got)
		}
		if len(b) > 1 {
			redundantZero := b[0] == 0x00 && b[1]&0x80 == 0
			redundantFF := b[0] == 0xff && b[1]&0x80 != 0
			if redundantZero || redundantFF {
				t.Fatalf("encoding of %s is not minimal: %x", u, b)
			}
		}
	})
}

// Rescaling a decimal never rounds: scaling up preserves the value exactly,
// scaling down succeeds iff the dropped digits are zero, and every failure is
// CodeValueOutOfRange (invariant 6).
func TestPropDecimalNoRounding(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		u := genBigInt().Draw(t, "u")
		scale := rapid.Int32Range(0, 40).Draw(t, "scale")
		target := rapid.Int32Range(0, 40).Draw(t, "target")
		str := Decimal{Unscaled: u, Scale: scale}.String()

		d, err := parseDecimalAtScale(str, target, "$")
		want := new(big.Rat).SetFrac(u, new(big.Int).Exp(bigTen, big.NewInt(int64(scale)), nil))
		exact := new(big.Rat).Mul(want, new(big.Rat).SetInt(new(big.Int).Exp(bigTen, big.NewInt(int64(target)), nil))).IsInt()
		if !exact {
			ce, ok := conduiterr.Get(err)
			if !ok || ce.Code != CodeValueOutOfRange {
				t.Fatalf("%s at scale %d: want value_out_of_range, got %v", str, target, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s at scale %d: %v", str, target, err)
		}
		got := new(big.Rat).SetFrac(d.Unscaled, new(big.Int).Exp(bigTen, big.NewInt(int64(d.Scale)), nil))
		if got.Cmp(want) != 0 || d.Scale != target {
			t.Fatalf("%s at scale %d became %s (scale %d)", str, target, d, d.Scale)
		}
	})
}

// VariableScaleDecimal never changes the value and never has trailing zeros
// in the unscaled value unless the scale is already 0.
func TestPropVariableScaleDecimal(t *testing.T) {
	s := &Schema{Type: TypeStruct, Name: LogicalVariableScaleDecimal, Fields: []Field{
		{Name: "scale", Schema: &Schema{Type: TypeInt32}},
		{Name: "value", Schema: &Schema{Type: TypeBytes}},
	}}
	rapid.Check(t, func(t *rapid.T) {
		u := genBigInt().Draw(t, "u")
		scale := rapid.Int32Range(-5, 40).Draw(t, "scale")
		var str string
		if scale >= 0 {
			str = Decimal{Unscaled: u, Scale: scale}.String()
		} else {
			str = u.String() + "E" + strconv.Itoa(int(-scale))
		}
		st, err := variableScaleDecimal(s, str, "$")
		if err != nil {
			t.Fatal(err)
		}
		gotScale := st.Values[0].(int64)
		gotU := fromTwosComplement(st.Values[1].([]byte))
		if gotScale < 0 {
			t.Fatalf("negative scale %d for %s", gotScale, str)
		}
		in, _ := new(big.Rat).SetString(str)
		if in == nil {
			f, _, _ := big.ParseFloat(str, 10, 4096, big.ToNearestEven)
			in, _ = f.Rat(nil)
		}
		got := new(big.Rat).SetFrac(gotU, new(big.Int).Exp(bigTen, big.NewInt(gotScale), nil))
		if got.Cmp(in) != 0 {
			t.Fatalf("%s became scale %d value %s", str, gotScale, gotU)
		}
		if gotScale > 0 && new(big.Int).Rem(gotU, bigTen).Sign() == 0 {
			t.Fatalf("%s kept a trailing zero: scale %d value %s", str, gotScale, gotU)
		}
	})
}

// JavaFloatString round-trips and uses E notation exactly outside
// [1e-3, 1e7), as Double.toString and Float.toString do.
func TestPropJavaFloatString(t *testing.T) {
	check := func(t *rapid.T, f float64, bits int) {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return
		}
		s := JavaFloatString(f, bits)
		back, err := strconv.ParseFloat(strings.Replace(s, "E", "e", 1), bits)
		if err != nil || back != f {
			t.Fatalf("%v (%d bit) printed as %q, parses back as %v (%v)", f, bits, s, back, err)
		}
		a := math.Abs(f)
		sci := strings.Contains(s, "E")
		if f != 0 && sci != (a < 1e-3 || a >= 1e7) {
			t.Fatalf("%v printed as %q: wrong notation", f, s)
		}
		if !strings.Contains(s, ".") {
			t.Fatalf("%v printed as %q: Java always prints a fraction digit", f, s)
		}
	}
	t.Run("double", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			check(t, math.Float64frombits(rapid.Uint64().Draw(t, "bits")), 64)
		})
	})
	t.Run("float", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			check(t, float64(math.Float32frombits(rapid.Uint32().Draw(t, "bits"))), 32)
		})
	})
}

// Strings written by appendJSONString are valid JSON that decode to the
// original string.
func TestPropJSONString(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.String().Draw(t, "s")
		var buf bytes.Buffer
		appendJSONString(&buf, s)
		var back string
		if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
			t.Fatalf("%q encoded as invalid JSON %s: %v", s, buf.Bytes(), err)
		}
		if back != s {
			t.Fatalf("%q encoded as %s decodes to %q", s, buf.Bytes(), back)
		}
	})
}

func genSchema(depth int) *rapid.Generator[*Schema] {
	return rapid.Custom(func(t *rapid.T) *Schema {
		prims := []Type{TypeInt8, TypeInt16, TypeInt32, TypeInt64, TypeFloat32, TypeFloat64, TypeBoolean, TypeString, TypeBytes}
		kinds := prims
		if depth < 3 {
			kinds = append(append([]Type{}, prims...), TypeArray, TypeMap, TypeStruct)
		}
		s := &Schema{Type: rapid.SampledFrom(kinds).Draw(t, "type"), Optional: rapid.Bool().Draw(t, "optional")}
		if rapid.Bool().Draw(t, "named") {
			s.Name = rapid.StringMatching(`[a-z][a-z0-9_.]{0,12}`).Draw(t, "name")
		}
		if rapid.Bool().Draw(t, "versioned") {
			v := rapid.IntRange(0, 5).Draw(t, "version")
			s.Version = &v
		}
		if rapid.Bool().Draw(t, "doc") {
			d := rapid.String().Draw(t, "docText")
			s.Doc = &d
		}
		if rapid.Bool().Draw(t, "params") {
			n := rapid.IntRange(0, 4).Draw(t, "nparams")
			s.Parameters = []Param{}
			seen := map[string]bool{}
			for i := 0; i < n; i++ {
				k := rapid.StringMatching(`[a-z.]{1,8}`).Draw(t, "pk")
				if seen[k] {
					continue
				}
				seen[k] = true
				s.Parameters = append(s.Parameters, Param{Key: k, Value: rapid.String().Draw(t, "pv")})
			}
		}
		switch s.Type { //nolint:exhaustive // the remaining types share the default branch
		case TypeArray:
			s.Items = genSchema(depth+1).Draw(t, "items")
		case TypeMap:
			s.Keys = genSchema(depth+1).Draw(t, "keys")
			s.Values = genSchema(depth+1).Draw(t, "values")
		case TypeStruct:
			n := rapid.IntRange(0, 5).Draw(t, "nfields")
			s.Fields = []Field{}
			seen := map[string]bool{}
			for i := 0; i < n; i++ {
				name := rapid.StringMatching(`[a-z_]{1,6}`).Draw(t, "field")
				if seen[name] {
					continue
				}
				seen[name] = true
				s.Fields = append(s.Fields, Field{Name: name, Schema: genSchema(depth+1).Draw(t, "fieldSchema")})
			}
		case TypeString:
			if rapid.Bool().Draw(t, "hasDefault") {
				s.Default, s.HasDefault = rapid.String().Draw(t, "default"), true
			}
		case TypeInt32:
			if rapid.Bool().Draw(t, "hasDefault") {
				s.Default, s.HasDefault = int64(rapid.Int32().Draw(t, "default")), true
			}
		}
		return s
	})
}

// A schema written by the converter parses back to a schema that the
// converter writes identically: field order, parameter order, presence of
// version and doc, and defaults all survive.
func TestPropSchemaRoundTrip(t *testing.T) {
	conv := NewJSONConverter()
	rapid.Check(t, func(t *rapid.T) {
		s := genSchema(0).Draw(t, "schema")
		b, err := conv.MarshalSchema(s)
		if err != nil {
			t.Fatal(err)
		}
		back, err := ParseSchemaJSON(b)
		if err != nil {
			t.Fatalf("parse %s: %v", b, err)
		}
		b2, err := conv.MarshalSchema(back)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, b2) {
			t.Fatalf("round trip changed the schema\n%s\n%s", b, b2)
		}
	})
}

// Struct output follows schema field order, never the order of the Go map
// the record value came in.
func TestPropStructFieldOrder(t *testing.T) {
	conv := NewJSONConverter()
	conv.SchemasEnable = false
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 12).Draw(t, "n")
		s := &Schema{Type: TypeStruct}
		m := map[string]any{}
		var want bytes.Buffer
		want.WriteByte('{')
		for i := 0; i < n; i++ {
			name := "f" + strconv.Itoa(rapid.IntRange(0, 1000).Draw(t, "name"))
			if _, dup := m[name]; dup {
				continue
			}
			v := rapid.Int64().Draw(t, "v")
			s.Fields = append(s.Fields, Field{Name: name, Schema: &Schema{Type: TypeInt64}})
			m[name] = v
			if want.Len() > 1 {
				want.WriteByte(',')
			}
			want.WriteString(strconv.Quote(name) + ":" + strconv.FormatInt(v, 10))
		}
		want.WriteByte('}')
		cv, err := FromGo(s, m)
		if err != nil {
			t.Fatal(err)
		}
		got, err := conv.Marshal(s, cv)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want.Bytes()) {
			t.Fatalf("got %s want %s", got, want.Bytes())
		}
	})
}

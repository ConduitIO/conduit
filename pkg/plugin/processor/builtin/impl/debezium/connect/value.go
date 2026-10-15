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
	"encoding/base64"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/goccy/go-json"
)

// Struct is a Connect struct value. Values are positional: Values[i] belongs
// to Schema.Fields[i].
type Struct struct {
	Schema *Schema
	Values []any
}

// MapEntry is one entry of a Connect map value. Maps are lists so that the
// output order is the input order.
type MapEntry struct {
	Key   any
	Value any
}

// Decimal is a value of the Connect Decimal logical type: Unscaled * 10^-Scale.
type Decimal struct {
	Unscaled *big.Int
	Scale    int32
}

// String renders the decimal as BigDecimal.toPlainString does.
func (d Decimal) String() string {
	return plainDecimal(d)
}

// FromGo converts a record value into the Connect value for schema s.
//
// The accepted Go types are deliberately narrow (invariant 6): a value that
// would need a conversion that can lose information is rejected with
// CodeRecordSchemaMismatch or CodeValueOutOfRange instead of being coerced.
//
//   - int8, int16, int32, int64: any Go integer type, range-checked. Floats
//     are refused even when integral, because a float64 integer above 2^53
//     has already lost precision before it got here (for example by crossing
//     a protobuf Struct) and cannot be told apart from a correct one.
//   - float: float32, or a float64 that converts to float32 exactly.
//     double: float64 or float32.
//   - boolean: bool. string: string. bytes: []byte, never a string.
//   - Decimal: a decimal string ("-12.30"); the value is rescaled to the
//     schema scale without rounding.
//   - VariableScaleDecimal: a decimal string, converted as Debezium does.
//   - array: []any or a typed slice of accepted element types.
//   - map: []MapEntry only. A Go map has no order, and the converter output
//     of a Connect map is ordered.
//   - struct: map[string]any holding every field of the schema (nil for a
//     null, never absent) and no other key; field names are looked up in
//     schema order, so map iteration order never matters. Or a *Struct
//     built against s.
//
// nil is accepted for an optional schema, and for a required schema with a
// default (the converter then writes the default or fails, as Kafka does); it
// is refused for any other required schema.
func FromGo(s *Schema, v any) (any, error) {
	return fromGo(s, v, "$")
}

//nolint:gocyclo // one branch per Connect type; splitting it hides the mapping
func fromGo(s *Schema, v any, path string) (any, error) {
	if v == nil {
		// A null in a required field with a default is what Kafka Connect
		// accepts too: the converter writes the default when
		// replace.null.with.default is on and fails the record when it is
		// off (oracle probe C8), so the decision is the converter's.
		if s.Optional || (s.HasDefault && s.Default != nil) {
			return nil, nil
		}
		return nil, mismatch(path, "null value for required field")
	}
	switch s.Name {
	case LogicalDecimal:
		str, ok := v.(string)
		if !ok {
			return nil, mismatch(path, fmt.Sprintf("Decimal value must be a decimal string, got %T", v))
		}
		scale, err := s.decimalScale()
		if err != nil {
			return nil, mismatch(path, err.Error())
		}
		return parseDecimalAtScale(str, scale, path)
	case LogicalVariableScaleDecimal:
		if s.Type != TypeStruct {
			return nil, mismatch(path, "VariableScaleDecimal must be a struct schema")
		}
		str, ok := v.(string)
		if !ok {
			return nil, mismatch(path, fmt.Sprintf("VariableScaleDecimal value must be a decimal string, got %T", v))
		}
		return variableScaleDecimal(s, str, path)
	}

	switch s.Type {
	case TypeInt8:
		return intValue(v, math.MinInt8, math.MaxInt8, path)
	case TypeInt16:
		return intValue(v, math.MinInt16, math.MaxInt16, path)
	case TypeInt32:
		return intValue(v, math.MinInt32, math.MaxInt32, path)
	case TypeInt64:
		return intValue(v, math.MinInt64, math.MaxInt64, path)
	case TypeFloat32:
		switch f := v.(type) {
		case float32:
			return f, nil
		case float64:
			f32 := float32(f)
			if float64(f32) == f || (math.IsNaN(f) && math.IsNaN(float64(f32))) {
				return f32, nil
			}
			return nil, outOfRange(path, fmt.Sprintf("float64 %v is not exactly representable as float32", f))
		}
		return nil, mismatch(path, fmt.Sprintf("float value must be float32, got %T", v))
	case TypeFloat64:
		switch f := v.(type) {
		case float64:
			return f, nil
		case float32:
			return float64(f), nil
		}
		return nil, mismatch(path, fmt.Sprintf("double value must be float64, got %T", v))
	case TypeBoolean:
		if b, ok := v.(bool); ok {
			return b, nil
		}
		return nil, mismatch(path, fmt.Sprintf("boolean value must be bool, got %T", v))
	case TypeString:
		if str, ok := v.(string); ok {
			return str, nil
		}
		return nil, mismatch(path, fmt.Sprintf("string value must be string, got %T", v))
	case TypeBytes:
		if b, ok := v.([]byte); ok {
			return b, nil
		}
		return nil, mismatch(path, fmt.Sprintf("bytes value must be []byte, got %T", v))
	case TypeArray:
		elems, ok := toSlice(v)
		if !ok {
			return nil, mismatch(path, fmt.Sprintf("array value must be a slice, got %T", v))
		}
		out := make([]any, len(elems))
		for i, e := range elems {
			cv, err := fromGo(s.Items, e, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out[i] = cv
		}
		return out, nil
	case TypeMap:
		entries, ok := v.([]MapEntry)
		if !ok {
			return nil, mismatch(path, fmt.Sprintf("map value must be []connect.MapEntry, got %T", v))
		}
		out := make([]MapEntry, len(entries))
		for i, e := range entries {
			k, err := fromGo(s.Keys, e.Key, fmt.Sprintf("%s{key %d}", path, i))
			if err != nil {
				return nil, err
			}
			val, err := fromGo(s.Values, e.Value, fmt.Sprintf("%s{value %d}", path, i))
			if err != nil {
				return nil, err
			}
			out[i] = MapEntry{Key: k, Value: val}
		}
		return out, nil
	case TypeStruct:
		return structValue(s, v, path)
	}
	return nil, conduiterr.New(CodeUnsupportedType, fmt.Sprintf("%s: unsupported Connect type %q", path, s.Type))
}

func structValue(s *Schema, v any, path string) (*Struct, error) {
	if st, ok := v.(*Struct); ok {
		// A *Struct is accepted only when built against this very schema.
		if st.Schema != s || len(st.Values) != len(s.Fields) {
			return nil, mismatch(path, "struct value has a different schema")
		}
		return st, nil
	}
	m, ok := toStringMap(v)
	if !ok {
		return nil, mismatch(path, fmt.Sprintf("struct value must be map[string]any, got %T", v))
	}
	out := &Struct{Schema: s, Values: make([]any, len(s.Fields))}
	matched := 0
	for i, f := range s.Fields {
		fv, present := m[f.Name]
		// Invariant 6: every field must be present, null or not. An absent
		// field would become null here, and with replace.null.with.default
		// the converter would write the column default: a value the row
		// never had (#3076 re-review). A Debezium-mode source sends every
		// column; a processor that removes one fails the record.
		if !present {
			return nil, mismatch(path+"."+f.Name, "field missing (send null for a NULL column)")
		}
		matched++
		cv, err := fromGo(f.Schema, fv, path+"."+f.Name)
		if err != nil {
			return nil, err
		}
		out.Values[i] = cv
	}
	// Invariant 6: a field the schema does not know is never dropped silently.
	// Every key of m must name a field. With every field present, the count
	// tells; the scan names the culprit.
	if matched != len(m) {
		for k := range m {
			if _, idx := s.Field(k); idx < 0 {
				return nil, mismatch(path+"."+k, "field not in the descriptor")
			}
		}
	}
	return out, nil
}

func intValue(v any, lo, hi int64, path string) (int64, error) {
	var i int64
	switch n := v.(type) {
	case int:
		i = int64(n)
	case int8:
		i = int64(n)
	case int16:
		i = int64(n)
	case int32:
		i = int64(n)
	case int64:
		i = n
	case uint:
		if uint64(n) > math.MaxInt64 {
			return 0, outOfRange(path, fmt.Sprintf("%d does not fit the Connect type", n))
		}
		i = int64(n)
	case uint8:
		i = int64(n)
	case uint16:
		i = int64(n)
	case uint32:
		i = int64(n)
	case uint64:
		if n > math.MaxInt64 {
			return 0, outOfRange(path, fmt.Sprintf("%d does not fit the Connect type", n))
		}
		i = int64(n)
	default:
		return 0, mismatch(path, fmt.Sprintf("integer value must be a Go integer, got %T", v))
	}
	if i < lo || i > hi {
		return 0, outOfRange(path, fmt.Sprintf("%d does not fit the Connect type", i))
	}
	return i, nil
}

// toSlice accepts []any and any other slice type (a source may hand over
// []string or []int64), read through reflection without converting the
// elements; each element is then checked against the item schema.
func toSlice(v any) ([]any, bool) {
	if s, ok := v.([]any); ok {
		return s, true
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice || rv.Type().Elem().Kind() == reflect.Uint8 {
		return nil, false // []byte is bytes, not an array
	}
	out := make([]any, rv.Len())
	for i := range out {
		out[i] = rv.Index(i).Interface()
	}
	return out, true
}

// toStringMap accepts map[string]any and named map types with that underlying
// type (opencdc.StructuredData) without copying.
func toStringMap(v any) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Map && rv.Type().ConvertibleTo(stringMapType) {
		return rv.Convert(stringMapType).Interface().(map[string]any), true
	}
	return nil, false
}

var stringMapType = reflect.TypeOf(map[string]any(nil))

// parseDecimal parses a plain or exponent decimal string exactly. It returns
// the unscaled value and scale as written ("1.10" is 110, 2), the form Java's
// new BigDecimal(String) produces. NaN and infinities are not decimals.
func parseDecimal(str string) (*big.Int, int32, error) {
	s := str
	if s == "" {
		return nil, 0, cerrors.New("empty decimal")
	}
	exp := int64(0)
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.ParseInt(s[i+1:], 10, 32)
		if err != nil {
			return nil, 0, cerrors.Errorf("invalid exponent in %q", str)
		}
		exp = e
		s = s[:i]
	}
	neg := false
	switch {
	case strings.HasPrefix(s, "-"):
		neg, s = true, s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	intPart, frac, hasDot := strings.Cut(s, ".")
	if intPart == "" && frac == "" {
		return nil, 0, cerrors.Errorf("invalid decimal %q", str)
	}
	if hasDot && frac == "" && intPart == "" {
		return nil, 0, cerrors.Errorf("invalid decimal %q", str)
	}
	digits := intPart + frac
	for _, c := range digits {
		if c < '0' || c > '9' {
			return nil, 0, cerrors.Errorf("invalid decimal %q", str)
		}
	}
	scale := int64(len(frac)) - exp
	// PostgreSQL numerics have at most 131072 digits before the point and
	// 16383 after; a scale far outside that is malformed input, and bounding
	// it keeps rescaling and rendering from allocating without limit.
	if scale < -maxDecimalScale || scale > maxDecimalScale || len(digits) > maxDecimalScale {
		return nil, 0, cerrors.Errorf("decimal scale out of range in %q", str)
	}
	u, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return nil, 0, cerrors.Errorf("invalid decimal %q", str)
	}
	if neg {
		u.Neg(u)
	}
	return u, int32(scale), nil
}

var bigTen = big.NewInt(10)

// maxDecimalScale bounds the scale and digit count parseDecimal accepts.
const maxDecimalScale = 1 << 18

// parseDecimalAtScale is BigDecimal.setScale(scale) with RoundingMode
// UNNECESSARY: scaling up multiplies, scaling down succeeds only when the
// dropped digits are zero. Anything else is CodeValueOutOfRange, matching the
// ArithmeticException Debezium raises.
func parseDecimalAtScale(str string, scale int32, path string) (Decimal, error) {
	u, sc, err := parseDecimal(str)
	if err != nil {
		if isSpecialDecimal(str) {
			return Decimal{}, outOfRange(path, fmt.Sprintf("%s cannot be encoded as a precise Decimal", str))
		}
		return Decimal{}, mismatch(path, err.Error())
	}
	switch {
	case sc == scale:
	case sc < scale:
		u.Mul(u, new(big.Int).Exp(bigTen, big.NewInt(int64(scale-sc)), nil))
	default:
		div := new(big.Int).Exp(bigTen, big.NewInt(int64(sc-scale)), nil)
		q, r := new(big.Int).QuoRem(u, div, new(big.Int))
		if r.Sign() != 0 {
			return Decimal{}, outOfRange(path, fmt.Sprintf("%s has more fractional digits than scale %d; refusing to round", str, scale))
		}
		u = q
	}
	return Decimal{Unscaled: u, Scale: scale}, nil
}

func isSpecialDecimal(s string) bool {
	switch strings.ToLower(s) {
	case "nan", "infinity", "-infinity", "+infinity", "inf", "-inf":
		return true
	}
	return false
}

// variableScaleDecimal builds Debezium's VariableScaleDecimal struct
// {scale int32, value bytes}. The goldens fix the rule: trailing zeros are
// stripped ("1.10" becomes scale 1, value 11; "1.000" scale 0, value 1), but
// the scale never goes below zero ("100" stays scale 0, value 100, where a
// bare stripTrailingZeros would give scale -2, value 1).
func variableScaleDecimal(s *Schema, str, path string) (*Struct, error) {
	u, sc, err := parseDecimal(str)
	if err != nil {
		if isSpecialDecimal(str) {
			return nil, outOfRange(path, fmt.Sprintf("%s cannot be encoded as a VariableScaleDecimal", str))
		}
		return nil, mismatch(path, err.Error())
	}
	u, sc = stripTrailingZeros(u, sc)
	if sc < 0 {
		u.Mul(u, new(big.Int).Exp(bigTen, big.NewInt(int64(-sc)), nil))
		sc = 0
	}
	if len(s.Fields) != 2 || s.Fields[0].Name != "scale" || s.Fields[1].Name != "value" {
		return nil, mismatch(path, "VariableScaleDecimal schema must have fields scale, value")
	}
	return &Struct{Schema: s, Values: []any{int64(sc), twosComplement(u)}}, nil
}

// stripTrailingZeros is BigDecimal.stripTrailingZeros: it removes trailing
// zero digits of the unscaled value, lowering the scale (possibly below zero).
// Zero becomes scale 0 (JDK 8 and later).
func stripTrailingZeros(u *big.Int, scale int32) (*big.Int, int32) {
	if u.Sign() == 0 {
		return new(big.Int), 0
	}
	u = new(big.Int).Set(u)
	r := new(big.Int)
	q := new(big.Int)
	for {
		q.QuoRem(u, bigTen, r)
		if r.Sign() != 0 || scale == math.MinInt32 {
			return u, scale
		}
		u.Set(q)
		scale--
	}
}

// twosComplement is BigInteger.toByteArray: the minimal big-endian two's
// complement encoding, at least one byte.
func twosComplement(u *big.Int) []byte {
	if u.Sign() >= 0 {
		b := u.Bytes()
		if len(b) == 0 || b[0]&0x80 != 0 {
			b = append([]byte{0}, b...)
		}
		return b
	}
	// -u - 1, inverted, is the two's complement of u.
	m := new(big.Int).Neg(u)
	m.Sub(m, big.NewInt(1))
	b := m.Bytes()
	for i := range b {
		b[i] = ^b[i]
	}
	if len(b) == 0 || b[0]&0x80 == 0 {
		b = append([]byte{0xff}, b...)
	}
	return b
}

// fromTwosComplement is new BigInteger(byte[]).
func fromTwosComplement(b []byte) *big.Int {
	u := new(big.Int).SetBytes(b)
	if len(b) > 0 && b[0]&0x80 != 0 {
		u.Sub(u, new(big.Int).Lsh(big.NewInt(1), uint(len(b))*8))
	}
	return u
}

// defaultFromJSON converts a schema default written in JsonConverter form
// back into a Connect value.
func defaultFromJSON(s *Schema, raw json.RawMessage) (any, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return jsonToConnect(s, v)
}

//nolint:gocyclo // one branch per Connect type; splitting it hides the mapping
func jsonToConnect(s *Schema, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if s.Name == LogicalDecimal {
		scale, err := s.decimalScale()
		if err != nil {
			return nil, err
		}
		switch d := v.(type) {
		case string: // decimal.format=BASE64
			b, err := base64.StdEncoding.DecodeString(d)
			if err != nil {
				return nil, err
			}
			return Decimal{Unscaled: fromTwosComplement(b), Scale: scale}, nil
		case json.Number: // decimal.format=NUMERIC
			return parseDecimalAtScale(d.String(), scale, "$default")
		}
		return nil, cerrors.New("Decimal default must be base64 or a number")
	}
	switch s.Type { //nolint:exhaustive // the remaining types share the default branch
	case TypeInt8, TypeInt16, TypeInt32, TypeInt64:
		n, ok := v.(json.Number)
		if !ok {
			return nil, cerrors.Errorf("%s default must be a number", s.Type)
		}
		i, err := n.Int64()
		if err != nil {
			return nil, err
		}
		lo, hi := intRange(s.Type)
		if i < lo || i > hi {
			return nil, cerrors.Errorf("%s default %d out of range", s.Type, i)
		}
		return i, nil
	case TypeFloat32, TypeFloat64:
		var f float64
		switch n := v.(type) {
		case json.Number:
			// Parse at the schema's width: rounding the text to float64 and
			// then to float32 can differ from rounding it to float32 once.
			bits := 64
			if s.Type == TypeFloat32 {
				bits = 32
			}
			var err error
			if f, err = strconv.ParseFloat(n.String(), bits); err != nil {
				return nil, err
			}
		case string:
			switch n {
			case "NaN":
				f = math.NaN()
			case "Infinity":
				f = math.Inf(1)
			case "-Infinity":
				f = math.Inf(-1)
			default:
				return nil, cerrors.Errorf("invalid %s default %q", s.Type, n)
			}
		default:
			return nil, cerrors.Errorf("%s default must be a number", s.Type)
		}
		if s.Type == TypeFloat32 {
			return float32(f), nil
		}
		return f, nil
	case TypeBoolean:
		b, ok := v.(bool)
		if !ok {
			return nil, cerrors.New("boolean default must be a boolean")
		}
		return b, nil
	case TypeString:
		str, ok := v.(string)
		if !ok {
			return nil, cerrors.New("string default must be a string")
		}
		return str, nil
	case TypeBytes:
		str, ok := v.(string)
		if !ok {
			return nil, cerrors.New("bytes default must be base64")
		}
		return base64.StdEncoding.DecodeString(str)
	case TypeArray:
		arr, ok := v.([]any)
		if !ok {
			return nil, cerrors.New("array default must be an array")
		}
		out := make([]any, len(arr))
		for i, e := range arr {
			cv, err := jsonToConnect(s.Items, e)
			if err != nil {
				return nil, err
			}
			out[i] = cv
		}
		return out, nil
	}
	// Map and struct defaults do not occur in Debezium schemas; refusing them
	// is safer than guessing an order for a JSON object.
	return nil, conduiterr.New(CodeUnsupportedType, fmt.Sprintf("%s defaults are not supported", s.Type))
}

func intRange(t Type) (int64, int64) {
	switch t { //nolint:exhaustive // the remaining types share the default branch
	case TypeInt8:
		return math.MinInt8, math.MaxInt8
	case TypeInt16:
		return math.MinInt16, math.MaxInt16
	case TypeInt32:
		return math.MinInt32, math.MaxInt32
	}
	return math.MinInt64, math.MaxInt64
}

func mismatch(path, msg string) error {
	return conduiterr.New(CodeRecordSchemaMismatch, path+": "+msg)
}

func outOfRange(path, msg string) error {
	return conduiterr.New(CodeValueOutOfRange, path+": "+msg)
}

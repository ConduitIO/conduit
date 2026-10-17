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
	"encoding/base64"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/goccy/go-json"
)

// appendJSONString writes s with the same escaping the Confluent JsonConverter
// (Jackson) produces, so the encoder's bytes match Debezium's:
//
//   - `"`, `\` and the control characters below 0x20 are escaped (the short
//     forms \b \t \n \f \r where Jackson uses them, \u00XX otherwise);
//   - U+2028 and U+2029 are escaped as \u2028 / \u2029;
//   - characters outside the Basic Multilingual Plane are written as a
//     surrogate pair (\uD83D\uDE00);
//   - everything else, including DEL (0x7F), the C1 range, `<>&'` and
//     non-ASCII BMP characters, is written raw.
func appendJSONString(b *bytes.Buffer, s string) {
	// Hex digits for \u escapes (Jackson uses uppercase).
	const hex = "0123456789ABCDEF"
	var buf [12]byte // room for \uXXXX + one rune, see below
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20:
			b.WriteString(`\u00`)
			b.WriteByte(hex[r>>4])
			b.WriteByte(hex[r&0xF])
		case r == 0x2028:
			b.WriteString(`\u2028`)
		case r == 0x2029:
			b.WriteString(`\u2029`)
		case r > utf8.MaxRune:
			// Invalid rune; write the replacement character's escapes.
			b.WriteString(`\uFFFD`)
		case r >= 0x10000:
			r1, r2 := utf16.EncodeRune(r)
			b.WriteString(`\u`)
			b.WriteByte(hex[r1>>12&0xF])
			b.WriteByte(hex[r1>>8&0xF])
			b.WriteByte(hex[r1>>4&0xF])
			b.WriteByte(hex[r1&0xF])
			b.WriteString(`\u`)
			b.WriteByte(hex[r2>>12&0xF])
			b.WriteByte(hex[r2>>8&0xF])
			b.WriteByte(hex[r2>>4&0xF])
			b.WriteByte(hex[r2&0xF])
		default:
			// Raw rune (BMP, including DEL and non-ASCII).
			n := utf8.EncodeRune(buf[:], r)
			b.Write(buf[:n])
		}
	}
}

// appendFloat writes f the way Java's Float.toString / Double.toString write
// it: uppercase E, no '+' in the exponent, and a decimal point even for whole
// numbers. NaN and infinity are written bare, as Jackson writes them. D9
// documents the residual exponent-threshold differences (numeric equality
// holds).
func appendFloat(b *bytes.Buffer, f float64, bits int) {
	switch {
	case math.IsNaN(f):
		b.WriteString("NaN")
		return
	case math.IsInf(f, 1):
		b.WriteString("Infinity")
		return
	case math.IsInf(f, -1):
		b.WriteString("-Infinity")
		return
	}
	s := strconv.FormatFloat(f, 'g', -1, bits)
	if strings.Contains(s, "e") {
		mant, exp, _ := strings.Cut(s, "e")
		exp = strings.TrimPrefix(exp, "+")
		if !strings.Contains(mant, ".") {
			mant += ".0"
		}
		b.WriteString(mant)
		b.WriteString("E")
		b.WriteString(exp)
		return
	}
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	b.WriteString(s)
}

// writeValue writes a record value against its Connect schema, in the
// JsonConverter's encoding. decimalFormat selects the Decimal encoding:
// decimalBase64 writes the unscaled value as base64, decimalNumeric writes
// the decimal literal as a JSON number.
func writeValue(b *bytes.Buffer, s *schema, v any, decimalFormat string) error {
	if v == nil {
		if s.Optional {
			b.WriteString("null")
			return nil
		}
		return newCoded(CodeRecordSchemaMismatch,
			"required field is null", "The record does not match its descriptor; check the pipeline's processors between the source and this encoder.")
	}
	switch s.Type {
	case typeStruct:
		return writeStructValue(b, s, v, decimalFormat)
	case typeArray:
		return writeArrayValue(b, s, v, decimalFormat)
	case typeMap:
		return writeMapValue(b, v)
	case typeBytes:
		return writeBytesValue(b, s, v, decimalFormat)
	case typeInt16, typeInt32, typeInt64:
		return writeInt(b, v)
	case typeFloat:
		return writeFloatValue(b, v, 32)
	case typeDouble:
		return writeFloatValue(b, v, 64)
	case typeBoolean:
		return writeBoolValue(b, v)
	case typeString:
		return writeStringValue(b, v)
	default:
		return newCoded(CodeUnsupportedType, fmt.Sprintf("Connect type %q is not supported", s.Type), "")
	}
}

func writeStructValue(b *bytes.Buffer, s *schema, v any, decimalFormat string) error {
	if s.Name == nameVariableScaleDecimal {
		return writeVariableScaleDecimal(b, v)
	}
	m, err := asMap(v)
	if err != nil {
		return newCoded(CodeRecordSchemaMismatch, "struct field is not an object: "+err.Error(), "")
	}
	b.WriteByte('{')
	for i, f := range s.Fields {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONString(b, f.Name)
		b.WriteByte(':')
		if err := writeValue(b, f.Schema, m[f.Name], decimalFormat); err != nil {
			return err
		}
	}
	b.WriteByte('}')
	return nil
}

func writeArrayValue(b *bytes.Buffer, s *schema, v any, decimalFormat string) error {
	arr, ok := v.([]any)
	if !ok {
		return newCoded(CodeRecordSchemaMismatch, "array field is not a list", "")
	}
	b.WriteByte('[')
	for i, e := range arr {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := writeValue(b, s.Items, e, decimalFormat); err != nil {
			return err
		}
	}
	b.WriteByte(']')
	return nil
}

func writeMapValue(b *bytes.Buffer, v any) error {
	m, err := asMap(v)
	if err != nil {
		return newCoded(CodeRecordSchemaMismatch, "map field is not an object: "+err.Error(), "")
	}
	b.WriteByte('{')
	first := true
	for k, mv := range m {
		if !first {
			b.WriteByte(',')
		}
		first = false
		writeJSONString(b, k)
		b.WriteByte(':')
		// hstore map mode is MAP<STRING,STRING>.
		writeJSONString(b, fmt.Sprint(mv))
	}
	b.WriteByte('}')
	return nil
}

func writeBytesValue(b *bytes.Buffer, s *schema, v any, decimalFormat string) error {
	if s.Name == nameDecimal {
		return writeDecimal(b, v, s, decimalFormat)
	}
	bs, ok := v.([]byte)
	if !ok {
		return newCoded(CodeRecordSchemaMismatch, "bytes field is not bytes", "")
	}
	b.WriteString(strconv.Quote(base64.StdEncoding.EncodeToString(bs)))
	return nil
}

func writeFloatValue(b *bytes.Buffer, v any, bits int) error {
	f, err := asFloat(v, bits)
	if err != nil {
		return newCoded(CodeRecordSchemaMismatch, err.Error(), "")
	}
	appendFloat(b, f, bits)
	return nil
}

func writeBoolValue(b *bytes.Buffer, v any) error {
	bl, ok := v.(bool)
	if !ok {
		return newCoded(CodeRecordSchemaMismatch, "boolean field is not a bool", "")
	}
	b.WriteString(strconv.FormatBool(bl))
	return nil
}

func writeStringValue(b *bytes.Buffer, v any) error {
	str, ok := v.(string)
	if !ok {
		return newCoded(CodeRecordSchemaMismatch, "string field is not a string", "")
	}
	writeJSONString(b, str)
	return nil
}

// asMap accepts the map shapes record data uses.
func asMap(v any) (map[string]any, error) {
	switch m := v.(type) {
	case map[string]any:
		return m, nil
	case opencdc.StructuredData:
		return map[string]any(m), nil
	case map[string]string:
		out := make(map[string]any, len(m))
		for k, e := range m {
			out[k] = e
		}
		return out, nil
	default:
		return nil, fmt.Errorf("got %T", v)
	}
}

func writeInt(b *bytes.Buffer, v any) error {
	switch n := v.(type) {
	case int:
		b.WriteString(strconv.FormatInt(int64(n), 10))
	case int8:
		b.WriteString(strconv.FormatInt(int64(n), 10))
	case int16:
		b.WriteString(strconv.FormatInt(int64(n), 10))
	case int32:
		b.WriteString(strconv.FormatInt(int64(n), 10))
	case int64:
		b.WriteString(strconv.FormatInt(n, 10))
	case uint64:
		b.WriteString(strconv.FormatUint(n, 10))
	case json.Number:
		b.WriteString(n.String())
	case float64:
		b.WriteString(strconv.FormatInt(int64(n), 10))
	default:
		return newCoded(CodeRecordSchemaMismatch, fmt.Sprintf("integer field is a %T", v), "")
	}
	return nil
}

func asFloat(v any, bits int) (float64, error) {
	switch f := v.(type) {
	case float32:
		return float64(f), nil
	case float64:
		return f, nil
	case json.Number:
		x, err := f.Float64()
		if err != nil {
			return 0, err
		}
		return x, nil
	default:
		return 0, fmt.Errorf("float field is a %T", v)
	}
}

// asDecimalString returns the decimal literal of a Decimal value, which the
// source carries as a string and a parsed old key carries as json.Number.
func asDecimalString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	default:
		return "", false
	}
}

// writeDecimal writes a org.apache.kafka.connect.data.Decimal value. The
// record value is a decimal literal at the schema's scale parameter.
func writeDecimal(b *bytes.Buffer, v any, s *schema, decimalFormat string) error {
	str, ok := asDecimalString(v)
	if !ok {
		return newCoded(CodeRecordSchemaMismatch, "decimal field is not a string", "")
	}
	scale, err := strconv.Atoi(s.param("scale"))
	if err != nil || scale < 0 {
		return newCoded(CodeRecordSchemaMismatch, "decimal schema has no scale parameter", "")
	}
	if decimalFormat == decimalNumeric {
		// The literal is already at the column scale; write it raw.
		if !validJSONNumber(str) {
			return newCoded(CodeValueOutOfRange, "decimal is not a valid number", "")
		}
		b.WriteString(str)
		return nil
	}
	unscaled, err := decimalUnscaled(str, scale)
	if err != nil {
		return newCoded(CodeValueOutOfRange, err.Error(), "")
	}
	b.WriteString(strconv.Quote(base64.StdEncoding.EncodeToString(javaBigIntBytes(unscaled))))
	return nil
}

// writeVariableScaleDecimal writes an io.debezium.data.VariableScaleDecimal:
// the record value is a decimal literal, trailing zeros of the fractional
// part are stripped to find the scale, and the unscaled value is base64.
func writeVariableScaleDecimal(b *bytes.Buffer, v any) error {
	str, ok := asDecimalString(v)
	if !ok {
		return newCoded(CodeRecordSchemaMismatch, "variable-scale decimal field is not a string", "")
	}
	scale, unscaled, err := variableScaleDecimal(str)
	if err != nil {
		return newCoded(CodeValueOutOfRange, err.Error(), "")
	}
	b.WriteString(`{"scale":`)
	b.WriteString(strconv.Itoa(scale))
	b.WriteString(`,"value":`)
	b.WriteString(strconv.Quote(base64.StdEncoding.EncodeToString(javaBigIntBytes(unscaled))))
	b.WriteByte('}')
	return nil
}

// decimalUnscaled parses a decimal literal at a fixed scale and returns the
// unscaled integer (the literal times 10^scale).
func decimalUnscaled(s string, scale int) (*big.Int, error) {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("decimal %q is not a number", s)
	}
	p := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	r.Mul(r, new(big.Rat).SetInt(p))
	if !r.IsInt() {
		return nil, fmt.Errorf("decimal %q has more than %d fractional digits", s, scale)
	}
	return new(big.Int).Set(r.Num()), nil
}

// variableScaleDecimal strips trailing zeros from a decimal literal's
// fractional part (BigDecimal.stripTrailingZeros) and returns the remaining
// scale and the unscaled value.
func variableScaleDecimal(s string) (int, *big.Int, error) {
	if !validJSONNumber(s) {
		return 0, nil, fmt.Errorf("decimal %q is not a number", s)
	}
	neg := false
	body := s
	if strings.HasPrefix(body, "-") {
		neg = true
		body = body[1:]
	} else if strings.HasPrefix(body, "+") {
		body = body[1:]
	}
	ip, fp, hasDot := strings.Cut(body, ".")
	if !hasDot {
		n, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return 0, nil, fmt.Errorf("decimal %q is not a number", s)
		}
		return 0, n, nil
	}
	fp = strings.TrimRight(fp, "0")
	scale := len(fp)
	digits := strings.TrimLeft(ip+fp, "0")
	if digits == "" {
		digits = "0"
	}
	if neg {
		digits = "-" + digits
	}
	n, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return 0, nil, fmt.Errorf("decimal %q is not a number", s)
	}
	return scale, n, nil
}

func validJSONNumber(s string) bool {
	if s == "" {
		return false
	}
	_, ok := new(big.Rat).SetString(s)
	return ok
}

// javaBigIntBytes returns the two's-complement, minimal big-endian encoding
// of n, exactly as Java's BigInteger.toByteArray produces it (the bytes the
// Confluent Decimal converter base64-encodes).
func javaBigIntBytes(n *big.Int) []byte {
	if n.Sign() == 0 {
		return []byte{0}
	}
	if n.Sign() > 0 {
		b := n.Bytes()
		if b[0]&0x80 != 0 {
			return append([]byte{0}, b...)
		}
		return b
	}
	// Negative: two's complement in the minimal byte length.
	l := len(n.Bytes())
	mod := new(big.Int).Lsh(big.NewInt(1), uint(8*l))
	tc := new(big.Int).Add(mod, n)
	b := tc.Bytes()
	if len(b) == 0 || b[0]&0x80 == 0 {
		mod.Lsh(mod, 8)
		tc.Add(mod, n)
		return tc.Bytes()
	}
	return b
}

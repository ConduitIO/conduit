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
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// DecimalFormat is the JsonConverter decimal.format setting.
type DecimalFormat string

const (
	// DecimalBase64 writes a Decimal as the base64 of its unscaled value in
	// two's complement (the JsonConverter default).
	DecimalBase64 DecimalFormat = "BASE64"
	// DecimalNumeric writes a Decimal as a JSON number.
	DecimalNumeric DecimalFormat = "NUMERIC"
)

// JSONConverter is a port of org.apache.kafka.connect.json.JsonConverter's
// serializing half. The zero value is not the converter default; use
// [NewJSONConverter].
type JSONConverter struct {
	// SchemasEnable is schemas.enable: wrap the payload in
	// {"schema": ..., "payload": ...}.
	SchemasEnable bool
	// DecimalFormat is decimal.format.
	DecimalFormat DecimalFormat
	// ReplaceNullWithDefault is replace.null.with.default: a null value in a
	// field whose schema has a default is written as the default.
	ReplaceNullWithDefault bool
}

// NewJSONConverter returns a converter with the JsonConverter defaults:
// schemas enabled, BASE64 decimals, nulls replaced by defaults.
func NewJSONConverter() JSONConverter {
	return JSONConverter{SchemasEnable: true, DecimalFormat: DecimalBase64, ReplaceNullWithDefault: true}
}

// Marshal serializes a Connect value with its schema, as fromConnectData
// does. A nil schema with a nil value is a Kafka null (tombstone) and returns
// nil; the caller must pass that through as a null message value, not an
// empty one.
func (c JSONConverter) Marshal(s *Schema, v any) ([]byte, error) {
	if s == nil && v == nil {
		return nil, nil
	}
	if s == nil {
		return nil, conduiterr.New(CodeUnsupportedType, "schemaless values are not supported")
	}
	if c.DecimalFormat != DecimalBase64 && c.DecimalFormat != DecimalNumeric {
		return nil, conduiterr.New(CodeConfigInvalid, fmt.Sprintf("decimal.format %q is not BASE64 or NUMERIC", c.DecimalFormat))
	}
	var buf bytes.Buffer
	if c.SchemasEnable {
		buf.WriteString(`{"schema":`)
		if err := c.appendSchema(&buf, s); err != nil {
			return nil, err
		}
		buf.WriteString(`,"payload":`)
	}
	if err := c.appendValue(&buf, s, v, "$"); err != nil {
		return nil, err
	}
	if c.SchemasEnable {
		buf.WriteByte('}')
	}
	return buf.Bytes(), nil
}

// MarshalSchema writes only the schema in JsonConverter form (asJsonSchema).
func (c JSONConverter) MarshalSchema(s *Schema) ([]byte, error) {
	var buf bytes.Buffer
	if err := c.appendSchema(&buf, s); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// appendSchema writes asJsonSchema's output. Key order is fixed by the
// converter: type, then the child schemas, then optional, name, version, doc,
// parameters and default. A struct field's schema gets "field" last.
func (c JSONConverter) appendSchema(buf *bytes.Buffer, s *Schema) error {
	buf.WriteString(`{"type":`)
	appendJSONString(buf, string(s.Type))
	switch s.Type { //nolint:exhaustive // the remaining types share the default branch
	case TypeArray:
		buf.WriteString(`,"items":`)
		if err := c.appendSchema(buf, s.Items); err != nil {
			return err
		}
	case TypeMap:
		buf.WriteString(`,"keys":`)
		if err := c.appendSchema(buf, s.Keys); err != nil {
			return err
		}
		buf.WriteString(`,"values":`)
		if err := c.appendSchema(buf, s.Values); err != nil {
			return err
		}
	case TypeStruct:
		buf.WriteString(`,"fields":[`)
		for i, f := range s.Fields {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := c.appendSchema(buf, f.Schema); err != nil {
				return err
			}
			// Replace the closing brace to append the field name.
			buf.Truncate(buf.Len() - 1)
			buf.WriteString(`,"field":`)
			appendJSONString(buf, f.Name)
			buf.WriteByte('}')
		}
		buf.WriteByte(']')
	}
	buf.WriteString(`,"optional":`)
	buf.WriteString(strconv.FormatBool(s.Optional))
	if s.Name != "" {
		buf.WriteString(`,"name":`)
		appendJSONString(buf, s.Name)
	}
	if s.Version != nil {
		buf.WriteString(`,"version":`)
		buf.WriteString(strconv.Itoa(*s.Version))
	}
	if s.Doc != nil {
		buf.WriteString(`,"doc":`)
		appendJSONString(buf, *s.Doc)
	}
	if s.Parameters != nil {
		buf.WriteString(`,"parameters":{`)
		for i, p := range s.Parameters {
			if i > 0 {
				buf.WriteByte(',')
			}
			appendJSONString(buf, p.Key)
			buf.WriteByte(':')
			appendJSONString(buf, p.Value)
		}
		buf.WriteByte('}')
	}
	if s.HasDefault && s.Default != nil {
		buf.WriteString(`,"default":`)
		if err := c.appendValue(buf, s, s.Default, "$default"); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

// appendValue writes convertToJson's output for a Connect value produced by
// [FromGo].
//
//nolint:gocyclo // one branch per Connect type; splitting it hides the mapping
func (c JSONConverter) appendValue(buf *bytes.Buffer, s *Schema, v any, path string) error {
	if v == nil {
		if s.HasDefault && s.Default != nil && c.ReplaceNullWithDefault {
			return c.appendValue(buf, s, s.Default, path)
		}
		if s.Optional {
			buf.WriteString("null")
			return nil
		}
		return mismatch(path, "null value for required field")
	}
	if s.Name == LogicalDecimal {
		d, ok := v.(Decimal)
		if !ok {
			return mismatch(path, fmt.Sprintf("Decimal value must be connect.Decimal, got %T", v))
		}
		if c.DecimalFormat == DecimalNumeric {
			buf.WriteString(plainDecimal(d))
			return nil
		}
		appendJSONString(buf, base64.StdEncoding.EncodeToString(twosComplement(d.Unscaled)))
		return nil
	}
	switch s.Type {
	case TypeInt8, TypeInt16, TypeInt32, TypeInt64:
		i, ok := v.(int64)
		if !ok {
			return mismatch(path, fmt.Sprintf("integer value must be int64, got %T", v))
		}
		buf.WriteString(strconv.FormatInt(i, 10))
	case TypeFloat32:
		f, ok := v.(float32)
		if !ok {
			return mismatch(path, fmt.Sprintf("float value must be float32, got %T", v))
		}
		appendJavaFloat(buf, float64(f), 32)
	case TypeFloat64:
		f, ok := v.(float64)
		if !ok {
			return mismatch(path, fmt.Sprintf("double value must be float64, got %T", v))
		}
		appendJavaFloat(buf, f, 64)
	case TypeBoolean:
		b, ok := v.(bool)
		if !ok {
			return mismatch(path, fmt.Sprintf("boolean value must be bool, got %T", v))
		}
		buf.WriteString(strconv.FormatBool(b))
	case TypeString:
		str, ok := v.(string)
		if !ok {
			return mismatch(path, fmt.Sprintf("string value must be string, got %T", v))
		}
		if !utf8.ValidString(str) {
			return outOfRange(path, "string is not valid UTF-8")
		}
		appendJSONString(buf, str)
	case TypeBytes:
		b, ok := v.([]byte)
		if !ok {
			return mismatch(path, fmt.Sprintf("bytes value must be []byte, got %T", v))
		}
		appendJSONString(buf, base64.StdEncoding.EncodeToString(b))
	case TypeArray:
		arr, ok := v.([]any)
		if !ok {
			return mismatch(path, fmt.Sprintf("array value must be []any, got %T", v))
		}
		buf.WriteByte('[')
		for i, e := range arr {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := c.appendValue(buf, s.Items, e, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case TypeMap:
		entries, ok := v.([]MapEntry)
		if !ok {
			return mismatch(path, fmt.Sprintf("map value must be []connect.MapEntry, got %T", v))
		}
		// String-keyed maps are JSON objects; any other key type is an array
		// of [key, value] pairs.
		objectMode := s.Keys.Type == TypeString
		if objectMode {
			buf.WriteByte('{')
		} else {
			buf.WriteByte('[')
		}
		for i, e := range entries {
			if i > 0 {
				buf.WriteByte(',')
			}
			if objectMode {
				k, ok := e.Key.(string)
				if !ok {
					return mismatch(path, "map key must be a string")
				}
				appendJSONString(buf, k)
				buf.WriteByte(':')
			} else {
				buf.WriteByte('[')
				if err := c.appendValue(buf, s.Keys, e.Key, path+"{key}"); err != nil {
					return err
				}
				buf.WriteByte(',')
			}
			if err := c.appendValue(buf, s.Values, e.Value, path+"{value}"); err != nil {
				return err
			}
			if !objectMode {
				buf.WriteByte(']')
			}
		}
		if objectMode {
			buf.WriteByte('}')
		} else {
			buf.WriteByte(']')
		}
	case TypeStruct:
		st, ok := v.(*Struct)
		if !ok {
			return mismatch(path, fmt.Sprintf("struct value must be *connect.Struct, got %T", v))
		}
		if len(st.Values) != len(s.Fields) {
			return mismatch(path, "struct value has the wrong number of fields")
		}
		buf.WriteByte('{')
		for i, f := range s.Fields {
			if i > 0 {
				buf.WriteByte(',')
			}
			appendJSONString(buf, f.Name)
			buf.WriteByte(':')
			if err := c.appendValue(buf, f.Schema, st.Values[i], path+"."+f.Name); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return conduiterr.New(CodeUnsupportedType, fmt.Sprintf("%s: unsupported Connect type %q", path, s.Type))
	}
	return nil
}

// plainDecimal renders a Decimal as BigDecimal.toPlainString would.
func plainDecimal(d Decimal) string {
	neg := d.Unscaled.Sign() < 0
	digits := new(big.Int).Abs(d.Unscaled).String()
	var out string
	switch {
	case d.Scale <= 0:
		out = digits
		if digits != "0" {
			out += string(bytes.Repeat([]byte{'0'}, int(-d.Scale)))
		} else if d.Scale < 0 {
			out = "0"
		}
	case int(d.Scale) >= len(digits):
		out = "0." + string(bytes.Repeat([]byte{'0'}, int(d.Scale)-len(digits))) + digits
	default:
		out = digits[:len(digits)-int(d.Scale)] + "." + digits[len(digits)-int(d.Scale):]
	}
	if neg {
		out = "-" + out
	}
	return out
}

// appendJavaFloat writes a float as Jackson writes a FloatNode or DoubleNode:
// Java's Float.toString / Double.toString as specified from JDK 19 (the
// shortest decimal that round-trips, with the one-digit exception handled in
// JavaFloatString), with NaN and the infinities as quoted strings. Both pinned
// Debezium images run JDK 21 or later; a worker on JDK 17 or older prints some
// values with more digits (design D9).
func appendJavaFloat(buf *bytes.Buffer, f float64, bitSize int) {
	switch {
	case math.IsNaN(f):
		buf.WriteString(`"NaN"`)
		return
	case math.IsInf(f, 1):
		buf.WriteString(`"Infinity"`)
		return
	case math.IsInf(f, -1):
		buf.WriteString(`"-Infinity"`)
		return
	}
	buf.WriteString(JavaFloatString(f, bitSize))
}

// JavaFloatString is Java's Double.toString (bitSize 64) or Float.toString
// (bitSize 32) for a finite value.
func JavaFloatString(f float64, bitSize int) string {
	if f == 0 {
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	// Shortest round-trip digits in the form d.ddde±XX.
	e := strconv.FormatFloat(f, 'e', -1, bitSize)
	if !strings.Contains(e, ".") {
		// The JDK 19+ specification: when the shortest decimal has one digit,
		// the closest decimal of length one or two is chosen instead
		// (Double.MIN_VALUE prints 4.9E-324, not 5.0E-324). The correctly
		// rounded two-digit decimal is that one, provided it still rounds
		// back to f.
		e2 := strconv.FormatFloat(f, 'e', 1, bitSize)
		if back, err := strconv.ParseFloat(e2, bitSize); err == nil && back == f && !strings.Contains(e2, ".0e") {
			e = e2
		}
	}
	neg := e[0] == '-'
	if neg {
		e = e[1:]
	}
	mant, expStr, _ := cutByte(e, 'e')
	exp, _ := strconv.Atoi(expStr)
	digits := mant[:1]
	if len(mant) > 2 {
		digits += mant[2:]
	}
	var out string
	abs := math.Abs(f)
	if abs >= 1e-3 && abs < 1e7 {
		if exp >= 0 {
			intLen := exp + 1
			if len(digits) <= intLen {
				out = digits + string(bytes.Repeat([]byte{'0'}, intLen-len(digits))) + ".0"
			} else {
				out = digits[:intLen] + "." + digits[intLen:]
			}
		} else {
			out = "0." + string(bytes.Repeat([]byte{'0'}, -exp-1)) + digits
		}
	} else {
		frac := digits[1:]
		if frac == "" {
			frac = "0"
		}
		out = digits[:1] + "." + frac + "E" + strconv.Itoa(exp)
	}
	if neg {
		out = "-" + out
	}
	return out
}

func cutByte(s string, b byte) (string, string, bool) {
	if i := bytes.IndexByte([]byte(s), b); i >= 0 {
		return s[:i], s[i+1:], true
	}
	return s, "", false
}

const hexUpper = "0123456789ABCDEF"

// appendJSONString writes a JSON string as the JsonConverter's Jackson
// generator does (golden-verified): '"' and '\\' escaped, the five short
// escapes \b \t \n \f \r, every other control character below 0x20 as
// \u00XX, characters outside the Basic Multilingual Plane as a UTF-16
// surrogate pair \uXXXX\uXXXX, all hex uppercase, and everything else
// (including '/', DEL and U+2028) written raw as UTF-8.
func appendJSONString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c >= 0x80 {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r > 0xFFFF {
				buf.WriteString(s[start:i])
				hi, lo := utf16.EncodeRune(r)
				appendU(buf, hi)
				appendU(buf, lo)
				start = i + size
			}
			i += size
			continue
		}
		if c >= 0x20 && c != '"' && c != '\\' {
			i++
			continue
		}
		buf.WriteString(s[start:i])
		switch c {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\t':
			buf.WriteString(`\t`)
		case '\n':
			buf.WriteString(`\n`)
		case '\f':
			buf.WriteString(`\f`)
		case '\r':
			buf.WriteString(`\r`)
		default:
			appendU(buf, rune(c))
		}
		i++
		start = i
	}
	buf.WriteString(s[start:])
	buf.WriteByte('"')
}

func appendU(buf *bytes.Buffer, r rune) {
	buf.WriteString(`\u`)
	buf.WriteByte(hexUpper[(r>>12)&0xf])
	buf.WriteByte(hexUpper[(r>>8)&0xf])
	buf.WriteByte(hexUpper[(r>>4)&0xf])
	buf.WriteByte(hexUpper[r&0xf])
}

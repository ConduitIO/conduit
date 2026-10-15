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
	// field whose schema has a default is written as the default (also for
	// array elements and a top-level null; oracle-verified). See
	// DefaultReplaceNullWithDefault for why it is a setting.
	ReplaceNullWithDefault bool
}

// DefaultReplaceNullWithDefault is the encoder's default for
// replace.null.with.default: true, as in Kafka Connect, so consumers receive
// the bytes they receive from Debezium today. Decided by DeVaris on
// 2026-10-15 (design D20 / Q16).
//
// The consequence, documented as difference D20: an explicit NULL in a field
// whose schema has a default is written as the default. For a column with a
// literal default the consumer sees the default value; for a column whose
// default is not a literal (DEFAULT now()), Debezium's schema default is the
// epoch, so a NULL is written as 1970-01-01T00:00:00.000000Z. Pipelines that
// must keep NULLs set the converter's replace.null.with.default to false.
const DefaultReplaceNullWithDefault = true

// NewJSONConverter returns a converter with the JsonConverter defaults:
// schemas enabled, BASE64 decimals, and replace.null.with.default as
// DefaultReplaceNullWithDefault says.
func NewJSONConverter() JSONConverter {
	return JSONConverter{SchemasEnable: true, DecimalFormat: DecimalBase64, ReplaceNullWithDefault: DefaultReplaceNullWithDefault}
}

// MaxNestingDepth is Jackson's default StreamWriteConstraints.maxNestingDepth,
// which the JsonConverter enforces when it writes: the 1001st nested object
// or array fails the record (DataException). The oracle (tests/debezium/oracle)
// pins the boundary: 1000 nested arrays serialize, 1001 do not, and the
// schemas.enable envelope counts toward the limit.
const MaxNestingDepth = 1000

// jsonWriter is the output of one Marshal call: the buffer and the current
// container nesting depth.
type jsonWriter struct {
	buf   bytes.Buffer
	depth int
}

// open writes a '{' or '[' and enforces MaxNestingDepth.
func (w *jsonWriter) open(c byte, path string) error {
	w.depth++
	if w.depth > MaxNestingDepth {
		return outOfRange(path, fmt.Sprintf("JSON nesting deeper than %d (Jackson's limit)", MaxNestingDepth))
	}
	w.buf.WriteByte(c)
	return nil
}

func (w *jsonWriter) close(c byte) {
	w.depth--
	w.buf.WriteByte(c)
}

// str writes a JSON string. Invalid UTF-8 is refused: a Java String cannot
// hold it, so Kafka Connect could never have produced the bytes, and writing
// U+FFFD instead would change the data (invariant 6).
func (w *jsonWriter) str(s, path string) error {
	if !utf8.ValidString(s) {
		return outOfRange(path, fmt.Sprintf("%q is not valid UTF-8", s))
	}
	appendJSONString(&w.buf, s)
	return nil
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
	w := &jsonWriter{}
	if c.SchemasEnable {
		if err := w.open('{', "$"); err != nil {
			return nil, err
		}
		w.buf.WriteString(`"schema":`)
		if err := c.appendSchema(w, s, "$schema"); err != nil {
			return nil, err
		}
		w.buf.WriteString(`,"payload":`)
	}
	if err := c.appendValue(w, s, v, "$"); err != nil {
		return nil, err
	}
	if c.SchemasEnable {
		w.close('}')
	}
	return w.buf.Bytes(), nil
}

// MarshalSchema writes only the schema in JsonConverter form (asJsonSchema).
func (c JSONConverter) MarshalSchema(s *Schema) ([]byte, error) {
	w := &jsonWriter{}
	if err := c.appendSchema(w, s, "$"); err != nil {
		return nil, err
	}
	return w.buf.Bytes(), nil
}

// appendSchema writes asJsonSchema's output. Key order is fixed by the
// converter: type, then the child schemas, then optional, name, version, doc,
// parameters and default. A struct field's schema gets "field" last. An empty
// parameter map is omitted, as the converter omits it (oracle EMPTYPARAMS).
func (c JSONConverter) appendSchema(w *jsonWriter, s *Schema, path string) error {
	return c.appendSchemaField(w, s, path, nil)
}

//nolint:gocyclo // one branch per schema attribute, in output order
func (c JSONConverter) appendSchemaField(w *jsonWriter, s *Schema, path string, fieldName *string) error {
	if err := w.open('{', path); err != nil {
		return err
	}
	w.buf.WriteString(`"type":`)
	if err := w.str(string(s.Type), path); err != nil {
		return err
	}
	switch s.Type { //nolint:exhaustive // the remaining types share the default branch
	case TypeArray:
		w.buf.WriteString(`,"items":`)
		if err := c.appendSchema(w, s.Items, path+".items"); err != nil {
			return err
		}
	case TypeMap:
		w.buf.WriteString(`,"keys":`)
		if err := c.appendSchema(w, s.Keys, path+".keys"); err != nil {
			return err
		}
		w.buf.WriteString(`,"values":`)
		if err := c.appendSchema(w, s.Values, path+".values"); err != nil {
			return err
		}
	case TypeStruct:
		w.buf.WriteString(`,"fields":`)
		if err := w.open('[', path); err != nil {
			return err
		}
		for i, f := range s.Fields {
			if i > 0 {
				w.buf.WriteByte(',')
			}
			name := f.Name
			if err := c.appendSchemaField(w, f.Schema, path+"."+f.Name, &name); err != nil {
				return err
			}
		}
		w.close(']')
	}
	w.buf.WriteString(`,"optional":`)
	w.buf.WriteString(strconv.FormatBool(s.Optional))
	if s.Name != "" {
		w.buf.WriteString(`,"name":`)
		if err := w.str(s.Name, path); err != nil {
			return err
		}
	}
	if s.Version != nil {
		w.buf.WriteString(`,"version":`)
		w.buf.WriteString(strconv.Itoa(*s.Version))
	}
	if s.Doc != nil {
		w.buf.WriteString(`,"doc":`)
		if err := w.str(*s.Doc, path); err != nil {
			return err
		}
	}
	if len(s.Parameters) > 0 {
		w.buf.WriteString(`,"parameters":`)
		if err := w.open('{', path); err != nil {
			return err
		}
		for i, p := range s.Parameters {
			if i > 0 {
				w.buf.WriteByte(',')
			}
			if err := w.str(p.Key, path); err != nil {
				return err
			}
			w.buf.WriteByte(':')
			if err := w.str(p.Value, path); err != nil {
				return err
			}
		}
		w.close('}')
	}
	if s.HasDefault && s.Default != nil {
		w.buf.WriteString(`,"default":`)
		if err := c.appendValue(w, s, s.Default, path+"$default"); err != nil {
			return err
		}
	}
	if fieldName != nil {
		w.buf.WriteString(`,"field":`)
		if err := w.str(*fieldName, path); err != nil {
			return err
		}
	}
	w.close('}')
	return nil
}

// appendValue writes convertToJson's output for a Connect value produced by
// [FromGo].
//
//nolint:gocyclo // one branch per Connect type; splitting it hides the mapping
func (c JSONConverter) appendValue(w *jsonWriter, s *Schema, v any, path string) error {
	if v == nil {
		// Invariant 6: the one sanctioned replacement of a value, design D20
		// (decided 2026-10-15: mirror Kafka Connect). With
		// replace.null.with.default on, an explicit NULL in a field whose
		// schema has a default is written as the default, as Connect writes
		// it; a NULL in a DEFAULT now() column becomes the epoch
		// (1970-01-01T00:00:00.000000Z). It is an explicit setting
		// (ReplaceNullWithDefault, default DefaultReplaceNullWithDefault), and
		// false writes the NULL.
		if s.HasDefault && s.Default != nil && c.ReplaceNullWithDefault {
			return c.appendValue(w, s, s.Default, path)
		}
		if s.Optional {
			w.buf.WriteString("null")
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
			// Jackson writes a DecimalNode with BigDecimal.toString, which
			// switches to E notation (1E-7, 0E-10, 5E+3); oracle-verified.
			w.buf.WriteString(javaBigDecimalString(d))
			return nil
		}
		return w.str(base64.StdEncoding.EncodeToString(twosComplement(d.Unscaled)), path)
	}
	switch s.Type {
	case TypeInt8, TypeInt16, TypeInt32, TypeInt64:
		i, ok := v.(int64)
		if !ok {
			return mismatch(path, fmt.Sprintf("integer value must be int64, got %T", v))
		}
		w.buf.WriteString(strconv.FormatInt(i, 10))
	case TypeFloat32:
		f, ok := v.(float32)
		if !ok {
			return mismatch(path, fmt.Sprintf("float value must be float32, got %T", v))
		}
		appendJavaFloat(&w.buf, float64(f), 32)
	case TypeFloat64:
		f, ok := v.(float64)
		if !ok {
			return mismatch(path, fmt.Sprintf("double value must be float64, got %T", v))
		}
		appendJavaFloat(&w.buf, f, 64)
	case TypeBoolean:
		b, ok := v.(bool)
		if !ok {
			return mismatch(path, fmt.Sprintf("boolean value must be bool, got %T", v))
		}
		w.buf.WriteString(strconv.FormatBool(b))
	case TypeString:
		str, ok := v.(string)
		if !ok {
			return mismatch(path, fmt.Sprintf("string value must be string, got %T", v))
		}
		return w.str(str, path)
	case TypeBytes:
		b, ok := v.([]byte)
		if !ok {
			return mismatch(path, fmt.Sprintf("bytes value must be []byte, got %T", v))
		}
		return w.str(base64.StdEncoding.EncodeToString(b), path)
	case TypeArray:
		arr, ok := v.([]any)
		if !ok {
			return mismatch(path, fmt.Sprintf("array value must be []any, got %T", v))
		}
		if err := w.open('[', path); err != nil {
			return err
		}
		for i, e := range arr {
			if i > 0 {
				w.buf.WriteByte(',')
			}
			if err := c.appendValue(w, s.Items, e, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		w.close(']')
	case TypeMap:
		entries, ok := v.([]MapEntry)
		if !ok {
			return mismatch(path, fmt.Sprintf("map value must be []connect.MapEntry, got %T", v))
		}
		// String-keyed maps are JSON objects; any other key type is an array
		// of [key, value] pairs.
		objectMode := s.Keys.Type == TypeString
		open, end := byte('['), byte(']')
		if objectMode {
			open, end = '{', '}'
		}
		if err := w.open(open, path); err != nil {
			return err
		}
		for i, e := range entries {
			if i > 0 {
				w.buf.WriteByte(',')
			}
			if objectMode {
				k, ok := e.Key.(string)
				if !ok {
					return mismatch(path, "map key must be a string")
				}
				if err := w.str(k, path); err != nil {
					return err
				}
				w.buf.WriteByte(':')
			} else {
				if err := w.open('[', path); err != nil {
					return err
				}
				if err := c.appendValue(w, s.Keys, e.Key, path+"{key}"); err != nil {
					return err
				}
				w.buf.WriteByte(',')
			}
			if err := c.appendValue(w, s.Values, e.Value, path+"{value}"); err != nil {
				return err
			}
			if !objectMode {
				w.close(']')
			}
		}
		w.close(end)
	case TypeStruct:
		st, ok := v.(*Struct)
		if !ok {
			return mismatch(path, fmt.Sprintf("struct value must be *connect.Struct, got %T", v))
		}
		if len(st.Values) != len(s.Fields) {
			return mismatch(path, "struct value has the wrong number of fields")
		}
		if err := w.open('{', path); err != nil {
			return err
		}
		for i, f := range s.Fields {
			if i > 0 {
				w.buf.WriteByte(',')
			}
			if err := w.str(f.Name, path); err != nil {
				return err
			}
			w.buf.WriteByte(':')
			if err := c.appendValue(w, f.Schema, st.Values[i], path+"."+f.Name); err != nil {
				return err
			}
		}
		w.close('}')
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

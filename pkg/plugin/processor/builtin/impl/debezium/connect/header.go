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
	"encoding/base64"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// SimpleHeaderConverter is org.apache.kafka.connect.storage.SimpleHeaderConverter,
// the Kafka Connect default header.converter: a header value is written as
// Values.convertToString(schema, value) in UTF-8.
//
// Verified against the golden captures (string headers, and key structs of
// integers, strings, uuids and Decimals for __debezium.newkey/oldkey) and
// against the converter oracle in tests/debezium/oracle (floats, Decimals,
// bytes, arrays, maps, embedded escaping, struct-field defaults).
type SimpleHeaderConverter struct{}

// Marshal serializes one header value. A nil value is a null header value;
// unlike the JsonConverter, a top-level null is not replaced by the schema
// default (oracle TOPNULL).
func (SimpleHeaderConverter) Marshal(s *Schema, v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	var sb strings.Builder
	if err := appendValuesString(&sb, s, v, false, "$header"); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}

var valuesEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// appendValuesString is Values.append. Embedded values (inside a struct,
// array or map) quote strings and escape only backslash and double quote;
// a top-level string is written as is. A struct field that is null takes the
// field's default, because Values.append reads fields through Struct.get,
// which substitutes it; array elements and map values do not (oracle
// DEFAULTS). A value of the wrong Go type is an error, never a panic.
//
//nolint:gocyclo // one branch per Connect type; splitting it hides the mapping
func appendValuesString(sb *strings.Builder, s *Schema, v any, embedded bool, path string) error {
	if v == nil {
		sb.WriteString("null")
		return nil
	}
	wrong := func() error {
		return mismatch(path, fmt.Sprintf("%s header value has Go type %T", s.Type, v))
	}
	quoted := func(str string) error {
		if !utf8.ValidString(str) {
			return outOfRange(path, fmt.Sprintf("%q is not valid UTF-8", str))
		}
		if embedded {
			sb.WriteByte('"')
			sb.WriteString(valuesEscaper.Replace(str))
			sb.WriteByte('"')
		} else {
			sb.WriteString(str)
		}
		return nil
	}
	if s.Name == LogicalDecimal {
		d, ok := v.(Decimal)
		if !ok {
			return wrong()
		}
		sb.WriteString(javaBigDecimalString(d))
		return nil
	}
	if ms, ok, err := connectTemporalMillis(s, v); ok || err != nil {
		if err != nil {
			return mismatch(path, err.Error())
		}
		sb.WriteString(javaDateString(ms))
		return nil
	}
	switch s.Type {
	case TypeInt8, TypeInt16, TypeInt32, TypeInt64:
		i, ok := v.(int64)
		if !ok {
			return wrong()
		}
		sb.WriteString(strconv.FormatInt(i, 10))
	case TypeFloat32:
		f, ok := v.(float32)
		if !ok {
			return wrong()
		}
		sb.WriteString(javaFloatOrSpecial(float64(f), 32))
	case TypeFloat64:
		f, ok := v.(float64)
		if !ok {
			return wrong()
		}
		sb.WriteString(javaFloatOrSpecial(f, 64))
	case TypeBoolean:
		b, ok := v.(bool)
		if !ok {
			return wrong()
		}
		sb.WriteString(strconv.FormatBool(b))
	case TypeString:
		str, ok := v.(string)
		if !ok {
			return wrong()
		}
		return quoted(str)
	case TypeBytes:
		b, ok := v.([]byte)
		if !ok {
			return wrong()
		}
		return quoted(base64.StdEncoding.EncodeToString(b))
	case TypeArray:
		arr, ok := v.([]any)
		if !ok {
			return wrong()
		}
		sb.WriteByte('[')
		for i, e := range arr {
			if i > 0 {
				sb.WriteByte(',')
			}
			if err := appendValuesString(sb, s.Items, e, true, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		sb.WriteByte(']')
	case TypeMap:
		entries, ok := v.([]MapEntry)
		if !ok {
			return wrong()
		}
		sb.WriteByte('{')
		for i, e := range entries {
			if i > 0 {
				sb.WriteByte(',')
			}
			if err := appendValuesString(sb, s.Keys, e.Key, true, path+"{key}"); err != nil {
				return err
			}
			sb.WriteByte(':')
			if err := appendValuesString(sb, s.Values, e.Value, true, path+"{value}"); err != nil {
				return err
			}
		}
		sb.WriteByte('}')
	case TypeStruct:
		st, ok := v.(*Struct)
		if !ok || len(st.Values) != len(s.Fields) {
			return wrong()
		}
		sb.WriteByte('{')
		for i, f := range s.Fields {
			if i > 0 {
				sb.WriteByte(',')
			}
			if !utf8.ValidString(f.Name) {
				return outOfRange(path, fmt.Sprintf("field name %q is not valid UTF-8", f.Name))
			}
			sb.WriteByte('"')
			sb.WriteString(valuesEscaper.Replace(f.Name))
			sb.WriteString(`":`)
			fv := st.Values[i]
			if fv == nil && f.Schema.HasDefault {
				fv = f.Schema.Default // Struct.get substitutes the default
			}
			if err := appendValuesString(sb, f.Schema, fv, true, path+"."+f.Name); err != nil {
				return err
			}
		}
		sb.WriteByte('}')
	default:
		return conduiterr.New(CodeUnsupportedType, fmt.Sprintf("%s: unsupported Connect type %q", path, s.Type))
	}
	return nil
}

// javaFloatOrSpecial is String.valueOf(double/float): Java's toString, with
// NaN and the infinities spelled as Java spells them.
func javaFloatOrSpecial(f float64, bits int) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return JavaFloatString(f, bits)
}

// javaBigDecimalString is BigDecimal.toString: plain notation when the scale
// is non-negative and the adjusted exponent is at least -6, otherwise
// scientific notation with an explicit exponent sign.
func javaBigDecimalString(d Decimal) string {
	neg := d.Unscaled.Sign() < 0
	digits := new(big.Int).Abs(d.Unscaled).String()
	adjusted := -int64(d.Scale) + int64(len(digits)-1)
	var out string
	if d.Scale >= 0 && adjusted >= -6 {
		out = plainDecimal(Decimal{Unscaled: new(big.Int).Abs(d.Unscaled), Scale: d.Scale})
	} else {
		out = digits[:1]
		if len(digits) > 1 {
			out += "." + digits[1:]
		}
		out += "E"
		if adjusted >= 0 {
			out += "+"
		}
		out += strconv.FormatInt(adjusted, 10)
	}
	if neg {
		out = "-" + out
	}
	return out
}

// connectTemporalMillis returns the java.util.Date millisecond value of a
// Connect Timestamp (int64 ms), Date (int32 days) or Time (int32 ms). ok is
// false for any other schema. Debezium uses these types for keys under
// time.precision.mode=connect.
func connectTemporalMillis(s *Schema, v any) (int64, bool, error) {
	switch s.Name {
	case LogicalTimestamp, LogicalDate, LogicalTime:
	default:
		return 0, false, nil
	}
	n, ok := v.(int64)
	if !ok {
		return 0, true, cerrors.Errorf("%s value must be int64, got %T", s.Name, v)
	}
	if s.Name == LogicalDate {
		return n * millisPerDay, true, nil
	}
	return n, true, nil
}

const millisPerDay = 86400000

// javaDateString is how Values.append writes a java.util.Date (oracle rows
// HT): the format is chosen by the value, not by the logical type. Below one
// day after the epoch (including every instant before 1970) it is the time of
// day, HH:mm:ss.SSS'Z'; at an exact UTC midnight it is yyyy-MM-dd; otherwise
// yyyy-MM-dd'T'HH:mm:ss.SSS'Z'. All in UTC, unquoted, even when embedded. So a
// Date of 1970-01-01 is written 00:00:00.000Z, as Kafka writes it.
func javaDateString(ms int64) string {
	t := time.UnixMilli(ms).UTC()
	timeOfDay := fmt.Sprintf("%02d:%02d:%02d.%03dZ", t.Hour(), t.Minute(), t.Second(), t.Nanosecond()/1e6)
	switch {
	case ms < millisPerDay:
		return timeOfDay
	case ms%millisPerDay == 0:
		return fmt.Sprintf("%04d-%02d-%02d", t.Year(), int(t.Month()), t.Day())
	}
	return fmt.Sprintf("%04d-%02d-%02dT%s", t.Year(), int(t.Month()), t.Day(), timeOfDay)
}

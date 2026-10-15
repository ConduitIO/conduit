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

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// SimpleHeaderConverter is org.apache.kafka.connect.storage.SimpleHeaderConverter,
// the Kafka Connect default header.converter: a header value is written as
// Values.convertToString(schema, value) in UTF-8.
//
// Golden-verified: strings (written raw), and key structs of integers,
// strings, uuids and Decimals (the __debezium.newkey/oldkey headers, written
// as {"seq":2,"region":"us"} and {"id":1.50}, a Decimal as
// BigDecimal.toString). Float, bytes, array and map values port Values.append
// from Kafka 3.7/4.x and are covered by unit tests only.
type SimpleHeaderConverter struct{}

// Marshal serializes one header value. A nil value is a null header value.
func (SimpleHeaderConverter) Marshal(s *Schema, v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	var sb strings.Builder
	if err := appendValuesString(&sb, s, v, false); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}

// appendValuesString is Values.append. Embedded values (inside a struct,
// array or map) quote strings and escape only backslash and double quote;
// a top-level string is written as is.
//
//nolint:gocyclo // one branch per Connect type; splitting it hides the mapping
func appendValuesString(sb *strings.Builder, s *Schema, v any, embedded bool) error {
	if v == nil {
		sb.WriteString("null")
		return nil
	}
	if s.Name == LogicalDecimal {
		d, ok := v.(Decimal)
		if !ok {
			return mismatch("header", fmt.Sprintf("Decimal value must be connect.Decimal, got %T", v))
		}
		sb.WriteString(javaBigDecimalString(d))
		return nil
	}
	switch s.Type {
	case TypeInt8, TypeInt16, TypeInt32, TypeInt64:
		sb.WriteString(strconv.FormatInt(v.(int64), 10))
	case TypeFloat32:
		f := float64(v.(float32))
		sb.WriteString(javaFloatOrSpecial(f, 32))
	case TypeFloat64:
		sb.WriteString(javaFloatOrSpecial(v.(float64), 64))
	case TypeBoolean:
		sb.WriteString(strconv.FormatBool(v.(bool)))
	case TypeString:
		str := v.(string)
		if embedded {
			sb.WriteByte('"')
			sb.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(str))
			sb.WriteByte('"')
		} else {
			sb.WriteString(str)
		}
	case TypeBytes:
		enc := base64.StdEncoding.EncodeToString(v.([]byte))
		if embedded {
			sb.WriteByte('"')
			sb.WriteString(enc)
			sb.WriteByte('"')
		} else {
			sb.WriteString(enc)
		}
	case TypeArray:
		sb.WriteByte('[')
		for i, e := range v.([]any) {
			if i > 0 {
				sb.WriteByte(',')
			}
			if err := appendValuesString(sb, s.Items, e, true); err != nil {
				return err
			}
		}
		sb.WriteByte(']')
	case TypeMap:
		sb.WriteByte('{')
		for i, e := range v.([]MapEntry) {
			if i > 0 {
				sb.WriteByte(',')
			}
			if err := appendValuesString(sb, s.Keys, e.Key, true); err != nil {
				return err
			}
			sb.WriteByte(':')
			if err := appendValuesString(sb, s.Values, e.Value, true); err != nil {
				return err
			}
		}
		sb.WriteByte('}')
	case TypeStruct:
		st, ok := v.(*Struct)
		if !ok {
			return mismatch("header", fmt.Sprintf("struct value must be *connect.Struct, got %T", v))
		}
		sb.WriteByte('{')
		for i, f := range s.Fields {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteByte('"')
			sb.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(f.Name))
			sb.WriteString(`":`)
			if err := appendValuesString(sb, f.Schema, st.Values[i], true); err != nil {
				return err
			}
		}
		sb.WriteByte('}')
	default:
		return conduiterr.New(CodeUnsupportedType, fmt.Sprintf("header: unsupported Connect type %q", s.Type))
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

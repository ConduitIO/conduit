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
	"io"
	"math"
	"strconv"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/goccy/go-json"
)

// ParsePayloadJSON reads a JsonConverter payload (schemas.enable=false form)
// for schema s and returns the Go values [FromGo] accepts: the inverse of
// FromGo followed by Marshal. Numbers are read exactly (no float64 detour for
// integers), Decimals come back as decimal strings, bytes as []byte.
//
// The source uses this form for values it must carry in metadata, which is a
// string map: the old key of a primary-key update (debezium.old_key).
func ParsePayloadJSON(s *Schema, raw []byte, format DecimalFormat) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, conduiterr.Wrap(CodeRecordSchemaMismatch, "payload is not JSON: "+err.Error(), err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, conduiterr.New(CodeRecordSchemaMismatch, "payload has trailing data")
	}
	out, err := payloadValue(s, v, format, "$")
	if err != nil {
		if _, ok := conduiterr.Get(err); ok {
			return nil, err
		}
		return nil, conduiterr.Wrap(CodeRecordSchemaMismatch, err.Error(), err)
	}
	return out, nil
}

//nolint:gocyclo // one branch per Connect type; splitting it hides the mapping
func payloadValue(s *Schema, v any, format DecimalFormat, path string) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch s.Name {
	case LogicalDecimal:
		if format == DecimalNumeric {
			n, ok := v.(json.Number)
			if !ok {
				return nil, cerrors.Errorf("%s: NUMERIC decimal must be a number, got %T", path, v)
			}
			return n.String(), nil
		}
		str, ok := v.(string)
		if !ok {
			return nil, cerrors.Errorf("%s: BASE64 decimal must be a string", path)
		}
		b, err := base64.StdEncoding.DecodeString(str)
		if err != nil {
			return nil, cerrors.Errorf("%s: %w", path, err)
		}
		scale, err := s.decimalScale()
		if err != nil {
			return nil, err
		}
		return Decimal{Unscaled: fromTwosComplement(b), Scale: scale}.String(), nil
	case LogicalVariableScaleDecimal:
		m, ok := v.(map[string]any)
		if !ok {
			return nil, cerrors.Errorf("%s: VariableScaleDecimal must be an object", path)
		}
		n, ok := m["scale"].(json.Number)
		if !ok {
			return nil, cerrors.Errorf("%s: VariableScaleDecimal scale missing", path)
		}
		scale, err := n.Int64()
		if err != nil || scale < math.MinInt32 || scale > math.MaxInt32 {
			return nil, cerrors.Errorf("%s: bad scale", path)
		}
		str, ok := m["value"].(string)
		if !ok {
			return nil, cerrors.Errorf("%s: VariableScaleDecimal value missing", path)
		}
		b, err := base64.StdEncoding.DecodeString(str)
		if err != nil {
			return nil, cerrors.Errorf("%s: %w", path, err)
		}
		u := fromTwosComplement(b)
		if scale >= 0 {
			return Decimal{Unscaled: u, Scale: int32(scale)}.String(), nil
		}
		return u.String() + "E" + strconv.FormatInt(-scale, 10), nil
	}
	switch s.Type { //nolint:exhaustive // the remaining types share the default branch
	case TypeInt8, TypeInt16, TypeInt32, TypeInt64:
		n, ok := v.(json.Number)
		if !ok {
			return nil, cerrors.Errorf("%s: integer must be a number", path)
		}
		return n.Int64()
	case TypeFloat32, TypeFloat64:
		bits := 64
		if s.Type == TypeFloat32 {
			bits = 32
		}
		var f float64
		switch n := v.(type) {
		case string:
			switch n {
			case "NaN":
				f = math.NaN()
			case "Infinity":
				f = math.Inf(1)
			case "-Infinity":
				f = math.Inf(-1)
			default:
				return nil, cerrors.Errorf("%s: bad float %q", path, n)
			}
		case json.Number:
			var err error
			if f, err = strconv.ParseFloat(n.String(), bits); err != nil {
				return nil, cerrors.Errorf("%s: %w", path, err)
			}
		default:
			return nil, cerrors.Errorf("%s: float must be a number", path)
		}
		if bits == 32 {
			return float32(f), nil
		}
		return f, nil
	case TypeBoolean:
		if b, ok := v.(bool); ok {
			return b, nil
		}
		return nil, cerrors.Errorf("%s: boolean expected", path)
	case TypeString:
		if str, ok := v.(string); ok {
			return str, nil
		}
		return nil, cerrors.Errorf("%s: string expected", path)
	case TypeBytes:
		str, ok := v.(string)
		if !ok {
			return nil, cerrors.Errorf("%s: base64 string expected", path)
		}
		return base64.StdEncoding.DecodeString(str)
	case TypeArray:
		arr, ok := v.([]any)
		if !ok {
			return nil, cerrors.Errorf("%s: array expected", path)
		}
		out := make([]any, len(arr))
		for i, e := range arr {
			ev, err := payloadValue(s.Items, e, format, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out[i] = ev
		}
		return out, nil
	case TypeStruct:
		m, ok := v.(map[string]any)
		if !ok {
			return nil, cerrors.Errorf("%s: object expected", path)
		}
		out := make(map[string]any, len(m))
		for k, fv := range m {
			f, idx := s.Field(k)
			if idx < 0 {
				return nil, cerrors.Errorf("%s.%s: not in the schema", path, k)
			}
			gv, err := payloadValue(f.Schema, fv, format, path+"."+k)
			if err != nil {
				return nil, err
			}
			out[k] = gv
		}
		return out, nil
	}
	// A map payload is a JSON object whose member order a generic decoder
	// does not keep; no Debezium key or metadata value needs one.
	return nil, conduiterr.New(CodeUnsupportedType, fmt.Sprintf("%s: %s payloads are not supported", path, s.Type))
}

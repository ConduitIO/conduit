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

package internal

import (
	"bytes"
	"math"
	"math/big"
	"reflect"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
)

// sameValue reports whether got, a value decoded after a round trip through
// an Avro schema, carries the same data as want, the value that was encoded.
//
// The comparison is by value, not by Go type: the decoder picks its own Go
// types (int for an Avro int, for example), and that is not a change of the
// data. Everything that is data must match exactly: map keys, numbers, string
// and byte contents, time instants, durations and decimals.
//
// The rules follow the processor SDK's encode middleware
// (conduit-processor-sdk, middleware_schema_fit.go), which decides the same
// question for the schema a processor's output is re-encoded with.
func sameValue(want, got any) bool {
	if isNil(want) || isNil(got) {
		return isNil(want) && isNil(got)
	}
	if same, ok := sameKnownType(want, got); ok {
		return same
	}

	wv, gv := reflect.ValueOf(want), reflect.ValueOf(got)
	if wb, ok := byteContents(wv); ok {
		gb, ok := byteContents(gv)
		return ok && bytes.Equal(wb, gb)
	}
	if isNumber(wv.Kind()) {
		return isNumber(gv.Kind()) && sameNumber(wv, gv)
	}
	if wv.Kind() == reflect.Map && wv.Type().Key().Kind() == reflect.String {
		return sameReflectMap(wv, gv)
	}
	if wv.Kind() == reflect.Slice || wv.Kind() == reflect.Array {
		return sameList(wv, gv)
	}
	return reflect.DeepEqual(want, got)
}

// sameKnownType compares want and got when want is one of the concrete types
// the codec decodes to. handled is false for any other type of want.
func sameKnownType(want, got any) (same, handled bool) {
	switch w := want.(type) {
	case opencdc.StructuredData:
		return sameMap(w, got), true
	case map[string]any:
		return sameMap(w, got), true
	case time.Time:
		g, ok := got.(time.Time)
		return ok && w.Equal(g), true
	case time.Duration:
		g, ok := got.(time.Duration)
		return ok && w == g, true
	case *big.Rat:
		g, ok := got.(*big.Rat)
		return ok && g != nil && w.Cmp(g) == 0, true
	case big.Rat:
		g, ok := got.(*big.Rat)
		return ok && g != nil && w.Cmp(g) == 0, true
	case string:
		g, ok := got.(string)
		return ok && w == g, true
	case bool:
		g, ok := got.(bool)
		return ok && w == g, true
	}
	return false, false
}

// sameMap compares a record or map value. Both must have exactly the same
// keys: the encoder ignores a key the schema does not define, and fills a
// missing one with the field default.
func sameMap(want map[string]any, got any) bool {
	g, ok := asStringMap(got)
	if !ok || len(g) != len(want) {
		return false
	}
	for k, wv := range want {
		gv, ok := g[k]
		if !ok || !sameValue(wv, gv) {
			return false
		}
	}
	return true
}

// sameReflectMap compares a typed Go map with string keys (for example
// map[string]int) with the decoded map[string]any.
func sameReflectMap(want, got reflect.Value) bool {
	g, ok := asStringMap(got.Interface())
	if !ok || len(g) != want.Len() {
		return false
	}
	iter := want.MapRange()
	for iter.Next() {
		gv, ok := g[iter.Key().String()]
		if !ok || !sameValue(iter.Value().Interface(), gv) {
			return false
		}
	}
	return true
}

func sameList(want, got reflect.Value) bool {
	if got.Kind() != reflect.Slice && got.Kind() != reflect.Array {
		return false
	}
	if want.Len() != got.Len() {
		return false
	}
	for i := range want.Len() {
		if !sameValue(want.Index(i).Interface(), got.Index(i).Interface()) {
			return false
		}
	}
	return true
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() { //nolint:exhaustive // only nillable kinds matter
	case reflect.Map, reflect.Slice, reflect.Pointer, reflect.Interface:
		return rv.IsNil()
	default:
		return false
	}
}

func asStringMap(v any) (map[string]any, bool) {
	switch v := v.(type) {
	case map[string]any:
		return v, true
	case opencdc.StructuredData:
		return v, true
	default:
		return nil, false
	}
}

// byteContents returns the contents of a []byte or [N]byte value.
func byteContents(v reflect.Value) ([]byte, bool) {
	if (v.Kind() != reflect.Slice && v.Kind() != reflect.Array) || v.Type().Elem().Kind() != reflect.Uint8 {
		return nil, false
	}
	if v.Kind() == reflect.Slice {
		return v.Bytes(), true
	}
	out := make([]byte, v.Len())
	reflect.Copy(reflect.ValueOf(out), v)
	return out, true
}

func isNumber(k reflect.Kind) bool {
	switch k { //nolint:exhaustive // only numeric kinds matter
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

// sameNumber compares two numbers exactly. Integers are compared as integers
// (no float64 rounding above 2^53), floats as float64, which is exact for
// float32 too. NaN equals NaN here: a NaN that round-trips is unchanged.
func sameNumber(a, b reflect.Value) bool {
	ar, aok := exactRat(a)
	br, bok := exactRat(b)
	if aok && bok {
		return ar.Cmp(br) == 0
	}
	af, bf := asFloat(a), asFloat(b)
	if math.IsNaN(af) || math.IsNaN(bf) {
		return math.IsNaN(af) && math.IsNaN(bf)
	}
	return af == bf
}

func exactRat(v reflect.Value) (*big.Rat, bool) {
	switch v.Kind() { //nolint:exhaustive // only numeric kinds reach here
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return new(big.Rat).SetInt64(v.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(v.Uint())), true
	default:
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, false
		}
		return new(big.Rat).SetFloat64(f), true
	}
}

func asFloat(v reflect.Value) float64 {
	switch v.Kind() { //nolint:exhaustive // only numeric kinds reach here
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return float64(v.Uint())
	default:
		return v.Float()
	}
}

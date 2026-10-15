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

// Package recordgen provides pgregory.net/rapid generators of OpenCDC
// records for the built-in processors' property tests. It is test support:
// only _test.go files may import it.
//
// It lives here, not in conduit-commons, because the engine can only use a
// released commons. Once commons ships an OpenCDC generator package, these
// move there and this package goes away.
package recordgen

import (
	"math"
	"strconv"

	"github.com/conduitio/conduit-commons/opencdc"
	"pgregory.net/rapid"
)

// maxDepth bounds how deeply JSONValue nests arrays and objects.
const maxDepth = 3

// JSONValue returns a generator of the values encoding/json decodes into an
// any: nil, bool, float64, string, []any and map[string]any, nested up to
// three levels. Every value it draws survives a JSON marshal and unmarshal
// round trip unchanged:
//
//   - numbers are finite float64s (JSON has no NaN or Inf, and decodes every
//     number as float64);
//   - strings are valid UTF-8 (JSON encoders replace invalid bytes with
//     U+FFFD).
func JSONValue() *rapid.Generator[any] {
	return rapid.Custom(func(t *rapid.T) any { return drawJSON(t, maxDepth) })
}

// JSONObject returns a generator of JSON objects whose values are drawn
// like JSONValue's.
func JSONObject() *rapid.Generator[map[string]any] {
	return rapid.Custom(func(t *rapid.T) map[string]any { return drawObject(t, maxDepth) })
}

func drawJSON(t *rapid.T, depth int) any {
	kinds := 4
	if depth > 0 {
		kinds = 6
	}
	switch rapid.IntRange(0, kinds-1).Draw(t, "json kind") {
	case 0:
		return nil
	case 1:
		return rapid.Bool().Draw(t, "bool")
	case 2:
		return rapid.Float64Range(-math.MaxFloat64, math.MaxFloat64).Draw(t, "number")
	case 3:
		return rapid.String().Draw(t, "string")
	case 4:
		a := make([]any, rapid.IntRange(0, 3).Draw(t, "array length"))
		for i := range a {
			a[i] = drawJSON(t, depth-1)
		}
		return a
	default:
		return drawObject(t, depth-1)
	}
}

func drawObject(t *rapid.T, depth int) map[string]any {
	keys := rapid.SliceOfNDistinct(rapid.String(), 0, 4, rapid.ID[string]).Draw(t, "object keys")
	m := make(map[string]any, len(keys))
	for _, k := range keys {
		m[k] = drawJSON(t, depth)
	}
	return m
}

// Metadata returns a generator of record metadata: zero to three entries
// with arbitrary keys and values.
func Metadata() *rapid.Generator[opencdc.Metadata] {
	return rapid.Custom(func(t *rapid.T) opencdc.Metadata {
		return rapid.MapOfN(rapid.String(), rapid.String(), 0, 3).Draw(t, "metadata")
	})
}

// Batch returns a generator of one to five records. Record i has position
// "i", so a property can check that a processor kept every record, in
// order. The key and both payload sides are drawn with data.
func Batch(data *rapid.Generator[opencdc.Data]) *rapid.Generator[[]opencdc.Record] {
	return rapid.Custom(func(t *rapid.T) []opencdc.Record {
		out := make([]opencdc.Record, rapid.IntRange(1, 5).Draw(t, "batch size"))
		for i := range out {
			out[i] = opencdc.Record{
				Position:  opencdc.Position(strconv.Itoa(i)),
				Operation: rapid.SampledFrom([]opencdc.Operation{opencdc.OperationCreate, opencdc.OperationUpdate, opencdc.OperationDelete, opencdc.OperationSnapshot}).Draw(t, "operation"),
				Metadata:  Metadata().Draw(t, "metadata"),
				Key:       data.Draw(t, "key"),
				Payload: opencdc.Change{
					Before: data.Draw(t, "before"),
					After:  data.Draw(t, "after"),
				},
			}
		}
		return out
	})
}

// StructuredData returns a generator of structured data drawn from
// JSONObject.
func StructuredData() *rapid.Generator[opencdc.Data] {
	return rapid.Custom(func(t *rapid.T) opencdc.Data {
		return opencdc.StructuredData(JSONObject().Draw(t, "structured"))
	})
}

// RawData returns a generator of raw data: zero to 32 arbitrary bytes.
func RawData() *rapid.Generator[opencdc.Data] {
	return rapid.Custom(func(t *rapid.T) opencdc.Data {
		return opencdc.RawData(rapid.SliceOfN(rapid.Byte(), 0, 32).Draw(t, "raw"))
	})
}

// Clone deep-copies records and keeps every Go type as it is.
// opencdc.Record.Clone is not usable for an expected value: it turns nested
// map[string]any values into opencdc.StructuredData, so a clone compares
// unequal to the original.
func Clone(in []opencdc.Record) []opencdc.Record {
	out := make([]opencdc.Record, len(in))
	for i, rec := range in {
		out[i] = opencdc.Record{
			Position:  clonePosition(rec.Position),
			Operation: rec.Operation,
			Metadata:  cloneMetadata(rec.Metadata),
			Key:       cloneData(rec.Key),
			Payload: opencdc.Change{
				Before: cloneData(rec.Payload.Before),
				After:  cloneData(rec.Payload.After),
			},
		}
	}
	return out
}

func clonePosition(p opencdc.Position) opencdc.Position {
	if p == nil {
		return nil
	}
	return append(opencdc.Position{}, p...)
}

func cloneMetadata(m opencdc.Metadata) opencdc.Metadata {
	if m == nil {
		return nil
	}
	out := make(opencdc.Metadata, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneData(d opencdc.Data) opencdc.Data {
	switch d := d.(type) {
	case opencdc.StructuredData:
		return opencdc.StructuredData(cloneValue(map[string]any(d)).(map[string]any)) //nolint:forcetypeassert // cloneValue keeps the type
	case opencdc.RawData:
		return append(opencdc.RawData{}, d...)
	}
	return d
}

func cloneValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = cloneValue(e)
		}
		return out
	case opencdc.StructuredData:
		return opencdc.StructuredData(cloneValue(map[string]any(v)).(map[string]any)) //nolint:forcetypeassert // cloneValue keeps the type
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = cloneValue(e)
		}
		return out
	case []byte:
		return append([]byte{}, v...)
	}
	return v
}

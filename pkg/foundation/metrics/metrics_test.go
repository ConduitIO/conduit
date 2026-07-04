// Copyright © 2024 Meroxa, Inc.
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

package metrics

import (
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/matryer/is"
)

// structuredRecord returns a record with structured key and payload resembling a
// typical CDC row: strings, numbers, a bool, a nested object, an array and null.
func structuredRecord() opencdc.Record {
	return opencdc.Record{
		Key: opencdc.StructuredData{"id": "12345"},
		Payload: opencdc.Change{
			After: opencdc.StructuredData{
				"id":       float64(42),
				"name":     "Alice Example",
				"email":    "alice@example.com",
				"active":   true,
				"score":    98.6,
				"tags":     []any{"reader", "writer"},
				"address":  map[string]any{"city": "Berlin", "zip": "10115"},
				"nickname": nil,
			},
		},
	}
}

// TestEstimateJSONSize_ByteExactVsEncoder is the core correctness test: for every
// value kind produced by JSON decoding, the estimate must equal the byte count
// of the real opencdc JSON encoder (StructuredData.Bytes()), including the
// tricky cases — HTML-escaped characters, backslash/control escapes, epoch-ms and
// boundary floats, unicode, and nesting.
func TestEstimateJSONSize_ByteExactVsEncoder(t *testing.T) {
	cases := map[string]opencdc.StructuredData{
		"simple":         {"id": "12345"},
		"url with &":     {"url": "https://example.com/x?a=1&b=2&c=3"},
		"escapes":        {"s": "line1\nline2\ttab \"quote\" back\\slash"},
		"html chars":     {"s": "a&b<c>d"},
		"epoch ms float": {"ts": float64(1_700_000_000_123)},
		"float boundaries": {
			"big":    1e20,  // 'f' format: 21 digits
			"bigger": 1e21,  // 'e' format
			"small":  1e-7,  // 'e' format (goccy keeps 1e-07, no exponent trim)
			"tiny":   1e-6,  // 'f' format
			"neg":    -12.5, // sign
		},
		"unicode":  {"s": "héllo wörld 日本語"},
		"bool/nil": {"a": true, "b": false, "c": nil},
		"nested":   {"n": map[string]any{"a": []any{float64(1), "two", true, nil}}},
		"array":    {"m": []any{float64(42), 98.6, "x", false}},
	}
	for name, sd := range cases {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			est := estimateJSONSize(map[string]any(sd))
			actual := len(sd.Bytes())
			is.Equal(est, actual) // estimate is byte-exact vs the opencdc JSON encoder
		})
	}
}

// TestRecordBytesHistogram_SizeOf_MatchesEncoder checks the full SizeOf path over
// structured key + payload equals the real encoded size.
func TestRecordBytesHistogram_SizeOf_MatchesEncoder(t *testing.T) {
	is := is.New(t)
	m := RecordBytesHistogram{}
	rec := structuredRecord()

	actual := len(rec.Key.Bytes()) + len(rec.Payload.After.Bytes())
	is.Equal(int(m.SizeOf(rec)), actual)
}

func TestRecordBytesHistogram_SizeOf_RawDataExact(t *testing.T) {
	is := is.New(t)
	m := RecordBytesHistogram{}
	rec := opencdc.Record{
		Key:     opencdc.RawData("key-123"),
		Payload: opencdc.Change{After: opencdc.RawData("hello world payload")},
	}
	// Raw data is measured exactly: raw byte lengths, no JSON framing.
	is.Equal(m.SizeOf(rec), float64(len("key-123")+len("hello world payload")))
}

func TestRecordBytesHistogram_SizeOf_Nil(t *testing.T) {
	is := is.New(t)
	m := RecordBytesHistogram{}
	is.Equal(m.SizeOf(opencdc.Record{}), float64(0))
}

// TestRecordBytesHistogram_SizeOf_ZeroAlloc locks in the headline property of
// #2268: computing a record's size must not allocate.
func TestRecordBytesHistogram_SizeOf_ZeroAlloc(t *testing.T) {
	is := is.New(t)
	m := RecordBytesHistogram{}
	rec := structuredRecord()
	avg := testing.AllocsPerRun(100, func() {
		_ = m.SizeOf(rec)
	})
	is.Equal(avg, 0.0) // SizeOf must be allocation-free
}

// BenchmarkRecordBytesHistogram_SizeOf measures the new marshal-free path.
func BenchmarkRecordBytesHistogram_SizeOf(b *testing.B) {
	m := RecordBytesHistogram{}
	rec := structuredRecord()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.SizeOf(rec)
	}
}

// BenchmarkSizeOf_ViaBytes measures the previous json.Marshal-based approach for
// comparison (issue #2268): calling Bytes() on structured key/payload.
func BenchmarkSizeOf_ViaBytes(b *testing.B) {
	rec := structuredRecord()
	b.ReportAllocs()
	for b.Loop() {
		var bytes int
		if rec.Key != nil {
			bytes += len(rec.Key.Bytes())
		}
		if rec.Payload.Before != nil {
			bytes += len(rec.Payload.Before.Bytes())
		}
		if rec.Payload.After != nil {
			bytes += len(rec.Payload.After.Bytes())
		}
		_ = float64(bytes)
	}
}

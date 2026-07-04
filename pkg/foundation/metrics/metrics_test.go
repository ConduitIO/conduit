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

// TestRecordBytesHistogram_SizeOf_ApproximatesJSON verifies that the marshal-free
// estimate stays close to the real json.Marshal byte count (the value the metric
// reported before #2268). We compute the "actual" size via Bytes(), which still
// marshals to JSON, and assert the estimate is within 10%.
func TestRecordBytesHistogram_SizeOf_ApproximatesJSON(t *testing.T) {
	is := is.New(t)
	m := RecordBytesHistogram{}
	rec := structuredRecord()

	actual := len(rec.Key.Bytes()) + len(rec.Payload.After.Bytes())
	est := int(m.SizeOf(rec))

	diff := actual - est
	if diff < 0 {
		diff = -diff
	}
	is.True(actual > 0)
	is.True(float64(diff) <= 0.10*float64(actual)) // estimate within 10% of real JSON size
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

func TestEstimateJSONSize_ExactForSimpleObject(t *testing.T) {
	is := is.New(t)
	// {"id":"12345"} encodes to exactly 14 bytes; the estimator is exact here
	// since there are no floats or escaped characters.
	sd := opencdc.StructuredData{"id": "12345"}
	is.Equal(estimateJSONSize(map[string]any(sd)), len(sd.Bytes()))
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

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

package funnel

import (
	"strconv"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/matryer/is"
)

// recordingTimer captures the start times updateTimer reports, so a test can
// assert which readAt each record was measured against.
type recordingTimer struct {
	since []time.Time
	durs  []time.Duration
}

func (t *recordingTimer) Update(d time.Duration)  { t.durs = append(t.durs, d) }
func (t *recordingTimer) UpdateSince(s time.Time) { t.since = append(t.since, s) }

func recordWithReadAt(raw string) opencdc.Record {
	md := opencdc.Metadata{opencdc.MetadataConduitSourceConnectorID: "src"}
	if raw != "" {
		md[opencdc.MetadataReadAt] = raw
	}
	return opencdc.Record{Metadata: md}
}

// TestWorker_updateTimer pins the behaviour of the per-record readAt lookup on
// the ack path. The lookup was rewritten to stop paying for an error it always
// discards and to parse once per distinct value instead of once per record;
// none of the cases below may change as a result.
func TestWorker_updateTimer(t *testing.T) {
	fallback := time.Unix(0, 1_700_000_000_000_000_000)
	a := time.Unix(0, 1_764_201_600_123_456_789)
	b := time.Unix(0, 1_764_201_600_987_654_321)
	rawA := strconv.FormatInt(a.UnixNano(), 10)
	rawB := strconv.FormatInt(b.UnixNano(), 10)

	testCases := []struct {
		name    string
		records []opencdc.Record
		want    []time.Time
	}{{
		name:    "no records",
		records: nil,
		want:    nil,
	}, {
		name:    "one readAt for the whole batch, as a source stamps it",
		records: []opencdc.Record{recordWithReadAt(rawA), recordWithReadAt(rawA), recordWithReadAt(rawA)},
		want:    []time.Time{a, a, a},
	}, {
		name:    "distinct readAt values are each parsed",
		records: []opencdc.Record{recordWithReadAt(rawA), recordWithReadAt(rawB), recordWithReadAt(rawA)},
		want:    []time.Time{a, b, a},
	}, {
		name:    "missing key falls back to the last source read",
		records: []opencdc.Record{recordWithReadAt("")},
		want:    []time.Time{fallback},
	}, {
		name:    "nil metadata falls back",
		records: []opencdc.Record{{}},
		want:    []time.Time{fallback},
	}, {
		name:    "malformed value falls back and is not cached",
		records: []opencdc.Record{recordWithReadAt("yesterday"), recordWithReadAt(rawA), recordWithReadAt("yesterday")},
		want:    []time.Time{fallback, a, fallback},
	}, {
		name:    "out of int64 range falls back",
		records: []opencdc.Record{recordWithReadAt("99999999999999999999")},
		want:    []time.Time{fallback},
	}, {
		name:    "a missing key between two set ones does not poison the cache",
		records: []opencdc.Record{recordWithReadAt(rawA), recordWithReadAt(""), recordWithReadAt(rawA)},
		want:    []time.Time{a, fallback, a},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			timer := &recordingTimer{}
			w := &Worker{timer: timer}
			w.lastReadAt.Store(fallback.UnixNano())

			w.updateTimer(tc.records)

			is.Equal(len(timer.since), len(tc.want))
			for i := range tc.want {
				is.True(timer.since[i].Equal(tc.want[i]))
			}
		})
	}
}

// TestWorker_updateTimer_NoAllocs is the regression test for the cost that
// motivated the rewrite. Metadata.GetReadAt reports a missing key by building
// an error with fmt.Errorf, and this caller discards it; on records without the
// key that discarded error was 44.6% of all objects allocated by the engine
// benchmark kit. The ack path must allocate nothing per record either way.
func TestWorker_updateTimer_NoAllocs(t *testing.T) {
	rawA := strconv.FormatInt(time.Now().UnixNano(), 10)

	testCases := []struct {
		name    string
		records []opencdc.Record
	}{{
		name:    "readAt set, as production records have it",
		records: []opencdc.Record{recordWithReadAt(rawA), recordWithReadAt(rawA), recordWithReadAt(rawA)},
	}, {
		name:    "readAt missing",
		records: []opencdc.Record{recordWithReadAt(""), recordWithReadAt(""), recordWithReadAt("")},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// A timer that keeps nothing: the assertion is about the lookup,
			// not about the histogram behind it.
			w := &Worker{timer: discardTimer{}}
			w.lastReadAt.Store(time.Now().UnixNano())

			allocs := testing.AllocsPerRun(100, func() {
				w.updateTimer(tc.records)
			})
			if allocs != 0 {
				t.Fatalf("updateTimer allocated %v objects per call for %d records, want 0", allocs, len(tc.records))
			}
		})
	}
}

type discardTimer struct{}

func (discardTimer) Update(time.Duration)  {}
func (discardTimer) UpdateSince(time.Time) {}

// BenchmarkWorker_updateTimer is the per-record cost of the ack path's readAt
// lookup, with and without the key present.
func BenchmarkWorker_updateTimer(b *testing.B) {
	const batchSize = 100
	raw := strconv.FormatInt(time.Now().UnixNano(), 10)

	for _, tc := range []struct {
		name string
		raw  string
	}{{"readAt-set", raw}, {"readAt-missing", ""}} {
		b.Run(tc.name, func(b *testing.B) {
			records := make([]opencdc.Record, batchSize)
			for i := range records {
				records[i] = recordWithReadAt(tc.raw)
			}
			w := &Worker{timer: discardTimer{}}
			w.lastReadAt.Store(time.Now().UnixNano())

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				w.updateTimer(records)
			}
			b.StopTimer()
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*batchSize), "ns/record")
		})
	}
}

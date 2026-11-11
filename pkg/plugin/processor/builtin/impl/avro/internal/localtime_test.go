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
	"context"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // America/New_York for the DST test, whatever the host has

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-commons/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

const (
	localMicros = `{"type":"long","logicalType":"local-timestamp-micros"}`
	utcMicros   = `{"type":"long","logicalType":"timestamp-micros"}`
)

// localTimeCase is one record encoded with the fromRecord strategy. field is
// the top-level field the schema_mismatch error must name, or "" when the
// record must encode.
type localTimeCase struct {
	name  string
	in    opencdc.StructuredData
	field string
}

// runLocalTimeCases encodes each case against carriedSchema. The caller sets
// time.Local.
func runLocalTimeCases(t *testing.T, carriedSchema string, cases []localTimeCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newFromRecordEnv(t, carriedSchema)
			_, err := env.enc.Encode(context.Background(), tc.in, env.carried)
			if tc.field == "" {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				return
			}
			wantCode(t, err, CodeSchemaMismatch)
			ce, _ := conduiterr.Get(err)
			if want := `field "` + tc.field + `"`; !strings.Contains(ce.Message, want) {
				t.Fatalf("message %q does not name %s", ce.Message, want)
			}
		})
	}
}

// setLocal sets time.Local for the test and restores it after. Tests that
// call it must not be parallel.
func setLocal(t *testing.T, loc *time.Location) {
	t.Helper()
	prev := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = prev })
}

var (
	ltUTC   = time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	ltPlus5 = time.Date(2024, 1, 1, 10, 0, 0, 0, time.FixedZone("x", 5*3600))
)

// TestFromRecord_LocalTimestampNamedTypes: a bare type name resolves in the
// namespace that encloses it, and a dotted name is a full name. Two records
// with the same short name in different namespaces must not be confused, or
// a changed wall clock in a local-timestamp field gets through (the walk
// would look at the record with the plain timestamp field). Not parallel: it
// sets time.Local.
func TestFromRecord_LocalTimestampNamedTypes(t *testing.T) {
	setLocal(t, time.UTC)
	ts := func(v time.Time) map[string]any { return map[string]any{"t": v} }

	t.Run("bare name in a namespace", func(t *testing.T) {
		// "Ts" in namespace a is a.Ts (local-timestamp), not b.Ts, which is
		// defined after it.
		const s = `{"type":"record","name":"root","namespace":"a","fields":[` +
			`{"name":"y","type":{"type":"record","name":"Ts","fields":[{"name":"t","type":` + localMicros + `}]}},` +
			`{"name":"z","type":{"type":"record","name":"Ts","namespace":"b","fields":[{"name":"t","type":` + utcMicros + `}]}},` +
			`{"name":"x","type":"Ts"},` +
			`{"name":"w","type":"b.Ts"}]}`
		row := func(x, w time.Time) opencdc.StructuredData {
			return opencdc.StructuredData{"y": ts(ltUTC), "z": ts(ltUTC), "x": ts(x), "w": ts(w)}
		}
		runLocalTimeCases(t, s, []localTimeCase{
			{"all UTC", row(ltUTC, ltUTC), ""},
			{"non-UTC through the bare name", row(ltPlus5, ltUTC), "x"},
			{"non-UTC through a full name of the plain timestamp record", row(ltUTC, ltPlus5), ""},
		})
	})

	t.Run("bare name in the null namespace", func(t *testing.T) {
		// No namespace anywhere but q.Ts: "Ts" is the record named Ts, not
		// q.Ts, whose short name is the same.
		const s = `{"type":"record","name":"root","fields":[` +
			`{"name":"y","type":{"type":"record","name":"Ts","fields":[{"name":"t","type":` + localMicros + `}]}},` +
			`{"name":"z","type":{"type":"record","name":"q.Ts","fields":[{"name":"t","type":` + utcMicros + `}]}},` +
			`{"name":"x","type":"Ts"}]}`
		row := func(x time.Time) opencdc.StructuredData {
			return opencdc.StructuredData{"y": ts(ltUTC), "z": ts(ltUTC), "x": ts(x)}
		}
		runLocalTimeCases(t, s, []localTimeCase{
			{"all UTC", row(ltUTC), ""},
			{"non-UTC through the bare name", row(ltPlus5), "x"},
		})
	})

	t.Run("names inside a referenced record resolve where it is defined", func(t *testing.T) {
		// a.Outer's field "in" has type "Ts", which is a.Ts (local-timestamp)
		// where Outer is defined, even when Outer is referenced from
		// namespace b, where "Ts" would be b.Ts.
		const s = `{"type":"record","name":"root","namespace":"b","fields":[` +
			`{"name":"p","type":{"type":"record","name":"Ts","namespace":"a","fields":[{"name":"t","type":` + localMicros + `}]}},` +
			`{"name":"q","type":{"type":"record","name":"Ts","fields":[{"name":"t","type":` + utcMicros + `}]}},` +
			`{"name":"o","type":{"type":"record","name":"Outer","namespace":"a","fields":[{"name":"in","type":"Ts"}]}},` +
			`{"name":"r","type":"a.Outer"}]}`
		row := func(r time.Time) opencdc.StructuredData {
			return opencdc.StructuredData{
				"p": ts(ltUTC), "q": ts(ltUTC),
				"o": map[string]any{"in": ts(ltUTC)}, "r": map[string]any{"in": ts(r)},
			}
		}
		runLocalTimeCases(t, s, []localTimeCase{
			{"all UTC", row(ltUTC), ""},
			{"non-UTC inside the referenced record", row(ltPlus5), "r"},
		})
	})
}

// TestFromRecord_LocalTimestampContainers pins each place the walk looks into:
// array items, map values, and a record referenced by bare and by full name.
// Each failing case passes the instant round trip, so it fails only if the
// walk reaches it. Not parallel: it sets time.Local.
func TestFromRecord_LocalTimestampContainers(t *testing.T) {
	setLocal(t, time.UTC)
	const s = `{"type":"record","name":"c","namespace":"public","fields":[` +
		`{"name":"arr","type":{"type":"array","items":` + localMicros + `}},` +
		`{"name":"m","type":{"type":"map","values":` + localMicros + `}},` +
		`{"name":"def","type":{"type":"record","name":"inner","fields":[{"name":"t","type":` + localMicros + `}]}},` +
		`{"name":"ref","type":"inner"},` +
		`{"name":"refFull","type":"public.inner"}]}`
	row := func(arr, m, ref, refFull time.Time) opencdc.StructuredData {
		return opencdc.StructuredData{
			"arr":     []any{ltUTC, arr},
			"m":       map[string]any{"k": m},
			"def":     map[string]any{"t": ltUTC},
			"ref":     map[string]any{"t": ref},
			"refFull": map[string]any{"t": refFull},
		}
	}
	runLocalTimeCases(t, s, []localTimeCase{
		{"all UTC", row(ltUTC, ltUTC, ltUTC, ltUTC), ""},
		{"non-UTC array item", row(ltPlus5, ltUTC, ltUTC, ltUTC), "arr"},
		{"non-UTC map value", row(ltUTC, ltPlus5, ltUTC, ltUTC), "m"},
		{"non-UTC in a record referenced by bare name", row(ltUTC, ltUTC, ltPlus5, ltUTC), "ref"},
		{"non-UTC in a record referenced by full name", row(ltUTC, ltUTC, ltUTC, ltPlus5), "refFull"},
	})
}

// TestFromRecord_LocalTimestampDST runs with a local zone that has DST. The
// check compares the value's offset with the local offset at that instant, so
// a fixed -05:00 time is fine in January (EST) and a mismatch in July (EDT).
// Not parallel: it sets time.Local.
func TestFromRecord_LocalTimestampDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	setLocal(t, ny)
	const s = `{"type":"record","name":"dst","namespace":"public","fields":[{"name":"a","type":` + localMicros + `}]}`
	minus5, minus4 := time.FixedZone("m5", -5*3600), time.FixedZone("m4", -4*3600)
	row := func(v time.Time) opencdc.StructuredData { return opencdc.StructuredData{"a": v} }
	runLocalTimeCases(t, s, []localTimeCase{
		{"local winter time", row(time.Date(2024, 1, 15, 10, 0, 0, 0, ny)), ""},
		{"local summer time", row(time.Date(2024, 7, 15, 10, 0, 0, 0, ny)), ""},
		{"-05:00 in winter matches EST", row(time.Date(2024, 1, 15, 10, 0, 0, 0, minus5)), ""},
		{"-04:00 in summer matches EDT", row(time.Date(2024, 7, 15, 10, 0, 0, 0, minus4)), ""},
		{"-05:00 in summer differs from EDT", row(time.Date(2024, 7, 15, 10, 0, 0, 0, minus5)), "a"},
		{"-04:00 in winter differs from EST", row(time.Date(2024, 1, 15, 10, 0, 0, 0, minus4)), "a"},
		{"UTC differs from the local zone", row(time.Date(2024, 7, 15, 10, 0, 0, 0, time.UTC)), "a"},
		// 2024-11-03 01:30 happens twice in New York. The first one (EDT)
		// round trips.
		{"first 01:30 of the fall-back overlap", row(time.Date(2024, 11, 3, 5, 30, 0, 0, time.UTC).In(ny)), ""},
	})

	// The second 01:30 (EST) passes the offset check, but the codec reads the
	// wall clock 01:30 back as a time in Local, and Go does not say which of
	// the two it picks. Either outcome is lossless: an exact round trip, or
	// schema_mismatch from the value comparison. Anything else is a bug.
	t.Run("second 01:30 of the fall-back overlap", func(t *testing.T) {
		env := newFromRecordEnv(t, s)
		_, err := env.enc.Encode(context.Background(), row(time.Date(2024, 11, 3, 6, 30, 0, 0, time.UTC).In(ny)), env.carried)
		if err != nil {
			wantCode(t, err, CodeSchemaMismatch)
		}
	})
}

// TestLocalTimeCheck_Cache: the parsed walk is cached by schema text, so two
// schemas that alternate keep their own answers, and a schema without a
// local-timestamp field is never parsed or cached. Not parallel: it sets
// time.Local.
func TestLocalTimeCheck_Cache(t *testing.T) {
	setLocal(t, time.UTC)
	local := schema.Schema{Type: schema.TypeAvro, Bytes: []byte(`{"type":"record","name":"l","fields":[{"name":"a","type":` + localMicros + `}]}`)}
	plain := schema.Schema{Type: schema.TypeAvro, Bytes: []byte(`{"type":"record","name":"l","fields":[{"name":"a","type":` + utcMicros + `}]}`)}
	sd := opencdc.StructuredData{"a": ltPlus5}

	var c localTimeCheck
	for i := range 3 {
		if got := c.changed(local, sd); got != "a" {
			t.Fatalf("round %d: local schema: want %q, got %q", i, "a", got)
		}
		if got := c.changed(plain, sd); got != "" {
			t.Fatalf("round %d: plain schema: want no mismatch, got %q", i, got)
		}
	}
	if len(c.walks) != 1 {
		t.Fatalf("want 1 cached walk (the local schema), got %d", len(c.walks))
	}

	// A full cache is cleared, not grown.
	for i := range maxLocalWalks + 1 {
		s := schema.Schema{Type: schema.TypeAvro, Bytes: []byte(`{"type":"record","name":"l` + strings.Repeat("x", i) + `","fields":[{"name":"a","type":` + localMicros + `}]}`)}
		if got := c.changed(s, sd); got != "a" {
			t.Fatalf("schema %d: want %q, got %q", i, "a", got)
		}
	}
	if len(c.walks) > maxLocalWalks {
		t.Fatalf("cache grew to %d entries, bound is %d", len(c.walks), maxLocalWalks)
	}
}

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

	t.Run("records defined inside an array and a map", func(t *testing.T) {
		// Item and Val are defined as array items and map values, and then
		// referenced by name in later fields.
		const s = `{"type":"record","name":"root","namespace":"a","fields":[` +
			`{"name":"arr","type":{"type":"array","items":{"type":"record","name":"Item","fields":[{"name":"t","type":` + localMicros + `}]}}},` +
			`{"name":"m","type":{"type":"map","values":{"type":"record","name":"Val","fields":[{"name":"t","type":` + localMicros + `}]}}},` +
			`{"name":"itemRef","type":"Item"},` +
			`{"name":"valRef","type":"a.Val"}]}`
		row := func(arr, m, itemRef, valRef time.Time) opencdc.StructuredData {
			return opencdc.StructuredData{
				"arr": []any{ts(arr)}, "m": map[string]any{"k": ts(m)},
				"itemRef": ts(itemRef), "valRef": ts(valRef),
			}
		}
		runLocalTimeCases(t, s, []localTimeCase{
			{"all UTC", row(ltUTC, ltUTC, ltUTC, ltUTC), ""},
			{"non-UTC in a record inside an array", row(ltPlus5, ltUTC, ltUTC, ltUTC), "arr"},
			{"non-UTC in a record inside a map", row(ltUTC, ltPlus5, ltUTC, ltUTC), "m"},
			{"non-UTC through a reference to the array's record", row(ltUTC, ltUTC, ltPlus5, ltUTC), "itemRef"},
			{"non-UTC through a reference to the map's record", row(ltUTC, ltUTC, ltUTC, ltPlus5), "valRef"},
		})
	})

	t.Run("recursive record", func(t *testing.T) {
		// Node refers to itself; the per-schema scan must end, and the value
		// walk must follow the chain.
		const s = `{"type":"record","name":"root","namespace":"a","fields":[` +
			`{"name":"x","type":{"type":"record","name":"Node","fields":[{"name":"t","type":` + localMicros + `},` +
			`{"name":"next","type":["null","Node"]}]}}]}`
		row := func(second time.Time) opencdc.StructuredData {
			return opencdc.StructuredData{"x": map[string]any{"t": ltUTC, "next": map[string]any{"t": second, "next": nil}}}
		}
		runLocalTimeCases(t, s, []localTimeCase{
			{"all UTC", row(ltUTC), ""},
			{"non-UTC in the second node", row(ltPlus5), "x"},
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

// runLocalTimeCasesNoRegistry runs the lossless part of Encoder.Encode (the
// codec encode and verifyRoundTrip) on schema text directly, without
// registering it. Used for schemas the Go codec and Conduit's built-in
// registry accept but the Confluent registry, which parses with Java Avro,
// refuses at registration (a reference by alias, a union wrapped in
// {"type": [...]}); the integration build runs every registry test against
// Confluent.
func runLocalTimeCasesNoRegistry(t *testing.T, text string, cases []localTimeCase) {
	t.Helper()
	s := schema.Schema{Type: schema.TypeAvro, Bytes: []byte(text)}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &Encoder{lossless: true}
			data, err := s.Marshal(tc.in)
			if err != nil {
				t.Fatalf("codec refused the record: %v", err)
			}
			err = e.verifyRoundTrip(s, tc.in, data)
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

// TestFromRecord_LocalTimestampCodecSpellings: valid schema spellings whose
// names or logical types the codec resolves in ways a reading of the JSON can
// miss. The check must see each local-timestamp field exactly where the codec
// writes one, or a changed wall clock is encoded. It tests the check on the
// codec's parse, so it runs without a registry (see
// runLocalTimeCasesNoRegistry). Not parallel: it sets time.Local.
func TestFromRecord_LocalTimestampCodecSpellings(t *testing.T) {
	setLocal(t, time.UTC)
	ts := func(v time.Time) map[string]any { return map[string]any{"t": v} }
	localTs := `{"type":"record","name":"Ts","fields":[{"name":"t","type":` + localMicros + `}]}`
	withY := func(v time.Time) opencdc.StructuredData {
		return opencdc.StructuredData{"y": ts(ltUTC), "x": ts(v)}
	}
	onlyX := func(v time.Time) opencdc.StructuredData { return opencdc.StructuredData{"x": ts(v)} }
	root := func(fields string) string {
		return `{"type":"record","name":"root","namespace":"a","fields":[` + fields + `]}`
	}

	testCases := []struct {
		name   string
		schema string
		row    func(time.Time) opencdc.StructuredData
	}{{
		name: "(a) reference by alias",
		schema: root(`{"name":"y","type":{"type":"record","name":"Ts","aliases":["Old"],"fields":[{"name":"t","type":` + localMicros + `}]}},` +
			`{"name":"x","type":"Old"}`),
		row: withY,
	}, {
		// The codec treats an empty namespace as "inherit", so this is a.Ts.
		name: "(b) empty namespace attribute",
		schema: root(`{"name":"y","type":{"type":"record","name":"Ts","namespace":"","fields":[{"name":"t","type":` + localMicros + `}]}},` +
			`{"name":"x","type":"Ts"}`),
		row: withY,
	}, {
		name:   "(c) reference wrapped in an object",
		schema: root(`{"name":"y","type":` + localTs + `},{"name":"x","type":{"type":"Ts"}}`),
		row:    withY,
	}, {
		name:   "(d) union wrapped in an object",
		schema: root(`{"name":"y","type":` + localTs + `},{"name":"x","type":{"type":["null","Ts"]}}`),
		row:    withY,
	}, {
		name:   "(e) error type",
		schema: root(`{"name":"x","type":{"type":"error","name":"Ts","fields":[{"name":"t","type":` + localMicros + `}]}}`),
		row:    onlyX,
	}, {
		name:   "(f) escaped logical type",
		schema: root(`{"name":"x","type":{"type":"record","name":"Ts","fields":[{"name":"t","type":{"type":"long","logicalType":"local\u002dtimestamp-micros"}}]}}`),
		row:    onlyX,
	}, {
		name:   "(g) local-timestamp-nanos",
		schema: root(`{"name":"x","type":{"type":"record","name":"Ts","fields":[{"name":"t","type":{"type":"long","logicalType":"local-timestamp-nanos"}}]}}`),
		row:    onlyX,
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			runLocalTimeCasesNoRegistry(t, tc.schema, []localTimeCase{
				{"UTC", tc.row(ltUTC), ""},
				{"non-UTC", tc.row(ltPlus5), "x"},
			})
		})
	}
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

// TestLocalTimeCheck_Cache: the parse is cached by schema text. Two schemas
// that both have a local-timestamp field, under different names, alternate
// and must keep their own answers (a cache that returned one schema's parse
// for the other would name the wrong field, or none). Not parallel: it sets
// time.Local.
func TestLocalTimeCheck_Cache(t *testing.T) {
	setLocal(t, time.UTC)
	avroSchema := func(fields string) schema.Schema {
		return schema.Schema{Type: schema.TypeAvro, Bytes: []byte(`{"type":"record","name":"l","fields":[` + fields + `]}`)}
	}
	localA := avroSchema(`{"name":"a","type":` + localMicros + `},{"name":"b","type":` + utcMicros + `}`)
	localB := avroSchema(`{"name":"a","type":` + utcMicros + `},{"name":"b","type":` + localMicros + `}`)
	plain := avroSchema(`{"name":"a","type":` + utcMicros + `},{"name":"b","type":` + utcMicros + `}`)
	sd := opencdc.StructuredData{"a": ltPlus5, "b": ltPlus5}

	var c localTimeCheck
	for i := range 3 {
		for _, want := range []struct {
			s     schema.Schema
			field string
		}{{localA, "a"}, {localB, "b"}, {plain, ""}} {
			if got := c.changed(want.s, sd); got != want.field {
				t.Fatalf("round %d, schema %s: want %q, got %q", i, want.s.Bytes, want.field, got)
			}
		}
	}
	if len(c.entries) != 3 {
		t.Fatalf("want 3 cached schemas, got %d", len(c.entries))
	}
}

// TestLocalTimeCheck_CacheBounds: a schema text above maxLocalSchemaBytes is
// checked but not kept, and the cache never holds more than maxLocalSchemas
// entries or maxLocalCacheBytes of text. Not parallel: it sets time.Local.
func TestLocalTimeCheck_CacheBounds(t *testing.T) {
	setLocal(t, time.UTC)
	// withDoc returns a schema with a local-timestamp field whose text is
	// at least size bytes, distinct per n.
	withDoc := func(n, size int) schema.Schema {
		pad := strings.Repeat("x", max(size, 0))
		return schema.Schema{Type: schema.TypeAvro, Bytes: []byte(`{"type":"record","name":"l` + strings.Repeat("y", n) +
			`","doc":"` + pad + `","fields":[{"name":"a","type":` + localMicros + `}]}`)}
	}
	sd := opencdc.StructuredData{"a": ltPlus5}
	check := func(c *localTimeCheck, s schema.Schema) {
		t.Helper()
		if got := c.changed(s, sd); got != "a" {
			t.Fatalf("want %q, got %q", "a", got)
		}
		if len(c.entries) > maxLocalSchemas {
			t.Fatalf("cache has %d entries, bound is %d", len(c.entries), maxLocalSchemas)
		}
		if c.bytes > maxLocalCacheBytes {
			t.Fatalf("cache holds %d bytes of text, bound is %d", c.bytes, maxLocalCacheBytes)
		}
		total := 0
		for k := range c.entries {
			total += len(k)
		}
		if total != c.bytes {
			t.Fatalf("byte count %d does not match the cached texts (%d)", c.bytes, total)
		}
	}

	t.Run("a large schema is not kept", func(t *testing.T) {
		var c localTimeCheck
		check(&c, withDoc(0, maxLocalSchemaBytes+1))
		if len(c.entries) != 0 {
			t.Fatalf("want the large schema not cached, got %d entries", len(c.entries))
		}
	})
	t.Run("entry bound", func(t *testing.T) {
		var c localTimeCheck
		for i := range maxLocalSchemas + 1 {
			check(&c, withDoc(i, 0))
		}
	})
	t.Run("byte bound", func(t *testing.T) {
		var c localTimeCheck
		// 200 KiB each: under the per-schema cap, and 41 of them pass 8 MiB.
		for i := range 50 {
			check(&c, withDoc(i, 200<<10))
		}
	})
}

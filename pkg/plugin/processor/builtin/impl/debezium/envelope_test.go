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

package debezium

import (
	"bytes"
	"strconv"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/debezium/connect"
	"github.com/matryer/is"
	"pgregory.net/rapid"
)

// table is a generated row shape: an int64 key column "id" and a few more
// columns of mixed types and optionality.
type table struct {
	desc *Descriptor
	text string
	cols []connect.Field
}

func genTable() *rapid.Generator[table] {
	return rapid.Custom(func(t *rapid.T) table {
		types := []connect.Type{connect.TypeInt32, connect.TypeInt64, connect.TypeString, connect.TypeBoolean, connect.TypeFloat64, connect.TypeBytes}
		cols := []connect.Field{{Name: "id", Schema: &connect.Schema{Type: connect.TypeInt64}}}
		n := rapid.IntRange(0, 6).Draw(t, "ncols")
		for i := 0; i < n; i++ {
			cols = append(cols, connect.Field{
				Name: "c" + strconv.Itoa(i),
				Schema: &connect.Schema{
					Type:     rapid.SampledFrom(types).Draw(t, "type"),
					Optional: rapid.Bool().Draw(t, "optional"),
				},
			})
		}
		value := &connect.Schema{Type: connect.TypeStruct, Optional: true, Name: "p.s.t.Value", Fields: cols}
		key := &connect.Schema{Type: connect.TypeStruct, Name: "p.s.t.Key", Fields: cols[:1]}
		d := &Descriptor{Key: key, Value: value}
		b, err := MarshalDescriptor(d)
		if err != nil {
			t.Fatal(err)
		}
		return table{desc: d, text: string(b), cols: cols}
	})
}

func genValue(t *rapid.T, s *connect.Schema, label string) any {
	if s.Optional && rapid.Bool().Draw(t, label+"-null") {
		return nil
	}
	switch s.Type { //nolint:exhaustive // the remaining types share the default branch
	case connect.TypeInt32:
		return rapid.Int32().Draw(t, label)
	case connect.TypeInt64:
		return rapid.Int64().Draw(t, label)
	case connect.TypeString:
		return rapid.String().Draw(t, label)
	case connect.TypeBoolean:
		return rapid.Bool().Draw(t, label)
	case connect.TypeFloat64:
		return rapid.Float64().Draw(t, label)
	case connect.TypeBytes:
		return rapid.SliceOf(rapid.Byte()).Draw(t, label)
	}
	panic(s.Type)
}

func genRow(t *rapid.T, tb table, id int64, label string) opencdc.StructuredData {
	row := opencdc.StructuredData{"id": id}
	for _, c := range tb.cols[1:] {
		row[c.Name] = genValue(t, c.Schema, label+c.Name)
	}
	return row
}

func sourceMD(md opencdc.Metadata) opencdc.Metadata {
	for k, v := range map[string]string{
		"version": "3.7.0.Final", "connector": "postgresql", "name": "p", "ts_ms": "1",
		"snapshot": "false", "db": "d", "schema": "s", "table": "t", "lsn": "42",
	} {
		md[MetaSourcePfx+k] = v
	}
	return md
}

func fixedClock() func() time.Time {
	return func() time.Time { return time.Unix(0, 1792037761675217963) }
}

func newTestBuilder(t interface{ Fatal(...any) }, tombstones bool) *Builder {
	b, err := NewBuilder(Options{Compat: Compat35, TombstonesOnDelete: tombstones, ExtendedHeaders: true, RunID: "run", Now: fixedClock()})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The plan for each op is exactly Debezium's message sequence (design: One
// WAL change, several messages): nothing is dropped, a delete's tombstone
// follows it with the same key, and a primary-key change is delete,
// tombstone, create with the newkey/oldkey headers.
//
//nolint:gocyclo // one assertion block per op
func TestPropPlanShape(t *testing.T) {
	conv := connect.NewJSONConverter()
	converters := Converters{Key: conv, Value: conv}
	rapid.Check(t, func(t *rapid.T) {
		tb := genTable().Draw(t, "table")
		tombstones := rapid.Bool().Draw(t, "tombstones")
		b := newTestBuilder(t, tombstones)
		op := rapid.SampledFrom([]string{"c", "r", "u", "d", "pk"}).Draw(t, "op")
		id := rapid.Int64().Draw(t, "id")
		md := sourceMD(opencdc.Metadata{MetaTopic: "p.s.t", MetaDescriptor: tb.text, MetaOp: op})
		rec := opencdc.Record{Key: opencdc.StructuredData{"id": id}, Metadata: md}
		switch op {
		case "c", "r":
			rec.Payload.After = genRow(t, tb, id, "after")
		case "u":
			if rapid.Bool().Draw(t, "hasBefore") {
				rec.Payload.Before = genRow(t, tb, id, "before")
			}
			rec.Payload.After = genRow(t, tb, id, "after")
		case "d":
			rec.Payload.Before = genRow(t, tb, id, "before")
		case "pk":
			oldID := rapid.Int64().Filter(func(v int64) bool { return v != id }).Draw(t, "oldID")
			md[MetaOp] = "u"
			md[MetaOldKey] = `{"id":` + strconv.FormatInt(oldID, 10) + `}`
			rec.Payload.Before = genRow(t, tb, oldID, "before")
			rec.Payload.After = genRow(t, tb, id, "after")
		}
		plan, err := b.Plan(rec)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		var enc []EncodedMessage
		for _, m := range plan {
			e, err := m.Encode(converters)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if e.Topic != "p.s.t" || e.Key == nil {
				t.Fatalf("message without topic or key: %+v", e)
			}
			enc = append(enc, e)
		}
		switch op {
		case "c", "r", "u":
			if len(enc) != 1 || enc[0].Value == nil {
				t.Fatalf("op %s: want one non-null message, got %d", op, len(enc))
			}
		case "d":
			want := 1
			if tombstones {
				want = 2
			}
			if len(enc) != want || enc[0].Value == nil {
				t.Fatalf("delete: want %d messages, got %d", want, len(enc))
			}
			if tombstones && (enc[1].Value != nil || !bytes.Equal(enc[1].Key, enc[0].Key)) {
				t.Fatal("tombstone must follow its delete with the same key and a null value")
			}
		case "pk":
			want := 2
			if tombstones {
				want = 3
			}
			if len(enc) != want {
				t.Fatalf("primary-key change: want %d messages, got %d", want, len(enc))
			}
			last := enc[len(enc)-1]
			if !bytes.Contains(enc[0].Value, []byte(`"op":"d"`)) || !bytes.Contains(last.Value, []byte(`"op":"c"`)) {
				t.Fatal("primary-key change must be delete ... create")
			}
			if bytes.Equal(enc[0].Key, last.Key) {
				t.Fatal("delete and create of a primary-key change must have different keys")
			}
			if enc[0].Headers[0].Key != HeaderNewKey || last.Headers[0].Key != HeaderOldKey {
				t.Fatal("missing newkey/oldkey headers")
			}
			if tombstones && (enc[1].Value != nil || !bytes.Equal(enc[1].Key, enc[0].Key) || enc[1].Headers[0].Key != HeaderNewKey) {
				t.Fatal("tombstone must repeat the delete's key and newkey header")
			}
		}
		// Encoding is deterministic: the same record encodes to the same
		// bytes, whatever the iteration order of the row maps.
		plan2, err := b.Plan(rec)
		if err != nil {
			t.Fatal(err)
		}
		for i, m := range plan2 {
			e, _ := m.Encode(converters)
			if !bytes.Equal(e.Value, enc[i].Value) || !bytes.Equal(e.Key, enc[i].Key) {
				t.Fatal("encoding is not deterministic")
			}
		}
	})
}

// A row field the descriptor does not know, a descriptor field the row does
// not have (absent, not null), or a source-block key the compat level does
// not have fails the record; nothing is dropped or invented (invariant 6).
func TestPropUnknownFieldsRejected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		tb := genTable().Draw(t, "table")
		b := newTestBuilder(t, true)
		row := genRow(t, tb, 1, "after")
		md := sourceMD(opencdc.Metadata{MetaTopic: "p.s.t", MetaDescriptor: tb.text, MetaOp: "c"})
		switch rapid.IntRange(0, 2).Draw(t, "case") {
		case 0:
			md[MetaSourcePfx+"unknown_"+rapid.StringMatching(`[a-z]{1,5}`).Draw(t, "k")] = "x"
		case 1:
			row["zz_"+rapid.StringMatching(`[a-z]{1,5}`).Draw(t, "f")] = "x"
		case 2:
			// Drop any one column, optional ones included: an absent
			// optional column would otherwise be written as its default.
			col := rapid.SampledFrom(tb.cols).Draw(t, "dropped")
			delete(row, col.Name)
			if rapid.Bool().Draw(t, "replaceWithUnknown") {
				row["zz_unknown"] = "x" // same key count, as in the first review's case
			}
		}
		_, err := b.Plan(opencdc.Record{Key: opencdc.StructuredData{"id": int64(1)}, Metadata: md, Payload: opencdc.Change{After: row}})
		ce, ok := conduiterr.Get(err)
		if !ok || ce.Code != connect.CodeRecordSchemaMismatch {
			t.Fatalf("want record_schema_mismatch, got %v", err)
		}
	})
}

func TestPlanErrors(t *testing.T) {
	tbDesc, err := MarshalDescriptor(&Descriptor{
		Key: &connect.Schema{Type: connect.TypeStruct, Name: "p.s.t.Key", Fields: []connect.Field{{Name: "id", Schema: &connect.Schema{Type: connect.TypeInt64}}}},
		Value: &connect.Schema{Type: connect.TypeStruct, Optional: true, Name: "p.s.t.Value", Fields: []connect.Field{
			{Name: "id", Schema: &connect.Schema{Type: connect.TypeInt64}},
			{Name: "note", Schema: &connect.Schema{Type: connect.TypeString, Optional: true, Default: "dflt", HasDefault: true}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ok := func() opencdc.Record {
		return opencdc.Record{
			Key:      opencdc.StructuredData{"id": int64(1)},
			Metadata: sourceMD(opencdc.Metadata{MetaTopic: "p.s.t", MetaDescriptor: string(tbDesc), MetaOp: "c"}),
			Payload:  opencdc.Change{After: opencdc.StructuredData{"id": int64(1), "note": nil}},
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*opencdc.Record)
		code   conduiterr.Code
	}{
		{"no descriptor", func(r *opencdc.Record) { delete(r.Metadata, MetaDescriptor) }, connect.CodeDescriptorMissing},
		{"descriptor version 2", func(r *opencdc.Record) {
			r.Metadata[MetaDescriptor] = `{"version":2,"key":null,"value":{"type":"struct","fields":[],"optional":true,"name":"a.Value"}}`
		}, connect.CodeDescriptorMissing},
		{"no topic", func(r *opencdc.Record) { delete(r.Metadata, MetaTopic) }, connect.CodeRecordSchemaMismatch},
		{"no op", func(r *opencdc.Record) { delete(r.Metadata, MetaOp) }, connect.CodeRecordSchemaMismatch},
		{"unknown op", func(r *opencdc.Record) { r.Metadata[MetaOp] = "x" }, connect.CodeRecordSchemaMismatch},
		{"truncate", func(r *opencdc.Record) { r.Metadata[MetaOp] = "t" }, connect.CodeUnsupportedType},
		{"missing required source field", func(r *opencdc.Record) { delete(r.Metadata, MetaSourcePfx+"db") }, connect.CodeRecordSchemaMismatch},
		{"non-numeric lsn", func(r *opencdc.Record) { r.Metadata[MetaSourcePfx+"lsn"] = "0/16B3748" }, connect.CodeRecordSchemaMismatch},
		{"create without after", func(r *opencdc.Record) { r.Payload.After = nil }, connect.CodeRecordSchemaMismatch},
		{"create with before", func(r *opencdc.Record) { r.Payload.Before = opencdc.StructuredData{"id": int64(1)} }, connect.CodeRecordSchemaMismatch},
		{"raw key", func(r *opencdc.Record) { r.Key = opencdc.RawData("1") }, connect.CodeRecordSchemaMismatch},
		{"float key", func(r *opencdc.Record) { r.Key = opencdc.StructuredData{"id": 1.0} }, connect.CodeRecordSchemaMismatch},
		{"tx id without orders", func(r *opencdc.Record) { r.Metadata[MetaTxID] = "1:2" }, connect.CodeRecordSchemaMismatch},
		{"old key not JSON", func(r *opencdc.Record) {
			r.Metadata[MetaOp] = "u"
			r.Metadata[MetaOldKey] = "{"
		}, connect.CodeRecordSchemaMismatch},
		// #3076 re-review: an absent column would be written as its default
		// ("dflt"), a value the row never had.
		{"row without an optional column", func(r *opencdc.Record) {
			r.Payload.After = opencdc.StructuredData{"id": int64(1)}
		}, connect.CodeRecordSchemaMismatch},
		// #3076 re-review: a lenient old-key parser would pick the last of two
		// ids and delete the wrong key.
		{"old key with a duplicate member", func(r *opencdc.Record) {
			r.Metadata[MetaOp] = "u"
			r.Metadata[MetaOldKey] = `{"id":2,"id":3}`
			r.Payload.Before = opencdc.StructuredData{"id": int64(2), "note": nil}
		}, connect.CodeRecordSchemaMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			r := ok()
			tc.mutate(&r)
			_, err := newTestBuilder(t, true).Plan(r)
			ce, found := conduiterr.Get(err)
			is.True(found)
			is.Equal(ce.Code, tc.code)
		})
	}
	t.Run("the valid record plans", func(t *testing.T) {
		is := is.New(t)
		plan, err := newTestBuilder(t, true).Plan(ok())
		is.NoErr(err)
		is.Equal(len(plan), 1)
	})
}

func TestParseCompat(t *testing.T) {
	is := is.New(t)
	for _, ok := range []string{"2.6", "3.5"} {
		c, err := ParseCompat(ok)
		is.NoErr(err)
		is.Equal(string(c), ok)
	}
	for _, bad := range []string{"", "3.0.5", "pre-2.6", "4.0"} {
		_, err := ParseCompat(bad)
		ce, found := conduiterr.Get(err)
		is.True(found)
		is.Equal(ce.Code, connect.CodeConfigInvalid)
	}
}

func FuzzParseDescriptor(f *testing.F) {
	f.Add([]byte(`{"version":1,"key":{"type":"struct","fields":[{"type":"int32","optional":false,"field":"id"}],"optional":false,"name":"a.b.c.Key"},"value":{"type":"struct","fields":[{"type":"int32","optional":false,"field":"id"}],"optional":true,"name":"a.b.c.Value"}}`))
	f.Add([]byte(`{"version":1,"key":null,"value":{"type":"struct","fields":[],"optional":true,"name":"a.Value"}}`))
	f.Add([]byte(`{"version":2}`))
	f.Fuzz(func(t *testing.T, in []byte) {
		d, err := ParseDescriptor(in)
		if err != nil {
			if _, ok := conduiterr.Get(err); !ok {
				t.Fatalf("error without a stable code: %v", err)
			}
			return
		}
		out, err := MarshalDescriptor(d)
		if err != nil {
			return
		}
		d2, err := ParseDescriptor(out)
		if err != nil {
			t.Fatalf("marshalled descriptor does not parse: %s: %v", out, err)
		}
		out2, err := MarshalDescriptor(d2)
		if err != nil || !bytes.Equal(out, out2) {
			t.Fatalf("descriptor not stable: %s vs %s (%v)", out, out2, err)
		}
	})
}

func TestEncodeRefusesValueWithoutSchema(t *testing.T) {
	is := is.New(t)
	_, err := Message{Topic: "t", Value: "x"}.Encode(Converters{Key: connect.NewJSONConverter(), Value: connect.NewJSONConverter()})
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, connect.CodeRecordSchemaMismatch)
}

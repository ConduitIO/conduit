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
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/debezium/connect"
	"github.com/goccy/go-json"
)

var goldenDir = filepath.Join("..", "..", "..", "..", "..", "..", "tests", "debezium", "testdata", "golden")

var goldenCompat = map[string]Compat{"2.7.4": Compat26, "3.7.0": Compat35}

type gBytes struct {
	Text   *string `json:"text"`
	Base64 *string `json:"base64"`
}

func (b *gBytes) raw(t *testing.T) []byte {
	t.Helper()
	if b == nil {
		return nil
	}
	if b.Text != nil {
		return []byte(*b.Text)
	}
	out, err := base64.StdEncoding.DecodeString(*b.Base64)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type gHeader struct {
	Key   string  `json:"key"`
	Value *gBytes `json:"value"`
}

type gEvent struct {
	Topic     string    `json:"topic"`
	Partition int32     `json:"partition"`
	Offset    int64     `json:"offset"`
	Key       *gBytes   `json:"key"`
	Value     *gBytes   `json:"value"`
	Headers   []gHeader `json:"headers"`
}

func (e gEvent) id() string { return fmt.Sprintf("%s[%d]@%d", e.Topic, e.Partition, e.Offset) }

func (e gEvent) header(t *testing.T, k string) (string, bool) {
	for _, h := range e.Headers {
		if h.Key == k {
			return string(h.Value.raw(t)), true
		}
	}
	return "", false
}

func readEvents(t *testing.T, version, converter string) []gEvent {
	t.Helper()
	f, err := os.Open(filepath.Join(goldenDir, version, converter, "events.jsonl"))
	if err != nil {
		t.Fatalf("golden files missing (tests/debezium/capture.sh): %v", err)
	}
	defer f.Close()
	var out []gEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var ev gEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	return out
}

// parsed is one schemas.enable=true message split into its parts.
type parsed struct {
	schema  *connect.Schema
	payload map[string]json.RawMessage
}

func parseMsg(t *testing.T, b []byte) parsed {
	t.Helper()
	var env struct {
		Schema  json.RawMessage `json:"schema"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatal(err)
	}
	s, err := connect.ParseSchemaJSON(env.Schema)
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatal(err)
	}
	return parsed{schema: s, payload: p}
}

func fieldSchema(t *testing.T, s *connect.Schema, name string) *connect.Schema {
	t.Helper()
	f, idx := s.Field(name)
	if idx < 0 {
		t.Fatalf("schema %s has no field %s", s.Name, name)
	}
	return f.Schema
}

// group is the golden messages one WAL change produced, and the record the
// source would have emitted for it.
type group struct {
	events []gEvent
	rec    opencdc.Record
	clock  []time.Time
	runID  string
}

// buildGroups reconstructs, from the golden messages, the records a source in
// Debezium mode emits: one per WAL change. A delete takes the tombstone that
// follows it; a primary-key change (a delete carrying __debezium.newkey) takes
// its tombstone and the create carrying __debezium.oldkey.
func buildGroups(t *testing.T, events []gEvent, format connect.DecimalFormat) []group {
	t.Helper()
	byPos := map[string]int{}
	for i, ev := range events {
		byPos[fmt.Sprintf("%s/%d/%d", ev.Topic, ev.Partition, ev.Offset)] = i
	}
	next := func(ev gEvent) gEvent {
		i, ok := byPos[fmt.Sprintf("%s/%d/%d", ev.Topic, ev.Partition, ev.Offset+1)]
		if !ok || events[i].Value != nil {
			t.Fatalf("%s: delete without a following tombstone", ev.id())
		}
		return events[i]
	}
	used := map[int]bool{}
	var groups []group
	for i, ev := range events {
		if used[i] || ev.Value == nil {
			continue
		}
		v := parseMsg(t, ev.Value.raw(t))
		var op string
		_ = json.Unmarshal(v.payload["op"], &op)
		g := group{events: []gEvent{ev}}
		g.runID, _ = ev.header(t, "__debezium.context.runId")
		switch op {
		case "m":
			g.rec = messageRecord(t, ev, v)
		case "c":
			if _, ok := ev.header(t, HeaderOldKey); ok {
				continue // part of a primary-key change, built from its delete
			}
			g.rec = rowRecord(t, ev, v, op, format)
		case "d":
			tomb := next(ev)
			used[byPos[fmt.Sprintf("%s/%d/%d", tomb.Topic, tomb.Partition, tomb.Offset)]] = true
			g.events = append(g.events, tomb)
			g.rec = rowRecord(t, ev, v, op, format)
			if _, ok := ev.header(t, HeaderNewKey); ok {
				cIdx := findCreate(t, events, ev, v, format)
				used[cIdx] = true
				create := events[cIdx]
				g.events = append(g.events, create)
				cv := parseMsg(t, create.Value.raw(t))
				crec := rowRecord(t, create, cv, "c", format)
				oldKey, _ := create.header(t, HeaderOldKey)
				// One record for the WAL change: op u, the new key and row,
				// the old row as before, the old key in metadata.
				g.rec.Metadata[MetaOp] = "u"
				g.rec.Metadata[MetaOldKey] = oldKey
				g.rec.Key = crec.Key
				g.rec.Payload.After = crec.Payload.After
				g.clock = append(g.clock, envelopeTime(t, cv))
			}
		default:
			g.rec = rowRecord(t, ev, v, op, format)
		}
		if op == "m" {
			var ms int64
			_ = json.Unmarshal(v.payload["ts_ms"], &ms)
			g.clock = append([]time.Time{time.UnixMilli(ms)}, g.clock...)
		} else {
			g.clock = append([]time.Time{envelopeTime(t, v)}, g.clock...)
		}
		groups = append(groups, g)
	}
	return groups
}

// findCreate finds the create event of a primary-key change: same topic and
// source LSN as the delete, and an oldkey header equal to the delete's key as
// the header converter writes it.
func findCreate(t *testing.T, events []gEvent, del gEvent, v parsed, format connect.DecimalFormat) int {
	t.Helper()
	k := parseMsg(t, del.Key.raw(t))
	goKey, err := connect.ParsePayloadJSON(k.schema, rawPayload(t, del.Key.raw(t)), format)
	if err != nil {
		t.Fatal(err)
	}
	cv, err := connect.FromGo(k.schema, goKey)
	if err != nil {
		t.Fatal(err)
	}
	delKey, err := connect.SimpleHeaderConverter{}.Marshal(k.schema, cv)
	if err != nil {
		t.Fatal(err)
	}
	lsn := sourceField(t, v, "lsn")
	for i, ev := range events {
		if ev.Topic != del.Topic || ev.Value == nil {
			continue
		}
		old, ok := ev.header(t, HeaderOldKey)
		if !ok || old != string(delKey) {
			continue
		}
		if sourceField(t, parseMsg(t, ev.Value.raw(t)), "lsn") == lsn {
			return i
		}
	}
	t.Fatalf("%s: no create for primary-key change", del.id())
	return -1
}

func sourceField(t *testing.T, v parsed, name string) string {
	t.Helper()
	var src map[string]json.RawMessage
	if err := json.Unmarshal(v.payload["source"], &src); err != nil {
		t.Fatal(err)
	}
	return string(src[name])
}

func rawPayload(t *testing.T, b []byte) []byte {
	t.Helper()
	var env struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatal(err)
	}
	return env.Payload
}

func envelopeTime(t *testing.T, v parsed) time.Time {
	t.Helper()
	var ns int64
	if raw, ok := v.payload["ts_ns"]; ok {
		_ = json.Unmarshal(raw, &ns)
		return time.Unix(0, ns)
	}
	var ms int64
	_ = json.Unmarshal(v.payload["ts_ms"], &ms)
	return time.UnixMilli(ms)
}

func sourceMetadata(t *testing.T, v parsed, md opencdc.Metadata) {
	t.Helper()
	var src map[string]any
	dec := json.NewDecoder(bytes.NewReader(v.payload["source"]))
	dec.UseNumber()
	if err := dec.Decode(&src); err != nil {
		t.Fatal(err)
	}
	for k, val := range src {
		switch x := val.(type) {
		case nil:
		case string:
			md[MetaSourcePfx+k] = x
		case json.Number:
			md[MetaSourcePfx+k] = x.String()
		default:
			t.Fatalf("source.%s: unexpected %T", k, val)
		}
	}
	if raw := v.payload["transaction"]; len(raw) > 0 && string(raw) != "null" {
		var tx struct {
			ID    string `json:"id"`
			Total int64  `json:"total_order"`
			Coll  int64  `json:"data_collection_order"`
		}
		if err := json.Unmarshal(raw, &tx); err != nil {
			t.Fatal(err)
		}
		md[MetaTxID] = tx.ID
		md[MetaTxTotal] = strconv.FormatInt(tx.Total, 10)
		md[MetaTxCollOrder] = strconv.FormatInt(tx.Coll, 10)
	}
}

func rowRecord(t *testing.T, ev gEvent, v parsed, op string, format connect.DecimalFormat) opencdc.Record {
	t.Helper()
	valueSchema := fieldSchema(t, v.schema, "after")
	desc := &Descriptor{Value: valueSchema}
	var key opencdc.Data
	if ev.Key != nil {
		k := parseMsg(t, ev.Key.raw(t))
		desc.Key = k.schema
		goKey, err := connect.ParsePayloadJSON(k.schema, rawPayload(t, ev.Key.raw(t)), format)
		if err != nil {
			t.Fatal(err)
		}
		key = opencdc.StructuredData(goKey.(map[string]any))
	}
	descText, err := MarshalDescriptor(desc)
	if err != nil {
		t.Fatal(err)
	}
	md := opencdc.Metadata{MetaTopic: ev.Topic, MetaOp: op, MetaDescriptor: string(descText)}
	sourceMetadata(t, v, md)
	row := func(name string) opencdc.Data {
		raw := v.payload[name]
		if len(raw) == 0 || string(raw) == "null" {
			return nil
		}
		gv, err := connect.ParsePayloadJSON(valueSchema, raw, format)
		if err != nil {
			t.Fatalf("%s %s: %v", ev.id(), name, err)
		}
		return opencdc.StructuredData(gv.(map[string]any))
	}
	return opencdc.Record{Key: key, Metadata: md, Payload: opencdc.Change{Before: row("before"), After: row("after")}}
}

func messageRecord(t *testing.T, ev gEvent, v parsed) opencdc.Record {
	t.Helper()
	md := opencdc.Metadata{MetaTopic: ev.Topic, MetaOp: "m"}
	sourceMetadata(t, v, md)
	var msg struct {
		Prefix  *string `json:"prefix"`
		Content []byte  `json:"content"`
	}
	if err := json.Unmarshal(v.payload["message"], &msg); err != nil {
		t.Fatal(err)
	}
	key := opencdc.StructuredData{"prefix": nil}
	if msg.Prefix != nil {
		key["prefix"] = *msg.Prefix
	}
	return opencdc.Record{Key: key, Metadata: md, Payload: opencdc.Change{After: opencdc.RawData(msg.Content)}}
}

// TestGoldenPlans rebuilds, for every WAL change in the goldens, the record a
// Debezium-mode source emits, runs it through Builder.Plan and the
// converters, and requires every message of the plan to equal what Debezium
// wrote: topic, key bytes, value bytes (timestamps included, the clock is fed
// the captured times), tombstones and headers. Only the runId header value is
// taken from the capture, because Debezium generates it per run.
func TestGoldenPlans(t *testing.T) {
	for version, compat := range goldenCompat {
		for _, tc := range []struct {
			converter string
			format    connect.DecimalFormat
		}{{"json", connect.DecimalBase64}, {"json-numeric", connect.DecimalNumeric}} {
			t.Run(version+"/"+tc.converter, func(t *testing.T) {
				events := readEvents(t, version, tc.converter)
				conv := connect.NewJSONConverter()
				conv.DecimalFormat = tc.format
				converters := Converters{Key: conv, Value: conv}
				groups := buildGroups(t, events, tc.format)
				covered := 0
				for _, g := range groups {
					clock := g.clock
					b, err := NewBuilder(Options{
						Compat:             compat,
						TombstonesOnDelete: true,
						ExtendedHeaders:    g.runID != "",
						RunID:              g.runID,
						Now: func() time.Time {
							now := clock[0]
							clock = clock[1:]
							return now
						},
					})
					if err != nil {
						t.Fatal(err)
					}
					plan, err := b.Plan(g.rec)
					if err != nil {
						t.Fatalf("%s: Plan: %v", g.events[0].id(), err)
					}
					if len(plan) != len(g.events) {
						t.Fatalf("%s: plan has %d messages, Debezium wrote %d", g.events[0].id(), len(plan), len(g.events))
					}
					for i, m := range plan {
						want := g.events[i]
						got, err := m.Encode(converters)
						if err != nil {
							t.Fatalf("%s: Encode: %v", want.id(), err)
						}
						compareMessage(t, want, got)
						covered++
					}
				}
				if covered != len(events) {
					t.Fatalf("covered %d of %d golden messages", covered, len(events))
				}
			})
		}
	}
}

func compareMessage(t *testing.T, want gEvent, got EncodedMessage) {
	t.Helper()
	if got.Topic != want.Topic {
		t.Fatalf("%s: topic %q", want.id(), got.Topic)
	}
	if !bytes.Equal(got.Key, want.Key.raw(t)) || (got.Key == nil) != (want.Key == nil) {
		t.Fatalf("%s: key differs\n got: %s\nwant: %s", want.id(), got.Key, want.Key.raw(t))
	}
	if (got.Value == nil) != (want.Value == nil) {
		t.Fatalf("%s: tombstone mismatch (got nil=%v)", want.id(), got.Value == nil)
	}
	if !bytes.Equal(got.Value, want.Value.raw(t)) {
		t.Fatalf("%s: value differs\n%s", want.id(), diffAt(got.Value, want.Value.raw(t)))
	}
	if len(got.Headers) != len(want.Headers) {
		t.Fatalf("%s: %d headers, want %d", want.id(), len(got.Headers), len(want.Headers))
	}
	for i, h := range want.Headers {
		if got.Headers[i].Key != h.Key || !bytes.Equal(got.Headers[i].Value, h.Value.raw(t)) {
			t.Fatalf("%s: header %d is %s=%s, want %s=%s", want.id(), i, got.Headers[i].Key, got.Headers[i].Value, h.Key, h.Value.raw(t))
		}
	}
}

func diffAt(a, b []byte) string {
	for i := 0; i < min(len(a), len(b)); i++ {
		if a[i] != b[i] {
			lo := max(0, i-80)
			return fmt.Sprintf("at byte %d:\n got: ...%s\nwant: ...%s", i, a[lo:min(len(a), i+80)], b[lo:min(len(b), i+80)])
		}
	}
	return fmt.Sprintf("length %d vs %d", len(a), len(b))
}

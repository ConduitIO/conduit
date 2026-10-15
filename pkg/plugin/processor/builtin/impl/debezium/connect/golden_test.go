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
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-json"
	"github.com/matryer/is"
)

// goldenDir is tests/debezium/testdata/golden, captured from real Debezium
// by tests/debezium/capture.sh.
var goldenDir = filepath.Join("..", "..", "..", "..", "..", "..", "..", "tests", "debezium", "testdata", "golden")

type goldenBytes struct {
	Text   *string `json:"text"`
	Base64 *string `json:"base64"`
}

func (b *goldenBytes) raw(t *testing.T) []byte {
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

type goldenEvent struct {
	Topic     string       `json:"topic"`
	Partition int32        `json:"partition"`
	Offset    int64        `json:"offset"`
	Key       *goldenBytes `json:"key"`
	Value     *goldenBytes `json:"value"`
}

func (e goldenEvent) id() string { return fmt.Sprintf("%s[%d]@%d", e.Topic, e.Partition, e.Offset) }

func readGolden(t *testing.T, version, converter string) []goldenEvent {
	t.Helper()
	f, err := os.Open(filepath.Join(goldenDir, version, converter, "events.jsonl"))
	if err != nil {
		t.Fatalf("golden files missing (run tests/debezium/capture.sh): %v", err)
	}
	defer f.Close()
	var out []goldenEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var ev goldenEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatalf("no golden events for %s/%s", version, converter)
	}
	return out
}

func goldenVersions(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(goldenDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	if len(out) == 0 {
		t.Fatal("no golden versions")
	}
	return out
}

// splitEnvelope returns the raw "schema" and "payload" members of a
// schemas.enable=true message.
func splitEnvelope(t *testing.T, b []byte) (json.RawMessage, json.RawMessage) {
	t.Helper()
	var env struct {
		Schema  json.RawMessage `json:"schema"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("not a JSON envelope: %v", err)
	}
	return env.Schema, env.Payload
}

// TestGoldenJSONConverter re-encodes every key and value Debezium wrote with
// the JsonConverter and requires the exact same bytes: the schema is parsed
// from the message, the payload is turned into the Go values a source would
// supply, and FromGo + Marshal must reproduce the original.
func TestGoldenJSONConverter(t *testing.T) {
	for _, version := range goldenVersions(t) {
		for _, tc := range []struct {
			converter string
			format    DecimalFormat
		}{
			{"json", DecimalBase64},
			{"json-numeric", DecimalNumeric},
			{"json-txmeta", DecimalBase64},
		} {
			t.Run(version+"/"+tc.converter, func(t *testing.T) {
				if _, err := os.Stat(filepath.Join(goldenDir, version, tc.converter)); err != nil {
					t.Skip("not captured for this version")
				}
				conv := NewJSONConverter()
				conv.DecimalFormat = tc.format
				for _, ev := range readGolden(t, version, tc.converter) {
					for part, b := range map[string]*goldenBytes{"key": ev.Key, "value": ev.Value} {
						if b == nil {
							continue // tombstone or null key
						}
						want := b.raw(t)
						schemaRaw, payloadRaw := splitEnvelope(t, want)
						s, err := ParseSchemaJSON(schemaRaw)
						if err != nil {
							t.Fatalf("%s %s: parse schema: %v", ev.id(), part, err)
						}
						goVal, err := ParsePayloadJSON(s, payloadRaw, tc.format)
						if err != nil {
							t.Fatalf("%s %s: payload: %v", ev.id(), part, err)
						}
						cv, err := FromGo(s, goVal)
						if err != nil {
							t.Fatalf("%s %s: FromGo: %v", ev.id(), part, err)
						}
						got, err := conv.Marshal(s, cv)
						if err != nil {
							t.Fatalf("%s %s: Marshal: %v", ev.id(), part, err)
						}
						if !bytes.Equal(got, want) {
							t.Fatalf("%s %s: bytes differ\n%s", ev.id(), part, firstDiff(got, want))
						}
					}
				}
			})
		}
	}
}

// TestGoldenJSONConverterNoSchema checks schemas.enable=false output. Those
// messages carry no schema, so each takes its schema from the matching
// message of the schemas.enable=true capture. Matching is per topic and key,
// in offset order: the key bytes differ between the two converters, so a key
// can land on a different partition, but the events of one key keep their
// order in both captures.
func TestGoldenJSONConverterNoSchema(t *testing.T) {
	for _, version := range goldenVersions(t) {
		t.Run(version, func(t *testing.T) {
			is := is.New(t)
			withSchema := readGolden(t, version, "json")
			noSchema := readGolden(t, version, "json-noschema")
			is.Equal(len(withSchema), len(noSchema))

			queues := map[string][]goldenEvent{}
			for _, ev := range withSchema {
				k := ev.Topic + "\x00"
				if ev.Key != nil {
					_, payload := splitEnvelope(t, ev.Key.raw(t))
					k += string(payload)
				}
				queues[k] = append(queues[k], ev)
			}
			conv := NewJSONConverter()
			conv.SchemasEnable = false
			for _, ev := range noSchema {
				k := ev.Topic + "\x00"
				if ev.Key != nil {
					k += string(ev.Key.raw(t))
				}
				if len(queues[k]) == 0 {
					t.Fatalf("%s: no matching event in the json capture", ev.id())
				}
				ref := queues[k][0]
				queues[k] = queues[k][1:]
				for _, pair := range []struct {
					part     string
					got, ref *goldenBytes
				}{{"key", ev.Key, ref.Key}, {"value", ev.Value, ref.Value}} {
					if pair.got == nil || pair.ref == nil {
						if (pair.got == nil) != (pair.ref == nil) {
							t.Fatalf("%s %s: null in one capture only", ev.id(), pair.part)
						}
						continue
					}
					schemaRaw, _ := splitEnvelope(t, pair.ref.raw(t))
					s, err := ParseSchemaJSON(schemaRaw)
					is.NoErr(err)
					want := pair.got.raw(t)
					goVal, err := ParsePayloadJSON(s, want, DecimalBase64)
					if err != nil {
						t.Fatalf("%s %s: payload: %v", ev.id(), pair.part, err)
					}
					cv, err := FromGo(s, goVal)
					if err != nil {
						t.Fatalf("%s %s: FromGo: %v", ev.id(), pair.part, err)
					}
					got, err := conv.Marshal(s, cv)
					is.NoErr(err)
					if !bytes.Equal(got, want) {
						t.Fatalf("%s %s: bytes differ\n%s", ev.id(), pair.part, firstDiff(got, want))
					}
				}
			}
		})
	}
}

func firstDiff(a, b []byte) string {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			lo := max(0, i-60)
			return fmt.Sprintf("first difference at byte %d:\n got: ...%s\nwant: ...%s", i, a[lo:min(len(a), i+60)], b[lo:min(len(b), i+60)])
		}
	}
	return fmt.Sprintf("one is a prefix of the other (len %d vs %d)", len(a), len(b))
}

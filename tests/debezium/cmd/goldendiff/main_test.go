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

package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/goccy/go-json"
)

var golden = filepath.Join("..", "..", "testdata", "golden")

// copyCapture copies one committed capture into a temp golden root and
// applies edit to every events.jsonl line.
func copyCapture(t *testing.T, version, converter string, edit func(i int, ev map[string]any)) string {
	t.Helper()
	root := t.TempDir()
	dst := filepath.Join(root, version, converter)
	if err := os.MkdirAll(dst, 0o750); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(golden, version, converter)
	if b, err := os.ReadFile(filepath.Join(src, "schemas.json")); err == nil {
		if err := os.WriteFile(filepath.Join(dst, "schemas.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(filepath.Join(src, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for i, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		var ev map[string]any
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatal(err)
		}
		if edit != nil {
			edit(i, ev)
		}
		enc, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(enc)
		out.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dst, "events.jsonl"), out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func committedRoot(t *testing.T, version, converter string) string {
	return copyCapture(t, version, converter, nil)
}

func editValueText(i int, f func(string) string) func(int, map[string]any) {
	return func(j int, ev map[string]any) {
		if j != i {
			return
		}
		v, _ := ev["value"].(map[string]any)
		v["text"] = f(v["text"].(string))
	}
}

func TestGoldenDiff(t *testing.T) {
	lsn := regexp.MustCompile(`"lsn":[0-9]+`)
	for _, tc := range []struct {
		name      string
		converter string
		edit      func(int, map[string]any)
		wantDiff  bool
	}{
		{"identical", "json", nil, false},
		{"lsn and ts shift (host-dependent)", "json", func(_ int, ev map[string]any) {
			if v, ok := ev["value"].(map[string]any); ok && v["text"] != nil {
				s := lsn.ReplaceAllString(v["text"].(string), `"lsn":1`)
				s = regexp.MustCompile(`"ts_ms":[0-9]+`).ReplaceAllString(s, `"ts_ms":2`)
				v["text"] = s
			}
		}, false},
		{"one payload digit", "json", editValueText(0, func(s string) string {
			return strings.Replace(s, `"seq":1`, `"seq":7`, 1)
		}), true},
		{"key order in the schema", "json", editValueText(0, func(s string) string {
			return strings.Replace(s, `{"type":"int32","optional":false,"field":"seq"}`, `{"optional":false,"type":"int32","field":"seq"}`, 1)
		}), true},
		{"escaping", "json", editValueText(0, func(s string) string {
			return strings.Replace(s, `"golden"`, `"gol`+"\\"+`u0064en"`, 1) // same text, escaped differently
		}), true},
		{"whitespace", "json", editValueText(0, func(s string) string {
			return strings.Replace(s, `{"schema":`, `{"schema": `, 1)
		}), true},
		{"tombstone turned into a value", "json", func(i int, ev map[string]any) {
			if ev["value"] == nil && ev["partition"] != nil {
				ev["value"] = map[string]any{"text": "{}"}
			}
		}, true},
		{"header value", "json", func(_ int, ev map[string]any) {
			hs, _ := ev["headers"].([]any)
			for _, h := range hs {
				hm := h.(map[string]any)
				if hm["key"] == "__debezium.newkey" {
					hm["value"] = map[string]any{"text": `{"seq":3,"region":"us"}`}
				}
			}
		}, true},
		{"runId header", "json", func(_ int, ev map[string]any) {
			hs, _ := ev["headers"].([]any)
			for _, h := range hs {
				hm := h.(map[string]any)
				if hm["key"] == "__debezium.context.runId" {
					hm["value"] = map[string]any{"text": "other"}
				}
			}
		}, false},
		{"avro identical", "avro", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := committedRoot(t, "3.7.0", tc.converter)
			b := copyCapture(t, "3.7.0", tc.converter, tc.edit)
			diffs, err := compareRoots(a, b)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(diffs) > 0; got != tc.wantDiff {
				t.Fatalf("diff = %v, want %v: %v", got, tc.wantDiff, diffs)
			}
		})
	}
}

// Avro values are decoded and compared: a changed byte in a non-volatile
// field is a difference, a changed LSN is not.
func TestGoldenDiffAvro(t *testing.T) {
	reg, err := loadRegistry(filepath.Join(golden, "3.7.0", "avro", "schemas.json"))
	if err != nil {
		t.Fatal(err)
	}
	evs, err := readEvents(filepath.Join(golden, "3.7.0", "avro", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	decoded := 0
	for _, ev := range evs {
		for _, b := range []*blob{ev.Key, ev.Value} {
			raw, err := b.raw()
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) < 5 {
				continue
			}
			if _, err := reg.decode(raw); err != nil {
				t.Fatalf("%s: %v", ev.id(), err)
			}
			decoded++
		}
	}
	if decoded < len(evs) {
		t.Fatalf("decoded only %d messages of %d events", decoded, len(evs))
	}

	// A row value: flip the last byte (a ts_ns field, volatile) and an
	// early payload byte (not volatile).
	var msg []byte
	for _, ev := range evs {
		if ev.Topic == "dbz.golden.customers" && ev.Value != nil {
			msg, _ = ev.Value.raw()
			break
		}
	}
	if binary.BigEndian.Uint32(msg[1:5]) == 0 {
		t.Fatal("no schema ID")
	}
	base, _ := reg.decode(msg)
	tampered := append([]byte{}, msg...)
	tampered[6] ^= 0x02 // inside "before"/"after": a row field
	if got, err := reg.decode(tampered); err == nil && bytes.Equal(got, base) {
		t.Fatal("a changed row byte decoded to the same value")
	}
}

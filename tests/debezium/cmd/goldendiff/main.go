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

// Command goldendiff checks that a fresh capture reproduces the committed
// golden files. Two captures of the same Debezium version differ in the
// wall-clock fields, the per-run context header and, across hosts, the
// absolute WAL positions (source.lsn, source.sequence) and transaction IDs,
// which depend on what initdb and the image wrote before the workload ran.
// Those are blanked before comparing; every other byte must match. On one
// host, two fresh stacks also agree on LSNs and transaction IDs.
//
//	goldendiff <committed-dir> <fresh-dir>
//
// Both directories are testdata/golden roots (<version>/<converter>/...).
// Avro values carry timestamps inside binary records, so for Avro only the
// event layout (topic, partition, offset, key bytes, headers) and the
// registry schemas are compared.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/goccy/go-json"
)

var (
	volatileTop    = []string{"ts_ms", "ts_us", "ts_ns"}
	volatileSource = []string{"ts_ms", "ts_us", "ts_ns", "lsn", "sequence", "txId", "xmin"}
)

type event struct {
	Topic     string          `json:"topic"`
	Partition int32           `json:"partition"`
	Offset    int64           `json:"offset"`
	Key       json.RawMessage `json:"key"`
	Value     *struct {
		Text   *string `json:"text"`
		Base64 *string `json:"base64"`
	} `json:"value"`
	Headers []struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	} `json:"headers"`
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: goldendiff <committed-dir> <fresh-dir>")
		os.Exit(2)
	}
	diffs, err := compareRoots(os.Args[1], os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "goldendiff:", err)
		os.Exit(2)
	}
	for _, d := range diffs {
		fmt.Println(d)
	}
	if len(diffs) > 0 {
		os.Exit(1)
	}
	fmt.Println("goldens reproduced")
}

func compareRoots(a, b string) ([]string, error) {
	var diffs []string
	matches, err := filepath.Glob(filepath.Join(a, "*", "*", "events.jsonl"))
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, cerrors.Errorf("no golden files under %s", a)
	}
	for _, m := range matches {
		rel, _ := filepath.Rel(a, filepath.Dir(m))
		d, err := compareDir(filepath.Join(a, rel), filepath.Join(b, rel), strings.HasSuffix(rel, "avro"))
		if err != nil {
			return nil, cerrors.Errorf("%s: %w", rel, err)
		}
		for _, x := range d {
			diffs = append(diffs, rel+": "+x)
		}
	}
	return diffs, nil
}

func compareDir(a, b string, avro bool) ([]string, error) {
	ea, err := readEvents(filepath.Join(a, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	eb, err := readEvents(filepath.Join(b, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	var diffs []string
	if len(ea) != len(eb) {
		diffs = append(diffs, fmt.Sprintf("event count %d != %d", len(ea), len(eb)))
	}
	for i := 0; i < min(len(ea), len(eb)); i++ {
		na, err := normalize(ea[i], avro)
		if err != nil {
			return nil, err
		}
		nb, err := normalize(eb[i], avro)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(na, nb) {
			diffs = append(diffs, fmt.Sprintf("event %d (%s[%d]@%d) differs:\n  committed: %s\n  fresh:     %s", i, ea[i].Topic, ea[i].Partition, ea[i].Offset, na, nb))
		}
	}
	if avro {
		sa, err := os.ReadFile(filepath.Join(a, "schemas.json")) //nolint:gosec // paths are the operator's own command-line arguments
		if err != nil {
			return nil, err
		}
		sb, err := os.ReadFile(filepath.Join(b, "schemas.json")) //nolint:gosec // paths are the operator's own command-line arguments
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(sa, sb) {
			diffs = append(diffs, "schemas.json differs")
		}
	}
	return diffs, nil
}

func readEvents(path string) ([]event, error) {
	f, err := os.Open(path) //nolint:gosec // paths are the operator's own command-line arguments
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var ev event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, sc.Err()
}

// normalize renders an event with its volatile fields blanked: the envelope's
// ts_ms/ts_us/ts_ns, the source block's timestamps, WAL positions and
// transaction IDs, the runId context header, and the
// partition of a null-key message, which the producer's sticky partitioner
// picks at random (design D7).
func normalize(ev event, avro bool) ([]byte, error) {
	if len(ev.Key) == 0 || string(ev.Key) == "null" {
		ev.Partition = -1
	}
	for i := range ev.Headers {
		if ev.Headers[i].Key == "__debezium.context.runId" {
			ev.Headers[i].Value = json.RawMessage(`"<volatile>"`)
		}
	}
	if ev.Value != nil && ev.Value.Text != nil && !avro {
		var v map[string]any
		dec := json.NewDecoder(strings.NewReader(*ev.Value.Text))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		payload := v
		if p, ok := v["payload"].(map[string]any); ok {
			payload = p
		}
		blank(payload, volatileTop)
		if src, ok := payload["source"].(map[string]any); ok {
			blank(src, volatileSource)
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		s := string(b)
		ev.Value.Text = &s
	}
	if avro {
		ev.Value = nil
	}
	return json.Marshal(ev)
}

func blank(m map[string]any, keys []string) {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			m[k] = "<volatile>"
		}
	}
}

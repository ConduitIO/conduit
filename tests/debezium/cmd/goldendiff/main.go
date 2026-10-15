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
// golden files.
//
//	goldendiff <committed-dir> <fresh-dir>
//
// Both directories are testdata/golden roots (<version>/<converter>/...).
//
// JSON-converter messages are compared as bytes. Before comparing, the
// volatile values are masked in the original text with regular expressions
// (see volatileJSON); nothing is decoded and re-encoded, so a change in key
// order, escaping, number formatting or whitespace is a difference.
//
// Avro messages are compared by schema ID (the wire header, byte for byte)
// and by decoded value with the volatile fields blanked (see avro.go); the
// registry state (schemas.json) is compared byte for byte.
//
// What may differ between two captures, and why:
//   - ts_ms, ts_us, ts_ns (envelope and source): wall clock.
//   - lsn, sequence, txId, and the transaction id "<txId>:<lsn>": absolute
//     WAL positions and transaction IDs depend on what initdb and the image
//     wrote before the workload, which differs across hosts.
//   - the __debezium.context.runId header: generated per connector start.
//   - the partition of a null-key message: the sticky partitioner picks it.
//
// Every other byte must match.
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/goccy/go-json"
)

// volatileJSON masks volatile values in JsonConverter output. Each pattern
// matches a payload member only ("ts_ms":<digits>), never the schema's
// "field":"ts_ms".
var volatileJSON = []*regexp.Regexp{
	regexp.MustCompile(`"(ts_ms|ts_us|ts_ns|lsn|txId)":-?[0-9]+`),
	regexp.MustCompile(`"sequence":"\[[^\]]*\]"`),
	regexp.MustCompile(`"id":"[0-9]+:[0-9]+"`),
}

func maskJSON(b []byte) []byte {
	for _, re := range volatileJSON {
		b = re.ReplaceAllFunc(b, func(m []byte) []byte {
			name, _, _ := bytes.Cut(m, []byte(":"))
			return append(append([]byte{}, name...), []byte(`:"<volatile>"`)...)
		})
	}
	return b
}

type blob struct {
	Text   *string `json:"text"`
	Base64 *string `json:"base64"`
}

func (b *blob) raw() ([]byte, error) {
	if b == nil {
		return nil, nil
	}
	if b.Text != nil {
		return []byte(*b.Text), nil
	}
	if b.Base64 != nil {
		return base64.StdEncoding.DecodeString(*b.Base64)
	}
	return []byte{}, nil
}

type event struct {
	Topic     string `json:"topic"`
	Partition int32  `json:"partition"`
	Offset    int64  `json:"offset"`
	Key       *blob  `json:"key"`
	Value     *blob  `json:"value"`
	Headers   []struct {
		Key   string `json:"key"`
		Value *blob  `json:"value"`
	} `json:"headers"`
}

func (e event) id() string { return fmt.Sprintf("%s[%d]@%d", e.Topic, e.Partition, e.Offset) }

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
	var regA, regB *registry
	if avro {
		if regA, err = loadRegistry(filepath.Join(a, "schemas.json")); err != nil {
			return nil, err
		}
		if regB, err = loadRegistry(filepath.Join(b, "schemas.json")); err != nil {
			return nil, err
		}
		sa, _ := os.ReadFile(filepath.Join(a, "schemas.json")) //nolint:gosec // operator-supplied paths
		sb, _ := os.ReadFile(filepath.Join(b, "schemas.json")) //nolint:gosec // operator-supplied paths
		if !bytes.Equal(sa, sb) {
			diffs = append(diffs, "schemas.json differs")
		}
	}
	for i := 0; i < min(len(ea), len(eb)); i++ {
		if d := compareEvent(ea[i], eb[i], regA, regB); d != "" {
			diffs = append(diffs, fmt.Sprintf("event %d (%s): %s", i, ea[i].id(), d))
		}
	}
	return diffs, nil
}

// compareEvent returns "" when the two events match, otherwise a description
// of the first difference. regA and regB are nil for JSON captures.
func compareEvent(x, y event, regA, regB *registry) string {
	if x.Topic != y.Topic || x.Offset != y.Offset {
		return fmt.Sprintf("position %s vs %s", x.id(), y.id())
	}
	if x.Key != nil && x.Partition != y.Partition {
		return fmt.Sprintf("partition %d vs %d", x.Partition, y.Partition)
	}
	if d := compareBlob("key", x.Key, y.Key, regA, regB); d != "" {
		return d
	}
	if d := compareBlob("value", x.Value, y.Value, regA, regB); d != "" {
		return d
	}
	if len(x.Headers) != len(y.Headers) {
		return fmt.Sprintf("%d headers vs %d", len(x.Headers), len(y.Headers))
	}
	for i := range x.Headers {
		hx, hy := x.Headers[i], y.Headers[i]
		if hx.Key != hy.Key {
			return fmt.Sprintf("header %d key %q vs %q", i, hx.Key, hy.Key)
		}
		if hx.Key == "__debezium.context.runId" {
			continue
		}
		if d := compareBlob("header "+hx.Key, hx.Value, hy.Value, nil, nil); d != "" {
			return d
		}
	}
	return ""
}

func compareBlob(what string, x, y *blob, regA, regB *registry) string {
	if (x == nil) != (y == nil) {
		return fmt.Sprintf("%s: null in one capture only", what)
	}
	bx, err := x.raw()
	if err != nil {
		return fmt.Sprintf("%s: %v", what, err)
	}
	by, err := y.raw()
	if err != nil {
		return fmt.Sprintf("%s: %v", what, err)
	}
	if regA != nil && len(bx) >= 5 && bx[0] == 0 {
		return compareAvro(what, bx, by, regA, regB)
	}
	mx, my := maskJSON(bx), maskJSON(by)
	if !bytes.Equal(mx, my) {
		return fmt.Sprintf("%s differs\n  committed: %s\n  fresh:     %s", what, excerpt(mx, my), excerpt(my, mx))
	}
	return ""
}

func compareAvro(what string, bx, by []byte, regA, regB *registry) string {
	if len(by) < 5 || !bytes.Equal(bx[:5], by[:5]) {
		return fmt.Sprintf("%s: Avro wire header (schema ID) differs", what)
	}
	vx, err := regA.decode(bx)
	if err != nil {
		return fmt.Sprintf("%s: committed: %v", what, err)
	}
	vy, err := regB.decode(by)
	if err != nil {
		return fmt.Sprintf("%s: fresh: %v", what, err)
	}
	if !bytes.Equal(vx, vy) {
		return fmt.Sprintf("%s differs (decoded, volatile fields blanked)\n  committed: %s\n  fresh:     %s", what, excerpt(vx, vy), excerpt(vy, vx))
	}
	return ""
}

// excerpt returns a window of a around its first difference with b.
func excerpt(a, b []byte) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	lo, hi := max(0, i-80), min(len(a), i+80)
	return "..." + string(a[lo:hi]) + "..."
}

func readEvents(path string) ([]event, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied paths
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

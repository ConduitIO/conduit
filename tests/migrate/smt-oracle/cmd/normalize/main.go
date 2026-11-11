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

// Command normalize turns the JSON lines Conduit's file destination wrote
// (one OpenCDC record each) into fixtures/conduit-postgres.json: a JSON array
// grouped by collection, in arrival order within a collection.
//
// Only what differs on every capture is rewritten: the position (it embeds
// the WAL position and the system identifier) becomes the collection and the
// record's index, and metadata that carries a wall-clock time is removed.
// The key, the payload and every other metadata key are exactly what the
// source produced.
//
// Usage: normalize <in.jsonl> <out.json>
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"sort"

	"github.com/goccy/go-json"
)

// volatileMetadata are the metadata keys that hold wall-clock time.
var volatileMetadata = []string{"opencdc.readAt", "opencdc.createdAt"}

const collectionKey = "opencdc.collection"

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: normalize <in.jsonl> <out.json>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "normalize:", err)
		os.Exit(1)
	}
}

func run(in, out string) error {
	f, err := os.Open(in) //nolint:gosec // both paths are arguments from capture.sh, not input from elsewhere
	if err != nil {
		return err
	}
	defer f.Close()

	var recs []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		dec.UseNumber()
		var r map[string]any
		if err := dec.Decode(&r); err != nil {
			return fmt.Errorf("decode record %d: %w", len(recs), err)
		}
		recs = append(recs, r)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	sort.SliceStable(recs, func(i, j int) bool { return collection(recs[i]) < collection(recs[j]) })

	next := map[string]int{}
	for _, r := range recs {
		c := collection(r)
		r["position"] = base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s#%d", c, next[c])))
		next[c]++
		if md, ok := r["metadata"].(map[string]any); ok {
			for _, k := range volatileMetadata {
				delete(md, k)
			}
		}
	}
	b, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(out, append(b, '\n'), 0o600) //nolint:gosec // see above
}

func collection(r map[string]any) string {
	md, _ := r["metadata"].(map[string]any)
	c, _ := md[collectionKey].(string)
	return c
}

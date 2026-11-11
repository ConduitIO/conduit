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

package smtoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/goccy/go-json"
)

func TestGoldensLoadAndMatchCases(t *testing.T) {
	names, err := Names()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no goldens")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			g, err := Load(name)
			if err != nil {
				t.Fatal(err)
			}
			c, err := LoadCases(name)
			if err != nil {
				t.Fatal(err)
			}
			if len(g.Runs) != len(c.Runs) {
				t.Fatalf("golden has %d runs, cases has %d; re-capture", len(g.Runs), len(c.Runs))
			}
			for i, run := range g.Runs {
				if run.Run != c.Runs[i].Run {
					t.Fatalf("run %d: golden %q, cases %q; re-capture", i, run.Run, c.Runs[i].Run)
				}
				recs, err := c.RecordsFor(run.Run)
				if err != nil {
					t.Fatal(err)
				}
				checkRun(t, g, run, recs)
			}
		})
	}
}

// checkRun checks that a run has one well-formed result per input record, in order.
func checkRun(t *testing.T, g *Suite, run Run, recs []InputRecord) {
	t.Helper()
	if len(run.Results) != len(recs) {
		t.Fatalf("run %q: %d results for %d records; re-capture", run.Run, len(run.Results), len(recs))
	}
	for j, res := range run.Results {
		if res.Record != recs[j].Name {
			t.Fatalf("run %q result %d: %q, want %q", run.Run, j, res.Record, recs[j].Name)
		}
		checkResult(t, g, run.Run, res)
	}
}

func checkResult(t *testing.T, g *Suite, run string, res Result) {
	t.Helper()
	switch res.Result {
	case ResultRecord:
		if res.Output == nil {
			t.Fatalf("%s/%s: record result without output", run, res.Record)
		}
		for _, side := range []Side{res.Output.Key, res.Output.Value} {
			if _, err := g.Schema(side); err != nil {
				t.Fatal(err)
			}
		}
	case ResultDropped:
	case ResultError:
		if res.Error == nil || res.Error.Class == "" {
			t.Fatalf("%s/%s: error result without error", run, res.Record)
		}
	default:
		t.Fatalf("%s/%s: unknown result %q", run, res.Record, res.Result)
	}
}

func TestFind(t *testing.T) {
	g, err := Load("filter-predicates")
	if err != nil {
		t.Fatal(err)
	}
	res, ok := g.Find("is-tombstone", "tombstone")
	if !ok || res.Result != ResultDropped {
		t.Fatalf("Filter(RecordIsTombstone) over a tombstone: %+v, %v", res, ok)
	}
	res, ok = g.Find("is-tombstone", "orders-plain")
	if !ok || res.Result != ResultRecord || res.Output.Topic != "orders" {
		t.Fatalf("Filter(RecordIsTombstone) over a plain record: %+v, %v", res, ok)
	}
	if _, ok := g.Find("is-tombstone", "no-such-record"); ok {
		t.Fatal("found a record that does not exist")
	}
}

func TestDiff(t *testing.T) {
	cases := []struct {
		name, want, got, diff string
	}{
		{"equal ignoring order", `{"a":1,"b":[1,2]}`, `{ "b": [1, 2], "a": 1 }`, ""},
		{"number type matters", `{"a":1}`, `{"a":1.0}`, "$.a: want 1, got 1.0"},
		{"missing key", `{"a":1,"b":2}`, `{"a":1}`, "$.b: missing key"},
		{"extra key", `{"a":1}`, `{"a":1,"b":2}`, "$.b: unexpected key"},
		{"array length", `[1,2]`, `[1]`, "$: want 2 elements, got 1"},
		{"null vs missing", `{"a":null}`, `{}`, "$.a: missing key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Diff(json.RawMessage(tc.want), json.RawMessage(tc.got)); got != tc.diff {
				t.Fatalf("Diff = %q, want %q", got, tc.diff)
			}
		})
	}
}

func TestPairsCoverEveryChange(t *testing.T) {
	pairs, err := Pairs()
	if err != nil {
		t.Fatal(err)
	}
	recs, err := ConduitRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != len(recs) {
		t.Fatalf("%d pairs for %d Conduit records", len(pairs), len(recs))
	}
	seen := map[int]bool{}
	tombstones := 0
	for _, p := range pairs {
		if seen[p.Conduit] {
			t.Fatalf("Conduit record %d is paired twice", p.Conduit)
		}
		seen[p.Conduit] = true
		if p.Tombstone != "" {
			tombstones++
			if p.Op != "delete" {
				t.Fatalf("%s: a tombstone follows a %s", p.Debezium, p.Op)
			}
		}
	}
	if tombstones == 0 {
		t.Fatal("the fixture has no delete followed by a tombstone")
	}
}

func TestSchemasAreRegisteredInOrder(t *testing.T) {
	ss, err := Schemas()
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) == 0 {
		t.Fatal("no schemas: the capture's records with attached schemas need them")
	}
	next := map[string]int{}
	for _, s := range ss {
		next[s.Subject]++
		if s.Version != next[s.Subject] {
			t.Fatalf("%s: version %d listed at position %d", s.Subject, s.Version, next[s.Subject])
		}
		if s.Schema == "" || s.Type == "" {
			t.Fatalf("%s v%d has no schema text or type", s.Subject, s.Version)
		}
	}
}

func TestFixtureRecordsReferenceKnownSchemas(t *testing.T) {
	ss, err := Schemas()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, s := range ss {
		have[fmt.Sprintf("%s/%d", s.Subject, s.Version)] = true
	}
	recs, err := ConduitRecords()
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range recs {
		for _, side := range []string{"key", "payload"} {
			sub, ok := r.Metadata["opencdc."+side+".schema.subject"]
			if !ok {
				continue
			}
			if !have[sub+"/"+r.Metadata["opencdc."+side+".schema.version"]] {
				t.Fatalf("record %d references %s schema %s v%s, which the capture did not register", i, side, sub, r.Metadata["opencdc."+side+".schema.version"])
			}
		}
	}
}

// TestGoldensMatchCaseDefinitions fails when a case file or a fixture has been edited since the
// golden was captured: the golden would then describe a different chain or different inputs.
func TestGoldensMatchCaseDefinitions(t *testing.T) {
	names, err := Names()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			g, err := Load(name)
			if err != nil {
				t.Fatal(err)
			}
			cb, err := CaseBytes(name)
			if err != nil {
				t.Fatal(err)
			}
			if got := hexSum(cb); got != g.CaseSha256 {
				t.Fatalf("cases/%s.json changed since the golden was captured (sha256 %s, golden has %s): re-capture with SKIP_SOURCE=1 tests/migrate/smt-oracle/capture.sh", name, got, g.CaseSha256)
			}
			for file, want := range g.FixtureSha256 {
				fb, err := Fixture(file)
				if err != nil {
					t.Fatal(err)
				}
				if got := hexSum(fb); got != want {
					t.Fatalf("fixtures/%s changed since the golden was captured: re-capture", file)
				}
			}
			c, err := LoadCases(name)
			if err != nil {
				t.Fatal(err)
			}
			if (c.RecordsFrom != "") != (len(g.FixtureSha256) > 0) {
				t.Fatalf("golden lists %d fixtures but the case reads records from %q", len(g.FixtureSha256), c.RecordsFrom)
			}
		})
	}
}

func hexSum(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

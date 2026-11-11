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

package replay

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	smtoracle "github.com/conduitio/conduit/tests/migrate/smt-oracle"
)

// newReplayer builds the replay environment from the committed fixtures.
func newReplayer(t *testing.T) *Replayer {
	t.Helper()
	schemas, err := smtoracle.Schemas()
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(context.Background(), schemas)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// kcView returns the captured Kafka Connect result for the Debezium message of a pair.
func kcView(t *testing.T, suite *smtoracle.Suite, run string, p smtoracle.Pair) View {
	t.Helper()
	res, ok := suite.Find(run, p.Debezium)
	if !ok {
		t.Fatalf("no captured result for run %q record %q; re-capture", run, p.Debezium)
	}
	v, err := FromKafkaConnect(*res)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestReplay_NativeSourceEqualsUnwrap proves the harness end to end with an empty chain:
// Conduit's Postgres source emits the row Debezium's ExtractNewRecordState
// leaves, for every change of the fixture database, once the representation
// differences of the two sources are normalized. The same must hold with
// the replayed chain in place of the empty one, which is what a translator
// case does.
func TestReplay_NativeSourceEqualsUnwrap(t *testing.T) {
	ctx := context.Background()
	suite, err := smtoracle.Load("pg-unwrap")
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := smtoracle.Pairs()
	if err != nil {
		t.Fatal(err)
	}
	recs, err := Records()
	if err != nil {
		t.Fatal(err)
	}
	outs, err := newReplayer(t).Run(ctx, nil, recs)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pairs {
		t.Run(p.Debezium+"/"+p.Op, func(t *testing.T) {
			kc := kcView(t, suite, "defaults", p)
			cd, err := FromConduit(outs[p.Conduit])
			if err != nil {
				t.Fatal(err)
			}
			diffs, err := Compare(kc, cd, PostgresOptions(p.Table))
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range diffs {
				t.Error(d)
			}
		})
	}
}

// excludeChain is what a ReplaceField exclude translates to today: field.exclude on the
// unwrapped row. guard skips records with no row, as KC does (design: Nil keys and payloads).
func excludeChain(guard bool) []Step {
	st := Step{Plugin: "builtin:field.exclude", Settings: map[string]string{"fields": ".Payload.After.big,.Payload.After.attrs"}}
	if guard {
		st.Condition = `{{ not (eq (printf "%v" .Operation) "delete") }}`
	}
	return []Step{st}
}

// TestReplay_ReplaceFieldExclude replays field.exclude against Kafka Connect's
// ReplaceField$Value exclude after ExtractNewRecordState. With a guard on deletes the chain diffs
// clean for every change of every table that has the columns; without it the
// harness reports the #3147 difference, a delete that comes out as {} where
// Kafka Connect has null. Both halves matter: a harness that cannot fail
// proves nothing.
func TestReplay_ReplaceFieldExclude(t *testing.T) {
	ctx := context.Background()
	suite, err := smtoracle.Load("pg-reshape")
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := smtoracle.Pairs()
	if err != nil {
		t.Fatal(err)
	}
	recs, err := Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		guard     bool
		wantDiffs bool
	}{{"guarded", true, false}, {"unguarded", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			outs, err := newReplayer(t).Run(ctx, excludeChain(tc.guard), recs)
			if err != nil {
				t.Fatal(err)
			}
			var failures []string
			for _, p := range pairs {
				if p.Table != TableItems && p.Table != TableStrict { // the tables that have the columns
					continue
				}
				kc := kcView(t, suite, "exclude", p)
				cd, err := FromConduit(outs[p.Conduit])
				if err != nil {
					t.Fatal(err)
				}
				diffs, err := Compare(kc, cd, PostgresOptions(p.Table))
				if err != nil {
					t.Fatal(err)
				}
				for _, d := range diffs {
					failures = append(failures, p.Debezium+" "+p.Op+": "+d)
				}
			}
			if tc.wantDiffs && len(failures) == 0 {
				t.Fatal("expected the unguarded chain to differ on deletes, found no difference")
			}
			if !tc.wantDiffs {
				for _, f := range failures {
					t.Error(f)
				}
			}
			if tc.wantDiffs {
				t.Logf("unguarded chain, as expected, differs: %v", failures)
			}
		})
	}
}

// TestReplay_SchemaBearingRecords runs a chain over records that carry registry schemas
// (the strict table, read by Conduit with its Avro schema attached). The processors resolve
// those schemas from the in-memory registry loaded from the capture; without it
// the same chain fails with "failed to get serde for schema".
func TestReplay_SchemaBearingRecords(t *testing.T) {
	ctx := context.Background()
	recs, err := Records()
	if err != nil {
		t.Fatal(err)
	}
	var strict []opencdc.Record
	for _, r := range recs {
		if r.Metadata["opencdc.collection"] == TableStrict {
			strict = append(strict, r)
		}
	}
	steps := []Step{{
		Plugin:    "builtin:field.exclude",
		Settings:  map[string]string{"fields": ".Payload.After.big"},
		Condition: `{{ ne (printf "%v" .Operation) "delete" }}`,
	}}

	rp := newReplayer(t)
	outs, err := rp.Run(ctx, steps, strict)
	if err != nil {
		t.Fatal(err)
	}
	for i, o := range outs {
		if o.Err != nil {
			t.Fatalf("strict record %d (%s): %v", i, o.Record.Operation, o.Err)
		}
	}
	// The chain changed the payload's shape, so the schema middleware registered a new version
	// of the source's subject (the effect the design's "Schema metadata after reshaping" is about).
	if _, err := rp.Registry().SchemaBySubjectVersion(ctx, "oracle:strict:strict_payload", 2); err != nil {
		t.Fatalf("expected a new version of the payload subject after dropping a column: %v", err)
	}

	// A registry without the captured schemas cannot serve them: the harness is exercising the registry.
	empty, err := New(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	outs, err = empty.Run(ctx, steps, strict)
	if err != nil {
		t.Fatal(err)
	}
	failed := 0
	for _, o := range outs {
		if o.Err != nil {
			failed++
		}
	}
	if failed == 0 {
		t.Fatal("a registry without the captured schemas should fail records that carry them")
	}
}

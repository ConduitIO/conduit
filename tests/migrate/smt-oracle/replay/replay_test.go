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
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	smtoracle "github.com/conduitio/conduit/tests/migrate/smt-oracle"
	"github.com/goccy/go-json"
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

// kcView returns the captured Kafka Connect result for a Debezium message (a pair's change event
// or its tombstone), against the message as the input it was run on.
func kcView(t *testing.T, suite *smtoracle.Suite, run, message string) View {
	t.Helper()
	res, ok := suite.Find(run, message)
	if !ok {
		t.Fatalf("no captured result for run %q record %q; re-capture", run, message)
	}
	events, err := smtoracle.DebeziumEvents()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Name == message {
			v, err := FromKafkaConnect(*res, e)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
	t.Fatalf("no Debezium message %q", message)
	return View{}
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
			kc := kcView(t, suite, "defaults", p.Debezium)
			cd, err := FromConduit(outs[p.Conduit], Mapping{})
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
				kc := kcView(t, suite, "exclude", p.Debezium)
				cd, err := FromConduit(outs[p.Conduit], Mapping{})
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
			if !tc.wantDiffs {
				failures = withoutKnownSDKFailure(t, failures)
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
	if _, err := rp.Registry().SchemaBySubjectVersion(ctx, strict[0].Metadata["opencdc.payload.schema.subject"], 2); err != nil {
		t.Fatalf("expected a new version of the payload subject after dropping a column: %v", err)
	}

	// A registry without the captured schemas cannot serve them: the harness is using the registry.
	empty, err := New(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := empty.Run(ctx, steps, strict); err == nil || !strings.Contains(err.Error(), "Subject not found") {
		t.Fatalf("a registry without the captured schemas should fail on records that carry them, got %v", err)
	}
}

// knownSDKFailure is a conduit-processor-sdk v0.9.2 bug the replay found, not a harness artifact and not
// a translation problem: when a nullable column flips from a value to NULL (items@7: qty 7 -> NULL), any
// processor that changes the after payload makes the SDK's encode middleware extract a new schema from the
// after values alone, and the before payload (qty is an int there) can no longer be encoded with it
// ("unknown union type int"). Production runs processors through the same middleware, so a real pipeline
// fails this record too, and Kafka Connect does not.
//
// TODO(ConduitIO/conduit-processor-sdk#193): remove this when the SDK is fixed.
//
// It is pinned here, not hidden, and only in this package: the helper is unexported and matches one record and
// one text. Translator tests must NOT filter it. Until it is fixed, a translation that reshapes the value of a
// schema-attached table with nullable columns is not exact, and its case correctly fails to diff clean on
// items@7. This test fails if the failure stops reproducing, so the fix is noticed.
const knownSDKFailure = "failed to encode before payload"

func withoutKnownSDKFailure(t *testing.T, failures []string) []string {
	t.Helper()
	var rest []string
	found := false
	for _, f := range failures {
		if strings.HasPrefix(f, "items@7 update") && strings.Contains(f, knownSDKFailure) {
			found = true
			continue
		}
		rest = append(rest, f)
	}
	if !found {
		t.Error("the known schema-middleware failure on items@7 no longer reproduces: delete knownSDKFailure")
	}
	return rest
}

func TestCoerceAvroRejectsInexactNumbers(t *testing.T) {
	long := map[string]any{"type": "long"}
	for name, v := range map[string]any{
		"fractional":  1.5,
		"above 2^53":  float64(1<<53) * 2,
		"below -2^53": -float64(1<<53) * 4,
		"not number":  "7",
	} {
		if _, err := coerceAvro(long, v); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := coerceAvro(map[string]any{"type": "int"}, float64(1<<31)); err == nil {
		t.Error("an int beyond int32 must fail")
	}
	got, err := coerceAvro(long, float64(1<<53))
	if err != nil || got != int64(1<<53) {
		t.Errorf("2^53 is exact: %v, %v", got, err)
	}
	got, err = coerceAvro(map[string]any{"type": "int"}, float64(-7))
	if err != nil || got != int32(-7) {
		t.Errorf("an int: %v, %v", got, err)
	}
}

// TestDebeziumFixturesAgree checks that the two Debezium captures (converters at false and at their
// default) hold the same messages, so a lagging second connector cannot commit a short fixture.
func TestDebeziumFixturesAgree(t *testing.T) {
	a, err := smtoracle.DebeziumEvents()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := smtoracle.Fixture(smtoracle.DebeziumDefaultConverterFile)
	if err != nil {
		t.Fatal(err)
	}
	var b []smtoracle.InputRecord
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("%d messages with replace.null.with.default=false, %d with the default", len(a), len(b))
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Topic[strings.LastIndex(a[i].Topic, "."):] != b[i].Topic[strings.LastIndex(b[i].Topic, "."):] {
			t.Fatalf("message %d: %s (%s) against %s (%s)", i, a[i].Name, a[i].Topic, b[i].Name, b[i].Topic)
		}
	}
}

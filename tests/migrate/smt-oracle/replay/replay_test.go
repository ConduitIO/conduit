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

// excludeChain is what a ReplaceField exclude translates to: field.exclude on the unwrapped row,
// from both images (the translator's BothImages, design decision 12). guard skips records with no row,
// as KC does (design: Nil keys and payloads). Excluding from After only is the shape processor-sdk
// v0.10.0 refuses on an update that has a Before image (see afterOnlyExcludeChain).
func excludeChain(guard bool) []Step {
	st := Step{Plugin: "builtin:field.exclude", Settings: map[string]string{
		"fields": ".Payload.Before.big,.Payload.Before.attrs,.Payload.After.big,.Payload.After.attrs",
	}}
	if guard {
		st.Condition = `{{ not (eq (printf "%v" .Operation) "delete") }}`
	}
	return []Step{st}
}

// afterOnlyExcludeChain is the negative control for the exclude replay: the same exclude from After
// only, which v0.9.2 let through with the field silently dropped from Before and v0.10.0 fails with
// ErrFieldNotInSchema.
func afterOnlyExcludeChain() []Step {
	return []Step{{
		Plugin:   "builtin:field.exclude",
		Settings: map[string]string{"fields": ".Payload.After.big,.Payload.After.attrs"},
	}}
}

// replayExcludeFailures replays chain and returns, per Debezium event of the tables that have the
// columns, the differences from Kafka Connect's ReplaceField$Value exclude after ExtractNewRecordState.
func replayExcludeFailures(t *testing.T, chain []Step) []string {
	t.Helper()
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
	outs, err := newReplayer(t).Run(ctx, chain, recs)
	if err != nil {
		t.Fatal(err)
	}
	var failures []string
	sawNullFlip := false
	for _, p := range pairs {
		if p.Table != TableItems && p.Table != TableStrict { // the tables that have the columns
			continue
		}
		sawNullFlip = sawNullFlip || p.Debezium == "items@7" && p.Op == "update"
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
	if !sawNullFlip {
		t.Fatal("items@7 (qty 7 to NULL) is not among the compared changes: the replay no longer covers the nullable flip")
	}
	return failures
}

// TestReplay_ReplaceFieldExclude replays field.exclude, from both images and guarded on deletes,
// against Kafka Connect's ReplaceField$Value exclude after ExtractNewRecordState. It must diff clean
// for every change of every table that has the columns, including items@7, the update where qty goes
// from 7 to NULL: processor-sdk v0.9.2 failed that record (conduit-processor-sdk#193), v0.10.0 (#195)
// encodes it. The unguarded chain also diffs clean now: v0.10.0's reference resolver no longer
// turns a delete's nil after into {} (#3147 was this difference).
func TestReplay_ReplaceFieldExclude(t *testing.T) {
	for _, guard := range []bool{true, false} {
		name := "guarded"
		if !guard {
			name = "unguarded"
		}
		t.Run(name, func(t *testing.T) {
			for _, f := range replayExcludeFailures(t, excludeChain(guard)) {
				t.Error(f)
			}
		})
	}
}

// TestReplay_ReplaceFieldExcludeAfterOnlyFails is the negative control: a harness that cannot fail
// proves nothing. Excluding from After only is exactly what v0.10.0 refuses on an update with a
// Before image, so the harness must report every such update as a failed record naming
// ErrFieldNotInSchema, and nothing else must differ (creates, snapshots and deletes have no
// Before and no After respectively, so the check does not apply). This replaces the pre-v0.10.0
// control, an unguarded chain turning a delete into {} (#3147).
func TestReplay_ReplaceFieldExcludeAfterOnlyFails(t *testing.T) {
	failures := replayExcludeFailures(t, afterOnlyExcludeChain())
	if len(failures) == 0 {
		t.Fatal("expected an After-only exclude to fail on updates with a Before image, found no difference")
	}
	for _, f := range failures {
		if !strings.Contains(f, " update: ") || !strings.Contains(f, "field is not in the attached schema") {
			t.Errorf("unexpected failure, want only updates that fail with ErrFieldNotInSchema: %s", f)
		}
	}
	t.Logf("After-only exclude, as expected, fails: %v", failures)
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

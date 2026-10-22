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

package recordgen

import (
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"pgregory.net/rapid"
)

// TestCheckOutput covers every verdict of CheckOutput, so a property built
// on it cannot pass because the checker accepts too much.
func TestCheckOutput(t *testing.T) {
	recs := []opencdc.Record{
		{Position: opencdc.Position("0"), Key: opencdc.RawData("a")},
		{Position: opencdc.Position("1"), Key: opencdc.RawData("b")},
		{Position: opencdc.Position("2"), Key: opencdc.RawData("c")},
	}
	single := func(i int) sdk.ProcessedRecord { return sdk.SingleRecord(recs[i]) }
	errRec := sdk.ErrorRecord{Error: cerrors.New("boom")}
	changed := sdk.SingleRecord{Position: opencdc.Position("1"), Key: opencdc.RawData("x")}

	testCases := []struct {
		name   string
		out    []sdk.ProcessedRecord
		failAt int
		ok     bool
	}{
		{"all processed", []sdk.ProcessedRecord{single(0), single(1), single(2)}, -1, true},
		{"one missing", []sdk.ProcessedRecord{single(0), single(1)}, -1, false},
		{"unexpected error", []sdk.ProcessedRecord{single(0), errRec, single(2)}, -1, false},
		{"record changed", []sdk.ProcessedRecord{single(0), changed, single(2)}, -1, false},
		{"filtered is not processed", []sdk.ProcessedRecord{single(0), sdk.FilterRecord{}, single(2)}, -1, false},
		{"stops at the error", []sdk.ProcessedRecord{single(0), errRec}, 1, true},
		{"continues after the error", []sdk.ProcessedRecord{single(0), errRec, single(2)}, 1, true},
		{"errors after the error", []sdk.ProcessedRecord{single(0), errRec, errRec}, 1, true},
		{"error missing", []sdk.ProcessedRecord{single(0)}, 1, false},
		{"error is a single record", []sdk.ProcessedRecord{single(0), single(1)}, 1, false},
		{"record after the error changed", []sdk.ProcessedRecord{errRec, changed}, 0, false},
		{"too many", []sdk.ProcessedRecord{errRec, single(1), single(2), single(2)}, 0, false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckOutput(tc.out, recs, tc.failAt)
			if (err == nil) != tc.ok {
				t.Fatalf("CheckOutput = %v, want ok = %v", err, tc.ok)
			}
		})
	}
}

func TestHelpers_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := Batch().Draw(t, "records")
		out := make([]sdk.ProcessedRecord, len(in))
		for i, rec := range in {
			out[i] = sdk.SingleRecord(rec)
		}
		if got := Records(t, out); len(got) != len(in) {
			t.Fatalf("Records returned %d of %d", len(got), len(in))
		}
		if k := FailAt(t, len(in)); k < -1 || k >= len(in) {
			t.Fatalf("FailAt = %d for %d records", k, len(in))
		}
		for _, rec := range in {
			sd := EnsureStructured(rec.Payload.After)
			if orig, ok := rec.Payload.After.(opencdc.StructuredData); ok {
				sd["probe"] = true
				if _, same := orig["probe"]; !same {
					t.Fatal("EnsureStructured copied structured data instead of returning it")
				}
			} else if len(sd) != 0 {
				t.Fatalf("EnsureStructured(%#v) = %v, want empty", rec.Payload.After, sd)
			}
		}
	})
}

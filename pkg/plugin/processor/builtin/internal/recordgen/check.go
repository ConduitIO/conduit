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
	"fmt"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal"
	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"
)

// CheckOutput checks a processor's output against the expected one, record
// by record, for a batch in which record failAt (or -1 for none) must fail:
//
//   - every record before failAt is a SingleRecord equal to want[i];
//   - record failAt is an ErrorRecord;
//   - records after failAt may be missing: the built-in processors stop at
//     the first error, and the engine retries what was not returned. A
//     record after failAt that is returned must be an ErrorRecord or equal
//     to want[i], so nothing is reported processed that was not;
//   - with no failure, there is one output per input.
//
// want[failAt] is not read. opts are added to the record comparison. It
// returns the first mismatch, or nil.
func CheckOutput(out []sdk.ProcessedRecord, want []opencdc.Record, failAt int, opts ...cmp.Option) error {
	opts = append(append([]cmp.Option{}, internal.CmpProcessedRecordOpts...), opts...)
	switch {
	case failAt < 0 && len(out) != len(want):
		return fmt.Errorf("%d records in, %d out", len(want), len(out))
	case failAt >= 0 && (len(out) <= failAt || len(out) > len(want)):
		return fmt.Errorf("%d records in, record %d must fail, %d out", len(want), failAt, len(out))
	}
	for i, p := range out {
		if i == failAt {
			if _, ok := p.(sdk.ErrorRecord); !ok {
				return fmt.Errorf("record %d: got %#v, want an ErrorRecord", i, p)
			}
			continue
		}
		if _, ok := p.(sdk.ErrorRecord); ok && failAt >= 0 && i > failAt {
			continue
		}
		rec, ok := p.(sdk.SingleRecord)
		if !ok {
			return fmt.Errorf("record %d: got %#v, want a SingleRecord", i, p)
		}
		if diff := cmp.Diff(want[i], opencdc.Record(rec), opts...); diff != "" {
			return fmt.Errorf("record %d (-want +got):\n%s", i, diff)
		}
	}
	return nil
}

// Records unwraps processed records that must all be SingleRecords, to feed
// them to the next processor.
func Records(t *rapid.T, out []sdk.ProcessedRecord) []opencdc.Record {
	recs := make([]opencdc.Record, len(out))
	for i, p := range out {
		rec, ok := p.(sdk.SingleRecord)
		if !ok {
			t.Fatalf("record %d: got %#v, want a SingleRecord", i, p)
		}
		recs[i] = opencdc.Record(rec)
	}
	return recs
}

// FailAt draws which record of an n-record batch fails: an index, or -1 for
// none (one draw in three).
func FailAt(t *rapid.T, n int) int {
	if rapid.IntRange(0, 2).Draw(t, "any failure") == 0 {
		return -1
	}
	return rapid.IntRange(0, n-1).Draw(t, "failing record")
}

// EnsureStructured returns d if it is structured data, and otherwise a new,
// empty StructuredData, for a property that needs a nested field to exist.
func EnsureStructured(d opencdc.Data) opencdc.StructuredData {
	if sd, ok := d.(opencdc.StructuredData); ok {
		return sd
	}
	return opencdc.StructuredData{}
}

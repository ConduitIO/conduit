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

package json

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/recordgen"
	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"
)

// TestEncodeDecode_PropertyRoundTrip: json.decode undoes json.encode. For
// any JSON-representable structured value at the configured field, encoding
// it to JSON and decoding it again gives back every record exactly as it
// was, in the same order, with nothing else in the record touched.
//
// The check count follows rapid: -rapid.checks or RAPID_CHECKS (default
// 100). The nightly property job runs many more.
func TestEncodeDecode_PropertyRoundTrip(t *testing.T) {
	ctx := context.Background()
	rapid.Check(t, func(t *rapid.T) {
		field := rapid.SampledFrom([]string{".Key", ".Payload.Before", ".Payload.After", ".Payload.After.nested"}).Draw(t, "field")
		in := recordgen.Batch(recordgen.StructuredData()).Draw(t, "records")
		if field == ".Payload.After.nested" {
			for i := range in {
				// A nested field holds any JSON value, not only an object.
				in[i].Payload.After.(opencdc.StructuredData)["nested"] = recordgen.JSONValue().Draw(t, "nested")
			}
		}

		enc := NewEncodeProcessor(log.Nop())
		dec := NewDecodeProcessor(log.Nop())
		for _, p := range []sdk.Processor{enc, dec} {
			if err := p.Configure(ctx, config.Config{"field": field}); err != nil {
				t.Fatalf("configure %s: %v", field, err)
			}
		}

		encoded := enc.Process(ctx, recordgen.Clone(in))
		got := dec.Process(ctx, singleRecords(t, encoded))
		if len(got) != len(in) {
			t.Fatalf("%d records in, %d out", len(in), len(got))
		}
		for i, rec := range singleRecords(t, got) {
			if diff := cmp.Diff(in[i], rec, internal.CmpProcessedRecordOpts...); diff != "" {
				t.Fatalf("record %d changed in an encode/decode round trip of %s (-want +got):\n%s", i, field, diff)
			}
		}
	})
}

// singleRecords unwraps processed records, failing on anything but a
// SingleRecord.
func singleRecords(t *rapid.T, processed []sdk.ProcessedRecord) []opencdc.Record {
	out := make([]opencdc.Record, len(processed))
	for i, p := range processed {
		rec, ok := p.(sdk.SingleRecord)
		if !ok {
			t.Fatalf("record %d: got %#v, want a SingleRecord", i, p)
		}
		out[i] = opencdc.Record(rec)
	}
	return out
}

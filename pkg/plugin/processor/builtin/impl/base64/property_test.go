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

package base64

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

// TestEncodeDecode_PropertyRoundTrip: base64.decode undoes base64.encode.
// For any bytes at the configured field, encoding and decoding gives back
// the same bytes, every record comes out, in order, and nothing else in the
// record changes.
//
// The bytes come back as the type base64.decode writes: raw data for a
// whole key or payload side, a string for a field inside structured data or
// metadata. A []byte field therefore comes back as a string with the same
// bytes.
//
// The check count follows rapid: -rapid.checks or RAPID_CHECKS (default
// 100). The nightly property job runs many more.
func TestEncodeDecode_PropertyRoundTrip(t *testing.T) {
	ctx := context.Background()
	rapid.Check(t, func(t *rapid.T) {
		field := rapid.SampledFrom([]string{".Key", ".Payload.After", ".Payload.After.blob", ".Metadata.blob"}).Draw(t, "field")
		raw := field == ".Key" || field == ".Payload.After"

		gen := recordgen.StructuredData()
		if raw {
			gen = recordgen.RawData()
		}
		in := recordgen.Batch(gen).Draw(t, "records")
		want := recordgen.Clone(in)
		for i := range in {
			b := rapid.SliceOfN(rapid.Byte(), 0, 64).Draw(t, "bytes")
			switch field {
			case ".Key":
				in[i].Key, want[i].Key = opencdc.RawData(b), opencdc.RawData(b)
			case ".Payload.After":
				in[i].Payload.After, want[i].Payload.After = opencdc.RawData(b), opencdc.RawData(b)
			case ".Payload.After.blob":
				var v any = string(b)
				if rapid.Bool().Draw(t, "as []byte") {
					v = b
				}
				in[i].Payload.After.(opencdc.StructuredData)["blob"] = v
				want[i].Payload.After.(opencdc.StructuredData)["blob"] = string(b)
			case ".Metadata.blob":
				in[i].Metadata["blob"], want[i].Metadata["blob"] = string(b), string(b)
			}
		}

		enc := NewEncodeProcessor(log.Nop())
		dec := NewDecodeProcessor(log.Nop())
		for _, p := range []sdk.Processor{enc, dec} {
			if err := p.Configure(ctx, config.Config{"field": field}); err != nil {
				t.Fatalf("configure %s: %v", field, err)
			}
		}

		got := dec.Process(ctx, singleRecords(t, enc.Process(ctx, in)))
		if len(got) != len(want) {
			t.Fatalf("%d records in, %d out", len(want), len(got))
		}
		for i, rec := range singleRecords(t, got) {
			if diff := cmp.Diff(want[i], rec, internal.CmpProcessedRecordOpts...); diff != "" {
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

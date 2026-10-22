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
	"encoding/base64"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/recordgen"
	"pgregory.net/rapid"
)

// The check count of every property here follows rapid: -rapid.checks or
// RAPID_CHECKS (default 100). The nightly property job runs many more.

func configured(t *rapid.T, field string, procs ...sdk.Processor) {
	for _, p := range procs {
		if err := p.Configure(context.Background(), config.Config{"field": field}); err != nil {
			t.Fatalf("configure %s: %v", field, err)
		}
	}
}

// TestEncodeDecode_PropertyRoundTrip: base64.decode undoes base64.encode.
// The batch mixes nil, raw and structured keys and payload sides; the
// configured field holds bytes or nothing (nil, which both processors leave
// alone). The same bytes come back, every record comes out in order, and
// nothing else in the record changes.
//
// The bytes come back as the type base64.decode writes: raw data for a
// whole key or payload side, a string for a field inside structured data or
// metadata. A []byte field therefore comes back as a string with the same
// bytes.
func TestEncodeDecode_PropertyRoundTrip(t *testing.T) {
	ctx := context.Background()
	rapid.Check(t, func(t *rapid.T) {
		field := rapid.SampledFrom([]string{".Key", ".Payload.After", ".Payload.After.blob", ".Metadata.blob"}).Draw(t, "field")
		in := recordgen.Batch().Draw(t, "records")
		want := make([]opencdc.Record, len(in))
		for i := range in {
			isNil := rapid.IntRange(0, 4).Draw(t, "nil target") == 0
			b := recordgen.Bytes().Draw(t, "bytes")
			switch field {
			case ".Key":
				in[i].Key = opencdc.RawData(b)
				if isNil {
					in[i].Key = nil
				}
			case ".Payload.After":
				in[i].Payload.After = opencdc.RawData(b)
				if isNil {
					in[i].Payload.After = nil
				}
			case ".Payload.After.blob":
				after := recordgen.EnsureStructured(in[i].Payload.After)
				var v any = string(b)
				switch {
				case isNil:
					v = nil
				case rapid.Bool().Draw(t, "as []byte"):
					v = b
				}
				after["blob"] = v
				in[i].Payload.After = after
			case ".Metadata.blob":
				if in[i].Metadata == nil {
					in[i].Metadata = opencdc.Metadata{}
				}
				in[i].Metadata["blob"] = string(b)
			}
			want[i] = recordgen.Clone(in[i : i+1])[0]
			if field == ".Payload.After.blob" && !isNil {
				want[i].Payload.After.(opencdc.StructuredData)["blob"] = string(b)
			}
		}

		enc, dec := NewEncodeProcessor(log.Nop()), NewDecodeProcessor(log.Nop())
		configured(t, field, enc, dec)
		got := dec.Process(ctx, recordgen.Records(t, enc.Process(ctx, in)))
		if err := recordgen.CheckOutput(got, want, -1); err != nil {
			t.Fatal(err)
		}
	})
}

// TestDecode_PropertyErrorAtBadRecord: a key that is not valid base64 fails
// its record with an ErrorRecord, and the records before it are decoded.
func TestDecode_PropertyErrorAtBadRecord(t *testing.T) {
	ctx := context.Background()
	rapid.Check(t, func(t *rapid.T) {
		in := recordgen.Batch().Draw(t, "records")
		failAt := recordgen.FailAt(t, len(in))
		want := recordgen.Clone(in)
		for i := range in {
			if i == failAt {
				// '*' is outside the base64 alphabet.
				in[i].Key = opencdc.RawData(rapid.StringMatching(`[A-Za-z0-9+/]{0,8}\*[A-Za-z0-9+/=]{0,8}`).Draw(t, "not base64"))
				continue
			}
			b := recordgen.Bytes().Draw(t, "bytes")
			in[i].Key = opencdc.RawData(base64.StdEncoding.EncodeToString(b))
			want[i].Key = opencdc.RawData(b)
		}

		dec := NewDecodeProcessor(log.Nop())
		configured(t, ".Key", dec)
		if err := recordgen.CheckOutput(dec.Process(ctx, in), want, failAt); err != nil {
			t.Fatal(err)
		}
	})
}

// TestEncode_PropertyErrorAtBadRecord: a payload that is structured data,
// which base64.encode cannot encode, fails its record with an ErrorRecord
// rather than being written in some other form, and the records before it
// are encoded.
func TestEncode_PropertyErrorAtBadRecord(t *testing.T) {
	ctx := context.Background()
	rapid.Check(t, func(t *rapid.T) {
		in := recordgen.Batch().Draw(t, "records")
		failAt := recordgen.FailAt(t, len(in))
		want := recordgen.Clone(in)
		for i := range in {
			if i == failAt {
				in[i].Payload.After = recordgen.StructuredData().Draw(t, "structured")
				continue
			}
			b := recordgen.Bytes().Draw(t, "bytes")
			in[i].Payload.After = opencdc.RawData(b)
			want[i].Payload.After = opencdc.RawData(base64.StdEncoding.EncodeToString(b))
		}

		enc := NewEncodeProcessor(log.Nop())
		configured(t, ".Payload.After", enc)
		if err := recordgen.CheckOutput(enc.Process(ctx, in), want, failAt); err != nil {
			t.Fatal(err)
		}
	})
}

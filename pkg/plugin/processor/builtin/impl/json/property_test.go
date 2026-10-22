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
	"math"
	"strconv"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/recordgen"
	"github.com/goccy/go-json"
	"pgregory.net/rapid"
)

// The check count of every property here follows rapid: -rapid.checks or
// RAPID_CHECKS (default 100). The nightly property job runs many more.

var jsonFields = []string{".Key", ".Payload.Before", ".Payload.After", ".Payload.After.nested"}

// setTarget puts v at field in rec. A nested field needs a structured
// .Payload.After, so one is made if the record has none.
func setTarget(rec *opencdc.Record, field string, v any) {
	switch field {
	case ".Key":
		rec.Key = toData(v)
	case ".Payload.Before":
		rec.Payload.Before = toData(v)
	case ".Payload.After":
		rec.Payload.After = toData(v)
	default:
		after := recordgen.EnsureStructured(rec.Payload.After)
		after["nested"] = v
		rec.Payload.After = after
	}
}

// decodedTarget returns the value json.decode should leave at field after
// json.encode wrote rec's value there: recordgen.Decoded of it, with a whole
// key or payload side turned back into a map so setTarget can place it.
func decodedTarget(field string, rec opencdc.Record) any {
	var v any
	switch field {
	case ".Key":
		v = rec.Key
	case ".Payload.Before":
		v = rec.Payload.Before
	case ".Payload.After":
		v = rec.Payload.After
	default:
		v = rec.Payload.After.(opencdc.StructuredData)["nested"] //nolint:forcetypeassert // setTarget made it structured
	}
	switch v := v.(type) {
	case nil:
		return nil
	case opencdc.StructuredData:
		return recordgen.Decoded(map[string]any(v))
	case opencdc.RawData:
		return []byte(v)
	}
	return recordgen.Decoded(v)
}

func toData(v any) opencdc.Data {
	switch v := v.(type) {
	case nil:
		return nil
	case map[string]any:
		return opencdc.StructuredData(v)
	case []byte:
		return opencdc.RawData(v)
	}
	panic("unexpected target value")
}

func configured(t *rapid.T, field string, procs ...sdk.Processor) {
	for _, p := range procs {
		if err := p.Configure(context.Background(), config.Config{"field": field}); err != nil {
			t.Fatalf("configure %s: %v", field, err)
		}
	}
}

// TestEncodeDecode_PropertyRoundTrip: json.decode undoes json.encode. The
// batch mixes nil, raw and structured keys and payload sides. The value at
// the configured field is a JSON object (a whole key or payload side), any
// JSON value (a nested field), or nil, which both processors leave alone.
// Every record comes back as it was, in the same order, with every other
// part untouched, except that json.decode narrows integral numbers to int64
// or uint64 (#3134), so the expected target value is recordgen.Decoded of
// the original. The comparison stays type-exact: a float64 where an int64 is
// expected, or the reverse, fails.
func TestEncodeDecode_PropertyRoundTrip(t *testing.T) {
	ctx := context.Background()
	rapid.Check(t, func(t *rapid.T) {
		field := rapid.SampledFrom(jsonFields).Draw(t, "field")
		in := recordgen.Batch().Draw(t, "records")
		for i := range in {
			var v any
			switch {
			case rapid.IntRange(0, 4).Draw(t, "nil target") == 0:
				// a tombstone or delete side: left alone
			case field == ".Payload.After.nested":
				v = recordgen.JSONValue().Draw(t, "nested")
			default:
				v = recordgen.JSONObject().Draw(t, "object")
			}
			setTarget(&in[i], field, v)
		}
		want := recordgen.Clone(in)
		for i := range want {
			setTarget(&want[i], field, decodedTarget(field, in[i]))
		}

		enc, dec := NewEncodeProcessor(log.Nop()), NewDecodeProcessor(log.Nop())
		configured(t, field, enc, dec)
		got := dec.Process(ctx, recordgen.Records(t, enc.Process(ctx, in)))
		if err := recordgen.CheckOutput(got, want, -1); err != nil {
			t.Fatal(err)
		}
	})
}

// TestEncode_PropertyErrorAtBadRecord: a value JSON cannot represent (NaN,
// ±Inf) fails its record with an ErrorRecord, never a SingleRecord with
// something else written in its place, and the records before it are
// encoded.
func TestEncode_PropertyErrorAtBadRecord(t *testing.T) {
	ctx := context.Background()
	rapid.Check(t, func(t *rapid.T) {
		in := recordgen.Batch().Draw(t, "records")
		failAt := recordgen.FailAt(t, len(in))
		want := make([]opencdc.Record, len(in))
		for i := range in {
			v := recordgen.JSONValue().Draw(t, "nested")
			if i == failAt {
				v = rapid.SampledFrom([]float64{math.NaN(), math.Inf(1), math.Inf(-1)}).Draw(t, "bad number")
			}
			setTarget(&in[i], ".Payload.After.nested", v)
			want[i] = recordgen.Clone(in[i : i+1])[0]
			if v != nil && i != failAt {
				b, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				want[i].Payload.After.(opencdc.StructuredData)["nested"] = string(b)
			}
		}

		enc := NewEncodeProcessor(log.Nop())
		configured(t, ".Payload.After.nested", enc)
		if err := recordgen.CheckOutput(enc.Process(ctx, in), want, failAt); err != nil {
			t.Fatal(err)
		}
	})
}

// TestDecode_PropertyErrorAtBadRecord: raw data that is not JSON fails its
// record with an ErrorRecord, and the records before it are decoded.
func TestDecode_PropertyErrorAtBadRecord(t *testing.T) {
	ctx := context.Background()
	rapid.Check(t, func(t *rapid.T) {
		in := recordgen.Batch().Draw(t, "records")
		failAt := recordgen.FailAt(t, len(in))
		want := recordgen.Clone(in)
		for i := range in {
			if i == failAt {
				bad := recordgen.Bytes().Filter(func(b []byte) bool {
					return len(b) > 0 && !json.Valid(b)
				}).Draw(t, "not JSON")
				in[i].Key = opencdc.RawData(bad)
				continue
			}
			obj := recordgen.JSONObject().Draw(t, "object")
			b, err := json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}
			in[i].Key = opencdc.RawData(b)
			want[i].Key = opencdc.StructuredData(recordgen.Decoded(obj).(map[string]any)) //nolint:forcetypeassert // Decoded keeps the type
		}

		dec := NewDecodeProcessor(log.Nop())
		configured(t, ".Key", dec)
		if err := recordgen.CheckOutput(dec.Process(ctx, in), want, failAt); err != nil {
			t.Fatal(err)
		}
	})
}

// TestDecode_PropertyIntegersLossless: every JSON integer in the int64 or
// uint64 range decodes to exactly that integer, as int64 when it fits and
// uint64 otherwise, including those above 2^53 that a float64 would round
// (#3134).
func TestDecode_PropertyIntegersLossless(t *testing.T) {
	ctx := context.Background()
	rapid.Check(t, func(t *rapid.T) {
		var text string
		var want any
		if rapid.Bool().Draw(t, "signed") {
			i := rapid.Int64().Draw(t, "int64")
			text, want = strconv.FormatInt(i, 10), i
		} else {
			u := rapid.Uint64Range(math.MaxInt64+1, math.MaxUint64).Draw(t, "uint64")
			text, want = strconv.FormatUint(u, 10), u
		}
		in := []opencdc.Record{{
			Position: opencdc.Position("0"),
			Key:      opencdc.RawData(`{"n":` + text + `}`),
		}}
		wantRec := recordgen.Clone(in)
		wantRec[0].Key = opencdc.StructuredData{"n": want}

		dec := NewDecodeProcessor(log.Nop())
		configured(t, ".Key", dec)
		if err := recordgen.CheckOutput(dec.Process(ctx, in), wantRec, -1); err != nil {
			t.Fatal(err)
		}
	})
}

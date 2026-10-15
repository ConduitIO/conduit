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
	"strconv"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/goccy/go-json"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"pgregory.net/rapid"
)

// TestJSONValue_PropertyRoundTrips checks the promise in JSONValue's doc:
// every value survives a JSON round trip unchanged, through goccy/go-json,
// the encoder the json processors use. A property built on a value
// JSON cannot represent would fail for the wrong reason.
func TestJSONValue_PropertyRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		v := JSONValue().Draw(t, "value")
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %#v: %v", v, err)
		}
		var got any
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		if diff := cmp.Diff(v, got); diff != "" {
			t.Fatalf("value changed in a JSON round trip (-want +got):\n%s", diff)
		}
	})
}

// TestClone_PropertyDeepAndTypePreserving: Clone returns records equal to
// the originals, with the same Go types at every level, and sharing no
// memory with them.
func TestClone_PropertyDeepAndTypePreserving(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		data := rapid.SampledFrom([]*rapid.Generator[opencdc.Data]{StructuredData(), RawData()}).Draw(t, "data kind")
		in := Batch(data).Draw(t, "records")
		got := Clone(in)
		opts := cmpopts.IgnoreUnexported(opencdc.Record{})
		if diff := cmp.Diff(in, got, opts); diff != "" {
			t.Fatalf("clone differs (-in +clone):\n%s", diff)
		}

		// Mutate everything mutable in the clone; the original must not move.
		before := Clone(in)
		for i := range got {
			got[i].Position = append(got[i].Position[:0], 'x')
			got[i].Metadata["mutated"] = "yes"
			mutate(got[i].Key)
			mutate(got[i].Payload.Before)
			mutate(got[i].Payload.After)
		}
		if diff := cmp.Diff(before, in, opts); diff != "" {
			t.Fatalf("mutating the clone changed the original (-before +after):\n%s", diff)
		}
	})
}

// TestBatch_PropertyPositions: record i has position "i", so properties can
// check order.
func TestBatch_PropertyPositions(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		for i, rec := range Batch(RawData()).Draw(t, "records") {
			if got, want := string(rec.Position), strconv.Itoa(i); got != want {
				t.Fatalf("record %d has position %q, want %q", i, got, want)
			}
		}
	})
}

// mutate changes every map, slice and byte slice reachable from d.
func mutate(d opencdc.Data) {
	switch d := d.(type) {
	case opencdc.StructuredData:
		mutateValue(map[string]any(d))
	case opencdc.RawData:
		for i := range d {
			d[i]++
		}
	}
}

func mutateValue(v any) {
	switch v := v.(type) {
	case map[string]any:
		for _, e := range v {
			mutateValue(e)
		}
		v["mutated"] = true
	case []any:
		for i, e := range v {
			mutateValue(e)
			v[i] = "mutated"
		}
	}
}

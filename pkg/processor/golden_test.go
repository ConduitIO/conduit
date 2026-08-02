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

// Pre-flip gate for the arch-v2 default flip (v0.20). The persisted
// processor.Instance blob is plain JSON with no version/schema tag anywhere,
// and Store.decode never sets DisallowUnknownFields - so an older binary
// reading a newer blob silently drops fields it doesn't recognize.
//
// TestGolden_ProcessorInstance_RoundTrip checks in the actual JSON currently
// persisted for a processor instance and asserts HEAD can parse it into a
// semantically identical Instance AND re-serialize it back to an equivalent
// blob. If a future change drops or renames a field, the re-serialized blob
// loses a key the checked-in golden still has, and the semantic JSON
// comparison fails.
//
// This file is intentionally `package processor` (not processor_test, unlike
// store_test.go) so it can reach Store.encode/addKeyPrefix directly to do
// the round-trip; Go allows internal and external test files to coexist in
// one directory.
package processor

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
)

func assertJSONSemanticallyEqual(t *testing.T, is *is.I, golden, got []byte) {
	t.Helper()

	var goldenMap, gotMap map[string]any
	is.NoErr(json.Unmarshal(golden, &goldenMap))
	is.NoErr(json.Unmarshal(got, &gotMap))

	if !reflect.DeepEqual(goldenMap, gotMap) {
		t.Fatalf(
			"re-serialized instance is not semantically equal to the golden blob "+
				"(a field was likely dropped or renamed):\ngolden: %s\ngot:    %s",
			golden, got,
		)
	}
}

func TestGolden_ProcessorInstance_RoundTrip(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	golden, err := os.ReadFile("testdata/golden_processor_instance.json")
	is.NoErr(err)

	db := &inmemory.DB{}
	s := NewStore(db)
	is.NoErr(db.Set(ctx, s.addKeyPrefix("golden-processor"), golden))

	got, err := s.Get(ctx, "golden-processor")
	is.NoErr(err)

	ts := time.Date(2026, 6, 15, 9, 30, 0, 0, time.UTC)
	want := &Instance{
		ID:            "golden-processor",
		CreatedAt:     ts,
		UpdatedAt:     ts.Add(time.Minute),
		ProvisionedBy: ProvisionTypeConfig,
		Plugin:        "builtin:field.set",
		Condition:     `{{ eq .Metadata["key"] "value" }}`,
		Parent: Parent{
			ID:   "golden-pipeline",
			Type: ParentTypePipeline,
		},
		Config: Config{
			Settings: map[string]string{"field": "value"},
			Workers:  4,
		},
	}
	is.Equal(want, got)

	// The actual drop/rename detector: re-encode what HEAD decoded and
	// compare against the golden blob itself.
	reEncoded, err := s.encode(got)
	is.NoErr(err)
	assertJSONSemanticallyEqual(t, is, golden, reEncoded)
}

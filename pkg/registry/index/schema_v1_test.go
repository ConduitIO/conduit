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

package index_test

import (
	"os"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry/index"
)

// TestGoldenRoundTrip_SampleIndex is the golden round-trip test plan-v2 §2.1
// requires: registry-index/sample-index.json (2 connectors — postgres with
// 2 versions, one yanked; example-vector-sink with a revoked publisher)
// must survive unmarshal-into-Payload -> marshal -> unmarshal-into-generic
// with no field lost, renamed, or reshaped. A mismatch here would mean this
// package's types have silently drifted from the frozen schema they must
// match field-for-field.
func TestGoldenRoundTrip_SampleIndex(t *testing.T) {
	is := is.New(t)

	raw, err := os.ReadFile("testdata/sample-index.json")
	is.NoErr(err)

	// Parse via the real, production entry point.
	payload, err := index.ParseUnverified(raw)
	is.NoErr(err)

	// Sanity-check a handful of fields survived untouched, at every nesting
	// level the schema has (payload -> connectors -> publisher/versions ->
	// artifacts -> signature/provenance, and the revocation/yank leaves).
	is.Equal(payload.SchemaVersion, 1)
	is.Equal(payload.Index.Version, int64(42))
	is.Equal(len(payload.Connectors), 2)

	pg := payload.Connectors[0]
	is.Equal(pg.Name, "postgres")
	is.Equal(pg.Publisher.ExpectedOIDCIssuer, "https://token.actions.githubusercontent.com")
	is.Equal(len(pg.Versions), 2)
	is.True(pg.Versions[0].Yanked != nil) // 0.14.0 is yanked
	is.Equal(pg.Versions[0].Yanked.Reason[:5], "0.14.")
	is.True(pg.Versions[1].Yanked == nil) // 0.14.1 is not
	is.Equal(len(pg.Versions[0].Artifacts), 2)
	art := pg.Versions[0].Artifacts[0]
	is.Equal(art.OS, "linux")
	is.Equal(art.Arch, "amd64")
	is.Equal(art.Kind, "standalone")
	is.True(art.Signature.BundleURL != "")
	is.True(pg.Versions[0].SLSAProvenance != nil)

	vs := payload.Connectors[1]
	is.Equal(vs.Name, "example-vector-sink")
	is.True(vs.Publisher.Revoked != nil)
	is.True(vs.Publisher.Revoked.Reason != "")

	// Full structural round trip: unmarshal original raw payload generically,
	// marshal the typed struct, unmarshal THAT generically too, and diff the
	// two generic representations — this catches a field this package's
	// struct silently drops or renames, which the targeted assertions above
	// could miss.
	var envelope struct {
		Payload json.RawMessage `json:"payload"`
	}
	is.NoErr(json.Unmarshal(raw, &envelope))

	var wantGeneric map[string]any
	is.NoErr(json.Unmarshal(envelope.Payload, &wantGeneric))

	roundTripped, err := json.Marshal(payload)
	is.NoErr(err)

	var gotGeneric map[string]any
	is.NoErr(json.Unmarshal(roundTripped, &gotGeneric))

	if diff := cmp.Diff(wantGeneric, gotGeneric); diff != "" {
		t.Fatalf("golden round-trip mismatch (-want +got):\n%s", diff)
	}
}

// TestParseUnverified_SchemaTooNew proves the schema-too-new refusal runs
// BEFORE any typed unmarshal is attempted (plan-v2 §15.1) — a schemaVersion
// higher than this build understands must fail with CodeSchemaTooNew even
// if the rest of the payload wouldn't otherwise parse into the current
// typed shape.
func TestParseUnverified_SchemaTooNew(t *testing.T) {
	is := is.New(t)

	raw := []byte(`{
		"payload": {
			"schemaVersion": 999,
			"index": {"version": 1, "timestamp": "2026-01-01T00:00:00Z"},
			"connectors": [{"totally": "not the current shape"}]
		},
		"signatures": []
	}`)

	_, err := index.ParseUnverified(raw)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, index.CodeSchemaTooNew)
}

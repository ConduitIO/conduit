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
	"strings"
	"testing"

	"github.com/matryer/is"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry/index"
)

func TestCheckNoDuplicateKeys_ValidSampleIndex(t *testing.T) {
	is := is.New(t)
	raw, err := os.ReadFile("testdata/sample-index.json")
	is.NoErr(err)
	is.NoErr(index.CheckNoDuplicateKeys(raw))
}

// TestCheckNoDuplicateKeys_TopLevel proves rejection at the outermost
// object, not just nested ones.
func TestCheckNoDuplicateKeys_TopLevel(t *testing.T) {
	is := is.New(t)
	raw := []byte(`{"payload": {}, "signatures": [], "payload": {}}`)
	err := index.CheckNoDuplicateKeys(raw)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, index.CodeIndexIntegrity)
}

// TestCheckNoDuplicateKeys_DeeplyNested proves rejection at a nesting level
// well below the schema's real depth, not only at the top level — a
// producer/verifier parser differential is exactly as dangerous buried
// inside connectors[].publisher as it is at the envelope's own top level.
func TestCheckNoDuplicateKeys_DeeplyNested(t *testing.T) {
	is := is.New(t)
	raw := []byte(`{
		"payload": {
			"schemaVersion": 1,
			"index": {"version": 1, "timestamp": "2026-01-01T00:00:00Z"},
			"connectors": [
				{
					"name": "postgres",
					"publisher": {
						"expectedOIDCIssuer": "a",
						"expectedIdentityPattern": "^a$",
						"expectedOIDCIssuer": "b"
					},
					"versions": []
				}
			]
		},
		"signatures": []
	}`)
	err := index.CheckNoDuplicateKeys(raw)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, index.CodeIndexIntegrity)
}

// TestCheckNoDuplicateKeys_NoFalsePositiveAcrossSiblingObjects proves the
// walker scopes "seen keys" per-object, not globally — two different
// objects (e.g. two connectors) legitimately sharing a key name (like
// "name") must never be flagged.
func TestCheckNoDuplicateKeys_NoFalsePositiveAcrossSiblingObjects(t *testing.T) {
	is := is.New(t)
	raw := []byte(`{"a": {"name": "x"}, "b": {"name": "y"}}`)
	is.NoErr(index.CheckNoDuplicateKeys(raw))
}

// TestCheckNoDuplicateKeys_NestingCapRefusesNotPanics is the P0-2 property
// test: an adversarial index with far more nesting than the real schema
// ever needs must be refused with CodeIndexNestingTooDeep, never crash the
// process (no stack overflow), regardless of how deep the input goes.
func TestCheckNoDuplicateKeys_NestingCapRefusesNotPanics(t *testing.T) {
	is := is.New(t)

	const depth = 100_000 // far beyond any real index and beyond the 64 cap
	raw := strings.Repeat(`{"a":`, depth) + "1" + strings.Repeat("}", depth)

	err := index.CheckNoDuplicateKeys([]byte(raw))
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, index.CodeIndexNestingTooDeep)
}

func TestCheckNoDuplicateKeys_NestingCapArray(t *testing.T) {
	is := is.New(t)

	const depth = 100_000
	raw := strings.Repeat(`[`, depth) + strings.Repeat(`]`, depth)

	err := index.CheckNoDuplicateKeys([]byte(raw))
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, index.CodeIndexNestingTooDeep)
}

func TestCheckNoDuplicateKeys_MalformedJSONNeverPanics(t *testing.T) {
	inputs := []string{
		``,
		`{`,
		`}`,
		`{"a":}`,
		`{"a": "unterminated`,
		`not json at all`,
		"\xff\xfe\x00",
		`{"a": 1 "b": 2}`, // missing comma
	}
	for _, in := range inputs {
		// Must return without panicking; the specific error (if any) isn't
		// asserted here beyond "no crash" — malformed-input handling.
		_ = index.CheckNoDuplicateKeys([]byte(in))
	}
}

// FuzzDuplicateKeyWalk is the P0-2 native Go fuzz target for the
// duplicate-key walker (plan-v2 §2.4 item 3): arbitrary bytes, valid JSON
// or not, must never panic/crash the process.
func FuzzDuplicateKeyWalk(f *testing.F) {
	sample, err := os.ReadFile("testdata/sample-index.json")
	if err == nil {
		f.Add(sample)
	}
	f.Add([]byte(`{"a":1,"a":2}`))
	f.Add([]byte(strings.Repeat(`{"a":`, 200) + "1" + strings.Repeat("}", 200)))
	f.Add([]byte(strings.Repeat(`[`, 200) + strings.Repeat(`]`, 200)))
	f.Add([]byte(`{"a": "` + strings.Repeat("x", 10000) + `"}`))
	f.Add([]byte(`{"a": "\xc3\x28"}`)) // invalid UTF-8 continuation
	f.Add([]byte(``))
	f.Add([]byte(`{`))
	f.Add([]byte(`null`))
	f.Add([]byte(`42`))
	f.Add([]byte(`"just a string"`))

	f.Fuzz(func(t *testing.T, data []byte) {
		// The only invariant under fuzzing: never panic. Any error return
		// (or none) is acceptable; we are hunting crashes, not asserting a
		// specific outcome for arbitrary bytes.
		_ = index.CheckNoDuplicateKeys(data)
	})
}

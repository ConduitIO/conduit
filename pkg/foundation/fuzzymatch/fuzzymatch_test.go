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

package fuzzymatch

import (
	"slices"
	"strings"
	"testing"

	"github.com/matryer/is"
)

// builtins is the real built-in connector list
// (pkg/plugin/connector/builtin/registry.go). Tests use the actual names rather
// than invented ones so the similarity floor is tuned against the distances
// that occur in practice, not against a convenient fixture.
var builtins = []string{"file", "generator", "kafka", "log", "postgres", "s3"}

func TestSuggest_RealTypos(t *testing.T) {
	// The cases the design doc cites by name (§7), plus the shapes users
	// actually produce.
	tests := []struct {
		name string
		want string
		exp  []string
	}{
		{"omission", "postgre", []string{"postgres"}},
		{"transposition", "kafak", []string{"kafka"}},
		{"insertion", "postgress", []string{"postgres"}},
		{"substitution", "kafla", []string{"kafka"}},
		{"case difference", "POSTGRES", []string{"postgres"}},
		{"mixed case typo", "Postgre", []string{"postgres"}},
		{"exact match is returned, not filtered", "kafka", []string{"kafka"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			is.Equal(Suggest(tt.want, builtins, 3), tt.exp)
		})
	}
}

// TestSuggest_TranspositionIsCovered pins the design doc's stated reason for
// choosing plain Levenshtein over Damerau-Levenshtein: transpositions cost 2
// edits instead of 1, which is still inside the floor for realistic names. If
// this ever fails, the doc's justification is wrong and the algorithm choice
// has to be revisited rather than the test relaxed.
func TestSuggest_TranspositionIsCovered(t *testing.T) {
	is := is.New(t)
	is.Equal(levenshtein([]rune("kafak"), []rune("kafka")), 2)
	is.Equal(Suggest("kafak", builtins, 1), []string{"kafka"})
}

// TestSuggest_NeverFabricates is the invariant that matters most.
//
// `conduit generate` feeds the suggestion back into a model that acts on it, so
// a confident wrong name is worse than silence. Asking for a connector that
// genuinely is not installed must produce NOTHING — not the nearest of six
// unrelated names.
func TestSuggest_NeverFabricates(t *testing.T) {
	is := is.New(t)

	// Real connectors that exist but are not built in. None is within edit
	// distance of any builtin.
	for _, absent := range []string{"mysql", "mongo", "snowflake", "redis", "bigquery"} {
		is.Equal(Suggest(absent, builtins, 3), nil)
	}
}

// TestSuggest_FloorIsLooserOfTwoBounds pins the max(absolute, relative) rule
// from the design doc. A flat 2-edit tolerance would reject real typos in long
// names; a flat 30% would accept nonsense in short ones. Each half of the rule
// is checked by the case the other half fails.
func TestSuggest_FloorIsLooserOfTwoBounds(t *testing.T) {
	is := is.New(t)

	// Short name: 30% of 3 is 0, so only the absolute bound saves this.
	is.Equal(Suggest("lgo", []string{"log"}, 1), []string{"log"})

	// Long name: 4 edits ("gres" dropped) — past the absolute bound of 2, but
	// inside 30% of the 22-character request (6). Distance must be strictly
	// greater than absoluteTolerance or this case passes under an
	// absolute-only rule too and proves nothing.
	longName := "conduit-connector-postgres"
	is.Equal(levenshtein([]rune("conduit-connector-post"), []rune(longName)), 4)
	is.Equal(Suggest("conduit-connector-post", []string{longName}, 1), []string{longName})

	// ...and the relative bound still has a limit: 12 edits on a 26-character
	// name is not a typo.
	is.Equal(Suggest("conduit-connector-postgres", []string{"totally-different-string!!"}, 1), nil)
}

// TestSuggest_DeterministicAcrossCandidateOrder pins invariant 1. Callers put
// these strings into error messages and golden files; output that reordered
// with the caller's map iteration could not be asserted on or alerted on.
func TestSuggest_DeterministicAcrossCandidateOrder(t *testing.T) {
	is := is.New(t)

	// All three are edit distance 1 from "xy", so only the tie-break orders them.
	candidates := []string{"xyc", "xya", "xyb"}
	expected := []string{"xya", "xyb", "xyc"}

	is.Equal(Suggest("xy", candidates, 3), expected)

	// Reversing the input must not change the output.
	reversed := slices.Clone(candidates)
	slices.Reverse(reversed)
	is.Equal(Suggest("xy", reversed, 3), expected)

	// And nearer matches still outrank the tie-break: distance beats name.
	is.Equal(Suggest("xya", []string{"xyz", "xya"}, 2), []string{"xya", "xyz"})
}

// TestSuggest_DuplicatesDoNotConsumeSlots pins that a repeated candidate cannot
// crowd out a genuinely different alternative. Callers assemble candidate lists
// by concatenating catalogs (built-in + installed + registry), so duplicates
// are the normal case, not a pathological one.
func TestSuggest_DuplicatesDoNotConsumeSlots(t *testing.T) {
	is := is.New(t)

	candidates := []string{"postgres", "postgres", "postgres", "postgre"}
	is.Equal(Suggest("postgres", candidates, 2), []string{"postgres", "postgre"})
}

func TestSuggest_RespectsMaxSuggestions(t *testing.T) {
	is := is.New(t)

	candidates := []string{"xya", "xyb", "xyc", "xyd"}
	is.Equal(len(Suggest("xy", candidates, 2)), 2)
	is.Equal(Suggest("xy", candidates, 1), []string{"xya"})
	is.Equal(len(Suggest("xy", candidates, 100)), 4)
}

func TestSuggest_EdgeCases(t *testing.T) {
	is := is.New(t)

	is.Equal(Suggest("", builtins, 3), nil)          // no request
	is.Equal(Suggest("postgres", nil, 3), nil)       // no catalog
	is.Equal(Suggest("postgres", builtins, 0), nil)  // caller wants none
	is.Equal(Suggest("postgres", builtins, -1), nil) // defensive

	// Empty candidates are skipped rather than matched. An empty plugin name is
	// not a suggestion anyone can act on, and it is within distance of any
	// short name.
	is.Equal(Suggest("s3", []string{"", "s3"}, 3), []string{"s3"})
}

func TestLevenshtein_KnownDistances(t *testing.T) {
	tests := []struct {
		a, b string
		exp  int
	}{
		{"", "", 0},
		{"", "abc", 3},
		{"abc", "", 3},
		{"abc", "abc", 0},
		{"kitten", "sitting", 3}, // the canonical example
		{"flaw", "lawn", 2},
		{"postgres", "postgre", 1},
		{"kafka", "kafak", 2},
	}

	for _, tt := range tests {
		t.Run(tt.a+"/"+tt.b, func(t *testing.T) {
			is := is.New(t)
			is.Equal(levenshtein([]rune(tt.a), []rune(tt.b)), tt.exp)
			// Edit distance is symmetric; an asymmetric implementation would
			// make results depend on argument order.
			is.Equal(levenshtein([]rune(tt.b), []rune(tt.a)), tt.exp)
		})
	}
}

// TestLevenshtein_MultiByteRunes pins that distance counts characters, not
// bytes. The package targets ASCII plugin names, but the input reaches it from
// natural-language prompts and model output, so it must not mis-slice or
// over-count on UTF-8.
func TestLevenshtein_MultiByteRunes(t *testing.T) {
	is := is.New(t)

	// One character replaced by another, both multi-byte: distance 1, not the
	// byte-level distance.
	is.Equal(levenshtein([]rune("café"), []rune("cafè")), 1)
	is.Equal(levenshtein([]rune("日本語"), []rune("日本")), 1)
}

// FuzzSuggest asserts the two package invariants hold for arbitrary input:
// nothing is ever fabricated, and the cap is always respected. It also covers
// the panic surface — rune slicing and the two-row matrix are the kind of index
// arithmetic that fails on inputs nobody thought to write a case for.
func FuzzSuggest(f *testing.F) {
	f.Add("postgres", "file,generator,kafka,log,postgres,s3", 3)
	f.Add("", "", 0)
	f.Add("kafak", "kafka", 1)
	f.Add("日本語", "日本,語", 2)
	f.Add("\x00\x00", ",,,", 5)

	f.Fuzz(func(t *testing.T, want, joined string, maxSuggestions int) {
		candidates := strings.Split(joined, ",")

		got := Suggest(want, candidates, maxSuggestions)

		if maxSuggestions > 0 && len(got) > maxSuggestions {
			t.Fatalf("returned %d suggestions, cap was %d", len(got), maxSuggestions)
		}

		for _, g := range got {
			if !slices.Contains(candidates, g) {
				t.Fatalf("fabricated suggestion %q not present in candidates", g)
			}
		}

		// Results must be ordered nearest-first, or callers cannot present the
		// first element as "the" suggestion.
		wantFolded := []rune(strings.ToLower(want))
		for i := 1; i < len(got); i++ {
			prev := levenshtein(wantFolded, []rune(strings.ToLower(got[i-1])))
			curr := levenshtein(wantFolded, []rune(strings.ToLower(got[i])))
			if prev > curr {
				t.Fatalf("results out of order: %q (d=%d) before %q (d=%d)", got[i-1], prev, got[i], curr)
			}
		}
	})
}

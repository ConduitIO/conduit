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
)

// absoluteTolerance is the edit-distance floor that applies regardless of name
// length. Short names need it: a purely proportional bound on a 5-character
// name allows only one edit, which rejects most real typos.
const absoluteTolerance = 2

// relativeTolerancePercent is the edit-distance budget as a percentage of the
// requested name's length. Long names need it: a flat 2-edit tolerance on a
// 20-character name is stricter than a user typing it by hand ever manages.
const relativeTolerancePercent = 30

// Suggest returns the candidates closest to want by case-insensitive
// Levenshtein distance, nearest first, capped to maxSuggestions.
//
// It returns nil rather than a weak guess when nothing clears the similarity
// floor. That is the point of the floor: callers print the result as advice,
// and `conduit generate` feeds it back into a model that will act on it, so a
// confident wrong name is worse than silence. Every returned string is an
// element of candidates — this never synthesizes a name.
//
// Ties are broken lexicographically so output is stable across runs and across
// the caller's candidate ordering. Callers put these strings in error messages
// and golden files; a suggestion list that reordered between runs could not be
// asserted on.
//
// An exact (case-insensitive) match is returned rather than filtered out. It
// costs nothing, and silently dropping it would make the one case where the
// caller's own lookup and this function disagree the hardest one to debug.
func Suggest(want string, candidates []string, maxSuggestions int) []string {
	if maxSuggestions <= 0 || want == "" || len(candidates) == 0 {
		return nil
	}

	wantFolded := []rune(strings.ToLower(want))
	// The budget is the LOOSER of the two bounds, so short and long names are
	// both served: max(2, 30% of length). Integer division floors, which keeps
	// the relative bound the stricter of two readings.
	budget := max(absoluteTolerance, len(wantFolded)*relativeTolerancePercent/100)

	type scored struct {
		name string
		dist int
	}

	matches := make([]scored, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		if c == "" {
			continue
		}
		// A duplicated candidate must not occupy two of the maxSuggestions
		// slots and crowd out a genuinely different alternative.
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}

		d := levenshtein(wantFolded, []rune(strings.ToLower(c)))
		if d <= budget {
			matches = append(matches, scored{name: c, dist: d})
		}
	}

	slices.SortFunc(matches, func(a, b scored) int {
		if a.dist != b.dist {
			return a.dist - b.dist
		}
		return strings.Compare(a.name, b.name)
	})

	// nil, not an empty slice: the doc contract is "nil rather than a weak
	// guess", and callers branch on `len(s) == 0` or `s == nil` interchangeably
	// only if the two never diverge.
	if len(matches) == 0 {
		return nil
	}

	if len(matches) > maxSuggestions {
		matches = matches[:maxSuggestions]
	}

	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m.name
	}
	return out
}

// levenshtein returns the edit distance between two rune slices: the minimum
// number of single-character insertions, deletions, or substitutions to turn a
// into b.
//
// Callers must fold case before calling — this compares runes as given.
//
// Only two rows of the full matrix are kept. The matrix is O(len(a)*len(b)) in
// memory and this runs once per candidate against the whole connector catalog,
// inside a generate retry loop; two rows is the same algorithm with the rows
// that can no longer be read dropped.
func levenshtein(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)

	// Row 0: turning an empty prefix of a into b's prefix costs one insertion
	// per character.
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		curr[0] = i // delete every character of a's prefix
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(
				prev[j]+1,      // deletion
				curr[j-1]+1,    // insertion
				prev[j-1]+cost, // substitution (or free match)
			)
		}
		prev, curr = curr, prev
	}

	return prev[len(b)]
}

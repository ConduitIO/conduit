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

// Package fuzzymatch answers "you asked for X, which is not a thing — did you
// mean Y?" for user- and model-supplied names.
//
// # Why this exists
//
// Two commands have been blocked on it. The `repair` design doc
// (docs/design-documents/20260712-repair-command.md, §6) left the
// "connector plugin not found" class unrepairable — "the correct plugin name is
// not mechanically knowable... Deferred until a did-you-mean index exists".
// `conduit generate` then made it a hard prerequisite rather than polish: its
// acceptance bar says an unknown connector must produce a closest match and an
// install suggestion, *never a fabricated plugin name*
// (docs/design-documents/20260722-conduit-generate.md, §7).
//
// That second use is the demanding one. When an LLM invents a connector,
// feeding "`postgre` does not exist; did you mean `postgres`?" back into the
// retry prompt turns a hallucination into a self-correction inside the retry
// budget, instead of a terminal failure. So this is shared infrastructure with
// two named consumers, not a helper for one caller.
//
// # Invariants
//
// Two properties are load-bearing, and both are enforced by test:
//
//  1. Output is deterministic. Results are ordered by edit distance, then
//     lexicographically. An error message whose wording depends on map
//     iteration order cannot be asserted on, alerted on, or trusted in a
//     golden file.
//
//  2. A suggestion is never fabricated. Every returned string is an element of
//     candidates, and nothing is returned unless it clears the similarity
//     floor. Returning a wrong-but-confident name is worse than returning
//     nothing: the caller prints it as advice, and for `generate` it is fed
//     back to a model that will act on it.
//
// # Scope
//
// Plain Levenshtein, not Damerau-Levenshtein. Real connector-name typos are
// substitutions, omissions, and insertions (`postgre`/`postgres`,
// `kafak`/`kafka`), not adjacent transpositions, and nothing in the acceptance
// bar needs the extra operation. Simpler to audit for no loss in coverage.
//
// Matching is case-insensitive: names arrive from natural-language prompts,
// which have no reliable casing convention.
package fuzzymatch

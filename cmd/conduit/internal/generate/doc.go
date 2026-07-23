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

// Package generate is the eval-harness half of `conduit generate`
// (docs/design-documents/20260722-conduit-generate.md). It contains no CLI
// command, no MCP tool, and no LLM provider client — those are the v0.20
// implementation, deferred per that design doc's Status section (the v0.19
// build slot went to the Python connector SDK). No new external dependency
// ships with this package.
//
// What v0.19 ships here is the eval harness the future `generate` command's
// acceptance bar (design doc §10) is measured against, plus the committed
// benchmark corpus it's measured over:
//
//   - The corpus: testdata/eval_requests.yaml, >= 25 canonical natural-
//     language pipeline requests, each grounded in one of Conduit's real
//     built-in connectors (file, generator, kafka, log, postgres, s3) and
//     carrying its expected semantic intent (source/destination category,
//     required processor capabilities) — see Request/Expect in fixture.go.
//     docs/generate-benchmark.md is the human-readable rendering of this
//     same file; the YAML is the source of truth.
//
//   - The scorer: given a Request->candidate-pipeline-YAML mapping (however
//     produced — a live provider call in v0.20, or a fixture in this
//     package's own tests today), ScoreRun/ScoreMedian report TWO separate
//     numbers, never collapsed into one:
//
//     1. Validate-pass rate — does the candidate pass the exact
//     cmd/conduit/internal/validate engine `conduit pipelines validate`
//     uses. Because that engine only exposes a path-based Run (there is no
//     RunBytes/RunReader in-memory seam yet — the design doc's Decision §3
//     names this exact gap and its two resolutions), this package uses the
//     documented fallback: a private (0600) temp file, validated, then
//     removed (validateCandidate in validate_score.go). The day RunBytes
//     lands, this is the only function that needs to change.
//
//     2. Semantic-intent-match rate — does the candidate's actual source/
//     destination connector category and processor set match the
//     Request's Expect, even when it validates cleanly. A pipeline can be
//     schema-valid and still point at the wrong connector or drop a
//     requested filter; this is the DX-review failure mode the design doc
//     names as the reason a validate-pass rate alone isn't a sufficient
//     bar (semantic_score.go).
//
// # Invariants this package holds
//
//   - A Request.ID with no entry in the candidates map counts as a FAIL on
//     BOTH metrics (Result.Missing), never silently excluded from the
//     denominator — a shrunk denominator would hide the worst failure (no
//     candidate produced at all) behind a better-looking rate.
//   - Medians, never best-of, across repeated runs (ScoreMedian) — the same
//     discipline CLAUDE.md requires of benchi results, applied here because
//     an eval harness that could be gamed by cherry-picking its best run is
//     not a release gate, it's theater.
//   - The scorer never calls an LLM provider and never makes a network
//     call — it is pure, deterministic (given a fixed candidates map), and
//     proven correct with fixture-only tests (fixture_test.go): known-good
//     candidates score true on both axes, known-bad ones score false on the
//     specific axis they're wrong on, with no live model in the loop.
package generate

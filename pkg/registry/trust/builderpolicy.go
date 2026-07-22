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

package trust

// ExpectedBuilderID is the single, global expected SLSA predicate.builder.id
// value (plan-v2 §9 / P1-2 — no per-connector policy: every connector name
// is meant to publish through the SAME shared build pipeline, whether
// directly via a minimal generator invocation (the Phase-1 seed-connector
// bootstrap, plan-v2 §9) or via the eventual
// conduitio/connector-publish-action wrapping it (PR-3)). CheckProvenanceBinding
// enforces an EXACT match against this constant unconditionally — P1-2's
// "enforced from day one" requirement: there is no soft/warn-only period
// where provenance is accepted without builder-ID binding.
//
// FLAGGED for confirmation (plan-v2 §12 item 2, R-1 OQ8): the exact value is
// genuinely blocked on epic step 4's (the publish Action, not yet built)
// final choice of SLSA generator and pinned ref. This constant is set to the
// well-known, publicly documented slsa-github-generator v2 "generic"
// builder's reusable-workflow ref
// (https://github.com/slsa-framework/slsa-github-generator) as the
// plan-recommended default (§1 "SLSA Level 3 via
// slsa-framework/slsa-github-generator"). Confirm this exact ref against
// whichever workflow the Phase-1 seed-connector bootstrap (plan-v2 §9) and,
// later, conduitio/connector-publish-action (PR-3) actually pin — a change
// here is a breaking change for every already-registered connector identity
// and must go through the same human-reviewed rigor as an identity change
// (R-1 §d), not a routine dependency bump.
const ExpectedBuilderID = "https://github.com/slsa-framework/slsa-github-generator/.github/workflows/generator_generic_slsa3.yml@refs/tags/v2.1.0"

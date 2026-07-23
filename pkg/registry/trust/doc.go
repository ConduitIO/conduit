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

// Package trust is identity-pinned signature and SLSA provenance
// verification for connector artifacts — the authorization gate described
// in docs/design-documents/20260714-connector-registry-index-schema.md
// (R-1 §c) and plan-v2 §2.

// # Public, not internal — same reason as pkg/registry/index
//
// index-CI's own re-verification tooling imports this package's exported
// surface directly (a pinned github.com/conduitio/conduit module version),
// so the client and index-CI run identical verification code — see
// pkg/registry/index's doc comment for the full rationale, which applies
// here unchanged.
//
// # What ships now vs. later (PR-0 vs PR-2)
//
// PR-0 ships only what has zero sigstore/cosign dependency and is fully
// testable today: PinnedIdentity (types.go), the three sentinel errors
// (errors.go), and ValidateIdentityPattern (identitypattern.go) — the
// registration-checklist tightness rules (anchoring, no weakening inline
// RE2 flags, a literal prefix that actually pins an owner/repo) that
// plan-v2 §10 requires index-CI to lint on every new registration.
//
// The real cryptographic bodies — VerifyArtifactSignature/
// VerifyAttestationEnvelope (sigstore-go wiring), CheckProvenanceBinding
// (subject-digest + builder.id/configSource.uri matching), and the
// ExpectedBuilderID constant (blocked on the bootstrap ceremony, plan-v2
// §9) — are deliberately NOT stubbed out in this PR. Two of the three
// (provenance binding's exact matching rule, and per-artifact-vs-
// per-version provenance shape) are still-open questions in R-1's own
// OPEN QUESTIONS section (OQ6, OQ8); freezing a function signature ahead of
// those being resolved risks locking in a wrong shape before PR-2's actual
// design is settled. Nothing in PR-0 or PR-1 calls these functions —
// pkg/registry.FailClosedVerifier.VerifyArtifact refuses unconditionally
// without reaching this package at all — so there is no compile-target
// this package needs to satisfy yet. The shared adversarial fixture corpus
// (plan-v2 §11, P1-4) that will exercise these bodies against known-bad
// fixtures also ships in PR-2, alongside the bodies it tests.
package trust

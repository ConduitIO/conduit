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

// Package index parses, freezes, and (eventually) verifies the signed
// connector registry index — the security-critical foundation `install`,
// `audit`, and the publish Action all trust. It implements
// docs/design-documents/20260714-connector-registry-index-schema.md (R-1,
// frozen) and plan-v2 §2 ("shared foundations").
//
// # What this package is public, not internal
//
// index-CI's own independent re-verification tooling (a separate repo) must
// run the IDENTICAL verification code the client runs — otherwise "index-CI
// re-verifies everything" is a comment, not a fact. Index-CI pins an exact
// github.com/conduitio/conduit module version and bumps it deliberately
// (never @latest), per plan-v2 §12 item 3's API-stability commitment: once a
// tagged release ships this package, its exported surface is a versioned
// API with the same announce → warn → remove discipline as config/protocol.
//
// # What ships now vs. later (PR-0 vs PR-2)
//
// This package's PR-0 scope is everything that does NOT require a signing
// key to exist yet: envelope parsing, duplicate-key rejection with a
// nesting-depth cap (P0-2), JCS (RFC 8785) canonicalization, the typed
// schema structs (schema_v1.go, golden-round-tripped against
// registry-index/sample-index.json), rollback/staleness comparisons
// (freeze.go, pure functions over already-parsed values), the persisted
// high-water-mark state file (state.go), and a bounded index fetch
// (fetch.go). ParseUnverified performs a shape/schema check ONLY — it makes
// no claim about the signatures envelope and must never be mistaken for a
// trust decision.
//
// Verify — the real, cryptographically-verifying counterpart that checks
// signatures against build-time-fixed TrustAnchors — lands in PR-2 (Tier 1,
// human sign-off required), once the root/freshness key material exists
// (plan-v2 §9's bootstrap ceremony). Nothing in this build calls Verify or
// treats a ParseUnverified result as trusted for any security decision;
// pkg/registry.FailClosedVerifier enforces that structurally.
//
// # Invariants
//
//   - Invariant 6 (schema handling never silently mangles data): a
//     schemaVersion newer than this build understands refuses
//     (CodeSchemaTooNew) rather than guessing at an unknown shape; a
//     duplicate JSON key at any nesting level refuses rather than resolving
//     silently to "last key wins" (a real producer/verifier parser
//     differential — see duplicatekey.go).
//   - Everything a client trusts for a security decision must live inside
//     the signed payload; the outer envelope's signatures array is the only
//     thing outside it, and a signature cannot cover itself.
package index

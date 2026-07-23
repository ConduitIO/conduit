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

// Package registry is the connector registry's top-level orchestration:
// resolving a connector name/version against the signed index and
// installing it (docs/design-documents/20260713-connector-registry-mvp.md,
// the epic; plan-v2, the consolidated implementation plan).
//
// # This PR's scope (PR-0 — shared foundations, Tier 2)
//
// This package ships only the contracts every later PR compiles against —
// no install pipeline, no verification implementation. Concretely:
//
//   - IndexVerifier / ArtifactVerifier (verify.go): the two-interface seam
//     PR-1 (install core) and PR-2 (trust core) both build against, fixing
//     a real compile error in the original step-plans' single-receiver,
//     duplicate-method-name sketch (plan-v2 §2.2).
//   - FailClosedVerifier (verify.go): the ONLY IndexVerifier/ArtifactVerifier
//     wired into production Install until PR-2 lands. It performs a
//     shape/schema check only (via pkg/registry/index.ParseUnverified) and
//     unconditionally refuses artifact verification — this package
//     introduces NO code path that can accept an artifact as verified.
//   - Manifest (manifest.go): the name@version-keyed install-manifest
//     format (plan-v2 §3) — the load-bearing fix letting two pipelines pin
//     two different versions of one connector simultaneously.
//   - NormalizeVersion (semver.go): the one, only version-comparison
//     primitive (plan-v2 §5) — semver equality tolerating a leading "v" —
//     every version comparison anywhere in this codebase must go through
//     this, never a bare string comparison.
//   - The full canonical error-code table (codes.go, plan-v2 §4), including
//     codes no PR-0 code path triggers yet, registered now so docs/llms.txt
//     generation has one complete, stable source from the first PR.
//
// # Fail-closed by construction
//
// Nothing in this package can install, verify, or otherwise accept a
// connector artifact. FailClosedVerifier.VerifyArtifact always returns
// ErrVerificationNotConfigured; VerifyIndex only ever produces a
// VerifiedIndex with Verified: false. The real install orchestrator
// (resolve.go, download.go, extract.go, lock.go, install.go — PR-1) and the
// real cryptographic verification bodies (PR-2, Tier 1) are deliberately
// not part of this PR.
package registry

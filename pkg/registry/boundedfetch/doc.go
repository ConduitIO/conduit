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

// Package boundedfetch is the shared size-capped fetch primitive behind
// P0-2 (plan-v2 §2.4 item 1): attacker-controlled bytes (an index, a
// connector artifact, a signature bundle, a provenance bundle) are always
// read through an io.LimitReader capped one byte past the caller's limit,
// so a response that would exceed the cap is distinguished (ErrTooLarge)
// from one that happens to land exactly on it, and from any other fetch
// failure.
//
// # Package placement
//
// plan-v2 §2.1 places this primitive at pkg/registry/boundedfetch.go (the
// registry root package). This module places it in its own leaf package
// instead: pkg/registry (root) constructs pkg/registry/index.VerifiedIndex
// and takes a pkg/registry/trust.PinnedIdentity, so it must import both —
// and pkg/registry/index's own index fetch (see index/fetch.go) needs this
// exact primitive too. Putting it at the registry root would make
// pkg/registry/index import pkg/registry, which already imports
// pkg/registry/index — an import cycle. A tiny, dependency-free leaf
// package that pkg/registry, pkg/registry/index, and (later) pkg/registry/
// trust can all import without importing each other resolves this cleanly;
// it is the same shared helper plan-v2 asks for, just not at the literal
// file path its package-layout sketch names. Flagged explicitly as a
// necessary structural correction, in the same spirit as plan-v2's own §0
// "coherence gap" fixes over the original step-plans.
//
// # What this package does NOT decide
//
// A too-large response is reported as the single, generic ErrTooLarge —
// this package registers no conduiterr.Code of its own, because "too
// large" means something different (and carries a different registered
// code and remediation) depending on what was being fetched:
// index.CodeIndexTooLarge for the index itself, trust.CodeBundleTooLarge
// for a signature/provenance bundle. Callers translate ErrTooLarge into
// their own domain-specific code.
package boundedfetch

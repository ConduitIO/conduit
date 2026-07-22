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

package index

import "crypto/ed25519"

// TrustAnchors holds the build-time-fixed root and freshness public keys
// compiled into the conduit binary (R-1 OQ1/OQ2 resolution: trust anchors
// are fixed at build time and are NEVER updated at runtime — there is no
// runtime-fetched rotation statement, which would let a compromised old key
// forge a rotation to an attacker key).
//
// No key material exists yet: it is generated during the bootstrap
// ceremony (plan-v2 §9) and go:embed'd once PR-2 lands. This type ships in
// PR-0, empty, purely so the eventual Verify(raw []byte, anchors
// TrustAnchors) (*VerifiedIndex, error) signature can be frozen ahead of
// the key material existing — PR-1's and PR-2's code can compile against
// this shape without waiting on the ceremony.
type TrustAnchors struct {
	// Roots holds every currently-trusted root public key, keyed by keyId
	// (e.g. "sha256:<hex fingerprint of the SPKI-encoded public key>").
	// During a rotation window both the outgoing and incoming root key are
	// present simultaneously (R-1 OQ2) for a retention window tracking
	// Conduit's supported-version policy.
	Roots map[string]ed25519.PublicKey
	// Freshness holds the currently-trusted freshness public key(s), keyed
	// by keyId. A freshness signature may only extend index.timestamp/bump
	// index.version over byte-identical connectors[]; it never authorizes
	// content on its own (R-1 OQ3).
	Freshness map[string]ed25519.PublicKey
}

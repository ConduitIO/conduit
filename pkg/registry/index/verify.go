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

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"

	json "github.com/goccy/go-json"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// VerifiedIndex wraps a parsed Payload together with an explicit Verified
// flag, so a caller can never mistake a ParseUnverified result (schema-valid
// bytes, cryptographically unchecked) for a trusted one by accident.
// pkg/registry.FailClosedVerifier always sets Verified: false; the install
// pipeline asserts Verified == true before using any field for a security
// decision — a second, structural belt-and-suspenders check on top of
// pkg/registry.ArtifactVerifier unconditionally refusing until a real
// verifier is wired in (plan-v2 §2.2).
type VerifiedIndex struct {
	Payload  Payload
	Verified bool
	// RootVerified is true only when acceptance came from a role:"root"
	// signature — as opposed to a role:"freshness" signature accepted
	// because connectors[] was byte-identical to the last root-verified
	// content (R-1 §a.2.c). Callers persisting State.LastVerifiedConnectorsHash
	// (via HashConnectors) should only update it when RootVerified is true:
	// a freshness-only acceptance by construction already matches the
	// existing persisted hash, so re-deriving it is a no-op at best and,
	// if ever wrong, must never widen what freshness alone can authorize.
	RootVerified bool
}

// ParseUnverified parses raw index bytes into a typed Payload WITHOUT
// checking any signature — it is a shape/schema check only, in bold: it
// makes NO claim about the signatures envelope, and its result must never
// be used for a security decision. Concretely, in the order R-1 §a
// requires:
//
//  1. Reject any duplicate JSON key at any nesting level (CheckNoDuplicateKeys,
//     P0-2) — before and independent of interpreting any field.
//  2. Extract payload/signatures generically (schemaVersion is not yet
//     trusted at this point, so nothing schema-version-specific has run).
//  3. Peek payload.schemaVersion and refuse (CodeSchemaTooNew) anything
//     newer than MaxSupportedSchemaVersion, "upgrade Conduit" — before
//     attempting the typed unmarshal, so an unrecognized future shape never
//     reaches struct decoding.
//  4. Unmarshal payload into the typed schemaVersion-1 Payload struct.
//
// The real, cryptographically-verifying counterpart (Verify, checking
// signatures against build-time-fixed TrustAnchors per R-1 §a step 2c) is
// implemented below — nothing in this build may treat ParseUnverified's
// result as trusted for any security decision; see pkg/registry.FailClosedVerifier.
func ParseUnverified(raw []byte) (*Payload, error) {
	env, err := decodeEnvelope(raw)
	if err != nil {
		return nil, err
	}
	return decodeTypedPayload(env.Payload)
}

// Verify is the cryptographically-verifying counterpart to ParseUnverified,
// implementing R-1 §a steps 2(a)-(d) in full:
//
//  1. Duplicate-key rejection (shared with ParseUnverified, via decodeEnvelope).
//  2. JCS-canonicalize the raw payload bytes.
//  3. For each signatures[] entry, resolve keyId against the compiled-in
//     anchors for that entry's declared role ("root" or "freshness") and
//     verify the ed25519 signature over the canonical bytes.
//  4. Accept if any role:"root" signature verifies. Otherwise, accept if a
//     role:"freshness" signature verifies AND the payload's connectors[]
//     (canonicalized, hashed via HashConnectors) is byte-identical to
//     lastVerifiedConnectorsHash — the last root-verified content on
//     record (persisted in State, R-1 §a.2.c: "a freshness signature may
//     extend freshness, never authorize content").
//  5. Only once accepted, peek schemaVersion and unmarshal into the typed
//     Payload (shared with ParseUnverified, via decodeTypedPayload).
//
// Distinguishes two failure modes as DISTINCT errors, never conflated
// (R-1 §a.2.c):
//
//   - ErrTrustAnchorExpired (CodeTrustAnchorExpired): no keyId in
//     signatures[] matches ANY compiled-in anchor (root or freshness) at
//     all — "upgrade Conduit", fail closed, never fall back to a stale
//     cache.
//   - ErrIndexIntegrity (CodeIndexIntegrity): at least one keyId WAS
//     recognized, but no recognized signature both verified cryptographically
//     AND was sufficient to authorize this content (a root signature that
//     fails crypto; a freshness signature that verifies crypto but doesn't
//     match lastVerifiedConnectorsHash; a duplicate key; malformed
//     signature bytes) — tampering/corruption, refuse and report loudly.
//
// lastVerifiedConnectorsHash is the caller's persisted
// State.LastVerifiedConnectorsHash (empty string if no index has ever been
// root-verified on this machine — R-1 §b's documented first-fetch gap: a
// freshness-only index can never be the FIRST index a client ever accepts,
// by construction, since an empty hash can never equal a real one).
func Verify(raw []byte, anchors TrustAnchors, lastVerifiedConnectorsHash string) (*VerifiedIndex, error) {
	env, err := decodeEnvelope(raw)
	if err != nil {
		return nil, err
	}

	canonical, err := Canonicalize(env.Payload)
	if err != nil {
		return nil, conduiterr.Wrap(CodeIndexIntegrity, "index payload could not be canonicalized", err)
	}

	var anyKeyRecognized, rootVerified, freshnessCryptoVerified bool
	for _, sig := range env.Signatures {
		key, recognized := resolveAnchor(anchors, sig.Role, sig.KeyID)
		if !recognized {
			continue
		}
		anyKeyRecognized = true

		if sig.Algorithm != "ed25519" {
			continue // recognized key, unsupported algorithm: treat as a failed verification for this entry, not a crash.
		}
		sigBytes, err := base64.StdEncoding.DecodeString(sig.Signature)
		if err != nil {
			continue // recognized key, malformed signature encoding: failed verification for this entry.
		}
		if !ed25519.Verify(key, canonical, sigBytes) {
			continue
		}

		switch sig.Role {
		case "root":
			rootVerified = true
		case "freshness":
			freshnessCryptoVerified = true
		}
	}

	freshnessVerified := freshnessCryptoVerified &&
		lastVerifiedConnectorsHash != "" &&
		matchesLastVerifiedConnectors(env.Payload, lastVerifiedConnectorsHash)

	if !rootVerified && !freshnessVerified {
		if !anyKeyRecognized {
			return nil, conduiterr.New(CodeTrustAnchorExpired,
				"no signature keyId in this index matches any trust anchor compiled into this build — upgrade Conduit")
		}
		return nil, conduiterr.New(CodeIndexIntegrity,
			"index signature verification failed — the index may be tampered or corrupted")
	}

	payload, err := decodeTypedPayload(env.Payload)
	if err != nil {
		return nil, err
	}
	return &VerifiedIndex{Payload: *payload, Verified: true, RootVerified: rootVerified}, nil
}

// resolveAnchor looks up keyId in the anchor set matching role. An
// unrecognized role (neither "root" nor "freshness") is treated identically
// to an unrecognized keyId: not a crash, just "this signature entry does not
// count towards anyKeyRecognized".
func resolveAnchor(anchors TrustAnchors, role, keyID string) (ed25519.PublicKey, bool) {
	switch role {
	case "root":
		key, ok := anchors.Roots[keyID]
		return key, ok
	case "freshness":
		key, ok := anchors.Freshness[keyID]
		return key, ok
	default:
		return nil, false
	}
}

// matchesLastVerifiedConnectors reports whether payloadRaw's connectors[]
// field, canonicalized and hashed, equals want. A malformed payload here
// (connectors[] doesn't even unmarshal) is treated as a mismatch, not a
// separate error: the caller's fall-through to CodeIndexIntegrity already
// covers "freshness signature verified crypto but content doesn't match".
func matchesLastVerifiedConnectors(payloadRaw json.RawMessage, want string) bool {
	var probe struct {
		Connectors []Connector `json:"connectors"`
	}
	if err := json.Unmarshal(payloadRaw, &probe); err != nil {
		return false
	}
	got, err := HashConnectors(probe.Connectors)
	if err != nil {
		return false
	}
	return got == want
}

// decodeEnvelope performs the parse-time steps shared by ParseUnverified and
// Verify: duplicate-key rejection (before anything else) followed by the
// generic {payload, signatures[]} unmarshal. Recovers from a panic anywhere
// in this pipeline and reports it as CodeIndexIntegrity rather than crashing
// the process (see CheckNoDuplicateKeys' doc comment for the concrete
// goccy/go-json panic the P0-2 fuzz corpus found) — both callers get this
// defense-in-depth "for free" via this single shared choke point.
func decodeEnvelope(raw []byte) (env rawEnvelope, err error) {
	defer func() {
		if r := recover(); r != nil {
			env = rawEnvelope{}
			err = conduiterr.New(CodeIndexIntegrity,
				fmt.Sprintf("index payload could not be parsed safely: %v", r))
		}
	}()

	if err := CheckNoDuplicateKeys(raw); err != nil {
		return rawEnvelope{}, err
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return rawEnvelope{}, conduiterr.Wrap(CodeIndexIntegrity, "index envelope is not valid JSON", err)
	}
	return env, nil
}

// decodeTypedPayload peeks payload.schemaVersion (refusing CodeSchemaTooNew
// before any typed unmarshal is attempted, per R-1 §a step 2d) and then
// unmarshals into the typed schemaVersion-1 Payload struct. Shared by
// ParseUnverified (called unconditionally) and Verify (called only AFTER
// cryptographic acceptance).
func decodeTypedPayload(payloadRaw json.RawMessage) (*Payload, error) {
	var probe struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(payloadRaw, &probe); err != nil {
		return nil, conduiterr.Wrap(CodeIndexIntegrity, "index payload is not valid JSON", err)
	}
	if probe.SchemaVersion > MaxSupportedSchemaVersion {
		return nil, conduiterr.New(CodeSchemaTooNew,
			"index schemaVersion is newer than this build supports — upgrade Conduit")
	}

	var p Payload
	if err := json.Unmarshal(payloadRaw, &p); err != nil {
		return nil, conduiterr.Wrap(CodeIndexIntegrity, "index payload does not match the expected schema", err)
	}
	return &p, nil
}

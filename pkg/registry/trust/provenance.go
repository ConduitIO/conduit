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

import (
	"encoding/hex"
	"fmt"

	in_toto "github.com/in-toto/attestation/go/v1"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// SupportedSLSAPredicateTypes are the in-toto predicateType values this
// build recognizes as SLSA provenance. A statement carrying any OTHER
// predicateType is a hard reject in CheckProvenanceBinding, never a silently
// skipped check — matching the frozen index schema's
// provenanceRef.predicateType doc comment ("a predicate type the client does
// not recognize is a hard reject").
var SupportedSLSAPredicateTypes = map[string]bool{
	"https://slsa.dev/provenance/v1":   true,
	"https://slsa.dev/provenance/v0.2": true,
}

// CheckProvenanceBinding performs the SLSA-semantic checks that
// VerifyAttestationEnvelope's cryptographic verification does NOT perform
// structurally (R-1 §c step 7 — "beyond what cosign verify-attestation
// checks structurally"):
//
//  1. statement.predicateType is one this build recognizes (see
//     SupportedSLSAPredicateTypes) — an unrecognized type is a hard reject.
//  2. At least one subject[].digest.sha256 equals artifactDigest (the
//     digest computed from the artifact's ACTUALLY RECEIVED bytes, never a
//     value read back out of the index) — the subject-digest binding: a
//     validly-signed attestation that simply attests to a DIFFERENT
//     artifact must not be accepted for this one. Per the P2
//     subject-digest-algorithm-pinning nit, a subject digest map lacking a
//     "sha256" key never counts as a match, even if it offers some other
//     (weaker or legacy) digest algorithm alongside it.
//  3. predicate.builder.id equals expectedBuilderID exactly (the
//     builder-ID binding, P1-2: enforced unconditionally from this
//     package's first release, no soft/warn-only period) — see
//     ExpectedBuilderID.
//
// Any failure returns CodeProvenanceInvalid wrapping ErrProvenanceInvalid —
// distinct from CodeIdentityMismatch (the signing identity itself can be
// exactly correct while the provenance's semantic claims are wrong; see
// sigstore.go's doc comment on why these are never conflated).
func CheckProvenanceBinding(statement *in_toto.Statement, artifactDigest [32]byte, expectedBuilderID string) error {
	if statement == nil {
		return conduiterr.Wrap(CodeProvenanceInvalid, "no provenance statement to check", ErrProvenanceInvalid)
	}

	if !SupportedSLSAPredicateTypes[statement.GetPredicateType()] {
		return conduiterr.Wrap(CodeProvenanceInvalid,
			fmt.Sprintf("unrecognized provenance predicateType %q — refusing rather than skipping the check", statement.GetPredicateType()),
			ErrProvenanceInvalid)
	}

	wantHex := hex.EncodeToString(artifactDigest[:])
	if !subjectDigestMatches(statement, wantHex) {
		return conduiterr.Wrap(CodeProvenanceInvalid,
			"provenance attestation's subject digest does not match this artifact's actual sha256", ErrProvenanceInvalid)
	}

	builderID, ok := extractBuilderID(statement, statement.GetPredicateType())
	if !ok || builderID != expectedBuilderID {
		return conduiterr.Wrap(CodeProvenanceInvalid, fmt.Sprintf(
			"provenance attestation's builder.id (%q) does not match the expected builder (%q)", builderID, expectedBuilderID),
			ErrProvenanceInvalid)
	}
	return nil
}

// subjectDigestMatches reports whether any subject in statement carries a
// "sha256" digest equal to wantHex (lowercase hex, no algorithm prefix,
// matching in-toto's own subject[].digest convention).
func subjectDigestMatches(statement *in_toto.Statement, wantHex string) bool {
	for _, subj := range statement.GetSubject() {
		if subj == nil {
			continue
		}
		got, ok := subj.GetDigest()["sha256"]
		if ok && got == wantHex {
			return true
		}
	}
	return false
}

// extractBuilderID reads the builder.id claim out of the statement's
// generic predicate struct (structpb.Struct — the in-toto Statement type
// does not know about SLSA's own predicate shape, since predicateType is
// itself just a string key; SLSA-specific field access is necessarily
// map-shaped, not a typed struct field).
//
// The field's PATH differs by SLSA predicate version — this is a real,
// easy-to-get-wrong schema difference, not a stylistic one:
//
//   - "https://slsa.dev/provenance/v1": predicate.runDetails.builder.id
//     (confirmed against a REAL slsa-github-generator-produced attestation
//     in testdata/real-sigstore-attestation.json, not just the SLSA spec
//     text — see sigstore_realworld_test.go).
//   - "https://slsa.dev/provenance/v0.2": predicate.builder.id (the flat,
//     top-level shape from the older predicate version).
//
// Returns ok=false if the shape doesn't match (missing builder, non-string
// id, missing predicate entirely, or an unrecognized predicateType) —
// CheckProvenanceBinding treats that identically to a mismatched value:
// refuse, never treat an absent/unrecognized builder.id as "no claim, so
// allow".
func extractBuilderID(statement *in_toto.Statement, predicateType string) (string, bool) {
	predicate := statement.GetPredicate()
	if predicate == nil {
		return "", false
	}
	fields := predicate.AsMap()

	switch predicateType {
	case "https://slsa.dev/provenance/v1":
		runDetails, ok := fields["runDetails"].(map[string]any)
		if !ok {
			return "", false
		}
		builder, ok := runDetails["builder"].(map[string]any)
		if !ok {
			return "", false
		}
		id, ok := builder["id"].(string)
		return id, ok
	case "https://slsa.dev/provenance/v0.2":
		builder, ok := fields["builder"].(map[string]any)
		if !ok {
			return "", false
		}
		id, ok := builder["id"].(string)
		return id, ok
	default:
		return "", false
	}
}

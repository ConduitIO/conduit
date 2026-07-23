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
	"context"
	"fmt"

	in_toto "github.com/in-toto/attestation/go/v1"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry/trust/trustroot"
)

// embeddedSigstoreTrustedRoot is Sigstore's own trust root (layer 1:
// Fulcio/Rekor/CTFE — see doc.go's two-layer distinction), loaded once at
// process init from the build-time-embedded snapshot
// (pkg/registry/trust/trustroot). This is the ONLY trust root
// VerifyArtifactSignature/VerifyAttestationEnvelope use in production —
// nothing in this package ever fetches a trust root over the network (R-1's
// hard invariant: trust anchors are fixed at build time). Loading failure
// panics at init time, loudly, at process startup — never silently falls
// back to "verification disabled" on the first real install attempt.
var embeddedSigstoreTrustedRoot root.TrustedMaterial

func init() {
	tr, err := root.NewTrustedRootFromJSON(trustroot.SigstoreTrustedRootJSON)
	if err != nil {
		panic(fmt.Sprintf("pkg/registry/trust: embedded sigstore trusted root failed to load: %v", err))
	}
	embeddedSigstoreTrustedRoot = tr
}

// newSigstoreVerifier builds a verify.Verifier requiring both transparency-
// log inclusion and an observer timestamp (matching the spike-confirmed
// offline-capable configuration in conduit-registry-plans/sigstore-spike):
// a bundle whose tlog entry lacks an inclusion proof/promise is rejected at
// PARSE time by sigstore-go itself (bundle.Bundle.validate), before this
// verifier is ever invoked — see bundle.go's UnmarshalJSON.
func newSigstoreVerifier(tm root.TrustedMaterial) (*verify.Verifier, error) {
	v, err := verify.NewVerifier(tm, verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	if err != nil {
		return nil, conduiterr.Wrap(conduiterr.CodeInternal, "could not construct sigstore verifier", err)
	}
	return v, nil
}

// certificateIdentityFor translates a PinnedIdentity (this package's own
// type — see types.go) into sigstore-go's verify.CertificateIdentity: an
// exact-match OIDC issuer (never a regex — the issuer URL is not something
// index-CI's registration checklist requires anchoring/tightness review
// for, unlike IdentityPattern) and the fully-anchored SAN regex pattern
// (see identitypattern.go for its tightness rules).
func certificateIdentityFor(identity PinnedIdentity) (verify.CertificateIdentity, error) {
	// NewShortCertificateIdentity(issuer, issuerRegex, sanValue, sanRegex).
	// The spike (test 4) confirmed sigstore-go's SAN/issuer matchers compile
	// patterns with Go's stdlib regexp package (RE2 semantics) — no ReDoS
	// surface, no backreference/PCRE extension support.
	return verify.NewShortCertificateIdentity(identity.OIDCIssuer, "", "", identity.IdentityPattern)
}

// loadBundle parses raw Sigstore bundle bytes (already fetched and
// size-bounded by the caller — pkg/registry/boundedfetch, P0-2 — this
// package never fetches over the network on its own). An empty/malformed
// bundle is reported as "no valid signature" (ErrUnsigned) rather than a
// generic parse error: from the caller's perspective, an artifact with no
// usable signature bundle at all is indistinguishable from one whose
// signature failed to verify — both refuse identically.
func loadBundle(bundleBytes []byte) (*bundle.Bundle, error) {
	if len(bundleBytes) == 0 {
		return nil, cerrors.Errorf("%w: no signature bundle bytes provided", ErrUnsigned)
	}
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleBytes); err != nil {
		return nil, cerrors.Errorf("%w: bundle is malformed or missing required verification material (e.g. Rekor inclusion proof): %v", ErrUnsigned, err)
	}
	return &b, nil
}

// classifyVerifyError distinguishes ErrIdentityMismatch (the cryptographic
// envelope verified — a validly Rekor-logged signature exists — but the
// certificate's identity does not match the pinned identity) from ErrUnsigned
// (every other verification failure: bad signature, untrusted cert chain,
// missing/invalid tlog inclusion, etc — no valid signature for ANY identity).
//
// This is the single highest-value piece of code in the trust core (per
// docs/design-documents/20260714-connector-registry-index-schema.md and the
// step3 plan): sigstore-go's Verifier.Verify runs identity-policy matching
// (policy.certificateIdentities.Verify) strictly AFTER the entity's full
// cryptographic material (signature, cert chain, tlog inclusion, SCTs if
// required) has already been verified successfully — see
// pkg/verify/signed_entity.go's Verify implementation, which only reaches
// the identity check once every earlier step has returned no error. So a
// *verify.ErrNoMatchingCertificateIdentity in the error chain PROVES the
// signature was cryptographically valid; anything else proves it was not.
func classifyVerifyError(err error) (identityMismatch bool, cause error) {
	var mismatch *verify.ErrNoMatchingCertificateIdentity
	if cerrors.As(err, &mismatch) {
		return true, cerrors.Errorf("%w: %v", ErrIdentityMismatch, err)
	}
	return false, cerrors.Errorf("%w: %v", ErrUnsigned, err)
}

// VerifyArtifactSignature verifies bundleBytes (a Sigstore bundle: cert
// chain + signature + Rekor inclusion proof/SET, offline-verifiable) covers
// artifactDigest (sha256, computed from ACTUALLY RECEIVED bytes by the
// caller — never re-read from a path, see pkg/registry.ArtifactRef's doc
// comment) and was signed by identity. On success, returns the actual
// certificate SAN that signed the artifact (for the install manifest's
// verifiedIdentity field). Uses the embedded Sigstore public-good trust
// root — see VerifyArtifactSignatureWithRoot for the test/index-CI-facing
// variant that accepts an explicit trust root.
func VerifyArtifactSignature(ctx context.Context, artifactDigest []byte, bundleBytes []byte, identity PinnedIdentity) (string, error) {
	return VerifyArtifactSignatureWithRoot(ctx, artifactDigest, bundleBytes, identity, embeddedSigstoreTrustedRoot)
}

// VerifyArtifactSignatureWithRoot is VerifyArtifactSignature parameterized
// over an explicit root.TrustedMaterial — the seam the adversarial fixture
// corpus (pkg/registry/trust/adversarial) and this package's own tests use
// to verify against a locally-generated (sigstore-go's
// pkg/testing/ca.VirtualSigstore), test-only trust root instead of the real
// embedded Sigstore public-good instance, without touching production
// infrastructure or requiring network access.
func VerifyArtifactSignatureWithRoot(_ context.Context, artifactDigest []byte, bundleBytes []byte, identity PinnedIdentity, tm root.TrustedMaterial) (string, error) {
	b, err := loadBundle(bundleBytes)
	if err != nil {
		return "", conduiterr.Wrap(CodeUnsigned, "no valid signature bundle for this artifact", err)
	}
	return VerifySignedEntitySignature(b, artifactDigest, identity, tm)
}

// VerifySignedEntitySignature is VerifyArtifactSignatureWithRoot's core
// logic, operating directly on an already-parsed verify.SignedEntity rather
// than raw bundle bytes. Exported so the adversarial fixture corpus (which
// builds fixtures directly from sigstore-go's pkg/testing/ca.VirtualSigstore
// as a verify.SignedEntity, per plan-v2 §11/P1-4) exercises the IDENTICAL
// verification and error-classification logic the byte-oriented entry point
// uses, without a redundant JSON bundle round-trip through sigstore-go's own
// (separately and extensively tested) bundle parser.
func VerifySignedEntitySignature(entity verify.SignedEntity, artifactDigest []byte, identity PinnedIdentity, tm root.TrustedMaterial) (string, error) {
	v, err := newSigstoreVerifier(tm)
	if err != nil {
		return "", err
	}
	certID, err := certificateIdentityFor(identity)
	if err != nil {
		return "", conduiterr.Wrap(conduiterr.CodeInternal, "pinned identity is not a valid certificate-identity pattern", err)
	}

	policy := verify.NewPolicy(verify.WithArtifactDigest("sha256", artifactDigest), verify.WithCertificateIdentity(certID))
	res, err := v.Verify(entity, policy)
	if err != nil {
		mismatch, cause := classifyVerifyError(err)
		if mismatch {
			msg := fmt.Sprintf("artifact signature is valid but was not signed by the identity pinned for this "+
				"connector (expected issuer %q, identity pattern %q)", identity.OIDCIssuer, identity.IdentityPattern)
			return "", conduiterr.Wrap(CodeIdentityMismatch, msg, cause)
		}
		return "", conduiterr.Wrap(CodeUnsigned, "no valid signature found for this artifact", cause)
	}
	if res.Signature == nil || res.Signature.Certificate == nil {
		// Should be unreachable: WithCertificateIdentity requires a
		// certificate-signed entity (see signed_entity.go's
		// RequireIdentities/signedWithCertificate checks) — but never trust
		// that structurally; fail closed rather than return an empty
		// identity as if it were meaningful.
		return "", conduiterr.Wrap(CodeUnsigned, "signature verified without an associated certificate identity", ErrUnsigned)
	}
	return res.Signature.Certificate.SubjectAlternativeName, nil
}

// VerifyAttestationEnvelope cryptographically verifies bundleBytes as a
// DSSE-enveloped in-toto attestation signed by identity, and returns the
// verified (but not yet SLSA-semantically checked — see provenance.go)
// in-toto Statement. Uses the embedded Sigstore public-good trust root.
func VerifyAttestationEnvelope(ctx context.Context, bundleBytes []byte, identity PinnedIdentity) (*in_toto.Statement, error) {
	return VerifyAttestationEnvelopeWithRoot(ctx, bundleBytes, identity, embeddedSigstoreTrustedRoot)
}

// VerifyAttestationEnvelopeWithRoot is VerifyAttestationEnvelope
// parameterized over an explicit trust root — see
// VerifyArtifactSignatureWithRoot's doc comment for why this seam exists.
func VerifyAttestationEnvelopeWithRoot(_ context.Context, bundleBytes []byte, identity PinnedIdentity, tm root.TrustedMaterial) (*in_toto.Statement, error) {
	b, err := loadBundle(bundleBytes)
	if err != nil {
		return nil, conduiterr.Wrap(CodeUnsigned, "no valid provenance attestation bundle for this artifact", err)
	}
	return VerifySignedEntityAttestation(b, identity, tm)
}

// VerifySignedEntityAttestation is VerifyAttestationEnvelopeWithRoot's core
// logic, operating directly on an already-parsed verify.SignedEntity — see
// VerifySignedEntitySignature's doc comment for why this seam exists.
//
// Deliberately does NOT pass an artifact-digest policy option (unlike
// VerifySignedEntitySignature): an attestation's DSSE envelope doesn't carry
// "the artifact" in sigstore-go's WithArtifactDigest sense — the
// subject-digest binding is a SLSA-semantic check over the verified
// Statement's own subject[] field, performed by provenance.go's
// CheckProvenanceBinding, strictly AFTER this function returns a
// cryptographically-verified Statement. Conflating the two would let a
// caller mistake "the DSSE envelope itself verified" for "this attestation
// is actually ABOUT the artifact in hand" — exactly the gap provenance.go
// exists to close (R-1 §c step 7).
func VerifySignedEntityAttestation(entity verify.SignedEntity, identity PinnedIdentity, tm root.TrustedMaterial) (*in_toto.Statement, error) {
	v, err := newSigstoreVerifier(tm)
	if err != nil {
		return nil, err
	}
	certID, err := certificateIdentityFor(identity)
	if err != nil {
		return nil, conduiterr.Wrap(conduiterr.CodeInternal, "pinned identity is not a valid certificate-identity pattern", err)
	}

	policy := verify.NewPolicy(verify.WithoutArtifactUnsafe(), verify.WithCertificateIdentity(certID))
	res, err := v.Verify(entity, policy)
	if err != nil {
		mismatch, cause := classifyVerifyError(err)
		if mismatch {
			msg := fmt.Sprintf("provenance attestation is validly signed but not by the identity pinned for this "+
				"connector (expected issuer %q, identity pattern %q)", identity.OIDCIssuer, identity.IdentityPattern)
			return nil, conduiterr.Wrap(CodeIdentityMismatch, msg, cause)
		}
		return nil, conduiterr.Wrap(CodeUnsigned, "no valid provenance attestation found for this artifact", cause)
	}
	if res.Statement == nil {
		return nil, conduiterr.Wrap(CodeProvenanceInvalid, "attestation envelope verified but carried no in-toto statement", ErrProvenanceInvalid)
	}
	return res.Statement, nil
}

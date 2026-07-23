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

// Package adversarial is the shared fixture corpus (plan-v2 §11, P1-4): a
// small set of hand-built, LOCALLY-generated (never touching production
// Sigstore or Conduit registry infrastructure) signed/attested test
// entities, each paired with the pkg/registry/trust error this build's
// verification pipeline must produce for it.
//
// # Why this is a real, importable package (not internal, not test-only)
//
// The index-repo's own CI re-verification tooling (a separate repo, plan-v2
// §7.4/§2.5) must run the SAME checks against the SAME fixtures the conduit
// client runs — otherwise "index-CI re-verifies everything" is a comment,
// not a fact. Index-CI imports this package via a pinned
// github.com/conduitio/conduit module version (never @latest, per plan-v2
// §12 item 3) and runs Corpus() against whatever pkg/registry/trust version
// it's pinned to. A CI job in both repos runs this corpus on every change to
// pkg/registry/trust (plan-v2 §11) — the concrete mechanism that makes
// "correlated failure between client and index-CI" a tested property.
//
// # What this corpus does and does not cover
//
// Every fixture here exercises pkg/registry/trust's cryptographic
// verification boundary (signature identity pinning, SLSA provenance
// binding) using sigstore-go's own official offline test harness
// (github.com/sigstore/sigstore-go/pkg/testing/ca.VirtualSigstore) — a
// self-contained, in-memory Fulcio-CA + Rekor-transparency-log simulator
// that produces REAL, cryptographically valid Sigstore bundles without any
// network access or dependency on the public Sigstore instance. Revocation
// (publisher.revoked) and yanking (versions[].yanked) are index-DATA checks,
// not cryptographic ones — they are covered by pkg/registry's own
// security-warranty test suite against hand-built index fixtures, not this
// package, since they have nothing to do with signature/provenance
// verification and pulling them in here would blur that boundary.
package adversarial

import (
	"crypto/sha256"
	"encoding/hex"

	json "github.com/goccy/go-json"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/registry/trust"
)

// Kind distinguishes the two things a Fixture can exercise: an artifact
// signature (trust.VerifySignedEntitySignature) or a provenance attestation
// (trust.VerifySignedEntityAttestation + trust.CheckProvenanceBinding).
type Kind int

const (
	KindSignature Kind = iota
	KindProvenance
)

// pinnedIdentity is the identity every "legitimate publisher" fixture in
// this corpus signs as; attackerIdentity is a DIFFERENT, plausible-looking
// identity used by the identity-mismatch fixture — both under the SAME OIDC
// issuer, since a real impersonation attempt would present a plausible
// issuer too (R-1's threat model: "anyone who forks the publish Action...
// earns a green verified badge" is exactly "same issuer, wrong SAN").
const (
	oidcIssuer            = "https://token.actions.githubusercontent.com"
	pinnedIdentity        = "https://github.com/ExampleOrg/example-connector/.github/workflows/publish.yml@refs/tags/v1.0.0"
	attackerIdentity      = "https://github.com/AttackerOrg/example-connector/.github/workflows/publish.yml@refs/tags/v1.0.0"
	pinnedIdentityPattern = `^https://github\.com/ExampleOrg/example-connector/\.github/workflows/publish\.yml@refs/tags/v.*$`
)

var testArtifact = []byte("adversarial-corpus-test-artifact-bytes")

// Fixture bundles a hand-built case name, a locally-signed
// verify.SignedEntity, the PinnedIdentity/trust root to verify it against,
// and the expected outcome (nil WantErr means "must succeed").
type Fixture struct {
	Name           string
	Kind           Kind
	Identity       trust.PinnedIdentity
	TrustedRoot    root.TrustedMaterial
	Entity         verify.SignedEntity
	ArtifactDigest [32]byte
	// ExpectedBuilderID overrides trust.ExpectedBuilderID for KindProvenance
	// fixtures that specifically test the builder-ID binding; empty uses
	// trust.ExpectedBuilderID (the real, production expected value).
	ExpectedBuilderID string
	// WantErr is one of trust.ErrUnsigned, trust.ErrIdentityMismatch,
	// trust.ErrProvenanceInvalid, or nil (expect success).
	WantErr error
}

// Run dispatches f through the identical pkg/registry/trust entry points
// production code uses, and reports whether the actual outcome matched
// f.WantErr (via cerrors.Is against the returned error's cause chain).
// Shared by this package's own tests and by index-CI's re-verification
// tooling, so "the corpus passes" means the same thing in both places.
func Run(f Fixture) error {
	switch f.Kind {
	case KindSignature:
		_, err := trust.VerifySignedEntitySignature(f.Entity, f.ArtifactDigest[:], f.Identity, f.TrustedRoot)
		return checkWant(err, f.WantErr)
	case KindProvenance:
		statement, err := trust.VerifySignedEntityAttestation(f.Entity, f.Identity, f.TrustedRoot)
		if err != nil {
			return checkWant(err, f.WantErr)
		}
		builderID := f.ExpectedBuilderID
		if builderID == "" {
			builderID = trust.ExpectedBuilderID
		}
		err = trust.CheckProvenanceBinding(statement, f.ArtifactDigest, builderID)
		return checkWant(err, f.WantErr)
	default:
		return cerrors.Errorf("adversarial: unknown fixture kind %v", f.Kind)
	}
}

func checkWant(got, want error) error {
	if want == nil {
		if got != nil {
			return cerrors.Errorf("expected success, got error: %w", got)
		}
		return nil
	}
	if got == nil {
		return cerrors.Errorf("expected error matching %v, got success", want)
	}
	if !cerrors.Is(got, want) {
		return cerrors.Errorf("expected error matching %v, got: %w", want, got)
	}
	return nil
}

// Corpus builds and returns every fixture, using two ephemeral, in-memory
// VirtualSigstore CAs generated fresh on each call (never persisted, never
// touching any production infrastructure). Returns an error rather than
// panicking so a non-test caller (index-CI's tooling) can handle setup
// failure gracefully.
func Corpus() ([]Fixture, error) {
	legitCA, err := ca.NewVirtualSigstore()
	if err != nil {
		return nil, cerrors.Errorf("could not create legitimate-publisher virtual sigstore: %w", err)
	}
	attackerCA, err := ca.NewVirtualSigstore()
	if err != nil {
		return nil, cerrors.Errorf("could not create attacker virtual sigstore: %w", err)
	}

	legitRoot, err := trustedMaterialFor(legitCA)
	if err != nil {
		return nil, cerrors.Errorf("could not build trusted root for legitimate CA: %w", err)
	}

	identity := trust.PinnedIdentity{OIDCIssuer: oidcIssuer, IdentityPattern: pinnedIdentityPattern}
	digest := sha256.Sum256(testArtifact)

	fixtures := []Fixture{}

	f, err := validSignatureFixture(legitCA, legitRoot, identity, digest)
	if err != nil {
		return nil, err
	}
	fixtures = append(fixtures, f)

	f, err = identityMismatchFixture(legitCA, legitRoot, identity, digest)
	if err != nil {
		return nil, err
	}
	fixtures = append(fixtures, f)

	f, err = untrustedSignerFixture(attackerCA, legitRoot, identity, digest)
	if err != nil {
		return nil, err
	}
	fixtures = append(fixtures, f)

	f, err = provenanceSubjectMismatchFixture(legitCA, legitRoot, identity, digest)
	if err != nil {
		return nil, err
	}
	fixtures = append(fixtures, f)

	f, err = provenanceBuilderMismatchFixture(legitCA, legitRoot, identity, digest)
	if err != nil {
		return nil, err
	}
	fixtures = append(fixtures, f)

	f, err = validProvenanceFixture(legitCA, legitRoot, identity, digest)
	if err != nil {
		return nil, err
	}
	fixtures = append(fixtures, f)

	return fixtures, nil
}

func trustedMaterialFor(virtualCA *ca.VirtualSigstore) (root.TrustedMaterial, error) {
	return root.NewTrustedRoot(
		root.TrustedRootMediaType01,
		virtualCA.FulcioCertificateAuthorities(),
		virtualCA.CTLogs(),
		virtualCA.TimestampingAuthorities(),
		virtualCA.RekorLogs(),
	)
}

// validSignatureFixture: the happy path — a legitimate signature by the
// pinned identity over the actual artifact digest. Must succeed.
func validSignatureFixture(virtualCA *ca.VirtualSigstore, tm root.TrustedMaterial, identity trust.PinnedIdentity, digest [32]byte) (Fixture, error) {
	entity, err := virtualCA.Sign(pinnedIdentity, oidcIssuer, testArtifact)
	if err != nil {
		return Fixture{}, cerrors.Errorf("could not sign valid-signature fixture: %w", err)
	}
	return Fixture{
		Name: "valid signature by the pinned identity", Kind: KindSignature,
		Identity: identity, TrustedRoot: tm, Entity: entity, ArtifactDigest: digest, WantErr: nil,
	}, nil
}

// identityMismatchFixture: a validly Rekor-logged signature (same CA, same
// transparency log — the cryptographic material is completely legitimate)
// but signed by a DIFFERENT identity than the one pinned for this connector
// name. Must refuse with trust.ErrIdentityMismatch, never trust.ErrUnsigned
// — conflating the two would hide "this artifact IS signed, just not by who
// you think" behind a message that reads like "nobody signed this at all".
func identityMismatchFixture(virtualCA *ca.VirtualSigstore, tm root.TrustedMaterial, identity trust.PinnedIdentity, digest [32]byte) (Fixture, error) {
	entity, err := virtualCA.Sign(attackerIdentity, oidcIssuer, testArtifact)
	if err != nil {
		return Fixture{}, cerrors.Errorf("could not sign identity-mismatch fixture: %w", err)
	}
	return Fixture{
		Name: "valid signature by a DIFFERENT identity (impersonation attempt)", Kind: KindSignature,
		Identity: identity, TrustedRoot: tm, Entity: entity, ArtifactDigest: digest, WantErr: trust.ErrIdentityMismatch,
	}, nil
}

// untrustedSignerFixture: a signature from the PINNED identity string, but
// produced by a completely different (attacker-controlled) Fulcio CA/Rekor
// log that the verifying trust root does not recognize at all. This models
// "bad/corrupted signature" from the caller's perspective: no valid
// signature chains to any trust anchor this build recognizes. Must refuse
// with trust.ErrUnsigned (the cryptographic material itself never validates
// — the identity check is never even reached, since cert-chain validation
// fails first).
func untrustedSignerFixture(attackerCA *ca.VirtualSigstore, legitRoot root.TrustedMaterial, identity trust.PinnedIdentity, digest [32]byte) (Fixture, error) {
	entity, err := attackerCA.Sign(pinnedIdentity, oidcIssuer, testArtifact)
	if err != nil {
		return Fixture{}, cerrors.Errorf("could not sign untrusted-signer fixture: %w", err)
	}
	return Fixture{
		Name: "signature from an untrusted CA (bad/corrupted signature)", Kind: KindSignature,
		Identity: identity, TrustedRoot: legitRoot, Entity: entity, ArtifactDigest: digest, WantErr: trust.ErrUnsigned,
	}, nil
}

// slsaStatement builds a minimal, schema-shaped in-toto/SLSA v1.0 provenance
// statement JSON body, parameterized over the subject digest and builder ID
// so the two provenance adversarial fixtures can each vary exactly one
// field. The builder.id path is predicate.runDetails.builder.id — NOT a
// flat predicate.builder.id — matching the REAL SLSA v1.0 predicate shape
// confirmed against a genuine slsa-github-generator attestation in
// pkg/registry/trust/testdata/real-sigstore-attestation.json (see
// pkg/registry/trust's sigstore_realworld_test.go and provenance.go's
// extractBuilderID doc comment: getting this path wrong here would have let
// a self-consistent-but-wrong assumption in both the fixture generator and
// the checker hide a real bug, exactly what the real-world test caught
// during this PR's development).
func slsaStatement(subjectDigestHex, builderID string) ([]byte, error) {
	stmt := map[string]any{
		"_type": "https://in-toto.io/Statement/v1",
		"subject": []map[string]any{
			{"name": "example-connector", "digest": map[string]string{"sha256": subjectDigestHex}},
		},
		"predicateType": "https://slsa.dev/provenance/v1",
		"predicate": map[string]any{
			"runDetails": map[string]any{
				"builder": map[string]any{"id": builderID},
			},
		},
	}
	return json.Marshal(stmt)
}

// provenanceSubjectMismatchFixture: a cryptographically valid attestation,
// signed by the correctly pinned identity, but whose subject digest does
// NOT match the artifact's actual digest — the attestation is real, just
// not ABOUT this artifact. Must refuse with trust.ErrProvenanceInvalid.
func provenanceSubjectMismatchFixture(virtualCA *ca.VirtualSigstore, tm root.TrustedMaterial, identity trust.PinnedIdentity, digest [32]byte) (Fixture, error) {
	wrongDigest := sha256.Sum256([]byte("a completely different artifact"))
	body, err := slsaStatement(hex.EncodeToString(wrongDigest[:]), trust.ExpectedBuilderID)
	if err != nil {
		return Fixture{}, err
	}
	entity, err := virtualCA.Attest(pinnedIdentity, oidcIssuer, body)
	if err != nil {
		return Fixture{}, cerrors.Errorf("could not attest subject-mismatch fixture: %w", err)
	}
	return Fixture{
		Name: "valid attestation whose subject digest does not match the artifact", Kind: KindProvenance,
		Identity: identity, TrustedRoot: tm, Entity: entity, ArtifactDigest: digest, WantErr: trust.ErrProvenanceInvalid,
	}, nil
}

// provenanceBuilderMismatchFixture: a cryptographically valid attestation
// whose subject digest correctly matches the artifact, but whose
// predicate.builder.id does not match the expected (pinned) builder — e.g.
// a different, non-SLSA3 build pipeline attesting to a real artifact. Must
// refuse with trust.ErrProvenanceInvalid (P1-2: builder-ID binding is
// enforced unconditionally, no soft period).
func provenanceBuilderMismatchFixture(virtualCA *ca.VirtualSigstore, tm root.TrustedMaterial, identity trust.PinnedIdentity, digest [32]byte) (Fixture, error) {
	body, err := slsaStatement(hex.EncodeToString(digest[:]), "https://example.com/some-other-untrusted-builder")
	if err != nil {
		return Fixture{}, err
	}
	entity, err := virtualCA.Attest(pinnedIdentity, oidcIssuer, body)
	if err != nil {
		return Fixture{}, cerrors.Errorf("could not attest builder-mismatch fixture: %w", err)
	}
	return Fixture{
		Name: "valid attestation with a non-matching builder.id", Kind: KindProvenance,
		Identity: identity, TrustedRoot: tm, Entity: entity, ArtifactDigest: digest, WantErr: trust.ErrProvenanceInvalid,
	}, nil
}

// validProvenanceFixture: the provenance happy path — matching subject
// digest AND matching (pinned) builder ID. Must succeed.
func validProvenanceFixture(virtualCA *ca.VirtualSigstore, tm root.TrustedMaterial, identity trust.PinnedIdentity, digest [32]byte) (Fixture, error) {
	body, err := slsaStatement(hex.EncodeToString(digest[:]), trust.ExpectedBuilderID)
	if err != nil {
		return Fixture{}, err
	}
	entity, err := virtualCA.Attest(pinnedIdentity, oidcIssuer, body)
	if err != nil {
		return Fixture{}, cerrors.Errorf("could not attest valid-provenance fixture: %w", err)
	}
	return Fixture{
		Name: "valid, correctly-bound L3 provenance attestation", Kind: KindProvenance,
		Identity: identity, TrustedRoot: tm, Entity: entity, ArtifactDigest: digest, WantErr: nil,
	}, nil
}

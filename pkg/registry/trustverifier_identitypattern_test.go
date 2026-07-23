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

// This file is the regression suite for the P1 adversarial-review finding
// fixed here: TrustedVerifier.VerifyArtifact used to pass
// Publisher.ExpectedIdentityPattern straight into PinnedIdentity with no
// re-check of trust.ValidateIdentityPattern's tightness rules — even though
// that function exists and is exported specifically for this purpose. The
// only enforcement of tightness was a manual reviewer checklist in an
// index-CI repo that does not exist yet, a single point of failure: a
// signature-verified index (proving only that the index bytes were not
// tampered with in transit) could still carry a dangerously loose pinned
// pattern like "^.*$", which would let ANY signing identity satisfy the pin.
package registry_test

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/matryer/is"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/trust"
)

// TestTrustedVerifier_VerifyArtifact_IdentityPatternTightness is the direct
// regression test for the fix: it calls the exact function the review
// flagged (TrustedVerifier.VerifyArtifact) with a battery of pinned
// patterns, and asserts on WHICH error code comes back — not just that an
// error occurs — because ordering is the whole point here. A too-loose or
// unanchored pattern must refuse with trust.CodeIdentityPatternTooLoose
// BEFORE the artifact's (here: absent/garbage) signature is ever consulted.
// A legitimately tight pattern must NOT be rejected by this check: it must
// fall through to real signature verification and fail for the ordinary
// reason (trust.CodeUnsigned, since no real signature bundle is supplied) —
// proving the defensive check does not over-tighten and break valid
// seed-connector-shaped patterns.
//
// Without the fix, every case below (including the two malicious patterns)
// falls straight through to trust.VerifyArtifactSignature and comes back
// trust.CodeUnsigned, because ArtifactRef.SignatureBundle is empty in every
// case — so this test fails without the fix (the malicious-pattern
// subtests would observe CodeUnsigned instead of
// CodeIdentityPatternTooLoose) and passes with it. Confirmed by running
// this test against the pre-fix TrustedVerifier.VerifyArtifact.
func TestTrustedVerifier_VerifyArtifact_IdentityPatternTightness(t *testing.T) {
	v := &registry.TrustedVerifier{}
	ref := registry.ArtifactRef{Digest: sha256.Sum256([]byte("irrelevant-for-this-test"))}

	tests := []struct {
		name     string
		pattern  string
		wantCode conduiterr.Code
	}{
		{
			name:     "too-loose wildcard pattern is refused before signature verification",
			pattern:  `^.*$`,
			wantCode: trust.CodeIdentityPatternTooLoose,
		},
		{
			name:     "unanchored pattern is refused before signature verification",
			pattern:  `https://github\.com/ConduitIO/conduit-connector-postgres/.*`,
			wantCode: trust.CodeIdentityPatternTooLoose,
		},
		{
			name:     "anchored but owner/repo not actually pinned is refused",
			pattern:  `^https://github\.com/.*$`,
			wantCode: trust.CodeIdentityPatternTooLoose,
		},
		{
			// The realistic pinned pattern a seed connector actually ships
			// (per this PR's adversarial self-check) must NOT be rejected by
			// the tightness gate — it must reach real signature
			// verification and fail for the ordinary reason.
			name:     "legitimately tight real-world pattern passes the gate and reaches signature verification",
			pattern:  `^https://github\.com/conduitio/conduit-connector-postgres/\.github/workflows/.*@refs/tags/.*$`,
			wantCode: trust.CodeUnsigned,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			identity := trust.PinnedIdentity{
				OIDCIssuer:      "https://token.actions.githubusercontent.com",
				IdentityPattern: tt.pattern,
			}

			_, err := v.VerifyArtifact(context.Background(), ref, identity)
			is.True(err != nil)
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, tt.wantCode)
		})
	}
}

// TestVerifySignedEntitySignature_TooLoosePattern_AcceptsArbitraryIdentity
// substantiates WHY the gate added above matters: it proves, against a real
// (VirtualSigstore-issued) cryptographically valid signature, that
// trust.VerifySignedEntitySignature — the function
// TrustedVerifier.VerifyArtifact ultimately delegates to — has no opinion
// of its own about pattern tightness. An attacker-controlled identity that
// has nothing to do with the pinned connector's actual publisher
// successfully satisfies a "^.*$" pin with a completely valid signature.
// This is not a bug in VerifySignedEntitySignature (matching a regex is
// exactly its job); it is the reason the caller — now
// TrustedVerifier.VerifyArtifact — must defensively reject such a pattern
// before it is ever handed down to this layer at all.
func TestVerifySignedEntitySignature_TooLoosePattern_AcceptsArbitraryIdentity(t *testing.T) {
	is := is.New(t)

	vca, err := ca.NewVirtualSigstore()
	is.NoErr(err)
	tm, err := root.NewTrustedRoot(root.TrustedRootMediaType01,
		vca.FulcioCertificateAuthorities(), vca.CTLogs(), vca.TimestampingAuthorities(), vca.RekorLogs())
	is.NoErr(err)

	artifactBytes := []byte("some-connector-binary-bytes")
	digest := sha256.Sum256(artifactBytes)

	const attackerIdentity = "https://github.com/attacker-org/totally-unrelated-repo/.github/workflows/publish.yml@refs/tags/v1.0.0"
	const oidcIssuer = "https://token.actions.githubusercontent.com"

	sigEntity, err := vca.Sign(attackerIdentity, oidcIssuer, artifactBytes)
	is.NoErr(err)

	pinned := trust.PinnedIdentity{OIDCIssuer: oidcIssuer, IdentityPattern: `^.*$`}

	// This is the exploit: a real, validly-Rekor-logged signature from an
	// identity with no relationship at all to the pinned connector passes,
	// solely because the pin is too loose to mean anything.
	verifiedIdentity, err := trust.VerifySignedEntitySignature(sigEntity, digest[:], pinned, tm)
	is.NoErr(err)
	is.Equal(verifiedIdentity, attackerIdentity)

	// And ValidateIdentityPattern — now consulted defensively by
	// TrustedVerifier.VerifyArtifact before this call is ever reached in
	// production — refuses the exact pattern that made the exploit above
	// possible.
	is.True(trust.ValidateIdentityPattern(pinned.IdentityPattern) != nil)
}

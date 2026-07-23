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

package trust_test

import (
	"context"
	"os"
	"testing"

	"github.com/matryer/is"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/registry/trust"
)

// TestVerifyArtifactSignature_EmptyBundleIsUnsigned proves ErrUnsigned (not
// a generic parse error, not a panic) for the "no signature bundle at all"
// case — the most common real-world shape of "unsigned artifact" (the
// index's signature.bundleURL was empty and pkg/registry's fetch step never
// populated ArtifactRef.SignatureBundle at all).
func TestVerifyArtifactSignature_EmptyBundleIsUnsigned(t *testing.T) {
	is := is.New(t)
	_, err := trust.VerifyArtifactSignature(context.Background(), make([]byte, 32), nil, trust.PinnedIdentity{
		OIDCIssuer: "https://token.actions.githubusercontent.com", IdentityPattern: "^x$",
	})
	is.True(err != nil)
	is.True(cerrors.Is(err, trust.ErrUnsigned))
}

// TestVerifyArtifactSignature_MalformedBundleIsUnsigned proves that bytes
// which aren't a valid Sigstore bundle at all (not just "wrong signature")
// refuse with ErrUnsigned rather than a bare unwrapped JSON error a caller
// might mistake for an internal bug instead of a security refusal.
func TestVerifyArtifactSignature_MalformedBundleIsUnsigned(t *testing.T) {
	is := is.New(t)
	_, err := trust.VerifyArtifactSignature(context.Background(), make([]byte, 32), []byte(`{"not":"a bundle"}`), trust.PinnedIdentity{
		OIDCIssuer: "https://token.actions.githubusercontent.com", IdentityPattern: "^x$",
	})
	is.True(err != nil)
	is.True(cerrors.Is(err, trust.ErrUnsigned))
}

// TestVerifyAttestationEnvelope_BundleMissingInclusionProofIsUnsigned mirrors
// the sigstore-go spike's Test 2 (conduit-registry-plans/sigstore-spike):
// a bundle whose tlog entry has neither an inclusion proof nor an inclusion
// promise is rejected — by sigstore-go itself, at PARSE time, before any
// verifier is even constructed — proving the "no silent live-Rekor
// fallback for an incomplete entry" property this package's design (§3.3)
// depends on actually holds through this package's own wrapper, not just in
// isolation.
func TestVerifyAttestationEnvelope_BundleMissingInclusionProofIsUnsigned(t *testing.T) {
	is := is.New(t)
	bundleBytes, err := os.ReadFile("testdata/bundle-no-inclusion.sigstore.json")
	is.NoErr(err)
	_, err = trust.VerifyAttestationEnvelope(context.Background(), bundleBytes, trust.PinnedIdentity{
		OIDCIssuer: "https://token.actions.githubusercontent.com", IdentityPattern: "^x$",
	})
	is.True(err != nil)
	is.True(cerrors.Is(err, trust.ErrUnsigned))
}

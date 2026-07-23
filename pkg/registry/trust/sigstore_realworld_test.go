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

// TestVerifyAttestationEnvelope_RealSigstoreArtifact_UsesEmbeddedProductionTrustRoot
// is deliberately NOT built on the adversarial corpus's VirtualSigstore
// fixtures: it verifies a REAL Sigstore-logged attestation
// (testdata/real-sigstore-attestation.json — a genuine slsa-github-generator
// SLSA v1.0 provenance attestation for sigstore/sigstore-js's release
// workflow, publicly logged in Rekor, captured during this PR's sigstore-go
// offline-verification spike) against this package's EMBEDDED, build-time
// Sigstore public-good trust root (VerifyAttestationEnvelope, not
// …WithRoot) — proving the production trust anchor this build actually
// ships loads correctly and verifies real-world cryptographic material, not
// just locally-generated VirtualSigstore fixtures the same test code also
// generated. This is the closest thing this PR has to the epic's north-star
// E2E test ("author a connector → publish → a fresh install verifies") for
// the trust core specifically, ahead of the seed-connector bootstrap
// (plan-v2 §9, out of this PR's scope — see PR description).
//
// This test makes NO network call: the bundle already embeds its Rekor
// inclusion proof/SET and certificate chain (see sigstore-spike/main.go's
// Test 1, which proved this exact file verifies fully offline).
func TestVerifyAttestationEnvelope_RealSigstoreArtifact_UsesEmbeddedProductionTrustRoot(t *testing.T) {
	is := is.New(t)

	bundleBytes, err := os.ReadFile("testdata/real-sigstore-attestation.json")
	is.NoErr(err)

	// The real identity/issuer this bundle was actually signed with —
	// extracted from the leaf certificate's SAN/OIDC-issuer extension (see
	// the PR description's failure-mode analysis for the exact openssl
	// invocation used to confirm this).
	identity := trust.PinnedIdentity{
		OIDCIssuer:      "https://token.actions.githubusercontent.com",
		IdentityPattern: `^https://github\.com/sigstore/sigstore-js/\.github/workflows/release\.yml@refs/heads/main$`,
	}

	statement, err := trust.VerifyAttestationEnvelope(context.Background(), bundleBytes, identity)
	is.NoErr(err)
	is.Equal(statement.GetPredicateType(), "https://slsa.dev/provenance/v1")
	is.True(len(statement.GetSubject()) > 0)
	is.Equal(statement.GetSubject()[0].GetName(), "pkg:npm/sigstore@2.0.0")
}

// TestVerifyAttestationEnvelope_RealSigstoreArtifact_WrongIdentityRefuses
// proves the identity-mismatch classification also holds against real
// (non-virtual) cryptographic material, not just VirtualSigstore fixtures.
func TestVerifyAttestationEnvelope_RealSigstoreArtifact_WrongIdentityRefuses(t *testing.T) {
	is := is.New(t)

	bundleBytes, err := os.ReadFile("testdata/real-sigstore-attestation.json")
	is.NoErr(err)

	identity := trust.PinnedIdentity{
		OIDCIssuer:      "https://token.actions.githubusercontent.com",
		IdentityPattern: `^https://github\.com/ConduitIO/conduit-connector-postgres/\.github/workflows/publish\.yml@refs/tags/v.*$`,
	}

	_, err = trust.VerifyAttestationEnvelope(context.Background(), bundleBytes, identity)
	is.True(err != nil)
	is.True(cerrors.Is(err, trust.ErrIdentityMismatch))
}

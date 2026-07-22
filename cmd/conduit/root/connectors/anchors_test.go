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

// Internal test (package connectors) so it can exercise the unexported
// anchor loader/parser directly.
package connectors

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"testing/fstest"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/index"
	"github.com/matryer/is"
)

func pubPEM(t *testing.T, pub any) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal SPKI: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

func newEd25519Pub(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen ed25519: %v", err)
	}
	return pub
}

func TestParseAnchorPEMs_SingleKey_KeyIDMatchesIndexKeyID(t *testing.T) {
	is := is.New(t)
	pub := newEd25519Pub(t)

	got, err := parseAnchorPEMs(pubPEM(t, pub))
	is.NoErr(err)
	is.Equal(len(got), 1)

	wantID, err := index.KeyID(pub)
	is.NoErr(err)
	key, ok := got[wantID] // keyId must be exactly index.KeyID's derivation
	is.True(ok)
	is.True(key.Equal(pub))
}

func TestParseAnchorPEMs_RotationWindow_MultipleBlocks(t *testing.T) {
	is := is.New(t)
	a, b := newEd25519Pub(t), newEd25519Pub(t)

	concatenated := append(pubPEM(t, a), pubPEM(t, b)...)
	got, err := parseAnchorPEMs(concatenated)
	is.NoErr(err)
	is.Equal(len(got), 2) // both the outgoing and incoming key are trusted

	idA, _ := index.KeyID(a)
	idB, _ := index.KeyID(b)
	_, okA := got[idA]
	_, okB := got[idB]
	is.True(okA)
	is.True(okB)
}

func TestParseAnchorPEMs_RejectsNonEd25519(t *testing.T) {
	is := is.New(t)
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	is.NoErr(err)

	_, err = parseAnchorPEMs(pubPEM(t, &ec.PublicKey))
	is.True(err != nil) // an ECDSA key is a valid SPKI PUBLIC KEY but not ed25519 — must refuse
}

func TestParseAnchorPEMs_RejectsWrongBlockType(t *testing.T) {
	is := is.New(t)
	// A well-formed PEM block that isn't a PUBLIC KEY.
	block := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{0x01, 0x02}})
	_, err := parseAnchorPEMs(block)
	is.True(err != nil)
}

func TestParseAnchorPEMs_RejectsEmptyAndGarbage(t *testing.T) {
	is := is.New(t)
	for _, in := range [][]byte{nil, {}, []byte("not pem at all"), []byte("-----BEGIN PUBLIC KEY-----\nnotbase64\n-----END PUBLIC KEY-----\n")} {
		_, err := parseAnchorPEMs(in)
		is.True(err != nil) // zero valid blocks (or an undecodable one) is always an error
	}
}

func TestLoadEmbeddedTrustAnchors_BothRolesFromFS(t *testing.T) {
	is := is.New(t)
	root, freshness := newEd25519Pub(t), newEd25519Pub(t)
	fsys := fstest.MapFS{
		rootAnchorPath:      {Data: pubPEM(t, root)},
		freshnessAnchorPath: {Data: pubPEM(t, freshness)},
	}

	anchors, err := loadEmbeddedTrustAnchors(fsys)
	is.NoErr(err)
	is.Equal(len(anchors.Roots), 1)
	is.Equal(len(anchors.Freshness), 1)

	rootID, _ := index.KeyID(root)
	freshID, _ := index.KeyID(freshness)
	_, okR := anchors.Roots[rootID]
	_, okF := anchors.Freshness[freshID]
	is.True(okR)
	is.True(okF)
}

func TestLoadEmbeddedTrustAnchors_MissingRoleFailsClosed(t *testing.T) {
	is := is.New(t)
	// Only the root file present — freshness missing.
	fsys := fstest.MapFS{rootAnchorPath: {Data: pubPEM(t, newEd25519Pub(t))}}
	_, err := loadEmbeddedTrustAnchors(fsys)
	is.True(err != nil) // both roles are required; a half-populated anchor set is an error, not a partial success
}

// Production trust-anchor keyIds minted by the bootstrap ceremony (Gate 2,
// ConduitIO/conduit-connector-registry root-keygen run 29950357295). Pinned
// here so an accidental or malicious swap of the embedded PEMs fails CI: a
// key change is deliberate (rotation) and must update these constants in the
// same, reviewed PR — never a silent substitution.
const (
	prodRootKeyID      = "sha256:d657c2717760931c3771ec151e88fc143642b5c73ce79a3665fbf0f37f009795"
	prodFreshnessKeyID = "sha256:50bfd2c15ecf3cfa41a220a6a5ab9711309751bbb84688fa574cd05d6b9cf783"
)

// TestEmbeddedTrustAnchorsParse is the CI guard against shipping a build with a
// missing, corrupt, or swapped embedded anchor set. It SKIPS on a build that
// predates the bootstrap ceremony (the real PEM files not yet committed) and is
// a hard assertion once they are — a corrupt/empty/unexpected embedded key
// fails CI here rather than silently degrading every install to fail-closed (or
// worse, trusting an unexpected key).
func TestEmbeddedTrustAnchorsParse(t *testing.T) {
	is := is.New(t)
	anchors, err := loadEmbeddedTrustAnchors(trustAnchorFS)
	if err != nil {
		t.Skipf("embedded trust anchors not present yet (pre-bootstrap): %v", err)
	}
	is.True(len(anchors.Roots) > 0)     // at least one root key
	is.True(len(anchors.Freshness) > 0) // at least one freshness key

	_, rootPinned := anchors.Roots[prodRootKeyID]
	_, freshPinned := anchors.Freshness[prodFreshnessKeyID]
	is.True(rootPinned)  // the embedded root key must be exactly the ceremony-minted one
	is.True(freshPinned) // the embedded freshness key must be exactly the ceremony-minted one
}

func TestGuardTrustAnchors(t *testing.T) {
	is := is.New(t)

	// Normal (release) build: anchors loaded, no error, guard passes.
	is.NoErr(guardTrustAnchors())

	// Broken/anchor-stripped build: guard returns a distinct, coded refusal.
	prev := errAnchorLoad
	errAnchorLoad = cerrors.New("simulated missing embed")
	defer func() { errAnchorLoad = prev }()

	err := guardTrustAnchors()
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, registry.CodeTrustAnchorsUnavailable) // machine-actionable "reinstall conduit", not a generic expired-anchor
}

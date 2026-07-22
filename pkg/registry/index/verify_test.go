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

package index_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/matryer/is"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry/index"
)

// testIndex builds a minimal, schema-valid, signed test index and returns
// its raw bytes alongside the root/freshness keypairs used to sign it, so
// each test can mutate exactly one thing (a byte in the payload, a keyId, a
// signature) and re-derive the raw bytes without duplicating the whole
// document shape.
type testIndexBuilder struct {
	rootPub   ed25519.PublicKey
	rootPriv  ed25519.PrivateKey
	freshPub  ed25519.PublicKey
	freshPriv ed25519.PrivateKey
}

func newTestIndexBuilder(t *testing.T) *testIndexBuilder {
	t.Helper()
	is := is.New(t)
	rootPub, rootPriv, err := ed25519.GenerateKey(nil)
	is.NoErr(err)
	freshPub, freshPriv, err := ed25519.GenerateKey(nil)
	is.NoErr(err)
	return &testIndexBuilder{rootPub: rootPub, rootPriv: rootPriv, freshPub: freshPub, freshPriv: freshPriv}
}

func (b *testIndexBuilder) anchors(t *testing.T) index.TrustAnchors {
	t.Helper()
	is := is.New(t)
	rootKeyID, err := index.KeyID(b.rootPub)
	is.NoErr(err)
	freshKeyID, err := index.KeyID(b.freshPub)
	is.NoErr(err)
	return index.TrustAnchors{
		Roots:     map[string]ed25519.PublicKey{rootKeyID: b.rootPub},
		Freshness: map[string]ed25519.PublicKey{freshKeyID: b.freshPub},
	}
}

// payload is a minimal but schema-shaped connectors-index payload.
type testPayload struct {
	SchemaVersion int               `json:"schemaVersion"`
	Index         index.IndexMeta   `json:"index"`
	Connectors    []index.Connector `json:"connectors"`
}

func defaultTestPayload() testPayload {
	return testPayload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: 1, Timestamp: time.Now().UTC()},
		Connectors: []index.Connector{
			{
				Name: "example",
				Publisher: index.Publisher{
					ExpectedOIDCIssuer:      "https://token.actions.githubusercontent.com",
					ExpectedIdentityPattern: `^https://github\.com/ExampleOrg/example/\.github/workflows/publish\.yml@refs/tags/v.*$`,
				},
				Versions: []index.ConnectorVersion{
					{Version: "1.0.0", MinConduitVersion: "0.1.0", MinProtocolVersion: "0.1.0"},
				},
			},
		},
	}
}

// sign marshals payload, canonicalizes it, signs it with the given role's
// key, and returns the full raw envelope bytes.
func (b *testIndexBuilder) sign(t *testing.T, payload testPayload, roles ...string) []byte {
	t.Helper()
	is := is.New(t)

	payloadRaw, err := json.Marshal(payload)
	is.NoErr(err)
	canonical, err := index.Canonicalize(payloadRaw)
	is.NoErr(err)

	type sigJSON struct {
		Role      string `json:"role"`
		KeyID     string `json:"keyId"`
		Algorithm string `json:"algorithm"`
		Signature string `json:"signature"`
	}
	var sigs []sigJSON
	for _, role := range roles {
		var priv ed25519.PrivateKey
		var pub ed25519.PublicKey
		switch role {
		case "root":
			priv, pub = b.rootPriv, b.rootPub
		case "freshness":
			priv, pub = b.freshPriv, b.freshPub
		}
		keyID, err := index.KeyID(pub)
		is.NoErr(err)
		sig := ed25519.Sign(priv, canonical)
		sigs = append(sigs, sigJSON{
			Role: role, KeyID: keyID, Algorithm: "ed25519",
			Signature: base64.StdEncoding.EncodeToString(sig),
		})
	}

	env := struct {
		Payload    json.RawMessage `json:"payload"`
		Signatures []sigJSON       `json:"signatures"`
	}{Payload: payloadRaw, Signatures: sigs}

	raw, err := json.Marshal(env)
	is.NoErr(err)
	return raw
}

func TestVerify_ValidRootSignature(t *testing.T) {
	is := is.New(t)
	b := newTestIndexBuilder(t)
	raw := b.sign(t, defaultTestPayload(), "root")

	vi, err := index.Verify(raw, b.anchors(t), "")
	is.NoErr(err)
	is.True(vi.Verified)
	is.True(vi.RootVerified)
	is.Equal(vi.Payload.Connectors[0].Name, "example")
}

func TestVerify_ValidFreshnessSignatureWithMatchingConnectors(t *testing.T) {
	is := is.New(t)
	b := newTestIndexBuilder(t)
	payload := defaultTestPayload()

	lastHash, err := index.HashConnectors(payload.Connectors)
	is.NoErr(err)

	// A "heartbeat" re-sign: same connectors[], bumped version/timestamp,
	// freshness-signed only (no root signature present at all).
	payload.Index.Version = 2
	payload.Index.Timestamp = time.Now().UTC()
	raw := b.sign(t, payload, "freshness")

	vi, err := index.Verify(raw, b.anchors(t), lastHash)
	is.NoErr(err)
	is.True(vi.Verified)
	is.True(!vi.RootVerified)
}

func TestVerify_FreshnessSignatureWithMismatchedConnectorsRequiresRoot(t *testing.T) {
	is := is.New(t)
	b := newTestIndexBuilder(t)
	payload := defaultTestPayload()

	lastHash, err := index.HashConnectors(payload.Connectors)
	is.NoErr(err)

	// Freshness key re-signs DIFFERENT connectors content — must be refused:
	// a freshness signature may never authorize new content on its own.
	payload.Connectors[0].Name = "different-connector"
	raw := b.sign(t, payload, "freshness")

	_, err = index.Verify(raw, b.anchors(t), lastHash)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), index.CodeIndexIntegrity.Reason())
}

func TestVerify_UnrecognizedKeyID(t *testing.T) {
	is := is.New(t)
	b := newTestIndexBuilder(t)
	raw := b.sign(t, defaultTestPayload(), "root")

	// A different (unrelated) anchor set: this build's compiled-in anchors
	// don't include the key that actually signed this index at all.
	other := newTestIndexBuilder(t)

	_, err := index.Verify(raw, other.anchors(t), "")
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), index.CodeTrustAnchorExpired.Reason())
}

func TestVerify_RecognizedKeyCorruptedSignature(t *testing.T) {
	is := is.New(t)
	b := newTestIndexBuilder(t)
	raw := b.sign(t, defaultTestPayload(), "root")

	// Flip a byte inside the base64 signature string without re-signing —
	// the keyId is still recognized, but the ed25519 verification must fail.
	corrupted := make([]byte, len(raw))
	copy(corrupted, raw)
	idx := indexOfSignatureByte(t, corrupted)
	if corrupted[idx] == 'A' {
		corrupted[idx] = 'B'
	} else {
		corrupted[idx] = 'A'
	}

	_, err := index.Verify(corrupted, b.anchors(t), "")
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), index.CodeIndexIntegrity.Reason())
}

// indexOfSignatureByte finds a byte inside the `"signature":"...."` value so
// the corruption test flips something that actually changes the decoded
// signature bytes, not incidental JSON structure.
func indexOfSignatureByte(t *testing.T, raw []byte) int {
	t.Helper()
	marker := []byte(`"signature":"`)
	for i := 0; i+len(marker) < len(raw); i++ {
		match := true
		for j, m := range marker {
			if raw[i+j] != m {
				match = false
				break
			}
		}
		if match {
			return i + len(marker) + 2
		}
	}
	t.Fatal("could not locate signature value in raw index bytes")
	return -1
}

func TestVerify_TamperedPayloadFieldWithoutResigning(t *testing.T) {
	is := is.New(t)
	b := newTestIndexBuilder(t)
	raw := b.sign(t, defaultTestPayload(), "root")

	tampered := []byte(string(raw)) // copy
	tampered = []byte(replaceOnce(string(tampered), `"example"`, `"tampered"`))

	_, err := index.Verify(tampered, b.anchors(t), "")
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), index.CodeIndexIntegrity.Reason())
}

func replaceOnce(s, old, replacement string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + replacement + s[i+len(old):]
		}
	}
	return s
}

func TestVerify_SchemaTooNewRefusesBeforeTypedUnmarshal(t *testing.T) {
	is := is.New(t)
	b := newTestIndexBuilder(t)
	payload := defaultTestPayload()
	payload.SchemaVersion = index.MaxSupportedSchemaVersion + 1
	raw := b.sign(t, payload, "root")

	_, err := index.Verify(raw, b.anchors(t), "")
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), index.CodeSchemaTooNew.Reason())
}

func TestVerify_NoSignaturesAtAll(t *testing.T) {
	is := is.New(t)
	b := newTestIndexBuilder(t)
	raw := b.sign(t, defaultTestPayload()) // zero roles => empty signatures[]

	_, err := index.Verify(raw, b.anchors(t), "")
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), index.CodeTrustAnchorExpired.Reason())
}

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

// payload is a minimal but schema-shaped connectors-index payload. Processors
// carries `omitempty` so the many tests that leave it nil sign byte-identical
// bytes to the pre-processor shape (mirrors index.Payload's own omitempty).
type testPayload struct {
	SchemaVersion int               `json:"schemaVersion"`
	Index         index.IndexMeta   `json:"index"`
	Connectors    []index.Connector `json:"connectors"`
	Processors    []index.Processor `json:"processors,omitempty"`
}

// withProcessor returns a copy of p carrying one minimal, schema-shaped
// processor entry — used by the freshness content-subtree (D4) and
// forward-compat tests.
func withProcessor(p testPayload) testPayload {
	p.Processors = []index.Processor{
		{
			Name: "example-processor",
			Publisher: index.Publisher{
				ExpectedOIDCIssuer:      "https://token.actions.githubusercontent.com",
				ExpectedIdentityPattern: `^https://github\.com/ExampleOrg/conduit-processor-example/\.github/workflows/publish\.yml@refs/tags/v.*$`,
			},
			Versions: []index.ProcessorVersion{
				{
					Version: "0.1.0", MinConduitVersion: "0.19.0", MinProtocolVersion: "0.14.0",
					Artifact: index.Artifact{
						OS: "wasip1", Arch: "wasm", Kind: "wasm-processor",
						URL: "https://example/proc_0.1.0_wasip1_wasm.wasm", SHA256: "abc123", Size: 4096,
						Signature: index.SignatureRef{BundleURL: "https://example/proc.wasm.sigstore.json"},
					},
				},
			},
		},
	}
	return p
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

	lastHash, err := index.HashContentSubtree(payload.Connectors, payload.Processors)
	is.NoErr(err)

	// A "heartbeat" re-sign: same content subtree, bumped version/timestamp,
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

	lastHash, err := index.HashContentSubtree(payload.Connectors, payload.Processors)
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

// TestVerify_FreshnessSignatureWithMutatedProcessorsRequiresRoot is the D4
// regression guard (design doc 20260727-registry-processor-artifacts, failure
// mode 2): the freshness content subtree covers processors[] too, so a
// freshness re-sign that mutates ONLY processors[] — leaving connectors[]
// byte-identical to the last root-verified content — must be REFUSED. Without
// the subtree widening (HashConnectors over connectors[] alone), this attack
// would have silently authorized a changed processor tree on the unattended
// freshness key: a content-authorization escalation. This test fails on the
// pre-D4 code and passes only with the widened HashContentSubtree.
func TestVerify_FreshnessSignatureWithMutatedProcessorsRequiresRoot(t *testing.T) {
	is := is.New(t)
	b := newTestIndexBuilder(t)
	payload := withProcessor(defaultTestPayload())

	// The last root-verified content hash covers BOTH collections.
	lastHash, err := index.HashContentSubtree(payload.Connectors, payload.Processors)
	is.NoErr(err)

	// Connectors[] stays byte-identical; ONLY the processor artifact URL is
	// swapped (e.g. repointed at an attacker-controlled .wasm). A subtree that
	// covered connectors[] alone would accept this on freshness; it must not.
	payload.Processors[0].Versions[0].Artifact.URL = "https://attacker.example/evil.wasm"
	payload.Index.Version = 2
	raw := b.sign(t, payload, "freshness")

	_, err = index.Verify(raw, b.anchors(t), lastHash)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), index.CodeIndexIntegrity.Reason())
}

// TestVerify_FreshnessSignatureWithMatchingProcessorsAccepted is the positive
// half of D4: a genuine heartbeat re-sign of a processor-bearing index (both
// collections byte-identical, only version/timestamp bumped) is still accepted
// on the freshness key alone — the widening refuses mutation, not legitimate
// freshness.
func TestVerify_FreshnessSignatureWithMatchingProcessorsAccepted(t *testing.T) {
	is := is.New(t)
	b := newTestIndexBuilder(t)
	payload := withProcessor(defaultTestPayload())

	lastHash, err := index.HashContentSubtree(payload.Connectors, payload.Processors)
	is.NoErr(err)

	payload.Index.Version = 2
	payload.Index.Timestamp = time.Now().UTC()
	raw := b.sign(t, payload, "freshness")

	vi, err := index.Verify(raw, b.anchors(t), lastHash)
	is.NoErr(err)
	is.True(vi.Verified)
	is.True(!vi.RootVerified)
	is.Equal(len(vi.Payload.Processors), 1)
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

// legacyPayload is the schemaVersion-1 payload shape as it existed BEFORE
// processors[] was added — a faithful stand-in for an older Conduit build's
// typed struct. It has no Processors field at all.
type legacyPayload struct {
	SchemaVersion int               `json:"schemaVersion"`
	Index         index.IndexMeta   `json:"index"`
	Connectors    []index.Connector `json:"connectors"`
}

// TestVerify_ForwardCompat_OlderClientIgnoresProcessors is the design doc's
// required upgrade test (Upgrade/rollback; failure mode 1). It proves the
// additive-under-schemaVersion-1 promise from both ends:
//
//   - The signature is computed over the WHOLE payload bytes (processors[]
//     included), so an older client — which canonicalizes and verifies those
//     exact bytes — still verifies successfully. Adding processors[] does NOT
//     invalidate the signature for anyone.
//   - An older typed struct (legacyPayload, no Processors field) unmarshals the
//     same payload fine: Go silently ignores the unknown "processors" key,
//     reads connectors[] normally, and sees schemaVersion 1 (NOT
//     CodeSchemaTooNew — MaxSupportedSchemaVersion stays 1).
//   - A current build (index.Verify) sees and reads processors[].
func TestVerify_ForwardCompat_OlderClientIgnoresProcessors(t *testing.T) {
	is := is.New(t)
	b := newTestIndexBuilder(t)

	payload := withProcessor(defaultTestPayload())
	raw := b.sign(t, payload, "root")

	// (a) Current build verifies and SEES processors[].
	vi, err := index.Verify(raw, b.anchors(t), "")
	is.NoErr(err)
	is.True(vi.Verified)
	is.Equal(vi.Payload.SchemaVersion, 1) // schemaVersion stays 1
	is.Equal(len(vi.Payload.Connectors), 1)
	is.Equal(len(vi.Payload.Processors), 1)
	is.Equal(vi.Payload.Processors[0].Name, "example-processor")

	// (b) Older client: extract the exact payload bytes the client received and
	// unmarshal into the pre-processor typed struct. connectors[] reads fine;
	// processors[] is silently ignored; no error; schemaVersion still 1.
	var env struct {
		Payload json.RawMessage `json:"payload"`
	}
	is.NoErr(json.Unmarshal(raw, &env))

	var legacy legacyPayload
	is.NoErr(json.Unmarshal(env.Payload, &legacy))
	is.Equal(legacy.SchemaVersion, 1)
	is.Equal(len(legacy.Connectors), 1)
	is.Equal(legacy.Connectors[0].Name, "example")

	// (c) And the signature still verifies over those whole bytes — the older
	// client's crypto check is unaffected by the field it will ignore. (We
	// re-run Verify here to stand in for the older client's identical
	// canonicalize+verify of the bytes it holds; the point is the SAME raw
	// bytes both verify AND legacy-parse.)
	_, err = index.Verify(raw, b.anchors(t), "")
	is.NoErr(err)
}

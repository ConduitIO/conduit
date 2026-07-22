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

// This file proves RunAudit against the REAL registry.TrustedVerifier — the
// exact production wiring cmd/conduit/root/connectors/audit.go uses — not
// just the fakeIndexVerifier test double connectoraudit_test.go uses for
// the bulk of the finding-table logic. This is the "audit reuses PR-2's
// verification, no lower-trust shortcut path" guarantee exercised for real:
// a hand-signed (ed25519) index, verified via the SAME index.Verify code
// path install.go's TrustedVerifier.VerifyIndex calls.
package registry_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/index"
)

// auditTrustCoreFixture builds a real, ed25519-signed test index and a
// registry.TrustedVerifier wired with the matching trust anchors — the
// minimal real cryptography needed to prove genuine reuse, without
// depending on sigstore-go/live Sigstore material (audit never re-verifies
// per-artifact signatures — see connectoraudit.go's doc comment: only the
// INDEX is re-verified, exactly as install.go does).
type auditTrustCoreFixture struct {
	rootPub  ed25519.PublicKey
	rootPriv ed25519.PrivateKey
}

func newAuditTrustCoreFixture(t *testing.T) *auditTrustCoreFixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	return &auditTrustCoreFixture{rootPub: pub, rootPriv: priv}
}

func (f *auditTrustCoreFixture) anchors(t *testing.T) index.TrustAnchors {
	t.Helper()
	keyID, err := index.KeyID(f.rootPub)
	require.NoError(t, err)
	return index.TrustAnchors{Roots: map[string]ed25519.PublicKey{keyID: f.rootPub}}
}

type auditSigJSON struct {
	Role      string `json:"role"`
	KeyID     string `json:"keyId"`
	Algorithm string `json:"algorithm"`
	Signature string `json:"signature"`
}

// sign marshals payload, canonicalizes it, and signs it with the root key,
// returning the full raw {payload, signatures[]} envelope bytes RunAudit's
// fetchIndexRawFrom/index.Fetch path expects.
func (f *auditTrustCoreFixture) sign(t *testing.T, payload index.Payload) []byte {
	t.Helper()
	payloadRaw, err := json.Marshal(payload)
	require.NoError(t, err)
	canonical, err := index.Canonicalize(payloadRaw)
	require.NoError(t, err)

	keyID, err := index.KeyID(f.rootPub)
	require.NoError(t, err)
	sig := ed25519.Sign(f.rootPriv, canonical)

	env := struct {
		Payload    json.RawMessage `json:"payload"`
		Signatures []auditSigJSON  `json:"signatures"`
	}{
		Payload: payloadRaw,
		Signatures: []auditSigJSON{{
			Role: "root", KeyID: keyID, Algorithm: "ed25519",
			Signature: base64.StdEncoding.EncodeToString(sig),
		}},
	}
	raw, err := json.Marshal(env)
	require.NoError(t, err)
	return raw
}

func writeRawIndexFile(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path
}

func TestRunAudit_RealTrustedVerifier_YankedVersion_Fails(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, "postgres", "0.14.0", "bytes")

	f := newAuditTrustCoreFixture(t)
	payload := index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: 1, Timestamp: time.Now().UTC()},
		Connectors: []index.Connector{{
			Name:      "postgres",
			Publisher: index.Publisher{ExpectedOIDCIssuer: "https://token.actions.githubusercontent.com"},
			Versions: []index.ConnectorVersion{
				{Version: "0.14.0", Yanked: &index.YankReason{Reason: "data-loss bug"}},
			},
		}},
	}
	raw := f.sign(t, payload)
	indexPath := writeRawIndexFile(t, raw)

	verifier := &registry.TrustedVerifier{
		Anchors:   f.anchors(t),
		StatePath: filepath.Join(dir, ".registry", "index-state.json"),
	}

	report, err := registry.RunAudit(context.Background(), registry.AuditOptions{
		ConnectorsPath: dir, IndexFile: indexPath, IndexVerifier: verifier,
	})
	require.NoError(t, err)
	require.Len(t, report.Findings, 1)
	assert.Equal(t, registry.FindingYankedVersion, report.Findings[0].Finding)
	assert.Equal(t, registry.AuditStatusFail, report.Findings[0].Status)
	assert.NotZero(t, report.ExitCode())
}

// TestRunAudit_RealTrustedVerifier_TamperedIndex_HardFails proves a
// tampered (signature no longer valid) index fails the WHOLE audit run
// distinctly — never a per-connector finding — via the real verification
// path.
func TestRunAudit_RealTrustedVerifier_TamperedIndex_HardFails(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, "postgres", "0.14.1", "bytes")

	f := newAuditTrustCoreFixture(t)
	payload := index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: 1, Timestamp: time.Now().UTC()},
		Connectors: []index.Connector{{
			Name:      "postgres",
			Publisher: index.Publisher{ExpectedOIDCIssuer: "https://token.actions.githubusercontent.com"},
			Versions:  []index.ConnectorVersion{{Version: "0.14.1"}},
		}},
	}
	raw := f.sign(t, payload)

	// Tamper: flip a byte inside the payload after signing, without
	// re-signing — the signature no longer verifies against the mutated
	// canonical content.
	tampered := make([]byte, len(raw))
	copy(tampered, raw)
	for i, b := range tampered {
		if b == 'p' { // first occurrence inside "postgres"/"payload" etc.
			tampered[i] = 'q'
			break
		}
	}
	indexPath := writeRawIndexFile(t, tampered)

	verifier := &registry.TrustedVerifier{
		Anchors:   f.anchors(t),
		StatePath: filepath.Join(dir, ".registry", "index-state.json"),
	}

	_, err := registry.RunAudit(context.Background(), registry.AuditOptions{
		ConnectorsPath: dir, IndexFile: indexPath, IndexVerifier: verifier,
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, index.CodeIndexIntegrity, ce.Code)
}

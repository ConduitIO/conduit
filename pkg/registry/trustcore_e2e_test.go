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

// This file is the security-warranty suite for the trust core wired into
// the FULL registry.Install pipeline (plan-v2 §15.2): every ERR_*/Code*
// path fires through the REAL pipeline — proving check ORDERING (e.g.
// corruption is checked before signature, index integrity before artifact
// identity), not just that each check works standalone in isolation
// (pkg/registry/trust's own tests already cover that).
//
// # A deliberate simplification, stated plainly
//
// registry.TrustedVerifier.VerifyIndex is exercised completely unchanged
// (index signing here is real, hand-rolled ed25519 — no sigstore-go
// dependency, so no simplification is needed or taken). For the ARTIFACT
// verification gate, this file uses entityArtifactVerifier — a thin test
// wrapper that calls the exact same trust.VerifySignedEntitySignature /
// VerifySignedEntityAttestation / CheckProvenanceBinding functions
// registry.TrustedVerifier.VerifyArtifact calls, but against an
// already-parsed verify.SignedEntity (a sigstore-go
// pkg/testing/ca.VirtualSigstore-signed TestEntity) rather than raw JSON
// bundle bytes. This is necessary because TestEntity does not expose a way
// to marshal itself back into a real Sigstore bundle JSON document (its
// fields are unexported) — the JSON bundle round-trip itself is already
// covered by pkg/registry/trust/sigstore_test.go and
// sigstore_realworld_test.go (a REAL, non-virtual Sigstore-signed bundle,
// loaded from actual JSON bytes on disk). What THIS file adds on top is
// what those don't cover: that registry.Install's pipeline calls the
// ArtifactVerifier at the right point, in the right order, with the right
// digest, and correctly propagates/refuses on every outcome.
package registry_test

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/index"
	"github.com/conduitio/conduit/pkg/registry/trust"
)

const (
	e2eOIDCIssuer = "https://token.actions.githubusercontent.com"
	e2ePublisher  = "https://github.com/ExampleOrg/example-connector/.github/workflows/publish.yml@refs/tags/v1.0.0"
	e2eAttacker   = "https://github.com/AttackerOrg/example-connector/.github/workflows/publish.yml@refs/tags/v1.0.0"
	e2ePattern    = `^https://github\.com/ExampleOrg/example-connector/\.github/workflows/publish\.yml@refs/tags/v.*$`
)

// entityArtifactVerifier — see file doc comment.
type entityArtifactVerifier struct {
	signatureEntity   verify.SignedEntity // nil => no signature at all (models ErrUnsigned)
	provenanceEntity  verify.SignedEntity // nil => no provenance bundle present
	trustedRoot       root.TrustedMaterial
	requireProvenance bool
}

var (
	_ registry.IndexVerifier    = (*registry.TrustedVerifier)(nil)
	_ registry.ArtifactVerifier = (*entityArtifactVerifier)(nil)
)

func (v *entityArtifactVerifier) VerifyArtifact(_ context.Context, ref registry.ArtifactRef, identity trust.PinnedIdentity) (registry.VerifyResult, error) {
	if v.signatureEntity == nil {
		_, err := trust.VerifyArtifactSignature(context.Background(), ref.Digest[:], nil, identity) // exercises the real empty-bytes path -> ErrUnsigned
		return registry.VerifyResult{}, err
	}
	verifiedIdentity, err := trust.VerifySignedEntitySignature(v.signatureEntity, ref.Digest[:], identity, v.trustedRoot)
	if err != nil {
		return registry.VerifyResult{}, err
	}
	if v.provenanceEntity == nil {
		if v.requireProvenance {
			return registry.VerifyResult{}, conduiterr.New(trust.CodeProvenanceInvalid, "provenance required but absent")
		}
		return registry.VerifyResult{Signed: true, VerifiedIdentity: verifiedIdentity}, nil
	}
	statement, err := trust.VerifySignedEntityAttestation(v.provenanceEntity, identity, v.trustedRoot)
	if err != nil {
		return registry.VerifyResult{}, err
	}
	if err := trust.CheckProvenanceBinding(statement, ref.Digest, trust.ExpectedBuilderID); err != nil {
		return registry.VerifyResult{}, err
	}
	return registry.VerifyResult{Signed: true, VerifiedIdentity: verifiedIdentity}, nil
}

// e2eFixture bundles everything a test needs: a real signed index (served
// over HTTP, or as file bytes), the artifact bytes/digest, and a
// TrustedVerifier for the index side.
type e2eFixture struct {
	srv             *httptest.Server
	indexURL        string
	archiveBytes    []byte
	digest          [32]byte
	connectorsPath  string
	trustedVerifier *registry.TrustedVerifier
}

func newE2EFixture(t *testing.T, name, version string, indexVersion int64, indexTimestamp time.Time) *e2eFixture {
	t.Helper()
	archiveBytes := buildInstallArchive(t, name)
	digest := sha256.Sum256(archiveBytes)

	rootPub, rootPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	rootKeyID, err := index.KeyID(rootPub)
	require.NoError(t, err)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)

	payload := index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: indexVersion, Timestamp: indexTimestamp},
		Connectors: []index.Connector{
			{
				Name: name,
				Publisher: index.Publisher{
					ExpectedOIDCIssuer:      e2eOIDCIssuer,
					ExpectedIdentityPattern: e2ePattern,
				},
				Versions: []index.ConnectorVersion{
					{
						Version: version, MinConduitVersion: "0.1.0", MinProtocolVersion: "0.1.0",
						Artifacts: []index.Artifact{
							{
								OS: runtime.GOOS, Arch: runtime.GOARCH, Kind: registry.StandaloneArtifactKind,
								URL: srv.URL + "/artifact.tar.gz", SHA256: hexEncode(digest), Size: int64(len(archiveBytes)),
								Signature: index.SignatureRef{BundleURL: srv.URL + "/sig.json"},
							},
						},
					},
				},
			},
		},
	}
	payloadRaw, err := json.Marshal(payload)
	require.NoError(t, err)
	canonical, err := index.Canonicalize(payloadRaw)
	require.NoError(t, err)
	sig := ed25519.Sign(rootPriv, canonical)

	envelope := map[string]any{
		"payload": payload,
		"signatures": []map[string]any{
			{"role": "root", "keyId": rootKeyID, "algorithm": "ed25519", "signature": base64.StdEncoding.EncodeToString(sig)},
		},
	}
	indexBytes, err := json.Marshal(envelope)
	require.NoError(t, err)

	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(indexBytes) })
	mux.HandleFunc("/artifact.tar.gz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archiveBytes) })
	mux.HandleFunc("/sig.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }) // unused: entityArtifactVerifier ignores bundle bytes

	connectorsPath := t.TempDir()
	t.Cleanup(srv.Close)

	return &e2eFixture{
		srv: srv, indexURL: srv.URL + "/index.json",
		archiveBytes: archiveBytes, digest: digest, connectorsPath: connectorsPath,
		trustedVerifier: &registry.TrustedVerifier{
			Anchors:   index.TrustAnchors{Roots: map[string]ed25519.PublicKey{rootKeyID: rootPub}},
			StatePath: registry.IndexStatePath(connectorsPath),
		},
	}
}

func (f *e2eFixture) opts(artifactVerifier registry.ArtifactVerifier, name string) registry.InstallOptions {
	return registry.InstallOptions{
		Name: name, ConnectorsPath: f.connectorsPath, IndexURL: f.indexURL,
		IndexVerifier: f.trustedVerifier, ArtifactVerifier: artifactVerifier,
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0", InstalledBy: "test-operator",
	}
}

func newVirtualCAWithRoot(t *testing.T) (*ca.VirtualSigstore, root.TrustedMaterial) {
	t.Helper()
	vca, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	tm, err := root.NewTrustedRoot(root.TrustedRootMediaType01, vca.FulcioCertificateAuthorities(), vca.CTLogs(), vca.TimestampingAuthorities(), vca.RekorLogs())
	require.NoError(t, err)
	return vca, tm
}

// TestE2E_HappyPath_ValidSignatureAndProvenance_Installs is the north-star
// test (plan-v2 §9's E2E test, adapted per this file's entity-based
// simplification): a correctly signed artifact with correctly bound SLSA
// provenance, against a correctly signed index, installs successfully end
// to end and records the verified identity in the manifest.
func TestE2E_HappyPath_ValidSignatureAndProvenance_Installs(t *testing.T) {
	f := newE2EFixture(t, "example-connector", "1.0.0", 1, time.Now())
	vca, tm := newVirtualCAWithRoot(t)

	sigEntity, err := vca.Sign(e2ePublisher, e2eOIDCIssuer, f.archiveBytes)
	require.NoError(t, err)
	provBody, err := json.Marshal(map[string]any{
		"_type": "https://in-toto.io/Statement/v1",
		"subject": []map[string]any{{"name": "example-connector", "digest": map[string]string{
			"sha256": hexEncode(f.digest),
		}}},
		"predicateType": "https://slsa.dev/provenance/v1",
		"predicate":     map[string]any{"runDetails": map[string]any{"builder": map[string]any{"id": trust.ExpectedBuilderID}}},
	})
	require.NoError(t, err)
	provEntity, err := vca.Attest(e2ePublisher, e2eOIDCIssuer, provBody)
	require.NoError(t, err)

	av := &entityArtifactVerifier{signatureEntity: sigEntity, provenanceEntity: provEntity, trustedRoot: tm}

	res, err := registry.Install(context.Background(), f.opts(av, "example-connector"))
	require.NoError(t, err)
	require.NotNil(t, res)

	m, err := registry.LoadManifest(filepath.Join(f.connectorsPath, ".registry", "manifest.json"))
	require.NoError(t, err)
	entry, ok := m.Installs["example-connector@1.0.0"]
	require.True(t, ok)
	assert.True(t, entry.Signed)
	assert.False(t, entry.AllowUnsigned)
	assert.NotEmpty(t, entry.VerifiedIdentity)
}

// TestE2E_Unsigned_Refuses proves CodeUnsigned fires through the real
// pipeline and installs nothing.
func TestE2E_Unsigned_Refuses(t *testing.T) {
	f := newE2EFixture(t, "example-connector", "1.0.0", 1, time.Now())
	av := &entityArtifactVerifier{signatureEntity: nil}

	_, err := registry.Install(context.Background(), f.opts(av, "example-connector"))
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, trust.CodeUnsigned, ce.Code)
	assertNoInstalledBinary(t, f.connectorsPath)
}

// TestE2E_IdentityMismatch_Refuses proves a validly-signed artifact by the
// WRONG identity refuses with CodeIdentityMismatch, distinct from
// CodeUnsigned.
func TestE2E_IdentityMismatch_Refuses(t *testing.T) {
	f := newE2EFixture(t, "example-connector", "1.0.0", 1, time.Now())
	vca, tm := newVirtualCAWithRoot(t)

	sigEntity, err := vca.Sign(e2eAttacker, e2eOIDCIssuer, f.archiveBytes)
	require.NoError(t, err)
	av := &entityArtifactVerifier{signatureEntity: sigEntity, trustedRoot: tm}

	_, err = registry.Install(context.Background(), f.opts(av, "example-connector"))
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, trust.CodeIdentityMismatch, ce.Code)
	assertNoInstalledBinary(t, f.connectorsPath)
}

// TestE2E_ProvenanceSubjectMismatch_Refuses proves a validly-signed artifact
// with a validly-signed BUT WRONG-DIGEST provenance attestation refuses
// with CodeProvenanceInvalid, distinct from CodeIdentityMismatch (the
// signing identity is perfectly correct; only the provenance's semantic
// claim is wrong).
func TestE2E_ProvenanceSubjectMismatch_Refuses(t *testing.T) {
	f := newE2EFixture(t, "example-connector", "1.0.0", 1, time.Now())
	vca, tm := newVirtualCAWithRoot(t)

	sigEntity, err := vca.Sign(e2ePublisher, e2eOIDCIssuer, f.archiveBytes)
	require.NoError(t, err)
	wrongDigest := sha256.Sum256([]byte("not the artifact"))
	provBody, err := json.Marshal(map[string]any{
		"_type": "https://in-toto.io/Statement/v1",
		"subject": []map[string]any{{"name": "example-connector", "digest": map[string]string{
			"sha256": hexEncode(wrongDigest),
		}}},
		"predicateType": "https://slsa.dev/provenance/v1",
		"predicate":     map[string]any{"runDetails": map[string]any{"builder": map[string]any{"id": trust.ExpectedBuilderID}}},
	})
	require.NoError(t, err)
	provEntity, err := vca.Attest(e2ePublisher, e2eOIDCIssuer, provBody)
	require.NoError(t, err)

	av := &entityArtifactVerifier{signatureEntity: sigEntity, provenanceEntity: provEntity, trustedRoot: tm}

	_, err = registry.Install(context.Background(), f.opts(av, "example-connector"))
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, trust.CodeProvenanceInvalid, ce.Code)
	assertNoInstalledBinary(t, f.connectorsPath)
}

// TestE2E_TamperedIndexField_RefusesIndexIntegrity proves mutating ANY
// index field (here: the artifact's sha256) without re-signing refuses with
// CodeIndexIntegrity BEFORE identity-pinning is ever reached — the
// verify-before-parse/verify-before-trust ordering the whole design depends
// on.
func TestE2E_TamperedIndexField_RefusesIndexIntegrity(t *testing.T) {
	f := newE2EFixture(t, "example-connector", "1.0.0", 1, time.Now())

	// Fetch, tamper with one byte of the served index's sha256 field, and
	// re-serve — WITHOUT re-signing.
	resp, err := http.Get(f.indexURL) //nolint:noctx // test-only httptest fetch
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	tampered := tamperFirstOccurrence(t, body, hexEncode(f.digest), flipHexChar(hexEncode(f.digest)))

	mux := http.NewServeMux()
	srv2 := httptest.NewServer(mux)
	t.Cleanup(srv2.Close)
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(tampered) })
	mux.HandleFunc("/artifact.tar.gz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(f.archiveBytes) })

	opts := f.opts(&entityArtifactVerifier{}, "example-connector")
	opts.IndexURL = srv2.URL + "/index.json"

	_, err = registry.Install(context.Background(), opts)
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, index.CodeIndexIntegrity, ce.Code)
	assertNoInstalledBinary(t, f.connectorsPath)
}

func flipHexChar(s string) string {
	r := []byte(s)
	if r[0] == '0' {
		r[0] = '1'
	} else {
		r[0] = '0'
	}
	return string(r)
}

func tamperFirstOccurrence(t *testing.T, body []byte, old, newStr string) []byte {
	t.Helper()
	s := string(body)
	idx := -1
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			idx = i
			break
		}
	}
	require.GreaterOrEqual(t, idx, 0, "could not find %q in served index body to tamper with", old)
	return []byte(s[:idx] + newStr + s[idx+len(old):])
}

// TestE2E_RollbackIndex_Refuses proves a lower-version index (even if
// validly signed) refuses with CodeIndexRollback once a higher version has
// already been verified once on this connectorsPath.
func TestE2E_RollbackIndex_Refuses(t *testing.T) {
	name := "example-connector"

	// First install attempt (allowed to fail at the artifact gate — we only
	// care that index verification succeeds and persists version 5 as the
	// high-water mark) against index.version = 5.
	rootPub, rootPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	rootKeyID, err := index.KeyID(rootPub)
	require.NoError(t, err)

	connectorsPath := t.TempDir()
	buildIndex := func(version int64) []byte {
		archiveBytes := buildInstallArchive(t, name)
		digest := sha256.Sum256(archiveBytes)
		payload := index.Payload{
			SchemaVersion: 1, Index: index.IndexMeta{Version: version, Timestamp: time.Now()},
			Connectors: []index.Connector{{
				Name:      name,
				Publisher: index.Publisher{ExpectedOIDCIssuer: e2eOIDCIssuer, ExpectedIdentityPattern: e2ePattern},
				Versions: []index.ConnectorVersion{{
					Version: "1.0.0", MinConduitVersion: "0.1.0", MinProtocolVersion: "0.1.0",
					Artifacts: []index.Artifact{{
						OS: runtime.GOOS, Arch: runtime.GOARCH, Kind: registry.StandaloneArtifactKind,
						URL: "http://unused.invalid/a.tar.gz", SHA256: hexEncode(digest), Size: int64(len(archiveBytes)),
					}},
				}},
			}},
		}
		payloadRaw, err := json.Marshal(payload)
		require.NoError(t, err)
		canonical, err := index.Canonicalize(payloadRaw)
		require.NoError(t, err)
		sig := ed25519.Sign(rootPriv, canonical)
		envelope := map[string]any{"payload": payload, "signatures": []map[string]any{
			{"role": "root", "keyId": rootKeyID, "algorithm": "ed25519", "signature": base64.StdEncoding.EncodeToString(sig)},
		}}
		data, err := json.Marshal(envelope)
		require.NoError(t, err)
		return data
	}

	tv := &registry.TrustedVerifier{
		Anchors:   index.TrustAnchors{Roots: map[string]ed25519.PublicKey{rootKeyID: rootPub}},
		StatePath: registry.IndexStatePath(connectorsPath),
	}

	_, err = tv.VerifyIndex(context.Background(), buildIndex(5))
	require.NoError(t, err)

	_, err = tv.VerifyIndex(context.Background(), buildIndex(3))
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, index.CodeIndexRollback, ce.Code)
}

// TestE2E_StaleIndex_Refuses proves an index whose timestamp is older than
// maxStaleness refuses with CodeIndexStale, independently of rollback.
func TestE2E_StaleIndex_Refuses(t *testing.T) {
	f := newE2EFixture(t, "example-connector", "1.0.0", 1, time.Now().Add(-30*24*time.Hour))
	f.trustedVerifier.MaxStaleness = 7 * 24 * time.Hour

	_, err := registry.Install(context.Background(), f.opts(&entityArtifactVerifier{}, "example-connector"))
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, index.CodeIndexStale, ce.Code)
	assertNoInstalledBinary(t, f.connectorsPath)
}

// TestE2E_RevokedPublisher_Refuses and TestE2E_YankedVersion_Refuses are
// index-DATA checks (Resolve's job, pkg/registry/resolve.go) — no crypto
// involved, so they use passThroughVerifier for the artifact side (nothing
// should ever reach it) and prove the refusal happens at resolution instead.
func TestE2E_RevokedPublisher_Refuses(t *testing.T) {
	f := newE2EFixtureWithRevocation(t, true, false)
	_, err := registry.Install(context.Background(), f.opts(passThroughVerifier{}, "example-connector"))
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, trust.CodeIdentityRevoked, ce.Code)
	assertNoInstalledBinary(t, f.connectorsPath)
}

func TestE2E_YankedVersion_Refuses(t *testing.T) {
	f := newE2EFixtureWithRevocation(t, false, true)
	_, err := registry.Install(context.Background(), registry.InstallOptions{
		Name: "example-connector", Version: "1.0.0", ConnectorsPath: f.connectorsPath, IndexURL: f.indexURL,
		IndexVerifier: f.trustedVerifier, ArtifactVerifier: passThroughVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, index.CodeVersionYanked, ce.Code)
	assertNoInstalledBinary(t, f.connectorsPath)
}

// newE2EFixtureWithRevocation is newE2EFixture's revoked/yanked variant.
func newE2EFixtureWithRevocation(t *testing.T, revokedPublisher, yankedVersion bool) *e2eFixture {
	t.Helper()
	name := "example-connector"
	archiveBytes := buildInstallArchive(t, name)
	digest := sha256.Sum256(archiveBytes)

	rootPub, rootPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	rootKeyID, err := index.KeyID(rootPub)
	require.NoError(t, err)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)

	publisher := index.Publisher{ExpectedOIDCIssuer: e2eOIDCIssuer, ExpectedIdentityPattern: e2ePattern}
	if revokedPublisher {
		publisher.Revoked = &index.Revocation{Reason: "test: identity compromised"}
	}
	v := index.ConnectorVersion{
		Version: "1.0.0", MinConduitVersion: "0.1.0", MinProtocolVersion: "0.1.0",
		Artifacts: []index.Artifact{{
			OS: runtime.GOOS, Arch: runtime.GOARCH, Kind: registry.StandaloneArtifactKind,
			URL: srv.URL + "/artifact.tar.gz", SHA256: hexEncode(digest), Size: int64(len(archiveBytes)),
		}},
	}
	if yankedVersion {
		v.Yanked = &index.YankReason{Reason: "test: known-bad release"}
	}

	payload := index.Payload{
		SchemaVersion: 1, Index: index.IndexMeta{Version: 1, Timestamp: time.Now()},
		Connectors: []index.Connector{{Name: name, Publisher: publisher, Versions: []index.ConnectorVersion{v}}},
	}
	payloadRaw, err := json.Marshal(payload)
	require.NoError(t, err)
	canonical, err := index.Canonicalize(payloadRaw)
	require.NoError(t, err)
	sig := ed25519.Sign(rootPriv, canonical)
	envelope := map[string]any{"payload": payload, "signatures": []map[string]any{
		{"role": "root", "keyId": rootKeyID, "algorithm": "ed25519", "signature": base64.StdEncoding.EncodeToString(sig)},
	}}
	indexBytes, err := json.Marshal(envelope)
	require.NoError(t, err)

	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(indexBytes) })
	mux.HandleFunc("/artifact.tar.gz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archiveBytes) })

	connectorsPath := t.TempDir()
	t.Cleanup(srv.Close)

	return &e2eFixture{
		srv: srv, indexURL: srv.URL + "/index.json", archiveBytes: archiveBytes, digest: digest, connectorsPath: connectorsPath,
		trustedVerifier: &registry.TrustedVerifier{
			Anchors:   index.TrustAnchors{Roots: map[string]ed25519.PublicKey{rootKeyID: rootPub}},
			StatePath: registry.IndexStatePath(connectorsPath),
		},
	}
}

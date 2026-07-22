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

package registry_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
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
	"github.com/conduitio/conduit/pkg/registry/trust"
)

// writeTestBundleTar builds a bundle tarball by hand (rather than going
// through Bundle()'s own online prep pipeline), so each offline-side test
// can independently omit or mutate exactly one entry (a missing
// index-snapshot.json, tampered artifact bytes, and so on) without needing
// a full networked prep pass first. nil for signatureBundle/provenanceBundle
// omits that entry entirely, matching a real bundle whose artifact had no
// provenance attestation.
func writeTestBundleTar(path string, manifest registry.BundleManifest, artifactBytes, signatureBundle, provenanceBundle, indexSnapshot []byte) error {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()

	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)

	writeEntry := func(name string, data []byte) error {
		if data == nil {
			return nil
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data))}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}

	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := writeEntry("manifest.json", manifestBytes); err != nil {
		return err
	}
	if err := writeEntry("artifact", artifactBytes); err != nil {
		return err
	}
	if err := writeEntry("signature.bundle", signatureBundle); err != nil {
		return err
	}
	if err := writeEntry("provenance.bundle", provenanceBundle); err != nil {
		return err
	}
	if err := writeEntry("index-snapshot.json", indexSnapshot); err != nil {
		return err
	}

	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// unsignedArtifactVerifier always reports an unsigned result — used to
// prove Bundle() refuses to package an artifact that would not itself pass
// a normal install.
type unsignedArtifactVerifier struct{ passThroughVerifier }

func (unsignedArtifactVerifier) VerifyArtifact(context.Context, registry.ArtifactRef, trust.PinnedIdentity) (registry.VerifyResult, error) {
	return registry.VerifyResult{Signed: false}, nil
}

func TestBundle_FullPrepSucceeds(t *testing.T) {
	srv, indexURL, archiveBytes := newInstallTestServer(t, "postgres", "0.14.1")
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "postgres-0.14.1.bundle.tar.gz")
	res, err := registry.Bundle(context.Background(), registry.BundleOptions{
		Name: "postgres", Version: "0.14.1", OutputPath: out,
		IndexURL: indexURL, IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
	})
	require.NoError(t, err)
	assert.Equal(t, "postgres", res.Name)
	assert.Equal(t, "0.14.1", res.Version)
	assert.Equal(t, int64(len(archiveBytes)), res.Size)

	info, statErr := os.Stat(out)
	require.NoError(t, statErr)
	assert.Positive(t, info.Size())
}

func TestBundle_RefusesUnsignedArtifact(t *testing.T) {
	srv, indexURL, _ := newInstallTestServer(t, "postgres", "0.14.1")
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "postgres-0.14.1.bundle.tar.gz")
	_, err := registry.Bundle(context.Background(), registry.BundleOptions{
		Name: "postgres", Version: "0.14.1", OutputPath: out,
		IndexURL: indexURL, IndexVerifier: passThroughVerifier{}, ArtifactVerifier: unsignedArtifactVerifier{},
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeVerificationUnavailable, ce.Code)

	_, statErr := os.Stat(out)
	assert.True(t, os.IsNotExist(statErr), "bundle output must not be written when the artifact fails verification")
}

// bundleTrustCoreFixture reuses the same hand-signed-ed25519-index approach
// as connectoraudit_trustcore_test.go, so InstallFromBundle's offline index
// re-verification can be tested against the REAL registry.TrustedVerifier.
func realBundleIndexSnapshot(t *testing.T, f *auditTrustCoreFixture, payload index.Payload) []byte {
	t.Helper()
	return f.sign(t, payload)
}

func buildTestBundleTar(t *testing.T, manifest registry.BundleManifest, artifactBytes, signatureBundle, provenanceBundle, indexSnapshot []byte) string {
	t.Helper()
	// Reuses this package's own writeBundleTar indirectly via Bundle() would
	// require a full online prep pass; for these lower-level offline tests
	// we build the tarball by hand so each test can independently mutate one
	// specific piece (tampered artifact, stale snapshot, etc.).
	path := filepath.Join(t.TempDir(), "test.bundle.tar.gz")
	require.NoError(t, writeTestBundleTar(path, manifest, artifactBytes, signatureBundle, provenanceBundle, indexSnapshot))
	return path
}

func TestInstallFromBundle_FormatVersionTooNew_Refuses(t *testing.T) {
	dir := t.TempDir()
	manifest := registry.BundleManifest{BundleFormatVersion: registry.BundleFormatVersion + 1, Name: "postgres", Version: "0.14.1"}
	path := buildTestBundleTar(t, manifest, []byte("artifact"), nil, nil, []byte("{}"))

	_, err := registry.InstallFromBundle(context.Background(), registry.InstallBundleOptions{
		BundlePath: path, ConnectorsPath: dir, Verifier: &registry.TrustedVerifier{},
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, conduiterr.CodeInvalidArgument, ce.Code)
}

func TestInstallFromBundle_MissingEntries_Refuses(t *testing.T) {
	dir := t.TempDir()
	// index-snapshot.json omitted entirely.
	path := buildTestBundleTar(t, registry.BundleManifest{BundleFormatVersion: 1, Name: "postgres", Version: "0.14.1"}, []byte("artifact"), nil, nil, nil)

	_, err := registry.InstallFromBundle(context.Background(), registry.InstallBundleOptions{
		BundlePath: path, ConnectorsPath: dir, Verifier: &registry.TrustedVerifier{},
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeArchiveInvalid, ce.Code)
}

// TestInstallFromBundle_TamperedArtifact_Refuses is AC15: a bundle whose
// embedded artifact bytes were tampered with after bundling fails the
// digest check and never touches --connectors.path.
func TestInstallFromBundle_TamperedArtifact_Refuses(t *testing.T) {
	dir := t.TempDir()
	f := newAuditTrustCoreFixture(t)
	payload := index.Payload{
		SchemaVersion: 1, Index: index.IndexMeta{Version: 1, Timestamp: time.Now().UTC()},
		Connectors: []index.Connector{{
			Name:      "postgres",
			Publisher: index.Publisher{ExpectedOIDCIssuer: "x"},
			Versions: []index.ConnectorVersion{{
				Version: "0.14.1", MinConduitVersion: "0.1.0", MinProtocolVersion: "0.1.0",
				Artifacts: []index.Artifact{{
					OS: "linux", Arch: "amd64", Kind: registry.StandaloneArtifactKind,
					URL: "https://example.test/artifact", SHA256: sha256Hex("original-bytes"), Size: 14,
				}},
			}},
		}},
	}
	snapshot := realBundleIndexSnapshot(t, f, payload)

	manifest := registry.BundleManifest{
		BundleFormatVersion: 1, Name: "postgres", Version: "0.14.1", OS: "linux", Arch: "amd64",
		SHA256: "sha256:" + sha256Hex("original-bytes"), Size: 14,
	}
	// Tampered: the bundled artifact bytes differ from what manifest.json
	// (and the index snapshot) both declare.
	path := buildTestBundleTar(t, manifest, []byte("tampered-bytes"), []byte("sig"), nil, snapshot)

	verifier := &registry.TrustedVerifier{Anchors: f.anchors(t), StatePath: filepath.Join(dir, ".registry", "index-state.json")}
	_, err := registry.InstallFromBundle(context.Background(), registry.InstallBundleOptions{
		BundlePath: path, ConnectorsPath: dir, Verifier: verifier,
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeCorruptDownload, ce.Code)

	// Never touches --connectors.path.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		assert.NotEqual(t, "conduit-connector-postgres_0.14.1", e.Name())
	}
}

// TestInstallFromBundle_YankedInSnapshot_Refuses is AC16: a bundle for a
// version already yanked in the BUNDLED snapshot refuses with the same
// yanked-version error an online install would give — proving the offline
// path doesn't silently skip the revocation check just because the network
// is down. This short-circuits before artifact verification is ever
// reached (Resolve refuses first), so it needs no real signature material.
func TestInstallFromBundle_YankedInSnapshot_Refuses(t *testing.T) {
	dir := t.TempDir()
	f := newAuditTrustCoreFixture(t)
	payload := index.Payload{
		SchemaVersion: 1, Index: index.IndexMeta{Version: 1, Timestamp: time.Now().UTC()},
		Connectors: []index.Connector{{
			Name:      "postgres",
			Publisher: index.Publisher{ExpectedOIDCIssuer: "x"},
			Versions: []index.ConnectorVersion{{
				Version: "0.14.1", Yanked: &index.YankReason{Reason: "bad release"},
			}},
		}},
	}
	snapshot := realBundleIndexSnapshot(t, f, payload)
	manifest := registry.BundleManifest{BundleFormatVersion: 1, Name: "postgres", Version: "0.14.1", OS: "linux", Arch: "amd64"}
	path := buildTestBundleTar(t, manifest, []byte("bytes"), nil, nil, snapshot)

	verifier := &registry.TrustedVerifier{Anchors: f.anchors(t), StatePath: filepath.Join(dir, ".registry", "index-state.json")}
	_, err := registry.InstallFromBundle(context.Background(), registry.InstallBundleOptions{
		BundlePath: path, ConnectorsPath: dir, Verifier: verifier,
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, index.CodeVersionYanked, ce.Code)
}

// TestInstallFromBundle_StaleBundle_RefusesByDefault_AllowsWithOverride is
// AC17: a bundle whose snapshot is older than maxStaleness refuses by
// default, and only proceeds (past the index-verification stage — this
// test does not carry real signature material, so it asserts the run moves
// on to the NEXT stage rather than reaching full success) with an explicit
// override.
func TestInstallFromBundle_StaleBundle_RefusesByDefault_AllowsWithOverride(t *testing.T) {
	dir := t.TempDir()
	f := newAuditTrustCoreFixture(t)
	payload := index.Payload{
		SchemaVersion: 1,
		// Timestamp far in the past: older than index.DefaultMaxStaleness (7 days).
		Index: index.IndexMeta{Version: 1, Timestamp: time.Now().Add(-30 * 24 * time.Hour)},
		Connectors: []index.Connector{{
			Name: "postgres",
			Publisher: index.Publisher{
				ExpectedOIDCIssuer:      "https://token.actions.githubusercontent.com",
				ExpectedIdentityPattern: `^https://github\.com/ExampleOrg/postgres/\.github/workflows/publish\.yml@refs/tags/v.*$`,
			},
			Versions: []index.ConnectorVersion{{
				Version: "0.14.1", MinConduitVersion: "0.1.0", MinProtocolVersion: "0.1.0",
				Artifacts: []index.Artifact{{
					OS: "linux", Arch: "amd64", Kind: registry.StandaloneArtifactKind,
					URL: "https://example.test/artifact", SHA256: sha256Hex("bytes"), Size: 5,
				}},
			}},
		}},
	}
	snapshot := realBundleIndexSnapshot(t, f, payload)
	manifest := registry.BundleManifest{
		BundleFormatVersion: 1, Name: "postgres", Version: "0.14.1", OS: "linux", Arch: "amd64",
		SHA256: "sha256:" + sha256Hex("bytes"), Size: 5,
	}
	path := buildTestBundleTar(t, manifest, []byte("bytes"), nil, nil, snapshot)

	verifier := &registry.TrustedVerifier{Anchors: f.anchors(t), StatePath: filepath.Join(dir, ".registry", "index-state.json")}

	// Default: refuses.
	_, err := registry.InstallFromBundle(context.Background(), registry.InstallBundleOptions{
		BundlePath: path, ConnectorsPath: dir, Verifier: verifier,
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeBundleStale, ce.Code)

	// With the full gate satisfied (operator policy + env var, non-interactive
	// path): the staleness check is overridden and execution proceeds past
	// it — to the NEXT stage, artifact verification, which fails with
	// trust.CodeUnsigned (no real signature bundle in this fixture) —
	// proving the override specifically unblocked staleness (and nothing
	// else: resolution/compatibility/artifact-selection all had to succeed
	// too for this exact failure point to be reached).
	_, err = registry.InstallFromBundle(context.Background(), registry.InstallBundleOptions{
		BundlePath: path, ConnectorsPath: dir, Verifier: verifier,
		AllowStaleBundle: true, OperatorAllowStaleBundle: true, EnvVarSet: true,
	})
	require.Error(t, err)
	ce, ok = conduiterr.Get(err)
	require.True(t, ok)
	assert.NotEqual(t, registry.CodeBundleStale, ce.Code, "staleness override should have unblocked the index check")
	assert.Equal(t, trust.CodeUnsigned, ce.Code, "expected to reach artifact verification and fail there (no real signature bundle in this fixture)")

	// The override itself must be logged (loud, explicit, never silent).
	data, readErr := os.ReadFile(filepath.Join(dir, ".registry", "audit.jsonl"))
	require.NoError(t, readErr)
	assert.Contains(t, string(data), `"event":"stale_bundle_override"`)
}

// TestInstallFromBundle_StaleBundle_DisabledByOperatorPolicy proves the
// operator policy check wins outright even when the flag and env var are
// both set — mirroring --allow-unsigned's own "operator policy checked
// first" behavior.
func TestInstallFromBundle_StaleBundle_DisabledByOperatorPolicy(t *testing.T) {
	dir := t.TempDir()
	f := newAuditTrustCoreFixture(t)
	payload := index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: 1, Timestamp: time.Now().Add(-30 * 24 * time.Hour)},
		Connectors: []index.Connector{{
			Name:      "postgres",
			Publisher: index.Publisher{ExpectedOIDCIssuer: "x"},
			Versions:  []index.ConnectorVersion{{Version: "0.14.1"}},
		}},
	}
	snapshot := realBundleIndexSnapshot(t, f, payload)
	manifest := registry.BundleManifest{BundleFormatVersion: 1, Name: "postgres", Version: "0.14.1", OS: "linux", Arch: "amd64"}
	path := buildTestBundleTar(t, manifest, []byte("bytes"), nil, nil, snapshot)

	verifier := &registry.TrustedVerifier{Anchors: f.anchors(t), StatePath: filepath.Join(dir, ".registry", "index-state.json")}
	_, err := registry.InstallFromBundle(context.Background(), registry.InstallBundleOptions{
		BundlePath: path, ConnectorsPath: dir, Verifier: verifier,
		AllowStaleBundle: true, OperatorAllowStaleBundle: false, EnvVarSet: true,
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeBundleStale, ce.Code)
}

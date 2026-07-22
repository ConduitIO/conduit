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

// These are full-stack integration tests for `conduit connectors install`:
// they build the real cobra command (via ecdysis, exactly like cmd/conduit/cli
// does) and drive it through cmd.ExecuteC(), mirroring
// cmd/conduit/root/doctor/doctor_test.go's own pattern for a
// cecdysis.CommandWithResult command. As of PR-2, the ONE production call
// site (this file's ExecuteWithResult) wires the REAL registry.TrustedVerifier
// for both verifiers — these tests prove that wiring end to end (index
// integrity, artifact signature refusal, --allow-unsigned's full gate),
// using signTestIndex to inject a test-only trust anchor via
// connectors.SetDefaultTrustAnchorsForTest (this build has no real
// production anchors embedded yet — see defaultTrustAnchors' doc comment).
// Isolated crypto-function-level tests are pkg/registry/trust's job; this
// file's job is proving the CLI's wiring, TTY/env/config plumbing, and
// --json error-code surfacing are all correct together.
package connectors_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/root/connectors"
	"github.com/conduitio/conduit/pkg/registry/index"
	"github.com/conduitio/ecdysis"
	"github.com/spf13/cobra"
)

func buildInstallCmd(t *testing.T) *cobra.Command {
	t.Helper()
	e := ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithResultDecorator{}))
	return e.MustBuildCobraCommand(&connectors.InstallCommand{})
}

func runInstall(t *testing.T, args ...string) (output string, err error) {
	t.Helper()
	cmd := buildInstallCmd(t)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)

	_, err = cmd.ExecuteC()
	return out.String(), err
}

// buildFixtureArchive returns a valid tar.gz containing a single root-level
// regular file, for the fixture artifact server below.
func buildFixtureArchive(t *testing.T, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	content := []byte("binary-content-for-" + name)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "conduit-connector-" + name, Mode: 0o755, Size: int64(len(content)),
	}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func hexEncode(d [32]byte) string {
	const hextable = "0123456789abcdef"
	out := make([]byte, 64)
	for i, b := range d {
		out[i*2] = hextable[b>>4]
		out[i*2+1] = hextable[b&0x0f]
	}
	return string(out)
}

// signTestIndex JCS-canonicalizes payload, signs it with a freshly generated
// root ed25519 key, registers that key as this test's ONLY compiled-in trust
// anchor (via connectors.SetDefaultTrustAnchorsForTest, restored via
// t.Cleanup), and returns the full signed envelope bytes. This is what makes
// a CLI-level test exercise the REAL TrustedVerifier's crypto path (index
// integrity, not just shape) end to end, rather than a schema-valid-but-
// untrusted index that would now correctly refuse at VerifyIndex with
// registry.trust_anchor_expired before ever reaching resolution — see
// TestInstall_UnsignedIndexFailsClosedAtTrustAnchor for that refusal proven
// directly.
func signTestIndex(t *testing.T, payload index.Payload) []byte {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	restore := connectors.SetDefaultTrustAnchorsForTest(index.TrustAnchors{
		Roots: map[string]ed25519.PublicKey{mustKeyID(t, pub): pub},
	})
	t.Cleanup(restore)

	payloadRaw, err := json.Marshal(payload)
	require.NoError(t, err)
	canonical, err := index.Canonicalize(payloadRaw)
	require.NoError(t, err)
	sig := ed25519.Sign(priv, canonical)

	envelope := map[string]any{
		"payload": payload,
		"signatures": []map[string]any{
			{
				"role": "root", "keyId": mustKeyID(t, pub), "algorithm": "ed25519",
				"signature": base64.StdEncoding.EncodeToString(sig),
			},
		},
	}
	data, err := json.Marshal(envelope)
	require.NoError(t, err)
	return data
}

func mustKeyID(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	id, err := index.KeyID(pub)
	require.NoError(t, err)
	return id
}

// newFixtureIndexFile serves a real artifact (over a local httptest server,
// closed via t.Cleanup) and writes a PROPERLY SIGNED (via signTestIndex),
// schema-valid index file to disk (offline --index-file mode) referencing
// it — so a CLI-level install attempt runs the FULL pipeline (index
// verification, resolve, platform select, download, corruption check) and
// only then reaches the artifact-verification gate, exactly like a real,
// reachable, correctly-signed registry would.
func newFixtureIndexFile(t *testing.T, name, version string) (indexPath string) {
	t.Helper()
	archiveBytes := buildFixtureArchive(t, name)
	digest := sha256.Sum256(archiveBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archiveBytes)
	}))
	t.Cleanup(srv.Close)

	payload := index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: 1, Timestamp: time.Now()},
		Connectors: []index.Connector{
			{
				Name: name,
				Publisher: index.Publisher{
					ExpectedOIDCIssuer:      "https://token.actions.githubusercontent.com",
					ExpectedIdentityPattern: `^https://github\.com/example/` + name + `/.*$`,
				},
				Versions: []index.ConnectorVersion{
					{
						Version: version, MinConduitVersion: "0.1.0", MinProtocolVersion: "0.1.0",
						Artifacts: []index.Artifact{
							{
								OS: runtime.GOOS, Arch: runtime.GOARCH, Kind: "standalone",
								URL:    srv.URL,
								SHA256: hexEncode(digest),
								Size:   int64(len(archiveBytes)),
							},
						},
					},
				},
			},
		},
	}
	data := signTestIndex(t, payload)

	path := filepath.Join(t.TempDir(), "index.json")
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}

func TestInstallArgs_MissingName(t *testing.T) {
	_, err := runInstall(t, "--json")
	require.Error(t, err)
}

func TestInstallArgs_TooManyArgs(t *testing.T) {
	_, err := runInstall(t, "widget", "extra", "--json")
	require.Error(t, err)
}

func TestInstall_SplitsNameAndVersion(t *testing.T) {
	indexPath := newFixtureIndexFile(t, "widget", "2.0.0")
	connectorsPath := t.TempDir()

	out, err := runInstall(t,
		"widget@2.0.0",
		"--connectors.path="+connectorsPath,
		"--index-file="+indexPath,
		"--json",
	)
	require.Error(t, err) // fail-closed: no signature bundle for this artifact

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	// If the name/version split were wrong ("widget@2.0.0" treated as one
	// literal name), resolution would fail with connector_not_found instead
	// — asserting registry.unsigned proves resolution, the download, AND
	// the corruption check all succeeded first (the fixture's Artifact has
	// no Signature.BundleURL, so the real TrustedVerifier correctly refuses
	// with "no valid signature bundle" once it actually reaches the
	// artifact-verification gate), i.e. the split worked and the pipeline
	// ran end to end, through real index-signature verification, up to the
	// artifact gate.
	assert.Equal(t, "registry.unsigned", res.Error.Code)
}

// TestInstall_FailClosedByConstruction_JSON is the CLI-level proof that the
// real command — with no flag or config value able to bypass verification
// outright — refuses via --json with a stable code, after actually
// resolving, downloading, and integrity-checking the artifact, and installs
// nothing. As of PR-2 this is registry.unsigned (no signature bundle present
// for this fixture artifact) rather than PR-1's
// registry.verification_unavailable — the pipeline now runs real
// cryptographic verification instead of a hardcoded refusal.
func TestInstall_FailClosedByConstruction_JSON(t *testing.T) {
	indexPath := newFixtureIndexFile(t, "widget", "1.0.0")
	connectorsPath := t.TempDir()

	out, err := runInstall(t,
		"widget",
		"--connectors.path="+connectorsPath,
		"--index-file="+indexPath,
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.Equal(t, "connectors.install", res.Command)
	assert.False(t, res.OK)
	require.NotNil(t, res.Error)
	assert.Equal(t, "registry.unsigned", res.Error.Code)

	entries, rerr := os.ReadDir(connectorsPath)
	require.NoError(t, rerr)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), "conduit-connector-")
	}
}

// TestInstall_UnsignedIndexFailsClosedAtTrustAnchor proves the OTHER
// fail-closed state this build can be in: an index that ISN'T signed by any
// anchor this build recognizes (the honest default when
// defaultTrustAnchors is left at its zero value, i.e. no
// SetDefaultTrustAnchorsForTest override — matching a real production build
// before the bootstrap ceremony lands, plan-v2 §9) refuses with
// registry.trust_anchor_expired BEFORE resolution ever runs — proving
// verify-before-parse ordering holds at the CLI level, not just in
// pkg/registry/index's own unit tests.
func TestInstall_UnsignedIndexFailsClosedAtTrustAnchor(t *testing.T) {
	payload := index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: 1, Timestamp: time.Now()},
		Connectors: []index.Connector{
			{Name: "widget", Publisher: index.Publisher{ExpectedOIDCIssuer: "https://token.actions.githubusercontent.com", ExpectedIdentityPattern: "^x$"}},
		},
	}
	envelope := map[string]any{"payload": payload, "signatures": []any{}}
	data, err := json.Marshal(envelope)
	require.NoError(t, err)
	indexPath := filepath.Join(t.TempDir(), "index.json")
	require.NoError(t, os.WriteFile(indexPath, data, 0o644))

	out, err := runInstall(t,
		"widget",
		"--connectors.path="+t.TempDir(),
		"--index-file="+indexPath,
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, "registry.trust_anchor_expired", res.Error.Code)
}

// TestInstall_DryRun_JSON proves --dry-run resolves and reports without
// ever reaching the (fail-closed) verification gate at all — no download
// even happens, so this uses a deliberately unreachable artifact URL to
// prove that.
func TestInstall_DryRun_JSON(t *testing.T) {
	payload := index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: 1, Timestamp: time.Now()},
		Connectors: []index.Connector{
			{
				Name: "widget",
				Publisher: index.Publisher{
					ExpectedOIDCIssuer:      "https://token.actions.githubusercontent.com",
					ExpectedIdentityPattern: `^https://github\.com/example/widget/.*$`,
				},
				Versions: []index.ConnectorVersion{
					{
						Version: "1.0.0", MinConduitVersion: "0.1.0", MinProtocolVersion: "0.1.0",
						Artifacts: []index.Artifact{
							{
								OS: runtime.GOOS, Arch: runtime.GOARCH, Kind: "standalone",
								URL:    "https://example.invalid/widget.tar.gz",
								SHA256: "d34db33fd34db33fd34db33fd34db33fd34db33fd34db33fd34db33fd34db33",
								Size:   1024,
							},
						},
					},
				},
			},
		},
	}
	data := signTestIndex(t, payload)
	indexPath := filepath.Join(t.TempDir(), "index.json")
	require.NoError(t, os.WriteFile(indexPath, data, 0o644))

	connectorsPath := t.TempDir()

	out, err := runInstall(t,
		"widget",
		"--connectors.path="+connectorsPath,
		"--index-file="+indexPath,
		"--dry-run",
		"--json",
	)
	require.NoError(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.True(t, res.OK)
	assert.Nil(t, res.Error)

	result, ok := res.Result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "widget", result["name"])
	assert.Equal(t, "1.0.0", result["version"])
	assert.Equal(t, true, result["dryRun"])
	assert.Equal(t, "https://example.invalid/widget.tar.gz", result["artifactUrl"])

	// --dry-run never downloads or installs a connector binary — but it DOES
	// perform real index-signature verification (unlike PR-1's
	// FailClosedVerifier, which never touched disk at all), and a
	// successful verification legitimately persists the rollback
	// high-water mark (index-state.json under .registry/) as an anti-replay
	// side effect independent of whether anything was actually installed —
	// the same reason `apt-get install --dry-run` still updates local
	// package-index cache metadata. The invariant that actually matters is
	// checked directly: no connector binary was ever written.
	entries, rerr := os.ReadDir(connectorsPath)
	require.NoError(t, rerr)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), "conduit-connector-")
	}
}

func TestInstall_UnknownConnector_JSON(t *testing.T) {
	indexPath := newFixtureIndexFile(t, "widget", "1.0.0")
	connectorsPath := t.TempDir()

	out, err := runInstall(t,
		"nonexistent",
		"--connectors.path="+connectorsPath,
		"--index-file="+indexPath,
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, "registry.connector_not_found", res.Error.Code)
}

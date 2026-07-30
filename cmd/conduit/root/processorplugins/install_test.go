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

// Full-stack integration tests for `conduit processor-plugins install`: they
// build the real cobra command (via ecdysis, exactly as cmd/conduit/cli does)
// and drive it through cmd.ExecuteC(), mirroring
// cmd/conduit/root/connectors/install_test.go. The command wires the REAL
// registry.TrustedVerifier for both verifiers; signTestProcessorIndex injects a
// test-only trust anchor via processorplugins.SetDefaultTrustAnchorsForTest so
// index-signature verification is exercised end to end.
//
// WASM-validation-dependent success paths (which need a real wasip1 artifact)
// live in pkg/registry/installprocessor_test.go; this file's job is the CLI's
// wiring, TTY/env/config plumbing, --json error-code surfacing, and the
// offline (--index-file) / gated --allow-unsigned flows — none of which need a
// real WASM module to reach the behavior under test.
package processorplugins_test

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
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/root/processorplugins"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/index"
	"github.com/conduitio/ecdysis"
	"github.com/spf13/cobra"
)

func buildInstallCmd(t *testing.T) *cobra.Command {
	t.Helper()
	e := ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithResultDecorator{}))
	return e.MustBuildCobraCommand(&processorplugins.InstallCommand{})
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

func hexEncode(d [32]byte) string {
	const hextable = "0123456789abcdef"
	out := make([]byte, 64)
	for i, b := range d {
		out[i*2] = hextable[b>>4]
		out[i*2+1] = hextable[b&0x0f]
	}
	return string(out)
}

// buildProcessorArchive returns a valid tar.gz containing a single root-level
// regular file. Its bytes are NOT a real WASM module: every test in this file
// asserts a behavior reached before (fail-closed, not-found, dry-run,
// trust-anchor) or at (the --allow-unsigned WASM-validation refusal) install-
// time validation, so a real wasip1 module is never needed here.
func buildProcessorArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	content := []byte("not-a-real-wasm-module")
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "processor.wasm", Mode: 0o644, Size: int64(len(content))}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func mustKeyID(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	id, err := index.KeyID(pub)
	require.NoError(t, err)
	return id
}

// signTestProcessorIndex JCS-canonicalizes payload, signs it with a freshly
// generated root ed25519 key, registers that key as this test's ONLY
// compiled-in trust anchor (via processorplugins.SetDefaultTrustAnchorsForTest,
// restored via t.Cleanup), and returns the full signed envelope bytes — so a
// CLI-level test exercises the REAL TrustedVerifier's index-integrity path end
// to end. Mirrors connectors_test.signTestIndex.
func signTestProcessorIndex(t *testing.T, payload index.Payload) []byte {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	restore := processorplugins.SetDefaultTrustAnchorsForTest(index.TrustAnchors{
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

// oneProcessorPayload builds a signed-shape payload carrying exactly one
// processor at one version. artifactURL/digest/size point at a served artifact;
// signature bundle URL is included so resolution/selection succeed (the trust
// gate then refuses because the bundle content is not a real signature).
func oneProcessorPayload(name, version, artifactURL, sha256hex string, size int64, sigBundleURL string) index.Payload {
	return index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: 7, Timestamp: time.Now()},
		Processors: []index.Processor{
			{
				Name: name,
				Publisher: index.Publisher{
					ExpectedOIDCIssuer:      "https://token.actions.githubusercontent.com",
					ExpectedIdentityPattern: `^https://github\.com/conduitio/conduit-processor-ai/.*$`,
				},
				Versions: []index.ProcessorVersion{
					{
						Version: version, MinConduitVersion: "0.1.0", MinProtocolVersion: "0.1.0",
						Artifact: index.Artifact{
							OS: "wasip1", Arch: "wasm", Kind: registry.WASMProcessorArtifactKind,
							URL: artifactURL, SHA256: sha256hex, Size: size,
							Signature: index.SignatureRef{BundleURL: sigBundleURL},
						},
						SLSAProvenance: &index.ProvenanceRef{BundleURL: sigBundleURL, PredicateType: "https://slsa.dev/provenance/v1"},
					},
				},
			},
		},
	}
}

// newProcessorFixtureIndexFile serves a real (non-WASM) artifact over httptest
// and writes a PROPERLY SIGNED, schema-valid index file to disk referencing it
// (offline --index-file mode) — so a CLI install runs the FULL pipeline (index
// verification, resolve, arch-neutral select, download, corruption check) and
// only then reaches the artifact-verification gate.
func newProcessorFixtureIndexFile(t *testing.T, name, version string) (indexPath string) {
	t.Helper()
	archiveBytes := buildProcessorArchive(t)
	digest := sha256.Sum256(archiveBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/p.tar.gz":
			_, _ = w.Write(archiveBytes)
		default:
			_, _ = w.Write([]byte(`{"sig":"fake-test-bundle"}`))
		}
	}))
	t.Cleanup(srv.Close)

	payload := oneProcessorPayload(name, version, srv.URL+"/p.tar.gz", hexEncode(digest), int64(len(archiveBytes)), srv.URL+"/sig.json")
	data := signTestProcessorIndex(t, payload)

	path := filepath.Join(t.TempDir(), "index.json")
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}

func TestInstallArgs_MissingName(t *testing.T) {
	_, err := runInstall(t, "--json")
	require.Error(t, err)
}

func TestInstallArgs_TooManyArgs(t *testing.T) {
	_, err := runInstall(t, "ai.embed", "extra", "--json")
	require.Error(t, err)
}

// TestInstall_DryRun_JSON proves --dry-run resolves the processor collection
// and reports without reaching the verification gate — no download happens, so
// the artifact URL is deliberately unreachable.
func TestInstall_DryRun_JSON(t *testing.T) {
	payload := oneProcessorPayload("ai.embed", "1.0.0", "https://example.invalid/p.tar.gz",
		"d34db33fd34db33fd34db33fd34db33fd34db33fd34db33fd34db33fd34db33", 1024, "")
	data := signTestProcessorIndex(t, payload)
	indexPath := filepath.Join(t.TempDir(), "index.json")
	require.NoError(t, os.WriteFile(indexPath, data, 0o644))

	processorsPath := t.TempDir()
	out, err := runInstall(t,
		"ai.embed",
		"--processors.path="+processorsPath,
		"--index-file="+indexPath,
		"--dry-run",
		"--json",
	)
	require.NoError(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.True(t, res.OK)
	assert.Nil(t, res.Error)
	assert.Equal(t, "processor-plugins.install", res.Command)

	result, ok := res.Result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ai.embed", result["name"])
	assert.Equal(t, "1.0.0", result["version"])
	assert.Equal(t, "wasip1", result["os"])
	assert.Equal(t, "wasm", result["arch"])
	assert.Equal(t, true, result["dryRun"])

	// --dry-run never writes a processor artifact.
	assertNoInstalledArtifact(t, processorsPath)
}

// TestInstall_FailClosedByConstruction_JSON: the real command with the real
// TrustedVerifier refuses via --json (no signature bundle content that verifies)
// after resolving, downloading, and integrity-checking, and installs nothing.
func TestInstall_FailClosedByConstruction_JSON(t *testing.T) {
	indexPath := newProcessorFixtureIndexFile(t, "ai.embed", "1.0.0")
	processorsPath := t.TempDir()

	out, err := runInstall(t,
		"ai.embed",
		"--processors.path="+processorsPath,
		"--index-file="+indexPath,
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.Equal(t, "processor-plugins.install", res.Command)
	assert.False(t, res.OK)
	require.NotNil(t, res.Error)
	// registry.unsigned: got all the way to the artifact-verification gate
	// (resolution, the arch-neutral select, download, and corruption all
	// succeeded first), which refuses because the fixture bundle isn't a real
	// signature.
	assert.Equal(t, "registry.unsigned", res.Error.Code)

	assertNoInstalledArtifact(t, processorsPath)
}

// TestInstall_SplitsNameAndVersion proves "ai.embed@1.0.0" is split on the
// first @ (a wrong split would resolve as processor_not_found instead of
// reaching the artifact gate with registry.unsigned).
func TestInstall_SplitsNameAndVersion(t *testing.T) {
	indexPath := newProcessorFixtureIndexFile(t, "ai.embed", "1.0.0")
	processorsPath := t.TempDir()

	out, err := runInstall(t,
		"ai.embed@1.0.0",
		"--processors.path="+processorsPath,
		"--index-file="+indexPath,
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, "registry.unsigned", res.Error.Code)
}

// TestInstall_NotFound_WithInterimSuggestion is edge case 1 / item 5: an
// install for a name absent from the index's processors[] returns
// registry.processor_not_found, and the suggestion points at the interim
// offline (--index-file/--bundle) path.
func TestInstall_NotFound_WithInterimSuggestion(t *testing.T) {
	indexPath := newProcessorFixtureIndexFile(t, "ai.embed", "1.0.0")
	processorsPath := t.TempDir()

	out, err := runInstall(t,
		"ai.chunk",
		"--processors.path="+processorsPath,
		"--index-file="+indexPath,
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, "registry.processor_not_found", res.Error.Code)
	assert.Contains(t, res.Error.Suggestion, "--index-file")
}

// TestInstall_UnsignedIndexFailsClosedAtTrustAnchor: an index not signed by any
// anchor this build recognizes (no SetDefaultTrustAnchorsForTest override)
// refuses with registry.trust_anchor_expired BEFORE resolution.
func TestInstall_UnsignedIndexFailsClosedAtTrustAnchor(t *testing.T) {
	payload := oneProcessorPayload("ai.embed", "1.0.0", "https://example.invalid/p.tar.gz", "00", 1, "")
	envelope := map[string]any{"payload": payload, "signatures": []any{}}
	data, err := json.Marshal(envelope)
	require.NoError(t, err)
	indexPath := filepath.Join(t.TempDir(), "index.json")
	require.NoError(t, os.WriteFile(indexPath, data, 0o644))

	out, err := runInstall(t,
		"ai.embed",
		"--processors.path="+t.TempDir(),
		"--index-file="+indexPath,
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, "registry.trust_anchor_expired", res.Error.Code)
}

// TestInstall_BrokenAnchorEmbed_ReportsTrustAnchorsUnavailable: on a build
// whose embedded anchors failed to load, install refuses up front with
// registry.trust_anchors_unavailable ("reinstall conduit").
func TestInstall_BrokenAnchorEmbed_ReportsTrustAnchorsUnavailable(t *testing.T) {
	restore := processorplugins.SetAnchorLoadErrForTest(cerrors.New("simulated stripped/corrupt anchor embed"))
	t.Cleanup(restore)

	out, err := runInstall(t,
		"ai.embed",
		"--processors.path="+t.TempDir(),
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, "registry.trust_anchors_unavailable", res.Error.Code)
}

// TestInstall_AllowUnsigned_OperatorDisabledByDefault_Refuses: with a fresh
// config, install.allow-unsigned is false, so --allow-unsigned refuses with the
// operator-disabled code even with the non-interactive env var set — the gate
// fires and refuses before any download/extract.
func TestInstall_AllowUnsigned_OperatorDisabledByDefault_Refuses(t *testing.T) {
	indexPath := newProcessorFixtureIndexFile(t, "ai.embed", "1.0.0")
	processorsPath := t.TempDir()

	t.Setenv(processorplugins.UnsignedInstallEnvVarForTest, "I_UNDERSTAND")

	out, err := runInstall(t,
		"ai.embed",
		"--processors.path="+processorsPath,
		"--index-file="+indexPath,
		"--allow-unsigned",
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, "registry.unsigned_install_disabled_by_policy", res.Error.Code)

	assertNoInstalledArtifact(t, processorsPath)
}

// TestInstall_AllowUnsigned_GateFiresAndLogsUnderProcessorsPath proves the
// SAME policy.Decide gate the connector path uses fires for processors (env-var
// path, operator-enabled, non-interactive → allowed), and — the regression
// guard for the unsigned-install-log-path fix — that its mandatory durable
// audit entry lands under --processors.path/.registry, NOT a stray relative
// path. The install then fails at install-time WASM validation
// (registry.invalid_processor_artifact) because the fixture artifact is not a
// real module — which itself proves the gate was PASSED (validation runs after
// the unsigned gate).
func TestInstall_AllowUnsigned_GateFiresAndLogsUnderProcessorsPath(t *testing.T) {
	indexPath := newProcessorFixtureIndexFile(t, "ai.embed", "1.0.0")
	processorsPath := t.TempDir()

	t.Setenv(processorplugins.UnsignedInstallEnvVarForTest, "I_UNDERSTAND")

	out, err := runInstall(t,
		"ai.embed",
		"--processors.path="+processorsPath,
		"--index-file="+indexPath,
		"--allow-unsigned",
		"--install.allow-unsigned", // operator policy: permit the gate at all
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, "registry.invalid_processor_artifact", res.Error.Code)

	// The mandatory unsigned-install audit entry must be under --processors.path.
	logData, readErr := os.ReadFile(filepath.Join(processorsPath, ".registry", "unsigned-installs.log"))
	require.NoError(t, readErr, "unsigned-install log must be written under --processors.path/.registry")
	assert.Contains(t, string(logData), "ai.embed")

	// No artifact landed (WASM validation refused before the rename).
	assertNoInstalledArtifact(t, processorsPath)
}

func assertNoInstalledArtifact(t *testing.T, processorsPath string) {
	t.Helper()
	entries, err := os.ReadDir(processorsPath)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), "conduit-processor-")
	}
}

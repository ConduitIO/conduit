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
// cecdysis.CommandWithResult command. The point of these tests is to prove
// the ONE production call site (this file's ExecuteWithResult) actually
// wires registry.FailClosedVerifier for both verifiers, and that --json
// carries the resulting registry.verification_unavailable code end to end
// — not just that pkg/registry.Install itself behaves (that is
// pkg/registry/install_test.go's job).
package connectors_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
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

// newFixtureIndexFile serves a real artifact (over a local httptest server,
// closed via t.Cleanup) and writes a schema-valid, unsigned index file to
// disk (offline --index-file mode) referencing it — so a CLI-level install
// attempt runs the FULL pipeline (resolve, platform select, download,
// corruption check) and only then reaches the verification gate, exactly
// like a real, reachable registry would.
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
	envelope := map[string]any{"payload": payload, "signatures": []any{}}
	data, err := json.Marshal(envelope)
	require.NoError(t, err)

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
	require.Error(t, err) // fail-closed: FailClosedVerifier always refuses

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	// If the name/version split were wrong ("widget@2.0.0" treated as one
	// literal name), resolution would fail with connector_not_found
	// instead — asserting verification_unavailable proves resolution
	// (and download/corruption-check) succeeded first, i.e. the split
	// worked and the pipeline actually ran end to end up to the gate.
	assert.Equal(t, "registry.verification_unavailable", res.Error.Code)
}

// TestInstall_FailClosedByConstruction_JSON is the CLI-level version of
// pkg/registry/install_test.go's AC-6 test: the real command, with no flag
// or config value able to change its verifier wiring, must refuse via
// --json with the stable registry.verification_unavailable code, after
// actually resolving, downloading, and integrity-checking the artifact —
// and must install nothing.
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
	assert.Equal(t, "registry.verification_unavailable", res.Error.Code)

	entries, rerr := os.ReadDir(connectorsPath)
	require.NoError(t, rerr)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), "conduit-connector-")
	}
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
	envelope := map[string]any{"payload": payload, "signatures": []any{}}
	data, err := json.Marshal(envelope)
	require.NoError(t, err)
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

	entries, rerr := os.ReadDir(connectorsPath)
	require.NoError(t, rerr)
	assert.Empty(t, entries)
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

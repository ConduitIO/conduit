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
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
)

// installFixture writes a manifest entry (and, unless noArtifact, the
// artifact file it references) directly to disk — a lightweight substitute
// for a full registry.Install run, since uninstall_test.go only needs a
// pre-existing installed state, not the install pipeline itself (that's
// install_test.go's job).
func installFixture(t *testing.T, connectorsPath, name, version, artifactBytes string) registry.ManifestEntry {
	t.Helper()
	require.NoError(t, os.MkdirAll(connectorsPath, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(connectorsPath, ".registry"), 0o755))

	artifactFile := name + "_" + version
	if artifactBytes != "" {
		require.NoError(t, os.WriteFile(filepath.Join(connectorsPath, artifactFile), []byte(artifactBytes), 0o755))
	}

	digest := sha256Hex(artifactBytes)
	entry := registry.ManifestEntry{
		Name: name, Version: version, Kind: "standalone", OS: "linux", Arch: "amd64",
		ArtifactFile: artifactFile, Digest: "sha256:" + digest, Size: int64(len(artifactBytes)),
		InstalledAt: time.Now().UTC(), InstalledBy: "test", Source: registry.InstallSourceIndex,
		Signed: true, VerifiedIdentity: "test-identity",
	}

	key, err := registry.ManifestKey(name, version)
	require.NoError(t, err)

	path := filepath.Join(connectorsPath, ".registry", "manifest.json")
	m, err := registry.LoadManifest(path)
	require.NoError(t, err)
	if m.Installs == nil {
		m.Installs = map[string]registry.ManifestEntry{}
	}
	m.Installs[key] = entry
	require.NoError(t, registry.SaveManifest(path, m))

	return entry
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestUninstall_NotInstalled_HardError(t *testing.T) {
	dir := t.TempDir()
	_, err := registry.Uninstall(registry.UninstallOptions{Name: "postgres", ConnectorsPath: dir})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeConnectorNotInstalled, ce.Code)
}

func TestUninstall_RemovesArtifactAndManifestEntry(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, "postgres", "0.14.1", "fake-binary-bytes")

	res, err := registry.Uninstall(registry.UninstallOptions{Name: "postgres", Version: "0.14.1", ConnectorsPath: dir})
	require.NoError(t, err)
	assert.Equal(t, "postgres", res.Name)
	assert.Equal(t, "0.14.1", res.Version)
	assert.False(t, res.Forced)
	assert.Empty(t, res.Warnings)

	_, statErr := os.Stat(filepath.Join(dir, "postgres_0.14.1"))
	assert.True(t, os.IsNotExist(statErr))

	m, err := registry.LoadManifest(filepath.Join(dir, ".registry", "manifest.json"))
	require.NoError(t, err)
	assert.Empty(t, m.Installs)
}

func TestUninstall_AutoResolvesSingleInstalledVersion(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, "postgres", "0.14.1", "bytes")

	res, err := registry.Uninstall(registry.UninstallOptions{Name: "postgres", ConnectorsPath: dir})
	require.NoError(t, err)
	assert.Equal(t, "0.14.1", res.Version)
}

func TestUninstall_AmbiguousWithoutVersion_Refuses(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, "postgres", "0.13.0", "bytes-a")
	installFixture(t, dir, "postgres", "0.14.1", "bytes-b")

	_, err := registry.Uninstall(registry.UninstallOptions{Name: "postgres", ConnectorsPath: dir})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeAmbiguousUninstall, ce.Code)

	// Both versions must remain untouched.
	m, err := registry.LoadManifest(filepath.Join(dir, ".registry", "manifest.json"))
	require.NoError(t, err)
	assert.Len(t, m.Installs, 2)
}

func TestUninstall_AmbiguousResolvedByExplicitVersion(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, "postgres", "0.13.0", "bytes-a")
	installFixture(t, dir, "postgres", "0.14.1", "bytes-b")

	res, err := registry.Uninstall(registry.UninstallOptions{Name: "postgres", Version: "0.14.1", ConnectorsPath: dir})
	require.NoError(t, err)
	assert.Equal(t, "0.14.1", res.Version)

	m, err := registry.LoadManifest(filepath.Join(dir, ".registry", "manifest.json"))
	require.NoError(t, err)
	assert.Len(t, m.Installs, 1)
	_, stillThere := m.Installs["postgres@0.13.0"]
	assert.True(t, stillThere)
}

func TestUninstall_InUse_RefusesWithoutForce(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, "postgres", "0.14.1", "bytes")

	_, err := registry.Uninstall(registry.UninstallOptions{
		Name: "postgres", Version: "0.14.1", ConnectorsPath: dir,
		InUseRefs: []registry.InUseRef{{PipelineID: "pipe-1", ConnectorID: "conn-1"}},
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeConnectorInUse, ce.Code)
	assert.Contains(t, ce.Message, "pipe-1")

	// Nothing removed.
	m, err := registry.LoadManifest(filepath.Join(dir, ".registry", "manifest.json"))
	require.NoError(t, err)
	assert.Len(t, m.Installs, 1)
}

func TestUninstall_InUse_ForceProceedsWithWarning(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, "postgres", "0.14.1", "bytes")

	res, err := registry.Uninstall(registry.UninstallOptions{
		Name: "postgres", Version: "0.14.1", ConnectorsPath: dir, Force: true,
		InUseRefs: []registry.InUseRef{{PipelineID: "pipe-1", ConnectorID: "conn-1"}},
	})
	require.NoError(t, err)
	assert.True(t, res.Forced)
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "pipe-1")

	m, err := registry.LoadManifest(filepath.Join(dir, ".registry", "manifest.json"))
	require.NoError(t, err)
	assert.Empty(t, m.Installs)
}

// TestUninstall_MissingArtifact_ToleratesAndCleansManifest is AC10: an
// artifact manually deleted with `rm` before uninstall runs must not be a
// failure — uninstall still cleans up the manifest entry.
func TestUninstall_MissingArtifact_ToleratesAndCleansManifest(t *testing.T) {
	dir := t.TempDir()
	entry := installFixture(t, dir, "postgres", "0.14.1", "bytes")
	require.NoError(t, os.Remove(filepath.Join(dir, entry.ArtifactFile)))

	res, err := registry.Uninstall(registry.UninstallOptions{Name: "postgres", Version: "0.14.1", ConnectorsPath: dir})
	require.NoError(t, err)
	assert.True(t, res.ArtifactAlreadyMissing)
	assert.False(t, res.DriftDetected)

	m, err := registry.LoadManifest(filepath.Join(dir, ".registry", "manifest.json"))
	require.NoError(t, err)
	assert.Empty(t, m.Installs)
}

// TestUninstall_DriftedArtifact_StillRemovedAndNoted is AC11: an artifact
// manually replaced with different bytes is still removed, with
// DriftDetected reported so an operator knows what they just deleted wasn't
// necessarily what install originally placed.
func TestUninstall_DriftedArtifact_StillRemovedAndNoted(t *testing.T) {
	dir := t.TempDir()
	entry := installFixture(t, dir, "postgres", "0.14.1", "original-bytes")
	require.NoError(t, os.WriteFile(filepath.Join(dir, entry.ArtifactFile), []byte("replaced-bytes"), 0o755))

	res, err := registry.Uninstall(registry.UninstallOptions{Name: "postgres", Version: "0.14.1", ConnectorsPath: dir})
	require.NoError(t, err)
	assert.True(t, res.DriftDetected)
	assert.False(t, res.ArtifactAlreadyMissing)

	_, statErr := os.Stat(filepath.Join(dir, entry.ArtifactFile))
	assert.True(t, os.IsNotExist(statErr))
}

func TestUninstall_AppendsAuditEvent(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, "postgres", "0.14.1", "bytes")

	_, err := registry.Uninstall(registry.UninstallOptions{
		Name: "postgres", Version: "0.14.1", ConnectorsPath: dir, InstalledBy: "devaris",
	})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, ".registry", "audit.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(data), `"event":"connector_uninstall"`)
	assert.Contains(t, string(data), `"connector":"postgres"`)
	assert.Contains(t, string(data), `"operator":"devaris"`)
}

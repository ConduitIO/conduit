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
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/registry"
)

func writeRawIndex(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestListInstalled_BasicRowsAndUpdateAvailable(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, "postgres", "0.14.0", "bytes")
	installFixture(t, dir, "s3-sink", "1.0.0", "bytes-2")

	indexBody := `{"payload":{"schemaVersion":1,"index":{"version":1,"timestamp":"2026-01-01T00:00:00Z"},"connectors":[
		{"name":"postgres","publisher":{"expectedOIDCIssuer":"x"},"versions":[{"version":"0.14.0"},{"version":"0.14.1"}]},
		{"name":"s3-sink","publisher":{"expectedOIDCIssuer":"x"},"versions":[{"version":"1.0.0"}]}
	]},"signatures":[]}`

	res, err := registry.ListInstalled(context.Background(), registry.ListInstalledOptions{
		ConnectorsPath: dir, IndexFile: writeRawIndex(t, indexBody),
	})
	require.NoError(t, err)
	assert.False(t, res.IndexUnreachable)
	require.Len(t, res.Installed, 2)

	byName := map[string]registry.InstalledConnector{}
	for _, r := range res.Installed {
		byName[r.Name] = r
	}

	pg := byName["postgres"]
	assert.Equal(t, "0.14.0", pg.Version)
	assert.Equal(t, "0.14.1", pg.LatestAvailable)
	assert.Equal(t, registry.InstalledStatusUpdateAvailable, pg.Status)

	s3 := byName["s3-sink"]
	assert.Equal(t, "1.0.0", s3.Version)
	assert.Equal(t, "1.0.0", s3.LatestAvailable)
	assert.Equal(t, registry.InstalledStatusOK, s3.Status)
}

// TestListInstalled_IndexUnreachable_DegradesGracefully is AC5: rows still
// render (with indexUnreachable: true) rather than failing the whole
// command.
func TestListInstalled_IndexUnreachable_DegradesGracefully(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, "postgres", "0.14.0", "bytes")

	res, err := registry.ListInstalled(context.Background(), registry.ListInstalledOptions{
		ConnectorsPath: dir, IndexFile: filepath.Join(dir, "does-not-exist.json"),
	})
	require.NoError(t, err)
	assert.True(t, res.IndexUnreachable)
	require.Len(t, res.Installed, 1)
	assert.Equal(t, registry.InstalledStatusIndexUnreachable, res.Installed[0].Status)
	assert.Empty(t, res.Installed[0].LatestAvailable)
}

func TestListInstalled_MissingArtifact(t *testing.T) {
	dir := t.TempDir()
	entry := installFixture(t, dir, "postgres", "0.14.0", "bytes")
	require.NoError(t, os.Remove(filepath.Join(dir, entry.ArtifactFile)))

	res, err := registry.ListInstalled(context.Background(), registry.ListInstalledOptions{
		ConnectorsPath: dir, IndexFile: filepath.Join(dir, "does-not-exist.json"),
	})
	require.NoError(t, err)
	require.Len(t, res.Installed, 1)
	assert.Equal(t, registry.InstalledStatusMissingArtifact, res.Installed[0].Status)
}

func TestListInstalled_Drifted(t *testing.T) {
	dir := t.TempDir()
	entry := installFixture(t, dir, "postgres", "0.14.0", "original")
	require.NoError(t, os.WriteFile(filepath.Join(dir, entry.ArtifactFile), []byte("replaced"), 0o755))

	res, err := registry.ListInstalled(context.Background(), registry.ListInstalledOptions{
		ConnectorsPath: dir, IndexFile: filepath.Join(dir, "does-not-exist.json"),
	})
	require.NoError(t, err)
	require.Len(t, res.Installed, 1)
	assert.Equal(t, registry.InstalledStatusDrifted, res.Installed[0].Status)
}

func TestListInstalled_NotInIndex(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, "old-thing", "0.2.0", "bytes")

	indexBody := `{"payload":{"schemaVersion":1,"index":{"version":1,"timestamp":"2026-01-01T00:00:00Z"},"connectors":[]},"signatures":[]}`
	res, err := registry.ListInstalled(context.Background(), registry.ListInstalledOptions{
		ConnectorsPath: dir, IndexFile: writeRawIndex(t, indexBody),
	})
	require.NoError(t, err)
	require.Len(t, res.Installed, 1)
	assert.Equal(t, registry.InstalledStatusNotInIndex, res.Installed[0].Status)
	assert.False(t, res.IndexUnreachable)
}

func TestListInstalled_EmptyManifest(t *testing.T) {
	dir := t.TempDir()
	res, err := registry.ListInstalled(context.Background(), registry.ListInstalledOptions{
		ConnectorsPath: dir, IndexFile: filepath.Join(dir, "does-not-exist.json"),
	})
	require.NoError(t, err)
	assert.Empty(t, res.Installed)
}

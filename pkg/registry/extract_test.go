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
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
)

// tarEntry is one entry to write into a test fixture archive.
type tarEntry struct {
	name     string
	typeflag byte
	linkname string
	content  string
}

func buildArchive(t *testing.T, entries []tarEntry) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: typeflag,
			Mode:     0o755,
			Size:     int64(len(e.content)),
			Linkname: e.linkname,
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if len(e.content) > 0 {
			_, err := tw.Write([]byte(e.content))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	dir := t.TempDir()
	path := filepath.Join(dir, "archive.tar.gz")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
	return path
}

func TestExtractBinary_SingleFileSucceeds(t *testing.T) {
	archivePath := buildArchive(t, []tarEntry{{name: "conduit-connector-postgres", content: "binary-bytes"}})
	destDir := t.TempDir()

	binPath, err := registry.ExtractBinary(archivePath, destDir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(destDir, "conduit-connector-postgres"), binPath)

	data, err := os.ReadFile(binPath)
	require.NoError(t, err)
	assert.Equal(t, "binary-bytes", string(data))
}

func TestExtractBinary_ZeroCandidatesRefuses(t *testing.T) {
	// Only a nested file (not root-level) and a bare directory entry — no
	// root-level regular file exists to be the candidate binary.
	archivePath := buildArchive(t, []tarEntry{
		{name: "docs", typeflag: tar.TypeDir},
		{name: "docs/README.md", content: "hi"},
	})
	_, err := registry.ExtractBinary(archivePath, t.TempDir())
	require.Error(t, err)
	assertArchiveInvalid(t, err)
}

func TestExtractBinary_MultipleCandidatesRefuses(t *testing.T) {
	archivePath := buildArchive(t, []tarEntry{
		{name: "conduit-connector-a", content: "a"},
		{name: "conduit-connector-b", content: "b"},
	})
	_, err := registry.ExtractBinary(archivePath, t.TempDir())
	require.Error(t, err)
	assertArchiveInvalid(t, err)
}

func TestExtractBinary_NestedFileIsNotACandidate(t *testing.T) {
	archivePath := buildArchive(t, []tarEntry{
		{name: "conduit-connector-postgres", content: "binary-bytes"},
		{name: "docs/README.md", content: "hi"},
	})
	binPath, err := registry.ExtractBinary(archivePath, t.TempDir())
	require.NoError(t, err)
	assert.Contains(t, binPath, "conduit-connector-postgres")
}

func TestExtractBinary_PathTraversalRefuses(t *testing.T) {
	archivePath := buildArchive(t, []tarEntry{{name: "../../etc/passwd", content: "evil"}})
	_, err := registry.ExtractBinary(archivePath, t.TempDir())
	require.Error(t, err)
	assertArchiveInvalid(t, err)
}

func TestExtractBinary_AbsolutePathRefuses(t *testing.T) {
	archivePath := buildArchive(t, []tarEntry{{name: "/etc/passwd", content: "evil"}})
	_, err := registry.ExtractBinary(archivePath, t.TempDir())
	require.Error(t, err)
	assertArchiveInvalid(t, err)
}

func TestExtractBinary_SymlinkEntryRefuses(t *testing.T) {
	archivePath := buildArchive(t, []tarEntry{
		{name: "conduit-connector-postgres", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
	})
	_, err := registry.ExtractBinary(archivePath, t.TempDir())
	require.Error(t, err)
	assertArchiveInvalid(t, err)
}

func TestExtractBinary_HardlinkEntryRefuses(t *testing.T) {
	archivePath := buildArchive(t, []tarEntry{
		{name: "conduit-connector-postgres", typeflag: tar.TypeLink, linkname: "somewhere"},
	})
	_, err := registry.ExtractBinary(archivePath, t.TempDir())
	require.Error(t, err)
	assertArchiveInvalid(t, err)
}

func assertArchiveInvalid(t *testing.T, err error) {
	t.Helper()
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeArchiveInvalid, ce.Code)
}

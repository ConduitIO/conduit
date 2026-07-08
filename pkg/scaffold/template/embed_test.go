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

package template_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/conduitio/conduit/pkg/scaffold/template"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtract_RestoresRealFilenames(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, template.Extract(template.KindConnector, dir))

	for _, want := range []string{"go.mod", "connector.go", "cmd/connector/main.go", "tools/go.mod"} {
		assert.FileExists(t, filepath.Join(dir, filepath.FromSlash(want)))
	}
	for _, mustNotExist := range []string{"go.mod.tmpl", "connector.go.tmpl"} {
		_, err := os.Stat(filepath.Join(dir, mustNotExist))
		assert.True(t, os.IsNotExist(err), "%s should not exist after Extract", mustNotExist)
	}
}

func TestExtract_ExecutableBits(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, template.Extract(template.KindConnector, dir))

	info, err := os.Stat(filepath.Join(dir, "scripts", "bump_version.sh"))
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&0o111, "scripts/bump_version.sh should be executable")

	info, err = os.Stat(filepath.Join(dir, "scripts", "common.sh"))
	require.NoError(t, err)
	assert.Zero(t, info.Mode()&0o111, "scripts/common.sh should not be executable")
}

func TestExtract_UnknownKind(t *testing.T) {
	dir := t.TempDir()
	err := template.Extract(template.Kind("bogus"), dir)
	assert.Error(t, err)
}

func TestFS_RootsAtTemplate(t *testing.T) {
	fsys, err := template.FS(template.KindProcessor)
	require.NoError(t, err)

	// FS is rooted at the template root, not prefixed with "processor/": a
	// direct read of "go.mod.tmpl" (the pre-Extract, still-renamed name —
	// see tmplSuffix) must succeed.
	_, err = fs.ReadFile(fsys, "go.mod.tmpl")
	require.NoError(t, err)

	entries, err := fs.ReadDir(fsys, ".")
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.Contains(t, names, "go.mod.tmpl")
	assert.Contains(t, names, "processor.go.tmpl")
}

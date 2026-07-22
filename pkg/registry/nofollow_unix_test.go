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

//go:build unix

// This is an internal (package registry, not registry_test) test file
// specifically because openRegularNoFollow is unexported — it is install.go's
// last line of defense right before the final install rename, and the
// property under test (an actual symlink AT THE EXPECTED PATH is refused,
// not just a symlink tar entry refused earlier by ExtractBinary) has no
// other way to reach it from outside the package.
package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

func TestOpenRegularNoFollow_RegularFileSucceeds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary")
	require.NoError(t, os.WriteFile(path, []byte("content"), 0o755))

	f, err := openRegularNoFollow(path)
	require.NoError(t, err)
	defer f.Close()

	data := make([]byte, len("content"))
	n, err := f.Read(data)
	require.NoError(t, err)
	assert.Equal(t, "content", string(data[:n]))
}

// TestOpenRegularNoFollow_SymlinkRefused is the direct proof of the TOCTOU
// guard install.go's rename call site relies on: if something (e.g. a
// symlink substituted into the staging directory between extraction and
// the final rename) replaces the expected binary path with a symlink,
// openRegularNoFollow must refuse it (ELOOP), not silently follow it to
// whatever it points at.
func TestOpenRegularNoFollow_SymlinkRefused(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target-outside")
	require.NoError(t, os.WriteFile(target, []byte("attacker-controlled"), 0o644))

	link := filepath.Join(dir, "binary")
	require.NoError(t, os.Symlink(target, link))

	_, err := openRegularNoFollow(link)
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, CodeArchiveInvalid, ce.Code)
}

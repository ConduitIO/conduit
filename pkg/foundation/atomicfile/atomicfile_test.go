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

package atomicfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/atomicfile"
	"github.com/matryer/is"
)

// TestWriteFile_ReplacesContentWholesale is the "torn write is impossible"
// half of Invariant 5: the original bytes are intact right up until the
// moment the new bytes are fully in place, and no temp file is left behind.
func TestWriteFile_ReplacesContentWholesale(t *testing.T) {
	is := is.New(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	is.NoErr(os.WriteFile(path, []byte("original"), 0o600))

	is.NoErr(atomicfile.WriteFile(path, []byte("repaired"), 0o600))

	got, err := os.ReadFile(path)
	is.NoErr(err)
	is.Equal(string(got), "repaired")

	// No leftover temp file in the directory.
	entries, err := os.ReadDir(dir)
	is.NoErr(err)
	is.Equal(len(entries), 1)
	is.Equal(entries[0].Name(), "manifest.json")
}

// TestWriteFile_FailureLeavesOriginalIntact simulates a crash mid-write (an
// unwritable target directory for the temp file) and asserts the original
// file is untouched — the other half of Invariant 5.
func TestWriteFile_FailureLeavesOriginalIntact(t *testing.T) {
	is := is.New(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	is.NoErr(os.WriteFile(path, []byte("original"), 0o600))

	// Make the directory read-only so CreateTemp in the same dir fails —
	// simulates a write failure between "compute the new bytes" and "make
	// them durable".
	is.NoErr(os.Chmod(dir, 0o500))
	defer func() { _ = os.Chmod(dir, 0o700) }() // t.TempDir() cleanup needs write back

	err := atomicfile.WriteFile(path, []byte("new content"), 0o600)
	is.True(err != nil)

	is.NoErr(os.Chmod(dir, 0o700))
	got, err := os.ReadFile(path)
	is.NoErr(err)
	is.Equal(string(got), "original")
}

// TestWriteFile_CreatesNewFile covers the "no prior file" case (e.g. a
// manifest written for the first time), not exercised by the repair
// command's original always-exists usage.
func TestWriteFile_CreatesNewFile(t *testing.T) {
	is := is.New(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	is.NoErr(atomicfile.WriteFile(path, []byte(`{"schemaVersion":1}`), 0o644))

	got, err := os.ReadFile(path)
	is.NoErr(err)
	is.Equal(string(got), `{"schemaVersion":1}`)
}

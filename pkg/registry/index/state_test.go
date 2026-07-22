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

package index_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/matryer/is"

	"github.com/conduitio/conduit/pkg/registry/index"
)

func TestLoadState_MissingFileIsZeroValue(t *testing.T) {
	is := is.New(t)
	s, err := index.LoadState(filepath.Join(t.TempDir(), "state.json"))
	is.NoErr(err)
	is.Equal(s.Version, int64(0))
}

func TestSaveAndLoadState_RoundTrip(t *testing.T) {
	is := is.New(t)
	path := filepath.Join(t.TempDir(), "state.json")

	is.NoErr(index.SaveState(path, index.State{Version: 42}))

	got, err := index.LoadState(path)
	is.NoErr(err)
	is.Equal(got.Version, int64(42))
}

// TestSaveState_NeverLeavesATornFile is the P0-2-adjacent chaos property
// (plan-v2 §15.3, "kill-mid-write on state.go"): a save that fails partway
// through (unwritable directory, simulating a crash between "compute the
// new high-water mark" and "make it durable") must leave any prior content
// completely intact, never truncated/partial.
func TestSaveState_NeverLeavesATornFile(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	is.NoErr(index.SaveState(path, index.State{Version: 1}))

	is.NoErr(os.Chmod(dir, 0o500))
	defer func() { _ = os.Chmod(dir, 0o700) }()

	err := index.SaveState(path, index.State{Version: 2})
	is.True(err != nil)

	is.NoErr(os.Chmod(dir, 0o700))
	got, err := index.LoadState(path)
	is.NoErr(err)
	is.Equal(got.Version, int64(1)) // old value intact, never torn/partial
}

func TestLoadState_CorruptFileIsAnError(t *testing.T) {
	is := is.New(t)
	path := filepath.Join(t.TempDir(), "state.json")
	is.NoErr(os.WriteFile(path, []byte("not json"), 0o600))

	_, err := index.LoadState(path)
	is.True(err != nil)
}

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

package atomicfile

import (
	"os"
	"path/filepath"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// WriteFile writes content to path via a temp file in the same directory
// plus rename. A crash between the temp write and the rename leaves the
// original path untouched; a crash after the rename leaves the new content,
// fully written, never a half-written file — Invariant 5 ("state and
// checkpoint writes are atomic") applied to a single whole-file write.
//
// The temp file is created in filepath.Dir(path) specifically so the final
// rename is same-filesystem (and therefore atomic on every platform Conduit
// supports) — a temp directory on a different filesystem/volume would make
// the rename a non-atomic copy instead.
func WriteFile(path string, content []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".atomicfile-*.tmp")
	if err != nil {
		return cerrors.Errorf("could not create a temp file to write %q atomically: %w", path, err)
	}
	tmpPath := tmp.Name()
	// Always attempt to remove the temp file; once Rename succeeds below
	// this is a no-op (the path no longer exists under tmpPath).
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return cerrors.Errorf("could not write to temp file for %q: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return cerrors.Errorf("could not sync temp file for %q: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return cerrors.Errorf("could not close temp file for %q: %w", path, err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return cerrors.Errorf("could not set permissions on temp file for %q: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return cerrors.Errorf("could not atomically replace %q: %w", path, err)
	}
	return nil
}

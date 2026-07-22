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

package registry

import (
	"os"
	"syscall"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// openRegularNoFollow opens path with O_NOFOLLOW so a symlink present at
// this name is refused (ELOOP) rather than silently followed, then confirms
// via fstat on the resulting fd that the resolved file is an ordinary
// regular file. See install.go's call site for exactly what risk this
// closes (the final install rename) and what it does NOT claim to do
// (re-verify the artifact's trust — that already happened, over an
// in-memory digest, before extraction ever ran).
//
// The returned *os.File is open; the caller is responsible for closing it.
func openRegularNoFollow(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, conduiterr.Wrap(CodeArchiveInvalid,
			"could not safely open the extracted binary (possible symlink substitution)", err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, conduiterr.Wrap(CodeArchiveInvalid, "could not stat the extracted binary", err)
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, conduiterr.New(CodeArchiveInvalid, "extracted binary is not a regular file")
	}
	return f, nil
}

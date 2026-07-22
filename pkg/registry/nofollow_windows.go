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

//go:build windows

package registry

import (
	"os"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// openRegularNoFollow is the Windows fallback for the unix build's
// O_NOFOLLOW-based guard: the stdlib exposes no direct O_NOFOLLOW
// equivalent on this platform. An Lstat-then-Open sequence is used instead.
//
// KNOWN, DOCUMENTED GAP: there is a window between the Lstat and the Open
// during which a symlink could be swapped into path — weaker than the unix
// build's single O_NOFOLLOW open, and deliberately not papered over as
// equivalent. Windows is a supported OS in the registry index schema, so
// this gap is called out explicitly in this package's failure-mode analysis
// (see the PR description), not left as a silent assumption.
func openRegularNoFollow(path string) (*os.File, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, conduiterr.Wrap(CodeArchiveInvalid, "could not stat the extracted binary", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, conduiterr.New(CodeArchiveInvalid, "extracted binary is a symlink, which is not permitted")
	}

	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, conduiterr.Wrap(CodeArchiveInvalid, "could not open the extracted binary", err)
	}
	fi2, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, conduiterr.Wrap(CodeArchiveInvalid, "could not stat the extracted binary", err)
	}
	if !fi2.Mode().IsRegular() {
		f.Close()
		return nil, conduiterr.New(CodeArchiveInvalid, "extracted binary is not a regular file")
	}
	return f, nil
}

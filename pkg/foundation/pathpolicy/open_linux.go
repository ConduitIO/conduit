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

//go:build linux

package pathpolicy

import (
	"os"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"

	"golang.org/x/sys/unix"
)

// errOpenat2Unsupported means the running kernel (or a seccomp filter) does
// not offer openat2; the caller falls back to walking the components.
var errOpenat2Unsupported = cerrors.New("openat2 unsupported")

func openat2Beneath(rootFd int, rel string, flag int, perm os.FileMode) (int, error) {
	how := &unix.OpenHow{
		Flags:   uint64(flag | unix.O_CLOEXEC), //nolint:gosec // open(2) flags are non-negative
		Mode:    uint64(perm.Perm()),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	}
	if flag&unix.O_CREAT == 0 {
		how.Mode = 0 // openat2 rejects a mode without O_CREAT
	}
	for {
		fd, err := unix.Openat2(rootFd, rel, how)
		switch {
		case err == nil:
			return fd, nil
		case cerrors.Is(err, unix.EINTR):
			continue
		case cerrors.Is(err, unix.ENOSYS), cerrors.Is(err, unix.EPERM), cerrors.Is(err, unix.EINVAL):
			return -1, errOpenat2Unsupported
		default:
			return -1, err
		}
	}
}

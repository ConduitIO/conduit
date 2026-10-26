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

package pathpolicy

import (
	"fmt"
	"os"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"golang.org/x/sys/unix"
)

const opOpen = "open"

// openBeneath opens rel (a clean, relative, symlink-free path) below root
// without following a symlink in any component. root is the resolved allowed
// directory. resolved is only used to name the returned file.
func openBeneath(root, resolved, rel string, flag int, perm os.FileMode) (*os.File, error) {
	parts, err := splitRel(rel)
	if err != nil {
		return nil, err
	}
	rootFd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: opOpen, Path: root, Err: err}
	}
	defer unix.Close(rootFd)

	if fd, err := openat2Beneath(rootFd, rel, flag, perm); err == nil {
		return os.NewFile(uintptr(fd), resolved), nil
	} else if !cerrors.Is(err, errOpenat2Unsupported) {
		return nil, &os.PathError{Op: opOpen, Path: resolved, Err: err}
	}

	// Portable path: one openat per component, never following a symlink.
	dirFd := rootFd
	for i, c := range parts {
		last := i == len(parts)-1
		var fd int
		if last {
			fd, err = unix.Openat(dirFd, c, flag|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm.Perm()))
		} else {
			fd, err = unix.Openat(dirFd, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if dirFd != rootFd {
			unix.Close(dirFd)
		}
		if err != nil {
			return nil, &os.PathError{Op: opOpen, Path: resolved, Err: err}
		}
		if last {
			return os.NewFile(uintptr(fd), resolved), nil
		}
		dirFd = fd
	}
	return nil, fmt.Errorf("pathpolicy: empty relative path")
}

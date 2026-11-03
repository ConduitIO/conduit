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

package chaos

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// pipeFill reports the unread bytes in a pipe (FIONREAD; TIOCINQ is the same
// request on Linux) and the pipe's capacity (F_GETPIPE_SZ). Any ioctl or fcntl
// error is returned so the caller fails the test with the errno.
func pipeFill(fd int) (pending, capacity int, supported bool, err error) {
	pending, err = unix.IoctlGetInt(fd, unix.TIOCINQ)
	if err != nil {
		return 0, 0, true, fmt.Errorf("FIONREAD: %w", err)
	}
	capacity, err = unix.FcntlInt(uintptr(fd), unix.F_GETPIPE_SZ, 0)
	if err != nil {
		return 0, 0, true, fmt.Errorf("F_GETPIPE_SZ: %w", err)
	}
	return pending, capacity, true, nil
}

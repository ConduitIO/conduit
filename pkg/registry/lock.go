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

package registry

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// DefaultLockTimeout bounds how long Install waits to acquire a lock before
// refusing with CodeInstallLocked — never an indefinite hang.
const DefaultLockTimeout = 30 * time.Second

// lockPollInterval is how often TryLockContext polls for the lock while
// waiting.
const lockPollInterval = 50 * time.Millisecond

// acquireLock creates (if needed) the lock file's parent directory and
// blocks — polling every lockPollInterval — until it acquires an exclusive
// flock, timeout elapses, or a lower-level error occurs, whichever comes
// first.
//
// flock releases automatically at the OS level on process exit, including
// SIGKILL — a killed holder never leaves a stale lock file that wedges a
// subsequent attempt indefinitely; --lock-timeout only bounds the wait for
// a lock genuinely still held by a live process. This property is exercised
// directly, not just assumed, by install_test.go's chaos/concurrent-install
// tests.
func acquireLock(path string, timeout time.Duration) (*flock.Flock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, conduiterr.Wrap(CodeInstallLocked, fmt.Sprintf("could not create lock directory for %q", path), err)
	}

	fl := flock.New(path)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	locked, err := fl.TryLockContext(ctx, lockPollInterval)
	if err != nil || !locked {
		return nil, conduiterr.New(CodeInstallLocked, fmt.Sprintf(
			"could not acquire the install lock %q within %s", path, timeout))
	}
	return fl, nil
}

// TargetLockPath returns the per-connector-name lock file path.
func TargetLockPath(connectorsPath, name string) string {
	return filepath.Join(locksDirPath(connectorsPath), name+".lock")
}

// ManifestLockPath returns the short-held global lock file guarding
// manifest.json's read-modify-write critical section.
func ManifestLockPath(connectorsPath string) string {
	return filepath.Join(locksDirPath(connectorsPath), ".manifest.lock")
}

// AcquireTargetLock acquires the per-connector-name install lock,
// serializing the FULL pipeline (download through manifest write) for two
// concurrent installs of the SAME connector name. Callers must Unlock the
// returned lock on every exit path (defer immediately after a successful
// call).
func AcquireTargetLock(connectorsPath, name string, timeout time.Duration) (*flock.Flock, error) {
	return acquireLock(TargetLockPath(connectorsPath, name), timeout)
}

// AcquireManifestLock acquires the short-held global manifest lock —
// required IN ADDITION to the per-target lock, because two installs of
// DIFFERENT connector names never contend on separate TargetLocks at all,
// but still share one manifest.json. Hold this only around the
// read-modify-write of manifest.json, never for the whole install pipeline.
func AcquireManifestLock(connectorsPath string, timeout time.Duration) (*flock.Flock, error) {
	return acquireLock(ManifestLockPath(connectorsPath), timeout)
}

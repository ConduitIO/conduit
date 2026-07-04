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

package conduit

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/matryer/is"
)

// TestEntrypoint_CancelOnInterrupt_SIGTERM is the regression test for #2519:
// SIGTERM must be caught and drive graceful cancellation (invariant 7), not
// terminate the process. Without the SIGTERM registration in CancelOnInterrupt,
// the default signal disposition kills this test's process, failing the test.
//
// Only SIGTERM is exercised. CancelOnInterrupt installs a process-global handler
// whose goroutine then blocks waiting for a *second* signal (which triggers a hard
// os.Exit). Sending a second, different signal from another subtest in the same
// process would be delivered to that leaked handler and exit the test binary, so we
// keep it to one signal per process for determinism.
func TestEntrypoint_CancelOnInterrupt_SIGTERM(t *testing.T) {
	is := is.New(t)
	e := &Entrypoint{}

	ctx := e.CancelOnInterrupt(context.Background())

	// Deliver SIGTERM to ourselves — what docker stop / kubectl / systemd send.
	err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
	is.NoErr(err)

	select {
	case <-ctx.Done():
		// SIGTERM was caught and canceled the context: the graceful path runs.
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM did not cancel the context; it was not handled (invariant 7)")
	}
}

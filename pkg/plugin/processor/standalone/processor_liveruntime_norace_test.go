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

//go:build !race

package standalone

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
)

// TestWASMProcessor_LiveRuntime_StuckModule_TeardownReturns covers the live
// record-path runtime, which is built WITHOUT close-on-context-done: cancelling
// the guest's context does nothing there, so kill falls back to closing the
// module from outside. Closing a module that is mid host call (the stall guest
// is in a sleep) trips the race detector inside wazero, so this test is excluded
// from -race builds; the race-safe property (Teardown returns within its bound
// after an abandoned command) is asserted by
// TestWASMProcessor_AbandonedCommand_TeardownReturns and the spin test.
func TestWASMProcessor_LiveRuntime_StuckModule_TeardownReturns(t *testing.T) {
	is := is.New(t)
	// Short stop window and grace so the fallback is reached; the guest sleeps 3s.
	oldStop, oldKill, oldGrace := moduleStopTimeout, moduleKillWait, moduleKillGrace
	moduleStopTimeout, moduleKillWait, moduleKillGrace = 200*time.Millisecond, 10*time.Second, 100*time.Millisecond
	t.Cleanup(func() { moduleStopTimeout, moduleKillWait, moduleKillGrace = oldStop, oldKill, oldGrace })

	underTest := newLiveProcessor(t, StallBinary)

	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelOnceGuestAccepts(underTest, cancel)
	_, err := underTest.executeCommand(cctx, specifyRequest())
	is.True(cerrors.Is(err, ErrCommandTimeout))

	err = teardownWithin(t, underTest, 2*time.Minute)
	is.True(cerrors.Is(err, ErrCommandTimeout))
	is.True(strings.Contains(err.Error(), "did not stop within"))
}

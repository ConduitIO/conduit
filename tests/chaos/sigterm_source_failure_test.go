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

package chaos

import (
	"testing"
	"time"

	"github.com/matryer/is"
)

// sigtermAfterSourceFailureExitBound is how long the child gets to exit
// after SIGTERM. A clean exit takes well under a second; Source.Teardown's own
// flush wait is bounded at connector.DefaultTeardownFlushTimeout (10s), so
// this leaves room for that worst case without letting a hang pass.
const sigtermAfterSourceFailureExitBound = 20 * time.Second

// TestSIGTERM_SourceFailureRacingStop_ExitsAndReplays is the end-to-end case
// for #2969 (#2977): a source fails while a graceful stop is in flight on the
// default engine (pkg/lifecycle), and the process must still shut down.
//
// The child runs a real pkg/lifecycle.Service. Its source fails at failAt
// while the destination holds every ack, so the failed source node cannot
// finish: it waits for its open records before tearing down. That is the
// window of #2969. SIGTERM then runs the runtime's own shutdown (StopAll with
// pipeline.ErrGracefulShutdown and a context without a deadline, then Wait).
// The stop reaches the source connector, which releases the destination's
// acks lcHoldAcksSettle later, so the stop is already injecting its
// control message when the node finishes. On v0.20.0 that injection held the
// PubNode lock while blocked on a channel nobody read, the node's cleanup
// waited on the lock, and StopAll never returned.
//
// Assertions:
//   - Invariant 7: the child exits on its own, with code 0, within
//     sigtermAfterSourceFailureExitBound.
//   - Invariant 1: every position the upstream committed by then was durably
//     delivered downstream.
//   - Invariant 3: a restart replays what was not acked and delivers every
//     position through total, with no gap.
//
// The SIGTERM is sent after the source has failed (markerLCSourceFailed) and
// before the destination can release a single ack (holdAcksUntilStop), so it
// lands inside the window by construction, not by timing. The one ordering
// left to the scheduler is the node noticing the failed read, which takes
// microseconds after the stream closes; the short sleep below covers it. If
// it ever lost, the stop would be an ordinary graceful stop, which must pass
// these assertions too, so the case cannot fail spuriously, only pass
// vacuously, and the "fails on v0.20.0" check in the PR guards that.
func TestSIGTERM_SourceFailureRacingStop_ExitsAndReplays(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	cfg := lcChildConfig{
		dbDir:       dir + "/db",
		upstreamDir: dir + "/upstream",
		mainDir:     dir + "/main-delivered",
		dlqDir:      dir + "/dlq-delivered",
		total:       20,
		paceMS:      2,
		failAt:      6, // positions 1-5 are delivered, none acked, then the source fails
		// No recovery inside this process: a stop was requested before the
		// run ended, so it must not recover (ADR
		// 20261007-stop-requested-never-recovers). A long backoff would make
		// an unexpected recovery show up as a timeout rather than pass.
		minDelayMS:        10_000,
		maxDelayMS:        10_000,
		engine:            lcEngineV1,
		sigterm:           true,
		holdAcksUntilStop: true,
	}

	first := spawnLCChild(t, cfg)
	first.waitForMarker(t, markerLCSourceFailed, 10*time.Second)
	time.Sleep(100 * time.Millisecond) // let the source node observe the failed read
	first.sigterm(t, sigtermAfterSourceFailureExitBound)

	_, exited := first.line(markerLCSigtermExit)
	is.True(exited) // invariant 7: StopAll and Wait returned
	_, stopReached := first.line(markerLCSourceStopCalled)
	is.True(stopReached) // vacuity guard: the stop reached the failed source

	committed := assertRecoveryInvariantsAtKill(t, cfg) // invariant 1
	is.True(committed <= cfg.failAt-1)                  // nothing past the failure was committed

	restart := lcChildConfig{
		dbDir: cfg.dbDir, upstreamDir: cfg.upstreamDir, mainDir: cfg.mainDir, dlqDir: cfg.dlqDir,
		total: cfg.total, paceMS: cfg.paceMS,
		minDelayMS: 10, maxDelayMS: 10,
		engine:   lcEngineV1,
		graceful: true,
	}
	second := spawnLCChild(t, restart)
	second.waitExit(t, parentWaitExit)
	_, done := second.line(markerLCDone)
	is.True(done)

	assertRecoveryInvariantsAfterRestart(t, cfg) // invariant 3
}

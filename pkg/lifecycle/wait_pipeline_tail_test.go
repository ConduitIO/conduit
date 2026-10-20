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

package lifecycle

import (
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// waitPipelineTailWindow is how long the tests below give a wrong
// WaitPipeline to return early. It is negative evidence only: on a correct
// run WaitPipeline cannot return inside it, because the goroutine it must wait
// for is held by a channel only the test closes. A slow machine can make a
// regression slip past the window, never make a correct run fail.
const waitPipelineTailWindow = 200 * time.Millisecond

// TestServiceLifecycle_WaitPipeline_WaitsForNodeGoroutineTail is the #3087
// regression test. A node goroutine writes its "node stopped" log line in a
// defer that runs after nodesWg.Done(), so the cleanup goroutine can record
// the terminal error and remove the run from runningPipelines while that node
// goroutine is still running. WaitPipeline then took its "already cleaned up"
// path and returned the terminal error without waiting for the run's
// goroutines. In CI that let TestServiceLifecycle_Stop return while a node
// goroutine was still logging through its testing.T, which the race detector
// reported against the next test (TestServiceLifecycle_PipelineStop).
//
// The gate holds the failing node's error-level "node stopped" line, and
// gate.held says the node goroutine is parked there. The OnFailure event is
// the second barrier: cleanupRun notifies only after it has deleted the
// runningPipelines entry, so once the event is in, WaitPipeline is on its
// post-cleanup path. It must still not return until the held node goroutine
// has returned.
func TestServiceLifecycle_WaitPipeline_WaitsForNodeGoroutineTail(t *testing.T) {
	fatalErr := cerrors.FatalError(cerrors.New("source schema is invalid"))
	tr := newTerminalRun(t, testErrRecoveryCfg(), true, newScriptedNode(fatalErr, nil))
	release := make(chan struct{})
	// Set before start: nothing reads the gate yet.
	tr.gate.release = release
	tr.gate.held = make(chan struct{})

	tr.start(t)
	waitClosed(t, tr.gate.held, "the failing node goroutine to park in its \"node stopped\" log line")
	recvFailure(t, tr)
	if _, live := tr.ls.runningPipelines.Get(tr.pl.ID); live {
		t.Fatal("the run is still in runningPipelines after OnFailure; the barrier did not hold")
	}

	assertWaitPipelineHeld(t, tr, release, fatalErr)
}

// TestServiceLifecycle_WaitPipeline_WaitsForCleanupTail covers the other
// goroutine that runs after the runningPipelines entry is gone: the cleanup
// goroutine itself, while it calls the OnFailure handlers. WaitPipeline must
// not return while a handler is still running.
func TestServiceLifecycle_WaitPipeline_WaitsForCleanupTail(t *testing.T) {
	fatalErr := cerrors.FatalError(cerrors.New("source schema is invalid"))
	tr := newTerminalRun(t, testErrRecoveryCfg(), false, newScriptedNode(fatalErr, nil))
	release := make(chan struct{})
	// Handlers run in registration order: the harness's handler delivers the
	// event (the barrier) before this one blocks.
	tr.ls.OnFailure(func(FailureEvent) {
		select {
		case <-release:
		case <-tr.abort:
		}
	})

	tr.start(t)
	recvFailure(t, tr)

	assertWaitPipelineHeld(t, tr, release, fatalErr)
}

func recvFailure(t *testing.T, tr *terminalRun) {
	t.Helper()
	select {
	case <-tr.failures:
	case <-time.After(terminalStatusGuard):
		t.Fatalf("no OnFailure event within %s; statuses so far: %v", terminalStatusGuard, tr.statuses())
	}
}

// assertWaitPipelineHeld checks that WaitPipeline does not return while a
// goroutine of the run is held, returns once release is closed, and returns
// the run's terminal error with every goroutine of the run gone.
func assertWaitPipelineHeld(t *testing.T, tr *terminalRun, release chan struct{}, wantErr error) {
	t.Helper()
	waitDone := make(chan error, 1)
	go func() { waitDone <- tr.ls.WaitPipeline(tr.pl.ID) }()

	select {
	case err := <-waitDone:
		close(release)
		t.Fatalf("WaitPipeline returned (%v) while a goroutine of the run was still running (#3087)", err)
	case <-time.After(waitPipelineTailWindow):
	}

	close(release)
	var err error
	select {
	case err = <-waitDone:
	case <-time.After(terminalStatusGuard):
		t.Fatalf("WaitPipeline did not return within %s after the run was released", terminalStatusGuard)
	}
	if !cerrors.Is(err, wantErr) {
		t.Fatalf("WaitPipeline returned %v, want %v", err, wantErr)
	}
	select {
	case <-tr.rp.t.Dead():
	default:
		t.Fatal("WaitPipeline returned before the run's tomb was dead")
	}
}

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
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
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

// messageGate is a log writer that holds the nth line carrying message until
// release is closed (or abort, so a failing test cannot hang). held closes
// when that line is being held.
type messageGate struct {
	message []byte
	nth     int64
	seen    atomic.Int64
	held    chan struct{}
	release <-chan struct{}
	abort   <-chan struct{}
}

func (g *messageGate) Write(p []byte) (int, error) {
	if !bytes.Contains(p, g.message) {
		return len(p), nil
	}
	if g.seen.Add(1) == g.nth {
		close(g.held)
		select {
		case <-g.release:
		case <-g.abort:
		}
	}
	return len(p), nil
}

// TestServiceLifecycle_WaitPipeline_WaitsForRecoveryPredecessor covers the
// recovery chain (#3087 review). A recovery restart runs on the failed run's
// cleanup goroutine: Start publishes the restarted run, and the old goroutine
// then keeps going (it logs "pipeline started", releases its reservation and
// returns). WaitPipeline on the restarted run used to wait for that run's
// tomb only, so it could report the pipeline stopped while the predecessor
// was still running.
//
// The gate holds the predecessor in its "pipeline started" log line (the
// second one: the first is the test's own Start). The restarted run is then
// stopped; WaitPipeline must not return until the predecessor is released.
func TestServiceLifecycle_WaitPipeline_WaitsForRecoveryPredecessor(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()

	release := make(chan struct{})
	abort := make(chan struct{})
	t.Cleanup(func() { close(abort) })
	gate := &messageGate{
		message: []byte(`"message":"pipeline started"`),
		nth:     2,
		held:    make(chan struct{}),
		release: release,
		abort:   abort,
	}
	logger := log.New(zerolog.New(gate).Level(zerolog.InfoLevel))
	db := &inmemory.DB{}
	persister := connector.NewPersister(log.Nop(), db, time.Second, 3)
	defer stopAndWaitPersister(t, killAll, persister)

	ps := pipeline.NewService(log.Nop(), db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	ctrl := gomock.NewController(t)
	source, srcDispenser := asserterSource(ctrl, persister, generateRecords(0), nil, true, 2)
	destination, destDispenser := asserterDestination(ctrl, persister, generateRecords(0), 2)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, 2)
	pl.DLQ.Plugin = dlq.Plugin
	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(logger, testErrRecoveryCfg(),
		testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
		testProcessorService{},
		testConnectorPluginService{
			source.Plugin:      srcDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		}, ps)

	is.NoErr(ls.Start(ctx, pl.ID))
	first, ok := ls.runningPipelines.Get(pl.ID)
	is.True(ok)

	// A recoverable error: the run's cleanup goroutine restarts the pipeline
	// after the backoff (MinDelay 1s) and is then held by the gate.
	injectSourceError(ctx, t, ls, pl.ID, cerrors.New("lost connection to database"))
	waitClosed(t, gate.held, "the predecessor to log \"pipeline started\" for the restart")

	restarted, ok := ls.runningPipelines.Get(pl.ID)
	is.True(ok)
	is.True(restarted != first)
	if isDead(first.t) {
		t.Fatal("the predecessor's tomb is dead while its goroutine is held; the gate does not hold the window")
	}

	// Stop the restarted run and wait for every goroutine of it to return.
	is.NoErr(ls.Stop(ctx, pl.ID, false))
	waitClosed(t, restarted.t.Dead(), "the restarted run to stop")

	waitDone := make(chan error, 1)
	go func() { waitDone <- ls.WaitPipeline(pl.ID) }()
	select {
	case err := <-waitDone:
		close(release)
		t.Fatalf("WaitPipeline returned (%v) while the recovery predecessor was still running (#3087)", err)
	case <-time.After(waitPipelineTailWindow):
	}

	close(release)
	select {
	case err = <-waitDone:
	case <-time.After(terminalStatusGuard):
		t.Fatalf("WaitPipeline did not return within %s after the predecessor was released", terminalStatusGuard)
	}
	is.NoErr(err)
	select {
	case <-first.t.Dead():
	default:
		t.Fatal("WaitPipeline returned before the predecessor's tomb was dead")
	}
	is.Equal(pipeline.StatusUserStopped, pl.GetStatus())
}

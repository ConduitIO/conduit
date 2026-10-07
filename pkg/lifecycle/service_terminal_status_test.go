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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle/stream"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

// terminalStatusGuard bounds how long these tests wait for a run to finish.
// It is a failure guard only: on a correct run every wait below is released
// by a channel the code under test closes, never by this timer.
const terminalStatusGuard = 10 * time.Second

// scriptedNode is a stream.StoppableNode whose Run either fails immediately
// with runErr, or (runErr == nil) blocks until Stop is called or its context
// is canceled. Stop's reason is returned by Run, matching
// stream.SourceNode's contract (a user Stop passes nil, StopAll passes
// pipeline.ErrGracefulShutdown). If ctxErr is set, Run returns it when its
// context is canceled, standing in for a node that fails while the tomb is
// being killed. If waitFor is set, a failing Run first waits for it to close,
// which lets a test order this node's failure after another node's exit.
type scriptedNode struct {
	id      string
	runErr  error
	ctxErr  error
	stop    chan error
	waitFor <-chan struct{}
}

func newScriptedNode(runErr, ctxErr error) *scriptedNode {
	return &scriptedNode{id: "node-" + uuid.NewString(), runErr: runErr, ctxErr: ctxErr, stop: make(chan error, 1)}
}

func (n *scriptedNode) ID() string { return n.id }

func (n *scriptedNode) Run(ctx context.Context) error {
	if n.runErr != nil {
		if n.waitFor != nil {
			<-n.waitFor
		}
		return n.runErr
	}
	select {
	case reason := <-n.stop:
		return reason
	case <-ctx.Done():
		return n.ctxErr
	}
}

func (n *scriptedNode) Stop(_ context.Context, reason error) error {
	n.stop <- reason
	return nil
}

var _ stream.StoppableNode = (*scriptedNode)(nil)

// nodeStoppedGate is a log writer that blocks the error-level "node stopped"
// line until release is closed (or abort, so a failing test cannot hang).
//
// That line is written by a node goroutine's deferred logger, after
// nodesWg.Done() and before the goroutine returns its error to the tomb. Holding it
// holds the #2896 window open: the cleanup goroutine is already past
// nodesWg.Wait() while the failing node's tomb.run bookkeeping has not run.
//
// hits counts the lines it actually held, so a test can assert the gate
// engaged: if the log line is renamed, the gate would otherwise silently stop
// holding anything and the test would degrade to racing the window.
//
// watchID/watched optionally signal when a given node's "node stopped" line
// (at any level) is written. That line is written after the node's deferred
// nodesWg.Done(), so watched closing proves that node's Done() has run.
type nodeStoppedGate struct {
	release <-chan struct{}
	abort   <-chan struct{}
	hits    atomic.Int64

	watchID   string
	watched   chan struct{}
	watchOnce sync.Once
}

func (w *nodeStoppedGate) Write(p []byte) (int, error) {
	if !bytes.Contains(p, []byte(`"message":"node stopped"`)) {
		return len(p), nil
	}
	if w.watched != nil && bytes.Contains(p, []byte(`"node_id":"`+w.watchID+`"`)) {
		w.watchOnce.Do(func() { close(w.watched) })
	}
	if bytes.Contains(p, []byte(`"level":"error"`)) {
		w.hits.Add(1)
		select {
		case <-w.release:
		case <-w.abort:
		}
	}
	return len(p), nil
}

// watch makes the gate close the returned channel once node id's "node
// stopped" line is written. Call before start.
func (w *nodeStoppedGate) watch(id string) <-chan struct{} {
	w.watchID = id
	w.watched = make(chan struct{})
	return w.watched
}

// terminalRun is the harness shared by the tests below: one runnablePipeline
// made of the given nodes, run through runPipeline directly (the code under
// test is runPipeline's node goroutines and cleanup goroutine; building real
// connectors would only add noise). It records every status written and
// every OnFailure event.
type terminalRun struct {
	ls       *Service
	rp       *runnablePipeline
	pl       *pipeline.Instance
	rec      *statusRecorder
	failures chan FailureEvent
	// gate is the log writer when gateLog was requested, nil otherwise.
	gate *nodeStoppedGate

	// firstTerminal closes on the first status write other than Running,
	// i.e. the cleanup goroutine's classification of the run.
	firstTerminal chan struct{}
	abort         chan struct{}
}

func newTerminalRun(t *testing.T, cfg *ErrRecoveryCfg, gateLog bool, nodes ...stream.Node) *terminalRun {
	t.Helper()
	tr := &terminalRun{
		pl:            &pipeline.Instance{ID: uuid.NewString(), Config: pipeline.Config{Name: "p"}},
		failures:      make(chan FailureEvent, 4),
		firstTerminal: make(chan struct{}),
		abort:         make(chan struct{}),
	}
	t.Cleanup(func() { close(tr.abort) })

	logger := log.Nop()
	if gateLog {
		tr.gate = &nodeStoppedGate{release: tr.firstTerminal, abort: tr.abort}
		// Trace level explicitly: the watch hook relies on the trace-level
		// "node stopped" line of a node that returned nil.
		logger = log.New(zerolog.New(tr.gate).Level(zerolog.TraceLevel))
	}

	tr.rec = newStatusRecorder(testPipelineService{tr.pl.ID: tr.pl})
	var once sync.Once
	tr.rec.onUpdate = func(s pipeline.Status, _ int) error {
		if s != pipeline.StatusRunning {
			once.Do(func() { close(tr.firstTerminal) })
		}
		return nil
	}

	tr.ls = NewService(logger, cfg, testConnectorService{}, testProcessorService{}, testConnectorPluginService{}, tr.rec)
	tr.ls.OnFailure(func(e FailureEvent) { tr.failures <- e })

	tr.rp = &runnablePipeline{
		pipeline:         tr.pl,
		n:                nodes,
		backoff:          cfg.toBackoff(),
		recoveryAttempts: &atomic.Int64{},
	}
	return tr
}

func (tr *terminalRun) start(t *testing.T) {
	t.Helper()
	if err := tr.ls.runPipeline(context.Background(), tr.rp); err != nil {
		t.Fatalf("runPipeline: %v", err)
	}
}

// waitDead blocks until every goroutine of the run, including the cleanup
// goroutine, has returned.
func (tr *terminalRun) waitDead(t *testing.T) {
	t.Helper()
	select {
	case <-tr.rp.t.Dead():
	case <-time.After(terminalStatusGuard):
		t.Fatalf("run did not finish within %s; statuses so far: %v", terminalStatusGuard, tr.statuses())
	}
}

func (tr *terminalRun) statuses() []pipeline.Status {
	tr.rec.mu.Lock()
	defer tr.rec.mu.Unlock()
	return append([]pipeline.Status(nil), tr.rec.statuses...)
}

func (tr *terminalRun) failureEvents() []FailureEvent {
	var out []FailureEvent
	for {
		select {
		case e := <-tr.failures:
			out = append(out, e)
		default:
			return out
		}
	}
}

// noRetryRecoveryCfg enters the recovery path (StatusRecovering, then
// StartWithBackoff) but gives up on the first attempt, without sleeping:
// attempt 1 > MaxRetries 0. That proves recovery was triggered while letting
// the run end deterministically, with no backoff goroutine outliving the test.
func noRetryRecoveryCfg() *ErrRecoveryCfg {
	cfg := testErrRecoveryCfg()
	cfg.MaxRetries = 0
	return cfg
}

// TestServiceLifecycle_NodeErrorNotReportedAsUserStopped is the #2896
// regression test.
//
// A node goroutine's deferred nodesWg.Done() used to run before its error
// reached the tomb (tomb.v2 records a t.Go'd function's error only after the
// function returns). The cleanup goroutine could wake from nodesWg.Wait() in
// between, read rp.t.Err() == tomb.ErrStillAlive and take the "manual stop"
// branch: StatusUserStopped with an empty error, no OnFailure, and for a
// transient error no recovery at all.
//
// The test does not race that window, it holds it open: the failing node's
// "node stopped" error log line (written after Done(), before the return)
// blocks until the cleanup goroutine has written its terminal status. On the
// pre-fix code that is a deterministic UserStopped; with the fix the error is
// on the tomb before Done(), so the cleanup classifies it correctly.
func TestServiceLifecycle_NodeErrorNotReportedAsUserStopped(t *testing.T) {
	testCases := []struct {
		name         string
		err          error
		cfg          *ErrRecoveryCfg
		wantStatuses []pipeline.Status
		// wantInFailure is a substring the OnFailure event and the stored
		// pipeline error must both carry.
		wantInFailure string
	}{{
		name:          "fatal error degrades",
		err:           cerrors.FatalError(cerrors.New("source connector error")),
		cfg:           testErrRecoveryCfg(),
		wantStatuses:  []pipeline.Status{pipeline.StatusRunning, pipeline.StatusDegraded},
		wantInFailure: "source connector error",
	}, {
		name:         "transient error triggers recovery",
		err:          cerrors.New("lost connection"),
		cfg:          noRetryRecoveryCfg(),
		wantStatuses: []pipeline.Status{pipeline.StatusRunning, pipeline.StatusRecovering, pipeline.StatusDegraded},
		// MaxRetries 0: recovery gives up on its first attempt, and that
		// is the error the run ends with.
		wantInFailure: pipeline.ErrPipelineCannotRecover.Error(),
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			tr := newTerminalRun(t, tc.cfg, true, newScriptedNode(tc.err, nil))
			tr.start(t)
			tr.waitDead(t)
			// The gate must actually have held the window open; otherwise
			// this test is only racing it.
			is.True(tr.gate.hits.Load() >= 1)

			got := tr.statuses()
			if len(got) >= 2 && got[1] == pipeline.StatusUserStopped {
				t.Fatalf("failed pipeline reported as %v (statuses %v): the cleanup goroutine read the tomb before the node's error reached it (#2896)", got[1], got)
			}
			is.Equal(tc.wantStatuses, got)

			events := tr.failureEvents()
			is.Equal(len(events), 1) // OnFailure must fire exactly once
			is.Equal(events[0].ID, tr.pl.ID)
			is.True(strings.Contains(events[0].Error.Error(), tc.wantInFailure))
			is.True(strings.Contains(tr.pl.Error, tc.wantInFailure))

			if tc.cfg.MaxRetries == 0 {
				is.Equal(tr.rp.recoveryAttempts.Load(), int64(1)) // StartWithBackoff ran
			}

			// The stored terminal error must not be nil either: WaitPipeline
			// after cleanup reads it from terminalErrors.
			err := tr.ls.WaitPipeline(tr.pl.ID)
			is.True(err != nil)
			is.True(strings.Contains(err.Error(), tc.wantInFailure))
		})
	}
}

// TestServiceLifecycle_NodeErrorAfterHealthyNodeExit pins the claim the #2896
// fix rests on with more than one node: nodesWg cannot reach zero before
// every failed node has recorded its error on the tomb, even when a healthy
// sibling has already returned nil and called Done().
//
// Ordering is forced, not raced: the failing node does not return its error
// until the healthy node's "node stopped" line has been written, which happens
// after the healthy node's deferred Done(). So the failing node is the last
// one to reach Done(), the case where the cleanup goroutine wakes right after
// it. The failing node's own error log line is then held by the gate, as in
// the single-node test.
func TestServiceLifecycle_NodeErrorAfterHealthyNodeExit(t *testing.T) {
	is := is.New(t)
	healthy := newScriptedNode(nil, nil)
	failing := newScriptedNode(cerrors.FatalError(cerrors.New("source connector error")), nil)
	tr := newTerminalRun(t, testErrRecoveryCfg(), true, healthy, failing)
	failing.waitFor = tr.gate.watch(healthy.ID())

	tr.start(t)
	// The healthy node returns nil (a source that finished, say) before the
	// failing node fails.
	is.NoErr(healthy.Stop(context.Background(), nil))
	tr.waitDead(t)

	is.True(tr.gate.hits.Load() >= 1)
	got := tr.statuses()
	if len(got) >= 2 && got[1] == pipeline.StatusUserStopped {
		t.Fatalf("failed pipeline reported as %v (statuses %v) after a healthy sibling exited first (#2896)", got[1], got)
	}
	is.Equal([]pipeline.Status{pipeline.StatusRunning, pipeline.StatusDegraded}, got)

	events := tr.failureEvents()
	is.Equal(len(events), 1)
	is.True(strings.Contains(events[0].Error.Error(), "source connector error"))
	is.True(strings.Contains(events[0].Error.Error(), failing.ID()))
}

// TestServiceLifecycle_UserStopStillUserStopped checks the other side of the
// #2896 fix: a genuine graceful Stop, where every node returns nil, must
// still be reported as UserStopped, with no failure event, no recovery and a
// nil terminal error.
func TestServiceLifecycle_UserStopStillUserStopped(t *testing.T) {
	is := is.New(t)
	node := newScriptedNode(nil, nil)
	tr := newTerminalRun(t, testErrRecoveryCfg(), false, node)
	tr.start(t)

	is.NoErr(tr.ls.Stop(context.Background(), tr.pl.ID, false))
	tr.waitDead(t)

	is.Equal([]pipeline.Status{pipeline.StatusRunning, pipeline.StatusUserStopped}, tr.statuses())
	is.Equal(len(tr.failureEvents()), 0)
	is.Equal(tr.rp.recoveryAttempts.Load(), int64(0))
	is.Equal(tr.pl.Error, "")
	is.NoErr(tr.ls.WaitPipeline(tr.pl.ID))
}

// TestServiceLifecycle_StopAllStillSystemStopped is the SIGTERM path:
// StopAll(ErrGracefulShutdown) makes nodes return ErrGracefulShutdown, which
// the node goroutine swallows (it is not a failure). That must still end as
// SystemStopped, not Degraded or Recovering — the fix only kills the tomb
// for errors the node goroutine actually returns.
func TestServiceLifecycle_StopAllStillSystemStopped(t *testing.T) {
	is := is.New(t)
	node := newScriptedNode(nil, nil)
	tr := newTerminalRun(t, testErrRecoveryCfg(), false, node)
	tr.start(t)

	tr.ls.StopAll(context.Background(), pipeline.ErrGracefulShutdown)
	tr.waitDead(t)

	is.Equal([]pipeline.Status{pipeline.StatusRunning, pipeline.StatusSystemStopped}, tr.statuses())
	is.Equal(len(tr.failureEvents()), 0)
	is.Equal(tr.rp.recoveryAttempts.Load(), int64(0))
	is.NoErr(tr.ls.WaitPipeline(tr.pl.ID))
}

// TestServiceLifecycle_ForceStopWinsOverNodeError covers a node that fails
// because a force Stop killed the tomb. stopForceful records
// FatalError(ErrForceStop) first; the node's own (transient) error, now also
// passed to Kill by the node goroutine, must not replace it. The run must end
// Degraded with the force-stop reason and must not enter recovery.
func TestServiceLifecycle_ForceStopWinsOverNodeError(t *testing.T) {
	is := is.New(t)
	node := newScriptedNode(nil, cerrors.New("lost connection"))
	tr := newTerminalRun(t, testErrRecoveryCfg(), false, node)
	tr.start(t)

	is.NoErr(tr.ls.Stop(context.Background(), tr.pl.ID, true))
	tr.waitDead(t)

	is.Equal([]pipeline.Status{pipeline.StatusRunning, pipeline.StatusDegraded}, tr.statuses())
	is.Equal(tr.rp.recoveryAttempts.Load(), int64(0))
	is.True(strings.Contains(tr.pl.Error, pipeline.ErrForceStop.Error()))
	is.True(!strings.Contains(tr.pl.Error, "lost connection"))

	events := tr.failureEvents()
	is.Equal(len(events), 1)
	is.True(cerrors.Is(events[0].Error, pipeline.ErrForceStop))
}

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

// Regression tests for a pipeline status write that fails while a run is live
// (#2898, #2899; docs/design-documents/20261007-lifecycle-status-write-failure.md).
// The status write is a report: once a run has goroutines, a failed write must
// not change what the run does.
//
// The fault is injected in the database under a real pipeline.Service, so the
// in-memory status moves first and only the store write fails, exactly as it
// does in production. Every wait is released by a channel or a tomb the code
// under test closes; the timers are failure guards only.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle/stream"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// pipelineKeyPrefix is pipeline.Store's key prefix. Only these keys are
// faulted, so position writes (same database) keep landing.
const pipelineKeyPrefix = "pipeline:instance:"

var errStatusStoreDown = cerrors.New("injected: pipeline store write failed")

type injectStatusFaultKey struct{}

type injectStatusHangKey struct{}

// statusFaultDB fails a pipeline-instance write whose context carries
// injectStatusFaultKey. With honourCtx it also fails a pipeline-instance write
// on a cancelled context, as the SQLite and Postgres backends do.
//
// A write whose context carries injectStatusHangKey blocks until release is
// closed (then lands), standing in for a hung store. With honourCtx it gives
// up when its context ends instead, as SQLite and Postgres would; without it,
// it ignores the context, as badger does.
type statusFaultDB struct {
	database.DB
	honourCtx bool
	release   chan struct{}
}

func (d *statusFaultDB) Set(ctx context.Context, key string, value []byte) error {
	if strings.HasPrefix(key, pipelineKeyPrefix) {
		if ctx.Value(injectStatusFaultKey{}) != nil {
			return errStatusStoreDown
		}
		if ctx.Value(injectStatusHangKey{}) != nil {
			if d.honourCtx {
				select {
				case <-d.release:
				case <-ctx.Done():
					return ctx.Err()
				}
			} else {
				<-d.release
			}
		}
		if d.honourCtx && ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return d.DB.Set(ctx, key, value)
}

// failStatusWrites wraps a pipeline.Service backed by a statusFaultDB and
// makes the store write of the UpdateStatus calls selected by fail fail.
// pipeline.Service still applies the status in memory first.
type failStatusWrites struct {
	PipelineService
	// fail selects the calls to fail by status and its 1-based occurrence.
	fail func(status pipeline.Status, nth int) bool
	// beforeFail, if set, runs before a selected write is attempted.
	beforeFail func(status pipeline.Status, nth int)
	// hang makes a selected write hang in the store instead of failing.
	hang bool

	mu     sync.Mutex
	counts map[pipeline.Status]int
	failed []pipeline.Status
}

func newFailStatusWrites(inner PipelineService, fail func(pipeline.Status, int) bool) *failStatusWrites {
	return &failStatusWrites{PipelineService: inner, fail: fail, counts: map[pipeline.Status]int{}}
}

func (f *failStatusWrites) UpdateStatus(ctx context.Context, id string, status pipeline.Status, errMsg string) error {
	f.mu.Lock()
	f.counts[status]++
	nth := f.counts[status]
	inject := f.fail(status, nth)
	if inject {
		f.failed = append(f.failed, status)
	}
	f.mu.Unlock()

	if inject {
		if f.beforeFail != nil {
			f.beforeFail(status, nth)
		}
		if f.hang {
			ctx = context.WithValue(ctx, injectStatusHangKey{}, true)
		} else {
			ctx = context.WithValue(ctx, injectStatusFaultKey{}, true)
		}
	}
	return f.PipelineService.UpdateStatus(ctx, id, status, errMsg)
}

func (f *failStatusWrites) failedStatuses() []pipeline.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pipeline.Status(nil), f.failed...)
}

func failNth(want pipeline.Status, n int) func(pipeline.Status, int) bool {
	return func(s pipeline.Status, nth int) bool { return s == want && nth == n }
}

// storedStatus reads the pipeline's status from the database, not memory.
func storedStatus(t *testing.T, db database.DB, id string) pipeline.Status {
	t.Helper()
	inst, err := pipeline.NewStore(db).Get(context.Background(), id)
	if err != nil {
		t.Fatalf("reading stored pipeline %s: %v", id, err)
	}
	return inst.GetStatus()
}

// statusFaultRun is a run of scripted nodes over a real pipeline.Service
// whose status writes can be faulted.
type statusFaultRun struct {
	ls       *Service
	db       *statusFaultDB
	ps       *failStatusWrites
	pl       *pipeline.Instance
	rp       *runnablePipeline
	failures chan FailureEvent
}

func newStatusFaultRun(t *testing.T, db *statusFaultDB, fail func(pipeline.Status, int) bool, nodes ...stream.Node) *statusFaultRun {
	t.Helper()
	logger := log.Nop()
	inner := pipeline.NewService(logger, db)
	pl, err := inner.Create(context.Background(), uuid.NewString(), pipeline.Config{Name: "p-" + uuid.NewString()}, pipeline.ProvisionTypeAPI)
	if err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	r := &statusFaultRun{
		db:       db,
		ps:       newFailStatusWrites(inner, fail),
		pl:       pl,
		failures: make(chan FailureEvent, 4),
	}
	cfg := testErrRecoveryCfg()
	r.ls = NewService(logger, cfg, testConnectorService{}, testProcessorService{}, testConnectorPluginService{}, r.ps)
	r.ls.OnFailure(func(e FailureEvent) { r.failures <- e })
	r.rp = &runnablePipeline{
		pipeline:         pl,
		n:                nodes,
		backoff:          cfg.toBackoff(),
		recoveryAttempts: &atomic.Int64{},
	}
	// On a regression the run can be left live and unreachable. Kill it so
	// the test fails instead of leaking its goroutines.
	t.Cleanup(func() {
		if r.rp.t != nil {
			r.rp.t.Kill(cerrors.FatalError(pipeline.ErrForceStop))
		}
	})
	return r
}

func (r *statusFaultRun) failureEvents() []FailureEvent {
	var out []FailureEvent
	for {
		select {
		case e := <-r.failures:
			out = append(out, e)
		default:
			return out
		}
	}
}

// TestServiceLifecycle_RunningWriteFails_RunStaysReachable is the #2898 (I1)
// regression test. runPipeline used to roll a run out of runningPipelines when
// its StatusRunning write failed, after its nodes had started, and return the
// error without registering a cleanup goroutine. The nodes kept running, Stop
// answered "not running", nothing would ever record a terminal status, and
// Wait blocked until the exit timeout.
func TestServiceLifecycle_RunningWriteFails_RunStaysReachable(t *testing.T) {
	is := is.New(t)
	r := newStatusFaultRun(t, &statusFaultDB{DB: &inmemory.DB{}}, failNth(pipeline.StatusRunning, 1), newScriptedNode(nil, nil))

	// The status write is a report: the run is live, so Start succeeds.
	is.NoErr(r.ls.runPipeline(context.Background(), r.rp))
	is.Equal(r.ps.failedStatuses(), []pipeline.Status{pipeline.StatusRunning}) // the fault engaged

	got, ok := r.ls.runningPipelines.Get(r.pl.ID)
	is.True(ok)
	is.True(got == r.rp)
	is.True(r.rp.t.Alive())
	is.Equal(r.pl.GetStatus(), pipeline.StatusRunning)                   // memory reports the live run
	is.Equal(storedStatus(t, r.db, r.pl.ID), pipeline.StatusUserStopped) // the store missed it

	is.NoErr(r.ls.Stop(context.Background(), r.pl.ID, false))
	is.NoErr(r.ls.Wait(terminalStatusGuard))
	waitClosed(t, r.rp.t.Dead(), "the run to finish")

	is.Equal(r.pl.GetStatus(), pipeline.StatusUserStopped)
	is.Equal(storedStatus(t, r.db, r.pl.ID), pipeline.StatusUserStopped)
	_, ok = r.ls.runningPipelines.Get(r.pl.ID)
	is.True(!ok)
	is.Equal(len(r.failureEvents()), 0)
}

// TestServiceLifecycle_RunningWrite_CallerCancelled is the cancelled-caller
// trigger of #2898. runPipeline wrote StatusRunning with the caller's context
// (for an API Start, the request context). A backend that honours the context
// (SQLite, Postgres) then failed the write when the client gave up after the
// nodes had started, which orphaned the run as above. The write now uses a
// context that ignores the caller's cancellation, so it lands.
func TestServiceLifecycle_RunningWrite_CallerCancelled(t *testing.T) {
	is := is.New(t)
	never := func(pipeline.Status, int) bool { return false }
	r := newStatusFaultRun(t, &statusFaultDB{DB: &inmemory.DB{}, honourCtx: true}, never, newScriptedNode(nil, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The nodes have started: the caller gives up now.
	r.ls.testBeforePublish = func(*runnablePipeline) { cancel() }

	is.NoErr(r.ls.runPipeline(ctx, r.rp))
	is.Equal(storedStatus(t, r.db, r.pl.ID), pipeline.StatusRunning)
	got, ok := r.ls.runningPipelines.Get(r.pl.ID)
	is.True(ok)
	is.True(got == r.rp)

	is.NoErr(r.ls.Stop(context.Background(), r.pl.ID, false))
	is.NoErr(r.ls.Wait(terminalStatusGuard))
	is.Equal(storedStatus(t, r.db, r.pl.ID), pipeline.StatusUserStopped)
}

// TestServiceLifecycle_Recovery_RunningWriteFails_RestartStaysLive is the I2
// regression test (#2898, #2899 item 1). A recovery restart runs nested on
// the failed run's cleanup goroutine. When the restarted run's StatusRunning
// write failed, the restart was rolled out of runningPipelines and orphaned,
// and the failed run's cleanup took the "recovery failed" arm: it wrote
// Degraded over the live restart and notified OnFailure, which trips
// exit-on-degraded. The restart is now the live run: it stays registered,
// reports Running, and can be stopped.
func TestServiceLifecycle_Recovery_RunningWriteFails_RestartStaysLive(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &statusFaultDB{DB: &inmemory.DB{}}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	defer stopAndWaitPersister(t, killAll, persister)

	inner := pipeline.NewService(logger, db)
	pl, err := inner.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	ctrl := gomock.NewController(t)
	source, srcDispenser := asserterSource(ctrl, persister, generateRecords(0), nil, true, 2)
	destination, destDispenser := asserterDestination(ctrl, persister, nil, 2)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, 2)
	pl.DLQ.Plugin = dlq.Plugin
	pl, err = inner.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = inner.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	// The 1st StatusRunning is the initial run, the 2nd the recovery restart.
	ps := newFailStatusWrites(inner, failNth(pipeline.StatusRunning, 2))

	cfg := testErrRecoveryCfg()
	cfg.MinDelay = time.Millisecond
	cfg.MaxDelay = time.Millisecond
	ls := NewService(logger, cfg,
		testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
		testProcessorService{},
		testConnectorPluginService{source.Plugin: srcDispenser, destination.Plugin: destDispenser, dlq.Plugin: dlqDispenser},
		ps,
	)
	failures := make(chan FailureEvent, 4)
	ls.OnFailure(func(e FailureEvent) { failures <- e })

	// Capture the restart by identity as it is published, so it can be
	// killed if a regression leaves it unreachable.
	var runs atomic.Int32
	var restart atomic.Pointer[runnablePipeline]
	ls.testBeforePublish = func(rp *runnablePipeline) {
		if runs.Add(1) == 2 {
			restart.Store(rp)
		}
	}
	defer func() {
		if rp := restart.Load(); rp != nil && rp.t != nil {
			rp.t.Kill(cerrors.FatalError(pipeline.ErrForceStop))
			select {
			case <-rp.t.Dead():
			case <-time.After(terminalStatusGuard):
			}
		}
	}()

	is.NoErr(ls.Start(ctx, pl.ID))
	first, ok := ls.runningPipelines.Get(pl.ID)
	is.True(ok)

	injectSourceError(ctx, t, ls, pl.ID, cerrors.New("lost connection to source"))
	// The failed run's cleanup returns once the restart's runPipeline returns.
	waitClosed(t, first.t.Dead(), "the pre-recovery run's cleanup to finish")
	is.Equal(ps.failedStatuses(), []pipeline.Status{pipeline.StatusRunning}) // the fault engaged

	select {
	case e := <-failures:
		t.Fatalf("OnFailure was notified for a pipeline whose restart is live: %v", e.Error)
	default:
	}
	second, ok := ls.runningPipelines.Get(pl.ID)
	if !ok {
		t.Fatal("the recovery restart is not in runningPipelines: its nodes run where Stop and StopAll cannot reach them")
	}
	is.True(second == restart.Load())
	is.True(second != first)
	is.True(second.t.Alive())
	is.Equal(pl.GetStatus(), pipeline.StatusRunning)

	is.NoErr(ls.Stop(ctx, pl.ID, false))
	is.NoErr(ls.WaitPipeline(pl.ID))
	is.Equal(pl.GetStatus(), pipeline.StatusUserStopped)
	is.Equal(storedStatus(t, db, pl.ID), pipeline.StatusUserStopped)
	is.Equal(len(failures), 0)
}

// notFoundOnRunning wraps a PipelineService and answers the StatusRunning
// write with pipeline.ErrInstanceNotFound, as if the pipeline had been
// deleted while its run was starting. Other writes pass through.
type notFoundOnRunning struct {
	PipelineService
}

func (n notFoundOnRunning) UpdateStatus(ctx context.Context, id string, status pipeline.Status, errMsg string) error {
	if status == pipeline.StatusRunning {
		return cerrors.Errorf("pipeline %s: %w", id, pipeline.ErrInstanceNotFound)
	}
	return n.PipelineService.UpdateStatus(ctx, id, status, errMsg)
}

// TestServiceLifecycle_RunningWriteNotFound_StopsRun covers R1's one
// fail-closed exception: a StatusRunning write that fails because the
// pipeline no longer exists means it was deleted under the starting run. The
// run must not keep moving data for a pipeline that is gone: it is stopped
// as a user stop, and Start still reports success because the run was live.
func TestServiceLifecycle_RunningWriteNotFound_StopsRun(t *testing.T) {
	is := is.New(t)
	never := func(pipeline.Status, int) bool { return false }
	r := newStatusFaultRun(t, &statusFaultDB{DB: &inmemory.DB{}}, never, newScriptedNode(nil, nil))
	r.ls.pipelines = notFoundOnRunning{PipelineService: r.ps}

	is.NoErr(r.ls.runPipeline(context.Background(), r.rp))
	// Nobody calls Stop: the run stops itself.
	waitClosed(t, r.rp.t.Dead(), "the run of the deleted pipeline to stop")

	is.True(r.rp.stop.requested())
	is.Equal(r.pl.GetStatus(), pipeline.StatusUserStopped)
	_, ok := r.ls.runningPipelines.Get(r.pl.ID)
	is.True(!ok)
	is.Equal(len(r.failureEvents()), 0)
}

// TestServiceLifecycle_RunningWriteHangs_StartReturnsWithinBound: a pipeline
// store that hangs on the StatusRunning write must not hold Start forever.
// runPipeline returns after the bound with the run registered and stoppable,
// whether the store gives up when the context ends (SQLite, Postgres) or
// ignores it (badger). In the second case the run cannot finish, and so Wait
// cannot return, before the hung write does: nothing writes the terminal
// status underneath it.
func TestServiceLifecycle_RunningWriteHangs_StartReturnsWithinBound(t *testing.T) {
	const bound = 50 * time.Millisecond
	for _, honourCtx := range []bool{true, false} {
		t.Run(fmt.Sprintf("honourCtx=%v", honourCtx), func(t *testing.T) {
			is := is.New(t)
			db := &statusFaultDB{DB: &inmemory.DB{}, honourCtx: honourCtx, release: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(db.release) }) }
			r := newStatusFaultRun(t, db, failNth(pipeline.StatusRunning, 1), newScriptedNode(nil, nil))
			t.Cleanup(release)
			r.ps.hang = true
			r.ls.statusWriteTimeout = bound

			started := make(chan error, 1)
			go func() { started <- r.ls.runPipeline(context.Background(), r.rp) }()
			select {
			case err := <-started:
				is.NoErr(err)
			case <-time.After(terminalStatusGuard):
				t.Fatalf("runPipeline did not return within %s while the status store hung (bound %s)", terminalStatusGuard, bound)
			}

			got, ok := r.ls.runningPipelines.Get(r.pl.ID)
			is.True(ok)
			is.True(got == r.rp)
			is.Equal(r.pl.GetStatus(), pipeline.StatusRunning)

			is.NoErr(r.ls.Stop(context.Background(), r.pl.ID, false))
			if !honourCtx {
				// The Running write is still in flight: the cleanup goroutine
				// waits for it, so the run is still live.
				is.True(r.rp.t.Alive())
				release()
			}
			is.NoErr(r.ls.Wait(terminalStatusGuard))
			is.Equal(r.pl.GetStatus(), pipeline.StatusUserStopped)
			is.Equal(storedStatus(t, r.db, r.pl.ID), pipeline.StatusUserStopped)
			is.Equal(len(r.failureEvents()), 0)
		})
	}
}

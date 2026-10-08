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
// Mirrors pkg/lifecycle's status_write_failure_test.go.
//
// The fault is injected in the database under a real pipeline.Service, so the
// in-memory status moves first and only the store write fails, exactly as it
// does in production.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database"
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

// pipelineKeyPrefix is pipeline.Store's key prefix. Only these keys are
// faulted, so position writes (same database) keep landing.
const pipelineKeyPrefix = "pipeline:instance:"

var errStatusStoreDown = cerrors.New("injected: pipeline store write failed")

type injectStatusFaultKey struct{}

type injectStatusHangKey struct{}

// statusFaultDB fails a pipeline-instance write whose context carries
// injectStatusFaultKey. One whose context carries injectStatusHangKey blocks
// until release is closed (then lands); with honourCtx it gives up when its
// context ends instead, as SQLite and Postgres would, and without it ignores
// the context, as badger does.
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
	inject := f.fail(status, f.counts[status])
	if inject {
		f.failed = append(f.failed, status)
	}
	f.mu.Unlock()

	if inject {
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

// TestServiceLifecycle_RunningWriteFails_StartSucceeds is the arch-v2 half of
// the I1 regression test (#2899 item 1). runPipeline published the run and
// registered its cleanup, but returned the failed StatusRunning write as
// Start's error, so the caller was told the start failed while the run moved
// data. The status write is a report: Start now succeeds, the run is
// reachable, and its terminal status lands when it stops.
func TestServiceLifecycle_RunningWriteFails_StartSucceeds(t *testing.T) {
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
	wantRecords := generateRecords(10)
	source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, nil, false)
	destination, destDispenser := asserterDestination(ctrl, persister, wantRecords, false)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin
	pl, err = inner.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = inner.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ps := newFailStatusWrites(inner, failNth(pipeline.StatusRunning, 1))
	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
		testProcessorService{},
		testConnectorPluginService{source.Plugin: sourceDispenser, destination.Plugin: destDispenser, dlq.Plugin: dlqDispenser},
		ps,
		false,
	)
	failures := make(chan FailureEvent, 4)
	ls.OnFailure(func(e FailureEvent) { failures <- e })
	// On a regression Start returns early; stop the live run so the test
	// fails instead of hanging in the persister drain.
	defer func() {
		if rp, ok := ls.runningPipelines.Get(pl.ID); ok && rp.t.Alive() {
			_ = ls.Stop(context.Background(), pl.ID, true)
			_ = ls.WaitPipeline(pl.ID)
		}
	}()

	is.NoErr(ls.Start(ctx, pl.ID))
	is.Equal(ps.failedStatuses(), []pipeline.Status{pipeline.StatusRunning}) // the fault engaged

	// The run moves data although its status was not persisted.
	waitForRecordsAcked(t, source, wantRecords)
	is.Equal(pl.GetStatus(), pipeline.StatusRunning)
	is.Equal(storedStatus(t, db, pl.ID), pipeline.StatusUserStopped) // the store missed it

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
// fail-closed exception in arch-v2: a pipeline deleted under its starting
// run gets that run stopped as a user stop instead of left moving data.
func TestServiceLifecycle_RunningWriteNotFound_StopsRun(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	defer stopAndWaitPersister(t, killAll, persister)

	inner := pipeline.NewService(logger, db)
	pl, err := inner.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	ctrl := gomock.NewController(t)
	source, sourceDispenser := generatorSource(ctrl, persister, nil, nil, false)
	destination, destDispenser := asserterDestination(ctrl, persister, nil, false)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin
	pl, err = inner.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = inner.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
		testProcessorService{},
		testConnectorPluginService{source.Plugin: sourceDispenser, destination.Plugin: destDispenser, dlq.Plugin: dlqDispenser},
		notFoundOnRunning{PipelineService: inner},
		false,
	)
	failures := make(chan FailureEvent, 4)
	ls.OnFailure(func(e FailureEvent) { failures <- e })
	var rp *runnablePipeline
	ls.testWorkersReleased = func(r *runnablePipeline) { rp = r }
	defer func() {
		if rp != nil && rp.t.Alive() {
			rp.t.Kill(cerrors.FatalError(pipeline.ErrForceStop))
			<-rp.t.Dead()
		}
	}()

	is.NoErr(ls.Start(ctx, pl.ID))
	is.True(rp != nil)
	// Nobody calls Stop: the run stops itself.
	select {
	case <-rp.t.Dead():
	case <-time.After(10 * time.Second):
		t.Fatal("the run of the deleted pipeline was not stopped")
	}
	is.NoErr(ls.WaitPipeline(pl.ID))
	is.Equal(pl.GetStatus(), pipeline.StatusUserStopped)
	_, ok := ls.runningPipelines.Get(pl.ID)
	is.True(!ok)
	is.Equal(len(failures), 0)
}

// TestServiceLifecycle_RunningWriteHangs_StartReturnsWithinBound is the
// arch-v2 half of pkg/lifecycle's test of the same name: a pipeline store
// that hangs on the StatusRunning write must not hold Start forever, and the
// run must stay registered and stoppable.
func TestServiceLifecycle_RunningWriteHangs_StartReturnsWithinBound(t *testing.T) {
	const bound = 50 * time.Millisecond
	for _, honourCtx := range []bool{true, false} {
		t.Run(fmt.Sprintf("honourCtx=%v", honourCtx), func(t *testing.T) {
			is := is.New(t)
			ctx, killAll := context.WithCancel(context.Background())
			defer killAll()
			logger := log.New(zerolog.Nop())
			db := &statusFaultDB{DB: &inmemory.DB{}, honourCtx: honourCtx, release: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(db.release) }) }
			persister := connector.NewPersister(logger, db, time.Second, 3)
			defer stopAndWaitPersister(t, killAll, persister)
			defer release()

			inner := pipeline.NewService(logger, db)
			pl, err := inner.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
			is.NoErr(err)

			ctrl := gomock.NewController(t)
			source, sourceDispenser := generatorSource(ctrl, persister, nil, nil, false)
			destination, destDispenser := asserterDestination(ctrl, persister, nil, false)
			dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
			pl.DLQ.Plugin = dlq.Plugin
			pl, err = inner.AddConnector(ctx, pl.ID, source.ID)
			is.NoErr(err)
			pl, err = inner.AddConnector(ctx, pl.ID, destination.ID)
			is.NoErr(err)

			ps := newFailStatusWrites(inner, failNth(pipeline.StatusRunning, 1))
			ps.hang = true
			ls := NewService(
				logger,
				testErrRecoveryCfg(),
				testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
				testProcessorService{},
				testConnectorPluginService{source.Plugin: sourceDispenser, destination.Plugin: destDispenser, dlq.Plugin: dlqDispenser},
				ps,
				false,
			)
			ls.statusWriteTimeout = bound
			rpCh := make(chan *runnablePipeline, 1)
			ls.testWorkersReleased = func(r *runnablePipeline) { rpCh <- r }

			started := make(chan error, 1)
			go func() { started <- ls.Start(ctx, pl.ID) }()
			select {
			case err := <-started:
				is.NoErr(err)
			case <-time.After(10 * time.Second):
				release() // let the run finish so the test can end
				t.Fatalf("Start did not return within 10s while the status store hung (bound %s)", bound)
			}
			rp := <-rpCh

			got, ok := ls.runningPipelines.Get(pl.ID)
			is.True(ok)
			is.True(got == rp)
			is.Equal(pl.GetStatus(), pipeline.StatusRunning)

			is.NoErr(ls.Stop(ctx, pl.ID, false))
			if !honourCtx {
				// The cleanup goroutine waits for the in-flight Running write.
				is.True(rp.t.Alive())
				release()
			}
			is.NoErr(ls.WaitPipeline(pl.ID))
			is.Equal(pl.GetStatus(), pipeline.StatusUserStopped)
			is.Equal(storedStatus(t, db, pl.ID), pipeline.StatusUserStopped)
		})
	}
}

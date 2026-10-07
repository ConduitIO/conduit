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
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/csync"
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

// failNthRunning wraps a PipelineService and fails the nth
// UpdateStatus(StatusRunning) call without applying it, so a test can make a
// specific run's "go live" announcement fail.
type failNthRunning struct {
	PipelineService
	n     int64
	err   error
	count atomic.Int64
}

func (f *failNthRunning) UpdateStatus(ctx context.Context, id string, status pipeline.Status, errMsg string) error {
	if status == pipeline.StatusRunning && f.count.Add(1) == f.n {
		return f.err
	}
	return f.PipelineService.UpdateStatus(ctx, id, status, errMsg)
}

// TestServiceLifecycle_Recovery_SupersededCleanupKeepsLiveEntry is the #2811
// regression test.
//
// A recovery restart runs nested on the failed run's cleanup goroutine:
// cleanup(rp1) -> recoverPipeline -> StartWithBackoff -> Start ->
// runPipeline(rp2). runPipeline publishes rp2 and releases its workers BEFORE
// announcing StatusRunning, and on that announcement failing it returns the
// error with rp2 still live. The error unwinds into rp1's cleanup, which takes
// the recovery-failed arm and falls through to its terminal block. That block
// used to call runningPipelines.Delete(id) — by key — erasing rp2's entry
// while rp2's workers were running: unreachable by Stop, StopAll, Wait and
// WaitPipeline, and WaitPipeline answering with rp1's stale terminal error.
//
// The test drives that exact interleaving deterministically: the second
// StatusRunning write (the recovery restart's) fails by construction, and the
// FailureHandler — which notify() calls only after the terminal block's
// delete — is the synchronization point. No sleeps.
func TestServiceLifecycle_Recovery_SupersededCleanupKeepsLiveEntry(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	defer stopAndWaitPersister(t, killAll, persister)

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	transientErr := cerrors.New("lost connection to source")
	announceErr := cerrors.New("injected: could not persist StatusRunning")
	// Zero records for the recovered run: its worker is released and then
	// parks on the mocked stream, so it stays alive until the test kills it.
	noRecords := generateRecords(0)

	ctrl := gomock.NewController(t)
	source, srcDispenser := sourceRecoversAfterTransientError(ctrl, persister, noRecords, transientErr)
	destination, destDispenser := destinationRecovers(ctrl, persister, noRecords)
	dlq, dlqDispenser := dlqDispenserTimes(ctrl, persister, 2)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	// The 1st StatusRunning is the initial run; the 2nd is the nested
	// recovery restart, which is the one that must fail.
	failing := &failNthRunning{PipelineService: ps, n: 2, err: announceErr}

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		testConnectorService{
			source.ID:      source,
			destination.ID: destination,
			testDLQID:      dlq,
		},
		testProcessorService{},
		testConnectorPluginService{
			source.Plugin:      srcDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		},
		failing,
		false,
	)

	// Capture the recovery restart's run (the 2nd runPipeline) by identity.
	var runCount atomic.Int64
	var liveRp atomic.Pointer[runnablePipeline]
	ls.testWorkersReleased = func(rp *runnablePipeline) {
		if runCount.Add(1) == 2 {
			liveRp.Store(rp)
		}
	}

	// notify() runs after rp1's terminal block (terminalErrors.Set + the
	// runningPipelines removal), so receiving here means that block is done.
	failures := make(chan FailureEvent, 4)
	ls.OnFailure(func(e FailureEvent) { failures <- e })

	is.NoErr(ls.Start(ctx, pl.ID))
	deadRp, ok := ls.runningPipelines.Get(pl.ID)
	is.True(ok)

	var outer FailureEvent
	select {
	case outer = <-failures:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the superseded run's cleanup to finish")
	}
	is.True(cerrors.Is(outer.Error, announceErr)) // rp1's recovery failed because rp2 could not announce
	is.True(cerrors.Is(deadRp.t.Err(), transientErr))

	rp2 := liveRp.Load()
	is.True(rp2 != nil)
	is.True(rp2 != deadRp)
	// Safety net, registered before the assertion so a pre-fix failure does
	// not leave rp2's worker parked until stopAndWaitPersister times out.
	defer rp2.t.Kill(cerrors.FatalError(pipeline.ErrForceStop))
	is.True(rp2.t.Alive()) // sanity: rp2 is really running

	// The regression: rp1's cleanup must not have erased rp2's entry.
	current, ok := ls.runningPipelines.Get(pl.ID)
	if !ok || current != rp2 {
		t.Fatalf(
			"runningPipelines[%s] no longer holds the live recovered run after the superseded "+
				"run's cleanup finished (present=%v, isLive=%v): Stop/StopAll/Wait/WaitPipeline "+
				"cannot reach a run whose workers are still running (#2811)",
			pl.ID, ok, current == rp2,
		)
	}

	// Behavioural half: WaitPipeline must reach rp2 and report ITS terminal
	// error, not rp1's stale one recorded in terminalErrors.
	rp2.t.Kill(cerrors.FatalError(pipeline.ErrForceStop))
	err = ls.WaitPipeline(pl.ID)
	is.True(cerrors.Is(err, pipeline.ErrForceStop))
	is.True(!cerrors.Is(err, announceErr))

	// rp2's own cleanup runs to completion and removes its own entry.
	select {
	case e := <-failures:
		is.True(cerrors.Is(e.Error, pipeline.ErrForceStop))
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the recovered run's cleanup to finish")
	}
	_, ok = ls.runningPipelines.Get(pl.ID)
	is.True(!ok)
	is.Equal(pipeline.StatusDegraded, pl.GetStatus())
}

// TestService_deleteRunningPipelineIfCurrent pins the compare-and-delete
// contract directly: a stale owner never removes a different run's entry.
func TestService_deleteRunningPipelineIfCurrent(t *testing.T) {
	is := is.New(t)
	s := &Service{runningPipelines: csync.NewMap[string, *runnablePipeline]()}

	older := &runnablePipeline{}
	newer := &runnablePipeline{}

	s.publishRunningPipeline("p", newer)
	s.deleteRunningPipelineIfCurrent("p", older) // stale owner: no-op
	got, ok := s.runningPipelines.Get("p")
	is.True(ok)
	is.True(got == newer)

	s.deleteRunningPipelineIfCurrent("p", newer) // current owner: removes
	_, ok = s.runningPipelines.Get("p")
	is.True(!ok)

	s.deleteRunningPipelineIfCurrent("missing", older) // absent key: no-op, no panic
}

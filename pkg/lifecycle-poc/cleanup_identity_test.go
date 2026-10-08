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

// TestServiceLifecycle_Recovery_SupersededCleanupKeepsLiveEntry is the #2811
// regression test, updated for #2899 item 1.
//
// A recovery restart runs nested on the failed run's cleanup goroutine:
// cleanup(rp1) -> recoverPipeline -> StartWithBackoff -> Start ->
// runPipeline(rp2). runPipeline publishes rp2 and releases its workers before
// announcing StatusRunning. When that announcement failed, Start used to
// return the error with rp2 live; rp1's cleanup then took the recovery-failed
// arm, wrote Degraded over the live rp2 and notified OnFailure (which trips
// exit-on-degraded). Before #2811 its terminal block also erased rp2's entry.
//
// A failed status write is now a report: Start succeeds, rp1's cleanup sees a
// successful restart and leaves rp2 alone. The test drives the same
// interleaving deterministically: the second StatusRunning store write (the
// restart's) fails by construction, after pipeline.Service has already moved
// the in-memory status, as a real store failure does.
func TestServiceLifecycle_Recovery_SupersededCleanupKeepsLiveEntry(t *testing.T) {
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

	transientErr := cerrors.New("lost connection to source")
	// Zero records for the recovered run: its worker is released and then
	// parks on the mocked stream, so it stays alive until the test stops it.
	noRecords := generateRecords(0)

	ctrl := gomock.NewController(t)
	source, srcDispenser := sourceRecoversAfterTransientError(ctrl, persister, noRecords, transientErr)
	destination, destDispenser := destinationRecovers(ctrl, persister, noRecords)
	dlq, dlqDispenser := dlqDispenserTimes(ctrl, persister, 2)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = inner.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = inner.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	// The 1st StatusRunning is the initial run; the 2nd is the nested
	// recovery restart, which is the one whose store write fails.
	failing := newFailStatusWrites(inner, failNth(pipeline.StatusRunning, 2))

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

	// Capture both runs by identity from inside runPipeline: the 1st call is
	// the initial run (rp1), the 2nd the recovery restart (rp2). rp1 must not
	// be read from runningPipelines after Start returns: its transient
	// failure and the 1ms backoff restart may already have replaced it.
	var runCount atomic.Int64
	var deadRpPtr, liveRp atomic.Pointer[runnablePipeline]
	ls.testWorkersReleased = func(rp *runnablePipeline) {
		switch runCount.Add(1) {
		case 1:
			deadRpPtr.Store(rp)
		case 2:
			liveRp.Store(rp)
		}
	}
	// Safety net: a regression can leave rp2 live where nothing stops it.
	defer func() {
		if rp := liveRp.Load(); rp != nil {
			rp.t.Kill(cerrors.FatalError(pipeline.ErrForceStop))
			select {
			case <-rp.t.Dead():
			case <-time.After(5 * time.Second):
			}
		}
	}()

	failures := make(chan FailureEvent, 4)
	ls.OnFailure(func(e FailureEvent) { failures <- e })

	is.NoErr(ls.Start(ctx, pl.ID))
	deadRp := deadRpPtr.Load()
	is.True(deadRp != nil)

	// rp1's cleanup goroutine runs the restart synchronously, so rp1's tomb
	// is dead once the restart's runPipeline has returned and rp1's cleanup
	// has finished with it.
	select {
	case <-deadRp.t.Dead():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the superseded run's cleanup to finish")
	}
	is.True(cerrors.Is(deadRp.t.Err(), transientErr))
	is.Equal(failing.failedStatuses(), []pipeline.Status{pipeline.StatusRunning}) // the fault engaged

	select {
	case e := <-failures:
		t.Fatalf("OnFailure was notified for a pipeline whose restart is live: %v", e.Error)
	default:
	}

	rp2 := liveRp.Load()
	is.True(rp2 != nil)
	is.True(rp2 != deadRp)
	is.True(rp2.t.Alive()) // sanity: rp2 is really running

	current, ok := ls.runningPipelines.Get(pl.ID)
	if !ok || current != rp2 {
		t.Fatalf(
			"runningPipelines[%s] no longer holds the live recovered run after the superseded "+
				"run's cleanup finished (present=%v, isLive=%v): Stop/StopAll/Wait/WaitPipeline "+
				"cannot reach a run whose workers are still running (#2811)",
			pl.ID, ok, current == rp2,
		)
	}
	// The restart is live, so the pipeline reports Running, not Degraded.
	is.Equal(pipeline.StatusRunning, pl.GetStatus())

	// rp2 is an ordinary live run: Stop reaches it and WaitPipeline reports
	// its own result, not rp1's error.
	is.NoErr(ls.Stop(ctx, pl.ID, false))
	is.NoErr(ls.WaitPipeline(pl.ID))
	_, ok = ls.runningPipelines.Get(pl.ID)
	is.True(!ok)
	is.Equal(pipeline.StatusUserStopped, pl.GetStatus())
	is.Equal(pipeline.StatusUserStopped, storedStatus(t, db, pl.ID))
	is.Equal(len(failures), 0)
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

// TestService_deleteRunningPipelineIfCurrent_SerializedAgainstPublish proves
// publishMu makes the compare-and-delete one step with respect to a
// publication (#2811). Without it the check is a TOCTOU that needs no
// recovery chain: an operator Stop finalizes StatusUserStopped, which admits a
// concurrent Start; if that Start's publish lands between the departing run's
// identity check and its Delete, the departing run erases the new live run.
//
// testCompareAndDeleteWindow holds the delete inside that window while a
// concurrent publish is attempted. Two checks, so removing the lock from
// either side fails:
//   - inside the window publishMu must be held (deterministic: TryLock fails);
//   - the concurrent publish must survive the delete. With the lock it cannot
//     land until the delete finishes, so the window waits out a bounded grace
//     period for it and then proceeds. The grace period never decides the
//     pass condition; it only gives an unserialized publish time to land in
//     the window and be erased.
func TestService_deleteRunningPipelineIfCurrent_SerializedAgainstPublish(t *testing.T) {
	is := is.New(t)
	s := &Service{runningPipelines: csync.NewMap[string, *runnablePipeline]()}

	departing := &runnablePipeline{}
	newer := &runnablePipeline{}
	s.publishRunningPipeline("p", departing)

	inWindow := make(chan struct{})
	published := make(chan struct{})
	// Written and read only on the test goroutine: the hook runs
	// synchronously inside deleteRunningPipelineIfCurrent below.
	lockHeldInWindow := false
	s.testCompareAndDeleteWindow = func() {
		if s.publishMu.TryLock() {
			s.publishMu.Unlock()
		} else {
			lockHeldInWindow = true
		}
		close(inWindow)
		select {
		case <-published:
		case <-time.After(200 * time.Millisecond):
		}
	}

	go func() {
		<-inWindow
		s.publishRunningPipeline("p", newer)
		close(published)
	}()

	s.deleteRunningPipelineIfCurrent("p", departing)

	select {
	case <-published:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent publish never completed")
	}

	is.True(lockHeldInWindow) // the compare-and-delete must run under publishMu
	got, ok := s.runningPipelines.Get("p")
	if !ok || got != newer {
		t.Fatalf("a publish that raced the departing run's compare-and-delete was erased "+
			"(present=%v, isNewer=%v): the newer live run is unreachable (#2811)", ok, got == newer)
	}
}

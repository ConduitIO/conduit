// Copyright © 2022 Meroxa, Inc.
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
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/cchan"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	lifecyclev1 "github.com/conduitio/conduit/pkg/lifecycle"
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/plugin"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

const testDLQID = "test-dlq"

func TestServiceLifecycle_buildRunnablePipeline(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	ctrl := gomock.NewController(t)
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	source := dummySource(persister)
	destination := dummyDestination(persister)
	dlq := dummyDestination(persister)
	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Config: pipeline.Config{Name: "test-pipeline"},
		DLQ: pipeline.DLQ{
			Plugin:              dlq.Plugin,
			Settings:            map[string]string{},
			WindowSize:          3,
			WindowNackThreshold: 2,
		},
		ConnectorIDs: []string{source.ID, destination.ID},
	}
	pl.SetStatus(pipeline.StatusUserStopped)

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
			source.Plugin:      pmock.NewDispenser(ctrl),
			destination.Plugin: pmock.NewDispenser(ctrl),
			dlq.Plugin:         pmock.NewDispenser(ctrl),
		},
		testPipelineService{},
		false,
	)

	got, err := ls.buildRunnablePipeline(
		ctx,
		pl,
	)

	is.NoErr(err)

	is.Equal("", cmp.Diff(pl, got.pipeline, cmpopts.IgnoreUnexported(pipeline.Instance{})))

	wantTasks := []funnel.Task{
		&funnel.SourceTask{},
		&funnel.DestinationTask{},
	}
	i := 0
	for got := range got.w.FirstTask.Tasks() {
		want := wantTasks[i]
		is.Equal(reflect.TypeOf(want), reflect.TypeOf(got)) // unexpected task type
		i++
	}
	is.Equal(got.w.Source.(*connector.Source).Instance, source)
}

func TestService_buildRunnablePipeline_NoSourceNode(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	ctrl := gomock.NewController(t)
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	destination := dummyDestination(persister)
	dlq := dummyDestination(persister)
	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Config: pipeline.Config{Name: "test-pipeline"},
		DLQ: pipeline.DLQ{
			Plugin:              dlq.Plugin,
			Settings:            map[string]string{},
			WindowSize:          3,
			WindowNackThreshold: 2,
		},
		ConnectorIDs: []string{destination.ID},
	}
	pl.SetStatus(pipeline.StatusUserStopped)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		testConnectorService{
			destination.ID: destination,
			testDLQID:      dlq,
		},
		testProcessorService{},
		testConnectorPluginService{
			destination.Plugin: pmock.NewDispenser(ctrl),
			dlq.Plugin:         pmock.NewDispenser(ctrl),
		},
		testPipelineService{},
		false,
	)

	wantErr := "can't build pipeline without any source connectors"

	got, err := ls.buildRunnablePipeline(
		ctx,
		pl,
	)

	is.True(err != nil)
	is.Equal(err.Error(), wantErr)
	is.Equal(got, nil)
}

func TestService_buildRunnablePipeline_NoDestinationNode(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	ctrl := gomock.NewController(t)
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	source := dummySource(persister)
	dlq := dummyDestination(persister)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		testConnectorService{
			source.ID: source,
			testDLQID: dlq,
		},
		testProcessorService{},
		testConnectorPluginService{
			source.Plugin: pmock.NewDispenser(ctrl),
			dlq.Plugin:    pmock.NewDispenser(ctrl),
		},
		testPipelineService{},
		false,
	)

	wantErr := "can't build pipeline without any destination connectors"

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Config: pipeline.Config{Name: "test-pipeline"},
		DLQ: pipeline.DLQ{
			Plugin:              dlq.Plugin,
			Settings:            map[string]string{},
			WindowSize:          3,
			WindowNackThreshold: 2,
		},
		ConnectorIDs: []string{source.ID},
	}
	pl.SetStatus(pipeline.StatusUserStopped)

	got, err := ls.buildRunnablePipeline(
		ctx,
		pl,
	)

	is.True(err != nil)
	is.Equal(err.Error(), wantErr)
	is.Equal(got, nil)
}

func TestServiceLifecycle_PipelineSuccess(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	defer persister.Wait()

	ps := pipeline.NewService(logger, db)

	// create a host pipeline
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	// create mocked connectors
	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(10)
	source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, nil, false)
	destination, destDispenser := asserterDestination(ctrl, persister, wantRecords, false)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

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
			source.Plugin:      sourceDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		},
		ps,
		false,
	)

	// start the pipeline now that everything is set up
	err = ls.Start(
		ctx,
		pl.ID,
	)
	is.NoErr(err)

	// Wait for the source to have acked every record before stopping: stopping
	// earlier races Worker.Stop against the in-flight batch loop and can leave
	// some of the 10 records undelivered, which fails the asserterDestination /
	// SourcePluginWithAcks mock expectations in t.Cleanup with an unrelated-looking
	// error. See waitForRecordsAcked.
	waitForRecordsAcked(t, source, wantRecords)

	is.Equal(pipeline.StatusRunning, pl.GetStatus())
	is.Equal("", pl.Error)

	// stop pipeline before ending test
	err = ls.Stop(ctx, pl.ID, false)
	is.NoErr(err)

	is.NoErr(ls.WaitPipeline(pl.ID))
}

func TestServiceLifecycle_PipelineError(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.Test(t)
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	// Without this, the persister's background flush timer can fire after this
	// test function returns and log via logger (log.Test(t), backed by t.Log),
	// which panics ("Log in goroutine after ... has completed") and can corrupt
	// unrelated concurrently-scheduled tests in the same process. Confirmed by
	// repro under `-race -shuffle=on -count=1500` under CPU load; the race
	// detector flags the underlying unsynchronized access to testing.T state.
	defer persister.Wait()

	ps := pipeline.NewService(logger, db)

	// create a host pipeline
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	// create mocked connectors. The source error is fatal so the pipeline degrades
	// immediately with it, instead of relying on the (unimplemented, TODO) recovery
	// path — this isolates what #1659 is about: the *cause* reported for the
	// degraded pipeline must be the source's real error, not the io.EOF the acker
	// sees when the closed source stream rejects an ack.
	//
	// The source's Teardown IS expected here (see generatorSourceFatalError):
	// even on the fatal-error path, where Worker.Stop is never called, Worker.Close
	// tears the source down via the idempotent Worker.tearDownSource (#2559). This
	// test therefore also guards against regressing that cleanup — without it,
	// Teardown is never called and the mock expectation fails.
	wantErr := cerrors.FatalError(cerrors.New("source connector error"))
	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(10)
	source, sourceDispenser := generatorSourceFatalError(ctrl, persister, wantRecords, wantErr)
	destination, destDispenser := asserterDestination(ctrl, persister, wantRecords, false)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

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
			source.Plugin:      sourceDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		},
		ps,
		false,
	)

	events := make(chan FailureEvent, 1)
	ls.OnFailure(func(e FailureEvent) {
		events <- e
	})

	// start the pipeline now that everything is set up
	err = ls.Start(
		ctx,
		pl.ID,
	)
	is.NoErr(err)

	// wait for pipeline to finish
	err = ls.WaitPipeline(pl.ID)
	is.True(err != nil)

	is.Equal(pipeline.StatusDegraded, pl.GetStatus())
	// pipeline errors contain only string messages, so we can only compare the errors by the messages
	t.Log(pl.Error)

	event, eventReceived, err := cchan.Chan[FailureEvent](events).RecvTimeout(ctx, 200*time.Millisecond)
	is.NoErr(err)
	is.True(eventReceived)
	is.Equal(pl.ID, event.ID)

	// With #1659 fixed, the degraded pipeline reports the source's real error, not
	// the io.EOF the acker gets from the closed stream. Unlike the tomb-node arch
	// (pkg/lifecycle), the funnel model reports failures as
	// "worker stopped with error: task <id>: failed to read from source: <cause>",
	// not "node <id> stopped with error".
	is.True( // error message attributes the failure to reading from the source
		strings.Contains(pl.Error, "failed to read from source:"),
	)
	is.True( // and carries the real cause
		strings.Contains(pl.Error, wantErr.Error()),
	)
	is.True(cerrors.Is(event.Error, wantErr))
}

func TestServiceLifecycle_PipelineStop(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	// See the comment on the equivalent defer in TestServiceLifecycle_PipelineError:
	// without waiting, the persister's background flush goroutine can outlive this
	// test function (goroutine leak); harmless here since the logger is a no-op and
	// db is test-local, but kept consistent with the other tests in this file.
	defer persister.Wait()

	ps := pipeline.NewService(logger, db)

	// create a host pipeline
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	// create mocked connectors
	// source will stop and return ErrGracefulShutdown which should signal to the
	// service that everything went well and the pipeline was gracefully shutdown
	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(10)
	source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, nil, false)
	destination, destDispenser := asserterDestination(ctrl, persister, wantRecords, false)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

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
			source.Plugin:      sourceDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		},
		ps,
		false,
	)

	// start the pipeline now that everything is set up
	err = ls.Start(
		ctx,
		pl.ID,
	)
	is.NoErr(err)

	// Wait for the source to have acked every record before stopping: stopping
	// earlier races Worker.Stop against the in-flight batch loop and can leave
	// some of the 10 records undelivered, which fails the asserterDestination /
	// SourcePluginWithAcks mock expectations in t.Cleanup with an unrelated-looking
	// error. See waitForRecordsAcked.
	waitForRecordsAcked(t, source, wantRecords)
	err = ls.StopAll(ctx, false)
	is.NoErr(err)

	// wait for pipeline to finish
	err = ls.WaitPipeline(pl.ID)
	is.NoErr(err)

	is.Equal(pipeline.StatusSystemStopped, pl.GetStatus())
	is.Equal("", pl.Error)
}

// TestServiceLifecycle_PipelineForceStop is the regression test for the v2
// force-stop fatal-tagging bug (arch-v2 recovery-port prerequisite). A forceful
// Stop must kill the pipeline tomb with a *fatal* error so the cleanup
// goroutine's `cerrors.IsFatalError(rp.t.Err())` classification treats it as
// terminal. Before the fix, v2 force-stopped with a plain (non-fatal)
// pipeline.ErrForceStop; harmless while recovery is disabled, but once the v1
// recovery loop is ported, a non-fatal force-stop would be classified transient
// and auto-restarted — restarting a pipeline the user explicitly force-stopped
// (violates the design-doc "force-stop is not recovered" acceptance criterion).
//
// This test asserts the property the port depends on: the terminal error is
// fatal-tagged and the pipeline lands in StatusDegraded. It fails without the
// fatal tag (IsFatalError is false and status stays Running, since the non-fatal
// arm writes no status), and passes with it. Mirrors v1's forceful-stop case in
// pkg/lifecycle/service_test.go (TestServiceLifecycle_Stop).
func TestServiceLifecycle_PipelineForceStop(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	// See TestServiceLifecycle_PipelineError's defer: prevents the persister's
	// background flush goroutine from outliving the test.
	defer persister.Wait()

	ps := pipeline.NewService(logger, db)

	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	// Zero records (mirrors v1's forceful-stop case): a force-stop cuts the tomb
	// immediately, so the number of acks that reach the source plugin before the
	// stream is torn down is inherently nondeterministic — asserting a nonzero ack
	// count would flake. With no records there is nothing to ack, the source Run
	// blocks (keeping the pipeline in StatusRunning so the force Stop is accepted),
	// and force-stop exercises exactly the tomb-kill classification we care about.
	// stop=false: a force-stop kills the tomb directly (Worker.Stop is never called
	// gracefully), so the source plugin gets no Stop call — only Teardown via
	// Worker.Close.
	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(0)
	source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, nil, false)
	destination, destDispenser := asserterDestination(ctrl, persister, wantRecords, false)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

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
			source.Plugin:      sourceDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		},
		ps,
		false,
	)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	// Wait until the pipeline is fully Running before force-stopping. Force-stopping
	// mid-startup is a separate, independently-tracked robustness concern (see the
	// equivalent note in v1's tests) and not what this regression test exercises;
	// Stop also rejects any status other than Running/Recovering.
	waitForPipelineRunning(t, pl)

	err = ls.Stop(ctx, pl.ID, true)
	is.NoErr(err)

	err = ls.WaitPipeline(pl.ID)
	is.True(err != nil)

	// The property the recovery port depends on: a user force-stop is fatal, so
	// the classification the port will branch on (IsFatalError) is true and the
	// pipeline is never eligible for auto-recovery. This assertion fails without
	// the fatal tag on the force-stop kill.
	is.True(cerrors.IsFatalError(err))
	is.True(cerrors.Is(err, pipeline.ErrForceStop))
	// Fatal terminal error routes to StatusDegraded (the fatal arm of the cleanup
	// switch), matching v1. Without the fatal tag, the non-fatal arm writes no
	// status and the pipeline would remain StatusRunning.
	is.Equal(pipeline.StatusDegraded, pl.GetStatus())
}

// TestServiceLifecycle_Recovery_TransientErrorRecovers is the core arch-v2
// recovery-port acceptance test (design-doc path: running → recovering →
// running). A running v2 pipeline whose source returns a transient (non-fatal)
// error must auto-restart with backoff and return to running WITHOUT operator
// action, then process records on the recovered run.
//
// It also pins the status-transition fidelity (AC-7): the recorded UpdateStatus
// sequence is exactly [Running, Recovering, Running, UserStopped] — the initial
// run, the recovery entry, the recovered run, and the final graceful stop.
func TestServiceLifecycle_Recovery_TransientErrorRecovers(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	defer persister.Wait()

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	// A non-fatal error drives recovery; the second (recovered) run delivers
	// these records end-to-end.
	transientErr := cerrors.New("lost connection to source")
	healthyRecords := generateRecords(3)

	ctrl := gomock.NewController(t)
	source, srcDispenser := sourceRecoversAfterTransientError(ctrl, persister, healthyRecords, transientErr)
	destination, destDispenser := destinationRecovers(ctrl, persister, healthyRecords)
	dlq, dlqDispenser := dlqDispenserTimes(ctrl, persister, 2)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	rec := newStatusRecorder(ps)
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
		rec,
		false,
	)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	// Wait until the pipeline has recovered: it must have passed through
	// Recovering and be Running again. (The initial run also briefly reports
	// Running, so "Running after a Recovering" is what distinguishes recovery.)
	waitForRecovered(t, rec)

	// The recovered run delivers its records end-to-end; wait for the source to
	// ack them so the graceful stop below has a deterministic last position.
	waitForRecordsAcked(t, source, healthyRecords)
	is.Equal(pipeline.StatusRunning, pl.GetStatus())

	err = ls.Stop(ctx, pl.ID, false)
	is.NoErr(err)
	is.NoErr(ls.WaitPipeline(pl.ID))
	is.Equal(pipeline.StatusUserStopped, pl.GetStatus())

	// AC-7: exact status-transition sequence for the recovered-then-stopped path.
	is.Equal(rec.snapshot(), []pipeline.Status{
		pipeline.StatusRunning,
		pipeline.StatusRecovering,
		pipeline.StatusRunning,
		pipeline.StatusUserStopped,
	})
}

// TestServiceLifecycle_Recovery_MaxRetriesExhausted proves the bounded-retry
// path (design-doc path: running → recovering → degraded). With a finite
// MaxRetries and a source that fails on every run, the pipeline attempts exactly
// MaxRetries restarts and then degrades with a fatal ErrPipelineCannotRecover —
// it does NOT loop forever. This also guards the §2.2(3) backoff-carry-over: if
// recoveryAttempts reset on each restart, MaxRetries would never bite and the
// source would be dispensed indefinitely (the Times(k+1) expectation would fail).
func TestServiceLifecycle_Recovery_MaxRetriesExhausted(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	defer persister.Wait()

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	const maxRetries = 2
	// Initial run + maxRetries restarts = maxRetries+1 dispenses, then degrade.
	const wantDispenses = maxRetries + 1

	transientErr := cerrors.New("source keeps flapping")
	ctrl := gomock.NewController(t)
	source, srcDispenser := failingSourceTimes(ctrl, persister, transientErr, wantDispenses)
	destination, destDispenser := destinationTimes(ctrl, persister, wantDispenses)
	dlq, dlqDispenser := dlqDispenserTimes(ctrl, persister, wantDispenses)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	cfg := testErrRecoveryCfg()
	cfg.MaxRetries = maxRetries

	rec := newStatusRecorder(ps)
	ls := NewService(
		logger,
		cfg,
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
		rec,
		false,
	)

	events := make(chan FailureEvent, 1)
	ls.OnFailure(func(e FailureEvent) { events <- e })

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	// The terminal fatal error is delivered via the Failurehandler (and recorded
	// in terminalErrors), not necessarily by WaitPipeline: WaitPipeline can latch
	// onto an intermediate run's tomb, which carries that run's (non-fatal)
	// transient error, not the final exhaustion error. The FailureEvent is only
	// emitted on the terminal degrade, so it carries the fatal cause.
	event, received, err := cchan.Chan[FailureEvent](events).RecvTimeout(ctx, 10*time.Second)
	is.NoErr(err)
	is.True(received)
	is.Equal(pl.ID, event.ID)
	is.True(cerrors.IsFatalError(event.Error))                          // exhaustion is terminal
	is.True(cerrors.Is(event.Error, pipeline.ErrPipelineCannotRecover)) // with the recovery-failed sentinel

	waitForStatus(t, pl, pipeline.StatusDegraded)

	// The recorded transitions: an initial Running, then a Recovering per restart
	// attempt (with an interleaved Running for each successful re-dispense), and a
	// terminal Degraded. Assert the shape without over-fitting the interleaving:
	// Recovering appears, and the terminal status is Degraded.
	statuses := rec.snapshot()
	is.True(len(statuses) >= 2)
	is.Equal(statuses[len(statuses)-1], pipeline.StatusDegraded)
	var recovering int
	for _, s := range statuses {
		if s == pipeline.StatusRecovering {
			recovering++
		}
	}
	is.Equal(recovering, wantDispenses) // one Recovering per recovery entry (incl. the exhausting one)
}

// TestServiceLifecycle_Recovery_GracefulShutdownDuringBackoff proves invariant 7
// under recovery: a graceful shutdown (StopAll) issued while the pipeline is
// parked in the recovery backoff wait must NOT restart it. The pipeline finalizes
// as StatusSystemStopped (a clean stop, not a degraded failure) and the source is
// dispensed exactly once — no recovery restart races the shutdown.
func TestServiceLifecycle_Recovery_GracefulShutdownDuringBackoff(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	defer persister.Wait()

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	transientErr := cerrors.New("lost connection to source")
	ctrl := gomock.NewController(t)
	// Exactly one dispense: the initial run. A recovery restart would be a second
	// dispense and fail the Times(1) expectation — precisely the bug this guards.
	source, srcDispenser := failingSourceTimes(ctrl, persister, transientErr, 1)
	destination, destDispenser := destinationTimes(ctrl, persister, 1)
	dlq, dlqDispenser := dlqDispenserTimes(ctrl, persister, 1)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	// A long MinDelay makes the backoff wait wide enough to reliably observe the
	// Recovering state and issue StopAll before the wait elapses.
	cfg := testErrRecoveryCfg()
	cfg.MinDelay = 500 * time.Millisecond
	cfg.MaxDelay = 500 * time.Millisecond

	rec := newStatusRecorder(ps)
	ls := NewService(
		logger,
		cfg,
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
		rec,
		false,
	)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	// Wait until the pipeline is parked in the backoff wait (StatusRecovering),
	// then trigger a graceful shutdown mid-wait.
	waitForStatus(t, pl, pipeline.StatusRecovering)
	err = ls.StopAll(ctx, false)
	is.NoErr(err)

	// It finalizes as a clean system stop and is never restarted. StatusSystemStopped
	// is written only by the sentinel (graceful-shutdown-during-backoff) path, so
	// observing it proves the recovery was aborted, not completed. The source is
	// dispensed exactly once (Times(1)); a restart would be a second dispense and
	// fail at controller finish.
	waitForStatus(t, pl, pipeline.StatusSystemStopped)
	// Drain the pipeline's goroutines before the persister.Wait/ctrl.Finish
	// deferred cleanups. WaitPipeline's return is intentionally not asserted: it
	// races the cleanup's runningPipelines delete and surfaces either the run's
	// transient tomb error or nil — the status assertion above is the invariant.
	_ = ls.WaitPipeline(pl.ID)

	statuses := rec.snapshot()
	is.Equal(statuses[len(statuses)-1], pipeline.StatusSystemStopped)
}

// statusRecorder wraps a PipelineService, recording every UpdateStatus target
// status in order so a test can assert the transition sequence. UpdateStatus is
// called from multiple goroutines (the initial run and the cleanup/recovery
// goroutine), so the slice is mutex-guarded.
type statusRecorder struct {
	PipelineService
	mu       sync.Mutex
	statuses []pipeline.Status
}

func newStatusRecorder(inner PipelineService) *statusRecorder {
	return &statusRecorder{PipelineService: inner}
}

func (r *statusRecorder) UpdateStatus(ctx context.Context, id string, status pipeline.Status, errMsg string) error {
	r.mu.Lock()
	r.statuses = append(r.statuses, status)
	r.mu.Unlock()
	return r.PipelineService.UpdateStatus(ctx, id, status, errMsg)
}

func (r *statusRecorder) snapshot() []pipeline.Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]pipeline.Status, len(r.statuses))
	copy(out, r.statuses)
	return out
}

// waitForRecovered blocks until the recorded status sequence shows a recovery:
// a Recovering entry followed by a later Running. Fails the test on timeout.
func waitForRecovered(t *testing.T, rec *statusRecorder) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		statuses := rec.snapshot()
		seenRecovering := false
		for _, s := range statuses {
			switch {
			case s == pipeline.StatusRecovering:
				seenRecovering = true
			case s == pipeline.StatusRunning && seenRecovering:
				return // Running after a Recovering == recovered
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for pipeline to recover (statuses: %v)", statuses)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitForStatus blocks until the pipeline instance reports the given status,
// failing the test on timeout. Used to catch a transient status (Recovering)
// during the backoff wait.
func waitForStatus(t *testing.T, pl *pipeline.Instance, want pipeline.Status) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if pl.GetStatus() == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for pipeline status %s (last: %s)", want, pl.GetStatus())
		}
		time.Sleep(time.Millisecond)
	}
}

// sourceRecoversAfterTransientError builds a source whose first dispensed run
// emits no records and fails with transientErr (driving exactly one recovery),
// and whose second (recovered) run emits healthyRecords and can be gracefully
// stopped. Exactly two dispenses are expected: the initial run and the recovery
// restart.
func sourceRecoversAfterTransientError(
	ctrl *gomock.Controller,
	persister *connector.Persister,
	healthyRecords []opencdc.Record,
	transientErr error,
) (*connector.Instance, *pmock.Dispenser) {
	source := dummySource(persister)
	var call atomic.Int64
	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseSource().DoAndReturn(func() (connectorPlugin.SourcePlugin, error) {
		if call.Add(1) == 1 {
			// First run: fail transiently, no graceful Stop (the error path only
			// tears the source down via Worker.Close).
			return pmock.NewConfigurableSourcePlugin(ctrl,
				pmock.SourcePluginWithConfigure(),
				pmock.SourcePluginWithOpen(),
				pmock.SourcePluginWithRun(),
				pmock.SourcePluginWithRecords(nil, transientErr),
				pmock.SourcePluginWithAcks(0, false),
				pmock.SourcePluginWithTeardown(),
			), nil
		}
		// Recovered run: emit records, stay running until stopped. The funnel's
		// graceful stop tears the source down (Worker.Close → Source.Teardown)
		// without sending the plugin Stop signal, so no SourcePluginWithStop here.
		return pmock.NewConfigurableSourcePlugin(ctrl,
			pmock.SourcePluginWithConfigure(),
			pmock.SourcePluginWithOpen(),
			pmock.SourcePluginWithRun(),
			pmock.SourcePluginWithRecords(healthyRecords, nil),
			pmock.SourcePluginWithAcks(len(healthyRecords), false),
			pmock.SourcePluginWithTeardown(),
		), nil
	}).Times(2)
	return source, dispenser
}

// destinationRecovers mirrors sourceRecoversAfterTransientError on the
// destination side: the first dispensed run receives no records and is only torn
// down (error path), the second receives healthyRecords and is gracefully
// stopped.
func destinationRecovers(
	ctrl *gomock.Controller,
	persister *connector.Persister,
	healthyRecords []opencdc.Record,
) (*connector.Instance, *pmock.Dispenser) {
	dest := dummyDestination(persister)
	var call atomic.Int64
	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseDestination().DoAndReturn(func() (connectorPlugin.DestinationPlugin, error) {
		if call.Add(1) == 1 {
			return pmock.NewConfigurableDestinationPlugin(ctrl,
				pmock.DestinationPluginWithConfigure(),
				pmock.DestinationPluginWithOpen(),
				pmock.DestinationPluginWithRun(),
				pmock.DestinationPluginWithRecords(nil),
				pmock.DestinationPluginWithTeardown(),
			), nil
		}
		// Recovered run receives the records and is torn down on stop (the funnel
		// does not send the plugin Stop signal — see sourceRecoversAfterTransientError).
		return pmock.NewConfigurableDestinationPlugin(ctrl,
			pmock.DestinationPluginWithConfigure(),
			pmock.DestinationPluginWithOpen(),
			pmock.DestinationPluginWithRun(),
			pmock.DestinationPluginWithRecords(healthyRecords),
			pmock.DestinationPluginWithTeardown(),
		), nil
	}).Times(2)
	return dest, dispenser
}

// failingSourceTimes builds a source that emits no records and fails with
// transientErr on every one of its `times` dispenses (a source that never
// recovers). Each dispense yields a fresh plugin, mirroring how
// buildRunnablePipeline re-dispenses on every restart.
func failingSourceTimes(
	ctrl *gomock.Controller,
	persister *connector.Persister,
	transientErr error,
	times int,
) (*connector.Instance, *pmock.Dispenser) {
	source := dummySource(persister)
	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseSource().DoAndReturn(func() (connectorPlugin.SourcePlugin, error) {
		return pmock.NewConfigurableSourcePlugin(ctrl,
			pmock.SourcePluginWithConfigure(),
			pmock.SourcePluginWithOpen(),
			pmock.SourcePluginWithRun(),
			pmock.SourcePluginWithRecords(nil, transientErr),
			pmock.SourcePluginWithAcks(0, false),
			pmock.SourcePluginWithTeardown(),
		), nil
	}).Times(times)
	return source, dispenser
}

// destinationTimes builds a destination that receives no records and is only
// torn down (no graceful Stop) on every one of its `times` dispenses. Suitable
// for the error/recovery paths where the destination is rebuilt per restart.
func destinationTimes(
	ctrl *gomock.Controller,
	persister *connector.Persister,
	times int,
) (*connector.Instance, *pmock.Dispenser) {
	dest := dummyDestination(persister)
	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseDestination().DoAndReturn(func() (connectorPlugin.DestinationPlugin, error) {
		return pmock.NewConfigurableDestinationPlugin(ctrl,
			pmock.DestinationPluginWithConfigure(),
			pmock.DestinationPluginWithOpen(),
			pmock.DestinationPluginWithRun(),
			pmock.DestinationPluginWithRecords(nil),
			pmock.DestinationPluginWithTeardown(),
		), nil
	}).Times(times)
	return dest, dispenser
}

// dlqDispenserTimes builds a DLQ destination dispensed `times` times (the DLQ is
// rebuilt on every pipeline (re)start). It receives no records and is only torn
// down.
func dlqDispenserTimes(
	ctrl *gomock.Controller,
	persister *connector.Persister,
	times int,
) (*connector.Instance, *pmock.Dispenser) {
	return destinationTimes(ctrl, persister, times)
}

// waitForPipelineRunning blocks until the pipeline instance reports
// StatusRunning, failing the test if that doesn't happen within the timeout. It
// is the deterministic replacement for a fixed sleep-after-Start: it lets the
// pipeline finish transitioning to Running before a test force-stops it, without
// guessing a duration. pipeline.Instance.GetStatus is lock-guarded, so this is a
// legitimate concurrent observation point.
func waitForPipelineRunning(t *testing.T, pl *pipeline.Instance) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if pl.GetStatus() == pipeline.StatusRunning {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for pipeline to reach StatusRunning (last status: %s)", pl.GetStatus())
		}
		time.Sleep(time.Millisecond)
	}
}

// waitForRecordsAcked blocks until the source connector has acked through the
// position of the last of the given records (or returns immediately if records
// is empty). It fails the test if that doesn't happen within the timeout.
//
// This is the deterministic replacement for a fixed time.Sleep used to let
// records "finish flowing" before a test stops the pipeline: a sleep is a
// guess at a duration, and under CI load the guess can be wrong, causing
// Worker.Stop to cut the pipeline off mid-delivery. Source.Ack persists the
// last acked position on connector.Instance.State (see pkg/connector/source.go),
// which is exported and lock-guarded via the embedded sync.RWMutex, so it's a
// legitimate, non-invasive observation point — no product code changes needed.
func waitForRecordsAcked(t *testing.T, source *connector.Instance, records []opencdc.Record) {
	t.Helper()
	if len(records) == 0 {
		return
	}
	want := records[len(records)-1].Position

	deadline := time.Now().Add(5 * time.Second)
	for {
		source.RLock()
		state, ok := source.State.(connector.SourceState)
		source.RUnlock()
		if ok && bytes.Equal(state.Position, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for source to ack all %d records (last acked state: %+v)", len(records), source.State)
		}
		time.Sleep(time.Millisecond)
	}
}

// testErrRecoveryCfg returns an error-recovery configuration tuned for fast,
// deterministic unit tests: a small MinDelay so the backoff wait is short, and a
// MaxRetriesWindow large enough that the retry-window decrement (a time.AfterFunc
// in StartWithBackoff) never fires mid-test — so attempt counting stays
// deterministic. Individual tests override MaxRetries where they need a finite
// bound. Mirrors pkg/lifecycle's testErrRecoveryCfg, retimed for the funnel.
func testErrRecoveryCfg() *lifecyclev1.ErrRecoveryCfg {
	return &lifecyclev1.ErrRecoveryCfg{
		MinDelay:         time.Millisecond,
		MaxDelay:         10 * time.Millisecond,
		BackoffFactor:    2,
		MaxRetries:       lifecyclev1.InfiniteRetriesErrRecovery,
		MaxRetriesWindow: time.Minute,
	}
}

func generateRecords(count int) []opencdc.Record {
	records := make([]opencdc.Record, count)
	for i := 0; i < count; i++ {
		records[i] = opencdc.Record{
			Key: opencdc.RawData(uuid.NewString()),
			Payload: opencdc.Change{
				Before: opencdc.RawData{},
				After:  opencdc.RawData(uuid.NewString()),
			},
			Position: opencdc.Position(strconv.Itoa(i)),
		}
	}
	return records
}

// generatorSource creates a connector source that fills up the returned slice
// with generated records as they are produced. After producing the requested
// number of records it returns wantErr.
func generatorSource(
	ctrl *gomock.Controller,
	persister *connector.Persister,
	records []opencdc.Record,
	wantErr error,
	stop bool,
) (*connector.Instance, *pmock.Dispenser) {
	sourcePluginOptions := []pmock.ConfigurableSourcePluginOption{
		pmock.SourcePluginWithConfigure(),
		pmock.SourcePluginWithOpen(),
		pmock.SourcePluginWithRun(),
		pmock.SourcePluginWithRecords(records, wantErr),
		pmock.SourcePluginWithAcks(len(records), wantErr == nil),
		pmock.SourcePluginWithTeardown(),
	}

	if stop {
		sourcePluginOptions = append(sourcePluginOptions, pmock.SourcePluginWithStop())
	}
	sourcePlugin := pmock.NewConfigurableSourcePlugin(ctrl, sourcePluginOptions...)

	source := dummySource(persister)

	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseSource().Return(sourcePlugin, nil)

	return source, dispenser
}

// generatorSourceFatalError is like generatorSource, but for a source whose
// Read returns a fatal, non-recovered error: the funnel Worker degrades the
// pipeline directly from that error and never calls Worker.Stop. Teardown is
// still expected exactly once — Worker.Close tears the source down on this path
// via the idempotent Worker.tearDownSource (#2559) — so requiring it here guards
// against regressing that cleanup.
func generatorSourceFatalError(
	ctrl *gomock.Controller,
	persister *connector.Persister,
	records []opencdc.Record,
	wantErr error,
) (*connector.Instance, *pmock.Dispenser) {
	sourcePluginOptions := []pmock.ConfigurableSourcePluginOption{
		pmock.SourcePluginWithConfigure(),
		pmock.SourcePluginWithOpen(),
		pmock.SourcePluginWithRun(),
		pmock.SourcePluginWithRecords(records, wantErr),
		pmock.SourcePluginWithAcks(len(records), false),
		pmock.SourcePluginWithTeardown(),
	}
	sourcePlugin := pmock.NewConfigurableSourcePlugin(ctrl, sourcePluginOptions...)

	source := dummySource(persister)

	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseSource().Return(sourcePlugin, nil)

	return source, dispenser
}

// asserterDestination creates a connector destination that checks if the records it gets
// match the expected records. On teardown it also makes sure that it received
// all expected records.
func asserterDestination(
	ctrl *gomock.Controller,
	persister *connector.Persister,
	records []opencdc.Record,
	stop bool,
) (*connector.Instance, *pmock.Dispenser) {
	destinationPluginOptions := []pmock.ConfigurableDestinationPluginOption{
		pmock.DestinationPluginWithConfigure(),
		pmock.DestinationPluginWithOpen(),
		pmock.DestinationPluginWithRun(),
		pmock.DestinationPluginWithRecords(records),
		pmock.DestinationPluginWithTeardown(),
	}

	if stop {
		var lastPosition opencdc.Position
		if len(records) > 0 {
			lastPosition = records[len(records)-1].Position
		}
		destinationPluginOptions = append(destinationPluginOptions, pmock.DestinationPluginWithStop(lastPosition))
	}

	destinationPlugin := pmock.NewConfigurableDestinationPlugin(ctrl, destinationPluginOptions...)

	dest := dummyDestination(persister)

	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseDestination().Return(destinationPlugin, nil)

	return dest, dispenser
}

// dummySource creates a dummy source connector.
func dummySource(persister *connector.Persister) *connector.Instance {
	// randomize plugin name in case of multiple sources
	testPluginName := "test-source-plugin-" + uuid.NewString()
	source := &connector.Instance{
		ID:         uuid.NewString(),
		Type:       connector.TypeSource,
		PipelineID: uuid.NewString(),
		Plugin:     testPluginName,
	}
	source.Init(log.Nop(), persister)

	return source
}

// dummyDestination creates a dummy destination connector.
func dummyDestination(persister *connector.Persister) *connector.Instance {
	// randomize plugin name in case of multiple destinations
	testPluginName := "test-destination-plugin-" + uuid.NewString()

	destination := &connector.Instance{
		ID:         uuid.NewString(),
		Type:       connector.TypeDestination,
		PipelineID: uuid.NewString(),
		Plugin:     testPluginName,
	}
	destination.Init(log.Nop(), persister)

	return destination
}

// testConnectorService fulfills the ConnectorService interface.
type testConnectorService map[string]*connector.Instance

func (s testConnectorService) Get(_ context.Context, id string) (*connector.Instance, error) {
	conn, ok := s[id]
	if !ok {
		return nil, connector.ErrInstanceNotFound
	}
	return conn, nil
}

func (s testConnectorService) Create(context.Context, string, connector.Type, string, string, connector.Config, connector.ProvisionType) (*connector.Instance, error) {
	return s[testDLQID], nil
}

// testProcessorService fulfills the ProcessorService interface.
type testProcessorService map[string]*processor.Instance

func (s testProcessorService) MakeRunnableProcessor(context.Context, *processor.Instance) (*processor.RunnableProcessor, error) {
	return nil, cerrors.New("not implemented")
}

func (s testProcessorService) Get(_ context.Context, id string) (*processor.Instance, error) {
	proc, ok := s[id]
	if !ok {
		return nil, processor.ErrInstanceNotFound
	}
	return proc, nil
}

// testConnectorPluginService fulfills the ConnectorPluginService interface.
type testConnectorPluginService map[string]connectorPlugin.Dispenser

func (s testConnectorPluginService) NewDispenser(_ log.CtxLogger, name string, _ string) (connectorPlugin.Dispenser, error) {
	plug, ok := s[name]
	if !ok {
		return nil, plugin.ErrPluginNotFound
	}
	return plug, nil
}

// testPipelineService fulfills the PipelineService interface.
type testPipelineService map[string]*pipeline.Instance

func (s testPipelineService) Get(_ context.Context, pipelineID string) (*pipeline.Instance, error) {
	p, ok := s[pipelineID]
	if !ok {
		return nil, processor.ErrInstanceNotFound
	}
	return p, nil
}

func (s testPipelineService) List(_ context.Context) map[string]*pipeline.Instance {
	instances := make(map[string]*pipeline.Instance)
	return instances
}

func (s testPipelineService) UpdateStatus(_ context.Context, pipelineID string, status pipeline.Status, errMsg string) error {
	p, ok := s[pipelineID]
	if !ok {
		return processor.ErrInstanceNotFound
	}
	p.SetStatus(status)
	p.Error = errMsg
	return nil
}

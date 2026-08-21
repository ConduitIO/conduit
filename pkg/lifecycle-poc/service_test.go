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
	"io"
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
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
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

	// Slice 3b: a Worker's own FirstTask.Tasks() iterator now stops before the
	// shared sink (see TaskNode.MarkSharedBoundary) - it only walks tasks the
	// WORKER itself owns (Open/Close-wise), which for a single, processor-less
	// source is just the source task. The destination is still reachable at
	// runtime via Next directly (doTask/doNextTask never use this iterator),
	// and is verified separately below via got.sink.
	is.Equal(1, len(got.workers))
	worker := got.workers[0]
	wantOwnTasks := []funnel.Task{&funnel.SourceTask{}}
	i := 0
	for task := range worker.FirstTask.Tasks() {
		want := wantOwnTasks[i]
		is.Equal(reflect.TypeOf(want), reflect.TypeOf(task)) // unexpected task type
		i++
	}
	is.Equal(len(wantOwnTasks), i)
	is.Equal(worker.Source.(*connector.Source).Instance, source)

	// The shared sink (destination) is attached to the worker's own tail via
	// Next, but owned/opened/closed by got.sink, not by this worker.
	is.True(got.sink != nil)
	is.Equal(1, len(worker.FirstTask.Next))
	is.Equal(reflect.TypeOf(&funnel.DestinationTask{}), reflect.TypeOf(worker.FirstTask.Next[0].Task))
}

// recordingConnectorService wraps testConnectorService and records every
// Create call's `id` argument, in call order. Used to verify slice 3b's
// per-source DLQ naming (buildDLQName) actually reaches the connector
// service - testConnectorService.Create itself ignores every argument but
// the DLQ lookup key, so it can't tell two differently-named DLQ creations
// apart on its own.
type recordingConnectorService struct {
	testConnectorService
	mu      sync.Mutex
	created []string
}

func (s *recordingConnectorService) Create(
	ctx context.Context,
	id string,
	t connector.Type,
	plug string,
	pipelineID string,
	cfg connector.Config,
	pt connector.ProvisionType,
) (*connector.Instance, error) {
	s.mu.Lock()
	s.created = append(s.created, id)
	s.mu.Unlock()
	return s.testConnectorService.Create(ctx, id, t, plug, pipelineID, cfg, pt)
}

// TestServiceLifecycle_buildRunnablePipeline_MultipleSources is slice 3b's
// core buildRunnablePipeline test: N (here 2) source connectors must each
// get their own funnel.Worker and their own per-source DLQ, while sharing
// exactly ONE destination TaskNode (by pointer, not by value) via
// runnablePipeline.sink.
func TestServiceLifecycle_buildRunnablePipeline_MultipleSources(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	ctrl := gomock.NewController(t)
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	sourceA := dummySource(persister)
	sourceB := dummySource(persister)
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
		ConnectorIDs: []string{sourceA.ID, sourceB.ID, destination.ID},
	}
	pl.SetStatus(pipeline.StatusUserStopped)

	connSvc := &recordingConnectorService{
		testConnectorService: testConnectorService{
			sourceA.ID:     sourceA,
			sourceB.ID:     sourceB,
			destination.ID: destination,
			testDLQID:      dlq,
		},
	}

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		connSvc,
		testProcessorService{},
		testConnectorPluginService{
			sourceA.Plugin:     pmock.NewDispenser(ctrl),
			sourceB.Plugin:     pmock.NewDispenser(ctrl),
			destination.Plugin: pmock.NewDispenser(ctrl),
			dlq.Plugin:         pmock.NewDispenser(ctrl),
		},
		testPipelineService{},
		false,
	)

	got, err := ls.buildRunnablePipeline(ctx, pl)
	is.NoErr(err)

	// One worker per source, each tagged with its own source's connector ID.
	is.Equal(2, len(got.workers))
	is.Equal(2, len(got.sourceIDs))
	gotSourceIDs := map[string]bool{got.sourceIDs[0]: true, got.sourceIDs[1]: true}
	is.True(gotSourceIDs[sourceA.ID])
	is.True(gotSourceIDs[sourceB.ID])
	is.True(got.sink != nil)

	// Both workers' own prefix is just their source task (no per-connector
	// processors configured), and both converge on the IDENTICAL shared
	// destination TaskNode - the same pointer, not two independently-built
	// copies of it. This is the shared-sink wiring the crux of this slice
	// depends on: closing got.sink tears down this ONE node exactly once,
	// regardless of how many workers point at it.
	is.Equal(1, len(got.workers[0].FirstTask.Next))
	is.Equal(1, len(got.workers[1].FirstTask.Next))
	is.True(got.workers[0].FirstTask.Next[0] == got.workers[1].FirstTask.Next[0])
	is.Equal(reflect.TypeOf(&funnel.DestinationTask{}), reflect.TypeOf(got.workers[0].FirstTask.Next[0].Task))

	// Each worker's own Source is its own, distinct connector wrapper - never
	// a sibling's. This is the structural property that makes cross-source
	// ack contamination impossible (see runnablePipeline.workers' doc):
	// Worker.Ack always calls THIS field's Ack, and there is exactly one
	// funnel.Worker per source.
	is.True(got.workers[0].Source != got.workers[1].Source)

	// Per-source DLQ naming (slice 3b, hash-suffixed since the L1 fix — see
	// buildDLQName): a distinct name for EACH source, never the old,
	// now-collision-prone pl.ID+"-dlq", and never the pl.ID+"-"+sourceID+"-dlq"
	// format that briefly replaced it (which double-embedded the pipeline ID,
	// since a provisioned connector ID is already pipelineID+":"+name — see
	// buildDLQName's doc).
	wantDLQNames := map[string]bool{
		buildDLQName(pl.ID, sourceA.ID): true,
		buildDLQName(pl.ID, sourceB.ID): true,
	}
	connSvc.mu.Lock()
	gotCreated := append([]string(nil), connSvc.created...)
	connSvc.mu.Unlock()
	is.Equal(2, len(gotCreated))
	for _, name := range gotCreated {
		is.True(wantDLQNames[name])
	}
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
	defer stopAndWaitPersister(t, killAll, persister)

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
	defer stopAndWaitPersister(t, killAll, persister)

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
	defer stopAndWaitPersister(t, killAll, persister)

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
	defer stopAndWaitPersister(t, killAll, persister)

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
	defer stopAndWaitPersister(t, killAll, persister)

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

// TestServiceLifecycle_Recovery_LiveEntryPublishedBeforeRunningStatus is the
// #2746 regression test.
//
// The invariant: whenever a caller can observe the pipeline as running,
// runningPipelines[id] is the run that is ACTUALLY running. Everything
// public resolves a pipeline through that map — Stop, StopAll, WaitPipeline,
// StopAndWait (and so provisioning.ApplyPlanLive) — and StartWithBackoff's
// "am I still the live pipeline" guard is a pointer comparison against it.
//
// Start used to publish the new run only AFTER runPipeline returned, i.e.
// after runPipeline had already announced StatusRunning. On a recovery
// restart the previous entry is deliberately left in place until that swap,
// so in the gap the map pointed at the FAILED run: WaitPipeline joined the
// dead tomb and returned the pre-recovery error for a pipeline that had just
// recovered, and Stop stopped the dead run while the recovered one kept
// running — a pipeline nobody could stop, and a persister that never
// quiesced (which is the 10-minute hang in #2746, not just its fast
// assertion failure).
//
// This does not try to catch that gap by racing it, which is what made the
// original symptom intermittent. It HOLDS the lifecycle inside the gap:
// statusRecorder.onUpdate blocks in the middle of the post-recovery
// StatusRunning write — the exact instant an observer first learns the
// pipeline is running again — and the assertions run there. Pre-fix that is
// a deterministic failure, not a probabilistic one.
func TestServiceLifecycle_Recovery_LiveEntryPublishedBeforeRunningStatus(t *testing.T) {
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

	// inWindow closes when the recovered run is mid-announcement; release
	// unblocks it once the assertions below have run.
	inWindow := make(chan struct{})
	release := make(chan struct{})
	rec := newStatusRecorder(ps)
	rec.onUpdate = func(status pipeline.Status, nth int) {
		// The 2nd StatusRunning is the recovery restart (the 1st is the
		// initial run). Fire once: a later run must not re-block.
		if status == pipeline.StatusRunning && nth == 2 {
			close(inWindow)
			<-release
		}
	}

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

	is.NoErr(ls.Start(ctx, pl.ID))

	<-inWindow

	// The assertion, taken while the lifecycle is frozen in the window: the
	// entry a caller would resolve right now must be the live run, not the
	// one that just failed. Reading rp.t directly (rather than going through
	// Stop/WaitPipeline, both of which legitimately BLOCK on a live tomb and
	// so cannot distinguish "correct" from "hung" here) is what keeps this
	// deterministic. Pre-fix, ok is true but the entry is the pre-recovery
	// runnablePipeline, whose tomb was killed by transientErr.
	rp, ok := ls.runningPipelines.Get(pl.ID)
	is.True(ok) // no live entry at all while the pipeline reports Running
	is.True(rp.t != nil)
	if !rp.t.Alive() {
		t.Fatalf(
			"runningPipelines[%s] is a DEAD run at the moment the pipeline announces StatusRunning "+
				"(tomb err: %v) - Stop/WaitPipeline/StopAndWait would all operate on the failed "+
				"pre-recovery run instead of the recovered one (#2746)",
			pl.ID, rp.t.Err(),
		)
	}

	close(release)

	// Behavioural half: with the live entry published in time, the recovered
	// run is the one a caller can actually drive to a clean stop - no
	// pre-recovery error resurfacing from a dead tomb, and no orphaned run
	// left behind (which is what stopAndWaitPersister above would hang on).
	waitForRecordsAcked(t, source, healthyRecords)
	is.Equal(pipeline.StatusRunning, pl.GetStatus())

	is.NoErr(ls.Stop(ctx, pl.ID, false))
	is.NoErr(ls.WaitPipeline(pl.ID))
	is.Equal(pipeline.StatusUserStopped, pl.GetStatus())

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
	defer stopAndWaitPersister(t, killAll, persister)

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
	defer stopAndWaitPersister(t, killAll, persister)

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

// TestServiceLifecycle_Stop_TransientErrorMidDrain_NoRecovery is the O3
// regression test (docs/design-documents/20260731-archv2-drain-reconfigure.md):
// the recovery port (a61d4bc) made a deliberate per-pipeline Stop(force=false)
// racy against a transient (non-fatal) error surfacing from the drain itself.
// A single record is read and reaches the destination (in flight, holding
// funnel.Worker's processingLock); the destination is held there
// deterministically (via pmock.DestinationPluginWithControlledError) until the
// test has confirmed — by polling the *runnablePipeline's own intentionalStop
// field, not a sleep — that Stop has already recorded this as an intentional,
// operator-initiated stop. Only then is the destination released to fail with
// a plain (non-fatal) error, exactly reproducing "Stop(force=false) racing a
// transient drain error".
//
// Without the intentionalStop fix, this error falls into runPipeline's
// recovery default arm and auto-restarts the pipeline — re-dispensing the
// source and destination a second time, which fails this test's Times(1)
// dispenser expectations, and finalizing with a Recovering status entry
// instead of going straight to UserStopped. With the fix, the pipeline
// finalizes UserStopped, recoverPipeline is never invoked (no Recovering
// status, source/destination dispensed exactly once), and WaitPipeline
// returns nil (the transient error is suppressed, mirroring the
// isGracefulShutdown arm's existing behavior).
func TestServiceLifecycle_Stop_TransientErrorMidDrain_NoRecovery(t *testing.T) {
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

	wantRecords := generateRecords(1)

	// Source: delivers the single record, then (since there's only one) its
	// onRun returns nil without closing the stream — it just goes quiet, as if
	// still waiting for a record that never comes. It is never acked (the
	// batch fails downstream before Ack), so no ack-count assertion.
	ctrl := gomock.NewController(t)
	sourcePlugin := pmock.NewConfigurableSourcePlugin(ctrl,
		pmock.SourcePluginWithConfigure(),
		pmock.SourcePluginWithOpen(),
		pmock.SourcePluginWithRun(),
		pmock.SourcePluginWithRecords(wantRecords, nil),
		pmock.SourcePluginWithAcks(0, false),
		pmock.SourcePluginWithTeardown(),
	)
	source := dummySource(persister)
	sourceDispenser := pmock.NewDispenser(ctrl)
	sourceDispenser.EXPECT().DispenseSource().Return(sourcePlugin, nil).Times(1)

	// Destination: receives the one record (signaling `received`), then
	// blocks on `release` — holding the batch, and thus processingLock, "in
	// flight" — until the test explicitly releases it with a plain (non-fatal)
	// transient error.
	received := make(chan struct{})
	release := make(chan struct{})
	transientErr := cerrors.New("transient destination write failure mid-drain")
	destPlugin := pmock.NewConfigurableDestinationPlugin(ctrl,
		pmock.DestinationPluginWithConfigure(),
		pmock.DestinationPluginWithOpen(),
		pmock.DestinationPluginWithRun(),
		pmock.DestinationPluginWithControlledError(wantRecords, received, release, transientErr),
		pmock.DestinationPluginWithTeardown(),
	)
	destination := dummyDestination(persister)
	destDispenser := pmock.NewDispenser(ctrl)
	destDispenser.EXPECT().DispenseDestination().Return(destPlugin, nil).Times(1)

	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
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
			source.Plugin:      sourceDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		},
		rec,
		false,
	)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	<-received // the record is in flight at the destination, processingLock held

	stopErr := make(chan error, 1)
	go func() { stopErr <- ls.Stop(ctx, pl.ID, false) }()

	// Poll the real runnablePipeline's intentionalStop field directly (this
	// test is in-package) instead of sleeping: this is the exact condition
	// runPipeline's cleanup goroutine will check, so waiting for it to become
	// true is the precise, race-free point at which releasing the transient
	// error reproduces "Stop already recorded this as intentional when the
	// error surfaced" — not a timing guess.
	deadline := time.Now().Add(5 * time.Second)
	for {
		rp, ok := ls.runningPipelines.Get(pl.ID)
		if ok && rp.intentionalStop.Load() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for intentionalStop to be set")
		}
		time.Sleep(time.Millisecond)
	}

	close(release) // now let the transient error surface

	is.NoErr(<-stopErr) // Stop itself (rp.w.Stop) completes once the batch unwinds

	// The core O3 assertion: the pipeline finalizes UserStopped, never
	// Recovering. WaitPipeline's own return is intentionally not asserted
	// here: it races runPipeline's cleanup deleting the runningPipelines
	// entry, and can surface either the tomb's raw (pre-reassignment) Kill
	// error or nil depending on which side of that race the caller lands on
	// — the same documented caveat TestServiceLifecycle_Recovery_
	// GracefulShutdownDuringBackoff already establishes for the sibling
	// isGracefulShutdown arm, which this new arm mirrors. The reliable
	// signal is the persisted status, not this return value.
	_ = ls.WaitPipeline(pl.ID)
	waitForStatus(t, pl, pipeline.StatusUserStopped)

	for _, s := range rec.snapshot() {
		if s == pipeline.StatusRecovering {
			t.Fatalf("pipeline entered StatusRecovering - the transient mid-drain error was auto-recovered instead of being treated as an intentional stop (statuses: %v)", rec.snapshot())
		}
	}

	// ctrl.Finish() (registered by gomock.NewController(t)) verifies both
	// dispensers were called exactly once (Times(1)) - i.e. recoverPipeline
	// never redispensed either connector for a restart.
}

// TestServiceLifecycle_Stop_SourceTeardownFails_NoRecovery is the Path-B O3
// regression test found by adversarial review of the drain PR
// (docs/design-documents/20260731-archv2-drain-reconfigure.md). It covers the
// case the sibling _TransientErrorMidDrain_ test structurally cannot: here
// funnel.Worker.Stop ARMS its stop flag (w.stop.Store(true)) and THEN
// tearDownSource FAILS — a wedged/dead source — so Stop returns a non-nil error
// while the worker is genuinely stopping (Stopping() == true).
//
// The original rollback cleared intentionalStop on ANY Stop error, which is
// correct only for the lock-acquisition-timeout path (flag never armed). On
// THIS path the flag WAS armed, so the unconditional rollback wrongly cleared
// it: the subsequent non-fatal Worker.Close teardown error then fell into
// runPipeline's recovery default arm and auto-restarted a pipeline the operator
// just stopped — reintroducing the exact O3 bug the field exists to prevent.
// The fix rolls back only when !rp.w.Stopping(). Without it this test's Times(1)
// dispenser expectations fail (connectors redispensed for the restart) and a
// Recovering status appears; with it the pipeline finalizes UserStopped and
// recoverPipeline is never invoked.
func TestServiceLifecycle_Stop_SourceTeardownFails_NoRecovery(t *testing.T) {
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

	wantRecords := generateRecords(1)

	// Source: delivers one record, then goes quiet. Its Teardown FAILS every
	// time (both Worker.Stop's and Worker.Close's retry), reproducing a
	// dead/wedged source whose teardown errors AFTER Stop already armed w.stop.
	teardownErr := cerrors.New("source teardown failure (wedged source)")
	ctrl := gomock.NewController(t)
	sourcePlugin := pmock.NewConfigurableSourcePlugin(ctrl,
		pmock.SourcePluginWithConfigure(),
		pmock.SourcePluginWithOpen(),
		pmock.SourcePluginWithRun(),
		pmock.SourcePluginWithRecords(wantRecords, nil),
		pmock.SourcePluginWithAcks(1, false),
		pmock.SourcePluginWithTeardownError(teardownErr),
	)
	source := dummySource(persister)
	sourceDispenser := pmock.NewDispenser(ctrl)
	sourceDispenser.EXPECT().DispenseSource().Return(sourcePlugin, nil).Times(1)

	// Destination holds the one record in flight (processingLock held), then is
	// released cleanly once the test has confirmed Stop recorded the intentional
	// stop — so Stop proceeds into the source teardown that fails. The release
	// carries no error: the drain-terminating error comes from source teardown,
	// not the destination, isolating the Path-B path.
	received := make(chan struct{})
	release := make(chan struct{})
	destPlugin := pmock.NewConfigurableDestinationPlugin(ctrl,
		pmock.DestinationPluginWithConfigure(),
		pmock.DestinationPluginWithOpen(),
		pmock.DestinationPluginWithRun(),
		pmock.DestinationPluginWithControlledBlock(wantRecords, received, release),
		pmock.DestinationPluginWithTeardown(),
	)
	destination := dummyDestination(persister)
	destDispenser := pmock.NewDispenser(ctrl)
	destDispenser.EXPECT().DispenseDestination().Return(destPlugin, nil).Times(1)

	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
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
			source.Plugin:      sourceDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		},
		rec,
		false,
	)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	<-received // record in flight at the destination, processingLock held

	stopErr := make(chan error, 1)
	go func() { stopErr <- ls.Stop(ctx, pl.ID, false) }()

	// Race-free: wait for intentionalStop to be set (exactly what runPipeline's
	// cleanup goroutine checks) before releasing the batch, so Stop then drives
	// into the failing source teardown.
	deadline := time.Now().Add(5 * time.Second)
	for {
		rp, ok := ls.runningPipelines.Get(pl.ID)
		if ok && rp.intentionalStop.Load() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for intentionalStop to be set")
		}
		time.Sleep(time.Millisecond)
	}

	close(release) // batch unwinds cleanly; Stop proceeds to tear down the source

	// Stop returns the source-teardown error — the whole point of Path B: Stop
	// errored, yet the worker armed its stop flag, so intentionalStop must NOT
	// be rolled back.
	err = <-stopErr
	is.True(err != nil)
	is.True(cerrors.Is(err, teardownErr))

	// Core assertion: finalizes UserStopped, never Recovering. (WaitPipeline's
	// own return races the cleanup delete — see the sibling test's note — so the
	// persisted status is the reliable signal.)
	_ = ls.WaitPipeline(pl.ID)
	waitForStatus(t, pl, pipeline.StatusUserStopped)

	for _, s := range rec.snapshot() {
		if s == pipeline.StatusRecovering {
			t.Fatalf("pipeline entered StatusRecovering - a Stop that errored on source teardown (with the stop flag armed) was auto-recovered instead of being treated as an intentional stop (statuses: %v)", rec.snapshot())
		}
	}
	// ctrl.Finish() verifies both dispensers were called exactly once — no
	// recovery restart redispensed either connector.
}

// multiDLQConnectorService routes each buildDLQ Create call to one of a
// fixed set of DLQ *connector.Instance values, CYCLING through them in call
// order (index i%len(dlqs)). This is what an N-source Service-level test
// needs and testConnectorService.Create (which returns the SAME instance for
// every call, ignoring its arguments) can't provide: buildRunnablePipeline's
// buildDLQ call happens once per source, in pl.ConnectorIDs order, and that
// same per-source order repeats identically on every recovery restart (the
// source list itself never changes) — so cycling (not simply popping a
// fixed queue) is what lets ONE multiDLQConnectorService correctly serve
// both a single-run test (N sources, N calls) and a recovery test (N
// sources × M restarts, N*M calls, each source's DLQ instance/dispenser
// reused with a Times(M) expectation) without the test needing to know how
// many restarts will happen in advance.
type multiDLQConnectorService struct {
	testConnectorService
	mu    sync.Mutex
	dlqs  []*connector.Instance
	calls int
}

func (s *multiDLQConnectorService) Create(context.Context, string, connector.Type, string, string, connector.Config, connector.ProvisionType) (*connector.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dlqs) == 0 {
		return nil, cerrors.New("multiDLQConnectorService: no DLQ instances configured")
	}
	dlq := s.dlqs[s.calls%len(s.dlqs)]
	s.calls++
	return dlq, nil
}

// idleSourceTimes builds a source that never produces any records (its Read
// blocks until stopped or the pipeline's context is canceled) and is
// dispensed `times` times — a fresh mock plugin per dispense, matching how
// buildRunnablePipeline re-dispenses on every restart. Used as an N-source
// test's "quiet sibling": present in every run, contributing nothing, so a
// test can isolate what happens to a DIFFERENT source without this one's
// behavior — or its interleaving with a shared destination's strict-order
// asserterDestination check — being part of what's under test.
func idleSourceTimes(
	ctrl *gomock.Controller,
	persister *connector.Persister,
	times int,
) (*connector.Instance, *pmock.Dispenser) {
	source := dummySource(persister)
	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseSource().DoAndReturn(func() (connectorPlugin.SourcePlugin, error) {
		return pmock.NewConfigurableSourcePlugin(ctrl,
			pmock.SourcePluginWithConfigure(),
			pmock.SourcePluginWithOpen(),
			pmock.SourcePluginWithRun(),
			pmock.SourcePluginWithRecords(nil, nil),
			pmock.SourcePluginWithAcks(0, false),
			pmock.SourcePluginWithTeardown(),
		), nil
	}).Times(times)
	return source, dispenser
}

// TestServiceLifecycle_NSource_PartialGracefulStop_Escalates is the
// Service-level H1 regression test (adversarial review of #2734). It closes
// the exact coverage hole the review found hid H1: before this test, there
// was no Service-level test that ever drove stopRunnablePipeline's N-worker
// choreography at all — service_test.go only asserted buildRunnablePipeline
// wiring, and the funnel-level tests build Worker/Sink by hand, never going
// through lifecycle-poc.Service.
//
// Two sources (A, B) share one destination. A's single record is held "in
// flight" at the shared destination — via
// pmock.DestinationPluginWithControlledBlock, which blocks the ack
// round-trip until the test releases it — so A holds its OWN processingLock
// (and, transitively, the shared destination's sharedMu) for the whole
// window. A's Stop call therefore cannot acquire that lock within a short
// ctx deadline. B has nothing in flight (idle, blocked in Read), so its
// Stop call arms almost immediately. This reproduces H1's exact shape: some
// source(s) armed (torn down) and other(s) didn't, within one bounded Stop
// deadline.
//
// Before the fix this could strand the pipeline reporting StatusRunning
// forever: workersWg never drains (B's own Do loop has nothing to make it
// exit, and A is genuinely still blocked), so runPipeline's cleanup
// goroutine — the only thing that ever writes a terminal status — never
// runs. With the fix, Stop detects the partial result and escalates:
// force-kills the pipeline's tomb and returns a coded
// CodePartialGracefulStopEscalated error naming which source(s) armed and
// which didn't. Releasing A's blocked write (this test's stand-in for "the
// slow write eventually completes, exactly as a real ctx-respecting gRPC
// call would") then lets A's worker unwind, and the pipeline reaches a
// terminal StatusDegraded — never stuck.
func TestServiceLifecycle_NSource_PartialGracefulStop_Escalates(t *testing.T) {
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

	recordsA := generateRecords(1)

	ctrl := gomock.NewController(t)

	// Source A: delivers its one record, then goes quiet (its Read just
	// blocks — no plugin Stop RPC is ever sent on a graceful stop, see the
	// sibling drain tests' comments on this file's established convention).
	sourceAPlugin := pmock.NewConfigurableSourcePlugin(ctrl,
		pmock.SourcePluginWithConfigure(),
		pmock.SourcePluginWithOpen(),
		pmock.SourcePluginWithRun(),
		pmock.SourcePluginWithRecords(recordsA, nil),
		pmock.SourcePluginWithAcks(0, false),
		pmock.SourcePluginWithTeardown(),
	)
	sourceA := dummySource(persister)
	sourceADispenser := pmock.NewDispenser(ctrl)
	sourceADispenser.EXPECT().DispenseSource().Return(sourceAPlugin, nil).Times(1)

	sourceB, sourceBDispenser := idleSourceTimes(ctrl, persister, 1)

	// The shared destination: A's record is held in flight (processingLock
	// AND sharedMu held by A's own Do goroutine) until the test releases it.
	// releaseFn is idempotent and deferred (in addition to the explicit call
	// below) so that if an assertion between here and the explicit call
	// fails, A's write is still unblocked during the resulting t.Fatal
	// unwind — without this, the deferred persister.Wait() above would hang
	// forever on a pending write that can never flush, turning an assertion
	// failure into a test-binary-wide timeout instead of a clean failure.
	received := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseFn()
	destPlugin := pmock.NewConfigurableDestinationPlugin(ctrl,
		pmock.DestinationPluginWithConfigure(),
		pmock.DestinationPluginWithOpen(),
		pmock.DestinationPluginWithRun(),
		pmock.DestinationPluginWithControlledBlock(recordsA, received, release),
		pmock.DestinationPluginWithTeardown(),
	)
	destination := dummyDestination(persister)
	destDispenser := pmock.NewDispenser(ctrl)
	destDispenser.EXPECT().DispenseDestination().Return(destPlugin, nil).Times(1)

	dlqA, dlqADispenser := asserterDestination(ctrl, persister, nil, false)
	dlqB, dlqBDispenser := asserterDestination(ctrl, persister, nil, false)
	connSvc := &multiDLQConnectorService{
		testConnectorService: testConnectorService{
			sourceA.ID:     sourceA,
			sourceB.ID:     sourceB,
			destination.ID: destination,
		},
		dlqs: []*connector.Instance{dlqA, dlqB},
	}

	pl, err = ps.AddConnector(ctx, pl.ID, sourceA.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, sourceB.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		connSvc,
		testProcessorService{},
		testConnectorPluginService{
			sourceA.Plugin:     sourceADispenser,
			sourceB.Plugin:     sourceBDispenser,
			destination.Plugin: destDispenser,
			dlqA.Plugin:        dlqADispenser,
			dlqB.Plugin:        dlqBDispenser,
		},
		ps,
		false,
	)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	<-received // A's record is in flight; A holds its own processingLock + sharedMu

	// A short deadline A can never meet (its own processingLock is held by
	// its blocked Do goroutine, and won't be released until this test closes
	// `release` below); B, idle, arms well within it.
	stopCtx, stopCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer stopCancel()

	stopErr := ls.Stop(stopCtx, pl.ID, false)
	is.True(stopErr != nil)

	ce, ok := conduiterr.Get(stopErr)
	is.True(ok)
	is.Equal(ce.Code, CodePartialGracefulStopEscalated)
	is.True(strings.Contains(stopErr.Error(), sourceB.ID)) // names the armed source
	is.True(strings.Contains(stopErr.Error(), sourceA.ID)) // names the unarmed source

	// Release A's blocked write — this test's stand-in for "the slow write
	// eventually completes" — letting A's worker unwind now that the
	// escalation has already force-killed the pipeline's tomb.
	releaseFn()

	// Core H1 assertion: the pipeline reaches a terminal status. Before the
	// fix, this would hang — workersWg never drains because B's Do loop, left
	// running with no operator-visible signal, has nothing left to make it
	// exit, and A's own exit (once release is closed) would just loop back
	// into reading a fresh batch instead of terminating.
	_ = ls.WaitPipeline(pl.ID)
	waitForStatus(t, pl, pipeline.StatusDegraded)
}

// TestServiceLifecycle_NSource_FatalErrorOneSource_DegradesWholePipeline
// generalizes TestServiceLifecycle_PipelineError to N sources: a fatal error
// in ONE of two sources sharing a destination must degrade the WHOLE
// pipeline (matching v1 and this slice's own design doc — "Status
// aggregation"), not just the failing source. The idle sibling (B) unwinds
// collaterally via context.Canceled once A's fatal error kills the shared
// tomb.
func TestServiceLifecycle_NSource_FatalErrorOneSource_DegradesWholePipeline(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.Test(t)
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	defer stopAndWaitPersister(t, killAll, persister)

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	wantErr := cerrors.FatalError(cerrors.New("source A connector error"))
	recordsA := generateRecords(3)

	ctrl := gomock.NewController(t)
	sourceA, sourceADispenser := generatorSourceFatalError(ctrl, persister, recordsA, wantErr)
	sourceB, sourceBDispenser := idleSourceTimes(ctrl, persister, 1)

	destination, destDispenser := asserterDestination(ctrl, persister, recordsA, false)

	dlqA, dlqADispenser := asserterDestination(ctrl, persister, nil, false)
	dlqB, dlqBDispenser := asserterDestination(ctrl, persister, nil, false)
	connSvc := &multiDLQConnectorService{
		testConnectorService: testConnectorService{
			sourceA.ID:     sourceA,
			sourceB.ID:     sourceB,
			destination.ID: destination,
		},
		dlqs: []*connector.Instance{dlqA, dlqB},
	}

	pl, err = ps.AddConnector(ctx, pl.ID, sourceA.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, sourceB.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		connSvc,
		testProcessorService{},
		testConnectorPluginService{
			sourceA.Plugin:     sourceADispenser,
			sourceB.Plugin:     sourceBDispenser,
			destination.Plugin: destDispenser,
			dlqA.Plugin:        dlqADispenser,
			dlqB.Plugin:        dlqBDispenser,
		},
		ps,
		false,
	)

	events := make(chan FailureEvent, 1)
	ls.OnFailure(func(e FailureEvent) { events <- e })

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	err = ls.WaitPipeline(pl.ID)
	is.True(err != nil)

	is.Equal(pipeline.StatusDegraded, pl.GetStatus())
	is.True(cerrors.Is(err, wantErr))

	event, eventReceived, err := cchan.Chan[FailureEvent](events).RecvTimeout(ctx, 200*time.Millisecond)
	is.NoErr(err)
	is.True(eventReceived)
	is.Equal(pl.ID, event.ID)
}

// TestServiceLifecycle_NSource_TransientErrorOneSource_Recovers generalizes
// TestServiceLifecycle_Recovery_TransientErrorRecovers to N sources: a
// transient (non-fatal) error in ONE of two sources drives PIPELINE-WIDE
// recovery — the design doc's "Status aggregation" section states recovery
// "rebuilds every source's worker and the shared sink from scratch" — so the
// idle sibling (B) must be re-dispensed on the recovery restart exactly like
// A is, proving the N-worker choreography survives a restart, not just an
// initial run.
func TestServiceLifecycle_NSource_TransientErrorOneSource_Recovers(t *testing.T) {
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

	transientErr := cerrors.New("lost connection to source A")
	healthyRecords := generateRecords(3)

	ctrl := gomock.NewController(t)
	sourceA, sourceADispenser := sourceRecoversAfterTransientError(ctrl, persister, healthyRecords, transientErr)
	sourceB, sourceBDispenser := idleSourceTimes(ctrl, persister, 2) // initial run + recovery restart
	destination, destDispenser := destinationRecovers(ctrl, persister, healthyRecords)

	dlqA, dlqADispenser := dlqDispenserTimes(ctrl, persister, 2)
	dlqB, dlqBDispenser := dlqDispenserTimes(ctrl, persister, 2)
	connSvc := &multiDLQConnectorService{
		testConnectorService: testConnectorService{
			sourceA.ID:     sourceA,
			sourceB.ID:     sourceB,
			destination.ID: destination,
		},
		dlqs: []*connector.Instance{dlqA, dlqB},
	}

	pl, err = ps.AddConnector(ctx, pl.ID, sourceA.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, sourceB.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	rec := newStatusRecorder(ps)
	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		connSvc,
		testProcessorService{},
		testConnectorPluginService{
			sourceA.Plugin:     sourceADispenser,
			sourceB.Plugin:     sourceBDispenser,
			destination.Plugin: destDispenser,
			dlqA.Plugin:        dlqADispenser,
			dlqB.Plugin:        dlqBDispenser,
		},
		rec,
		false,
	)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	// Wait until the pipeline has recovered: it must have passed through
	// Recovering and be Running again.
	waitForRecovered(t, rec)

	waitForRecordsAcked(t, sourceA, healthyRecords)
	is.Equal(pipeline.StatusRunning, pl.GetStatus())

	err = ls.Stop(ctx, pl.ID, false)
	is.NoErr(err)
	is.NoErr(ls.WaitPipeline(pl.ID))
	is.Equal(pipeline.StatusUserStopped, pl.GetStatus())

	is.Equal(rec.snapshot(), []pipeline.Status{
		pipeline.StatusRunning,
		pipeline.StatusRecovering,
		pipeline.StatusRunning,
		pipeline.StatusUserStopped,
	})
}

// TestServiceLifecycle_NSource_OneSourceFinishesGracefully_StaysRunningUntilAllExit
// covers M3 and the design doc's "a source finishing gracefully is not a
// failure" acceptance criterion at the Service level: source A exhausts a
// fixed record set (io.EOF — see Worker.doTaskAttempt's io.EOF branch)
// while source B, still connected and alive but with nothing to emit yet
// (an idle sibling, deliberately not ALSO writing to the shared destination
// — see idleSourceTimes' doc on why: two sources concurrently writing to
// the same asserterDestination would make its strict-arrival-order check
// flaky), keeps its own worker running. The pipeline must stay Running the
// whole time A is gone and B hasn't been asked to stop, and only reach a
// terminal status once BOTH have exited.
func TestServiceLifecycle_NSource_OneSourceFinishesGracefully_StaysRunningUntilAllExit(t *testing.T) {
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

	recordsA := generateRecords(2)

	ctrl := gomock.NewController(t)

	// Source A: exhausts its fixed record set with io.EOF — nobody calls
	// Stop on it. See Worker.doTaskAttempt's io.EOF branch.
	sourceAPlugin := pmock.NewConfigurableSourcePlugin(ctrl,
		pmock.SourcePluginWithConfigure(),
		pmock.SourcePluginWithOpen(),
		pmock.SourcePluginWithRun(),
		pmock.SourcePluginWithRecords(recordsA, io.EOF),
		pmock.SourcePluginWithAcks(len(recordsA), false),
		pmock.SourcePluginWithTeardown(),
	)
	sourceA := dummySource(persister)
	sourceADispenser := pmock.NewDispenser(ctrl)
	sourceADispenser.EXPECT().DispenseSource().Return(sourceAPlugin, nil).Times(1)

	sourceB, sourceBDispenser := idleSourceTimes(ctrl, persister, 1)

	destination, destDispenser := asserterDestination(ctrl, persister, recordsA, false)

	dlqA, dlqADispenser := asserterDestination(ctrl, persister, nil, false)
	dlqB, dlqBDispenser := asserterDestination(ctrl, persister, nil, false)
	connSvc := &multiDLQConnectorService{
		testConnectorService: testConnectorService{
			sourceA.ID:     sourceA,
			sourceB.ID:     sourceB,
			destination.ID: destination,
		},
		dlqs: []*connector.Instance{dlqA, dlqB},
	}

	pl, err = ps.AddConnector(ctx, pl.ID, sourceA.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, sourceB.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		connSvc,
		testProcessorService{},
		testConnectorPluginService{
			sourceA.Plugin:     sourceADispenser,
			sourceB.Plugin:     sourceBDispenser,
			destination.Plugin: destDispenser,
			dlqA.Plugin:        dlqADispenser,
			dlqB.Plugin:        dlqBDispenser,
		},
		ps,
		false,
	)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	// Wait for A to have acked its own records — proof it fully exhausted
	// and gracefully exited (its own worker returned nil, never touching
	// rp.t.Kill — see runPipeline's doc on why an io.EOF exit never kills
	// the tomb).
	waitForRecordsAcked(t, sourceA, recordsA)

	// The pipeline must still be Running: B's worker is still registered on
	// the tomb (idle, but alive) — A's own Worker.Close only tore down A's
	// own source + DLQ, never the shared sink (see funnel.Sink's doc), and
	// nothing about A finishing touches B at all.
	is.Equal(pipeline.StatusRunning, pl.GetStatus())

	// Only now stop the pipeline (and therefore B) — the terminal status
	// must not have appeared before this point.
	err = ls.Stop(ctx, pl.ID, false)
	is.NoErr(err)
	is.NoErr(ls.WaitPipeline(pl.ID))
	is.Equal(pipeline.StatusUserStopped, pl.GetStatus())
}

// statusRecorder wraps a PipelineService, recording every UpdateStatus target
// status in order so a test can assert the transition sequence. UpdateStatus is
// called from multiple goroutines (the initial run and the cleanup/recovery
// goroutine), so the slice is mutex-guarded.
type statusRecorder struct {
	PipelineService
	mu       sync.Mutex
	statuses []pipeline.Status

	// onUpdate, if set, is called from inside UpdateStatus — after the status
	// has been recorded (so a concurrent snapshot/waitForRecovered already
	// sees it) but before the wrapped service applies it. It is the seam that
	// lets a test hold the lifecycle inside the "run is going live" window
	// and inspect it, instead of trying to catch that window by racing it.
	// The argument is the status being written and its 1-based occurrence
	// count for that status, so a hook can distinguish e.g. the initial
	// StatusRunning from the post-recovery one.
	onUpdate func(status pipeline.Status, nth int)
}

func newStatusRecorder(inner PipelineService) *statusRecorder {
	return &statusRecorder{PipelineService: inner}
}

func (r *statusRecorder) UpdateStatus(ctx context.Context, id string, status pipeline.Status, errMsg string) error {
	r.mu.Lock()
	r.statuses = append(r.statuses, status)
	nth := 0
	for _, s := range r.statuses {
		if s == status {
			nth++
		}
	}
	hook := r.onUpdate
	r.mu.Unlock()

	if hook != nil {
		hook(status, nth)
	}
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

// WaitPersisted is a no-op here: none of the existing tests using
// testConnectorService directly need a real durability wait — see
// testConnectorServiceWithPersister below for the tests that do. Mirrors
// pkg/lifecycle's identical testConnectorService.WaitPersisted no-op.
func (s testConnectorService) WaitPersisted() {}

// testConnectorServiceWithPersister routes WaitPersisted to the real
// persister backing its connectors, for tests that need to prove
// StopAndWait actually blocked until a flush landed (O2/StopAndWait tests).
// Mirrors pkg/lifecycle's identical helper.
type testConnectorServiceWithPersister struct {
	testConnectorService
	persister *connector.Persister
}

func (s testConnectorServiceWithPersister) WaitPersisted() {
	s.persister.WaitPendingWrites()
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

// persisterDrainTimeout bounds the deferred persister wait. Generously larger
// than the 1s flush delay these tests configure, so a legitimate drain always
// finishes inside it.
const persisterDrainTimeout = 10 * time.Second

// stopAndWaitPersister cancels the pipeline context and waits for the persister
// to drain, but only for a bounded time.
//
// The bound is the whole point. Persister.Wait blocks on connWg, which only
// reaches zero once every connector has called ConnectorStopped — and that
// happens through pipeline teardown (ls.Stop), not through context
// cancellation. Cancelling first is still right, but it cannot substitute for
// the stop.
//
// On the happy path this is free: the test body already called ls.Stop, so the
// wait returns at once. The bound only matters when an assertion has ALREADY
// failed. is.NoErr calls t.FailNow, which is runtime.Goexit: the rest of the
// test body is skipped — including ls.Stop — so the connectors never stop,
// connWg never reaches zero, and an unbounded Wait blocks until the
// package-wide timeout fires.
//
// That is #2746. An intermittent one-line assertion failure presented as a
// 10-minute hang that took the whole `test` job down, with a goroutine dump
// pointing at the recovery path rather than at the assertion that actually
// failed. All 13 tests here shared the shape, so ANY failing assertion in this
// file could produce it.
//
// This wait is hygiene, not an assertion — the in-file comments that introduced
// it say so: it exists to stop the persister's background flush goroutine from
// outliving the test and logging into a finished t. Bounding it preserves that
// and gives up the deadlock. If the drain genuinely does not finish, that is
// reported rather than waited on forever.
//
// t.Errorf, never t.Fatalf: this runs as a deferred function, and Fatalf's
// Goexit inside a defer would abandon the remaining cleanup.
func stopAndWaitPersister(t *testing.T, killAll context.CancelFunc, p *connector.Persister) {
	t.Helper()
	killAll()

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Wait()
	}()

	select {
	case <-done:
	case <-time.After(persisterDrainTimeout):
		t.Errorf("persister did not drain within %s: some connector never reported "+
			"ConnectorStopped (see #2746)", persisterDrainTimeout)
	}
}

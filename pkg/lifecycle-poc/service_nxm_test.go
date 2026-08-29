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
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/cchan"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
	"github.com/conduitio/conduit/pkg/pipeline"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// This file is the N×M shape-coverage gate for arch-v2 (see
// docs/design-documents/20260801-archv2-multiconnector-nsource.md): N sources
// x M destinations together already RUNS on main (slice 3a shipped M
// destinations from one source, slice 3b shipped N sources onto one shared
// destination), but nobody had tested the COMBINATION. This file - together
// with pkg/lifecycle-poc/funnel/worker_nxm_test.go, which covers the funnel
// level - is that missing verification.

// fakeTask is a minimal funnel.Task double for testing buildSharedTail
// directly: it doesn't need to run any real logic, just to be identifiable by
// ID and walkable in the TaskNode graph buildSharedTail returns.
type fakeTask struct{ id string }

func (f *fakeTask) ID() string                              { return f.id }
func (f *fakeTask) Open(context.Context) error              { return nil }
func (f *fakeTask) Close(context.Context) error             { return nil }
func (f *fakeTask) Do(context.Context, *funnel.Batch) error { return nil }

// TestBuildSharedTail_ShapeByProcessorPresence is test 1 of the N×M shape
// coverage gate: it calls the REAL buildSharedTail directly and asserts the
// two structurally different graphs it documents actually come out that way.
//
//   - >=1 pipeline-level processor: buildSharedTail returns exactly ONE root
//     (the processor chain), and that root's own tail fans out internally to
//     every destination branch - so funnel.NewSink marks exactly ONE shared
//     boundary, and every source Worker serializes on ONE mutex spanning the
//     processor AND every destination branch.
//   - 0 pipeline-level processors: buildSharedTail returns M INDEPENDENT
//     roots (one per destination branch) - funnel.NewSink marks M shared
//     boundaries, each with its own mutex.
//
// This locks the SHAPE in so a future buildSharedTail refactor that silently
// collapses M independent roots into one (or the reverse) fails here, rather
// than only showing up as an invisible throughput regression. The RUNTIME
// CONSEQUENCE of this shape (that M independent roots give real
// cross-destination concurrency, and one shared root forces full
// serialization) is proven separately, where it's actually observable: see
// funnel.TestNxM_NegativeSpace_NoProcessor_CrossDestinationConcurrency and
// funnel.TestNxM_PositiveComplement_WithProcessor_NoOverlapInSharedSubtree.
func TestBuildSharedTail_ShapeByProcessorPresence(t *testing.T) {
	s := &Service{}

	destTasks := [][]funnel.Task{
		{&fakeTask{id: "dest-0"}},
		{&fakeTask{id: "dest-1"}},
		{&fakeTask{id: "dest-2"}},
	}

	t.Run("with pipeline processor: one shared root fanning out to M branches", func(t *testing.T) {
		is := is.New(t)
		procTasks := []funnel.Task{&fakeTask{id: "shared-proc"}}

		roots, err := s.buildSharedTail(procTasks, destTasks)
		is.NoErr(err)
		is.Equal(1, len(roots))
		is.Equal("shared-proc", roots[0].Task.ID())
		is.Equal(len(destTasks), len(roots[0].Next))
		for i, branch := range roots[0].Next {
			is.Equal(destTasks[i][0].ID(), branch.Task.ID())
		}

		sink, err := funnel.NewSink(roots...)
		is.NoErr(err)
		is.True(sink != nil)
	})

	t.Run("no pipeline processor: M independent roots, one per destination branch", func(t *testing.T) {
		is := is.New(t)
		roots, err := s.buildSharedTail(nil, destTasks)
		is.NoErr(err)
		is.Equal(len(destTasks), len(roots))
		for i, root := range roots {
			is.Equal(destTasks[i][0].ID(), root.Task.ID())
			is.Equal(0, len(root.Next)) // single-task branch in this test, nothing further attached
		}

		sink, err := funnel.NewSink(roots...)
		is.NoErr(err)
		is.True(sink != nil)
	})
}

// TestServiceLifecycle_buildRunnablePipeline_NxM extends
// TestServiceLifecycle_buildRunnablePipeline_MultipleSources (which only
// covers N sources x M=1 destination) to N=2 sources x M=2 destinations: both
// workers' own prefixes must attach the SAME M destination TaskNode pointers
// - not two independently-built copies - so that got.sink tears down each
// destination exactly once regardless of how many sources point at it.
func TestServiceLifecycle_buildRunnablePipeline_NxM(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	ctrl := gomock.NewController(t)
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	sourceA := dummySource(persister)
	sourceB := dummySource(persister)
	dest1 := dummyDestination(persister)
	dest2 := dummyDestination(persister)
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
		ConnectorIDs: []string{sourceA.ID, sourceB.ID, dest1.ID, dest2.ID},
	}
	pl.SetStatus(pipeline.StatusUserStopped)

	connSvc := &recordingConnectorService{
		testConnectorService: testConnectorService{
			sourceA.ID: sourceA,
			sourceB.ID: sourceB,
			dest1.ID:   dest1,
			dest2.ID:   dest2,
			testDLQID:  dlq,
		},
	}

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		connSvc,
		testProcessorService{},
		testConnectorPluginService{
			sourceA.Plugin: pmock.NewDispenser(ctrl),
			sourceB.Plugin: pmock.NewDispenser(ctrl),
			dest1.Plugin:   pmock.NewDispenser(ctrl),
			dest2.Plugin:   pmock.NewDispenser(ctrl),
			dlq.Plugin:     pmock.NewDispenser(ctrl),
		},
		testPipelineService{},
		false,
	)

	got, err := ls.buildRunnablePipeline(ctx, pl)
	is.NoErr(err)

	is.Equal(2, len(got.workers))
	is.True(got.sink != nil)

	// No pipeline-level processors configured: buildSharedTail's
	// no-processor shape, so each worker's own tail attaches ALL M=2
	// destination branches DIRECTLY (M independent shared roots - see
	// TestBuildSharedTail_ShapeByProcessorPresence).
	is.Equal(2, len(got.workers[0].FirstTask.Next))
	is.Equal(2, len(got.workers[1].FirstTask.Next))

	// Both workers converge on the IDENTICAL M destination TaskNode
	// pointers - not two independently-built copies.
	for i := range 2 {
		is.True(got.workers[0].FirstTask.Next[i] == got.workers[1].FirstTask.Next[i])
	}
}

// generateRecordsWithPositionPrefix builds count records whose positions are
// prefixed (so two sources' record sets never collide) - used by the N×M
// service-level tests below that need multiple sources' records to coexist
// unambiguously in a shared destination's expectation set.
func generateRecordsWithPositionPrefix(prefix string, count int) []opencdc.Record {
	records := make([]opencdc.Record, count)
	for i := 0; i < count; i++ {
		records[i] = opencdc.Record{
			Key: opencdc.RawData(uuid.NewString()),
			Payload: opencdc.Change{
				Before: opencdc.RawData{},
				After:  opencdc.RawData(uuid.NewString()),
			},
			Position: opencdc.Position(prefix + "-" + strconv.Itoa(i)),
		}
	}
	return records
}

// unorderedAsserterDestination is the N-source-safe sibling of
// asserterDestination: it checks that the destination receives exactly the
// given records, but (unlike asserterDestination's strict positional match)
// tolerates ANY arrival order. Needed the moment more than one source can
// concurrently write to the SAME destination - see
// pmock.DestinationPluginWithUnorderedRecords's doc for why a fixed-order
// assertion would be flaky, not merely stricter, in that shape.
func unorderedAsserterDestination(
	ctrl *gomock.Controller,
	persister *connector.Persister,
	records []opencdc.Record,
) (*connector.Instance, *pmock.Dispenser) {
	destinationPlugin := pmock.NewConfigurableDestinationPlugin(ctrl,
		pmock.DestinationPluginWithConfigure(),
		pmock.DestinationPluginWithOpen(),
		pmock.DestinationPluginWithRun(),
		pmock.DestinationPluginWithUnorderedRecords(records),
		pmock.DestinationPluginWithTeardown(),
	)

	dest := dummyDestination(persister)

	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseDestination().Return(destinationPlugin, nil)

	return dest, dispenser
}

// TestServiceLifecycle_NxM_StartMovesRecordsStopsCleanly is test 7: an N=2 x
// M=2 pipeline starts, moves every record from every source to every
// destination, and stops cleanly.
func TestServiceLifecycle_NxM_StartMovesRecordsStopsCleanly(t *testing.T) {
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

	recordsA := generateRecordsWithPositionPrefix("A", 3)
	recordsB := generateRecordsWithPositionPrefix("B", 3)

	ctrl := gomock.NewController(t)
	sourceA, sourceADispenser := generatorSource(ctrl, persister, recordsA, nil, false)
	sourceB, sourceBDispenser := generatorSource(ctrl, persister, recordsB, nil, false)

	allRecords := append(append([]opencdc.Record{}, recordsA...), recordsB...)
	dest1, dest1Dispenser := unorderedAsserterDestination(ctrl, persister, allRecords)
	dest2, dest2Dispenser := unorderedAsserterDestination(ctrl, persister, allRecords)

	dlqA, dlqADispenser := asserterDestination(ctrl, persister, nil, false)
	dlqB, dlqBDispenser := asserterDestination(ctrl, persister, nil, false)
	connSvc := &multiDLQConnectorService{
		testConnectorService: testConnectorService{
			sourceA.ID: sourceA,
			sourceB.ID: sourceB,
			dest1.ID:   dest1,
			dest2.ID:   dest2,
		},
		dlqs: []*connector.Instance{dlqA, dlqB},
	}

	pl, err = ps.AddConnector(ctx, pl.ID, sourceA.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, sourceB.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, dest1.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, dest2.ID)
	is.NoErr(err)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		connSvc,
		testProcessorService{},
		testConnectorPluginService{
			sourceA.Plugin: sourceADispenser,
			sourceB.Plugin: sourceBDispenser,
			dest1.Plugin:   dest1Dispenser,
			dest2.Plugin:   dest2Dispenser,
			dlqA.Plugin:    dlqADispenser,
			dlqB.Plugin:    dlqBDispenser,
		},
		ps,
		false,
	)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	waitForRecordsAcked(t, sourceA, recordsA)
	waitForRecordsAcked(t, sourceB, recordsB)
	is.Equal(pipeline.StatusRunning, pl.GetStatus())

	err = ls.Stop(ctx, pl.ID, false)
	is.NoErr(err)
	is.NoErr(ls.WaitPipeline(pl.ID))
	is.Equal(pipeline.StatusUserStopped, pl.GetStatus())
}

// TestServiceLifecycle_NxM_FatalErrorOneDestination_DegradesWholePipeline is
// test 8: a fatal error on ONE of two destinations (shared across two
// sources) must degrade the WHOLE pipeline, matching the single-destination
// and single-source-fatal-error behavior generalized to the N×M shape.
func TestServiceLifecycle_NxM_FatalErrorOneDestination_DegradesWholePipeline(t *testing.T) {
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

	wantErr := cerrors.FatalError(cerrors.New("destination 1 write error"))
	recordsA := generateRecords(1)

	ctrl := gomock.NewController(t)

	// sourceA: NOT generatorSource - the destination fails before the record
	// is ever acked, so the source's ack count is 0, not len(recordsA).
	// assertAckCount=false because that count depends on exactly how far the
	// pipeline got before the fatal error, not a fixed expectation (mirrors
	// TestServiceLifecycle_NSource_PartialGracefulStop_Escalates's identical
	// sourceA pattern).
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

	received := make(chan struct{})
	release := make(chan struct{})
	close(release) // fail immediately, no need to hold the batch in flight first

	dest1Plugin := pmock.NewConfigurableDestinationPlugin(ctrl,
		pmock.DestinationPluginWithConfigure(),
		pmock.DestinationPluginWithOpen(),
		pmock.DestinationPluginWithRun(),
		pmock.DestinationPluginWithControlledError(recordsA, received, release, wantErr),
		pmock.DestinationPluginWithTeardown(),
	)
	dest1 := dummyDestination(persister)
	dest1Dispenser := pmock.NewDispenser(ctrl)
	dest1Dispenser.EXPECT().DispenseDestination().Return(dest1Plugin, nil).Times(1)

	// dest2: the M-branch companion, unblocked and healthy - proves the
	// fatal error on dest1 still degrades the whole pipeline even though
	// dest2 succeeds.
	dest2, dest2Dispenser := asserterDestination(ctrl, persister, recordsA, false)

	dlqA, dlqADispenser := asserterDestination(ctrl, persister, nil, false)
	dlqB, dlqBDispenser := asserterDestination(ctrl, persister, nil, false)
	connSvc := &multiDLQConnectorService{
		testConnectorService: testConnectorService{
			sourceA.ID: sourceA,
			sourceB.ID: sourceB,
			dest1.ID:   dest1,
			dest2.ID:   dest2,
		},
		dlqs: []*connector.Instance{dlqA, dlqB},
	}

	pl, err = ps.AddConnector(ctx, pl.ID, sourceA.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, sourceB.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, dest1.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, dest2.ID)
	is.NoErr(err)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		connSvc,
		testProcessorService{},
		testConnectorPluginService{
			sourceA.Plugin: sourceADispenser,
			sourceB.Plugin: sourceBDispenser,
			dest1.Plugin:   dest1Dispenser,
			dest2.Plugin:   dest2Dispenser,
			dlqA.Plugin:    dlqADispenser,
			dlqB.Plugin:    dlqBDispenser,
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

	event, eventReceived, err := cchan.Chan[FailureEvent](events).RecvTimeout(ctx, 200*time.Millisecond)
	is.NoErr(err)
	is.True(eventReceived)
	is.Equal(pl.ID, event.ID)
}

// TestServiceLifecycle_NxM_TransientErrorOneSource_RecoversAllSourcesAndDestinations
// is test 9: a transient error in one of two sources drives PIPELINE-WIDE
// recovery, which must rebuild ALL N sources AND ALL M destinations from
// scratch - and the shared destinations' poison state (see
// TaskNode.poisoned) is implicitly proven cleared, because the recovered run
// is an entirely NEW TaskNode graph (a fresh, unpoisoned flag by
// construction) that successfully accepts writes again post-recovery.
func TestServiceLifecycle_NxM_TransientErrorOneSource_RecoversAllSourcesAndDestinations(t *testing.T) {
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
	dest1, dest1Dispenser := destinationRecovers(ctrl, persister, healthyRecords)
	dest2, dest2Dispenser := destinationRecovers(ctrl, persister, healthyRecords)

	dlqA, dlqADispenser := dlqDispenserTimes(ctrl, persister, 2)
	dlqB, dlqBDispenser := dlqDispenserTimes(ctrl, persister, 2)
	connSvc := &multiDLQConnectorService{
		testConnectorService: testConnectorService{
			sourceA.ID: sourceA,
			sourceB.ID: sourceB,
			dest1.ID:   dest1,
			dest2.ID:   dest2,
		},
		dlqs: []*connector.Instance{dlqA, dlqB},
	}

	pl, err = ps.AddConnector(ctx, pl.ID, sourceA.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, sourceB.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, dest1.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, dest2.ID)
	is.NoErr(err)

	rec := newStatusRecorder(ps)
	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		connSvc,
		testProcessorService{},
		testConnectorPluginService{
			sourceA.Plugin: sourceADispenser,
			sourceB.Plugin: sourceBDispenser,
			dest1.Plugin:   dest1Dispenser,
			dest2.Plugin:   dest2Dispenser,
			dlqA.Plugin:    dlqADispenser,
			dlqB.Plugin:    dlqBDispenser,
		},
		rec,
		false,
	)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	// Must have passed through Recovering and be Running again.
	waitForRecovered(t, rec, pl)

	// Post-recovery records flow cleanly through the REBUILT shared
	// destinations - proof the poison flag (which never clears in place) is
	// moot here because recovery builds an entirely fresh TaskNode graph.
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

// TestServiceLifecycle_NxM_OneSourceExhausts_OtherStreams_BothDestinationsWritable
// is test 10: source A exhausts a fixed record set (io.EOF) while source B
// (idle, still connected but with nothing to emit) keeps its own worker
// alive. The pipeline must stay Running through A's exit - proven here by A's
// M=2 fan-out completing successfully (both destinations receive and durably
// process every one of A's records) and the pipeline staying Running and
// gracefully stoppable afterward, with B's worker still registered the whole
// time.
//
// B is deliberately idle here, not a second ACTIVELY streaming/acking source,
// for two reasons - one still live, one now historical.
//
// Still live: asserterDestination asserts records arrive in order, which two
// concurrently-producing sources would legitimately violate. The funnel-level
// equivalent, funnel.TestNxM_OneSourceExhausts_OtherStreams_BothDestinationsWritable,
// covers the SAME property with a second GENUINELY concurrent sibling, using an
// unordered destination.
//
// Historical: this test originally used an active sibling and reliably
// reproduced a concurrency bug in pkg/connector.Persister that this N×M work
// discovered - Source.Teardown -> WaitPendingWritesContext -> WaitPendingWrites
// read p.flushWg/p.callbackWg without holding p.m while another connector's Ack
// mutated them under p.m, panicking the process with "sync: WaitGroup is reused
// before previous Wait has returned". Two connectors share one Persister in
// production (connector.Service passes the SAME *Persister to every connector),
// so "one source exits while another acks" hit it reliably.
//
// That bug is FIXED (#2743, fixed in #2749: the WaitGroups were replaced with a
// per-flush generation whose channels a waiter snapshots under p.m). It is no
// longer a reason to keep B idle, and this test could be strengthened to an
// active sibling once asserterDestination gains an unordered mode. Recorded
// here so the constraint is not mistaken for a still-open bug.
func TestServiceLifecycle_NxM_OneSourceExhausts_OtherStreams_BothDestinationsWritable(t *testing.T) {
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

	recordsA := generateRecordsWithPositionPrefix("A", 2)

	ctrl := gomock.NewController(t)

	// Source A: exhausts its fixed record set with io.EOF - nobody calls
	// Stop. See Worker.doTaskAttempt's io.EOF branch.
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

	// Both M=2 destinations must fully and correctly receive A's records -
	// ordered asserterDestination is safe here since A is the only ACTIVE
	// producer (B never writes).
	dest1, dest1Dispenser := asserterDestination(ctrl, persister, recordsA, false)
	dest2, dest2Dispenser := asserterDestination(ctrl, persister, recordsA, false)

	dlqA, dlqADispenser := asserterDestination(ctrl, persister, nil, false)
	dlqB, dlqBDispenser := asserterDestination(ctrl, persister, nil, false)
	connSvc := &multiDLQConnectorService{
		testConnectorService: testConnectorService{
			sourceA.ID: sourceA,
			sourceB.ID: sourceB,
			dest1.ID:   dest1,
			dest2.ID:   dest2,
		},
		dlqs: []*connector.Instance{dlqA, dlqB},
	}

	pl, err = ps.AddConnector(ctx, pl.ID, sourceA.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, sourceB.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, dest1.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, dest2.ID)
	is.NoErr(err)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		connSvc,
		testProcessorService{},
		testConnectorPluginService{
			sourceA.Plugin: sourceADispenser,
			sourceB.Plugin: sourceBDispenser,
			dest1.Plugin:   dest1Dispenser,
			dest2.Plugin:   dest2Dispenser,
			dlqA.Plugin:    dlqADispenser,
			dlqB.Plugin:    dlqBDispenser,
		},
		ps,
		false,
	)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	// A exhausts and acks its own records through BOTH destinations; the
	// pipeline must stay Running (B's worker is still registered on the
	// tomb).
	waitForRecordsAcked(t, sourceA, recordsA)
	is.Equal(pipeline.StatusRunning, pl.GetStatus())

	err = ls.Stop(ctx, pl.ID, false)
	is.NoErr(err)
	is.NoErr(ls.WaitPipeline(pl.ID))
	is.Equal(pipeline.StatusUserStopped, pl.GetStatus())
}

// TestServiceLifecycle_NxM_PartialGracefulStop_Escalates is test 11: it
// generalizes TestServiceLifecycle_NSource_PartialGracefulStop_Escalates (N
// sources x M=1) to N=2 x M=2 - source A's record is held in flight on
// destination 1 while destination 2 (the M-branch companion) succeeds
// normally; source B (idle) arms well within the deadline, source A does
// not, and the partial-arming escalation path must still trigger exactly as
// it does at M=1.
func TestServiceLifecycle_NxM_PartialGracefulStop_Escalates(t *testing.T) {
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

	// dest1: A's record is held in flight (processingLock AND sharedMu held
	// by A's own Do goroutine) until the test releases it. releaseFn is
	// idempotent and deferred so an assertion failure between here and the
	// explicit call still unblocks A's write during the t.Fatal unwind - see
	// TestServiceLifecycle_NSource_PartialGracefulStop_Escalates's identical
	// comment for the full rationale.
	received := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseFn()
	dest1Plugin := pmock.NewConfigurableDestinationPlugin(ctrl,
		pmock.DestinationPluginWithConfigure(),
		pmock.DestinationPluginWithOpen(),
		pmock.DestinationPluginWithRun(),
		pmock.DestinationPluginWithControlledBlock(recordsA, received, release),
		pmock.DestinationPluginWithTeardown(),
	)
	dest1 := dummyDestination(persister)
	dest1Dispenser := pmock.NewDispenser(ctrl)
	dest1Dispenser.EXPECT().DispenseDestination().Return(dest1Plugin, nil).Times(1)

	// dest2: the M-branch companion - unblocked, succeeds normally, proving
	// the M-branch fan-out is genuinely exercised (not just N sources at
	// M=1).
	dest2, dest2Dispenser := asserterDestination(ctrl, persister, recordsA, false)

	dlqA, dlqADispenser := asserterDestination(ctrl, persister, nil, false)
	dlqB, dlqBDispenser := asserterDestination(ctrl, persister, nil, false)
	connSvc := &multiDLQConnectorService{
		testConnectorService: testConnectorService{
			sourceA.ID: sourceA,
			sourceB.ID: sourceB,
			dest1.ID:   dest1,
			dest2.ID:   dest2,
		},
		dlqs: []*connector.Instance{dlqA, dlqB},
	}

	pl, err = ps.AddConnector(ctx, pl.ID, sourceA.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, sourceB.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, dest1.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, dest2.ID)
	is.NoErr(err)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		connSvc,
		testProcessorService{},
		testConnectorPluginService{
			sourceA.Plugin: sourceADispenser,
			sourceB.Plugin: sourceBDispenser,
			dest1.Plugin:   dest1Dispenser,
			dest2.Plugin:   dest2Dispenser,
			dlqA.Plugin:    dlqADispenser,
			dlqB.Plugin:    dlqBDispenser,
		},
		ps,
		false,
	)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	<-received // A's record is in flight; A holds its own processingLock + sharedMu

	// A short deadline A can never meet (its own processingLock is held by
	// its blocked Do goroutine, released only when `release` is closed); B,
	// idle, arms well within it.
	stopCtx, stopCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer stopCancel()

	stopErr := ls.Stop(stopCtx, pl.ID, false)
	is.True(stopErr != nil)

	ce, ok := conduiterr.Get(stopErr)
	is.True(ok)
	is.Equal(ce.Code, CodePartialGracefulStopEscalated)
	is.True(strings.Contains(stopErr.Error(), sourceB.ID)) // names the armed source
	is.True(strings.Contains(stopErr.Error(), sourceA.ID)) // names the unarmed source

	releaseFn()

	_ = ls.WaitPipeline(pl.ID)
	waitForStatus(t, pl, pipeline.StatusDegraded)
}

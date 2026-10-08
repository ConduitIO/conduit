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

// Regression tests for admission by run liveness (#2899 item 2; design doc
// 20261007-lifecycle-status-write-failure, rule R3), the arch-v2 half of
// pkg/lifecycle's admission_test.go. A Start is held inside runPipeline,
// admitted but not yet published, by a destination whose Open blocks.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

const admissionGuard = 10 * time.Second

type admissionRun struct {
	ls        *Service
	pl        *pipeline.Instance
	dispensed atomic.Int32
	failures  chan FailureEvent
	// held is the destination whose Open blocks, for the holdDest-th
	// dispense.
	held *blockingOpenDestination

	mu        sync.Mutex
	published []*runnablePipeline
}

// newAdmissionRun builds a pipeline whose source and destination plugins can
// be dispensed any number of times. With failFirst, the first source fails
// with a transient error (no stop); every other source idles until stopped.
// The holdDest-th destination (0 for none) blocks in Open until
// r.held.release is closed.
func newAdmissionRun(t *testing.T, failFirst bool, holdDest int32) *admissionRun {
	t.Helper()
	ctx, killAll := context.WithCancel(context.Background())
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	if err != nil {
		t.Fatal(err)
	}
	r := &admissionRun{pl: pl, failures: make(chan FailureEvent, 8)}
	ctrl := gomock.NewController(t)

	source := dummySource(persister)
	srcDispenser := pmock.NewDispenser(ctrl)
	srcDispenser.EXPECT().DispenseSource().DoAndReturn(func() (connectorPlugin.SourcePlugin, error) {
		var srcErr error
		if r.dispensed.Add(1) == 1 && failFirst {
			srcErr = cerrors.New("lost connection to source")
		}
		return pmock.NewConfigurableSourcePlugin(ctrl,
			pmock.SourcePluginWithConfigure(),
			pmock.SourcePluginWithOpen(),
			pmock.SourcePluginWithRun(),
			pmock.SourcePluginWithRecords(nil, srcErr),
			pmock.SourcePluginWithAcks(0, false),
			pmock.SourcePluginWithTeardown(),
		), nil
	}).AnyTimes()

	destination := dummyDestination(persister)
	destDispenser := pmock.NewDispenser(ctrl)
	var destN atomic.Int32
	r.held = &blockingOpenDestination{entered: make(chan struct{}), release: make(chan struct{})}
	destDispenser.EXPECT().DispenseDestination().DoAndReturn(func() (connectorPlugin.DestinationPlugin, error) {
		plugin := pmock.NewConfigurableDestinationPlugin(ctrl,
			pmock.DestinationPluginWithConfigure(),
			pmock.DestinationPluginWithOpen(),
			pmock.DestinationPluginWithRun(),
			pmock.DestinationPluginWithRecords(nil),
			pmock.DestinationPluginWithTeardown(),
		)
		if destN.Add(1) == holdDest {
			r.held.DestinationPlugin = plugin
			return r.held, nil
		}
		return plugin, nil
	}).AnyTimes()

	dlq, dlqDispenser := dlqDispenserAnyTimes(ctrl, persister)
	pl.DLQ.Plugin = dlq.Plugin
	if pl, err = ps.AddConnector(ctx, pl.ID, source.ID); err != nil {
		t.Fatal(err)
	}
	if pl, err = ps.AddConnector(ctx, pl.ID, destination.ID); err != nil {
		t.Fatal(err)
	}
	r.pl = pl

	r.ls = NewService(logger, testErrRecoveryCfg(),
		testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
		testProcessorService{},
		testConnectorPluginService{source.Plugin: srcDispenser, destination.Plugin: destDispenser, dlq.Plugin: dlqDispenser},
		ps, false,
	)
	r.ls.OnFailure(func(e FailureEvent) { r.failures <- e })
	r.ls.testWorkersReleased = func(rp *runnablePipeline) {
		r.mu.Lock()
		r.published = append(r.published, rp)
		r.mu.Unlock()
	}

	t.Cleanup(func() { stopAndWaitPersister(t, killAll, persister) })
	t.Cleanup(func() {
		select {
		case <-r.held.release:
		default:
			close(r.held.release)
		}
		for _, rp := range r.runs() {
			if rp.t.Alive() {
				rp.t.Kill(cerrors.FatalError(pipeline.ErrForceStop))
				select {
				case <-rp.t.Dead():
				case <-time.After(admissionGuard):
					t.Error("a run did not die after a force kill")
				}
			}
		}
	})
	return r
}

func dlqDispenserAnyTimes(ctrl *gomock.Controller, persister *connector.Persister) (*connector.Instance, *pmock.Dispenser) {
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
	}).AnyTimes()
	return dest, dispenser
}

func (r *admissionRun) runs() []*runnablePipeline {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*runnablePipeline(nil), r.published...)
}

func (r *admissionRun) alive() []*runnablePipeline {
	var out []*runnablePipeline
	for _, rp := range r.runs() {
		if rp.t.Alive() {
			out = append(out, rp)
		}
	}
	return out
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(admissionGuard):
		t.Fatalf("timed out after %s waiting for %s", admissionGuard, what)
	}
}

// TestServiceLifecycle_Recovery_ExternalStartRacesPendingRestart is the I3
// regression test (#2899 item 2). The restart's unlocked "am I still the live
// run" guard found the failed run while an operator's Start was building, and
// started a second run: two runs on one source. Now the operator's Start
// takes the pipeline over and the pending restart is abandoned.
func TestServiceLifecycle_Recovery_ExternalStartRacesPendingRestart(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	// Dispense 1: the run that fails. Dispense 2: the operator's run, held
	// in its destination's Open (admitted, not published).
	r := newAdmissionRun(t, true, 2)

	restartWaiting := make(chan struct{})
	releaseRestart := make(chan struct{})
	var restartOnce sync.Once
	r.ls.testAfterBackoffWait = func(*runnablePipeline) {
		restartOnce.Do(func() { close(restartWaiting) })
		<-releaseRestart
	}
	t.Cleanup(func() {
		select {
		case <-releaseRestart:
		default:
			close(releaseRestart)
		}
	})

	is.NoErr(r.ls.Start(ctx, r.pl.ID))
	first := r.runs()[0]
	waitFor(t, restartWaiting, "the restart to finish its backoff wait")

	external := make(chan error, 1)
	go func() { external <- r.ls.Start(ctx, r.pl.ID) }()
	waitFor(t, r.held.entered, "the operator's Start to open its destination")

	close(releaseRestart)
	waitFor(t, first.t.Dead(), "the failed run's cleanup to finish")

	close(r.held.release)
	select {
	case err := <-external:
		is.NoErr(err)
	case <-time.After(admissionGuard):
		t.Fatal("the operator's Start did not return")
	}

	if alive := r.alive(); len(alive) != 1 {
		t.Fatalf("%d runs of one pipeline are live (want 1): two runs read one source (#2899 item 2)", len(alive))
	}
	is.Equal(r.dispensed.Load(), int32(2)) // the failed run and the operator's; no restart
	live, ok := r.ls.runningPipelines.Get(r.pl.ID)
	is.True(ok)
	is.True(live == r.alive()[0])
	is.Equal(len(r.failures), 0)

	is.NoErr(r.ls.Stop(ctx, r.pl.ID, false))
	is.NoErr(r.ls.WaitPipeline(r.pl.ID))
	is.Equal(len(r.alive()), 0)
	is.Equal(r.pl.GetStatus(), pipeline.StatusUserStopped)
}

// TestServiceLifecycle_ConcurrentStarts_OneWins: a second Start while the
// first is still building is refused with ErrPipelineRunning instead of
// building a second run.
func TestServiceLifecycle_ConcurrentStarts_OneWins(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	r := newAdmissionRun(t, false, 1)

	first := make(chan error, 1)
	go func() { first <- r.ls.Start(ctx, r.pl.ID) }()
	waitFor(t, r.held.entered, "the first Start to open its destination")

	err := r.ls.Start(ctx, r.pl.ID)
	is.True(cerrors.Is(err, pipeline.ErrPipelineRunning))
	is.True(r.ls.IsActive(r.pl.ID)) // a starting run counts

	close(r.held.release)
	is.NoErr(<-first)
	is.Equal(len(r.alive()), 1)
	is.Equal(r.dispensed.Load(), int32(1))

	is.NoErr(r.ls.Stop(ctx, r.pl.ID, false))
	is.NoErr(r.ls.WaitPipeline(r.pl.ID))
	is.True(!r.ls.IsActive(r.pl.ID))
}

// TestServiceLifecycle_StopWhileStarting_StopsRun: a Stop that arrives while
// the run is being built is applied when the run is published.
func TestServiceLifecycle_StopWhileStarting_StopsRun(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	r := newAdmissionRun(t, false, 1)

	started := make(chan error, 1)
	go func() { started <- r.ls.Start(ctx, r.pl.ID) }()
	waitFor(t, r.held.entered, "Start to open its destination")

	is.NoErr(r.ls.Stop(ctx, r.pl.ID, false))
	waited := make(chan error, 1)
	go func() { waited <- r.ls.WaitPipeline(r.pl.ID) }()

	close(r.held.release)
	is.NoErr(<-started)
	select {
	case err := <-waited:
		is.NoErr(err)
	case <-time.After(admissionGuard):
		t.Fatal("the run stopped while starting did not finish")
	}
	is.Equal(len(r.alive()), 0)
	is.Equal(r.pl.GetStatus(), pipeline.StatusUserStopped)
	is.Equal(len(r.failures), 0)
}

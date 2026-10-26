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
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/cchan"
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

// TestInit_ArchV2BatchingRefusalRecordedAsDegraded is #2989: a pipeline the
// previous process left StatusSystemStopped is refused at boot (Init) and the
// refusal becomes its status, Degraded with the coded error, instead of a
// plain stopped pipeline with no error. Nothing is dispensed (the harness's
// dispensers fail the test otherwise), so nothing is read or acked.
func TestInit_ArchV2BatchingRefusalRecordedAsDegraded(t *testing.T) {
	is := is.New(t)
	h := newBatchingHarness(t, map[string]string{"sdk.batch.size": "100"}, nil, false)
	h.pl.SetStatus(pipeline.StatusSystemStopped)

	events := make(chan FailureEvent, 1)
	h.ls.OnFailure(func(e FailureEvent) { events <- e })

	err := h.ls.Init(context.Background())
	_ = requireBatchingRefusal(t, err) // Init still returns the refusal

	is.Equal(h.pl.GetStatus(), pipeline.StatusDegraded)
	is.True(strings.HasPrefix(h.pl.Error, "pipeline.archv2_destination_batching_unsupported: ")) // the code
	is.True(strings.Contains(h.pl.Error, h.dest.ID))                                             // the connector
	is.True(strings.Contains(h.pl.Error, "sdk.batch.size=100"))                                  // the setting
	is.True(strings.Contains(h.pl.Error, "(suggestion: remove sdk.batch.size"))                  // the fix

	_, running := h.ls.runningPipelines.Get(h.pl.ID)
	is.True(!running)
	select {
	case e := <-events:
		t.Fatalf("a boot refusal must not notify OnFailure (exit-on-degraded): %v", e)
	default:
	}

	// Degraded is not auto-started: a second boot does not retry the refusal.
	is.NoErr(h.ls.Init(context.Background()))
	is.Equal(h.pl.GetStatus(), pipeline.StatusDegraded)
}

// TestStartOnBoot_ArchV2BatchingRefusalRecordedAsDegraded covers the
// provisioning boot path, which calls StartOnBoot for a config pipeline whose
// status is running.
func TestStartOnBoot_ArchV2BatchingRefusalRecordedAsDegraded(t *testing.T) {
	is := is.New(t)
	h := newBatchingHarness(t, nil, map[string]string{"sdk.batch.size": "5"}, false)

	_ = requireBatchingRefusal(t, h.ls.StartOnBoot(context.Background(), h.pl.ID))

	is.Equal(h.pl.GetStatus(), pipeline.StatusDegraded)
	is.True(strings.Contains(h.pl.Error, "dead letter queue"))
}

// TestStart_ArchV2BatchingRefusalLeavesStatusUntouched: the API path is not
// changed by #2989. The caller gets the refusal; the stored status and error
// stay as they were.
func TestStart_ArchV2BatchingRefusalLeavesStatusUntouched(t *testing.T) {
	is := is.New(t)
	h := newBatchingHarness(t, map[string]string{"sdk.batch.size": "100"}, nil, false)
	h.pl.SetStatus(pipeline.StatusUserStopped)

	_ = requireBatchingRefusal(t, h.ls.Start(context.Background(), h.pl.ID))

	is.Equal(h.pl.GetStatus(), pipeline.StatusUserStopped)
	is.Equal(h.pl.Error, "")
}

// TestStartOnBoot_OtherErrorsLeaveStatusUntouched: only the config refusal is
// recorded. Any other start failure (here a missing pipeline connector the
// build trips on) leaves the status for the next boot to retry.
func TestStartOnBoot_OtherErrorsLeaveStatusUntouched(t *testing.T) {
	is := is.New(t)
	h := newBatchingHarness(t, nil, nil, false)
	// A connector ID with no stored connector: the build fails on it.
	h.pl.ConnectorIDs = append([]string{"missing"}, h.pl.ConnectorIDs...)
	h.pl.SetStatus(pipeline.StatusSystemStopped)

	err := h.ls.StartOnBoot(context.Background(), h.pl.ID)
	is.True(err != nil)
	is.Equal(h.pl.GetStatus(), pipeline.StatusSystemStopped)
	is.Equal(h.pl.Error, "")
}

// TestServiceLifecycle_Recovery_ArchV2BatchingRefusalEndsDegraded is the
// recovery-restart test #2987 left out (#2989): the pipeline starts clean, the
// source fails with a recoverable error, and while the run waits out its
// backoff the destination's stored config gains sdk.batch.size=100. The
// restart is refused, and the pipeline ends Degraded with the refusal as its
// error. The source and destination are dispensed exactly once: the refused
// restart opened nothing.
func TestServiceLifecycle_Recovery_ArchV2BatchingRefusalEndsDegraded(t *testing.T) {
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

	ctrl := gomock.NewController(t)
	source, srcDispenser := failingSourceTimes(ctrl, persister, cerrors.New("source flapped"), 1)
	destination, destDispenser := destinationTimes(ctrl, persister, 1)
	dlq, dlqDispenser := dlqDispenserTimes(ctrl, persister, 1)
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
		},
		ps, false)

	// The config changes between the first run and its restart. Through the
	// API this is refused while a run waits out its backoff (#2961), so the
	// test writes the stored connector directly, at the restart's
	// reservation, after the first run's workers are gone.
	ls.testAtReservation = func(_ string, predecessor *runnablePipeline) {
		if predecessor == nil {
			return
		}
		destination.Lock()
		destination.Config.Settings = map[string]string{"sdk.batch.size": "100"}
		destination.Unlock()
	}

	events := make(chan FailureEvent, 1)
	ls.OnFailure(func(e FailureEvent) { events <- e })

	is.NoErr(ls.Start(ctx, pl.ID))

	event, received, err := cchan.Chan[FailureEvent](events).RecvTimeout(ctx, 10*time.Second)
	is.NoErr(err)
	is.True(received)
	_ = requireBatchingRefusal(t, event.Error)

	is.Equal(pl.GetStatus(), pipeline.StatusDegraded)
	is.True(strings.Contains(pl.Error, "sdk.batch.size=100"))
	is.True(strings.Contains(pl.Error, destination.ID))
	_, running := ls.runningPipelines.Get(pl.ID)
	is.True(!running)
}

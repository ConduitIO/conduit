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
	"testing"
	"time"

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

// TestServiceLifecycle_WaitPipeline_WaitsForCleanupTail is the arch-v2 port of
// pkg/lifecycle's #3087 regression test of the same name. The cleanup
// goroutine removes the run from runningPipelines and only then calls the
// OnFailure handlers. WaitPipeline used to take its "already cleaned up" path
// in that window and return while the handlers were still running, so a
// caller that WaitPipeline told the pipeline was stopped could still race a
// goroutine of the run.
//
// The first handler delivers the event (the barrier: the entry is gone once
// it is in); the second blocks until the test releases it. WaitPipeline must
// not return before then. The 200ms window is negative evidence only: a
// correct run cannot return inside it, because the handler is held by a
// channel only the test closes.
func TestServiceLifecycle_WaitPipeline_WaitsForCleanupTail(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	t.Cleanup(func() { stopAndWaitPersister(t, killAll, persister) })

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	wantErr := cerrors.FatalError(cerrors.New("source connector error"))
	ctrl := gomock.NewController(t)
	source, sourceDispenser := generatorSourceFatalError(ctrl, persister, nil, wantErr)
	destination, destDispenser := asserterDestination(ctrl, persister, nil, false)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin
	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
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
	release := make(chan struct{})
	abort := make(chan struct{})
	t.Cleanup(func() { close(abort) })
	// Handlers run in registration order.
	ls.OnFailure(func(e FailureEvent) { events <- e })
	ls.OnFailure(func(FailureEvent) {
		select {
		case <-release:
		case <-abort:
		}
	})

	is.NoErr(ls.Start(context.Background(), pl.ID))

	select {
	case <-events:
	case <-time.After(10 * time.Second):
		t.Fatal("no OnFailure event within 10s")
	}
	if _, live := ls.runningPipelines.Get(pl.ID); live {
		t.Fatal("the run is still in runningPipelines after OnFailure; the barrier did not hold")
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- ls.WaitPipeline(pl.ID) }()
	select {
	case err := <-waitDone:
		close(release)
		t.Fatalf("WaitPipeline returned (%v) while the cleanup goroutine was still in OnFailure (#3087)", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	select {
	case err = <-waitDone:
	case <-time.After(10 * time.Second):
		t.Fatal("WaitPipeline did not return within 10s after the handler was released")
	}
	is.True(cerrors.Is(err, wantErr))
	is.Equal(pipeline.StatusDegraded, pl.GetStatus())
}

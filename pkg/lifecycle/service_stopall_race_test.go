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

// Review finding B1 on #2912: a run is published to runningPipelines before
// it announces StatusRunning, so for a moment its entry carries the status of
// the pipeline's previous run (UserStopped after a Stop, SystemStopped at
// boot, Degraded, or the zero status of a new pipeline). StopAll used to skip
// entries whose status was not Running or Recovering, so a StopAll landing in
// that window missed the run; the run had already read the shutdown flag (it
// was not set yet), so it did not stop itself either. It then went Running
// and kept running past Wait, and the runtime closed the database under it.
//
// The tests hold the window open deterministically: StopAll is called from
// inside the run's first UpdateStatus(StatusRunning), before the status is
// applied.

import (
	"context"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// assertNothingLiveAfterWait calls Wait and checks that it returned in time
// and that no run is live afterwards.
func assertNothingLiveAfterWait(t *testing.T, ls *Service, statuses func() []pipeline.Status) {
	t.Helper()
	if err := ls.Wait(terminalStatusGuard); err == context.DeadlineExceeded {
		t.Fatalf("run still live after StopAll+Wait (%v); statuses=%v (#2912 B1)", err, statuses())
	}
	for _, rp := range ls.runningPipelines.All() {
		if rp.t != nil && rp.t.Alive() {
			t.Fatalf("run of pipeline %s live after Wait returned; statuses=%v (#2912 B1)", rp.pipeline.ID, statuses())
		}
	}
}

func TestServiceLifecycle_StopAllDuringRunningAnnouncement(t *testing.T) {
	testCases := []struct {
		name  string
		prior pipeline.Status
	}{
		{name: "new pipeline", prior: 0},
		{name: "after a user stop", prior: pipeline.StatusUserStopped},
		{name: "at boot", prior: pipeline.StatusSystemStopped},
		{name: "after degrading", prior: pipeline.StatusDegraded},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			tr := newTerminalRun(t, testErrRecoveryCfg(), false, newScriptedNode(nil, nil))
			tr.pl.SetStatus(tc.prior)
			inner := tr.rec.onUpdate
			tr.rec.onUpdate = func(s pipeline.Status, nth int) error {
				if s == pipeline.StatusRunning && nth == 1 {
					tr.ls.StopAll(context.Background(), pipeline.ErrGracefulShutdown)
				}
				return inner(s, nth)
			}
			tr.start(t)

			assertNothingLiveAfterWait(t, tr.ls, tr.statuses)
			is.Equal([]pipeline.Status{pipeline.StatusRunning, pipeline.StatusSystemStopped}, tr.statuses())
		})
	}
}

// TestServiceLifecycle_StopAllDuringRunningAnnouncement_RealStarts covers the
// two production callers that start a run whose pipeline has a non-Running
// prior status: a restart after Stop (the shape of
// provisioning.ApplyPlanLive's stop-drain-restart, prior status UserStopped)
// and boot Init racing SIGTERM (prior status SystemStopped).
func TestServiceLifecycle_StopAllDuringRunningAnnouncement_RealStarts(t *testing.T) {
	testCases := []struct {
		name string
		// start starts the pipeline the way the caller does; runningNth is
		// which StatusRunning write is the one to hold.
		start      func(ctx context.Context, ls *Service, ps *pipeline.Service, pl *pipeline.Instance) error
		runningNth int
	}{{
		name: "restart after Stop (ApplyPlanLive)",
		start: func(ctx context.Context, ls *Service, _ *pipeline.Service, pl *pipeline.Instance) error {
			if err := ls.Start(ctx, pl.ID); err != nil {
				return err
			}
			if err := ls.StopAndWait(ctx, pl.ID); err != nil {
				return err
			}
			return ls.Start(ctx, pl.ID)
		},
		runningNth: 2,
	}, {
		name: "boot Init",
		start: func(ctx context.Context, ls *Service, ps *pipeline.Service, pl *pipeline.Instance) error {
			if err := ps.UpdateStatus(ctx, pl.ID, pipeline.StatusSystemStopped, ""); err != nil {
				return err
			}
			return ls.Init(ctx)
		},
		runningNth: 1,
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			logger := log.New(zerolog.Nop())
			db := &inmemory.DB{}
			persister := connector.NewPersister(logger, db, time.Second, 3)

			ps := pipeline.NewService(logger, db)
			pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
			is.NoErr(err)

			ctrl := gomock.NewController(t)
			source, srcDispenser, _ := countingSource(ctrl, persister, noRecords)
			destination, destDispenser := countingDestination(ctrl, persister, noRecords)
			dlq, dlqDispenser := countingDestination(ctrl, persister, noRecords)
			pl.DLQ.Plugin = dlq.Plugin
			pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
			is.NoErr(err)
			pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
			is.NoErr(err)

			var ls *Service
			rec := newStatusRecorder(ps)
			rec.onUpdate = func(s pipeline.Status, nth int) error {
				if s == pipeline.StatusRunning && nth == tc.runningNth {
					ls.StopAll(context.Background(), pipeline.ErrGracefulShutdown)
				}
				return nil
			}
			ls = NewService(logger, testErrRecoveryCfg(),
				testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
				testProcessorService{},
				testConnectorPluginService{source.Plugin: srcDispenser, destination.Plugin: destDispenser, dlq.Plugin: dlqDispenser},
				rec,
			)
			// Failure-path cleanup: stop a run that outlived the test body.
			t.Cleanup(func() {
				for _, rp := range ls.runningPipelines.All() {
					if rp.t != nil && rp.t.Alive() {
						_ = ls.stopForceful(context.Background(), rp)
						select {
						case <-rp.t.Dead():
						case <-time.After(terminalStatusGuard):
						}
					}
				}
			})

			is.NoErr(tc.start(ctx, ls, ps, pl))
			statuses := func() []pipeline.Status {
				rec.mu.Lock()
				defer rec.mu.Unlock()
				return append([]pipeline.Status(nil), rec.statuses...)
			}
			assertNothingLiveAfterWait(t, ls, statuses)
			is.Equal(pipeline.StatusSystemStopped, pl.GetStatus())
		})
	}
}

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

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/lifecycle/stream"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/matryer/is"
)

// TestServiceLifecycle_StatusWriteFails_TerminalTailRuns is the I5 regression
// test (#2899 item 4). Every arm of the cleanup goroutine used to return as
// soon as its status write failed, skipping the terminal error record, the
// removal from runningPipelines and the OnFailure notification. A fatal error
// whose Degraded write failed therefore never reached OnFailure, so
// exit-on-degraded silently did not trip, and the dead run stayed in
// runningPipelines. The status write is now a report: the tail always runs,
// and the cleanup goroutine returns the run's terminal error, not the status
// write's.
//
// The Recovering case is the same rule one step earlier: a failed Recovering
// write sent the run to Degraded with the store error instead of recovering.
func TestServiceLifecycle_StatusWriteFails_TerminalTailRuns(t *testing.T) {
	fatalErr := cerrors.FatalError(cerrors.New("source schema is invalid"))
	transientErr := cerrors.New("lost connection to source")
	drainErr := cerrors.New("destination write failed while draining")

	testCases := []struct {
		name  string
		cfg   *ErrRecoveryCfg
		node  func() stream.Node
		fail  pipeline.Status
		stop  bool // stop the run with a user Stop
		final pipeline.Status
		// wantErr is the terminal error: WaitPipeline's result and the
		// cleanup goroutine's return value. nil for a clean stop.
		wantErr error
		// notified: the run is a failure, so OnFailure fires with wantErr.
		notified bool
		// attempts is the expected number of recovery attempts.
		attempts int64
	}{{
		name:     "fatal error, Degraded write fails",
		cfg:      testErrRecoveryCfg(),
		node:     func() stream.Node { return newScriptedNode(fatalErr, nil) },
		fail:     pipeline.StatusDegraded,
		final:    pipeline.StatusDegraded,
		wantErr:  fatalErr,
		notified: true,
	}, {
		name:  "user stop, UserStopped write fails",
		cfg:   testErrRecoveryCfg(),
		node:  func() stream.Node { return newScriptedNode(nil, nil) },
		fail:  pipeline.StatusUserStopped,
		stop:  true,
		final: pipeline.StatusUserStopped,
	}, {
		name:    "stop with a drain error, UserStopped write fails",
		cfg:     testErrRecoveryCfg(),
		node:    func() stream.Node { return newDrainFailNode(drainErr) },
		fail:    pipeline.StatusUserStopped,
		stop:    true,
		final:   pipeline.StatusUserStopped,
		wantErr: drainErr,
	}, {
		name:     "recovery exhausted, Degraded write fails",
		cfg:      noRetryRecoveryCfg(),
		node:     func() stream.Node { return newScriptedNode(transientErr, nil) },
		fail:     pipeline.StatusDegraded,
		final:    pipeline.StatusDegraded,
		wantErr:  pipeline.ErrPipelineCannotRecover,
		notified: true,
		attempts: 1,
	}, {
		name:     "Recovering write fails, recovery still runs",
		cfg:      noRetryRecoveryCfg(),
		node:     func() stream.Node { return newScriptedNode(transientErr, nil) },
		fail:     pipeline.StatusRecovering,
		final:    pipeline.StatusDegraded,
		wantErr:  pipeline.ErrPipelineCannotRecover,
		notified: true,
		attempts: 1,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			r := newStatusFaultRunCfg(t, tc.cfg, &statusFaultDB{DB: &inmemory.DB{}}, failNth(tc.fail, 1), tc.node())

			is.NoErr(r.ls.runPipeline(context.Background(), r.rp))
			if tc.stop {
				is.NoErr(r.ls.Stop(context.Background(), r.pl.ID, false))
			}
			waitClosed(t, r.rp.t.Dead(), "the run to finish")
			is.Equal(r.ps.failedStatuses(), []pipeline.Status{tc.fail}) // the fault engaged

			// The cleanup goroutine returned the run's terminal error, not
			// the status write's. The tomb keeps the first error it saw, so
			// this is only visible when no node failed.
			gotErr := r.rp.t.Err()
			is.True(!cerrors.Is(gotErr, errStatusStoreDown))
			if tc.wantErr == nil {
				is.NoErr(gotErr)
			}

			// The terminal error is recorded for WaitPipeline ...
			waitErr := r.ls.WaitPipeline(r.pl.ID)
			if tc.wantErr == nil {
				is.NoErr(waitErr)
			} else {
				is.True(cerrors.Is(waitErr, tc.wantErr))
			}
			// ... the dead run is no longer registered ...
			_, live := r.ls.runningPipelines.Get(r.pl.ID)
			is.True(!live)
			// ... and a failure reaches OnFailure, so exit-on-degraded trips.
			events := r.failureEvents()
			if tc.notified {
				is.Equal(len(events), 1)
				is.True(cerrors.Is(events[0].Error, tc.wantErr))
			} else {
				is.Equal(len(events), 0)
			}

			is.Equal(r.pl.GetStatus(), tc.final) // memory has the intended status
			is.Equal(r.rp.recoveryAttempts.Load(), tc.attempts)
			is.NoErr(r.ls.Wait(terminalStatusGuard))
		})
	}
}

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
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	lifecyclev1 "github.com/conduitio/conduit/pkg/lifecycle"
	"github.com/conduitio/conduit/pkg/pipeline"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// TestServiceLifecycle_StatusWriteFails_TerminalTailRuns is the I5 regression
// test (#2899 item 4), the arch-v2 half of pkg/lifecycle's test of the same
// name. Every arm of the cleanup goroutine used to return as soon as its
// status write failed, skipping the terminal error record, the removal from
// runningPipelines and the OnFailure notification: a fatal error whose
// Degraded write failed never tripped exit-on-degraded, and the dead run
// stayed registered. A failed Recovering write likewise degraded the pipeline
// with the store error instead of recovering it.
func TestServiceLifecycle_StatusWriteFails_TerminalTailRuns(t *testing.T) {
	fatalErr := cerrors.FatalError(cerrors.New("source connector error"))
	transientErr := cerrors.New("lost connection to source")

	type connectors func(ctrl *gomock.Controller, persister *connector.Persister) (src, dst, dlq *connector.Instance, srcD, dstD, dlqD *pmock.Dispenser)

	// healthy moves records until it is stopped.
	healthy := func(records []opencdc.Record) connectors {
		return func(ctrl *gomock.Controller, persister *connector.Persister) (*connector.Instance, *connector.Instance, *connector.Instance, *pmock.Dispenser, *pmock.Dispenser, *pmock.Dispenser) {
			src, srcD := generatorSource(ctrl, persister, records, nil, false)
			dst, dstD := asserterDestination(ctrl, persister, records, false)
			dlq, dlqD := asserterDestination(ctrl, persister, nil, false)
			return src, dst, dlq, srcD, dstD, dlqD
		}
	}
	fatal := func(ctrl *gomock.Controller, persister *connector.Persister) (*connector.Instance, *connector.Instance, *connector.Instance, *pmock.Dispenser, *pmock.Dispenser, *pmock.Dispenser) {
		records := generateRecords(10)
		src, srcD := generatorSourceFatalError(ctrl, persister, records, fatalErr)
		dst, dstD := asserterDestination(ctrl, persister, records, false)
		dlq, dlqD := asserterDestination(ctrl, persister, nil, false)
		return src, dst, dlq, srcD, dstD, dlqD
	}
	// transient fails once; MaxRetries 0 makes the recovery give up on its
	// first attempt without a restart, so the run ends deterministically.
	transient := func(ctrl *gomock.Controller, persister *connector.Persister) (*connector.Instance, *connector.Instance, *connector.Instance, *pmock.Dispenser, *pmock.Dispenser, *pmock.Dispenser) {
		src, srcD := failingSourceTimes(ctrl, persister, transientErr, 1)
		dst, dstD := destinationTimes(ctrl, persister, 1)
		dlq, dlqD := dlqDispenserTimes(ctrl, persister, 1)
		return src, dst, dlq, srcD, dstD, dlqD
	}

	testCases := []struct {
		name       string
		connectors connectors
		maxRetries int64
		fail       pipeline.Status
		stop       bool
		final      pipeline.Status
		wantErr    error // nil for a clean stop
		notified   bool
	}{{
		name:       "fatal error, Degraded write fails",
		connectors: fatal,
		maxRetries: lifecyclev1.InfiniteRetriesErrRecovery,
		fail:       pipeline.StatusDegraded,
		final:      pipeline.StatusDegraded,
		wantErr:    fatalErr,
		notified:   true,
	}, {
		name:       "user stop, UserStopped write fails",
		connectors: healthy(generateRecords(10)),
		maxRetries: lifecyclev1.InfiniteRetriesErrRecovery,
		fail:       pipeline.StatusUserStopped,
		stop:       true,
		final:      pipeline.StatusUserStopped,
	}, {
		name:       "recovery exhausted, Degraded write fails",
		connectors: transient,
		maxRetries: 0,
		fail:       pipeline.StatusDegraded,
		final:      pipeline.StatusDegraded,
		wantErr:    pipeline.ErrPipelineCannotRecover,
		notified:   true,
	}, {
		name:       "Recovering write fails, recovery still runs",
		connectors: transient,
		maxRetries: 0,
		fail:       pipeline.StatusRecovering,
		final:      pipeline.StatusDegraded,
		wantErr:    pipeline.ErrPipelineCannotRecover,
		notified:   true,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
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

			ctrl := gomock.NewController(t)
			src, dst, dlq, srcD, dstD, dlqD := tc.connectors(ctrl, persister)
			pl.DLQ.Plugin = dlq.Plugin
			pl, err = inner.AddConnector(ctx, pl.ID, src.ID)
			is.NoErr(err)
			pl, err = inner.AddConnector(ctx, pl.ID, dst.ID)
			is.NoErr(err)

			cfg := testErrRecoveryCfg()
			cfg.MaxRetries = tc.maxRetries
			ps := newFailStatusWrites(inner, failNth(tc.fail, 1))
			ls := NewService(
				logger,
				cfg,
				testConnectorService{src.ID: src, dst.ID: dst, testDLQID: dlq},
				testProcessorService{},
				testConnectorPluginService{src.Plugin: srcD, dst.Plugin: dstD, dlq.Plugin: dlqD},
				ps,
				false,
			)
			failures := make(chan FailureEvent, 4)
			ls.OnFailure(func(e FailureEvent) { failures <- e })
			// Capture the run inside runPipeline: a failing run can finish
			// and leave runningPipelines before Start returns.
			var rp *runnablePipeline
			ls.testWorkersReleased = func(r *runnablePipeline) { rp = r }

			is.NoErr(ls.Start(ctx, pl.ID))
			is.True(rp != nil)
			if tc.stop {
				waitForRecordsAcked(t, src, generateRecords(10))
				is.NoErr(ls.Stop(ctx, pl.ID, false))
			}
			select {
			case <-rp.t.Dead():
			case <-time.After(10 * time.Second):
				t.Fatal("the run did not finish")
			}
			is.Equal(ps.failedStatuses(), []pipeline.Status{tc.fail}) // the fault engaged

			// The cleanup goroutine returned the run's terminal error, not
			// the status write's.
			is.True(!cerrors.Is(rp.t.Err(), errStatusStoreDown))
			if tc.wantErr == nil {
				is.NoErr(rp.t.Err())
			}

			// The terminal error is recorded for WaitPipeline ...
			waitErr := ls.WaitPipeline(pl.ID)
			if tc.wantErr == nil {
				is.NoErr(waitErr)
			} else {
				is.True(cerrors.Is(waitErr, tc.wantErr))
			}
			// ... the dead run is no longer registered ...
			_, live := ls.runningPipelines.Get(pl.ID)
			is.True(!live)
			// ... and a failure reaches OnFailure, so exit-on-degraded trips.
			if tc.notified {
				is.Equal(len(failures), 1)
				e := <-failures
				is.True(cerrors.Is(e.Error, tc.wantErr))
			} else {
				is.Equal(len(failures), 0)
			}
			is.Equal(pl.GetStatus(), tc.final)
		})
	}
}

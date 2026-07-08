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

package provisioning

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/conduit/pkg/provisioning/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// expectExportRunning is expectExport's counterpart for a RUNNING pipeline.
// It can't reuse expectExport as-is: expectExport registers its own
// AnyTimes() pipSrv.Get(gomock.Any(), cfg.ID) expectation returning a
// non-running instance, and gomock's callSet.FindMatch resolves a method's
// expected calls in registration order, returning the first match — so a
// second, more specific Get expectation registered afterward (e.g. "running
// this time") would never be reached; the first AnyTimes() wildcard always
// wins. Every ApplyPlanLive test against a running pipeline therefore needs
// its own single Get registration, already running, serving both Plan's
// Export call and ApplyPlanLive's own running-status check.
func expectExportRunning(pipSrv *mock.PipelineService, connSrv *mock.ConnectorService, procSrv *mock.ProcessorService, cfg config.Pipeline) *pipeline.Instance {
	connIDs := make([]string, len(cfg.Connectors))
	for i, c := range cfg.Connectors {
		connIDs[i] = c.ID
	}
	procIDs := make([]string, len(cfg.Processors))
	for i, p := range cfg.Processors {
		procIDs[i] = p.ID
	}

	instance := &pipeline.Instance{
		ID:           cfg.ID,
		Config:       pipeline.Config{Name: cfg.Name, Description: cfg.Description},
		ConnectorIDs: connIDs,
		ProcessorIDs: procIDs,
	}
	if cfg.DLQ.WindowSize != nil && cfg.DLQ.WindowNackThreshold != nil {
		instance.DLQ = pipeline.DLQ{
			Plugin:              cfg.DLQ.Plugin,
			Settings:            cfg.DLQ.Settings,
			WindowSize:          *cfg.DLQ.WindowSize,
			WindowNackThreshold: *cfg.DLQ.WindowNackThreshold,
		}
	}
	instance.SetStatus(pipeline.StatusRunning)
	pipSrv.EXPECT().Get(gomock.Any(), cfg.ID).Return(instance, nil).AnyTimes()

	for _, c := range cfg.Connectors {
		procIDsForConn := make([]string, len(c.Processors))
		for i, p := range c.Processors {
			procIDsForConn[i] = p.ID
		}
		connInstance := &connector.Instance{
			ID:           c.ID,
			Type:         connTypeOf(c.Type),
			Plugin:       c.Plugin,
			PipelineID:   cfg.ID,
			Config:       connector.Config{Name: c.Name, Settings: c.Settings},
			ProcessorIDs: procIDsForConn,
		}
		connSrv.EXPECT().Get(gomock.Any(), c.ID).Return(connInstance, nil).AnyTimes()
		for _, p := range c.Processors {
			procSrv.EXPECT().Get(gomock.Any(), p.ID).Return(processorInstanceFor(p), nil).AnyTimes()
		}
	}
	for _, p := range cfg.Processors {
		procSrv.EXPECT().Get(gomock.Any(), p.ID).Return(processorInstanceFor(p), nil).AnyTimes()
	}

	return instance
}

// settingsUpdateFixture returns an old/desired config.Pipeline pair whose
// only difference is a single connector setting — the same EffectInPlace
// shape as TestPlan_SettingsUpdate_InPlace — for tests that need *some*
// non-empty diff against a running pipeline without the added complexity of
// a restart-class (delete+create) change.
func settingsUpdateFixture() (old, desired config.Pipeline) {
	old = config.Pipeline{
		ID:  "p1",
		DLQ: dlqFixture(),
		Connectors: []config.Connector{
			{ID: "p1:conn:src", Type: config.TypeSource, Plugin: "builtin:postgres", Settings: map[string]string{"table": "users"}},
		},
	}
	desired = old
	desired.Connectors = []config.Connector{
		{ID: "p1:conn:src", Type: config.TypeSource, Plugin: "builtin:postgres", Settings: map[string]string{"table": "orders_v2"}},
	}
	return old, desired
}

// TestApplyPlanLive_RunningPipeline_StopsAppliesRestarts is the regression
// test for AC-3's control flow (the real drain-and-persist guarantee itself
// is proven at the primitive level by
// TestServiceLifecycle_StopAndWait_DrainsAndPersists in pkg/lifecycle):
// against a running pipeline, ApplyPlanLive must call StopAndWait, then run
// the import, then Start — in that order — rather than ApplyPlan's "refuse"
// behavior.
//
// This also pins the scope decision documented on ApplyPlanLive: the diff
// here is EffectInPlace-only (a connector settings change), yet ApplyPlanLive
// still goes through the full stop-drain-restart because it doesn't branch on
// Effect (see plan.go's "Scope decision" doc).
func TestApplyPlanLive_RunningPipeline_StopsAppliesRestarts(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, lifecycleSrv := newTestService(ctrl, log.Nop())

	old, desired := settingsUpdateFixture()
	expectExportRunning(pipSrv, connSrv, procSrv, old)

	var order []string
	lifecycleSrv.EXPECT().StopAndWait(gomock.Any(), "p1").DoAndReturn(func(context.Context, string) error {
		order = append(order, "stop")
		return nil
	})
	connSrv.EXPECT().Update(gomock.Any(), "p1:conn:src", "builtin:postgres", connector.Config{Settings: map[string]string{"table": "orders_v2"}}).
		DoAndReturn(func(context.Context, string, string, connector.Config) (*connector.Instance, error) {
			order = append(order, "import")
			return &connector.Instance{ID: "p1:conn:src"}, nil
		})
	lifecycleSrv.EXPECT().Start(gomock.Any(), "p1").DoAndReturn(func(context.Context, string) error {
		order = append(order, "start")
		return nil
	})

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	is.True(!diff.Empty())
	is.Equal(diff.Changes[0].Effect, EffectInPlace) // sanity: this is the in-place-only case

	applied, err := srv.ApplyPlanLive(ctx, desired, diff.Hash)
	is.NoErr(err)
	is.Equal(applied.Hash, diff.Hash)
	is.Equal(order, []string{"stop", "import", "start"})
}

// TestApplyPlanLive_StoppedPipeline_NoLifecycleCalls confirms ApplyPlanLive
// behaves exactly like ApplyPlan when the pipeline isn't running: no
// StopAndWait/Start expectation is set on lifecycleSrv, so gomock fails the
// test if ApplyPlanLive calls either.
func TestApplyPlanLive_StoppedPipeline_NoLifecycleCalls(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, _, _, _, _ := newTestService(ctrl, log.Nop())

	desired := config.Pipeline{ID: "p1", Name: "orders", DLQ: dlqFixture()}

	pipSrv.EXPECT().Get(gomock.Any(), "p1").Return(nil, pipeline.ErrInstanceNotFound).AnyTimes()
	pipSrv.EXPECT().Create(gomock.Any(), "p1", gomock.Any(), pipeline.ProvisionTypeConfig).
		Return(&pipeline.Instance{ID: "p1"}, nil)
	pipSrv.EXPECT().UpdateDLQ(gomock.Any(), "p1", gomock.Any()).Return(&pipeline.Instance{ID: "p1"}, nil)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)

	applied, err := srv.ApplyPlanLive(ctx, desired, diff.Hash)
	is.NoErr(err)
	is.Equal(applied.Hash, diff.Hash)
}

// TestApplyPlanLive_Idempotent_NoOp is the regression test for AC-8's
// live-apply counterpart: an empty diff against a running pipeline is a
// no-op, and — just like ApplyPlan — skips the running-pipeline handling
// entirely, so no StopAndWait/Start call happens (no expectation is set).
func TestApplyPlanLive_Idempotent_NoOp(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, _ := newTestService(ctrl, log.Nop())

	current := config.Pipeline{ID: "p1", Name: "orders", DLQ: dlqFixture()}
	expectExportRunning(pipSrv, connSrv, procSrv, current)

	diff, err := srv.Plan(ctx, current)
	is.NoErr(err)
	is.True(diff.Empty())

	applied, err := srv.ApplyPlanLive(ctx, current, diff.Hash)
	is.NoErr(err)
	is.True(applied.Empty())
}

// TestApplyPlanLive_StaleHash_RefusedNoMutation_RunningPipeline is the
// regression test for AC-4 against a running pipeline: a stale hash is
// refused with CodePlanStale before ApplyPlanLive even checks whether the
// pipeline is running, let alone calls StopAndWait — no expectation is set on
// lifecycleSrv, connSrv, or procSrv, so gomock fails the test if anything
// more is attempted.
func TestApplyPlanLive_StaleHash_RefusedNoMutation_RunningPipeline(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, _ := newTestService(ctrl, log.Nop())

	desired := config.Pipeline{ID: "p1", Name: "orders", DLQ: dlqFixture()}
	expectExportRunning(pipSrv, connSrv, procSrv, desired)

	_, err := srv.ApplyPlanLive(ctx, desired, "not-a-real-hash")
	is.True(err != nil)

	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodePlanStale.Reason())
}

// TestApplyPlanLive_ImportFails_RunningPipeline_LeftStopped is the regression
// test for AC-5 (partial-apply failure leaves the running pipeline stopped, not
// half-applied-and-running): once StopAndWait has succeeded, the pipeline is
// stopped. If the transactional import then fails (importPipeline's own reverse
// rollback already ran — see TestApplyPlan_PartialFailure_RollsBackPrefix for
// that invariant pinned at the import layer), ApplyPlanLive must surface the
// error and must NOT call Start — no Start expectation is set on lifecycleSrv,
// so gomock fails the test if it's called. The pipeline is left stopped
// (StopAndWait already ran, Start never does), matching the design doc's "never
// auto-start into a half-applied or rolled-back state."
//
// This is the control-flow half of AC-6, NOT a real crash-recovery test. A true
// SIGKILL-mid-apply test belongs to the Phase-2 chaos suite (see CLAUDE.md's
// process-maturity table — chaos is not yet a live gate), so it is deliberately
// deferred, not claimed here. Crash safety in PR1 rests on two things that ARE
// verified: the DB-transaction atomicity of importPipeline (#2595, tested
// separately) and StopAndWait's durable-checkpoint guarantee
// (TestServiceLifecycle_StopAndWait_DrainsAndPersists); this test pins the
// leave-stopped control flow on top of them.
func TestApplyPlanLive_ImportFails_RunningPipeline_LeftStopped(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, lifecycleSrv := newTestService(ctrl, log.Nop())

	old, desired := settingsUpdateFixture()
	expectExportRunning(pipSrv, connSrv, procSrv, old)

	lifecycleSrv.EXPECT().StopAndWait(gomock.Any(), "p1").Return(nil)

	wantErr := cerrors.New("forced connector update failure")
	// Do (new settings) fails; importPipeline then rolls back the failed
	// action too (see import.go's importPipeline: rollbackActions includes
	// the failed action itself), which retries Update with the old settings —
	// a second, distinct call to the same method, so it needs its own
	// expectation.
	connSrv.EXPECT().Update(gomock.Any(), "p1:conn:src", "builtin:postgres", connector.Config{Settings: map[string]string{"table": "orders_v2"}}).
		Return(nil, wantErr)
	connSrv.EXPECT().Update(gomock.Any(), "p1:conn:src", "builtin:postgres", connector.Config{Settings: map[string]string{"table": "users"}}).
		Return(&connector.Instance{ID: "p1:conn:src"}, nil)
	// No lifecycleSrv.EXPECT().Start(...) — gomock fails the test if apply
	// attempts to restart after a failed import.

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)

	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash)
	is.True(err != nil) // the import failure must be surfaced, not swallowed
}

// TestApplyPlanLive_RestartFails_LeftStoppedWithNewConfig is the regression
// test for the design doc's "Restart failure" failure mode: the import
// commits successfully (the new config is durable), but Start itself then
// fails — e.g. because the new plugin path is bad. ApplyPlanLive must surface
// that error clearly (rather than silently dropping it or retrying) and must
// leave the pipeline stopped with the new config already applied; it must not
// attempt a second Start.
func TestApplyPlanLive_RestartFails_LeftStoppedWithNewConfig(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, lifecycleSrv := newTestService(ctrl, log.Nop())

	old, desired := settingsUpdateFixture()
	expectExportRunning(pipSrv, connSrv, procSrv, old)

	lifecycleSrv.EXPECT().StopAndWait(gomock.Any(), "p1").Return(nil)
	connSrv.EXPECT().Update(gomock.Any(), "p1:conn:src", "builtin:postgres", gomock.Any()).
		Return(&connector.Instance{ID: "p1:conn:src"}, nil)

	wantErr := cerrors.New("forced start failure: bad plugin path")
	lifecycleSrv.EXPECT().Start(gomock.Any(), "p1").Return(wantErr)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)

	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr)) // the restart failure must still be reachable in the chain
}

// TestApplyPlanLive_StopAndWaitFails_NoMutation proves ApplyPlanLive never
// attempts to mutate the pipeline's stored config if StopAndWait itself
// fails. This is also the generic mechanism behind the lifecycle-poc parity
// guard (pkg/lifecycle-poc.Service.StopAndWait always returns
// CodeStopAndWaitUnsupported): ApplyPlanLive needs no poc-specific branch —
// whatever error StopAndWait returns, for whatever reason, is surfaced here
// and nothing downstream runs. No Create/Update/Delete/Start expectation is
// set on any mock, so gomock fails the test if apply attempts anything beyond
// StopAndWait itself.
func TestApplyPlanLive_StopAndWaitFails_NoMutation(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, lifecycleSrv := newTestService(ctrl, log.Nop())

	old, desired := settingsUpdateFixture()
	expectExportRunning(pipSrv, connSrv, procSrv, old)

	wantErr := conduiterr.New(CodePlanStale, "stand-in for lifecycle_v2.CodeStopAndWaitUnsupported")
	lifecycleSrv.EXPECT().StopAndWait(gomock.Any(), "p1").Return(wantErr)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)

	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr))
}

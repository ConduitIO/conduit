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
	"sync/atomic"
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

	applied, err := srv.ApplyPlanLive(ctx, desired, diff.Hash, false)
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

	applied, err := srv.ApplyPlanLive(ctx, desired, diff.Hash, false)
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

	applied, err := srv.ApplyPlanLive(ctx, current, diff.Hash, false)
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

	_, err := srv.ApplyPlanLive(ctx, desired, "not-a-real-hash", false)
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

	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash, false)
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

	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash, false)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr)) // the restart failure must still be reachable in the chain
}

// restartFixture returns an old/desired config.Pipeline pair whose only
// difference is an existing connector's immutable Type field — the same
// shape as TestPlan_ConnectorTypeChange_Restart — which produces a
// delete+create pair, both EffectRestart (see deleteConnectorAction.Describe
// and createConnectorAction.Describe in import_actions.go). This is the
// fixture the operator-authorization gate tests need: a diff against a
// running pipeline that actually includes a restart-class change, unlike
// settingsUpdateFixture (EffectInPlace-only).
func restartFixture() (old, desired config.Pipeline) {
	old = config.Pipeline{
		ID:  "p1",
		DLQ: dlqFixture(),
		Connectors: []config.Connector{
			{ID: "p1:conn:1", Type: config.TypeSource, Plugin: "builtin:generator"},
		},
	}
	desired = old
	desired.Connectors = []config.Connector{
		{ID: "p1:conn:1", Type: config.TypeDestination, Plugin: "builtin:generator"},
	}
	return old, desired
}

// TestApplyPlanLive_RestartOnRunning_DeniedWithoutFlag is the regression test
// for AC-7 / the design doc's Item 6 rework (the enforced data-path gate):
// ApplyPlanLive against a running pipeline whose diff includes an
// EffectRestart change (restartFixture) must refuse with
// CodeLiveApplyUnauthorized when allowRestartOnRunning is false — the
// server's default, absent an explicit operator flag. No StopAndWait/Create/
// Delete/Start expectation is set on any mock, so gomock fails the test if
// the gate doesn't stop ApplyPlanLive before it touches anything.
func TestApplyPlanLive_RestartOnRunning_DeniedWithoutFlag(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, _ := newTestService(ctrl, log.Nop())

	old, desired := restartFixture()
	expectExportRunning(pipSrv, connSrv, procSrv, old)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	is.True(!diff.Empty())
	is.Equal(diff.Changes[0].Effect, EffectRestart) // sanity: this is the restart-class case

	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash, false)
	is.True(err != nil)

	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeLiveApplyUnauthorized.Reason())
}

// TestApplyPlanLive_RestartOnRunning_AllowedWithFlag is
// TestApplyPlanLive_RestartOnRunning_DeniedWithoutFlag's counterpart: the
// exact same restart-class diff against the exact same running pipeline
// proceeds (StopAndWait -> import -> Start) when allowRestartOnRunning is
// true — proving the gate is a real conditional, not a permanent refusal.
func TestApplyPlanLive_RestartOnRunning_AllowedWithFlag(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, lifecycleSrv := newTestService(ctrl, log.Nop())

	old, desired := restartFixture()
	expectExportRunning(pipSrv, connSrv, procSrv, old)

	var order []string
	lifecycleSrv.EXPECT().StopAndWait(gomock.Any(), "p1").DoAndReturn(func(context.Context, string) error {
		order = append(order, "stop")
		return nil
	})
	connSrv.EXPECT().Delete(gomock.Any(), "p1:conn:1", gomock.Any()).DoAndReturn(func(context.Context, string, connector.PluginDispenserFetcher) error {
		order = append(order, "delete")
		return nil
	})
	connSrv.EXPECT().Create(gomock.Any(), "p1:conn:1", connector.TypeDestination, "builtin:generator", "p1", gomock.Any(), connector.ProvisionTypeConfig).
		DoAndReturn(func(context.Context, string, connector.Type, string, string, connector.Config, connector.ProvisionType) (*connector.Instance, error) {
			order = append(order, "create")
			return &connector.Instance{ID: "p1:conn:1"}, nil
		})
	lifecycleSrv.EXPECT().Start(gomock.Any(), "p1").DoAndReturn(func(context.Context, string) error {
		order = append(order, "start")
		return nil
	})

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)

	applied, err := srv.ApplyPlanLive(ctx, desired, diff.Hash, true)
	is.NoErr(err)
	is.Equal(applied.Hash, diff.Hash)
	is.Equal(order, []string{"stop", "delete", "create", "start"})
}

// TestApplyPlanLive_RestartOnRunning_GateSkippedWhenNotRunning confirms the
// gate is specific to a *running* pipeline: the identical restart-class diff
// (restartFixture) against an existing but STOPPED pipeline applies without
// the flag (like ApplyPlan always has) — the gate must not become a blanket
// restart-change refusal regardless of running status.
func TestApplyPlanLive_RestartOnRunning_GateSkippedWhenNotRunning(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, _ := newTestService(ctrl, log.Nop())

	old, desired := restartFixture()
	// expectExport (unlike expectExportRunning) returns an instance with no
	// status set, i.e. not running — isRunningStatus's default case reports
	// false for it, same as an explicit StatusUserStopped/StatusSystemStopped.
	expectExport(pipSrv, connSrv, procSrv, old)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	is.Equal(diff.Changes[0].Effect, EffectRestart) // sanity: still restart-class

	connSrv.EXPECT().Delete(gomock.Any(), "p1:conn:1", gomock.Any()).Return(nil)
	connSrv.EXPECT().Create(gomock.Any(), "p1:conn:1", connector.TypeDestination, "builtin:generator", "p1", gomock.Any(), connector.ProvisionTypeConfig).
		Return(&connector.Instance{ID: "p1:conn:1"}, nil)

	applied, err := srv.ApplyPlanLive(ctx, desired, diff.Hash, false)
	is.NoErr(err)
	is.Equal(applied.Hash, diff.Hash)
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

	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash, false)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr))
}

// TestApplyPlanLive_TOCTOU_BecameRunningBetweenChecks_Drains is the
// regression test for the TOCTOU fix in ApplyPlanLive: the pipeline is
// STOPPED at the first running check (right after Plan/hash-verify), but has
// become RUNNING by the time of the second, re-check immediately before the
// mutating write — simulating an external Start (e.g. the Start RPC, called
// directly against the lifecycle/pipeline service, which the per-pipeline
// provisioning lock does not reach) racing in during Plan's own work.
// ApplyPlanLive must notice the re-check's answer and take the StopAndWait
// (running) branch — never call transactionalImport directly against a
// pipeline that is, at that moment, actually running. No connSrv.Update
// expectation is registered outside the StopAndWait/Start bracket, so gomock
// fails the test if the plain (undrained) mutation path runs instead.
func TestApplyPlanLive_TOCTOU_BecameRunningBetweenChecks_Drains(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, lifecycleSrv := newTestService(ctrl, log.Nop())

	old, desired := settingsUpdateFixture()

	stopped := pipelineInstanceFor(old, pipeline.StatusUserStopped)
	running := pipelineInstanceFor(old, pipeline.StatusRunning)

	// Call 1: Plan's own Export -> Get (stopped, so Plan computes a normal
	// diff against existing state). Call 2: ApplyPlanLive's first isRunning
	// check (still stopped). Call 3+: the TOCTOU re-check and any further
	// lookups — now running, as if Start landed in between.
	var calls int32
	pipSrv.EXPECT().Get(gomock.Any(), "p1").DoAndReturn(func(context.Context, string) (*pipeline.Instance, error) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			return stopped, nil
		}
		return running, nil
	}).AnyTimes()

	for _, c := range old.Connectors {
		connSrv.EXPECT().Get(gomock.Any(), c.ID).Return(&connector.Instance{
			ID:         c.ID,
			Type:       connTypeOf(c.Type),
			Plugin:     c.Plugin,
			PipelineID: "p1",
			Config:     connector.Config{Name: c.Name, Settings: c.Settings},
		}, nil).AnyTimes()
	}
	_ = procSrv // no processors in settingsUpdateFixture; kept for signature symmetry with other helpers

	// Only reachable via the running (StopAndWait) branch: if ApplyPlanLive
	// instead fell through to the !running branch's plain transactionalImport,
	// none of these would be called and gomock would fail on the missing
	// connSrv.Update expectation below (registered without a plain-import
	// counterpart).
	lifecycleSrv.EXPECT().StopAndWait(gomock.Any(), "p1").Return(nil)
	connSrv.EXPECT().Update(gomock.Any(), "p1:conn:src", "builtin:postgres", connector.Config{Settings: map[string]string{"table": "orders_v2"}}).
		Return(&connector.Instance{ID: "p1:conn:src"}, nil)
	lifecycleSrv.EXPECT().Start(gomock.Any(), "p1").Return(nil)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)

	applied, err := srv.ApplyPlanLive(ctx, desired, diff.Hash, false)
	is.NoErr(err)
	is.Equal(applied.Hash, diff.Hash)
}

// pipelineInstanceFor builds a *pipeline.Instance for cfg (mirroring
// expectExport/expectExportRunning's instance construction) with the given
// status, without registering any mock expectations — used by tests that
// need to hand back a *different* status on successive Get calls (a stateful
// DoAndReturn), which expectExport/expectExportRunning's fixed-AnyTimes()
// registration can't express.
func pipelineInstanceFor(cfg config.Pipeline, status pipeline.Status) *pipeline.Instance {
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
	instance.SetStatus(status)
	return instance
}

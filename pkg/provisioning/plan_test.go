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

	"github.com/conduitio/conduit-commons/lang"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/conduit/pkg/provisioning/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// dlqFixture returns a fully-enriched DLQ (non-nil pointers), matching what
// config.Enrich / Export would always produce, so tests that don't care
// about DLQ diffing don't trip diffPipelineFields on a nil-vs-enriched
// mismatch.
func dlqFixture() config.DLQ {
	return config.DLQ{
		Plugin:              "builtin:log",
		Settings:            map[string]string{"level": "warn"},
		WindowSize:          lang.Ptr(1),
		WindowNackThreshold: lang.Ptr(0),
	}
}

// TestPlan_NewPipeline_AllCreate is the regression test for AC-1: a pipeline
// that doesn't exist yet produces an all-create Diff, every Change is
// EffectInPlace (nothing is running to disrupt), and Plan makes no mutating
// calls at all — gomock fails the test if Create/Update/Delete is ever
// called, proving "no side effects" structurally rather than by assertion.
func TestPlan_NewPipeline_AllCreate(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, _, _, _, _ := newTestService(ctrl, log.Nop())

	desired := config.Pipeline{
		ID:  "p1",
		DLQ: dlqFixture(),
		Connectors: []config.Connector{
			{ID: "p1:conn:src", Type: config.TypeSource, Plugin: "builtin:generator"},
		},
		Processors: []config.Processor{
			{ID: "p1:proc:1", Plugin: "builtin:base64.encode"},
		},
	}

	pipSrv.EXPECT().Get(ctx, "p1").Return(nil, pipeline.ErrInstanceNotFound).AnyTimes()

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	is.Equal(diff.PipelineID, "p1")
	is.Equal(len(diff.Changes), 3) // pipeline + connector + processor
	is.True(diff.Hash != "")

	for _, c := range diff.Changes {
		is.Equal(c.Action, ChangeActionCreate)
		is.Equal(c.Effect, EffectInPlace) // brand-new pipeline override
	}
}

// TestPlan_SettingsUpdate_InPlace is the regression test for AC-2: changing
// a single connector Settings field produces exactly one update/in_place
// Change naming the precise configPath ("settings.<key>"), and Plan itself
// performs no mutation.
func TestPlan_SettingsUpdate_InPlace(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, _ := newTestService(ctrl, log.Nop())

	old := config.Pipeline{
		ID:  "p1",
		DLQ: dlqFixture(),
		Connectors: []config.Connector{
			{ID: "p1:conn:src", Type: config.TypeSource, Plugin: "builtin:postgres", Settings: map[string]string{"table": "users"}},
		},
	}
	desired := old
	desired.Connectors = []config.Connector{
		{ID: "p1:conn:src", Type: config.TypeSource, Plugin: "builtin:postgres", Settings: map[string]string{"table": "orders_v2"}},
	}

	expectExport(pipSrv, connSrv, procSrv, old)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	is.Equal(len(diff.Changes), 1)

	c := diff.Changes[0]
	is.Equal(c.Resource, ResourceConnector)
	is.Equal(c.Action, ChangeActionUpdate)
	is.Equal(c.Effect, EffectInPlace)
	is.Equal(c.ConfigPaths, []string{"settings.table"})
}

// TestPlan_ConnectorTypeChange_Restart is the regression test for AC-3:
// changing an immutable field (Type) produces a delete+create pair, both
// EffectRestart — never a single "update".
func TestPlan_ConnectorTypeChange_Restart(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, _ := newTestService(ctrl, log.Nop())

	old := config.Pipeline{
		ID:  "p1",
		DLQ: dlqFixture(),
		Connectors: []config.Connector{
			{ID: "p1:conn:1", Type: config.TypeSource, Plugin: "builtin:generator"},
		},
	}
	desired := old
	desired.Connectors = []config.Connector{
		{ID: "p1:conn:1", Type: config.TypeDestination, Plugin: "builtin:generator"},
	}

	expectExport(pipSrv, connSrv, procSrv, old)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	is.Equal(len(diff.Changes), 2)

	byAction := map[ChangeAction]Change{}
	for _, c := range diff.Changes {
		byAction[c.Action] = c
	}
	del, ok := byAction[ChangeActionDelete]
	is.True(ok)
	is.Equal(del.Resource, ResourceConnector)
	is.Equal(del.Effect, EffectRestart)

	cr, ok := byAction[ChangeActionCreate]
	is.True(ok)
	is.Equal(cr.Resource, ResourceConnector)
	is.Equal(cr.Effect, EffectRestart)
}

// TestPlan_RemoveAndAddResource is the regression test for AC-4: removing a
// connector from the file produces a delete Change, and adding a different
// one produces a create Change.
func TestPlan_RemoveAndAddResource(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, _ := newTestService(ctrl, log.Nop())

	old := config.Pipeline{
		ID:  "p1",
		DLQ: dlqFixture(),
		Connectors: []config.Connector{
			{ID: "p1:conn:old", Type: config.TypeSource, Plugin: "builtin:generator"},
		},
	}
	desired := old
	desired.Connectors = []config.Connector{
		{ID: "p1:conn:new", Type: config.TypeDestination, Plugin: "builtin:log"},
	}

	expectExport(pipSrv, connSrv, procSrv, old)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	// delete(old connector) + create(new connector) + update(pipeline, since
	// its Connectors membership also changed — preparePipelineActions
	// compares the whole config, not just the connector-level diff).
	is.Equal(len(diff.Changes), 3)

	var sawDelete, sawCreate, sawPipelineUpdate bool
	for _, c := range diff.Changes {
		switch {
		case c.Action == ChangeActionDelete && c.Resource == ResourceConnector:
			is.Equal(c.ID, "p1:conn:old")
			sawDelete = true
		case c.Action == ChangeActionCreate && c.Resource == ResourceConnector:
			is.Equal(c.ID, "p1:conn:new")
			sawCreate = true
		case c.Action == ChangeActionUpdate && c.Resource == ResourcePipeline:
			is.Equal(c.Effect, EffectRestart) // connector membership changed
			sawPipelineUpdate = true
		default:
			t.Fatalf("unexpected change: %+v", c)
		}
	}
	is.True(sawDelete)
	is.True(sawCreate)
	is.True(sawPipelineUpdate)
}

// TestPlan_HashStableAndSensitive is the regression test for AC-5: two Plan
// calls over the same (file, current-state) pair produce the same hash, and
// changing the desired config changes the hash.
func TestPlan_HashStableAndSensitive(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, _, _, _, _ := newTestService(ctrl, log.Nop())

	desired := config.Pipeline{ID: "p1", Name: "orders", DLQ: dlqFixture()}
	pipSrv.EXPECT().Get(ctx, "p1").Return(nil, pipeline.ErrInstanceNotFound).AnyTimes()

	d1, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	d2, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	is.Equal(d1.Hash, d2.Hash)

	changed := desired
	changed.Name = "orders-v2"
	d3, err := srv.Plan(ctx, changed)
	is.NoErr(err)
	is.True(d3.Hash != d1.Hash)
}

// TestApplyPlan_MatchingHash_Executes is the regression test for AC-6: a
// matching plan hash actually executes the underlying actions.
func TestApplyPlan_MatchingHash_Executes(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, _, _, _, _ := newTestService(ctrl, log.Nop())

	desired := config.Pipeline{ID: "p1", Name: "orders", DLQ: dlqFixture()}

	// apply wraps importPipeline in a DB transaction (plan.go), so the writes
	// inside it run with the transactional ctx, not the plain ctx Plan uses —
	// match gomock.Any() on ctx rather than pinning the transaction plumbing.
	pipSrv.EXPECT().Get(gomock.Any(), "p1").Return(nil, pipeline.ErrInstanceNotFound).AnyTimes()
	pipSrv.EXPECT().Create(gomock.Any(), "p1", gomock.Any(), pipeline.ProvisionTypeConfig).
		Return(&pipeline.Instance{ID: "p1"}, nil)
	pipSrv.EXPECT().UpdateDLQ(gomock.Any(), "p1", gomock.Any()).Return(&pipeline.Instance{ID: "p1"}, nil)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)

	applied, err := srv.ApplyPlan(ctx, desired, diff.Hash)
	is.NoErr(err)
	is.Equal(applied.Hash, diff.Hash)
}

// TestApplyPlan_WrapsImportInTransaction is the regression test for the
// crash-safety fix (#2588 review): apply must run importPipeline inside a single
// DB transaction (invariant 5 — state writes are atomic), exactly as
// provisionPipeline does, so a crash or a failed in-process rollback can't leave
// the store partially mutated. It captures the ctx importPipeline's Create runs
// with: under the fix that is the transactional child ctx, distinct from the
// plain ctx handed to ApplyPlan. Removing the transaction wrapping regresses the
// captured ctx back to the plain ctx and fails this test.
func TestApplyPlan_WrapsImportInTransaction(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, _, _, _, _ := newTestService(ctrl, log.Nop())

	desired := config.Pipeline{ID: "p1", Name: "orders", DLQ: dlqFixture()}
	pipSrv.EXPECT().Get(gomock.Any(), "p1").Return(nil, pipeline.ErrInstanceNotFound).AnyTimes()

	var createCtx context.Context
	pipSrv.EXPECT().Create(gomock.Any(), "p1", gomock.Any(), pipeline.ProvisionTypeConfig).
		DoAndReturn(func(c context.Context, _ string, _ pipeline.Config, _ pipeline.ProvisionType) (*pipeline.Instance, error) {
			createCtx = c
			return &pipeline.Instance{ID: "p1"}, nil
		})
	pipSrv.EXPECT().UpdateDLQ(gomock.Any(), "p1", gomock.Any()).Return(&pipeline.Instance{ID: "p1"}, nil)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	_, err = srv.ApplyPlan(ctx, desired, diff.Hash)
	is.NoErr(err)

	is.True(createCtx != nil) // importPipeline ran
	is.True(createCtx != ctx) // ...under the transactional ctx, not the plain one
}

// TestApplyPlan_StaleHash_RefusedNoMutation is the regression test for AC-7:
// a stale (non-matching) hash is refused with CodePlanStale, exit-classified
// as a validation failure, and — critically — no action ever runs. gomock's
// pipSrv/connSrv/procSrv have no Create/Update/Delete expectations set at
// all, so any attempted mutation fails the test outright.
func TestApplyPlan_StaleHash_RefusedNoMutation(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, _, _, _, _ := newTestService(ctrl, log.Nop())

	desired := config.Pipeline{ID: "p1", Name: "orders", DLQ: dlqFixture()}
	pipSrv.EXPECT().Get(ctx, "p1").Return(nil, pipeline.ErrInstanceNotFound).AnyTimes()

	_, err := srv.ApplyPlan(ctx, desired, "not-a-real-hash")
	is.True(err != nil)

	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodePlanStale.Reason())
}

// TestApplyPlan_Idempotent_NoOp is the regression test for AC-8: applying an
// already-applied config produces an empty Diff and executes nothing.
func TestApplyPlan_Idempotent_NoOp(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, _ := newTestService(ctrl, log.Nop())

	current := config.Pipeline{ID: "p1", Name: "orders", DLQ: dlqFixture()}
	expectExport(pipSrv, connSrv, procSrv, current)

	diff, err := srv.Plan(ctx, current)
	is.NoErr(err)
	is.True(diff.Empty())

	applied, err := srv.ApplyPlan(ctx, current, diff.Hash)
	is.NoErr(err)
	is.True(applied.Empty())
}

// TestApplyPlan_PartialFailure_RollsBackPrefix is the regression test for
// AC-9: when an action fails mid-sequence, the already-executed prefix is
// rolled back in reverse order. This pins the exact invariant importPipeline
// already provides (see import.go) — ApplyPlan must not bypass it.
func TestApplyPlan_PartialFailure_RollsBackPrefix(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, _, _, _ := newTestService(ctrl, log.Nop())

	desired := config.Pipeline{
		ID:  "p1",
		DLQ: dlqFixture(),
		Connectors: []config.Connector{
			{ID: "p1:conn:src", Type: config.TypeSource, Plugin: "builtin:generator"},
		},
	}

	// apply wraps importPipeline in a DB transaction (plan.go), so calls inside
	// it run with the transactional ctx — match gomock.Any() on ctx.
	pipSrv.EXPECT().Get(gomock.Any(), "p1").Return(nil, pipeline.ErrInstanceNotFound).AnyTimes()

	// Pipeline create (and its rollback, a Delete) succeeds; the connector
	// create that follows fails, forcing a rollback of the pipeline create.
	pipSrv.EXPECT().Create(gomock.Any(), "p1", gomock.Any(), pipeline.ProvisionTypeConfig).
		Return(&pipeline.Instance{ID: "p1"}, nil)
	pipSrv.EXPECT().UpdateDLQ(gomock.Any(), "p1", gomock.Any()).Return(&pipeline.Instance{ID: "p1"}, nil)
	pipSrv.EXPECT().AddConnector(gomock.Any(), "p1", "p1:conn:src").Return(&pipeline.Instance{ID: "p1"}, nil)

	wantErr := cerrors.New("forced connector create failure")
	connSrv.EXPECT().Create(gomock.Any(), "p1:conn:src", gomock.Any(), "builtin:generator", "p1", gomock.Any(), gomock.Any()).
		Return(nil, wantErr)

	// rollbackActions rolls back the whole executed-plus-failed prefix in
	// reverse (see import.go's importPipeline and executeActions' doc): the
	// failed createConnectorAction is rolled back first (its own comment:
	// "ignore instance not found errors, this means the action failed to
	// create the connector in the first place"), then the pipeline create.
	connSrv.EXPECT().Delete(gomock.Any(), "p1:conn:src", gomock.Any()).Return(connector.ErrInstanceNotFound)
	pipSrv.EXPECT().Delete(gomock.Any(), "p1").Return(nil)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)

	_, err = srv.ApplyPlan(ctx, desired, diff.Hash)
	is.True(err != nil) // apply must surface the failure, not swallow it
}

// TestApplyPlan_RunningPipeline_Refused is the regression test for AC-13:
// apply must never silently mutate a running pipeline. No Create/Update/
// Delete expectation is set on any mock, so gomock fails the test if apply
// attempts anything beyond the Get status check.
func TestApplyPlan_RunningPipeline_Refused(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, _, _, _, _ := newTestService(ctrl, log.Nop())

	current := config.Pipeline{ID: "p1", Name: "orders", DLQ: dlqFixture()}
	desired := current
	desired.Name = "orders-v2"

	// Export (inside Plan) reads the pipeline once per call; ApplyPlan's own
	// running-state check reads it again. Both must see the running status.
	running := &pipeline.Instance{
		ID:     "p1",
		Config: pipeline.Config{Name: "orders"},
		DLQ: pipeline.DLQ{
			Plugin:              current.DLQ.Plugin,
			Settings:            current.DLQ.Settings,
			WindowSize:          *current.DLQ.WindowSize,
			WindowNackThreshold: *current.DLQ.WindowNackThreshold,
		},
	}
	running.SetStatus(pipeline.StatusRunning)
	pipSrv.EXPECT().Get(ctx, "p1").Return(running, nil).AnyTimes()

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	is.True(!diff.Empty()) // sanity: there is something to apply

	_, err = srv.ApplyPlan(ctx, desired, diff.Hash)
	is.True(err != nil)

	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodePipelineRunning.Reason())
}

// -------------
// -- HELPERS --
// -------------

// connTypeOf maps a config.Connector.Type string onto connector.Type, the
// enum connector.Instance carries.
func connTypeOf(t string) connector.Type {
	if t == config.TypeDestination {
		return connector.TypeDestination
	}
	return connector.TypeSource
}

// processorInstanceFor builds the *processor.Instance Export's
// processorToConfig would translate back into cfg.
func processorInstanceFor(cfg config.Processor) *processor.Instance {
	return &processor.Instance{
		ID:     cfg.ID,
		Plugin: cfg.Plugin,
		Config: processor.Config{Settings: cfg.Settings, Workers: cfg.Workers},
	}
}

// expectExport wires pipSrv/connSrv/procSrv to answer Export(ctx, cfg.ID)
// with the given config.Pipeline, translated into the pipeline/connector/
// processor.Instance shapes Export expects, so tests can express "the
// current state is X" directly in config.Pipeline terms instead of hand
// -building instances.
func expectExport(pipSrv *mock.PipelineService, connSrv *mock.ConnectorService, procSrv *mock.ProcessorService, cfg config.Pipeline) {
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
}

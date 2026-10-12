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

// Regression tests for #2961: the orchestrator checked that a pipeline had no
// run and then wrote its config in a separate step, so a Start could admit a
// run between the two. Each case wires the REAL orchestrator to the REAL
// lifecycle service and calls Start from inside the config write itself, which
// is the exact point between "no run" and "config changed" that used to be
// open. The call is synchronous, so there is no scheduling luck and no sleep:
// without the fix Start is admitted there (a live run beside a changed or
// deleted pipeline); with it Start is refused with pipeline.config_changing.

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/orchestrator"
	"github.com/conduitio/conduit/pkg/orchestrator/mock"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// startProbe attempts a Start of the pipeline each time a config write runs.
type startProbe struct {
	ctx    context.Context
	ls     *Service
	id     string
	errs   []error
	onFire func()
}

func (p *startProbe) fire() {
	p.errs = append(p.errs, p.ls.Start(p.ctx, p.id))
	if p.onFire != nil {
		p.onFire()
	}
}

// probedPipelines runs the probe before every pipeline-store write the
// orchestrator makes, then performs it. The connector and processor
// membership writes only fire the probe: the pipeline they name is not what
// the case is about.
type probedPipelines struct {
	*pipeline.Service
	probe *startProbe
	// failAdd makes AddProcessor/AddConnector fail after the probe, to drive
	// the orchestrator's rollback.
	failAdd error
}

func (p probedPipelines) Update(ctx context.Context, id string, cfg pipeline.Config) (*pipeline.Instance, error) {
	p.probe.fire()
	return p.Service.Update(ctx, id, cfg)
}

func (p probedPipelines) UpdateDLQ(ctx context.Context, id string, dlq pipeline.DLQ) (*pipeline.Instance, error) {
	p.probe.fire()
	return p.Service.UpdateDLQ(ctx, id, dlq)
}

func (p probedPipelines) Delete(ctx context.Context, id string) error {
	p.probe.fire()
	return p.Service.Delete(ctx, id)
}

func (p probedPipelines) AddConnector(ctx context.Context, id, _ string) (*pipeline.Instance, error) {
	p.probe.fire()
	if p.failAdd != nil {
		return nil, p.failAdd
	}
	return p.Service.Get(ctx, id)
}

func (p probedPipelines) RemoveConnector(ctx context.Context, id, _ string) (*pipeline.Instance, error) {
	p.probe.fire()
	return p.Service.Get(ctx, id)
}

func (p probedPipelines) AddProcessor(ctx context.Context, id, _ string) (*pipeline.Instance, error) {
	p.probe.fire()
	if p.failAdd != nil {
		return nil, p.failAdd
	}
	return p.Service.Get(ctx, id)
}

func (p probedPipelines) RemoveProcessor(ctx context.Context, id, _ string) (*pipeline.Instance, error) {
	p.probe.fire()
	return p.Service.Get(ctx, id)
}

type configWriteRun struct {
	*admissionRun
	probe *startProbe
	pls   probedPipelines
	cons  *mock.ConnectorService
	procs *mock.ProcessorService
	plugs *mock.ConnectorPluginService
	orch  *orchestrator.Orchestrator
}

func newConfigWriteRun(t *testing.T) *configWriteRun {
	t.Helper()
	ctx := context.Background()
	r := &configWriteRun{admissionRun: newAdmissionRun(t)}
	ps, ok := r.ls.pipelines.(*pipeline.Service)
	if !ok {
		t.Fatalf("expected the real pipeline service, got %T", r.ls.pipelines)
	}
	r.probe = &startProbe{ctx: ctx, ls: r.ls, id: r.pl.ID}
	r.pls = probedPipelines{Service: ps, probe: r.probe}
	ctrl := gomock.NewController(t)
	r.cons = mock.NewConnectorService(ctrl)
	r.procs = mock.NewProcessorService(ctrl)
	r.plugs = mock.NewConnectorPluginService(ctrl)
	r.plugs.EXPECT().ValidateSourceConfig(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	r.plugs.EXPECT().ValidateDestinationConfig(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	r.build()
	return r
}

// build (re)creates the orchestrator; call it after changing r.pls.
func (r *configWriteRun) build() {
	r.orch = orchestrator.NewOrchestrator(
		&inmemory.DB{}, log.New(zerolog.Nop()),
		r.pls, r.cons, r.procs, r.plugs, nil, r.ls,
	)
}

// startAndStop proves the pipeline is startable again (the mark is released),
// then stops the run so its plugins are torn down.
func (r *configWriteRun) startAndStop(t *testing.T) {
	t.Helper()
	is := is.New(t)
	is.NoErr(r.ls.Start(r.probe.ctx, r.pl.ID))
	is.NoErr(r.ls.Stop(r.probe.ctx, r.pl.ID, false))
	is.NoErr(r.ls.WaitPipeline(r.pl.ID))
}

// requireRefused asserts every Start attempted inside a config write was
// refused because of that write, and that none produced a run.
func (r *configWriteRun) requireRefused(t *testing.T, wantAtLeast int) {
	t.Helper()
	if got := len(r.probe.errs); got < wantAtLeast {
		t.Fatalf("the config write fired the probe %d times, want at least %d", got, wantAtLeast)
	}
	for i, err := range r.probe.errs {
		if !cerrors.Is(err, pipeline.ErrPipelineConfigChanging) {
			t.Fatalf("Start #%d inside the config write was admitted or failed otherwise (err=%v): "+
				"a run could be built against a config that is changing", i, err)
		}
	}
	if n := len(r.runs()); n != 0 {
		t.Fatalf("%d run(s) were published while the config was being written", n)
	}
}

func apiConnector(pipelineID string) *connector.Instance {
	return &connector.Instance{
		ID: "conn-under-change", PipelineID: pipelineID, Type: connector.TypeSource,
		Plugin: "test-plugin", ProvisionedBy: connector.ProvisionTypeAPI,
	}
}

func apiProcessor(pipelineID string) *processor.Instance {
	return &processor.Instance{
		ID: "proc-under-change", Plugin: "test-processor", ProvisionedBy: processor.ProvisionTypeAPI,
		Parent: processor.Parent{ID: pipelineID, Type: processor.ParentTypePipeline},
	}
}

// pkg/orchestrator/pipelines.go

func TestConfigWrite_PipelineUpdate_StartRefusedInsideWindow(t *testing.T) {
	is := is.New(t)
	r := newConfigWriteRun(t)

	_, err := r.orch.Pipelines.Update(r.probe.ctx, r.pl.ID, pipeline.Config{Name: "renamed"})
	is.NoErr(err)

	r.requireRefused(t, 1)
	// The mark is released: a Start after the write is admitted.
	r.startAndStop(t)
}

func TestConfigWrite_PipelineUpdateDLQ_StartRefusedInsideWindow(t *testing.T) {
	is := is.New(t)
	r := newConfigWriteRun(t)

	dlq := pipeline.DLQ{Plugin: "builtin:log", WindowSize: 3, WindowNackThreshold: 1}
	_, err := r.orch.Pipelines.UpdateDLQ(r.probe.ctx, r.pl.ID, dlq)
	is.NoErr(err)

	r.requireRefused(t, 1)
	r.startAndStop(t)
}

// A deleted pipeline must not get a live run. The pipeline here has no
// connectors, so Start cannot build a run for it: the assertion is that Start
// was refused at admission, not that it failed later.
func TestConfigWrite_PipelineDelete_StartRefusedInsideWindow(t *testing.T) {
	is := is.New(t)
	r := newConfigWriteRun(t)
	empty, err := r.pls.Service.Create(r.probe.ctx, "empty-pipeline", pipeline.Config{Name: "empty"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)
	r.probe.id = empty.ID

	is.NoErr(r.orch.Pipelines.Delete(r.probe.ctx, empty.ID))

	r.requireRefused(t, 1)
	_, err = r.pls.Service.Get(r.probe.ctx, empty.ID)
	is.True(cerrors.Is(err, pipeline.ErrInstanceNotFound)) // deleted, and no run for it
}

// The mark is also released when the call fails, so a refused or failed
// change never leaves the pipeline unstartable.
func TestConfigWrite_PipelineDelete_ReleasedOnFailure(t *testing.T) {
	is := is.New(t)
	r := newConfigWriteRun(t)

	// r.pl has connectors attached, so Delete is refused after the mark is taken.
	err := r.orch.Pipelines.Delete(r.probe.ctx, r.pl.ID)
	is.True(cerrors.Is(err, orchestrator.ErrPipelineHasConnectorsAttached))
	r.startAndStop(t)
}

// pkg/orchestrator/connectors.go

func TestConfigWrite_ConnectorUpdate_StartRefusedInsideWindow(t *testing.T) {
	is := is.New(t)
	r := newConfigWriteRun(t)
	conn := apiConnector(r.pl.ID)
	r.cons.EXPECT().Get(gomock.Any(), conn.ID).Return(conn, nil)
	r.cons.EXPECT().Update(gomock.Any(), conn.ID, conn.Plugin, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, _ string, _ connector.Config) (*connector.Instance, error) {
			r.probe.fire()
			return conn, nil
		})

	_, err := r.orch.Connectors.Update(r.probe.ctx, conn.ID, conn.Plugin, connector.Config{Settings: map[string]string{"a": "b"}})
	is.NoErr(err)

	r.requireRefused(t, 1)
	r.startAndStop(t)
}

func TestConfigWrite_ConnectorDelete_StartRefusedInsideWindow(t *testing.T) {
	is := is.New(t)
	r := newConfigWriteRun(t)
	conn := apiConnector(r.pl.ID)
	r.cons.EXPECT().Get(gomock.Any(), conn.ID).Return(conn, nil)
	r.cons.EXPECT().Delete(gomock.Any(), conn.ID, gomock.Any()).
		DoAndReturn(func(context.Context, string, connector.PluginDispenserFetcher) error {
			r.probe.fire()
			return nil
		})

	is.NoErr(r.orch.Connectors.Delete(r.probe.ctx, conn.ID))

	// Fired by the connector delete and by the pipeline membership write.
	r.requireRefused(t, 2)
	r.startAndStop(t)
}

func TestConfigWrite_ConnectorCreate_StartRefusedInsideWindow(t *testing.T) {
	is := is.New(t)
	r := newConfigWriteRun(t)
	conn := apiConnector(r.pl.ID)
	r.cons.EXPECT().Create(gomock.Any(), gomock.Any(), connector.TypeSource, conn.Plugin, r.pl.ID, gomock.Any(), connector.ProvisionTypeAPI).
		DoAndReturn(func(context.Context, string, connector.Type, string, string, connector.Config, connector.ProvisionType) (*connector.Instance, error) {
			r.probe.fire()
			return conn, nil
		})

	_, err := r.orch.Connectors.Create(r.probe.ctx, connector.TypeSource, conn.Plugin, r.pl.ID, connector.Config{Name: "c"})
	is.NoErr(err)

	r.requireRefused(t, 2)
	r.startAndStop(t)
}

// pkg/orchestrator/processors.go

func TestConfigWrite_ProcessorUpdate_StartRefusedInsideWindow(t *testing.T) {
	is := is.New(t)
	r := newConfigWriteRun(t)
	proc := apiProcessor(r.pl.ID)
	r.procs.EXPECT().Get(gomock.Any(), proc.ID).Return(proc, nil)
	r.procs.EXPECT().Update(gomock.Any(), proc.ID, proc.Plugin, gomock.Any()).
		DoAndReturn(func(context.Context, string, string, processor.Config) (*processor.Instance, error) {
			r.probe.fire()
			return proc, nil
		})

	_, err := r.orch.Processors.Update(r.probe.ctx, proc.ID, proc.Plugin, processor.Config{Settings: map[string]string{"a": "b"}})
	is.NoErr(err)

	r.requireRefused(t, 1)
	r.startAndStop(t)
}

func TestConfigWrite_ProcessorDelete_StartRefusedInsideWindow(t *testing.T) {
	is := is.New(t)
	r := newConfigWriteRun(t)
	proc := apiProcessor(r.pl.ID)
	r.procs.EXPECT().Get(gomock.Any(), proc.ID).Return(proc, nil)
	r.procs.EXPECT().Delete(gomock.Any(), proc.ID).
		DoAndReturn(func(context.Context, string) error {
			r.probe.fire()
			return nil
		})

	is.NoErr(r.orch.Processors.Delete(r.probe.ctx, proc.ID))

	r.requireRefused(t, 2)
	r.startAndStop(t)
}

// The orchestrator's rollback after a failed Create is itself a config write.
// The mark must still be held while it runs, or a Start could be admitted
// against the half-undone state.
func TestConfigWrite_ProcessorCreate_StartRefusedDuringRollback(t *testing.T) {
	is := is.New(t)
	r := newConfigWriteRun(t)
	r.pls.failAdd = cerrors.New("injected: cannot add processor to pipeline")
	r.build()
	proc := apiProcessor(r.pl.ID)
	r.procs.EXPECT().Create(gomock.Any(), gomock.Any(), proc.Plugin, proc.Parent, gomock.Any(), processor.ProvisionTypeAPI, "").
		DoAndReturn(func(context.Context, string, string, processor.Parent, processor.Config, processor.ProvisionType, string) (*processor.Instance, error) {
			r.probe.fire()
			return proc, nil
		})
	r.procs.EXPECT().Delete(gomock.Any(), proc.ID).
		DoAndReturn(func(context.Context, string) error {
			// the rollback of the create
			r.probe.fire()
			return nil
		})

	_, err := r.orch.Processors.Create(r.probe.ctx, proc.Plugin, proc.Parent, processor.Config{}, "")
	is.True(err != nil)

	r.requireRefused(t, 3) // create, add (fails), rollback delete
	r.startAndStop(t)
}

// The other half of the rule: a pipeline with a run refuses the change, and
// takes no mark.
func TestConfigWrite_RefusedWhileRunning_HoldsNothing(t *testing.T) {
	is := is.New(t)
	r := newConfigWriteRun(t)
	is.NoErr(r.ls.Start(r.probe.ctx, r.pl.ID))

	_, err := r.orch.Pipelines.Update(r.probe.ctx, r.pl.ID, pipeline.Config{Name: "renamed"})
	is.True(cerrors.Is(err, pipeline.ErrPipelineRunning))
	is.Equal(len(r.probe.errs), 0) // the write never ran
	defer func() {
		is.NoErr(r.ls.Stop(r.probe.ctx, r.pl.ID, false))
		is.NoErr(r.ls.WaitPipeline(r.pl.ID))
	}()

	r.ls.publishMu.Lock()
	marks := len(r.ls.mutating)
	r.ls.publishMu.Unlock()
	is.Equal(marks, 0)
}

// Mark semantics on the service itself.
func TestTryLockForMutation(t *testing.T) {
	ctx := context.Background()

	t.Run("excludes Start until released, idempotently", func(t *testing.T) {
		is := is.New(t)
		r := newConfigWriteRun(t)
		unlock1, ok := r.ls.TryLockForMutation(r.pl.ID)
		is.True(ok)
		unlock2, ok := r.ls.TryLockForMutation(r.pl.ID) // concurrent mutations do not exclude each other
		is.True(ok)
		is.True(cerrors.Is(r.ls.Start(ctx, r.pl.ID), pipeline.ErrPipelineConfigChanging))
		unlock1()
		unlock1() // idempotent: must not release unlock2's mark
		is.True(cerrors.Is(r.ls.Start(ctx, r.pl.ID), pipeline.ErrPipelineConfigChanging))
		unlock2()
		r.startAndStop(t)
	})

	t.Run("refused while a run is live", func(t *testing.T) {
		is := is.New(t)
		r := newConfigWriteRun(t)
		is.NoErr(r.ls.Start(ctx, r.pl.ID))
		_, ok := r.ls.TryLockForMutation(r.pl.ID)
		is.True(!ok)
		is.NoErr(r.ls.Stop(ctx, r.pl.ID, false))
		is.NoErr(r.ls.WaitPipeline(r.pl.ID))
	})

	t.Run("refused while a Start holds its reservation", func(t *testing.T) {
		is := is.New(t)
		r := newConfigWriteRun(t)
		r.conns.gate = make(chan struct{})
		r.conns.failGet.Store(true)
		startErr := make(chan error, 1)
		go func() { startErr <- r.ls.Start(ctx, r.pl.ID) }()
		<-r.conns.entered // the Start holds its reservation and is building
		_, ok := r.ls.TryLockForMutation(r.pl.ID)
		is.True(!ok)
		close(r.conns.gate)
		is.True(cerrors.Is(<-startErr, errBuildFailed))
		// The failed Start released its reservation, so the mark is granted.
		unlock, ok := r.ls.TryLockForMutation(r.pl.ID)
		is.True(ok)
		unlock()
	})

	t.Run("does not block other pipelines", func(t *testing.T) {
		is := is.New(t)
		r := newConfigWriteRun(t)
		unlock, ok := r.ls.TryLockForMutation("some-other-pipeline")
		is.True(ok)
		defer unlock()
		r.startAndStop(t)
	})
}

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
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/conduit/pkg/provisioning/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// Regression tests for #2965: ApplyPlan and ApplyPlanLive decide whether a
// pipeline is running from LifecycleService.IsActive, not from its status.
// Each test gives the status and the run opposite answers.

// expectExportWithStatus serves Export for settingsUpdateFixture's old config
// (one connector, no processors) with the pipeline reporting status.
func expectExportWithStatus(pipSrv *mock.PipelineService, connSrv *mock.ConnectorService, cfg config.Pipeline, status pipeline.Status) {
	pipSrv.EXPECT().Get(gomock.Any(), cfg.ID).Return(pipelineInstanceFor(cfg, status), nil).AnyTimes()
	for _, c := range cfg.Connectors {
		connSrv.EXPECT().Get(gomock.Any(), c.ID).Return(&connector.Instance{
			ID:         c.ID,
			Type:       connTypeOf(c.Type),
			Plugin:     c.Plugin,
			PipelineID: cfg.ID,
			Config:     connector.Config{Name: c.Name, Settings: c.Settings},
		}, nil).AnyTimes()
	}
}

// A run in its starting window still shows the previous status (stopped).
// ApplyPlanLive must treat it as running and go through stop/apply/start,
// not rewrite the config the run is being built from.
func TestApplyPlanLive_StartingRun_StatusStopped_StopsAppliesRestarts(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, _, _, lifecycleSrv := newTestService(ctrl, log.Nop())
	var order []string
	expectMark(t, lifecycleSrv, &order)

	old, desired := settingsUpdateFixture()
	expectExportWithStatus(pipSrv, connSrv, old, pipeline.StatusUserStopped)
	expectActive(lifecycleSrv, "p1", true) // the run is starting

	lifecycleSrv.EXPECT().StopAndWait(gomock.Any(), "p1").DoAndReturn(func(context.Context, string) error {
		order = append(order, "stop")
		return nil
	})
	connSrv.EXPECT().Update(gomock.Any(), "p1:conn:src", "builtin:postgres", gomock.Any()).
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
	applied, err := srv.ApplyPlanLive(ctx, desired, diff.Hash, true)
	is.NoErr(err)
	is.Equal(applied.AppliedMode, ApplyModeRestart)
	is.Equal(order, []string{"stop", "mark", "import", "unmark", "start"})
}

// The same starting run against ApplyPlan: refused, nothing written.
func TestApplyPlan_StartingRun_StatusStopped_Refused(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, _, _, lifecycleSrv := newTestService(ctrl, log.Nop())

	old, desired := settingsUpdateFixture()
	expectExportWithStatus(pipSrv, connSrv, old, pipeline.StatusUserStopped)
	expectActive(lifecycleSrv, "p1", true)
	// No Update expectation: gomock fails the test if the import runs.

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	_, err = srv.ApplyPlan(ctx, desired, diff.Hash)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodePipelineRunning.Reason())
}

// A stale Running status with no run behind it: ApplyPlanLive imports
// directly, with no needless stop/start and no operator flag required. No
// StopAndWait or Start expectation is set, so gomock fails on either.
func TestApplyPlanLive_StaleRunningStatus_NoRun_ImportsWithoutRestart(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, _, _, lifecycleSrv := newTestService(ctrl, log.Nop())
	expectMark(t, lifecycleSrv, nil)

	old, desired := settingsUpdateFixture()
	expectExportWithStatus(pipSrv, connSrv, old, pipeline.StatusRunning)
	expectActive(lifecycleSrv, "p1", false)
	connSrv.EXPECT().Update(gomock.Any(), "p1:conn:src", "builtin:postgres", gomock.Any()).
		Return(&connector.Instance{ID: "p1:conn:src"}, nil)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	applied, err := srv.ApplyPlanLive(ctx, desired, diff.Hash, false)
	is.NoErr(err)
	is.Equal(applied.AppliedMode, ApplyModeProvisioned)
}

// A pipeline that ended Degraded has no run. ApplyPlan used to refuse it as
// running, while the suggested "stop it first" failed with "not running".
// It now applies.
func TestApplyPlan_DegradedNoRun_Applies(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, _, _, lifecycleSrv := newTestService(ctrl, log.Nop())

	old, desired := settingsUpdateFixture()
	expectExportWithStatus(pipSrv, connSrv, old, pipeline.StatusDegraded)
	expectActive(lifecycleSrv, "p1", false)
	connSrv.EXPECT().Update(gomock.Any(), "p1:conn:src", "builtin:postgres", gomock.Any()).
		Return(&connector.Instance{ID: "p1:conn:src"}, nil)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	_, err = srv.ApplyPlan(ctx, desired, diff.Hash)
	is.NoErr(err)
}

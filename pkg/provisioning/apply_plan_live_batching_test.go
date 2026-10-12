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
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	lifecyclev1 "github.com/conduitio/conduit/pkg/lifecycle"
	lifecyclev2 "github.com/conduitio/conduit/pkg/lifecycle-poc"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/conduit/pkg/provisioning/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// archV2Lifecycle is what runtime.go wires under --preview.pipeline-arch-v2:
// the real lifecycle-poc service (which carries PreflightPipelineConfig),
// except lifecycle calls go to a strict mock so the test sees every call.
type archV2Lifecycle struct {
	*mock.LifecycleService
	v2 *lifecyclev2.Service
}

func (l archV2Lifecycle) PreflightPipelineConfig(cfg config.Pipeline) error {
	return l.v2.PreflightPipelineConfig(cfg)
}

func newArchV2PreflightService(ctrl *gomock.Controller) (*Service, *mock.PipelineService, *mock.ConnectorService, *mock.ProcessorService, *mock.LifecycleService) {
	v2 := lifecyclev2.NewService(log.Nop(), &lifecyclev1.ErrRecoveryCfg{
		MinDelay: time.Millisecond, MaxDelay: time.Millisecond, BackoffFactor: 2, MaxRetriesWindow: time.Minute,
	}, noopV2ConnectorService{}, noopV2ProcessorService{}, noopV2ConnectorPluginService{}, noopV2PipelineService{}, true)

	pipSrv := mock.NewPipelineService(ctrl)
	connSrv := mock.NewConnectorService(ctrl)
	procSrv := mock.NewProcessorService(ctrl)
	connPlugSrv := mock.NewConnectorPluginService(ctrl)
	lifecycleSrv := mock.NewLifecycleService(ctrl)
	srv := NewService(&inmemory.DB{}, log.Nop(), pipSrv, connSrv, procSrv, connPlugSrv,
		archV2Lifecycle{LifecycleService: lifecycleSrv, v2: v2}, "")
	return srv, pipSrv, connSrv, procSrv, lifecycleSrv
}

func batchingApplyFixture() (old, desired config.Pipeline) {
	old = config.Pipeline{
		ID:  "p1",
		DLQ: dlqFixture(),
		Connectors: []config.Connector{
			{ID: "p1:src", Type: config.TypeSource, Plugin: "builtin:generator", Settings: map[string]string{"recordCount": "1"}},
			{ID: "p1:out", Type: config.TypeDestination, Plugin: "builtin:file", Settings: map[string]string{"path": "/tmp/x"}},
		},
	}
	desired = old
	desired.Connectors = []config.Connector{
		old.Connectors[0],
		{ID: "p1:out", Type: config.TypeDestination, Plugin: "builtin:file", Settings: map[string]string{"path": "/tmp/x", "sdk.batch.size": "100"}},
	}
	return old, desired
}

// TestApplyPlanLive_ArchV2_RefusesDestinationBatchingBeforeStopping: a live
// apply that adds sdk.batch.size to a RUNNING arch-v2 pipeline is refused with
// the coded error before StopAndWait. The strict lifecycle mock has no
// StopAndWait/Start expectation and the connector mock no Update expectation,
// so stopping the pipeline or committing the config fails the test (#2979).
func TestApplyPlanLive_ArchV2_RefusesDestinationBatchingBeforeStopping(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _ := newArchV2PreflightService(ctrl)

	old, desired := batchingApplyFixture()
	instance := expectExportRunning(pipSrv, connSrv, procSrv, old)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	is.True(!diff.Empty())

	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash, true)

	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, lifecyclev2.CodeArchV2DestinationBatchingUnsupported)
	is.Equal(ce.ConfigPath, "/connectors/1/settings/sdk.batch.size")
	is.Equal(instance.GetStatus(), pipeline.StatusRunning) // untouched
}

// TestApplyPlanLive_NoPreflightOnDefaultEngine pins that the default engine,
// which has no preflight, applies the same config through the normal
// stop-import-start sequence.
func TestApplyPlanLive_NoPreflightOnDefaultEngine(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, lifecycleSrv := newTestService(ctrl, log.Nop())

	old, desired := batchingApplyFixture()
	expectExportRunning(pipSrv, connSrv, procSrv, old)
	lifecycleSrv.EXPECT().StopAndWait(gomock.Any(), "p1").Return(nil)
	connSrv.EXPECT().Update(gomock.Any(), "p1:out", "builtin:file", gomock.Any()).Return(&connector.Instance{ID: "p1:out"}, nil)
	lifecycleSrv.EXPECT().Start(gomock.Any(), "p1").Return(nil)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash, true)
	is.NoErr(err)
}

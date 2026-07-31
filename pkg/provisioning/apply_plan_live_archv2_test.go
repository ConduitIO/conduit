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
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	lifecyclev1 "github.com/conduitio/conduit/pkg/lifecycle"
	lifecyclev2 "github.com/conduitio/conduit/pkg/lifecycle-poc"
	"github.com/conduitio/conduit/pkg/pipeline"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/provisioning/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// The noopV2* types below satisfy lifecycle-poc (arch-v2)'s own small
// PipelineService/ConnectorService/ProcessorService/ConnectorPluginService
// interfaces, so TestApplyPlanLive_ArchV2_ProcessorUpdate_FallsBackToRestart
// can construct a REAL *lifecyclev2.Service (not a mock standing in for it).
// None of their methods are ever invoked by that test: ReconfigureProcessor
// (O1) returns its sentinel unconditionally without touching any dependency,
// and the StopAndWait fallback's Stop call fails fast on "pipeline not
// running" (the pipeline was never Started against this real v2 service)
// before reaching any of them either.
type noopV2PipelineService struct{}

func (noopV2PipelineService) Get(context.Context, string) (*pipeline.Instance, error) {
	return nil, pipeline.ErrInstanceNotFound
}

func (noopV2PipelineService) List(context.Context) map[string]*pipeline.Instance { return nil }

func (noopV2PipelineService) UpdateStatus(context.Context, string, pipeline.Status, string) error {
	return nil
}

type noopV2ConnectorService struct{}

func (noopV2ConnectorService) Get(context.Context, string) (*connector.Instance, error) {
	return nil, connector.ErrInstanceNotFound
}

func (noopV2ConnectorService) Create(context.Context, string, connector.Type, string, string, connector.Config, connector.ProvisionType) (*connector.Instance, error) {
	return nil, cerrors.New("noopV2ConnectorService: not implemented")
}

func (noopV2ConnectorService) WaitPersisted() {}

type noopV2ProcessorService struct{}

func (noopV2ProcessorService) Get(context.Context, string) (*processor.Instance, error) {
	return nil, processor.ErrInstanceNotFound
}

func (noopV2ProcessorService) MakeRunnableProcessor(context.Context, *processor.Instance) (*processor.RunnableProcessor, error) {
	return nil, cerrors.New("noopV2ProcessorService: not implemented")
}

type noopV2ConnectorPluginService struct{}

func (noopV2ConnectorPluginService) NewDispenser(log.CtxLogger, string, string) (connectorPlugin.Dispenser, error) {
	return nil, cerrors.New("noopV2ConnectorPluginService: not implemented")
}

// TestApplyPlanLive_ArchV2_ProcessorUpdate_FallsBackToRestart is the O1
// integration proof (docs/design-documents/20260731-archv2-drain-reconfigure.md):
// unlike every other ApplyPlanLive test in this package, this one wires a REAL
// *lifecycle-poc.Service (Preview.PipelineArchV2's implementation) as
// applyInPlace's lifecycleService, instead of a mock standing in for it. The
// same processorUpdateFixture that applies genuinely in place under
// pkg/lifecycle (TestApplyPlanLive_ProcessorUpdate_AppliesInPlace_NoRestart)
// must, under arch-v2, fall through to the StopAndWait restart path: v2's
// ReconfigureProcessor has no live-swap capability at all and unconditionally
// returns the shared lifecyclev1.ErrProcessorNotLiveReconfigurable sentinel,
// which applyInPlace's existing cerrors.Is match (plan.go) already handles
// without any arch-v2-specific branch — this test pins that the real
// production wiring actually takes that path, not just the mock-level
// contract.
//
// The pipeline was never Started against this real v2 service, so the
// fallback's StopAndWait call fails fast with a
// pipeline.ErrPipelineNotRunning-coded error — that error surfacing (rather
// than a nil, AppliedMode=in_place success) is exactly what proves the
// fallback branch ran instead of a live swap.
func TestApplyPlanLive_ArchV2_ProcessorUpdate_FallsBackToRestart(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	v2Service := lifecyclev2.NewService(
		log.Nop(),
		&lifecyclev1.ErrRecoveryCfg{
			MinDelay:         time.Millisecond,
			MaxDelay:         time.Millisecond,
			BackoffFactor:    2,
			MaxRetries:       0,
			MaxRetriesWindow: time.Minute,
		},
		noopV2ConnectorService{},
		noopV2ProcessorService{},
		noopV2ConnectorPluginService{},
		noopV2PipelineService{},
		true,
	)

	db := &inmemory.DB{}
	pipSrv := mock.NewPipelineService(ctrl)
	connSrv := mock.NewConnectorService(ctrl)
	procSrv := mock.NewProcessorService(ctrl)
	connPlugSrv := mock.NewConnectorPluginService(ctrl)
	srv := NewService(db, log.Nop(), pipSrv, connSrv, procSrv, connPlugSrv, v2Service, "")

	old, desired := processorUpdateFixture()
	expectExportRunning(pipSrv, connSrv, procSrv, old)

	// applyInPlace commits the new processor config to the store BEFORE
	// attempting the (here, always-refused) live swap - same expectation
	// TestApplyPlanLive_ProcessorUpdate_AppliesInPlace_NoRestart sets for the
	// mock-backed happy path.
	procSrv.EXPECT().UpdateWhileRunning(gomock.Any(), "p1:proc:1", "builtin:field.set", gomock.Any()).
		Return(&processor.Instance{ID: "p1:proc:1"}, nil)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	is.True(diff.LiveEligible()) // sanity: a processor-only update is live-eligible

	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash, true) // authorized
	is.True(err != nil)

	// The restart-fallback branch called the real v2 StopAndWait, which
	// refused because no pipeline is registered as running under this ID in
	// that service - the sentinel proving the fallback branch (not an
	// in-place success) is what actually ran.
	is.True(cerrors.Is(err, pipeline.ErrPipelineNotRunning))
}

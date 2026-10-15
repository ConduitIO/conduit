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
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// TestService_DeleteOldPipelines_RetriesAfterHookFailure is the
// provisioning half of #3058. A pipeline removed from the config files is
// deleted on boot. When a connector's delete hook fails, the connector is
// kept and the pipeline delete fails, so the pipeline is not reported as
// deleted. Connectors and processors deleted before the failure stay
// deleted, but the pipeline still references them; the next boot's retry
// must skip those and finish the job instead of failing on them forever.
func TestService_DeleteOldPipelines_RetriesAfterHookFailure(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	service, pipelineService, connService, procService, plugService, _ := newTestService(ctrl, log.Nop())

	pl := oldPipelineInstance
	all := map[string]*pipeline.Instance{pl.ID: pl}
	hookErr := conduiterr.New(connector.CodeConnectorDeleteHookFailed, "delete hook failed")

	// First boot: con2's processor, the pipeline processor and con2 are
	// deleted, then con1's hook fails.
	gomock.InOrder(
		pipelineService.EXPECT().List(anyCtx).Return(all),
		pipelineService.EXPECT().Get(anyCtx, pl.ID).Return(pl, nil).Times(2),
	)
	connService.EXPECT().Get(anyCtx, oldConnector1Instance.ID).Return(oldConnector1Instance, nil)
	connService.EXPECT().Get(anyCtx, oldConnector2Instance.ID).Return(oldConnector2Instance, nil)
	procService.EXPECT().Get(anyCtx, oldConnectorProcessorInstance.ID).Return(oldConnectorProcessorInstance, nil)
	procService.EXPECT().Get(anyCtx, oldPipelineProcessorInstance.ID).Return(oldPipelineProcessorInstance, nil)
	procService.EXPECT().Delete(anyCtx, oldPipelineProcessorInstance.ID).Return(nil)
	procService.EXPECT().Delete(anyCtx, oldConnectorProcessorInstance.ID).Return(nil)
	connService.EXPECT().Delete(anyCtx, oldConnector2Instance.ID, plugService).Return(nil)
	connService.EXPECT().Delete(anyCtx, oldConnector1Instance.ID, plugService).Return(hookErr)

	deleted := service.deleteOldPipelines(ctx, nil)
	is.Equal(len(deleted), 0) // not deleted: con1 and the pipeline are kept for the retry

	// Next boot: the pipeline still lists con2 and both processors, which
	// are gone. They are skipped, con1's hook now succeeds, and the
	// pipeline is deleted.
	gomock.InOrder(
		pipelineService.EXPECT().List(anyCtx).Return(all),
		pipelineService.EXPECT().Get(anyCtx, pl.ID).Return(pl, nil).Times(2),
	)
	connService.EXPECT().Get(anyCtx, oldConnector1Instance.ID).Return(oldConnector1Instance, nil)
	connService.EXPECT().Get(anyCtx, oldConnector2Instance.ID).Return(nil, cerrors.Errorf("gone: %w", connector.ErrInstanceNotFound))
	procService.EXPECT().Get(anyCtx, oldPipelineProcessorInstance.ID).Return(nil, cerrors.Errorf("gone: %w", processor.ErrInstanceNotFound))
	connService.EXPECT().Delete(anyCtx, oldConnector1Instance.ID, plugService).Return(nil)
	pipelineService.EXPECT().Delete(anyCtx, pl.ID).Return(nil)

	deleted = service.deleteOldPipelines(ctx, nil)
	is.Equal(deleted, []string{pl.ID})
}

// TestService_Export_StillFailsOnMissingChild: only deletion skips missing
// children. Export itself must keep reporting them, since outside a delete a
// pipeline pointing at a connector that does not exist is real damage.
func TestService_Export_StillFailsOnMissingChild(t *testing.T) {
	is := is.New(t)
	ctrl := gomock.NewController(t)
	service, pipelineService, connService, _, _, _ := newTestService(ctrl, log.Nop())

	pipelineService.EXPECT().Get(anyCtx, oldPipelineInstance.ID).Return(oldPipelineInstance, nil)
	connService.EXPECT().Get(anyCtx, oldConnector1Instance.ID).Return(nil, cerrors.Errorf("gone: %w", connector.ErrInstanceNotFound))

	_, err := service.Export(context.Background(), oldPipelineInstance.ID)
	is.True(cerrors.Is(err, connector.ErrInstanceNotFound))
}

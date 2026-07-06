// Copyright © 2022 Meroxa, Inc.
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

package orchestrator

import (
	"context"
	"fmt"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/google/uuid"
)

type PipelineOrchestrator base

func (po *PipelineOrchestrator) Start(ctx context.Context, id string) error {
	// TODO lock pipeline
	return po.lifecycle.Start(ctx, id)
}

func (po *PipelineOrchestrator) Stop(ctx context.Context, id string, force bool) error {
	// TODO lock pipeline
	return po.lifecycle.Stop(ctx, id, force)
}

func (po *PipelineOrchestrator) List(ctx context.Context) map[string]*pipeline.Instance {
	return po.pipelines.List(ctx)
}

func (po *PipelineOrchestrator) Get(ctx context.Context, id string) (*pipeline.Instance, error) {
	return po.pipelines.Get(ctx, id)
}

func (po *PipelineOrchestrator) Create(ctx context.Context, cfg pipeline.Config) (*pipeline.Instance, error) {
	return po.pipelines.Create(ctx, uuid.NewString(), cfg, pipeline.ProvisionTypeAPI)
}

func (po *PipelineOrchestrator) Update(ctx context.Context, id string, cfg pipeline.Config) (*pipeline.Instance, error) {
	pl, err := po.pipelines.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if pl.ProvisionedBy != pipeline.ProvisionTypeAPI {
		// Invariant: errors.Is(err, ErrImmutableProvisionedByConfig) still holds —
		// sentinel wrapped, ConduitError adds the code.
		return nil, immutableProvisionedByConfigErr(fmt.Sprintf("pipeline %q cannot be updated", pl.ID))
	}
	// TODO lock pipeline
	if pl.GetStatus() == pipeline.StatusRunning {
		// Invariant: errors.Is(err, ErrPipelineRunning) still holds — sentinel
		// wrapped, ConduitError adds the code.
		return nil, pipelineRunningErr(pipeline.ErrPipelineRunning.Error())
	}
	return po.pipelines.Update(ctx, pl.ID, cfg)
}

func (po *PipelineOrchestrator) Delete(ctx context.Context, id string) error {
	pl, err := po.pipelines.Get(ctx, id)
	if err != nil {
		return err
	}

	if pl.ProvisionedBy != pipeline.ProvisionTypeAPI {
		// Invariant: errors.Is(err, ErrImmutableProvisionedByConfig) still holds —
		// sentinel wrapped, ConduitError adds the code.
		return immutableProvisionedByConfigErr(fmt.Sprintf("pipeline %q cannot be deleted", pl.ID))
	}
	if pl.GetStatus() == pipeline.StatusRunning {
		// Invariant: errors.Is(err, ErrPipelineRunning) still holds — sentinel
		// wrapped, ConduitError adds the code.
		return pipelineRunningErr(pipeline.ErrPipelineRunning.Error())
	}
	if len(pl.ConnectorIDs) != 0 {
		// Invariant: errors.Is(err, ErrPipelineHasConnectorsAttached) still holds —
		// sentinel wrapped, ConduitError adds the code.
		err := conduiterr.Wrap(CodePipelineHasConnectorsAttached, ErrPipelineHasConnectorsAttached.Error(), ErrPipelineHasConnectorsAttached)
		err.Suggestion = "remove the pipeline's connectors first, then delete the pipeline"
		return err
	}
	if len(pl.ProcessorIDs) != 0 {
		// Invariant: errors.Is(err, ErrPipelineHasProcessorsAttached) still holds —
		// sentinel wrapped, ConduitError adds the code.
		err := conduiterr.Wrap(CodePipelineHasProcessorsAttached, ErrPipelineHasProcessorsAttached.Error(), ErrPipelineHasProcessorsAttached)
		err.Suggestion = "remove the pipeline's processors first, then delete the pipeline"
		return err
	}
	return po.pipelines.Delete(ctx, pl.ID)
}

func (po *PipelineOrchestrator) UpdateDLQ(ctx context.Context, id string, dlq pipeline.DLQ) (*pipeline.Instance, error) {
	pl, err := po.pipelines.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if pl.ProvisionedBy != pipeline.ProvisionTypeAPI {
		// Invariant: errors.Is(err, ErrImmutableProvisionedByConfig) still holds —
		// sentinel wrapped, ConduitError adds the code.
		return nil, immutableProvisionedByConfigErr(fmt.Sprintf("pipeline %q cannot be updated", pl.ID))
	}
	// TODO lock pipeline
	if pl.GetStatus() == pipeline.StatusRunning {
		// Invariant: errors.Is(err, ErrPipelineRunning) still holds — sentinel
		// wrapped, ConduitError adds the code.
		return nil, pipelineRunningErr(pipeline.ErrPipelineRunning.Error())
	}

	// cast orchestrator to a ConnectorOrchestrator to get access to the plugin
	// config validate function
	err = (*ConnectorOrchestrator)(po).Validate(
		ctx,
		connector.TypeDestination,
		dlq.Plugin,
		connector.Config{
			// the name of the DLQ connector doesn't matter, the connector is
			// not actually created, and it's not visible to the user
			Name:     "temporary-dlq",
			Settings: dlq.Settings,
		},
	)
	if err != nil {
		return nil, err
	}

	return po.pipelines.UpdateDLQ(ctx, id, dlq)
}

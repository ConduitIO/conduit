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

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/google/uuid"
)

type PipelineOrchestrator base

func (po *PipelineOrchestrator) Start(ctx context.Context, id string) error {
	// TODO lock pipeline
	return po.pipelines.Start(ctx, po.connectors, po.processors, po.connectorPlugins, id)
}

func (po *PipelineOrchestrator) Stop(ctx context.Context, id string, force bool) error {
	// TODO lock pipeline
	return po.pipelines.Stop(ctx, id, force)
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
		return nil, cerrors.Errorf("pipeline %q cannot be updated: %w", pl.ID, ErrImmutableProvisionedByConfig)
	}
	// TODO lock pipeline
	if pl.GetStatus() == pipeline.StatusRunning {
		return nil, pipeline.ErrPipelineRunning
	}
	return po.pipelines.Update(ctx, pl.ID, cfg)
}

func (po *PipelineOrchestrator) Delete(ctx context.Context, id string) error {
	pl, err := po.pipelines.Get(ctx, id)
	if err != nil {
		return err
	}

	if pl.ProvisionedBy != pipeline.ProvisionTypeAPI {
		return cerrors.Errorf("pipeline %q cannot be deleted: %w", pl.ID, ErrImmutableProvisionedByConfig)
	}
	if pl.GetStatus() == pipeline.StatusRunning {
		return pipeline.ErrPipelineRunning
	}
	if len(pl.ConnectorIDs) != 0 {
		return ErrPipelineHasConnectorsAttached
	}
	if len(pl.ProcessorIDs) != 0 {
		return ErrPipelineHasProcessorsAttached
	}
	return po.pipelines.Delete(ctx, pl.ID)
}

func (po *PipelineOrchestrator) UpdateDLQ(ctx context.Context, id string, dlq pipeline.DLQ) (*pipeline.Instance, error) {
	pl, err := po.pipelines.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if pl.ProvisionedBy != pipeline.ProvisionTypeAPI {
		return nil, cerrors.Errorf("pipeline %q cannot be updated: %w", pl.ID, ErrImmutableProvisionedByConfig)
	}
	// TODO lock pipeline
	if pl.GetStatus() == pipeline.StatusRunning {
		return nil, pipeline.ErrPipelineRunning
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

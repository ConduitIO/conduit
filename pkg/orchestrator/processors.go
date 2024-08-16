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

	"github.com/conduitio/conduit-commons/rollback"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/inspector"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/google/uuid"
)

type ProcessorOrchestrator base

func (p *ProcessorOrchestrator) Create(
	ctx context.Context,
	plugin string,
	parent processor.Parent,
	cfg processor.Config,
	cond string,
) (*processor.Instance, error) {
	var r rollback.R
	defer r.MustExecute()

	txn, ctx, err := p.db.NewTransaction(ctx, true)
	if err != nil {
		return nil, cerrors.Errorf("could not create db transaction: %w", err)
	}
	r.AppendPure(txn.Discard)

	pl, err := p.getProcessorsPipeline(ctx, parent)
	if err != nil {
		return nil, err
	}

	// check if pipeline was provisioned by config
	if pl.ProvisionedBy != pipeline.ProvisionTypeAPI {
		return nil, cerrors.Errorf("cannot add a processor to the pipeline %q: %w", pl.ID, ErrImmutableProvisionedByConfig)
	}

	if pl.GetStatus() == pipeline.StatusRunning {
		return nil, pipeline.ErrPipelineRunning
	}

	// create processor and add to pipeline or connector
	proc, err := p.processors.Create(
		ctx,
		uuid.NewString(),
		plugin,
		parent,
		cfg,
		processor.ProvisionTypeAPI,
		cond,
	)
	if err != nil {
		return nil, err
	}
	r.Append(func() error { return p.processors.Delete(ctx, proc.ID) })

	switch parent.Type {
	case processor.ParentTypePipeline:
		_, err = p.pipelines.AddProcessor(ctx, pl.ID, proc.ID)
		if err != nil {
			return nil, cerrors.Errorf("could not add processor to pipeline: %w", err)
		}
		r.Append(func() error {
			_, err := p.pipelines.RemoveProcessor(ctx, pl.ID, proc.ID)
			return err
		})
	case processor.ParentTypeConnector:
		_, err = p.connectors.AddProcessor(ctx, parent.ID, proc.ID)
		if err != nil {
			return nil, cerrors.Errorf("could not add processor to connector: %w", err)
		}
		r.Append(func() error {
			_, err := p.connectors.RemoveProcessor(ctx, parent.ID, proc.ID)
			return err
		})
	default:
		return nil, cerrors.Errorf("%w: %s", ErrInvalidProcessorParentType, parent.Type)
	}

	// commit db transaction and skip rollback
	err = txn.Commit()
	if err != nil {
		return nil, cerrors.Errorf("could not commit db transaction: %w", err)
	}

	r.Skip() // skip rollback
	return proc, err
}

func (p *ProcessorOrchestrator) List(ctx context.Context) map[string]*processor.Instance {
	return p.processors.List(ctx)
}

func (p *ProcessorOrchestrator) InspectIn(
	ctx context.Context,
	id string,
) (*inspector.Session, error) {
	proc, err := p.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	return proc.InspectIn(ctx, proc.ID), nil
}

func (p *ProcessorOrchestrator) InspectOut(
	ctx context.Context,
	id string,
) (*inspector.Session, error) {
	proc, err := p.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	return proc.InspectOut(ctx, proc.ID), nil
}

func (p *ProcessorOrchestrator) Get(ctx context.Context, id string) (*processor.Instance, error) {
	return p.processors.Get(ctx, id)
}

func (p *ProcessorOrchestrator) Update(ctx context.Context, id string, cfg processor.Config) (*processor.Instance, error) {
	var r rollback.R
	defer r.MustExecute()

	txn, ctx, err := p.db.NewTransaction(ctx, true)
	if err != nil {
		return nil, cerrors.Errorf("could not create db transaction: %w", err)
	}
	r.AppendPure(txn.Discard)

	proc, err := p.processors.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	// check if processor was provisioned by config
	if proc.ProvisionedBy != processor.ProvisionTypeAPI {
		return nil, cerrors.Errorf("processor %q cannot be updated: %w", proc.ID, ErrImmutableProvisionedByConfig)
	}
	// provisioned by API
	oldConfig := proc.Config

	pl, err := p.getProcessorsPipeline(ctx, proc.Parent)
	if err != nil {
		return nil, err
	}

	if pl.GetStatus() == pipeline.StatusRunning {
		return nil, pipeline.ErrPipelineRunning
	}

	proc, err = p.processors.Update(ctx, id, cfg)
	if err != nil {
		return nil, err
	}
	r.Append(func() error {
		_, err = p.processors.Update(ctx, proc.ID, oldConfig)
		return err
	})

	// commit db transaction and skip rollback
	err = txn.Commit()
	if err != nil {
		return nil, cerrors.Errorf("could not commit db transaction: %w", err)
	}

	r.Skip() // skip rollback
	return proc, err
}

func (p *ProcessorOrchestrator) Delete(ctx context.Context, id string) error {
	var r rollback.R
	defer r.MustExecute()

	txn, ctx, err := p.db.NewTransaction(ctx, true)
	if err != nil {
		return cerrors.Errorf("could not create db transaction: %w", err)
	}
	r.AppendPure(txn.Discard)

	proc, err := p.processors.Get(ctx, id)
	if err != nil {
		return err
	}

	// check if processor was provisioned by config
	if proc.ProvisionedBy != processor.ProvisionTypeAPI {
		return cerrors.Errorf("processor %q cannot be deleted: %w", proc.ID, ErrImmutableProvisionedByConfig)
	}

	pl, err := p.getProcessorsPipeline(ctx, proc.Parent)
	if err != nil {
		return err
	}

	if pl.GetStatus() == pipeline.StatusRunning {
		return pipeline.ErrPipelineRunning
	}

	err = p.processors.Delete(ctx, id)
	if err != nil {
		return err
	}
	r.Append(func() error {
		_, err = p.processors.Create(ctx, id, proc.Plugin, proc.Parent, proc.Config, processor.ProvisionTypeAPI, proc.Condition)
		return err
	})

	switch proc.Parent.Type {
	case processor.ParentTypePipeline:
		_, err = p.pipelines.RemoveProcessor(ctx, pl.ID, proc.ID)
		if err != nil {
			return cerrors.Errorf("could not add processor to pipeline: %w", err)
		}
		r.Append(func() error {
			_, err := p.pipelines.AddProcessor(ctx, pl.ID, proc.ID)
			return err
		})
	case processor.ParentTypeConnector:
		_, err = p.connectors.RemoveProcessor(ctx, proc.Parent.ID, proc.ID)
		if err != nil {
			return cerrors.Errorf("could not add processor to connector: %w", err)
		}
		r.Append(func() error {
			_, err := p.connectors.AddProcessor(ctx, proc.Parent.ID, proc.ID)
			return err
		})
	default:
		return cerrors.Errorf("%w: %s", ErrInvalidProcessorParentType, proc.Parent.Type)
	}

	// commit db transaction and skip rollback
	err = txn.Commit()
	if err != nil {
		return cerrors.Errorf("could not commit db transaction: %w", err)
	}

	r.Skip() // skip rollback
	return err
}

func (p *ProcessorOrchestrator) getProcessorsPipeline(ctx context.Context, parent processor.Parent) (*pipeline.Instance, error) {
	switch parent.Type {
	case processor.ParentTypePipeline:
		return p.pipelines.Get(ctx, parent.ID)
	case processor.ParentTypeConnector:
		conn, err := p.connectors.Get(ctx, parent.ID)
		if err != nil {
			return nil, err
		}
		return p.pipelines.Get(ctx, conn.PipelineID)
	default:
		return nil, cerrors.Errorf("%w: %s", ErrInvalidProcessorParentType, parent.Type)
	}
}

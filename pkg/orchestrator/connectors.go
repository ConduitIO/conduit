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
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/inspector"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/google/uuid"
)

type ConnectorOrchestrator base

func (c *ConnectorOrchestrator) Inspect(ctx context.Context, id string) (*inspector.Session, error) {
	conn, err := c.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	return conn.Inspect(ctx), nil
}

func (c *ConnectorOrchestrator) Create(
	ctx context.Context,
	t connector.Type,
	plugin string,
	pipelineID string,
	config connector.Config,
) (*connector.Instance, error) {
	var r rollback.R
	defer r.MustExecute()

	txn, ctx, err := c.db.NewTransaction(ctx, true)
	if err != nil {
		return nil, cerrors.Errorf("could not create db transaction: %w", err)
	}
	r.AppendPure(txn.Discard)

	// TODO lock pipeline
	pl, err := c.pipelines.Get(ctx, pipelineID)
	if err != nil {
		return nil, cerrors.Errorf("couldn't get pipeline: %w", err)
	}

	if pl.ProvisionedBy != pipeline.ProvisionTypeAPI {
		return nil, cerrors.Errorf("cannot add a connector to the pipeline %q: %w", pl.ID, ErrImmutableProvisionedByConfig)
	}
	if *pl.Status.Load() == pipeline.StatusRunning {
		return nil, cerrors.Errorf("cannot create connector: %w", pipeline.ErrPipelineRunning)
	}

	err = c.Validate(ctx, t, plugin, config)
	if err != nil {
		return nil, err
	}

	conn, err := c.connectors.Create(ctx, uuid.NewString(), t, plugin, pl.ID, config, connector.ProvisionTypeAPI)
	if err != nil {
		return nil, err
	}
	r.Append(func() error { return c.connectors.Delete(ctx, conn.ID, c.connectorPlugins) })

	_, err = c.pipelines.AddConnector(ctx, pl.ID, conn.ID)
	if err != nil {
		return nil, cerrors.Errorf("couldn't add connector %v to pipeline %v: %w", conn.ID, pl.ID, err)
	}
	r.Append(func() error {
		_, err := c.pipelines.RemoveConnector(ctx, pl.ID, conn.ID)
		return err
	})

	err = txn.Commit()
	if err != nil {
		return nil, cerrors.Errorf("could not commit db transaction: %w", err)
	}

	r.Skip()
	return conn, nil
}

func (c *ConnectorOrchestrator) List(ctx context.Context) map[string]*connector.Instance {
	return c.connectors.List(ctx)
}

func (c *ConnectorOrchestrator) Get(ctx context.Context, id string) (*connector.Instance, error) {
	return c.connectors.Get(ctx, id)
}

func (c *ConnectorOrchestrator) Delete(ctx context.Context, id string) error {
	var r rollback.R
	defer r.MustExecute()
	txn, ctx, err := c.db.NewTransaction(ctx, true)
	if err != nil {
		return cerrors.Errorf("could not create db transaction: %w", err)
	}
	r.AppendPure(txn.Discard)
	conn, err := c.connectors.Get(ctx, id)
	if err != nil {
		return err
	}
	if conn.ProvisionedBy != connector.ProvisionTypeAPI {
		return cerrors.Errorf("connector %q cannot be deleted: %w", conn.ID, ErrImmutableProvisionedByConfig)
	}
	if len(conn.ProcessorIDs) != 0 {
		return ErrConnectorHasProcessorsAttached
	}
	pl, err := c.pipelines.Get(ctx, conn.PipelineID)
	if err != nil {
		return err
	}
	if *pl.Status.Load() == pipeline.StatusRunning {
		return pipeline.ErrPipelineRunning
	}
	err = c.connectors.Delete(ctx, id, c.connectorPlugins)
	if err != nil {
		return err
	}
	r.Append(func() error {
		_, err = c.connectors.Create(ctx, id, conn.Type, conn.Plugin, conn.PipelineID, conn.Config, conn.ProvisionedBy)
		return err
	})
	_, err = c.pipelines.RemoveConnector(ctx, pl.ID, id)
	if err != nil {
		return err
	}
	r.Append(func() error {
		_, err := c.pipelines.AddConnector(ctx, pl.ID, id)
		return err
	})
	err = txn.Commit()
	if err != nil {
		return cerrors.Errorf("could not commit db transaction: %w", err)
	}
	r.Skip()
	return nil
}

func (c *ConnectorOrchestrator) Update(ctx context.Context, id string, config connector.Config) (*connector.Instance, error) {
	var r rollback.R
	defer r.MustExecute()
	txn, ctx, err := c.db.NewTransaction(ctx, true)
	if err != nil {
		return nil, cerrors.Errorf("could not create db transaction: %w", err)
	}
	r.AppendPure(txn.Discard)
	conn, err := c.connectors.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if conn.ProvisionedBy != connector.ProvisionTypeAPI {
		return nil, cerrors.Errorf("connector %q cannot be updated: %w", conn.ID, ErrImmutableProvisionedByConfig)
	}

	pl, err := c.pipelines.Get(ctx, conn.PipelineID)
	if err != nil {
		return nil, err
	}
	if *pl.Status.Load() == pipeline.StatusRunning {
		return nil, pipeline.ErrPipelineRunning
	}

	err = c.Validate(ctx, conn.Type, conn.Plugin, config)
	if err != nil {
		return nil, err
	}

	oldConfig := conn.Config
	conn, err = c.connectors.Update(ctx, id, config)
	if err != nil {
		return nil, err
	}
	r.Append(func() error {
		_, err = c.connectors.Update(ctx, id, oldConfig)
		return err
	})
	err = txn.Commit()
	if err != nil {
		return nil, cerrors.Errorf("could not commit db transaction: %w", err)
	}
	r.Skip()
	return conn, nil
}

func (c *ConnectorOrchestrator) Validate(
	ctx context.Context,
	t connector.Type,
	plugin string,
	config connector.Config,
) error {
	var err error
	switch t {
	case connector.TypeSource:
		err = c.connectorPlugins.ValidateSourceConfig(ctx, plugin, config.Settings)
	case connector.TypeDestination:
		err = c.connectorPlugins.ValidateDestinationConfig(ctx, plugin, config.Settings)
	default:
		return cerrors.New("invalid connector type")
	}

	if err != nil {
		return cerrors.Errorf("invalid connector config: %w", err)
	}

	return nil
}

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
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/rollback"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type ConnectorOrchestrator base

func (c *ConnectorOrchestrator) Create(
	ctx context.Context,
	t connector.Type,
	config connector.Config,
) (connector.Connector, error) {
	var r rollback.R
	defer r.MustExecute()

	txn, ctx, err := c.db.NewTransaction(ctx, true)
	if err != nil {
		return nil, cerrors.Errorf("could not create db transaction: %w", err)
	}
	r.AppendPure(txn.Discard)

	// TODO lock pipeline
	pl, err := c.pipelines.Get(ctx, config.PipelineID)
	if err != nil {
		return nil, cerrors.Errorf("couldn't get pipeline: %w", err)
	}

	if pl.Status == pipeline.StatusRunning {
		return nil, cerrors.Errorf("cannot create connector: %w", pipeline.ErrPipelineRunning)
	}

	conn, err := c.connectors.Create(ctx, uuid.NewString(), t, config)
	if err != nil {
		return nil, err
	}
	r.Append(func() error { return c.connectors.Delete(ctx, conn.ID()) })

	_, err = c.pipelines.AddConnector(ctx, pl, conn.ID())
	if err != nil {
		return nil, cerrors.Errorf("couldn't add connector %v to pipeline %v: %w", conn.ID(), pl.ID, err)
	}
	r.Append(func() error {
		_, err := c.pipelines.RemoveConnector(ctx, pl, conn.ID())
		return err
	})

	err = txn.Commit()
	if err != nil {
		return nil, cerrors.Errorf("could not commit db transaction: %w", err)
	}

	r.Skip()
	return conn, nil
}

func (c *ConnectorOrchestrator) List(ctx context.Context) map[string]connector.Connector {
	return c.connectors.List(ctx)
}

func (c *ConnectorOrchestrator) Get(ctx context.Context, id string) (connector.Connector, error) {
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
	if len(conn.Config().ProcessorIDs) != 0 {
		return ErrConnectorHasProcessorsAttached
	}
	pl, err := c.pipelines.Get(ctx, conn.Config().PipelineID)
	if err != nil {
		return err
	}
	if pl.Status == pipeline.StatusRunning {
		return pipeline.ErrPipelineRunning
	}
	err = c.connectors.Delete(ctx, id)
	if err != nil {
		return err
	}
	r.Append(func() error {
		_, err = c.connectors.Create(ctx, id, conn.Type(), conn.Config())
		return err
	})
	_, err = c.pipelines.RemoveConnector(ctx, pl, id)
	if err != nil {
		return err
	}
	r.Append(func() error {
		_, err = c.pipelines.AddConnector(ctx, pl, id)
		return err
	})
	err = txn.Commit()
	if err != nil {
		return cerrors.Errorf("could not commit db transaction: %w", err)
	}
	r.Skip()
	return nil
}

func (c *ConnectorOrchestrator) Update(ctx context.Context, id string, config connector.Config) (connector.Connector, error) {
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
	oldConfig := conn.Config()
	pl, err := c.pipelines.Get(ctx, conn.Config().PipelineID)
	if err != nil {
		return nil, err
	}
	if pl.Status == pipeline.StatusRunning {
		return nil, pipeline.ErrPipelineRunning
	}
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
	config connector.Config,
) error {
	logger := log.CtxLogger{Logger: zerolog.Nop()}
	d, err := c.plugins.NewDispenser(logger, config.Plugin)
	if err != nil {
		return cerrors.Errorf("couldn't get dispenser: %w", err)
	}

	switch t {
	case connector.TypeSource:
		src, _ := d.DispenseSource()
		err := src.Configure(ctx, config.Settings)
		if err != nil {
			return cerrors.Errorf("configurations failed: %w", err)
		}
		err = src.Teardown(ctx)
		if err != nil {
			return cerrors.Errorf("couldn't teardown the connector: %w", err)
		}
	case connector.TypeDestination:
		dest, _ := d.DispenseSource()
		err := dest.Configure(ctx, config.Settings)
		if err != nil {
			return cerrors.Errorf("configurations failed: %w", err)
		}
		err = dest.Teardown(ctx)
		if err != nil {
			return cerrors.Errorf("couldn't teardown the connector: %w", err)
		}
	default:
		return cerrors.Errorf("invalid connector type: %w", err)
	}

	return nil
}

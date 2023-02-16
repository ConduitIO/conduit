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

//go:generate mockgen -destination=mock/orchestrator.go -package=mock -mock_names=PipelineService=PipelineService,ConnectorService=ConnectorService,ProcessorService=ProcessorService,PluginService=PluginService . PipelineService,ConnectorService,ProcessorService,PluginService

package orchestrator

import (
	"context"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/processor"
)

type Orchestrator struct {
	Processors *ProcessorOrchestrator
	Pipelines  *PipelineOrchestrator
	Connectors *ConnectorOrchestrator
	Plugins    *PluginOrchestrator
}

func NewOrchestrator(
	db database.DB,
	logger log.CtxLogger,
	pipelines PipelineService,
	connectors ConnectorService,
	processors ProcessorService,
	plugins PluginService,
) *Orchestrator {
	b := base{
		db:         db,
		logger:     logger.WithComponent("orchestrator"),
		pipelines:  pipelines,
		connectors: connectors,
		processors: processors,
		plugins:    plugins,
	}

	return &Orchestrator{
		Processors: (*ProcessorOrchestrator)(&b),
		Pipelines:  (*PipelineOrchestrator)(&b),
		Connectors: (*ConnectorOrchestrator)(&b),
		Plugins:    (*PluginOrchestrator)(&b),
	}
}

type base struct {
	db     database.DB
	logger log.CtxLogger

	pipelines  PipelineService
	connectors ConnectorService
	processors ProcessorService
	plugins    PluginService
}

type PipelineService interface {
	Start(ctx context.Context, connFetcher pipeline.ConnectorFetcher, procFetcher pipeline.ProcessorFetcher, pluginFetcher pipeline.PluginDispenserFetcher, pipelineID string) error
	// Stop initiates a stop of the given pipeline. The method does not wait for
	// the pipeline (and its nodes) to actually stop.
	// When force is false the pipeline will try to stop gracefully and drain
	// any in-flight messages that have not yet reached the destination. When
	// force is true the pipeline will stop without draining in-flight messages.
	// It is allowed to execute a force stop even after a graceful stop was
	// requested.
	Stop(ctx context.Context, pipelineID string, force bool) error

	List(ctx context.Context) map[string]*pipeline.Instance
	Get(ctx context.Context, id string) (*pipeline.Instance, error)
	Create(ctx context.Context, id string, cfg pipeline.Config, p pipeline.ProvisionType) (*pipeline.Instance, error)
	Update(ctx context.Context, pipelineID string, cfg pipeline.Config) (*pipeline.Instance, error)
	Delete(ctx context.Context, pipelineID string) error
	UpdateDLQ(ctx context.Context, id string, dlq pipeline.DLQ) (*pipeline.Instance, error)

	AddConnector(ctx context.Context, pipelineID string, connectorID string) (*pipeline.Instance, error)
	RemoveConnector(ctx context.Context, pipelineID string, connectorID string) (*pipeline.Instance, error)
	AddProcessor(ctx context.Context, pipelineID string, processorID string) (*pipeline.Instance, error)
	RemoveProcessor(ctx context.Context, pipelineID string, processorID string) (*pipeline.Instance, error)
}

type ConnectorService interface {
	List(ctx context.Context) map[string]*connector.Instance
	Get(ctx context.Context, id string) (*connector.Instance, error)
	Create(ctx context.Context, id string, t connector.Type, plugin string, pipelineID string, c connector.Config, p connector.ProvisionType) (*connector.Instance, error)
	Delete(ctx context.Context, id string) error
	Update(ctx context.Context, id string, c connector.Config) (*connector.Instance, error)

	AddProcessor(ctx context.Context, connectorID string, processorID string) (*connector.Instance, error)
	RemoveProcessor(ctx context.Context, connectorID string, processorID string) (*connector.Instance, error)
}

type ProcessorService interface {
	List(ctx context.Context) map[string]*processor.Instance
	Get(ctx context.Context, id string) (*processor.Instance, error)
	Create(ctx context.Context, id string, procType string, parent processor.Parent, cfg processor.Config, p processor.ProvisionType) (*processor.Instance, error)
	Update(ctx context.Context, id string, cfg processor.Config) (*processor.Instance, error)
	Delete(ctx context.Context, id string) error
}

type PluginService interface {
	List(ctx context.Context) (map[string]plugin.Specification, error)
	NewDispenser(logger log.CtxLogger, name string) (plugin.Dispenser, error)
	ValidateSourceConfig(ctx context.Context, d plugin.Dispenser, settings map[string]string) error
	ValidateDestinationConfig(ctx context.Context, d plugin.Dispenser, settings map[string]string) error
}

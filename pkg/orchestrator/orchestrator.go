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
	pipelines PipelineService,
	connectors ConnectorService,
	processors ProcessorService,
	plugins PluginService,
) *Orchestrator {
	b := base{
		db:         db,
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
	db database.DB

	pipelines  PipelineService
	connectors ConnectorService
	processors ProcessorService
	plugins    PluginService
}

type PipelineService interface {
	Start(ctx context.Context, connFetcher pipeline.ConnectorFetcher, procFetcher pipeline.ProcessorFetcher, pipeline *pipeline.Instance) error
	// Stop initiates a graceful shutdown of the given pipeline and sets its status to the provided status.
	// The method does not wait for the pipeline (and its nodes) to actually stop,
	// because there still might be some in-flight messages.
	Stop(ctx context.Context, pipeline *pipeline.Instance) error

	List(ctx context.Context) map[string]*pipeline.Instance
	Get(ctx context.Context, id string) (*pipeline.Instance, error)
	Create(ctx context.Context, id string, cfg pipeline.Config) (*pipeline.Instance, error)
	Update(ctx context.Context, pl *pipeline.Instance, cfg pipeline.Config) (*pipeline.Instance, error)
	Delete(ctx context.Context, pl *pipeline.Instance) error

	AddConnector(ctx context.Context, pl *pipeline.Instance, connectorID string) (*pipeline.Instance, error)
	RemoveConnector(ctx context.Context, pl *pipeline.Instance, connectorID string) (*pipeline.Instance, error)
	AddProcessor(ctx context.Context, pl *pipeline.Instance, processorID string) (*pipeline.Instance, error)
	RemoveProcessor(ctx context.Context, pl *pipeline.Instance, processorID string) (*pipeline.Instance, error)
}

type ConnectorService interface {
	List(ctx context.Context) map[string]connector.Connector
	Get(ctx context.Context, id string) (connector.Connector, error)
	Create(ctx context.Context, id string, t connector.Type, c connector.Config) (connector.Connector, error)
	Delete(ctx context.Context, id string) error
	Update(ctx context.Context, id string, c connector.Config) (connector.Connector, error)

	AddProcessor(ctx context.Context, connectorID string, processorID string) (connector.Connector, error)
	RemoveProcessor(ctx context.Context, connectorID string, processorID string) (connector.Connector, error)
}

type ProcessorService interface {
	List(ctx context.Context) map[string]*processor.Instance
	Get(ctx context.Context, id string) (*processor.Instance, error)
	Create(ctx context.Context, id string, name string, t processor.Type, parent processor.Parent, cfg processor.Config) (*processor.Instance, error)
	Update(ctx context.Context, id string, cfg processor.Config) (*processor.Instance, error)
	Delete(ctx context.Context, id string) error
}

type PluginService interface {
	List(ctx context.Context) (map[string]plugin.Specification, error)
	NewDispenser(logger log.CtxLogger, name string) (plugin.Dispenser, error)
}

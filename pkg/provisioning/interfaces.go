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

package provisioning

import (
	"context"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/processor"
)

//go:generate mockgen -destination=mock/provisioning.go -package=mock -mock_names=PipelineService=PipelineService,ConnectorService=ConnectorService,ProcessorService=ProcessorService,PluginService=PluginService . PipelineService,ConnectorService,ProcessorService,PluginService

type PipelineService interface {
	Get(ctx context.Context, id string) (*pipeline.Instance, error)
	List(ctx context.Context) map[string]*pipeline.Instance
	Create(ctx context.Context, id string, cfg pipeline.Config, p pipeline.ProvisionType) (*pipeline.Instance, error)
	Update(ctx context.Context, pipelineID string, cfg pipeline.Config) (*pipeline.Instance, error)
	Delete(ctx context.Context, pipelineID string) error
	UpdateDLQ(ctx context.Context, pipelineID string, cfg pipeline.DLQ) (*pipeline.Instance, error)

	Start(ctx context.Context, connFetcher pipeline.ConnectorFetcher, procFetcher pipeline.ProcessorFetcher, pluginFetcher pipeline.PluginDispenserFetcher, pipelineID string) error
	Stop(ctx context.Context, pipelineID string, force bool) error

	AddConnector(ctx context.Context, pipelineID string, connectorID string) (*pipeline.Instance, error)
	RemoveConnector(ctx context.Context, pipelineID string, connectorID string) (*pipeline.Instance, error)
	AddProcessor(ctx context.Context, pipelineID string, processorID string) (*pipeline.Instance, error)
	RemoveProcessor(ctx context.Context, pipelineID string, processorID string) (*pipeline.Instance, error)
}

type ConnectorService interface {
	Get(ctx context.Context, id string) (*connector.Instance, error)
	Create(ctx context.Context, id string, t connector.Type, plugin string, pipelineID string, c connector.Config, p connector.ProvisionType) (*connector.Instance, error)
	Update(ctx context.Context, id string, c connector.Config) (*connector.Instance, error)
	Delete(ctx context.Context, id string, dispenserFetcher connector.PluginDispenserFetcher) error

	AddProcessor(ctx context.Context, connectorID string, processorID string) (*connector.Instance, error)
	RemoveProcessor(ctx context.Context, connectorID string, processorID string) (*connector.Instance, error)
}

type ProcessorService interface {
	Get(ctx context.Context, id string) (*processor.Instance, error)
	Create(ctx context.Context, id string, procType string, parent processor.Parent, cfg processor.Config, p processor.ProvisionType) (*processor.Instance, error)
	Update(ctx context.Context, id string, cfg processor.Config) (*processor.Instance, error)
	Delete(ctx context.Context, id string) error
}

type PluginService interface {
	NewDispenser(ctx log.CtxLogger, name string) (plugin.Dispenser, error)
}

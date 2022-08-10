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
	"io/ioutil"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
)

var update = false

type Service struct {
	ctx              *context.Context
	logger           log.CtxLogger
	pipelineService  *pipeline.Service
	connectorService *connector.Service
	processorService *processor.Service
}

func GetService(
	ctx *context.Context,
	logger log.CtxLogger,
	plService *pipeline.Service,
	connService *connector.Service,
	procService *processor.Service,
) *Service {
	return &Service{
		ctx:              ctx,
		logger:           logger.WithComponent("provision"),
		pipelineService:  plService,
		connectorService: connService,
		processorService: procService,
	}
}

func (s *Service) ProvisionConfigFile(path string) error {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}

	before, err := Parse(data)
	if err != nil {
		return err
	}

	got := EnrichPipelinesConfig(before)

	for k, v := range got {
		// skip non valid? multierror?
		err = ValidatePipelinesConfig(v)
		if err != nil {
			return err
		}
		err = s.provisionPipeline(k, v)
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) provisionPipeline(id string, config PipelineConfig) error {
	var destMap map[string]connector.DestinationState
	var srcMap map[string]connector.SourceState
	var err error
	oldPl, err := s.pipelineService.Get(*s.ctx, id)
	if err == nil {
		update = true
		destMap, srcMap, err = s.copyStatesAndDeletePipeline(oldPl, config)
		if err != nil {
			return err
		}
	} else {
		update = false
	}

	newPl, err := s.createPipeline(id, config)
	if err != nil {
		return err
	}
	for k, cfg := range config.Connectors {
		err := s.createConnector(id, k, cfg, destMap, srcMap)
		if err != nil {
			return err
		}
		_, err = s.pipelineService.AddConnector(*s.ctx, newPl, k)
		if err != nil {
			return err
		}
		for k1, cfg1 := range cfg.Processors {
			err := s.createProcessor(id, processor.ParentTypeConnector, k1, cfg1)
			if err != nil {
				return err
			}
			_, err = s.connectorService.AddProcessor(*s.ctx, k, k1)
			if err != nil {
				return err
			}
		}
	}
	for k, cfg := range config.Processors {
		err := s.createProcessor(id, processor.ParentTypePipeline, k, cfg)
		if err != nil {
			return err
		}
		_, err = s.pipelineService.AddProcessor(*s.ctx, newPl, k)
		if err != nil {
			return err
		}
	}

	// set pipeline status
	status := pipeline.StatusUserStopped
	if config.Status == StatusRunning {
		err := s.pipelineService.Start(*s.ctx, s.connectorService, s.processorService, newPl)
		if err != nil {
			return err
		}
		status = pipeline.StatusRunning
	}
	newPl.Status = status

	return nil
}

func (s *Service) copyStatesAndDeletePipeline(pl *pipeline.Instance, config PipelineConfig) (map[string]connector.DestinationState, map[string]connector.SourceState, error) {
	destMap := make(map[string]connector.DestinationState, 1)
	srcMap := make(map[string]connector.SourceState, 1)

	for k := range config.Connectors {
		destState, srcState, err := s.getConnectorState(k)
		if err != nil {
			return nil, nil, err
		}
		destMap[k] = destState
		srcMap[k] = srcState
	}

	err := s.pipelineService.Delete(*s.ctx, pl)
	if err != nil {
		return nil, nil, err
	}

	return destMap, srcMap, nil
}

func (s *Service) createPipeline(id string, config PipelineConfig) (*pipeline.Instance, error) {
	cfg := pipeline.Config{
		Name:        config.Name,
		Description: config.Description,
	}

	pl, err := s.pipelineService.Create(*s.ctx, id, cfg, pipeline.ProvisionTypeConfig)
	if err != nil {
		return nil, err
	}
	return pl, nil
}

func (s *Service) createConnector(pipelineID string, id string, config ConnectorConfig, destMap map[string]connector.DestinationState, srcMap map[string]connector.SourceState) error {
	cfg := connector.Config{
		Name:       config.Name,
		Plugin:     config.Plugin,
		PipelineID: pipelineID,
		Settings:   config.Settings,
	}

	connType := connector.TypeSource
	if config.Type == TypeDestination {
		connType = connector.TypeDestination
	}

	conn, err := s.connectorService.Create(*s.ctx, id, connType, cfg, connector.ProvisionTypeConfig)
	if err != nil {
		return err
	}

	if update {
		switch v := conn.(type) {
		case connector.Destination:
			v.SetState(destMap[id])
		case connector.Source:
			v.SetState(srcMap[id])
		}
	}

	return nil
}

func (s *Service) getConnectorState(id string) (connector.DestinationState, connector.SourceState, error) {
	var destState connector.DestinationState
	var srcState connector.SourceState
	conn, err := s.connectorService.Get(*s.ctx, id)
	if err != nil {
		return destState, srcState, err
	}
	switch v := conn.(type) {
	case connector.Destination:
		destState = v.State()

	case connector.Source:
		srcState = v.State()
	}
	return destState, srcState, nil
}

func (s *Service) createProcessor(parentID string, parentType processor.ParentType, id string, config ProcessorConfig) error {
	cfg := processor.Config{
		Settings: config.Settings,
	}
	parent := processor.Parent{
		ID:   parentID,
		Type: parentType,
	}
	// processor name will be renamed to type https://github.com/ConduitIO/conduit/issues/498
	_, err := s.processorService.Create(*s.ctx, id, config.Type, parent, cfg, processor.ProvisionTypeConfig)
	if err != nil {
		return err
	}
	return nil
}

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
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/foundation/rollback"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
)

type Service struct {
	db               database.DB
	logger           log.CtxLogger
	pipelineService  PipelineService
	connectorService ConnectorService
	processorService ProcessorService
}

func NewService(
	db database.DB,
	logger log.CtxLogger,
	plService PipelineService,
	connService ConnectorService,
	procService ProcessorService,
) *Service {
	return &Service{
		db:               db,
		logger:           logger.WithComponent("provision"),
		pipelineService:  plService,
		connectorService: connService,
		processorService: procService,
	}
}

func (s *Service) ProvisionConfigFile(ctx context.Context, path string) error {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return cerrors.Errorf("could not read the file %q: %w", path, err)
	}

	before, err := Parse(data)
	if err != nil {
		return err
	}

	got := EnrichPipelinesConfig(before)

	var multi error
	for k, v := range got {
		err = ValidatePipelinesConfig(v)
		if err != nil {
			multi = multierror.Append(multi, cerrors.Errorf("invalid pipeline config: %w", err))
			continue
		}
		err = s.provisionPipeline(ctx, k, v)
		if err != nil {
			// should we return, or add one to the multierror
			return cerrors.Errorf("error while provisioning the pipeline %q: %w", k, err)
		}
	}

	return multi
}

func (s *Service) provisionPipeline(ctx context.Context, id string, config PipelineConfig) error {
	var err error
	var destMap map[string]connector.DestinationState
	var srcMap map[string]connector.SourceState

	var r rollback.R
	defer r.MustExecute()
	txn, ctx, err := s.db.NewTransaction(ctx, true)
	if err != nil {
		return cerrors.Errorf("could not create db transaction: %w", err)
	}
	r.AppendPure(txn.Discard)

	// check if pipeline already exists
	oldPl, err := s.pipelineService.Get(ctx, id)
	if err != nil && !cerrors.Is(err, pipeline.ErrInstanceNotFound) {
		return cerrors.Errorf("could not get the pipeline %q: %w", id, err)
	} else if err == nil {
		destMap, srcMap, err = s.copyStatesAndDeletePipeline(ctx, oldPl, config)
		if err != nil {
			return cerrors.Errorf("could not copy states from the pipeline %q: %w", id, err)
		}
		// todo: create a pipeline copy in case of error, add to rollback
	}

	// create new pipeline
	newPl, err := s.createPipeline(ctx, id, config)
	if err != nil {
		return cerrors.Errorf("could not create pipeline %q: %w", id, err)
	}
	r.Append(func() error {
		err := s.pipelineService.Delete(ctx, newPl)
		return err
	})
	for k, cfg := range config.Connectors {
		err := s.createConnector(ctx, id, k, cfg, destMap, srcMap)
		if err != nil {
			return cerrors.Errorf("could not create connector %q: %w", k, err)
		}
		s.rollbackCreateConnector(ctx, &r, k)
		_, err = s.pipelineService.AddConnector(ctx, newPl, k)
		if err != nil {
			return cerrors.Errorf("could not add connector %q to the pipeline %q: %w", k, id, err)
		}
		s.rollbackAddConnector(ctx, &r, newPl, k)
		for k1, cfg1 := range cfg.Processors {
			err := s.createProcessor(ctx, id, processor.ParentTypeConnector, k1, cfg1)
			if err != nil {
				return cerrors.Errorf("could not create processor %q: %w", k1, err)
			}
			s.rollbackCreateConnectorProcessor(ctx, &r, k, k1)
			_, err = s.connectorService.AddProcessor(ctx, k, k1)
			if err != nil {
				return cerrors.Errorf("could not add processor %q to the connector %q: %w", k1, k, err)
			}
			s.rollbackAddConnectorProcessor(ctx, &r, k, k1)
		}
	}
	for k, cfg := range config.Processors {
		err := s.createProcessor(ctx, id, processor.ParentTypePipeline, k, cfg)
		if err != nil {
			return cerrors.Errorf("could not create processor %q: %w", k, err)
		}
		s.rollbackCreatePipelineProcessor(ctx, &r, newPl, k)
		_, err = s.pipelineService.AddProcessor(ctx, newPl, k)
		if err != nil {
			return cerrors.Errorf("could not add processor %q to the pipeline %q: %w", k, id, err)
		}
		s.rollbackAddPipelineProcessor(ctx, &r, newPl, k)
	}

	// check if pipeline is running
	if config.Status == StatusRunning {
		err := s.pipelineService.Start(ctx, s.connectorService, s.processorService, newPl)
		if err != nil {
			return cerrors.Errorf("could not start the pipeline %q: %w", err)
		}
	}

	// commit db transaction and skip rollback
	err = txn.Commit()
	if err != nil {
		return cerrors.Errorf("could not commit db transaction: %w", err)
	}

	r.Skip() // skip rollback

	return nil
}

func (s *Service) copyStatesAndDeletePipeline(ctx context.Context, pl *pipeline.Instance, config PipelineConfig) (map[string]connector.DestinationState, map[string]connector.SourceState, error) {
	destMap := make(map[string]connector.DestinationState, 1)
	srcMap := make(map[string]connector.SourceState, 1)

	for k := range config.Connectors {
		destState, srcState, err := s.getConnectorState(ctx, k, config.Connectors[k].Plugin)
		if err != nil {
			return nil, nil, err
		}
		destMap[k] = destState
		srcMap[k] = srcState
	}

	err := s.deletePipeline(ctx, pl, config)
	if err != nil {
		return nil, nil, err
	}

	return destMap, srcMap, nil
}

func (s *Service) deletePipeline(ctx context.Context, pl *pipeline.Instance, config PipelineConfig) error {
	var err error
	// remove pipeline processors
	for k := range config.Processors {
		_, err = s.pipelineService.RemoveProcessor(ctx, pl, k)
		if err != nil {
			return err
		}
	}
	// remove connector processors
	for k, cfg := range config.Connectors {
		for k1 := range cfg.Processors {
			_, err = s.connectorService.RemoveProcessor(ctx, k, k1)
			if err != nil {
				return err
			}
		}
	}
	// remove connectors
	for k := range config.Connectors {
		_, err = s.pipelineService.RemoveConnector(ctx, pl, k)
		if err != nil {
			return err
		}
	}
	err = s.pipelineService.Delete(ctx, pl)
	if err != nil {
		return err
	}

	return nil
}

func (s *Service) createPipeline(ctx context.Context, id string, config PipelineConfig) (*pipeline.Instance, error) {
	cfg := pipeline.Config{
		Name:        config.Name,
		Description: config.Description,
	}

	pl, err := s.pipelineService.Create(ctx, id, cfg, pipeline.ProvisionTypeConfig)
	if err != nil {
		return nil, err
	}
	return pl, nil
}

func (s *Service) createConnector(ctx context.Context, pipelineID string, id string, config ConnectorConfig, destMap map[string]connector.DestinationState, srcMap map[string]connector.SourceState) error {
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

	conn, err := s.connectorService.Create(ctx, id, connType, cfg, connector.ProvisionTypeConfig)
	if err != nil {
		return cerrors.Errorf("could not create connector %q on pipeline %q: %w", id, pipelineID, err)
	}

	// check if states should be updated
	if len(srcMap) != 0 || len(destMap) != 0 {
		switch v := conn.(type) {
		case connector.Destination:
			v.SetState(destMap[id])
		case connector.Source:
			v.SetState(srcMap[id])
		}
	}

	return nil
}

func (s *Service) getConnectorState(ctx context.Context, id string, plugin string) (connector.DestinationState, connector.SourceState, error) {
	var destState connector.DestinationState
	var srcState connector.SourceState
	conn, err := s.connectorService.Get(ctx, id)
	if err != nil {
		return destState, srcState, cerrors.Errorf("could not get connector %q: connector id cannot be changed", id)
	}
	// check if plugin was changed, if it was, then we cannot copy states
	if conn.Config().Plugin != plugin {
		s.logger.Warn(ctx).Msg("a connector plugin was changed, try creating a new connector with that plugin instead")
		return destState, srcState, cerrors.Errorf("connector %q: connectors plugin cannot be changed", id)
	}
	switch v := conn.(type) {
	case connector.Destination:
		destState = v.State()

	case connector.Source:
		srcState = v.State()
	}
	return destState, srcState, nil
}

func (s *Service) createProcessor(ctx context.Context, parentID string, parentType processor.ParentType, id string, config ProcessorConfig) error {
	cfg := processor.Config{
		Settings: config.Settings,
	}
	parent := processor.Parent{
		ID:   parentID,
		Type: parentType,
	}
	// processor name will be renamed to type https://github.com/ConduitIO/conduit/issues/498
	_, err := s.processorService.Create(ctx, id, config.Type, parent, cfg, processor.ProvisionTypeConfig)
	if err != nil {
		return cerrors.Errorf("could not create processor %q on parent %q: %w", id, parentID, err)
	}
	return nil
}

// rollback functions
func (s *Service) rollbackCreateConnector(ctx context.Context, r *rollback.R, connID string) {
	r.Append(func() error {
		err := s.connectorService.Delete(ctx, connID)
		return err
	})
}
func (s *Service) rollbackAddConnector(ctx context.Context, r *rollback.R, pl *pipeline.Instance, connID string) {
	r.Append(func() error {
		_, err := s.pipelineService.RemoveConnector(ctx, pl, connID)
		return err
	})
}
func (s *Service) rollbackCreateConnectorProcessor(ctx context.Context, r *rollback.R, connID string, processorID string) {
	r.Append(func() error {
		_, err := s.connectorService.RemoveProcessor(ctx, connID, processorID)
		return err
	})
}
func (s *Service) rollbackCreatePipelineProcessor(ctx context.Context, r *rollback.R, pl *pipeline.Instance, processorID string) {
	r.Append(func() error {
		_, err := s.pipelineService.RemoveProcessor(ctx, pl, processorID)
		return err
	})
}
func (s *Service) rollbackAddConnectorProcessor(ctx context.Context, r *rollback.R, connID string, processorID string) {
	r.Append(func() error {
		_, err := s.connectorService.RemoveProcessor(ctx, connID, processorID)
		return err
	})
}
func (s *Service) rollbackAddPipelineProcessor(ctx context.Context, r *rollback.R, pl *pipeline.Instance, processorID string) {
	r.Append(func() error {
		_, err := s.pipelineService.RemoveProcessor(ctx, pl, processorID)
		return err
	})
}

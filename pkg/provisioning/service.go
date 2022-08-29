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
	"fmt"
	"os"

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
		logger:           logger.WithComponent("provisioning.Service"),
		pipelineService:  plService,
		connectorService: connService,
		processorService: procService,
	}
}

func (s *Service) ProvisionConfigFile(ctx context.Context, path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, cerrors.Errorf("could not read the file %q: %w", path, err)
	}

	before, err := Parse(data)
	if err != nil {
		return nil, err
	}

	got := EnrichPipelinesConfig(before)

	var pls []string
	var multierr error
	for k, v := range got {
		err = ValidatePipelinesConfig(v)
		if err != nil {
			multierr = multierror.Append(multierr, cerrors.Errorf("invalid pipeline config: %w", err))
			continue
		}
		err = s.provisionPipeline(ctx, k, v)
		if err != nil {
			multierr = multierror.Append(multierr, cerrors.Errorf("error while provisioning the pipeline %q: %w", k, err))
			continue
		}
		pls = append(pls, k)
	}

	return pls, multierr
}

func (s *Service) provisionPipeline(ctx context.Context, id string, config PipelineConfig) error {
	var r rollback.R
	defer r.MustExecute()
	txn, ctx, err := s.db.NewTransaction(ctx, true)
	if err != nil {
		return cerrors.Errorf("could not create db transaction: %w", err)
	}
	r.AppendPure(txn.Discard)

	// check if pipeline already exists
	var connStates map[string]func(context.Context) error
	oldPl, err := s.pipelineService.Get(ctx, id)
	if err != nil && !cerrors.Is(err, pipeline.ErrInstanceNotFound) {
		return cerrors.Errorf("could not get the pipeline %q: %w", id, err)
	} else if err == nil {
		if oldPl.Status == pipeline.StatusRunning {
			err := s.stopPipeline(ctx, oldPl)
			if err != nil {
				return err
			}
		}
		connStates, err = s.collectConnectorStates(ctx, oldPl)
		if err != nil {
			return cerrors.Errorf("could not collect connector states from the pipeline %q: %w", id, err)
		}
		err = s.deletePipeline(ctx, &r, oldPl, config, connStates)
		if err != nil {
			return cerrors.Errorf("could not copy states from the pipeline %q: %w", id, err)
		}
	}

	// create new pipeline
	newPl, err := s.createPipeline(ctx, id, config)
	if err != nil {
		return cerrors.Errorf("could not create pipeline %q: %w", id, err)
	}
	s.rollbackCreatePipeline(ctx, &r, newPl)

	err = s.createConnectors(ctx, &r, newPl, config.Connectors)
	if err != nil {
		return cerrors.Errorf("error while creating connectors: %w", err)
	}

	err = s.createProcessors(ctx, &r, id, processor.ParentTypePipeline, config.Processors)
	if err != nil {
		return cerrors.Errorf("error while creating connectors: %w", err)
	}

	err = s.applyConnectorStates(ctx, newPl, connStates)
	if err != nil {
		return cerrors.Errorf("could not apply connector states on pipeline %q: %w", id, err)
	}

	// check if pipeline is running
	if config.Status == StatusRunning {
		err := s.pipelineService.Start(ctx, s.connectorService, s.processorService, newPl)
		if err != nil {
			return cerrors.Errorf("could not start the pipeline %q: %w", id, err)
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

func (s *Service) collectConnectorStates(ctx context.Context, pl *pipeline.Instance) (map[string]func(context.Context) error, error) {
	connStates := make(map[string]func(context.Context) error)

	for _, k := range pl.ConnectorIDs {
		connID := k // store locally because k will get overwritten
		oldConn, err := s.connectorService.Get(ctx, connID)
		if err != nil {
			return nil, fmt.Errorf("could not get connector %q: %w", connID, err)
		}

		switch oldConn := oldConn.(type) {
		case connector.Destination:
			oldState := oldConn.State()
			connStates[connID] = func(ctx context.Context) error {
				newConn, err := s.connectorService.Get(ctx, connID)
				if err != nil {
					return fmt.Errorf("could not get connector %q: %w", connID, err)
				}
				if newConn.Config().Plugin != oldConn.Config().Plugin {
					s.logger.Warn(ctx).
						Str(log.PluginNameField, newConn.Config().Plugin).
						Msg("plugin name was changes, could not apply old destination state so connector will start from the beginning")
					return nil
				}
				_, err = s.connectorService.SetDestinationState(ctx, connID, oldState)
				if cerrors.Is(err, connector.ErrInvalidConnectorType) {
					s.logger.Warn(ctx).
						Str(log.ConnectorIDField, connID).
						Err(err).
						Msg("could not apply old destination state, the connector will start from the beginning")
					return nil
				}
				return err
			}
		case connector.Source:
			oldState := oldConn.State()
			connStates[connID] = func(ctx context.Context) error {
				newConn, err := s.connectorService.Get(ctx, connID)
				if err != nil {
					return fmt.Errorf("could not get connector %q: %w", connID, err)
				}
				if newConn.Config().Plugin != oldConn.Config().Plugin {
					s.logger.Warn(ctx).
						Str(log.PluginNameField, newConn.Config().Plugin).
						Str(log.ConnectorIDField, connID).
						Msg("plugin name was changes, could not apply old source state so connector will start from the beginning")
					return nil
				}
				_, err = s.connectorService.SetSourceState(ctx, connID, oldState)
				if cerrors.Is(err, connector.ErrInvalidConnectorType) {
					s.logger.Warn(ctx).
						Str(log.ConnectorIDField, connID).
						Err(err).
						Msg("could not apply old source state, the connector will start from the beginning")
					return nil
				}
				return err
			}
		}
	}

	return connStates, nil
}

func (s *Service) applyConnectorStates(ctx context.Context, pl *pipeline.Instance, connectorStates map[string]func(context.Context) error) error {
	if connectorStates == nil {
		return nil
	}

	for _, connID := range pl.ConnectorIDs {
		applyState, ok := connectorStates[connID]
		if !ok {
			// no state to apply
			continue
		}

		err := applyState(ctx)
		if err != nil {
			return cerrors.Errorf("could not apply connector state: %w", err)
		}
	}

	return nil
}

func (s *Service) deletePipeline(ctx context.Context, r *rollback.R, pl *pipeline.Instance, config PipelineConfig, connStates map[string]func(context.Context) error) error {
	var err error
	// remove pipeline processors
	for k, v := range config.Processors {
		_, err = s.pipelineService.RemoveProcessor(ctx, pl, k)
		if err != nil {
			return err
		}
		s.rollbackRemoveProcessorFromPipeline(ctx, r, pl, k)

		err = s.processorService.Delete(ctx, k)
		if err != nil {
			return err
		}
		s.rollbackDeleteProcessor(ctx, r, pl.ID, processor.ParentTypePipeline, k, v)
	}

	r.Append(func() error {
		err := s.applyConnectorStates(ctx, pl, connStates)
		if err != nil {
			return err
		}
		if pl.Status == pipeline.StatusRunning {
			err = s.pipelineService.Start(ctx, s.connectorService, s.processorService, pl)
		}
		return err
	})

	// remove connector processors
	for k, cfg := range config.Connectors {
		for k1, cfg1 := range cfg.Processors {
			_, err = s.connectorService.RemoveProcessor(ctx, k, k1)
			if err != nil {
				return err
			}
			s.rollbackRemoveProcessorFromConnector(ctx, r, k, k1)

			err = s.processorService.Delete(ctx, k1)
			if err != nil {
				return err
			}
			s.rollbackDeleteProcessor(ctx, r, k, processor.ParentTypeConnector, k1, cfg1)
		}
	}

	// remove connectors
	for k, v := range config.Connectors {
		_, err = s.pipelineService.RemoveConnector(ctx, pl, k)
		if err != nil {
			return err
		}
		s.rollbackRemoveConnector(ctx, r, pl, k)

		err = s.connectorService.Delete(ctx, k)
		if err != nil {
			return err
		}
		s.rollbackDeleteConnector(ctx, r, pl.ID, k, v)
	}
	err = s.pipelineService.Delete(ctx, pl)
	if err != nil {
		return err
	}
	s.rollbackDeletePipeline(ctx, r, pl.ID, config)

	return nil
}

func (s *Service) stopPipeline(ctx context.Context, pl *pipeline.Instance) error {
	err := s.pipelineService.Stop(ctx, pl)
	if err != nil {
		return cerrors.Errorf("could not stop pipeline %q: %w", pl.ID, err)
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

func (s *Service) createConnector(ctx context.Context, pipelineID string, id string, config ConnectorConfig) error {
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

	_, err := s.connectorService.Create(ctx, id, connType, cfg, connector.ProvisionTypeConfig)
	if err != nil {
		return cerrors.Errorf("could not create connector %q on pipeline %q: %w", id, pipelineID, err)
	}

	return nil
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

func (s *Service) createConnectors(ctx context.Context, r *rollback.R, pl *pipeline.Instance, mp map[string]ConnectorConfig) error {
	for k, cfg := range mp {
		err := s.createConnector(ctx, pl.ID, k, cfg)

		if err != nil {
			return cerrors.Errorf("could not create connector %q: %w", k, err)
		}
		s.rollbackCreateConnector(ctx, r, k)
		_, err = s.pipelineService.AddConnector(ctx, pl, k)
		if err != nil {
			return cerrors.Errorf("could not add connector %q to the pipeline %q: %w", k, pl.ID, err)
		}
		s.rollbackAddConnector(ctx, r, pl, k)

		err = s.createProcessors(ctx, r, k, processor.ParentTypeConnector, cfg.Processors)
		if err != nil {
			return cerrors.Errorf("could not create processors on connector %q: %w", k, err)
		}
	}
	return nil
}

func (s *Service) createProcessors(ctx context.Context, r *rollback.R, parentID string, parentType processor.ParentType, mp map[string]ProcessorConfig) error {
	for k, cfg := range mp {
		err := s.createProcessor(ctx, parentID, parentType, k, cfg)
		if err != nil {
			return cerrors.Errorf("could not create processor %q: %w", k, err)
		}
		s.rollbackCreateProcessor(ctx, r, k)

		switch parentType {
		case processor.ParentTypePipeline:
			pl, err := s.pipelineService.Get(ctx, parentID)
			if err != nil {
				return cerrors.Errorf("could not get pipeline %q: %w", parentID, err)
			}
			_, err = s.pipelineService.AddProcessor(ctx, pl, k)
			if err != nil {
				return cerrors.Errorf("could not add processor %q to the pipeline %q: %w", k, pl.ID, err)
			}
			s.rollbackAddPipelineProcessor(ctx, r, pl, k)
		case processor.ParentTypeConnector:
			_, err = s.connectorService.AddProcessor(ctx, parentID, k)
			if err != nil {
				return cerrors.Errorf("could not add processor %q to the connector %q: %w", k, parentID, err)
			}
			s.rollbackAddConnectorProcessor(ctx, r, parentID, k)
		}
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
func (s *Service) rollbackDeleteConnector(ctx context.Context, r *rollback.R, pipelineID string, connID string, config ConnectorConfig) {
	r.Append(func() error {
		err := s.createConnector(ctx, pipelineID, connID, config)
		return err
	})
}
func (s *Service) rollbackAddConnector(ctx context.Context, r *rollback.R, pl *pipeline.Instance, connID string) {
	r.Append(func() error {
		_, err := s.pipelineService.RemoveConnector(ctx, pl, connID)
		return err
	})
}
func (s *Service) rollbackRemoveConnector(ctx context.Context, r *rollback.R, pl *pipeline.Instance, connID string) {
	r.Append(func() error {
		_, err := s.pipelineService.AddConnector(ctx, pl, connID)
		return err
	})
}
func (s *Service) rollbackCreateProcessor(ctx context.Context, r *rollback.R, processorID string) {
	r.Append(func() error {
		err := s.processorService.Delete(ctx, processorID)
		return err
	})
}
func (s *Service) rollbackDeleteProcessor(ctx context.Context, r *rollback.R, parentID string, parentType processor.ParentType, processorID string, config ProcessorConfig) {
	r.Append(func() error {
		err := s.createProcessor(ctx, parentID, parentType, processorID, config)
		return err
	})
}
func (s *Service) rollbackAddConnectorProcessor(ctx context.Context, r *rollback.R, connID string, processorID string) {
	r.Append(func() error {
		_, err := s.connectorService.RemoveProcessor(ctx, connID, processorID)
		return err
	})
}
func (s *Service) rollbackRemoveProcessorFromConnector(ctx context.Context, r *rollback.R, connID string, processorID string) {
	r.Append(func() error {
		_, err := s.connectorService.AddProcessor(ctx, connID, processorID)
		return err
	})
}
func (s *Service) rollbackAddPipelineProcessor(ctx context.Context, r *rollback.R, pl *pipeline.Instance, processorID string) {
	r.Append(func() error {
		_, err := s.pipelineService.RemoveProcessor(ctx, pl, processorID)
		return err
	})
}

func (s *Service) rollbackRemoveProcessorFromPipeline(ctx context.Context, r *rollback.R, pl *pipeline.Instance, processorID string) {
	r.Append(func() error {
		_, err := s.pipelineService.AddProcessor(ctx, pl, processorID)
		return err
	})
}

func (s *Service) rollbackCreatePipeline(ctx context.Context, r *rollback.R, pl *pipeline.Instance) {
	r.Append(func() error {
		err := s.pipelineService.Delete(ctx, pl)
		return err
	})
}
func (s *Service) rollbackDeletePipeline(ctx context.Context, r *rollback.R, id string, config PipelineConfig) {
	r.Append(func() error {
		_, err := s.createPipeline(ctx, id, config)
		return err
	})
}

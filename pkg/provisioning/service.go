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
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/foundation/rollback"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"golang.org/x/exp/slices"
)

type Service struct {
	db               database.DB
	logger           log.CtxLogger
	pipelineService  PipelineService
	connectorService ConnectorService
	processorService ProcessorService
	pipelinesPath    string
}

func NewService(
	db database.DB,
	logger log.CtxLogger,
	plService PipelineService,
	connService ConnectorService,
	procService ProcessorService,
	pipelinesDir string,
) *Service {
	return &Service{
		db:               db,
		logger:           logger.WithComponent("provisioning.Service"),
		pipelineService:  plService,
		connectorService: connService,
		processorService: procService,
		pipelinesPath:    pipelinesDir,
	}
}

func (s *Service) Init(ctx context.Context) error {
	s.logger.Debug(ctx).
		Str("pipelines_path", s.pipelinesPath).
		Msg("initializing the provisioning service")

	var files []string
	err := filepath.WalkDir(s.pipelinesPath, func(path string, fileInfo fs.DirEntry, err error) error {
		if strings.HasSuffix(path, ".yml") || strings.HasSuffix(path, ".yaml") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return cerrors.Errorf("could not iterate through the pipelines folder %q: %w", s.pipelinesPath, err)
	}

	// contains all the errors occurred while provisioning configuration files.
	var multierr error
	// contains pipelineIDs of successfully provisioned pipelines.
	var successPls []string
	// contains pipelineIDs of all the pipelines in all the configuration files, either successfully provisioned or not.
	var allPls []string
	for _, file := range files {
		provPipelines, all, err := s.provisionConfigFile(ctx, file, successPls)
		if err != nil {
			multierr = multierror.Append(multierr, err)
		}
		allPls = append(allPls, all...)
		if len(provPipelines) != 0 {
			successPls = append(successPls, provPipelines...)
		}
	}

	// pipelines that were provisioned by config but are not in the config files anymore
	deletedPls := s.deleteOldPipelines(ctx, allPls)
	s.logger.Info(ctx).
		Strs("created", successPls).
		Strs("deleted", deletedPls).
		Str("pipelines_path", s.pipelinesPath).
		Msg("pipeline configs provisioned")

	return multierr
}

// provisionConfigFile returns a list of all successfully provisioned pipelines, a list of all pipeline IDs from the
// file, and an error if any failure happened while provisioning the file.
func (s *Service) provisionConfigFile(ctx context.Context, path string, alreadyProvisioned []string) ([]string, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, cerrors.Errorf("could not read the file %q: %w", path, err)
	}

	before, err := Parse(data)
	if err != nil {
		return nil, nil, cerrors.Errorf("could not parse the file %q: %w", path, err)
	}

	got := EnrichPipelinesConfig(before)

	// contains pipelineIDs of successfully provisioned pipelines for this file.
	var successPls []string
	// contains pipelineIDs of all the pipelines in this configuration files, either successfully provisioned or not.
	var allPls []string
	var multierr error
	for k, v := range got {
		allPls = append(allPls, k)
		err = ValidatePipelinesConfig(v)
		if err != nil {
			multierr = multierror.Append(multierr, cerrors.Errorf("pipeline %q, invalid pipeline config: %w", k, err))
			continue
		}
		err = s.provisionPipeline(ctx, k, v, alreadyProvisioned)
		if err != nil {
			multierr = multierror.Append(multierr, cerrors.Errorf("pipeline %q, error while provisioning: %w", k, err))
			continue
		}
		successPls = append(successPls, k)
	}

	return successPls, allPls, multierr
}

func (s *Service) provisionPipeline(ctx context.Context, id string, config PipelineConfig, alreadyProvisioned []string) error {
	var r rollback.R
	defer r.MustExecute()
	txn, ctx, err := s.db.NewTransaction(ctx, true)
	if err != nil {
		return cerrors.Errorf("could not create db transaction: %w", err)
	}
	r.AppendPure(txn.Discard)

	// check if pipeline was already provisioned
	if slices.Contains(alreadyProvisioned, id) {
		return cerrors.Errorf("duplicated pipeline id %q, pipeline will be skipped: %s", id, config.Name)
	}

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
			s.rollbackStopPipeline(ctx, &r, oldPl.ID)
		}
		connStates, err = s.collectConnectorStates(ctx, oldPl)
		if err != nil {
			return cerrors.Errorf("could not collect connector states from the pipeline %q: %w", id, err)
		}
		err = s.deletePipeline(ctx, &r, oldPl, config, connStates)
		if err != nil {
			return cerrors.Errorf("could not delete pipeline %q: %w", id, err)
		}
	}

	// create new pipeline
	newPl, err := s.createPipeline(ctx, id, config)
	if err != nil {
		return cerrors.Errorf("could not create pipeline %q: %w", id, err)
	}
	s.rollbackCreatePipeline(ctx, &r, newPl.ID)

	err = s.createConnectors(ctx, &r, newPl.ID, config.Connectors)
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
		err := s.pipelineService.Start(ctx, s.connectorService, s.processorService, newPl.ID)
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
						Msg("plugin name changed, could not apply old destination state, the connector will start from the beginning")
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
						Msg("plugin name changed, could not apply old source state, the connector will start from the beginning")
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
	// remove and delete processors
	for _, id := range pl.ProcessorIDs {
		proc, err := s.processorService.Get(ctx, id)
		if err != nil {
			return err
		}

		if proc.Parent.Type == processor.ParentTypePipeline {
			_, err = s.pipelineService.RemoveProcessor(ctx, pl.ID, id)
			if err != nil {
				return err
			}
			s.rollbackRemoveProcessorFromPipeline(ctx, r, pl.ID, id)
		} else if proc.Parent.Type == processor.ParentTypeConnector {
			_, err = s.connectorService.RemoveProcessor(ctx, proc.Parent.ID, id)
			if err != nil {
				return err
			}
			s.rollbackRemoveProcessorFromConnector(ctx, r, proc.Parent.ID, id)
		}

		err = s.processorService.Delete(ctx, id)
		if err != nil {
			return err
		}
		s.rollbackDeleteProcessor(ctx, r, proc.Parent, proc)
	}

	r.Append(func() error {
		err := s.applyConnectorStates(ctx, pl, connStates)
		return err
	})

	// remove and delete connectors
	for _, id := range pl.ConnectorIDs {
		conn, err := s.connectorService.Get(ctx, id)
		if err != nil {
			return err
		}
		_, err = s.pipelineService.RemoveConnector(ctx, pl.ID, id)
		if err != nil {
			return err
		}
		s.rollbackRemoveConnector(ctx, r, pl.ID, id)

		err = s.connectorService.Delete(ctx, id)
		if err != nil {
			return err
		}
		s.rollbackDeleteConnector(ctx, r, pl.ID, id, conn)
	}

	// delete pipeline
	err := s.pipelineService.Delete(ctx, pl.ID)
	if err != nil {
		return err
	}
	s.rollbackDeletePipeline(ctx, r, pl.ID, config)

	return nil
}

func (s *Service) stopPipeline(ctx context.Context, pl *pipeline.Instance) error {
	err := s.pipelineService.Stop(ctx, pl.ID)
	if err != nil {
		return cerrors.Errorf("could not stop pipeline %q: %w", pl.ID, err)
	}
	pl.Wait() //nolint:errcheck // error will be logged and stored in the pipeline
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

func (s *Service) createConnectors(ctx context.Context, r *rollback.R, pipelineID string, mp map[string]ConnectorConfig) error {
	for k, cfg := range mp {
		err := s.createConnector(ctx, pipelineID, k, cfg)
		if err != nil {
			return cerrors.Errorf("could not create connector %q: %w", k, err)
		}
		s.rollbackCreateConnector(ctx, r, k)

		_, err = s.pipelineService.AddConnector(ctx, pipelineID, k)
		if err != nil {
			return cerrors.Errorf("could not add connector %q to the pipeline %q: %w", k, pipelineID, err)
		}
		s.rollbackAddConnector(ctx, r, pipelineID, k)

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
			_, err = s.pipelineService.AddProcessor(ctx, pl.ID, k)
			if err != nil {
				return cerrors.Errorf("could not add processor %q to the pipeline %q: %w", k, pl.ID, err)
			}
			s.rollbackAddPipelineProcessor(ctx, r, pl.ID, k)
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

func (s *Service) deleteOldPipelines(ctx context.Context, pipelines []string) []string {
	var pls []string
	plMp := s.pipelineService.List(ctx)
	for id, pl := range plMp {
		if !slices.Contains(pipelines, id) && pl.ProvisionedBy == pipeline.ProvisionTypeConfig {
			err := s.deletePipelineWithoutRollback(ctx, pl)
			if err != nil {
				s.logger.Warn(ctx).
					Str(log.PipelineIDField, id).
					Msg("failed to delete pipeline provisioned by a config file, the pipeline is probably in a broken state, Conduit will try to remove it again next time it runs")
			} else {
				pls = append(pls, id)
			}
		}
	}
	return pls
}

func (s *Service) deletePipelineWithoutRollback(ctx context.Context, pl *pipeline.Instance) error {
	var err error

	if pl.Status == pipeline.StatusRunning {
		err := s.stopPipeline(ctx, pl)
		if err != nil {
			return err
		}
	}

	// remove pipeline processors
	for _, procID := range pl.ProcessorIDs {
		_, err = s.pipelineService.RemoveProcessor(ctx, pl.ID, procID)
		if err != nil {
			return err
		}

		err = s.processorService.Delete(ctx, procID)
		if err != nil {
			return err
		}
	}
	// remove connector processors
	for _, connID := range pl.ConnectorIDs {
		for _, procID := range pl.ProcessorIDs {
			_, err = s.connectorService.RemoveProcessor(ctx, connID, procID)
			if err != nil {
				return err
			}

			err = s.processorService.Delete(ctx, procID)
			if err != nil {
				return err
			}
		}
	}
	// remove connectors
	for _, connID := range pl.ConnectorIDs {
		_, err = s.pipelineService.RemoveConnector(ctx, pl.ID, connID)
		if err != nil {
			return err
		}

		err = s.connectorService.Delete(ctx, connID)
		if err != nil {
			return err
		}
	}
	// delete pipeline
	err = s.pipelineService.Delete(ctx, pl.ID)
	if err != nil {
		return err
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
func (s *Service) rollbackDeleteConnector(ctx context.Context, r *rollback.R, pipelineID string, connID string, conn connector.Connector) {
	r.Append(func() error {
		config := ConnectorConfig{
			Name:     conn.Config().Name,
			Plugin:   conn.Config().Plugin,
			Settings: conn.Config().Settings,
			Type:     strings.ToLower(conn.Type().String()),
		}
		err := s.createConnector(ctx, pipelineID, connID, config)
		return err
	})
}
func (s *Service) rollbackAddConnector(ctx context.Context, r *rollback.R, pipelineID string, connID string) {
	r.Append(func() error {
		_, err := s.pipelineService.RemoveConnector(ctx, pipelineID, connID)
		return err
	})
}
func (s *Service) rollbackRemoveConnector(ctx context.Context, r *rollback.R, pipelineID string, connID string) {
	r.Append(func() error {
		_, err := s.pipelineService.AddConnector(ctx, pipelineID, connID)
		return err
	})
}
func (s *Service) rollbackCreateProcessor(ctx context.Context, r *rollback.R, processorID string) {
	r.Append(func() error {
		err := s.processorService.Delete(ctx, processorID)
		return err
	})
}
func (s *Service) rollbackDeleteProcessor(ctx context.Context, r *rollback.R, parent processor.Parent, proc *processor.Instance) {
	r.Append(func() error {
		config := ProcessorConfig{
			Type:     proc.Name,
			Settings: proc.Config.Settings,
		}
		err := s.createProcessor(ctx, parent.ID, parent.Type, proc.ID, config)
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
func (s *Service) rollbackAddPipelineProcessor(ctx context.Context, r *rollback.R, pipelineID string, processorID string) {
	r.Append(func() error {
		_, err := s.pipelineService.RemoveProcessor(ctx, pipelineID, processorID)
		return err
	})
}

func (s *Service) rollbackRemoveProcessorFromPipeline(ctx context.Context, r *rollback.R, pipelineID string, processorID string) {
	r.Append(func() error {
		_, err := s.pipelineService.AddProcessor(ctx, pipelineID, processorID)
		return err
	})
}

func (s *Service) rollbackCreatePipeline(ctx context.Context, r *rollback.R, pipelineID string) {
	r.Append(func() error {
		err := s.pipelineService.Delete(ctx, pipelineID)
		return err
	})
}
func (s *Service) rollbackDeletePipeline(ctx context.Context, r *rollback.R, id string, config PipelineConfig) {
	r.Append(func() error {
		_, err := s.createPipeline(ctx, id, config)
		return err
	})
}
func (s *Service) rollbackStopPipeline(ctx context.Context, r *rollback.R, pipelineID string) {
	r.Append(func() error {
		err := s.pipelineService.Start(ctx, s.connectorService, s.processorService, pipelineID)
		return err
	})
}

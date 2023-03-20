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
	"sort"
	"strings"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/foundation/rollback"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/conduit/pkg/provisioning/config/yaml"
	"golang.org/x/exp/slices"
)

type Service struct {
	db               database.DB
	logger           log.CtxLogger
	parser           config.Parser
	pipelineService  PipelineService
	connectorService ConnectorService
	processorService ProcessorService
	pluginService    PluginService
	pipelinesPath    string
}

func NewService(
	db database.DB,
	logger log.CtxLogger,
	plService PipelineService,
	connService ConnectorService,
	procService ProcessorService,
	pluginService PluginService,
	pipelinesDir string,
) *Service {
	return &Service{
		db:               db,
		logger:           logger.WithComponent("provisioning.Service"),
		parser:           yaml.NewParser(logger),
		pipelineService:  plService,
		connectorService: connService,
		processorService: procService,
		pluginService:    pluginService,
		pipelinesPath:    pipelinesDir,
	}
}

// Init provision pipelines defined in pipelinePath directory. should initialize pipeline service
// before calling this function, and all pipelines should be stopped.
func (s *Service) Init(ctx context.Context) error {
	s.logger.Debug(ctx).
		Str("pipelines_path", s.pipelinesPath).
		Msg("initializing the provisioning service")

	files, err := s.getYamlFiles(s.pipelinesPath)
	if err != nil {
		return cerrors.Errorf("failed to read pipelines folder %q: %w", s.pipelinesPath, err)
	}

	// contains all the errors occurred while provisioning configuration files.
	var multierr error

	// parse pipeline config files
	configs := make([]config.Pipeline, 0, len(files))
	for _, file := range files {
		cfg, err := s.parsePipelineConfigFile(ctx, file)
		if err != nil {
			return multierror.Append(multierr, err)
		}
		configs = append(configs, cfg...)
	}

	// contains pipelineIDs of all the pipelines in all the configuration files, either successfully provisioned or not.
	var allPls []string

	// delete duplicate pipelines (if any)
	for duplicateID, duplicateIndexes := range s.findDuplicateIDs(configs) {
		multierr = cerrors.Errorf("%d pipelines with ID %q will be skipped: %w", len(duplicateIndexes), duplicateID, ErrDuplicatedPipelineID)
		configs = s.deleteIndexes(configs, duplicateIndexes)

		// duplicated IDs should still count towards all encountered pipeline IDs
		allPls = append(allPls, duplicateID)
	}

	// contains pipelineIDs of successfully provisioned pipelines.
	var successPls []string
	for _, cfg := range configs {
		allPls = append(allPls, cfg.ID)

		err = s.provisionPipeline(ctx, cfg)
		if err != nil {
			multierr = multierror.Append(multierr, cerrors.Errorf("pipeline %q, error while provisioning: %w", cfg.ID, err))
			continue
		}
		successPls = append(successPls, cfg.ID)
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

// getYamlFiles recursively reads folders in the path and collects paths to all
// files that end with .yml or .yaml.
func (s *Service) getYamlFiles(path string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(path, func(path string, fileInfo fs.DirEntry, err error) error {
		if strings.HasSuffix(path, ".yml") || strings.HasSuffix(path, ".yaml") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (s *Service) parsePipelineConfigFile(ctx context.Context, path string) ([]config.Pipeline, error) {
	ctx = ctxutil.ContextWithFilepath(ctx, path) // attach path to context for logs

	file, err := os.Open(path)
	if err != nil {
		return nil, cerrors.Errorf("could not open file %q: %w", path, err)
	}
	defer file.Close()

	configs, err := s.parser.Parse(ctx, file)
	if err != nil {
		return nil, cerrors.Errorf("could not parse file %q: %w", path, err)
	}
	return configs, nil
}

func (s *Service) findDuplicateIDs(configs []config.Pipeline) map[string][]int {
	// build a map that contains a list of all indexes for each pipeline ID
	pipelineIndexesByID := make(map[string][]int)
	for i, cfg := range configs {
		indexes := pipelineIndexesByID[cfg.ID]
		pipelineIndexesByID[cfg.ID] = append(indexes, i)
	}
	// remove entries that have only one index
	for id, indexes := range pipelineIndexesByID {
		if len(indexes) == 1 {
			delete(pipelineIndexesByID, id)
		}
	}
	return pipelineIndexesByID
}

func (s *Service) deleteIndexes(configs []config.Pipeline, indexes []int) []config.Pipeline {
	// sort indexes in reverse so we can safely remove them
	sort.Sort(sort.Reverse(sort.IntSlice(indexes)))
	for _, i := range indexes {
		// slice trick: delete element at i and preserve order
		configs = configs[:i+copy(configs[i:], configs[i+1:])]
	}
	return configs
}

// provisionPipeline provisions a single pipeline and returns an error if
// any failure happened during provisioning.
func (s *Service) provisionPipeline(ctx context.Context, cfg config.Pipeline) error {
	var r rollback.R
	defer r.MustExecute()
	txn, ctx, err := s.db.NewTransaction(ctx, true)
	if err != nil {
		return cerrors.Errorf("could not create db transaction: %w", err)
	}
	r.AppendPure(txn.Discard)

	// enrich and validate config
	cfg = config.Enrich(cfg)
	err = config.Validate(cfg)
	if err != nil {
		return cerrors.Errorf("invalid pipeline config: %w", err)
	}

	// check if pipeline already exists
	var connStates map[string]func(context.Context) error
	oldPl, err := s.pipelineService.Get(ctx, cfg.ID)
	if err != nil && !cerrors.Is(err, pipeline.ErrInstanceNotFound) {
		return cerrors.Errorf("could not get the pipeline %q: %w", cfg.ID, err)
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
			return cerrors.Errorf("could not collect connector states from the pipeline %q: %w", cfg.ID, err)
		}
		err = s.deletePipeline(ctx, &r, oldPl, connStates)
		if err != nil {
			return cerrors.Errorf("could not delete pipeline %q: %w", cfg.ID, err)
		}
	}

	// create new pipeline
	newPl, err := s.createPipeline(ctx, cfg)
	if err != nil {
		return cerrors.Errorf("could not create pipeline %q: %w", cfg.ID, err)
	}
	s.rollbackCreatePipeline(ctx, &r, newPl.ID)

	err = s.createConnectors(ctx, &r, newPl.ID, cfg.Connectors)
	if err != nil {
		return cerrors.Errorf("error while creating connectors: %w", err)
	}

	err = s.createProcessors(ctx, &r, cfg.ID, processor.ParentTypePipeline, cfg.Processors)
	if err != nil {
		return cerrors.Errorf("error while creating connectors: %w", err)
	}

	err = s.applyConnectorStates(ctx, newPl, connStates)
	if err != nil {
		return cerrors.Errorf("could not apply connector states on pipeline %q: %w", cfg.ID, err)
	}

	// check if pipeline is running
	if cfg.Status == config.StatusRunning {
		err := s.pipelineService.Start(ctx, s.connectorService, s.processorService, s.pluginService, newPl.ID)
		if err != nil {
			return cerrors.Errorf("could not start the pipeline %q: %w", cfg.ID, err)
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

		oldState := oldConn.State
		if oldState == nil {
			continue // no need to copy state
		}
		// store function which copies the state for the connector
		connStates[connID] = func(ctx context.Context) error {
			newConn, err := s.connectorService.Get(ctx, connID)
			if err != nil {
				return fmt.Errorf("could not get connector %q: %w", connID, err)
			}
			if newConn.Plugin != oldConn.Plugin {
				s.logger.Warn(ctx).
					Str(log.PluginNameField, newConn.Plugin).
					Msg("plugin name changed, could not apply old destination state, the connector will start from the beginning")
				return nil
			}
			_, err = s.connectorService.SetState(ctx, connID, oldState)
			if cerrors.Is(err, connector.ErrInvalidConnectorStateType) {
				s.logger.Warn(ctx).
					Str(log.ConnectorIDField, connID).
					Err(err).
					Msg("could not apply old state, the connector will start from the beginning")
				return nil
			}
			return err
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

func (s *Service) deletePipeline(ctx context.Context, r *rollback.R, pl *pipeline.Instance, connStates map[string]func(context.Context) error) error {
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
	s.rollbackDeletePipeline(ctx, r, pl)

	return nil
}

func (s *Service) stopPipeline(ctx context.Context, pl *pipeline.Instance) error {
	err := s.pipelineService.Stop(ctx, pl.ID, false)
	if err != nil {
		return cerrors.Errorf("could not stop pipeline %q: %w", pl.ID, err)
	}
	pl.Wait() //nolint:errcheck // error will be logged and stored in the pipeline
	return nil
}

func (s *Service) createPipeline(ctx context.Context, cfg config.Pipeline) (*pipeline.Instance, error) {
	pl, err := s.pipelineService.Create(
		ctx,
		cfg.ID,
		pipeline.Config{
			Name:        cfg.Name,
			Description: cfg.Description,
		},
		pipeline.ProvisionTypeConfig,
	)
	if err != nil {
		return nil, err
	}

	dlq := pl.DLQ
	var updateDLQ bool
	if cfg.DLQ.Plugin != "" {
		dlq.Plugin = cfg.DLQ.Plugin
		updateDLQ = true
	}
	if cfg.DLQ.Settings != nil {
		dlq.Settings = cfg.DLQ.Settings
		updateDLQ = true
	}
	if cfg.DLQ.WindowSize != nil {
		dlq.WindowSize = *cfg.DLQ.WindowSize
		updateDLQ = true
	}
	if cfg.DLQ.WindowNackThreshold != nil {
		dlq.WindowNackThreshold = *cfg.DLQ.WindowNackThreshold
		updateDLQ = true
	}

	if updateDLQ {
		pl, err = s.pipelineService.UpdateDLQ(ctx, cfg.ID, dlq)
		if err != nil {
			return nil, err
		}
	}

	return pl, nil
}

func (s *Service) createConnector(ctx context.Context, pipelineID string, cfg config.Connector) error {
	connType := connector.TypeSource
	if cfg.Type == config.TypeDestination {
		connType = connector.TypeDestination
	}

	_, err := s.connectorService.Create(
		ctx,
		cfg.ID,
		connType,
		cfg.Plugin,
		pipelineID,
		connector.Config{
			Name:     cfg.Name,
			Settings: cfg.Settings,
		},
		connector.ProvisionTypeConfig,
	)
	if err != nil {
		return cerrors.Errorf("could not create connector %q on pipeline %q: %w", cfg.ID, pipelineID, err)
	}

	return nil
}

func (s *Service) createProcessor(ctx context.Context, parentID string, parentType processor.ParentType, cfg config.Processor) error {
	_, err := s.processorService.Create(
		ctx,
		cfg.ID,
		cfg.Type,
		processor.Parent{
			ID:   parentID,
			Type: parentType,
		},
		processor.Config{
			Settings: cfg.Settings,
			Workers:  cfg.Workers,
		},
		processor.ProvisionTypeConfig,
	)
	if err != nil {
		return cerrors.Errorf("could not create processor %q on parent %q: %w", cfg.ID, parentID, err)
	}
	return nil
}

func (s *Service) createConnectors(ctx context.Context, r *rollback.R, pipelineID string, list []config.Connector) error {
	for _, cfg := range list {
		err := s.createConnector(ctx, pipelineID, cfg)
		if err != nil {
			return cerrors.Errorf("could not create connector %q: %w", cfg.ID, err)
		}
		s.rollbackCreateConnector(ctx, r, cfg.ID)

		_, err = s.pipelineService.AddConnector(ctx, pipelineID, cfg.ID)
		if err != nil {
			return cerrors.Errorf("could not add connector %q to the pipeline %q: %w", cfg.ID, pipelineID, err)
		}
		s.rollbackAddConnector(ctx, r, pipelineID, cfg.ID)

		err = s.createProcessors(ctx, r, cfg.ID, processor.ParentTypeConnector, cfg.Processors)
		if err != nil {
			return cerrors.Errorf("could not create processors on connector %q: %w", cfg.ID, err)
		}
	}
	return nil
}

func (s *Service) createProcessors(ctx context.Context, r *rollback.R, parentID string, parentType processor.ParentType, list []config.Processor) error {
	for _, cfg := range list {
		err := s.createProcessor(ctx, parentID, parentType, cfg)
		if err != nil {
			return cerrors.Errorf("could not create processor %q: %w", cfg.ID, err)
		}
		s.rollbackCreateProcessor(ctx, r, cfg.ID)

		switch parentType {
		case processor.ParentTypePipeline:
			pl, err := s.pipelineService.Get(ctx, parentID)
			if err != nil {
				return cerrors.Errorf("could not get pipeline %q: %w", parentID, err)
			}
			_, err = s.pipelineService.AddProcessor(ctx, pl.ID, cfg.ID)
			if err != nil {
				return cerrors.Errorf("could not add processor %q to the pipeline %q: %w", cfg.ID, pl.ID, err)
			}
			s.rollbackAddPipelineProcessor(ctx, r, pl.ID, cfg.ID)
		case processor.ParentTypeConnector:
			_, err = s.connectorService.AddProcessor(ctx, parentID, cfg.ID)
			if err != nil {
				return cerrors.Errorf("could not add processor %q to the connector %q: %w", cfg.ID, parentID, err)
			}
			s.rollbackAddConnectorProcessor(ctx, r, parentID, cfg.ID)
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
func (s *Service) rollbackDeleteConnector(ctx context.Context, r *rollback.R, pipelineID string, connID string, conn *connector.Instance) {
	r.Append(func() error {
		cfg := config.Connector{
			ID:       connID,
			Name:     conn.Config.Name,
			Plugin:   conn.Plugin,
			Settings: conn.Config.Settings,
			Type:     strings.ToLower(conn.Type.String()),
		}
		return s.createConnector(ctx, pipelineID, cfg)
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
		cfg := config.Processor{
			ID:       proc.ID,
			Type:     proc.Type,
			Settings: proc.Config.Settings,
		}
		return s.createProcessor(ctx, parent.ID, parent.Type, cfg)
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
func (s *Service) rollbackDeletePipeline(ctx context.Context, r *rollback.R, p *pipeline.Instance) {
	r.Append(func() error {
		cfg := config.Pipeline{
			ID:          p.ID,
			Status:      p.Status.String(),
			Name:        p.Config.Name,
			Description: p.Config.Description,
			DLQ: config.DLQ{
				Plugin:              p.DLQ.Plugin,
				Settings:            p.DLQ.Settings,
				WindowSize:          &p.DLQ.WindowSize,
				WindowNackThreshold: &p.DLQ.WindowNackThreshold,
			},
		}
		_, err := s.createPipeline(ctx, cfg)
		return err
	})
}
func (s *Service) rollbackStopPipeline(ctx context.Context, r *rollback.R, pipelineID string) {
	r.Append(func() error {
		err := s.pipelineService.Start(ctx, s.connectorService, s.processorService, s.pluginService, pipelineID)
		return err
	})
}

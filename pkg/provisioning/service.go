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
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/pipeline"
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
	// enrich and validate config
	cfg = config.Enrich(cfg)
	err := config.Validate(cfg)
	if err != nil {
		return cerrors.Errorf("invalid pipeline config: %w", err)
	}

	txn, ctx, err := s.db.NewTransaction(ctx, true)
	if err != nil {
		return cerrors.Errorf("could not create db transaction: %w", err)
	}
	defer txn.Discard()

	err = s.Import(ctx, cfg)
	if err != nil {
		return cerrors.Errorf("could not import pipeline: %w", err)
	}

	// check if pipeline should be running
	if cfg.Status == config.StatusRunning {
		// TODO set status and let the pipeline service start it
		err := s.pipelineService.Start(ctx, s.connectorService, s.processorService, s.pluginService, cfg.ID)
		if err != nil {
			return cerrors.Errorf("could not start the pipeline %q: %w", cfg.ID, err)
		}
	}

	// commit db transaction
	err = txn.Commit()
	if err != nil {
		return cerrors.Errorf("could not commit db transaction: %w", err)
	}

	return nil
}

func (s *Service) deleteOldPipelines(ctx context.Context, ids []string) []string {
	var deletedIDs []string
	pipelines := s.pipelineService.List(ctx)
	for id, pl := range pipelines {
		if !slices.Contains(ids, id) && pl.ProvisionedBy == pipeline.ProvisionTypeConfig {
			oldConfig, err := s.Export(ctx, id)
			if err != nil {
				s.logger.Warn(ctx).
					Err(err).
					Str(log.PipelineIDField, id).
					Msg("failed to delete a pipeline provisioned by a config file, the pipeline is probably in a broken state, Conduit will try to remove it again next time it runs")
				continue
			}
			actions := s.newActionsBuilder().Build(oldConfig, config.Pipeline{})
			_, err = s.executeActions(ctx, actions)
			if err != nil {
				s.logger.Warn(ctx).
					Err(err).
					Str(log.PipelineIDField, id).
					Msg("failed to delete a pipeline provisioned by a config file, the pipeline is probably in a broken state, Conduit will try to remove it again next time it runs")
				continue
			}

			deletedIDs = append(deletedIDs, id)
		}
	}
	return deletedIDs
}

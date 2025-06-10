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
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/conduit/pkg/provisioning/config/yaml"
)

type Service struct {
	db                     database.DB
	logger                 log.CtxLogger
	parser                 config.Parser
	pipelineService        PipelineService
	connectorService       ConnectorService
	processorService       ProcessorService
	connectorPluginService ConnectorPluginService
	lifecycleService       LifecycleService
	pipelinesPath          string
}

func NewService(
	db database.DB,
	logger log.CtxLogger,
	plService PipelineService,
	connService ConnectorService,
	procService ProcessorService,
	connPluginService ConnectorPluginService,
	lifecycleService LifecycleService,
	pipelinesDir string,
) *Service {
	return &Service{
		db:                     db,
		logger:                 logger.WithComponent("provisioning.Service"),
		parser:                 yaml.NewParser(logger),
		pipelineService:        plService,
		connectorService:       connService,
		processorService:       procService,
		connectorPluginService: connPluginService,
		lifecycleService:       lifecycleService,
		pipelinesPath:          pipelinesDir,
	}
}

// Init provision pipelines defined in pipelinePath directory. should initialize pipeline service
// before calling this function, and all pipelines should be stopped.
func (s *Service) Init(ctx context.Context) error {
	s.logger.Debug(ctx).
		Str("pipelines_path", s.pipelinesPath).
		Msg("initializing the provisioning service")

	files, err := s.getPipelineConfigFiles(ctx, s.pipelinesPath)
	if err != nil {
		return cerrors.Errorf("failed to read pipelines folder %q: %w", s.pipelinesPath, err)
	}

	// contains all the errors occurred while provisioning configuration files.
	var errs []error

	// parse pipeline config files
	configs := make([]config.Pipeline, 0, len(files))
	for _, file := range files {
		cfg, err := s.parsePipelineConfigFile(ctx, file)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		configs = append(configs, cfg...)
	}

	// contains pipelineIDs of all the pipelines in all the configuration files, either successfully provisioned or not.
	var allPls []string

	// delete duplicate pipelines (if any)
	for duplicateID, duplicateIndexes := range s.findDuplicateIDs(configs) {
		errs = append(errs, cerrors.Errorf("%d pipelines with ID %q will be skipped: %w", len(duplicateIndexes), duplicateID, ErrDuplicatedPipelineID))
		configs = s.deleteIndexes(configs, duplicateIndexes)

		// duplicated IDs should still count towards all encountered pipeline IDs
		allPls = append(allPls, duplicateID)
	}

	// remove pipelines with duplicate IDs from API pipelines
	var apiProvisioned []int
	for i, pl := range configs {
		pipelineInstance, err := s.pipelineService.Get(ctx, pl.ID)
		if err != nil {
			if !cerrors.Is(err, pipeline.ErrInstanceNotFound) {
				errs = append(errs, cerrors.Errorf("error getting pipeline instance with ID %q: %w", pl.ID, err))
			}
			continue
		}
		if pipelineInstance.ProvisionedBy != pipeline.ProvisionTypeConfig {
			errs = append(errs, cerrors.Errorf("pipelines with ID %q will be skipped: %w", pl.ID, ErrNotProvisionedByConfig))
			apiProvisioned = append(apiProvisioned, i)
		}
	}
	configs = s.deleteIndexes(configs, apiProvisioned)

	// contains pipelineIDs of successfully provisioned pipelines.
	var successPls []string
	for _, cfg := range configs {
		allPls = append(allPls, cfg.ID)

		err = s.provisionPipeline(ctx, cfg)
		if err != nil {
			errs = append(errs, cerrors.Errorf("pipeline %q, error while provisioning: %w", cfg.ID, err))
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

	return cerrors.Join(errs...)
}

// Delete exposes a way to delete pipelines provisioned using the provisioning
// service.
func (s *Service) Delete(ctx context.Context, id string) error {
	pl, err := s.pipelineService.Get(ctx, id)
	if err != nil {
		return cerrors.Errorf("could not get pipeline %q: %w", id, err)
	}
	if pl.ProvisionedBy != pipeline.ProvisionTypeConfig {
		return ErrNotProvisionedByConfig
	}
	oldConfig, err := s.Export(ctx, id)
	if err != nil {
		return cerrors.Errorf("failed to export pipeline: %w", err)
	}
	actions := s.newActionsBuilder().Build(oldConfig, config.Pipeline{})
	_, err = s.executeActions(ctx, actions)
	if err != nil {
		return cerrors.Errorf("failed to delete pipeline: %w", err)
	}
	return nil
}

// getPipelineConfigFiles collects individual configuration files from the given path.
// If the given path is NOT a directory, then the path is used as is.
// If the given path is a directory, then this method returns all entries in the directory
// that are not directories themselves, and that have their names end in yml or yaml.
func (s *Service) getPipelineConfigFiles(ctx context.Context, path string) ([]string, error) {
	if path == "" {
		return nil, cerrors.Errorf("failed to read pipelines folder %q: %w", path, cerrors.New("pipeline path cannot be empty"))
	}

	s.logger.Debug(ctx).
		Str("pipelines_path", path).
		Msg("loading pipeline configuration files")

	info, err := os.Stat(path)
	if err != nil {
		return nil, cerrors.Errorf("failed to stat pipelines path %q: %w", path, err)
	}
	if !info.IsDir() {
		return []string{path}, nil
	}

	return s.getYamlFilesFromDir(ctx, path)
}

func (s *Service) getYamlFilesFromDir(ctx context.Context, path string) ([]string, error) {
	dirEntries, err := os.ReadDir(path)
	if err != nil {
		s.logger.Warn(ctx).
			Err(err).
			Msg("could not read pipelines directory")
		return nil, err
	}

	var files []string
	for _, dirEntry := range dirEntries {
		filePath := filepath.Join(path, dirEntry.Name())
		if s.isYamlFile(filePath) {
			files = append(files, filePath)
		}
	}

	return files, nil
}

func (s *Service) resolveSymLink(basePath, symlink string) (string, error) {
	fullPath := filepath.Join(basePath, symlink)
	resolvedPath, err := os.Readlink(fullPath)
	if err != nil {
		return "", cerrors.Errorf("could not read symlink: %w", err)
	}

	// If symlink path is relative, make it absolute
	if !filepath.IsAbs(resolvedPath) {
		resolvedPath = filepath.Join(basePath, resolvedPath)
	}

	// Check if the resolved path exists and is a regular file
	_, err = os.Stat(resolvedPath)
	if err != nil {
		return "", cerrors.Errorf("could not stat symlink: %w", err)
	}

	return resolvedPath, nil
}

func (s *Service) isYamlFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if !info.Mode().IsRegular() {
		return false
	}

	// Check if it's a YAML file
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yml" || ext == ".yaml"
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

	txn, importCtx, err := s.db.NewTransaction(ctx, true)
	if err != nil {
		return cerrors.Errorf("could not create db transaction: %w", err)
	}
	defer txn.Discard()

	err = s.Import(importCtx, cfg)
	if err != nil {
		return cerrors.Errorf("could not import pipeline: %w", err)
	}

	// commit db transaction
	err = txn.Commit()
	if err != nil {
		return cerrors.Errorf("could not commit db transaction: %w", err)
	}

	// check if pipeline should be running
	if cfg.Status == config.StatusRunning {
		err := s.lifecycleService.Start(ctx, cfg.ID)
		if err != nil {
			return cerrors.Errorf("could not start the pipeline %q: %w", cfg.ID, err)
		}
	}

	return nil
}

func (s *Service) deleteOldPipelines(ctx context.Context, keepIDs []string) []string {
	var deletedIDs []string
	pipelines := s.pipelineService.List(ctx)
	for id, pl := range pipelines {
		if slices.Contains(keepIDs, id) || pl.ProvisionedBy != pipeline.ProvisionTypeConfig {
			continue
		}
		err := s.Delete(ctx, id)
		if err != nil {
			s.logger.Warn(ctx).
				Err(err).
				Str(log.PipelineIDField, id).
				Msg("failed to delete a pipeline provisioned by a config file, the pipeline is probably in a broken state, Conduit will try to remove it again next time it runs")
			continue
		}
		deletedIDs = append(deletedIDs, id)
	}
	return deletedIDs
}

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

// parseYamlInput parses a YAML input (either a file path or a YAML string) and returns all pipeline configurations.
func (s *Service) parseYamlInput(ctx context.Context, yamlInput string) ([]config.Pipeline, string, error) {
	// First, determine if the input is a file path or a YAML string
	isFilePath := false
	if !strings.Contains(yamlInput, "\n") && len(yamlInput) < 1024 {
		if _, err := os.Stat(yamlInput); err == nil {
			isFilePath = true
		}
	}

	var source string
	var pipelineConfigs []config.Pipeline

	// Parse the YAML input
	if isFilePath {
		s.logger.Debug(ctx).
			Str("file_path", yamlInput).
			Msg("parsing pipeline configurations from file")

		// Parse the file and get all pipeline configurations
		configs, err := s.parsePipelineConfigFile(ctx, yamlInput)
		if err != nil {
			return nil, "", cerrors.Errorf("failed to parse file %q: %w", yamlInput, err)
		}

		if len(configs) == 0 {
			return nil, "", cerrors.New("no pipelines found in the YAML file")
		}

		// Use all pipelines from the file
		pipelineConfigs = configs
		source = yamlInput
	} else {
		s.logger.Debug(ctx).
			Msg("parsing pipeline configurations from YAML string")

		reader := strings.NewReader(yamlInput)
		configs, err := s.parser.Parse(ctx, reader)
		if err != nil {
			return nil, "", cerrors.Errorf("failed to parse YAML string: %w", err)
		}

		if len(configs) == 0 {
			return nil, "", cerrors.New("no pipelines found in the YAML string")
		}

		// Use all pipelines from the string
		pipelineConfigs = configs
		source = "<yaml-string>"
	}

	// Log the number of pipelines found
	s.logger.Debug(ctx).
		Int("pipeline_count", len(pipelineConfigs)).
		Msg("found pipelines in YAML")

	return pipelineConfigs, source, nil
}

// UpsertYamlResult represents the result of processing a YAML input with multiple pipelines
type UpsertYamlResult struct {
	PipelineIDs []string `json:"pipelineIDs"` // IDs of all successfully processed pipelines
}

// UpsertYaml parses a YAML input (either a file path or a YAML string) and creates or updates pipelines.
// If a pipeline doesn't exist, it will be created. If it does exist, it will be updated.
// This can be used to reload pipeline configurations without restarting Conduit.
// The function processes all pipeline definitions found in the YAML input.
// Returns a list of all successfully processed pipeline IDs, or an error if parsing failed.
func (s *Service) UpsertYaml(ctx context.Context, yamlInput string) (*UpsertYamlResult, error) {
	// Parse the YAML input
	pipelineConfigs, source, err := s.parseYamlInput(ctx, yamlInput)
	if err != nil {
		return nil, err
	}

	// Initialize the result
	result := &UpsertYamlResult{
		PipelineIDs: make([]string, 0, len(pipelineConfigs)),
	}

	// Process all pipelines
	var errs []error

	s.logger.Info(ctx).
		Int("pipeline_count", len(pipelineConfigs)).
		Msg("processing pipelines from YAML")

	for i, pipelineConfig := range pipelineConfigs {
		// Process the pipeline
		pipelineID, err := s.reloadPipeline(ctx, pipelineConfig, source)
		if err != nil {
			errs = append(errs, cerrors.Errorf("failed to process pipeline %d (%s): %w", i, pipelineConfig.ID, err))
		} else {
			// Add successfully processed pipeline ID to the result
			result.PipelineIDs = append(result.PipelineIDs, pipelineID)
		}
	}

	// If there were any errors, return them along with the partial result
	if len(errs) > 0 {
		return result, cerrors.Join(errs...)
	}

	return result, nil
}

// reloadPipeline is a helper function that handles the common logic for reloading a pipeline
// from either a file or a YAML string. It will create a new pipeline if it doesn't exist,
// or update an existing one if it does.
func (s *Service) reloadPipeline(ctx context.Context, pipelineConfig config.Pipeline, source string) (string, error) {
	// Extract the pipeline ID from the config
	pipelineID := pipelineConfig.ID

	// Check if pipeline already exists
	pipelineInstance, err := s.pipelineService.Get(ctx, pipelineID)
	if err != nil {
		if cerrors.Is(err, pipeline.ErrInstanceNotFound) {
			// Pipeline doesn't exist, create it
			return s.upsertPipeline(ctx, nil, pipelineConfig, source)
		} else {
			return "", cerrors.Errorf("error getting pipeline instance with ID %q: %w", pipelineID, err)
		}
	} else {
		// Pipeline exists, update it
		return s.upsertPipeline(ctx, pipelineInstance, pipelineConfig, source)
	}
}

// upsertPipeline creates a new pipeline or updates an existing one with the given configuration.
// If pipelineInstance is nil, a new pipeline will be created. Otherwise, the existing pipeline will be updated.
func (s *Service) upsertPipeline(ctx context.Context, pipelineInstance *pipeline.Instance, pipelineConfig config.Pipeline, source string) (string, error) {
	pipelineID := pipelineConfig.ID

	// Check if we're creating a new pipeline or updating an existing one
	isCreate := pipelineInstance == nil

	if isCreate {
		// Creating a new pipeline
		s.logger.Info(ctx).
			Str("pipeline_id", pipelineID).
			Msg("pipeline doesn't exist, creating new pipeline")
	} else {
		// Updating an existing pipeline
		// Check if the pipeline was provisioned by config
		if pipelineInstance.ProvisionedBy != pipeline.ProvisionTypeConfig {
			return "", cerrors.Errorf("pipeline with ID %q was not provisioned by config: %w", pipelineID, ErrNotProvisionedByConfig)
		}

		// Check if pipeline is running and stop it if needed
		pipelineWasRunning := pipelineInstance.GetStatus() == pipeline.StatusRunning
		if pipelineWasRunning {
			s.logger.Debug(ctx).
				Str("pipeline_id", pipelineID).
				Msg("stopping pipeline before updating configuration")

			err := s.lifecycleService.Stop(ctx, pipelineID, false)
			if err != nil {
				// Ignore the error if the pipeline is not running
				if !strings.Contains(err.Error(), "pipeline not running") {
					return "", cerrors.Errorf("could not stop pipeline %q before updating: %w", pipelineID, err)
				}
				s.logger.Debug(ctx).
					Str("pipeline_id", pipelineID).
					Msg("pipeline was not running, continuing with update")
			}
		}

		// Delete the existing pipeline
		s.logger.Debug(ctx).
			Str("pipeline_id", pipelineID).
			Msg("deleting existing pipeline before recreating")

		// Use s.Delete to properly clean up the pipeline
		err := s.Delete(ctx, pipelineID)
		if err != nil {
			return "", cerrors.Errorf("could not delete existing pipeline %q: %w", pipelineID, err)
		}
	}

	// Provision the pipeline with the configuration
	s.logger.Debug(ctx).
		Str("pipeline_id", pipelineID).
		Msg("provisioning pipeline")

	err := s.provisionPipeline(ctx, pipelineConfig)
	if err != nil {
		return "", cerrors.Errorf("pipeline %q, error while provisioning: %w", pipelineID, err)
	}

	// Log success message based on whether we created or updated the pipeline
	if isCreate {
		s.logger.Info(ctx).
			Str("pipeline_id", pipelineID).
			Str("source", source).
			Msg("pipeline created successfully")
	} else {
		s.logger.Info(ctx).
			Str("pipeline_id", pipelineID).
			Str("source", source).
			Msg("pipeline configuration updated successfully")
	}

	return pipelineID, nil
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

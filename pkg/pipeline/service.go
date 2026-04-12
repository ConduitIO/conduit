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

package pipeline

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var idRegex = regexp.MustCompile(`^[A-Za-z0-9-_:.]*$`)

const (
	IDLengthLimit          = 128
	NameLengthLimit        = 128
	DescriptionLengthLimit = 8192
)

// Service manages pipelines.
type Service struct {
	logger log.CtxLogger

	store *Store

	instances     map[string]*Instance
	instanceNames map[string]bool
}

// NewService initializes and returns a pipeline Service.
func NewService(logger log.CtxLogger, db database.DB) *Service {
	return &Service{
		logger:        logger.WithComponent("pipeline.Service"),
		store:         NewStore(db),
		instances:     make(map[string]*Instance),
		instanceNames: make(map[string]bool),
	}
}

func (s *Service) Check(ctx context.Context) error {
	return s.store.db.Ping(ctx)
}

// Init fetches instances from the store without running any. Connectors and processors should be initialized
// before calling this function.
func (s *Service) Init(ctx context.Context) error {
	s.logger.Debug(ctx).Msg("initializing pipelines")
	instances, err := s.store.GetAll(ctx)
	if err != nil {
		return cerrors.Errorf("could not retrieve pipeline instances from store: %w", err)
	}

	s.instances = instances

	// some instances may be in a running state, put them in StatusSystemStopped state for now
	for _, instance := range instances {
		s.instanceNames[instance.Config.Name] = true
		if instance.GetStatus() == StatusRunning {
			// change status to "systemStopped" to mark which pipeline was running
			instance.SetStatus(StatusSystemStopped)
		}

		s.updateNewStatusMetrics(instance)
	}

	s.logger.Info(ctx).Int("count", len(s.instances)).Msg("pipelines initialized")

	return err
}

// List returns all pipeline instances in the Service.
func (s *Service) List(context.Context) map[string]*Instance {
	if len(s.instances) == 0 {
		return nil
	}

	// make a copy of the map
	tmp := make(map[string]*Instance, len(s.instances))
	for k, v := range s.instances {
		tmp[k] = v
	}

	return tmp
}

// Get will return a single pipeline instance or an error.
func (s *Service) Get(_ context.Context, id string) (*Instance, error) {
	p, ok := s.instances[id]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "pipeline instance not found (ID: %s)", id)
	}
	return p, nil
}

// Create will create a new pipeline instance with the given config and return
// if it was successfully saved to the database.
func (s *Service) Create(ctx context.Context, id string, cfg Config, p ProvisionType) (*Instance, error) {
	err := s.validatePipeline(cfg, id)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "pipeline is invalid: %v", err.Error())
	}

	t := time.Now()
	pl := &Instance{
		ID:            id,
		Config:        cfg,
		status:        StatusUserStopped,
		CreatedAt:     t,
		UpdatedAt:     t,
		ProvisionedBy: p,
		DLQ:           DefaultDLQ,
	}

	err = s.store.Set(ctx, pl.ID, pl)
	if err != nil {
		return nil, cerrors.Errorf("failed to save pipeline with ID %q: %w", pl.ID, err)
	}

	s.instances[pl.ID] = pl
	s.instanceNames[cfg.Name] = true

	s.updateNewStatusMetrics(pl)

	return pl, nil
}

// Update will update a pipeline instance config.
func (s *Service) Update(ctx context.Context, pipelineID string, cfg Config) (*Instance, error) {
	pl, err := s.Get(ctx, pipelineID)
	if err != nil {
		return nil, err
	}
	if cfg.Name == "" {
		return nil, status.Errorf(codes.InvalidArgument, "must provide a pipeline name")
	}

	// delete the old name from the names set
	exists := s.instanceNames[cfg.Name]
	if exists && pl.Config.Name != cfg.Name {
		return nil, status.Errorf(codes.AlreadyExists, "pipeline name already exists")
	}

	delete(s.instanceNames, pl.Config.Name)
	pl.Config = cfg
	pl.UpdatedAt = time.Now()
	// update the name in the names set
	s.instanceNames[cfg.Name] = true
	err = s.store.Set(ctx, pl.ID, pl)
	if err != nil {
		return nil, cerrors.Errorf("failed to save pipeline with ID %q: %w", pl.ID, err)
	}

	return pl, err
}

// UpdateDLQ will update a pipeline DLQ config.
func (s *Service) UpdateDLQ(ctx context.Context, pipelineID string, cfg DLQ) (*Instance, error) {
	pl, err := s.Get(ctx, pipelineID)
	if err != nil {
		return nil, err
	}

	if cfg.Plugin == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%w: DLQ plugin must be provided", ErrPipelineInvalidDLQConfig)
	}
	if cfg.WindowSize < 0 {
		return nil, status.Errorf(codes.InvalidArgument, "%w: DLQ window size must be non-negative", ErrPipelineInvalidDLQConfig)
	}
	if cfg.WindowNackThreshold < 0 {
		return nil, status.Errorf(codes.InvalidArgument, "%w: DLQ window nack threshold must be non-negative", ErrPipelineInvalidDLQConfig)
	}
	if cfg.WindowSize > 0 && cfg.WindowSize <= cfg.WindowNackThreshold {
		return nil, status.Errorf(codes.InvalidArgument, "%w: DLQ window nack threshold must be lower than window size", ErrPipelineInvalidDLQConfig)
	}

	pl.DLQ = cfg
	pl.UpdatedAt = time.Now()
	err = s.store.Set(ctx, pl.ID, pl)
	if err != nil {
		return nil, cerrors.Errorf("failed to save pipeline with ID %q: %w", pl.ID, err)
	}

	return pl, err
}

// AddConnector adds a connector to a pipeline.
func (s *Service) AddConnector(ctx context.Context, pipelineID string, connectorID string) (*Instance, error) {
	pl, err := s.Get(ctx, pipelineID)
	if err != nil {
		return nil, err
	}
	pl.ConnectorIDs = append(pl.ConnectorIDs, connectorID)
	pl.UpdatedAt = time.Now()
	err = s.store.Set(ctx, pl.ID, pl)
	if err != nil {
		return nil, cerrors.Errorf("failed to save pipeline with ID %q: %w", pl.ID, err)
	}

	return pl, err
}

// RemoveConnector removes a connector from a pipeline.
func (s *Service) RemoveConnector(ctx context.Context, pipelineID string, connectorID string) (*Instance, error) {
	pl, err := s.Get(ctx, pipelineID)
	if err != nil {
		return nil, err
	}
	connectorIndex := -1
	for index, id := range pl.ConnectorIDs {
		if id == connectorID {
			connectorIndex = index
			break
		}
	}
	if connectorIndex == -1 {
		return nil, status.Errorf(codes.NotFound, "connector ID not found (ID: %s)", connectorID)
	}

	pl.ConnectorIDs = pl.ConnectorIDs[:connectorIndex+copy(pl.ConnectorIDs[connectorIndex:], pl.ConnectorIDs[connectorIndex+1:])]
	pl.UpdatedAt = time.Now()

	err = s.store.Set(ctx, pl.ID, pl)
	if err != nil {
		return nil, cerrors.Errorf("failed to save pipeline with ID %q: %w", pl.ID, err)
	}

	return pl, err
}

// AddProcessor adds a processor to a pipeline.
func (s *Service) AddProcessor(ctx context.Context, pipelineID string, processorID string) (*Instance, error) {
	pl, err := s.Get(ctx, pipelineID)
	if err != nil {
		return nil, err
	}
	pl.ProcessorIDs = append(pl.ProcessorIDs, processorID)
	pl.UpdatedAt = time.Now()
	err = s.store.Set(ctx, pl.ID, pl)
	if err != nil {
		return nil, cerrors.Errorf("failed to save pipeline with ID %q: %w", pl.ID, err)
	}

	return pl, err
}

// RemoveProcessor removes a processor from a pipeline.
func (s *Service) RemoveProcessor(ctx context.Context, pipelineID string, processorID string) (*Instance, error) {
	pl, err := s.Get(ctx, pipelineID)
	if err != nil {
		return nil, err
	}
	processorIndex := -1
	for index, id := range pl.ProcessorIDs {
		if id == processorID {
			processorIndex = index
			break
		}
	}
	if processorIndex == -1 {
		return nil, status.Errorf(codes.NotFound, "processor ID not found (ID: %s)", processorID)
	}

	pl.ProcessorIDs = pl.ProcessorIDs[:processorIndex+copy(pl.ProcessorIDs[processorIndex:], pl.ProcessorIDs[processorIndex+1:])]
	pl.UpdatedAt = time.Now()

	err = s.store.Set(ctx, pl.ID, pl)
	if err != nil {
		return nil, cerrors.Errorf("failed to save pipeline with ID %q: %w", pl.ID, err)
	}

	return pl, err
}

// Delete removes a pipeline instance from the Service.
func (s *Service) Delete(ctx context.Context, pipelineID string) error {
	pl, err := s.Get(ctx, pipelineID)
	if err != nil {
		return err
	}
	err = s.store.Delete(ctx, pl.ID)
	if err != nil {
		return cerrors.Errorf("could not delete pipeline instance from store: %w", err)
	}

	delete(s.instances, pl.ID)
	delete(s.instanceNames, pl.Config.Name)

	s.updateOldStatusMetrics(pl)

	return nil
}

// Start starts a pipeline.
func (s *Service) Start(ctx context.Context, id string) error {
	pl, err := s.Get(ctx, id)
	if err != nil {
		return err
	}

	if pl.GetStatus() == StatusRunning {
		return status.Errorf(codes.FailedPrecondition, "%w: pipeline is already running", ErrPipelineRunning)
	}

	// Simple validation based on issue: "pipeline which don't have connectors or have only 1 connector"
	// A real implementation would fetch connector types (source/destination) from connector service
	if len(pl.ConnectorIDs) == 0 {
		return status.Errorf(codes.FailedPrecondition, "%w: pipeline can't be started without any connectors", ErrPipelineNoSourceConnectors)
	}
	if len(pl.ConnectorIDs) == 1 {
		// This covers the case of "only 1 connector" - it cannot be both source and destination
		return status.Errorf(codes.FailedPrecondition, "%w: pipeline requires at least one source and one destination connector to start", ErrPipelineInvalidTopology)
	}
	
	err = s.UpdateStatus(ctx, id, StatusRunning, "")
	if err != nil {
		return status.Errorf(codes.Internal, "failed to update pipeline status to running: %v", err)
	}
	s.logger.Info(ctx).Str(log.PipelineIDField, pl.ID).Msg("Pipeline started successfully")
	return nil
}

// Stop stops a pipeline.
func (s *Service) Stop(ctx context.Context, id string) error {
	pl, err := s.Get(ctx, id)
	if err != nil {
		return err
	}

	if pl.GetStatus() != StatusRunning {
		return status.Errorf(codes.FailedPrecondition, "%w: pipeline is not running or already stopped", ErrPipelineNotRunning)
	}

	err = s.UpdateStatus(ctx, id, StatusUserStopped, "")
	if err != nil {
		return status.Errorf(codes.Internal, "failed to update pipeline status to stopped: %v", err)
	}
	s.logger.Info(ctx).Str(log.PipelineIDField, pl.ID).Msg("Pipeline stopped successfully")
	return nil
}

func (s *Service) validatePipeline(cfg Config, id string) error {
	// contains all the errors occurred while provisioning configuration files.
	var errs []error

	if cfg.Name == "" {
		errs = append(errs, ErrNameMissing)
	}
	if s.instanceNames[cfg.Name] {
		errs = append(errs, ErrNameAlreadyExists)
	}
	if len(cfg.Name) > NameLengthLimit {
		errs = append(errs, cerrors.Errorf("%w: max %d characters", ErrNameOverLimit, NameLengthLimit))
	}
	if len(cfg.Description) > DescriptionLengthLimit {
		errs = append(errs, cerrors.Errorf("%w: max %d characters", ErrDescriptionOverLimit, DescriptionLengthLimit))
	}
	if id == "" {
		errs = append(errs, ErrIDMissing)
	}
	matched := idRegex.MatchString(id)
	if !matched {
		errs = append(errs, ErrInvalidCharacters)
	}
	if len(id) > IDLengthLimit {
		errs = append(errs, cerrors.Errorf("%w: max %d characters", ErrIDOverLimit, IDLengthLimit))
	}

	return cerrors.Join(errs...)
}

// UpdateStatus updates the status of a pipeline by the ID.
func (s *Service) UpdateStatus(ctx context.Context, id string, status Status, errMsg string) error {
	pipeline, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	s.updateOldStatusMetrics(pipeline)
	pipeline.SetStatus(status)

	pipeline.Error = errMsg
	s.updateNewStatusMetrics(pipeline)

	err = s.store.Set(ctx, pipeline.ID, pipeline)
	if err != nil {
		return cerrors.Errorf("pipeline not updated in store: %w", err)
	}
	return nil
}

func (s *Service) updateOldStatusMetrics(pl *Instance) {
	status := strings.ToLower(pl.GetStatus().String())
	measure.PipelinesGauge.WithValues(status).Dec()
}

func (s *Service) updateNewStatusMetrics(pl *Instance) {
	status := strings.ToLower(pl.GetStatus().String())
	measure.PipelinesGauge.WithValues(status).Inc()
	measure.PipelineStatusGauge.WithValues(pl.Config.Name).Set(float64(pl.GetStatus()))
}

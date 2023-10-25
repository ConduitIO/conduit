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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
)

type FailureEvent struct {
	// ID is the ID of the pipeline which failed.
	ID    string
	Error error
}

type FailureHandler func(FailureEvent)

// Service manages pipelines.
type Service struct {
	logger log.CtxLogger

	store *Store

	instances     map[string]*Instance
	instanceNames map[string]bool
	handlers      []FailureHandler
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
		if instance.Status == StatusRunning {
			// change status to "systemStopped" to mark which pipeline was running
			instance.Status = StatusSystemStopped
		}
		measure.PipelinesGauge.WithValues(strings.ToLower(instance.Status.String())).Inc()
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
		return nil, cerrors.Errorf("%w (ID: %s)", ErrInstanceNotFound, id)
	}
	return p, nil
}

// Create will create a new pipeline instance with the given config and return
// it if it was successfully saved to the database.
func (s *Service) Create(ctx context.Context, id string, cfg Config, p ProvisionType) (*Instance, error) {
	err := s.ValidatePipeline(cfg, id)
	if err != nil {
		return nil, cerrors.Errorf("pipeline could not be validated: %w", err)
	}

	t := time.Now()
	pl := &Instance{
		ID:            id,
		Config:        cfg,
		Status:        StatusUserStopped,
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
	measure.PipelinesGauge.WithValues(strings.ToLower(pl.Status.String())).Inc()

	return pl, nil
}

// Update will update a pipeline instance config.
func (s *Service) Update(ctx context.Context, pipelineID string, cfg Config) (*Instance, error) {
	pl, err := s.Get(ctx, pipelineID)
	if err != nil {
		return nil, err
	}
	if cfg.Name == "" {
		return nil, ErrNameMissing
	}

	// delete the old name from the names set
	exists := s.instanceNames[cfg.Name]
	if exists && pl.Config.Name != cfg.Name {
		return nil, ErrNameAlreadyExists
	}

	delete(s.instanceNames, pl.Config.Name) // delete the old name
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
		return nil, cerrors.New("DLQ plugin must be provided")
	}
	if cfg.WindowSize < 0 {
		return nil, cerrors.New("DLQ window size must be non-negative")
	}
	if cfg.WindowNackThreshold < 0 {
		return nil, cerrors.New("DLQ window nack threshold must be non-negative")
	}
	if cfg.WindowSize > 0 && cfg.WindowSize <= cfg.WindowNackThreshold {
		return nil, cerrors.New("DLQ window nack threshold must be lower than window size")
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
		return nil, cerrors.Errorf("%w (ID: %s)", ErrConnectorIDNotFound, connectorID)
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
		return nil, cerrors.Errorf("%w (ID: %s)", ErrProcessorIDNotFound, processorID)
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
	measure.PipelinesGauge.WithValues(strings.ToLower(pl.Status.String())).Dec()

	return nil
}

// OnFailure registers a handler for a pipeline.FailureEvent.
// Only errors which happen after a pipeline has been started
// are being sent.
func (s *Service) OnFailure(handler FailureHandler) {
	s.handlers = append(s.handlers, handler)
}

// notify notifies all registered FailureHandlers about an error.
func (s *Service) notify(pipelineID string, err error) {
	if err == nil {
		return
	}
	e := FailureEvent{
		ID:    pipelineID,
		Error: err,
	}
	for _, handler := range s.handlers {
		handler(e)
	}
}
func (s *Service) ValidatePipeline(cfg Config, id string) (err error) {
	pattern := `[A-Za-z0-9\-\_]`

	if cfg.Name == "" {
		return ErrNameMissing
	}
	if s.instanceNames[cfg.Name] {
		return ErrNameAlreadyExists
	}
	if len(cfg.Name) > 1<<6 { // 64 characters
		return ErrNameOverLimit
	}
	if len(cfg.Description) > 1<<13 { // 8192 characters
		return ErrDescriptionOverLimit
	}

	matched, err := regexp.MatchString(pattern, id)
	if err != nil {
		return cerrors.Errorf("failed to match string: %w", err)
	}
	if !matched {
		return ErrInvalidCharacters
	}

	return nil
}

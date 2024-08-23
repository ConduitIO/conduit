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

//go:generate mockgen -typed -destination=mock/plugin_service.go -package=mock -mock_names=PluginService=PluginService . PluginService

package processor

import (
	"context"
	"time"

	"github.com/conduitio/conduit-commons/database"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
)

type PluginService interface {
	NewProcessor(ctx context.Context, pluginName string, id string) (sdk.Processor, error)
}

type Service struct {
	logger log.CtxLogger

	registry  PluginService
	instances map[string]*Instance
	store     *Store
}

// NewService creates a new processor plugin service.
func NewService(logger log.CtxLogger, db database.DB, registry PluginService) *Service {
	return &Service{
		logger:    logger.WithComponent("processor.Service"),
		registry:  registry,
		instances: make(map[string]*Instance),
		store:     NewStore(db),
	}
}

// Init fetches instances from the store.
func (s *Service) Init(ctx context.Context) error {
	s.logger.Debug(ctx).Msg("initializing processors")
	instances, err := s.store.GetAll(ctx)
	if err != nil {
		return cerrors.Errorf("could not retrieve processor instances from store: %w", err)
	}

	s.instances = instances
	s.logger.Info(ctx).Int("count", len(s.instances)).Msg("processors initialized")

	for _, i := range instances {
		i.init(s.logger)
		measure.ProcessorsGauge.WithValues(i.Plugin).Inc()
	}

	return nil
}

func (s *Service) Check(ctx context.Context) error {
	return s.store.db.Ping(ctx)
}

// List returns all processors in the Service.
func (s *Service) List(_ context.Context) map[string]*Instance {
	// make a copy of the map
	tmp := make(map[string]*Instance, len(s.instances))
	for k, v := range s.instances {
		tmp[k] = v
	}
	return tmp
}

// Get will return a single processor or an error.
func (s *Service) Get(_ context.Context, id string) (*Instance, error) {
	ins, ok := s.instances[id]
	if !ok {
		return nil, cerrors.Errorf("%w (ID: %s)", ErrInstanceNotFound, id)
	}
	return ins, nil
}

func (s *Service) MakeRunnableProcessor(ctx context.Context, i *Instance) (*RunnableProcessor, error) {
	if i.running {
		return nil, ErrProcessorRunning
	}

	p, err := s.registry.NewProcessor(ctx, i.Plugin, i.ID)
	if err != nil {
		return nil, err
	}
	cond, err := newProcessorCondition(i.Condition)
	if err != nil {
		return nil, cerrors.Errorf("invalid condition: %w", err)
	}

	i.running = true
	return newRunnableProcessor(p, cond, i), nil
}

// Create will create a new processor instance.
func (s *Service) Create(
	ctx context.Context,
	id string,
	plugin string,
	parent Parent,
	cfg Config,
	pt ProvisionType,
	cond string,
) (*Instance, error) {
	if cfg.Workers == 0 {
		cfg.Workers = 1
	}

	// check if the processor plugin exists
	p, err := s.registry.NewProcessor(ctx, plugin, id)
	if err != nil {
		return nil, cerrors.Errorf("could not get processor: %w", err)
	}
	err = p.Teardown(ctx)
	if err != nil {
		s.logger.Warn(ctx).Err(err).Msg("processor teardown failed")
	}

	now := time.Now()
	instance := &Instance{
		ID:            id,
		UpdatedAt:     now,
		CreatedAt:     now,
		ProvisionedBy: pt,
		Plugin:        plugin,
		Parent:        parent,
		Config:        cfg,
		Condition:     cond,
	}
	instance.init(s.logger)

	// persist instance
	err = s.store.Set(ctx, instance.ID, instance)
	if err != nil {
		return nil, err
	}

	s.instances[instance.ID] = instance
	measure.ProcessorsGauge.WithValues(plugin).Inc()

	return instance, nil
}

// Update will update a processor instance config.
func (s *Service) Update(ctx context.Context, id string, cfg Config) (*Instance, error) {
	instance, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if instance.running {
		return nil, cerrors.Errorf("could not update processor instance (ID: %s): %w", id, ErrProcessorRunning)
	}

	instance.Config = cfg
	instance.UpdatedAt = time.Now()

	// persist instance
	err = s.store.Set(ctx, instance.ID, instance)
	if err != nil {
		return nil, err
	}

	return instance, nil
}

// Delete removes a processor from the Service.
func (s *Service) Delete(ctx context.Context, id string) error {
	// make sure the processor exists
	instance, err := s.Get(ctx, id)
	if err != nil {
		return err
	}

	if instance.running {
		return cerrors.Errorf("could not delete processor instance (ID: %s): %w", id, ErrProcessorRunning)
	}

	err = s.store.Delete(ctx, id)
	if err != nil {
		return cerrors.Errorf("could not delete processor instance from store: %w", err)
	}
	delete(s.instances, id)
	instance.Close()
	measure.ProcessorsGauge.WithValues(instance.Plugin).Dec()

	return nil
}

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

package processor

import (
	"context"
	"github.com/conduitio/conduit/pkg/plugin/processor"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
)

type Service struct {
	logger log.CtxLogger

	registry  *processor.Registry
	instances map[string]*Instance
	store     *Store
}

// NewService creates a new processor service.
func NewService(logger log.CtxLogger, db database.DB, registry *processor.Registry) *Service {
	return &Service{
		logger:    logger.WithComponent("processor.Service"),
		registry:  registry,
		instances: make(map[string]*Instance),
		store:     NewStore(db, registry),
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
		measure.ProcessorsGauge.WithValues(i.Type).Inc()
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
	if cfg.Workers < 0 {
		return nil, cerrors.New("processor workers can't be negative")
	}
	if cfg.Workers == 0 {
		cfg.Workers = 1
	}

	// todo make registru return a processor.Interface
	// (add inspector there automatically)
	p, err := s.registry.Get(ctx, plugin, id)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	instance := &Instance{
		ID:            id,
		UpdatedAt:     now,
		CreatedAt:     now,
		ProvisionedBy: pt,
		Type:          plugin,
		Parent:        parent,
		Config:        cfg,
		Processor:     newInspectableProcessor(p, s.logger),
		Condition:     cond,
	}

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

	// this can't really fail, this call already passed when creating the instance
	p, err := s.registry.Get(ctx, instance.Type, instance.ID)
	if err != nil {
		return nil, cerrors.Errorf("could not get processor: %w", err)
	}

	instance.Processor = newInspectableProcessor(p, s.logger)
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

	err = s.store.Delete(ctx, id)
	if err != nil {
		return cerrors.Errorf("could not delete processor instance from store: %w", err)
	}
	delete(s.instances, id)
	// todo handle error
	instance.Processor.Teardown(ctx)
	measure.ProcessorsGauge.WithValues(instance.Type).Dec()

	return nil
}

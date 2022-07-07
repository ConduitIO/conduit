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
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
)

type Service struct {
	logger log.CtxLogger

	registry  *BuilderRegistry
	instances map[string]*Instance
	store     *Store
}

// NewService creates a new processor service.
func NewService(logger log.CtxLogger, db database.DB, registry *BuilderRegistry) *Service {
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
		measure.ProcessorsGauge.WithValues(i.Name).Inc()
	}

	return nil
}

// List returns all processors in the Service.
func (s *Service) List(ctx context.Context) map[string]*Instance {
	// make a copy of the map
	tmp := make(map[string]*Instance, len(s.instances))
	for k, v := range s.instances {
		tmp[k] = v
	}
	return tmp
}

// Get will return a single processor or an error.
func (s *Service) Get(ctx context.Context, id string) (*Instance, error) {
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
	name string,
	parent Parent,
	cfg Config,
	pt ProvisionType,
) (*Instance, error) {
	builder, err := s.registry.Get(name)
	if err != nil {
		return nil, err
	}

	p, err := builder(cfg)
	if err != nil {
		return nil, cerrors.Errorf("could not build processor: %w", err)
	}

	now := time.Now()
	instance := &Instance{
		ID:            id,
		UpdatedAt:     now,
		CreatedAt:     now,
		ProvisionedBy: pt,
		Name:          name,
		Parent:        parent,
		Config:        cfg,
		Processor:     p,
	}

	// persist instance
	err = s.store.Set(ctx, instance.ID, instance)
	if err != nil {
		return nil, err
	}

	s.instances[instance.ID] = instance
	measure.ProcessorsGauge.WithValues(name).Inc()

	return instance, nil
}

// Update will update a processor instance config.
func (s *Service) Update(ctx context.Context, id string, cfg Config) (*Instance, error) {
	instance, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	// this can't really fail, this call already passed when creating the instance
	builder, _ := s.registry.Get(instance.Name)

	p, err := builder(cfg)
	if err != nil {
		return nil, cerrors.Errorf("could not build processor: %w", err)
	}

	instance.Processor = p
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
	measure.ProcessorsGauge.WithValues(instance.Name).Dec()

	return nil
}

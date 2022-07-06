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

package connector

import (
	"context"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
)

// Service manages connectors.
type Service struct {
	logger      log.CtxLogger
	connBuilder Builder

	connectors map[string]Connector
	store      *Store
}

// NewService creates a Store-backed implementation of Service.
func NewService(logger log.CtxLogger, db database.DB, connBuilder Builder) *Service {
	return &Service{
		logger:      logger.WithComponent("connector.Service"),
		connBuilder: connBuilder,
		store:       NewStore(db, logger, connBuilder),
		connectors:  make(map[string]Connector),
	}
}

// Init fetches connectors from the store.
func (s *Service) Init(ctx context.Context) error {
	s.logger.Debug(ctx).Msg("initializing connectors")
	connectors, err := s.store.GetAll(ctx)
	if err != nil {
		return cerrors.Errorf("could not retrieve connectors from store: %w", err)
	}

	s.connectors = connectors
	s.logger.Info(ctx).Int("count", len(s.connectors)).Msg("connectors initialized")

	for _, i := range connectors {
		measure.ConnectorsGauge.WithValues(strings.ToLower(i.Type().String())).Inc()
	}

	return nil
}

// List returns a map of Instances keyed by their ID. Instances do not
// necessarily have a running plugin associated with them.
func (s *Service) List(ctx context.Context) map[string]Connector {
	// make a copy of the map
	tmp := make(map[string]Connector, len(s.connectors))
	for k, v := range s.connectors {
		tmp[k] = v
	}
	return tmp
}

// Get retrieves a single connector instance by ID.
func (s *Service) Get(ctx context.Context, id string) (Connector, error) {
	ins, ok := s.connectors[id]
	if !ok {
		return nil, cerrors.Errorf("%w (ID: %s)", ErrInstanceNotFound, id)
	}
	return ins, nil
}

// Create will create a connector instance, persist it and return it.
func (s *Service) Create(ctx context.Context, id string, t Type, cfg Config, p ProvisionType) (Connector, error) {
	// determine the path of the Connector binary
	if cfg.Plugin == "" {
		return nil, cerrors.New("must provide a path to plugin binary")
	}
	if cfg.PipelineID == "" {
		return nil, cerrors.New("must provide a pipeline ID")
	}

	conn, err := s.connBuilder.Build(t)
	if err != nil {
		return nil, cerrors.Errorf("could not create connector: %w", err)
	}

	err = s.connBuilder.Init(conn, id, cfg)
	if err != nil {
		return nil, cerrors.Errorf("could not init connector: %w", err)
	}

	conn.SetProvisionedBy(p)
	// persist instance
	err = s.store.Set(ctx, id, conn)
	if err != nil {
		return nil, err
	}

	s.connectors[id] = conn
	measure.ConnectorsGauge.WithValues(strings.ToLower(t.String())).Inc()
	return conn, nil
}

// Delete removes.
func (s *Service) Delete(ctx context.Context, id string) error {
	// make sure instance exists
	instance, err := s.Get(ctx, id)
	if err != nil {
		return err
	}

	if instance.IsRunning() {
		return ErrConnectorRunning
	}

	err = s.store.Delete(ctx, id)
	if err != nil {
		return cerrors.Errorf("could not delete connector instance %v from store: %w", id, err)
	}
	delete(s.connectors, id)
	measure.ConnectorsGauge.WithValues(strings.ToLower(instance.Type().String())).Dec()

	return nil
}

// Update updates the connector config.
func (s *Service) Update(ctx context.Context, id string, data Config) (Connector, error) {
	conn, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	err = conn.Validate(ctx, data.Settings)
	if err != nil {
		return nil, cerrors.Errorf("could not update connector settings: %w", err)
	}

	conn.SetConfig(data)
	conn.SetUpdatedAt(time.Now())

	// persist conn
	err = s.store.Set(ctx, id, conn)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

// AddProcessor adds a processor to a connector.
func (s *Service) AddProcessor(ctx context.Context, connectorID string, processorID string) (Connector, error) {
	conn, err := s.Get(ctx, connectorID)
	if err != nil {
		return nil, err
	}

	d := conn.Config()
	d.ProcessorIDs = append(d.ProcessorIDs, processorID)
	conn.SetConfig(d)
	conn.SetUpdatedAt(time.Now())

	// persist conn
	err = s.store.Set(ctx, connectorID, conn)
	if err != nil {
		return nil, err
	}

	return conn, err
}

// RemoveProcessor removes a processor from a connector.
func (s *Service) RemoveProcessor(ctx context.Context, connectorID string, processorID string) (Connector, error) {
	conn, err := s.Get(ctx, connectorID)
	if err != nil {
		return nil, err
	}

	d := conn.Config()
	processorIndex := -1
	for index, id := range d.ProcessorIDs {
		if id == processorID {
			processorIndex = index
			break
		}
	}
	if processorIndex == -1 {
		return nil, cerrors.Errorf("%w (ID: %s)", ErrProcessorIDNotFound, processorID)
	}

	d.ProcessorIDs = d.ProcessorIDs[:processorIndex+copy(d.ProcessorIDs[processorIndex:], d.ProcessorIDs[processorIndex+1:])]
	conn.SetConfig(d)
	conn.SetUpdatedAt(time.Now())

	// persist conn
	err = s.store.Set(ctx, connectorID, conn)
	if err != nil {
		return nil, err
	}

	return conn, err
}

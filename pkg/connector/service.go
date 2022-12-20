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
	"github.com/conduitio/conduit/pkg/plugin"
)

// Service manages connectors.
type Service struct {
	logger log.CtxLogger

	connectors map[string]*Instance
	store      *Store
	persister  *Persister
}

// NewService creates a Store-backed implementation of Service.
func NewService(logger log.CtxLogger, db database.DB, persister *Persister) *Service {
	return &Service{
		logger:     logger.WithComponent("connector.Service"),
		store:      NewStore(db, logger),
		connectors: make(map[string]*Instance),
		persister:  persister,
	}
}

// Init fetches connectors from the store.
func (s *Service) Init(ctx context.Context, pluginService *plugin.Service) error {
	s.logger.Debug(ctx).Msg("initializing connectors")
	connectors, err := s.store.GetAll(ctx)
	if err != nil {
		return cerrors.Errorf("could not retrieve connectors from store: %w", err)
	}

	s.connectors = connectors
	s.logger.Info(ctx).Int("count", len(s.connectors)).Msg("connectors initialized")

	for _, i := range connectors {
		measure.ConnectorsGauge.WithValues(strings.ToLower(i.Type.String())).Inc()
		s.initInstance(i)

		// try to get plugin dispenser, if that's not possible log a warning
		// Conduit should not crash because a plugin does not exist, that
		// pipeline just won't be able to start
		i.pluginDispenser, err = pluginService.NewDispenser(i.logger, i.Plugin)
		if err != nil {
			s.logger.Warn(ctx).
				Err(err).
				Str(log.ConnectorIDField, i.ID).
				Str(log.PipelineIDField, i.PipelineID).
				Str(log.PluginNameField, i.Plugin).
				Msgf("did not find plugin for connector, pipeline won't be able to start (tip: make sure plugin %v is available and restart Conduit)", i.Plugin)
		}
	}

	return nil
}

// List returns a map of Instances keyed by their ID. Instances do not
// necessarily have a running plugin associated with them.
func (s *Service) List(ctx context.Context) map[string]*Instance {
	// make a copy of the map
	tmp := make(map[string]*Instance, len(s.connectors))
	for k, v := range s.connectors {
		tmp[k] = v
	}
	return tmp
}

// Get retrieves a single connector instance by ID.
func (s *Service) Get(ctx context.Context, id string) (*Instance, error) {
	ins, ok := s.connectors[id]
	if !ok {
		return nil, cerrors.Errorf("%w (ID: %s)", ErrInstanceNotFound, id)
	}
	return ins, nil
}

// Create will create a connector instance, persist it and return it.
func (s *Service) Create(
	ctx context.Context,
	id string,
	t Type,
	pluginDispenser plugin.Dispenser,
	pipelineID string,
	cfg Config,
	p ProvisionType,
) (*Instance, error) {
	// determine the path of the Connector binary
	if pluginDispenser == nil {
		return nil, cerrors.New("must provide a plugin")
	}
	if pipelineID == "" {
		return nil, cerrors.New("must provide a pipeline ID")
	}
	if t != TypeSource && t != TypeDestination {
		return nil, ErrInvalidConnectorType
	}

	now := time.Now().UTC()
	conn := &Instance{
		ID:         id,
		Type:       t,
		Config:     cfg,
		PipelineID: pipelineID,
		Plugin:     string(pluginDispenser.FullName()),

		ProvisionedBy: p,
		CreatedAt:     now,
		UpdatedAt:     now,

		pluginDispenser: pluginDispenser,
	}
	s.initInstance(conn)

	if p == ProvisionTypeDLQ {
		// do not persist the instance, just return the connector
		return conn, nil
	}

	// persist instance
	err := s.store.Set(ctx, id, conn)
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

	err = s.store.Delete(ctx, id)
	if err != nil {
		return cerrors.Errorf("could not delete connector instance %v from store: %w", id, err)
	}
	delete(s.connectors, id)
	measure.ConnectorsGauge.WithValues(strings.ToLower(instance.Type.String())).Dec()

	return nil
}

// Update updates the connector config.
func (s *Service) Update(ctx context.Context, id string, data Config) (*Instance, error) {
	conn, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	conn.Config = data
	conn.UpdatedAt = time.Now().UTC()

	// persist conn
	err = s.store.Set(ctx, id, conn)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

// AddProcessor adds a processor to a connector.
func (s *Service) AddProcessor(ctx context.Context, connectorID string, processorID string) (*Instance, error) {
	conn, err := s.Get(ctx, connectorID)
	if err != nil {
		return nil, err
	}

	conn.ProcessorIDs = append(conn.ProcessorIDs, processorID)
	conn.UpdatedAt = time.Now().UTC()

	// persist conn
	err = s.store.Set(ctx, connectorID, conn)
	if err != nil {
		return nil, err
	}

	return conn, err
}

// RemoveProcessor removes a processor from a connector.
func (s *Service) RemoveProcessor(ctx context.Context, connectorID string, processorID string) (*Instance, error) {
	conn, err := s.Get(ctx, connectorID)
	if err != nil {
		return nil, err
	}

	processorIndex := -1
	for index, id := range conn.ProcessorIDs {
		if id == processorID {
			processorIndex = index
			break
		}
	}
	if processorIndex == -1 {
		return nil, cerrors.Errorf("%w (ID: %s)", ErrProcessorIDNotFound, processorID)
	}

	conn.ProcessorIDs = conn.ProcessorIDs[:processorIndex+copy(conn.ProcessorIDs[processorIndex:], conn.ProcessorIDs[processorIndex+1:])]
	conn.UpdatedAt = time.Now().UTC()

	// persist conn
	err = s.store.Set(ctx, connectorID, conn)
	if err != nil {
		return nil, err
	}

	return conn, err
}

func (s *Service) initInstance(conn *Instance) {
	connLogger := s.logger
	connLogger.Logger = connLogger.Logger.With().
		Str(log.ConnectorIDField, conn.ID).
		Logger()
	connLogger = connLogger.WithComponent("connector." + conn.Type.String())

	conn.persister = s.persister
	conn.logger = connLogger
}

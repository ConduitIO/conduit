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
	"regexp"
	"strings"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
)

var idRegex = regexp.MustCompile(`^[A-Za-z0-9-_:.]*$`)

const (
	IDLengthLimit   = 256
	NameLengthLimit = 256
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
func (s *Service) Init(ctx context.Context) error {
	s.logger.Debug(ctx).Msg("initializing connectors")
	connectors, err := s.store.GetAll(ctx)
	if err != nil {
		return cerrors.Errorf("could not retrieve connectors from store: %w", err)
	}

	for id, c := range connectors {
		if c.ProvisionedBy == ProvisionTypeDLQ {
			// Older versions of Conduit erroneously persisted DLQ connectors,
			// which caused them to be retrieved from the DB after a restart.
			// We clean it up now. More info: https://github.com/ConduitIO/conduit/issues/1016
			s.logger.Warn(ctx).Str(log.ConnectorIDField, c.ID).Msg("found a persisted DLQ connector, ignoring it and deleting it from the store, for more info see issue #1016")
			delete(connectors, id)
			err := s.store.Delete(ctx, c.ID)
			if err != nil {
				s.logger.Err(ctx, err).Str(log.ConnectorIDField, c.ID).Msg("deletion of DLQ connector failed")
			}
		}
	}

	s.connectors = connectors
	s.logger.Info(ctx).Int("count", len(s.connectors)).Msg("connectors initialized")

	for _, conn := range connectors {
		measure.ConnectorsGauge.WithValues(strings.ToLower(conn.Type.String())).Inc()
		conn.Init(s.logger, s.persister)
	}

	return nil
}

func (s *Service) Check(ctx context.Context) error {
	return s.store.db.Ping(ctx)
}

// List returns a map of Instances keyed by their ID. Instances do not
// necessarily have a running plugin associated with them.
func (s *Service) List(context.Context) map[string]*Instance {
	// make a copy of the map
	tmp := make(map[string]*Instance, len(s.connectors))
	for k, v := range s.connectors {
		tmp[k] = v
	}
	return tmp
}

// Get retrieves a single connector instance by ID.
func (s *Service) Get(_ context.Context, id string) (*Instance, error) {
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
	plugin string,
	pipelineID string,
	cfg Config,
	p ProvisionType,
) (*Instance, error) {
	err := s.validateConnector(cfg, id)
	if err != nil {
		return nil, cerrors.Errorf("connector is invalid: %w", err)
	}

	// determine the path of the Connector binary
	if plugin == "" {
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
		Plugin:     plugin,

		ProvisionedBy: p,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	conn.Init(s.logger, s.persister)

	if p == ProvisionTypeDLQ {
		// do not persist the instance, just return the connector
		return conn, nil
	}

	// persist instance
	err = s.store.Set(ctx, id, conn)
	if err != nil {
		return nil, err
	}

	s.connectors[id] = conn
	measure.ConnectorsGauge.WithValues(strings.ToLower(t.String())).Inc()
	return conn, nil
}

// Delete removes the connector.
func (s *Service) Delete(ctx context.Context, id string, dispenserFetcher PluginDispenserFetcher) error {
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

	err = instance.Close(ctx, dispenserFetcher)
	if err != nil {
		// connector is already deleted, only log the error coming from the
		// cleanup function, use the instance logger to attach connector ID
		instance.logger.Err(ctx, err).Msg("could not close connector instance")
	}
	measure.ConnectorsGauge.WithValues(strings.ToLower(instance.Type.String())).Dec()

	return nil
}

// Update updates the connector config.
func (s *Service) Update(ctx context.Context, id string, plugin string, data Config) (*Instance, error) {
	conn, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if conn.Plugin != plugin {
		s.logger.Warn(ctx).Msgf("connector pluging changing from %v to %v, this may lead to unexpected behavior and configuration issues.")
	}
	conn.Plugin = plugin
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

func (s *Service) SetState(ctx context.Context, id string, state any) (*Instance, error) {
	conn, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if state != nil {
		switch conn.Type {
		case TypeSource:
			if _, ok := state.(SourceState); !ok {
				return nil, cerrors.Errorf("expected source state (ID: %s): %w", id, ErrInvalidConnectorStateType)
			}
		case TypeDestination:
			if _, ok := state.(DestinationState); !ok {
				return nil, cerrors.Errorf("expected destination state (ID: %s): %w", id, ErrInvalidConnectorStateType)
			}
		default:
			return nil, ErrInvalidConnectorType
		}
	}

	conn.State = state

	err = s.store.Set(ctx, id, conn)
	if err != nil {
		return nil, err
	}

	return conn, err
}

func (s *Service) validateConnector(cfg Config, id string) error {
	// contains all the errors occurred while provisioning configuration files.
	var errs []error

	if cfg.Name == "" {
		errs = append(errs, ErrNameMissing)
	}
	if len(cfg.Name) > NameLengthLimit {
		errs = append(errs, ErrNameOverLimit)
	}
	if id == "" {
		errs = append(errs, ErrIDMissing)
	}
	matched := idRegex.MatchString(id)
	if !matched {
		errs = append(errs, ErrInvalidCharacters)
	}
	if len(id) > IDLengthLimit {
		errs = append(errs, ErrIDOverLimit)
	}

	return cerrors.Join(errs...)
}

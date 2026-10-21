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
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
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

	settingsResolver SettingsResolver
}

// ServiceOption configures a Service at construction.
type ServiceOption func(*Service)

// WithSettingsResolver sets the resolver every instance uses to vet and expand
// its settings before the plugin is configured. Without it no ${VAR} reference
// is expanded (DefaultSettingsResolver).
func WithSettingsResolver(r SettingsResolver) ServiceOption {
	return func(s *Service) { s.settingsResolver = r }
}

// NewService creates a Store-backed implementation of Service.
func NewService(logger log.CtxLogger, db database.DB, persister *Persister, opts ...ServiceOption) *Service {
	s := &Service{
		logger:     logger.WithComponent("connector.Service"),
		store:      NewStore(db, logger),
		connectors: make(map[string]*Instance),
		persister:  persister,
	}
	for _, o := range opts {
		o(s)
	}
	return s
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
		conn.settingsResolver = s.settingsResolver
	}

	return nil
}

func (s *Service) Check(ctx context.Context) error {
	return s.store.db.Ping(ctx)
}

// WaitPersisted blocks until every connector position/state write already
// queued for persistence (via a connector's Ack -> Persister.Persist, or the
// final flush a connector's Teardown triggers via Persister.ConnectorStopped)
// has been durably committed to the store.
//
// It deliberately does not take a pipeline or connector ID: the persister
// batches writes across every connector in the process, so there is no
// narrower durability signal to observe than "the batch(es) already in
// flight have landed" — see Persister.WaitPendingWrites's doc for why that is
// still a precise, non-deadlocking signal for a caller that already knows
// (e.g. via lifecycle.Service.WaitPipeline) that the connector it cares about
// has stopped and thus already triggered its final flush.
func (s *Service) WaitPersisted() {
	s.persister.WaitPendingWrites()
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
		// Invariant: errors.Is(err, ErrInstanceNotFound) still holds — the sentinel
		// is wrapped, and the ConduitError adds the machine-actionable code.
		err := conduiterr.Wrap(
			CodeConnectorNotFound,
			fmt.Sprintf("connector %q not found", id),
			ErrInstanceNotFound,
		)
		err.Suggestion = "run `conduit connectors list` to see existing connectors"
		return nil, err
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
		// Invariant: errors.Is(err, ErrInvalidConnectorType) still holds — sentinel
		// wrapped, ConduitError adds the code.
		err := conduiterr.Wrap(CodeConnectorInvalidType, "invalid connector type", ErrInvalidConnectorType)
		err.Suggestion = `set the connector type to either "source" or "destination"`
		return nil, err
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
	conn.settingsResolver = s.settingsResolver

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

// Restore puts back an instance that Delete removed, for a caller that rolls
// back a failed operation after Delete succeeded (#3137). It reinstates the
// same *Instance rather than creating a new one, so the connector keeps its
// State (the source position), LastActiveConfig, ProcessorIDs and CreatedAt.
// Creating a fresh instance instead left the process with a connector that
// had never run until the next restart.
//
// If Delete already delivered the "deleted" lifecycle event to the plugin,
// the plugin may have released what the position points into: the Postgres
// connector drops its replication slot, and resuming would recreate it at
// the current WAL and skip the gap. Clearing the position is not safe either
// (a Kafka source without one starts at the end). So Restore keeps State and
// LastActiveConfig unchanged, and the instance stays marked: its next start
// fails with CodeConnectorDeleteHookRan (invariant 3, fail closed). The mark
// is in memory only; a process restart loads the instance without it.
//
// Restore writes to the store only when ctx carries no transaction. Inside a
// transaction the caller is rolling back and will discard it, so the store
// keeps the record the discarded delete never removed, and a write there
// could fail on a backend that rejects statements in an aborted transaction
// (Postgres, 25P02). Without a transaction (Service.Import, used by the
// embed API's Engine.Import) the delete reached the store, and Restore
// writes the record back.
//
// It returns an error, and changes nothing, for a nil or DLQ instance, or if
// a different instance is registered under the same ID. Restoring an
// instance that is already registered is a no-op.
func (s *Service) Restore(ctx context.Context, instance *Instance) error {
	if instance == nil {
		return cerrors.New("can't restore a nil connector instance")
	}
	if instance.ProvisionedBy == ProvisionTypeDLQ {
		return cerrors.Errorf("can't restore DLQ connector %v: DLQ connectors are not registered", instance.ID)
	}
	if current, ok := s.connectors[instance.ID]; ok {
		if current == instance {
			return nil
		}
		return cerrors.Errorf("can't restore connector %v: a different instance with that ID exists", instance.ID)
	}

	// Invariant 2: memory gets back the instance itself, never a fresh one.
	s.connectors[instance.ID] = instance
	measure.ConnectorsGauge.WithValues(strings.ToLower(instance.Type.String())).Inc()

	instance.RLock()
	hookRan := instance.deleteHookRan
	instance.RUnlock()
	if hookRan {
		instance.logger.Warn(ctx).Msg("connector restored after a failed delete whose delete hook had already run; " +
			"it will refuse to start until it is deleted and created again or Conduit is restarted")
	}

	if database.TransactionFromContext(ctx) != nil {
		return nil
	}
	if err := s.store.Set(ctx, instance.ID, instance); err != nil {
		return cerrors.Errorf("could not restore connector instance %v in store: %w", instance.ID, err)
	}
	return nil
}

// Update updates the connector config.
func (s *Service) Update(ctx context.Context, id string, plugin string, data Config) (*Instance, error) {
	conn, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if conn.Plugin != plugin {
		s.logger.Warn(ctx).Msgf("connector plugin changing from %v to %v, "+
			"this may lead to unexpected behavior and configuration issues.", conn.Plugin, plugin)
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

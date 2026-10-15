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

// Delete runs the connector's delete hook and then removes the connector.
//
// Invariant (#3058): the record is removed only after the hook succeeded.
// The hook releases what the connector created outside Conduit (a Postgres
// replication slot, for example); dropping the record first left nothing to
// retry a failed hook with, and the resource was orphaned silently. When the
// hook fails, or the plugin cannot be dispensed to run it, Delete returns
// CodeConnectorDeleteHookFailed and changes nothing, so the caller (an
// operator, or provisioning's next boot) can retry: hooks are idempotent by
// contract. A connector that never started has no hook to run (see
// Source.OnDelete) and is deleted directly.
//
// If the hook succeeds and the store delete then fails, the record is kept
// and the store error returned; a retry runs the hook again.
func (s *Service) Delete(ctx context.Context, id string, dispenserFetcher PluginDispenserFetcher) error {
	instance, err := s.Get(ctx, id)
	if err != nil {
		return err
	}

	if err := instance.Close(ctx, dispenserFetcher); err != nil {
		hookErr := deleteHookError(instance, err)
		instance.logger.Err(ctx, hookErr).Str(log.PipelineIDField, instance.PipelineID).
			Msg("connector delete hook failed, keeping the connector so the delete can be retried")
		return hookErr
	}

	return s.removeRecord(ctx, instance)
}

// ForceDelete is Delete for a connector whose delete hook cannot succeed:
// it runs the hook, and if the hook succeeds it is an ordinary Delete and
// returns a nil Orphan. If the hook fails, it records an Orphan entry and
// removes the connector anyway, returning the Orphan so the caller can
// report CodeConnectorOrphanedResources (Orphan.Warning).
//
// Invariant 5: the orphan entry and the record deletion are two writes, and
// they are atomic only inside the caller's store transaction (the
// orchestrator wraps every connector delete in one). The entry is written
// first, so without a transaction a crash between the writes leaves an entry
// next to a record that still exists, never a deleted record with no entry.
func (s *Service) ForceDelete(ctx context.Context, id string, dispenserFetcher PluginDispenserFetcher) (*Orphan, error) {
	instance, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	hookErr := instance.Close(ctx, dispenserFetcher)
	if hookErr == nil {
		return nil, s.removeRecord(ctx, instance)
	}

	orphan := &Orphan{
		Version:     OrphanVersion,
		ConnectorID: instance.ID,
		Type:        strings.ToLower(instance.Type.String()),
		PipelineID:  instance.PipelineID,
		Plugin:      instance.Plugin,
		HookError:   scrubbedHookError(instance, hookErr),
		DeletedAt:   time.Now().UTC(),
	}
	if err := s.store.setOrphan(ctx, orphan); err != nil {
		return nil, err
	}
	if err := s.removeRecord(ctx, instance); err != nil {
		return nil, err
	}
	instance.logger.Warn(ctx).
		Str(log.PipelineIDField, instance.PipelineID).
		Str("code", CodeConnectorOrphanedResources.Reason()).
		Msg(orphan.Warning().Message)
	return orphan, nil
}

// removeRecord deletes instance's record from the store and then from the
// in-memory map. The map is only touched once the store write succeeded, so
// a failed write leaves the connector visible and deletable again.
func (s *Service) removeRecord(ctx context.Context, instance *Instance) error {
	if err := s.store.Delete(ctx, instance.ID); err != nil {
		return cerrors.Errorf("could not delete connector instance %v from store: %w", instance.ID, err)
	}
	delete(s.connectors, instance.ID)
	measure.ConnectorsGauge.WithValues(strings.ToLower(instance.Type.String())).Dec()
	return nil
}

// Orphan returns the orphan entry recorded for connector id by ForceDelete,
// or nil if there is none.
func (s *Service) Orphan(ctx context.Context, id string) (*Orphan, error) {
	return s.store.getOrphan(ctx, id)
}

// AcknowledgeOrphan removes the orphan entry for connector id, once the
// operator has cleaned up what the connector left behind. It returns
// CodeConnectorOrphanNotFound if there is no entry.
func (s *Service) AcknowledgeOrphan(ctx context.Context, id string) error {
	o, err := s.store.getOrphan(ctx, id)
	if err != nil {
		return err
	}
	if o == nil {
		ce := conduiterr.New(CodeConnectorOrphanNotFound, fmt.Sprintf("connector %q has no orphan entry to acknowledge", id))
		ce.Suggestion = "run 'conduit doctor --check connectors.orphaned_resources' to list the recorded orphans"
		return ce
	}
	return s.store.deleteOrphan(ctx, id)
}

// ListOrphans returns every orphan entry, sorted by connector ID.
func (s *Service) ListOrphans(ctx context.Context) ([]*Orphan, error) {
	return ListOrphans(ctx, s.store.db)
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

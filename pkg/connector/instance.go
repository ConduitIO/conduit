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

//go:generate stringer -type=Type -trimprefix=Type

package connector

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/env"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/inspector"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
)

const (
	TypeSource Type = iota + 1
	TypeDestination
)

const (
	ProvisionTypeAPI ProvisionType = iota
	ProvisionTypeConfig
	ProvisionTypeDLQ // used for provisioning DLQ connectors which are not persisted
)

type (
	// Type defines the connector type.
	Type int
	// ProvisionType defines provisioning type
	ProvisionType int
)

// Instance represents a connector instance.
type Instance struct {
	ID         string
	Type       Type
	Config     Config
	PipelineID string
	Plugin     string

	ProcessorIDs  []string
	State         any
	ProvisionedBy ProvisionType
	CreatedAt     time.Time
	UpdatedAt     time.Time
	// LastActiveConfig is the last config that was used to successfully start
	// the connector. If Config and LastActiveConfig are not the same, the
	// connector will trigger a connector lifecycle event.
	LastActiveConfig Config

	logger    log.CtxLogger
	persister *Persister
	inspector *inspector.Inspector
	// settingsResolver vets and expands the stored settings right before the
	// plugin is configured. Set by the Service; nil resolves no ${VAR}.
	settingsResolver SettingsResolver
	// redactions are the environment values substituted into the settings the
	// plugin was last configured with; see redactError.
	redactMu   sync.Mutex
	redactions []env.Expansion

	// connector stores the reference to a running connector. If the field is
	// nil the connector is not running.
	connector Connector

	// deleteHookRan is set when OnDelete delivered the "deleted" lifecycle
	// event to the plugin and the plugin did not answer that it does not
	// implement it. From then on the plugin may have released what State
	// points into (for example a Postgres replication slot). If the delete is
	// rolled back (Service.Restore), the instance keeps State, and Connector
	// refuses to start it with CodeConnectorDeleteHookRan (#3137).
	//
	// It lives in memory only: persisting it would add a field to the stored
	// connector record and need a write outside the rolled-back transaction.
	// A process restart therefore loads the instance without it.
	deleteHookRan bool

	sync.RWMutex
}

// Config collects common data stored for a connector.
type Config struct {
	Name     string
	Settings map[string]string
}

type Connector interface {
	OnDelete(ctx context.Context) error
}

// PluginDispenserFetcher can fetch a plugin dispenser.
type PluginDispenserFetcher interface {
	NewDispenser(logger log.CtxLogger, name string, connectorID string) (connectorPlugin.Dispenser, error)
}

func (i *Instance) Init(logger log.CtxLogger, persister *Persister) {
	connLogger := logger
	connLogger.Logger = connLogger.Logger.With().
		Str(log.ConnectorIDField, i.ID).
		Logger()
	connLogger = connLogger.WithComponent("connector." + i.Type.String())

	i.logger = connLogger
	i.persister = persister
	i.inspector = inspector.New(i.logger, inspector.DefaultBufferSize)
}

// Inspect returns an inspector.Session which exposes the records
// coming into or out of this connector (depending on the connector type).
func (i *Instance) Inspect(ctx context.Context) *inspector.Session {
	return i.inspector.NewSession(ctx, i.ID)
}

// Connector fetches a new plugin dispenser and returns a connector that can be
// used to interact with that plugin. If Instance.Type is TypeSource this method
// returns *Source, if it's TypeDestination it returns *Destination, otherwise
// it returns an error. The plugin is not started in this method, that happens
// when Open is called in the returned connector.
// If a connector is already running for this Instance this method returns an
// error.
func (i *Instance) Connector(ctx context.Context, dispenserFetcher PluginDispenserFetcher) (Connector, error) {
	// guard: connector-connector-checkstartable
	if err := i.CheckStartable(); err != nil {
		return nil, err
	}
	return i.newConnector(ctx, dispenserFetcher)
}

// CheckStartable returns a CodeConnectorDeleteHookRan error if this instance
// must not be started, and nil otherwise. Connector calls it; the lifecycle
// services also call it for every connector of a pipeline before they build
// any node, so a refused start leaves nothing half built (#3137).
func (i *Instance) CheckStartable() error {
	// Invariant 3 (#3137): fail closed. A rolled-back delete already ran the
	// plugin's delete hook; resuming from the saved position could skip
	// records, and clearing it could too (a Kafka source with no position
	// starts at the end). Make an operator decide.
	i.RLock()
	hookRan := i.deleteHookRan
	i.RUnlock()
	if !hookRan {
		return nil
	}
	ce := conduiterr.New(CodeConnectorDeleteHookRan, fmt.Sprintf(
		"connector %q (plugin %q, pipeline %q) was restored after a failed delete, but its plugin's delete hook "+
			"had already run and may have released resources its saved position depends on "+
			"(for example a Postgres replication slot); refusing to start from that position",
		i.ID, i.Plugin, i.PipelineID,
	))
	ce.Suggestion = "delete the connector and create it again to start it from the beginning; " +
		"to resume from the saved position instead, first check that the resources it depends on still exist " +
		"(for example the replication slot), then restart Conduit, which reloads the connector without this check"
	return ce
}

// newConnector is Connector without the delete-hook check. Close uses it: a
// connector whose delete hook already ran must still be deletable.
func (i *Instance) newConnector(_ context.Context, dispenserFetcher PluginDispenserFetcher) (Connector, error) {
	if i.connector != nil {
		// connector is already running, might be a bug where an old connector is stuck
		// Invariant: errors.Is(err, ErrConnectorRunning) still holds — sentinel
		// wrapped, ConduitError adds the code.
		err := conduiterr.Wrap(CodeConnectorRunning, "connector is running", ErrConnectorRunning)
		err.Suggestion = "wait for the current connector operation to finish, or restart the pipeline"
		return nil, err
	}

	pluginDispenser, err := dispenserFetcher.NewDispenser(i.logger, i.Plugin, i.ID)
	if err != nil {
		return nil, cerrors.Errorf("failed to get plugin dispenser: %w", err)
	}

	switch i.Type {
	case TypeSource:
		errs := make(chan error)
		return &Source{
			Instance:    i,
			dispenser:   pluginDispenser,
			errs:        errs,
			persistErrs: newPersistErrReporter(errs),
		}, nil
	case TypeDestination:
		errs := make(chan error)
		return &Destination{
			Instance:    i,
			dispenser:   pluginDispenser,
			errs:        errs,
			persistErrs: newPersistErrReporter(errs),
		}, nil
	default:
		return nil, ErrInvalidConnectorType
	}
}

func (i *Instance) Close(ctx context.Context, dispenserFetcher PluginDispenserFetcher) error {
	i.inspector.Close()
	conn, err := i.newConnector(ctx, dispenserFetcher)
	if err != nil {
		return err
	}
	err = conn.OnDelete(ctx)
	if err != nil {
		return err
	}

	return nil
}

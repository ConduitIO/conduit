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
	"sync"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/inspector"
	"github.com/conduitio/conduit/pkg/plugin"
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

	logger    log.CtxLogger
	persister *Persister
	inspector *inspector.Inspector

	// connector stores the reference to a running connector. If the field is
	// nil the connector is not running.
	connector Connector

	sync.RWMutex
}

// Config collects common data stored for a connector.
type Config struct {
	Name     string
	Settings map[string]string
}

type Connector interface{}

// PluginDispenserFetcher can fetch a plugin dispenser.
type PluginDispenserFetcher interface {
	NewDispenser(logger log.CtxLogger, name string) (plugin.Dispenser, error)
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
	return i.inspector.NewSession(ctx)
}

// Connector fetches a new plugin dispenser and returns a connector that can be
// used to interact with that plugin. If Instance.Type is TypeSource this method
// returns *Source, if it's TypeDestination it returns *Destination, otherwise
// it returns an error. The plugin is not started in this method, that happens
// when Open is called in the returned connector.
// If a connector is already running for this Instance this method returns an
// error.
func (i *Instance) Connector(ctx context.Context, dispenserFetcher PluginDispenserFetcher) (Connector, error) {
	if i.connector != nil {
		// connector is already running, might be a bug where an old connector is stuck
		return nil, ErrConnectorRunning
	}

	pluginDispenser, err := dispenserFetcher.NewDispenser(i.logger, i.Plugin)
	if err != nil {
		return nil, cerrors.Errorf("failed to get plugin dispenser: %w", err)
	}

	switch i.Type {
	case TypeSource:
		return &Source{
			Instance:  i,
			dispenser: pluginDispenser,
			errs:      make(chan error),
		}, nil
	case TypeDestination:
		return &Destination{
			Instance:  i,
			dispenser: pluginDispenser,
			errs:      make(chan error),
		}, nil
	default:
		return nil, ErrInvalidConnectorType
	}
}

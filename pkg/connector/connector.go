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

const inspectorBufferSize = 1000

type (
	// Type defines the connector type.
	Type int
	// ProvisionType defines provisioning type
	ProvisionType int
)

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

	pluginDispenser plugin.Dispenser
	logger          log.CtxLogger
	persister       *Persister
	inspector       *inspector.Inspector

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

func (i *Instance) init(logger log.CtxLogger, persister *Persister) {
	connLogger := logger
	connLogger.Logger = connLogger.Logger.With().
		Str(log.ConnectorIDField, i.ID).
		Logger()
	connLogger = connLogger.WithComponent("connector." + i.Type.String())

	i.persister = persister
	i.logger = connLogger
	i.inspector = inspector.New(i.logger, inspectorBufferSize)
}

// Inspect returns an inspector.Session which exposes the records
// coming into or out of this connector (depending on the connector type).
func (i *Instance) Inspect(ctx context.Context) *inspector.Session {
	return i.inspector.NewSession(ctx)
}

func (i *Instance) Connector(ctx context.Context) (Connector, error) {
	if i.pluginDispenser == nil {
		return nil, fmt.Errorf("connector has no plugin dispenser, make sure plugin %s exists", i.Plugin)
	}
	if i.connector != nil {
		return nil, ErrConnectorRunning
	}

	i.logger.Debug(ctx).Msg("starting connector plugin")

	switch i.Type {
	case TypeSource:
		return &Source{
			Instance: i,
			errs:     make(chan error),
		}, nil
	case TypeDestination:
		return &Destination{
			Instance: i,
			errs:     make(chan error),
		}, nil
	default:
		return nil, ErrInvalidConnectorType
	}
}

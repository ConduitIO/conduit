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
	"time"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/inspector"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/record"
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
	// TODO make sure Connector can only be called if no connector is dispensed
}

// Config collects common data stored for a connector.
type Config struct {
	Name     string
	Settings map[string]string
}

type SourceState struct {
	Position record.Position
}

type DestinationState struct {
	Positions map[string]record.Position
}

type Connector interface {
	// Inspect returns an inspector.Session which exposes the records
	// coming into or out of this connector (depending on the connector type).
	Inspect(context.Context) *inspector.Session
}

func (i *Instance) Connector(ctx context.Context) (Connector, error) {
	if i.pluginDispenser == nil {
		return nil, fmt.Errorf("connector has no plugin dispenser, make sure plugin %s exists", i.Plugin)
	}

	i.logger.Debug(ctx).Msg("starting connector plugin")

	switch i.Type {
	case TypeSource:
		src, err := i.pluginDispenser.DispenseSource()
		if err != nil {
			return nil, err
		}
		return &Source{
			instance:  i,
			plugin:    src,
			errs:      make(chan error),
			inspector: inspector.New(i.logger, inspectorBufferSize),
		}, nil
	case TypeDestination:
		dest, err := i.pluginDispenser.DispenseDestination()
		if err != nil {
			return nil, err
		}
		return &Destination{
			instance:  i,
			plugin:    dest,
			errs:      make(chan error),
			inspector: inspector.New(i.logger, inspectorBufferSize),
		}, nil
	default:
		return nil, ErrInvalidConnectorType
	}
}

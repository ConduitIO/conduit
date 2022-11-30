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
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/inspector"
	"github.com/conduitio/conduit/pkg/plugin"
)

const inspectorBufferSize = 1000

// Builder represents an object that can build a connector.
// The main use of this interface is to be able to switch out the connector
// implementations for mocks in tests.
type Builder interface {
	Build(t Type, p ProvisionType) (Connector, error)

	// Init initializes a connector and validates it to make sure it's ready for use.
	Init(c Connector, id string, config Config) error
}

// DefaultBuilder is a Builder that builds regular destinations and sources connected
// to actual plugins.
type DefaultBuilder struct {
	logger    log.CtxLogger
	persister *Persister
	service   *plugin.Service
}

func NewDefaultBuilder(logger log.CtxLogger, persister *Persister, service *plugin.Service) *DefaultBuilder {
	return &DefaultBuilder{
		logger:    logger,
		persister: persister,
		service:   service,
	}
}

func (b *DefaultBuilder) Build(t Type, p ProvisionType) (Connector, error) {
	var c Connector
	now := time.Now()
	switch t {
	case TypeSource:
		c = &source{
			XCreatedAt:     now,
			XUpdatedAt:     now,
			XProvisionedBy: p,
		}
	case TypeDestination:
		c = &destination{
			XCreatedAt:     now,
			XUpdatedAt:     now,
			XProvisionedBy: p,
		}
	default:
		return nil, ErrInvalidConnectorType
	}
	return c, nil
}

func (b *DefaultBuilder) Init(c Connector, id string, config Config) error {
	connLogger := b.logger
	connLogger.Logger = connLogger.Logger.With().
		Str(log.ConnectorIDField, id).
		Logger()

	p, err := b.service.NewDispenser(connLogger, config.Plugin)
	if err != nil {
		return cerrors.Errorf("could not create plugin %q: %w", config.Plugin, err)
	}

	switch v := c.(type) {
	case *source:
		v.XID = id
		v.XConfig = config
		connLogger = connLogger.WithComponent("connector.Source")
		v.logger = connLogger
		v.persister = b.persister
		v.pluginDispenser = p
		v.errs = make(chan error)
	case *destination:
		v.XID = id
		v.XConfig = config
		connLogger = connLogger.WithComponent("connector.Destination")
		v.logger = connLogger
		v.persister = b.persister
		v.pluginDispenser = p
		v.errs = make(chan error)
		v.inspector = inspector.New(v.logger, inspectorBufferSize)
	default:
		return ErrInvalidConnectorType
	}

	return c.Validate(context.Background(), c.Config().Settings)
}

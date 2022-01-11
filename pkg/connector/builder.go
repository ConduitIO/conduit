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

	"github.com/conduitio/conduit/pkg/foundation/log"
)

// Builder represents an object that can build a connector.
// The main use of this interface is to be able to switch out the connector
// implementations for mocks in tests.
type Builder interface {
	Build(t Type) (Connector, error)

	// Init initializes a connector and validates it to make sure it's ready for use.
	Init(c Connector, id string, config Config) error
}

// DefaultBuilder is a Builder that builds regular destinations and sources connected
// to actual plugins.
type DefaultBuilder struct {
	logger    log.CtxLogger
	persister *Persister
}

func NewDefaultBuilder(logger log.CtxLogger, persister *Persister) DefaultBuilder {
	return DefaultBuilder{
		logger:    logger,
		persister: persister,
	}
}

func (b DefaultBuilder) Build(t Type) (Connector, error) {
	var c Connector
	switch t {
	case TypeSource:
		c = &source{}
	case TypeDestination:
		c = &destination{}
	default:
		return nil, ErrInvalidConnectorType
	}
	return c, nil
}

func (b DefaultBuilder) Init(c Connector, id string, config Config) error {
	connLogger := b.logger
	connLogger.Logger = connLogger.Logger.With().
		Str(log.ConnectorIDField, c.ID()).
		Logger()

	switch v := c.(type) {
	case *source:
		v.XID = id
		v.XConfig = config
		connLogger = connLogger.WithComponent("source")
		v.logger = connLogger
		v.persister = b.persister
		v.errs = make(chan error)
	case *destination:
		v.XID = id
		v.XConfig = config
		connLogger = connLogger.WithComponent("destination")
		v.logger = connLogger
		v.persister = b.persister
		v.errs = make(chan error)
	default:
		return ErrInvalidConnectorType
	}

	return c.Validate(context.Background(), c.Config().Settings)
}

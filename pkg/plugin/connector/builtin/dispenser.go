// Copyright © 2024 Meroxa, Inc.
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

package builtin

import (
	"context"
	"github.com/conduitio/conduit-commons/hostpolicy"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit-connector-protocol/pconnutils"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/connector"
)

type Dispenser struct {
	name              plugin.FullName
	logger            log.CtxLogger
	specifierPlugin   func() pconnector.SpecifierPlugin
	sourcePlugin      func() pconnector.SourcePlugin
	destinationPlugin func() pconnector.DestinationPlugin
	// schemaService is the owning runtime's connector schema service, handed
	// to every source and destination adapter this dispenser creates.
	schemaService pconnutils.SchemaService
	// policy is put on the context of every source/destination call. The zero
	// value (NewDispenser's default) denies ambient credentials.
	policy hostpolicy.Policy
	// decorate, when not nil, adds engine-owned values to the context of every
	// source/destination call (see WithContextDecorator).
	decorate func(context.Context) context.Context
}

func NewDispenser(
	name plugin.FullName,
	logger log.CtxLogger,
	specifierPlugin func() pconnector.SpecifierPlugin,
	sourcePlugin func() pconnector.SourcePlugin,
	destinationPlugin func() pconnector.DestinationPlugin,
) *Dispenser {
	return &Dispenser{
		name:              name,
		logger:            logger,
		specifierPlugin:   specifierPlugin,
		sourcePlugin:      sourcePlugin,
		destinationPlugin: destinationPlugin,
	}
}

func (d *Dispenser) DispenseSpecifier() (connector.SpecifierPlugin, error) {
	return newSpecifierPluginAdapter(d.specifierPlugin(), d.pluginLogger("specifier")), nil
}

func (d *Dispenser) DispenseSource() (connector.SourcePlugin, error) {
	a := newSourcePluginAdapter(d.sourcePlugin(), d.pluginLogger("source"), d.policy)
	a.schemaService = d.schemaService
	a.decorate = d.decorate
	return a, nil
}

func (d *Dispenser) DispenseDestination() (connector.DestinationPlugin, error) {
	a := newDestinationPluginAdapter(d.destinationPlugin(), d.pluginLogger("destination"), d.policy)
	a.schemaService = d.schemaService
	a.decorate = d.decorate
	return a, nil
}

func (d *Dispenser) pluginLogger(pluginType string) log.CtxLogger {
	logger := d.logger
	logger.Logger = logger.With().
		Str(log.PluginTypeField, pluginType).
		Str(log.PluginNameField, string(d.name)).Logger()
	return logger
}

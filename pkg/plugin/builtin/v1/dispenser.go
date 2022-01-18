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

package builtinv1

import (
	"github.com/conduitio/conduit-plugin/cpluginv1"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
)

type Dispenser struct {
	name              string
	logger            log.CtxLogger
	specifierPlugin   cpluginv1.SpecifierPlugin
	sourcePlugin      cpluginv1.SourcePlugin
	destinationPlugin cpluginv1.DestinationPlugin
}

func NewDispenser(
	name string,
	logger log.CtxLogger,
	specifierPlugin cpluginv1.SpecifierPlugin,
	sourcePlugin cpluginv1.SourcePlugin,
	destinationPlugin cpluginv1.DestinationPlugin,
) *Dispenser {
	return &Dispenser{
		name:              name,
		logger:            logger,
		specifierPlugin:   specifierPlugin,
		sourcePlugin:      sourcePlugin,
		destinationPlugin: destinationPlugin,
	}
}

func (d *Dispenser) DispenseSpecifier() (plugin.SpecifierPlugin, error) {
	return newSpecifierPluginAdapter(d.specifierPlugin), nil
}

func (d *Dispenser) DispenseSource() (plugin.SourcePlugin, error) {
	return newSourcePluginAdapter(d.sourcePlugin, d.pluginLogger("source")), nil
}

func (d *Dispenser) DispenseDestination() (plugin.DestinationPlugin, error) {
	return newDestinationPluginAdapter(d.destinationPlugin, d.pluginLogger("destination")), nil
}

func (d *Dispenser) pluginLogger(pluginType string) log.CtxLogger {
	logger := d.logger
	logger.Logger = logger.With().
		Str(log.PluginTypeField, pluginType).
		Str(log.PluginNameField, d.name).Logger()
	return logger
}
